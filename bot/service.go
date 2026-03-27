/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/markdr-hue/HO/brain"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
)

// BotService orchestrates messaging providers and routes messages between
// bot users and the brain. It is provider-agnostic — Telegram, Discord,
// Slack etc. are pluggable via the Provider interface.
type BotService struct {
	deps      *BotDeps
	providers map[string]Provider
	users     map[string]*UserState // key: "provider:chatID"
	mu        sync.RWMutex
	logger    *slog.Logger
}

// NewBotService creates a new bot service.
func NewBotService(deps *BotDeps) *BotService {
	return &BotService{
		deps:      deps,
		providers: make(map[string]Provider),
		users:     LoadSessions(deps.DB),
		logger:    deps.Logger.With("component", "bot"),
	}
}

// RegisterProvider adds a messaging provider.
func (s *BotService) RegisterProvider(p Provider) {
	s.mu.Lock()
	s.providers[p.Name()] = p
	s.mu.Unlock()
	s.logger.Info("registered bot provider", "provider", p.Name())
}

// ReloadTelegram checks the telegram_bot_token setting and starts/stops
// the Telegram provider as needed. Safe to call at any time.
func (s *BotService) ReloadTelegram(ctx context.Context) {
	token, _ := s.deps.DB.GetSetting("telegram_bot_token")

	s.mu.RLock()
	existing, hasExisting := s.providers["telegram"]
	s.mu.RUnlock()

	if token == "" && hasExisting {
		// Token removed — stop provider.
		existing.Stop()
		s.mu.Lock()
		delete(s.providers, "telegram")
		s.mu.Unlock()
		s.logger.Info("telegram bot stopped (token removed)")
		return
	}

	if token != "" && !hasExisting {
		// Token added — start provider.
		tg := NewTelegramProvider(token, s.HandleIncoming, s.HandleCallback)
		s.RegisterProvider(tg)
		go tg.Start(ctx)
		s.logger.Info("telegram bot started (token added)")
	}
}

// Start launches all registered providers and subscribes to bus events.
func (s *BotService) Start(ctx context.Context) {
	if len(s.providers) == 0 {
		s.logger.Info("no bot providers configured, skipping")
		return
	}

	// Subscribe to brain events for outbound messages.
	s.deps.Bus.Subscribe(events.EventBrainMessage, s.onBrainMessage)
	s.deps.Bus.Subscribe(events.EventQuestionAsked, s.onQuestionAsked)
	s.deps.Bus.Subscribe(events.EventBrainStageChange, s.onStageChange)
	s.deps.Bus.Subscribe(events.EventBrainModeChanged, s.onModeChanged)
	s.deps.Bus.Subscribe(events.EventBrainError, s.onBrainError)
	s.deps.Bus.Subscribe(events.EventBrainStarted, s.onBrainStarted)
	s.deps.Bus.Subscribe(events.EventBrainStopped, s.onBrainStopped)
	s.deps.Bus.Subscribe(events.EventBrainProgress, s.onBrainProgress)
	s.deps.Bus.Subscribe(events.EventToolFailed, s.onToolFailed)
	s.deps.Bus.Subscribe(events.EventWebhookReceived, s.onWebhookReceived)
	s.deps.Bus.Subscribe(events.EventPaymentCompleted, s.onPaymentCompleted)
	s.deps.Bus.Subscribe(events.EventPaymentFailed, s.onPaymentFailed)

	for name, p := range s.providers {
		s.logger.Info("starting bot provider", "provider", name)
		go func(p Provider) {
			if err := p.Start(ctx); err != nil {
				s.logger.Error("bot provider stopped", "provider", p.Name(), "error", err)
			}
		}(p)
	}
}

// Stop shuts down all providers.
func (s *BotService) Stop() {
	for _, p := range s.providers {
		p.Stop()
	}
}

// isAllowed checks the telegram_allowed_users setting. If no users are
// configured, access is denied (must configure allowed users).
func (s *BotService) isAllowed(userID string) bool {
	if userID == "" {
		return false
	}
	allowed, err := s.deps.DB.GetSetting("telegram_allowed_users")
	if err != nil || allowed == "" {
		return false
	}
	for _, id := range strings.Split(allowed, ",") {
		if strings.TrimSpace(id) == userID {
			return true
		}
	}
	return false
}

