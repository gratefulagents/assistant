package assistant

import "testing"

func TestParseModelFallbacksFromFlags(t *testing.T) {
	cfg, err := parseConfig([]string{
		"--provider", "openrouter",
		"--model", "openrouter/qwen/qwen3-coder-480b-a35b-instruct:free",
		"--model-fallback", "openrouter/qwen/qwen3-coder-480b-a35b-instruct",
		"--model-fallback", "openrouter/deepseek/deepseek-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"openrouter/qwen/qwen3-coder-480b-a35b-instruct",
		"openrouter/deepseek/deepseek-chat",
	}
	if len(cfg.ModelFallbacks) != len(want) {
		t.Fatalf("ModelFallbacks = %v, want %v", cfg.ModelFallbacks, want)
	}
	for i := range want {
		if cfg.ModelFallbacks[i] != want[i] {
			t.Fatalf("ModelFallbacks = %v, want %v", cfg.ModelFallbacks, want)
		}
	}
}

func TestParseModelFallbacksFromEnv(t *testing.T) {
	// Colons must be preserved: OpenRouter model IDs carry ":free"-style suffixes.
	t.Setenv("ASSISTANT_MODEL_FALLBACKS", "deepseek/deepseek-chat:free, openrouter/auto ,a:b")
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"deepseek/deepseek-chat:free", "openrouter/auto", "a:b"}
	if len(cfg.ModelFallbacks) != len(want) {
		t.Fatalf("ModelFallbacks = %v, want %v", cfg.ModelFallbacks, want)
	}
	for i := range want {
		if cfg.ModelFallbacks[i] != want[i] {
			t.Fatalf("ModelFallbacks = %v, want %v", cfg.ModelFallbacks, want)
		}
	}
}
