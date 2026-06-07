// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
)

func TestNormalizeProvider(t *testing.T) {
	tests := map[string]string{
		"openai-oauth": providerOpenAIOAuth,
		"oauth":        providerOpenAIOAuth,
		"openai-api":   providerOpenAIAPI,
		"api":          providerOpenAIAPI,
		"openrouter":   providerOpenRouter,
		"open-router":  providerOpenRouter,
		"local":        "",
	}
	for in, want := range tests {
		if got := normalizeProvider(in); got != want {
			t.Fatalf("normalizeProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunReturnsUsageErrorForBadProvider(t *testing.T) {
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider", "local"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unsupported provider") {
		t.Fatalf("stderr missing validation error: %q", stderr.String())
	}
}

func TestRunVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var stdout, stderr strings.Builder
		code := Run([]string{arg}, strings.NewReader(""), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("Run(%q) exit code = %d, want 0", arg, code)
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%q) stderr = %q, want empty", arg, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"assistant ", "commit:", "built:", "go:"} {
			if !strings.Contains(out, want) {
				t.Fatalf("Run(%q) stdout missing %q in %q", arg, want, out)
			}
		}
	}
}

func TestValidateAPIProviderRequiresKey(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.Provider = providerOpenAIAPI
	cfg.APIKey = ""
	if err := cfg.validate(); err == nil {
		t.Fatal("validate succeeded without API key")
	}
	cfg.APIKey = "sk-test"
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("BaseURL = %q, want OpenAI API default", cfg.BaseURL)
	}
	if cfg.APIMode != "responses" {
		t.Fatalf("APIMode = %q, want responses", cfg.APIMode)
	}
	if cfg.Model != sdkopenai.DefaultChatModel {
		t.Fatalf("Model = %q, want %q", cfg.Model, sdkopenai.DefaultChatModel)
	}
}

