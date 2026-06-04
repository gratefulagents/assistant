// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type deviceStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
	Error                   string `json:"error"`
}

type deviceTokenResponse struct {
	Status      string   `json:"status"`
	AssistantID string   `json:"assistant_id"`
	Scopes      []string `json:"scopes"`
	Email       string   `json:"email"`
	Error       string   `json:"error"`
}

// runGoogleConnect performs the device-style pairing flow against the Connect
// broker: it registers a pairing credential, prints a URL for the user to
// complete Google SSO in a browser, polls until the broker reports the grant is
// authorized, then writes the local google-auth.json credential.
func runGoogleConnect(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	broker := strings.TrimRight(strings.TrimSpace(cfg.GoogleConnectURL), "/")
	path := googleAuthPath(cfg)
	assistantID := randomHex(16)
	secret := randomHex(32)
	scopes := normalizeGoogleScopes(cfg.GoogleScopes)
	if len(scopes) == 0 {
		scopes = defaultGoogleScopes()
	}

	start, err := brokerPostJSON[deviceStartResponse](ctx, defaultHTTPClient, broker+"/device/start", map[string]any{
		"scopes":       scopes,
		"assistant_id": assistantID,
		"secret_hash":  sha256Hex(secret),
	})
	if err != nil {
		return fmt.Errorf("start Google connect: %w", err)
	}
	if strings.TrimSpace(start.Error) != "" {
		return fmt.Errorf("start Google connect: %s", start.Error)
	}
	if strings.TrimSpace(start.DeviceCode) == "" {
		return errors.New("broker did not return a device code")
	}

	authURL := firstNonEmpty(start.VerificationURIComplete, start.VerificationURI)
	fmt.Fprintf(stdout, "To connect your Google account, open:\n\n  %s\n\n", authURL)
	if strings.TrimSpace(start.UserCode) != "" {
		fmt.Fprintf(stdout, "If prompted, enter the code: %s\n\n", start.UserCode)
	}
	fmt.Fprintln(stderr, "assistant: waiting for Google authorization...")

	interval := time.Duration(start.Interval) * time.Second
	if interval < 2*time.Second {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(15 * time.Minute)
	if start.ExpiresIn > 0 {
		deadline = time.Now().Add(time.Duration(start.ExpiresIn) * time.Second)
	}

	var authorized deviceTokenResponse
	for {
		if !sleepContext(ctx, interval) {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("Google authorization timed out; run `assistant google-connect` again")
		}
		resp, err := brokerPostJSON[deviceTokenResponse](ctx, defaultHTTPClient, broker+"/device/token", map[string]string{
			"device_code": start.DeviceCode,
			"secret":      secret,
		})
		if err != nil {
			fmt.Fprintf(stderr, "assistant: connect poll warning: %v\n", err)
			continue
		}
		switch {
		case resp.Status == "authorized":
			authorized = resp
		case resp.Error == "slow_down":
			interval += 5 * time.Second
			continue
		case resp.Error == "", resp.Status == "pending":
			continue
		default:
			return fmt.Errorf("Google connect failed: %s", resp.Error)
		}
		break
	}

	grantScopes := authorized.Scopes
	if len(grantScopes) == 0 {
		grantScopes = scopes
	}
	file := googleAuthFile{
		BrokerURL:   broker,
		AssistantID: assistantID,
		Secret:      secret,
		Scopes:      grantScopes,
		Email:       authorized.Email,
	}
	if err := saveGoogleAuthFile(path, file); err != nil {
		return fmt.Errorf("write Google auth file: %w", err)
	}

	// Best-effort: mint an initial access token so the credential is ready to use.
	if sess, err := newGoogleAuthSession(cfg); err == nil {
		if _, err := sess.AccessToken(ctx); err != nil {
			fmt.Fprintf(stderr, "assistant: initial token fetch warning: %v\n", err)
		}
	}

	who := authorized.Email
	if who == "" {
		who = "your Google account"
	}
	fmt.Fprintf(stdout, "Connected %s. Credential saved to %s\n", who, path)
	return nil
}

// runGoogleRefresh keeps the cached Google access token fresh, mirroring the
// OpenAI oauth-refresh daemon. With a zero interval it refreshes once.
func runGoogleRefresh(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	sess, err := newGoogleAuthSession(cfg)
	if err != nil {
		return err
	}
	if cfg.OAuthRefreshInterval <= 0 {
		if _, err := sess.AccessToken(ctx); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "refreshed Google access token at %s\n", sess.path)
		return nil
	}

	fmt.Fprintf(stderr, "assistant google refresher interval=%s path=%s\n", cfg.OAuthRefreshInterval, sess.path)
	for {
		if _, err := sess.AccessToken(ctx); err != nil {
			if errors.Is(err, errGoogleReconnect) {
				return err
			}
			fmt.Fprintln(stderr, "assistant: google refresh:", err)
		} else {
			fmt.Fprintf(stdout, "refreshed Google access token at %s\n", sess.path)
		}
		if !sleepContext(ctx, cfg.OAuthRefreshInterval) {
			return nil
		}
	}
}

// runGoogleDisconnect revokes the grant at the broker and deletes the local
// credential.
func runGoogleDisconnect(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	path := googleAuthPath(cfg)
	file, exists, err := loadGoogleAuthFile(path)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Fprintf(stdout, "No Google credential found at %s\n", path)
		return nil
	}
	if strings.TrimSpace(file.BrokerURL) != "" && strings.TrimSpace(file.AssistantID) != "" {
		if _, err := brokerPostJSON[map[string]any](ctx, defaultHTTPClient, strings.TrimRight(file.BrokerURL, "/")+"/revoke", map[string]string{
			"assistant_id": file.AssistantID,
			"secret":       file.Secret,
		}); err != nil {
			fmt.Fprintf(stderr, "assistant: revoke warning: %v\n", err)
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove Google auth file: %w", err)
	}
	fmt.Fprintf(stdout, "Disconnected Google account; removed %s\n", path)
	return nil
}
