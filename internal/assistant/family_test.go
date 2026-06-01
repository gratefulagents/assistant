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
		Model:                "gpt-test",
		TelegramBotToken:     "123:abc",
		TelegramAllowedUsers: []string{"42"},
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
}

func TestLoadFamilyConfigMissing(t *testing.T) {
	_, err := loadFamilyConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}
