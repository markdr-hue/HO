/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package llm

import (
	"database/sql"
	"log/slog"

	"github.com/markdr-hue/HO/internal/sqliteretry"
)

const logBufferSize = 256

// DBLLMLogger writes LLM log entries to a site's SQLite database
// through a buffered channel and single writer goroutine.
type DBLLMLogger struct {
	db   *sql.DB
	ch   chan LLMLogEntry
	done chan struct{}
}

// NewDBLLMLogger creates a logger that writes to the given site database.
// Call Close() when done to drain the buffer.
func NewDBLLMLogger(siteDB *sql.DB) *DBLLMLogger {
	l := &DBLLMLogger{
		db:   siteDB,
		ch:   make(chan LLMLogEntry, logBufferSize),
		done: make(chan struct{}),
	}
	go l.writeLoop()
	return l
}

// LogLLMCall queues a log entry for async writing.
func (l *DBLLMLogger) LogLLMCall(entry LLMLogEntry) {
	select {
	case l.ch <- entry:
	default:
		slog.Warn("llm log buffer full, dropping entry", "source", entry.Source)
	}
}

// Close drains remaining entries and stops the writer goroutine.
func (l *DBLLMLogger) Close() {
	close(l.ch)
	<-l.done
}

func (l *DBLLMLogger) writeLoop() {
	defer close(l.done)
	for entry := range l.ch {
		if _, err := sqliteretry.Exec(l.db,
			`INSERT INTO ho_llm_log (
				source, session_id, iteration, model, provider_type,
				request_messages, request_system, request_tools, request_max_tokens,
				response_content, response_tool_calls, response_stop_reason,
				input_tokens, output_tokens, duration_ms, error_message
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.Source, entry.SessionID, entry.Iteration,
			entry.Model, entry.ProviderType,
			entry.RequestMessages, entry.RequestSystem,
			entry.RequestTools, entry.RequestMaxTokens,
			entry.ResponseContent, entry.ResponseToolCalls, entry.ResponseStopReason,
			entry.InputTokens, entry.OutputTokens,
			entry.DurationMs, nullIfEmpty(entry.ErrorMessage),
		); err != nil {
			slog.Debug("llm log write failed", "source", entry.Source, "error", err)
		}
	}
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
