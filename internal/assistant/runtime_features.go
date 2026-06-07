// SPDX-License-Identifier: GPL-3.0-only

package assistant

import sdkruntime "github.com/gratefulagents/sdk/pkg/agentsdk/runtime"

type assistantFeaturesConfig struct {
	Defaults     *bool                          `json:"defaults,omitempty"`
	Tools        *assistantToolFeatures         `json:"tools,omitempty"`
	MCP          *assistantMCPFeatures          `json:"mcp,omitempty"`
	Handoffs     *assistantHandoffFeatures      `json:"handoffs,omitempty"`
	SubAgents    *assistantSubAgentFeatures     `json:"subAgents,omitempty"`
	Guardrails   *assistantGuardrailFeatures    `json:"guardrails,omitempty"`
	Modes        *assistantModeFeatures         `json:"modes,omitempty"`
	ProjectState *assistantProjectStateFeatures `json:"projectState,omitempty"`
	Runtime      *assistantRuntimeFeatures      `json:"runtime,omitempty"`
}

type assistantToolFeatures struct {
	ListFiles      *bool                    `json:"listFiles,omitempty"`
	ReadFile       *bool                    `json:"readFile,omitempty"`
	Glob           *bool                    `json:"glob,omitempty"`
	Grep           *bool                    `json:"grep,omitempty"`
	LSP            *bool                    `json:"lsp,omitempty"`
	Bash           *bool                    `json:"bash,omitempty"`
	Write          *bool                    `json:"write,omitempty"`
	Edit           *bool                    `json:"edit,omitempty"`
	WebFetch       *bool                    `json:"webFetch,omitempty"`
	AsyncShell     *bool                    `json:"asyncShell,omitempty"`
	ExtraTools     *bool                    `json:"extraTools,omitempty"`
	VisionAnalyzer *bool                    `json:"visionAnalyzer,omitempty"`
	Signals        *assistantSignalFeatures `json:"signals,omitempty"`
}

type assistantSignalFeatures struct {
	AskUserQuestion *bool `json:"askUserQuestion,omitempty"`
	PresentPlan     *bool `json:"presentPlan,omitempty"`
	Finish          *bool `json:"finish,omitempty"`
	SetPhase        *bool `json:"setPhase,omitempty"`
}

type assistantMCPFeatures struct {
	Enabled         *bool    `json:"enabled,omitempty"`
	AllowAllServers *bool    `json:"allowAllServers,omitempty"`
	AllowedServers  []string `json:"allowedServers,omitempty"`
	AllowAllTools   *bool    `json:"allowAllTools,omitempty"`
	AllowedTools    []string `json:"allowedTools,omitempty"`
	ResourceTools   *bool    `json:"resourceTools,omitempty"`
}

type assistantHandoffFeatures struct {
	Enabled         *bool `json:"enabled,omitempty"`
	GenericFallback *bool `json:"genericFallback,omitempty"`
}

type assistantSubAgentFeatures struct {
	SyncTools       *bool                           `json:"syncTools,omitempty"`
	GenericFallback *bool                           `json:"genericFallback,omitempty"`
	Async           *assistantAsyncSubAgentFeatures `json:"async,omitempty"`
}

type assistantAsyncSubAgentFeatures struct {
	Spawn     *bool `json:"spawn,omitempty"`
	Run       *bool `json:"run,omitempty"`
	Graph     *bool `json:"graph,omitempty"`
	List      *bool `json:"list,omitempty"`
	Status    *bool `json:"status,omitempty"`
	Activity  *bool `json:"activity,omitempty"`
	TaskGraph *bool `json:"taskGraph,omitempty"`
	Message   *bool `json:"message,omitempty"`
	Collect   *bool `json:"collect,omitempty"`
	Cancel    *bool `json:"cancel,omitempty"`
}

type assistantGuardrailFeatures struct {
	Builtin *bool `json:"builtin,omitempty"`
}

type assistantModeFeatures struct {
	Instructions  *bool `json:"instructions,omitempty"`
	PhaseTracking *bool `json:"phaseTracking,omitempty"`
	ModelRouting  *bool `json:"modelRouting,omitempty"`
}