// HandleIncoming processes a message from any provider. Called by providers.
func (s *BotService) HandleIncoming(provider, chatID, userID, text string) {
	if !s.isAllowed(userID) {
		if p, ok := s.providers[provider]; ok {
			p.SendMessage(chatID, "Not authorized. Contact the admin to add your user ID: "+userID)
		}
		return
	}

	state := s.getOrCreateUser(provider, chatID)
	if userID != "" && state.UserID != userID {
		state.UserID = userID
		SaveSession(s.deps.DB, state)
	}

	text = strings.TrimSpace(text)

	// Handle multi-step flows (awaiting input).
	if state.PendingAction != "" {
		s.handlePending(state, text)
		return
	}

	// Command routing.
	if strings.HasPrefix(text, "/") {
		s.handleCommand(state, text)
		return
	}

	// Free text → send to brain as chat.
	s.handleChat(state, text)
}

// HandleCallback processes inline button presses (e.g., site selection, question answers).
func (s *BotService) HandleCallback(provider, chatID, userID, data string) {
	if !s.isAllowed(userID) {
		return
	}

	state := s.getOrCreateUser(provider, chatID)
	if userID != "" && state.UserID != userID {
		state.UserID = userID
		SaveSession(s.deps.DB, state)
	}

	// site:{id}:{label} → switch to site
	if strings.HasPrefix(data, "site:") {
		parts := strings.SplitN(data, ":", 3)
		if len(parts) >= 2 {
			var siteID int
			fmt.Sscanf(parts[1], "%d", &siteID)
			if siteID > 0 {
				s.switchSite(state, siteID)
			}
		}
		return
	}

	// answer:123:text → answer question 123
	if strings.HasPrefix(data, "answer:") {
		parts := strings.SplitN(data, ":", 3)
		if len(parts) == 3 {
			s.answerQuestion(state, parts[1], parts[2])
		}
		return
	}
}

// --- Command handlers ---

func (s *BotService) handleCommand(state *UserState, text string) {
	parts := strings.SplitN(text, " ", 2)
	cmd := strings.ToLower(parts[0])
	// Strip @botname suffix (e.g. /status@MyBot).
	if at := strings.Index(cmd, "@"); at > 0 {
		cmd = cmd[:at]
	}
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	switch cmd {
	case "/start", "/help":
		s.send(state, "<b>HO Bot</b>\n\n"+
			"<b>Projects</b>\n"+
			"/projects — list &amp; switch projects\n"+
			"/new — create a new project\n"+
			"/status — detailed project status\n"+
			"/logs — recent brain activity\n"+
			"/cost — token usage summary\n\n"+
			"<b>Brain Control</b>\n"+
			"/pause — pause the brain\n"+
			"/resume — resume the brain\n"+
			"/mode — switch mode (building/monitoring/paused)\n"+
			"/wake — trigger brain processing\n"+
			"/update — trigger incremental update\n"+
			"/stop — stop the active brain\n\n"+
			"<b>Notifications</b>\n"+
			"/mute — mute event notifications\n"+
			"/unmute — unmute event notifications\n\n"+
			"Or just type a message to chat with the brain.")

	case "/projects":
		s.cmdSites(state)

	case "/cancel":
		if state.PendingAction != "" {
			state.PendingAction = ""
			state.PendingData = nil
			s.send(state, "Cancelled.")
		} else {
			s.send(state, "Nothing to cancel.")
		}

	case "/new":
		s.cmdNew(state, arg)

	case "/status":
		s.cmdStatus(state)

	case "/logs":
		s.cmdLogs(state)

	case "/cost":
		s.cmdCost(state)

	case "/pause":
		s.cmdPause(state)

	case "/resume":
		s.cmdResume(state)

	case "/mode":
		s.cmdMode(state, arg)

	case "/wake":
		s.cmdWake(state)

	case "/update":
		s.cmdUpdate(state)

	case "/stop":
		s.cmdStop(state)

	case "/mute":
		state.Muted = true
		SaveSession(s.deps.DB, state)
		s.send(state, "Notifications muted. You'll still get direct messages and questions.")

	case "/unmute":
		state.Muted = false
		SaveSession(s.deps.DB, state)
		s.send(state, "Notifications unmuted.")

	default:
		s.send(state, "Unknown command. Try /help")
	}
}

