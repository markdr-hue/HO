/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/prompt"
	"github.com/markdr-hue/HO/tools"
)

// endpointActionTables maps plan endpoint actions to their database table names.
var endpointActionTables = map[string]string{
	"create_api":       "ho_api_endpoints",
	"create_auth":      "ho_auth_endpoints",
	"create_websocket": "ho_ws_endpoints",
	"create_stream":    "ho_stream_endpoints",
	"create_upload":    "ho_upload_endpoints",
	"create_llm":       "ho_llm_endpoints",
}

// buildToolSetForPlan computes a plan-specific tool set so the system prompt
// and API tool definitions only include tools the plan actually needs.
// Core tools (pages, files, layout, schema, endpoints, data) are always included.
// Optional tools (actions, webhooks, search, media, etc.) are added only when
// the plan references them. This avoids sending ~4K tokens of unused tool docs per call.
func buildToolSetForPlan(plan *Plan) map[string]bool {
	ts := map[string]bool{
		"manage_pages":         true,
		"manage_files":         true,
		"manage_layout":        true,
		"manage_schema":        true,
		"manage_endpoints":     true,
		"manage_data":          true,
		"manage_communication": true, // always available for owner comms
		"manage_secrets":       true, // cheap; needed for API keys, email, payments
		"manage_providers":     true, // cheap; needed for email, payment, etc.
		"manage_testing":       true, // self-testing for built endpoints/pages
		"manage_plan":          true, // mid-build plan amendments
		"make_http_request":    true, // cheap read-only; needed for CDN/API verification
	}
	if len(plan.Actions) > 0 {
		ts["manage_actions"] = true
	}
	if len(plan.Webhooks) > 0 {
		ts["manage_webhooks"] = true
	}
	if len(plan.ScheduledTasks) > 0 {
		ts["manage_scheduler"] = true
	}
	for _, ep := range plan.Endpoints {
		if ep.Action == "create_upload" {
			ts["manage_media"] = true
			ts["manage_blobs"] = true
		}
	}
	// Check for searchable columns → FTS.
	for _, t := range plan.Tables {
		if len(t.SearchableColumns) > 0 {
			ts["manage_search"] = true
			break
		}
	}
	// Check page notes for external API references.
	for _, pg := range plan.Pages {
		lower := strings.ToLower(pg.Notes)
		if strings.Contains(lower, "http request") || strings.Contains(lower, "external api") || strings.Contains(lower, "fetch from") {
			ts["make_http_request"] = true
			break
		}
	}
	// Email when actions include send_email.
	for _, a := range plan.Actions {
		if a.ActionType == "send_email" {
			ts["manage_email"] = true
			ts["manage_providers"] = true
			break
		}
	}
	// Payments when endpoints or page notes suggest payment features.
	for _, ep := range plan.Endpoints {
		lower := strings.ToLower(ep.Path)
		if strings.Contains(lower, "payment") || strings.Contains(lower, "checkout") || strings.Contains(lower, "subscription") || strings.Contains(lower, "stripe") {
			ts["manage_payments"] = true
			ts["manage_providers"] = true
			ts["manage_secrets"] = true
			break
		}
	}
	// Jobs when scheduled tasks exist or plan implies async work.
	if len(plan.ScheduledTasks) > 0 {
		ts["manage_jobs"] = true
	}
	// Components when the plan includes reusable HTML blocks.
	if len(plan.Components) > 0 {
		ts["manage_components"] = true
	}
	// SEO when there are any public pages (even a 1-page site needs meta tags).
	for _, pg := range plan.Pages {
		if !pg.Auth {
			ts["manage_seo"] = true
			break
		}
	}
	return ts
}

// stageRun tracks timing and logging for a pipeline stage.
type stageRun struct {
	w     *PipelineWorker
	start time.Time
	logID int64
	stage PipelineStage
}

func (w *PipelineWorker) beginStage(stage PipelineStage) *stageRun {
	logID, _ := LogStageStart(w.siteDB, stage)
	return &stageRun{w: w, start: time.Now(), logID: logID, stage: stage}
}

func (sr *stageRun) fail(err error) (PipelineStage, error) {
	LogStageError(sr.w.siteDB, sr.logID, err.Error())
	return sr.stage, err
}

func (sr *stageRun) complete(tokens, toolCalls int) {
	LogStageComplete(sr.w.siteDB, sr.logID, tokens, 0, toolCalls, time.Since(sr.start))
}

// llmRetryParse retries an LLM call when JSON parsing fails. It logs a warning,
// publishes a user-facing message, appends the assistant's raw output plus a
// fix-up user message to the conversation, and calls runToolLoop again.
// Returns the raw retry content and additional tokens used.
func (w *PipelineWorker) llmRetryParse(
	ctx context.Context,
	label string, parseErr error, rawContent string,
	retryUserMsg string,
	provider llm.Provider, modelID, prompt string,
	messages []llm.Message, tools []llm.ToolDef,
	maxTokens int, temp float64,
) (string, int, error) {
	w.logger.Warn(label+" JSON parse failed, retrying", "error", parseErr)
	w.publishBrainMessage(label + " response wasn't valid JSON, retrying...")
	// Copy to avoid aliasing the caller's slice backing array.
	retryMsgs := make([]llm.Message, len(messages), len(messages)+2)
	copy(retryMsgs, messages)
	retryMsgs = append(retryMsgs,
		llm.Message{Role: llm.RoleAssistant, Content: rawContent},
		llm.Message{Role: llm.RoleUser, Content: retryUserMsg},
	)
	retryContent, _, tokens, _, err := w.runToolLoop(ctx, provider, modelID, prompt, retryMsgs, tools, 1, maxTokens, temp)
	return retryContent, tokens, err
}

// --- PLAN stage ---

