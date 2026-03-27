/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
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
	StageExpand:     2 * time.Minute,
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
	lastPageTables    string            // table names for the last built page (for diff detection)
	layoutSummary     string            // compact layout structure reminder (header/footer/nav presence)

	// Quality & consistency features.
	componentGroups string            // CSS component families (card: .card, .card-header, ...)
	pageStructures  map[string]string // path -> compact HTML structure skeleton
	designNotes     []string          // brief structural notes per page ("/ : hero, feature grid, CTA")
	pagesBuiltCount int               // pages built so far (for aesthetic re-anchoring cadence)

	// Dedup set: tracks anchor content already injected to avoid O(n) scans.
	seenAnchors map[string]bool
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
			StageExpand:     w.runExpand,
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
			// If the tick paused for owner input, exit to the paused loop.
			if w.State() == StatePaused {
				w.runPausedLoop(ctx)
				return
			}
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

func (w *PipelineWorker) setState(s BrainState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = s
}
