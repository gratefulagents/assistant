// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	scheduleStateFileName = "schedules.json"
	schedulerTickInterval = 30 * time.Second
)

var scheduleFileMu sync.Mutex

type scheduleState struct {
	Schedules []scheduleEntry `json:"schedules"`
}

type scheduleEntry struct {
	ID           string            `json:"id"`
	Name         string            `json:"name,omitempty"`
	Prompt       string            `json:"prompt"`
	Cron         string            `json:"cron,omitempty"`
	EverySeconds int               `json:"everySeconds,omitempty"`
	RunAt        string            `json:"runAt,omitempty"`
	Timezone     string            `json:"timezone,omitempty"`
	Deliver      *scheduleDelivery `json:"deliver,omitempty"`
	Enabled      bool              `json:"enabled"`
	NextRun      time.Time         `json:"nextRun,omitempty"`
	LastRun      time.Time         `json:"lastRun,omitempty"`
	LastError    string            `json:"lastError,omitempty"`
	LastOutput   string            `json:"lastOutput,omitempty"`
	RunCount     int               `json:"runCount,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

type scheduleDelivery struct {
	Channel string `json:"channel,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
}

type scheduleCreateInput struct {
	Name         string            `json:"name"`
	Prompt       string            `json:"prompt"`
	Cron         string            `json:"cron"`
	EverySeconds int               `json:"every_seconds"`
	RunAt        string            `json:"run_at"`
	Timezone     string            `json:"timezone"`
	Deliver      *scheduleDelivery `json:"deliver"`
	Enabled      *bool             `json:"enabled"`
}

type scheduleUpdateInput struct {
	ID           string            `json:"id"`
	Name         *string           `json:"name"`
	Prompt       *string           `json:"prompt"`
	Cron         *string           `json:"cron"`
	EverySeconds *int              `json:"every_seconds"`
	RunAt        *string           `json:"run_at"`
	Timezone     *string           `json:"timezone"`
	Deliver      *scheduleDelivery `json:"deliver"`
	Enabled      *bool             `json:"enabled"`
}

type scheduleRunInput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	AllowDisabled bool   `json:"allow_disabled"`
}

type scheduleRunResult struct {
	Schedule scheduleEntry `json:"schedule"`
	Output   string        `json:"output,omitempty"`
}

func runScheduler(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	fmt.Fprintf(stderr, "assistant scheduler watching %s\n", scheduleFilePath(cfg))
	if err := runDueSchedules(ctx, cfg, stdout, stderr); err != nil {
		return err
	}
	ticker := time.NewTicker(schedulerTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := runDueSchedules(ctx, cfg, stdout, stderr); err != nil {
				return err
			}
		}
	}
}

func runDueSchedules(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	due, err := claimDueSchedules(cfg, time.Now())
	if err != nil {
		return err
	}
	for _, entry := range due {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		label := scheduleLabel(entry)
		fmt.Fprintf(stderr, "assistant schedule %s running\n", label)
		reply, runErr := runPromptText(ctx, cfg, scheduledPrompt(entry), stdout, stderr)
		if runErr != nil {
			_ = finishScheduleRun(cfg, entry.ID, "", runErr)
			fmt.Fprintf(stderr, "assistant schedule %s failed: %v\n", label, runErr)
			continue
		}
		deliveryErr := deliverScheduleOutput(ctx, cfg, entry, reply)
		if err := finishScheduleRun(cfg, entry.ID, reply, deliveryErr); err != nil {
			return err
		}
		if deliveryErr != nil {
			fmt.Fprintf(stderr, "assistant schedule %s delivery failed: %v\n", label, deliveryErr)
			continue
		}
		fmt.Fprintf(stdout, "\n[schedule %s]\n%s\n", label, reply)
	}
	return nil
}

