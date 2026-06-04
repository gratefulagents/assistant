// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const telegramAPIBase = "https://api.telegram.org/bot"
const telegramFileAPIBase = "https://api.telegram.org/file/bot"
const telegramMessageChunkRunes = 3800
const telegramMaxImageBytes = 12 << 20
const telegramMaxInboundImages = 8

// Base URLs are vars so tests can point them at a stub server and so
// --telegram-api-base can redirect the bot/file APIs at an ingress proxy.
var telegramAPIBaseURL = telegramAPIBase
var telegramFileAPIBaseURL = telegramFileAPIBase

// applyTelegramAPIBase redirects the Telegram bot and file APIs at an alternate
// root (e.g. an ingress gateway that multiplexes a shared bot's update stream).
// The value is the API root with no trailing path — the standard "/bot<token>"
// and "/file/bot<token>" suffixes are appended exactly as for api.telegram.org,
// so a backend believes it is talking to a private bot. An empty value keeps the
// public Telegram defaults.
func applyTelegramAPIBase(base string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return
	}
	telegramAPIBaseURL = base + "/bot"
	telegramFileAPIBaseURL = base + "/file/bot"
}

var telegramApprovalSeq atomic.Uint64

type telegramOffsetState struct {
	Offset int64 `json:"offset"`
}

type telegramUpdate struct {
	UpdateID      int64                 `json:"update_id"`
	Message       telegramMessage       `json:"message"`
	CallbackQuery telegramCallbackQuery `json:"callback_query"`
}

type telegramMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Caption   string `json:"caption"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From     telegramUser        `json:"from"`
	Photo    []telegramPhotoSize `json:"photo"`
	Document *telegramDocument   `json:"document"`
}

type telegramPhotoSize struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type telegramDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type telegramCallbackQuery struct {
	ID      string          `json:"id"`
	Data    string          `json:"data"`
	From    telegramUser    `json:"from"`
	Message telegramMessage `json:"message"`
}

type telegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type telegramUpdatesResponse struct {
	OK          bool             `json:"ok"`
	Description string           `json:"description"`
	Result      []telegramUpdate `json:"result"`
}

type telegramBotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

func telegramBotCommands() []telegramBotCommand {
	return []telegramBotCommand{
		{Command: "start", Description: "Show assistant commands"},
		{Command: "help", Description: "Show assistant commands"},
		{Command: "version", Description: "Show assistant version and build information"},
		{Command: "clear", Description: "Clear this chat's assistant history"},
		{Command: "plan", Description: "Switch this chat to planning mode"},
		{Command: "chat", Description: "Switch this chat to chat mode"},
		{Command: "stop", Description: "Stop an active run when supported"},
	}
}

func telegramConfigureBot(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return postJSON(ctx, telegramAPIBaseURL+token+"/setMyCommands", "", map[string]any{
		"commands": telegramBotCommands(),
	})
}

func runTelegramPoller(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	token := strings.TrimSpace(cfg.TelegramBotToken)
	if token == "" {
		return errors.New("telegram polling requires --telegram-bot-token or ASSISTANT_TELEGRAM_BOT_TOKEN")
	}
	conversations := newConversationStore()
	offset, err := loadTelegramOffset(cfg)
	if err != nil {
		return err
	}
	if err := telegramConfigureBot(ctx, token); err != nil {
		fmt.Fprintf(stderr, "telegram menu warning: %v\n", err)
	}
	if len(cfg.TelegramAllowedUsers) == 0 && len(cfg.TelegramAllowedChats) == 0 {
		fmt.Fprintf(stderr, "telegram access allowlist is empty; incoming messages will be ignored\n")
	}
	fmt.Fprintf(stderr, "assistant telegram polling; no inbound port required\n")
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		updates, err := fetchTelegramUpdates(ctx, token, offset, cfg.TelegramPollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(stderr, "telegram poll warning: %v\n", err)
			if !sleepContext(ctx, 5*time.Second) {
				return nil
			}
			continue
		}
		for _, update := range updates {
			nextOffset := update.UpdateID + 1
			if nextOffset <= offset {
				continue
			}
			offset = nextOffset
			if err := handleTelegramUpdate(ctx, cfg, stdout, stderr, token, update, conversations); err != nil {
				fmt.Fprintf(stderr, "telegram message warning: %v\n", err)
			}
			if err := saveTelegramOffset(cfg, offset); err != nil {
				return err
			}
		}
	}
}

func fetchTelegramUpdates(ctx context.Context, token string, offset int64, timeoutSeconds int) ([]telegramUpdate, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 50
	}
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSeconds,
		"allowed_updates": []string{"message", "callback_query"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := telegramAPIBaseURL + token + "/getUpdates"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: time.Duration(timeoutSeconds+10) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram getUpdates: %s: %s", resp.Status, firstLine(string(data)))
	}
	updates, err := decodeTelegramUpdates(data)
	if err != nil {
		return nil, err
	}
	if !updates.OK {
		return nil, fmt.Errorf("telegram getUpdates failed: %s", updates.Description)
	}
	return updates.Result, nil
}

func decodeTelegramUpdates(data []byte) (telegramUpdatesResponse, error) {
	var out telegramUpdatesResponse
	err := json.Unmarshal(data, &out)
	return out, err
}

type telegramFileResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		FileID   string `json:"file_id"`
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	} `json:"result"`
}

// downloadTelegramImages collects image attachments (photos and image documents)
// from an inbound message and returns them as base64-encoded attachments.
func downloadTelegramImages(ctx context.Context, token string, message telegramMessage) ([]agentsdk.ImageAttachment, error) {
	type pending struct {
		fileID    string
		mediaType string
	}
	var queue []pending
	if photo, ok := largestTelegramPhoto(message.Photo); ok {
		queue = append(queue, pending{fileID: photo.FileID, mediaType: "image/jpeg"})
	}
	if doc := message.Document; doc != nil {
		mediaType := strings.ToLower(strings.TrimSpace(doc.MimeType))
		if telegramSupportedImageType(mediaType) {
			queue = append(queue, pending{fileID: doc.FileID, mediaType: mediaType})
		}
	}
	var (
		images []agentsdk.ImageAttachment
		errs   []string
	)
	for _, item := range queue {
		if len(images) >= telegramMaxInboundImages {
			break
		}
		attachment, err := downloadTelegramImage(ctx, token, item.fileID, item.mediaType)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		images = append(images, attachment)
	}
	if len(errs) > 0 {
		return images, errors.New(strings.Join(errs, "; "))
	}
	return images, nil
}

func largestTelegramPhoto(sizes []telegramPhotoSize) (telegramPhotoSize, bool) {
	var best telegramPhotoSize
	found := false
	for _, size := range sizes {
		if strings.TrimSpace(size.FileID) == "" {
			continue
		}
		if !found || size.FileSize > best.FileSize || (size.FileSize == best.FileSize && size.Width*size.Height > best.Width*best.Height) {
			best = size
			found = true
		}
	}
	return best, found
}

func downloadTelegramImage(ctx context.Context, token, fileID, mediaType string) (agentsdk.ImageAttachment, error) {
	filePath, err := telegramResolveFilePath(ctx, token, fileID)
	if err != nil {
		return agentsdk.ImageAttachment{}, err
	}
	data, err := telegramDownloadFile(ctx, token, filePath)
	if err != nil {
		return agentsdk.ImageAttachment{}, err
	}
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = strings.ToLower(http.DetectContentType(data))
		if i := strings.IndexByte(mediaType, ';'); i >= 0 {
			mediaType = strings.TrimSpace(mediaType[:i])
		}
	}
	if !telegramSupportedImageType(mediaType) {
		return agentsdk.ImageAttachment{}, fmt.Errorf("telegram file %s is not a supported image type (%s)", fileID, mediaType)
	}
	return agentsdk.ImageAttachment{
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(data),
	}, nil
}

// telegramSupportedImageType reports whether a media type is accepted by the
// model providers for inbound image attachments.
func telegramSupportedImageType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func telegramResolveFilePath(ctx context.Context, token, fileID string) (string, error) {
	body, err := json.Marshal(map[string]any{"file_id": fileID})
	if err != nil {
		return "", err
	}
	url := telegramAPIBaseURL + token + "/getFile"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("telegram getFile: %s: %s", resp.Status, firstLine(string(data)))
	}
	var parsed telegramFileResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if !parsed.OK || strings.TrimSpace(parsed.Result.FilePath) == "" {
		return "", fmt.Errorf("telegram getFile failed: %s", firstLine(parsed.Description))
	}
	return parsed.Result.FilePath, nil
}

func telegramDownloadFile(ctx context.Context, token, filePath string) ([]byte, error) {
	url := telegramFileAPIBaseURL + token + "/" + filePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("telegram file download: %s: %s", resp.Status, firstLine(string(preview)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, telegramMaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > telegramMaxImageBytes {
		return nil, fmt.Errorf("telegram file exceeds %d byte limit", telegramMaxImageBytes)
	}
	return data, nil
}

func handleTelegramUpdate(ctx context.Context, cfg appConfig, stdout, stderr io.Writer, token string, update telegramUpdate, conversations *conversationStore) error {
	if strings.TrimSpace(update.CallbackQuery.ID) != "" {
		return handleTelegramCallbackQuery(ctx, cfg, stdout, stderr, token, update.CallbackQuery, conversations)
	}
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		text = strings.TrimSpace(update.Message.Caption)
	}
	hasMedia := len(update.Message.Photo) > 0 || update.Message.Document != nil
	if text == "" && !hasMedia {
		return nil
	}
	chatID := update.Message.Chat.ID
	if chatID == 0 {
		return nil
	}
	if !telegramAccessAllowed(cfg, chatID, update.Message.From) {
		fmt.Fprintf(stderr, "telegram access denied: chat=%d %s\n", chatID, telegramUserLogString(update.Message.From))
		return nil
	}
	userID := fmt.Sprintf("%d", update.Message.From.ID)
	if strings.TrimSpace(update.Message.From.Username) != "" {
		userID = update.Message.From.Username
	}
	msg := inboundMessage{
		Channel: "telegram",
		UserID:  userID,
		Thread:  fmt.Sprintf("%d", chatID),
		Text:    text,
	}
	session := conversations.sessionFor(msg)
	if handled, err := handleTelegramApprovalText(ctx, token, chatID, text, session); handled || err != nil {
		return err
	}
	if pending, ok := session.pendingApprovalSnapshot(); ok {
		return postTelegramApprovalMessage(ctx, token, chatID, pending, "Approval pending")
	}
	if session.isRunning() {
		return postTelegramMessage(ctx, token, chatID, "Still working on the previous message. I will reply here when it finishes.")
	}
	if command := handleSlashCommand(text, session, false); command.Handled {
		return postTelegramMessage(ctx, token, chatID, command.Reply)
	}
	if !session.beginRun() {
		return postTelegramMessage(ctx, token, chatID, "Still working on the previous message. I will reply here when it finishes.")
	}
	go runTelegramMessage(ctx, cfg, stdout, stderr, token, chatID, msg, update.Message, session)
	return nil
}

func handleTelegramCallbackQuery(ctx context.Context, cfg appConfig, stdout, stderr io.Writer, token string, query telegramCallbackQuery, conversations *conversationStore) error {
	chatID := query.Message.Chat.ID
	if chatID == 0 {
		return answerTelegramCallbackQuery(ctx, token, query.ID, "Action unavailable in this chat")
	}
	if !telegramAccessAllowed(cfg, chatID, query.From) {
		fmt.Fprintf(stderr, "telegram access denied: chat=%d %s\n", chatID, telegramUserLogString(query.From))
		return nil
	}
	msg := inboundMessage{
		Channel: "telegram",
		UserID:  telegramUserID(query.From),
		Thread:  fmt.Sprintf("%d", chatID),
	}
	session := conversations.sessionFor(msg)
	if approvalID, approved, ok := telegramApprovalCallback(query.Data); ok {
		return handleTelegramApprovalCallback(ctx, token, chatID, query, session, approvalID, approved)
	}
	if session.isRunning() {
		return answerTelegramCallbackQuery(ctx, token, query.ID, "Still working on the previous message")
	}
	command := telegramCallbackCommand(query.Data)
	if command == "" {
		return answerTelegramCallbackQuery(ctx, token, query.ID, "Unknown action")
	}
	reply, err := replyToInbound(ctx, cfg, inboundMessage{
		Channel: "telegram",
		UserID:  telegramUserID(query.From),
		Thread:  fmt.Sprintf("%d", chatID),
		Text:    command,
	}, stdout, stderr, conversations)
	if err != nil {
		_ = answerTelegramCallbackQuery(ctx, token, query.ID, "Action failed")
		return err
	}
	if err := answerTelegramCallbackQuery(ctx, token, query.ID, telegramCallbackNotice(command, reply)); err != nil {
		return err
	}
	return postTelegramMessage(ctx, token, chatID, reply)
}

func runTelegramMessage(ctx context.Context, cfg appConfig, stdout, stderr io.Writer, token string, chatID int64, msg inboundMessage, media telegramMessage, session *conversationSession) {
	defer session.finishRun()
	doneTyping := make(chan struct{})
	go telegramTypingLoop(ctx, token, chatID, doneTyping)
	defer close(doneTyping)

	if len(media.Photo) > 0 || media.Document != nil {
		images, err := downloadTelegramImages(ctx, token, media)
		if err != nil {
			fmt.Fprintf(stderr, "telegram image warning: %v\n", err)
		}
		msg.Images = images
	}

	approval := telegramApprovalRequester{token: token, chatID: chatID, session: session}
	reply, err := runPromptTextWithSessionApprovalImages(ctx, cfg, inboundPrompt(msg, msg.Text), msg.Images, stdout, stderr, session, approval)
	if err != nil {
		fmt.Fprintf(stderr, "telegram run warning: %v\n", err)
		if postErr := postTelegramMessage(ctx, token, chatID, "Run failed: "+firstLine(err.Error())); postErr != nil {
			fmt.Fprintf(stderr, "telegram reply warning: %v\n", postErr)
		}
		return
	}
	if strings.TrimSpace(reply) == "" {
		reply = "Done."
	}
	if err := postTelegramMessage(ctx, token, chatID, reply); err != nil {
		fmt.Fprintf(stderr, "telegram reply warning: %v\n", err)
	}
}

type telegramApprovalRequester struct {
	token   string
	chatID  int64
	session *conversationSession
}

func (r telegramApprovalRequester) RequestApproval(ctx context.Context, pending *agentsdk.Interruption, _ approvalRequestContext) (approvalDecision, error) {
	approvalID := newTelegramApprovalID()
	approval, ok := r.session.openApproval(approvalID, pending)
	if !ok {
		return approvalDecision{}, fmt.Errorf("tool %q requires approval but another approval is already pending", pending.ToolName)
	}
	defer r.session.clearApproval(approval.ID)
	if err := postTelegramApprovalMessage(ctx, r.token, r.chatID, approval.snapshot(), "Approval required"); err != nil {
		return approvalDecision{}, err
	}
	select {
	case decision := <-approval.Decision:
		return decision, nil
	case <-ctx.Done():
		return approvalDecision{}, ctx.Err()
	}
}

func newTelegramApprovalID() string {
	seq := telegramApprovalSeq.Add(1)
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(seq, 36)
}

func handleTelegramApprovalText(ctx context.Context, token string, chatID int64, text string, session *conversationSession) (bool, error) {
	pending, ok := session.pendingApprovalSnapshot()
	if !ok {
		return false, nil
	}
	approved, ok := telegramApprovalTextDecision(text)
	if !ok {
		return true, postTelegramApprovalMessage(ctx, token, chatID, pending, "Approval pending")
	}
	decision := approvalDecision{
		Approved: approved,
		Reason:   telegramApprovalDecisionReason(approved),
	}
	approval, decided := session.decideApproval(pending.ID, decision)
	if !decided {
		return true, postTelegramMessage(ctx, token, chatID, "That approval request is no longer pending.")
	}
	return true, postTelegramMessage(ctx, token, chatID, telegramApprovalDecisionNotice(approval, approved))
}

func handleTelegramApprovalCallback(ctx context.Context, token string, chatID int64, query telegramCallbackQuery, session *conversationSession, approvalID string, approved bool) error {
	decision := approvalDecision{
		Approved: approved,
		Reason:   telegramApprovalDecisionReason(approved),
	}
	approval, ok := session.decideApproval(approvalID, decision)
	if !ok {
		if query.Message.MessageID != 0 {
			_ = editTelegramMessageReplyMarkup(ctx, token, chatID, query.Message.MessageID)
		}
		return answerTelegramCallbackQuery(ctx, token, query.ID, "Approval is no longer pending")
	}
	if query.Message.MessageID != 0 {
		_ = editTelegramMessageReplyMarkup(ctx, token, chatID, query.Message.MessageID)
	}
	notice := telegramApprovalDecisionNotice(approval, approved)
	if err := answerTelegramCallbackQuery(ctx, token, query.ID, notice); err != nil {
		return err
	}
	return postTelegramMessage(ctx, token, chatID, notice)
}

func telegramApprovalTextDecision(text string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "yes", "approve", "approved", "allow", "/approve", "/yes":
		return true, true
	case "n", "no", "deny", "denied", "reject", "/deny", "/no":
		return false, true
	default:
		return false, false
	}
}

func telegramApprovalDecisionReason(approved bool) string {
	if approved {
		return "approved through Telegram"
	}
	return "tool call denied through Telegram"
}

func telegramApprovalDecisionNotice(approval conversationApprovalSnapshot, approved bool) string {
	action := "Denied"
	if approved {
		action = "Approved"
	}
	tool := strings.TrimSpace(approval.ToolName)
	if tool == "" {
		tool = "tool"
	}
	return action + " " + tool + ". Continuing."
}

func telegramUserID(user telegramUser) string {
	if strings.TrimSpace(user.Username) != "" {
		return user.Username
	}
	if user.ID != 0 {
		return fmt.Sprintf("%d", user.ID)
	}
	return ""
}

func telegramUserLogString(user telegramUser) string {
	username := strings.TrimSpace(user.Username)
	if username == "" {
		username = "-"
	}
	return fmt.Sprintf("user_id=%d username=%s", user.ID, username)
}

func telegramAccessAllowed(cfg appConfig, chatID int64, user telegramUser) bool {
	if telegramAllowListContains(cfg.TelegramAllowedChats, fmt.Sprintf("%d", chatID)) {
		return true
	}
	if user.ID != 0 && telegramAllowListContains(cfg.TelegramAllowedUsers, fmt.Sprintf("%d", user.ID)) {
		return true
	}
	if username := normalizeTelegramAllowListValue(user.Username); username != "" {
		return telegramAllowListContains(cfg.TelegramAllowedUsers, username)
	}
	return false
}

func telegramAllowListContains(values []string, value string) bool {
	value = normalizeTelegramAllowListValue(value)
	for _, allowed := range values {
		allowed = normalizeTelegramAllowListValue(allowed)
		if allowed == "*" || allowed == value {
			return true
		}
	}
	return false
}

func normalizeTelegramAllowList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, field := range splitTelegramAllowListValue(value) {
			if normalized := normalizeTelegramAllowListValue(field); normalized != "" {
				out = append(out, normalized)
			}
		}
	}
	return uniqueStrings(out)
}

func splitTelegramAllowListValue(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

func normalizeTelegramAllowListValue(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
}

func telegramApprovalCallback(data string) (string, bool, bool) {
	const prefix = "assistant:approval:"
	data = strings.TrimSpace(data)
	if !strings.HasPrefix(data, prefix) {
		return "", false, false
	}
	rest := strings.TrimPrefix(data, prefix)
	id, action, ok := strings.Cut(rest, ":")
	if !ok || strings.TrimSpace(id) == "" {
		return "", false, false
	}
	switch strings.TrimSpace(action) {
	case "approve":
		return id, true, true
	case "deny":
		return id, false, true
	default:
		return "", false, false
	}
}

func telegramCallbackCommand(data string) string {
	const prefix = "assistant:"
	data = strings.TrimSpace(data)
	if !strings.HasPrefix(data, prefix) {
		return ""
	}
	switch command := strings.TrimSpace(strings.TrimPrefix(data, prefix)); command {
	case "/clear", "/plan", "/chat", "/help", "/version":
		return command
	default:
		return ""
	}
}

func telegramCallbackNotice(command, reply string) string {
	if command == "/help" {
		return "Help sent"
	}
	if command == "/version" {
		return "Version sent"
	}
	reply = firstLine(strings.TrimSpace(reply))
	if reply == "" {
		return "Done"
	}
	runes := []rune(reply)
	if len(runes) > 180 {
		reply = string(runes[:180])
	}
	return reply
}

func answerTelegramCallbackQuery(ctx context.Context, token, callbackID, text string) error {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(callbackID) == "" {
		return nil
	}
	payload := map[string]any{"callback_query_id": callbackID}
	if strings.TrimSpace(text) != "" {
		payload["text"] = text
	}
	return postJSON(ctx, telegramAPIBaseURL+token+"/answerCallbackQuery", "", payload)
}

func editTelegramMessageReplyMarkup(ctx context.Context, token string, chatID, messageID int64) error {
	if strings.TrimSpace(token) == "" || chatID == 0 || messageID == 0 {
		return nil
	}
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reply_markup": map[string]any{
			"inline_keyboard": []any{},
		},
	}
	return postJSON(ctx, telegramAPIBaseURL+token+"/editMessageReplyMarkup", "", payload)
}

func sendTelegramChatAction(ctx context.Context, token string, chatID int64, action string) error {
	if strings.TrimSpace(token) == "" || chatID == 0 || strings.TrimSpace(action) == "" {
		return nil
	}
	return postJSON(ctx, telegramAPIBaseURL+token+"/sendChatAction", "", map[string]any{
		"chat_id": chatID,
		"action":  action,
	})
}

func telegramTypingLoop(ctx context.Context, token string, chatID int64, done <-chan struct{}) {
	_ = sendTelegramChatAction(ctx, token, chatID, "typing")
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			_ = sendTelegramChatAction(ctx, token, chatID, "typing")
		}
	}
}

func postTelegramApprovalMessage(ctx context.Context, token string, chatID int64, approval conversationApprovalSnapshot, title string) error {
	if strings.TrimSpace(token) == "" || chatID == 0 || strings.TrimSpace(approval.ID) == "" {
		return nil
	}
	payload := map[string]any{
		"chat_id":      chatID,
		"reply_markup": telegramApprovalKeyboard(approval.ID),
	}
	return postTelegramPayload(ctx, telegramAPIBaseURL+token+"/sendMessage", payload, telegramApprovalMessage(approval, title))
}

func telegramApprovalKeyboard(id string) map[string]any {
	return map[string]any{
		"inline_keyboard": [][]map[string]string{{
			{
				"text":          "Approve",
				"callback_data": "assistant:approval:" + id + ":approve",
			},
			{
				"text":          "Deny",
				"callback_data": "assistant:approval:" + id + ":deny",
			},
		}},
	}
}

func telegramApprovalMessage(approval conversationApprovalSnapshot, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Approval required"
	}
	tool := strings.TrimSpace(approval.ToolName)
	if tool == "" {
		tool = "tool"
	}
	input := telegramApprovalInput(approval.Input)
	lines := []string{
		"<b>" + stdhtml.EscapeString(title) + "</b>",
		"Tool: <code>" + stdhtml.EscapeString(tool) + "</code>",
	}
	if !approval.CreatedAt.IsZero() {
		lines = append(lines, "Requested: <code>"+stdhtml.EscapeString(approval.CreatedAt.Format(time.RFC3339))+"</code>")
	}
	lines = append(lines,
		"",
		"<pre>"+stdhtml.EscapeString(input)+"</pre>",
		"",
		"Tap Approve or Deny, or reply with yes/no.",
	)
	return strings.Join(lines, "\n")
}

func telegramApprovalInput(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "{}"
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		if pretty, err := json.MarshalIndent(decoded, "", "  "); err == nil {
			text = string(pretty)
		}
	}
	return telegramTruncateRunes(text, 2600)
}

func telegramTruncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return strings.TrimSpace(string(runes[:max])) + "\n... truncated"
}

func postTelegramMessage(ctx context.Context, token string, chatID int64, text string) error {
	if strings.TrimSpace(token) == "" || chatID == 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	url := telegramAPIBaseURL + token + "/sendMessage"
	for _, chunk := range telegramMessageChunks(text) {
		if err := postTelegramMessageChunk(ctx, url, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func postTelegramMessageChunk(ctx context.Context, endpoint string, chatID int64, text string) error {
	return postTelegramPayload(ctx, endpoint, map[string]any{"chat_id": chatID}, text)
}

func postTelegramPayload(ctx context.Context, endpoint string, payload map[string]any, text string) error {
	richPayload := copyTelegramPayload(payload)
	richPayload["text"] = telegramHTMLMessage(text)
	richPayload["parse_mode"] = "HTML"
	if err := postJSON(ctx, endpoint, "", richPayload); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "400 Bad Request") {
		return err
	} else {
		plain := strings.TrimSpace(telegramPlainText(text))
		if plain == "" {
			return err
		}
		fallbackPayload := copyTelegramPayload(payload)
		fallbackPayload["text"] = plain
		if fallbackErr := postJSON(ctx, endpoint, "", fallbackPayload); fallbackErr != nil {
			return fmt.Errorf("telegram rich message failed: %w; plain fallback failed: %v", err, fallbackErr)
		}
	}
	return nil
}

func copyTelegramPayload(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		out[key] = value
	}
	return out
}

func telegramReplyFormattingInstructions() string {
	return strings.Join([]string{
		"Reply using Telegram Bot API HTML formatting when it improves readability.",
		"Supported tags include <b>/<strong>, <i>/<em>, <u>/<ins>, <s>/<strike>/<del>, <tg-spoiler>, <span class=\"tg-spoiler\">, <a href=\"...\">, <tg-emoji emoji-id=\"...\">, <tg-time unix=\"...\" format=\"...\">, <code>, <pre>, <pre><code class=\"language-...\">, <blockquote>, and <blockquote expandable>.",
		"Telegram does not support native table tags in messages; for tables, use aligned columns inside <pre>...</pre>.",
		"Escape literal <, >, and & characters that are not part of Telegram HTML tags.",
	}, "\n")
}

func telegramMessageChunks(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var chunks []string
	for len([]rune(text)) > telegramMessageChunkRunes {
		runes := []rune(text)
		split := telegramChunkSplitIndex(runes, telegramMessageChunkRunes)
		chunks = append(chunks, strings.TrimSpace(string(runes[:split])))
		text = strings.TrimSpace(string(runes[split:]))
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}

func telegramChunkSplitIndex(runes []rune, max int) int {
	if len(runes) <= max {
		return len(runes)
	}
	for i := max; i > max-600 && i > 0; i-- {
		if runes[i-1] == '\n' && i < len(runes) && runes[i] == '\n' {
			return i
		}
	}
	for i := max; i > max-600 && i > 0; i-- {
		if runes[i-1] == '\n' {
			return i
		}
	}
	for i := max; i > max-600 && i > 0; i-- {
		if runes[i-1] == ' ' || runes[i-1] == '\t' {
			return i
		}
	}
	return max
}

func telegramHTMLMessage(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return strings.TrimSpace(telegramSanitizeHTML(telegramNormalizeMarkdownBlocks(text)))
}

func telegramPlainText(text string) string {
	return strings.TrimSpace(telegramExtractText(telegramHTMLMessage(text)))
}

func telegramNormalizeMarkdownBlocks(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	for i := 0; i < len(lines); {
		if language, ok := telegramFenceStart(lines[i]); ok {
			var code []string
			i++
			for i < len(lines) {
				if _, end := telegramFenceStart(lines[i]); end {
					i++
					break
				}
				code = append(code, lines[i])
				i++
			}
			out = append(out, telegramPreBlock(strings.Join(code, "\n"), language))
			continue
		}
		if block, next, ok := telegramMarkdownTable(lines, i); ok {
			out = append(out, telegramPreBlock(block, ""))
			i = next
			continue
		}
		out = append(out, lines[i])
		i++
	}
	return strings.Join(out, "\n")
}

func telegramFenceStart(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "```") {
		return "", false
	}
	language := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
	return telegramSafeCodeLanguage(language), true
}

