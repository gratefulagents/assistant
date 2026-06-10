// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

var microsoftGraphAPIBase = "https://graph.microsoft.com/v1.0"

const (
	defaultOutlookBodyChars = 8000
	maxOutlookBodyChars     = 20000
)

// microsoftMailTools returns Outlook mail read tools when the connected
// Microsoft account granted Mail.Read. Write scopes are intentionally not used
// by agent tools.
func microsoftMailTools(cfg appConfig) ([]agentsdk.Tool, error) {
	scopes := microsoftConnectedScopes(cfg)
	if !hasMicrosoftScope(scopes, "mail.read") {
		return nil, nil
	}
	session, err := newMicrosoftAuthSession(cfg)
	if err != nil {
		return nil, err
	}
	base := graphToolBase{src: session.AccessToken, client: defaultHTTPClient}
	return []agentsdk.Tool{
		&outlookSearchMessagesTool{graphToolBase: base},
		&outlookGetMessageTool{graphToolBase: base},
	}, nil
}

// graphToolBase issues authorized Microsoft Graph requests, refreshing the
// access token through the broker as needed. Shared by mail and calendar tools.
type graphToolBase struct {
	src    gmailTokenSource
	client *http.Client
}

func (t graphToolBase) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t graphToolBase) NeedsApproval() bool                 { return false }
func (t graphToolBase) TimeoutSeconds() int                 { return 0 }

func (t graphToolBase) do(ctx context.Context, method, endpoint string, body, out any) error {
	return t.doWithHeaders(ctx, method, endpoint, body, out, nil)
}

func graphToolResult(value any, err error) agentsdk.ToolResult {
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}
	}
	text, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}
	}
	return agentsdk.ToolResult{Content: string(text)}
}

// outlookMessage is the subset of the Graph message resource the tools expose.
type outlookMessage struct {
	ID               string              `json:"id"`
	Subject          string              `json:"subject"`
	BodyPreview      string              `json:"bodyPreview"`
	From             outlookRecipient    `json:"from"`
	ToRecipients     []outlookRecipient  `json:"toRecipients"`
	CcRecipients     []outlookRecipient  `json:"ccRecipients"`
	ReceivedDateTime string              `json:"receivedDateTime"`
	ConversationID   string              `json:"conversationId"`
	HasAttachments   bool                `json:"hasAttachments"`
	IsRead           bool                `json:"isRead"`
	WebLink          string              `json:"webLink"`
	Body             *outlookItemBody    `json:"body,omitempty"`
	Attachments      []outlookAttachment `json:"attachments,omitempty"`
}