func (s *BotService) cmdSites(state *UserState) {
	sites, err := models.ListSites(s.deps.DB.DB)
	if err != nil || len(sites) == 0 {
		s.send(state, "No projects yet. Use /new to create one.")
		return
	}

	p, ok := s.providers[state.Provider]
	if !ok {
		return
	}

	// Build site list with callback data prefixed for HandleCallback.
	options := make([]string, len(sites))
	for i, site := range sites {
		marker := ""
		if site.ID == state.ActiveSiteID {
			marker = " (active)"
		}
		// Format: "site:{id}:{label}" — HandleCallback parses the prefix, Telegram shows the label.
		options[i] = fmt.Sprintf("site:%d:%s — %s%s", site.ID, site.Name, site.Mode, marker)
	}

	p.SendQuestion(state.ChatID, "Select a project:", options)
}

func (s *BotService) cmdNew(state *UserState, description string) {
	if description != "" {
		// Direct creation: /new a simple calculator
		s.createSite(state, description)
		return
	}
	// Multi-step: ask for description.
	state.PendingAction = "awaiting_description"
	s.send(state, "What should I build? Describe the project:")
}

func (s *BotService) cmdStatus(state *UserState) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project. Use /projects to select one.")
		return
	}

	site, err := models.GetSiteByID(s.deps.DB.DB, state.ActiveSiteID)
	if err != nil {
		s.send(state, "Project not found.")
		return
	}

	brainState := s.deps.BrainManager.Status(state.ActiveSiteID)

	// Get pipeline stage from site DB.
	stageInfo := ""
	if siteDB := s.deps.SiteDBManager.Get(state.ActiveSiteID); siteDB != nil {
		var stage, pauseReason string
		var paused bool
		err := siteDB.QueryRow(
			"SELECT stage, paused, COALESCE(pause_reason,'') FROM ho_pipeline_state WHERE id = 1",
		).Scan(&stage, &paused, &pauseReason)
		if err == nil && stage != "" {
			stageInfo = fmt.Sprintf("\nStage: <code>%s</code>", stage)
			if paused && pauseReason != "" {
				stageInfo += fmt.Sprintf(" (paused: %s)", pauseReason)
			}
		}
	}

	domain := ""
	if site.Domain != nil && *site.Domain != "" {
		domain = fmt.Sprintf("\nDomain: %s", *site.Domain)
	}

	msg := fmt.Sprintf(
		"<b>%s</b>\nBrain: <code>%s</code> | Mode: <code>%s</code>%s%s",
		site.Name, brainState, site.Mode, stageInfo, domain,
	)
	s.send(state, msg)
}

func (s *BotService) cmdLogs(state *UserState) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project. Use /projects to select one.")
		return
	}

	siteDB := s.deps.SiteDBManager.Get(state.ActiveSiteID)
	if siteDB == nil {
		s.send(state, "Project database not available.")
		return
	}

	rows, err := siteDB.Query(
		`SELECT event_type, summary, created_at FROM ho_brain_log
		 ORDER BY created_at DESC LIMIT 5`,
	)
	if err != nil {
		s.send(state, "Failed to read logs.")
		return
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var eventType, summary, createdAt string
		if rows.Scan(&eventType, &summary, &createdAt) == nil {
			if len(summary) > 80 {
				summary = summary[:80] + "..."
			}
			lines = append(lines, fmt.Sprintf("<code>%s</code> [%s]\n%s", createdAt, eventType, summary))
		}
	}

	if len(lines) == 0 {
		s.send(state, "No log entries yet.")
		return
	}
	s.send(state, "<b>Recent Logs</b>\n\n"+strings.Join(lines, "\n\n"))
}

func (s *BotService) cmdCost(state *UserState) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project. Use /projects to select one.")
		return
	}

	siteDB := s.deps.SiteDBManager.Get(state.ActiveSiteID)
	if siteDB == nil {
		s.send(state, "Project database not available.")
		return
	}

	rows, err := siteDB.Query(
		`SELECT model, COUNT(*) as calls,
		        SUM(input_tokens) as total_in,
		        SUM(output_tokens) as total_out
		 FROM ho_llm_log GROUP BY model ORDER BY total_in DESC`,
	)
	if err != nil {
		s.send(state, "Failed to read token usage.")
		return
	}
	defer rows.Close()

	var lines []string
	var grandIn, grandOut int64
	for rows.Next() {
		var model string
		var calls, totalIn, totalOut int64
		if rows.Scan(&model, &calls, &totalIn, &totalOut) == nil {
			lines = append(lines, fmt.Sprintf(
				"<code>%s</code>: %d calls, %dk in / %dk out",
				model, calls, totalIn/1000, totalOut/1000,
			))
			grandIn += totalIn
			grandOut += totalOut
		}
	}

	if len(lines) == 0 {
		s.send(state, "No token usage recorded yet.")
		return
	}

	header := fmt.Sprintf("<b>Token Usage</b>\nTotal: %dk in / %dk out\n", grandIn/1000, grandOut/1000)
	s.send(state, header+"\n"+strings.Join(lines, "\n"))
}

