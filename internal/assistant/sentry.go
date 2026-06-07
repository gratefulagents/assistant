// SPDX-License-Identifier: GPL-3.0-only

//go:build sentry

// Package-level Sentry integration. This file — and the getsentry/sentry-go
// dependency it imports — is only compiled into builds made with `-tags sentry`.
// Default builds use the zero-cost no-op stubs in sentry_stub.go, so the binary
// carries no Sentry code or dependency unless an operator opts in.
package assistant

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

// sentryFlushTimeout bounds how long a flush blocks. Sentry is best-effort
// observability, never on a hot path, so we never wait long for delivery.
const sentryFlushTimeout = 2 * time.Second

// sentryActive guards every capture/recover helper so they are cheap no-ops
// until initSentry succeeds. It is process-global because sentry-go keeps a
// single global hub; the once guard keeps repeated inits (tests, REPL) safe.
var (
	sentryActive bool
	sentryOnce   sync.Once
)

// initSentry wires up the optional Sentry crash/error sink. It mirrors the
// Langfuse client: disabled by default, a silent no-op when the DSN is missing,
// and never fatal. Sentry complements Langfuse (agent traces) and the audit log
// (local NDJSON) by aggregating panics and operational errors across the fleet
// with alerting. The returned flush should be deferred by the caller so buffered
// events are delivered before the process exits.
func initSentry(cfg appConfig) (func(), bool) {
	if !cfg.SentryEnabled {
		return func() {}, false
	}
	dsn := strings.TrimSpace(cfg.SentryDSN)
	if dsn == "" {
		return func() {}, false
	}
	ok := false
	sentryOnce.Do(func() {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:              dsn,
			Environment:      strings.TrimSpace(cfg.SentryEnvironment),
			Release:          sentryRelease(),
			AttachStacktrace: true,
			BeforeSend:       sentryRedactEvent,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "[sentry] init warning:", err)
			return
		}
		if uid := strings.TrimSpace(cfg.UserID); uid != "" {
			sentry.ConfigureScope(func(scope *sentry.Scope) {
				scope.SetUser(sentry.User{ID: uid})
			})
		}
		sentryActive = true
		ok = true
	})
	if !sentryActive {
		return func() {}, false
	}
	return func() { sentry.Flush(sentryFlushTimeout) }, ok
}

// sentryRelease tags events with the build version so errors group per release.
// Unstamped dev builds report no release rather than a misleading "dev".
func sentryRelease() string {
	details := currentBuildDetails()
	version := strings.TrimSpace(details.Version)
	if version == "" || version == "dev" {
		return ""
	}
	return details.Name + "@" + version
}

// sentryRedactEvent runs the audit redactors over every free-text field before
// an event leaves the process, so tokens and secrets are never shipped to a
// third party. It reuses the exact patterns the audit log already trusts.
func sentryRedactEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return event
	}
	event.Message = redactAuditText(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = redactAuditText(event.Exception[i].Value)
	}
	for _, crumb := range event.Breadcrumbs {
		if crumb != nil {
			crumb.Message = redactAuditText(crumb.Message)
		}
	}
	return event
}

// captureSentryError reports a non-fatal operational error, tagged by component
// and stage so the fleet dashboard can slice failures (e.g. gmail/token,
// telegram/poll). It is a no-op unless Sentry is enabled and initialized.
func captureSentryError(cfg appConfig, component, stage string, err error) {
	if !sentryActive || !cfg.SentryEnabled || err == nil {
		return
	}
	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		sentryTagComponent(scope, component, stage)
	})
	hub.CaptureException(err)
}

// reportSentryPanic captures a recovered panic with a fatal level and flushes
// synchronously, because the caller typically re-panics or exits immediately
// afterwards and an unflushed event would be lost.
func reportSentryPanic(cfg appConfig, component, stage string, recovered any) {
	if !sentryActive || !cfg.SentryEnabled || recovered == nil {
		return
	}
	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelFatal)
		sentryTagComponent(scope, component, stage)
	})
	hub.Recover(recovered)
	hub.Flush(sentryFlushTimeout)
}

func sentryTagComponent(scope *sentry.Scope, component, stage string) {
	if c := strings.TrimSpace(component); c != "" {
		scope.SetTag("component", c)
	}
	if s := strings.TrimSpace(stage); s != "" {
		scope.SetTag("stage", s)
	}
}

// recoverTopLevel is deferred on the main goroutine. It reports a panic to
// Sentry, then re-panics so the process still crashes loudly with its stack
// trace — the prior behavior is preserved, only now the crash is also recorded.
func recoverTopLevel(cfg appConfig, component, stage string) {
	if r := recover(); r != nil {
		reportSentryPanic(cfg, component, stage, r)
		panic(r)
	}
}

// panicError renders a recovered panic as an error so a supervised goroutine
// can surface it through its error channel instead of taking down the process.
func panicError(component, stage string, recovered any) error {
	return fmt.Errorf("panic in %s/%s: %v", strings.TrimSpace(component), strings.TrimSpace(stage), recovered)
}

// safeGo launches a supervised long-running goroutine whose panic is captured
// by Sentry and converted into an error on errCh. This keeps the existing
// supervisor semantics (the first non-nil error wins and unwinds the process)
// while ensuring a crash in any poller or the scheduler is never silent.
func safeGo(cfg appConfig, component, stage string, errCh chan<- error, fn func() error) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportSentryPanic(cfg, component, stage, r)
				errCh <- panicError(component, stage, r)
			}
		}()
		errCh <- fn()
	}()
}
