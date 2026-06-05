// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestGatewayUsageEndpoint(t *testing.T) {
	resetUsageStores()
	cfg := appConfig{
		UserID:       "user-1",
		TokenLimit:   1000,
		UsagePath:    filepath.Join(t.TempDir(), "usage.json"),
		GatewayToken: "secret",
	}
	store, err := usageStoreFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddAt(time.Now(), agentsdk.Usage{InputTokens: 120, OutputTokens: 80, Requests: 2}); err != nil {
		t.Fatal(err)
	}

	handler := newGateway(cfg, io.Discard, io.Discard).routes()

	// Missing token -> 401.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d, want 401", rec.Code)
	}

	// Valid token -> snapshot.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/usage", nil)
	req.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200", rec.Code)
	}
	var snap usageSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.UserID != "user-1" || snap.TotalTokens != 200 || snap.Limit != 1000 || snap.Remaining != 800 {
		t.Fatalf("unexpected snapshot: %#v", snap)
	}
	if snap.Exceeded {
		t.Fatal("should not be exceeded")
	}
}

func TestLangfuseClientSendPayload(t *testing.T) {
	type captured struct {
		auth    string
		path    string
		payload langfuseIngestion
	}
	got := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p langfuseIngestion
		_ = json.NewDecoder(r.Body).Decode(&p)
		got <- captured{auth: r.Header.Get("Authorization"), path: r.URL.Path, payload: p}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := appConfig{
		LangfuseEnabled:   true,
		LangfuseHost:      server.URL,
		LangfusePublicKey: "pub",
		LangfuseSecretKey: "sec",
		UserID:            "user-9",
	}
	client, ok := newLangfuseClient(cfg)
	if !ok {
		t.Fatal("client should be enabled")
	}
	payload := langfuseIngestion{Batch: []langfuseEvent{{
		ID:   "evt",
		Type: "trace-create",
		Body: map[string]any{"userId": "user-9"},
	}}}
	if err := client.send(context.Background(), payload); err != nil {
		t.Fatal(err)
	}

	select {
	case c := <-got:
		if c.path != "/api/public/ingestion" {
			t.Fatalf("path = %q", c.path)
		}
		// base64("pub:sec") = cHViOnNlYw==
		if c.auth != "Basic cHViOnNlYw==" {
			t.Fatalf("auth = %q", c.auth)
		}
		if len(c.payload.Batch) != 1 || c.payload.Batch[0].Body["userId"] != "user-9" {
			t.Fatalf("payload = %#v", c.payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive payload")
	}
}

func TestBuildLangfusePayloadEnriched(t *testing.T) {
	cfg := appConfig{
		LangfuseEnabled: true,
		UserID:          "user-9",
		Model:           "claude-3",
		Provider:        "anthropic",
		Reasoning:       "high",
		MaxTokens:       4096,
		ActiveMode:      "chat",
		ActivePhase:     "build",
	}
	items := []agentsdk.RunItem{
		{Type: agentsdk.RunItemMessage, Message: &agentsdk.MessageOutput{Text: "hello there"}},
		{Type: agentsdk.RunItemToolCall, Agent: &agentsdk.Agent{Name: "main"}, ToolCall: &agentsdk.ToolCallData{ID: "call-1", Name: "grep", Input: json.RawMessage(`{"q":"x"}`)}},
		{Type: agentsdk.RunItemToolOutput, ToolOutput: &agentsdk.ToolOutputData{CallID: "call-1", Content: "boom", IsError: true}},
		{Type: agentsdk.RunItemMessage, Agent: &agentsdk.Agent{Name: "main"}, Message: &agentsdk.MessageOutput{Text: "the answer"}},
	}
	turn := langfuseTurn{
		cfg:       cfg,
		startTime: time.Now().Add(-time.Second),
		endTime:   time.Now(),
		usage:     agentsdk.Usage{Requests: 1, InputTokens: 120, OutputTokens: 80, CacheReadTokens: 10},
		meta:      transcriptContext{Channel: "telegram", SessionID: "sess-1", ConversationID: "conv-1"},
		prompt:    "hello there",
		finalText: "the answer",
		items:     items,
	}
	payload := buildLangfusePayload(turn)

	if len(payload.Batch) != 3 {
		t.Fatalf("expected trace+generation+1 span, got %d events", len(payload.Batch))
	}
	trace, gen, span := payload.Batch[0], payload.Batch[1], payload.Batch[2]
	if trace.Type != "trace-create" || gen.Type != "generation-create" || span.Type != "span-create" {
		t.Fatalf("unexpected event types: %s %s %s", trace.Type, gen.Type, span.Type)
	}
	// Envelope IDs must be unique and distinct from the observation body IDs.
	ids := map[string]bool{trace.ID: true, gen.ID: true, span.ID: true}
	if len(ids) != 3 {
		t.Fatalf("envelope IDs not unique: %v", ids)
	}
	if trace.ID == trace.Body["id"] || gen.ID == gen.Body["id"] {
		t.Fatal("envelope ID should differ from body id")
	}
	if trace.Body["sessionId"] != "sess-1" {
		t.Fatalf("trace sessionId = %v", trace.Body["sessionId"])
	}
	if trace.Body["input"] != "hello there" || trace.Body["output"] != "the answer" {
		t.Fatalf("trace input/output = %v / %v", trace.Body["input"], trace.Body["output"])
	}
	if gen.Body["output"] != "the answer" {
		t.Fatalf("generation output = %v", gen.Body["output"])
	}
	if gen.Body["traceId"] != trace.Body["id"] {
		t.Fatal("generation traceId must reference trace id")
	}
	msgs, ok := gen.Body["input"].([]map[string]any)
	if !ok || len(msgs) != 4 {
		t.Fatalf("generation input messages = %#v", gen.Body["input"])
	}
	if msgs[0]["role"] != "user" || msgs[3]["role"] != "assistant" {
		t.Fatalf("message roles = %v / %v", msgs[0]["role"], msgs[3]["role"])
	}
	// Tool span pairs the call with its output by CallID and flags the error.
	if span.Body["name"] != "tool:grep" {
		t.Fatalf("span name = %v", span.Body["name"])
	}
	if span.Body["traceId"] != trace.Body["id"] {
		t.Fatal("span must be a child of the trace")
	}
	if span.Body["output"] != "boom" {
		t.Fatalf("span output = %v", span.Body["output"])
	}
	if span.Body["level"] != "ERROR" {
		t.Fatalf("span level = %v", span.Body["level"])
	}
	meta, ok := gen.Body["metadata"].(map[string]any)
	if !ok || meta["tool_errors"] != 1 {
		t.Fatalf("metadata tool_errors = %#v", gen.Body["metadata"])
	}
}

func TestBuildLangfusePayloadSheddsOversizeContent(t *testing.T) {
	big := strings.Repeat("x", langfuseMaxPayloadBytes)
	items := []agentsdk.RunItem{
		{Type: agentsdk.RunItemToolCall, ToolCall: &agentsdk.ToolCallData{ID: "c1", Name: "bash", Input: json.RawMessage(`"in"`)}},
		{Type: agentsdk.RunItemToolOutput, ToolOutput: &agentsdk.ToolOutputData{CallID: "c1", Content: big}},
	}
	turn := langfuseTurn{
		cfg:       appConfig{LangfuseEnabled: true, UserID: "u"},
		startTime: time.Now(),
		endTime:   time.Now(),
		prompt:    big,
		finalText: big,
		items:     items,
	}
	payload := buildLangfusePayload(turn)
	if langfusePayloadTooLarge(payload.Batch) {
		t.Fatal("oversize payload should have been trimmed under the budget")
	}
	// The usage skeleton (trace + generation) must always survive.
	if len(payload.Batch) < 2 {
		t.Fatalf("expected trace+generation to survive, got %d", len(payload.Batch))
	}
}

func TestLangfuseDisabledIsNoOp(t *testing.T) {
	if _, ok := newLangfuseClient(appConfig{LangfuseEnabled: false}); ok {
		t.Fatal("disabled config should not produce a client")
	}
	// Missing keys -> no client even when enabled.
	if _, ok := newLangfuseClient(appConfig{LangfuseEnabled: true, LangfuseHost: "https://x"}); ok {
		t.Fatal("missing keys should not produce a client")
	}

	called := make(chan struct{}, 1)
	orig := langfuseExporter
	defer func() { langfuseExporter = orig }()
	langfuseExporter = func(appConfig, langfuseIngestion) { called <- struct{}{} }

	emitLangfuseTurn(langfuseTurn{cfg: appConfig{LangfuseEnabled: false}, startTime: time.Now(), endTime: time.Now()})
	select {
	case <-called:
		t.Fatal("disabled emit should not call exporter")
	case <-time.After(200 * time.Millisecond):
	}
}
