// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

// microsoftCalendarTools returns Outlook calendar read tools when the connected
// Microsoft account granted Calendars.Read (or ReadWrite).
func microsoftCalendarTools(cfg appConfig) ([]agentsdk.Tool, error) {
	scopes := microsoftConnectedScopes(cfg)
	if !hasMicrosoftScope(scopes, "calendars.read") {
		return nil, nil
	}
	session, err := newMicrosoftAuthSession(cfg)
	if err != nil {
		return nil, err
	}
	base := graphToolBase{src: session.AccessToken, client: defaultHTTPClient}
	return []agentsdk.Tool{
		&outlookListEventsTool{graphToolBase: base},
		&outlookGetEventTool{graphToolBase: base},
	}, nil
}

// outlookEvent is the subset of the Graph event resource the tools expose.
type outlookEvent struct {
	ID        string             `json:"id,omitempty"`
	Subject   string             `json:"subject,omitempty"`
	BodyPview string             `json:"bodyPreview,omitempty"`
	WebLink   string             `json:"webLink,omitempty"`
	IsAllDay  bool               `json:"isAllDay,omitempty"`
	Start     outlookDateTime    `json:"start,omitempty"`
	End       outlookDateTime    `json:"end,omitempty"`
	Location  outlookLocation    `json:"location,omitempty"`
	Organizer *outlookRecipient  `json:"organizer,omitempty"`
	Attendees []outlookAttendee  `json:"attendees,omitempty"`
}

type outlookDateTime struct {
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

type outlookLocation struct {
	DisplayName string `json:"displayName,omitempty"`
}

type outlookAttendee struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
	Status struct {
		Response string `json:"response"`
	} `json:"status"`
}

func compactOutlookEvent(event outlookEvent) map[string]any {
	out := map[string]any{
		"id":         event.ID,
		"subject":    event.Subject,
		"start":      event.Start,
		"end":        event.End,
		"is_all_day": event.IsAllDay,
	}
	if event.Location.DisplayName != "" {
		out["location"] = event.Location.DisplayName
	}
	if event.BodyPview != "" {
		out["preview"] = event.BodyPview
	}
	if event.Organizer != nil {
		out["organizer"] = formatOutlookRecipient(*event.Organizer)
	}
	if len(event.Attendees) > 0 {
		attendees := make([]map[string]string, 0, len(event.Attendees))
		for _, a := range event.Attendees {
			attendees = append(attendees, map[string]string{
				"email":    a.EmailAddress.Address,
				"name":     a.EmailAddress.Name,
				"response": a.Status.Response,
			})
		}
		out["attendees"] = attendees
	}
	if event.WebLink != "" {
		out["web_link"] = event.WebLink
	}
	return out
}

// --- outlook_list_events ---

type outlookListEventsTool struct{ graphToolBase }

func (t *outlookListEventsTool) Name() string { return "outlook_list_events" }
func (t *outlookListEventsTool) Description() string {
	return "List upcoming events from the connected Outlook (Microsoft) calendar. Defaults to the next 7 days of the primary calendar."
}
func (t *outlookListEventsTool) IsReadOnly() bool { return true }
func (t *outlookListEventsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"time_min": {"type": "string", "description": "RFC3339 lower bound (inclusive). Defaults to now."},
			"time_max": {"type": "string", "description": "RFC3339 upper bound (exclusive). Defaults to 7 days from now."},
			"max_results": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum events to return; defaults to 10."}
		}
	}`)
}
func (t *outlookListEventsTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		TimeMin    string `json:"time_min"`
		TimeMax    string `json:"time_max"`
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
		timeMin = now.UTC().Format(time.RFC3339)
	}
	timeMax := strings.TrimSpace(in.TimeMax)
	if timeMax == "" {
		timeMax = now.Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	}
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}
	values := url.Values{}
	values.Set("startDateTime", timeMin)
	values.Set("endDateTime", timeMax)
	values.Set("$top", strconv.Itoa(maxResults))
	values.Set("$orderby", "start/dateTime")
	values.Set("$select", "id,subject,bodyPreview,start,end,location,organizer,attendees,isAllDay,webLink")
	endpoint := microsoftGraphAPIBase + "/me/calendarView?" + values.Encode()
	var resp struct {
		Value []outlookEvent `json:"value"`
	}
	if err := t.do(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		return graphToolResult(nil, err), nil
	}
	events := make([]any, 0, len(resp.Value))
	for _, event := range resp.Value {
		events = append(events, compactOutlookEvent(event))
	}
	return graphToolResult(map[string]any{"count": len(events), "events": events}, nil), nil
}

// --- outlook_get_event ---

type outlookGetEventTool struct{ graphToolBase }

func (t *outlookGetEventTool) Name() string { return "outlook_get_event" }
func (t *outlookGetEventTool) Description() string {
	return "Get the full details of a single event from the connected Outlook (Microsoft) calendar by event id."
}
func (t *outlookGetEventTool) IsReadOnly() bool { return true }
func (t *outlookGetEventTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"event_id": {"type": "string", "description": "The event id to fetch."}
		},
		"required": ["event_id"]
	}`)
}
func (t *outlookGetEventTool) Execute(ctx context.Context, raw json.RawMessage, _ string) (agentsdk.ToolResult, error) {
	var in struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return agentsdk.ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}
	eventID := strings.TrimSpace(in.EventID)
	if eventID == "" {
		return agentsdk.ToolResult{Content: "event_id is required", IsError: true}, nil
	}
	endpoint := microsoftGraphAPIBase + "/me/events/" + url.PathEscape(eventID)
	var event outlookEvent
	if err := t.do(ctx, http.MethodGet, endpoint, nil, &event); err != nil {
		return graphToolResult(nil, err), nil
	}
	return graphToolResult(compactOutlookEvent(event), nil), nil
}