func telegramPreBlock(text, language string) string {
	escaped := stdhtml.EscapeString(text)
	if language == "" {
		return "<pre>" + escaped + "</pre>"
	}
	return `<pre><code class="language-` + language + `">` + escaped + "</code></pre>"
}

func telegramMarkdownTable(lines []string, start int) (string, int, bool) {
	if start+1 >= len(lines) || !telegramLooksLikeTableRow(lines[start]) || !telegramIsTableSeparator(lines[start+1]) {
		return "", start, false
	}
	var rows [][]string
	rows = append(rows, telegramParseTableRow(lines[start]))
	i := start + 2
	for i < len(lines) && telegramLooksLikeTableRow(lines[i]) {
		rows = append(rows, telegramParseTableRow(lines[i]))
		i++
	}
	if len(rows) < 2 {
		return "", start, false
	}
	widths := telegramTableWidths(rows)
	rendered := make([]string, 0, len(rows)+1)
	rendered = append(rendered, telegramRenderTableRow(rows[0], widths))
	rendered = append(rendered, telegramTableDivider(widths))
	for _, row := range rows[1:] {
		rendered = append(rendered, telegramRenderTableRow(row, widths))
	}
	return strings.Join(rendered, "\n"), i, true
}

func telegramLooksLikeTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.Count(trimmed, "|") >= 2
}

