// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func scheduleTools(cfg appConfig) []agentsdk.Tool {
	base := scheduleToolBase{cfg: cfg}
	return []agentsdk.Tool{
		&scheduleCreateTool{scheduleToolBase: base},
		&scheduleListTool{scheduleToolBase: base},
		&scheduleGetTool{scheduleToolBase: base},
		&scheduleUpdateTool{scheduleToolBase: base},
		&scheduleDeleteTool{scheduleToolBase: base},
		&scheduleRunTool{scheduleToolBase: base},
	}
}

type scheduleToolBase struct {
	cfg appConfig
}

func (t scheduleToolBase) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t scheduleToolBase) NeedsApproval() bool                 { return false }
func (t scheduleToolBase) TimeoutSeconds() int                 { return 0 }

type scheduleCreateTool struct{ scheduleToolBase }

func (t *scheduleCreateTool) Name() string { return "schedule_create" }
func (t *scheduleCreateTool) Description() string {
	return "Create a durable scheduled assistant prompt. Use only when the user asks for a reminder, recurring cron, or scheduled follow-up."
}
func (t *scheduleCreateTool) IsReadOnly() bool { return false }
func (t *scheduleCreateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"prompt": {"type": "string", "description": "The prompt the assistant should run when the schedule fires."},
			"cron": {"type": "string", "description": "Standard five-field cron expression, e.g. '0 9 * * MON-FRI'."},
			"every_seconds": {"type": "integer", "minimum": 10, "description": "Fixed interval in seconds."},
			"run_at": {"type": "string", "description": "One-time run time as RFC3339 or 'YYYY-MM-DD HH:MM'."},
			"timezone": {"type": "string", "description": "IANA timezone, e.g. 'America/New_York'. Defaults to local time."},
			"enabled": {"type": "boolean"}
		},
		"required": ["prompt"]
	}`)
}
func (t *scheduleCreateTool) Execute(_ context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in scheduleCreateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	entry, err := createSchedule(t.cfg, in)
	return scheduleToolResult(entry, err), nil
}

type scheduleListTool struct{ scheduleToolBase }

func (t *scheduleListTool) Name() string { return "schedule_list" }
func (t *scheduleListTool) Description() string {
	return "List durable scheduled assistant prompts, including next run, last run, and last error."
}
func (t *scheduleListTool) IsReadOnly() bool { return true }
func (t *scheduleListTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"include_disabled":{"type":"boolean"}}}`)
}
func (t *scheduleListTool) Execute(_ context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		IncludeDisabled bool `json:"include_disabled"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
		}
	}
	schedules, err := listSchedules(t.cfg)
	if err != nil {
		return scheduleToolResult(nil, err), nil
	}
	if !in.IncludeDisabled {
		filtered := schedules[:0]
		for _, entry := range schedules {
			if entry.Enabled {
				filtered = append(filtered, entry)
			}
		}
		schedules = filtered
	}
	return scheduleToolResult(schedules, nil), nil
}

type scheduleGetTool struct{ scheduleToolBase }

func (t *scheduleGetTool) Name() string { return "schedule_get" }
func (t *scheduleGetTool) Description() string {
	return "Show one durable scheduled assistant prompt by id."
}
func (t *scheduleGetTool) IsReadOnly() bool { return true }
func (t *scheduleGetTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
}
func (t *scheduleGetTool) Execute(_ context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	entry, ok, err := getSchedule(t.cfg, in.ID)
	if err != nil {
		return scheduleToolResult(nil, err), nil
	}
	if !ok {
		return agentsdk.ToolResult{Content: fmt.Sprintf("schedule %q not found", in.ID), IsError: true}, nil
	}
	return scheduleToolResult(entry, nil), nil
}

type scheduleUpdateTool struct{ scheduleToolBase }

func (t *scheduleUpdateTool) Name() string { return "schedule_update" }
func (t *scheduleUpdateTool) Description() string {
	return "Update a durable scheduled assistant prompt."
}
func (t *scheduleUpdateTool) IsReadOnly() bool { return false }
func (t *scheduleUpdateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"name": {"type": "string"},
			"prompt": {"type": "string"},
			"cron": {"type": "string"},
			"every_seconds": {"type": "integer", "minimum": 10},
			"run_at": {"type": "string"},
			"timezone": {"type": "string"},
			"enabled": {"type": "boolean"}
		},
		"required": ["id"]
	}`)
}
func (t *scheduleUpdateTool) Execute(_ context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in scheduleUpdateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	entry, err := updateSchedule(t.cfg, in)
	return scheduleToolResult(entry, err), nil
}

type scheduleDeleteTool struct{ scheduleToolBase }

func (t *scheduleDeleteTool) Name() string { return "schedule_delete" }
func (t *scheduleDeleteTool) Description() string {
	return "Delete a durable scheduled assistant prompt by id."
}
func (t *scheduleDeleteTool) IsReadOnly() bool { return false }
func (t *scheduleDeleteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
}
func (t *scheduleDeleteTool) Execute(_ context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	entry, err := deleteSchedule(t.cfg, in.ID)
	return scheduleToolResult(entry, err), nil
}

type scheduleRunTool struct{ scheduleToolBase }

func (t *scheduleRunTool) Name() string { return "schedule_run" }
func (t *scheduleRunTool) Description() string {
	return "Run an existing durable scheduled assistant prompt immediately by id or exact name without changing its next scheduled run."
}
func (t *scheduleRunTool) IsReadOnly() bool { return false }
func (t *scheduleRunTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Schedule id to run."},
			"name": {"type": "string", "description": "Exact schedule name to run when id is not known. Use id if names are duplicated."},
			"allow_disabled": {"type": "boolean", "description": "Set true to run a disabled schedule manually."}
		}
	}`)
}
func (t *scheduleRunTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in scheduleRunInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	result, err := runScheduleNow(ctx, t.cfg, in, nil, nil)
	return scheduleToolResult(result, err), nil
}

func scheduleToolResult(value any, err error) agentsdk.ToolResult {
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}
	}
	text, err := scheduleJSON(value)
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}
	}
	return agentsdk.ToolResult{Content: text}
}
