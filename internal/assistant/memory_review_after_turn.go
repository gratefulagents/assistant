// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	memoryReviewModeOff     = "off"
	memoryReviewModePreview = "preview"
	memoryReviewModeApply   = "apply"
)

type memoryReviewRunFunc func(context.Context, appConfig, memoryReviewInput, io.Writer) (memoryDistillResult, error)

func normalizeMemoryReviewMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "false", "disabled", memoryReviewModeOff:
		return memoryReviewModeOff
	case "preview", "review", "dry-run", "dry_run":
		return memoryReviewModePreview
	case "apply", "auto", "automatic", "write", "remember":
		return memoryReviewModeApply
	default:
		return ""
	}
}

func triggerAfterTurnMemoryReview(ctx context.Context, cfg appConfig, since time.Time, stderr io.Writer, async bool) {
	if normalizeMemoryReviewMode(cfg.MemoryReviewMode) == memoryReviewModeOff {
		return
	}
	run := func(runCtx context.Context) {
		runAfterTurnMemoryReview(runCtx, cfg, since, stderr, reviewTranscriptMemories)
	}
	if async {
		go run(context.WithoutCancel(ctx))
		return
	}
	run(ctx)
}

func runAfterTurnMemoryReview(ctx context.Context, cfg appConfig, since time.Time, stderr io.Writer, reviewer memoryReviewRunFunc) {
	mode := normalizeMemoryReviewMode(cfg.MemoryReviewMode)
	if mode == memoryReviewModeOff {
		return
	}
	if mode == "" {
		memoryReviewLog(stderr, "warning: unsupported after-turn mode %q", cfg.MemoryReviewMode)
		return
	}
	if !cfg.EnableTranscripts || !cfg.EnableProjectState {
		memoryReviewLog(stderr, "warning: after-turn review requires transcripts and project-state")
		return
	}
	if cfg.Command == memoryReviewerName || cfg.Command == autoReviewerName {
		return
	}
	if since.IsZero() {
		since = time.Now().UTC().Add(-time.Minute)
	}
	if reviewer == nil {
		reviewer = reviewTranscriptMemories
	}
	limit := cfg.MemoryReviewLimit
	if limit <= 0 {
		limit = 8
	}
	if limit > 50 {
		limit = 50
	}
	result, err := reviewer(ctx, cfg, memoryReviewInput{
		Action:           mode,
		Since:            since.Add(-time.Second).UTC().Format(time.RFC3339Nano),
		Limit:            limit,
		IncludeHeuristic: true,
	}, stderr)
	if err != nil {
		memoryReviewLog(stderr, "warning: %v", err)
		return
	}
	logAfterTurnMemoryReviewResult(stderr, result)
}

func logAfterTurnMemoryReviewResult(stderr io.Writer, result memoryDistillResult) {
	switch result.Action {
	case memoryReviewModeApply:
		if len(result.Applied) == 0 {
			return
		}
		memoryReviewLog(stderr, "saved %d memories", len(result.Applied))
		for _, mem := range result.Applied {
			memoryReviewLog(stderr, "- %s", firstLine(mem.Content))
		}
	default:
		if len(result.Candidates) == 0 {
			return
		}
		memoryReviewLog(stderr, "%d candidate memories (preview; not saved)", len(result.Candidates))
		for _, candidate := range result.Candidates {
			memoryReviewLog(stderr, "- %s", firstLine(candidate.Content))
		}
	}
}

func memoryReviewLog(stderr io.Writer, format string, args ...any) {
	if stderr == nil {
		return
	}
	fmt.Fprintf(stderr, "[memory-review] "+format+"\n", args...)
}
