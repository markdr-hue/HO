/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

//go:generate goversioninfo -64 -icon=ho.ico

package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/markdr-hue/HO/actions"
	"github.com/markdr-hue/HO/bot"
	"github.com/markdr-hue/HO/brain"
	"github.com/markdr-hue/HO/caddy"
	"github.com/markdr-hue/HO/chat"
	"github.com/markdr-hue/HO/config"
	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/llm/anthropic"
	"github.com/markdr-hue/HO/llm/openai"
	"github.com/markdr-hue/HO/security"
	"github.com/markdr-hue/HO/server"
	"github.com/markdr-hue/HO/tools"
	"github.com/markdr-hue/HO/webhooks"
)

var Version = "0.1.0"

func printBanner(cfg *config.Config) {

	useColor := os.Getenv("NO_COLOR") == ""

	cyan := ""
	green := ""
	dim := ""
	reset := ""
	if useColor {
		cyan = "\033[36m"
		green = "\033[32m"
		dim = "\033[2m"
		reset = "\033[0m"
	}

	adminURL := fmt.Sprintf("http://localhost:%d", cfg.AdminPort)
	publicURL := fmt.Sprintf("http://localhost:%d", cfg.PublicPort)

	fmt.Fprintf(os.Stdout, `
%s   ██╗  ██╗ ██████╗
   ██║  ██║██╔═══██╗
   ███████║██║   ██║
   ██╔══██║██║   ██║
   ██║  ██║╚██████╔╝
   ╚═╝  ╚═╝ ╚═════╝%s
   %sHumans. Out.%s  %sv%s%s

   ─────────────────────────────────
   %sAdmin:%s     %s
   %sProjects:%s  %s
   ───────────────────────────────── 
   %sSpecial thanks to:
   https://caddyserver.com
   https://letsencrypt.org

   Created by Mark Durlinger
   https://github.com/markdr-hue/HO  (MIT License)
   %s

`, cyan, reset, reset, reset, dim, Version, reset,
		green, reset, adminURL,
		green, reset, publicURL,
		dim, reset)
}