func TestValidateOpenRouterProviderDefaults(t *testing.T) {
	t.Setenv("ASSISTANT_OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.Provider = providerOpenRouter
	cfg.APIKey = ""
	cfg.Model = ""
	if err := cfg.validate(); err == nil {
		t.Fatal("validate succeeded without OpenRouter API key")
	}
	cfg.APIKey = "sk-or-test"
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != defaultOpenRouterBaseURL {
		t.Fatalf("BaseURL = %q, want OpenRouter default", cfg.BaseURL)
	}
	if cfg.APIMode != defaultOpenRouterAPIMode {
		t.Fatalf("APIMode = %q, want %q", cfg.APIMode, defaultOpenRouterAPIMode)
	}
	if cfg.Model != defaultOpenRouterModel {
		t.Fatalf("Model = %q, want %q", cfg.Model, defaultOpenRouterModel)
	}

	rt, err := runtimeConfig(cfg, extensionBundle{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Provider != "openrouter" {
		t.Fatalf("runtime Provider = %q, want openrouter", rt.Provider)
	}
	if rt.AuthMode != string(sdkopenai.AuthModeAPIKey) {
		t.Fatalf("runtime AuthMode = %q, want api-key", rt.AuthMode)
	}
	if rt.APIKey != "sk-or-test" {
		t.Fatalf("runtime APIKey = %q, want sk-or-test", rt.APIKey)
	}
}

func TestValidateOpenRouterProviderReadsEnvKey(t *testing.T) {
	t.Setenv("ASSISTANT_OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-env")
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.Provider = providerOpenRouter
	cfg.APIKey = ""
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-or-env" {
		t.Fatalf("APIKey = %q, want sk-or-env from OPENROUTER_API_KEY", cfg.APIKey)
	}
}

func TestValidateOAuthProviderDefaultsModel(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.Provider = providerOpenAIOAuth
	cfg.Model = ""
	cfg.OpenAIOAuthPath = "~/.codex/auth.json"
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Model != sdkopenai.DefaultChatMiniModel {
		t.Fatalf("Model = %q, want %q", cfg.Model, sdkopenai.DefaultChatMiniModel)
	}
}

func TestOpenAIOAuthRefreshFlagOverridesEnv(t *testing.T) {
	t.Setenv("ASSISTANT_OPENAI_OAUTH_REFRESH", "false")
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DisableOpenAIOAuthRefresh {
		t.Fatal("DisableOpenAIOAuthRefresh = false, want true from env")
	}
	if cfg.OAuthRefreshInterval != time.Hour {
		t.Fatalf("OAuthRefreshInterval = %s, want 1h", cfg.OAuthRefreshInterval)
	}
	cfg, err = parseConfig([]string{"--openai-oauth-refresh=true"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisableOpenAIOAuthRefresh {
		t.Fatal("DisableOpenAIOAuthRefresh = true, want false from flag override")
	}
}

func TestTelegramErrorDetailsFlagOverridesEnv(t *testing.T) {
	t.Setenv("ASSISTANT_TELEGRAM_ERROR_DETAILS", "true")
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TelegramErrorDetails {
		t.Fatal("TelegramErrorDetails = false, want true from env")
	}
	cfg, err = parseConfig([]string{"--telegram-error-details=false"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramErrorDetails {
		t.Fatal("TelegramErrorDetails = true, want false from flag override")
	}
}

func TestRuntimeConfigCanDisableOpenAIOAuthRefresh(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"old-access","refresh_token":"old-refresh","account_id":"acct-1"},"last_refresh":"2000-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.Provider = providerOpenAIOAuth
	cfg.OpenAIOAuthPath = authPath
	cfg.DisableOpenAIOAuthRefresh = true
	rt, err := runtimeConfig(cfg, extensionBundle{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.OpenAIAuthSession == nil {
		t.Fatal("runtime OpenAIAuthSession = nil, want preloaded session")
	}
	if rt.OpenAIAuthSession.SupportsRefresh() {
		t.Fatal("OpenAIAuthSession SupportsRefresh = true, want false")
	}
}

func TestRefreshOAuthAuthFileWritesSerializedToken(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"old-access","refresh_token":"old-refresh","account_id":"acct-1","custom":"keep-token"},"last_refresh":"2000-01-01T00:00:00Z","custom":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"refresh_token":"old-refresh"`) {
			t.Fatalf("refresh request missing refresh token: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh"}`))
	}))
	defer tokenServer.Close()

	cfg := appConfig{OpenAIOAuthPath: authPath}
	if err := refreshOAuthAuthFile(context.Background(), cfg, tokenServer.URL); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"access_token":"new-access"`, `"refresh_token":"new-refresh"`, `"account_id":"acct-1"`, `"custom":"keep"`, `"custom":"keep-token"`, `"last_refresh":`} {
		if !strings.Contains(text, want) {
			t.Fatalf("refreshed auth file missing %s in %s", want, text)
		}
	}
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth file mode = %v, want 0600", got)
	}
}

func TestWriteFileAtomicFallsBackInPlaceWhenRenameBusy(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	err := writeFileAtomicWithRename(authPath, []byte("new"), 0o600, func(_, _ string) error {
		return &os.PathError{Op: "rename", Path: authPath, Err: syscall.EBUSY}
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "new\n"; got != want {
		t.Fatalf("auth file = %q, want %q", got, want)
	}
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("auth file mode = %v, want 0640", got)
	}
}

func TestDefaultExtensionsAreOptIn(t *testing.T) {
	cfg := defaultConfig()
	if cfg.EnableMCP {
		t.Fatal("EnableMCP default = true, want false")
	}
	if cfg.EnableSkills {
		t.Fatal("EnableSkills default = true, want false")
	}
}

func TestBuildEmbedderDisabledByDefault(t *testing.T) {
	embedder, err := buildEmbedder(appConfig{})
	if err != nil {
		t.Fatalf("buildEmbedder error = %v", err)
	}
	if embedder != nil {
		t.Fatal("buildEmbedder with no model = non-nil, want nil (lexical-only)")
	}
}

func TestBuildEmbedderEnabledWithModel(t *testing.T) {
	embedder, err := buildEmbedder(appConfig{
		EmbeddingModel:   "text-embedding-3-small",
		EmbeddingBaseURL: "http://localhost:11434/v1",
		EmbeddingAPIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("buildEmbedder error = %v", err)
	}
	if embedder == nil {
		t.Fatal("buildEmbedder with model = nil, want embedder")
	}
	if embedder.Model() != "text-embedding-3-small" {
		t.Fatalf("embedder model = %q, want text-embedding-3-small", embedder.Model())
	}
}

func TestDurableMemoryToolsBuildWithEmbedder(t *testing.T) {
	cfg := appConfig{
		StateDir:        filepath.Join(t.TempDir(), "state"),
		WorkDir:         t.TempDir(),
		EmbeddingModel:  "text-embedding-3-small",
		EmbeddingAPIKey: "sk-test",
	}
	tools, err := durableMemoryTools(context.Background(), cfg)
	if err != nil {
		t.Fatalf("durableMemoryTools error = %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("durableMemoryTools returned no tools")
	}
}

func TestPrimeMemoryEmptyWhenNothingStored(t *testing.T) {
	store, err := newMemoryStore(appConfig{StateDir: filepath.Join(t.TempDir(), "state"), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := primeMemory(context.Background(), store); got != "" {
		t.Fatalf("primeMemory with empty store = %q, want empty", got)
	}
	if instr := instructionsWithMemory("", ""); instr != defaultInstructions() {
		t.Fatal("instructionsWithMemory(empty) should equal defaultInstructions()")
	}
	const customPrompt = "You are Helga, a terse ops bot. Never use emoji."
	if instr := instructionsWithMemory(customPrompt, ""); instr != customPrompt {
		t.Fatalf("custom instructions should override default, got:\n%s", instr)
	}
	if instr := instructionsWithMemory(customPrompt, "remember: ping me at 9am"); !strings.Contains(instr, customPrompt) || !strings.Contains(instr, "ping me at 9am") || strings.Contains(instr, "lightweight personal AI assistant") {
		t.Fatalf("custom instructions + prime should use override base, got:\n%s", instr)
	}
}

func TestPrimeMemoryInjectsStoredMemory(t *testing.T) {
	ctx := context.Background()
	cfg := appConfig{StateDir: filepath.Join(t.TempDir(), "state"), WorkDir: t.TempDir()}
	store, err := newMemoryStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertMemory(ctx, sdkprojectstate.UpsertMemoryInput{
		Kind:    sdkprojectstate.MemoryKindPinned,
		Scope:   sdkprojectstate.MemoryScopeUser,
		Content: "Allergic to shellfish.",
	}); err != nil {
		t.Fatal(err)
	}
	prime := primeMemory(ctx, store)
	if !strings.Contains(prime, "Allergic to shellfish.") {
		t.Fatalf("primeMemory = %q, want it to contain the pinned memory", prime)
	}
	instr := instructionsWithMemory("", prime)
	if !strings.Contains(instr, "Allergic to shellfish.") || !strings.Contains(instr, "loaded for this run") {
		t.Fatalf("instructionsWithMemory did not inject primed memory:\n%s", instr)
	}
}

func TestAuditRecorderMirrorsStdoutAndFileWithRedaction(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.Audit = true
	cfg.AuditLogPath = filepath.Join(dir, "audit.ndjson")

	var stdout strings.Builder
	audit, err := newAuditRecorder(cfg, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	audit.EmitRunItem(&agentsdk.RunItem{
		Type: agentsdk.RunItemToolCall,
		ToolCall: &agentsdk.ToolCallData{
			ID:    "call_1",
			Name:  "Bash",
			Input: json.RawMessage(`{"cmd":"echo sk-testsecret"}`),
		},
	})
	if err := audit.Close(); err != nil {
		t.Fatal(err)
	}

	out := stdout.String()
	if !strings.Contains(out, "[audit]") || !strings.Contains(out, `"event":"tool_call"`) {
		t.Fatalf("stdout missing audit event: %q", out)
	}
	if strings.Contains(out, "sk-testsecret") {
		t.Fatalf("stdout leaked secret: %q", out)
	}
	data, err := os.ReadFile(cfg.AuditLogPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"event":"tool_call"`) {
		t.Fatalf("audit file missing event: %q", text)
	}
	if strings.Contains(text, "sk-testsecret") {
		t.Fatalf("audit file leaked secret: %q", text)
	}
}

