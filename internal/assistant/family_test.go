// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeDockerName(t *testing.T) {
	tests := map[string]string{
		"Alice":       "alice",
		"Bob Smith":   "bob-smith",
		"  Carl!  ":   "carl",
		"x@y.z":       "x-y.z",
		"":            "member",
		"---":         "member",
		"José":        "jos",
		"under_score": "under_score",
	}
	for in, want := range tests {
		if got := sanitizeDockerName(in); got != want {
			t.Errorf("sanitizeDockerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFamilyConfigApplyDefaults(t *testing.T) {
	cfg := familyConfig{Members: []familyMember{{Name: "Alice"}, {Name: "Bob", Role: "FREELOADER"}}}
	cfg.applyDefaults()

	if cfg.Image != defaultFamilyImage {
		t.Errorf("image = %q, want %q", cfg.Image, defaultFamilyImage)
	}
	if cfg.Provider != providerOpenAIOAuth {
		t.Errorf("provider = %q, want %q", cfg.Provider, providerOpenAIOAuth)
	}
	if cfg.User != defaultFamilyUser {
		t.Errorf("user = %q, want %q", cfg.User, defaultFamilyUser)
	}
	if cfg.Refresher.Container != defaultFamilyRefresher {
		t.Errorf("refresher container = %q, want %q", cfg.Refresher.Container, defaultFamilyRefresher)
	}
	if cfg.Refresher.Interval != defaultOAuthRefreshIntervalText {
		t.Errorf("refresher interval = %q, want %q", cfg.Refresher.Interval, defaultOAuthRefreshIntervalText)
	}
	if got, want := cfg.Members[0].Container, "assistant-family-alice"; got != want {
		t.Errorf("member[0].Container = %q, want %q", got, want)
	}
	if got, want := cfg.Members[0].Volume, "assistant-family-alice-state"; got != want {
		t.Errorf("member[0].Volume = %q, want %q", got, want)
	}
	if got, want := cfg.Members[1].Container, "assistant-freeloader-bob"; got != want {
		t.Errorf("member[1].Container = %q, want %q", got, want)
	}
}

func TestFamilyRefresherRunArgs(t *testing.T) {
	cfg := familyConfig{
		Image:    "ghcr.io/example/assistant:1.2.3",
		Provider: providerOpenAIOAuth,
		Restart:  "unless-stopped",
		User:     "0:0",
		Refresher: familyRefresher{
			Container: "assistant-oauth-refresher",
			Interval:  "1h",
		},
	}
	args := familyRefresherRunArgs(cfg, "/host/auth.json")
	joined := strings.Join(args, " ")

	wants := []string{
		"run -d",
		"--name assistant-oauth-refresher",
		"--restart unless-stopped",
		"--label com.gratefulagents.assistant.role=oauth-refresher",
		"-v /host/auth.json:" + familyOAuthMount,
		"--user 0:0",
		"ghcr.io/example/assistant:1.2.3 oauth-refresh",
		"--openai-oauth-path " + familyOAuthMount,
		"--oauth-refresh-interval 1h",
	}
	for _, want := range wants {
		if !strings.Contains(joined, want) {
			t.Errorf("refresher args missing %q\n got: %s", want, joined)
		}
	}
	if strings.Contains(joined, ":ro") {
		t.Errorf("refresher auth mount should be writable, got: %s", joined)
	}
}

func TestFamilyRunArgs(t *testing.T) {
	cfg := familyConfig{
		Image:    "ghcr.io/example/assistant:1.2.3",
		Provider: providerOpenAIOAuth,
		Restart:  "unless-stopped",
		User:     "0:0",
	}
	m := familyMember{
		Name:                 "Alice",
		Role:                 "family",
		Container:            "assistant-family-alice",
		Volume:               "assistant-family-alice-state",
		TelegramBotToken:     "123:abc",
		TelegramAllowedUsers: []string{"42"},
		Settings:             assistantSettings{Model: "gpt-test"},
	}
	args := familyRunArgs(cfg, m, "/host/auth.json")
	joined := strings.Join(args, " ")

	wants := []string{
		"run -d",
		"--name assistant-family-alice",
		"--restart unless-stopped",
		"-v assistant-family-alice-state:" + familyStateMount,
		"-v /host/auth.json:" + familyOAuthMount + ":ro",
		"--user 0:0",
		"-e ASSISTANT_TELEGRAM_BOT_TOKEN=123:abc",
		"ghcr.io/example/assistant:1.2.3 telegram",
		"--provider openai-oauth",
		"--openai-oauth-path " + familyOAuthMount,
		"--openai-oauth-refresh=false",
		"--state-dir " + familyStateMount,
		"--model gpt-test",
		"--telegram-allowed-user 42",
	}
	for _, want := range wants {
		if !strings.Contains(joined, want) {
			t.Errorf("run args missing %q\n got: %s", want, joined)
		}
	}
}

func TestSaveAndLoadFamilyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assistant.yaml")
	in := familyConfig{
		Members: []familyMember{
			{Name: "Alice", TelegramBotToken: "1:a", TelegramAllowedUsers: []string{"111"}},
			{Name: "Bob", Role: familyRoleFreeloader, TelegramBotToken: "2:b", TelegramAllowedUsers: []string{"222"}},
		},
	}
	in.applyDefaults()
	if err := saveFamilyConfig(path, in); err != nil {
		t.Fatalf("saveFamilyConfig: %v", err)
	}
	out, err := loadFamilyConfig(path)
	if err != nil {
		t.Fatalf("loadFamilyConfig: %v", err)
	}
	if len(out.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(out.Members))
	}
	if out.Members[1].Role != familyRoleFreeloader {
		t.Errorf("member[1].Role = %q, want %q", out.Members[1].Role, familyRoleFreeloader)
	}
	if out.Image != defaultFamilyImage {
		t.Errorf("image = %q, want %q", out.Image, defaultFamilyImage)
	}
	if out.Refresher.Interval != defaultOAuthRefreshIntervalText {
		t.Errorf("refresher interval = %q, want %q", out.Refresher.Interval, defaultOAuthRefreshIntervalText)
	}
}

