// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
)

const (
	providerOpenAIOAuth = "openai-oauth"
	providerOpenAIAPI   = "openai-api"
	providerOpenRouter  = "openrouter"
)

const (
	defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	defaultOpenRouterAPIMode = "chat-completions"
	defaultOpenRouterModel   = "openai/gpt-4o-mini"
)

func normalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case providerOpenAIOAuth, "oauth":
		return providerOpenAIOAuth
	case providerOpenAIAPI, "api":
		return providerOpenAIAPI
	case providerOpenRouter, "open-router":
		return providerOpenRouter
	default:
		return ""
	}
}

// sdkProviderName maps an assistant provider to the SDK provider identifier
// consumed by the runtime/provider factory.
func sdkProviderName(provider string) string {
	switch provider {
	case providerOpenRouter:
		return "openrouter"
	default:
		return "openai"
	}
}

func defaultModel(provider string) string {
	switch provider {
	case providerOpenAIOAuth:
		return sdkopenai.DefaultChatMiniModel
	case providerOpenAIAPI:
		return sdkopenai.DefaultChatModel
	case providerOpenRouter:
		return defaultOpenRouterModel
	default:
		return sdkopenai.DefaultChatModel
	}
}

func normalizePermission(permission string) string {
	mode := sdkpolicy.NormalizePermissionMode(permission)
	if mode == sdkpolicy.PermissionModeReadOnly {
		return string(sdkpolicy.PermissionModeReadOnly)
	}
	return string(sdkpolicy.PermissionModeWorkspaceWrite)
}

func toolAccess(permission string) agentsdk.ToolAccessLevel {
	if sdkpolicy.NormalizePermissionMode(permission) == sdkpolicy.PermissionModeReadOnly {
		return agentsdk.ToolAccessLevelReadOnly
	}
	return agentsdk.ToolAccessLevelFull
}
