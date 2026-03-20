/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// LogsHandler handles brain log and LLM log listing.
type LogsHandler struct {
	deps *Deps
}

type brainLogEntry struct {
	ID         int       `json:"id"`
	EventType  string    `json:"event_type"`
	Summary    *string   `json:"summary"`
	Details    *string   `json:"details"`
	TokensUsed int       `json:"tokens_used"`
	Model      *string   `json:"model"`
	DurationMs int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// List returns brain logs for a site with optional filtering.
func (h *LogsHandler) List(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	eventType := r.URL.Query().Get("event_type")
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 500 {
			limit = v
		}
	}

	var rows interface{ Next() bool }
	var queryErr error

	if eventType != "" {
		r, err := siteDB.Query(
			"SELECT id, event_type, summary, details, tokens_used, model, duration_ms, created_at FROM ho_brain_log WHERE event_type = ? ORDER BY created_at DESC LIMIT ?",
			eventType, limit,
		)
		rows = r
		queryErr = err
		if err == nil {
			defer r.Close()
		}
	} else {
		r, err := siteDB.Query(
			"SELECT id, event_type, summary, details, tokens_used, model, duration_ms, created_at FROM ho_brain_log ORDER BY created_at DESC LIMIT ?",
			limit,
		)
		rows = r
		queryErr = err
		if err == nil {
			defer r.Close()
		}
	}

	if queryErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to query brain logs")
		return
	}

	type scanner interface {
		Next() bool
		Scan(dest ...interface{}) error
	}

	var entries []brainLogEntry
	s := rows.(scanner)
	for s.Next() {
		var e brainLogEntry
		if err := s.Scan(&e.ID, &e.EventType, &e.Summary, &e.Details, &e.TokensUsed, &e.Model, &e.DurationMs, &e.CreatedAt); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	if entries == nil {
		entries = []brainLogEntry{}
	}

	writeJSON(w, http.StatusOK, entries)
}

// --- LLM Log types ---

type llmLogListEntry struct {
	ID                 int       `json:"id"`
	Source             string    `json:"source"`
	SessionID          string    `json:"session_id"`
	Iteration          int       `json:"iteration"`
	Model              string    `json:"model"`
	ProviderType       string    `json:"provider_type"`
	RequestTools       *string   `json:"request_tools"`
	RequestMaxTokens   *int      `json:"request_max_tokens"`
	ResponseStopReason *string   `json:"response_stop_reason"`
	InputTokens        int       `json:"input_tokens"`
	OutputTokens       int       `json:"output_tokens"`
	DurationMs         int64     `json:"duration_ms"`
	ErrorMessage       *string   `json:"error_message"`
	ResponsePreview    *string   `json:"response_preview"`
	CreatedAt          time.Time `json:"created_at"`
}

type llmLogDetailEntry struct {
	llmLogListEntry
	RequestMessages   *string `json:"request_messages"`
	RequestSystem     *string `json:"request_system"`
	ResponseContent   *string `json:"response_content"`
	ResponseToolCalls *string `json:"response_tool_calls"`
}

type llmLogStats struct {
	TotalCalls        int              `json:"total_calls"`
	TotalInputTokens  int              `json:"total_input_tokens"`
	TotalOutputTokens int              `json:"total_output_tokens"`
	TotalErrors       int              `json:"total_errors"`
	ByModel           []llmModelStats  `json:"by_model"`
	BySource          []llmSourceStats `json:"by_source"`
}

type llmModelStats struct {
	Model        string `json:"model"`
	Calls        int    `json:"calls"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

type llmSourceStats struct {
	Source       string `json:"source"`
	Calls        int    `json:"calls"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// ListLLM returns lightweight llm_log entries for a site.
func (h *LogsHandler) ListLLM(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	q := r.URL.Query()
	limit := parseIntParam(q.Get("limit"), 50, 1, 500)
	offset := parseIntParam(q.Get("offset"), 0, 0, 100000)

	var conditions []string
	var args []interface{}

	if source := q.Get("source"); source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, source)
	}
	if model := q.Get("model"); model != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, model)
	}
	if q.Get("has_error") == "true" {
		conditions = append(conditions, "error_message IS NOT NULL")
	}
	if from := q.Get("from"); from != "" {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, from)
	}
	if to := q.Get("to"); to != "" {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, to)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	query := `SELECT id, source, session_id, iteration, model, provider_type,
		request_tools, request_max_tokens,
		response_stop_reason, input_tokens, output_tokens, duration_ms,
		error_message, SUBSTR(response_content, 1, 200), created_at
		FROM ho_llm_log` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := siteDB.Query(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query LLM logs")
		return
	}
	defer rows.Close()

	var entries []llmLogListEntry
	for rows.Next() {
		var e llmLogListEntry
		if err := rows.Scan(
			&e.ID, &e.Source, &e.SessionID, &e.Iteration, &e.Model, &e.ProviderType,
			&e.RequestTools, &e.RequestMaxTokens,
			&e.ResponseStopReason, &e.InputTokens, &e.OutputTokens, &e.DurationMs,
			&e.ErrorMessage, &e.ResponsePreview, &e.CreatedAt,
		); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	if entries == nil {
		entries = []llmLogListEntry{}
	}

	writeJSON(w, http.StatusOK, entries)
}