func (w *PipelineWorker) runPlan(ctx context.Context) (PipelineStage, error) {
	sr := w.beginStage(StagePlan)

	provider, modelID, err := w.getProvider()
	if err != nil {
		return sr.fail(err)
	}

	site, err := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	if err != nil {
		return sr.fail(err)
	}

	// Check for answered questions (wake context).
	var answers string
	w.mu.RLock()
	if w.wakeContext != nil {
		if a, ok := w.wakeContext["answer"].(string); ok {
			answers = a
		}
	}
	w.mu.RUnlock()

	// Crash recovery: if pending questions already exist from a previous run,
	// pause again rather than re-asking the same questions.
	if answers == "" {
		var pendingQCount int
		w.siteDB.Reader().QueryRow("SELECT COUNT(*) FROM ho_questions WHERE status = 'pending'").Scan(&pendingQCount)
		if pendingQCount > 0 {
			w.logger.Info("plan stage: pending questions exist from previous run, re-pausing", "count", pendingQCount)
			PausePipeline(w.siteDB, PauseReasonOwnerAnswers)
			sr.complete(0, 0)
			return StagePlan, fmt.Errorf("paused: awaiting owner answers (recovered)")
		}
	}

	cfg := StageConfigs[StagePlan]
	capRef := cfg.BuildGuide(w.deps.ToolRegistry, nil)
	prompt := buildPlanPrompt(site, w.ownerName(), answers, capRef)
	userMsg := "Analyze the site requirements and produce a complete Plan JSON."
	if answers != "" {
		userMsg = fmt.Sprintf("The owner answered your questions: %q\n\nNow produce a complete Plan JSON.", answers)
	}

	w.saveChatMessageOnce(llm.Message{Role: llm.RoleUser, Content: userMsg})

	messages := []llm.Message{{Role: llm.RoleUser, Content: userMsg}}
	toolDefs := cfg.BuildToolDefs(w.deps.ToolRegistry, nil)
	content, _, tokens, _, err := w.runToolLoop(ctx, provider, modelID, prompt, messages, toolDefs, cfg.MaxIterations, cfg.MaxTokens, cfg.Temperature)
	if err != nil {
		return sr.fail(err)
	}

	plan, err := ParsePlan(content)
	if err != nil {
		retryUser := fmt.Sprintf(`Your previous response could not be parsed. Error: %s

Respond with ONLY a raw JSON object — no markdown code fences, no explanation text. Here is the minimum valid structure:

{"app_type": "example", "design_system": {"colors": {"primary": "#6366f1", "bg": "#ffffff"}}, "layout": {"style": "topnav", "header": "full|minimal|none", "footer": "full|minimal|none"}, "pages": [{"path": "/", "title": "Home", "purpose": "Main page", "sections": []}]}

Include all fields from the original instructions. Output the JSON now.`, err.Error())
		retryContent, tokens2, err2 := w.llmRetryParse(ctx, "Plan", err, content, retryUser, provider, modelID, prompt, messages, toolDefs, cfg.MaxTokens, cfg.Temperature)
		tokens += tokens2
		if err2 != nil {
			return sr.fail(err2)
		}
		plan, err = ParsePlan(retryContent)
		if err != nil {
			// Third attempt: try aggressive extraction — strip everything outside the JSON object.
			w.logger.Warn("plan JSON still invalid after retry, attempting aggressive extraction", "error", err)
			aggressive := retryContent
			if idx := strings.Index(aggressive, "{"); idx >= 0 {
				aggressive = aggressive[idx:]
			}
			if idx := strings.LastIndex(aggressive, "}"); idx >= 0 {
				aggressive = aggressive[:idx+1]
			}
			plan, err = ParsePlan(aggressive)
			if err != nil {
				return sr.fail(fmt.Errorf("plan JSON invalid after 3 attempts: %w", err))
			}
		}
	}

	// DESIGN: When the LLM returns questions instead of a full plan, we pause
	// the pipeline by returning (StagePlan, error). runBuildPipeline detects
	// the pause via LoadPipelineState().Paused and enters runPausedLoop instead
	// of treating it as a stage failure. The plan stage re-runs after wake.
	if len(plan.Questions) > 0 {
		w.logger.Info("plan has questions, pausing for owner answers", "count", len(plan.Questions))
		// Mark stale pending questions as superseded.
		if _, err := w.siteDB.ExecWrite("UPDATE ho_questions SET status = 'superseded' WHERE status = 'pending'"); err != nil {
			w.logger.Warn("failed to supersede old questions", "error", err)
		}
		for _, q := range plan.Questions {
			opts := "[]"
			if len(q.Options) > 0 {
				if b, err := json.Marshal(q.Options); err == nil {
					opts = string(b)
				}
			}
			qType := q.Type
			if qType == "" {
				qType = "open"
			}
			var secretName *string
			if q.SecretName != "" {
				secretName = &q.SecretName
			}
			var fieldsStr *string
			if len(q.Fields) > 0 && string(q.Fields) != "null" {
				s := string(q.Fields)
				fieldsStr = &s
			}
			qResult, _ := w.siteDB.ExecWrite(
				"INSERT INTO ho_questions (question, urgency, status, options, type, secret_name, fields) VALUES (?, 'normal', 'pending', ?, ?, ?, ?)",
				q.Question, opts, qType, secretName, fieldsStr,
			)
			qID, _ := qResult.LastInsertId()
			if w.deps.Bus != nil {
				w.deps.Bus.Publish(events.NewEvent(events.EventQuestionAsked, w.siteID, map[string]interface{}{
					"id":       qID,
					"question": q.Question,
					"options":  q.Options,
					"type":     qType,
				}))
			}
		}
		PausePipeline(w.siteDB, PauseReasonOwnerAnswers)
		sr.complete(tokens, 0)
		return StagePlan, fmt.Errorf("paused: awaiting owner answers")
	}

	// Auto-inject missing CRUD endpoints for tables referenced by pages.
	if injected := normalizePlanEndpoints(plan); len(injected) > 0 {
		w.logger.Info("auto-injected missing CRUD endpoints", "paths", injected)
	}

	if errs := ValidatePlan(plan); len(errs) > 0 {
		return sr.fail(fmt.Errorf("plan validation errors: %s", strings.Join(errs, "; ")))
	}

	// Save plan to pipeline state.
	planJSON, _ := marshalToJSON(plan)
	w.siteDB.ExecWrite("UPDATE ho_pipeline_state SET plan_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1", planJSON)

	// Store architecture in site config for public handler.
	configJSON := fmt.Sprintf(`{"architecture":"%s"}`, plan.Architecture)
	if _, err := w.deps.DB.ExecWrite("UPDATE sites SET config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", configJSON, w.siteID); err != nil {
		w.logger.Warn("failed to store architecture in site config", "error", err)
	}

	w.publishBrainMessage(fmt.Sprintf("Plan ready: %s (%s), %d pages, %d endpoints, %d tables",
		plan.AppType, plan.Architecture, len(plan.Pages), len(plan.Endpoints), len(plan.Tables)))
	sr.complete(tokens, 0)

	// Track token cost.
	if w.costTracker != nil {
		if alert := w.costTracker.addTokens(tokens); alert != "" {
			w.publishBrainMessage(alert)
		}
		w.persistBuildTokens(w.costTracker.totalTokens)
	}

	// Clear wake context after successful plan.
	w.mu.Lock()
	w.wakeContext = nil
	w.mu.Unlock()

	return StageBuild, nil
}

