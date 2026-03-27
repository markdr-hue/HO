/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
)

// executeNativeAction runs a scheduled task directly in Go without LLM involvement.
// Supported action types: delete_stale, count_rows, truncate, sql.
func (w *PipelineWorker) executeNativeAction(ctx context.Context, actionJSON string, runID int64, taskID int) {
	start := time.Now()

	var action struct {
		Type           string        `json:"type"`
		Table          string        `json:"table"`
		Column         string        `json:"column"`
		Age            string        `json:"age"`
		Query          string        `json:"query"`
		Params         []interface{} `json:"params"`
		URL            string        `json:"url"`
		Method         string        `json:"method"`
		ExpectedStatus int           `json:"expected_status"`
	}
	if err := json.Unmarshal([]byte(actionJSON), &action); err != nil {
		w.logger.Error("native action: invalid JSON", "error", err)
		w.finalizeTaskRun(runID, taskID, false, fmt.Sprintf("invalid native_action JSON: %v", err))
		return
	}

	var result string
	var actionErr error

	switch action.Type {
	case "delete_stale":
		if action.Table == "" || action.Age == "" {
			actionErr = fmt.Errorf("delete_stale requires table and age")
			break
		}
		col := action.Column
		if col == "" {
			col = "created_at"
		}
		// Sanitize table and column names (alphanumeric + underscore only).
		if !isSafeName(action.Table) || !isSafeName(col) {
			actionErr = fmt.Errorf("invalid table or column name")
			break
		}
		query := fmt.Sprintf("DELETE FROM %s WHERE %s < datetime('now', ?)", action.Table, col)
		res, err := w.siteDB.ExecWrite(query, "-"+action.Age)
		if err != nil {
			actionErr = fmt.Errorf("delete_stale: %w", err)
			break
		}
		affected, _ := res.RowsAffected()
		result = fmt.Sprintf("deleted %d rows from %s older than %s", affected, action.Table, action.Age)

	case "count_rows":
		if action.Table == "" {
			actionErr = fmt.Errorf("count_rows requires table")
			break
		}
		if !isSafeName(action.Table) {
			actionErr = fmt.Errorf("invalid table name")
			break
		}
		var count int
		err := w.siteDB.Reader().QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", action.Table)).Scan(&count)
		if err != nil {
			actionErr = fmt.Errorf("count_rows: %w", err)
			break
		}
		result = fmt.Sprintf("table %s has %d rows", action.Table, count)

	case "truncate":
		if action.Table == "" {
			actionErr = fmt.Errorf("truncate requires table")
			break
		}
		if !isSafeName(action.Table) {
			actionErr = fmt.Errorf("invalid table name")
			break
		}
		res, err := w.siteDB.ExecWrite(fmt.Sprintf("DELETE FROM %s", action.Table))
		if err != nil {
			actionErr = fmt.Errorf("truncate: %w", err)
			break
		}
		affected, _ := res.RowsAffected()
		result = fmt.Sprintf("truncated %s: %d rows deleted", action.Table, affected)

	case "sql":
		if action.Query == "" {
			actionErr = fmt.Errorf("sql requires query")
			break
		}
		q := strings.TrimSpace(action.Query)
		upper := strings.ToUpper(q)
		if strings.HasPrefix(upper, "SELECT") {
			rows, err := w.siteDB.Reader().Query(q, action.Params...)
			if err != nil {
				actionErr = fmt.Errorf("sql query: %w", err)
				break
			}
			defer rows.Close()
			cols, _ := rows.Columns()
			var count int
			for rows.Next() {
				count++
			}
			result = fmt.Sprintf("query returned %d rows (%d columns)", count, len(cols))
		} else if strings.HasPrefix(upper, "DELETE") || strings.HasPrefix(upper, "UPDATE") || strings.HasPrefix(upper, "INSERT") {
			res, err := w.siteDB.ExecWrite(q, action.Params...)
			if err != nil {
				actionErr = fmt.Errorf("sql exec: %w", err)
				break
			}
			affected, _ := res.RowsAffected()
			result = fmt.Sprintf("sql executed: %d rows affected", affected)
		} else {
			actionErr = fmt.Errorf("sql action only supports SELECT, INSERT, UPDATE, DELETE")
		}

	case "trigger_event":
		// Publish an event to the bus so actions subscribed to that event_type fire.
		// Config: {"type":"trigger_event", "event_type":"scheduled.cleanup", "payload":{"task":"cleanup"}}
		var triggerCfg struct {
			Type      string                 `json:"type"`
			EventType string                 `json:"event_type"`
			Payload   map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(actionJSON), &triggerCfg); err != nil {
			actionErr = fmt.Errorf("trigger_event: invalid config: %w", err)
			break
		}
		if triggerCfg.EventType == "" {
			actionErr = fmt.Errorf("trigger_event: event_type is required")
			break
		}
		if triggerCfg.Payload == nil {
			triggerCfg.Payload = map[string]interface{}{}
		}
		triggerCfg.Payload["source"] = "scheduler"
		triggerCfg.Payload["task_id"] = taskID
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventType(triggerCfg.EventType), w.siteID, triggerCfg.Payload))
		}
		result = fmt.Sprintf("published event '%s'", triggerCfg.EventType)

	default:
		actionErr = fmt.Errorf("unknown native action type: %s", action.Type)
	}

	duration := time.Since(start)
	if actionErr != nil {
		w.logger.Error("native action failed", "type", action.Type, "error", actionErr, "duration", duration)
		w.finalizeTaskRun(runID, taskID, false, actionErr.Error())
		return
	}

	w.logger.Info("native action completed", "type", action.Type, "result", result, "duration", duration)
	w.logBrainEvent("scheduled_task_native", result, action.Type, 0, "", duration.Milliseconds())
	w.finalizeTaskRun(runID, taskID, true, "")
}

