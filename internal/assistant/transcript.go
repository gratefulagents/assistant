// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

const (
	transcriptStateFileName = "transcripts.ndjson"
	transcriptMaxTextChars  = 8000
	transcriptMaxJSONChars  = 4000
)

var transcriptFileMu sync.Mutex

type transcriptContext struct {
	SessionID      string `json:"session_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Channel        string `json:"channel,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	Thread         string `json:"thread,omitempty"`
	UserText       string `json:"user_text,omitempty"`
}

type transcriptTurn struct {
	ID             string           `json:"id"`
	SessionID      string           `json:"session_id"`
	ConversationID string           `json:"conversation_id,omitempty"`
	Channel        string           `json:"channel,omitempty"`
	UserID         string           `json:"user_id,omitempty"`
	Thread         string           `json:"thread,omitempty"`
	Mode           string           `json:"mode,omitempty"`
	Command        string           `json:"command,omitempty"`
	Provider       string           `json:"provider,omitempty"`
	Model          string           `json:"model,omitempty"`
	WorkDir        string           `json:"workdir,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	EndedAt        time.Time        `json:"ended_at"`
	UserText       string           `json:"user_text,omitempty"`
	Prompt         string           `json:"prompt,omitempty"`
	FinalText      string           `json:"final_text,omitempty"`
	Summary        string           `json:"summary,omitempty"`
	ToolCalls      []string         `json:"tool_calls,omitempty"`
	Items          []transcriptItem `json:"items,omitempty"`
}

type transcriptItem struct {
	Type    string `json:"type"`
	Agent   string `json:"agent,omitempty"`
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	CallID  string `json:"call_id,omitempty"`
	Input   string `json:"input,omitempty"`
	Content string `json:"content,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

type transcriptSearchInput struct {
	Query        string `json:"query"`
	SessionID    string `json:"session_id"`
	AroundTurnID string `json:"around_turn_id"`
	Limit        int    `json:"limit"`
	Window       int    `json:"window"`
	IncludeItems bool   `json:"include_items"`
}

type transcriptSearchResult struct {
	Mode       string                     `json:"mode"`
	TotalTurns int                        `json:"total_turns"`
	Sessions   []transcriptSessionSummary `json:"sessions,omitempty"`
	Turns      []transcriptTurnResult     `json:"turns,omitempty"`
}

type transcriptSessionSummary struct {
	SessionID      string    `json:"session_id"`
	ConversationID string    `json:"conversation_id,omitempty"`
	Channel        string    `json:"channel,omitempty"`
	UserID         string    `json:"user_id,omitempty"`
	Thread         string    `json:"thread,omitempty"`
	FirstTurnAt    time.Time `json:"first_turn_at"`
	LastTurnAt     time.Time `json:"last_turn_at"`
	Turns          int       `json:"turns"`
	LastUserText   string    `json:"last_user_text,omitempty"`
	LastFinalText  string    `json:"last_final_text,omitempty"`
}

type transcriptTurnResult struct {
	ID             string           `json:"id"`
	SessionID      string           `json:"session_id"`
	ConversationID string           `json:"conversation_id,omitempty"`
	Channel        string           `json:"channel,omitempty"`
	UserID         string           `json:"user_id,omitempty"`
	Thread         string           `json:"thread,omitempty"`
	Mode           string           `json:"mode,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	EndedAt        time.Time        `json:"ended_at"`
	UserText       string           `json:"user_text,omitempty"`
	FinalText      string           `json:"final_text,omitempty"`
	Summary        string           `json:"summary,omitempty"`
	ToolCalls      []string         `json:"tool_calls,omitempty"`
	Score          int              `json:"score,omitempty"`
	Snippet        string           `json:"snippet,omitempty"`
	Items          []transcriptItem `json:"items,omitempty"`
}

func transcriptFilePath(cfg appConfig) string {
	if path := strings.TrimSpace(cfg.TranscriptLogPath); path != "" {
		return path
	}
	return stateFilePath(cfg, transcriptStateFileName)
}

func newTranscriptID(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "tr"
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return prefix + "_" + time.Now().UTC().Format("20060102T150405.000000000Z") + "_" + hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func transcriptContextForInbound(msg inboundMessage, session *conversationSession, userText string) transcriptContext {
	ctx := transcriptContext{
		ConversationID: conversationKey(msg),
		Channel:        strings.TrimSpace(msg.Channel),
		UserID:         strings.TrimSpace(msg.UserID),
		Thread:         strings.TrimSpace(msg.Thread),
		UserText:       strings.TrimSpace(userText),
	}
	if session != nil {
		ctx.SessionID = session.transcriptID
	}
	return ctx
}

func transcriptContextForTerminal(session *conversationSession, userText string) transcriptContext {
	ctx := transcriptContext{
		ConversationID: "terminal:default",
		Channel:        "terminal",
		UserText:       strings.TrimSpace(userText),
	}
	if session != nil {
		ctx.SessionID = session.transcriptID
	}
	return ctx
}

func recordTranscriptTurn(ctx context.Context, cfg appConfig, meta transcriptContext, prompt, mode string, started time.Time, items []agentsdk.RunItem, finalText string) error {
	if !cfg.EnableTranscripts {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	sessionID := strings.TrimSpace(meta.SessionID)
	if sessionID == "" {
		sessionID = newTranscriptID("sess")
	}
	userText := strings.TrimSpace(meta.UserText)
	if userText == "" {
		userText = prompt
	}
	if started.IsZero() {
		started = time.Now().UTC()
	}
	turn := transcriptTurn{
		ID:             newTranscriptID("turn"),
		SessionID:      sessionID,
		ConversationID: strings.TrimSpace(meta.ConversationID),
		Channel:        strings.TrimSpace(meta.Channel),
		UserID:         strings.TrimSpace(meta.UserID),
		Thread:         strings.TrimSpace(meta.Thread),
		Mode:           strings.TrimSpace(mode),
		Command:        strings.TrimSpace(cfg.Command),
		Provider:       strings.TrimSpace(cfg.Provider),
		Model:          strings.TrimSpace(cfg.Model),
		WorkDir:        strings.TrimSpace(cfg.WorkDir),
		StartedAt:      started.UTC(),
		EndedAt:        time.Now().UTC(),
		UserText:       transcriptText(userText, transcriptMaxTextChars),
		Prompt:         transcriptText(prompt, transcriptMaxTextChars),
		FinalText:      transcriptText(finalText, transcriptMaxTextChars),
		Summary:        transcriptText(agentsdk.BuildAssistantTurnSummary(items), 1200),
		ToolCalls:      agentsdk.SummarizeTurnToolCalls(items, 12),
		Items:          transcriptItems(items),
	}
	return appendTranscriptTurn(cfg, turn)
}

func appendTranscriptTurn(cfg appConfig, turn transcriptTurn) error {
	data, err := json.Marshal(turn)
	if err != nil {
		return err
	}
	redacted := redactAuditText(string(data))

	transcriptFileMu.Lock()
	defer transcriptFileMu.Unlock()
	db, err := stateDBFor(cfg)
	if err != nil {
		return err
	}
	if err := ensureTranscriptImport(cfg, db); err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO assistant_transcript_turns (id, session_id, started_at, ended_at, data)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				session_id = excluded.session_id,
				started_at = excluded.started_at,
				ended_at   = excluded.ended_at,
				data       = excluded.data`,
		turn.ID, turn.SessionID, turn.StartedAt.UnixNano(), turn.EndedAt.UnixNano(), redacted)
	return err
}

