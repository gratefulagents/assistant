// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestCalendarScopeGating(t *testing.T) {
	readonly := []string{"https://www.googleapis.com/auth/calendar.readonly"}
	full := []string{"https://www.googleapis.com/auth/calendar"}
	gmailOnly := []string{"https://www.googleapis.com/auth/gmail.readonly"}

	if !hasAnyCalendarScope(readonly) || !hasAnyCalendarScope(full) {
		t.Fatal("expected calendar scopes to be detected")
	}
	if hasAnyCalendarScope(gmailOnly) {
		t.Fatal("gmail-only should not report a calendar scope")
	}
}

func writeConnectedAuth(t *testing.T, scopes []string) appConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "google-auth.json")
	if err := saveGoogleAuthFile(path, googleAuthFile{
		BrokerURL:   "https://connect.example.com",
		AssistantID: "aid",
		Secret:      "secret",
		Scopes:      scopes,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.GoogleAuthPath = path
	return cfg
}

func TestGoogleCalendarToolsRegistration(t *testing.T) {
	// Read-only calendar scope: list + get tools are registered.
	cfg := writeConnectedAuth(t, []string{"https://www.googleapis.com/auth/calendar.readonly"})
	tools, err := googleCalendarTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name() != "calendar_list_events" || tools[1].Name() != "calendar_get_event" {
		t.Fatalf("expected calendar_list_events + calendar_get_event, got %v", toolNames(tools))
	}

	// Full calendar scope: same read tools (no write tools).
	cfg = writeConnectedAuth(t, []string{"https://www.googleapis.com/auth/calendar"})
	tools, err = googleCalendarTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 calendar tools, got %v", toolNames(tools))
	}

	// No calendar scope: no tools.
	cfg = writeConnectedAuth(t, []string{"https://www.googleapis.com/auth/gmail.readonly"})
	tools, err = googleCalendarTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected no calendar tools, got %v", toolNames(tools))
	}
}

func toolNames(tools []agentsdk.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}

func TestCalendarListAndGetEvent(t *testing.T) {
	calendar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/calendars/primary/events":
			if r.URL.Query().Get("singleEvents") != "true" {
				t.Errorf("expected singleEvents=true, got %q", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": []calendarEvent{{
				ID:      "evt-1",
				Summary: "Standup",
				Start:   calendarTime{DateTime: "2026-06-05T09:00:00-07:00"},
				End:     calendarTime{DateTime: "2026-06-05T09:15:00-07:00"},
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/calendars/primary/events/evt-1":
			writeJSON(w, http.StatusOK, calendarEvent{
				ID:          "evt-1",
				Summary:     "Standup",
				Description: "Daily sync",
				Location:    "Room 4",
				Start:       calendarTime{DateTime: "2026-06-05T09:00:00-07:00"},
				End:         calendarTime{DateTime: "2026-06-05T09:15:00-07:00"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer calendar.Close()

	prev := googleCalendarAPIBase
	googleCalendarAPIBase = calendar.URL
	defer func() { googleCalendarAPIBase = prev }()

	base := calendarToolBase{src: func(context.Context) (string, error) { return "tok", nil }, client: defaultHTTPClient}

	list := &calendarListEventsTool{calendarToolBase: base}
	res, _ := list.Execute(context.Background(), json.RawMessage(`{}`), "")
	if res.IsError || !strings.Contains(res.Content, "Standup") || !strings.Contains(res.Content, "\"count\": 1") {
		t.Fatalf("list result unexpected: %#v", res)
	}

	get := &calendarGetEventTool{calendarToolBase: base}
	if !get.IsReadOnly() {
		t.Fatal("get event should be read-only")
	}
	res, _ = get.Execute(context.Background(), json.RawMessage(`{"event_id":"evt-1"}`), "")
	if res.IsError || !strings.Contains(res.Content, "Daily sync") || !strings.Contains(res.Content, "Room 4") {
		t.Fatalf("get result unexpected: %#v", res)
	}

	res, _ = get.Execute(context.Background(), json.RawMessage(`{}`), "")
	if !res.IsError || !strings.Contains(res.Content, "event_id is required") {
		t.Fatalf("expected event_id required error, got %#v", res)
	}
}

func TestCalendarDeniedScopeMessage(t *testing.T) {
	calendar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"insufficient"}`, http.StatusForbidden)
	}))
	defer calendar.Close()
	prev := googleCalendarAPIBase
	googleCalendarAPIBase = calendar.URL
	defer func() { googleCalendarAPIBase = prev }()

	base := calendarToolBase{src: func(context.Context) (string, error) { return "tok", nil }, client: defaultHTTPClient}
	list := &calendarListEventsTool{calendarToolBase: base}
	res, _ := list.Execute(context.Background(), json.RawMessage(`{}`), "")
	if !res.IsError || !strings.Contains(res.Content, "google-connect") {
		t.Fatalf("expected scope hint on 403, got %#v", res)
	}
}