func runScheduleNow(ctx context.Context, cfg appConfig, in scheduleRunInput, stdout, stderr io.Writer) (scheduleRunResult, error) {
	entry, err := claimManualScheduleRun(cfg, in, time.Now())
	if err != nil {
		return scheduleRunResult{}, err
	}
	label := scheduleLabel(entry)
	if stderr != nil {
		fmt.Fprintf(stderr, "assistant schedule %s running manually\n", label)
	}
	reply, runErr := runPromptText(ctx, cfg, scheduledPrompt(entry), stdout, stderr)
	deliveryErr := error(nil)
	if runErr == nil {
		deliveryErr = deliverScheduleOutput(ctx, cfg, entry, reply)
	}
	finished, finishErr := finishScheduleRunEntry(cfg, entry.ID, reply, firstScheduleRunError(runErr, deliveryErr))
	if finishErr != nil {
		return scheduleRunResult{}, finishErr
	}
	result := scheduleRunResult{Schedule: finished}
	if runErr != nil {
		return result, runErr
	}
	if deliveryErr != nil {
		return result, deliveryErr
	}
	result.Output = reply
	return result, nil
}

func scheduledPrompt(entry scheduleEntry) string {
	label := scheduleLabel(entry)
	return "Scheduled assistant job " + label + " is due. Run this scheduled prompt:\n\n" + strings.TrimSpace(entry.Prompt)
}

func scheduleLabel(entry scheduleEntry) string {
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		return entry.ID
	}
	return fmt.Sprintf("%s (%s)", name, entry.ID)
}

func scheduleFilePath(cfg appConfig) string {
	return stateFilePath(cfg, scheduleStateFileName)
}

func createSchedule(cfg appConfig, in scheduleCreateInput) (scheduleEntry, error) {
	now := time.Now().UTC()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	entry := scheduleEntry{
		ID:           newScheduleID(),
		Name:         strings.TrimSpace(in.Name),
		Prompt:       strings.TrimSpace(in.Prompt),
		Cron:         strings.TrimSpace(in.Cron),
		EverySeconds: in.EverySeconds,
		RunAt:        strings.TrimSpace(in.RunAt),
		Timezone:     strings.TrimSpace(in.Timezone),
		Deliver:      normalizeScheduleDelivery(in.Deliver),
		Enabled:      enabled,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := validateSchedule(entry); err != nil {
		return scheduleEntry{}, err
	}
	if entry.Enabled {
		next, err := nextScheduleRun(entry, now)
		if err != nil {
			return scheduleEntry{}, err
		}
		entry.NextRun = next
	}

	scheduleFileMu.Lock()
	defer scheduleFileMu.Unlock()
	state, err := readScheduleStateLocked(cfg)
	if err != nil {
		return scheduleEntry{}, err
	}
	state.Schedules = append(state.Schedules, entry)
	if err := writeScheduleStateLocked(cfg, state); err != nil {
		return scheduleEntry{}, err
	}
	return entry, nil
}

func listSchedules(cfg appConfig) ([]scheduleEntry, error) {
	scheduleFileMu.Lock()
	defer scheduleFileMu.Unlock()
	state, err := readScheduleStateLocked(cfg)
	if err != nil {
		return nil, err
	}
	return append([]scheduleEntry(nil), state.Schedules...), nil
}

func getSchedule(cfg appConfig, id string) (scheduleEntry, bool, error) {
	scheduleFileMu.Lock()
	defer scheduleFileMu.Unlock()
	state, err := readScheduleStateLocked(cfg)
	if err != nil {
		return scheduleEntry{}, false, err
	}
	id = strings.TrimSpace(id)
	for _, entry := range state.Schedules {
		if entry.ID == id {
			return entry, true, nil
		}
	}
	return scheduleEntry{}, false, nil
}

func updateSchedule(cfg appConfig, in scheduleUpdateInput) (scheduleEntry, error) {
	if strings.TrimSpace(in.ID) == "" {
		return scheduleEntry{}, errors.New("id is required")
	}
	timingUpdates := 0
	if in.Cron != nil {
		timingUpdates++
	}
	if in.EverySeconds != nil {
		timingUpdates++
	}
	if in.RunAt != nil {
		timingUpdates++
	}
	if timingUpdates > 1 {
		return scheduleEntry{}, errors.New("update only one of cron, every_seconds, or run_at at a time")
	}

	now := time.Now().UTC()
	scheduleFileMu.Lock()
	defer scheduleFileMu.Unlock()
	state, err := readScheduleStateLocked(cfg)
	if err != nil {
		return scheduleEntry{}, err
	}
	for i := range state.Schedules {
		entry := state.Schedules[i]
		if entry.ID != strings.TrimSpace(in.ID) {
			continue
		}
		if in.Name != nil {
			entry.Name = strings.TrimSpace(*in.Name)
		}
		if in.Prompt != nil {
			entry.Prompt = strings.TrimSpace(*in.Prompt)
		}
		if in.Timezone != nil {
			entry.Timezone = strings.TrimSpace(*in.Timezone)
		}
		if in.Deliver != nil {
			entry.Deliver = normalizeScheduleDelivery(in.Deliver)
		}
		if in.Cron != nil {
			entry.Cron = strings.TrimSpace(*in.Cron)
			entry.EverySeconds = 0
			entry.RunAt = ""
		}
		if in.EverySeconds != nil {
			entry.EverySeconds = *in.EverySeconds
			entry.Cron = ""
			entry.RunAt = ""
		}
		if in.RunAt != nil {
			entry.RunAt = strings.TrimSpace(*in.RunAt)
			entry.Cron = ""
			entry.EverySeconds = 0
		}
		if in.Enabled != nil {
			entry.Enabled = *in.Enabled
		}
		if err := validateSchedule(entry); err != nil {
			return scheduleEntry{}, err
		}
		entry.UpdatedAt = now
		entry.LastError = ""
		if entry.Enabled {
			next, err := nextScheduleRun(entry, now)
			if err != nil {
				return scheduleEntry{}, err
			}
			entry.NextRun = next
		} else {
			entry.NextRun = time.Time{}
		}
		state.Schedules[i] = entry
		if err := writeScheduleStateLocked(cfg, state); err != nil {
			return scheduleEntry{}, err
		}
		return entry, nil
	}
	return scheduleEntry{}, fmt.Errorf("schedule %q not found", strings.TrimSpace(in.ID))
}

func deleteSchedule(cfg appConfig, id string) (scheduleEntry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return scheduleEntry{}, errors.New("id is required")
	}
	scheduleFileMu.Lock()
	defer scheduleFileMu.Unlock()
	state, err := readScheduleStateLocked(cfg)
	if err != nil {
		return scheduleEntry{}, err
	}
	for i, entry := range state.Schedules {
		if entry.ID != id {
			continue
		}
		state.Schedules = append(state.Schedules[:i], state.Schedules[i+1:]...)
		if err := writeScheduleStateLocked(cfg, state); err != nil {
			return scheduleEntry{}, err
		}
		return entry, nil
	}
	return scheduleEntry{}, fmt.Errorf("schedule %q not found", id)
}

