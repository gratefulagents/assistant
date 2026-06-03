// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	sdkpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
	sdkruntime "github.com/gratefulagents/sdk/pkg/agentsdk/runtime"
)

type extensionBundle struct {
	MCPConfig   *sdkmcp.Config
	ExtraTools  []agentsdk.Tool
	MemoryPrime string
}

func buildBundle(ctx context.Context, cfg appConfig, stderr io.Writer, audit *auditRecorder) (*sdkruntime.Bundle, error) {
	extensions, err := loadExtensions(ctx, cfg)
	if err != nil {
		return nil, err
	}
	rt, err := runtimeConfig(cfg, extensions, audit)
	if err != nil {
		return nil, err
	}
	builder := sdkruntime.NewBuilder(rt,
		sdkruntime.WithStatusFunc(func(text string) {
			if cfg.Debug {
				fmt.Fprintln(stderr, "[status]", text)
			}
		}),
		sdkruntime.WithLogFunc(func(text string) {
			fmt.Fprintln(stderr, "[log]", text)
		}),
	)
	return builder.Build(ctx)
}

func runtimeConfig(cfg appConfig, extensions extensionBundle, audit *auditRecorder) (sdkruntime.Config, error) {
	sessionMode := cfg.SessionMode
	if sessionMode == "" {
		sessionMode = agentsdk.SessionModeChat
	}
	activeMode := strings.TrimSpace(cfg.ActiveMode)
	if activeMode == "" {
		activeMode = "assistant"
	}
	activePhase := strings.TrimSpace(cfg.ActivePhase)
	if activePhase == "" {
		activePhase = "chat"
	}
	rt := sdkruntime.Config{
		Provider:                sdkProviderName(cfg.Provider),
		Model:                   cfg.Model,
		BaseURL:                 cfg.BaseURL,
		APIMode:                 cfg.APIMode,
		WorkDir:                 cfg.WorkDir,
		AgentName:               "assistant",
		Instructions:            instructionsWithMemory(extensions.MemoryPrime),
		SessionMode:             sessionMode,
		ActiveMode:              activeMode,
		ActivePhase:             activePhase,
		Reasoning:               cfg.Reasoning,
		Verbosity:               cfg.Verbosity,
		MaxTurns:                cfg.MaxTurns,
		MaxTokens:               cfg.MaxTokens,
		ToolTimeout:             cfg.ToolTimeout,
		ToolAccess:              toolAccess(cfg.Permission),
		PermissionMode:          sdkpolicy.NormalizePermissionMode(cfg.Permission),
		EnableTools:             cfg.EnableTools,
		EnableMCP:               cfg.EnableMCP,
		EnableHandoffs:          false,
		EnableSubAgents:         false,
		EnableGuardrails:        cfg.EnableGuardrails,
		EnableCompaction:        cfg.EnableCompaction,
		EnableApproval:          cfg.EnableApproval,
		EnableRetry:             true,
		EnableAsyncShell:        false,
		AllowPrivateNetworkURLs: cfg.AllowPrivateNetwork,
		Debug:                   cfg.Debug,
		// Durable memory is intentionally exposed through ExtraTools so the
		// model owns recall/store decisions instead of host-side auto-recall.
		EnableProjectState: false,
		MCPConfig:          extensions.MCPConfig,
		ExtraTools:         extensions.ExtraTools,
		TracingProcessor:   audit,
		FeatureSummary:     featureSummary(cfg, extensions),
		ModeDirectiveText:  strings.TrimSpace(cfg.ModeDirectiveText),
	}
	switch cfg.Provider {
	case providerOpenAIOAuth:
		rt.AuthMode = string(sdkopenai.AuthModeOAuth)
		rt.OpenAIOAuthPath = cfg.OpenAIOAuthPath
		rt.OpenAIOAuthAccountID = cfg.OpenAIOAuthAccountID
		rt.OpenAIOAuthAccountIDPath = cfg.OpenAIAccountIDPath
		if cfg.DisableOpenAIOAuthRefresh {
			session, err := newOpenAIOAuthSession(cfg, "")
			if err != nil {
				return sdkruntime.Config{}, err
			}
			session.DisableRefresh()
			rt.OpenAIAuthSession = session
		}
	case providerOpenAIAPI:
		rt.AuthMode = string(sdkopenai.AuthModeAPIKey)
		rt.APIKey = cfg.APIKey
	case providerOpenRouter:
		rt.AuthMode = string(sdkopenai.AuthModeAPIKey)
		rt.APIKey = cfg.APIKey
	}
	return rt, nil
}

