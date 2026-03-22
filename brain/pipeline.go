/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/markdr-hue/HO/chat"
	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/prompt"
	"github.com/markdr-hue/HO/tools"
)

// --- Pipeline tuning constants ---
// Adjust these to trade speed vs reliability vs cost.
const (
	// Monitoring interval ramps from base to max when idle.
	monitoringBaseDefault = 5 * time.Minute
	monitoringMaxDefault  = 15 * time.Minute
	idleThreshold         = 3 // consecutive healthy ticks before interval grows

	// Retry/timeout limits.
	maxStageRetries  = 3               // retries per stage before aborting
	maxLLMRetries    = 3               // retries per individual LLM call
	maxContinuations = 5               // max "continue" requests when LLM hits max_tokens
	llmTimeout       = 7 * time.Minute // per-call timeout for LLM API
	toolTimeout      = 2 * time.Minute // per-call timeout for tool execution
)

// stageTimeouts defines the maximum wall-clock time each stage may run.
var stageTimeouts = map[PipelineStage]time.Duration{
	StagePlan:       10 * time.Minute,
	StageBuild:      120 * time.Minute,
	StageValidate:   5 * time.Minute,
	StageComplete:   1 * time.Minute,
	StageUpdatePlan: 10 * time.Minute,
}

// PipelineWorker is a goroutine that autonomously builds a site using a
// deterministic stage pipeline. It replaces the tick-based BrainWorker.
type PipelineWorker struct {
	siteID   int
	siteDB   *db.SiteDB
	deps     *Deps
	logger   *slog.Logger
	commands chan BrainCommand

	mu             sync.RWMutex
	state          BrainState
	idleCheckCount int
	wakeContext    map[string]interface{}

	semaphore             chan struct{}
	maxToolResultOverride int // if > 0, caps truncateToolResult below this size

	// Build progress tracking.
	buildProgress *buildProgressTracker

	// Anchor store for pre-page context refresh (Part 5).
	anchors *anchorStore

	// Cost tracking across all stages of a build.
	costTracker *buildCostTracker

	// systemPromptUpdater is called before each LLM call in runToolLoop.
	// If non-nil, it may return a new system prompt (e.g. to drop the Build
	// Guide after infrastructure is complete). Cleared after build.
	systemPromptUpdater func(current string) string

	// currentProviderID tracks the active LLM provider for fallback lookups.
	currentProviderID int

	// pauseRequested is set by executeToolCalls when a tool (e.g., manage_communication
	// with type=secret) signals that the pipeline should pause for owner input.
	pauseRequested bool
}

// anchorStore holds structured summaries of key build artifacts so they
// can be injected as context before each page creation.
type anchorStore struct {
	tableSchemas      map[string]string // table_name -> compact schema summary
	endpointAPIs      map[string]string // "/api/path" -> compact API description
	cssReference      string            // latest structured CSS reference
	jsReference       string            // latest structured JS API reference
	lastPageEndpoints string            // endpoint set of the last built page (for diff detection)
	layoutSummary     string            // compact layout structure reminder (header/footer/nav presence)

	// Quality & consistency features.
	componentGroups string            // CSS component families (card: .card, .card-header, ...)
	pageStructures  map[string]string // path -> compact HTML structure skeleton
	designNotes     []string          // brief structural notes per page ("/ : hero, feature grid, CTA")
	pagesBuiltCount int               // pages built so far (for aesthetic re-anchoring cadence)
}

// NewPipelineWorker creates a new pipeline worker for the given site.
func NewPipelineWorker(siteID int, deps *Deps, semaphore chan struct{}) (*PipelineWorker, error) {
	siteDB, err := deps.SiteDBManager.Open(siteID)
	if err != nil {
		return nil, err
	}
	return &PipelineWorker{
		siteID:    siteID,
		siteDB:    siteDB,
		deps:      deps,
		logger:    slog.With("component", "pipeline", "site_id", siteID),
		commands:  make(chan BrainCommand, 16),
		state:     StateIdle,
		semaphore: semaphore,
	}, nil
}

// State returns the current worker state.
func (w *PipelineWorker) State() BrainState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// Send enqueues a command for the worker.
func (w *PipelineWorker) Send(cmd BrainCommand) error {
	select {
	case w.commands <- cmd:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("pipeline command channel full, command %s not delivered", cmd.Type)
	}
}

// Run is the main loop. It should be called in its own goroutine.
func (w *PipelineWorker) Run(ctx context.Context) {
	w.logger.Info("pipeline worker started")
	if w.deps.Bus != nil {
		w.deps.Bus.Publish(events.NewEvent(events.EventBrainStarted, w.siteID, map[string]interface{}{
			"site_id": w.siteID,
		}))
	}

	defer func() {
		w.logger.Info("pipeline worker stopped")
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventBrainStopped, w.siteID, map[string]interface{}{
				"site_id": w.siteID,
			}))
		}
	}()

	// Load site mode to determine initial behavior.
	site, err := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	if err != nil {
		w.logger.Error("failed to load site", "error", err)
		return
	}

	switch site.Mode {
	case "building":
		// Brief delay for WAL checkpoint on fresh sites.
		time.Sleep(200 * time.Millisecond)
		if w.autoRecover() {
			w.setState(StateBuilding)
			w.runBuildPipeline(ctx)
		} else {
			w.setState(StatePaused)
			w.runPausedLoop(ctx)
		}
	case "monitoring":
		w.setState(StateMonitoring)
		w.runMonitoringLoop(ctx)
	case "paused":
		w.setState(StatePaused)
		w.runPausedLoop(ctx)
	default:
		w.setState(StateIdle)
		w.runPausedLoop(ctx)
	}
}

// autoRecover attempts to recover from a crash. Returns true if the pipeline
// can proceed, false if it should stay paused.
func (w *PipelineWorker) autoRecover() bool {
	var errorCount int
	w.siteDB.Reader().QueryRow("SELECT error_count FROM ho_pipeline_state WHERE id = 1").Scan(&errorCount)

	// Too many failures — require manual resume.
	if errorCount >= maxStageRetries {
		w.logger.Warn("auto-recovery skipped: error_count too high", "error_count", errorCount)
		return false
	}

	// Clear errors and non-question pauses.
	w.siteDB.ExecWrite(`UPDATE ho_pipeline_state SET error_count = 0, updated_at = CURRENT_TIMESTAMP WHERE id = 1 AND error_count > 0`)
	w.siteDB.ExecWrite(`UPDATE ho_pipeline_state SET paused = 0, pause_reason = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = 1 AND paused = 1 AND pause_reason NOT IN (?, ?)`, PauseReasonOwnerAnswers, PauseReasonApproval)

	// Auto-resume if paused for answers but all questions are already answered.
	var paused bool
	var reason string
	row := w.siteDB.Reader().QueryRow("SELECT paused, COALESCE(pause_reason,'') FROM ho_pipeline_state WHERE id = 1")
	if row.Scan(&paused, &reason) == nil && paused && reason == PauseReasonOwnerAnswers {
		var pending int
		if w.siteDB.Reader().QueryRow("SELECT COUNT(*) FROM ho_questions WHERE status = 'pending'").Scan(&pending) == nil && pending == 0 {
			w.logger.Info("auto-recovery: all questions answered, resuming")
			rows, err := w.siteDB.Query(
				`SELECT q.question, a.answer FROM ho_questions q JOIN ho_answers a ON a.question_id = q.id WHERE q.status = 'answered' ORDER BY q.id`)
			if err == nil {
				var parts []string
				for rows.Next() {
					var q, a string
					if rows.Scan(&q, &a) == nil {
						parts = append(parts, q+": "+a)
					}
				}
				rows.Close()
				w.mu.Lock()
				w.wakeContext = map[string]interface{}{"reason": "question_answered", "answer": strings.Join(parts, "\n")}
				w.mu.Unlock()
				ResumePipeline(w.siteDB)
			}
		}
	}

	return true
}

