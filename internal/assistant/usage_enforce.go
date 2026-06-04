// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"fmt"
	"io"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

// checkAndStartUsage opens the per-user usage store and reports whether the user
// is already over their monthly token limit. When over limit it returns a
// friendly message that callers surface in place of starting a model call. The
// returned store is used after a successful turn to record consumed usage.
//
// Store-open failures fail open (allow the turn) rather than block the user on
// an accounting problem; the error is logged by the caller path via recordUsage.
func checkAndStartUsage(cfg appConfig) (*usageStore, string) {
	store, err := usageStoreFor(cfg)
	if err != nil {
		return nil, ""
	}
	if store.Exceeded() {
		return store, quotaExceededMessage(store.Snapshot())
	}
	return store, ""
}

// recordUsage accumulates a completed turn's token usage into the local store
// (for enforcement and GET /usage) and best-effort exports it to Langfuse for
// observability. Langfuse is never on the enforcement hot path.
func recordUsage(cfg appConfig, store *usageStore, started time.Time, usage agentsdk.Usage, channel string, stderr io.Writer) {
	if store != nil {
		if err := store.AddAt(started, usage); err != nil && stderr != nil {
			fmt.Fprintln(stderr, "[usage] persist warning:", err)
		}
	}
	emitLangfuseUsage(cfg, started, time.Now().UTC(), usage, channel)
}
