// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
	sdkprojectstatetools "github.com/gratefulagents/sdk/pkg/agentsdk/tools/projectstate"
	sdkskills "github.com/gratefulagents/sdk/pkg/agentsdk/tools/skills"
)

type assistantConfigFile struct {
	Instructions     string                         `json:"instructions,omitempty"`
	InstructionsPath string                         `json:"instructionsPath,omitempty"`
	MCPServers       map[string]sdkmcp.ServerConfig `json:"mcpServers,omitempty"`
	MCPConfigPaths   stringListFlag                 `json:"mcpConfigPaths,omitempty"`
	Skills           skillsConfig                   `json:"skills,omitempty"`
	Approvals        approvalsConfig                `json:"approvals,omitempty"`
	Plugins          []pluginConfig                 `json:"plugins,omitempty"`
	Extensions       []pluginConfig                 `json:"extensions,omitempty"`
}

type approvalsConfig struct {
	Reviewer        string `json:"reviewer,omitempty"`
	ReviewerModel   string `json:"reviewerModel,omitempty"`
	ReviewerTimeout int    `json:"reviewerTimeout,omitempty"`
}

type skillsConfig struct {
	Enabled     *bool  `json:"enabled,omitempty"`
	CatalogPath string `json:"catalogPath,omitempty"`
}

type pluginConfig struct {
	Name           string                         `json:"name,omitempty"`
	Type           string                         `json:"type,omitempty"`
	Enabled        *bool                          `json:"enabled,omitempty"`
	ConfigPath     string                         `json:"configPath,omitempty"`
	MCPConfigPaths stringListFlag                 `json:"mcpConfigPaths,omitempty"`
	MCPServers     map[string]sdkmcp.ServerConfig `json:"mcpServers,omitempty"`
}

func (p pluginConfig) enabled() bool {
	return p.Enabled == nil || *p.Enabled
}

func loadAssistantConfigFile(path string) (assistantConfigFile, bool, error) {
	if strings.TrimSpace(path) == "" {
		return assistantConfigFile{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return assistantConfigFile{}, false, nil
		}
		return assistantConfigFile{}, false, fmt.Errorf("read assistant config %s: %w", path, err)
	}
	var cfg assistantConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return assistantConfigFile{}, true, fmt.Errorf("parse assistant config %s: %w", path, err)
	}
	cfg.Extensions = append(cfg.Extensions, cfg.Plugins...)
	return cfg, true, nil
}

func loadExtensions(ctx context.Context, cfg appConfig) (extensionBundle, error) {
	bundle := extensionBundle{}
	mcpCfg, err := loadMergedMCPConfig(cfg)
	if err != nil {
		return extensionBundle{}, err
	}
	bundle.MCPConfig = mcpCfg
	// appTools are the assistant's own capability tools (durable memory, tasks,
	// scheduling, transcript search, calendar). Their side effects target the
	// assistant's datastores/services — never the workspace filesystem — so under
	// the read-only permission tier (which restricts filesystem mutation) they are
	// kept available via markFilesystemExempt. Skills are intentionally excluded:
	// a skill (e.g. install) can write the workspace filesystem, so it stays
	// governed by the permission tier.
	var appTools []agentsdk.Tool
	if cfg.EnableProjectState {
		store, err := newMemoryStore(cfg)
		if err != nil {
			return extensionBundle{}, err
		}
		appTools = append(appTools, sdkprojectstatetools.Tools(store, "assistant")...)
		bundle.MemoryPrime = primeMemory(ctx, store)
	}
	if cfg.EnableScheduling {
		appTools = append(appTools, scheduleTools(cfg)...)
	}
	if cfg.EnableTranscripts {
		appTools = append(appTools, sessionSearchTools(cfg)...)
	}
	if cfg.EnableTranscripts && cfg.EnableProjectState {
		appTools = append(appTools, memoryDistillTools(cfg)...)
		appTools = append(appTools, memoryReviewTools(cfg)...)
	}
	if cfg.EnableTools && googleAuthConfigured(cfg) {
		tools, err := googleCalendarTools(cfg)
		if err != nil {
			return extensionBundle{}, err
		}
		appTools = append(appTools, tools...)
	}
	if toolAccess(cfg.Permission) == agentsdk.ToolAccessLevelReadOnly {
		appTools = markFilesystemExempt(appTools)
	}
	bundle.ExtraTools = append(bundle.ExtraTools, appTools...)
	if cfg.EnableSkills {
		tools, err := skillTools(cfg)
		if err != nil {
			return extensionBundle{}, err
		}
		bundle.ExtraTools = append(bundle.ExtraTools, tools...)
	}
	select {
	case <-ctx.Done():
		return extensionBundle{}, ctx.Err()
	default:
	}
	return bundle, nil
}

// fsExemptTool wraps a host capability tool whose side effects target the
// assistant's own datastores/services (durable memory, tasks, scheduling,
// calendar) rather than the workspace filesystem. The SDK's read-only access
// filter keeps a tool only when IsReadOnly() reports true, so reporting true here
// keeps these non-filesystem tools available under the read-only permission tier.
// Every other method delegates to the wrapped tool, preserving its real behavior.
type fsExemptTool struct{ agentsdk.Tool }

func (fsExemptTool) IsReadOnly() bool { return true }

