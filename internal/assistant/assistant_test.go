// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if instr := instructionsWithMemory(""); instr != defaultInstructions() {
		t.Fatal("instructionsWithMemory(empty) should equal defaultInstructions()")
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
	instr := instructionsWithMemory(prime)
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
	for _, want := range []string{"memory_remember", "memory_recall", "memory_list", "prime_context"} {
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
