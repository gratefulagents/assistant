// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

	emitLangfuseUsage(appConfig{LangfuseEnabled: false}, time.Now(), time.Now(), agentsdk.Usage{}, "direct")
	select {
	case <-called:
		t.Fatal("disabled emit should not call exporter")
	case <-time.After(200 * time.Millisecond):
	}
}
