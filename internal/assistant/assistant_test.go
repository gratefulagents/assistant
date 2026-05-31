// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
)

func TestNormalizeProvider(t *testing.T) {
	tests := map[string]string{
		"openai-oauth": providerOpenAIOAuth,
		"oauth":        providerOpenAIOAuth,
		"openai-api":   providerOpenAIAPI,
		"api":          providerOpenAIAPI,
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

func TestDefaultExtensionsAreOptIn(t *testing.T) {
	cfg := defaultConfig()
	if cfg.EnableMCP {
		t.Fatal("EnableMCP default = true, want false")
	}
	if cfg.EnableSkills {
		t.Fatal("EnableSkills default = true, want false")
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
	rt := runtimeConfig(cfg, extensions)
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
	gw := newGateway(appConfig{})
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
