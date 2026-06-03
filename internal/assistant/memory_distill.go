// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
)

const (
	memoryDistillActionPreview = "preview"
	memoryDistillActionApply   = "apply"
)

type memoryDistillInput struct {
	Action         string  `json:"action"`
	Since          string  `json:"since"`
	SinceHours     int     `json:"since_hours"`
	Limit          int     `json:"limit"`
	MinConfidence  float64 `json:"min_confidence"`
	IncludeSkipped bool    `json:"include_skipped"`
}

type memoryDistillCandidate struct {
	Content          string    `json:"content"`
	Kind             string    `json:"kind"`
	Scope            string    `json:"scope"`
	Tags             []string  `json:"tags,omitempty"`
	Confidence       float64   `json:"confidence"`
	Reason           string    `json:"reason"`
	SourceTurnID     string    `json:"source_turn_id"`
	SourceSessionID  string    `json:"source_session_id"`
	SourceTime       time.Time `json:"source_time"`
	ExistingMemoryID string    `json:"existing_memory_id,omitempty"`
}

type memoryDistillResult struct {
	Action       string                   `json:"action"`
	Since        time.Time                `json:"since"`
	ScannedTurns int                      `json:"scanned_turns"`
	Candidates   []memoryDistillCandidate `json:"candidates"`
	Applied      []sdkprojectstate.Memory `json:"applied,omitempty"`
	Skipped      []memoryDistillCandidate `json:"skipped,omitempty"`
}

type memoryCandidatePattern struct {
	Re         *regexp.Regexp
	Template   string
	Kind       string
	Scope      string
	Tags       []string
	Confidence float64
	Reason     string
}

var memoryCandidatePatterns = []memoryCandidatePattern{
	{
		Re:         regexp.MustCompile(`(?i)\b(?:please\s+)?remember(?:\s+this)?(?:\s+for\s+me)?\s*[:\-]?\s*(?:that\s+)?(.{4,240})`),
		Template:   "$1",
		Kind:       sdkprojectstate.MemoryKindSemantic,
		Scope:      sdkprojectstate.MemoryScopeUser,
		Tags:       []string{"distilled", "explicit"},
		Confidence: 0.95,
		Reason:     "explicit remember request",
	},
	{
		Re:         regexp.MustCompile(`(?i)\bcall me\s+(.{2,80})`),
		Template:   "User prefers to be called $1.",
		Kind:       sdkprojectstate.MemoryKindSemantic,
		Scope:      sdkprojectstate.MemoryScopeUser,
		Tags:       []string{"distilled", "profile"},
		Confidence: 0.9,
		Reason:     "preferred name",
	},
	{
		Re:         regexp.MustCompile(`(?i)\bmy preferred\s+(.{2,80}?)\s+is\s+(.{2,120})`),
		Template:   "User's preferred $1 is $2.",
		Kind:       sdkprojectstate.MemoryKindSemantic,
		Scope:      sdkprojectstate.MemoryScopeUser,
		Tags:       []string{"distilled", "preference"},
		Confidence: 0.85,
		Reason:     "explicit preference",
	},
	{
		Re:         regexp.MustCompile(`(?i)\bi prefer\s+(.{3,180})`),
		Template:   "User prefers $1.",
		Kind:       sdkprojectstate.MemoryKindSemantic,
		Scope:      sdkprojectstate.MemoryScopeUser,
		Tags:       []string{"distilled", "preference"},
		Confidence: 0.8,
		Reason:     "explicit preference",
	},
	{
		Re:         regexp.MustCompile(`(?i)\bi usually\s+(.{3,180})`),
		Template:   "User usually $1.",
		Kind:       sdkprojectstate.MemoryKindProcedural,
		Scope:      sdkprojectstate.MemoryScopeUser,
		Tags:       []string{"distilled", "routine"},
		Confidence: 0.76,
		Reason:     "user routine",
	},
	{
		Re:         regexp.MustCompile(`(?i)\bi always\s+(.{3,180})`),
		Template:   "User always $1.",
		Kind:       sdkprojectstate.MemoryKindProcedural,
		Scope:      sdkprojectstate.MemoryScopeUser,
		Tags:       []string{"distilled", "routine"},
		Confidence: 0.78,
		Reason:     "user routine",
	},
	{
		Re:         regexp.MustCompile(`(?i)\bi never\s+(.{3,180})`),
		Template:   "User never $1.",
		Kind:       sdkprojectstate.MemoryKindSemantic,
		Scope:      sdkprojectstate.MemoryScopeUser,
		Tags:       []string{"distilled", "preference"},
		Confidence: 0.78,
		Reason:     "negative preference",
	},
	{
		Re:         regexp.MustCompile(`(?i)\bi use\s+(.{3,180})`),
		Template:   "User uses $1.",
		Kind:       sdkprojectstate.MemoryKindSemantic,
		Scope:      sdkprojectstate.MemoryScopeUser,
		Tags:       []string{"distilled", "tools"},
		Confidence: 0.7,
		Reason:     "tool or workflow fact",
	},
}

