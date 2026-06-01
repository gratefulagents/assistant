// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"path/filepath"
	"testing"
	"time"
)

func TestScheduleCronNextRunUsesCronLibrary(t *testing.T) {
	entry := scheduleEntry{
		Prompt:   "send weekday summary",
		Cron:     "30 9 * * MON-FRI",
		Timezone: "UTC",
	}
	after := time.Date(2026, time.May, 29, 9, 31, 0, 0, time.UTC)
	next, err := nextScheduleRun(entry, after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.June, 1, 9, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s, want %s", next, want)
	}
}

func TestScheduleCreateClaimAndDelete(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.StateDir = filepath.Join(t.TempDir(), "state")

	created, err := createSchedule(cfg, scheduleCreateInput{
		Name:         "test",
		Prompt:       "check in",
		EverySeconds: 10,
		Timezone:     "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.NextRun.IsZero() {
		t.Fatalf("bad created schedule: %#v", created)
	}

	due, err := claimDueSchedules(cfg, created.NextRun.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != created.ID {
		t.Fatalf("due=%#v, want created schedule", due)
	}

	claimed, ok, err := getSchedule(cfg, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("created schedule missing after claim")
	}
	if claimed.RunCount != 1 || claimed.LastRun.IsZero() || claimed.NextRun.IsZero() {
		t.Fatalf("bad claimed schedule: %#v", claimed)
	}

	deleted, err := deleteSchedule(cfg, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ID != created.ID {
		t.Fatalf("deleted=%#v, want %s", deleted, created.ID)
	}
	schedules, err := listSchedules(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 0 {
		t.Fatalf("schedules after delete=%#v, want empty", schedules)
	}
}

func TestScheduleManualRunKeepsNextRun(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.StateDir = filepath.Join(t.TempDir(), "state")

	created, err := createSchedule(cfg, scheduleCreateInput{
		Name:         "morning summary",
		Prompt:       "summarize things",
		EverySeconds: 3600,
		Timezone:     "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	originalNextRun := created.NextRun
	manualRunAt := created.CreatedAt.Add(5 * time.Minute)

	claimed, err := claimManualScheduleRun(cfg, scheduleRunInput{Name: "morning summary"}, manualRunAt)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != created.ID {
		t.Fatalf("claimed id = %q, want %q", claimed.ID, created.ID)
	}
	if claimed.RunCount != 1 || !claimed.LastRun.Equal(manualRunAt.UTC()) {
		t.Fatalf("bad manual claim metadata: %#v", claimed)
	}
	if !claimed.NextRun.Equal(originalNextRun) {
		t.Fatalf("manual run changed next run: got %s want %s", claimed.NextRun, originalNextRun)
	}

	finished, err := finishScheduleRunEntry(cfg, created.ID, "done", nil)
	if err != nil {
		t.Fatal(err)
	}
	if finished.LastOutput != "done" || finished.LastError != "" {
		t.Fatalf("bad finished schedule: %#v", finished)
	}
	if !finished.NextRun.Equal(originalNextRun) {
		t.Fatalf("finish changed next run: got %s want %s", finished.NextRun, originalNextRun)
	}
}

func TestScheduleManualRunDisabledRequiresOptIn(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	enabled := false

	created, err := createSchedule(cfg, scheduleCreateInput{
		Name:         "disabled",
		Prompt:       "run only manually",
		EverySeconds: 3600,
		Enabled:      &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimManualScheduleRun(cfg, scheduleRunInput{ID: created.ID}, time.Now()); err == nil {
		t.Fatal("expected disabled schedule to require allow_disabled")
	}
	claimed, err := claimManualScheduleRun(cfg, scheduleRunInput{ID: created.ID, AllowDisabled: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != created.ID || claimed.RunCount != 1 {
		t.Fatalf("bad disabled manual claim: %#v", claimed)
	}
}

func TestScheduleToolsExposedWhenEnabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConfigPath = ""
	cfg.WorkDir = t.TempDir()
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.EnableProjectState = false
	cfg.EnableScheduling = true

	extensions, err := loadExtensions(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range extensions.ExtraTools {
		names[tool.Name()] = true
	}
	for _, want := range []string{"schedule_create", "schedule_list", "schedule_get", "schedule_update", "schedule_delete", "schedule_run"} {
		if !names[want] {
			t.Fatalf("missing schedule tool %q; names=%v", want, names)
		}
	}
}
