/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/tools"
)

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

		// Progress messages for short fix-up loops (VALIDATE, UPDATE_PLAN) so
		// the user sees activity. Skip for chat-wake and BUILD (which have
		// their own progress tracking).
		if maxIter <= 10 && maxIter != ChatWakeLiteConfig.MaxIterations && maxIter != ChatWakeConfig.MaxIterations && i > 0 {
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

		// Token budget governor: check cumulative usage (across all stages
		// of this build) plus tokens consumed so far in this loop.
		// At 90% of budget, nudge the LLM to wrap up. At 100%, hard-stop.
		// Note: costTracker.totalTokens reflects tokens from prior stages;
		// totalTokens reflects the current loop. The stage runner calls
		// addTokens after the loop, so we peek ahead here without mutating.
		if w.costTracker != nil {
			projected := w.costTracker.totalTokens + totalTokens
			if w.costTracker.tokenBudget > 0 && projected >= w.costTracker.tokenBudget {
				w.logger.Warn("token budget exhausted, force-stopping tool loop",
					"projected_tokens", projected, "budget", w.costTracker.tokenBudget)
				w.publishBrainMessage(fmt.Sprintf("Token budget reached (%dK tokens) — stopping build. Remaining items may be incomplete.",
					projected/1000))
				break
			}
			if w.costTracker.tokenBudget > 0 && !w.costTracker.wrapUpSent && projected >= w.costTracker.tokenBudget*9/10 {
				w.costTracker.wrapUpSent = true
				w.logger.Info("token budget 90% reached, injecting wrap-up nudge",
					"projected_tokens", projected, "budget", w.costTracker.tokenBudget)
				w.publishBrainMessage(fmt.Sprintf("Token usage at 90%% of budget (%dK/%dK) — wrapping up remaining items.",
					projected/1000, w.costTracker.tokenBudget/1000))
				messages = append(messages, llm.Message{
					Role:    llm.RoleUser,
					Content: "IMPORTANT: You are approaching the token budget limit. Focus on completing the most critical remaining items. Skip optional polish and move to the next unbuilt item immediately.",
				})
			}
		}

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
		if w.buildProgress != nil {
			complexity := w.buildProgress.pagesTotal + w.buildProgress.endpointsTotal
			if complexity > 10 {
				recentWindow = 24
			} else if complexity > 5 {
				recentWindow = 21
			}
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