// runBuildPipeline executes the deterministic build pipeline, resuming from
// the current stage recorded in ho_pipeline_state.
func (w *PipelineWorker) runBuildPipeline(ctx context.Context) {
	state, err := LoadPipelineState(w.siteDB.Reader())
	if err != nil {
		w.logger.Error("failed to load pipeline state", "error", err)
		w.publishBrainError(fmt.Sprintf("Failed to load pipeline state: %v", err))
		return
	}

	if state.Paused {
		w.setState(StatePaused)
		w.publishBrainMessage(fmt.Sprintf("Pipeline paused: %s", state.PauseReason))
		w.runPausedLoop(ctx)
		return
	}

	// Initialize cost tracker for the build.
	w.costTracker = newBuildCostTracker()
	defer func() { w.costTracker = nil }()

	// Execute stages sequentially from current stage.
	stage := state.Stage

	stageRetries := 0
	var stageStart time.Time
	for {
		if ctx.Err() != nil {
			return
		}

		stageStart = time.Now()
		w.logger.Info("executing pipeline stage", "stage", stage)
		w.publishBrainMessage(fmt.Sprintf("Starting stage: **%s**", stage))

		var nextStage PipelineStage
		var stageErr error

		// Terminal stages that exit the build loop.
		if stage == StageMonitoring {
			w.setState(StateMonitoring)
			w.runMonitoringLoop(ctx)
			return
		}

		// Stage dispatch table — each stage returns (nextStage, error).
		stageHandlers := map[PipelineStage]func(context.Context) (PipelineStage, error){
			StagePlan:       w.runPlan,
			StageBuild:      w.runBuild,
			StageValidate:   w.runValidate,
			StageComplete:   w.runComplete,
			StageUpdatePlan: w.runUpdatePlan,
		}

		handler, ok := stageHandlers[stage]
		if !ok {
			w.logger.Error("unknown pipeline stage", "stage", stage)
			return
		}

		// Run the stage in a closure with panic recovery so a nil-pointer
		// or index-out-of-bounds inside any stage doesn't kill the worker.
		func() {
			defer func() {
				if r := recover(); r != nil {
					stageErr = fmt.Errorf("panic in stage %s: %v", stage, r)
					w.logger.Error("stage panic", "stage", stage, "panic", r, "stack", string(debug.Stack()))
				}
			}()

			// Per-stage timeout prevents any single stage from running indefinitely.
			stageCtx := ctx
			if timeout, ok := stageTimeouts[stage]; ok {
				var cancel context.CancelFunc
				stageCtx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			nextStage, stageErr = handler(stageCtx)
		}()

		if stageErr != nil {
			// Check if the stage intentionally paused (e.g., awaiting owner answers).
			// Don't count intentional pauses as errors.
			ps, psErr := LoadPipelineState(w.siteDB.Reader())
			if psErr != nil {
				w.logger.Warn("failed to check pause state after stage error", "error", psErr)
			}
			if ps != nil && ps.Paused {
				w.logger.Info("stage paused intentionally", "stage", stage, "reason", ps.PauseReason)
				w.setState(StatePaused)
				w.runPausedLoop(ctx)
				return
			}

			// VALIDATE: retry once for critical issues, then advance regardless.
			// Critical issues (missing pages, endpoints) get one more attempt.
			// Cosmetic or timeout failures skip directly to COMPLETE.
			if stage == StageValidate {
				if stageRetries < 1 && strings.Contains(stageErr.Error(), "critical validation") {
					w.logger.Warn("validate has critical issues, retrying once", "error", stageErr)
					w.publishBrainMessage("Critical issues found — retrying validation...")
					stageRetries++
					select {
					case <-ctx.Done():
						return
					case <-time.After(5 * time.Second):
					}
					continue
				}
				w.logger.Warn("validate stage failed, advancing to complete", "error", stageErr)
				w.publishBrainMessage("Validation fix-up exhausted — proceeding to completion.")
				if err := AdvanceStage(w.siteDB, StageComplete); err != nil {
					w.logger.Error("failed to advance past validate", "error", err)
				}
				stage = StageComplete
				stageRetries = 0
				continue
			}

			w.logger.Error("stage failed", "stage", stage, "error", stageErr)

			// Circuit breaker: permanent errors (bad config) should pause
			// immediately instead of burning retries that can never succeed.
			if isPermanentError(stageErr) {
				PausePipeline(w.siteDB, fmt.Sprintf("permanent error in %s: %v", stage, stageErr))
				w.publishBrainError(fmt.Sprintf("Pipeline paused: **%s** — %v. Fix the configuration and resume.", stage, stageErr))
				w.setState(StatePaused)
				w.runPausedLoop(ctx)
				return
			}

			IncrementErrorCount(w.siteDB, stageErr.Error())
			stageRetries++

			if stageRetries >= maxStageRetries {
				PausePipeline(w.siteDB, fmt.Sprintf("stage %s failed %d times", stage, stageRetries))
				w.publishBrainError(fmt.Sprintf("Pipeline paused: stage **%s** failed %d consecutive times. Last error: %v", stage, stageRetries, stageErr))
				w.setState(StatePaused)
				w.runPausedLoop(ctx)
				return
			}

			// Backoff before retrying to prevent tight loops.
			backoff := time.Duration(stageRetries) * 5 * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			w.logger.Info("retrying stage after backoff", "stage", stage, "backoff", backoff)
			w.publishBrainMessage(fmt.Sprintf("Stage %s failed (attempt %d/%d): %v — retrying...", stage, stageRetries, maxStageRetries, stageErr))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue // retry same stage
		}

		// Stage succeeded — advance (also resets error count atomically).
		if err := AdvanceStage(w.siteDB, nextStage); err != nil {
			w.logger.Error("failed to advance stage", "error", err)
			// Don't update in-memory stage — retry so the DB stays consistent.
			// Stages are idempotent, so re-executing is safe.
			continue
		}

		// Publish stage change event with timing data.
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventBrainStageChange, w.siteID, map[string]interface{}{
				"prev_stage":  string(stage),
				"stage":       string(nextStage),
				"duration_ms": time.Since(stageStart).Milliseconds(),
			}))
		}

		stage = nextStage
		stageRetries = 0
	}
}

// runMonitoringLoop handles the monitoring phase with adaptive timing.
func (w *PipelineWorker) runMonitoringLoop(ctx context.Context) {
	interval := w.monitoringInterval()
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-w.commands:
			w.handleCommand(ctx, cmd)
			timer.Reset(w.monitoringInterval())
		case <-timer.C:
			w.monitoringTick(ctx)
			timer.Reset(w.monitoringInterval())
		}
	}
}

// runPausedLoop waits for commands while paused.
func (w *PipelineWorker) runPausedLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-w.commands:
			w.handleCommand(ctx, cmd)
			// Check if we were unpaused.
			if w.State() == StateBuilding {
				w.runBuildPipeline(ctx)
				return
			}
			if w.State() == StateMonitoring {
				w.runMonitoringLoop(ctx)
				return
			}
		}
	}
}

// handleCommand dispatches a command.
func (w *PipelineWorker) handleCommand(ctx context.Context, cmd BrainCommand) {
	w.logger.Info("handling command", "type", cmd.Type)

	switch cmd.Type {
	case CommandWake:
		w.mu.Lock()
		w.idleCheckCount = 0
		if cmd.Payload != nil {
			// Deep-copy to prevent races if the sender retains a reference.
			cp := make(map[string]interface{}, len(cmd.Payload))
			for k, v := range cmd.Payload {
				cp[k] = v
			}
			w.wakeContext = cp
		}
		w.mu.Unlock()
		// If paused waiting for answers, resume the pipeline.
		if w.State() == StatePaused {
			ResumePipeline(w.siteDB)
			w.setState(StateBuilding)
		}

	case CommandModeChange:
		if mode, ok := cmd.Payload["mode"].(string); ok {
			switch mode {
			case "building":
				w.setState(StateBuilding)
				// Reset pipeline for fresh build.
				ResetPipeline(w.siteDB)
			case "monitoring":
				w.setState(StateMonitoring)
			case "paused":
				w.setState(StatePaused)
			}
			if w.deps.Bus != nil {
				w.deps.Bus.Publish(events.NewEvent(events.EventBrainModeChanged, w.siteID, map[string]interface{}{
					"site_id": w.siteID,
					"mode":    mode,
				}))
			}
		}

	case CommandUpdate:
		// Trigger incremental update with change description.
		w.mu.Lock()
		w.idleCheckCount = 0
		w.mu.Unlock()
		w.setState(StateBuilding)
		desc := ""
		if d, ok := cmd.Payload["description"].(string); ok {
			desc = d
		}
		w.siteDB.ExecWrite("UPDATE ho_pipeline_state SET update_description = ? WHERE id = 1", desc)
		if err := AdvanceStage(w.siteDB, StageUpdatePlan); err != nil {
			w.logger.Error("failed to set update stage", "error", err)
		}

	case CommandScheduledTask:
		runID := payloadInt64(cmd.Payload, "run_id")
		taskID := int(payloadInt64(cmd.Payload, "task_id"))
		if nativeAction, ok := cmd.Payload["native_action"].(string); ok && nativeAction != "" {
			w.executeNativeAction(ctx, nativeAction, runID, taskID)
		} else if prompt, ok := cmd.Payload["prompt"].(string); ok {
			w.executeScheduledTask(ctx, prompt, runID, taskID)
		}

	case CommandChat:
		if w.State() == StateMonitoring {
			w.mu.Lock()
			w.idleCheckCount = 0
			w.mu.Unlock()

			// If the command carries a user message, run a chat-wake with
			// write tools so the brain can fix things the owner reports.
			if msg, ok := cmd.Payload["message"].(string); ok && msg != "" {
				go w.handleChatWake(ctx, msg)
			}
		}

	case CommandShutdown:
		w.logger.Info("shutdown command received")
	}
}

// monitoringInterval returns the current monitoring tick interval with adaptive backoff.
func (w *PipelineWorker) monitoringInterval() time.Duration {
	base := monitoringBaseDefault
	if w.deps.MonitoringBase > 0 {
		base = w.deps.MonitoringBase
	}
	max := monitoringMaxDefault
	if w.deps.MonitoringMax > 0 {
		max = w.deps.MonitoringMax
	}

	w.mu.RLock()
	idle := w.idleCheckCount
	w.mu.RUnlock()

	if idle >= idleThreshold {
		return max
	}
	return base
}

// --- LLM execution helpers ---

// callWithRetry makes a single LLM call with retry/backoff for transient errors.
// After exhausting retries on the primary provider, it attempts a single fallback
// to an alternative provider if one is configured.
func (w *PipelineWorker) callWithRetry(ctx context.Context, lp llm.Provider, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	resp, err := w.callWithRetryOnProvider(ctx, lp, req)
	if err == nil {
		return resp, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}

	// Try fallback provider if primary exhausted retries.
	fallbackProvider, fallbackModelID, fbErr := w.getFallbackProvider()
	if fbErr != nil {
		return nil, err // return original error, no fallback available
	}

	w.logger.Warn("primary provider failed, trying fallback",
		"primary_error", err, "fallback_provider", fallbackProvider.Name(), "fallback_model", fallbackModelID)

	// Use fallback model ID in the request.
	fbReq := req
	fbReq.Model = fallbackModelID

	resp, fbCallErr := w.callWithRetryOnProvider(ctx, fallbackProvider, fbReq)
	if fbCallErr != nil {
		w.logger.Warn("fallback provider also failed", "error", fbCallErr)
		return nil, err // return original error
	}

	w.logger.Info("fallback provider succeeded", "provider", fallbackProvider.Name(), "model", fallbackModelID)
	return resp, nil
}

