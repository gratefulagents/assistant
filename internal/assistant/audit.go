// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

type auditRecorder struct {
	runID  string
	level  string
	stdout io.Writer
	file   *os.File
	mu     sync.Mutex
}

const (
	auditLevelLow  = "low"
	auditLevelFull = "full"
)

type auditRedactor struct {
	re   *regexp.Regexp
	repl string
}

var auditRedactors = []auditRedactor{
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)(bot)[0-9]+:[A-Za-z0-9_\-]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|api_key|authorization|password|secret|token)"\s*:\s*")[^"]+(")`), `${1}[REDACTED]${2}`},
	{regexp.MustCompile(`(?i)(\\"(?:access_token|refresh_token|id_token|api_key|authorization|password|secret|token)\\"\s*:\s*\\")[^\\"]+(\\")`), `${1}[REDACTED]${2}`},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+`), `[REDACTED]`},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]+\b`), `[REDACTED]`},
}

func newAuditRecorder(cfg appConfig, stdout io.Writer) (*auditRecorder, error) {
	if !cfg.Audit {
		return nil, nil
	}
	if stdout == nil {
		stdout = io.Discard
	}
	path := strings.TrimSpace(cfg.AuditLogPath)
	if path == "" {
		path = stateFilePath(cfg, "audit.ndjson")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create audit log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &auditRecorder{
		runID:  newAuditRunID(),
		level:  normalizeAuditLevel(cfg.AuditLevel),
		stdout: stdout,
		file:   file,
	}, nil
}

func (a *auditRecorder) Close() error {
	if a == nil || a.file == nil {
		return nil
	}
	return a.file.Close()
}

func (a *auditRecorder) EmitRunStart(cfg appConfig, prompt string) {
	if a == nil {
		return
	}
	a.emit("run_start", map[string]any{
		"command":            cfg.Command,
		"provider":           cfg.Provider,
		"model":              cfg.Model,
		"workdir":            cfg.WorkDir,
		"permission":         cfg.Permission,
		"tools":              cfg.EnableTools,
		"mcp":                cfg.EnableMCP,
		"skills":             cfg.EnableSkills,
		"scheduling":         cfg.EnableScheduling,
		"prompt":             prompt,
		"audit_log":          cfg.AuditLogPath,
		"state_dir":          cfg.StateDir,
		"max_turns":          cfg.MaxTurns,
		"max_tokens":         cfg.MaxTokens,
		"approval":           cfg.EnableApproval,
		"approvals_reviewer": cfg.ApprovalsReviewer,
		"guardrails":         cfg.EnableGuardrails,
		"compaction":         cfg.EnableCompaction,
	})
}

func (a *auditRecorder) EmitRunEnd(result *agentsdk.RunResult) {
	if a == nil {
		return
	}
	if a.level == auditLevelLow {
		if result != nil && !hasAssistantMessage(result.NewItems) && strings.TrimSpace(result.FinalText()) != "" {
			a.emit("assistant_message", map[string]any{
				"item_type": "message",
				"text":      result.FinalText(),
			})
		}
		return
	}
	fields := map[string]any{"status": "ok"}
	if result != nil {
		fields["final_text"] = result.FinalText()
		fields["usage"] = result.Usage
		if result.LastAgent != nil {
			fields["last_agent"] = result.LastAgent.Name
		}
		fields["new_items"] = len(result.NewItems)
		fields["raw_responses"] = len(result.RawResponses)
	}
	a.emit("run_end", fields)
}

func (a *auditRecorder) EmitRunError(err error) {
	if a == nil || err == nil {
		return
	}
	a.emit("run_error", map[string]any{"error": err.Error()})
}

func (a *auditRecorder) EmitApprovalRequest(pending *agentsdk.Interruption) {
	if a == nil || pending == nil {
		return
	}
	a.emit("approval_request", map[string]any{
		"tool":    pending.ToolName,
		"call_id": pending.ToolCallID,
		"input":   auditRawJSON(pending.ToolInput),
	})
}

func (a *auditRecorder) EmitApprovalDecision(tool string, input json.RawMessage, approved bool) {
	if a == nil {
		return
	}
	a.emit("approval_decision", map[string]any{
		"tool":     tool,
		"input":    auditRawJSON(input),
		"approved": approved,
	})
}

func (a *auditRecorder) EmitApprovalReview(tool string, input json.RawMessage, outcome, riskLevel, userAuthorization, rationale, status string) {
	if a == nil {
		return
	}
	fields := map[string]any{
		"tool":   tool,
		"input":  auditRawJSON(input),
		"status": strings.TrimSpace(status),
	}
	if strings.TrimSpace(outcome) != "" {
		fields["outcome"] = strings.TrimSpace(outcome)
	}
	if strings.TrimSpace(riskLevel) != "" {
		fields["risk_level"] = strings.TrimSpace(riskLevel)
	}
	if strings.TrimSpace(userAuthorization) != "" {
		fields["user_authorization"] = strings.TrimSpace(userAuthorization)
	}
	if strings.TrimSpace(rationale) != "" {
		fields["rationale"] = strings.TrimSpace(rationale)
	}
	a.emit("approval_review", fields)
}

func (a *auditRecorder) EmitRunItem(item *agentsdk.RunItem) {
	if a == nil || item == nil {
		return
	}
	fields := map[string]any{"item_type": runItemTypeName(item.Type)}
	if item.Agent != nil {
		fields["agent"] = item.Agent.Name
	}
	switch item.Type {
	case agentsdk.RunItemMessage:
		if item.Message != nil {
			fields["text"] = item.Message.Text
			fields["images"] = len(item.Message.Images)
		}
		a.emit("assistant_message", fields)
	case agentsdk.RunItemToolCall:
		if item.ToolCall != nil {
			fields["call_id"] = item.ToolCall.ID
			fields["tool"] = item.ToolCall.Name
			fields["input"] = auditRawJSON(item.ToolCall.Input)
		}
		a.emit("tool_call", fields)
	case agentsdk.RunItemToolOutput:
		if item.ToolOutput != nil {
			if item.ToolOutput.IsError {
				a.emit("tool_error", map[string]any{
					"item_type": "tool_output",
					"call_id":   item.ToolOutput.CallID,
					"content":   item.ToolOutput.Content,
					"is_error":  true,
				})
				if a.level == auditLevelLow {
					return
				}
			}
			fields["call_id"] = item.ToolOutput.CallID
			fields["content"] = item.ToolOutput.Content
			fields["is_error"] = item.ToolOutput.IsError
		}
		a.emit("tool_output", fields)
	case agentsdk.RunItemHandoffCall:
		if item.HandoffCall != nil {
			fields["from_agent"] = item.HandoffCall.FromAgent
			fields["to_agent"] = item.HandoffCall.ToAgent
		}
		a.emit("handoff_call", fields)
	case agentsdk.RunItemHandoffOutput:
		if item.HandoffOutput != nil {
			fields["from_agent"] = item.HandoffOutput.FromAgent
			fields["to_agent"] = item.HandoffOutput.ToAgent
		}
		a.emit("handoff_output", fields)
	case agentsdk.RunItemReasoning:
		if item.Reasoning != nil {
			fields["id"] = item.Reasoning.ID
			fields["text"] = item.Reasoning.Text
			fields["has_signature"] = item.Reasoning.Signature != ""
			fields["has_redacted_data"] = item.Reasoning.RedactedData != ""
			fields["has_encrypted_content"] = item.Reasoning.EncryptedContent != ""
		}
		a.emit("reasoning", fields)
	case agentsdk.RunItemToolApproval:
		if item.ToolApproval != nil {
			fields["tool"] = item.ToolApproval.ToolName
			fields["call_id"] = item.ToolApproval.CallID
			fields["approved"] = item.ToolApproval.Approved
			fields["input"] = auditRawJSON(item.ToolApproval.Input)
		}
		a.emit("tool_approval", fields)
	case agentsdk.RunItemCompaction:
		if item.Compaction != nil {
			fields["id"] = item.Compaction.ID
			fields["created_by"] = item.Compaction.CreatedBy
			fields["has_encrypted_content"] = item.Compaction.EncryptedContent != ""
		}
		a.emit("compaction", fields)
	default:
		a.emit("run_item", fields)
	}
}

func (a *auditRecorder) EmitContentEvent(ev *agentsdk.ContentEvent) {
	if a == nil || ev == nil {
		return
	}
	a.emit("content_event", map[string]any{
		"name":    ev.Type,
		"content": auditJSONValue(ev),
	})
}

func (a *auditRecorder) EmitAgentUpdated(agent *agentsdk.Agent) {
	if a == nil || agent == nil {
		return
	}
	a.emit("agent_updated", map[string]any{
		"agent": agent.Name,
		"model": agent.Model,
	})
}

func (a *auditRecorder) OnTraceStart(trace *agentsdk.Trace) {
	if a == nil || trace == nil {
		return
	}
	a.emit("trace_start", map[string]any{
		"trace_id": trace.ID,
		"name":     trace.Name,
	})
}

func (a *auditRecorder) OnTraceEnd(trace *agentsdk.Trace) {
	if a == nil || trace == nil {
		return
	}
	a.emit("trace_end", map[string]any{
		"trace_id":    trace.ID,
		"name":        trace.Name,
		"duration_ms": trace.EndTime.Sub(trace.StartTime).Milliseconds(),
		"spans":       len(trace.Spans),
	})
}

func (a *auditRecorder) OnSpanStart(span *agentsdk.Span) {
	if a == nil || span == nil {
		return
	}
	a.emit(spanAuditEvent(span, "start"), spanAuditFields(span, false))
}

func (a *auditRecorder) OnSpanEnd(span *agentsdk.Span) {
	if a == nil || span == nil {
		return
	}
	fields := spanAuditFields(span, true)
	fields["duration_ms"] = span.DurationMS()
	a.emit(spanAuditEvent(span, "end"), fields)
}

func (a *auditRecorder) Flush() {}

func (a *auditRecorder) emit(event string, fields map[string]any) {
	if a == nil {
		return
	}
	if !a.shouldEmit(event) {
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	fields["run_id"] = a.runID
	fields["event"] = event
	data, err := json.Marshal(fields)
	if err != nil {
		data, _ = json.Marshal(map[string]any{
			"time":   time.Now().UTC().Format(time.RFC3339Nano),
			"run_id": a.runID,
			"event":  "audit_error",
			"error":  err.Error(),
		})
	}
	data = []byte(redactAuditText(string(data)))

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdout != nil {
		fmt.Fprintf(a.stdout, "\n[audit] %s\n", data)
	}
	log.Printf("[audit] %s", data)
	if a.file != nil {
		_, _ = a.file.Write(append(data, '\n'))
	}
}

func (a *auditRecorder) shouldEmit(event string) bool {
	if a == nil {
		return false
	}
	if a.level == "" || a.level == auditLevelFull {
		return true
	}
	switch event {
	case "assistant_message", "tool_call", "tool_error", "run_error", "audit_error":
		return true
	default:
		return false
	}
}

func spanAuditEvent(span *agentsdk.Span, suffix string) string {
	switch span.Name {
	case "generation":
		if suffix == "start" {
			return "llm_start"
		}
		return "llm_end"
	case "function":
		if suffix == "start" {
			return "tool_start"
		}
		return "tool_end"
	case "handoff":
		if suffix == "start" {
			return "handoff_start"
		}
		return "handoff_end"
	default:
		return "span_" + suffix
	}
}

func spanAuditFields(span *agentsdk.Span, ended bool) map[string]any {
	fields := map[string]any{
		"span_id":   span.ID,
		"parent_id": span.ParentID,
		"name":      span.Name,
	}
	switch data := span.Data.(type) {
	case *agentsdk.GenerationSpanData:
		fields["attempt"] = data.AttemptNumber
		fields["turn"] = data.Turn
		fields["scope"] = data.Scope
		fields["task_id"] = data.TaskID
		fields["requested_model"] = data.RequestedModel
		fields["resolved_model"] = data.ResolvedModel
		fields["provider"] = data.ModelProvider
		fields["status"] = data.Status
		fields["tool_count"] = data.ToolCount
		fields["input_item_count"] = data.InputItemCount
		fields["output_item_count"] = data.OutputItemCount
		fields["request"] = auditJSONValue(data.Request)
		if ended {
			fields["success"] = data.Success
			fields["error"] = data.Error
			fields["latency_ms"] = data.LatencyMS
			fields["usage_available"] = data.UsageAvailable
			fields["input_tokens"] = data.PromptTokens
			fields["output_tokens"] = data.CompletionTokens
			fields["cache_read_tokens"] = data.CacheReadTokens
			fields["cache_create_tokens"] = data.CacheCreateTokens
			fields["total_tokens"] = data.TotalTokens
			fields["cost_usd"] = data.CostUSD
			fields["cost_known"] = data.CostKnown
			fields["response"] = auditJSONValue(data.Response)
		}
	case *agentsdk.FunctionSpanData:
		fields["tool"] = data.ToolName
		fields["input"] = data.Input
		if ended {
			fields["output"] = data.Output
			fields["is_error"] = data.IsError
		}
	case *agentsdk.HandoffSpanData:
		fields["from_agent"] = data.FromAgent
		fields["to_agent"] = data.ToAgent
	case *agentsdk.GuardrailSpanData:
		fields["guardrail"] = data.GuardrailName
		fields["triggered"] = data.Triggered
	case *agentsdk.SessionSpanData:
		fields["model"] = data.Model
		fields["cost_usd"] = data.CostUSD
		fields["turns"] = data.NumTurns
		fields["duration_ms"] = data.DurationMS
		fields["input_tokens"] = data.InputTokens
		fields["output_tokens"] = data.OutputTokens
		fields["stop_reason"] = data.StopReason
	case *agentsdk.SubagentSpanData:
		fields["task_id"] = data.TaskID
		fields["type"] = data.Type
		fields["description"] = data.Description
		fields["model"] = data.Model
		fields["status"] = data.Status
		fields["prompt"] = data.Prompt
		if ended {
			fields["result_text"] = data.ResultText
			fields["files_read"] = data.FilesRead
			fields["files_written"] = data.FilesWritten
			fields["tool_count"] = data.ToolCount
			fields["total_tokens"] = data.TotalTokens
			fields["stop_reason"] = data.StopReason
		}
	case *agentsdk.RetrySpanData:
		fields["error_code"] = data.ErrorCode
		fields["retry_after_ms"] = data.RetryAfterMS
		fields["attempt"] = data.Attempt
		fields["max_retries"] = data.MaxRetries
	case *agentsdk.CompactionSpanData:
		fields["tokens_before"] = data.TokensBefore
		fields["tokens_after"] = data.TokensAfter
	default:
		fields["data"] = auditJSONValue(span.Data)
	}
	return fields
}

func hasAssistantMessage(items []agentsdk.RunItem) bool {
	for _, item := range items {
		if item.Type == agentsdk.RunItemMessage && item.Message != nil && strings.TrimSpace(item.Message.Text) != "" {
			return true
		}
	}
	return false
}

func auditRawJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func normalizeAuditLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", auditLevelFull, "debug", "verbose":
		return auditLevelFull
	case auditLevelLow, "lowest", "minimal", "min":
		return auditLevelLow
	default:
		return ""
	}
}

func auditJSONValue(value any) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return string(data)
	}
	return out
}

func redactAuditText(text string) string {
	out := text
	for _, redactor := range auditRedactors {
		out = redactor.re.ReplaceAllString(out, redactor.repl)
	}
	return out
}

func runItemTypeName(t agentsdk.RunItemType) string {
	switch t {
	case agentsdk.RunItemMessage:
		return "message"
	case agentsdk.RunItemToolCall:
		return "tool_call"
	case agentsdk.RunItemToolOutput:
		return "tool_output"
	case agentsdk.RunItemHandoffCall:
		return "handoff_call"
	case agentsdk.RunItemHandoffOutput:
		return "handoff_output"
	case agentsdk.RunItemReasoning:
		return "reasoning"
	case agentsdk.RunItemToolApproval:
		return "tool_approval"
	case agentsdk.RunItemCompaction:
		return "compaction"
	default:
		return "unknown"
	}
}

func newAuditRunID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(buf[:])
}
