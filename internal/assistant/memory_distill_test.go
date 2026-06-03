// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"strings"
	"testing"
	"time"

	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
)

func TestMemoryDistillPreviewApplyAndSkipExisting(t *testing.T) {
	cfg := transcriptTestConfig(t)
	now := time.Now().UTC()
	for i, text := range []string{
		"I prefer compact answers.",
		"I usually start code searches with rg.",
	} {
		if err := appendTranscriptTurn(cfg, transcriptTurn{
			ID:        "turn_distill_" + string(rune('a'+i)),
			SessionID: "sess_distill",
			Channel:   "test",
			StartedAt: now.Add(time.Duration(i) * time.Minute),
			EndedAt:   now.Add(time.Duration(i)*time.Minute + time.Second),
			UserText:  text,
			FinalText: "noted",
		}); err != nil {
			t.Fatal(err)
		}
	}

	preview, err := distillTranscriptMemories(t.Context(), cfg, memoryDistillInput{SinceHours: 1})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Action != memoryDistillActionPreview || preview.ScannedTurns != 2 {
		t.Fatalf("preview metadata = %#v", preview)
	}
	if len(preview.Candidates) != 2 {
		t.Fatalf("preview candidates = %#v, want 2", preview.Candidates)
	}
	if !containsDistillCandidate(preview.Candidates, "User prefers compact answers.") {
		t.Fatalf("preview missing preference candidate: %#v", preview.Candidates)
	}

	applied, err := distillTranscriptMemories(t.Context(), cfg, memoryDistillInput{Action: memoryDistillActionApply, SinceHours: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Applied) != 2 {
		t.Fatalf("applied memories = %#v, want 2", applied.Applied)
	}

	store, err := newMemoryStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	memories, err := store.ListMemories(t.Context(), sdkprojectstate.MemoryFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 2 {
		t.Fatalf("stored memories = %#v, want 2", memories)
	}

	again, err := distillTranscriptMemories(t.Context(), cfg, memoryDistillInput{SinceHours: 1, IncludeSkipped: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Candidates) != 0 || len(again.Skipped) != 2 {
		t.Fatalf("duplicate handling = %#v, want no candidates and 2 skipped", again)
	}
}

func TestMemoryDistillToolRegisteredWithProjectStateAndTranscripts(t *testing.T) {
	cfg := transcriptTestConfig(t)
	cfg.EnableProjectState = true
	cfg.EnableTranscripts = true
	extensions, err := loadExtensions(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range extensions.ExtraTools {
		names[tool.Name()] = true
	}
	for _, want := range []string{"session_search", "memory_distill"} {
		if !names[want] {
			t.Fatalf("missing tool %q; names=%v", want, names)
		}
	}
}

func containsDistillCandidate(candidates []memoryDistillCandidate, content string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.Content, content) {
			return true
		}
	}
	return false
}