// callWithRetryOnProvider attempts LLM calls with retry/backoff on a single provider.
func (w *PipelineWorker) callWithRetryOnProvider(ctx context.Context, lp llm.Provider, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	var resp *llm.CompletionResponse
	var err error
	for attempt := 0; attempt < maxLLMRetries; attempt++ {
		llmCtx, cancel := context.WithTimeout(ctx, llmTimeout)
		resp, err = lp.Complete(llmCtx, req)
		cancel()
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}

		errMsg := err.Error()

		// Connection errors (DNS, refused, TLS) won't succeed on retry.
		if strings.Contains(errMsg, "send request:") && !strings.Contains(errMsg, "API error") &&
			!strings.Contains(errMsg, "timeout") && !strings.Contains(errMsg, "context deadline exceeded") {
			return nil, err
		}

		isRateLimit := strings.Contains(errMsg, "API error 429") || strings.Contains(errMsg, "API error 529") || strings.Contains(errMsg, "overloaded")
		isTimeout := strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "context deadline exceeded")

		var backoff time.Duration
		switch {
		case isRateLimit:
			backoff = time.Duration(15*(attempt+1)) * time.Second
			w.logger.Warn("LLM rate limited", "attempt", attempt, "backoff", backoff)
		case isTimeout:
			backoff = time.Duration(5*(attempt+1)) * time.Second
			w.logger.Warn("LLM timeout", "attempt", attempt, "backoff", backoff)
		default:
			backoff = time.Duration(2*(attempt+1)) * time.Second
			w.logger.Warn("LLM call failed", "attempt", attempt, "error", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, err
}

// runToolLoop executes an LLM tool-call loop: call LLM, execute tool calls,
// repeat until no more tool calls or maxIter reached.
func (w *PipelineWorker) runToolLoop(ctx context.Context, provider llm.Provider, modelID, systemPrompt string, messages []llm.Message, toolDefs []llm.ToolDef, maxIter, maxTokens int, temperature ...float64) (lastContent, lastModel string, totalTokens, totalToolCalls int, err error) {
	var temp *float64
	if len(temperature) > 0 {
		temp = &temperature[0]
	}
	// Build allowed tool set from the definitions sent to the LLM.
	// This ensures tools outside the stage's allowed set are rejected
	// even if they exist in the full registry.
	allowedTools := make(map[string]bool, len(toolDefs))
	for _, td := range toolDefs {
		allowedTools[td.Name] = true
	}

	iteration := 0
	continuationCount := 0
	llmLogger := llm.NewDBLLMLogger(w.siteDB.Writer())
	defer llmLogger.Close()
	loggedProvider := llm.NewLoggedProvider(provider, llmLogger, "brain", "brain", &iteration)

	for i := 0; i < maxIter; i++ {
		iteration = i

		// Progress messages for short loops (VALIDATE, UPDATE_PLAN) so the
		// user sees activity. BUILD uses its own buildProgress tracker.
		if maxIter <= 10 && i > 0 {
			w.publishBrainMessage(fmt.Sprintf("Fix-up pass %d/%d...", i+1, maxIter))
		}

		// Allow mid-loop system prompt updates (e.g. drop Build Guide after infrastructure).
		if w.systemPromptUpdater != nil {
			systemPrompt = w.systemPromptUpdater(systemPrompt)
		}

		resp, callErr := w.callWithRetry(ctx, loggedProvider, llm.CompletionRequest{
			Model:       modelID,
			System:      systemPrompt,
			Messages:    messages,
			Tools:       toolDefs,
			MaxTokens:   maxTokens,
			Temperature: temp,
			CacheSystem: true,
		})
		if callErr != nil {
			return "", "", totalTokens, totalToolCalls, fmt.Errorf("LLM call failed at iteration %d: %w", i, callErr)
		}

		totalTokens += resp.Usage.InputTokens + resp.Usage.OutputTokens
		lastContent = resp.Content
		lastModel = resp.Model

		// Save and publish assistant message.
		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		if resp.Content != "" || len(resp.ToolCalls) > 0 {
			w.saveChatMessage(assistantMsg)
		}
		// Only publish assistant text to chat when it's a final response (no tool calls).
		// When tool calls follow, the text is just LLM "thinking aloud" — not useful for the user.
		if resp.Content != "" && len(resp.ToolCalls) == 0 && w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventBrainMessage, w.siteID, map[string]interface{}{
				"session_id": "brain",
				"role":       "assistant",
				"content":    resp.Content,
			}))
		}

		if len(resp.ToolCalls) == 0 {
			if resp.StopReason == "max_tokens" {
				continuationCount++
				if continuationCount >= maxContinuations {
					w.logger.Warn("LLM hit max_tokens too many times, stopping", "iteration", i, "continuations", continuationCount)
					w.publishBrainMessage("Build hit output limit — some items may be incomplete. Use chat to request fixes.")
					break
				}
				w.logger.Warn("LLM hit max_tokens, requesting continuation", "iteration", i, "continuation", continuationCount)
				messages = append(messages, assistantMsg)
				contMsg := "Your response was cut off. Continue building from where you left off — call your next tool."
				if w.buildProgress != nil {
					if remaining := w.buildProgress.remainingItems(); remaining != "" {
						contMsg += "\n\n" + remaining
					}
				}
				messages = append(messages, llm.Message{
					Role:    llm.RoleUser,
					Content: contMsg,
				})
				continue
			}
			break
		}

		// Detect and discard incomplete tool calls caused by max_tokens truncation.
		// When the LLM is cut off mid-tool-call, the last call may have malformed JSON.
		if resp.StopReason == "max_tokens" && len(resp.ToolCalls) > 0 {
			lastTC := resp.ToolCalls[len(resp.ToolCalls)-1]
			var testArgs map[string]interface{}
			if err := json.Unmarshal([]byte(lastTC.Arguments), &testArgs); err != nil {
				w.logger.Warn("discarding truncated tool call", "tool", lastTC.Name, "error", err)
				resp.ToolCalls = resp.ToolCalls[:len(resp.ToolCalls)-1]
				assistantMsg.ToolCalls = resp.ToolCalls
				if len(resp.ToolCalls) == 0 {
					// All tool calls were truncated — request continuation.
					continuationCount++
					messages = append(messages, assistantMsg)
					contMsg := fmt.Sprintf("Your response was truncated while calling %s. Continue from where you left off — call the tool again with complete arguments.", lastTC.Name)
					messages = append(messages, llm.Message{Role: llm.RoleUser, Content: contMsg})
					continue
				}
			}
		}

		messages = append(messages, assistantMsg)
		messages = w.executeToolCalls(ctx, resp.ToolCalls, messages, allowedTools)
		totalToolCalls += len(resp.ToolCalls)

		// Check if any tool requested a pipeline pause (e.g., secret question asked
		// during BUILD). When detected, save checkpoint and return sentinel error.
		if w.pauseRequested {
			w.pauseRequested = false
			w.saveCheckpointMessages(messages)
			PausePipeline(w.siteDB, PauseReasonOwnerAnswers)
			w.logger.Info("tool requested pipeline pause, saving checkpoint and pausing")
			return lastContent, lastModel, totalTokens, totalToolCalls, ErrPipelinePaused
		}

		// Early termination: once all plan items are built, allow a small grace
		// window for polish (e.g. seed data, verify), then force stop to prevent
		// the LLM from endlessly reading/re-saving in a fidget loop.
		if w.buildProgress != nil && w.buildProgress.allComplete() {
			w.buildProgress.postCompletionIters++
			if w.buildProgress.postCompletionIters == 1 {
				w.logger.Info("all plan items built, starting grace window", "iteration", i)
			}
			graceLimit := 2
			// Detect read-only fidget loops: if the LLM is only reading
			// (no writes), it's reviewing not polishing — cut grace short.
			hasWrites := false
			for _, tc := range resp.ToolCalls {
				var tcArgs map[string]interface{}
				if json.Unmarshal([]byte(tc.Arguments), &tcArgs) == nil {
					action, _ := tcArgs["action"].(string)
					if action != "get" && action != "list" && action != "describe" && action != "search" && action != "history" {
						hasWrites = true
						break
					}
				}
			}
			if !hasWrites {
				w.buildProgress.readOnlyGraceIters++
			} else {
				w.buildProgress.readOnlyGraceIters = 0
			}
			if w.buildProgress.readOnlyGraceIters >= 2 {
				w.logger.Info("grace window: 2 consecutive read-only iterations, stopping", "iteration", i)
				break
			}
			if w.buildProgress.postCompletionIters > graceLimit {
				w.logger.Info("grace window exhausted, stopping build loop", "iteration", i, "grace_iters", w.buildProgress.postCompletionIters)
				break
			}
		}

		// At 75% of budget, nudge the LLM about remaining plan items.
		if w.buildProgress != nil && !w.buildProgress.nudgeSent && i >= maxIter*3/4 {
			if reminder := w.buildProgress.remainingItems(); reminder != "" {
				w.buildProgress.nudgeSent = true
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: reminder})
			}
		}

		// Prune old messages with anchor preservation.
		// Dynamic recent window — complex sites need more context.
		// Only prune when message count exceeds threshold to avoid unnecessary O(n) walks.
		recentWindow := 18
		if w.buildProgress != nil && w.buildProgress.pagesTotal > 6 {
			recentWindow = 24
		}
		if len(messages) > recentWindow+8 {
			messages = pruneMessages(messages, recentWindow)
		}
	}
	return
}

// toolCallResult holds the outcome of a single tool execution.
type toolCallResult struct {
	tc      llm.ToolCall
	args    map[string]interface{}
	result  string
	toolErr error
}

// toolResultSucceeded returns true only when the tool executed without a Go-level
// error AND the JSON result indicates success. Tools return business-logic failures
// via Result{Success: false} with a nil Go error — those must NOT be treated as
// successful for progress tracking, messaging, or anchor injection.
func (r *toolCallResult) toolResultSucceeded() bool {
	if r.toolErr != nil {
		return false
	}
	// Quick JSON check — all tool results are marshaled Result structs where
	// "success":false appears near the start of the string.
	return !strings.Contains(r.result, `"success":false`)
}

