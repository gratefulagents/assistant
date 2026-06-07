// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	Images  []agentsdk.ImageAttachment
	Raw     json.RawMessage
}

func replyToInbound(ctx context.Context, cfg appConfig, msg inboundMessage, stdout, stderr io.Writer, conversations *conversationStore) (string, error) {
	return replyToInboundWithApproval(ctx, cfg, msg, stdout, stderr, conversations, nil)
}

func replyToInboundWithApproval(ctx context.Context, cfg appConfig, msg inboundMessage, stdout, stderr io.Writer, conversations *conversationStore, approvals approvalRequester) (string, error) {
	text := strings.TrimSpace(msg.Text)
	if text == "" && len(msg.Images) == 0 {
		return "", errors.New("empty message")
	}
	session := conversations.sessionFor(msg)
	if command := handleSlashCommand(text, session, false); command.Handled {
		return command.Reply, nil
	}
	prompt := inboundPrompt(msg, text)
	meta := transcriptContextForInbound(msg, session, text)
	return runPromptTextWithSessionApprovalMeta(ctx, cfg, prompt, msg.Images, stdout, stderr, session, approvals, meta)
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
	if len(msg.Images) > 0 {
		noun := "image"
		if len(msg.Images) > 1 {
			noun = "images"
		}
		prompt += fmt.Sprintf("\n\nThe user attached %d %s, included with this message.", len(msg.Images), noun)
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
	return runPromptTextWithSessionApprovalMeta(ctx, cfg, prompt, nil, stdout, stderr, session, approvals, transcriptContext{})
}

func runPromptTextWithSessionApprovalImages(ctx context.Context, cfg appConfig, prompt string, images []agentsdk.ImageAttachment, stdout, stderr io.Writer, session *conversationSession, approvals approvalRequester) (string, error) {
	return runPromptTextWithSessionApprovalMeta(ctx, cfg, prompt, images, stdout, stderr, session, approvals, transcriptContext{})
}

func runPromptTextWithSessionApprovalMeta(ctx context.Context, cfg appConfig, prompt string, images []agentsdk.ImageAttachment, stdout, stderr io.Writer, session *conversationSession, approvals approvalRequester, meta transcriptContext) (string, error) {
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
	defer func() {
		if closeErr := audit.Close(); closeErr != nil {
			log.Printf("[audit] close error: %v", closeErr)
		}
	}()
	audit.EmitRunStart(cfg, prompt)

	store, overMsg := checkAndStartUsage(cfg)
	if overMsg != "" {
		return overMsg, nil
	}
	var totalUsage agentsdk.Usage

	input := []agentsdk.RunItem(nil)
	if session != nil {
		input = append(input, session.history...)
	}
	userItem := userMessageWithImages(prompt, images)
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
		totalUsage.Add(result.Usage)
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
			if finalText == "" {
				// The run can end without a string FinalOutput when the model
				// pauses on a signal tool (AskUserQuestion / present_plan): it
				// emits an assistant message and a tool call but no "final
				// output". Channels are non-streaming, so without this fallback
				// the user would only see the generic "Done." placeholder
				// instead of the assistant's actual reply and question.
				finalText = strings.TrimSpace(replyFromTurnItems(newItems))
			}
			recordUsage(cfg, store, started, totalUsage, meta, prompt, finalText, turnItems, stderr)
			if err := recordTranscriptTurn(ctx, cfg, meta, prompt, cfg.ActivePhase, started, turnItems, finalText); err != nil {
				fmt.Fprintln(stderr, "[log] transcript warning:", err)
			}
			triggerAfterTurnMemoryReview(ctx, cfg, meta.Channel, started, stderr, true)
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

// replyFromTurnItems reconstructs a channel reply from a turn's run items when
// the run produced no string FinalOutput. This happens when the model pauses on
// a signal tool (AskUserQuestion / present_plan): the assistant's text and the
// pending question live in the run items rather than in FinalOutput. It returns
// the assistant message text followed by any question and its choices so that
// non-streaming channels deliver the real content instead of "Done.".
func replyFromTurnItems(items []agentsdk.RunItem) string {
	var assistantText []string
	for _, item := range items {
		if item.Type == agentsdk.RunItemMessage && item.Message != nil {
			if t := strings.TrimSpace(item.Message.Text); t != "" {
				assistantText = append(assistantText, t)
			}
		}
	}
	parts := []string{}
	if len(assistantText) > 0 {
		parts = append(parts, strings.Join(assistantText, "\n\n"))
	}
	for _, item := range items {
		if item.Type != agentsdk.RunItemToolCall || item.ToolCall == nil {
			continue
		}
		switch item.ToolCall.Name {
		case "AskUserQuestion":
			if q := formatAskUserQuestion(item.ToolCall.Input, len(assistantText) == 0); q != "" {
				parts = append(parts, q)
			}
		case "present_plan":
			if p := formatPresentPlan(item.ToolCall.Input); p != "" {
				parts = append(parts, p)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// formatAskUserQuestion renders an AskUserQuestion tool input as text. The
// question is included only when includeQuestion is true (i.e. the assistant
// did not already pose it in its message), to avoid duplicating it. Choices are
// always listed so the user knows the available options.
func formatAskUserQuestion(raw json.RawMessage, includeQuestion bool) string {
	var in struct {
		Question string   `json:"question"`
		Choices  []string `json:"choices"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	var b strings.Builder
	if includeQuestion {
		if q := strings.TrimSpace(in.Question); q != "" {
			b.WriteString(q)
		}
	}
	for _, c := range in.Choices {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("• " + c)
	}
	return strings.TrimSpace(b.String())
}

// formatPresentPlan renders a present_plan tool input as text so the proposed
// plan and its action labels are delivered to the user instead of being
// silently dropped on pause.
func formatPresentPlan(raw json.RawMessage) string {
	var in struct {
		Summary string `json:"summary"`
		Actions []struct {
			Label string `json:"label"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	var b strings.Builder
	if s := strings.TrimSpace(in.Summary); s != "" {
		b.WriteString(s)
	}
	for _, a := range in.Actions {
		if label := strings.TrimSpace(a.Label); label != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString("• " + label)
		}
	}
	return strings.TrimSpace(b.String())
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
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return fmt.Errorf("POST %s: %s: read error body: %w", redactedEndpoint(url), resp.Status, readErr)
		}
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
