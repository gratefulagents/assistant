// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
)

const (
	conversationModeChat = "chat"
	conversationModePlan = "plan"
)

type conversationSession struct {
	mu              sync.Mutex
	mode            string
	transcriptID    string
	history         []agentsdk.RunItem
	stateMu         sync.Mutex
	running         bool
	approvalMu      sync.Mutex
	pendingApproval *conversationApproval
}

type approvalDecision struct {
	Approved bool
	Reason   string
}

type conversationApproval struct {
	ID        string
	ToolName  string
	Input     json.RawMessage
	CallID    string
	CreatedAt time.Time
	Decision  chan approvalDecision
}

type conversationApprovalSnapshot struct {
	ID        string
	ToolName  string
	Input     json.RawMessage
	CallID    string
	CreatedAt time.Time
}

func newConversationSession() *conversationSession {
	return &conversationSession{mode: conversationModeChat, transcriptID: newTranscriptID("sess")}
}

func (s *conversationSession) currentModeLocked() string {
	if s == nil || strings.TrimSpace(s.mode) == "" {
		return conversationModeChat
	}
	return s.mode
}

func (s *conversationSession) setModeLocked(mode string) string {
	if s == nil {
		return conversationModeChat
	}
	mode = normalizeConversationModeName(mode)
	if mode == "" {
		mode = conversationModeChat
	}
	s.mode = mode
	return mode
}

func (s *conversationSession) clearHistoryLocked() {
	if s != nil {
		s.history = nil
	}
}

func (s *conversationSession) beginRun() bool {
	if s == nil {
		return true
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *conversationSession) finishRun() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.running = false
	s.stateMu.Unlock()
	s.clearApproval("")
}