// --- BUILD stage ---
// Single continuous LLM tool-calling session that builds everything:
// schema, endpoints, CSS, layout, and all pages in one conversation.

func (w *PipelineWorker) runBuild(ctx context.Context) (PipelineStage, error) {
	sr := w.beginStage(StageBuild)

	provider, modelID, err := w.getProvider()
	if err != nil {
		return sr.fail(err)
	}

	plan, err := w.loadPlan()
	if err != nil {
		return sr.fail(err)
	}

	site, err := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	if err != nil {
		return sr.fail(fmt.Errorf("loading site: %w", err))
	}
	db := w.siteDB.Reader()

	// Check for crash recovery: what already exists?
	existingManifest := buildCrashRecoveryManifest(db)

	cfg := StageConfigs[StageBuild]
	dynTools := buildToolSetForPlan(plan)
	toolGuide := cfg.BuildGuide(w.deps.ToolRegistry, dynTools)
	prompt := buildBuildPrompt(plan, site, w.ownerName(), existingManifest, toolGuide, nil)

	// Include the full plan JSON in the first user message (not the system prompt)
	// so it gets naturally pruned from conversation history after early iterations.
	planJSON, _ := json.MarshalIndent(plan, "", "  ")
	userMsg := fmt.Sprintf("## Plan\n```json\n%s\n```\n\nBuild this site from the plan. Use the recommended build order in the system prompt as a guide. When everything is built, stop.", string(planJSON))
	if existingManifest != "" {
		userMsg = "Resume building. Items already built are listed above under 'Already Built'. Build only the remaining items from the plan."
	}

	w.publishBrainMessage(fmt.Sprintf("Building %s: %d pages, %d endpoints, %d tables...",
		plan.AppType, len(plan.Pages), len(plan.Endpoints), len(plan.Tables)))

	// Dynamic iteration budget based on plan complexity.
	// Extra budget per page for quality review passes (read-back + potential patch).
	maxIter := 30 + len(plan.Pages)*4 + len(plan.Endpoints)*2 + len(plan.Tables)*2
	if maxIter < 50 {
		maxIter = 50
	}
	if maxIter > 180 {
		maxIter = 180
	}

	w.buildProgress = newBuildProgressTracker(plan)
	w.anchors = &anchorStore{
		tableSchemas:   make(map[string]string),
		endpointAPIs:   make(map[string]string),
		pageStructures: make(map[string]string),
	}

	// On crash recovery, try to restore conversation context from checkpoint.
	var messages []llm.Message
	if existingManifest != "" {
		if checkpoint := w.loadCheckpointMessages(); checkpoint != nil {
			messages = checkpoint
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: userMsg})
			w.buildProgress.checkpointed = true
			w.publishBrainMessage("Resumed from checkpoint with conversation context.")
		}
	}
	if messages == nil {
		messages = []llm.Message{{Role: llm.RoleUser, Content: userMsg}}
	}

	// Regenerate the system prompt with current progress on each LLM call.
	// Completed items are marked [DONE] in the checklist so the LLM skips them.
	// Once infrastructure (tables, endpoints) is done, also compact the Build Guide
	// to save ~1,600 tokens per call.
	var promptCompacted bool
	ownerName := w.ownerName()
	w.systemPromptUpdater = func(current string) string {
		if w.buildProgress == nil {
			return current
		}
		if !promptCompacted && w.buildProgress.infrastructureComplete() {
			promptCompacted = true
		}
		if promptCompacted {
			return buildBuildPrompt(plan, site, ownerName, "", toolGuide, w.buildProgress, true)
		}
		return buildBuildPrompt(plan, site, ownerName, "", toolGuide, w.buildProgress)
	}

	toolDefs := cfg.BuildToolDefs(w.deps.ToolRegistry, dynTools)
	_, _, totalTokens, totalToolCalls, loopErr := w.runToolLoop(ctx, provider, modelID, prompt, messages, toolDefs, maxIter, cfg.MaxTokens, cfg.Temperature)

	// If the tool loop was paused for owner input (e.g., secret request),
	// return StageBuild so the pipeline re-enters BUILD after the owner responds.
	// Checkpoint was already saved by runToolLoop before returning.
	if errors.Is(loopErr, ErrPipelinePaused) {
		w.buildProgress = nil
		w.anchors = nil
		w.systemPromptUpdater = nil
		w.publishBrainMessage("Build paused — waiting for owner input.")
		sr.complete(totalTokens, totalToolCalls)
		return StageBuild, fmt.Errorf("paused: awaiting owner answers")
	}

	// Clear checkpoint after successful build (no longer needed).
	w.siteDB.ExecWrite("UPDATE ho_pipeline_state SET checkpoint_messages = NULL WHERE id = 1")

	w.buildProgress = nil // clear after build
	w.anchors = nil
	w.systemPromptUpdater = nil

	if loopErr != nil {
		return sr.fail(loopErr)
	}

	// Track token cost.
	if w.costTracker != nil {
		if alert := w.costTracker.addTokens(totalTokens); alert != "" {
			w.publishBrainMessage(alert)
		}
		w.persistBuildTokens(w.costTracker.totalTokens)
	}

	w.publishBrainMessage(fmt.Sprintf("Build complete: %d tool calls, %s — validating...", totalToolCalls, time.Since(sr.start).Round(time.Second)))
	sr.complete(totalTokens, totalToolCalls)

	return StageValidate, nil
}

// --- VALIDATE stage ---