// GetLLM returns a single llm_log entry with full payloads.
func (h *LogsHandler) GetLLM(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	logID, err := strconv.Atoi(chi.URLParam(r, "logID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid log ID")
		return
	}

	var e llmLogDetailEntry
	err = siteDB.QueryRow(
		`SELECT id, source, session_id, iteration, model, provider_type,
			request_tools, request_max_tokens,
			response_stop_reason, input_tokens, output_tokens, duration_ms,
			error_message, SUBSTR(response_content, 1, 200), created_at,
			request_messages, request_system, response_content, response_tool_calls
		FROM ho_llm_log WHERE id = ?`, logID,
	).Scan(
		&e.ID, &e.Source, &e.SessionID, &e.Iteration, &e.Model, &e.ProviderType,
		&e.RequestTools, &e.RequestMaxTokens,
		&e.ResponseStopReason, &e.InputTokens, &e.OutputTokens, &e.DurationMs,
		&e.ErrorMessage, &e.ResponsePreview, &e.CreatedAt,
		&e.RequestMessages, &e.RequestSystem, &e.ResponseContent, &e.ResponseToolCalls,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "log entry not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch log entry")
		return
	}

	writeJSON(w, http.StatusOK, e)
}

// Stats returns aggregate llm_log statistics.
func (h *LogsHandler) Stats(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	stats := llmLogStats{}

	// Overall totals.
	row := siteDB.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(CASE WHEN error_message IS NOT NULL THEN 1 ELSE 0 END),0)
		FROM ho_llm_log`,
	)
	if err := row.Scan(&stats.TotalCalls, &stats.TotalInputTokens, &stats.TotalOutputTokens, &stats.TotalErrors); err != nil {
		writeJSON(w, http.StatusOK, stats)
		return
	}

	// By model.
	modelRows, err := siteDB.Query(
		`SELECT model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0)
		FROM ho_llm_log GROUP BY model ORDER BY COUNT(*) DESC`,
	)
	if err == nil {
		defer modelRows.Close()
		for modelRows.Next() {
			var ms llmModelStats
			if err := modelRows.Scan(&ms.Model, &ms.Calls, &ms.InputTokens, &ms.OutputTokens); err == nil {
				stats.ByModel = append(stats.ByModel, ms)
			}
		}
	}
	if stats.ByModel == nil {
		stats.ByModel = []llmModelStats{}
	}

	// By source.
	sourceRows, err := siteDB.Query(
		`SELECT source, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0)
		FROM ho_llm_log GROUP BY source ORDER BY COUNT(*) DESC`,
	)
	if err == nil {
		defer sourceRows.Close()
		for sourceRows.Next() {
			var ss llmSourceStats
			if err := sourceRows.Scan(&ss.Source, &ss.Calls, &ss.InputTokens, &ss.OutputTokens); err == nil {
				stats.BySource = append(stats.BySource, ss)
			}
		}
	}
	if stats.BySource == nil {
		stats.BySource = []llmSourceStats{}
	}

	writeJSON(w, http.StatusOK, stats)
}

// ExportCSV streams llm_log entries as a CSV file download.
func (h *LogsHandler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	q := r.URL.Query()
	limit := parseIntParam(q.Get("limit"), 10000, 1, 50000)

	var conditions []string
	var args []interface{}

	if source := q.Get("source"); source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, source)
	}
	if model := q.Get("model"); model != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, model)
	}
	if q.Get("has_error") == "true" {
		conditions = append(conditions, "error_message IS NOT NULL")
	}
	if from := q.Get("from"); from != "" {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, from)
	}
	if to := q.Get("to"); to != "" {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, to)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	query := `SELECT id, created_at, source, session_id, iteration, model, provider_type,
		input_tokens, output_tokens, duration_ms, response_stop_reason,
		error_message, request_max_tokens,
		request_system, request_messages, response_content, response_tool_calls, request_tools
		FROM ho_llm_log` + where + ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := siteDB.Query(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query logs")
		return
	}
	defer rows.Close()

	siteIDStr := chi.URLParam(r, "siteID")
	filename := fmt.Sprintf("llm_log_%s_%s.csv", siteIDStr, time.Now().Format("20060102_150405"))

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	cw := csv.NewWriter(w)
	cw.Write([]string{
		"ID", "Timestamp", "Source", "Session ID", "Iteration", "Model", "Provider",
		"Input Tokens", "Output Tokens", "Total Tokens", "Duration (ms)",
		"Stop Reason", "Error", "Max Tokens",
		"System Prompt", "Messages", "Response Content", "Tool Calls", "Tools",
	})

	for rows.Next() {
		var (
			id            int
			createdAt     time.Time
			source        string
			sessionID     string
			iteration     int
			model         string
			providerType  string
			inputTokens   int
			outputTokens  int
			durationMs    int64
			stopReason    *string
			errorMsg      *string
			maxTokens     *int
			reqSystem     *string
			reqMessages   *string
			respContent   *string
			respToolCalls *string
			reqTools      *string
		)
		if err := rows.Scan(&id, &createdAt, &source, &sessionID, &iteration, &model, &providerType,
			&inputTokens, &outputTokens, &durationMs, &stopReason, &errorMsg, &maxTokens,
			&reqSystem, &reqMessages, &respContent, &respToolCalls, &reqTools); err != nil {
			continue
		}

		maxTokStr := ""
		if maxTokens != nil {
			maxTokStr = strconv.Itoa(*maxTokens)
		}

		cw.Write([]string{
			strconv.Itoa(id),
			createdAt.Format(time.RFC3339),
			source,
			sessionID,
			strconv.Itoa(iteration),
			model,
			providerType,
			strconv.Itoa(inputTokens),
			strconv.Itoa(outputTokens),
			strconv.Itoa(inputTokens + outputTokens),
			strconv.FormatInt(durationMs, 10),
			derefStr(stopReason),
			derefStr(errorMsg),
			maxTokStr,
			derefStr(reqSystem),
			derefStr(reqMessages),
			derefStr(respContent),
			derefStr(respToolCalls),
			derefStr(reqTools),
		})
	}

	cw.Flush()
}

func parseIntParam(s string, def, min, max int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < min {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