func readTranscriptTurns(ctx context.Context, cfg appConfig) ([]transcriptTurn, error) {
	db, err := stateDBFor(cfg)
	if err != nil {
		return nil, err
	}
	if err := ensureTranscriptImport(cfg, db); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT data FROM assistant_transcript_turns ORDER BY rowid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []transcriptTurn
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var turn transcriptTurn
		if err := json.Unmarshal([]byte(raw), &turn); err != nil {
			return nil, fmt.Errorf("parse transcript row: %w", err)
		}
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return turns, nil
}

// transcriptImport guards the one-shot import of a legacy transcripts.ndjson
// file into the database. A path is recorded as imported only after a fully
// successful, committed import, so a transient failure (bad line, DB error) is
// retried on the next call instead of leaving a partial import wedged.
var (
	transcriptImportMu sync.Mutex
	transcriptImported = map[string]bool{}
)

// ensureTranscriptImport migrates a legacy transcripts.ndjson file into the
// database exactly once per process. All rows are inserted inside a single
// transaction; only after it commits is the legacy file renamed to <path>.bak
// and the path marked imported. It is a no-op when no legacy file exists.
func ensureTranscriptImport(cfg appConfig, db *sql.DB) error {
	path := strings.TrimSpace(transcriptFilePath(cfg))
	if path == "" {
		return nil
	}
	transcriptImportMu.Lock()
	defer transcriptImportMu.Unlock()
	if transcriptImported[path] {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			transcriptImported[path] = true
			return nil
		}
		return err
	}
	defer file.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var turn transcriptTurn
		if err := json.Unmarshal([]byte(line), &turn); err != nil {
			return fmt.Errorf("parse legacy transcript %s: %w", path, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO assistant_transcript_turns (id, session_id, started_at, ended_at, data)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(id) DO NOTHING`,
			turn.ID, turn.SessionID, turn.StartedAt.UnixNano(), turn.EndedAt.UnixNano(), line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = os.Rename(path, path+".bak")
	transcriptImported[path] = true
	return nil
}

func searchTranscriptTurns(ctx context.Context, cfg appConfig, in transcriptSearchInput) (transcriptSearchResult, error) {
	turns, err := readTranscriptTurns(ctx, cfg)
	if err != nil {
		return transcriptSearchResult{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	result := transcriptSearchResult{TotalTurns: len(turns)}
	query := strings.TrimSpace(in.Query)
	sessionID := strings.TrimSpace(in.SessionID)
	aroundID := strings.TrimSpace(in.AroundTurnID)

	switch {
	case aroundID != "":
		result.Mode = "scroll"
		result.Turns = scrollTranscriptTurns(turns, sessionID, aroundID, in.Window, in.IncludeItems)
	case sessionID != "":
		result.Mode = "session"
		result.Turns = sessionTranscriptTurns(turns, sessionID, limit, in.IncludeItems)
	case query != "":
		result.Mode = "search"
		result.Turns = queryTranscriptTurns(turns, query, limit, in.IncludeItems)
	default:
		result.Mode = "browse"
		result.Sessions = recentTranscriptSessions(turns, limit)
	}
	return result, nil
}

func transcriptItems(items []agentsdk.RunItem) []transcriptItem {
	out := make([]transcriptItem, 0, len(items))
	for _, item := range items {
		entry := transcriptItem{Type: runItemTypeName(item.Type)}
		if item.Agent != nil {
			entry.Agent = item.Agent.Name
		}
		switch item.Type {
		case agentsdk.RunItemMessage:
			if item.Message == nil {
				continue
			}
			entry.Text = transcriptText(item.Message.Text, transcriptMaxTextChars)
		case agentsdk.RunItemToolCall:
			if item.ToolCall == nil {
				continue
			}
			entry.Tool = item.ToolCall.Name
			entry.CallID = item.ToolCall.ID
			entry.Input = transcriptText(string(item.ToolCall.Input), transcriptMaxJSONChars)
		case agentsdk.RunItemToolOutput:
			if item.ToolOutput == nil {
				continue
			}
			entry.CallID = item.ToolOutput.CallID
			entry.Content = transcriptText(item.ToolOutput.Content, transcriptMaxTextChars)
			entry.IsError = item.ToolOutput.IsError
		case agentsdk.RunItemToolApproval:
			if item.ToolApproval == nil {
				continue
			}
			entry.Tool = item.ToolApproval.ToolName
			entry.CallID = item.ToolApproval.CallID
			entry.Input = transcriptText(string(item.ToolApproval.Input), transcriptMaxJSONChars)
		case agentsdk.RunItemHandoffCall:
			if item.HandoffCall == nil {
				continue
			}
			entry.Text = transcriptText(item.HandoffCall.FromAgent+" -> "+item.HandoffCall.ToAgent, 400)
		case agentsdk.RunItemHandoffOutput:
			if item.HandoffOutput == nil {
				continue
			}
			entry.Text = transcriptText(item.HandoffOutput.FromAgent+" -> "+item.HandoffOutput.ToAgent, 400)
		case agentsdk.RunItemCompaction:
			if item.Compaction == nil {
				continue
			}
			entry.Text = transcriptText("provider compaction", 400)
		case agentsdk.RunItemReasoning:
			if item.Reasoning == nil {
				continue
			}
			entry.Text = "reasoning omitted"
		default:
			continue
		}
		out = append(out, entry)
	}
	return out
}

func recentTranscriptSessions(turns []transcriptTurn, limit int) []transcriptSessionSummary {
	byID := map[string]*transcriptSessionSummary{}
	for _, turn := range turns {
		id := strings.TrimSpace(turn.SessionID)
		if id == "" {
			continue
		}
		summary := byID[id]
		if summary == nil {
			summary = &transcriptSessionSummary{
				SessionID:      id,
				ConversationID: turn.ConversationID,
				Channel:        turn.Channel,
				UserID:         turn.UserID,
				Thread:         turn.Thread,
				FirstTurnAt:    turn.StartedAt,
				LastTurnAt:     turn.EndedAt,
			}
			byID[id] = summary
		}
		if summary.FirstTurnAt.IsZero() || turn.StartedAt.Before(summary.FirstTurnAt) {
			summary.FirstTurnAt = turn.StartedAt
		}
		if summary.LastTurnAt.IsZero() || turn.EndedAt.After(summary.LastTurnAt) {
			summary.LastTurnAt = turn.EndedAt
			summary.LastUserText = transcriptText(turn.UserText, 400)
			summary.LastFinalText = transcriptText(turn.FinalText, 400)
			summary.ConversationID = turn.ConversationID
			summary.Channel = turn.Channel
			summary.UserID = turn.UserID
			summary.Thread = turn.Thread
		}
		summary.Turns++
	}
	out := make([]transcriptSessionSummary, 0, len(byID))
	for _, summary := range byID {
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastTurnAt.After(out[j].LastTurnAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func queryTranscriptTurns(turns []transcriptTurn, query string, limit int, includeItems bool) []transcriptTurnResult {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	type scoredTurn struct {
		turn    transcriptTurn
		score   int
		snippet string
	}
	var scored []scoredTurn
	for _, turn := range turns {
		text := transcriptSearchText(turn)
		score := 0
		lower := strings.ToLower(text)
		for _, term := range terms {
			score += strings.Count(lower, term)
		}
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredTurn{
			turn:    turn,
			score:   score,
			snippet: transcriptSnippet(text, terms),
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].turn.EndedAt.After(scored[j].turn.EndedAt)
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]transcriptTurnResult, 0, len(scored))
	for _, item := range scored {
		result := transcriptTurnToResult(item.turn, includeItems)
		result.Score = item.score
		result.Snippet = item.snippet
		out = append(out, result)
	}
	return out
}

func sessionTranscriptTurns(turns []transcriptTurn, sessionID string, limit int, includeItems bool) []transcriptTurnResult {
	filtered := make([]transcriptTurn, 0)
	for _, turn := range turns {
		if turn.SessionID == sessionID {
			filtered = append(filtered, turn)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].StartedAt.Before(filtered[j].StartedAt)
	})
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	out := make([]transcriptTurnResult, 0, len(filtered))
	for _, turn := range filtered {
		out = append(out, transcriptTurnToResult(turn, includeItems))
	}
	return out
}

func scrollTranscriptTurns(turns []transcriptTurn, sessionID, aroundID string, window int, includeItems bool) []transcriptTurnResult {
	if window <= 0 {
		window = 5
	}
	if window > 25 {
		window = 25
	}
	filtered := make([]transcriptTurn, 0)
	for _, turn := range turns {
		if sessionID != "" && turn.SessionID != sessionID {
			continue
		}
		filtered = append(filtered, turn)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].StartedAt.Before(filtered[j].StartedAt)
	})
	index := -1
	for i, turn := range filtered {
		if turn.ID == aroundID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil
	}
	start := index - window
	if start < 0 {
		start = 0
	}
	end := index + window + 1
	if end > len(filtered) {
		end = len(filtered)
	}
	out := make([]transcriptTurnResult, 0, end-start)
	for _, turn := range filtered[start:end] {
		out = append(out, transcriptTurnToResult(turn, includeItems))
	}
	return out
}

func transcriptTurnToResult(turn transcriptTurn, includeItems bool) transcriptTurnResult {
	result := transcriptTurnResult{
		ID:             turn.ID,
		SessionID:      turn.SessionID,
		ConversationID: turn.ConversationID,
		Channel:        turn.Channel,
		UserID:         turn.UserID,
		Thread:         turn.Thread,
		Mode:           turn.Mode,
		StartedAt:      turn.StartedAt,
		EndedAt:        turn.EndedAt,
		UserText:       transcriptText(turn.UserText, 1200),
		FinalText:      transcriptText(turn.FinalText, 1200),
		Summary:        transcriptText(turn.Summary, 1200),
		ToolCalls:      turn.ToolCalls,
	}
	if includeItems {
		result.Items = turn.Items
	}
	return result
}

func transcriptSearchText(turn transcriptTurn) string {
	parts := []string{
		turn.UserText,
		turn.Prompt,
		turn.FinalText,
		turn.Summary,
		strings.Join(turn.ToolCalls, " "),
	}
	for _, item := range turn.Items {
		parts = append(parts, item.Text, item.Tool, item.Input, item.Content)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func queryTerms(query string) []string {
	raw := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z':
			return false
		case r >= '0' && r <= '9':
			return false
		default:
			return true
		}
	})
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, term := range raw {
		term = strings.TrimSpace(term)
		if len(term) < 2 {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

func transcriptSnippet(text string, terms []string) string {
	lower := strings.ToLower(text)
	index := -1
	for _, term := range terms {
		if i := strings.Index(lower, term); i >= 0 && (index < 0 || i < index) {
			index = i
		}
	}
	if index < 0 {
		return transcriptText(text, 240)
	}
	start := index - 120
	if start < 0 {
		start = 0
	}
	end := index + 240
	if end > len(text) {
		end = len(text)
	}
	snippet := strings.TrimSpace(text[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(text) {
		snippet += "..."
	}
	return transcriptText(snippet, 320)
}

func transcriptText(text string, max int) string {
	text = strings.TrimSpace(redactAuditText(text))
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "...(truncated)"
}