func (w *PipelineWorker) runValidate(ctx context.Context) (PipelineStage, error) {
	sr := w.beginStage(StageValidate)
	// Use the writer pool for validation reads to ensure visibility of
	// all tables/rows created during BUILD (WAL mode read pool may lag).
	db := w.siteDB.Writer()

	// Load the plan to know what should exist.
	state, err := LoadPipelineState(db)
	if err != nil || state.PlanJSON == "" {
		sr.complete(0, 0)
		return StageComplete, nil // no plan — skip validation
	}
	plan, err := ParsePlan(state.PlanJSON)
	if err != nil {
		sr.complete(0, 0)
		return StageComplete, nil
	}

	issues := validateBuild(db, plan)

	// Functional validation: HTTP-check pages and endpoints.
	if funcIssues := w.functionalValidate(plan); len(funcIssues) > 0 {
		w.publishBrainMessage(fmt.Sprintf("Functional checks found %d issues:\n- %s", len(funcIssues), strings.Join(funcIssues, "\n- ")))
		// Functional issues are informational — append but don't block completion.
		issues = append(issues, funcIssues...)
	}

	if len(issues) == 0 {
		w.publishBrainMessage("Validation passed — all plan items verified.")
		sr.complete(0, 0)
		return StageComplete, nil
	}

	// Report issues and attempt LLM fix-up.
	issueText := strings.Join(issues, "\n- ")
	w.publishBrainMessage(fmt.Sprintf("Validation found %d issues — attempting fixes:\n- %s", len(issues), strings.Join(issues, "\n- ")))
	w.logger.Warn("build validation issues", "count", len(issues), "issues", issueText)

	provider, modelID, err := w.getProvider()
	if err != nil {
		return sr.fail(err)
	}

	cfg := StageConfigs[StageValidate]
	prompt := buildValidatePrompt(db, plan, issueText)
	messages := []llm.Message{{
		Role:    llm.RoleUser,
		Content: fmt.Sprintf("Fix ONLY these %d issues. Read first, patch second, do not rewrite working content:\n- %s", len(issues), issueText),
	}}
	toolDefs := cfg.BuildToolDefs(w.deps.ToolRegistry, nil)
	// Dynamic iteration cap: scale with issue count so complex sites get enough
	// fix-up passes, but cap to prevent the LLM from wandering into rewrites.
	fixIter := len(issues) * 2
	if fixIter < 4 {
		fixIter = 4
	}
	if fixIter > 16 {
		fixIter = 16
	}
	_, _, tokens, toolCalls, loopErr := w.runToolLoop(ctx, provider, modelID, prompt, messages, toolDefs, fixIter, cfg.MaxTokens, cfg.Temperature)
	if loopErr != nil {
		w.logger.Warn("validate fix-up loop failed", "error", loopErr)
	}

	// Refresh CSS reference — validation may have patched the stylesheet.
	w.refreshCSSReference(db)

	// Re-validate and classify remaining issues as critical or cosmetic.
	remaining := validateBuild(db, plan)
	var critical, cosmetic []string
	for _, issue := range remaining {
		if isCriticalIssue(issue) {
			critical = append(critical, issue)
		} else {
			cosmetic = append(cosmetic, issue)
		}
	}

	if len(critical) > 0 {
		w.publishBrainMessage(fmt.Sprintf("Validation: %d critical issues remain — these need attention:\n- %s", len(critical), strings.Join(critical, "\n- ")))
		w.logger.Warn("critical validation issues remain", "count", len(critical))
		sr.complete(tokens, toolCalls)
		// Return error to trigger stage retry — critical issues block completion.
		return StageValidate, fmt.Errorf("critical validation issues remain: %s", strings.Join(critical, "; "))
	}

	if len(cosmetic) > 0 {
		w.publishBrainMessage(fmt.Sprintf("Validation: %d cosmetic issues noted (non-blocking):\n- %s", len(cosmetic), strings.Join(cosmetic, "\n- ")))
	}
	if len(remaining) == 0 {
		w.publishBrainMessage("Validation passed after fix-up.")
	} else {
		w.publishBrainMessage("Validation passed — only cosmetic issues remain.")
	}

	// Self-review: ask the LLM to read the first and last page and fix consistency issues.
	if len(plan.Pages) >= 3 {
		w.runSelfReview(ctx, provider, modelID, plan, db)
		// Refresh CSS reference — self-review may have patched styles.
		w.refreshCSSReference(db)
	}

	sr.complete(tokens, toolCalls)
	return StageComplete, nil
}

// runSelfReview asks the LLM to read the first and last page and fix any
// design consistency issues. This lightweight pass catches problems that
// structural validation misses (mismatched styles, broken layouts, etc.).
func (w *PipelineWorker) runSelfReview(ctx context.Context, provider llm.Provider, modelID string, plan *Plan, db *sql.DB) {
	if len(plan.Pages) < 3 {
		return
	}
	firstPage := plan.Pages[0].Path
	lastPage := plan.Pages[len(plan.Pages)-1].Path

	w.publishBrainMessage("Running design consistency review...")

	reviewPrompt := fmt.Sprintf(`You are HO. The build and validation are done. Do a quick design consistency review:
1. Read the first page (%s) and the last page (%s) using manage_pages(action="get")
2. Read the global CSS using manage_files(action="get")
3. Compare: are they using the same CSS classes, consistent spacing, matching design language?
4. If you find inconsistencies, fix them with targeted patches. If everything looks good, stop.

Be brief. Only fix genuine inconsistencies — do not refactor or add features.`, firstPage, lastPage)

	messages := []llm.Message{{Role: llm.RoleUser, Content: reviewPrompt}}
	valCfg := StageConfigs[StageValidate]
	toolDefs := valCfg.BuildToolDefs(w.deps.ToolRegistry, nil)
	_, _, _, _, err := w.runToolLoop(ctx, provider, modelID, reviewPrompt, messages, toolDefs, 6, valCfg.MaxTokens, valCfg.Temperature)
	if err != nil {
		w.logger.Warn("self-review failed", "error", err)
	}
}

