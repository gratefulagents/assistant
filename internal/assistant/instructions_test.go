// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveInstructionsInlineWins(t *testing.T) {
	t.Setenv("ASSISTANT_INSTRUCTIONS", "")
	t.Setenv("ASSISTANT_INSTRUCTIONS_FILE", "")
	file := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(file, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := appConfig{Instructions: "  inline prompt  ", InstructionsPath: file}
	if err := cfg.resolveInstructions(); err != nil {
		t.Fatal(err)
	}
	if cfg.Instructions != "inline prompt" {
		t.Fatalf("Instructions = %q, want inline prompt (trimmed, file ignored)", cfg.Instructions)
	}
}

func TestResolveInstructionsReadsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(file, []byte("  You are Helga.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := appConfig{InstructionsPath: file}
	if err := cfg.resolveInstructions(); err != nil {
		t.Fatal(err)
	}
	if cfg.Instructions != "You are Helga." {
		t.Fatalf("Instructions = %q, want file contents trimmed", cfg.Instructions)
	}
}

func TestResolveInstructionsMissingFileErrors(t *testing.T) {
	cfg := appConfig{InstructionsPath: filepath.Join(t.TempDir(), "nope.txt")}
	if err := cfg.resolveInstructions(); err == nil {
		t.Fatal("expected error for missing instructions file")
	}
}

func TestInstructionsFlagOverridesConfigFile(t *testing.T) {
	t.Setenv("ASSISTANT_INSTRUCTIONS", "")
	t.Setenv("ASSISTANT_INSTRUCTIONS_FILE", "")
	configPath := filepath.Join(t.TempDir(), "assistant.json")
	if err := os.WriteFile(configPath, []byte(`{"instructions": "from config file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig([]string{
		"--config", configPath,
		"--provider", "openai-api",
		"--api-key", "sk-test",
		"--instructions", "from flag",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Instructions != "from flag" {
		t.Fatalf("Instructions = %q, want flag to win over config file", cfg.Instructions)
	}
}

func TestInstructionsFromConfigFileWhenUnset(t *testing.T) {
	t.Setenv("ASSISTANT_INSTRUCTIONS", "")
	t.Setenv("ASSISTANT_INSTRUCTIONS_FILE", "")
	configPath := filepath.Join(t.TempDir(), "assistant.json")
	if err := os.WriteFile(configPath, []byte(`{"instructions": "from config file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.ConfigPath = configPath
	cfg.Provider = providerOpenAIAPI
	cfg.APIKey = "sk-test"
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Instructions != "from config file" {
		t.Fatalf("Instructions = %q, want value from config file", cfg.Instructions)
	}
}
