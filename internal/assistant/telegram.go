// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const telegramAPIBase = "https://api.telegram.org/bot"

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
	return postJSON(ctx, url, "", map[string]any{"chat_id": chatID, "text": text})
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