// executeToolCalls runs tool calls and appends results to messages.
// When multiple tool calls are present, they execute concurrently (up to 4 at a time)
// with results collected in the original order for deterministic message history.
func (w *PipelineWorker) executeToolCalls(ctx context.Context, toolCalls []llm.ToolCall, messages []llm.Message, allowedTools map[string]bool) []llm.Message {
	// Pre-validate all tool calls and parse arguments.
	type preparedCall struct {
		tc   llm.ToolCall
		args map[string]interface{}
		err  string // non-empty if validation failed
	}
	prepared := make([]preparedCall, len(toolCalls))
	for i, tc := range toolCalls {
		prepared[i].tc = tc
		w.logger.Info("tool call", "tool", tc.Name, "call_id", tc.ID)

		// Reject tools not in the stage's allowed set (prevents hallucinated
		// calls to tools that exist in the registry but weren't sent to the LLM).
		if allowedTools != nil && !allowedTools[tc.Name] {
			w.logger.Warn("tool not allowed in current stage", "tool", tc.Name)
			prepared[i].err = fmt.Sprintf(`{"error": "tool '%s' is not available in the current stage"}`, tc.Name)
			continue
		}

		if !w.deps.ToolRegistry.Has(tc.Name) {
			w.logger.Warn("unknown tool called by LLM", "tool", tc.Name)
			prepared[i].err = fmt.Sprintf(`{"error": "unknown tool '%s' — check available tool names"}`, tc.Name)
			continue
		}

		argBytes := []byte(tc.Arguments)
		if len(argBytes) > 0 && argBytes[0] == '[' {
			var arr []json.RawMessage
			if err := json.Unmarshal(argBytes, &arr); err == nil && len(arr) == 1 {
				argBytes = arr[0]
			}
		}
		var args map[string]interface{}
		if err := json.Unmarshal(argBytes, &args); err != nil {
			w.logger.Warn("bad tool arguments", "tool", tc.Name, "error", err)
			prepared[i].err = fmt.Sprintf(`{"error": "invalid JSON arguments for tool %s: %s"}`, tc.Name, err.Error())
			continue
		}
		prepared[i].args = args
	}

	// Execute valid tool calls concurrently with a semaphore.
	const maxConcurrentToolCalls = 4
	sem := make(chan struct{}, maxConcurrentToolCalls)
	results := make([]toolCallResult, len(prepared))
	var wg sync.WaitGroup

	for i, pc := range prepared {
		if pc.err != "" {
			results[i] = toolCallResult{tc: pc.tc, args: pc.args, result: pc.err, toolErr: fmt.Errorf("validation")}
			continue
		}

		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventBrainToolStart, w.siteID, map[string]interface{}{
				"tool":    pc.tc.Name,
				"name":    pc.tc.Name,
				"call_id": pc.tc.ID,
				"args":    pc.args,
			}))
		}

		wg.Add(1)
		go func(idx int, pc preparedCall) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					w.logger.Error("tool panicked", "tool", pc.tc.Name, "panic", r, "stack", string(debug.Stack()))
					results[idx] = toolCallResult{
						tc:      pc.tc,
						args:    pc.args,
						result:  fmt.Sprintf(`{"error": "tool %s panicked: %v"}`, pc.tc.Name, r),
						toolErr: fmt.Errorf("panic: %v", r),
					}
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()

			toolCtx := &tools.ToolContext{
				DB:         w.siteDB.Writer(),
				GlobalDB:   w.deps.DB.DB,
				SiteID:     w.siteID,
				Logger:     w.logger,
				Bus:        w.deps.Bus,
				Encryptor:  w.deps.Encryptor,
				PublicPort: w.deps.PublicPort,
			}

			toolExecCtx, toolCancel := context.WithTimeout(ctx, toolTimeout)
			result, toolErr := w.deps.ToolExecutor.Execute(toolExecCtx, toolCtx, pc.tc.Name, pc.args)
			toolCancel()
			if toolErr != nil {
				w.logger.Error("tool failed", "tool", pc.tc.Name, "error", toolErr)
				result = fmt.Sprintf(`{"error": "tool %s failed: %s", "advice": "Read the current state of the resource before retrying. Check for constraint violations, missing dependencies, or typos in names."}`, pc.tc.Name, toolErr.Error())
			}
			results[idx] = toolCallResult{tc: pc.tc, args: pc.args, result: result, toolErr: toolErr}
		}(i, pc)
	}
	wg.Wait()

	// Append results in original order for deterministic message history.
	for _, r := range results {
		toolResultMsg := llm.Message{
			Role:       llm.RoleTool,
			Content:    w.truncateToolResult(r.tc.Name, r.result),
			ToolCallID: r.tc.ID,
		}
		messages = append(messages, toolResultMsg)

		summaryMsg := llm.Message{
			Role:       llm.RoleTool,
			Content:    w.summarizeToolResult(r.tc.Name, r.result),
			ToolCallID: r.tc.ID,
		}
		w.saveChatMessage(summaryMsg)

		resultPayload := map[string]interface{}{
			"tool":    r.tc.Name,
			"name":    r.tc.Name,
			"call_id": r.tc.ID,
			"args":    r.args,
		}
		if isInteractiveTool(r.tc.Name) {
			resultPayload["result"] = r.result
		} else {
			resultPayload["result"] = truncate(r.result, 500)
		}
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventBrainToolResult, w.siteID, resultPayload))
		}

		if r.toolResultSucceeded() {
			if msg := toolProgressMessage(r.tc.Name, r.args); msg != "" {
				w.publishBrainMessage(msg)
			}
		}

		if r.toolResultSucceeded() && w.buildProgress != nil {
			w.buildProgress.trackToolResult(r.tc.Name, r.args)
			if w.buildProgress.toolCallsSeen%5 == 0 && w.deps.Bus != nil {
				w.deps.Bus.Publish(events.NewEvent(events.EventBrainProgress, w.siteID, w.buildProgress.toPayload()))
			}
			// Checkpoint: save conversation once when infrastructure phase completes.
			if !w.buildProgress.checkpointed && w.buildProgress.infrastructureComplete() {
				w.buildProgress.checkpointed = true
				w.saveCheckpointMessages(messages)
				w.publishBrainMessage("Infrastructure phase complete — checkpoint saved.")
			}
		}

		if r.toolResultSucceeded() {
			messages = w.injectAnchors(r.tc.Name, r.args, messages)
		}

		// Detect secret/question requests from manage_communication during BUILD.
		// When the brain asks a question (especially type=secret), signal the
		// pipeline to pause so the owner can respond.
		if r.toolResultSucceeded() && r.tc.Name == "manage_communication" {
			if action, _ := r.args["action"].(string); action == "ask" {
				if qType, _ := r.args["type"].(string); qType == "secret" {
					w.logger.Info("secret question asked during build, requesting pause")
					w.pauseRequested = true
				}
			}
		}
	}
	return messages
}

// appendAnchorIfNew adds a user message to the conversation only if an identical
// message doesn't already exist. Prevents duplicate anchors from accumulating.
func appendAnchorIfNew(messages []llm.Message, msg llm.Message) []llm.Message {
	for _, m := range messages {
		if m.Role == llm.RoleUser && m.Content == msg.Content {
			return messages
		}
	}
	return append(messages, msg)
}

// injectAnchors adds context-preserving [ANCHOR] messages after key tool calls.
// Anchors survive message pruning and keep the LLM aware of CSS classes, JS APIs,
// table schemas, and endpoint contracts as the conversation grows.
func (w *PipelineWorker) injectAnchors(toolName string, args map[string]interface{}, messages []llm.Message) []llm.Message {
	action, _ := args["action"].(string)

	switch toolName {
	case "manage_files":
		if action != "save" && action != "" {
			break
		}
		fn, _ := args["filename"].(string)
		content, _ := args["content"].(string)
		if content == "" {
			break
		}
		if strings.HasSuffix(fn, ".css") {
			if ref := prompt.ExtractCSSReference(content); ref != "" {
				msg := "[ANCHOR] CSS design vocabulary (" + fn + "). Prefer var(--color-*) tokens for primary colors.\n" + ref
				messages = appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: msg})
				if w.anchors != nil {
					w.anchors.cssReference = ref
					// Extract component families for consistent cross-page usage.
					if groups := prompt.ExtractComponentGroups(content); groups != "" {
						w.anchors.componentGroups = groups
					}
				}
			}
		}
		if strings.HasSuffix(fn, ".js") {
			scope, _ := args["scope"].(string)
			if scope == "global" {
				if ref := prompt.ExtractJSReference(content); ref != "" {
					msg := "[ANCHOR] JS API (" + fn + ", global) — use these exact signatures in page JS:\n" + ref
					messages = appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: msg})
					if w.anchors != nil {
						w.anchors.jsReference = ref
					}
				}
			}
		}

	case "manage_schema":
		if action == "create" {
			if summary := buildSchemaAnchor(args); summary != "" {
				messages = appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: summary})
				if w.anchors != nil {
					if name, _ := args["table_name"].(string); name != "" {
						w.anchors.tableSchemas[name] = summary
					}
				}
			}
		}

	case "manage_endpoints":
		if strings.HasPrefix(action, "create_") {
			if summary := buildEndpointAnchor(args); summary != "" {
				messages = appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: summary})
				if w.anchors != nil {
					if path, _ := args["path"].(string); path != "" {
						w.anchors.endpointAPIs["/api/"+path] = summary
					}
				}
			}
		}

	case "manage_layout":
		if action == "save" {
			template, _ := args["template"].(string)
			if template != "" {
				lower := strings.ToLower(template)
				hasHeader := strings.Contains(lower, "<header")
				hasFooter := strings.Contains(lower, "<footer")
				hasNav := strings.Contains(lower, "<nav")
				var parts []string
				if hasHeader {
					parts = append(parts, "has <header>")
				}
				if hasNav {
					parts = append(parts, "has <nav>")
				}
				if hasFooter {
					parts = append(parts, "has <footer>")
				}
				var summary string
				if len(parts) == 0 {
					summary = "[ANCHOR] Layout is chromeless (no header/nav/footer). Pages fill the viewport — do NOT add <header>, <nav>, or <footer> in page content."
				} else {
					summary = "[ANCHOR] Layout template provides: " + strings.Join(parts, ", ") + ". Do NOT duplicate these in page content — they are already in the layout."
				}
				messages = appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: summary})
				if w.anchors != nil {
					w.anchors.layoutSummary = summary
				}
			}
		}

	case "manage_pages":
		if action == "save" && w.anchors != nil {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)

			// Track page structure from the first few pages as design reference.
			if content != "" && len(w.anchors.pageStructures) < 3 {
				if structure := prompt.ExtractPageStructure(content); structure != "" {
					w.anchors.pageStructures[path] = structure
				}
			}

			// Record a brief design note for this page.
			if content != "" && path != "" {
				note := buildPageDesignNote(path, content)
				if note != "" {
					w.anchors.designNotes = append(w.anchors.designNotes, note)
				}
			}

			w.anchors.pagesBuiltCount++

			// Inject quality review nudge + next page context.
			if refresh := w.buildPageContextRefresh(); refresh != "" {
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: refresh})
			}
		}
	}

	return messages
}