type outlookRecipient struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type outlookItemBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type outlookAttachment struct {
	Name        string `json:"name,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Size        int    `json:"size,omitempty"`
	ID          string `json:"id,omitempty"`
}

func formatOutlookRecipient(r outlookRecipient) string {
	name := strings.TrimSpace(r.EmailAddress.Name)
	address := strings.TrimSpace(r.EmailAddress.Address)
	if name != "" && address != "" && !strings.EqualFold(name, address) {
		return name + " <" + address + ">"
	}
	return firstNonEmpty(address, name)
}

func formatOutlookRecipients(rs []outlookRecipient) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if v := formatOutlookRecipient(r); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func compactOutlookMessage(msg outlookMessage, maxBodyChars int) map[string]any {
	out := map[string]any{
		"id":              msg.ID,
		"conversation_id": msg.ConversationID,
		"subject":         msg.Subject,
		"from":            formatOutlookRecipient(msg.From),
		"to":              formatOutlookRecipients(msg.ToRecipients),
		"cc":              formatOutlookRecipients(msg.CcRecipients),
		"received":        msg.ReceivedDateTime,
		"is_read":         msg.IsRead,
		"has_attachments": msg.HasAttachments,
		"snippet":         msg.BodyPreview,
	}
	if msg.Body != nil {
		out["body_text"] = truncateGmailText(msg.Body.Content, maxBodyChars)
	}
	if len(msg.Attachments) > 0 {
		out["attachments"] = msg.Attachments
	}
	return out
}

func clampOutlookBodyChars(value int) int {
	if value <= 0 {
		return defaultOutlookBodyChars
	}
	if value > maxOutlookBodyChars {
		return maxOutlookBodyChars
	}
	return value
}

// --- outlook_search_messages ---

type outlookSearchMessagesTool struct{ graphToolBase }

func (t *outlookSearchMessagesTool) Name() string { return "outlook_search_messages" }
func (t *outlookSearchMessagesTool) Description() string {
	return "Search messages in the connected Outlook (Microsoft) mailbox. Uses Microsoft Graph $search syntax (e.g. 'from:alice subject:invoice'). Read-only."
}
func (t *outlookSearchMessagesTool) IsReadOnly() bool { return true }
func (t *outlookSearchMessagesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Graph $search query, such as 'from:alice subject:invoice'. Empty lists the most recent messages."},
			"max_results": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum messages to return; defaults to 10."}
		}
	}`)
}
func (t *outlookSearchMessagesTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
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
	values.Set("$top", strconv.Itoa(maxResults))
	values.Set("$select", "id,subject,bodyPreview,from,toRecipients,receivedDateTime,conversationId,hasAttachments,isRead,webLink")
	if query := strings.TrimSpace(in.Query); query != "" {
		values.Set("$search", `"`+strings.ReplaceAll(query, `"`, ``)+`"`)
	} else {
		// $orderby cannot be combined with $search; sort only the plain listing.
		values.Set("$orderby", "receivedDateTime desc")
	}
	endpoint := microsoftGraphAPIBase + "/me/messages?" + values.Encode()
	var resp struct {
		Value []outlookMessage `json:"value"`
	}
	if err := t.do(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		return graphToolResult(nil, err), nil
	}
	messages := make([]any, 0, len(resp.Value))
	for _, msg := range resp.Value {
		messages = append(messages, compactOutlookMessage(msg, 0))
	}
	return graphToolResult(map[string]any{
		"count":    len(messages),
		"messages": messages,
	}, nil), nil
}

// --- outlook_get_message ---

type outlookGetMessageTool struct{ graphToolBase }

func (t *outlookGetMessageTool) Name() string { return "outlook_get_message" }
func (t *outlookGetMessageTool) Description() string {
	return "Read one Outlook message by id, returning sender, recipients, subject, and body text. Read-only."
}
func (t *outlookGetMessageTool) IsReadOnly() bool { return true }
func (t *outlookGetMessageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"message_id": {"type": "string", "description": "The Outlook message id to read."},
			"max_body_chars": {"type": "integer", "minimum": 1, "maximum": 20000, "description": "Maximum body characters to return; defaults to 8000."}
		},
		"required": ["message_id"]
	}`)
}
func (t *outlookGetMessageTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
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
	values := url.Values{}
	values.Set("$select", "id,subject,bodyPreview,body,from,toRecipients,ccRecipients,receivedDateTime,conversationId,hasAttachments,isRead,webLink")
	values.Set("$expand", "attachments($select=id,name,contentType,size)")
	endpoint := microsoftGraphAPIBase + "/me/messages/" + url.PathEscape(messageID) + "?" + values.Encode()
	var msg outlookMessage
	// Prefer the text body so the agent never has to parse HTML.
	headers := map[string]string{"Prefer": `outlook.body-content-type="text"`}
	if err := t.doWithHeaders(ctx, http.MethodGet, endpoint, nil, &msg, headers); err != nil {
		return graphToolResult(nil, err), nil
	}
	return graphToolResult(compactOutlookMessage(msg, clampOutlookBodyChars(in.MaxBodyChars)), nil), nil
}

// doWithHeaders is do with extra request headers (e.g. Graph Prefer hints).
func (t graphToolBase) doWithHeaders(ctx context.Context, method, endpoint string, body, out any, headers map[string]string) error {
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
	for k, v := range headers {
		req.Header.Set(k, v)
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
			return fmt.Errorf("Microsoft Graph denied the request (%s). The connected account may lack the needed scope; reconnect with `assistant microsoft-connect`. detail: %s", resp.Status, firstLine(string(data)))
		}
		return fmt.Errorf("graph %s %s: %s: %s", method, redactedEndpoint(endpoint), resp.Status, firstLine(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
