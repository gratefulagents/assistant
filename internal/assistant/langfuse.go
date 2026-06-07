// SPDX-License-Identifier: GPL-3.0-only

//go:build langfuse

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

const (
	// Per-field caps mirror the transcript log so Langfuse payloads stay
	// bounded while still capturing the substance of each turn.
	langfuseMaxTextChars = 8000
	langfuseMaxJSONChars = 4000
	// Langfuse legacy ingestion batches are limited to ~3.5MB. Stay well under
	// so a single verbose turn can never get the whole batch rejected.
	langfuseMaxPayloadBytes = 3 << 20
)

// langfuseClient posts usage observations to a Langfuse instance for fleet-wide
// cost and observability dashboards. It is intentionally minimal and used only
// for observability — never on the quota enforcement path.
type langfuseClient struct {
	host       string
	publicKey  string
	secretKey  string
	httpClient *http.Client
}

func newLangfuseClient(cfg appConfig) (*langfuseClient, bool) {
	if !cfg.LangfuseEnabled {
		return nil, false
	}
	host := strings.TrimRight(strings.TrimSpace(cfg.LangfuseHost), "/")
	if host == "" || strings.TrimSpace(cfg.LangfusePublicKey) == "" || strings.TrimSpace(cfg.LangfuseSecretKey) == "" {
		return nil, false
	}
	return &langfuseClient{
		host:       host,
		publicKey:  strings.TrimSpace(cfg.LangfusePublicKey),
		secretKey:  strings.TrimSpace(cfg.LangfuseSecretKey),
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}, true
}

// langfuseExporter is overridable in tests; defaults to the real HTTP client.
var langfuseExporter = func(cfg appConfig, payload langfuseIngestion) {
	client, ok := newLangfuseClient(cfg)
	if !ok {
		return
	}
	if err := client.send(context.Background(), payload); err != nil {
		fmt.Fprintln(os.Stderr, "[langfuse] export warning:", err)
	}
}

// langfuseTurn carries everything captured about one completed assistant turn
// so the exporter can write the fullest possible observation to Langfuse.
type langfuseTurn struct {
	cfg       appConfig
	startTime time.Time
	endTime   time.Time
	usage     agentsdk.Usage
	meta      transcriptContext
	prompt    string
	finalText string
	items     []agentsdk.RunItem
}

// emitLangfuseTurn fires a best-effort, asynchronous Langfuse export for one
// completed turn. It is a no-op when Langfuse is disabled. The full payload is
// built synchronously so the goroutine never holds the caller's RunItem slice.
func emitLangfuseTurn(t langfuseTurn) {
	if !t.cfg.LangfuseEnabled {
		return
	}
	payload := buildLangfusePayload(t)
	go langfuseExporter(t.cfg, payload)
}

