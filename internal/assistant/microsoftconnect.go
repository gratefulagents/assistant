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

// runMicrosoftConnect performs the device-style pairing flow against the
// Connect broker for a Microsoft account: it registers a pairing credential
// with provider=microsoft, prints a URL for the user to complete Microsoft SSO
// in a browser, polls until the broker reports the grant is authorized, then
// writes the local microsoft-auth.json credential.
func runMicrosoftConnect(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	broker := strings.TrimRight(strings.TrimSpace(cfg.MicrosoftConnectURL), "/")
	path := microsoftAuthPath(cfg)
	assistantID := randomHex(16)
	secret := randomHex(32)
	scopes := normalizeMicrosoftScopes(cfg.MicrosoftScopes)
	if len(scopes) == 0 {
		scopes = defaultMicrosoftScopes()
	}

	start, err := brokerPostJSON[deviceStartResponse](ctx, defaultHTTPClient, broker+"/device/start", map[string]any{
		"provider":     "microsoft",
		"scopes":       scopes,
		"assistant_id": assistantID,
		"secret_hash":  sha256Hex(secret),
	})
	if err != nil {
		return fmt.Errorf("start Microsoft connect: %w", err)
	}
	if strings.TrimSpace(start.Error) != "" {
		return fmt.Errorf("start Microsoft connect: %s", start.Error)
	}
	if strings.TrimSpace(start.DeviceCode) == "" {
		return errors.New("broker did not return a device code")
	}

	authURL := firstNonEmpty(start.VerificationURIComplete, start.VerificationURI)
	fmt.Fprintf(stdout, "To connect your Microsoft account, open:\n\n  %s\n\n", authURL)
	if strings.TrimSpace(start.UserCode) != "" {
		fmt.Fprintf(stdout, "If prompted, enter the code: %s\n\n", start.UserCode)
	}
	fmt.Fprintln(stderr, "assistant: waiting for Microsoft authorization...")

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
			return errors.New("Microsoft authorization timed out; run `assistant microsoft-connect` again")
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
			return fmt.Errorf("Microsoft connect failed: %s", resp.Error)
		}
		break
	}

	grantScopes := authorized.Scopes
	if len(grantScopes) == 0 {
		grantScopes = scopes
	}
	file := microsoftAuthFile{
		BrokerURL:   broker,
		AssistantID: assistantID,
		Secret:      secret,
		Scopes:      grantScopes,
		Email:       authorized.Email,
	}
	if err := saveMicrosoftAuthFile(path, file); err != nil {
		return fmt.Errorf("write Microsoft auth file: %w", err)
	}

	// Best-effort: mint an initial access token so the credential is ready to use.
	if sess, err := newMicrosoftAuthSession(cfg); err == nil {
		if _, err := sess.AccessToken(ctx); err != nil {
			fmt.Fprintf(stderr, "assistant: initial token fetch warning: %v\n", err)
		}
	}

	who := authorized.Email
	if who == "" {
		who = "your Microsoft account"
	}
	fmt.Fprintf(stdout, "Connected %s. Credential saved to %s\n", who, path)
	return nil
}

// runMicrosoftRefresh keeps the cached Microsoft Graph access token fresh,
// mirroring runGoogleRefresh. With a zero interval it refreshes once.
func runMicrosoftRefresh(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	sess, err := newMicrosoftAuthSession(cfg)
	if err != nil {
		return err
	}
	if cfg.OAuthRefreshInterval <= 0 {
		if _, err := sess.AccessToken(ctx); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "refreshed Microsoft access token at %s\n", sess.path)
		return nil
	}

	fmt.Fprintf(stderr, "assistant microsoft refresher interval=%s path=%s\n", cfg.OAuthRefreshInterval, sess.path)
	for {
		if _, err := sess.AccessToken(ctx); err != nil {
			if errors.Is(err, errMicrosoftReconnect) {
				return err
			}
			fmt.Fprintln(stderr, "assistant: microsoft refresh:", err)
		} else {
			fmt.Fprintf(stdout, "refreshed Microsoft access token at %s\n", sess.path)
		}
		if !sleepContext(ctx, cfg.OAuthRefreshInterval) {
			return nil
		}
	}
}

// runMicrosoftDisconnect revokes the grant at the broker and deletes the local
// credential.
func runMicrosoftDisconnect(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	path := microsoftAuthPath(cfg)
	file, exists, err := loadMicrosoftAuthFile(path)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Fprintf(stdout, "No Microsoft credential found at %s\n", path)
		return nil
	}
	if strings.TrimSpace(file.BrokerURL) != "" && strings.TrimSpace(file.AssistantID) != "" {
		if _, err := brokerPostJSON[map[string]any](ctx, defaultHTTPClient, strings.TrimRight(file.BrokerURL, "/")+"/revoke", map[string]string{
			"provider":     "microsoft",
			"assistant_id": file.AssistantID,
			"secret":       file.Secret,
		}); err != nil {
			fmt.Fprintf(stderr, "assistant: revoke warning: %v\n", err)
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove Microsoft auth file: %w", err)
	}
	fmt.Fprintf(stdout, "Disconnected Microsoft account; removed %s\n", path)
	return nil
}
