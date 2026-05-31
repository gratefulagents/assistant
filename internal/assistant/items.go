// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"encoding/json"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func userMessage(text string) agentsdk.RunItem {
	return agentsdk.RunItem{Type: agentsdk.RunItemMessage, Message: &agentsdk.MessageOutput{Text: text}}
}

func cloneRunItems(items []agentsdk.RunItem) []agentsdk.RunItem {
	out := make([]agentsdk.RunItem, len(items))
	for i, item := range items {
		out[i] = item
		if item.Message != nil {
			msg := *item.Message
			out[i].Message = &msg
		}
		if item.ToolCall != nil {
			call := *item.ToolCall
			call.Input = cloneRaw(call.Input)
			out[i].ToolCall = &call
		}
		if item.ToolOutput != nil {
			toolOut := *item.ToolOutput
			out[i].ToolOutput = &toolOut
		}
		if item.ToolApproval != nil {
			approval := *item.ToolApproval
			approval.Input = cloneRaw(approval.Input)
			out[i].ToolApproval = &approval
		}
	}
	return out
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}