func (s *BotService) cmdPause(state *UserState) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project.")
		return
	}
	cmd := brain.BrainCommand{
		Type:    brain.CommandModeChange,
		Payload: map[string]interface{}{"mode": "paused"},
	}
	if err := s.deps.BrainManager.SendCommand(state.ActiveSiteID, cmd); err != nil {
		s.send(state, "Failed: "+err.Error())
		return
	}
	s.send(state, "Brain paused.")
}

func (s *BotService) cmdResume(state *UserState) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project.")
		return
	}
	cmd := brain.BrainCommand{
		Type:    brain.CommandModeChange,
		Payload: map[string]interface{}{"mode": "monitoring"},
	}
	if err := s.deps.BrainManager.SendCommand(state.ActiveSiteID, cmd); err != nil {
		s.send(state, "Failed: "+err.Error())
		return
	}
	s.send(state, "Brain resumed.")
}

func (s *BotService) cmdMode(state *UserState, arg string) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project.")
		return
	}
	valid := map[string]bool{"building": true, "monitoring": true, "paused": true}
	if !valid[arg] {
		s.send(state, "Usage: /mode [building|monitoring|paused]")
		return
	}
	cmd := brain.BrainCommand{
		Type:    brain.CommandModeChange,
		Payload: map[string]interface{}{"mode": arg},
	}
	if err := s.deps.BrainManager.SendCommand(state.ActiveSiteID, cmd); err != nil {
		s.send(state, "Failed: "+err.Error())
		return
	}
	s.send(state, fmt.Sprintf("Mode set to: %s", arg))
}

func (s *BotService) cmdWake(state *UserState) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project.")
		return
	}
	cmd := brain.BrainCommand{Type: brain.CommandWake}
	if err := s.deps.BrainManager.SendCommand(state.ActiveSiteID, cmd); err != nil {
		s.send(state, "Failed: "+err.Error())
		return
	}
	s.send(state, "Wake signal sent.")
}

func (s *BotService) cmdUpdate(state *UserState) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project.")
		return
	}
	cmd := brain.BrainCommand{Type: brain.CommandUpdate}
	if err := s.deps.BrainManager.SendCommand(state.ActiveSiteID, cmd); err != nil {
		s.send(state, "Failed: "+err.Error())
		return
	}
	s.send(state, "Update triggered.")
}

func (s *BotService) cmdStop(state *UserState) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project. Use /projects to select one.")
		return
	}
	if err := s.deps.BrainManager.StopSite(state.ActiveSiteID); err != nil {
		s.send(state, "Failed to stop: "+err.Error())
		return
	}
	s.send(state, "Brain stopped.")
}

// --- Multi-step flow handlers ---

func (s *BotService) handlePending(state *UserState, text string) {
	switch state.PendingAction {
	case "awaiting_description":
		state.PendingAction = ""
		s.createSite(state, text)
	default:
		state.PendingAction = ""
		s.send(state, "Cancelled. Try /help for commands.")
	}
}

// --- Chat (free text → brain) ---

func (s *BotService) handleChat(state *UserState, text string) {
	if state.ActiveSiteID == 0 {
		s.send(state, "No active project. Use /projects to select one, or /new to create one.")
		return
	}

	if !s.deps.BrainManager.IsRunning(state.ActiveSiteID) {
		// Try to start the brain first.
		if err := s.deps.BrainManager.StartSite(state.ActiveSiteID); err != nil {
			s.send(state, "Brain is not running and couldn't be started: "+err.Error())
			return
		}
	}

	cmd := brain.BrainCommand{
		Type: brain.CommandChat,
		Payload: map[string]interface{}{
			"message": text,
		},
	}
	if err := s.deps.BrainManager.SendCommand(state.ActiveSiteID, cmd); err != nil {
		s.send(state, "Failed to send message: "+err.Error())
		return
	}
	// Response comes back via EventBrainMessage → onBrainMessage.
}

// --- Site operations ---

