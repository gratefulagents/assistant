// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func memoryDistillTools(cfg appConfig) []agentsdk.Tool {
	return []agentsdk.Tool{&memoryDistillTool{cfg: cfg}}
}

type memoryDistillTool struct {
	cfg appConfig
}

func (t *memoryDistillTool) Name() string { return "memory_distill" }
func (t *memoryDistillTool) Description() string {
	return "Review recent persisted transcripts for stable user preferences, facts, and routines. Defaults to preview. Use action=apply only when the user asks to save distilled memories."
}
func (t *memoryDistillTool) IsReadOnly() bool                    { return false }
func (t *memoryDistillTool) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t *memoryDistillTool) NeedsApproval() bool                 { return false }
func (t *memoryDistillTool) TimeoutSeconds() int                 { return 0 }
func (t *memoryDistillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"action":{"type":"string","enum":["preview","apply"],"description":"preview returns candidates; apply writes non-duplicate candidates to durable memory."},
			"since":{"type":"string","description":"RFC3339 lower bound for transcript turns. Overrides since_hours."},
			"since_hours":{"type":"integer","minimum":1,"maximum":8760,"description":"Look back this many hours. Defaults to 24."},
			"limit":{"type":"integer","minimum":1,"maximum":1000,"description":"Maximum recent turns to scan. Defaults to 200."},
			"min_confidence":{"type":"number","minimum":0,"maximum":1,"description":"Minimum candidate confidence. Defaults to 0.75."},
			"include_skipped":{"type":"boolean","description":"Include candidates skipped because matching memories already exist."}
		}
	}`)
}

func (t *memoryDistillTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in memoryDistillInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
		}
	}
	result, err := distillTranscriptMemories(ctx, t.cfg, in)
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return agentsdk.ToolResult{Content: string(data)}, nil
}
