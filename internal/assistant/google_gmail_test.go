// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGmailReadonlyScopeGating(t *testing.T) {
	readonly := []string{"https://www.googleapis.com/auth/gmail.readonly"}
	modify := []string{"https://www.googleapis.com/auth/gmail.modify"}
	calendarOnly := []string{"https://www.googleapis.com/auth/calendar.readonly"}

	if !hasGmailReadonlyScope(readonly) {
		t.Fatal("expected gmail.readonly scope to be detected")
	}
	if hasGmailReadonlyScope(modify) {
		t.Fatal("gmail.modify should not satisfy read-only scope gate")
	}
	if hasGmailReadonlyScope(calendarOnly) {
		t.Fatal("calendar-only should not report a Gmail scope")
	}
}

func TestGoogleGmailToolsRegistration(t *testing.T) {
	cfg := writeConnectedAuth(t, []string{"https://www.googleapis.com/auth/gmail.readonly"})
	tools, err := googleGmailTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 || tools[0].Name() != "gmail_search_messages" || tools[1].Name() != "gmail_get_message" || tools[2].Name() != "gmail_get_thread" {
		t.Fatalf("expected Gmail read tools, got %v", toolNames(tools))
	}
	for _, tool := range tools {
		if !tool.IsReadOnly() {
			t.Fatalf("%s should be read-only", tool.Name())
		}
	}

	cfg = writeConnectedAuth(t, []string{"https://www.googleapis.com/auth/calendar.readonly"})
	tools, err = googleGmailTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected no Gmail tools, got %v", toolNames(tools))
	}
}

func TestGmailSearchGetMessageAndThread(t *testing.T) {
	message := gmailMessage{
		ID:           "msg-1",
		ThreadID:     "thread-1",
		LabelIDs:     []string{"INBOX", "UNREAD"},
		Snippet:      "hello snippet",
		InternalDate: "1780617600000",
		Payload: gmailMessagePart{
			MIMEType: "text/plain",
			Headers: []gmailHeaderField{
				{Name: "From", Value: "Alice <alice@example.com>"},
				{Name: "To", Value: "User <user@example.com>"},
				{Name: "Subject", Value: "Hello"},
				{Name: "Date", Value: "Fri, 5 Jun 2026 09:00:00 +0000"},
			},
			Body: gmailMessageBody{Data: encodeGmailTestBody("Hello from Alice")},
		},
	}
	gmail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/users/me/messages":
			if r.URL.Query().Get("q") != "from:alice" {
				t.Errorf("expected query from:alice, got %q", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, gmailListResponse{
				Messages:           []gmailMessageRef{{ID: "msg-1", ThreadID: "thread-1"}},
				NextPageToken:      "next-1",
				ResultSizeEstimate: 1,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/users/me/messages/msg-1":
			if r.URL.Query().Get("format") != "full" {
				t.Errorf("expected format=full, got %q", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, message)
		case r.Method == http.MethodGet && r.URL.Path == "/users/me/threads/thread-1":
			if r.URL.Query().Get("format") != "full" {
				t.Errorf("expected format=full, got %q", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, gmailThread{ID: "thread-1", Messages: []gmailMessage{message}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gmail.Close()

	prev := gmailAPIBase
	gmailAPIBase = gmail.URL + "/users/"
	defer func() { gmailAPIBase = prev }()

	cfg := defaultConfig()
	base := gmailToolBase{cfg: cfg, src: func(context.Context) (string, error) { return "tok", nil }, client: gmail.Client()}

	search := &gmailSearchMessagesTool{gmailToolBase: base}
	res, _ := search.Execute(context.Background(), json.RawMessage(`{"query":"from:alice","max_results":5}`), "")
	if res.IsError || !strings.Contains(res.Content, "msg-1") || !strings.Contains(res.Content, "next-1") {
		t.Fatalf("search result unexpected: %#v", res)
	}

	get := &gmailGetMessageTool{gmailToolBase: base}
	res, _ = get.Execute(context.Background(), json.RawMessage(`{"message_id":"msg-1"}`), "")
	for _, want := range []string{"alice@example.com", "Hello from Alice", "INBOX"} {
		if res.IsError || !strings.Contains(res.Content, want) {
			t.Fatalf("message result missing %q: %#v", want, res)
		}
	}

	thread := &gmailGetThreadTool{gmailToolBase: base}
	res, _ = thread.Execute(context.Background(), json.RawMessage(`{"thread_id":"thread-1"}`), "")
	if res.IsError || !strings.Contains(res.Content, "\"count\": 1") || !strings.Contains(res.Content, "Hello from Alice") {
		t.Fatalf("thread result unexpected: %#v", res)
	}

	res, _ = get.Execute(context.Background(), json.RawMessage(`{}`), "")
	if !res.IsError || !strings.Contains(res.Content, "message_id is required") {
		t.Fatalf("expected message_id required error, got %#v", res)
	}
}

func encodeGmailTestBody(text string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(text))
}
