// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

// fakeWriteTool is a non-read-only tool used to exercise the read-only access
// filter and the filesystem-exempt wrapper.
type fakeWriteTool struct{ name string }

func (f fakeWriteTool) Name() string                 { return f.name }
func (f fakeWriteTool) Description() string          { return "fake" }
func (f fakeWriteTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f fakeWriteTool) Execute(context.Context, json.RawMessage, string) (agentsdk.ToolResult, error) {
	return agentsdk.ToolResult{}, nil
}
func (f fakeWriteTool) IsReadOnly() bool                    { return false }
func (f fakeWriteTool) IsEnabled(*agentsdk.RunContext) bool { return true }
func (f fakeWriteTool) NeedsApproval() bool                 { return false }
func (f fakeWriteTool) TimeoutSeconds() int                 { return 0 }

func TestMarkFilesystemExemptSurvivesReadOnlyFilter(t *testing.T) {
	raw := []agentsdk.Tool{fakeWriteTool{name: "memory_remember"}}

	// Without exemption, a write tool is stripped by the read-only filter.
	if got := agentsdk.FilterToolsByAccess(raw, "read-only"); len(got) != 0 {
		t.Fatalf("expected write tool to be filtered under read-only, got %d tools", len(got))
	}

	exempt := markFilesystemExempt(raw)
	if len(exempt) != 1 {
		t.Fatalf("markFilesystemExempt returned %d tools, want 1", len(exempt))
	}
	if !exempt[0].IsReadOnly() {
		t.Fatal("exempt tool must report IsReadOnly() == true")
	}
	if exempt[0].Name() != "memory_remember" {
		t.Fatalf("exempt tool name = %q, want delegated memory_remember", exempt[0].Name())
	}

	got := agentsdk.FilterToolsByAccess(exempt, "read-only")
	if len(got) != 1 || got[0].Name() != "memory_remember" {
		t.Fatalf("exempt tool did not survive read-only filter: %v", toolNames(got))
	}
}

func TestMarkFilesystemExemptEmpty(t *testing.T) {
	if got := markFilesystemExempt(nil); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}
