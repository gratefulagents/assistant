// SPDX-License-Identifier: GPL-3.0-only

//go:build sentry

package assistant

import (
	"errors"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
)

func TestSentryRedactEventScrubsSecrets(t *testing.T) {
	event := &sentry.Event{
		Message: `failed with "access_token":"super-secret-value"`,
		Exception: []sentry.Exception{
			{Value: "Authorization: Bearer abc123tokenvalue"},
		},
		Breadcrumbs: []*sentry.Breadcrumb{
			{Message: "calling api with sk-livesecretkey123"},
		},
	}
	out := sentryRedactEvent(event, nil)
	if strings.Contains(out.Message, "super-secret-value") {
		t.Errorf("message not redacted: %q", out.Message)
	}
	if strings.Contains(out.Exception[0].Value, "abc123tokenvalue") {
		t.Errorf("exception value not redacted: %q", out.Exception[0].Value)
	}
	if strings.Contains(out.Breadcrumbs[0].Message, "sk-livesecretkey123") {
		t.Errorf("breadcrumb not redacted: %q", out.Breadcrumbs[0].Message)
	}
}

func TestSentryHelpersNoOpWhenDisabled(t *testing.T) {
	cfg := appConfig{SentryEnabled: false}
	flush, ok := initSentry(cfg)
	if ok {
		t.Fatal("disabled config should not initialize Sentry")
	}
	flush() // must be safe to call.

	// Disabled capture/recover must not touch the (uninitialized) global hub.
	captureSentryError(cfg, "test", "stage", errors.New("boom"))
	reportSentryPanic(cfg, "test", "stage", "boom")

	// Missing DSN is also a silent no-op even when enabled.
	if _, ok := initSentry(appConfig{SentryEnabled: true}); ok {
		t.Fatal("missing DSN should not initialize Sentry")
	}
}

func TestSafeGoConvertsPanicToError(t *testing.T) {
	errCh := make(chan error, 1)
	safeGo(appConfig{}, "test", "stage", errCh, func() error {
		panic("kaboom")
	})
	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected panic surfaced as error, got %v", err)
	}
}
