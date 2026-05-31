// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

func runPollers(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	errCh := make(chan error, 2)
	started := 0
	if strings.TrimSpace(cfg.TelegramBotToken) != "" {
		started++
		go func() {
			errCh <- runTelegramPoller(ctx, cfg, stdout, stderr)
		}()
	}
	if strings.TrimSpace(cfg.GmailToken) != "" {
		started++
		go func() {
			errCh <- runGmailPoller(ctx, cfg, stdout, stderr)
		}()
	}
	if started == 0 {
		return errors.New("poll requires ASSISTANT_TELEGRAM_BOT_TOKEN and/or ASSISTANT_GMAIL_ACCESS_TOKEN")
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
