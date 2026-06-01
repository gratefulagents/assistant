// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"strings"
	"testing"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
)

func TestConversationStoreUsesChannelAndThread(t *testing.T) {
	store := newConversationStore()
	first := store.sessionFor(inboundMessage{Channel: "telegram", UserID: "alice", Thread: "chat 1"})
	sameThread := store.sessionFor(inboundMessage{Channel: "telegram", UserID: "bob", Thread: "chat 1"})
	if first != sameThread {
		t.Fatal("same channel/thread did not reuse conversation session")
	}

	otherThread := store.sessionFor(inboundMessage{Channel: "telegram", UserID: "alice", Thread: "chat 2"})
	if otherThread == first {
		t.Fatal("different thread reused conversation session")
	}

	userFallback := store.sessionFor(inboundMessage{Channel: "generic", UserID: "alice"})
	sameUserFallback := store.sessionFor(inboundMessage{Channel: "generic", UserID: "alice"})
	if userFallback != sameUserFallback {
		t.Fatal("same channel/user fallback did not reuse conversation session")
	}
}

func TestConversationKeyPrefersThreadThenUser(t *testing.T) {
	if got := conversationKey(inboundMessage{Channel: "Telegram", UserID: "alice", Thread: "Room 1"}); got != "telegram:room_1" {
		t.Fatalf("conversationKey with thread = %q", got)
	}
	if got := conversationKey(inboundMessage{Channel: "generic", UserID: "Alice@example.com"}); got != "generic:alice@example.com" {
		t.Fatalf("conversationKey with user fallback = %q", got)
	}
}

func TestTelegramInboundPromptIncludesChatIDForScheduleDelivery(t *testing.T) {
	prompt := inboundPrompt(inboundMessage{
		Channel: "telegram",
		UserID:  "alice",
		Thread:  "12345",
	}, "send me weather every morning")
	for _, want := range []string{"Telegram chat_id for this conversation: 12345", "deliver.chat_id"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q in %q", want, prompt)
		}
	}
}

func TestSlashCommandsUpdateSessionWithoutModelRun(t *testing.T) {
	store := newConversationStore()
	msg := inboundMessage{Channel: "generic", UserID: "alice", Text: "/plan"}
	reply, err := replyToInbound(t.Context(), appConfig{}, msg, nil, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "mode set to plan" {
		t.Fatalf("reply = %q, want plan acknowledgement", reply)
	}

	session := store.sessionFor(msg)
	session.mu.Lock()
	if session.currentModeLocked() != conversationModePlan {
		t.Fatalf("mode = %q, want plan", session.currentModeLocked())
	}
	session.history = []agentsdk.RunItem{userMessage("remember this")}
	session.mu.Unlock()

	reply, err = replyToInbound(t.Context(), appConfig{}, inboundMessage{Channel: "generic", UserID: "alice", Text: "/clear"}, nil, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "history cleared" {
		t.Fatalf("clear reply = %q", reply)
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.history) != 0 {
		t.Fatalf("history length = %d, want 0", len(session.history))
	}
	if session.currentModeLocked() != conversationModePlan {
		t.Fatalf("clear changed mode to %q, want plan", session.currentModeLocked())
	}
}

func TestSlashCommandsSupportTelegramMentionSuffix(t *testing.T) {
	session := newConversationSession()
	got := handleSlashCommand("/clear@TestAssistantBot", session, false)
	if !got.Handled || got.Reply != "history cleared" {
		t.Fatalf("telegram mention command = %#v, want clear acknowledgement", got)
	}
}

func TestSlashStartShowsHelp(t *testing.T) {
	got := handleSlashCommand("/start", newConversationSession(), false)
	if !got.Handled || !strings.Contains(got.Reply, "/clear - clear this conversation's history") {
		t.Fatalf("/start result = %#v, want help", got)
	}
}

func TestSlashVersionShowsBuildInfo(t *testing.T) {
	got := handleSlashCommand("/version", newConversationSession(), false)
	if !got.Handled {
		t.Fatalf("/version result = %#v, want handled", got)
	}
	for _, want := range []string{"assistant ", "commit:", "built:", "go:"} {
		if !strings.Contains(got.Reply, want) {
			t.Fatalf("/version reply missing %q in %q", want, got.Reply)
		}
	}
}

func TestSlashExitOnlyAllowedInREPL(t *testing.T) {
	if got := handleSlashCommand("/exit", newConversationSession(), false); !got.Handled || got.Exit {
		t.Fatalf("non-REPL /exit result = %#v, want handled non-exit", got)
	}
	if got := handleSlashCommand("/exit", newConversationSession(), true); !got.Handled || !got.Exit {
		t.Fatalf("REPL /exit result = %#v, want exit", got)
	}
}

func TestApplyConversationMode(t *testing.T) {
	base := defaultConfig()
	base.Permission = string(sdkpolicy.PermissionModeWorkspaceWrite)

	chat := applyConversationMode(base, conversationModeChat)
	if chat.SessionMode != agentsdk.SessionModeChat || chat.ActiveMode != "assistant" || chat.ActivePhase != "chat" {
		t.Fatalf("chat config = %#v", chat)
	}
	if chat.Permission != string(sdkpolicy.PermissionModeWorkspaceWrite) {
		t.Fatalf("chat permission = %q, want original workspace-write", chat.Permission)
	}

	plan := applyConversationMode(base, conversationModePlan)
	if plan.SessionMode != agentsdk.SessionModePlan || plan.ActiveMode != "plan" || plan.ActivePhase != "plan" {
		t.Fatalf("plan config = %#v", plan)
	}
	if plan.Permission != string(sdkpolicy.PermissionModeReadOnly) {
		t.Fatalf("plan permission = %q, want read-only", plan.Permission)
	}
	if !strings.Contains(plan.ModeDirectiveText, "Plan mode is active") {
		t.Fatalf("plan directive = %q", plan.ModeDirectiveText)
	}

	custom := applyConversationMode(base, "/Mode Name!")
	if custom.SessionMode != agentsdk.SessionModeChat || custom.ActiveMode != "modename" || custom.ActivePhase != "modename" {
		t.Fatalf("custom config = %#v", custom)
	}
}
