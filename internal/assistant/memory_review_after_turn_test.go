// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMemoryReviewMode(t *testing.T) {
	tests := map[string]string{
		"":          memoryReviewModeOff,
		"off":       memoryReviewModeOff,
		"disabled":  memoryReviewModeOff,
		"preview":   memoryReviewModePreview,
		"dry-run":   memoryReviewModePreview,
		"apply":     memoryReviewModeApply,
		"automatic": memoryReviewModeApply,
		"bogus":     "",
	}
	for input, want := range tests {
		if got := normalizeMemoryReviewMode(input); got != want {
			t.Fatalf("normalizeMemoryReviewMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateMemoryReviewConfig(t *testing.T) {
	cfg := transcriptTestConfig(t)
	cfg.MemoryReviewMode = "dry-run"
	cfg.MemoryReviewLimit = 999
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.MemoryReviewMode != memoryReviewModePreview {
		t.Fatalf("MemoryReviewMode = %q, want preview", cfg.MemoryReviewMode)
	}
	if cfg.MemoryReviewLimit != 50 {
		t.Fatalf("MemoryReviewLimit = %d, want capped at 50", cfg.MemoryReviewLimit)
	}

	cfg.MemoryReviewMode = "bogus"
	if err := cfg.validate(); err == nil {
		t.Fatal("validate succeeded with invalid memory review mode")
	}
}

func TestRunAfterTurnMemoryReviewSkipsWhenOff(t *testing.T) {
	cfg := transcriptTestConfig(t)
	cfg.MemoryReviewMode = memoryReviewModeOff
	called := false
	runAfterTurnMemoryReview(t.Context(), cfg, time.Now().UTC(), nil, func(context.Context, appConfig, memoryReviewInput, io.Writer) (memoryDistillResult, error) {
		called = true
		return memoryDistillResult{}, nil
	})
	if called {
		t.Fatal("reviewer called while mode is off")
	}
}

func TestRunAfterTurnMemoryReviewBuildsInputAndLogsPreview(t *testing.T) {
	cfg := transcriptTestConfig(t)
	cfg.EnableProjectState = true
	cfg.EnableTranscripts = true
	cfg.MemoryReviewMode = memoryReviewModePreview
	cfg.MemoryReviewLimit = 3
	since := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	var stderr strings.Builder
	var got memoryReviewInput
	runAfterTurnMemoryReview(t.Context(), cfg, since, &stderr, func(_ context.Context, _ appConfig, in memoryReviewInput, _ io.Writer) (memoryDistillResult, error) {
		got = in
		return memoryDistillResult{
			Action: memoryReviewModePreview,
			Candidates: []memoryDistillCandidate{{
				Content: "User prefers compact answers.",
			}},
		}, nil
	})
	if got.Action != memoryReviewModePreview || got.Limit != 3 || !got.IncludeHeuristic {
		t.Fatalf("review input = %#v", got)
	}
	if got.Since == "" || !strings.Contains(got.Since, "2026-06-03T11:59:59") {
		t.Fatalf("since = %q", got.Since)
	}
	if !strings.Contains(stderr.String(), "candidate memories") || !strings.Contains(stderr.String(), "compact answers") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunAfterTurnMemoryReviewLogsApplyAndErrors(t *testing.T) {
	cfg := transcriptTestConfig(t)
	cfg.EnableProjectState = true
	cfg.EnableTranscripts = true
	cfg.MemoryReviewMode = memoryReviewModeApply
	var stderr strings.Builder
	runAfterTurnMemoryReview(t.Context(), cfg, time.Now().UTC(), &stderr, func(context.Context, appConfig, memoryReviewInput, io.Writer) (memoryDistillResult, error) {
		return memoryDistillResult{}, errors.New("review failed")
	})
	if !strings.Contains(stderr.String(), "review failed") {
		t.Fatalf("stderr missing error: %q", stderr.String())
	}
}
