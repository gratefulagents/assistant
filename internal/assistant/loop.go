// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func runREPL(ctx context.Context, cfg appConfig, stdin io.Reader, stdout, stderr io.Writer) error {
	fmt.Fprintf(stderr, "assistant %s model=%s workdir=%s\n", cfg.Provider, cfg.Model, cfg.WorkDir)
	fmt.Fprintln(stderr, "type /exit to quit")

	session := newConversationSession()
	reader := bufio.NewReader(stdin)
	for {
		fmt.Fprint(stderr, "\n> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
				break
			}
			if !errors.Is(err, io.EOF) {
				return err
			}
		}
		prompt := strings.TrimSpace(line)
		if prompt == "" {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		readErr := err
		if command := handleSlashCommand(prompt, session, true); command.Handled {
			if command.Exit {
				return nil
			}
			if strings.TrimSpace(command.Reply) != "" {
				fmt.Fprintln(stderr, command.Reply)
			}
			continue
		}
		session.mu.Lock()
		runErr := runPrompt(ctx, cfg, prompt, reader, stdout, stderr, &session.history, session.currentModeLocked())
		session.mu.Unlock()
		if runErr != nil {
			return runErr
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return nil
}

func runPrompt(ctx context.Context, cfg appConfig, prompt string, approvalIn io.Reader, stdout, stderr io.Writer, history *[]agentsdk.RunItem, mode string) error {
	cfg = applyConversationMode(cfg, mode)
	audit, err := newAuditRecorder(cfg, stdout)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := audit.Close(); closeErr != nil {
			fmt.Fprintln(stderr, "[log] audit close warning:", closeErr)
		}
	}()
	audit.EmitRunStart(cfg, prompt)

	items := []agentsdk.RunItem(nil)
	if history != nil {
		items = append(items, (*history)...)
	}
	items = append(items, userMessage(prompt))
	approvals := approvalRequesterForConfig(cfg, terminalApprovalRequester{input: approvalIn, stderr: stderr}, stderr, audit)

	for resumes := 0; ; resumes++ {
		if resumes > 12 {
			err := errors.New("too many approval resumes")
			audit.EmitRunError(err)
			return err
		}
		bundle, err := buildBundle(ctx, cfg, stderr, audit)
		if err != nil {
			audit.EmitRunError(err)
			return err
		}

		wroteDelta, result, runErr := runStream(ctx, bundle, items, stdout, stderr, audit)
		if runErr != nil {
			closeBundle(bundle, stderr)
			audit.EmitRunError(runErr)
			return runErr
		}
		if result == nil {
			closeBundle(bundle, stderr)
			err := errors.New("runner returned no result")
			audit.EmitRunError(err)
			return err
		}
		if !wroteDelta && strings.TrimSpace(result.FinalText()) != "" {
			fmt.Fprintln(stdout, result.FinalText())
		} else if wroteDelta {
			fmt.Fprintln(stdout)
		}

		items = append(items, cloneRunItems(result.NewItems)...)
		if result.Interruption == nil {
			closeBundle(bundle, stderr)
			if history != nil {
				*history = items
			}
			audit.EmitRunEnd(result)
			return nil
		}

		audit.EmitApprovalRequest(result.Interruption)
		approvalItems, err := resolveApprovalWithRequester(ctx, bundle, result.Interruption, approvals, approvalRequestContext{
			Items: cloneRunItems(items),
			Mode:  mode,
		}, stderr, audit)
		closeBundle(bundle, stderr)
		if err != nil {
			audit.EmitRunError(err)
			return err
		}
		for i := range approvalItems {
			audit.EmitRunItem(&approvalItems[i])
		}
		items = append(items, approvalItems...)
	}
}
