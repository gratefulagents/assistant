// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkruntime "github.com/gratefulagents/sdk/pkg/agentsdk/runtime"
)

type approvalRequester interface {
	RequestApproval(ctx context.Context, pending *agentsdk.Interruption, reqCtx approvalRequestContext) (approvalDecision, error)
}

type approvalRequestContext struct {
	Items []agentsdk.RunItem
	Mode  string
}

type terminalApprovalRequester struct {
	input  io.Reader
	stderr io.Writer
}

func resolveApproval(ctx context.Context, bundle *sdkruntime.Bundle, pending *agentsdk.Interruption, approvalIn io.Reader, reqCtx approvalRequestContext, stderr io.Writer, audit *auditRecorder) ([]agentsdk.RunItem, error) {
	if pending == nil {
		return nil, nil
	}
	return resolveApprovalWithRequester(ctx, bundle, pending, terminalApprovalRequester{input: approvalIn, stderr: stderr}, reqCtx, stderr, audit)
}

func (r terminalApprovalRequester) RequestApproval(_ context.Context, pending *agentsdk.Interruption, _ approvalRequestContext) (approvalDecision, error) {
	fmt.Fprintf(r.stderr, "\n[approval] %s %s\napprove? [y/N] ", pending.ToolName, compactJSON(pending.ToolInput))
	input := r.input
	if input == nil {
		input = os.Stdin
	}
	reader, ok := input.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(input)
	}
	reply, _ := reader.ReadString('\n')
	approved := strings.EqualFold(strings.TrimSpace(reply), "y") || strings.EqualFold(strings.TrimSpace(reply), "yes")
	reason := "tool call approved by user"
	if !approved {
		reason = "tool call denied by user"
	}
	return approvalDecision{Approved: approved, Reason: reason}, nil
}

func resolveApprovalWithRequester(ctx context.Context, bundle *sdkruntime.Bundle, pending *agentsdk.Interruption, requester approvalRequester, reqCtx approvalRequestContext, stderr io.Writer, audit *auditRecorder) ([]agentsdk.RunItem, error) {
	if pending == nil {
		return nil, nil
	}
	if requester == nil {
		return nil, fmt.Errorf("tool %q requires approval; no approval requester available", pending.ToolName)
	}
	decision, err := requester.RequestApproval(ctx, pending, reqCtx)
	if err != nil {
		return nil, err
	}
	approved := decision.Approved
	audit.EmitApprovalDecision(pending.ToolName, pending.ToolInput, approved)
	items := []agentsdk.RunItem{{
		Type: agentsdk.RunItemToolApproval,
		ToolApproval: &agentsdk.ToolApprovalData{
			ToolName: pending.ToolName,
			Input:    cloneRaw(pending.ToolInput),
			CallID:   pending.ToolCallID,
			Approved: approved,
		},
	}}
	if !approved {
		content := strings.TrimSpace(decision.Reason)
		if content == "" {
			content = "tool call denied by user"
		}
		items = append(items, agentsdk.RunItem{
			Type: agentsdk.RunItemToolOutput,
			ToolOutput: &agentsdk.ToolOutputData{
				CallID:  pending.ToolCallID,
				Content: content,
				IsError: true,
			},
		})
		return items, nil
	}
	item, _, _, _, err := bundle.Runner.ExecuteApprovedTool(ctx, bundle.Agent, agentsdk.ToolCallData{
		ID:    pending.ToolCallID,
		Name:  pending.ToolName,
		Input: cloneRaw(pending.ToolInput),
	}, bundle.Config)
	if err != nil {
		return items, err
	}
	items = append(items, item)
	if item.ToolOutput != nil {
		status := "ok"
		if item.ToolOutput.IsError {
			status = "error"
		}
		fmt.Fprintf(stderr, "[tool:%s] %s\n", status, firstLine(item.ToolOutput.Content))
	}
	return items, nil
}
