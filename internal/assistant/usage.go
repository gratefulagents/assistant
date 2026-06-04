// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

// usageRecord is the persisted per-user token accounting for the current
// monthly window. One assistant instance serves one user (identity via
// ASSISTANT_USER_ID), so a single record per instance is sufficient.
type usageRecord struct {
	UserID            string    `json:"user_id"`
	WindowStart       time.Time `json:"window_start"`
	InputTokens       int64     `json:"input_tokens"`
	OutputTokens      int64     `json:"output_tokens"`
	CacheReadTokens   int64     `json:"cache_read_tokens"`
	CacheCreateTokens int64     `json:"cache_create_tokens"`
	Requests          int64     `json:"requests"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// usageSnapshot is the read-only view returned to callers and the gateway. Its
// JSON tags define the GET /usage response shape.
type usageSnapshot struct {
	UserID            string    `json:"user_id"`
	WindowStart       time.Time `json:"window_start"`
	InputTokens       int64     `json:"input_tokens"`
	OutputTokens      int64     `json:"output_tokens"`
	TotalTokens       int64     `json:"total_tokens"`
	CacheReadTokens   int64     `json:"cache_read_tokens"`
	CacheCreateTokens int64     `json:"cache_create_tokens"`
	Requests          int64     `json:"requests"`
	Limit             int64     `json:"limit"`
	Remaining         int64     `json:"remaining"`
	Exceeded          bool      `json:"exceeded"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// usageStore is a process-level, file-backed token accountant. It is kept as a
// singleton per path (see usageStoreFor) so concurrent turns from telegram,
// gmail, and the gateway in a single process share one in-memory record and do
// not lose updates to each other via separate load-modify-write cycles.
type usageStore struct {
	mu    sync.Mutex
	db    *sql.DB
	rec   usageRecord
	limit int64
}

var (
	usageStoresMu sync.Mutex
	usageStores   = map[string]*usageStore{}
)

// usagePath resolves the on-disk location of the usage record.
func usagePath(cfg appConfig) string {
	if p := strings.TrimSpace(cfg.UsagePath); p != "" {
		return p
	}
	return stateFilePath(cfg, "usage.json")
}

// usageStoreFor returns the singleton usage store for the configured path,
// loading it from disk on first use. The mutable limit and user id are
// refreshed on every call so config changes take effect without a restart.
func usageStoreFor(cfg appConfig) (*usageStore, error) {
	path := expandUserPath(usagePath(cfg))
	// There is one "usage" row per state.db, so key the singleton by the
	// canonical DB directory rather than the file path. Two usage paths in the
	// same directory therefore share one in-memory accountant and never lose
	// increments to a stale sibling.
	dir := filepath.Dir(path)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	usageStoresMu.Lock()
	defer usageStoresMu.Unlock()
	if s, ok := usageStores[dir]; ok {
		s.refresh(cfg.UserID, cfg.TokenLimit)
		return s, nil
	}
	db, err := stateDBForDir(dir)
	if err != nil {
		return nil, err
	}
	s := &usageStore{db: db, limit: cfg.TokenLimit}
	var rec usageRecord
	exists, err := kvGetOrImport(db, "usage", path, &rec)
	if err != nil {
		return nil, err
	}
	if exists {
		s.rec = rec
	}
	s.refresh(cfg.UserID, cfg.TokenLimit)
	usageStores[dir] = s
	return s, nil
}

// resetUsageStores clears the singleton cache. It exists for tests so each test
// starts from a clean process-level state.
func resetUsageStores() {
	usageStoresMu.Lock()
	defer usageStoresMu.Unlock()
	usageStores = map[string]*usageStore{}
}

func (s *usageStore) refresh(userID string, limit int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit = limit
	if u := strings.TrimSpace(userID); u != "" {
		s.rec.UserID = u
	}
}

// monthStart returns the first instant of t's calendar month in UTC.
func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// rolloverLocked advances the window to ref's calendar month when ref is in a
// later month than the stored window, zeroing the counters. It never rolls the
// window backwards, so usage from a turn that started in an earlier month than
// the current window is simply added to the current window.
func (s *usageStore) rolloverLocked(ref time.Time) {
	start := monthStart(ref)
	if s.rec.WindowStart.IsZero() {
		s.rec.WindowStart = start
		return
	}
	if start.After(s.rec.WindowStart) {
		s.rec.WindowStart = start
		s.rec.InputTokens = 0
		s.rec.OutputTokens = 0
		s.rec.CacheReadTokens = 0
		s.rec.CacheCreateTokens = 0
		s.rec.Requests = 0
	}
}

func (s *usageStore) totalLocked() int64 {
	return s.rec.InputTokens + s.rec.OutputTokens
}

// Exceeded reports whether the user has reached or passed the configured limit
// for the current window. A limit of zero or less means unlimited.
func (s *usageStore) Exceeded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloverLocked(time.Now())
	return s.limit > 0 && s.totalLocked() >= s.limit
}

// Snapshot returns the current accounting for the active window.
func (s *usageStore) Snapshot() usageSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloverLocked(time.Now())
	total := s.totalLocked()
	remaining := int64(0)
	if s.limit > 0 {
		remaining = s.limit - total
		if remaining < 0 {
			remaining = 0
		}
	}
	return usageSnapshot{
		UserID:            s.rec.UserID,
		WindowStart:       s.rec.WindowStart,
		InputTokens:       s.rec.InputTokens,
		OutputTokens:      s.rec.OutputTokens,
		TotalTokens:       total,
		CacheReadTokens:   s.rec.CacheReadTokens,
		CacheCreateTokens: s.rec.CacheCreateTokens,
		Requests:          s.rec.Requests,
		Limit:             s.limit,
		Remaining:         remaining,
		Exceeded:          s.limit > 0 && total >= s.limit,
		UpdatedAt:         s.rec.UpdatedAt,
	}
}

// AddAt accumulates a turn's usage, attributing it to the calendar month of the
// turn's start time, and persists the record atomically. The in-memory counters
// are updated even if persistence fails so /usage and enforcement stay correct
// for the life of the process; the error is returned for logging.
func (s *usageStore) AddAt(started time.Time, u agentsdk.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if started.IsZero() {
		started = time.Now()
	}
	s.rolloverLocked(started)
	s.rec.InputTokens += u.InputTokens
	s.rec.OutputTokens += u.OutputTokens
	s.rec.CacheReadTokens += u.CacheReadTokens
	s.rec.CacheCreateTokens += u.CacheCreateTokens
	s.rec.Requests += int64(u.Requests)
	s.rec.UpdatedAt = time.Now().UTC()
	return kvPut(s.db, "usage", s.rec)
}

// quotaExceededMessage is the friendly reply returned to a user who has reached
// their monthly limit. It is surfaced on every channel (terminal, telegram,
// gmail, gateway) in place of starting a model call.
func quotaExceededMessage(s usageSnapshot) string {
	reset := monthStart(s.WindowStart.AddDate(0, 1, 0)).Format("2006-01-02")
	return "You've reached your monthly usage limit. Access resets on " + reset + " (UTC)."
}