func distillTranscriptMemories(ctx context.Context, cfg appConfig, in memoryDistillInput) (memoryDistillResult, error) {
	action := normalizeMemoryDistillAction(in.Action)
	if action == "" {
		return memoryDistillResult{}, fmt.Errorf("action must be preview or apply")
	}
	since, err := memoryDistillSince(in)
	if err != nil {
		return memoryDistillResult{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	minConfidence := in.MinConfidence
	if minConfidence <= 0 {
		minConfidence = 0.75
	}

	turns, err := readTranscriptTurns(ctx, cfg)
	if err != nil {
		return memoryDistillResult{}, err
	}
	turns = recentTurnsSince(turns, since, limit)
	candidates := extractMemoryCandidates(turns, minConfidence)
	result := memoryDistillResult{
		Action:       action,
		Since:        since,
		ScannedTurns: len(turns),
	}
	return finalizeMemoryCandidates(ctx, cfg, result, candidates, action, in.IncludeSkipped)
}

func normalizeMemoryDistillAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", memoryDistillActionPreview:
		return memoryDistillActionPreview
	case memoryDistillActionApply, "write", "remember":
		return memoryDistillActionApply
	default:
		return ""
	}
}

func memoryDistillSince(in memoryDistillInput) (time.Time, error) {
	if value := strings.TrimSpace(in.Since); value != "" {
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse since as RFC3339: %w", err)
		}
		return t.UTC(), nil
	}
	hours := in.SinceHours
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*365 {
		hours = 24 * 365
	}
	return time.Now().UTC().Add(-time.Duration(hours) * time.Hour), nil
}

func recentTurnsSince(turns []transcriptTurn, since time.Time, limit int) []transcriptTurn {
	filtered := make([]transcriptTurn, 0, len(turns))
	for _, turn := range turns {
		t := turn.EndedAt
		if t.IsZero() {
			t = turn.StartedAt
		}
		if t.IsZero() || t.Before(since) {
			continue
		}
		filtered = append(filtered, turn)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].StartedAt.Before(filtered[j].StartedAt)
	})
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered
}

func extractMemoryCandidates(turns []transcriptTurn, minConfidence float64) []memoryDistillCandidate {
	seen := map[string]struct{}{}
	var out []memoryDistillCandidate
	for _, turn := range turns {
		for _, sentence := range memoryCandidateSentences(turn.UserText) {
			for _, pattern := range memoryCandidatePatterns {
				matches := pattern.Re.FindStringSubmatch(sentence)
				if len(matches) == 0 {
					continue
				}
				content := renderMemoryCandidate(pattern.Template, matches)
				content = cleanMemoryCandidateContent(content)
				if !validMemoryCandidateContent(content) || pattern.Confidence < minConfidence {
					continue
				}
				key := normalizedMemoryContent(content)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, memoryDistillCandidate{
					Content:         content,
					Kind:            pattern.Kind,
					Scope:           pattern.Scope,
					Tags:            pattern.Tags,
					Confidence:      pattern.Confidence,
					Reason:          pattern.Reason,
					SourceTurnID:    turn.ID,
					SourceSessionID: turn.SessionID,
					SourceTime:      turn.EndedAt,
				})
			}
		}
	}
	return out
}

func memoryCandidateSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	raw := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '.' || r == '!' || r == '?'
	})
	out := make([]string, 0, len(raw))
	for _, sentence := range raw {
		sentence = strings.TrimSpace(sentence)
		if len(sentence) < 6 {
			continue
		}
		out = append(out, sentence)
	}
	return out
}

func renderMemoryCandidate(template string, matches []string) string {
	out := template
	for i := len(matches) - 1; i >= 1; i-- {
		out = strings.ReplaceAll(out, "$"+fmt.Sprint(i), strings.TrimSpace(matches[i]))
	}
	return out
}

func cleanMemoryCandidateContent(content string) string {
	content = transcriptText(content, 280)
	content = strings.Trim(content, " \t\r\n\"'`")
	content = strings.TrimSuffix(content, ",")
	content = strings.TrimSuffix(content, ";")
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if !strings.HasSuffix(content, ".") {
		content += "."
	}
	return content
}

func validMemoryCandidateContent(content string) bool {
	if len(content) < 8 || len(content) > 320 {
		return false
	}
	lower := strings.ToLower(content)
	for _, phrase := range []string{
		" do not remember",
		" don't remember",
		" forget ",
		"ignore previous",
		"system prompt",
		"access token",
		"api key",
		"password",
		"[redacted]",
	} {
		if strings.Contains(lower, phrase) {
			return false
		}
	}
	return true
}

func existingMemoryByContent(memories []sdkprojectstate.Memory) map[string]string {
	out := map[string]string{}
	for _, mem := range memories {
		key := normalizedMemoryContent(mem.Content)
		if key == "" {
			continue
		}
		out[key] = mem.ID
	}
	return out
}

func normalizedMemoryContent(content string) string {
	content = strings.ToLower(strings.TrimSpace(content))
	content = strings.TrimSuffix(content, ".")
	fields := strings.Fields(content)
	return strings.Join(fields, " ")
}

func memoryDistillMetadata(candidate memoryDistillCandidate) json.RawMessage {
	data, err := json.Marshal(map[string]any{
		"source":            "transcript_distill",
		"source_turn_id":    candidate.SourceTurnID,
		"source_session_id": candidate.SourceSessionID,
		"source_time":       candidate.SourceTime,
		"confidence":        candidate.Confidence,
		"reason":            candidate.Reason,
	})
	if err != nil {
		return nil
	}
	return data
}
