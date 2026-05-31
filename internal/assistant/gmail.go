// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const gmailAPIBase = "https://gmail.googleapis.com/gmail/v1/users/"

type gmailSeenState struct {
	Seen map[string]bool `json:"seen"`
}

type gmailMessageRef struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

type gmailListResponse struct {
	Messages []gmailMessageRef `json:"messages"`
}

type gmailMessage struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
	Snippet  string `json:"snippet"`
	Payload  struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
	} `json:"payload"`
}

func runGmailPoller(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	token := strings.TrimSpace(cfg.GmailToken)
	if token == "" {
		return errors.New("gmail polling requires --gmail-token, ASSISTANT_GMAIL_ACCESS_TOKEN, or ASSISTANT_GMAIL_TOKEN")
	}
	state, err := loadGmailSeenState(cfg)
	if err != nil {
		return err
	}
	conversations := newConversationStore()
	fmt.Fprintf(stderr, "assistant gmail polling query=%q; no inbound port required\n", cfg.GmailQuery)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := pollGmailOnce(ctx, cfg, token, &state, stdout, stderr, conversations); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(stderr, "gmail poll warning: %v\n", err)
		}
		if !sleepContext(ctx, time.Duration(cfg.GmailPollInterval)*time.Second) {
			return nil
		}
	}
}

func pollGmailOnce(ctx context.Context, cfg appConfig, token string, state *gmailSeenState, stdout, stderr io.Writer, conversations *conversationStore) error {
	refs, err := listGmailMessages(ctx, cfg, token)
	if err != nil {
		return err
	}
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		if strings.TrimSpace(ref.ID) == "" || state.Seen[ref.ID] {
			continue
		}
		msg, err := fetchGmailMessage(ctx, cfg, token, ref.ID)
		if err != nil {
			fmt.Fprintf(stderr, "gmail message warning: %v\n", err)
			continue
		}
		reply, err := replyToInbound(ctx, cfg, inboundMessage{
			Channel: "gmail",
			UserID:  gmailHeader(msg, "From"),
			Thread:  firstNonEmpty(msg.ThreadID, ref.ThreadID),
			Text:    gmailInboundText(msg),
		}, stdout, stderr, conversations)
		if err != nil {
			fmt.Fprintf(stderr, "gmail assistant warning for %s: %v\n", ref.ID, err)
		} else if cfg.GmailSendReplies {
			if err := sendGmailReply(ctx, cfg, token, msg, reply); err != nil {
				fmt.Fprintf(stderr, "gmail send warning for %s: %v\n", ref.ID, err)
			}
		} else if strings.TrimSpace(reply) != "" {
			fmt.Fprintf(stdout, "gmail %s reply:\n%s\n", ref.ID, reply)
		}
		if cfg.GmailMarkRead {
			if err := markGmailRead(ctx, cfg, token, ref.ID); err != nil {
				fmt.Fprintf(stderr, "gmail mark-read warning for %s: %v\n", ref.ID, err)
			}
		}
		state.Seen[ref.ID] = true
		if err := saveGmailSeenState(cfg, *state); err != nil {
			return err
		}
	}
	return nil
}

func listGmailMessages(ctx context.Context, cfg appConfig, token string) ([]gmailMessageRef, error) {
	endpoint := gmailUserURL(cfg, "messages")
	values := url.Values{}
	values.Set("q", cfg.GmailQuery)
	values.Set("maxResults", strconv.Itoa(cfg.GmailMaxResults))
	endpoint += "?" + values.Encode()
	var out gmailListResponse
	if err := gmailGET(ctx, token, endpoint, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func fetchGmailMessage(ctx context.Context, cfg appConfig, token, id string) (gmailMessage, error) {
	endpoint := gmailUserURL(cfg, "messages/"+url.PathEscape(id))
	values := url.Values{}
	values.Set("format", "metadata")
	for _, header := range []string{"From", "To", "Subject", "Date", "Message-ID"} {
		values.Add("metadataHeaders", header)
	}
	endpoint += "?" + values.Encode()
	var out gmailMessage
	return out, gmailGET(ctx, token, endpoint, &out)
}

func gmailGET(ctx context.Context, token, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s: %s", endpoint, resp.Status, firstLine(string(data)))
	}
	return json.Unmarshal(data, target)
}

func sendGmailReply(ctx context.Context, cfg appConfig, token string, msg gmailMessage, body string) error {
	to := strings.TrimSpace(gmailHeader(msg, "From"))
	if to == "" {
		return errors.New("original Gmail message has no From header")
	}
	subject := replySubject(gmailHeader(msg, "Subject"))
	messageID := strings.TrimSpace(gmailHeader(msg, "Message-ID"))
	var raw strings.Builder
	fmt.Fprintf(&raw, "To: %s\r\n", to)
	fmt.Fprintf(&raw, "Subject: %s\r\n", subject)
	if messageID != "" {
		fmt.Fprintf(&raw, "In-Reply-To: %s\r\n", messageID)
		fmt.Fprintf(&raw, "References: %s\r\n", messageID)
	}
	raw.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	raw.WriteString("\r\n")
	raw.WriteString(body)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(raw.String()))
	payload := map[string]any{"raw": encoded}
	if strings.TrimSpace(msg.ThreadID) != "" {
		payload["threadId"] = msg.ThreadID
	}
	return postJSON(ctx, gmailUserURL(cfg, "messages/send"), token, payload)
}

func markGmailRead(ctx context.Context, cfg appConfig, token, id string) error {
	payload := map[string]any{"removeLabelIds": []string{"UNREAD"}}
	return postJSON(ctx, gmailUserURL(cfg, "messages/"+url.PathEscape(id)+"/modify"), token, payload)
}

func gmailUserURL(cfg appConfig, suffix string) string {
	user := strings.TrimSpace(cfg.GmailUser)
	if user == "" {
		user = "me"
	}
	return gmailAPIBase + url.PathEscape(user) + "/" + suffix
}

func gmailHeader(msg gmailMessage, name string) string {
	for _, header := range msg.Payload.Headers {
		if strings.EqualFold(header.Name, name) {
			return strings.TrimSpace(header.Value)
		}
	}
	return ""
}

func gmailInboundText(msg gmailMessage) string {
	var parts []string
	if from := gmailHeader(msg, "From"); from != "" {
		parts = append(parts, "From: "+from)
	}
	if subject := gmailHeader(msg, "Subject"); subject != "" {
		parts = append(parts, "Subject: "+subject)
	}
	if date := gmailHeader(msg, "Date"); date != "" {
		parts = append(parts, "Date: "+date)
	}
	if snippet := strings.TrimSpace(msg.Snippet); snippet != "" {
		parts = append(parts, "Snippet: "+snippet)
	}
	return strings.Join(parts, "\n")
}

func replySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "Re:"
	}
	if strings.HasPrefix(strings.ToLower(subject), "re:") {
		return subject
	}
	return "Re: " + subject
}

func loadGmailSeenState(cfg appConfig) (gmailSeenState, error) {
	state := gmailSeenState{Seen: map[string]bool{}}
	if _, err := readJSONFile(stateFilePath(cfg, "gmail_seen.json"), &state); err != nil {
		return state, err
	}
	if state.Seen == nil {
		state.Seen = map[string]bool{}
	}
	return state, nil
}

func saveGmailSeenState(cfg appConfig, state gmailSeenState) error {
	if state.Seen == nil {
		state.Seen = map[string]bool{}
	}
	return writeJSONFile(stateFilePath(cfg, "gmail_seen.json"), state)
}