func claimDueSchedules(cfg appConfig, now time.Time) ([]scheduleEntry, error) {
	now = now.UTC()
	scheduleFileMu.Lock()
	defer scheduleFileMu.Unlock()
	state, err := readScheduleStateLocked(cfg)
	if err != nil {
		return nil, err
	}
	changed := false
	var due []scheduleEntry
	for i := range state.Schedules {
		entry := state.Schedules[i]
		if !entry.Enabled {
			continue
		}
		if entry.NextRun.IsZero() {
			next, err := nextScheduleRun(entry, now)
			if err != nil {
				entry.LastError = err.Error()
				entry.UpdatedAt = now
				state.Schedules[i] = entry
				changed = true
				continue
			}
			entry.NextRun = next
			entry.UpdatedAt = now
			state.Schedules[i] = entry
			changed = true
		}
		if entry.NextRun.After(now) {
			continue
		}
		due = append(due, entry)
		entry.LastRun = now
		entry.RunCount++
		entry.LastError = ""
		entry.LastOutput = ""
		entry.UpdatedAt = now
		if strings.TrimSpace(entry.RunAt) != "" {
			entry.Enabled = false
			entry.NextRun = time.Time{}
		} else {
			next, err := nextScheduleRun(entry, now)
			if err != nil {
				entry.LastError = err.Error()
				entry.NextRun = time.Time{}
			} else {
				entry.NextRun = next
			}
		}
		state.Schedules[i] = entry
		changed = true
	}
	if changed {
		if err := writeScheduleStateLocked(cfg, state); err != nil {
			return nil, err
		}
	}
	return due, nil
}

func finishScheduleRun(cfg appConfig, id string, output string, runErr error) error {
	_, err := finishScheduleRunEntry(cfg, id, output, runErr)
	return err
}

