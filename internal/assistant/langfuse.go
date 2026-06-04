// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
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
	_ = client.send(context.Background(), payload)
}

// emitLangfuseUsage fires a best-effort, asynchronous Langfuse export for one
// completed turn. It is a no-op when Langfuse is disabled or unconfigured.
func emitLangfuseUsage(cfg appConfig, startTime, endTime time.Time, usage agentsdk.Usage, channel string) {
	if !cfg.LangfuseEnabled {
		return
	}
	traceID := randomHex(16)
	genID := randomHex(16)
	total := usage.InputTokens + usage.OutputTokens
	meta := map[string]any{"channel": channel, "requests": usage.Requests}
	usageDetails := map[string]any{
		"input":        usage.InputTokens,
		"output":       usage.OutputTokens,
		"total":        total,
		"cache_read":   usage.CacheReadTokens,
		"cache_create": usage.CacheCreateTokens,
	}
	payload := langfuseIngestion{Batch: []langfuseEvent{
		{
			ID:        randomHex(8),
			Type:      "trace-create",
			Timestamp: endTime.UTC().Format(time.RFC3339Nano),
			Body: map[string]any{
				"id":        traceID,
				"name":      "assistant-turn",
				"userId":    cfg.UserID,
				"timestamp": startTime.UTC().Format(time.RFC3339Nano),
				"metadata":  meta,
			},
		},
		{
			ID:        randomHex(8),
			Type:      "generation-create",
			Timestamp: endTime.UTC().Format(time.RFC3339Nano),
			Body: map[string]any{
				"id":        genID,
				"traceId":   traceID,
				"name":      "assistant-generation",
				"userId":    cfg.UserID,
				"model":     cfg.Model,
				"startTime": startTime.UTC().Format(time.RFC3339Nano),
				"endTime":   endTime.UTC().Format(time.RFC3339Nano),
				"usage": map[string]any{
					"input":  usage.InputTokens,
					"output": usage.OutputTokens,
					"total":  total,
					"unit":   "TOKENS",
				},
				"usageDetails": usageDetails,
				"metadata":     meta,
			},
		},
	}}
	go langfuseExporter(cfg, payload)
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("langfuse ingestion: %s", resp.Status)
	}
	return nil
}
