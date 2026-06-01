// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
)

func newOpenAIOAuthSession(cfg appConfig, tokenEndpoint string) (*sdkopenai.AuthSession, error) {
	sessionCfg := sdkopenai.OAuthSessionConfig{
		AuthJSONPath:  strings.TrimSpace(cfg.OpenAIOAuthPath),
		AccountID:     strings.TrimSpace(cfg.OpenAIOAuthAccountID),
		AccountIDPath: strings.TrimSpace(cfg.OpenAIAccountIDPath),
	}
	if strings.TrimSpace(tokenEndpoint) != "" {
		sessionCfg.TokenEndpoint = strings.TrimSpace(tokenEndpoint)
	}
	session, err := sdkopenai.NewOAuthAuthSessionFromConfig(sessionCfg)
	if err != nil {
		return nil, fmt.Errorf("load OpenAI OAuth session from %s: %w", cfg.OpenAIOAuthPath, err)
	}
	return session, nil
}

func runOAuthRefresh(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if cfg.OAuthRefreshInterval <= 0 {
		if err := refreshOAuthAuthFile(ctx, cfg, ""); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "refreshed OpenAI OAuth token at %s\n", cfg.OpenAIOAuthPath)
		return nil
	}

	fmt.Fprintf(stderr, "assistant oauth refresher interval=%s path=%s\n", cfg.OAuthRefreshInterval, cfg.OpenAIOAuthPath)
	for {
		if err := refreshOAuthAuthFile(ctx, cfg, ""); err != nil {
			fmt.Fprintln(stderr, "assistant: oauth refresh:", err)
		} else {
			fmt.Fprintf(stdout, "refreshed OpenAI OAuth token at %s\n", cfg.OpenAIOAuthPath)
		}

		timer := time.NewTimer(cfg.OAuthRefreshInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func refreshOAuthAuthFile(ctx context.Context, cfg appConfig, tokenEndpoint string) error {
	session, err := newOpenAIOAuthSession(cfg, tokenEndpoint)
	if err != nil {
		return err
	}
	if !session.SupportsRefresh() {
		return errors.New("oauth refresh token is unavailable")
	}
	data, err := session.RefreshAndSerialize(ctx, cfg.OpenAIOAuthAccountID)
	if err != nil {
		return fmt.Errorf("refresh OpenAI OAuth token: %w", err)
	}
	data = mergeOAuthAuthJSON(cfg.OpenAIOAuthPath, data)
	return writeFileAtomic(cfg.OpenAIOAuthPath, data, 0o600)
}

func mergeOAuthAuthJSON(path string, refreshed []byte) []byte {
	originalRaw, err := os.ReadFile(path)
	if err != nil {
		return refreshed
	}
	var original map[string]any
	var update map[string]any
	if err := json.Unmarshal(originalRaw, &original); err != nil {
		return refreshed
	}
	if err := json.Unmarshal(refreshed, &update); err != nil {
		return refreshed
	}
	if updateTokens, ok := update["tokens"].(map[string]any); ok {
		if originalTokens, ok := original["tokens"].(map[string]any); ok {
			for k, v := range updateTokens {
				originalTokens[k] = v
			}
			original["tokens"] = originalTokens
		} else {
			original["tokens"] = updateTokens
		}
		delete(update, "tokens")
	}
	for k, v := range update {
		original[k] = v
	}
	merged, err := json.Marshal(original)
	if err != nil {
		return refreshed
	}
	return merged
}

func writeFileAtomic(path string, data []byte, fallbackMode os.FileMode) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = fallbackMode
	}
	if mode == 0 {
		mode = 0o600
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		if _, err := tmp.Write([]byte("\n")); err != nil {
			return fmt.Errorf("write temp file newline: %w", err)
		}
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	closed = true

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_ = f.Sync()
}
