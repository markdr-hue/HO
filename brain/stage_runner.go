/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"errors"
	"time"

	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
)

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

// StageExecution describes everything needed to run one LLM-backed stage.
// Callers fill in the fields they need; the runStage function handles the
// shared boilerplate (getProvider, BuildToolDefs, runToolLoop, pause handling,
// stage logging).
type StageExecution struct {
	// Stage is the pipeline stage for logging. Use "" for non-pipeline
	// contexts (chat-wake, scheduled tasks) to skip stage logging.
	Stage PipelineStage

	// Config provides tool sets, LLM parameters, and guide mode.
	Config StageConfig

	// SystemPrompt is the fully-built system prompt.
	SystemPrompt string

	// Messages is the initial conversation.
	Messages []llm.Message

	// ToolOverride, if non-nil, overrides Config.ToolSet for BuildToolDefs
	// (used by BUILD's dynamic tool set).
	ToolOverride map[string]bool

	// MaxIterations overrides Config.MaxIterations when > 0.
	MaxIterations int

	// MaxTokens overrides Config.MaxTokens when > 0.
	MaxTokens int
}

// StageResult holds the output of runStage.
type StageResult struct {
	Content    string
	Model      string
	Tokens     int
	ToolCalls  int
}

// runStage executes the common LLM stage pattern:
//
//	beginStage → getProvider → BuildToolDefs → runToolLoop → handle pause → complete
//
// Returns (StageResult, error) where error may be ErrPipelinePaused.
// When Stage is non-empty, stage start/complete/fail logging is automatic.
func (w *PipelineWorker) runStage(ctx context.Context, exec StageExecution) (StageResult, error) {
	start := time.Now()
	var sr *stageRun
	if exec.Stage != "" {
		sr = w.beginStage(exec.Stage)
	}

	fail := func(err error) (StageResult, error) {
		if sr != nil {
			sr.fail(err)
		}
		w.emitStageTelemetry(exec.Stage, StageResult{}, start, "failed")
		return StageResult{}, err
	}

	provider, modelID, err := w.getProvider()
	if err != nil {
		return fail(err)
	}

	toolDefs := exec.Config.BuildToolDefs(w.deps.ToolRegistry, exec.ToolOverride)

	maxIter := exec.Config.MaxIterations
	if exec.MaxIterations > 0 {
		maxIter = exec.MaxIterations
	}
	maxTokens := exec.Config.MaxTokens
	if exec.MaxTokens > 0 {
		maxTokens = exec.MaxTokens
	}

	content, model, tokens, toolCalls, loopErr := w.runToolLoop(
		ctx, provider, modelID,
		exec.SystemPrompt, exec.Messages, toolDefs,
		maxIter, maxTokens, exec.Config.Temperature,
	)

	result := StageResult{
		Content:   content,
		Model:     model,
		Tokens:    tokens,
		ToolCalls: toolCalls,
	}

	if errors.Is(loopErr, ErrPipelinePaused) {
		if sr != nil {
			sr.complete(tokens, toolCalls)
		}
		w.emitStageTelemetry(exec.Stage, result, start, "paused")
		return result, ErrPipelinePaused
	}

	if loopErr != nil {
		return fail(loopErr)
	}

	if sr != nil {
		sr.complete(tokens, toolCalls)
	}
	w.emitStageTelemetry(exec.Stage, result, start, "completed")
	return result, nil
}

// emitStageTelemetry publishes a telemetry event with stage execution metrics.
func (w *PipelineWorker) emitStageTelemetry(stage PipelineStage, result StageResult, start time.Time, status string) {
	if w.deps.Bus == nil {
		return
	}
	label := string(stage)
	if label == "" {
		label = "non_pipeline"
	}
	w.deps.Bus.Publish(events.NewEvent(events.EventBrainStageTelemetry, w.siteID, map[string]interface{}{
		"stage":       label,
		"status":      status,
		"model":       result.Model,
		"tokens":      result.Tokens,
		"tool_calls":  result.ToolCalls,
		"duration_ms": time.Since(start).Milliseconds(),
	}))
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