func (s *BotService) createSite(state *UserState, description string) {
	// Use default model.
	defModel, _, err := models.GetDefaultModel(s.deps.DB.DB)
	if err != nil {
		s.send(state, "No default model configured. Please set one in the admin panel first.")
		return
	}

	// Generate a short name from the description.
	name := description
	if len(name) > 40 {
		name = name[:40]
	}

	desc := description
	site, err := models.CreateSite(s.deps.DB.DB, name, nil, &desc, defModel.ID)
	if err != nil {
		s.send(state, "Failed to create project: "+err.Error())
		return
	}

	// Create per-site database.
	if _, err := s.deps.SiteDBManager.Create(site.ID); err != nil {
		_ = models.DeleteSite(s.deps.DB.DB, site.ID)
		s.send(state, "Failed to create project database: "+err.Error())
		return
	}

	// Publish event (triggers brain auto-start via existing main.go subscriber).
	s.deps.Bus.Publish(events.NewEvent(events.EventSiteCreated, site.ID, map[string]interface{}{
		"name": site.Name,
	}))

	// Switch user to the new site.
	s.switchSite(state, site.ID)
	s.send(state, fmt.Sprintf("Project created: %s\nBrain is starting...", site.Name))
}

func (s *BotService) switchSite(state *UserState, siteID int) {
	state.ActiveSiteID = siteID
	SaveSession(s.deps.DB, state)

	site, err := models.GetSiteByID(s.deps.DB.DB, siteID)
	if err != nil {
		s.send(state, fmt.Sprintf("Switched to project %d", siteID))
		return
	}
	s.send(state, fmt.Sprintf("Switched to: %s (%s)", site.Name, site.Mode))
}

// --- Question answering ---

func (s *BotService) answerQuestion(state *UserState, questionIDStr, answer string) {
	if state.ActiveSiteID == 0 {
		return
	}

	siteDB := s.deps.SiteDBManager.Get(state.ActiveSiteID)
	if siteDB == nil {
		return
	}

	// Store answer.
	siteDB.ExecWrite(
		"INSERT INTO ho_answers (question_id, answer) VALUES (?, ?)",
		questionIDStr, answer,
	)

	// Mark question as answered.
	siteDB.ExecWrite("UPDATE ho_questions SET status = 'answered' WHERE id = ?", questionIDStr)

	// Check if all pending questions are answered.
	var pending int
	siteDB.QueryRow("SELECT COUNT(*) FROM ho_questions WHERE status = 'pending'").Scan(&pending)

	if pending == 0 {
		// Collect all answers.
		rows, _ := siteDB.Query(`
			SELECT q.question, a.answer FROM ho_questions q
			JOIN ho_answers a ON a.question_id = q.id
			WHERE q.status = 'answered'
			ORDER BY q.id DESC LIMIT 10
		`)
		var combined []string
		if rows != nil {
			for rows.Next() {
				var q, a string
				if rows.Scan(&q, &a) == nil {
					combined = append(combined, fmt.Sprintf("Q: %s\nA: %s", q, a))
				}
			}
			rows.Close()
		}

		s.deps.Bus.Publish(events.NewEvent(events.EventQuestionAnswered, state.ActiveSiteID, map[string]interface{}{
			"answer": strings.Join(combined, "\n\n"),
		}))
		s.send(state, "Answer received. Brain is resuming...")
	} else {
		s.send(state, fmt.Sprintf("Answer recorded. %d question(s) remaining.", pending))
	}
}

// --- Event handlers (brain → bot users) ---

func (s *BotService) onBrainMessage(e events.Event) {
	content, _ := e.Payload["content"].(string)
	if content == "" {
		return
	}
	s.broadcastToSiteUsers(e.SiteID, content)
}