// executeScheduledTask runs a scheduled task with a custom prompt.
func (w *PipelineWorker) executeScheduledTask(ctx context.Context, prompt string, runID int64, taskID int) {
	start := time.Now()
	cfg := ScheduledTaskConfig
	toolGuide := cfg.BuildGuide(w.deps.ToolRegistry, nil)

	result, err := w.runStage(ctx, StageExecution{
		Config:       cfg,
		SystemPrompt: buildScheduledTaskPrompt(w.deps.DB.DB, w.siteDB.Reader(), w.siteID, toolGuide, prompt, w.ownerName()),
		Messages:     []llm.Message{{Role: llm.RoleUser, Content: prompt}},
	})
	if err != nil {
		w.finalizeTaskRun(runID, taskID, false, fmt.Sprintf("LLM error: %v", err))
		return
	}

	w.logBrainEvent("scheduled_task", result.Content, prompt, result.Tokens, result.Model, time.Since(start).Milliseconds())
	w.finalizeTaskRun(runID, taskID, true, "")
}

// finalizeTaskRun updates a task run record and publishes completion/failure events.
func (w *PipelineWorker) finalizeTaskRun(runID int64, taskID int, success bool, errMsg string) {
	if runID <= 0 {
		return
	}
	if success {
		w.siteDB.ExecWrite("UPDATE ho_task_runs SET status = 'completed', completed_at = CURRENT_TIMESTAMP WHERE id = ?", runID)
		if taskID > 0 {
			w.siteDB.ExecWrite("UPDATE ho_scheduled_tasks SET run_count = run_count + 1 WHERE id = ?", taskID)
		}
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventScheduledCompleted, w.siteID, map[string]interface{}{
				"task_id": taskID,
				"run_id":  runID,
			}))
		}
	} else {
		w.siteDB.ExecWrite("UPDATE ho_task_runs SET status = 'failed', error_message = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?", errMsg, runID)
		if taskID > 0 {
			w.siteDB.ExecWrite("UPDATE ho_scheduled_tasks SET error_count = error_count + 1 WHERE id = ?", taskID)
		}
		if w.deps.Bus != nil {
			w.deps.Bus.Publish(events.NewEvent(events.EventScheduledFailed, w.siteID, map[string]interface{}{
				"task_id": taskID,
				"run_id":  runID,
				"error":   errMsg,
			}))
		}
	}
}

// isSafeName checks that a table/column name contains only alphanumeric chars and underscores.
func isSafeName(name string) bool {
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return len(name) > 0
}