type assistantProjectStateFeatures struct {
	PrimeContext *bool `json:"primeContext,omitempty"`
	TaskTools    *bool `json:"taskTools,omitempty"`
	MemoryTools  *bool `json:"memoryTools,omitempty"`
	PrimeTool    *bool `json:"primeTool,omitempty"`
}

type assistantRuntimeFeatures struct {
	Compaction            *bool `json:"compaction,omitempty"`
	Approval              *bool `json:"approval,omitempty"`
	Retry                 *bool `json:"retry,omitempty"`
	ForceFinalSummary     *bool `json:"forceFinalSummary,omitempty"`
	EventStream           *bool `json:"eventStream,omitempty"`
	Tracing               *bool `json:"tracing,omitempty"`
	ImmediateInputPolling *bool `json:"immediateInputPolling,omitempty"`
	HandoffHistory        *bool `json:"handoffHistory,omitempty"`
	ParallelToolCalls     *bool `json:"parallelToolCalls,omitempty"`
	UntrustedToolOutputs  *bool `json:"untrustedToolOutputs,omitempty"`
}

func runtimeFeatures(cfg appConfig, extensions extensionBundle, audit *auditRecorder) sdkruntime.Features {
	features := assistantDefaultRuntimeFeatures(cfg, extensions, audit)
	if cfg.FeatureOverrides.Defaults != nil && !*cfg.FeatureOverrides.Defaults {
		features = sdkruntime.Features{}
	}
	applyFeatureOverrides(&features, cfg.FeatureOverrides)
	return features
}

func assistantDefaultRuntimeFeatures(cfg appConfig, extensions extensionBundle, audit *auditRecorder) sdkruntime.Features {
	sdkTools := cfg.EnableTools
	return sdkruntime.Features{
		Tools: sdkruntime.ToolFeatures{
			ListFiles:      sdkTools,
			ReadFile:       sdkTools,
			Glob:           sdkTools,
			Grep:           sdkTools,
			LSP:            sdkTools,
			Bash:           sdkTools,
			Write:          sdkTools,
			Edit:           sdkTools,
			WebFetch:       sdkTools,
			ExtraTools:     sdkTools && len(extensions.ExtraTools) > 0,
			VisionAnalyzer: sdkTools,
			Signals: sdkruntime.SignalFeatures{
				AskUserQuestion: sdkTools,
				PresentPlan:     sdkTools,
				Finish:          sdkTools,
				SetPhase:        sdkTools,
			},
		},
		MCP: sdkruntime.MCPFeatures{
			Enabled:         cfg.EnableMCP,
			AllowAllServers: cfg.EnableMCP,
			AllowAllTools:   cfg.EnableMCP,
			ResourceTools:   cfg.EnableMCP,
		},
		Guardrails: sdkruntime.GuardrailFeatures{
			Builtin: cfg.EnableGuardrails,
		},
		Modes: sdkruntime.ModeFeatures{
			Instructions:  true,
			PhaseTracking: true,
			ModelRouting:  true,
		},
		Runtime: sdkruntime.RuntimeFeatures{
			Compaction:           cfg.EnableCompaction,
			Approval:             cfg.EnableApproval,
			Retry:                true,
			Tracing:              audit != nil,
			HandoffHistory:       cfg.EnableCompaction,
			ParallelToolCalls:    true,
			UntrustedToolOutputs: true,
		},
	}
}