// buildLangfusePayload renders a turn into a Langfuse ingestion batch: one
// trace (the turn, with input/output and session grouping), one generation
// (the model call, with usage and the full message list), and one span per
// tool call (with its paired output). All free text is redacted and capped,
// and the whole batch is trimmed if it would exceed the ingestion size budget.
func buildLangfusePayload(t langfuseTurn) langfuseIngestion {
	cfg := t.cfg
	traceID := randomHex(16)
	genID := randomHex(16)
	channel := strings.TrimSpace(t.meta.Channel)
	if channel == "" {
		channel = "direct"
	}
	startISO := t.startTime.UTC().Format(time.RFC3339Nano)
	endISO := t.endTime.UTC().Format(time.RFC3339Nano)
	total := t.usage.InputTokens + t.usage.OutputTokens

	messages, toolSpans, toolErrors := langfuseTurnItems(t.items, traceID, startISO, endISO)

	meta := map[string]any{
		"channel":  channel,
		"requests": t.usage.Requests,
	}
	if v := strings.TrimSpace(t.meta.ConversationID); v != "" {
		meta["conversation_id"] = v
	}
	if v := strings.TrimSpace(cfg.ActivePhase); v != "" {
		meta["phase"] = v
	}
	if v := strings.TrimSpace(cfg.ActiveMode); v != "" {
		meta["mode"] = v
	}
	if v := strings.TrimSpace(cfg.Provider); v != "" {
		meta["provider"] = v
	}
	if toolErrors > 0 {
		meta["tool_errors"] = toolErrors
	}

	tags := []string{"channel:" + channel}
	if v := strings.TrimSpace(cfg.Model); v != "" {
		tags = append(tags, "model:"+v)
	}
	if v := strings.TrimSpace(cfg.ActiveMode); v != "" {
		tags = append(tags, "mode:"+v)
	}

	traceBody := map[string]any{
		"id":        traceID,
		"name":      "assistant-turn",
		"userId":    cfg.UserID,
		"timestamp": startISO,
		"metadata":  meta,
		"tags":      tags,
	}
	if v := strings.TrimSpace(t.meta.SessionID); v != "" {
		traceBody["sessionId"] = v
	}
	if s := transcriptText(t.prompt, langfuseMaxTextChars); s != "" {
		traceBody["input"] = s
	}
	if s := transcriptText(t.finalText, langfuseMaxTextChars); s != "" {
		traceBody["output"] = s
	}

	usageDetails := map[string]any{
		"input":        t.usage.InputTokens,
		"output":       t.usage.OutputTokens,
		"total":        total,
		"cache_read":   t.usage.CacheReadTokens,
		"cache_create": t.usage.CacheCreateTokens,
	}
	modelParams := map[string]any{}
	if v := strings.TrimSpace(cfg.Reasoning); v != "" {
		modelParams["reasoning"] = v
	}
	if v := strings.TrimSpace(cfg.Verbosity); v != "" {
		modelParams["verbosity"] = v
	}
	if cfg.MaxTokens > 0 {
		modelParams["max_tokens"] = cfg.MaxTokens
	}

	genBody := map[string]any{
		"id":        genID,
		"traceId":   traceID,
		"name":      "assistant-generation",
		"userId":    cfg.UserID,
		"model":     cfg.Model,
		"startTime": startISO,
		"endTime":   endISO,
		"usage": map[string]any{
			"input":  t.usage.InputTokens,
			"output": t.usage.OutputTokens,
			"total":  total,
			"unit":   "TOKENS",
		},
		"usageDetails": usageDetails,
		"metadata":     meta,
	}
	if len(modelParams) > 0 {
		genBody["modelParameters"] = modelParams
	}
	if len(messages) > 0 {
		genBody["input"] = messages
	}
	if s := transcriptText(t.finalText, langfuseMaxTextChars); s != "" {
		genBody["output"] = s
	}

	batch := []langfuseEvent{
		{ID: uuid.NewString(), Type: "trace-create", Timestamp: endISO, Body: traceBody},
		{ID: uuid.NewString(), Type: "generation-create", Timestamp: endISO, Body: genBody},
	}
	batch = append(batch, toolSpans...)

	// Enforce a total-size budget. Shed the most verbose, lowest-value content
	// first (tool spans, then the message list, then the duplicated trace I/O)
	// so the high-value usage skeleton always survives.
	if langfusePayloadTooLarge(batch) {
		batch = batch[:2]
		if langfusePayloadTooLarge(batch) {
			delete(genBody, "input")
			if langfusePayloadTooLarge(batch) {
				delete(traceBody, "input")
				delete(traceBody, "output")
				delete(genBody, "output")
			}
		}
	}
	return langfuseIngestion{Batch: batch}
}