// normalizePlanEndpoints scans page endpoint references and auto-injects
// missing create_api endpoints for tables that pages need CRUD access to.
// Returns the list of paths that were auto-injected.
func normalizePlanEndpoints(plan *Plan) []string {
	// Build a set of table names for quick lookup.
	tableNames := make(map[string]bool, len(plan.Tables))
	for _, t := range plan.Tables {
		tableNames[t.Name] = true
	}

	// Build a set of existing endpoint paths by action type.
	existingAPI := make(map[string]bool)
	for _, ep := range plan.Endpoints {
		if ep.Action == "create_api" {
			existingAPI[ep.Path] = true
		}
	}

	// Collect unique CRUD paths referenced by pages.
	neededCRUD := make(map[string]bool)
	for _, pg := range plan.Pages {
		for _, epRef := range pg.Endpoints {
			// Parse "GET /api/modals", "POST /api/modals", etc.
			path := extractCRUDPath(epRef)
			if path == "" {
				continue
			}
			// Only auto-inject if the path matches a table and no create_api exists.
			if tableNames[path] && !existingAPI[path] {
				neededCRUD[path] = true
			}
		}
		// Also check section-level endpoint refs.
		for _, sec := range pg.Sections {
			for _, epRef := range sec.Endpoints {
				path := extractCRUDPath(epRef)
				if path == "" {
					continue
				}
				if tableNames[path] && !existingAPI[path] {
					neededCRUD[path] = true
				}
			}
		}
	}

	var injected []string
	for path := range neededCRUD {
		plan.Endpoints = append(plan.Endpoints, EndpointSpec{
			Action:    "create_api",
			Path:      path,
			TableName: path,
		})
		injected = append(injected, path)
	}
	return injected
}

// extractCRUDPath extracts the base resource path from a page endpoint reference
// like "GET /api/modals" or "POST /api/modals". Returns "" for non-CRUD refs
// (e.g., "POST /api/chat/complete", "POST /api/foo/chat").
func extractCRUDPath(ref string) string {
	// Normalize: "GET /api/modals?limit=100" → parts
	ref = strings.Split(ref, "?")[0] // strip query params
	parts := strings.Fields(ref)
	if len(parts) < 2 {
		return ""
	}
	method := strings.ToUpper(parts[0])
	urlPath := parts[1]

	// Only consider standard CRUD methods.
	if method != "GET" && method != "POST" && method != "PUT" && method != "DELETE" {
		return ""
	}

	// Strip /api/ prefix.
	urlPath = strings.TrimPrefix(urlPath, "/api/")
	if urlPath == "" {
		return ""
	}

	// Extract base path (first segment only, ignore /id, /chat, /complete, etc.)
	segments := strings.SplitN(urlPath, "/", 2)
	basePath := segments[0]

	// Skip if the base path itself is a known sub-route keyword (e.g. /api/upload/foo
	// is an upload endpoint reference, not a CRUD resource named "upload").
	if basePath == "chat" || basePath == "complete" || basePath == "stream" || basePath == "upload" || basePath == "ws" || basePath == "rooms" {
		return ""
	}

	// Skip if the second segment is a known sub-route (LLM, upload, stream, ws).
	if len(segments) > 1 {
		sub := segments[1]
		if sub == "chat" || sub == "complete" || sub == "stream" || sub == "upload" || sub == "ws" || sub == "rooms" {
			return ""
		}
	}

	return basePath
}

// validateBuild checks that all plan items were actually created in the database.
func validateBuild(db *sql.DB, plan *Plan) []string {
	var issues []string
	issues = append(issues, validateTablesExist(db, plan)...)
	issues = append(issues, validateEndpointsExist(db, plan)...)
	issues = append(issues, validatePagesExist(db, plan)...)
	issues = append(issues, validateAuthStrategy(db, plan)...)
	issues = append(issues, validateGlobalCSS(db)...)
	issues = append(issues, validateLayoutExists(db)...)
	issues = append(issues, validatePageAssets(db)...)
	issues = append(issues, validatePageEndpointJS(db, plan)...)
	issues = append(issues, validateCSSResponsive(db, plan)...)
	issues = append(issues, validateLayoutHead(db)...)
	issues = append(issues, validateNoPlaceholders(db, plan)...)
	issues = append(issues, validateNoConsoleLog(db)...)
	return issues
}

func validateTablesExist(db *sql.DB, plan *Plan) []string {
	var issues []string
	for _, t := range plan.Tables {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?", t.Name).Scan(&count)
		if count == 0 {
			if tools.IsSystemTable(t.Name) {
				issues = append(issues, fmt.Sprintf("Table '%s' collides with a reserved system table — rename it (e.g. 'user_%s' or 'app_%s') and update endpoints/pages that reference it", t.Name, t.Name, t.Name))
			} else {
				issues = append(issues, fmt.Sprintf("Table '%s' not created", t.Name))
			}
		}
	}
	return issues
}

func validateEndpointsExist(db *sql.DB, plan *Plan) []string {
	var issues []string
	for _, ep := range plan.Endpoints {
		found := false
		if tbl, ok := endpointActionTables[ep.Action]; ok {
			var count int
			db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE path = ?", tbl), ep.Path).Scan(&count)
			if count > 0 {
				found = true
			}
		}
		if !found {
			issues = append(issues, fmt.Sprintf("Endpoint '%s %s' not created", ep.Action, ep.Path))
		}
	}

	// Cross-check: verify that endpoints referenced by pages actually exist in the DB.
	// Check all endpoint tables — not just ho_api_endpoints — since pages can
	// reference upload, stream, websocket, auth, and LLM endpoints too.
	endpointTables := []string{
		"ho_api_endpoints",
		"ho_upload_endpoints",
		"ho_auth_endpoints",
		"ho_stream_endpoints",
		"ho_ws_endpoints",
		"ho_llm_endpoints",
	}
	checked := make(map[string]bool)
	for _, pg := range plan.Pages {
		for _, epRef := range pg.Endpoints {
			if checked[epRef] {
				continue
			}
			checked[epRef] = true
			crudPath := extractCRUDPath(epRef)
			if crudPath != "" {
				found := false
				for _, tbl := range endpointTables {
					var count int
					db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE path = ?", tbl), crudPath).Scan(&count)
					if count > 0 {
						found = true
						break
					}
				}
				if !found {
					issues = append(issues, fmt.Sprintf("Page '%s' references '%s' but no endpoint exists for path '%s'", pg.Path, epRef, crudPath))
				}
			}
		}
	}
	return issues
}

