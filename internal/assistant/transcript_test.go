// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestRecordTranscriptTurnRedactsAndSearches(t *testing.T) {
	cfg := transcriptTestConfig(t)
	started := time.Date(2026, 6, 3, 1, 2, 3, 0, time.UTC)
	items := []agentsdk.RunItem{
		userMessage("launch project alpha with sk-secret123"),
		{
			Type:    agentsdk.RunItemMessage,
			Message: &agentsdk.MessageOutput{Text: "Project alpha plan saved."},
		},
		{
			Type: agentsdk.RunItemToolCall,
			ToolCall: &agentsdk.ToolCallData{
				ID:    "call-1",
				Name:  "memory_remember",
				Input: []byte(`{"content":"token sk-toolsecret"}`),
			},
		},
	}

	if err := recordTranscriptTurn(t.Context(), cfg, transcriptContext{
		SessionID:      "sess_alpha",
		ConversationID: "test:alpha",
		Channel:        "test",
		UserText:       "launch project alpha with sk-secret123",
	}, "launch project alpha with sk-secret123", conversationModeChat, started, items, "Project alpha plan saved."); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(cfg.TranscriptLogPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "sk-secret123") || strings.Contains(text, "sk-toolsecret") {
		t.Fatalf("transcript leaked secret: %s", text)
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("transcript missing redaction marker: %s", text)
	}

	result, err := searchTranscriptTurns(t.Context(), cfg, transcriptSearchInput{Query: "alpha", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "search" || len(result.Turns) != 1 {
		t.Fatalf("search result = %#v, want one matching turn", result)
	}
	if result.Turns[0].SessionID != "sess_alpha" || !strings.Contains(result.Turns[0].Snippet, "alpha") {
		t.Fatalf("search turn = %#v", result.Turns[0])
	}
}

func TestTranscriptSearchBrowseSessionAndScroll(t *testing.T) {
	cfg := transcriptTestConfig(t)
	base := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	for i, text := range []string{"first note", "middle decision", "final launch"} {
		turn := transcriptTurn{
			ID:             "turn_" + string(rune('a'+i)),
			SessionID:      "sess_one",
			ConversationID: "test:one",
			Channel:        "test",
			StartedAt:      base.Add(time.Duration(i) * time.Minute),
			EndedAt:        base.Add(time.Duration(i)*time.Minute + time.Second),
			UserText:       text,
			FinalText:      "reply " + text,
		}
		if err := appendTranscriptTurn(cfg, turn); err != nil {
			t.Fatal(err)
		}
	}

	browse, err := searchTranscriptTurns(t.Context(), cfg, transcriptSearchInput{})
	if err != nil {
		t.Fatal(err)
	}
	if browse.Mode != "browse" || len(browse.Sessions) != 1 || browse.Sessions[0].Turns != 3 {
		t.Fatalf("browse result = %#v", browse)
	}

	session, err := searchTranscriptTurns(t.Context(), cfg, transcriptSearchInput{SessionID: "sess_one", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode != "session" || len(session.Turns) != 2 || session.Turns[0].ID != "turn_b" || session.Turns[1].ID != "turn_c" {
		t.Fatalf("session result = %#v", session)
	}

	scroll, err := searchTranscriptTurns(t.Context(), cfg, transcriptSearchInput{SessionID: "sess_one", AroundTurnID: "turn_b", Window: 1})
	if err != nil {
		t.Fatal(err)
	}
	if scroll.Mode != "scroll" || len(scroll.Turns) != 3 || scroll.Turns[0].ID != "turn_a" || scroll.Turns[2].ID != "turn_c" {
		t.Fatalf("scroll result = %#v", scroll)
	}
}

func transcriptTestConfig(t *testing.T) appConfig {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.StateDir = dir
	cfg.EnableTranscripts = true
	cfg.TranscriptLogPath = filepath.Join(dir, transcriptStateFileName)
	cfg.EmbeddingModel = ""
	cfg.EmbeddingBaseURL = ""
	cfg.EmbeddingAPIKey = ""
	return cfg
}
