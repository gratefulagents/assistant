// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"fmt"
	"io"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkruntime "github.com/gratefulagents/sdk/pkg/agentsdk/runtime"
)

func runStream(ctx context.Context, bundle *sdkruntime.Bundle, input []agentsdk.RunItem, stdout, stderr io.Writer, audit *auditRecorder) (bool, *agentsdk.RunResult, error) {
	stream := bundle.Runner.RunStreamed(ctx, bundle.Agent, cloneRunItems(input), bundle.Config)
	wroteDelta := false
	for ev := range stream.Events {
		switch ev.Type {
		case agentsdk.StreamEventRawResponse:
			if ev.Delta != "" {
				fmt.Fprint(stdout, ev.Delta)
				wroteDelta = true
			}
		case agentsdk.StreamEventRunItem:
			if ev.Item != nil {
				audit.EmitRunItem(ev.Item)
				if ev.Item.Type == agentsdk.RunItemToolCall && ev.Item.ToolCall != nil {
					fmt.Fprintf(stderr, "\n[tool] %s %s\n", ev.Item.ToolCall.Name, compactJSON(ev.Item.ToolCall.Input))
				}
			}
		case agentsdk.StreamEventAgentUpdated:
			if ev.NewAgent != nil {
				audit.EmitAgentUpdated(ev.NewAgent)
				fmt.Fprintf(stderr, "\n[agent] %s\n", ev.NewAgent.Name)
			}
		case agentsdk.StreamEventContent:
			audit.EmitContentEvent(ev.Content)
		case agentsdk.StreamEventSubAgent:
			if ev.Content != nil {
				audit.EmitContentEvent(ev.Content)
			}
		}
	}
	result := stream.FinalResult()
	if err := stream.Err(); err != nil {
		return wroteDelta, result, err
	}
	return wroteDelta, result, nil
}
