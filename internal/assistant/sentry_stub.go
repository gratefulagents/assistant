// SPDX-License-Identifier: GPL-3.0-only

//go:build !sentry

// Zero-cost stubs for the optional Sentry sink. These are compiled into every
// default build so the call sites in run.go, audit.go and poll.go need no build
// guards, while the getsentry/sentry-go dependency stays out of the binary
// entirely. Build with `-tags sentry` to swap in the real integration.
package assistant

// initSentry is a no-op when the Sentry build tag is absent.
func initSentry(_ appConfig) (func(), bool) { return func() {}, false }

// captureSentryError is a no-op when the Sentry build tag is absent.
func captureSentryError(_ appConfig, _, _ string, _ error) {}

// reportSentryPanic is a no-op when the Sentry build tag is absent.
func reportSentryPanic(_ appConfig, _, _ string, _ any) {}

// recoverTopLevel is a no-op when the Sentry build tag is absent: any panic
// propagates exactly as it did before Sentry support was added.
func recoverTopLevel(_ appConfig, _, _ string) {}

// safeGo preserves the original "fire the goroutine, forward its error" behavior
// when the Sentry build tag is absent.
func safeGo(_ appConfig, _, _ string, errCh chan<- error, fn func() error) {
	go func() { errCh <- fn() }()
}