func closeBundle(bundle *sdkruntime.Bundle, stderr io.Writer) {
	if bundle == nil {
		return
	}
	for _, closer := range bundle.Closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil {
			fmt.Fprintln(stderr, "[log] close warning:", err)
		}
	}
}

func defaultInstructions() string {
	return strings.Join([]string{
		"You are Assistant, a lightweight personal AI assistant hosted by github.com/gratefulagents/assistant.",
		"Be concise, practical, and clear. Use tools when they materially improve the answer.",
		modelDrivenMemoryInstructions(),
		"Use session_search when the user asks about prior conversations, past decisions, earlier work, or chat history not already visible in the current turn.",
		"Use memory_distill with action=preview for a fast deterministic transcript memory scan. Use memory_review with action=preview for deeper LLM-backed transcript memory review. Use action=apply only when the user asks to save distilled or reviewed memories.",
		"Use task tools for follow-up work, reminders, and multi-step personal projects.",
		"Use schedule tools only when the user asks for a timed reminder, cron, recurring task, or scheduled follow-up. Scheduled prompts run while a long-running assistant command is active; one-shot prompts exit after the reply.",
		"Use skill_search and skill_install when the user needs a new integration. Installed skills become MCP servers through .mcp.json and are available on the next turn.",
		"Ask before externally visible, destructive, expensive, or security-sensitive actions.",
	}, " ")
}

// instructionsWithMemory appends a primed durable-memory block to the base
// instructions so the agent starts each run already aware of pinned memories
// and active tasks, without depending on it to call prime_context. When there
// is nothing durable to surface, the base instructions are returned unchanged.
func instructionsWithMemory(prime string) string {
	prime = strings.TrimSpace(prime)
	if prime == "" {
		return defaultInstructions()
	}
	return strings.Join([]string{
		defaultInstructions(),
		"The following durable project state was loaded for this run. Treat it as known background and prefer it over re-deriving facts; call memory_recall for anything not covered here.",
		prime,
	}, "\n\n")
}

func modelDrivenMemoryInstructions() string {
	return strings.Join([]string{
		"Durable memory is model-driven. The host only exposes memory tools; you decide when to recall and store.",
		"When the user asks about preferences, prior facts, routines, people, places, projects, or anything that may depend on prior context, call memory_recall or prime_context before answering.",
		"When the user asks you to remember something, or states a stable preference, fact, routine, decision, or long-lived project context, call memory_remember before finalizing the turn.",
		"Store concise memories with appropriate kind/scope/tags. Do not store secrets, access tokens, one-time chatter, or sensitive personal data unless the user explicitly asks.",
	}, " ")
}

func featureSummary(cfg appConfig, extensions extensionBundle) string {
	return strings.Join([]string{
		"provider=" + cfg.Provider,
		"tools=" + onOff(cfg.EnableTools),
		"mcp=" + onOff(cfg.EnableMCP),
		"skills=" + onOff(cfg.EnableSkills),
		"scheduling=" + onOff(cfg.EnableScheduling),
		"project_state=" + onOff(cfg.EnableProjectState),
		"transcripts=" + onOff(cfg.EnableTranscripts),
		"embeddings=" + onOff(strings.TrimSpace(cfg.EmbeddingModel) != ""),
		"mcp_servers=" + itoa(lenMCPServers(extensions.MCPConfig)),
		"extra_tools=" + itoa(len(extensions.ExtraTools)),
	}, ", ")
}

func lenMCPServers(cfg *sdkmcp.Config) int {
	if cfg == nil {
		return 0
	}
	return len(cfg.MCPServers)
}
