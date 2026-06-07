// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

type longRunningFunc func(context.Context, appConfig, io.Writer, io.Writer) error

func runWithOptionalScheduler(ctx context.Context, cfg appConfig, stdout, stderr io.Writer, primary longRunningFunc) error {
	return runWithOptionalSchedulerFunc(ctx, cfg, stdout, stderr, primary, runScheduler)
}

func runWithOptionalSchedulerFunc(ctx context.Context, cfg appConfig, stdout, stderr io.Writer, primary, scheduler longRunningFunc) error {
	if primary == nil {
		return errors.New("primary runner is required")
	}
	if !cfg.EnableScheduling {
		return primary(ctx, cfg, stdout, stderr)
	}
	if scheduler == nil {
		return errors.New("scheduler runner is required")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	safeGo(cfg, "supervisor", "primary", errCh, func() error {
		return primary(runCtx, cfg, stdout, stderr)
	})
	safeGo(cfg, "supervisor", "scheduler", errCh, func() error {
		return scheduler(runCtx, cfg, stdout, stderr)
	})

	err := <-errCh
	cancel()
	return err
}

func runPollers(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	errCh := make(chan error, 3)
	started := 0
	if strings.TrimSpace(cfg.TelegramBotToken) != "" {
		started++
		safeGo(cfg, "telegram", "poller", errCh, func() error {
			return runTelegramPoller(ctx, cfg, stdout, stderr)
		})
	}
	if strings.TrimSpace(cfg.GmailToken) != "" || googleAuthConfigured(cfg) {
		started++
		safeGo(cfg, "gmail", "poller", errCh, func() error {
			return runGmailPoller(ctx, cfg, stdout, stderr)
		})
	}
	if cfg.EnableScheduling {
		started++
		safeGo(cfg, "schedule", "poller", errCh, func() error {
			return runScheduler(ctx, cfg, stdout, stderr)
		})
	}
	if started == 0 {
		return errors.New("poll requires ASSISTANT_TELEGRAM_BOT_TOKEN, ASSISTANT_GMAIL_ACCESS_TOKEN, a connected Google account (assistant google-connect), or --scheduling=true")
	}
	fmt.Fprintf(stderr, "assistant polling %d channel(s); no inbound port required\n", started)
	for completed := 0; completed < started; completed++ {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if err != nil {
				return err
			}
		}
	}
	return nil
}
