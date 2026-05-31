// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const telegramAPIBase = "https://api.telegram.org/bot"
const telegramMessageChunkRunes = 3800

type telegramOffsetState struct {
	Offset int64 `json:"offset"`
}

type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"from"`
	} `json:"message"`
}

type telegramUpdatesResponse struct {
	OK          bool             `json:"ok"`
	Description string           `json:"description"`
	Result      []telegramUpdate `json:"result"`
}

func runTelegramPoller(ctx context.Context, cfg appConfig, stdout, stderr io.Writer) error {
	_ = stdout
	token := strings.TrimSpace(cfg.TelegramBotToken)
	if token == "" {
		return errors.New("telegram polling requires --telegram-bot-token or ASSISTANT_TELEGRAM_BOT_TOKEN")
	}
	offset, err := loadTelegramOffset(cfg)
	if err != nil {
		return err
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
			if err := handleTelegramUpdate(ctx, cfg, token, update); err != nil {
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
		"allowed_updates": []string{"message"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := telegramAPIBase + token + "/getUpdates"
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

func handleTelegramUpdate(ctx context.Context, cfg appConfig, token string, update telegramUpdate) error {
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return nil
	}
	chatID := update.Message.Chat.ID
	if chatID == 0 {
		return nil
	}
	userID := fmt.Sprintf("%d", update.Message.From.ID)
	if strings.TrimSpace(update.Message.From.Username) != "" {
		userID = update.Message.From.Username
	}
	reply, err := replyToInbound(ctx, cfg, inboundMessage{
		Channel: "telegram",
		UserID:  userID,
		Thread:  fmt.Sprintf("%d", chatID),
		Text:    text,
	})
	if err != nil {
		return err
	}
	return postTelegramMessage(ctx, token, chatID, reply)
}

func postTelegramMessage(ctx context.Context, token string, chatID int64, text string) error {
	if strings.TrimSpace(token) == "" || chatID == 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	url := telegramAPIBase + token + "/sendMessage"
	for _, chunk := range telegramMessageChunks(text) {
		if err := postTelegramMessageChunk(ctx, url, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func postTelegramMessageChunk(ctx context.Context, endpoint string, chatID int64, text string) error {
	richPayload := map[string]any{
		"chat_id":    chatID,
		"text":       telegramHTMLMessage(text),
		"parse_mode": "HTML",
	}
	if err := postJSON(ctx, endpoint, "", richPayload); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "400 Bad Request") {
		return err
	} else {
		plain := strings.TrimSpace(telegramPlainText(text))
		if plain == "" {
			return err
		}
		if fallbackErr := postJSON(ctx, endpoint, "", map[string]any{"chat_id": chatID, "text": plain}); fallbackErr != nil {
			return fmt.Errorf("telegram rich message failed: %w; plain fallback failed: %v", err, fallbackErr)
		}
	}
	return nil
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