func TestLowAuditLevelKeepsOnlyToolInputsAssistantTextAndErrors(t *testing.T) {
	cfg := defaultConfig()
	cfg.Audit = true
	cfg.AuditLevel = auditLevelLow
	cfg.AuditLogPath = filepath.Join(t.TempDir(), "audit.ndjson")

	var stdout strings.Builder
	audit, err := newAuditRecorder(cfg, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	audit.EmitRunStart(cfg, "hidden prompt")
	audit.EmitRunItem(&agentsdk.RunItem{
		Type: agentsdk.RunItemToolCall,
		ToolCall: &agentsdk.ToolCallData{
			ID:    "call_1",
			Name:  "Read",
			Input: json.RawMessage(`{"file":"README.md"}`),
		},
	})
	audit.EmitRunItem(&agentsdk.RunItem{
		Type:       agentsdk.RunItemToolOutput,
		ToolOutput: &agentsdk.ToolOutputData{CallID: "call_1", Content: "success"},
	})
	audit.EmitRunItem(&agentsdk.RunItem{
		Type:    agentsdk.RunItemMessage,
		Message: &agentsdk.MessageOutput{Text: "assistant text"},
	})
	audit.EmitRunItem(&agentsdk.RunItem{
		Type: agentsdk.RunItemToolOutput,
		ToolOutput: &agentsdk.ToolOutputData{
			CallID:  "call_2",
			Content: "failed",
			IsError: true,
		},
	})
	audit.EmitRunError(errors.New("run failed"))
	audit.EmitOperationalError("telegram", "poll", errors.New("poll failed"))
	audit.EmitApprovalDecision("Read", json.RawMessage(`{"file":"README.md"}`), true)
	audit.EmitRunEnd(&agentsdk.RunResult{})
	if err := audit.Close(); err != nil {
		t.Fatal(err)
	}

	out := stdout.String()
	for _, want := range []string{
		`"event":"tool_call"`,
		`"tool":"Read"`,
		`"input":{"file":"README.md"}`,
		`"event":"assistant_message"`,
		`"text":"assistant text"`,
		`"event":"tool_error"`,
		`"content":"failed"`,
		`"event":"run_error"`,
		`"error":"run failed"`,
		`"event":"operational_error"`,
		`"component":"telegram"`,
		`"stage":"poll"`,
		`"error":"poll failed"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("low audit stdout missing %s in %q", want, out)
		}
	}
	for _, forbidden := range []string{
		`"event":"run_start"`,
		`hidden prompt`,
		`"event":"tool_output"`,
		`"content":"success"`,
		`"event":"approval_decision"`,
		`"event":"run_end"`,
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("low audit stdout included %s in %q", forbidden, out)
		}
	}
}

func TestEmitAuditErrorWritesOperationalError(t *testing.T) {
	cfg := defaultConfig()
	cfg.Audit = true
	cfg.AuditLogPath = filepath.Join(t.TempDir(), "audit.ndjson")

	var stdout strings.Builder
	emitAuditError(cfg, &stdout, "telegram", "reply", errors.New("send failed"))

	out := stdout.String()
	for _, want := range []string{
		`"event":"operational_error"`,
		`"component":"telegram"`,
		`"stage":"reply"`,
		`"error":"send failed"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("operational audit stdout missing %s in %q", want, out)
		}
	}
	data, err := os.ReadFile(cfg.AuditLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, `"event":"operational_error"`) {
		t.Fatalf("audit file missing operational error: %q", text)
	}
}