func validatePagesExist(db *sql.DB, plan *Plan) []string {
	var issues []string
	for _, pg := range plan.Pages {
		var content sql.NullString
		db.QueryRow("SELECT content FROM ho_pages WHERE path = ? AND is_deleted = 0", pg.Path).Scan(&content)
		if !content.Valid || content.String == "" {
			issues = append(issues, fmt.Sprintf("Page '%s' (%s) missing or empty", pg.Path, pg.Title))
		}
	}
	return issues
}

func validateAuthStrategy(db *sql.DB, plan *Plan) []string {
	if plan.AuthStrategy != "jwt" {
		return nil
	}
	var authCount int
	db.QueryRow("SELECT COUNT(*) FROM ho_auth_endpoints").Scan(&authCount)
	if authCount == 0 {
		return []string{"auth_strategy is 'jwt' but no auth endpoint was created — use manage_endpoints create_auth"}
	}
	return nil
}

func validateGlobalCSS(db *sql.DB) []string {
	var cssCount int
	db.QueryRow("SELECT COUNT(*) FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.css'").Scan(&cssCount)
	if cssCount == 0 {
		return []string{"No global CSS file found"}
	}
	return nil
}

func validateLayoutExists(db *sql.DB) []string {
	var layoutCount int
	db.QueryRow("SELECT COUNT(*) FROM ho_layouts").Scan(&layoutCount)
	if layoutCount == 0 {
		return []string{"No layout created"}
	}
	return nil
}

func validatePageAssets(db *sql.DB) []string {
	// Collect all rows first, then check assets in a separate loop.
	// db has MaxOpenConns=1; querying inside an open cursor deadlocks because
	// QueryRow blocks waiting for the connection held by the rows cursor.
	type pageAssets struct {
		path   string
		assets []string
	}
	var pageAssetList []pageAssets
	rows, err := db.Query("SELECT path, assets FROM ho_pages WHERE is_deleted = 0 AND assets IS NOT NULL AND assets != '' AND assets != '[]'")
	if err == nil {
		for rows.Next() {
			var path, assetsJSON string
			if rows.Scan(&path, &assetsJSON) != nil {
				continue
			}
			var assets []string
			if json.Unmarshal([]byte(assetsJSON), &assets) == nil && len(assets) > 0 {
				pageAssetList = append(pageAssetList, pageAssets{path: path, assets: assets})
			}
		}
		rows.Close()
	}
	var issues []string
	for _, pa := range pageAssetList {
		for _, asset := range pa.assets {
			var aCount int
			db.QueryRow("SELECT COUNT(*) FROM ho_assets WHERE filename = ?", asset).Scan(&aCount)
			if aCount == 0 {
				issues = append(issues, fmt.Sprintf("Page '%s' references asset '%s' which doesn't exist", pa.path, asset))
			}
		}
	}
	return issues
}

func validatePageEndpointJS(db *sql.DB, plan *Plan) []string {
	var issues []string
	for _, pg := range plan.Pages {
		if len(pg.Endpoints) == 0 {
			continue
		}
		var assetsJSON sql.NullString
		db.QueryRow("SELECT assets FROM ho_pages WHERE path = ? AND is_deleted = 0", pg.Path).Scan(&assetsJSON)
		if !assetsJSON.Valid || assetsJSON.String == "" || assetsJSON.String == "[]" {
			issues = append(issues, fmt.Sprintf("Page '%s' lists endpoints but has no JS assets wired", pg.Path))
		}
	}
	return issues
}

func validateCSSResponsive(db *sql.DB, plan *Plan) []string {
	// Chromeless apps (games, canvas, visualizations) may not need responsive breakpoints.
	header, footer := layoutHeaderFooter(plan)
	if header == "none" && footer == "none" {
		return nil
	}
	var globalCSS sql.NullString
	db.QueryRow("SELECT content FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.css' ORDER BY id LIMIT 1").Scan(&globalCSS)
	if globalCSS.Valid && globalCSS.String != "" && !strings.Contains(globalCSS.String, "@media") {
		return []string{"Global CSS has no @media responsive breakpoints"}
	}
	return nil
}

func validateLayoutHead(db *sql.DB) []string {
	var headContent sql.NullString
	db.QueryRow("SELECT head_content FROM ho_layouts WHERE name = 'default'").Scan(&headContent)
	if !headContent.Valid || headContent.String == "" {
		return []string{"Default layout has no head_content (missing fonts, favicon, or CDN links)"}
	}
	return nil
}

func validateNoPlaceholders(db *sql.DB, plan *Plan) []string {
	var issues []string
	for _, pg := range plan.Pages {
		var content sql.NullString
		db.QueryRow("SELECT content FROM ho_pages WHERE path = ? AND is_deleted = 0", pg.Path).Scan(&content)
		if !content.Valid || content.String == "" {
			continue
		}
		lower := strings.ToLower(content.String)
		if strings.Contains(lower, "lorem ipsum") || strings.Contains(lower, "todo:") || strings.Contains(lower, "<!-- todo") || strings.Contains(lower, "placeholder text") {
			issues = append(issues, fmt.Sprintf("Page '%s' contains placeholder text", pg.Path))
		}
	}
	return issues
}

func validateNoConsoleLog(db *sql.DB) []string {
	type jsAsset struct {
		filename string
		content  string
	}
	var jsAssets []jsAsset
	jsRows, jsErr := db.Query("SELECT filename, content FROM ho_assets WHERE filename LIKE '%.js' AND content IS NOT NULL AND content != ''")
	if jsErr == nil {
		for jsRows.Next() {
			var fn, c string
			if jsRows.Scan(&fn, &c) == nil {
				jsAssets = append(jsAssets, jsAsset{filename: fn, content: c})
			}
		}
		jsRows.Close()
	}
	var issues []string
	for _, js := range jsAssets {
		if consoleLogOutsideCatch(js.content) {
			issues = append(issues, fmt.Sprintf("Asset '%s' has console.log outside error handlers — remove for production", js.filename))
		}
	}
	return issues
}