func main() {
	initLogger("info") // init early so config.Load() logs use the correct format
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	printBanner(cfg)
	initLogger(cfg.LogLevel)

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	encryptor, err := security.NewEncryptor(cfg.EncryptionKey)
	if err != nil {
		slog.Error("failed to init encryptor", "error", err)
		os.Exit(1)
	}

	jwtMgr := security.NewJWTManager(cfg.JWTSecret, 24*time.Hour)
	jwtMgr.SecureCookies = cfg.CaddyEnabled // Caddy implies TLS

	siteDBMgr := db.NewSiteDBManager(cfg.DataDir)
	defer siteDBMgr.CloseAll()

	bus := events.NewBus()
	webhooks.NewDispatcher(siteDBMgr, bus)                       // Self-registers on the event bus via side effects.
	actionRunner := actions.NewRunner(siteDBMgr, bus, encryptor) // Server-side actions (email, HTTP, data) on events.
	actions.NewAuditLogger(siteDBMgr, bus)                       // Audit logging for security/data events.

	llmRegistry := llm.NewRegistry()
	toolRegistry := tools.NewRegistry()
	tools.RegisterAll(toolRegistry)
	toolExecutor := tools.NewExecutor(toolRegistry)

	slog.Debug("tools registered", "count", len(toolRegistry.List()))

	llmHTTPClient := &http.Client{Timeout: cfg.LLMTimeout()}
	providerFactory := func(name, providerType, apiKey, baseURL string) llm.Provider {
		switch strings.ToLower(providerType) {
		case "anthropic":
			opts := []anthropic.Option{anthropic.WithHTTPClient(llmHTTPClient)}
			if baseURL != "" {
				opts = append(opts, anthropic.WithBaseURL(baseURL))
			}
			return anthropic.New(name, apiKey, opts...)
		case "openai":
			opts := []openai.Option{openai.WithHTTPClient(llmHTTPClient)}
			if baseURL != "" {
				opts = append(opts, openai.WithBaseURL(baseURL))
			}
			return openai.New(name, apiKey, opts...)
		default:
			slog.Warn("unknown provider type, skipping", "type", providerType, "name", name)
			return nil
		}
	}

	if err := llm.LoadSeedWithFactory(cfg.SeedPath, database.DB, encryptor, llmRegistry, providerFactory); err != nil {
		slog.Debug("seed skipped", "reason", err)
	}

	brainDeps := &brain.Deps{
		DB:              database,
		SiteDBManager:   siteDBMgr,
		Encryptor:       encryptor,
		LLMRegistry:     llmRegistry,
		ToolRegistry:    toolRegistry,
		ToolExecutor:    toolExecutor,
		Bus:             bus,
		ProviderFactory: providerFactory,
		MonitoringBase:  time.Duration(cfg.BrainMonitoringBaseSec) * time.Second,
		MonitoringMax:   time.Duration(cfg.BrainMonitoringMaxSec) * time.Second,
		PublicPort:      cfg.PublicPort,
	}
	brainCtx, brainCancel := context.WithCancel(context.Background())
	defer brainCancel()
	brainMgr := brain.NewBrainManager(brainDeps, brainCtx)

	// Auto-start brain when a new project is created (via any path)
	bus.Subscribe(events.EventSiteCreated, func(e events.Event) {
		if err := brainMgr.StartSite(e.SiteID); err != nil {
			slog.Error("failed to auto-start brain on project creation", "site_id", e.SiteID, "error", err)
		}
	})

	// Wake the brain when a question is answered so it can resume building.
	// Pass the answer context so the brain can acknowledge it.
	bus.Subscribe(events.EventQuestionAnswered, func(e events.Event) {
		cmd := brain.BrainCommand{
			Type: brain.CommandWake,
			Payload: map[string]interface{}{
				"reason":      "question_answered",
				"question_id": e.Payload["question_id"],
				"answer":      e.Payload["answer"],
			},
		}
		if err := brainMgr.SendCommand(e.SiteID, cmd); err != nil {
			slog.Error("failed to wake brain after question answered, retrying", "site_id", e.SiteID, "error", err)
			time.AfterFunc(3*time.Second, func() {
				if err2 := brainMgr.SendCommand(e.SiteID, cmd); err2 != nil {
					slog.Error("retry wake after question answered also failed", "site_id", e.SiteID, "error", err2)
				}
			})
		}
	})

	// Wake the brain when the user sends a chat message so it can
	// validate any changes the chat may have made, or fix things during monitoring.
	bus.Subscribe(events.EventChatMessage, func(e events.Event) {
		role, _ := e.Payload["role"].(string)
		if role != "user" {
			return
		}
		content, _ := e.Payload["content"].(string)
		if err := brainMgr.SendCommand(e.SiteID, brain.BrainCommand{
			Type:    brain.CommandChat,
			Payload: map[string]interface{}{"message": content},
		}); err != nil {
			slog.Debug("failed to wake brain on chat message", "site_id", e.SiteID, "error", err)
		}
	})

	chatDeps := chat.SessionDeps{
		DB:            database.DB,
		SiteDBManager: siteDBMgr,
		LLMRegistry:   llmRegistry,
		ToolRegistry:  toolRegistry,
		ToolExecutor:  toolExecutor,
		Bus:           bus,
		Logger:        slog.Default().With("component", "chat"),
		Encryptor:     encryptor,
	}
	chatHandler := chat.NewChatHandler(chatDeps)

	userCount, err := models.CountUsers(database.DB)
	if err != nil {
		slog.Error("failed to count users", "error", err)
		os.Exit(1)
	}

	if userCount == 0 {
		slog.Debug("first run detected, setup required via admin UI")
	} else {
		slog.Debug("existing installation detected", "users", userCount)
	}

	if err := brainMgr.AutoStart(); err != nil {
		slog.Error("brain auto-start failed", "error", err)
	}

	brainMgr.StartScheduler()
	brainMgr.StartLogCleanup()

	// Bot service (Telegram, Discord, etc.) — provider-agnostic messaging.
	botService := bot.NewBotService(&bot.BotDeps{
		DB:            database,
		SiteDBManager: siteDBMgr,
		BrainManager:  brainMgr,
		Bus:           bus,
		Encryptor:     encryptor,
		Logger:        slog.Default(),
	})
	if token, _ := database.GetSetting("telegram_bot_token"); token != "" {
		tg := bot.NewTelegramProvider(token, botService.HandleIncoming, botService.HandleCallback)
		botService.RegisterProvider(tg)
	}
	go botService.Start(brainCtx)

	// Hot-reload bot providers when settings change (no restart needed).
	bus.Subscribe(events.EventSettingsChanged, func(e events.Event) {
		botService.ReloadTelegram(brainCtx)
	})

	caddyMgr := caddy.NewManager(cfg, database.DB, slog.Default())
	caddyMgr.SubscribeToEvents(bus)
	if err := caddyMgr.Start(); err != nil {
		slog.Error("caddy start failed", "error", err)
	}

	adminSubFS, err := fs.Sub(adminFS, "web/admin")
	if err != nil {
		slog.Error("failed to create admin sub-filesystem", "error", err)
		os.Exit(1)
	}

	srv := server.New(&server.ServerDeps{
		Config:          cfg,
		DB:              database,
		SiteDBManager:   siteDBMgr,
		JWTManager:      jwtMgr,
		Encryptor:       encryptor,
		Bus:             bus,
		BrainManager:    brainMgr,
		ChatHandler:     chatHandler,
		LLMRegistry:     llmRegistry,
		ToolRegistry:    toolRegistry,
		ProviderFactory: providerFactory,
		Logger:          slog.Default(),
		AdminFS:         adminSubFS,
		Version:         Version,
	})

	// Wire the WebSocket broadcaster into the Actions Runner so ws_broadcast actions work.
	actionRunner.SetBroadcaster(srv.WSBroadcaster())

	slog.Debug("HO ready",
		"admin", cfg.AdminPort,
		"public", cfg.PublicPort,
		"caddy", cfg.CaddyEnabled,
	)

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		slog.Error("server error", "error", err)
	}

	// Graceful shutdown
	caddyMgr.Stop()
	brainMgr.StopAll()
	slog.Info("HO shutdown complete")
}

func initLogger(level string) {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})
	slog.SetDefault(slog.New(handler))
}