func finishScheduleRunEntry(cfg appConfig, id string, output string, runErr error) (scheduleEntry, error) {
	now := time.Now().UTC()
	scheduleFileMu.Lock()
	defer scheduleFileMu.Unlock()
	state, err := readScheduleStateLocked(cfg)
	if err != nil {
		return scheduleEntry{}, err
	}
	for i := range state.Schedules {
		if state.Schedules[i].ID != id {
			continue
		}
		state.Schedules[i].UpdatedAt = now
		if strings.TrimSpace(output) != "" {
			state.Schedules[i].LastOutput = truncateScheduleOutput(output)
		} else {
			state.Schedules[i].LastOutput = ""
		}
		if runErr != nil {
			state.Schedules[i].LastError = runErr.Error()
		} else {
			state.Schedules[i].LastError = ""
		}
		entry := state.Schedules[i]
		if err := writeScheduleStateLocked(cfg, state); err != nil {
			return scheduleEntry{}, err
		}
		return entry, nil
	}
	return scheduleEntry{}, nil
}

func claimManualScheduleRun(cfg appConfig, in scheduleRunInput, now time.Time) (scheduleEntry, error) {
	id := strings.TrimSpace(in.ID)
	name := strings.TrimSpace(in.Name)
	if id == "" && name == "" {
		return scheduleEntry{}, errors.New("id or name is required")
	}
	if id != "" && name != "" {
		return scheduleEntry{}, errors.New("use only one of id or name")
	}

	now = now.UTC()
	scheduleFileMu.Lock()
	defer scheduleFileMu.Unlock()
	state, err := readScheduleStateLocked(cfg)
	if err != nil {
		return scheduleEntry{}, err
	}
	match := -1
	for i, entry := range state.Schedules {
		matches := entry.ID == id
		if name != "" {
			matches = entry.Name == name
		}
		if !matches {
			continue
		}
		if match >= 0 && name != "" {
			return scheduleEntry{}, fmt.Errorf("multiple schedules named %q; use id", name)
		}
		match = i
	}
	if match < 0 {
		if id != "" {
			return scheduleEntry{}, fmt.Errorf("schedule %q not found", id)
		}
		return scheduleEntry{}, fmt.Errorf("schedule named %q not found", name)
	}

	entry := state.Schedules[match]
	if !entry.Enabled && !in.AllowDisabled {
		return scheduleEntry{}, fmt.Errorf("schedule %q is disabled; set allow_disabled to true to run it manually", entry.ID)
	}
	if err := validateSchedule(entry); err != nil {
		return scheduleEntry{}, err
	}
	entry.LastRun = now
	entry.RunCount++
	entry.LastError = ""
	entry.LastOutput = ""
	entry.UpdatedAt = now
	state.Schedules[match] = entry
	if err := writeScheduleStateLocked(cfg, state); err != nil {
		return scheduleEntry{}, err
	}
	return entry, nil
}

func firstScheduleRunError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func hasConfiguredSchedules(cfg appConfig) (bool, error) {
	schedules, err := listSchedules(cfg)
	if err != nil {
		return false, err
	}
	for _, entry := range schedules {
		if entry.Enabled {
			return true, nil
		}
	}
	return false, nil
}

func validateSchedule(entry scheduleEntry) error {
	if strings.TrimSpace(entry.Prompt) == "" {
		return errors.New("prompt is required")
	}
	kinds := 0
	if strings.TrimSpace(entry.Cron) != "" {
		kinds++
	}
	if entry.EverySeconds > 0 {
		kinds++
	}
	if strings.TrimSpace(entry.RunAt) != "" {
		kinds++
	}
	if kinds != 1 {
		return errors.New("exactly one of cron, every_seconds, or run_at is required")
	}
	if _, err := scheduleLocation(entry.Timezone); err != nil {
		return err
	}
	if err := validateScheduleDelivery(entry.Deliver); err != nil {
		return err
	}
	if strings.TrimSpace(entry.Cron) != "" {
		if _, err := parseCronSchedule(entry.Cron); err != nil {
			return err
		}
	}
	if entry.EverySeconds > 0 && entry.EverySeconds < 10 {
		return errors.New("every_seconds must be at least 10")
	}
	if strings.TrimSpace(entry.RunAt) != "" {
		if _, err := parseRunAt(entry.RunAt, entry.Timezone); err != nil {
			return err
		}
	}
	return nil
}