// isCriticalIssue returns true for structural issues that indicate missing functionality.
// Cosmetic issues (inline styles, console.log, hardcoded colors) are non-blocking.
func isCriticalIssue(issue string) bool {
	critical := []string{
		"not created",
		"missing or empty",
		"No global CSS",
		"No layout",
		"no auth endpoint",
		"no head_content",
		"no JS assets wired",
		"doesn't exist", // missing referenced assets
		"no @media",
	}
	lower := strings.ToLower(issue)
	for _, keyword := range critical {
		if strings.Contains(lower, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// hexInHTML detects hardcoded hex color values in HTML attributes (not CSS custom properties).
func hexInHTML(html string) bool {
	// Look for color="# or :#hex patterns in inline attributes, but not inside
	// var(-- references or CSS files. We check for hex in attribute values.
	inAttr := false
	for i := 0; i < len(html)-6; i++ {
		if html[i] == '"' {
			inAttr = !inAttr
			continue
		}
		if inAttr && html[i] == '#' {
			// Check it looks like a hex color: #xxx or #xxxxxx
			hexLen := 0
			for j := i + 1; j < len(html) && j < i+7; j++ {
				c := html[j]
				if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
					hexLen++
				} else {
					break
				}
			}
			if hexLen == 3 || hexLen == 6 {
				// Make sure it's not inside a URL (like picsum.photos or pravatar)
				// by checking the surrounding context.
				start := i - 20
				if start < 0 {
					start = 0
				}
				ctx := strings.ToLower(html[start:i])
				if !strings.Contains(ctx, "http") && !strings.Contains(ctx, "src") && !strings.Contains(ctx, "href") && !strings.Contains(ctx, "url(") {
					return true
				}
			}
		}
	}
	return false
}

// consoleLogOutsideCatch checks if JS content has console.log statements that
// are NOT inside catch blocks or error handlers.
func consoleLogOutsideCatch(js string) bool {
	lines := strings.Split(js, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "console.log") {
			continue
		}
		// Check if this is inside a catch block or error handler by looking
		// at the preceding 3 lines for "catch", ".catch", or "onerror".
		inErrorHandler := false
		for j := i; j >= 0 && j >= i-3; j-- {
			prev := strings.ToLower(strings.TrimSpace(lines[j]))
			if strings.Contains(prev, "catch") || strings.Contains(prev, "onerror") || strings.Contains(prev, "error") {
				inErrorHandler = true
				break
			}
		}
		if !inErrorHandler {
			return true
		}
	}
	return false
}

// functionalValidate makes HTTP requests to built pages and API endpoints to
// verify they actually respond. Returns issues as non-critical/informational.
func (w *PipelineWorker) functionalValidate(plan *Plan) []string {
	port := w.deps.PublicPort
	if port == 0 {
		return nil // can't test without knowing the port
	}

	// Look up the site's domain for the Host header.
	var domain sql.NullString
	w.deps.DB.DB.QueryRow("SELECT domain FROM sites WHERE id = ?", w.siteID).Scan(&domain)
	if !domain.Valid || domain.String == "" {
		return nil // no domain configured, skip functional checks
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var issues []string

	// Check each page returns 200 with content.
	for _, pg := range plan.Pages {
		url := fmt.Sprintf("http://127.0.0.1:%d%s", port, pg.Path)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Host = domain.String
		resp, err := client.Do(req)
		if err != nil {
			issues = append(issues, fmt.Sprintf("Page '%s' failed HTTP check: %v", pg.Path, err))
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			issues = append(issues, fmt.Sprintf("Page '%s' returned HTTP %d (expected 200)", pg.Path, resp.StatusCode))
		} else if len(body) < 50 {
			issues = append(issues, fmt.Sprintf("Page '%s' returned very short response (%d bytes)", pg.Path, len(body)))
		}
	}

	// Check GET API endpoints (skip auth-protected ones).
	for _, ep := range plan.Endpoints {
		if ep.Action == "create_auth" || ep.Action == "create_upload" || ep.Action == "create_websocket" || ep.Action == "create_stream" || ep.Action == "create_llm" {
			continue
		}
		if ep.RequiresAuth && !ep.PublicRead {
			continue
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/api/%s", port, ep.Path)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Host = domain.String
		resp, err := client.Do(req)
		if err != nil {
			issues = append(issues, fmt.Sprintf("Endpoint GET /api/%s failed HTTP check: %v", ep.Path, err))
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			issues = append(issues, fmt.Sprintf("Endpoint GET /api/%s returned HTTP %d", ep.Path, resp.StatusCode))
		}
	}

	return issues
}

// --- COMPLETE stage ---

func (w *PipelineWorker) runComplete(ctx context.Context) (PipelineStage, error) {
	sr := w.beginStage(StageComplete)

	w.logBrainEvent("complete", "Site build completed", "", 0, "", 0)
	w.publishBrainMessage("Site build complete! Switching to monitoring mode.")

	w.deps.DB.ExecWrite("UPDATE sites SET mode = 'monitoring', updated_at = CURRENT_TIMESTAMP WHERE id = ?", w.siteID)
	if w.deps.Bus != nil {
		w.deps.Bus.Publish(events.NewEvent(events.EventBrainModeChanged, w.siteID, map[string]interface{}{
			"site_id": w.siteID,
			"mode":    "monitoring",
		}))
	}

	sr.complete(0, 0)
	return StageMonitoring, nil
}

// --- UPDATE_PLAN stage (incremental) ---

func (w *PipelineWorker) runUpdatePlan(ctx context.Context) (PipelineStage, error) {
	sr := w.beginStage(StageUpdatePlan)

	provider, modelID, err := w.getProvider()
	if err != nil {
		return sr.fail(err)
	}

	existingPlan, _ := w.loadPlan()

	state, _ := LoadPipelineState(w.siteDB.Reader())
	changeDesc := ""
	if state != nil {
		changeDesc = state.UpdateDescription
	}

	cfg := StageConfigs[StageUpdatePlan]
	site, _ := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	capRef := cfg.BuildGuide(w.deps.ToolRegistry, nil)
	prompt := buildUpdatePlanPrompt(existingPlan, w.siteDB.Reader(), site, changeDesc, w.ownerName(), capRef)
	userMsg := "Create a PlanPatch JSON describing the changes needed."
	if changeDesc != "" {
		userMsg = fmt.Sprintf("The owner requested: %s\n\nCreate a PlanPatch JSON describing the changes needed.", changeDesc)
	}
	w.saveChatMessageOnce(llm.Message{Role: llm.RoleUser, Content: userMsg})

	messages := []llm.Message{{Role: llm.RoleUser, Content: userMsg}}
	content, _, tokens, _, err := w.runToolLoop(ctx, provider, modelID, prompt, messages, cfg.BuildToolDefs(w.deps.ToolRegistry, nil), cfg.MaxIterations, cfg.MaxTokens, cfg.Temperature)
	if err != nil {
		return sr.fail(err)
	}

	patch, err := ParsePlanPatch(content)
	if err != nil {
		retryUser := fmt.Sprintf("Your previous response could not be parsed. Error: %s\nRespond with ONLY a raw JSON object. No markdown code fences, no explanation text.", err.Error())
		retryContent, tokens2, err2 := w.llmRetryParse(ctx, "Patch", err, content, retryUser, provider, modelID, prompt, messages, cfg.BuildToolDefs(w.deps.ToolRegistry, nil), cfg.MaxTokens, cfg.Temperature)
		tokens += tokens2
		if err2 != nil {
			return sr.fail(err2)
		}
		patch, err = ParsePlanPatch(retryContent)
		if err != nil {
			return sr.fail(fmt.Errorf("patch JSON still invalid after retry: %w", err))
		}
	}

	if existingPlan == nil {
		return sr.fail(fmt.Errorf("cannot apply patch: no existing plan found"))
	}

	w.siteDB.ExecWrite("UPDATE ho_pipeline_state SET update_description = NULL WHERE id = 1")

	// Mark modified pages as deleted so BUILD recreates them.
	for _, mod := range patch.ModifyPages {
		w.siteDB.ExecWrite("UPDATE ho_pages SET is_deleted = 1 WHERE path = ? AND is_deleted = 0", mod.Path)
	}

	// Delete modified endpoints so BUILD recreates them with updated config.
	for _, mod := range patch.ModifyEndpoints {
		if tbl, ok := endpointActionTables[mod.Action]; ok {
			w.siteDB.ExecWrite(fmt.Sprintf("DELETE FROM %s WHERE path = ?", tbl), mod.Path)
		}
	}

	existingPlan.ApplyPatch(patch)

	planJSON, _ := marshalToJSON(existingPlan)
	w.siteDB.ExecWrite("UPDATE ho_pipeline_state SET plan_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1", planJSON)

	sr.complete(tokens, 0)
	return StageBuild, nil
}

// --- Monitoring tick ---

func (w *PipelineWorker) monitoringTick(ctx context.Context) {
	start := time.Now()

	select {
	case w.semaphore <- struct{}{}:
		defer func() { <-w.semaphore }()
	case <-ctx.Done():
		return
	}

	// Only count application errors, not infrastructure errors (LLM timeouts, rate limits).
	var recentErrors int
	w.siteDB.Reader().QueryRow("SELECT COUNT(*) FROM ho_brain_log WHERE event_type = 'error' AND created_at > datetime('now', '-1 hour') AND details NOT LIKE '%LLM%' AND details NOT LIKE '%timeout%' AND details NOT LIKE '%rate limit%' AND details NOT LIKE '%429%' AND details NOT LIKE '%529%'").Scan(&recentErrors)

	if recentErrors == 0 {
		w.mu.Lock()
		w.idleCheckCount++
		w.mu.Unlock()
		w.logBrainEvent("tick", "Monitoring: healthy", "", 0, "", time.Since(start).Milliseconds())
		return
	}

	// Issues found — reset idle counter.
	w.mu.Lock()
	w.idleCheckCount = 0
	w.mu.Unlock()

	provider, modelID, err := w.getProvider()
	if err != nil {
		w.logger.Error("monitoring: provider error", "error", err)
		return
	}

	cfg := MonitoringConfig
	site, _ := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	plan, _ := w.loadPlan()
	prompt := buildMonitoringPrompt(site, w.siteDB.Reader(), plan, w.ownerName())
	var contextMsg strings.Builder
	contextMsg.WriteString("Check site health. Issues detected:\n")
	contextMsg.WriteString(fmt.Sprintf("- %d recent errors in the last hour\n", recentErrors))

	messages := []llm.Message{{Role: llm.RoleUser, Content: contextMsg.String()}}
	toolDefs := cfg.BuildToolDefs(w.deps.ToolRegistry, nil)

	_, lastModel, totalTokens, _, loopErr := w.runToolLoop(ctx, provider, modelID, prompt, messages, toolDefs, cfg.MaxIterations, cfg.MaxTokens, cfg.Temperature)
	if loopErr != nil {
		w.logger.Error("monitoring tick failed", "error", loopErr)
		return
	}

	w.logBrainEvent("tick", "Monitoring: investigated issues", "", totalTokens, lastModel, time.Since(start).Milliseconds())
}

// --- Helper methods ---

func (w *PipelineWorker) loadPlan() (*Plan, error) {
	state, err := LoadPipelineState(w.siteDB.Reader())
	if err != nil {
		return nil, err
	}
	if state.PlanJSON == "" {
		return nil, fmt.Errorf("no plan found in pipeline state")
	}
	return ParsePlan(state.PlanJSON)
}

func (w *PipelineWorker) ownerName() string {
	var name string
	w.deps.DB.DB.QueryRow("SELECT display_name FROM users WHERE role = 'admin' ORDER BY id LIMIT 1").Scan(&name)
	if name != "" {
		name = strings.ReplaceAll(name, "\n", " ")
		name = strings.ReplaceAll(name, "\r", "")
		if runes := []rune(name); len(runes) > 50 {
			name = string(runes[:50])
		}
	}
	return name
}

// refreshCSSReference re-reads global CSS from the database and updates the
// anchor so subsequent page builds see the latest class definitions.
func (w *PipelineWorker) refreshCSSReference(db *sql.DB) {
	if w.anchors == nil {
		return
	}
	css := prompt.LoadGlobalCSS(db)
	if css == "" {
		return
	}
	if ref := prompt.ExtractCSSReference(css); ref != "" {
		w.anchors.cssReference = ref
		if groups := prompt.ExtractComponentGroups(css); groups != "" {
			w.anchors.componentGroups = groups
		}
	}
}