// buildPageContextRefresh assembles a compact context summary from the anchor
// store to inject after each page save. This ensures the LLM has fresh awareness
// of the JS API, CSS vocabulary, and available endpoints when building the next page.
// Also includes quality review nudges, component patterns, and aesthetic re-anchoring.
func (w *PipelineWorker) buildPageContextRefresh() string {
	if w.anchors == nil {
		return ""
	}
	var b strings.Builder

	// Only include if we have meaningful content.
	hasContent := false

	// --- Quality review nudge ---
	// Brief self-check prompt to catch issues before moving on.
	// Adapt the check to the app type — chromeless apps don't use .container.
	isChromeless := false
	if w.buildProgress != nil && w.buildProgress.plan != nil {
		h, f := layoutHeaderFooter(w.buildProgress.plan)
		isChromeless = h == "none" && f == "none"
	}
	if isChromeless {
		b.WriteString("**Quality check:** Before moving to the next item, verify the page you just built: Does it work as intended? Are interactive elements wired? Is the viewport usage correct? Is it consistent with the design intent? If anything is off, patch it now.\n\n")
	} else {
		b.WriteString("**Quality check:** Before moving to the next item, verify the page you just built: Does it have clear visual hierarchy? Do interactive elements work (buttons, links, forms)? Does it use .container for alignment? Is it consistent with earlier pages? If anything is off, patch it now.\n\n")
	}
	hasContent = true

	// --- Layout structure reminder ---
	// Re-inject what the layout provides so pages don't duplicate header/footer/nav.
	if w.anchors.layoutSummary != "" {
		b.WriteString(w.anchors.layoutSummary + "\n\n")
	}

	// --- Aesthetic re-anchoring (every 3 pages) ---
	// Re-inject design intent and established patterns to prevent drift.
	if w.anchors.pagesBuiltCount > 0 && w.anchors.pagesBuiltCount%3 == 0 {
		if w.buildProgress != nil && w.buildProgress.plan != nil {
			plan := w.buildProgress.plan
			if plan.DesignSystem != nil && plan.DesignSystem.DesignIntent != "" {
				b.WriteString("### Design Reminder\n")
				b.WriteString("Design intent: " + plan.DesignSystem.DesignIntent + "\n")
				if plan.AppType != "" {
					b.WriteString("App type: " + plan.AppType + "\n")
				}
				// Include design notes from pages built so far.
				if len(w.anchors.designNotes) > 0 {
					b.WriteString("Patterns used so far: " + strings.Join(w.anchors.designNotes, " | ") + "\n")
				}
				b.WriteString("Maintain this aesthetic consistently on the remaining pages.\n\n")
			}
		}
	}

	// --- Component groups (from CSS) ---
	// Inject component families so the LLM uses consistent class groups.
	if w.anchors.componentGroups != "" && w.anchors.pagesBuiltCount <= 2 {
		// Only inject for first few pages — after that the LLM has established patterns.
		b.WriteString("### Component Families (use these class groups together)\n")
		b.WriteString(w.anchors.componentGroups)
		b.WriteString("\n\n")
		hasContent = true
	}

	// --- Page structure reference (from first page) ---
	// Show the HTML patterns established by the first page so later pages stay consistent.
	if len(w.anchors.pageStructures) > 0 && w.anchors.pagesBuiltCount >= 1 && w.anchors.pagesBuiltCount <= 4 {
		b.WriteString("### Established HTML Patterns (match this structure)\n")
		count := 0
		for path, structure := range w.anchors.pageStructures {
			if count >= 2 {
				break
			}
			b.WriteString("From " + path + ":\n" + structure + "\n")
			count++
		}
		b.WriteString("\n")
		hasContent = true
	}

	// Inject the plan spec for the next unbuilt page so the LLM has
	// fresh awareness of what to build next.
	var nextPageEndpoints string
	if w.buildProgress != nil && w.buildProgress.plan != nil {
		for _, pg := range w.buildProgress.plan.Pages {
			if !w.buildProgress.pagesDone[pg.Path] {
				b.WriteString(fmt.Sprintf("## Next Page: %s — %s\n", pg.Path, pg.Title))
				if pg.Purpose != "" {
					b.WriteString("Purpose: " + pg.Purpose + "\n")
				}
				if len(pg.Sections) > 0 {
					b.WriteString("Sections: ")
					for i, s := range pg.Sections {
						if i > 0 {
							b.WriteString(", ")
						}
						b.WriteString(s.Name)
						if s.Purpose != "" {
							b.WriteString(" (" + s.Purpose + ")")
						}
					}
					b.WriteString("\n")
				}
				if len(pg.Endpoints) > 0 {
					nextPageEndpoints = strings.Join(pg.Endpoints, ",")
					b.WriteString("Uses: " + strings.Join(pg.Endpoints, ", ") + "\n")
				}
				if pg.Notes != "" {
					b.WriteString("Notes: " + pg.Notes + "\n")
				}
				b.WriteString("\n")
				hasContent = true
				break
			}
		}
	}

	b.WriteString("**Rules:** Prefer var(--color-*) tokens for primary colors. No TODOs/placeholders/Lorem ipsum. Every interactive element must work.\n\n")

	// Only inject full reference blocks (tables, JS API, endpoints, CSS classes)
	// when the next page uses different endpoints than the last one — this avoids
	// re-processing identical context on consecutive similar pages.
	endpointsChanged := nextPageEndpoints != w.anchors.lastPageEndpoints
	w.anchors.lastPageEndpoints = nextPageEndpoints

	if endpointsChanged {
		// Re-inject table schemas so late pages know column names for fetch/render.
		if len(w.anchors.tableSchemas) > 0 {
			b.WriteString("### Tables (column reference)\n")
			for _, summary := range w.anchors.tableSchemas {
				s := strings.TrimPrefix(summary, "[ANCHOR] ")
				b.WriteString("- " + s + "\n")
			}
			b.WriteString("\n")
			hasContent = true
		}

		if w.anchors.jsReference != "" {
			b.WriteString("### JS API (use exact signatures)\n")
			ref := w.anchors.jsReference
			if len(ref) > 1500 {
				ref = ref[:1500] + "\n..."
			}
			b.WriteString(ref)
			b.WriteString("\n\n")
			hasContent = true
		}

		if len(w.anchors.endpointAPIs) > 0 {
			b.WriteString("### Available Endpoints\n")
			for _, summary := range w.anchors.endpointAPIs {
				s := strings.TrimPrefix(summary, "[ANCHOR] ")
				b.WriteString("- " + s + "\n")
			}
			b.WriteString("\n")
			hasContent = true
		}
	}

	// Always inject CSS reference — compact class+property summaries prevent
	// class drift and help the LLM reuse existing classes correctly.
	if w.anchors.cssReference != "" {
		b.WriteString("### CSS Classes (prefer these — add new ones to global CSS via manage_files patch if needed)\n")
		b.WriteString(w.anchors.cssReference)
		b.WriteString("\n\n")
		hasContent = true
	}

	if !hasContent {
		return ""
	}
	// Tag so pruneMessages can identify and deduplicate page context messages.
	return "[PAGE_CTX]\n" + b.String()
}

// buildPageDesignNote creates a brief structural description of a page for
// design decision logging. This helps the LLM maintain aesthetic coherence
// across pages by knowing what patterns were used on earlier pages.
func buildPageDesignNote(path, html string) string {
	lower := strings.ToLower(html)
	var patterns []string

	// Detect common section types by class names and content patterns.
	sectionHints := []struct {
		keyword string
		label   string
	}{
		{"hero", "hero"},
		{"banner", "banner"},
		{"feature", "features"},
		{"pricing", "pricing"},
		{"testimonial", "testimonials"},
		{"faq", "FAQ"},
		{"cta", "CTA"},
		{"contact", "contact form"},
		{"footer", "footer"},
		{"sidebar", "sidebar"},
		{"grid", "grid layout"},
		{"carousel", "carousel"},
		{"slider", "slider"},
		{"gallery", "gallery"},
		{"timeline", "timeline"},
		{"stats", "stats"},
		{"metric", "metrics"},
		{"dashboard", "dashboard"},
		{"table", "data table"},
		{"form", "form"},
		{"card", "cards"},
		{"list", "list"},
		{"modal", "modal"},
		{"tab", "tabs"},
		{"accordion", "accordion"},
	}

	seen := map[string]bool{}
	for _, hint := range sectionHints {
		if strings.Contains(lower, hint.keyword) && !seen[hint.label] {
			seen[hint.label] = true
			patterns = append(patterns, hint.label)
		}
	}

	if len(patterns) == 0 {
		return ""
	}
	// Cap at 6 patterns to keep notes compact.
	if len(patterns) > 6 {
		patterns = patterns[:6]
	}
	return path + ": " + strings.Join(patterns, ", ")
}

// buildSchemaAnchor creates a compact summary of a schema creation for context preservation.
func buildSchemaAnchor(args map[string]interface{}) string {
	name, _ := args["table_name"].(string)
	if name == "" {
		return ""
	}
	var cols []string
	if colsRaw, ok := args["columns"]; ok {
		if colSlice, ok := colsRaw.([]interface{}); ok {
			for _, c := range colSlice {
				if cm, ok := c.(map[string]interface{}); ok {
					colName, _ := cm["name"].(string)
					colType, _ := cm["type"].(string)
					if colName != "" {
						cols = append(cols, colName+" "+colType)
					}
				}
			}
		}
	}
	summary := fmt.Sprintf("[ANCHOR] Table '%s': id (auto), %s, created_at (auto).", name, strings.Join(cols, ", "))
	if sc, ok := args["searchable_columns"]; ok {
		if scSlice, ok := sc.([]interface{}); ok {
			var scNames []string
			for _, s := range scSlice {
				if sn, ok := s.(string); ok {
					scNames = append(scNames, sn)
				}
			}
			if len(scNames) > 0 {
				summary += " FTS: " + strings.Join(scNames, ", ") + "."
			}
		}
	}
	return summary
}

// buildEndpointAnchor creates a compact summary of an endpoint creation.
func buildEndpointAnchor(args map[string]interface{}) string {
	action, _ := args["action"].(string)
	path, _ := args["path"].(string)
	table, _ := args["table_name"].(string)
	if path == "" {
		return ""
	}

	var summary string
	switch action {
	case "create_api":
		summary = fmt.Sprintf("[ANCHOR] CRUD /api/%s (table: %s). GET ?q=&sort=&order=, POST, PUT/:id, DELETE/:id. Frontend: use query params (?col=val), not filters=[{...}].", path, table)
		if ra, ok := args["requires_auth"].(bool); ok && ra {
			summary += " Auth: required."
		}
		if pr, ok := args["public_read"].(bool); ok && pr {
			summary += " Public read: yes."
		}
	case "create_auth":
		usernameCol, _ := args["username_column"].(string)
		passwordCol, _ := args["password_column"].(string)
		if passwordCol == "" {
			passwordCol = "password"
		}
		summary = fmt.Sprintf("[ANCHOR] Auth /api/%s: login(POST), register(POST), me(GET+Bearer). Table: %s. FIELD NAMES for register/login body: {%q: \"...\", %q: \"...\"}.", path, table, usernameCol, passwordCol)
	case "create_websocket":
		summary = fmt.Sprintf("[ANCHOR] WebSocket /api/%s/ws. Pure relay, echo suppression.", path)
		if room, ok := args["room_column"].(string); ok && room != "" {
			summary += " Rooms via ?room=."
		}
	case "create_stream":
		summary = fmt.Sprintf("[ANCHOR] SSE /api/%s/stream.", path)
	case "create_upload":
		summary = fmt.Sprintf("[ANCHOR] Upload POST /api/%s/upload -> {url, filename, size, type}.", path)
	case "create_llm":
		summary = fmt.Sprintf("[ANCHOR] LLM POST /api/%s/chat (SSE) and POST /api/%s/complete (JSON). No CRUD — use a separate create_api endpoint if page also needs data listing.", path, path)
		if streaming, ok := args["streaming"].(bool); ok && !streaming {
			summary += " Plan says streaming=false: page JS should use /complete (JSON), not /chat (SSE)."
		} else {
			summary += " Plan says streaming=true: page JS should use /chat (SSE) with EventSource pattern."
		}
	default:
		summary = fmt.Sprintf("[ANCHOR] Endpoint %s /api/%s.", action, path)
	}
	return summary
}

