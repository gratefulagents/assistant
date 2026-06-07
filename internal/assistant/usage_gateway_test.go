// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
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