// markFilesystemExempt wraps each tool as filesystem-exempt so it survives the
// read-only access filter. Used for capability tools that never write the
// workspace filesystem; callers must not pass filesystem-mutating tools.
func markFilesystemExempt(tools []agentsdk.Tool) []agentsdk.Tool {
	if len(tools) == 0 {
		return tools
	}
	out := make([]agentsdk.Tool, len(tools))
	for i, t := range tools {
		out[i] = fsExemptTool{t}
	}
	return out
}

func durableMemoryTools(ctx context.Context, cfg appConfig) ([]agentsdk.Tool, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	store, err := newMemoryStore(cfg)
	if err != nil {
		return nil, err
	}
	return sdkprojectstatetools.Tools(store, "assistant"), nil
}

// newMemoryStore builds the durable memory store, attaching an embedder for
// hybrid recall when one is configured.
func newMemoryStore(cfg appConfig) (sdkprojectstate.Store, error) {
	embedder, err := buildEmbedder(cfg)
	if err != nil {
		return nil, fmt.Errorf("initialize memory embedder: %w", err)
	}
	db, err := stateDBFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	store, err := sdkprojectstate.NewSQLiteStore(sdkprojectstate.SQLiteOptions{
		DB:        db,
		ProjectID: "personal-assistant",
		WorkDir:   cfg.WorkDir,
		Actor:     "assistant",
		Embedder:  embedder,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize durable memory: %w", err)
	}
	return store, nil
}

// primeMemory builds a compact durable-context block to inject into the system
// prompt. The bundle is rebuilt per run, so this re-primes on every turn and
// stays fresh even in a long-running process: memories stored on one turn are
// reflected in the next. The agent begins each run already aware of pinned
// memories and active tasks instead of depending on it to call prime_context.
// It returns an empty string when there is nothing durable to surface or when
// priming fails, so memory bootstrap never blocks a run.
func primeMemory(ctx context.Context, store sdkprojectstate.Store) string {
	text, err := store.PrimeContext(ctx, sdkprojectstate.PrimeOptions{Actor: "assistant"})
	if err != nil {
		return ""
	}
	text = strings.TrimSpace(text)
	// PrimeContext always emits a "## Durable Project State" header with
	// Project/Workspace lines. Actual content lives under "### " sections
	// (tasks, pinned/recent memories). With no sections there is nothing
	// worth injecting, so skip it to avoid wasting tokens.
	if text == "" || strings.Contains(text, "No durable tasks or memories yet") || !strings.Contains(text, "### ") {
		return ""
	}
	return text
}

// buildEmbedder returns an OpenAI-compatible embedder for hybrid memory recall,
// or nil when no embedding model is configured (recall stays lexical-only).
func buildEmbedder(cfg appConfig) (sdkprojectstate.Embedder, error) {
	model := strings.TrimSpace(cfg.EmbeddingModel)
	if model == "" {
		return nil, nil
	}
	return sdkprojectstate.NewOpenAIEmbedder(sdkprojectstate.OpenAIEmbedderOptions{
		BaseURL:    cfg.EmbeddingBaseURL,
		APIKey:     cfg.EmbeddingAPIKey,
		ModelID:    model,
		Dimensions: cfg.EmbeddingDimensions,
	})
}

func loadMergedMCPConfig(cfg appConfig) (*sdkmcp.Config, error) {
	merged := &sdkmcp.Config{MCPServers: map[string]sdkmcp.ServerConfig{}}
	loaded := false

	defaultPath := sdkmcp.ConfigPathForWorkDir(cfg.WorkDir)
	paths := uniqueStrings(append([]string{defaultPath}, cfg.MCPConfigPaths...))
	for _, path := range paths {
		path = expandUserPath(path)
		if strings.TrimSpace(path) == "" {
			continue
		}
		part, exists, err := sdkmcp.LoadConfig(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		loaded = true
		mergeMCPServers(merged.MCPServers, part.MCPServers)
	}

	if len(cfg.FileConfig.MCPServers) > 0 {
		loaded = true
		mergeMCPServers(merged.MCPServers, cfg.FileConfig.MCPServers)
	}
	for _, ext := range cfg.FileConfig.Extensions {
		if !ext.enabled() || len(ext.MCPServers) == 0 {
			continue
		}
		loaded = true
		mergeMCPServers(merged.MCPServers, ext.MCPServers)
	}
	if !loaded {
		return nil, nil
	}
	return merged, nil
}

func mergeMCPServers(dst, src map[string]sdkmcp.ServerConfig) {
	for name, server := range src {
		if strings.TrimSpace(name) == "" {
			continue
		}
		dst[name] = server
	}
}

func skillTools(cfg appConfig) ([]agentsdk.Tool, error) {
	registry, err := skillRegistry(cfg.SkillCatalogPath)
	if err != nil {
		return nil, err
	}
	return sdkskills.Tools(registry, sdkskills.NewInstaller(registry), cfg.WorkDir), nil
}

func skillRegistry(path string) (*sdkskills.Registry, error) {
	if strings.TrimSpace(path) == "" {
		return sdkskills.NewRegistry()
	}
	raw, err := os.ReadFile(expandUserPath(path))
	if err != nil {
		return nil, fmt.Errorf("read skill catalog %s: %w", path, err)
	}
	var wrapped struct {
		Skills []sdkskills.SkillEntry `json:"skills"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("parse skill catalog %s: %w", path, err)
	}
	return sdkskills.NewRegistryFromEntries(wrapped.Skills), nil
}
