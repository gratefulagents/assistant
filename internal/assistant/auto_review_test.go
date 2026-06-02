// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

type fakeApprovalRequester struct {
	decision approvalDecision
	err      error
	called   bool
	items    int
}

func (f *fakeApprovalRequester) RequestApproval(_ context.Context, _ *agentsdk.Interruption, reqCtx approvalRequestContext) (approvalDecision, error) {
	f.called = true
	f.items = len(reqCtx.Items)
	return f.decision, f.err
}

func TestNormalizeApprovalsReviewerAliases(t *testing.T) {
	tests := map[string]string{
		"":                  approvalReviewerUser,
		"user":              approvalReviewerUser,
		"human":             approvalReviewerUser,
		"auto":              approvalReviewerAutoReview,
		"auto_review":       approvalReviewerAutoReview,
		"guardian":          approvalReviewerAutoReview,
		"guardian_subagent": approvalReviewerAutoReview,
		"bogus":             "",
	}
	for input, want := range tests {
		if got := normalizeApprovalsReviewer(input); got != want {
			t.Fatalf("normalizeApprovalsReviewer(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateAppliesApprovalsConfigFile(t *testing.T) {
	t.Setenv("ASSISTANT_APPROVALS_REVIEWER_MODEL", "")
	configPath := filepath.Join(t.TempDir(), "assistant.json")
	if err := os.WriteFile(configPath, []byte(`{
		"approvals": {
			"reviewer": "guardian_subagent",
			"reviewerModel": "gpt-test-reviewer",
			"reviewerTimeout": 37
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.ConfigPath = configPath
	cfg.Provider = providerOpenAIAPI
	cfg.APIKey = "sk-test"
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.ApprovalsReviewer != approvalReviewerAutoReview {
		t.Fatalf("ApprovalsReviewer = %q, want auto-review", cfg.ApprovalsReviewer)
	}
	if cfg.ApprovalsReviewerModel != "gpt-test-reviewer" {
		t.Fatalf("ApprovalsReviewerModel = %q", cfg.ApprovalsReviewerModel)
	}
	if cfg.ApprovalsReviewerTimeout != 37 {
		t.Fatalf("ApprovalsReviewerTimeout = %d, want 37", cfg.ApprovalsReviewerTimeout)
	}
}

func TestApprovalsFlagsOverrideConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "assistant.json")
	if err := os.WriteFile(configPath, []byte(`{
		"approvals": {
			"reviewer": "auto-review",
			"reviewerModel": "file-reviewer",
			"reviewerTimeout": 37
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig([]string{
		"--config", configPath,
		"--provider", "openai-api",
		"--api-key", "sk-test",
		"--approvals-reviewer", "user",
		"--approvals-reviewer-model", "cli-reviewer",
		"--approvals-reviewer-timeout", "13",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.ApprovalsReviewer != approvalReviewerUser {
		t.Fatalf("ApprovalsReviewer = %q, want user from flag", cfg.ApprovalsReviewer)
	}
	if cfg.ApprovalsReviewerModel != "cli-reviewer" {
		t.Fatalf("ApprovalsReviewerModel = %q, want cli-reviewer", cfg.ApprovalsReviewerModel)
	}
	if cfg.ApprovalsReviewerTimeout != 13 {
		t.Fatalf("ApprovalsReviewerTimeout = %d, want 13", cfg.ApprovalsReviewerTimeout)
	}
}

func TestAutoReviewRequesterAllowsAndDenies(t *testing.T) {
	pending := &agentsdk.Interruption{
		ToolName:   "shell",
		ToolInput:  json.RawMessage(`{"cmd":"pwd"}`),
		ToolCallID: "call-1",
	}
	for _, tc := range []struct {
		name     string
		outcome  string
		approved bool
	}{
		{name: "allow", outcome: "allow", approved: true},
		{name: "deny", outcome: "deny", approved: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requester := autoReviewApprovalRequester{
				review: func(context.Context, *agentsdk.Interruption, approvalRequestContext) (approvalReviewAssessment, error) {
					return approvalReviewAssessment{
						Outcome:           tc.outcome,
						RiskLevel:         "low",
						UserAuthorization: "high",
						Rationale:         "test decision",
					}, nil
				},
			}
			decision, err := requester.RequestApproval(t.Context(), pending, approvalRequestContext{})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Approved != tc.approved {
				t.Fatalf("Approved = %v, want %v", decision.Approved, tc.approved)
			}
			if !strings.Contains(decision.Reason, "auto-review") {
				t.Fatalf("Reason = %q, want auto-review context", decision.Reason)
			}
		})
	}
}

func TestAutoReviewRequesterEscalatesToFallback(t *testing.T) {
	pending := &agentsdk.Interruption{
		ToolName:   "shell",
		ToolInput:  json.RawMessage(`{"cmd":"touch x"}`),
		ToolCallID: "call-1",
	}
	fallback := &fakeApprovalRequester{decision: approvalDecision{Approved: true, Reason: "human approved"}}
	requester := autoReviewApprovalRequester{
		fallback: fallback,
		review: func(context.Context, *agentsdk.Interruption, approvalRequestContext) (approvalReviewAssessment, error) {
			return approvalReviewAssessment{
				Outcome:           "escalate",
				RiskLevel:         "high",
				UserAuthorization: "unknown",
				Rationale:         "external side effect needs confirmation",
			}, nil
		},
	}
	decision, err := requester.RequestApproval(t.Context(), pending, approvalRequestContext{Items: []agentsdk.RunItem{userMessage("please do it")}})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved || !fallback.called {
		t.Fatalf("decision = %+v, fallback called = %v", decision, fallback.called)
	}
	if fallback.items != 1 {
		t.Fatalf("fallback saw %d context items, want 1", fallback.items)
	}
}

func TestAutoReviewRequesterDeniesWhenEscalationUnavailable(t *testing.T) {
	pending := &agentsdk.Interruption{
		ToolName:   "shell",
		ToolInput:  json.RawMessage(`{"cmd":"rm -rf build"}`),
		ToolCallID: "call-1",
	}
	requester := autoReviewApprovalRequester{
		review: func(context.Context, *agentsdk.Interruption, approvalRequestContext) (approvalReviewAssessment, error) {
			return approvalReviewAssessment{}, errors.New("review timeout")
		},
	}
	decision, err := requester.RequestApproval(t.Context(), pending, approvalRequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Approved {
		t.Fatal("Approved = true, want denial when review fails and no fallback exists")
	}
	for _, want := range []string{"review timeout", "no human approval requester"} {
		if !strings.Contains(decision.Reason, want) {
			t.Fatalf("Reason = %q, want %q", decision.Reason, want)
		}
	}
}

func TestParseApprovalReviewAssessment(t *testing.T) {
	got, err := parseApprovalReviewAssessment(map[string]any{
		"outcome":            "approve",
		"risk_level":         "medium",
		"user_authorization": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != autoReviewOutcomeAllow {
		t.Fatalf("Outcome = %q, want allow", got.Outcome)
	}

	got, err = parseApprovalReviewAssessment(`{"outcome":"escalate","risk_level":"high","user_authorization":"unknown"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != autoReviewOutcomeEscalate {
		t.Fatalf("Outcome = %q, want escalate", got.Outcome)
	}
}