func (s *conversationSession) isRunning() bool {
	if s == nil {
		return false
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.running
}

func (s *conversationSession) openApproval(id string, pending *agentsdk.Interruption) (*conversationApproval, bool) {
	if s == nil || pending == nil || strings.TrimSpace(id) == "" {
		return nil, false
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	if s.pendingApproval != nil {
		return nil, false
	}
	approval := &conversationApproval{
		ID:        strings.TrimSpace(id),
		ToolName:  pending.ToolName,
		Input:     cloneRaw(pending.ToolInput),
		CallID:    pending.ToolCallID,
		CreatedAt: time.Now(),
		Decision:  make(chan approvalDecision, 1),
	}
	s.pendingApproval = approval
	return approval, true
}

func (s *conversationSession) pendingApprovalSnapshot() (conversationApprovalSnapshot, bool) {
	if s == nil {
		return conversationApprovalSnapshot{}, false
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	if s.pendingApproval == nil {
		return conversationApprovalSnapshot{}, false
	}
	return s.pendingApproval.snapshot(), true
}

func (s *conversationSession) decideApproval(id string, decision approvalDecision) (conversationApprovalSnapshot, bool) {
	if s == nil || strings.TrimSpace(id) == "" {
		return conversationApprovalSnapshot{}, false
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	if s.pendingApproval == nil || s.pendingApproval.ID != id {
		return conversationApprovalSnapshot{}, false
	}
	approval := s.pendingApproval
	s.pendingApproval = nil
	select {
	case approval.Decision <- decision:
	default:
	}
	return approval.snapshot(), true
}

func (s *conversationSession) clearApproval(id string) {
	if s == nil {
		return
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	if s.pendingApproval == nil {
		return
	}
	if strings.TrimSpace(id) == "" || s.pendingApproval.ID == id {
		s.pendingApproval = nil
	}
}

func (a *conversationApproval) snapshot() conversationApprovalSnapshot {
	if a == nil {
		return conversationApprovalSnapshot{}
	}
	return conversationApprovalSnapshot{
		ID:        a.ID,
		ToolName:  a.ToolName,
		Input:     cloneRaw(a.Input),
		CallID:    a.CallID,
		CreatedAt: a.CreatedAt,
	}
}

type conversationStore struct {
	mu       sync.Mutex
	sessions map[string]*conversationSession
}

func newConversationStore() *conversationStore {
	return &conversationStore{sessions: map[string]*conversationSession{}}
}

func (s *conversationStore) sessionFor(msg inboundMessage) *conversationSession {
	if s == nil {
		return newConversationSession()
	}
	key := conversationKey(msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[key]; session != nil {
		return session
	}
	session := newConversationSession()
	s.sessions[key] = session
	return session
}

func conversationKey(msg inboundMessage) string {
	channel := normalizeConversationKeyPart(firstNonEmpty(msg.Channel, "generic"))
	id := firstNonEmpty(msg.Thread, msg.UserID, "default")
	return channel + ":" + normalizeConversationKeyPart(id)
}

func normalizeConversationKeyPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.', r == ':', r == '@':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

type slashCommandResult struct {
	Handled bool
	Reply   string
	Exit    bool
}

func handleSlashCommand(text string, session *conversationSession, allowExit bool) slashCommandResult {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return slashCommandResult{}
	}
	command, arg, _ := strings.Cut(trimmed, " ")
	command = strings.ToLower(strings.TrimSpace(command))
	if base, _, ok := strings.Cut(command, "@"); ok {
		command = base
	}
	arg = strings.TrimSpace(arg)

	switch command {
	case "/start":
		return slashCommandResult{Handled: true, Reply: slashCommandHelp()}
	case "/version":
		return slashCommandResult{Handled: true, Reply: versionText()}
	case "/exit", "/quit":
		if allowExit {
			return slashCommandResult{Handled: true, Exit: true}
		}
		return slashCommandResult{Handled: true, Reply: "That command is only available in the interactive REPL."}
	case "/clear":
		if session != nil {
			session.mu.Lock()
			session.clearHistoryLocked()
			session.mu.Unlock()
		}
		return slashCommandResult{Handled: true, Reply: "history cleared"}
	case "/plan":
		return setSessionModeCommand(session, conversationModePlan)
	case "/chat":
		return setSessionModeCommand(session, conversationModeChat)
	case "/mode":
		mode := normalizeConversationModeName(arg)
		if mode == "" {
			return slashCommandResult{Handled: true, Reply: "usage: /mode <name>"}
		}
		return setSessionModeCommand(session, mode)
	case "/stop":
		return slashCommandResult{Handled: true, Reply: "no active run to stop"}
	case "/help":
		return slashCommandResult{Handled: true, Reply: slashCommandHelp()}
	default:
		return slashCommandResult{Handled: true, Reply: "unknown command: " + command + "\n\n" + slashCommandHelp()}
	}
}

func setSessionModeCommand(session *conversationSession, mode string) slashCommandResult {
	if session != nil {
		session.mu.Lock()
		mode = session.setModeLocked(mode)
		session.mu.Unlock()
	} else {
		mode = normalizeConversationModeName(mode)
	}
	return slashCommandResult{Handled: true, Reply: "mode set to " + mode}
}

func slashCommandHelp() string {
	return strings.Join([]string{
		"commands:",
		"/start - show this help",
		"/help - show this help",
		"/version - show assistant version and build information",
		"/plan - switch this conversation to planning mode",
		"/chat - switch this conversation to chat mode",
		"/mode <name> - set a custom mode label",
		"/clear - clear this conversation's history",
		"/stop - stop an active run when supported",
	}, "\n")
}

func normalizeConversationModeName(mode string) string {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		return ""
	}
	switch mode {
	case "assistant", "default":
		return conversationModeChat
	}
	var b strings.Builder
	for _, r := range mode {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func applyConversationMode(cfg appConfig, mode string) appConfig {
	mode = normalizeConversationModeName(mode)
	if mode == "" {
		mode = conversationModeChat
	}

	cfg.SessionMode = agentsdk.SessionModeChat
	cfg.ActiveMode = "assistant"
	cfg.ActivePhase = conversationModeChat
	cfg.ModeDirectiveText = ""

	switch mode {
	case conversationModeChat:
		return cfg
	case conversationModePlan:
		cfg.SessionMode = agentsdk.SessionModePlan
		cfg.ActiveMode = conversationModePlan
		cfg.ActivePhase = conversationModePlan
		cfg.Permission = string(sdkpolicy.PermissionModeReadOnly)
		cfg.ModeDirectiveText = strings.Join([]string{
			"Plan mode is active for this conversation.",
			"Focus on understanding, tradeoffs, and a concrete plan.",
			"Use read-only inspection tools when useful, but do not modify files or take externally visible actions until the user switches back to chat mode.",
		}, " ")
		return cfg
	default:
		cfg.ActiveMode = mode
		cfg.ActivePhase = mode
		cfg.ModeDirectiveText = "Custom mode " + mode + " is active for this conversation. Follow the user's request under that mode label while preserving the default assistant behavior."
		return cfg
	}
}
