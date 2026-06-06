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
	"net/url"
	"strconv"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

const (
	defaultGmailBodyChars = 8000
	maxGmailBodyChars     = 20000
)

// googleGmailTools returns Gmail read tools when the connected account granted
// gmail.readonly. Write scopes are intentionally not used by agent tools.
func googleGmailTools(cfg appConfig) ([]agentsdk.Tool, error) {
	scopes := googleConnectedScopes(cfg)
	if !hasGmailReadonlyScope(scopes) {
		return nil, nil
	}
	session, err := newGoogleAuthSession(cfg)
	if err != nil {
		return nil, err
	}
	base := gmailToolBase{cfg: cfg, src: session.AccessToken, client: defaultHTTPClient}
	return []agentsdk.Tool{
		&gmailSearchMessagesTool{gmailToolBase: base},
		&gmailGetMessageTool{gmailToolBase: base},
		&gmailGetThreadTool{gmailToolBase: base},
	}, nil
}

func hasGmailReadonlyScope(scopes []string) bool {
	for _, scope := range scopes {
		if strings.Contains(scope, "/auth/gmail.readonly") {
			return true
		}
	}
	return false
}

type gmailToolBase struct {
	cfg    appConfig
	src    gmailTokenSource
	client *http.Client
}

func (t gmailToolBase) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t gmailToolBase) NeedsApproval() bool                 { return false }
func (t gmailToolBase) TimeoutSeconds() int                 { return 0 }

func (t gmailToolBase) do(ctx context.Context, method, endpoint string, body, out any) error {
	token, err := t.src(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := t.client
	if client == nil {
		client = defaultHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("Gmail denied the request (%s). The connected account may lack the needed scope; reconnect with `assistant google-connect --google-scope gmail.readonly`. detail: %s", resp.Status, firstLine(string(data)))
		}
		return fmt.Errorf("gmail %s %s: %s: %s", method, redactedEndpoint(endpoint), resp.Status, firstLine(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func gmailToolResult(value any, err error) agentsdk.ToolResult {
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}
	}
	text, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}
	}
	return agentsdk.ToolResult{Content: string(text)}
}

// --- gmail_search_messages ---

type gmailSearchMessagesTool struct{ gmailToolBase }

func (t *gmailSearchMessagesTool) Name() string { return "gmail_search_messages" }
func (t *gmailSearchMessagesTool) Description() string {
	return "Search messages in the connected Gmail account using Gmail search syntax. Read-only."
}
func (t *gmailSearchMessagesTool) IsReadOnly() bool { return true }
func (t *gmailSearchMessagesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Gmail search query, such as 'from:alice newer_than:30d'."},
			"max_results": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum messages to return; defaults to 10."},
			"page_token": {"type": "string", "description": "Optional Gmail pagination token from a previous search."},
			"include_spam_trash": {"type": "boolean", "description": "Whether to include spam and trash in the search."}
		}
	}`)
}
func (t *gmailSearchMessagesTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		Query            string `json:"query"`
		MaxResults       int    `json:"max_results"`
		PageToken        string `json:"page_token"`
		IncludeSpamTrash bool   `json:"include_spam_trash"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
		}
	}
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}
	values := url.Values{}
	values.Set("maxResults", strconv.Itoa(maxResults))
	if query := strings.TrimSpace(in.Query); query != "" {
		values.Set("q", query)
	}
	if pageToken := strings.TrimSpace(in.PageToken); pageToken != "" {
		values.Set("pageToken", pageToken)
	}
	if in.IncludeSpamTrash {
		values.Set("includeSpamTrash", "true")
	}
	var resp gmailListResponse
	endpoint := gmailUserURL(t.cfg, "messages") + "?" + values.Encode()
	if err := t.do(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		return gmailToolResult(nil, err), nil
	}
	return gmailToolResult(map[string]any{
		"count":                len(resp.Messages),
		"messages":             resp.Messages,
		"next_page_token":      resp.NextPageToken,
		"result_size_estimate": resp.ResultSizeEstimate,
	}, nil), nil
}

// --- gmail_get_message ---

type gmailGetMessageTool struct{ gmailToolBase }

func (t *gmailGetMessageTool) Name() string { return "gmail_get_message" }
func (t *gmailGetMessageTool) Description() string {
	return "Read one Gmail message by id, returning selected headers, labels, snippet, and extracted body text. Read-only."
}
func (t *gmailGetMessageTool) IsReadOnly() bool { return true }
func (t *gmailGetMessageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"message_id": {"type": "string", "description": "The Gmail message id to read."},
			"max_body_chars": {"type": "integer", "minimum": 1, "maximum": 20000, "description": "Maximum body characters to return; defaults to 8000."}
		},
		"required": ["message_id"]
	}`)
}
func (t *gmailGetMessageTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		MessageID    string `json:"message_id"`
		MaxBodyChars int    `json:"max_body_chars"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	messageID := strings.TrimSpace(in.MessageID)
	if messageID == "" {
		return agentsdk.ToolResult{Content: "message_id is required", IsError: true}, nil
	}
	endpoint := gmailUserURL(t.cfg, "messages/"+url.PathEscape(messageID)) + "?format=full"
	var msg gmailMessage
	if err := t.do(ctx, http.MethodGet, endpoint, nil, &msg); err != nil {
		return gmailToolResult(nil, err), nil
	}
	return gmailToolResult(compactGmailMessage(msg, clampGmailBodyChars(in.MaxBodyChars)), nil), nil
}

