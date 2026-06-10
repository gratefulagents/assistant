// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeMicrosoftScopes(t *testing.T) {
	got := normalizeMicrosoftScopes([]string{"mail.read", "Mail.Read", "https://graph.microsoft.com/Calendars.Read", "  "})
	want := []string{
		"https://graph.microsoft.com/Mail.Read",
		"https://graph.microsoft.com/Calendars.Read",
	}
	if len(got) != len(want) {
		t.Fatalf("normalizeMicrosoftScopes=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeMicrosoftScopes[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestMicrosoftScopeGating(t *testing.T) {
	mail := []string{"https://graph.microsoft.com/Mail.Read"}
	calendarFull := []string{"https://graph.microsoft.com/Calendars.ReadWrite"}
	short := []string{"Mail.Read"}

	if !hasMicrosoftScope(mail, "mail.read") || !hasMicrosoftScope(short, "mail.read") {
		t.Fatal("expected mail scope to be detected")
	}
	// Calendars.ReadWrite implies read access.
	if !hasMicrosoftScope(calendarFull, "calendars.read") {
		t.Fatal("expected ReadWrite to satisfy calendars.read")
	}
	if hasMicrosoftScope(mail, "calendars.read") {
		t.Fatal("mail-only should not report a calendar scope")
	}
}

func TestMicrosoftAuthFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "microsoft-auth.json")
	in := microsoftAuthFile{
		BrokerURL:   "https://connect.example.com",
		AssistantID: "aid",
		Secret:      "secret",
		Scopes:      []string{"https://graph.microsoft.com/Mail.Read"},
		Email:       "user@example.com",
	}
	if err := saveMicrosoftAuthFile(path, in); err != nil {
		t.Fatal(err)
	}
	out, exists, err := loadMicrosoftAuthFile(path)
	if err != nil || !exists {
		t.Fatalf("load exists=%v err=%v", exists, err)
	}
	if out.AssistantID != "aid" || out.Secret != "secret" || out.Email != "user@example.com" {
		t.Fatalf("round trip mismatch: %#v", out)
	}
}

func TestMicrosoftAuthSessionMintsAndCaches(t *testing.T) {
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

	path := filepath.Join(t.TempDir(), "microsoft-auth.json")
	if err := saveMicrosoftAuthFile(path, microsoftAuthFile{BrokerURL: broker.URL, AssistantID: "aid", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.MicrosoftAuthPath = path
	session, err := newMicrosoftAuthSession(cfg)
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
	saved, _, err := loadMicrosoftAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "access-1" {
		t.Fatalf("persisted token=%q", saved.AccessToken)
	}
}

func TestMicrosoftAuthSessionReconnectOnInvalidGrant(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, brokerTokenResponse{Error: "invalid_grant"})
	}))
	defer broker.Close()
	path := filepath.Join(t.TempDir(), "microsoft-auth.json")
	if err := saveMicrosoftAuthFile(path, microsoftAuthFile{BrokerURL: broker.URL, AssistantID: "aid", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.MicrosoftAuthPath = path
	session, err := newMicrosoftAuthSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.AccessToken(context.Background()); !errors.Is(err, errMicrosoftReconnect) {
		t.Fatalf("expected reconnect error, got %v", err)
	}
}

func writeConnectedMicrosoftAuth(t *testing.T, scopes []string) appConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "microsoft-auth.json")
	if err := saveMicrosoftAuthFile(path, microsoftAuthFile{
		BrokerURL:   "https://connect.example.com",
		AssistantID: "aid",
		Secret:      "secret",
		Scopes:      scopes,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.MicrosoftAuthPath = path
	return cfg
}

func TestMicrosoftToolsRegistration(t *testing.T) {
	// Mail scope: mail tools only.
	cfg := writeConnectedMicrosoftAuth(t, []string{"https://graph.microsoft.com/Mail.Read"})
	tools, err := microsoftMailTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name() != "outlook_search_messages" || tools[1].Name() != "outlook_get_message" {
		t.Fatalf("expected outlook mail tools, got %v", toolNames(tools))
	}
	calTools, err := microsoftCalendarTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(calTools) != 0 {
		t.Fatalf("expected no calendar tools for mail-only scope, got %v", toolNames(calTools))
	}

	// Calendar scope: calendar tools only.
	cfg = writeConnectedMicrosoftAuth(t, []string{"https://graph.microsoft.com/Calendars.Read"})
	calTools, err = microsoftCalendarTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(calTools) != 2 || calTools[0].Name() != "outlook_list_events" || calTools[1].Name() != "outlook_get_event" {
		t.Fatalf("expected outlook calendar tools, got %v", toolNames(calTools))
	}
	tools, err = microsoftMailTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected no mail tools for calendar-only scope, got %v", toolNames(tools))
	}
}

