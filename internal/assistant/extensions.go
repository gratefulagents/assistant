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
	MCPServers     map[string]sdkmcp.ServerConfig `json:"mcpServers,omitempty"`
	MCPConfigPaths stringListFlag                 `json:"mcpConfigPaths,omitempty"`
	Skills         skillsConfig                   `json:"skills,omitempty"`
	Plugins        []pluginConfig                 `json:"plugins,omitempty"`
	Extensions     []pluginConfig                 `json:"extensions,omitempty"`
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
	if cfg.EnableProjectState {
		tools, err := durableMemoryTools(ctx, cfg)
		if err != nil {
			return extensionBundle{}, err
		}
		bundle.ExtraTools = append(bundle.ExtraTools, tools...)
	}
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

func durableMemoryTools(ctx context.Context, cfg appConfig) ([]agentsdk.Tool, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	store, err := sdkprojectstate.NewFilesystemStore(sdkprojectstate.FilesystemOptions{
		StateDir:  cfg.StateDir,
		ProjectID: "personal-assistant",
		WorkDir:   cfg.WorkDir,
		Actor:     "assistant",
	})
	if err != nil {
		return nil, fmt.Errorf("initialize durable memory: %w", err)
	}
	return sdkprojectstatetools.Tools(store, "assistant"), nil
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
