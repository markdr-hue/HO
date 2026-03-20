/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package llm

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// LLMLogEntry represents a single LLM API call log record.
type LLMLogEntry struct {
	Source             string
	SessionID          string
	Iteration          int
	Model              string
	ProviderType       string
	RequestMessages    string // JSON
	RequestSystem      string
	RequestTools       string // JSON array of tool names
	RequestMaxTokens   int
	ResponseContent    string
	ResponseToolCalls  string // JSON
	ResponseStopReason string
	InputTokens        int
	OutputTokens       int
	DurationMs         int64
	ErrorMessage       string
}

// LLMLogger persists LLM log entries.
type LLMLogger interface {
	LogLLMCall(entry LLMLogEntry)
}

// LoggedProvider wraps a Provider and logs every Complete/Stream call.
type LoggedProvider struct {
	inner       Provider
	logger      LLMLogger
	source      string // "brain" or "chat"
	session     string
	iterCounter *int
}

// NewLoggedProvider wraps a provider with per-call logging.
func NewLoggedProvider(inner Provider, logger LLMLogger, source, session string, iterCounter *int) *LoggedProvider {
	return &LoggedProvider{
		inner:       inner,
		logger:      logger,
		source:      source,
		session:     session,
		iterCounter: iterCounter,
	}
}

func (lp *LoggedProvider) Name() string { return lp.inner.Name() }
func (lp *LoggedProvider) Type() string { return lp.inner.Type() }
func (lp *LoggedProvider) Ping(ctx context.Context, model string) error {
	return lp.inner.Ping(ctx, model)
}

func (lp *LoggedProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	start := time.Now()
	resp, err := lp.inner.Complete(ctx, req)
	duration := time.Since(start).Milliseconds()

	entry := lp.buildEntry(req, duration)

	if err != nil {
		entry.ErrorMessage = err.Error()
	} else {
		entry.ResponseContent = resp.Content
		entry.ResponseStopReason = resp.StopReason
		entry.InputTokens = resp.Usage.InputTokens
		entry.OutputTokens = resp.Usage.OutputTokens
		if len(resp.ToolCalls) > 0 {
			if tcJSON, e := json.Marshal(resp.ToolCalls); e == nil {
				entry.ResponseToolCalls = string(tcJSON)
			}
		}
	}

	lp.logger.LogLLMCall(entry)
	return resp, err
}

func (lp *LoggedProvider) Stream(ctx context.Context, req CompletionRequest, callback StreamCallback) error {
	start := time.Now()

	var (
		contentBuilder strings.Builder
		toolCalls      []ToolCall
		usage          Usage
		streamErr      error
	)

	wrappedCallback := func(chunk StreamChunk) {
		if chunk.Delta != "" {
			contentBuilder.WriteString(chunk.Delta)
		}
		if chunk.ToolCall != nil {
			toolCalls = append(toolCalls, *chunk.ToolCall)
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if chunk.Error != nil {
			streamErr = chunk.Error
		}
		callback(chunk)
	}

	err := lp.inner.Stream(ctx, req, wrappedCallback)
	duration := time.Since(start).Milliseconds()

	entry := lp.buildEntry(req, duration)
	entry.ResponseContent = contentBuilder.String()
	entry.InputTokens = usage.InputTokens
	entry.OutputTokens = usage.OutputTokens

	if len(toolCalls) > 0 {
		if tcJSON, e := json.Marshal(toolCalls); e == nil {
			entry.ResponseToolCalls = string(tcJSON)
		}
	}

	if err != nil {
		entry.ErrorMessage = err.Error()
	} else if streamErr != nil {
		entry.ErrorMessage = streamErr.Error()
	}

	lp.logger.LogLLMCall(entry)
	return err
}

func (lp *LoggedProvider) buildEntry(req CompletionRequest, durationMs int64) LLMLogEntry {
	entry := LLMLogEntry{
		Source:           lp.source,
		SessionID:        lp.session,
		Iteration:        *lp.iterCounter,
		Model:            req.Model,
		ProviderType:     lp.inner.Type(),
		RequestSystem:    req.System,
		RequestMaxTokens: req.MaxTokens,
		DurationMs:       durationMs,
	}

	if msgJSON, e := json.Marshal(req.Messages); e == nil {
		entry.RequestMessages = string(msgJSON)
	}

	entry.RequestTools = toolNamesJSON(req.Tools)
	return entry
}

// toolNamesJSON returns a JSON array of just the tool names.
func toolNamesJSON(tools []ToolDef) string {
	if len(tools) == 0 {
		return "[]"
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	data, _ := json.Marshal(names)
	return string(data)
}
