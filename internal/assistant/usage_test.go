// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func testUsageConfig(t *testing.T, limit int64) appConfig {
	t.Helper()
	resetUsageStores()
	return appConfig{
		UserID:     "user-1",
		TokenLimit: limit,
		UsagePath:  filepath.Join(t.TempDir(), "usage.json"),
	}
}

func TestUsageStoreRoundTripAndPersist(t *testing.T) {
	cfg := testUsageConfig(t, 0)
	store, err := usageStoreFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddAt(time.Now(), agentsdk.Usage{InputTokens: 100, OutputTokens: 50, Requests: 1}); err != nil {
		t.Fatal(err)
	}
	// A fresh singleton (cache cleared) must reload the persisted totals.
	resetUsageStores()
	reloaded, err := usageStoreFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	snap := reloaded.Snapshot()
	if snap.InputTokens != 100 || snap.OutputTokens != 50 || snap.TotalTokens != 150 || snap.Requests != 1 {
		t.Fatalf("reloaded snapshot mismatch: %#v", snap)
	}
}

func TestUsageAddAccumulates(t *testing.T) {
	cfg := testUsageConfig(t, 0)
	store, _ := usageStoreFor(cfg)
	_ = store.AddAt(time.Now(), agentsdk.Usage{InputTokens: 10, OutputTokens: 5})
	_ = store.AddAt(time.Now(), agentsdk.Usage{InputTokens: 20, OutputTokens: 7})
	snap := store.Snapshot()
	if snap.TotalTokens != 42 {
		t.Fatalf("total = %d, want 42", snap.TotalTokens)
	}
}

func TestUsageMonthlyReset(t *testing.T) {
	cfg := testUsageConfig(t, 0)
	store, _ := usageStoreFor(cfg)
	// Seed usage attributed to two months ago, then read in the current month.
	twoMonthsAgo := time.Now().UTC().AddDate(0, -2, 0)
	if err := store.AddAt(twoMonthsAgo, agentsdk.Usage{InputTokens: 1000, OutputTokens: 1000}); err != nil {
		t.Fatal(err)
	}
	snap := store.Snapshot()
	if snap.TotalTokens != 0 {
		t.Fatalf("expected reset to 0 in new month, got %d", snap.TotalTokens)
	}
	if !snap.WindowStart.Equal(monthStart(time.Now())) {
		t.Fatalf("window not reset to current month: %v", snap.WindowStart)
	}
}

func TestUsageExceededBoundary(t *testing.T) {
	// limit 0 = unlimited
	unlimited, _ := usageStoreFor(testUsageConfig(t, 0))
	_ = unlimited.AddAt(time.Now(), agentsdk.Usage{InputTokens: 1_000_000})
	if unlimited.Exceeded() {
		t.Fatal("limit 0 should be unlimited")
	}

	// total == limit -> exceeded
	cfg := testUsageConfig(t, 100)
	store, _ := usageStoreFor(cfg)
	if store.Exceeded() {
		t.Fatal("empty store should not be exceeded")
	}
	_ = store.AddAt(time.Now(), agentsdk.Usage{InputTokens: 60, OutputTokens: 40})
	if !store.Exceeded() {
		t.Fatal("total at limit should be exceeded")
	}
}

func TestCheckAndStartUsage(t *testing.T) {
	cfg := testUsageConfig(t, 50)
	store, msg := checkAndStartUsage(cfg)
	if store == nil || msg != "" {
		t.Fatalf("under limit should pass: store=%v msg=%q", store, msg)
	}
	_ = store.AddAt(time.Now(), agentsdk.Usage{InputTokens: 50})
	_, msg = checkAndStartUsage(cfg)
	if msg == "" {
		t.Fatal("over limit should return a friendly message")
	}
}

func TestUsageLimitRefreshedOnReopen(t *testing.T) {
	cfg := testUsageConfig(t, 0)
	store, _ := usageStoreFor(cfg)
	_ = store.AddAt(time.Now(), agentsdk.Usage{InputTokens: 200})
	if store.Exceeded() {
		t.Fatal("unlimited yet")
	}
	// Lower the limit without clearing the singleton cache.
	cfg.TokenLimit = 100
	store2, _ := usageStoreFor(cfg)
	if !store2.Exceeded() {
		t.Fatal("limit change should take effect on reopen")
	}
}

// TestChannelEnforcesQuota verifies the over-limit channel path returns the
// friendly message without starting a model call. No provider is configured, so
// if the runner were invoked the call would error instead of returning the
// quota message.
func TestChannelEnforcesQuota(t *testing.T) {
	cfg := testUsageConfig(t, 10)
	cfg.Provider = providerOpenAIOAuth
	store, _ := usageStoreFor(cfg)
	_ = store.AddAt(time.Now(), agentsdk.Usage{InputTokens: 10})

	reply, err := runPromptText(context.Background(), cfg, "hello", io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != quotaExceededMessage(store.Snapshot()) {
		t.Fatalf("expected quota message, got %q", reply)
	}
}