func TestDurableMemoryToolsAreModelDriven(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.WorkDir = t.TempDir()
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.EnableProjectState = true

	extensions, err := loadExtensions(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range extensions.ExtraTools {
		names[tool.Name()] = true
	}
	for _, want := range []string{
		"memory_remember",
		"memory_recall",
		"memory_list",
		"memory_update",
		"memory_delete",
		"memory_stats",
		"prime_context",
	} {
		if !names[want] {
			t.Fatalf("missing durable memory tool %q; names=%v", want, names)
		}
	}
	rt, err := runtimeConfig(cfg, extensions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.EnableProjectState {
		t.Fatal("runtime EnableProjectState = true, want false so host does not auto-prime memory")
	}
	if !strings.Contains(defaultInstructions(), "Durable memory is model-driven") {
		t.Fatal("default instructions do not state model-driven memory policy")
	}
}

func TestRuntimeConfigUsesExplicitSDKFeatures(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.EnableTools = true
	cfg.EnableMCP = true
	cfg.EnableApproval = true
	cfg.EnableCompaction = true
	cfg.EnableGuardrails = true
	extensions := extensionBundle{
		MCPConfig: &sdkmcp.Config{MCPServers: map[string]sdkmcp.ServerConfig{
			"files": {Type: "stdio", Command: "files-mcp"},
		}},
		ExtraTools: []agentsdk.Tool{fakeWriteTool{name: "host_tool"}},
	}
	rt, err := runtimeConfig(cfg, extensions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Features == nil {
		t.Fatal("runtime Features = nil, want explicit SDK features")
	}
	features := *rt.Features
	for name, enabled := range map[string]bool{
		"listFiles":        features.Tools.ListFiles,
		"readFile":         features.Tools.ReadFile,
		"grep":             features.Tools.Grep,
		"bash":             features.Tools.Bash,
		"write":            features.Tools.Write,
		"webFetch":         features.Tools.WebFetch,
		"extraTools":       features.Tools.ExtraTools,
		"finish":           features.Tools.Signals.Finish,
		"mcp":              features.MCP.Enabled,
		"mcpAllServers":    features.MCP.AllowAllServers,
		"mcpAllTools":      features.MCP.AllowAllTools,
		"guardrails":       features.Guardrails.Builtin,
		"modeInstructions": features.Modes.Instructions,
		"phaseTracking":    features.Modes.PhaseTracking,
		"compaction":       features.Runtime.Compaction,
		"approval":         features.Runtime.Approval,
		"retry":            features.Runtime.Retry,
		"parallelTools":    features.Runtime.ParallelToolCalls,
		"untrustedOutput":  features.Runtime.UntrustedToolOutputs,
	} {
		if !enabled {
			t.Fatalf("feature %s = false, want true", name)
		}
	}
	if features.ProjectState.TaskTools || features.ProjectState.MemoryTools || features.ProjectState.PrimeContext {
		t.Fatalf("SDK project state features = %+v, want off because assistant exposes memory as host ExtraTools", features.ProjectState)
	}
	if features.SubAgents.SyncTools || features.SubAgents.Async.Spawn || features.Handoffs.Enabled {
		t.Fatalf("sub-agent/handoff features unexpectedly enabled: subagents=%+v handoffs=%+v", features.SubAgents, features.Handoffs)
	}
}

func TestRuntimeFeatureOverridesCanDisableDefaults(t *testing.T) {
	off := false
	on := true
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.EnableTools = true
	cfg.EnableApproval = true
	cfg.FeatureOverrides = assistantFeaturesConfig{
		Tools: &assistantToolFeatures{
			Bash:     &off,
			Write:    &off,
			WebFetch: &off,
			Signals: &assistantSignalFeatures{
				PresentPlan: &off,
			},
		},
		MCP: &assistantMCPFeatures{
			Enabled:         &on,
			AllowAllServers: &off,
			AllowedServers:  []string{"files"},
			AllowAllTools:   &off,
			AllowedTools:    []string{"read"},
		},
		Runtime: &assistantRuntimeFeatures{
			Approval:             &off,
			ParallelToolCalls:    &off,
			UntrustedToolOutputs: &off,
		},
	}
	rt, err := runtimeConfig(cfg, extensionBundle{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	features := *rt.Features
	if features.Tools.Bash || features.Tools.Write || features.Tools.WebFetch || features.Tools.Signals.PresentPlan {
		t.Fatalf("disabled tool features still enabled: %+v", features.Tools)
	}
	if !features.Tools.ReadFile || !features.Tools.Grep {
		t.Fatalf("unspecified default tool features were not preserved: %+v", features.Tools)
	}
	if !features.MCP.Enabled || features.MCP.AllowAllServers || features.MCP.AllowAllTools {
		t.Fatalf("MCP overrides not applied: %+v", features.MCP)
	}
	if len(features.MCP.AllowedServers) != 1 || features.MCP.AllowedServers[0] != "files" {
		t.Fatalf("AllowedServers = %#v", features.MCP.AllowedServers)
	}
	if len(features.MCP.AllowedTools) != 1 || features.MCP.AllowedTools[0] != "read" {
		t.Fatalf("AllowedTools = %#v", features.MCP.AllowedTools)
	}
	if features.Runtime.Approval || features.Runtime.ParallelToolCalls || features.Runtime.UntrustedToolOutputs {
		t.Fatalf("runtime disables not applied: %+v", features.Runtime)
	}
}

func TestRuntimeFeatureOverridesCanStartAllOff(t *testing.T) {
	off := false
	on := true
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.EnableTools = true
	cfg.EnableApproval = true
	cfg.EnableCompaction = true
	cfg.FeatureOverrides = assistantFeaturesConfig{
		Defaults: &off,
		Tools: &assistantToolFeatures{
			ReadFile: &on,
			Signals: &assistantSignalFeatures{
				Finish: &on,
			},
		},
		Runtime: &assistantRuntimeFeatures{
			Retry:                &on,
			UntrustedToolOutputs: &on,
		},
	}
	rt, err := runtimeConfig(cfg, extensionBundle{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	features := *rt.Features
	if !features.Tools.ReadFile || !features.Tools.Signals.Finish || !features.Runtime.Retry || !features.Runtime.UntrustedToolOutputs {
		t.Fatalf("selected all-off overrides not enabled: %+v", features)
	}
	if features.Tools.ListFiles || features.Tools.Bash || features.Tools.Write || features.Runtime.Approval || features.Runtime.Compaction || features.Modes.Instructions {
		t.Fatalf("all-off base left defaults enabled: %+v", features)
	}
}

func TestLoadMergedMCPConfigMergesFilesInlineAndPlugins(t *testing.T) {
	workDir := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra.json")
	if err := os.WriteFile(filepath.Join(workDir, sdkmcp.ConfigFileName), []byte(`{
		"mcpServers": {
			"workspace": {"type": "stdio", "command": "workspace-mcp"}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, []byte(`{
		"mcpServers": {
			"extra": {"type": "stdio", "command": "extra-mcp"}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	cfg.WorkDir = workDir
	cfg.MCPConfigPaths = []string{extra}
	cfg.FileConfig = assistantConfigFile{
		MCPServers: map[string]sdkmcp.ServerConfig{
			"inline": {Type: "stdio", Command: "inline-mcp"},
		},
		Extensions: []pluginConfig{{
			Name: "plugin",
			MCPServers: map[string]sdkmcp.ServerConfig{
				"plugin": {Type: "stdio", Command: "plugin-mcp"},
			},
		}},
	}

	merged, err := loadMergedMCPConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace", "extra", "inline", "plugin"} {
		if _, ok := merged.MCPServers[name]; !ok {
			t.Fatalf("missing server %q in %#v", name, merged.MCPServers)
		}
	}
}

func TestGatewayBearerAuthFailsClosed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	gw := newGateway(appConfig{}, io.Discard, io.Discard)
	if gw.authorized(req, "") {
		t.Fatal("empty gateway token authorized request")
	}
	req.Header.Set("Authorization", "good")
	if gw.authorized(req, "good") {
		t.Fatal("gateway authorized token without bearer scheme")
	}
	req.Header.Set("Authorization", "Bearer good")
	if !gw.authorized(req, "good") {
		t.Fatal("valid gateway token rejected")
	}
}

func TestRedactedEndpointRemovesTelegramBotToken(t *testing.T) {
	got := redactedEndpoint("https://api.telegram.org/bot123456:secret/sendMessage")
	if strings.Contains(got, "123456:secret") {
		t.Fatalf("token was not redacted: %q", got)
	}
	if want := "https://api.telegram.org/bot<redacted>/sendMessage"; got != want {
		t.Fatalf("redacted endpoint = %q, want %q", got, want)
	}
}

func TestDecodeTelegramUpdates(t *testing.T) {
	resp, err := decodeTelegramUpdates([]byte(`{
		"ok": true,
		"result": [
			{
				"update_id": 41,
				"message": {
					"text": "hello",
					"chat": {"id": 9},
					"from": {"id": 7, "username": "hunter"}
				}
			}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || len(resp.Result) != 1 {
		t.Fatalf("unexpected telegram response: %#v", resp)
	}
	update := resp.Result[0]
	if update.UpdateID != 41 || update.Message.Text != "hello" || update.Message.Chat.ID != 9 {
		t.Fatalf("bad telegram update decode: %#v", update)
	}
}

func TestDecodeTelegramCallbackQueryUpdates(t *testing.T) {
	resp, err := decodeTelegramUpdates([]byte(`{
		"ok": true,
		"result": [
			{
				"update_id": 42,
				"callback_query": {
					"id": "callback-1",
					"data": "assistant:/clear",
					"from": {"id": 7, "username": "hunter"},
					"message": {
						"text": "history cleared",
						"chat": {"id": 9}
					}
				}
			}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	update := resp.Result[0]
	if update.CallbackQuery.ID != "callback-1" || update.CallbackQuery.Data != "assistant:/clear" || update.CallbackQuery.Message.Chat.ID != 9 {
		t.Fatalf("bad telegram callback decode: %#v", update)
	}
}

func TestDecodeTelegramPhotoMessage(t *testing.T) {
	resp, err := decodeTelegramUpdates([]byte(`{
		"ok": true,
		"result": [
			{
				"update_id": 43,
				"message": {
					"caption": "look at this",
					"chat": {"id": 9},
					"from": {"id": 7, "username": "hunter"},
					"photo": [
						{"file_id": "small", "file_size": 100, "width": 90, "height": 90},
						{"file_id": "large", "file_size": 5000, "width": 1280, "height": 1280}
					]
				}
			}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	msg := resp.Result[0].Message
	if msg.Caption != "look at this" || len(msg.Photo) != 2 {
		t.Fatalf("bad photo message decode: %#v", msg)
	}
	best, ok := largestTelegramPhoto(msg.Photo)
	if !ok || best.FileID != "large" {
		t.Fatalf("largestTelegramPhoto = %#v, %v; want file_id large", best, ok)
	}
}

func TestLargestTelegramPhotoSkipsEmptyFileID(t *testing.T) {
	if _, ok := largestTelegramPhoto(nil); ok {
		t.Fatal("expected no photo for empty slice")
	}
	if _, ok := largestTelegramPhoto([]telegramPhotoSize{{FileID: "", FileSize: 10}}); ok {
		t.Fatal("expected no photo when file_id is empty")
	}
	best, ok := largestTelegramPhoto([]telegramPhotoSize{
		{FileID: "a", FileSize: 0, Width: 10, Height: 10},
		{FileID: "b", FileSize: 0, Width: 20, Height: 20},
	})
	if !ok || best.FileID != "b" {
		t.Fatalf("largestTelegramPhoto tie-break = %#v; want file_id b", best)
	}
}

func TestDownloadTelegramImages(t *testing.T) {
	imageBytes := []byte("\xff\xd8\xfffake-jpeg-data")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_path":"photos/file_1.jpg"}}`)
		case strings.Contains(r.URL.Path, "/file/bot"):
			_, _ = w.Write(imageBytes)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	origAPI, origFile := telegramAPIBaseURL, telegramFileAPIBaseURL
	telegramAPIBaseURL = server.URL + "/bot"
	telegramFileAPIBaseURL = server.URL + "/file/bot"
	t.Cleanup(func() {
		telegramAPIBaseURL = origAPI
		telegramFileAPIBaseURL = origFile
	})

	images, err := downloadTelegramImages(context.Background(), "token", telegramMessage{
		Photo: []telegramPhotoSize{{FileID: "large", FileSize: 5000}},
	})
	if err != nil {
		t.Fatalf("downloadTelegramImages error: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].MediaType != "image/jpeg" {
		t.Fatalf("media type = %q, want image/jpeg", images[0].MediaType)
	}
	decoded, err := base64.StdEncoding.DecodeString(images[0].Data)
	if err != nil {
		t.Fatalf("image data not base64: %v", err)
	}
	if string(decoded) != string(imageBytes) {
		t.Fatalf("decoded image mismatch: %q", decoded)
	}
}

func TestTelegramCallbackCommandAllowList(t *testing.T) {
	for _, input := range []string{"assistant:/clear", "assistant:/plan", "assistant:/chat", "assistant:/help", "assistant:/version"} {
		if got := telegramCallbackCommand(input); got == "" {
			t.Fatalf("telegramCallbackCommand(%q) rejected allowed action", input)
		}
	}
	for _, input := range []string{"", "/clear", "assistant:/mode admin", "other:/clear"} {
		if got := telegramCallbackCommand(input); got != "" {
			t.Fatalf("telegramCallbackCommand(%q) = %q, want empty", input, got)
		}
	}
}

func TestTelegramApprovalCallbackParsing(t *testing.T) {
	id, approved, ok := telegramApprovalCallback("assistant:approval:req-1:approve")
	if !ok || id != "req-1" || !approved {
		t.Fatalf("approve callback parsed as id=%q approved=%v ok=%v", id, approved, ok)
	}
	id, approved, ok = telegramApprovalCallback("assistant:approval:req-2:deny")
	if !ok || id != "req-2" || approved {
		t.Fatalf("deny callback parsed as id=%q approved=%v ok=%v", id, approved, ok)
	}
	for _, input := range []string{"", "assistant:/clear", "assistant:approval::approve", "assistant:approval:req-3:maybe"} {
		if _, _, ok := telegramApprovalCallback(input); ok {
			t.Fatalf("telegramApprovalCallback(%q) accepted invalid callback", input)
		}
	}
}

func TestTelegramApprovalTextDecision(t *testing.T) {
	for _, input := range []string{"y", "yes", "approve", "/approve", "allow"} {
		approved, ok := telegramApprovalTextDecision(input)
		if !ok || !approved {
			t.Fatalf("telegramApprovalTextDecision(%q) = %v, %v; want approval", input, approved, ok)
		}
	}
	for _, input := range []string{"n", "no", "deny", "/deny", "reject"} {
		approved, ok := telegramApprovalTextDecision(input)
		if !ok || approved {
			t.Fatalf("telegramApprovalTextDecision(%q) = %v, %v; want denial", input, approved, ok)
		}
	}
	if _, ok := telegramApprovalTextDecision("run something else"); ok {
		t.Fatal("telegramApprovalTextDecision accepted unrelated text")
	}
}

func TestTelegramBotCommandsIncludeChatControls(t *testing.T) {
	data, err := json.Marshal(telegramBotCommands())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"clear", "version", "plan", "chat", "help"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("telegram bot commands missing %q in %s", want, data)
		}
	}
}

func TestTelegramRunFailureReplyDefaultsToGeneric(t *testing.T) {
	err := errors.New("provider returned secret failure detail\nsecond line")
	got := telegramRunFailureReply(appConfig{}, err)
	if got != telegramGenericFailureReply {
		t.Fatalf("telegramRunFailureReply = %q, want generic reply", got)
	}
	for _, leaked := range []string{"provider", "secret", "second line"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("generic Telegram failure reply leaked %q in %q", leaked, got)
		}
	}
}

func TestTelegramRunFailureReplyCanExposeDetails(t *testing.T) {
	err := errors.New("provider returned failure detail\nsecond line")
	got := telegramRunFailureReply(appConfig{TelegramErrorDetails: true}, err)
	want := "Run failed: provider returned failure detail"
	if got != want {
		t.Fatalf("telegramRunFailureReply = %q, want %q", got, want)
	}
	if strings.Contains(got, "second line") {
		t.Fatalf("telegramRunFailureReply included more than first line: %q", got)
	}
}

func TestTelegramAccessRequiresAllowList(t *testing.T) {
	cfg := appConfig{}
	user := telegramUser{ID: 7, Username: "hunter"}
	if telegramAccessAllowed(cfg, 9, user) {
		t.Fatal("telegramAccessAllowed allowed a user with no allowlist")
	}
}

func TestTelegramAccessAllowList(t *testing.T) {
	user := telegramUser{ID: 7, Username: "Hunter"}
	tests := []struct {
		name    string
		cfg     appConfig
		allowed bool
	}{
		{
			name:    "user id",
			cfg:     appConfig{TelegramAllowedUsers: normalizeTelegramAllowList([]string{"7"})},
			allowed: true,
		},
		{
			name:    "username",
			cfg:     appConfig{TelegramAllowedUsers: normalizeTelegramAllowList([]string{"@hunter"})},
			allowed: true,
		},
		{
			name:    "chat id",
			cfg:     appConfig{TelegramAllowedChats: normalizeTelegramAllowList([]string{"9"})},
			allowed: true,
		},
		{
			name:    "wildcard",
			cfg:     appConfig{TelegramAllowedUsers: normalizeTelegramAllowList([]string{"*"})},
			allowed: true,
		},
		{
			name:    "different user",
			cfg:     appConfig{TelegramAllowedUsers: normalizeTelegramAllowList([]string{"8"})},
			allowed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := telegramAccessAllowed(tt.cfg, 9, user); got != tt.allowed {
				t.Fatalf("telegramAccessAllowed() = %v, want %v", got, tt.allowed)
			}
		})
	}
}

func TestNormalizeTelegramAllowList(t *testing.T) {
	got := normalizeTelegramAllowList([]string{"@Hunter, 7", "hunter"})
	want := []string{"hunter", "7"}
	if len(got) != len(want) {
		t.Fatalf("normalizeTelegramAllowList() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeTelegramAllowList() = %#v, want %#v", got, want)
		}
	}
}

func TestTelegramHTMLMessagePreservesSupportedFormatting(t *testing.T) {
	input := strings.Join([]string{
		`<b>Weather</b>`,
		`<blockquote expandable>Details tomorrow</blockquote>`,
		`<tg-spoiler>Possible rain</tg-spoiler>`,
		`<tg-time unix="1647531900" format="r">soon</tg-time>`,
	}, "\n")
	got := telegramHTMLMessage(input)
	for _, want := range []string{
		`<b>Weather</b>`,
		`<blockquote expandable>Details tomorrow</blockquote>`,
		`<tg-spoiler>Possible rain</tg-spoiler>`,
		`<tg-time unix="1647531900" format="r">soon</tg-time>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("telegramHTMLMessage missing %q in %q", want, got)
		}
	}
}

func TestTelegramHTMLMessageConvertsMarkdownTableToPre(t *testing.T) {
	input := strings.Join([]string{
		`<b>Forecast</b>`,
		``,
		`| Time | Temp | Conditions |`,
		`| --- | --- | --- |`,
		`| Now | 72 F | Clear |`,
		`| Tonight | 61 F | Cloudy |`,
	}, "\n")
	got := telegramHTMLMessage(input)
	for _, want := range []string{
		`<pre>Time`,
		`Temp`,
		`Conditions`,
		`Tonight`,
		`</pre>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("telegramHTMLMessage missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "| --- |") {
		t.Fatalf("telegramHTMLMessage kept markdown separator: %q", got)
	}
}

func TestTelegramHTMLMessageConvertsMarkdownFenceToPreCode(t *testing.T) {
	input := "```python\nprint('hi')\n```"
	got := telegramHTMLMessage(input)
	want := `<pre><code class="language-python">print(&#39;hi&#39;)</code></pre>`
	if got != want {
		t.Fatalf("telegramHTMLMessage = %q, want %q", got, want)
	}
}

func TestTelegramHTMLMessageDropsUnsupportedHTMLAndEscapesText(t *testing.T) {
	got := telegramHTMLMessage(`5 < 7 & <table><tr><td>x</td></tr></table><script>alert(1)</script>`)
	for _, bad := range []string{"<table", "<tr", "<td", "<script"} {
		if strings.Contains(got, bad) {
			t.Fatalf("telegramHTMLMessage kept unsupported tag %q in %q", bad, got)
		}
	}
	for _, want := range []string{"5 &lt; 7 &amp;", "x", "alert(1)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("telegramHTMLMessage missing %q in %q", want, got)
		}
	}
}

func TestTelegramPlainTextRemovesMarkup(t *testing.T) {
	got := telegramPlainText(`<b>Forecast</b><pre>Time  Temp
Now   72 F</pre>`)
	for _, want := range []string{"Forecast", "Time  Temp", "Now   72 F"} {
		if !strings.Contains(got, want) {
			t.Fatalf("telegramPlainText missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "<b>") || strings.Contains(got, "<pre>") {
		t.Fatalf("telegramPlainText kept markup: %q", got)
	}
}

func TestTelegramOffsetStateRoundTrip(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	if err := saveTelegramOffset(cfg, 42); err != nil {
		t.Fatal(err)
	}
	offset, err := loadTelegramOffset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 42 {
		t.Fatalf("offset=%d, want 42", offset)
	}
}

func TestGmailHelpers(t *testing.T) {
	var msg gmailMessage
	msg.ID = "msg-1"
	msg.ThreadID = "thread-1"
	msg.Snippet = "short body"
	msg.Payload.Headers = append(msg.Payload.Headers,
		struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: "From", Value: "Sender <sender@example.com>"},
		struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: "Subject", Value: "Status"},
	)
	if got := gmailHeader(msg, "from"); got != "Sender <sender@example.com>" {
		t.Fatalf("gmailHeader=%q", got)
	}
	text := gmailInboundText(msg)
	for _, want := range []string{"From: Sender <sender@example.com>", "Subject: Status", "Snippet: short body"} {
		if !strings.Contains(text, want) {
			t.Fatalf("gmailInboundText missing %q: %q", want, text)
		}
	}
	if got := replySubject("Status"); got != "Re: Status" {
		t.Fatalf("replySubject=%q", got)
	}
	if got := replySubject("Re: Status"); got != "Re: Status" {
		t.Fatalf("replySubject existing re=%q", got)
	}
}

func TestGmailSeenStateRoundTrip(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	state := gmailSeenState{Seen: map[string]bool{"msg-1": true}}
	if err := saveGmailSeenState(cfg, state); err != nil {
		t.Fatal(err)
	}
	got, err := loadGmailSeenState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Seen["msg-1"] {
		t.Fatalf("missing saved gmail seen state: %#v", got)
	}
}
