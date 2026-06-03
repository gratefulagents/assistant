// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func memoryReviewTools(cfg appConfig) []agentsdk.Tool {
	return []agentsdk.Tool{&memoryReviewTool{cfg: cfg}}
}

type memoryReviewTool struct {
	cfg appConfig
}

func (t *memoryReviewTool) Name() string { return "memory_review" }
func (t *memoryReviewTool) Description() string {
	return "Run an LLM-backed review of recent persisted transcripts for durable memory candidates. Defaults to preview. Use action=apply only when the user asks to save reviewed memories."
}
func (t *memoryReviewTool) IsReadOnly() bool                    { return false }
func (t *memoryReviewTool) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t *memoryReviewTool) NeedsApproval() bool                 { return false }
func (t *memoryReviewTool) TimeoutSeconds() int                 { return 0 }
func (t *memoryReviewTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"action":{"type":"string","enum":["preview","apply"],"description":"preview returns candidates; apply writes non-duplicate candidates to durable memory."},
			"since":{"type":"string","description":"RFC3339 lower bound for transcript turns. Overrides since_hours."},
			"since_hours":{"type":"integer","minimum":1,"maximum":8760,"description":"Look back this many hours. Defaults to 24."},
			"limit":{"type":"integer","minimum":1,"maximum":200,"description":"Maximum recent turns to send to the reviewer. Defaults to 80."},
			"min_confidence":{"type":"number","minimum":0,"maximum":1,"description":"Minimum candidate confidence. Defaults to 0.75."},
			"include_skipped":{"type":"boolean","description":"Include candidates skipped because matching memories already exist."},
			"include_heuristic":{"type":"boolean","description":"Also include deterministic memory_distill candidates and dedupe them with reviewer candidates."}
		}
	}`)
}

func (t *memoryReviewTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in memoryReviewInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
		}
	}
	result, err := reviewTranscriptMemories(ctx, t.cfg, in, io.Discard)
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return agentsdk.ToolResult{Content: string(data)}, nil
}
