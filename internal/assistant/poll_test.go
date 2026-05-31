// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestRunWithOptionalSchedulerDisabled(t *testing.T) {
	cfg := appConfig{EnableScheduling: false}
	var primaryCalled, schedulerCalled bool

	err := runWithOptionalSchedulerFunc(t.Context(), cfg, io.Discard, io.Discard,
		func(context.Context, appConfig, io.Writer, io.Writer) error {
			primaryCalled = true
			return nil
		},
		func(context.Context, appConfig, io.Writer, io.Writer) error {
			schedulerCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !primaryCalled {
		t.Fatal("primary runner was not called")
	}
	if schedulerCalled {
		t.Fatal("scheduler runner was called with scheduling disabled")
	}
}

func TestRunWithOptionalSchedulerStartsAndCancelsScheduler(t *testing.T) {
	cfg := appConfig{EnableScheduling: true}
	started := make(chan struct{})
	done := make(chan struct{})

	err := runWithOptionalSchedulerFunc(t.Context(), cfg, io.Discard, io.Discard,
		func(context.Context, appConfig, io.Writer, io.Writer) error {
			<-started
			return nil
		},
		func(ctx context.Context, _ appConfig, _ io.Writer, _ io.Writer) error {
			close(started)
			<-ctx.Done()
			close(done)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler was not canceled after primary runner completed")
	}
}

func TestRunWithOptionalSchedulerReturnsSchedulerError(t *testing.T) {
	cfg := appConfig{EnableScheduling: true}
	want := errors.New("scheduler failed")

	err := runWithOptionalSchedulerFunc(t.Context(), cfg, io.Discard, io.Discard,
		func(ctx context.Context, _ appConfig, _ io.Writer, _ io.Writer) error {
			<-ctx.Done()
			return nil
		},
		func(context.Context, appConfig, io.Writer, io.Writer) error {
			return want
		},
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