func telegramIsTableSeparator(line string) bool {
	if !telegramLooksLikeTableRow(line) {
		return false
	}
	for _, cell := range telegramParseTableRow(line) {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		for _, r := range cell {
			if r != '-' && r != ':' && r != ' ' {
				return false
			}
		}
	}
	return true
}

func telegramParseTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func telegramTableWidths(rows [][]string) []int {
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	widths := make([]int, maxCols)
	for _, row := range rows {
		for i, cell := range row {
			if n := len([]rune(cell)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	return widths
}

func telegramRenderTableRow(row []string, widths []int) string {
	cells := make([]string, len(widths))
	for i := range widths {
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		cells[i] = telegramPadRight(cell, widths[i])
	}
	return strings.TrimRight(strings.Join(cells, "  "), " ")
}

func telegramTableDivider(widths []int) string {
	cells := make([]string, len(widths))
	for i, width := range widths {
		if width < 3 {
			width = 3
		}
		cells[i] = strings.Repeat("-", width)
	}
	return strings.Join(cells, "  ")
}

func telegramPadRight(text string, width int) string {
	padding := width - len([]rune(text))
	if padding <= 0 {
		return text
	}
	return text + strings.Repeat(" ", padding)
}

func telegramSafeCodeLanguage(language string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(language) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '+' || r == '#' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func telegramSanitizeHTML(text string) string {
	nodes, err := nethtml.ParseFragment(strings.NewReader(text), &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return stdhtml.EscapeString(text)
	}
	var b strings.Builder
	for _, node := range nodes {
		telegramRenderHTMLNode(&b, node, "")
	}
	return b.String()
}

func telegramRenderHTMLNode(b *strings.Builder, node *nethtml.Node, parent string) {
	switch node.Type {
	case nethtml.TextNode:
		b.WriteString(stdhtml.EscapeString(node.Data))
	case nethtml.ElementNode:
		tag := strings.ToLower(node.Data)
		if !telegramAllowedHTMLTag(tag, node) {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				telegramRenderHTMLNode(b, child, parent)
			}
			return
		}
		telegramRenderHTMLStartTag(b, tag, node, parent)
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			telegramRenderHTMLNode(b, child, tag)
		}
		b.WriteString("</")
		b.WriteString(tag)
		b.WriteString(">")
	case nethtml.DocumentNode:
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			telegramRenderHTMLNode(b, child, parent)
		}
	}
}

func telegramAllowedHTMLTag(tag string, node *nethtml.Node) bool {
	switch tag {
	case "b", "strong", "i", "em", "u", "ins", "s", "strike", "del", "tg-spoiler", "pre", "code", "blockquote":
		return true
	case "span":
		return telegramAttr(node, "class") == "tg-spoiler"
	case "a":
		return telegramAllowedHref(telegramAttr(node, "href"))
	case "tg-emoji":
		return strings.TrimSpace(telegramAttr(node, "emoji-id")) != ""
	case "tg-time":
		return strings.TrimSpace(telegramAttr(node, "unix")) != ""
	default:
		return false
	}
}

func telegramRenderHTMLStartTag(b *strings.Builder, tag string, node *nethtml.Node, parent string) {
	b.WriteString("<")
	b.WriteString(tag)
	switch tag {
	case "a":
		b.WriteString(` href="`)
		b.WriteString(stdhtml.EscapeString(telegramAttr(node, "href")))
		b.WriteString(`"`)
	case "span":
		b.WriteString(` class="tg-spoiler"`)
	case "tg-emoji":
		b.WriteString(` emoji-id="`)
		b.WriteString(stdhtml.EscapeString(telegramAttr(node, "emoji-id")))
		b.WriteString(`"`)
	case "tg-time":
		b.WriteString(` unix="`)
		b.WriteString(stdhtml.EscapeString(telegramAttr(node, "unix")))
		b.WriteString(`"`)
		if format := strings.TrimSpace(telegramAttr(node, "format")); format != "" {
			b.WriteString(` format="`)
			b.WriteString(stdhtml.EscapeString(format))
			b.WriteString(`"`)
		}
	case "code":
		if parent == "pre" {
			class := strings.TrimSpace(telegramAttr(node, "class"))
			if strings.HasPrefix(class, "language-") && len(class) > len("language-") {
				b.WriteString(` class="`)
				b.WriteString(stdhtml.EscapeString(class))
				b.WriteString(`"`)
			}
		}
	case "blockquote":
		if telegramHasAttr(node, "expandable") {
			b.WriteString(" expandable")
		}
	}
	b.WriteString(">")
}

func telegramAttr(node *nethtml.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return strings.TrimSpace(attr.Val)
		}
	}
	return ""
}

