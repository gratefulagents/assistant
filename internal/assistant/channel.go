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

func replyToInbound(ctx context.Context, cfg appConfig, msg inboundMessage, stdout, stderr io.Writer, conversations *conversationStore) (string, error) {
	return replyToInboundWithApproval(ctx, cfg, msg, stdout, stderr, conversations, nil)
}

func replyToInboundWithApproval(ctx context.Context, cfg appConfig, msg inboundMessage, stdout, stderr io.Writer, conversations *conversationStore, approvals approvalRequester) (string, error) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return "", errors.New("empty message")
	}
	session := conversations.sessionFor(msg)
	if command := handleSlashCommand(text, session, false); command.Handled {
		return command.Reply, nil
	}
	prompt := inboundPrompt(msg, text)
	meta := transcriptContextForInbound(msg, session, text)
	return runPromptTextWithSessionApprovalMeta(ctx, cfg, prompt, stdout, stderr, session, approvals, meta)
}

func inboundPrompt(msg inboundMessage, text string) string {
	prompt := "Incoming " + msg.Channel + " message"
	if msg.UserID != "" {
		prompt += " from " + msg.UserID
	}
	if msg.Thread != "" {
		prompt += " in thread " + msg.Thread
	}
	prompt += "."
	if strings.EqualFold(msg.Channel, "telegram") {
		if msg.Thread != "" {
			prompt += "\n\nTelegram chat_id for this conversation: " + msg.Thread + ". Use this value as deliver.chat_id when creating schedules that should send completed output back to this chat."
		}
		prompt += "\n\n" + telegramReplyFormattingInstructions()
	}
	prompt += "\n\nMessage:\n\n" + text
	return prompt
}

func runPromptText(ctx context.Context, cfg appConfig, prompt string, stdout, stderr io.Writer) (string, error) {
	return runPromptTextWithSession(ctx, cfg, prompt, stdout, stderr, nil)
}

func runPromptTextWithSession(ctx context.Context, cfg appConfig, prompt string, stdout, stderr io.Writer, session *conversationSession) (string, error) {
	return runPromptTextWithSessionApproval(ctx, cfg, prompt, stdout, stderr, session, nil)
}

func runPromptTextWithSessionApproval(ctx context.Context, cfg appConfig, prompt string, stdout, stderr io.Writer, session *conversationSession, approvals approvalRequester) (string, error) {
	return runPromptTextWithSessionApprovalMeta(ctx, cfg, prompt, stdout, stderr, session, approvals, transcriptContext{})
}

func runPromptTextWithSessionApprovalMeta(ctx context.Context, cfg appConfig, prompt string, stdout, stderr io.Writer, session *conversationSession, approvals approvalRequester, meta transcriptContext) (string, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	started := time.Now().UTC()
	if session != nil {
		session.mu.Lock()
		defer session.mu.Unlock()
		cfg = applyConversationMode(cfg, session.currentModeLocked())
		if strings.TrimSpace(meta.SessionID) == "" {
			meta.SessionID = session.transcriptID
		}
	} else {
		cfg = applyConversationMode(cfg, conversationModeChat)
	}
	if strings.TrimSpace(meta.Channel) == "" {
		meta.Channel = "direct"
	}
	if strings.TrimSpace(meta.UserText) == "" {
		meta.UserText = prompt
	}
	audit, err := newAuditRecorder(cfg, stdout)
	if err != nil {
		return "", err
	}
	defer func() { _ = audit.Close() }()
	audit.EmitRunStart(cfg, prompt)

	input := []agentsdk.RunItem(nil)
	if session != nil {
		input = append(input, session.history...)
	}
	userItem := userMessage(prompt)
	input = append(input, userItem)
	turnItems := []agentsdk.RunItem{userItem}

	items := input
	approvals = approvalRequesterForConfig(cfg, approvals, stderr, audit)
	for resumes := 0; ; resumes++ {
		if resumes > 12 {
			err := errors.New("too many approval resumes")
			audit.EmitRunError(err)
			return "", err
		}
		bundle, err := buildBundle(ctx, cfg, stderr, audit)
		if err != nil {
			audit.EmitRunError(err)
			return "", err
		}
		result, err := bundle.Runner.Run(ctx, bundle.Agent, cloneRunItems(items), bundle.Config)
		if err != nil {
			closeBundle(bundle, stderr)
			audit.EmitRunError(err)
			return "", err
		}
		if result == nil {
			closeBundle(bundle, stderr)
			err := errors.New("runner returned no result")
			audit.EmitRunError(err)
			return "", err
		}
		for i := range result.NewItems {
			audit.EmitRunItem(&result.NewItems[i])
		}
		newItems := cloneRunItems(result.NewItems)
		items = append(items, newItems...)
		turnItems = append(turnItems, newItems...)
		if result.Interruption == nil {
			closeBundle(bundle, stderr)
			if session != nil {
				session.history = items
			}
			audit.EmitRunEnd(result)
			finalText := strings.TrimSpace(result.FinalText())
			if err := recordTranscriptTurn(ctx, cfg, meta, prompt, cfg.ActivePhase, started, turnItems, finalText); err != nil {
				fmt.Fprintln(stderr, "[log] transcript warning:", err)
			}
			triggerAfterTurnMemoryReview(ctx, cfg, started, stderr, true)
			return finalText, nil
		}
		audit.EmitApprovalRequest(result.Interruption)
		if approvals == nil {
			closeBundle(bundle, stderr)
			err := fmt.Errorf("tool %q requires approval; channel mode cannot prompt", result.Interruption.ToolName)
			audit.EmitRunError(err)
			return "", err
		}
		approvalItems, err := resolveApprovalWithRequester(ctx, bundle, result.Interruption, approvals, approvalRequestContext{
			Items: cloneRunItems(items),
			Mode:  cfg.ActiveMode,
		}, stderr, audit)
		closeBundle(bundle, stderr)
		if err != nil {
			audit.EmitRunError(err)
			return "", err
		}
		for i := range approvalItems {
			audit.EmitRunItem(&approvalItems[i])
		}
		items = append(items, approvalItems...)
		turnItems = append(turnItems, cloneRunItems(approvalItems)...)
	}
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
