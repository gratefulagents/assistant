// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func msgItem(text string) agentsdk.RunItem {
	return agentsdk.RunItem{Type: agentsdk.RunItemMessage, Message: &agentsdk.MessageOutput{Text: text}}
}

func toolCallItem(name string, input any) agentsdk.RunItem {
	raw, _ := json.Marshal(input)
	return agentsdk.RunItem{
		Type:     agentsdk.RunItemToolCall,
		ToolCall: &agentsdk.ToolCallData{Name: name, Input: raw},
	}
}

func TestReplyFromTurnItems(t *testing.T) {
	t.Run("empty items yields empty string", func(t *testing.T) {
		if got := replyFromTurnItems(nil); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})

	t.Run("plain assistant message is returned", func(t *testing.T) {
		got := replyFromTurnItems([]agentsdk.RunItem{msgItem("Here is your answer.")})
		if got != "Here is your answer." {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("AskUserQuestion with message keeps text and appends choices once", func(t *testing.T) {
		items := []agentsdk.RunItem{
			msgItem("I can help in a few ways. Which would you like?"),
			toolCallItem("AskUserQuestion", map[string]any{
				"question": "Which would you like?",
				"choices":  []string{"Set up a reminder", "Check events"},
			}),
		}
		got := replyFromTurnItems(items)
		if !strings.Contains(got, "I can help in a few ways.") {
			t.Fatalf("missing assistant text: %q", got)
		}
		if !strings.Contains(got, "• Set up a reminder") || !strings.Contains(got, "• Check events") {
			t.Fatalf("missing choices: %q", got)
		}
		// The question must not be duplicated when the assistant already spoke.
		if strings.Count(got, "Which would you like?") != 1 {
			t.Fatalf("question should not be duplicated: %q", got)
		}
	})

	t.Run("AskUserQuestion without message includes question and choices", func(t *testing.T) {
		items := []agentsdk.RunItem{
			toolCallItem("AskUserQuestion", map[string]any{
				"question": "Pick a database:",
				"choices":  []string{"Postgres", "MySQL"},
			}),
		}
		got := replyFromTurnItems(items)
		if !strings.Contains(got, "Pick a database:") {
			t.Fatalf("missing question: %q", got)
		}
		if !strings.Contains(got, "• Postgres") || !strings.Contains(got, "• MySQL") {
			t.Fatalf("missing choices: %q", got)
		}
	})

	t.Run("present_plan renders summary and action labels", func(t *testing.T) {
		items := []agentsdk.RunItem{
			toolCallItem("present_plan", map[string]any{
				"summary": "I'll refactor the auth module.",
				"actions": []map[string]any{
					{"id": "go", "label": "Proceed"},
					{"id": "edit", "label": "Adjust the plan"},
				},
			}),
		}
		got := replyFromTurnItems(items)
		if !strings.Contains(got, "I'll refactor the auth module.") {
			t.Fatalf("missing summary: %q", got)
		}
		if !strings.Contains(got, "• Proceed") || !strings.Contains(got, "• Adjust the plan") {
			t.Fatalf("missing action labels: %q", got)
		}
	})
}
