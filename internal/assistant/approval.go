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

func resolveApproval(ctx context.Context, bundle *sdkruntime.Bundle, pending *agentsdk.Interruption, approvalIn io.Reader, stderr io.Writer) ([]agentsdk.RunItem, error) {
	if pending == nil {
		return nil, nil
	}
	fmt.Fprintf(stderr, "\n[approval] %s %s\napprove? [y/N] ", pending.ToolName, compactJSON(pending.ToolInput))
	if approvalIn == nil {
		approvalIn = os.Stdin
	}
	reader, ok := approvalIn.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(approvalIn)
	}
	reply, _ := reader.ReadString('\n')
	approved := strings.EqualFold(strings.TrimSpace(reply), "y") || strings.EqualFold(strings.TrimSpace(reply), "yes")
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
		items = append(items, agentsdk.RunItem{
			Type: agentsdk.RunItemToolOutput,
			ToolOutput: &agentsdk.ToolOutputData{
				CallID:  pending.ToolCallID,
				Content: "tool call denied by user",
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
