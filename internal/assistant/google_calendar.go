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
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

var googleCalendarAPIBase = "https://www.googleapis.com/calendar/v3"

// googleCalendarTools returns Google Calendar read tools when the connected
// account granted a Calendar scope.
func googleCalendarTools(cfg appConfig) ([]agentsdk.Tool, error) {
	scopes := googleConnectedScopes(cfg)
	if !hasAnyCalendarScope(scopes) {
		return nil, nil
	}
	session, err := newGoogleAuthSession(cfg)
	if err != nil {
		return nil, err
	}
	base := calendarToolBase{src: session.AccessToken, client: defaultHTTPClient}
	tools := []agentsdk.Tool{
		&calendarListEventsTool{calendarToolBase: base},
		&calendarGetEventTool{calendarToolBase: base},
	}
	return tools, nil
}

func googleConnectedScopes(cfg appConfig) []string {
	file, exists, err := loadGoogleAuthFile(googleAuthPath(cfg))
	if !exists || err != nil {
		return nil
	}
	return file.Scopes
}

func hasAnyCalendarScope(scopes []string) bool {
	for _, scope := range scopes {
		if strings.Contains(scope, "/auth/calendar") {
			return true
		}
	}
	return false
}

type calendarToolBase struct {
	src    gmailTokenSource
	client *http.Client
}

func (t calendarToolBase) IsEnabled(*agentsdk.RunContext) bool { return true }
func (t calendarToolBase) NeedsApproval() bool                 { return false }
func (t calendarToolBase) TimeoutSeconds() int                 { return 0 }

// do performs an authorized Calendar API request, refreshing the access token
// through the broker as needed.
func (t calendarToolBase) do(ctx context.Context, method, endpoint string, body, out any) error {
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
	req.Header.Set("Authorization", "Bearer "+token)
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("Google Calendar denied the request (%s). The connected account may lack the needed scope; reconnect with `assistant google-connect --google-scope calendar`. detail: %s", resp.Status, firstLine(string(data)))
		}
		return fmt.Errorf("calendar %s %s: %s: %s", method, redactedEndpoint(endpoint), resp.Status, firstLine(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

type calendarTime struct {
	DateTime string `json:"dateTime,omitempty"`
	Date     string `json:"date,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

type calendarAttendee struct {
	Email          string `json:"email"`
	ResponseStatus string `json:"responseStatus,omitempty"`
}

type calendarEvent struct {
	ID          string             `json:"id,omitempty"`
	Summary     string             `json:"summary,omitempty"`
	Description string             `json:"description,omitempty"`
	Location    string             `json:"location,omitempty"`
	HTMLLink    string             `json:"htmlLink,omitempty"`
	Start       calendarTime       `json:"start,omitempty"`
	End         calendarTime       `json:"end,omitempty"`
	Attendees   []calendarAttendee `json:"attendees,omitempty"`
}

func calendarToolResult(value any, err error) agentsdk.ToolResult {
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}
	}
	text, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return agentsdk.ToolResult{Content: err.Error(), IsError: true}
	}
	return agentsdk.ToolResult{Content: string(text)}
}

func calendarPath(calendarID string) string {
	calendarID = strings.TrimSpace(calendarID)
	if calendarID == "" {
		calendarID = "primary"
	}
	return googleCalendarAPIBase + "/calendars/" + url.PathEscape(calendarID) + "/events"
}

// --- calendar_list_events ---

type calendarListEventsTool struct{ calendarToolBase }

func (t *calendarListEventsTool) Name() string { return "calendar_list_events" }
func (t *calendarListEventsTool) Description() string {
	return "List upcoming events from the connected Google Calendar. Defaults to the next 7 days of the primary calendar."
}
func (t *calendarListEventsTool) IsReadOnly() bool { return true }
func (t *calendarListEventsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"calendar_id": {"type": "string", "description": "Calendar id; defaults to 'primary'."},
			"time_min": {"type": "string", "description": "RFC3339 lower bound (inclusive). Defaults to now."},
			"time_max": {"type": "string", "description": "RFC3339 upper bound (exclusive). Defaults to 7 days from now."},
			"query": {"type": "string", "description": "Free-text search over event fields."},
			"max_results": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum events to return; defaults to 10."}
		}
	}`)
}
func (t *calendarListEventsTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		CalendarID string `json:"calendar_id"`
		TimeMin    string `json:"time_min"`
		TimeMax    string `json:"time_max"`
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
		}
	}
	now := time.Now()
	timeMin := strings.TrimSpace(in.TimeMin)
	if timeMin == "" {
		timeMin = now.Format(time.RFC3339)
	}
	timeMax := strings.TrimSpace(in.TimeMax)
	if timeMax == "" {
		timeMax = now.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	}
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}
	values := url.Values{}
	values.Set("timeMin", timeMin)
	values.Set("timeMax", timeMax)
	values.Set("singleEvents", "true")
	values.Set("orderBy", "startTime")
	values.Set("maxResults", strconv.Itoa(maxResults))
	if q := strings.TrimSpace(in.Query); q != "" {
		values.Set("q", q)
	}
	endpoint := calendarPath(in.CalendarID) + "?" + values.Encode()
	var resp struct {
		Items []calendarEvent `json:"items"`
	}
	if err := t.do(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		return calendarToolResult(nil, err), nil
	}
	return calendarToolResult(map[string]any{"count": len(resp.Items), "events": resp.Items}, nil), nil
}

// --- calendar_get_event ---

type calendarGetEventTool struct{ calendarToolBase }

func (t *calendarGetEventTool) Name() string { return "calendar_get_event" }
func (t *calendarGetEventTool) Description() string {
	return "Get the full details of a single event from the connected Google Calendar by event id."
}
func (t *calendarGetEventTool) IsReadOnly() bool { return true }
func (t *calendarGetEventTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"calendar_id": {"type": "string", "description": "Calendar id; defaults to 'primary'."},
			"event_id": {"type": "string", "description": "The event id to fetch."}
		},
		"required": ["event_id"]
	}`)
}
func (t *calendarGetEventTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		CalendarID string `json:"calendar_id"`
		EventID    string `json:"event_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(in.EventID) == "" {
		return agentsdk.ToolResult{Content: "event_id is required", IsError: true}, nil
	}
	endpoint := calendarPath(in.CalendarID) + "/" + url.PathEscape(strings.TrimSpace(in.EventID))
	var event calendarEvent
	if err := t.do(ctx, http.MethodGet, endpoint, nil, &event); err != nil {
		return calendarToolResult(nil, err), nil
	}
	return calendarToolResult(event, nil), nil
}