func (s *BotService) onQuestionAsked(e events.Event) {
	question, _ := e.Payload["question"].(string)
	if question == "" {
		return
	}
	questionID := fmt.Sprintf("%v", e.Payload["id"])

	var options []string
	if opts, ok := e.Payload["options"].([]interface{}); ok {
		for _, o := range opts {
			if str, ok := o.(string); ok {
				options = append(options, str)
			}
		}
	}
	// Also try JSON string options.
	if len(options) == 0 {
		if optsStr, ok := e.Payload["options"].(string); ok && optsStr != "" && optsStr != "[]" {
			json.Unmarshal([]byte(optsStr), &options)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, state := range s.users {
		if state.ActiveSiteID != e.SiteID {
			continue
		}
		p, ok := s.providers[state.Provider]
		if !ok {
			continue
		}
		if len(options) > 0 {
			// Prefix options with answer callback data.
			labeled := make([]string, len(options))
			for i, opt := range options {
				labeled[i] = fmt.Sprintf("answer:%s:%s", questionID, opt)
			}
			p.SendQuestion(state.ChatID, question, labeled)
		} else {
			p.SendMessage(state.ChatID, fmt.Sprintf("Question: %s\n\nReply with your answer:", question))
		}
	}
}

func (s *BotService) onStageChange(e events.Event) {
	stage, _ := e.Payload["stage"].(string)
	if stage == "" {
		return
	}
	s.broadcastNotification(e.SiteID, fmt.Sprintf("Stage: %s", stage))
}

func (s *BotService) onModeChanged(e events.Event) {
	mode, _ := e.Payload["mode"].(string)
	if mode == "" {
		return
	}
	s.broadcastNotification(e.SiteID, fmt.Sprintf("Mode changed: %s", mode))
}

func (s *BotService) onBrainError(e events.Event) {
	errMsg, _ := e.Payload["error"].(string)
	if errMsg == "" {
		return
	}
	s.broadcastToSiteUsers(e.SiteID, fmt.Sprintf("Error: %s", errMsg))
}

func (s *BotService) onBrainStarted(e events.Event) {
	s.broadcastNotification(e.SiteID, "Brain started.")
}

func (s *BotService) onBrainStopped(e events.Event) {
	reason, _ := e.Payload["reason"].(string)
	msg := "Brain stopped."
	if reason != "" {
		msg += " Reason: " + reason
	}
	s.broadcastNotification(e.SiteID, msg)
}

func (s *BotService) onBrainProgress(e events.Event) {
	summary, _ := e.Payload["summary"].(string)
	if summary == "" {
		summary, _ = e.Payload["message"].(string)
	}
	if summary == "" {
		return
	}
	s.broadcastNotification(e.SiteID, fmt.Sprintf("Progress: %s", summary))
}

func (s *BotService) onToolFailed(e events.Event) {
	tool, _ := e.Payload["tool"].(string)
	errMsg, _ := e.Payload["error"].(string)
	s.broadcastNotification(e.SiteID, fmt.Sprintf("Tool failed: %s — %s", tool, errMsg))
}

func (s *BotService) onWebhookReceived(e events.Event) {
	source, _ := e.Payload["source"].(string)
	s.broadcastNotification(e.SiteID, fmt.Sprintf("Webhook received from: %s", source))
}

func (s *BotService) onPaymentCompleted(e events.Event) {
	amount, _ := e.Payload["amount"].(string)
	s.broadcastNotification(e.SiteID, fmt.Sprintf("Payment completed: %s", amount))
}

func (s *BotService) onPaymentFailed(e events.Event) {
	reason, _ := e.Payload["reason"].(string)
	s.broadcastNotification(e.SiteID, fmt.Sprintf("Payment failed: %s", reason))
}

// --- Helpers ---

func (s *BotService) getOrCreateUser(provider, chatID string) *UserState {
	key := provider + ":" + chatID
	s.mu.Lock()
	defer s.mu.Unlock()

	if state, ok := s.users[key]; ok {
		return state
	}
	state := &UserState{Provider: provider, ChatID: chatID}
	s.users[key] = state
	return state
}

func (s *BotService) send(state *UserState, text string) {
	if p, ok := s.providers[state.Provider]; ok {
		if err := p.SendMessage(state.ChatID, text); err != nil {
			s.logger.Warn("failed to send bot message", "provider", state.Provider, "chatID", state.ChatID, "error", err)
		}
	}
}

// broadcastToSiteUsers sends a message to all users on a site (always delivered, ignores mute).
func (s *BotService) broadcastToSiteUsers(siteID int, text string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, state := range s.users {
		if state.ActiveSiteID == siteID {
			if p, ok := s.providers[state.Provider]; ok {
				p.SendMessage(state.ChatID, text)
			}
		}
	}
}

// broadcastNotification sends a message to non-muted users on a site.
func (s *BotService) broadcastNotification(siteID int, text string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, state := range s.users {
		if state.ActiveSiteID == siteID && !state.Muted {
			if p, ok := s.providers[state.Provider]; ok {
				p.SendMessage(state.ChatID, text)
			}
		}
	}
}

// Ensure sql is used (for question answering).
var _ = (*sql.DB)(nil)
