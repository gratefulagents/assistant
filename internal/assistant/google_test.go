// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestNormalizeGoogleScopes(t *testing.T) {
	got := normalizeGoogleScopes([]string{"gmail.readonly", "gmail.readonly", "https://www.googleapis.com/auth/calendar", "  "})
	want := []string{
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/calendar",
	}
	if len(got) != len(want) {
		t.Fatalf("normalizeGoogleScopes=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeGoogleScopes[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestGoogleAuthFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "google-auth.json")
	in := googleAuthFile{
		BrokerURL:   "https://connect.example.com",
		AssistantID: "aid",
		Secret:      "secret",
		Scopes:      []string{"https://www.googleapis.com/auth/gmail.readonly"},
		Email:       "user@example.com",
	}
	if err := saveGoogleAuthFile(path, in); err != nil {
		t.Fatal(err)
	}
	out, exists, err := loadGoogleAuthFile(path)
	if err != nil || !exists {
		t.Fatalf("load exists=%v err=%v", exists, err)
	}
	if out.AssistantID != "aid" || out.Secret != "secret" || out.Email != "user@example.com" {
		t.Fatalf("round trip mismatch: %#v", out)
	}
}

func TestGoogleAuthSessionMintsAndCaches(t *testing.T) {
	var calls int
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		calls++
		writeJSON(w, http.StatusOK, brokerTokenResponse{AccessToken: "access-1", ExpiresIn: 3600, Email: "user@example.com"})
	}))
	defer broker.Close()

	path := filepath.Join(t.TempDir(), "google-auth.json")
	if err := saveGoogleAuthFile(path, googleAuthFile{BrokerURL: broker.URL, AssistantID: "aid", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.GoogleAuthPath = path
	session, err := newGoogleAuthSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	token, err := session.AccessToken(context.Background())
	if err != nil || token != "access-1" {
		t.Fatalf("AccessToken=%q err=%v", token, err)
	}
	// Second call should use the cached token.
	if _, err := session.AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 broker call, got %d", calls)
	}
	// The minted token should be persisted to disk.
	saved, _, err := loadGoogleAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "access-1" {
		t.Fatalf("persisted token=%q", saved.AccessToken)
	}
}

func TestGoogleAuthSessionReconnectOnInvalidGrant(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, brokerTokenResponse{Error: "invalid_grant"})
	}))
	defer broker.Close()
	path := filepath.Join(t.TempDir(), "google-auth.json")
	if err := saveGoogleAuthFile(path, googleAuthFile{BrokerURL: broker.URL, AssistantID: "aid", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.GoogleAuthPath = path
	session, err := newGoogleAuthSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.AccessToken(context.Background()); err == nil {
		t.Fatal("expected reconnect error")
	}
}