// pruneMessages keeps the first user message (build instructions), anchor messages
// ([ANCHOR] prefix), and the most recent messages. This prevents
// losing critical early context (table structures, CSS class names) that later pages need.
func pruneMessages(messages []llm.Message, maxRecent int) []llm.Message {
	total := len(messages)
	if total <= maxRecent+1 {
		return messages
	}

	// Remove stale [PAGE_CTX] messages: only the latest one matters (it
	// describes the NEXT page to build). Older ones are for already-built pages.
	latestPageCtx := -1
	for i := total - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleUser && strings.HasPrefix(messages[i].Content, "[PAGE_CTX]") {
			if latestPageCtx == -1 {
				latestPageCtx = i
			} else {
				// Mark older [PAGE_CTX] for removal by clearing content.
				messages[i] = llm.Message{Role: llm.RoleUser, Content: ""}
			}
		}
	}
	// Rebuild without empty messages.
	if latestPageCtx > 0 {
		cleaned := make([]llm.Message, 0, len(messages))
		for _, m := range messages {
			if m.Content != "" || m.Role != llm.RoleUser {
				cleaned = append(cleaned, m)
			}
		}
		messages = cleaned
		total = len(messages)
		if total <= maxRecent+1 {
			return messages
		}
	}

	// Mark anchor indices: messages with [ANCHOR] prefix survive pruning.
	// These are synthetic summaries injected after key tool calls.
	anchors := map[int]bool{}
	middle := messages[1 : total-maxRecent]
	for i, msg := range middle {
		if msg.Role == llm.RoleUser && strings.HasPrefix(msg.Content, "[ANCHOR]") {
			anchors[i+1] = true // +1 because middle starts at index 1
		}
	}

	// No need to keep assistant messages before anchors — [ANCHOR] messages
	// are self-contained user messages, not tool results.
	anchorPairs := map[int]bool{}
	for idx := range anchors {
		anchorPairs[idx] = true
	}

	// Dynamic anchor budget — more tables/endpoints need more context preserved.
	maxAnchors := 14 + len(anchors)/3
	if maxAnchors > 24 {
		maxAnchors = 24
	}
	if len(anchorPairs) > maxAnchors {
		sorted := make([]int, 0, len(anchorPairs))
		for idx := range anchorPairs {
			sorted = append(sorted, idx)
		}
		sort.Ints(sorted)
		trimmed := make(map[int]bool, maxAnchors)
		for _, idx := range sorted[:maxAnchors] {
			trimmed[idx] = true
		}
		anchorPairs = trimmed
	}

	recentStart := total - maxRecent
	// Don't split tool call/result groups — the API requires every tool_use
	// to have a matching tool_result. Back up to the nearest non-tool message.
	for recentStart > 1 && messages[recentStart].Role == llm.RoleTool {
		recentStart--
	}

	pruned := make([]llm.Message, 0, 1+len(anchorPairs)+maxRecent+4)
	pruned = append(pruned, messages[0]) // first user message

	// Build summary zone: collapse non-anchor middle messages into compact summaries.
	// This preserves awareness of what was built without consuming many tokens.
	var summaryParts []string
	for idx := 1; idx < recentStart; idx++ {
		if anchorPairs[idx] {
			// Flush any pending summary before the anchor.
			if len(summaryParts) > 0 {
				pruned = append(pruned, llm.Message{
					Role:    llm.RoleUser,
					Content: "[Progress] " + strings.Join(summaryParts, ", "),
				})
				summaryParts = nil
			}
			pruned = append(pruned, messages[idx])
			continue
		}

		// Summarize tool calls from assistant messages.
		msg := messages[idx]
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				summary := summarizeToolCall(tc.Name, tc.Arguments)
				if summary != "" {
					summaryParts = append(summaryParts, summary)
				}
			}
		}
		// Skip tool result and other messages — they're captured by the tool call summary.
	}
	// Flush remaining summary.
	if len(summaryParts) > 0 {
		pruned = append(pruned, llm.Message{
			Role:    llm.RoleUser,
			Content: "[Progress] " + strings.Join(summaryParts, ", "),
		})
	}

	// Add the recent window.
	pruned = append(pruned, messages[recentStart:]...)
	return pruned
}

// summarizeToolCall creates a one-line summary of a tool call for the progress zone.
func summarizeToolCall(toolName, argsJSON string) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}

	action, _ := args["action"].(string)
	switch toolName {
	case "manage_pages":
		path, _ := args["path"].(string)
		if action == "save" && path != "" {
			return "built page " + path
		}
	case "manage_schema":
		name, _ := args["table_name"].(string)
		if action == "create" && name != "" {
			return "created table " + name
		}
	case "manage_endpoints":
		path, _ := args["path"].(string)
		if strings.HasPrefix(action, "create_") && path != "" {
			return action + " /api/" + path
		}
	case "manage_files":
		fn, _ := args["filename"].(string)
		if fn != "" {
			return "saved " + fn
		}
	case "manage_layout":
		if action == "save" {
			return "saved layout"
		}
	case "manage_data":
		table, _ := args["table_name"].(string)
		if action == "insert" && table != "" {
			return "seeded " + table
		}
	}
	return ""
}

// buildProgressTracker tracks which plan items have been completed during BUILD.
type buildProgressTracker struct {
	plan                *Plan
	tablesTotal         int
	pagesTotal          int
	endpointsTotal      int
	tablesDone          map[string]bool
	pagesDone           map[string]bool
	endpointsDone       map[string]bool
	toolCallsSeen       int
	nudgeSent           bool
	postCompletionIters int
	readOnlyGraceIters  int  // consecutive grace iterations with only read tool calls
	checkpointed        bool // true after infrastructure checkpoint is saved
}

func newBuildProgressTracker(plan *Plan) *buildProgressTracker {
	return &buildProgressTracker{
		plan:           plan,
		tablesTotal:    len(plan.Tables),
		pagesTotal:     len(plan.Pages),
		endpointsTotal: len(plan.Endpoints),
		tablesDone:     make(map[string]bool),
		pagesDone:      make(map[string]bool),
		endpointsDone:  make(map[string]bool),
	}
}

// remainingItems returns a human-readable list of plan items not yet completed.
// Returns empty string if everything is done.
func (bp *buildProgressTracker) remainingItems() string {
	var items []string
	for _, t := range bp.plan.Tables {
		if !bp.tablesDone[t.Name] {
			items = append(items, "table: "+t.Name)
		}
	}
	for _, ep := range bp.plan.Endpoints {
		key := ep.Action + ":" + ep.Path
		if !bp.endpointsDone[key] {
			items = append(items, "endpoint: "+ep.Action+" "+ep.Path)
		}
	}
	for _, pg := range bp.plan.Pages {
		if !bp.pagesDone[pg.Path] {
			items = append(items, "page: "+pg.Path+" ("+pg.Title+")")
		}
	}
	if len(items) == 0 {
		return ""
	}
	return "REMINDER — these plan items are still missing. Create them now:\n- " + strings.Join(items, "\n- ")
}

// allComplete returns true when every plan item (tables, endpoints, pages) has been built.
func (bp *buildProgressTracker) allComplete() bool {
	return len(bp.tablesDone) >= bp.tablesTotal &&
		len(bp.endpointsDone) >= bp.endpointsTotal &&
		len(bp.pagesDone) >= bp.pagesTotal
}

// trackToolResult inspects a successful tool call and marks plan items as done.
func (bp *buildProgressTracker) trackToolResult(toolName string, args map[string]interface{}) {
	bp.toolCallsSeen++
	switch toolName {
	case "manage_schema":
		if action, _ := args["action"].(string); action == "create" {
			if name, _ := args["table_name"].(string); name != "" {
				bp.tablesDone[name] = true
			}
		}
	case "manage_pages":
		if action, _ := args["action"].(string); action == "save" || action == "" {
			if path, _ := args["path"].(string); path != "" {
				bp.pagesDone[path] = true
			}
		}
	case "manage_endpoints":
		if action, _ := args["action"].(string); strings.HasPrefix(action, "create_") {
			if path, _ := args["path"].(string); path != "" {
				bp.endpointsDone[action+":"+path] = true
			}
		}
	}
}

func (bp *buildProgressTracker) toPayload() map[string]interface{} {
	return map[string]interface{}{
		"tables_done":     len(bp.tablesDone),
		"tables_total":    bp.tablesTotal,
		"pages_done":      len(bp.pagesDone),
		"pages_total":     bp.pagesTotal,
		"endpoints_done":  len(bp.endpointsDone),
		"endpoints_total": bp.endpointsTotal,
		"tool_calls":      bp.toolCallsSeen,
	}
}

// infrastructureComplete returns true when all tables and endpoints are done
// but at least one page remains. This marks the boundary between the infrastructure
// phase and the page-building phase — the ideal checkpoint moment.
func (bp *buildProgressTracker) infrastructureComplete() bool {
	tablesReady := bp.tablesTotal == 0 || len(bp.tablesDone) >= bp.tablesTotal
	endpointsReady := bp.endpointsTotal == 0 || len(bp.endpointsDone) >= bp.endpointsTotal
	pagesRemain := len(bp.pagesDone) < bp.pagesTotal
	return tablesReady && endpointsReady && pagesRemain
}

// --- Build cost tracker ---

// buildCostTracker accumulates token usage across all stages of a build and
// fires alerts when configurable thresholds are crossed.
type buildCostTracker struct {
	totalTokens int
	alertedAt   map[int]bool // thresholds already alerted
}

// Token thresholds for owner alerts.
var costAlertThresholds = []int{500_000, 1_000_000, 2_000_000}

func newBuildCostTracker() *buildCostTracker {
	return &buildCostTracker{alertedAt: make(map[int]bool)}
}

// addTokens records token usage and returns a message if a threshold was crossed.
func (ct *buildCostTracker) addTokens(tokens int) string {
	ct.totalTokens += tokens
	for _, threshold := range costAlertThresholds {
		if ct.totalTokens >= threshold && !ct.alertedAt[threshold] {
			ct.alertedAt[threshold] = true
			label := fmt.Sprintf("%dK", threshold/1000)
			if threshold >= 1_000_000 {
				label = fmt.Sprintf("%.1fM", float64(threshold)/1_000_000)
			}
			return fmt.Sprintf("Token usage alert: this build has used %s tokens so far (%d total). Large builds may incur significant API costs.",
				label, ct.totalTokens)
		}
	}
	return ""
}

// --- Checkpoint helpers ---

// saveCheckpointMessages persists the current conversation state to ho_pipeline_state.
func (w *PipelineWorker) saveCheckpointMessages(messages []llm.Message) {
	data, err := json.Marshal(messages)
	if err != nil {
		w.logger.Warn("failed to serialize checkpoint messages", "error", err)
		return
	}
	if _, err := w.siteDB.ExecWrite(
		"UPDATE ho_pipeline_state SET checkpoint_messages = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
		string(data),
	); err != nil {
		w.logger.Warn("failed to save checkpoint messages", "error", err)
	} else {
		w.logger.Info("checkpoint saved", "messages", len(messages))
	}
}

