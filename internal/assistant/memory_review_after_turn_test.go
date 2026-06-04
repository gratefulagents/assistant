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
	runAfterTurnMemoryReview(t.Context(), cfg, "terminal", time.Now().UTC(), nil, func(context.Context, appConfig, memoryReviewInput, io.Writer) (memoryDistillResult, error) {
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
	runAfterTurnMemoryReview(t.Context(), cfg, "terminal", since, &stderr, func(_ context.Context, _ appConfig, in memoryReviewInput, _ io.Writer) (memoryDistillResult, error) {
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
	runAfterTurnMemoryReview(t.Context(), cfg, "terminal", time.Now().UTC(), &stderr, func(context.Context, appConfig, memoryReviewInput, io.Writer) (memoryDistillResult, error) {
		return memoryDistillResult{}, errors.New("review failed")
	})
	if !strings.Contains(stderr.String(), "review failed") {
		t.Fatalf("stderr missing error: %q", stderr.String())
	}
}

func TestRunAfterTurnMemoryReviewApplyTrustsLocalChannelOnly(t *testing.T) {
	cfg := transcriptTestConfig(t)
	cfg.EnableProjectState = true
	cfg.EnableTranscripts = true
	cfg.MemoryReviewMode = memoryReviewModeApply

	// Local terminal: apply stays apply and must not auto-include heuristic
	// candidates, which would bypass LLM review.
	var local memoryReviewInput
	runAfterTurnMemoryReview(t.Context(), cfg, "terminal", time.Now().UTC(), io.Discard, func(_ context.Context, _ appConfig, in memoryReviewInput, _ io.Writer) (memoryDistillResult, error) {
		local = in
		return memoryDistillResult{Action: in.Action}, nil
	})
	if local.Action != memoryReviewModeApply {
		t.Fatalf("local action = %q, want apply", local.Action)
	}
	if local.IncludeHeuristic {
		t.Fatal("apply must not auto-include heuristic candidates")
	}

	// Untrusted remote channel: apply is downgraded to preview so nothing is
	// written without a human at the terminal.
	var stderr strings.Builder
	var remote memoryReviewInput
	runAfterTurnMemoryReview(t.Context(), cfg, "telegram", time.Now().UTC(), &stderr, func(_ context.Context, _ appConfig, in memoryReviewInput, _ io.Writer) (memoryDistillResult, error) {
		remote = in
		return memoryDistillResult{Action: in.Action}, nil
	})
	if remote.Action != memoryReviewModePreview {
		t.Fatalf("remote action = %q, want preview downgrade", remote.Action)
	}
	if !strings.Contains(stderr.String(), "restricted to the local terminal") {
		t.Fatalf("stderr missing downgrade notice: %q", stderr.String())
	}
}

func TestIsLocalMemoryReviewChannel(t *testing.T) {
	for _, channel := range []string{"", "terminal", "cli", "CLI", " Terminal "} {
		if !isLocalMemoryReviewChannel(channel) {
			t.Fatalf("isLocalMemoryReviewChannel(%q) = false, want true", channel)
		}
	}
	for _, channel := range []string{"telegram", "gmail", "schedule", "generic"} {
		if isLocalMemoryReviewChannel(channel) {
			t.Fatalf("isLocalMemoryReviewChannel(%q) = true, want false", channel)
		}
	}
}