func telegramHasAttr(node *nethtml.Node, key string) bool {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return true
		}
	}
	return false
}

func telegramAllowedHref(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "tg", "mailto":
		return true
	default:
		return false
	}
}

func telegramExtractText(text string) string {
	nodes, err := nethtml.ParseFragment(strings.NewReader(text), &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return stdhtml.UnescapeString(text)
	}
	var b strings.Builder
	for _, node := range nodes {
		telegramExtractTextNode(&b, node)
	}
	return stdhtml.UnescapeString(b.String())
}

func telegramExtractTextNode(b *strings.Builder, node *nethtml.Node) {
	switch node.Type {
	case nethtml.TextNode:
		b.WriteString(node.Data)
	case nethtml.ElementNode:
		switch node.Data {
		case "p", "div", "br", "pre", "blockquote":
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
				b.WriteString("\n")
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			telegramExtractTextNode(b, child)
		}
		switch node.Data {
		case "p", "div", "br", "pre", "blockquote":
			if !strings.HasSuffix(b.String(), "\n") {
				b.WriteString("\n")
			}
		}
	case nethtml.DocumentNode:
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			telegramExtractTextNode(b, child)
		}
	}
}

func loadTelegramOffset(cfg appConfig) (int64, error) {
	var state telegramOffsetState
	if _, err := readJSONFile(stateFilePath(cfg, "telegram_offset.json"), &state); err != nil {
		return 0, err
	}
	return state.Offset, nil
}

func saveTelegramOffset(cfg appConfig, offset int64) error {
	return writeJSONFile(stateFilePath(cfg, "telegram_offset.json"), telegramOffsetState{Offset: offset})
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
