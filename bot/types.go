/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package bot

import (
	"context"
	"log/slog"

	"github.com/markdr-hue/HO/brain"
	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/security"
)

// Provider abstracts a messaging platform (Telegram, Discord, Slack, etc.).
// Each provider handles transport-specific concerns (polling, webhooks, message
// formatting) and delegates business logic to the BotService via OnMessage.
type Provider interface {
	Name() string
	Start(ctx context.Context) error
	Stop() error
	SendMessage(chatID string, text string) error
	SendQuestion(chatID string, question string, options []string) error
}

// OnMessageFunc is the callback providers call when a message arrives.
type OnMessageFunc func(provider, chatID, userID, text string)

// OnCallbackFunc is the callback providers call when an inline button is pressed.
type OnCallbackFunc func(provider, chatID, userID, callbackData string)

// BotDeps holds dependencies injected from main.go.
type BotDeps struct {
	DB            *db.DB
	SiteDBManager *db.SiteDBManager
	BrainManager  *brain.BrainManager
	Bus           *events.Bus
	Encryptor     *security.Encryptor
	Logger        *slog.Logger
}

// UserState tracks which site a bot user is currently interacting with.
type UserState struct {
	Provider      string            // "telegram", "discord", etc.
	ChatID        string            // provider-specific conversation ID
	UserID        string            // platform-specific user ID (for allowlist checks)
	ActiveSiteID  int               // currently selected site ID (0 = none)
	Muted         bool              // true = suppress event notifications
	PendingAction string            // "awaiting_site_name", "awaiting_description", etc.
	PendingData   map[string]string // temporary data for multi-step flows
}