func normalizeScheduleDelivery(delivery *scheduleDelivery) *scheduleDelivery {
	if delivery == nil {
		return nil
	}
	out := scheduleDelivery{
		Channel: strings.ToLower(strings.TrimSpace(delivery.Channel)),
		ChatID:  strings.TrimSpace(delivery.ChatID),
	}
	if out.Channel == "" && out.ChatID != "" {
		out.Channel = "telegram"
	}
	if out.Channel == "" && out.ChatID == "" {
		return nil
	}
	return &out
}

func validateScheduleDelivery(delivery *scheduleDelivery) error {
	if delivery == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(delivery.Channel)) {
	case "telegram":
		if _, err := parseScheduleTelegramChatID(delivery.ChatID); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported delivery channel %q", delivery.Channel)
	}
}

func deliverScheduleOutput(ctx context.Context, cfg appConfig, entry scheduleEntry, output string) error {
	if entry.Deliver == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(entry.Deliver.Channel)) {
	case "telegram":
		token := strings.TrimSpace(cfg.TelegramBotToken)
		if token == "" {
			return errors.New("telegram delivery requires --telegram-bot-token or ASSISTANT_TELEGRAM_BOT_TOKEN")
		}
		chatID, err := parseScheduleTelegramChatID(entry.Deliver.ChatID)
		if err != nil {
			return err
		}
		return postTelegramMessage(ctx, token, chatID, output)
	default:
		return fmt.Errorf("unsupported delivery channel %q", entry.Deliver.Channel)
	}
}

func parseScheduleTelegramChatID(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("telegram delivery requires chat_id")
	}
	chatID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || chatID == 0 {
		return 0, fmt.Errorf("telegram delivery chat_id must be a non-zero integer")
	}
	return chatID, nil
}

func nextScheduleRun(entry scheduleEntry, after time.Time) (time.Time, error) {
	if err := validateSchedule(entry); err != nil {
		return time.Time{}, err
	}
	if strings.TrimSpace(entry.RunAt) != "" {
		runAt, err := parseRunAt(entry.RunAt, entry.Timezone)
		if err != nil {
			return time.Time{}, err
		}
		return runAt.UTC(), nil
	}
	if entry.EverySeconds > 0 {
		every := time.Duration(entry.EverySeconds) * time.Second
		base := entry.CreatedAt
		if base.IsZero() {
			base = after
		}
		if entry.LastRun.After(base) {
			base = entry.LastRun
		}
		next := base.Add(every)
		for !next.After(after) {
			missed := int64(after.Sub(next)/every) + 1
			next = next.Add(time.Duration(missed) * every)
		}
		return next.UTC(), nil
	}
	loc, err := scheduleLocation(entry.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	cronSchedule, err := parseCronSchedule(entry.Cron)
	if err != nil {
		return time.Time{}, err
	}
	next := cronSchedule.Next(after.In(loc))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron %q did not produce a next run", entry.Cron)
	}
	return next.UTC(), nil
}

func readScheduleStateLocked(cfg appConfig) (scheduleState, error) {
	var state scheduleState
	_, err := readJSONFile(scheduleFilePath(cfg), &state)
	if err != nil {
		return scheduleState{}, err
	}
	if state.Schedules == nil {
		state.Schedules = []scheduleEntry{}
	}
	return state, nil
}

func writeScheduleStateLocked(cfg appConfig, state scheduleState) error {
	return writeJSONFile(scheduleFilePath(cfg), state)
}

func newScheduleID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return "sched_" + hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("sched_%d", time.Now().UnixNano())
}

func truncateScheduleOutput(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 4000 {
		return text
	}
	return text[:4000] + "...(truncated)"
}

func parseRunAt(value string, timezone string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("run_at is required")
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	loc, err := scheduleLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse run_at %q: use RFC3339 or YYYY-MM-DD HH:MM", value)
}

func scheduleLocation(timezone string) (*time.Location, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" || strings.EqualFold(timezone, "local") {
		return time.Local, nil
	}
	if strings.EqualFold(timezone, "utc") {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	return loc, nil
}

func parseCronSchedule(expr string) (cron.Schedule, error) {
	schedule, err := cron.ParseStandard(strings.TrimSpace(expr))
	if err != nil {
		return nil, fmt.Errorf("parse cron %q: %w", expr, err)
	}
	return schedule, nil
}

func scheduleJSON(value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