// --- gmail_get_thread ---

type gmailGetThreadTool struct{ gmailToolBase }

func (t *gmailGetThreadTool) Name() string { return "gmail_get_thread" }
func (t *gmailGetThreadTool) Description() string {
	return "Read a Gmail thread by id, returning compact details for each message. Read-only."
}
func (t *gmailGetThreadTool) IsReadOnly() bool { return true }
func (t *gmailGetThreadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"thread_id": {"type": "string", "description": "The Gmail thread id to read."},
			"max_body_chars": {"type": "integer", "minimum": 1, "maximum": 20000, "description": "Maximum body characters per message; defaults to 8000."}
		},
		"required": ["thread_id"]
	}`)
}
func (t *gmailGetThreadTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		ThreadID     string `json:"thread_id"`
		MaxBodyChars int    `json:"max_body_chars"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	threadID := strings.TrimSpace(in.ThreadID)
	if threadID == "" {
		return agentsdk.ToolResult{Content: "thread_id is required", IsError: true}, nil
	}
	endpoint := gmailUserURL(t.cfg, "threads/"+url.PathEscape(threadID)) + "?format=full"
	var thread gmailThread
	if err := t.do(ctx, http.MethodGet, endpoint, nil, &thread); err != nil {
		return gmailToolResult(nil, err), nil
	}
	maxBodyChars := clampGmailBodyChars(in.MaxBodyChars)
	messages := make([]any, 0, len(thread.Messages))
	for _, msg := range thread.Messages {
		messages = append(messages, compactGmailMessage(msg, maxBodyChars))
	}
	return gmailToolResult(map[string]any{
		"id":       thread.ID,
		"count":    len(messages),
		"messages": messages,
	}, nil), nil
}

func clampGmailBodyChars(value int) int {
	if value <= 0 {
		return defaultGmailBodyChars
	}
	if value > maxGmailBodyChars {
		return maxGmailBodyChars
	}
	return value
}

func compactGmailMessage(msg gmailMessage, maxBodyChars int) map[string]any {
	return map[string]any{
		"id":            msg.ID,
		"thread_id":     msg.ThreadID,
		"label_ids":     msg.LabelIDs,
		"snippet":       msg.Snippet,
		"internal_date": msg.InternalDate,
		"headers":       selectedGmailHeaders(msg),
		"body_text":     gmailBodyText(msg.Payload, maxBodyChars),
		"attachments":   gmailAttachments(msg.Payload),
	}
}

func selectedGmailHeaders(msg gmailMessage) map[string]string {
	headers := map[string]string{}
	for _, name := range []string{"From", "To", "Cc", "Bcc", "Subject", "Date", "Message-ID"} {
		if value := gmailHeader(msg, name); value != "" {
			headers[strings.ToLower(name)] = value
		}
	}
	return headers
}

func gmailBodyText(part gmailMessagePart, maxChars int) string {
	var plain, html []string
	collectGmailBodyText(part, &plain, &html)
	text := strings.Join(nonEmptyStrings(plain), "\n\n")
	if strings.TrimSpace(text) == "" {
		text = strings.Join(nonEmptyStrings(html), "\n\n")
	}
	return truncateGmailText(text, maxChars)
}

func collectGmailBodyText(part gmailMessagePart, plain, html *[]string) {
	if data := strings.TrimSpace(part.Body.Data); data != "" {
		decoded, err := decodeGmailBodyData(data)
		if err == nil {
			text := strings.TrimSpace(strings.ReplaceAll(decoded, "\r\n", "\n"))
			switch strings.ToLower(strings.TrimSpace(part.MIMEType)) {
			case "text/plain":
				*plain = append(*plain, text)
			case "text/html":
				*html = append(*html, text)
			}
		}
	}
	for _, child := range part.Parts {
		collectGmailBodyText(child, plain, html)
	}
}

func decodeGmailBodyData(data string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(data)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func truncateGmailText(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return strings.TrimSpace(string(runes[:maxChars])) + "\n[truncated]"
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

type gmailAttachmentInfo struct {
	Filename     string `json:"filename,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
	Size         int    `json:"size,omitempty"`
	AttachmentID string `json:"attachment_id,omitempty"`
}

func gmailAttachments(part gmailMessagePart) []gmailAttachmentInfo {
	var out []gmailAttachmentInfo
	collectGmailAttachments(part, &out)
	return out
}

func collectGmailAttachments(part gmailMessagePart, out *[]gmailAttachmentInfo) {
	if strings.TrimSpace(part.Filename) != "" || strings.TrimSpace(part.Body.AttachmentID) != "" {
		*out = append(*out, gmailAttachmentInfo{
			Filename:     part.Filename,
			MIMEType:     part.MIMEType,
			Size:         part.Body.Size,
			AttachmentID: part.Body.AttachmentID,
		})
	}
	for _, child := range part.Parts {
		collectGmailAttachments(child, out)
	}
}