// loadCheckpointMessages restores conversation state from ho_pipeline_state.
func (w *PipelineWorker) loadCheckpointMessages() []llm.Message {
	var raw sql.NullString
	if err := w.siteDB.Reader().QueryRow(
		"SELECT checkpoint_messages FROM ho_pipeline_state WHERE id = 1",
	).Scan(&raw); err != nil || !raw.Valid || raw.String == "" {
		return nil
	}
	var messages []llm.Message
	if err := json.Unmarshal([]byte(raw.String), &messages); err != nil {
		w.logger.Warn("failed to deserialize checkpoint messages", "error", err)
		return nil
	}
	w.logger.Info("checkpoint restored", "messages", len(messages))
	return messages
}

// persistBuildTokens saves cumulative token count to pipeline state.
func (w *PipelineWorker) persistBuildTokens(tokens int) {
	w.siteDB.ExecWrite(
		"UPDATE ho_pipeline_state SET total_build_tokens = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
		tokens,
	)
}

// fileTypeLabel returns a human-readable label for a filename based on its extension.
func fileTypeLabel(filename string) string {
	ext := ""
	if i := strings.LastIndex(filename, "."); i >= 0 {
		ext = strings.ToLower(filename[i+1:])
	}
	switch ext {
	case "js", "mjs", "ts", "jsx", "tsx":
		return "script"
	case "css", "scss", "sass", "less":
		return "stylesheet"
	case "json", "xml", "yml", "yaml", "toml":
		return "data file"
	case "svg":
		return "SVG"
	case "png", "jpg", "jpeg", "gif", "webp", "ico", "bmp", "avif":
		return "image"
	case "woff", "woff2", "ttf", "otf", "eot":
		return "font"
	case "mp4", "webm", "ogg", "mov":
		return "video"
	case "mp3", "wav", "flac", "aac", "m4a":
		return "audio"
	case "pdf":
		return "document"
	default:
		return "file"
	}
}

// toolProgressMessage returns a human-readable message for successful write operations.
func toolProgressMessage(toolName string, args map[string]interface{}) string {
	action, _ := args["action"].(string)
	switch {
	case toolName == "manage_files" && action == "save":
		if f, _ := args["filename"].(string); f != "" {
			return fmt.Sprintf("Created **%s** (%s)", f, fileTypeLabel(f))
		}
	case toolName == "manage_files" && action == "delete":
		if f, _ := args["filename"].(string); f != "" {
			return fmt.Sprintf("Deleted **%s**", f)
		}
	case toolName == "manage_layout" && action == "save":
		name, _ := args["name"].(string)
		if name == "" {
			name = "default"
		}
		return fmt.Sprintf("Saved layout: **%s**", name)
	case toolName == "manage_schema" && action == "create":
		if t, _ := args["table_name"].(string); t != "" {
			return fmt.Sprintf("Created table: **%s**", t)
		}
	case toolName == "manage_endpoints" && strings.HasPrefix(action, "create_"):
		if p, _ := args["path"].(string); p != "" {
			return fmt.Sprintf("Created endpoint: **/api/%s** (%s)", p, action)
		}
	}
	return ""
}

// executeNativeAction runs a scheduled task directly in Go without LLM involvement.
// Supported action types: delete_stale, count_rows, truncate, sql.
func (w *PipelineWorker) executeNativeAction(ctx context.Context, actionJSON string, runID int64, taskID int) {
	start := time.Now()

	var action struct {
		Type           string        `json:"type"`
		Table          string        `json:"table"`
		Column         string        `json:"column"`
		Age            string        `json:"age"`
		Query          string        `json:"query"`
		Params         []interface{} `json:"params"`
		URL            string        `json:"url"`
		Method         string        `json:"method"`
		ExpectedStatus int           `json:"expected_status"`
	}
	if err := json.Unmarshal([]byte(actionJSON), &action); err != nil {
		w.logger.Error("native action: invalid JSON", "error", err)
		w.finalizeTaskRun(runID, taskID, false, fmt.Sprintf("invalid native_action JSON: %v", err))
		return
	}

	var result string
	var actionErr error

	switch action.Type {
	case "delete_stale":
		if action.Table == "" || action.Age == "" {
			actionErr = fmt.Errorf("delete_stale requires table and age")
			break
		}
		col := action.Column
		if col == "" {
			col = "created_at"
		}
		// Sanitize table and column names (alphanumeric + underscore only).
		if !isSafeName(action.Table) || !isSafeName(col) {
			actionErr = fmt.Errorf("invalid table or column name")
			break
		}
		query := fmt.Sprintf("DELETE FROM %s WHERE %s < datetime('now', ?)", action.Table, col)
		res, err := w.siteDB.ExecWrite(query, "-"+action.Age)
		if err != nil {
			actionErr = fmt.Errorf("delete_stale: %w", err)
			break
		}
		affected, _ := res.RowsAffected()
		result = fmt.Sprintf("deleted %d rows from %s older than %s", affected, action.Table, action.Age)

	case "count_rows":
		if action.Table == "" {
			actionErr = fmt.Errorf("count_rows requires table")
			break
		}
		if !isSafeName(action.Table) {
			actionErr = fmt.Errorf("invalid table name")
			break
		}
		var count int
		err := w.siteDB.Reader().QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", action.Table)).Scan(&count)
		if err != nil {
			actionErr = fmt.Errorf("count_rows: %w", err)
			break
		}
		result = fmt.Sprintf("table %s has %d rows", action.Table, count)

	case "truncate":
		if action.Table == "" {
			actionErr = fmt.Errorf("truncate requires table")
			break
		}
		if !isSafeName(action.Table) {
			actionErr = fmt.Errorf("invalid table name")
			break
		}
		res, err := w.siteDB.ExecWrite(fmt.Sprintf("DELETE FROM %s", action.Table))
		if err != nil {
			actionErr = fmt.Errorf("truncate: %w", err)
			break
		}
		affected, _ := res.RowsAffected()
		result = fmt.Sprintf("truncated %s: %d rows deleted", action.Table, affected)

	case "sql":
		if action.Query == "" {
			actionErr = fmt.Errorf("sql requires query")
			break
		}
		q := strings.TrimSpace(action.Query)
		upper := strings.ToUpper(q)
		if strings.HasPrefix(upper, "SELECT") {
			rows, err := w.siteDB.Reader().Query(q, action.Params...)
			if err != nil {
				actionErr = fmt.Errorf("sql query: %w", err)
				break
			}
			defer rows.Close()
			cols, _ := rows.Columns()
			var count int
			for rows.Next() {
				count++
			}
			result = fmt.Sprintf("query returned %d rows (%d columns)", count, len(cols))
		} else if strings.HasPrefix(upper, "DELETE") || strings.HasPrefix(upper, "UPDATE") || strings.HasPrefix(upper, "INSERT") {
			res, err := w.siteDB.ExecWrite(q, action.Params...)
			if err != nil {
				actionErr = fmt.Errorf("sql exec: %w", err)
				break
			}
			affected, _ := res.RowsAffected()
			result = fmt.Sprintf("sql executed: %d rows affected", affected)
		} else {
			actionErr = fmt.Errorf("sql action only supports SELECT, INSERT, UPDATE, DELETE")
		}

	case "trigger_event":
		// Publish an event to the bus so actions subscribed to that event_type fire.
		// Config: {"type":"trigger_event", "event_type":"scheduled.cleanup", "payload":{"task":"cleanup"}}
		var triggerCfg struct {
			Type      string                 `json:"type"`
			EventType string                 `json:"event_type"`
			Payload   map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(actionJSON), &triggerCfg); err != nil {
			actionErr = fmt.Errorf("trigger_event: invalid config: %w", err)
			break
		}
		if triggerCfg.EventType == "" {
			actionErr = fmt.Errorf("trigger_event: event_type is required")
			break
		}
		if triggerCfg.Payload == nil {
			triggerCfg.Payload = map[string]interface{}{}
		}
		triggerCfg.Payload["source"] = "scheduler"
		triggerCfg.Payload["task_id"] = taskID
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventType(triggerCfg.EventType), w.siteID, triggerCfg.Payload))
		}
		result = fmt.Sprintf("published event '%s'", triggerCfg.EventType)

	default:
		actionErr = fmt.Errorf("unknown native action type: %s", action.Type)
	}

	duration := time.Since(start)
	if actionErr != nil {
		w.logger.Error("native action failed", "type", action.Type, "error", actionErr, "duration", duration)
		w.finalizeTaskRun(runID, taskID, false, actionErr.Error())
		return
	}

	w.logger.Info("native action completed", "type", action.Type, "result", result, "duration", duration)
	w.logBrainEvent("scheduled_task_native", result, action.Type, 0, "", duration.Milliseconds())
	w.finalizeTaskRun(runID, taskID, true, "")
}

// isSafeName checks that a table/column name contains only alphanumeric chars and underscores.
func isSafeName(name string) bool {
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return len(name) > 0
}

// executeScheduledTask runs a scheduled task with a custom prompt.
func (w *PipelineWorker) executeScheduledTask(ctx context.Context, prompt string, runID int64, taskID int) {
	provider, modelID, err := w.getProvider()
	if err != nil {
		w.logger.Error("scheduled task: provider error", "error", err)
		w.finalizeTaskRun(runID, taskID, false, fmt.Sprintf("provider error: %v", err))
		return
	}

	cfg := ScheduledTaskConfig
	toolGuide := cfg.BuildGuide(w.deps.ToolRegistry, nil)
	systemPrompt := buildScheduledTaskPrompt(w.deps.DB.DB, w.siteDB.Reader(), w.siteID, toolGuide, prompt, w.ownerName())
	messages := []llm.Message{{Role: llm.RoleUser, Content: prompt}}
	toolDefs := cfg.BuildToolDefs(w.deps.ToolRegistry, nil)
	start := time.Now()

	lastContent, lastModel, totalTokens, _, iterErr := w.runToolLoop(ctx, provider, modelID, systemPrompt, messages, toolDefs, cfg.MaxIterations, cfg.MaxTokens, cfg.Temperature)
	if iterErr != nil {
		w.finalizeTaskRun(runID, taskID, false, fmt.Sprintf("LLM error: %v", iterErr))
		return
	}

	w.logBrainEvent("scheduled_task", lastContent, prompt, totalTokens, lastModel, time.Since(start).Milliseconds())
	w.finalizeTaskRun(runID, taskID, true, "")
}