func TestFamilyConfigValidateRequiresTokenAndAllowList(t *testing.T) {
	base := func() familyConfig {
		c := familyConfig{Members: []familyMember{{
			Name:                 "Alice",
			TelegramBotToken:     "1:a",
			TelegramAllowedUsers: []string{"111"},
		}}}
		c.applyDefaults()
		return c
	}
	if err := base().validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	noToken := base()
	noToken.Members[0].TelegramBotToken = ""
	if err := noToken.validate(); err == nil {
		t.Error("expected error when telegramBotToken is empty")
	}

	noAllow := base()
	noAllow.Members[0].TelegramAllowedUsers = nil
	if err := noAllow.validate(); err == nil {
		t.Error("expected error when telegramAllowedUsers is empty")
	}

	duplicateRefresher := base()
	duplicateRefresher.Members[0].Container = duplicateRefresher.Refresher.Container
	if err := duplicateRefresher.validate(); err == nil {
		t.Error("expected error when a member uses the refresher container name")
	}
}

func TestFamilySettingsMergeAndRender(t *testing.T) {
	on := true
	off := false
	turns := 20
	defaults := assistantSettings{
		Reasoning: "high",
		MaxTurns:  &turns,
		Tools:     &on,
		Skills:    &on,
	}
	override := assistantSettings{
		Reasoning: "low", // member overrides default
		Skills:    &off,  // member disables
	}
	merged := defaults.merge(override)
	joined := strings.Join(merged.renderArgs(), " ")

	wants := []string{"--reasoning low", "--max-turns 20", "--tools=true", "--skills=false"}
	for _, want := range wants {
		if !strings.Contains(joined, want) {
			t.Errorf("rendered args missing %q\n got: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--reasoning high") {
		t.Errorf("override should win for reasoning\n got: %s", joined)
	}
}

func TestFamilyInlineSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assistant.yaml")
	turns := 12
	in := familyConfig{
		Defaults: assistantSettings{Reasoning: "medium"},
		Members: []familyMember{{
			Name:                 "Alice",
			TelegramBotToken:     "1:a",
			TelegramAllowedUsers: []string{"111"},
			Settings:             assistantSettings{Permission: "read-only", MaxTurns: &turns},
		}},
	}
	in.applyDefaults()
	if err := saveFamilyConfig(path, in); err != nil {
		t.Fatalf("saveFamilyConfig: %v", err)
	}
	out, err := loadFamilyConfig(path)
	if err != nil {
		t.Fatalf("loadFamilyConfig: %v", err)
	}
	if out.Defaults.Reasoning != "medium" {
		t.Errorf("defaults.reasoning = %q, want medium", out.Defaults.Reasoning)
	}
	if out.Members[0].Settings.Permission != "read-only" {
		t.Errorf("member permission = %q, want read-only", out.Members[0].Settings.Permission)
	}
	if out.Members[0].Settings.MaxTurns == nil || *out.Members[0].Settings.MaxTurns != 12 {
		t.Errorf("member maxTurns not preserved: %+v", out.Members[0].Settings.MaxTurns)
	}
}

func TestLoadFamilyConfigMissing(t *testing.T) {
	_, err := loadFamilyConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}
