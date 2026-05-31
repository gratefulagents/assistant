// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
)

// Run executes the assistant command and returns a process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	command := ""
	if len(args) > 0 && isCommand(args[0]) {
		command = args[0]
		args = args[1:]
	}
	cfg, err := parseConfig(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := cfg.validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg.Command = command
	cfg.Serve = command == "serve"
	configureLogging(cfg, stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch cfg.Command {
	case "serve":
		err = runWithOptionalScheduler(ctx, cfg, stdout, stderr, runGateway)
	case "telegram":
		err = runWithOptionalScheduler(ctx, cfg, stdout, stderr, runTelegramPoller)
	case "gmail":
		err = runWithOptionalScheduler(ctx, cfg, stdout, stderr, runGmailPoller)
	case "schedule":
		err = runScheduler(ctx, cfg, stdout, stderr)
	case "poll":
		err = runPollers(ctx, cfg, stdout, stderr)
	default:
		if strings.TrimSpace(cfg.Prompt) != "" {
			err = runPrompt(ctx, cfg, strings.TrimSpace(cfg.Prompt), stdin, stdout, stderr, nil, conversationModeChat)
		} else {
			err = runWithOptionalScheduler(ctx, cfg, stdout, stderr, func(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
				return runREPL(ctx, cfg, stdin, stdout, stderr)
			})
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "assistant:", err)
		return 1
	}
	return 0
}

func isCommand(arg string) bool {
	switch arg {
	case "serve", "telegram", "gmail", "schedule", "poll":
		return true
	default:
		return false
	}
}

func configureLogging(cfg appConfig, stderr io.Writer) {
	if cfg.Debug || cfg.Audit {
		log.SetOutput(stderr)
		return
	}
	log.SetOutput(io.Discard)
}
