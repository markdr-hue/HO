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
	"log/slog"
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

// --- EXPAND stage ---

// minDescriptionLen is the threshold below which the EXPAND stage enriches the
// user's raw description. Descriptions longer than this are assumed to be
// detailed enough and skip expansion.
const minDescriptionLen = 200

func (w *PipelineWorker) runExpand(ctx context.Context) (PipelineStage, error) {
	sr := w.beginStage(StageExpand)

	site, err := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	if err != nil {
		return sr.fail(err)
	}

	// Skip expansion if description is already detailed or empty.
	rawDesc := ""
	if site.Description != nil {
		rawDesc = strings.TrimSpace(*site.Description)
	}
	if rawDesc == "" || len(rawDesc) >= minDescriptionLen {
		w.logger.Info("expand: skipping (description already detailed or empty)", "len", len(rawDesc))
		sr.complete(0, 0)
		return StagePlan, nil
	}

	provider, modelID, err := w.getProvider()
	if err != nil {
		return sr.fail(err)
	}

	cfg := StageConfigs[StageExpand]
	prompt := buildExpandPrompt(site.Name, rawDesc, w.ownerName())
	messages := []llm.Message{{Role: llm.RoleUser, Content: "Expand this project description."}}

	content, _, tokens, _, err := w.runToolLoop(
		ctx, provider, modelID,
		prompt, messages, nil, // no tools
		cfg.MaxIterations, cfg.MaxTokens, cfg.Temperature,
	)
	if err != nil {
		// Expansion failure is non-fatal — continue with the raw description.
		w.logger.Warn("expand stage failed, continuing with raw description", "error", err)
		sr.complete(tokens, 0)
		return StagePlan, nil
	}

	expanded := strings.TrimSpace(content)
	if expanded == "" {
		w.logger.Warn("expand stage returned empty, continuing with raw description")
		sr.complete(tokens, 0)
		return StagePlan, nil
	}

	// Store the expanded description on the site, preserving the original.
	fullDesc := fmt.Sprintf("%s\n\n---\nExpanded requirements:\n%s", rawDesc, expanded)
	if err := models.UpdateSiteDescription(w.deps.DB.DB, w.siteID, &fullDesc); err != nil {
		w.logger.Warn("failed to save expanded description", "error", err)
		// Non-fatal — the plan stage will still use the raw description.
	} else {
		w.logger.Info("expand: description enriched", "original_len", len(rawDesc), "expanded_len", len(fullDesc))
		w.publishBrainMessage("Requirements expanded")
	}

	sr.complete(tokens, 0)
	return StagePlan, nil
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

	// Validate and resolve CDN library URLs (pin @latest, fix broken versions).
	if cdnMsgs := validateExternalLibraries(plan, w.logger); len(cdnMsgs) > 0 {
		w.publishBrainMessage("CDN check: " + strings.Join(cdnMsgs, "; "))
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
		seenAnchors:    make(map[string]bool),
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
	// Build Guide uses phased injection — only the guidance relevant to the
	// current phase is included, saving ~800-1,200 tokens per call.
	ownerName := w.ownerName()
	w.systemPromptUpdater = func(current string) string {
		if w.buildProgress == nil {
			return current
		}
		phase := BuildPhaseInfra
		if w.buildProgress.infrastructureComplete() {
			if len(w.buildProgress.pagesDone) > 0 {
				phase = BuildPhasePages
			} else {
				phase = BuildPhaseFoundation
			}
		}
		return buildBuildPrompt(plan, site, ownerName, "", toolGuide, w.buildProgress, phase)
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

	// Auto-wire page-scope assets before validation to fix the most common
	// BUILD omission (LLM creates JS files but forgets to set the assets array).
	if wired := autoWirePageAssets(db, plan, w.logger); wired > 0 {
		w.publishBrainMessage(fmt.Sprintf("Auto-wired JS assets to %d page(s)", wired))
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
	if errors.Is(loopErr, ErrPipelinePaused) {
		w.publishBrainMessage("Validation paused — waiting for owner input.")
		sr.complete(tokens, toolCalls)
		return StageValidate, fmt.Errorf("paused: awaiting owner answers")
	}
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

	// Self-review: ask the LLM to read pages and fix consistency issues.
	if len(plan.Pages) >= 2 {
		if err := w.runSelfReview(ctx, provider, modelID, plan, db); errors.Is(err, ErrPipelinePaused) {
			w.publishBrainMessage("Validation paused — waiting for owner input.")
			sr.complete(tokens, toolCalls)
			return StageValidate, fmt.Errorf("paused: awaiting owner answers")
		}
		// Refresh CSS reference — self-review may have patched styles.
		w.refreshCSSReference(db)
	}

	sr.complete(tokens, toolCalls)
	return StageComplete, nil
}

// runSelfReview asks the LLM to read the first and last page and fix any
// design consistency issues. This lightweight pass catches problems that
// structural validation misses (mismatched styles, broken layouts, etc.).
func (w *PipelineWorker) runSelfReview(ctx context.Context, provider llm.Provider, modelID string, plan *Plan, db *sql.DB) error {
	if len(plan.Pages) < 2 {
		return nil
	}
	firstPage := plan.Pages[0].Path
	lastPage := plan.Pages[len(plan.Pages)-1].Path
	// Pick a middle page for broader coverage (same as last for 2-page sites).
	midPage := lastPage
	if len(plan.Pages) >= 3 {
		midPage = plan.Pages[len(plan.Pages)/2].Path
	}

	w.publishBrainMessage("Running design consistency review...")

	reviewPrompt := fmt.Sprintf(`You are HO. Build and validation complete. Quick design consistency review:
1. Read %s, %s, and %s using manage_pages(action="get")
2. Read the global CSS using manage_files(action="get")
3. Check these specific items:
   - Section spacing: do all pages use the same vertical rhythm (padding/margin between sections)?
   - Heading hierarchy: do pages follow consistent heading levels?
   - Button styles: are buttons styled consistently (same classes, padding, border-radius)?
   - Color usage: are pages using var(--color-*) tokens, not hardcoded hex values?
   - Empty/loading states: do data-driven pages handle empty state?
4. Fix ONLY genuine inconsistencies with targeted patches. Do not refactor or add features.`, firstPage, midPage, lastPage)

	messages := []llm.Message{{Role: llm.RoleUser, Content: reviewPrompt}}
	valCfg := StageConfigs[StageValidate]
	toolDefs := valCfg.BuildToolDefs(w.deps.ToolRegistry, nil)
	_, _, _, _, err := w.runToolLoop(ctx, provider, modelID, reviewPrompt, messages, toolDefs, 6, valCfg.MaxTokens, valCfg.Temperature)
	if errors.Is(err, ErrPipelinePaused) {
		return ErrPipelinePaused
	}
	if err != nil {
		w.logger.Warn("self-review failed", "error", err)
	}
	return nil
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
	issues = append(issues, validateCSSCompleteness(db)...)
	issues = append(issues, validateLayoutHead(db)...)
	issues = append(issues, validateNoPlaceholders(db, plan)...)
	issues = append(issues, validateNoConsoleLog(db)...)
	issues = append(issues, validateDesignTokenUsage(db, plan)...)
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
	// If global-scope JS files exist, they are auto-injected into every page
	// via the layout — so pages don't need page-scope assets to call endpoints.
	var globalJSCount int
	db.QueryRow("SELECT COUNT(*) FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.js'").Scan(&globalJSCount)
	if globalJSCount > 0 {
		return nil
	}

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

// autoWirePageAssets deterministically wires page-scope JS/CSS assets to pages
// that declare endpoints but have no assets set. This fixes the most common
// BUILD-stage omission without relying on LLM fix-up.
func autoWirePageAssets(db *sql.DB, plan *Plan, logger *slog.Logger) int {
	// 1. Collect pages that need wiring: have endpoints but NULL/empty assets.
	type needsWire struct {
		path string
	}
	var unwired []needsWire
	for _, pg := range plan.Pages {
		if len(pg.Endpoints) == 0 {
			continue
		}
		var assetsJSON sql.NullString
		db.QueryRow("SELECT assets FROM ho_pages WHERE path = ? AND is_deleted = 0", pg.Path).Scan(&assetsJSON)
		if !assetsJSON.Valid || assetsJSON.String == "" || assetsJSON.String == "[]" {
			unwired = append(unwired, needsWire{path: pg.Path})
		}
	}
	if len(unwired) == 0 {
		return 0
	}

	// 2. If global-scope JS exists it is auto-injected — pages don't need
	//    page-scope assets wired to call endpoints.
	var globalJSCount int
	db.QueryRow("SELECT COUNT(*) FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.js'").Scan(&globalJSCount)
	if globalJSCount > 0 {
		logger.Info("autoWirePageAssets: global JS exists, skipping page-scope wiring")
		return 0
	}

	// 3. Collect all page-scope asset files.
	rows, err := db.Query("SELECT filename FROM ho_assets WHERE scope = 'page' AND (filename LIKE '%.js' OR filename LIKE '%.css')")
	if err != nil {
		logger.Warn("autoWirePageAssets: query failed", "error", err)
		return 0
	}
	defer rows.Close()
	var pageFiles []string
	for rows.Next() {
		var fn string
		if rows.Scan(&fn) == nil {
			pageFiles = append(pageFiles, fn)
		}
	}
	if len(pageFiles) == 0 {
		return 0
	}

	// 3. Match files to pages.
	// For single unwired page: wire everything to it.
	// For multiple: match by path slug, then assign unmatched to all.
	wiring := make(map[string][]string) // path → filenames

	if len(unwired) == 1 {
		wiring[unwired[0].path] = pageFiles
	} else {
		matched := make(map[string]bool) // track which files got matched
		for _, uw := range unwired {
			slug := strings.TrimPrefix(uw.path, "/")
			slug = strings.ReplaceAll(slug, "/", "-")
			if slug == "" {
				slug = "index"
			}
			// Remove :param segments for matching
			parts := strings.Split(slug, "-")
			var cleanParts []string
			for _, p := range parts {
				if !strings.HasPrefix(p, ":") {
					cleanParts = append(cleanParts, p)
				}
			}
			slug = strings.Join(cleanParts, "-")

			for _, fn := range pageFiles {
				base := strings.TrimSuffix(fn, ".js")
				base = strings.TrimSuffix(base, ".css")
				if slug != "" && (strings.HasPrefix(base, slug) || strings.HasPrefix(slug, base)) {
					wiring[uw.path] = append(wiring[uw.path], fn)
					matched[fn] = true
				}
			}
		}

		// Collect unmatched files and assign them to all still-unwired pages.
		var unmatched []string
		for _, fn := range pageFiles {
			if !matched[fn] {
				unmatched = append(unmatched, fn)
			}
		}
		if len(unmatched) > 0 {
			for _, uw := range unwired {
				if len(wiring[uw.path]) == 0 {
					wiring[uw.path] = unmatched
				}
			}
		}
	}

	// 4. Write the wiring to the database.
	wired := 0
	for path, files := range wiring {
		assetsJSON, err := json.Marshal(files)
		if err != nil {
			continue
		}
		_, err = db.Exec("UPDATE ho_pages SET assets = ?, updated_at = CURRENT_TIMESTAMP WHERE path = ? AND is_deleted = 0", string(assetsJSON), path)
		if err != nil {
			logger.Warn("autoWirePageAssets: update failed", "path", path, "error", err)
			continue
		}
		logger.Info("auto-wired page assets", "path", path, "assets", files)
		wired++
	}
	return wired
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

// validateCSSCompleteness checks that CSS classes used in page HTML are defined
// in the global CSS. Catches missing layout/container classes that cause broken spacing.
func validateCSSCompleteness(db *sql.DB) []string {
	// Load global CSS.
	var globalCSS sql.NullString
	db.QueryRow("SELECT content FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.css' ORDER BY id LIMIT 1").Scan(&globalCSS)
	if !globalCSS.Valid || globalCSS.String == "" {
		return nil
	}
	css := globalCSS.String

	// Extract all class selectors defined in CSS (e.g. ".card-grid" from ".card-grid {").
	cssClasses := map[string]bool{}
	for _, line := range strings.Split(css, "\n") {
		line = strings.TrimSpace(line)
		i := 0
		for i < len(line) {
			if line[i] == '.' {
				j := i + 1
				for j < len(line) && (line[j] == '-' || line[j] == '_' || (line[j] >= 'a' && line[j] <= 'z') || (line[j] >= 'A' && line[j] <= 'Z') || (line[j] >= '0' && line[j] <= '9')) {
					j++
				}
				if j > i+1 {
					cssClasses[line[i+1:j]] = true
				}
				i = j
			} else {
				i++
			}
		}
	}

	// Collect all class="..." values from all pages.
	htmlClasses := map[string]int{} // class name → occurrence count
	rows, err := db.Query("SELECT content FROM ho_pages WHERE is_deleted = 0 AND content IS NOT NULL")
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var content string
		if rows.Scan(&content) != nil {
			continue
		}
		// Find all class="..." attributes.
		for _, attr := range extractClassAttrs(content) {
			for _, cls := range strings.Fields(attr) {
				htmlClasses[cls]++
			}
		}
	}

	// Find HTML classes missing from CSS, focusing on layout/container classes
	// that are most likely to cause visible spacing/layout bugs.
	layoutKeywords := []string{"grid", "container", "section", "wrapper", "layout", "cta", "actions", "group", "list", "row", "col"}
	var missing []string
	for cls, count := range htmlClasses {
		if cssClasses[cls] {
			continue
		}
		// Skip common utility-like prefixes that may be generated in bulk.
		if strings.HasPrefix(cls, "mt-") || strings.HasPrefix(cls, "mb-") ||
			strings.HasPrefix(cls, "pt-") || strings.HasPrefix(cls, "pb-") ||
			strings.HasPrefix(cls, "text-") || strings.HasPrefix(cls, "flex") ||
			strings.HasPrefix(cls, "items-") || strings.HasPrefix(cls, "justify-") ||
			strings.HasPrefix(cls, "gap-") || strings.HasPrefix(cls, "w-") ||
			strings.HasPrefix(cls, "h-") || strings.HasPrefix(cls, "p-") ||
			strings.HasPrefix(cls, "m-") || strings.HasPrefix(cls, "bg-") ||
			strings.HasPrefix(cls, "font-") || strings.HasPrefix(cls, "rounded") {
			continue
		}
		// Flag layout-related classes that appear multiple times or contain layout keywords.
		isLayout := count >= 2
		if !isLayout {
			lower := strings.ToLower(cls)
			for _, kw := range layoutKeywords {
				if strings.Contains(lower, kw) {
					isLayout = true
					break
				}
			}
		}
		if isLayout {
			missing = append(missing, cls)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	// Cap at 8 to avoid overwhelming the fix-up stage.
	if len(missing) > 8 {
		missing = missing[:8]
	}
	return []string{fmt.Sprintf("CSS classes used in HTML but not defined in stylesheet (likely missing layout/spacing styles): %s", strings.Join(missing, ", "))}
}

// extractClassAttrs finds all class="..." attribute values in HTML content.
func extractClassAttrs(html string) []string {
	var attrs []string
	search := html
	for {
		idx := strings.Index(search, `class="`)
		if idx == -1 {
			break
		}
		start := idx + 7
		end := strings.Index(search[start:], `"`)
		if end == -1 {
			break
		}
		attrs = append(attrs, search[start:start+end])
		search = search[start+end+1:]
	}
	return attrs
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

// validateDesignTokenUsage checks that the global CSS defines --color-* custom
// properties and doesn't excessively hardcode design token colors outside :root.
func validateDesignTokenUsage(db *sql.DB, plan *Plan) []string {
	if plan.DesignSystem == nil || len(plan.DesignSystem.Colors) == 0 {
		return nil
	}
	var globalCSS sql.NullString
	db.QueryRow("SELECT content FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.css' ORDER BY id LIMIT 1").Scan(&globalCSS)
	if !globalCSS.Valid || globalCSS.String == "" {
		return nil
	}
	if !strings.Contains(globalCSS.String, "--color-") {
		return []string{"Global CSS does not define --color-* custom properties from design tokens"}
	}
	hexCount := countHardcodedDesignColors(globalCSS.String, plan.DesignSystem.Colors)
	if hexCount > 5 {
		return []string{fmt.Sprintf("Global CSS has %d hardcoded design-token colors — use var(--color-*) instead", hexCount)}
	}
	return nil
}

// countHardcodedDesignColors counts occurrences of plan color hex values
// in CSS content outside the :root block. Colors inside :root are expected
// (that's where custom properties are defined).
func countHardcodedDesignColors(css string, colors map[string]string) int {
	// Strip the :root block where custom property definitions live.
	outside := css
	if start := strings.Index(css, ":root"); start >= 0 {
		if braceStart := strings.Index(css[start:], "{"); braceStart >= 0 {
			depth := 0
			for i := start + braceStart; i < len(css); i++ {
				if css[i] == '{' {
					depth++
				} else if css[i] == '}' {
					depth--
					if depth == 0 {
						outside = css[:start] + css[i+1:]
						break
					}
				}
			}
		}
	}
	lower := strings.ToLower(outside)
	count := 0
	for _, hex := range colors {
		hex = strings.ToLower(hex)
		count += strings.Count(lower, hex)
	}
	return count
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

	// Build the preview URL from the site's domain setting, falling back to localhost.
	port := w.deps.PublicPort
	if port == 0 {
		port = 5000
	}
	previewURL := fmt.Sprintf("http://localhost:%d", port)
	var domain sql.NullString
	w.deps.DB.DB.QueryRow("SELECT domain FROM sites WHERE id = ?", w.siteID).Scan(&domain)
	if domain.Valid && domain.String != "" {
		d := strings.ToLower(domain.String)
		if strings.HasSuffix(d, ".localhost") {
			previewURL = fmt.Sprintf("http://%s:%d", domain.String, port)
		} else if d == "localhost" {
			previewURL = fmt.Sprintf("http://localhost:%d", port)
		} else {
			previewURL = "https://" + domain.String
		}
	}

	completeMsg := fmt.Sprintf(
		"**Your project is live!**\n\n"+
			"It has been built and is ready to use:\n\n"+
			"- **Preview locally:** [%s](%s)\n"+
			"- **Share with the world:** [Open Settings](/#/sites/%d/settings) to publish your project online in one click\n\n"+
			"You can chat here anytime to ask questions, request changes, or add new features. I'll keep monitoring everything in the background.",
		previewURL, previewURL, w.siteID,
	)
	w.publishBrainMessage(completeMsg)

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

	cfg := MonitoringConfig
	site, _ := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	plan, _ := w.loadPlan()

	var contextMsg strings.Builder
	contextMsg.WriteString("Check site health. Issues detected:\n")
	contextMsg.WriteString(fmt.Sprintf("- %d recent errors in the last hour\n", recentErrors))

	result, loopErr := w.runStage(ctx, StageExecution{
		Config:       cfg,
		SystemPrompt: buildMonitoringPrompt(site, w.siteDB.Reader(), plan, w.ownerName()),
		Messages:     []llm.Message{{Role: llm.RoleUser, Content: contextMsg.String()}},
	})
	if errors.Is(loopErr, ErrPipelinePaused) {
		w.publishBrainMessage("Monitoring paused — waiting for owner input.")
		w.setState(StatePaused)
		PausePipeline(w.siteDB, PauseReasonOwnerAnswers)
		return
	}
	if loopErr != nil {
		w.logger.Error("monitoring tick failed", "error", loopErr)
		return
	}

	w.logBrainEvent("tick", "Monitoring: investigated issues", "", result.Tokens, result.Model, time.Since(start).Milliseconds())
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
