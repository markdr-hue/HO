/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/markdr-hue/HO/events"
)

// Executor dispatches tool calls through the registry and publishes events.
type Executor struct {
	registry *Registry
}

// NewExecutor creates an Executor backed by the given registry.
func NewExecutor(registry *Registry) *Executor {
	return &Executor{registry: registry}
}

// Execute looks up the named tool, runs it, publishes an event, and returns
// the result JSON-marshalled for LLM consumption.
func (e *Executor) Execute(ctx context.Context, toolCtx *ToolContext, name string, args map[string]interface{}) (string, error) {
	tool, err := e.registry.Get(name)
	if err != nil {
		return e.marshalError(fmt.Sprintf("unknown tool: %s", name))
	}

	// Shallow-copy ToolContext so we don't mutate the caller's pointer.
	localCtx := *toolCtx
	localCtx.Ctx = ctx

	start := time.Now()

	// Run tool in a goroutine so the context timeout can cancel it.
	type toolResultPair struct {
		result *Result
		err    error
	}
	ch := make(chan toolResultPair, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- toolResultPair{nil, fmt.Errorf("tool %s panicked: %v\n%s", name, p, debug.Stack())}
			}
		}()
		r, err := tool.Execute(&localCtx, args)
		ch <- toolResultPair{r, err}
	}()

	var result *Result
	var execErr error
	select {
	case tr := <-ch:
		result, execErr = tr.result, tr.err
	case <-ctx.Done():
		execErr = fmt.Errorf("tool %s timed out: %w", name, ctx.Err())
		// Drain the channel in background with an abandon timer to bound
		// goroutine lifetime if the tool itself is permanently stuck.
		go func() {
			timer := time.NewTimer(5 * time.Minute)
			defer timer.Stop()
			select {
			case <-ch:
			case <-timer.C:
			}
		}()
	}
	duration := time.Since(start)

	// Build event payload.
	payload := map[string]interface{}{
		"tool":        name,
		"args":        args,
		"duration_ms": duration.Milliseconds(),
	}

	if execErr != nil {
		payload["error"] = execErr.Error()
		if toolCtx.Bus != nil {
			toolCtx.Bus.Publish(events.NewEvent(events.EventToolFailed, toolCtx.SiteID, payload))
		}
		jsonResult, _ := e.marshalError(execErr.Error())
		return jsonResult, execErr
	}

	if result == nil {
		result = &Result{Success: true}
	}

	payload["success"] = result.Success
	if toolCtx.Bus != nil {
		toolCtx.Bus.Publish(events.NewEvent(events.EventToolExecuted, toolCtx.SiteID, payload))
	}

	data, err := json.Marshal(result)
	if err != nil {
		return e.marshalError(fmt.Sprintf("failed to marshal result: %v", err))
	}
	return string(data), nil
}

// marshalError returns a JSON-encoded error Result.
func (e *Executor) marshalError(msg string) (string, error) {
	r := &Result{Success: false, Error: msg}
	data, _ := json.Marshal(r)
	return string(data), nil
}
