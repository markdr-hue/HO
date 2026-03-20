/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// TelegramProvider implements the Provider interface for Telegram Bot API.
// Uses long-polling (no webhook server needed).
type TelegramProvider struct {
	token      string
	baseURL    string
	client     *http.Client
	onMessage  OnMessageFunc
	onCallback OnCallbackFunc
	cancel     context.CancelFunc
	logger     *slog.Logger
}

// NewTelegramProvider creates a Telegram bot provider.
func NewTelegramProvider(token string, onMessage OnMessageFunc, onCallback OnCallbackFunc) *TelegramProvider {
	return &TelegramProvider{
		token:      token,
		baseURL:    "https://api.telegram.org/bot" + token,
		client:     &http.Client{Timeout: 35 * time.Second},
		onMessage:  onMessage,
		onCallback: onCallback,
		logger:     slog.Default().With("component", "telegram"),
	}
}

func (t *TelegramProvider) Name() string { return "telegram" }

// Start begins long-polling for updates.
func (t *TelegramProvider) Start(ctx context.Context) error {
	ctx, t.cancel = context.WithCancel(ctx)

	// Register commands with Telegram.
	t.setCommands()

	t.logger.Info("telegram bot started, polling for updates")

	offset := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := t.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			t.logger.Warn("telegram getUpdates failed", "error", err)
			time.Sleep(3 * time.Second)
			continue
		}

		for _, update := range updates {
			offset = update.UpdateID + 1

			if update.Message != nil && update.Message.Text != "" {
				chatID := fmt.Sprintf("%d", update.Message.Chat.ID)
				userID := ""
				if update.Message.From != nil {
					userID = fmt.Sprintf("%d", update.Message.From.ID)
				}
				t.onMessage("telegram", chatID, userID, update.Message.Text)
			}

			if update.CallbackQuery != nil {
				chatID := fmt.Sprintf("%d", update.CallbackQuery.Message.Chat.ID)
				userID := ""
				if update.CallbackQuery.From != nil {
					userID = fmt.Sprintf("%d", update.CallbackQuery.From.ID)
				}
				t.onCallback("telegram", chatID, userID, update.CallbackQuery.Data)
				// Acknowledge the callback to remove the loading indicator.
				t.answerCallback(update.CallbackQuery.ID)
			}
		}
	}
}

// Stop cancels the polling loop.
func (t *TelegramProvider) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

// SendMessage sends a text message to a Telegram chat.
func (t *TelegramProvider) SendMessage(chatID string, text string) error {
	// Truncate long messages (Telegram limit is 4096 chars).
	if len(text) > 4000 {
		text = text[:4000] + "\n..."
	}

	body := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	_, err := t.apiCall("sendMessage", body)
	return err
}

// SendQuestion sends a message with inline keyboard buttons.
func (t *TelegramProvider) SendQuestion(chatID string, question string, options []string) error {
	// Build inline keyboard rows (one button per row).
	var rows [][]map[string]string
	for _, opt := range options {
		// Options use format "prefix:id:label" (e.g. "site:1:My App — monitoring").
		// callback_data = "prefix:id", display label = the rest after second colon.
		label := opt
		callbackData := opt
		if strings.HasPrefix(opt, "answer:") || strings.HasPrefix(opt, "site:") {
			parts := strings.SplitN(opt, ":", 3)
			if len(parts) == 3 {
				label = parts[2]                         // "My App — monitoring"
				callbackData = parts[0] + ":" + parts[1] // "site:1"
			}
		}
		// Telegram callback_data max is 64 bytes.
		if len(callbackData) > 64 {
			callbackData = callbackData[:64]
		}
		rows = append(rows, []map[string]string{
			{"text": label, "callback_data": callbackData},
		})
	}

	body := map[string]interface{}{
		"chat_id":    chatID,
		"text":       question,
		"parse_mode": "HTML",
		"reply_markup": map[string]interface{}{
			"inline_keyboard": rows,
		},
	}
	_, err := t.apiCall("sendMessage", body)
	return err
}

// --- Telegram API internals ---

type tgUpdate struct {
	UpdateID      int              `json:"update_id"`
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgUser struct {
	ID int64 `json:"id"`
}

type tgMessage struct {
	MessageID int     `json:"message_id"`
	Text      string  `json:"text"`
	Chat      tgChat  `json:"chat"`
	From      *tgUser `json:"from"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgCallbackQuery struct {
	ID      string     `json:"id"`
	Data    string     `json:"data"`
	Message *tgMessage `json:"message"`
	From    *tgUser    `json:"from"`
}

func (t *TelegramProvider) getUpdates(ctx context.Context, offset int) ([]tgUpdate, error) {
	body := map[string]interface{}{
		"offset":          offset,
		"timeout":         30,
		"allowed_updates": []string{"message", "callback_query"},
	}

	data, err := t.apiCallCtx(ctx, "getUpdates", body)
	if err != nil {
		return nil, err
	}

	var resp struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("telegram: invalid response: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram: API returned ok=false")
	}
	return resp.Result, nil
}

func (t *TelegramProvider) answerCallback(callbackID string) {
	t.apiCall("answerCallbackQuery", map[string]interface{}{
		"callback_query_id": callbackID,
	})
}

func (t *TelegramProvider) setCommands() {
	t.apiCall("setMyCommands", map[string]interface{}{
		"commands": []map[string]string{
			{"command": "start", "description": "Welcome & help"},
			{"command": "projects", "description": "List & switch projects"},
			{"command": "new", "description": "Create a new project"},
			{"command": "status", "description": "Detailed project status"},
			{"command": "logs", "description": "Recent brain activity"},
			{"command": "cost", "description": "Token usage summary"},
			{"command": "pause", "description": "Pause the brain"},
			{"command": "resume", "description": "Resume the brain"},
			{"command": "mode", "description": "Switch mode (building/monitoring/paused)"},
			{"command": "wake", "description": "Trigger brain processing"},
			{"command": "update", "description": "Trigger incremental update"},
			{"command": "stop", "description": "Stop the active brain"},
			{"command": "mute", "description": "Mute event notifications"},
			{"command": "unmute", "description": "Unmute event notifications"},
			{"command": "help", "description": "Show commands"},
		},
	})
}

func (t *TelegramProvider) apiCall(method string, body map[string]interface{}) ([]byte, error) {
	return t.apiCallCtx(context.Background(), method, body)
}

func (t *TelegramProvider) apiCallCtx(ctx context.Context, method string, body map[string]interface{}) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", t.baseURL+"/"+method, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return data, fmt.Errorf("telegram API %s returned %d: %s", method, resp.StatusCode, string(data[:min(200, len(data))]))
	}

	return data, nil
}
