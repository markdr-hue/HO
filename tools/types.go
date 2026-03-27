/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/security"
)

// Tool is the interface every tool must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]interface{} // JSON Schema
	Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error)
}

// ToolContext carries dependencies into tool execution.
type ToolContext struct {
	Ctx        context.Context // cancellation/timeout context from the pipeline
	DB         *sql.DB         // site-scoped database
	GlobalDB   *sql.DB         // global database (users, providers, sites)
	SiteID     int             // kept for file paths and logging, not for queries
	Logger     *slog.Logger
	Bus        *events.Bus
	Encryptor  *security.Encryptor
	PublicPort int             // public server port for self-testing (default 5000)
}

// Result is the standard return value from a tool execution.
type Result struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// Summarizer is an optional interface tools can implement to provide
// custom result summarization for cross-stage chat history.
type Summarizer interface {
	Summarize(result string) string
}

// ResultSizer is an optional interface tools can implement to override
// the default result truncation limit (4KB).
type ResultSizer interface {
	MaxResultSize() int
}

// Documented is an optional interface tools can implement to provide
// rich behavioral documentation for LLM prompts. Guide() returns
// markdown text describing when/why/how to use the tool, response
// shapes, protocols, and behavioral contracts.
type Documented interface {
	Guide() string
}

// Context returns the tool's context, falling back to context.Background()
// if none was set (e.g., in tests or direct calls).
func (tc *ToolContext) Context() context.Context {
	if tc.Ctx != nil {
		return tc.Ctx
	}
	return context.Background()
}

// RequireAction extracts and validates the "action" arg. Returns the action
// string on success, or an error Result if missing/empty.
func RequireAction(args map[string]interface{}) (string, *Result) {
	action, _ := args["action"].(string)
	if action == "" {
		return "", &Result{Success: false, Error: "action is required"}
	}
	return action, nil
}
