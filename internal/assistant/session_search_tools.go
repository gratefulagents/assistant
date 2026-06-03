// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func sessionSearchTools(cfg appConfig) []agentsdk.Tool {
	return []agentsdk.Tool{&sessionSearchTool{cfg: cfg}}
}

type sessionSearchTool struct {
	cfg appConfig
}

func (t *sessionSearchTool) Name() string { return "session_search" }
func (t *sessionSearchTool) Description() string {
	return "Search persisted assistant conversation transcripts. With no input, browse recent sessions. With query, search prior turns. With session_id, show turns in that session. With around_turn_id, scroll near a prior turn."
}
func (t *sessionSearchTool) IsReadOnly() bool                    { return true }
func (t *sessionSearchTool) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t *sessionSearchTool) NeedsApproval() bool                 { return false }
func (t *sessionSearchTool) TimeoutSeconds() int                 { return 0 }
func (t *sessionSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string","description":"Search text for prior conversation turns."},
			"session_id":{"type":"string","description":"Transcript session id to browse."},
			"around_turn_id":{"type":"string","description":"Turn id to scroll around, optionally constrained by session_id."},
			"limit":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum sessions or turns returned. Defaults to 10."},
			"window":{"type":"integer","minimum":1,"maximum":25,"description":"Turns before and after around_turn_id for scroll mode. Defaults to 5."},
			"include_items":{"type":"boolean","description":"Include compact message/tool items for each returned turn."}
		}
	}`)
}

func (t *sessionSearchTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in transcriptSearchInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
		}
	}
	result, err := searchTranscriptTurns(ctx, t.cfg, in)
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return agentsdk.ToolResult{Content: string(data)}, nil
}