// handleChatWake runs a targeted LLM call with write tools in response to
// a user chat message during monitoring. This lets the brain fix issues
// the owner reports without restarting the full pipeline.
func (w *PipelineWorker) handleChatWake(ctx context.Context, userMessage string) {
	start := time.Now()

	// Acquire semaphore to prevent concurrent execution with monitoring ticks.
	select {
	case w.semaphore <- struct{}{}:
		defer func() { <-w.semaphore }()
	case <-ctx.Done():
		return
	}

	w.publishBrainMessage("Working on your request...")

	provider, modelID, err := w.getProvider()
	if err != nil {
		w.logger.Error("chat-wake: provider error", "error", err)
		return
	}

	cfg := ChatWakeConfig
	site, _ := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	plan, _ := w.loadPlan()
	prompt := buildChatWakePrompt(site, w.siteDB.Reader(), userMessage, plan, w.ownerName())

	// Load recent conversation history so the LLM has context about prior exchanges.
	recentHistory, _ := chat.LoadHistory(w.siteDB.Reader(), "admin", 3)
	messages := make([]llm.Message, 0, len(recentHistory)+1)
	for _, msg := range recentHistory {
		// Only include user and assistant text messages (skip tool call/result noise).
		if (msg.Role == llm.RoleUser || msg.Role == llm.RoleAssistant) && msg.Content != "" {
			messages = append(messages, msg)
		}
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: userMessage})
	toolDefs := cfg.BuildToolDefs(w.deps.ToolRegistry, nil)

	w.maxToolResultOverride = 4000
	_, lastModel, totalTokens, _, iterErr := w.runToolLoop(ctx, provider, modelID, prompt, messages, toolDefs, cfg.MaxIterations, cfg.MaxTokens, cfg.Temperature)
	w.maxToolResultOverride = 0
	if iterErr != nil {
		w.logger.Error("chat-wake: tool loop error", "error", iterErr)
	}

	w.logBrainEvent("chat_wake", "Responded to owner chat message", userMessage, totalTokens, lastModel, time.Since(start).Milliseconds())
}

// --- Provider resolution ---

func (w *PipelineWorker) getProvider() (llm.Provider, string, error) {
	model, providerRow, err := models.GetModelForSite(w.deps.DB.DB, w.siteID)
	if err != nil {
		w.logger.Warn("site has no valid model, falling back to default", "error", err)
		model, providerRow, err = models.GetDefaultModel(w.deps.DB.DB)
		if err != nil {
			return nil, "", fmt.Errorf("no model configured and no default available: %w", err)
		}
	}

	p, modelID, err := w.resolveProvider(model, providerRow)
	if err != nil {
		return nil, "", err
	}

	// Store the current provider ID for fallback lookup.
	w.currentProviderID = providerRow.ID
	return p, modelID, nil
}

// resolveProvider turns a model+provider row into a live llm.Provider.
func (w *PipelineWorker) resolveProvider(model *models.LLMModel, providerRow *models.LLMProvider) (llm.Provider, string, error) {
	var apiKey string
	var err error
	if providerRow.APIKeyEncrypted != nil && *providerRow.APIKeyEncrypted != "" {
		apiKey, err = w.deps.Encryptor.Decrypt(*providerRow.APIKeyEncrypted)
		if err != nil {
			return nil, "", fmt.Errorf("failed to decrypt API key for %q: %w", providerRow.Name, err)
		}
	} else if providerRow.RequiresAPIKey() {
		return nil, "", fmt.Errorf("provider %q has no API key", providerRow.Name)
	}

	var baseURL string
	if providerRow.BaseURL != nil {
		baseURL = *providerRow.BaseURL
	}

	if providerRow.ProviderType == "openai" && baseURL == "" {
		return nil, "", fmt.Errorf("provider %q (openai) has no base_url", providerRow.Name)
	}

	if w.deps.ProviderFactory != nil {
		p := w.deps.ProviderFactory(providerRow.Name, providerRow.ProviderType, apiKey, baseURL)
		if p != nil {
			return p, model.ModelID, nil
		}
	}

	p, err := w.deps.LLMRegistry.Get(providerRow.Name)
	if err != nil {
		return nil, "", fmt.Errorf("provider %q not available: %w", providerRow.Name, err)
	}
	return p, model.ModelID, nil
}

// getFallbackProvider returns an alternative provider on a different LLM provider
// than the current one. Returns nil if no fallback is available.
func (w *PipelineWorker) getFallbackProvider() (llm.Provider, string, error) {
	if w.currentProviderID == 0 {
		return nil, "", fmt.Errorf("no current provider to fall back from")
	}
	model, providerRow, err := models.GetFallbackModel(w.deps.DB.DB, w.currentProviderID)
	if err != nil {
		return nil, "", fmt.Errorf("no fallback provider available: %w", err)
	}
	return w.resolveProvider(model, providerRow)
}

// --- Persistence helpers ---

func (w *PipelineWorker) saveChatMessage(msg llm.Message) {
	var toolCallsJSON *string
	if len(msg.ToolCalls) > 0 {
		data, err := json.Marshal(msg.ToolCalls)
		if err == nil {
			s := string(data)
			toolCallsJSON = &s
		}
	}

	var toolCallID *string
	if msg.ToolCallID != "" {
		toolCallID = &msg.ToolCallID
	}

	_, err := w.siteDB.ExecWrite(
		`INSERT INTO ho_chat_messages (session_id, role, content, tool_calls, tool_call_id) VALUES ('brain', ?, ?, ?, ?)`,
		string(msg.Role), msg.Content, toolCallsJSON, toolCallID,
	)
	if err != nil {
		w.logger.Error("failed to save chat message", "error", err)
	}
}

// saveChatMessageOnce saves a user message only if an identical one hasn't been
// saved recently (within 30 minutes). Prevents duplicate messages on stage retries.
func (w *PipelineWorker) saveChatMessageOnce(msg llm.Message) {
	if msg.Role == llm.RoleUser {
		var exists int
		w.siteDB.Reader().QueryRow(
			"SELECT COUNT(*) FROM ho_chat_messages WHERE session_id = 'brain' AND role = 'user' AND content = ? AND created_at > datetime('now', '-30 minutes')",
			msg.Content,
		).Scan(&exists)
		if exists > 0 {
			return
		}
	}
	w.saveChatMessage(msg)
}

func (w *PipelineWorker) logBrainEvent(eventType, summary, details string, tokens int, model string, durationMs int64) {
	_, err := w.siteDB.ExecWrite(
		"INSERT INTO ho_brain_log (event_type, summary, details, tokens_used, model, duration_ms) VALUES (?, ?, ?, ?, ?, ?)",
		eventType, summary, details, tokens, model, durationMs,
	)
	if err != nil {
		w.logger.Error("failed to log brain event", "error", err)
	}
}

func (w *PipelineWorker) setState(s BrainState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = s
}

func (w *PipelineWorker) publishBrainMessage(content string) {
	w.saveChatMessage(llm.Message{Role: llm.RoleAssistant, Content: content})
	if w.deps.Bus != nil {
		w.deps.Bus.Publish(events.NewEvent(events.EventBrainMessage, w.siteID, map[string]interface{}{
			"session_id": "brain",
			"role":       "assistant",
			"content":    content,
		}))
	}
}

func (w *PipelineWorker) publishBrainError(errMsg string) {
	w.publishBrainMessage("**Pipeline Error:** " + errMsg)
	if w.deps.Bus != nil {
		w.deps.Bus.Publish(events.NewEvent(events.EventBrainError, w.siteID, map[string]interface{}{
			"error": errMsg,
		}))
	}
}

func (w *PipelineWorker) finalizeTaskRun(runID int64, taskID int, success bool, errMsg string) {
	if runID <= 0 {
		return
	}
	if success {
		w.siteDB.ExecWrite("UPDATE ho_task_runs SET status = 'completed', completed_at = CURRENT_TIMESTAMP WHERE id = ?", runID)
		if taskID > 0 {
			w.siteDB.ExecWrite("UPDATE ho_scheduled_tasks SET run_count = run_count + 1 WHERE id = ?", taskID)
		}
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventScheduledCompleted, w.siteID, map[string]interface{}{
				"task_id": taskID,
				"run_id":  runID,
			}))
		}
	} else {
		w.siteDB.ExecWrite("UPDATE ho_task_runs SET status = 'failed', error_message = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?", errMsg, runID)
		if taskID > 0 {
			w.siteDB.ExecWrite("UPDATE ho_scheduled_tasks SET error_count = error_count + 1 WHERE id = ?", taskID)
		}
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventScheduledFailed, w.siteID, map[string]interface{}{
				"task_id": taskID,
				"run_id":  runID,
				"error":   errMsg,
			}))
		}
	}
}

// --- Utility functions ---

// isPermanentError returns true for errors that can never succeed on retry,
// such as missing API keys or invalid provider configuration.
func isPermanentError(err error) bool {
	msg := strings.ToLower(err.Error())
	permanentPatterns := []string{
		"no api key", "no model configured", "provider not available",
		"failed to decrypt", "has no api key", "has no base_url",
	}
	for _, p := range permanentPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	// Connection errors (DNS, refused, TLS) — endpoint unreachable, retrying won't help.
	// But timeouts are transient and should be retried.
	if strings.Contains(msg, "send request:") && !strings.Contains(msg, "api error") &&
		!strings.Contains(msg, "timeout") && !strings.Contains(msg, "context deadline exceeded") {
		return true
	}
	return false
}

func isInteractiveTool(name string) bool {
	switch name {
	case "manage_communication", "manage_pages",
		"manage_files", "manage_layout", "manage_schema",
		"manage_data", "manage_endpoints":
		return true
	}
	return false
}

// payloadInt64 extracts an int64 from a payload map, handling int64, float64,
// and int types (JSON numbers deserialize as float64 in Go's interface{}).
func payloadInt64(payload map[string]interface{}, key string) int64 {
	if v, ok := payload[key].(int64); ok {
		return v
	}
	if v, ok := payload[key].(float64); ok {
		return int64(v)
	}
	if v, ok := payload[key].(int); ok {
		return int64(v)
	}
	if v, ok := payload[key].(string); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Truncate at rune boundary to avoid splitting multi-byte UTF-8 characters.
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func (w *PipelineWorker) truncateToolResult(toolName string, result string) string {
	maxSize := 6000
	if tool, err := w.deps.ToolRegistry.Get(toolName); err == nil {
		if sizer, ok := tool.(tools.ResultSizer); ok {
			maxSize = sizer.MaxResultSize()
		}
	}
	if w.maxToolResultOverride > 0 && w.maxToolResultOverride < maxSize {
		maxSize = w.maxToolResultOverride
	}
	return truncate(result, maxSize)
}

func (w *PipelineWorker) summarizeToolResult(toolName string, result string) string {
	if tool, err := w.deps.ToolRegistry.Get(toolName); err == nil {
		if summarizer, ok := tool.(tools.Summarizer); ok {
			return summarizer.Summarize(result)
		}
	}
	return tools.GenericSummarize(result)
}
