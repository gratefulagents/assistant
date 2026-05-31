// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

var defaultHTTPClient = &http.Client{Timeout: 60 * time.Second}

type inboundMessage struct {
	Channel string
	UserID  string
	Thread  string
	Text    string
	Raw     json.RawMessage
}

func replyToInbound(ctx context.Context, cfg appConfig, msg inboundMessage, stdout, stderr io.Writer) (string, error) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return "", errors.New("empty message")
	}
	prompt := "Incoming " + msg.Channel + " message"
	if msg.UserID != "" {
		prompt += " from " + msg.UserID
	}
	if msg.Thread != "" {
		prompt += " in thread " + msg.Thread
	}
	prompt += "."
	if strings.EqualFold(msg.Channel, "telegram") {
		prompt += "\n\n" + telegramReplyFormattingInstructions()
	}
	prompt += "\n\nMessage:\n\n" + text
	return runPromptText(ctx, cfg, prompt, stdout, stderr)
}

func runPromptText(ctx context.Context, cfg appConfig, prompt string, stdout, stderr io.Writer) (string, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	audit, err := newAuditRecorder(cfg, stdout)
	if err != nil {
		return "", err
	}
	defer func() { _ = audit.Close() }()
	audit.EmitRunStart(cfg, prompt)

	input := []agentsdk.RunItem{userMessage(prompt)}
	bundle, err := buildBundle(ctx, cfg, stderr, audit)
	if err != nil {
		audit.EmitRunError(err)
		return "", err
	}
	defer closeBundle(bundle, stderr)
	result, err := bundle.Runner.Run(ctx, bundle.Agent, cloneRunItems(input), bundle.Config)
	if err != nil {
		audit.EmitRunError(err)
		return "", err
	}
	if result == nil {
		err := errors.New("runner returned no result")
		audit.EmitRunError(err)
		return "", err
	}
	for i := range result.NewItems {
		audit.EmitRunItem(&result.NewItems[i])
	}
	if result.Interruption != nil {
		audit.EmitApprovalRequest(result.Interruption)
		err := fmt.Errorf("tool %q requires approval; channel mode cannot prompt", result.Interruption.ToolName)
		audit.EmitRunError(err)
		return "", err
	}
	audit.EmitRunEnd(result)
	return strings.TrimSpace(result.FinalText()), nil
}

func postJSON(ctx context.Context, url, bearer string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(bearer) != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s: %s: %s", redactedEndpoint(url), resp.Status, string(data))
	}
	return nil
}

func redactedEndpoint(endpoint string) string {
	const marker = "/bot"
	idx := strings.Index(endpoint, marker)
	if idx < 0 {
		return endpoint
	}
	tokenStart := idx + len(marker)
	tokenEnd := strings.IndexByte(endpoint[tokenStart:], '/')
	if tokenEnd < 0 {
		return endpoint[:tokenStart] + "<redacted>"
	}
	tokenEnd += tokenStart
	return endpoint[:tokenStart] + "<redacted>" + endpoint[tokenEnd:]
}