// langfuseTurnItems renders a turn's RunItems into a Langfuse message list and
// a tool span per tool call (paired with its output by CallID). Tool spans are
// children of the trace, not the generation, because tools run outside the
// model call; only the individual span is flagged ERROR on a tool failure.
func langfuseTurnItems(items []agentsdk.RunItem, traceID, startISO, endISO string) (messages []map[string]any, spans []langfuseEvent, toolErrors int) {
	outputs := map[string]agentsdk.ToolOutputData{}
	for _, item := range items {
		if item.Type == agentsdk.RunItemToolOutput && item.ToolOutput != nil {
			outputs[item.ToolOutput.CallID] = *item.ToolOutput
		}
	}
	for _, item := range items {
		switch item.Type {
		case agentsdk.RunItemMessage:
			if item.Message == nil {
				continue
			}
			role := "assistant"
			if item.Agent == nil {
				role = "user"
			}
			msg := map[string]any{"role": role, "content": transcriptText(item.Message.Text, langfuseMaxTextChars)}
			if n := len(item.Message.Images); n > 0 {
				msg["images"] = n
			}
			messages = append(messages, msg)
		case agentsdk.RunItemToolCall:
			if item.ToolCall == nil {
				continue
			}
			input := transcriptText(string(item.ToolCall.Input), langfuseMaxJSONChars)
			messages = append(messages, map[string]any{
				"role":    "tool_call",
				"name":    item.ToolCall.Name,
				"call_id": item.ToolCall.ID,
				"input":   input,
			})
			span := map[string]any{
				"id":        randomHex(16),
				"traceId":   traceID,
				"name":      "tool:" + item.ToolCall.Name,
				"startTime": startISO,
				"endTime":   endISO,
				"input":     input,
				// We only have turn-level timing, so the span duration is the
				// whole turn rather than the real tool latency.
				"metadata": map[string]any{"timing": "approximate"},
			}
			if out, ok := outputs[item.ToolCall.ID]; ok {
				span["output"] = transcriptText(out.Content, langfuseMaxTextChars)
				if out.IsError {
					span["level"] = "ERROR"
					span["statusMessage"] = "tool returned an error"
					toolErrors++
				}
			}
			spans = append(spans, langfuseEvent{ID: uuid.NewString(), Type: "span-create", Timestamp: endISO, Body: span})
		case agentsdk.RunItemToolOutput:
			if item.ToolOutput == nil {
				continue
			}
			messages = append(messages, map[string]any{
				"role":     "tool_output",
				"call_id":  item.ToolOutput.CallID,
				"content":  transcriptText(item.ToolOutput.Content, langfuseMaxTextChars),
				"is_error": item.ToolOutput.IsError,
			})
		case agentsdk.RunItemToolApproval:
			if item.ToolApproval == nil {
				continue
			}
			messages = append(messages, map[string]any{
				"role":    "tool_approval",
				"name":    item.ToolApproval.ToolName,
				"call_id": item.ToolApproval.CallID,
				"input":   transcriptText(string(item.ToolApproval.Input), langfuseMaxJSONChars),
			})
		case agentsdk.RunItemReasoning:
			if item.Reasoning == nil {
				continue
			}
			if s := transcriptText(item.Reasoning.Text, langfuseMaxTextChars); s != "" {
				messages = append(messages, map[string]any{"role": "reasoning", "content": s})
			}
		case agentsdk.RunItemHandoffCall:
			if item.HandoffCall == nil {
				continue
			}
			messages = append(messages, map[string]any{"role": "handoff", "content": item.HandoffCall.FromAgent + " -> " + item.HandoffCall.ToAgent})
		case agentsdk.RunItemHandoffOutput:
			if item.HandoffOutput == nil {
				continue
			}
			messages = append(messages, map[string]any{"role": "handoff_output", "content": item.HandoffOutput.FromAgent + " -> " + item.HandoffOutput.ToAgent})
		}
	}
	return messages, spans, toolErrors
}

// langfusePayloadTooLarge reports whether the serialized batch would exceed the
// ingestion size budget.
func langfusePayloadTooLarge(batch []langfuseEvent) bool {
	data, err := json.Marshal(langfuseIngestion{Batch: batch})
	if err != nil {
		return true
	}
	return len(data) > langfuseMaxPayloadBytes
}

type langfuseIngestion struct {
	Batch []langfuseEvent `json:"batch"`
}

type langfuseEvent struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Body      map[string]any `json:"body"`
}

func (c *langfuseClient) send(ctx context.Context, payload langfuseIngestion) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/api/public/ingestion", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	auth := base64.StdEncoding.EncodeToString([]byte(c.publicKey + ":" + c.secretKey))
	req.Header.Set("Authorization", "Basic "+auth)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("langfuse ingestion: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("langfuse ingestion: %s: %s", resp.Status, firstLine(string(body)))
	}
	// 207 Multi-Status carries per-event errors in an "errors" array even
	// though the request itself succeeded; surface them so schema mistakes
	// aren't silently dropped.
	if resp.StatusCode == http.StatusMultiStatus {
		var parsed struct {
			Errors []struct {
				ID      string `json:"id"`
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if json.Unmarshal(body, &parsed) == nil && len(parsed.Errors) > 0 {
			return fmt.Errorf("langfuse ingestion: %d event(s) rejected (first: %s)", len(parsed.Errors), strings.TrimSpace(parsed.Errors[0].Message))
		}
	}
	return nil
}
