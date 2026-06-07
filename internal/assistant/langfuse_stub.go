// SPDX-License-Identifier: GPL-3.0-only

//go:build !langfuse

// Zero-cost stubs for the optional Langfuse sink. These are compiled into every
// default build so the call site in usage_enforce.go needs no build guards,
// while the Langfuse client and its transitive dependency (github.com/google/uuid)
// stay out of the binary entirely. Build with `-tags langfuse` to swap in the
// real exporter in langfuse.go.
package assistant

import (
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

// langfuseTurn mirrors the real struct so callers (recordUsage) can construct it
// unconditionally; in the default build it is simply discarded.
type langfuseTurn struct {
	cfg       appConfig
	startTime time.Time
	endTime   time.Time
	usage     agentsdk.Usage
	meta      transcriptContext
	prompt    string
	finalText string
	items     []agentsdk.RunItem
}

// emitLangfuseTurn is a no-op when the Langfuse build tag is absent.
func emitLangfuseTurn(_ langfuseTurn) {}