func applyFeatureOverrides(features *sdkruntime.Features, overrides assistantFeaturesConfig) {
	if features == nil {
		return
	}
	if tools := overrides.Tools; tools != nil {
		setBool(&features.Tools.ListFiles, tools.ListFiles)
		setBool(&features.Tools.ReadFile, tools.ReadFile)
		setBool(&features.Tools.Glob, tools.Glob)
		setBool(&features.Tools.Grep, tools.Grep)
		setBool(&features.Tools.LSP, tools.LSP)
		setBool(&features.Tools.Bash, tools.Bash)
		setBool(&features.Tools.Write, tools.Write)
		setBool(&features.Tools.Edit, tools.Edit)
		setBool(&features.Tools.WebFetch, tools.WebFetch)
		setBool(&features.Tools.AsyncShell, tools.AsyncShell)
		setBool(&features.Tools.ExtraTools, tools.ExtraTools)
		setBool(&features.Tools.VisionAnalyzer, tools.VisionAnalyzer)
		if signals := tools.Signals; signals != nil {
			setBool(&features.Tools.Signals.AskUserQuestion, signals.AskUserQuestion)
			setBool(&features.Tools.Signals.PresentPlan, signals.PresentPlan)
			setBool(&features.Tools.Signals.Finish, signals.Finish)
			setBool(&features.Tools.Signals.SetPhase, signals.SetPhase)
		}
	}
	if mcp := overrides.MCP; mcp != nil {
		setBool(&features.MCP.Enabled, mcp.Enabled)
		setBool(&features.MCP.AllowAllServers, mcp.AllowAllServers)
		setBool(&features.MCP.AllowAllTools, mcp.AllowAllTools)
		setBool(&features.MCP.ResourceTools, mcp.ResourceTools)
		if mcp.AllowedServers != nil {
			features.MCP.AllowedServers = append([]string(nil), mcp.AllowedServers...)
		}
		if mcp.AllowedTools != nil {
			features.MCP.AllowedTools = append([]string(nil), mcp.AllowedTools...)
		}
	}
	if handoffs := overrides.Handoffs; handoffs != nil {
		setBool(&features.Handoffs.Enabled, handoffs.Enabled)
		setBool(&features.Handoffs.GenericFallback, handoffs.GenericFallback)
	}
	if subAgents := overrides.SubAgents; subAgents != nil {
		setBool(&features.SubAgents.SyncTools, subAgents.SyncTools)
		setBool(&features.SubAgents.GenericFallback, subAgents.GenericFallback)
		if async := subAgents.Async; async != nil {
			setBool(&features.SubAgents.Async.Spawn, async.Spawn)
			setBool(&features.SubAgents.Async.Run, async.Run)
			setBool(&features.SubAgents.Async.Graph, async.Graph)
			setBool(&features.SubAgents.Async.List, async.List)
			setBool(&features.SubAgents.Async.Status, async.Status)
			setBool(&features.SubAgents.Async.Activity, async.Activity)
			setBool(&features.SubAgents.Async.TaskGraph, async.TaskGraph)
			setBool(&features.SubAgents.Async.Message, async.Message)
			setBool(&features.SubAgents.Async.Collect, async.Collect)
			setBool(&features.SubAgents.Async.Cancel, async.Cancel)
		}
	}
	if guardrails := overrides.Guardrails; guardrails != nil {
		setBool(&features.Guardrails.Builtin, guardrails.Builtin)
	}
	if modes := overrides.Modes; modes != nil {
		setBool(&features.Modes.Instructions, modes.Instructions)
		setBool(&features.Modes.PhaseTracking, modes.PhaseTracking)
		setBool(&features.Modes.ModelRouting, modes.ModelRouting)
	}
	if projectState := overrides.ProjectState; projectState != nil {
		setBool(&features.ProjectState.PrimeContext, projectState.PrimeContext)
		setBool(&features.ProjectState.TaskTools, projectState.TaskTools)
		setBool(&features.ProjectState.MemoryTools, projectState.MemoryTools)
		setBool(&features.ProjectState.PrimeTool, projectState.PrimeTool)
	}
	if runtime := overrides.Runtime; runtime != nil {
		setBool(&features.Runtime.Compaction, runtime.Compaction)
		setBool(&features.Runtime.Approval, runtime.Approval)
		setBool(&features.Runtime.Retry, runtime.Retry)
		setBool(&features.Runtime.ForceFinalSummary, runtime.ForceFinalSummary)
		setBool(&features.Runtime.EventStream, runtime.EventStream)
		setBool(&features.Runtime.Tracing, runtime.Tracing)
		setBool(&features.Runtime.ImmediateInputPolling, runtime.ImmediateInputPolling)
		setBool(&features.Runtime.HandoffHistory, runtime.HandoffHistory)
		setBool(&features.Runtime.ParallelToolCalls, runtime.ParallelToolCalls)
		setBool(&features.Runtime.UntrustedToolOutputs, runtime.UntrustedToolOutputs)
	}
}

func setBool(dst *bool, src *bool) {
	if dst == nil || src == nil {
		return
	}
	*dst = *src
}
