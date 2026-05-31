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
	MCPConfig  *sdkmcp.Config
	ExtraTools []agentsdk.Tool
}

func buildBundle(ctx context.Context, cfg appConfig, stderr io.Writer) (*sdkruntime.Bundle, error) {
	extensions, err := loadExtensions(ctx, cfg)
	if err != nil {
		return nil, err
	}
	builder := sdkruntime.NewBuilder(runtimeConfig(cfg, extensions),
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

func runtimeConfig(cfg appConfig, extensions extensionBundle) sdkruntime.Config {
	rt := sdkruntime.Config{
		Provider:                "openai",
		Model:                   cfg.Model,
		BaseURL:                 cfg.BaseURL,
		APIMode:                 cfg.APIMode,
		WorkDir:                 cfg.WorkDir,
		AgentName:               "assistant",
		Instructions:            defaultInstructions(),
		SessionMode:             agentsdk.SessionModeChat,
		ActiveMode:              "assistant",
		ActivePhase:             "chat",
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
		FeatureSummary:     featureSummary(cfg, extensions),
	}
	switch cfg.Provider {
	case providerOpenAIOAuth:
		rt.AuthMode = string(sdkopenai.AuthModeOAuth)
		rt.OpenAIOAuthPath = cfg.OpenAIOAuthPath
		rt.OpenAIOAuthAccountID = cfg.OpenAIOAuthAccountID
		rt.OpenAIOAuthAccountIDPath = cfg.OpenAIAccountIDPath
	case providerOpenAIAPI:
		rt.AuthMode = string(sdkopenai.AuthModeAPIKey)
		rt.APIKey = cfg.APIKey
	}
	return rt
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
		"Use task tools for follow-up work, reminders, and multi-step personal projects.",
		"Use schedule tools only when the user asks for a timed reminder, cron, recurring task, or scheduled follow-up. Scheduled prompts run only while the assistant schedule or poll command is running.",
		"Use skill_search and skill_install when the user needs a new integration. Installed skills become MCP servers through .mcp.json and are available on the next turn.",
		"Ask before externally visible, destructive, expensive, or security-sensitive actions.",
	}, " ")
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