func TestOutlookSearchAndGetMessage(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/me/messages":
			if !strings.Contains(r.URL.Query().Get("$search"), "invoice") {
				t.Errorf("expected $search with invoice, got %q", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{{
				"id":      "msg-1",
				"subject": "Invoice due",
				"from":    map[string]any{"emailAddress": map[string]string{"name": "Alice", "address": "alice@example.com"}},
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/me/messages/msg-1":
			if got := r.Header.Get("Prefer"); !strings.Contains(got, "text") {
				t.Errorf("expected Prefer text body header, got %q", got)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"id":      "msg-1",
				"subject": "Invoice due",
				"body":    map[string]string{"contentType": "text", "content": "Please pay soon."},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer graph.Close()

	prev := microsoftGraphAPIBase
	microsoftGraphAPIBase = graph.URL
	defer func() { microsoftGraphAPIBase = prev }()

	base := graphToolBase{src: func(context.Context) (string, error) { return "tok", nil }, client: defaultHTTPClient}

	search := &outlookSearchMessagesTool{graphToolBase: base}
	res, _ := search.Execute(context.Background(), json.RawMessage(`{"query":"invoice"}`), "")
	if res.IsError || !strings.Contains(res.Content, "Invoice due") || !strings.Contains(res.Content, "alice@example.com") {
		t.Fatalf("search result unexpected: %#v", res)
	}

	get := &outlookGetMessageTool{graphToolBase: base}
	if !get.IsReadOnly() {
		t.Fatal("get message should be read-only")
	}
	res, _ = get.Execute(context.Background(), json.RawMessage(`{"message_id":"msg-1"}`), "")
	if res.IsError || !strings.Contains(res.Content, "Please pay soon.") {
		t.Fatalf("get result unexpected: %#v", res)
	}

	res, _ = get.Execute(context.Background(), json.RawMessage(`{}`), "")
	if !res.IsError || !strings.Contains(res.Content, "message_id is required") {
		t.Fatalf("expected message_id required error, got %#v", res)
	}
}

func TestOutlookListAndGetEvent(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/me/calendarView":
			if r.URL.Query().Get("startDateTime") == "" || r.URL.Query().Get("endDateTime") == "" {
				t.Errorf("expected start/end bounds, got %q", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{{
				"id":      "evt-1",
				"subject": "Standup",
				"start":   map[string]string{"dateTime": "2026-06-11T09:00:00", "timeZone": "UTC"},
				"end":     map[string]string{"dateTime": "2026-06-11T09:15:00", "timeZone": "UTC"},
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/me/events/evt-1":
			writeJSON(w, http.StatusOK, map[string]any{
				"id":       "evt-1",
				"subject":  "Standup",
				"location": map[string]string{"displayName": "Room 4"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer graph.Close()

	prev := microsoftGraphAPIBase
	microsoftGraphAPIBase = graph.URL
	defer func() { microsoftGraphAPIBase = prev }()

	base := graphToolBase{src: func(context.Context) (string, error) { return "tok", nil }, client: defaultHTTPClient}

	list := &outlookListEventsTool{graphToolBase: base}
	res, _ := list.Execute(context.Background(), json.RawMessage(`{}`), "")
	if res.IsError || !strings.Contains(res.Content, "Standup") || !strings.Contains(res.Content, "\"count\": 1") {
		t.Fatalf("list result unexpected: %#v", res)
	}

	get := &outlookGetEventTool{graphToolBase: base}
	res, _ = get.Execute(context.Background(), json.RawMessage(`{"event_id":"evt-1"}`), "")
	if res.IsError || !strings.Contains(res.Content, "Room 4") {
		t.Fatalf("get result unexpected: %#v", res)
	}

	res, _ = get.Execute(context.Background(), json.RawMessage(`{}`), "")
	if !res.IsError || !strings.Contains(res.Content, "event_id is required") {
		t.Fatalf("expected event_id required error, got %#v", res)
	}
}

func TestOutlookDeniedScopeMessage(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"insufficient"}`, http.StatusForbidden)
	}))
	defer graph.Close()
	prev := microsoftGraphAPIBase
	microsoftGraphAPIBase = graph.URL
	defer func() { microsoftGraphAPIBase = prev }()

	base := graphToolBase{src: func(context.Context) (string, error) { return "tok", nil }, client: defaultHTTPClient}
	list := &outlookListEventsTool{graphToolBase: base}
	res, _ := list.Execute(context.Background(), json.RawMessage(`{}`), "")
	if !res.IsError || !strings.Contains(res.Content, "microsoft-connect") {
		t.Fatalf("expected scope hint on 403, got %#v", res)
	}
}
