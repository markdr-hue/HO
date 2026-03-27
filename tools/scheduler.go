/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/markdr-hue/HO/internal/cron"
)

// computeNextRun calculates the next run time for a task.
// For interval-based tasks, adds interval seconds to now.
// For cron-based tasks, uses the shared cron parser.
func computeNextRun(cronExpr string, intervalSec int, now time.Time) time.Time {
	if cronExpr != "" {
		return cron.NextTime(cronExpr, now)
	}
	if intervalSec > 0 {
		return now.Add(time.Duration(intervalSec) * time.Second)
	}
	return now.Add(1 * time.Hour)
}

// SchedulerTool consolidates create, list, update, and delete into a single
// manage_scheduler tool.
type SchedulerTool struct{}

func (t *SchedulerTool) Name() string { return "manage_scheduler" }
func (t *SchedulerTool) Description() string {
	return "Create, list, update, or delete scheduled tasks."
}

func (t *SchedulerTool) Guide() string {
	return `### Scheduled Tasks (manage_scheduler)
- Tasks run on cron expression or interval_seconds.
- Brain executes the task prompt with full tool access.
- For simple operations (cleanup, counting), use native_action instead of prompt to avoid LLM token cost.
- native_action is a JSON object. Supported types:
  - {"type":"delete_stale","table":"sessions","age":"1 hour","column":"created_at"} — delete old rows
  - {"type":"count_rows","table":"sessions"} — count rows in a table
  - {"type":"truncate","table":"temp_data"} — delete all rows from a table
  - {"type":"sql","query":"DELETE FROM logs WHERE level='debug'","params":[]} — run parameterized SQL (advanced: mutations bypass validation — prefer delete_stale or prompt-based tasks for write operations)
  - {"type":"trigger_event","event_type":"scheduled.cleanup","payload":{"task":"cleanup"}} — publish an event that triggers matching actions (bridges scheduler → actions)
- Use run_now action to manually trigger a task immediately.`
}

func (t *SchedulerTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"create", "list", "update", "delete", "run_now"},
			},
			"id":               map[string]interface{}{"type": "number", "description": "Task ID (for update/delete/run_now)"},
			"name":             map[string]interface{}{"type": "string", "description": "Name of the scheduled task"},
			"description":      map[string]interface{}{"type": "string", "description": "Description of what the task does"},
			"cron_expression":  map[string]interface{}{"type": "string", "description": "Cron expression for scheduling (e.g. '0 */6 * * *')"},
			"interval_seconds": map[string]interface{}{"type": "number", "description": "Alternative: run every N seconds"},
			"prompt":           map[string]interface{}{"type": "string", "description": "Prompt to execute when the task runs (LLM-driven)"},
			"native_action":    map[string]interface{}{"type": "string", "description": "JSON action for direct execution without LLM (e.g. {\"type\":\"delete_stale\",\"table\":\"sessions\",\"age\":\"1 hour\"})"},
			"is_enabled":       map[string]interface{}{"type": "boolean", "description": "Enable or disable the task"},
		},
		"required": []string{},
	}
}

func (t *SchedulerTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"create":  t.executeCreate,
		"list":    t.executeList,
		"update":  t.executeUpdate,
		"delete":  t.executeDelete,
		"run_now": t.executeRunNow,
	}, nil)
}

func (t *SchedulerTool) executeCreate(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	prompt, _ := args["prompt"].(string)
	nativeAction, _ := args["native_action"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}
	if prompt == "" && nativeAction == "" {
		return &Result{Success: false, Error: "either prompt or native_action is required"}, nil
	}
	description, _ := args["description"].(string)
	cronExpr, _ := args["cron_expression"].(string)
	intervalSec, _ := args["interval_seconds"].(float64)

	// Validate cron expression if provided.
	if cronExpr != "" {
		if err := cron.Validate(cronExpr); err != nil {
			return &Result{Success: false, Error: "invalid cron expression: " + err.Error()}, nil
		}
	}

	// Compute initial next_run so the scheduler picks it up immediately.
	now := time.Now()
	var nextRun *time.Time
	if cronExpr != "" {
		nr := computeNextRun(cronExpr, 0, now)
		nextRun = &nr
	} else if intervalSec > 0 {
		nr := computeNextRun("", int(intervalSec), now)
		nextRun = &nr
	}

	result, err := ctx.DB.Exec(
		"INSERT INTO ho_scheduled_tasks (name, description, cron_expression, interval_seconds, prompt, native_action, next_run) VALUES (?, ?, ?, ?, ?, ?, ?)",
		name, description, cronExpr, int(intervalSec), prompt, nativeAction, nextRun,
	)
	if err != nil {
		return nil, fmt.Errorf("creating task: %w", err)
	}

	id, _ := result.LastInsertId()
	data := map[string]interface{}{
		"id":   id,
		"name": name,
	}
	if nativeAction != "" {
		data["native_action"] = true
	}
	if nextRun != nil {
		data["next_run"] = *nextRun
	}
	return &Result{Success: true, Data: data}, nil
}

func (t *SchedulerTool) executeList(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT id, name, description, cron_expression, interval_seconds, prompt, native_action, is_enabled, last_run, next_run, created_at FROM ho_scheduled_tasks ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	defer rows.Close()

	var tasks []map[string]interface{}
	for rows.Next() {
		var id int
		var name string
		var description, cronExpr, prompt, nativeAction sql.NullString
		var intervalSec sql.NullInt64
		var isEnabled bool
		var lastRun, nextRun sql.NullTime
		var createdAt time.Time

		if err := rows.Scan(&id, &name, &description, &cronExpr, &intervalSec, &prompt, &nativeAction, &isEnabled, &lastRun, &nextRun, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}

		task := map[string]interface{}{
			"id":         id,
			"name":       name,
			"is_enabled": isEnabled,
			"created_at": createdAt,
		}
		if description.Valid {
			task["description"] = description.String
		}
		if cronExpr.Valid {
			task["cron_expression"] = cronExpr.String
		}
		if intervalSec.Valid {
			task["interval_seconds"] = intervalSec.Int64
		}
		if prompt.Valid && prompt.String != "" {
			task["prompt"] = prompt.String
		}
		if nativeAction.Valid && nativeAction.String != "" {
			task["native_action"] = nativeAction.String
		}
		if lastRun.Valid {
			task["last_run"] = lastRun.Time
		}
		if nextRun.Valid {
			task["next_run"] = nextRun.Time
		}
		tasks = append(tasks, task)
	}

	return &Result{Success: true, Data: tasks}, nil
}

func (t *SchedulerTool) executeUpdate(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	idFloat, _ := args["id"].(float64)
	if idFloat == 0 {
		return &Result{Success: false, Error: "id is required"}, nil
	}
	taskID := int64(idFloat)

	setClauses := []string{"updated_at = CURRENT_TIMESTAMP"}
	var values []interface{}

	if name, ok := args["name"].(string); ok && name != "" {
		setClauses = append(setClauses, "name = ?")
		values = append(values, name)
	}
	if description, ok := args["description"].(string); ok {
		setClauses = append(setClauses, "description = ?")
		values = append(values, description)
	}
	var newCronExpr string
	var newIntervalSec int
	var scheduleChanged bool
	if cronExpr, ok := args["cron_expression"].(string); ok {
		if cronExpr != "" {
			if err := cron.Validate(cronExpr); err != nil {
				return &Result{Success: false, Error: "invalid cron expression: " + err.Error()}, nil
			}
		}
		setClauses = append(setClauses, "cron_expression = ?")
		values = append(values, cronExpr)
		newCronExpr = cronExpr
		scheduleChanged = true
	}
	if intervalSec, ok := args["interval_seconds"].(float64); ok {
		setClauses = append(setClauses, "interval_seconds = ?")
		values = append(values, int(intervalSec))
		newIntervalSec = int(intervalSec)
		scheduleChanged = true
	}
	if prompt, ok := args["prompt"].(string); ok && prompt != "" {
		setClauses = append(setClauses, "prompt = ?")
		values = append(values, prompt)
	}
	if nativeAction, ok := args["native_action"].(string); ok {
		setClauses = append(setClauses, "native_action = ?")
		values = append(values, nativeAction)
	}
	if isEnabled, ok := args["is_enabled"].(bool); ok {
		setClauses = append(setClauses, "is_enabled = ?")
		values = append(values, isEnabled)
	}

	values = append(values, taskID)

	query := fmt.Sprintf("UPDATE ho_scheduled_tasks SET %s WHERE id = ?",
		strings.Join(setClauses, ", "))

	res, err := ctx.DB.Exec(query, values...)
	if err != nil {
		return nil, fmt.Errorf("updating task: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "task not found"}, nil
	}

	// Recalculate next_run if the schedule changed.
	if scheduleChanged {
		now := time.Now()
		nextRun := computeNextRun(newCronExpr, newIntervalSec, now)
		if _, err := ctx.DB.Exec(
			"UPDATE ho_scheduled_tasks SET next_run = ? WHERE id = ?",
			nextRun, taskID,
		); err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("failed to update next_run: %v", err)}, nil
		}
	}

	return &Result{Success: true, Data: map[string]interface{}{"id": taskID}}, nil
}

func (t *SchedulerTool) executeDelete(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	idFloat, _ := args["id"].(float64)
	if idFloat == 0 {
		return &Result{Success: false, Error: "id is required"}, nil
	}
	taskID := int64(idFloat)

	res, err := ctx.DB.Exec(
		"DELETE FROM ho_scheduled_tasks WHERE id = ?",
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("deleting task: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "task not found"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{"deleted_id": taskID}}, nil
}

func (t *SchedulerTool) executeRunNow(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	idFloat, _ := args["id"].(float64)
	if idFloat == 0 {
		return &Result{Success: false, Error: "id is required"}, nil
	}
	taskID := int64(idFloat)

	// Set next_run to now so the scheduler picks it up on the next tick.
	res, err := ctx.DB.Exec(
		"UPDATE ho_scheduled_tasks SET next_run = CURRENT_TIMESTAMP WHERE id = ? AND is_enabled = 1",
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("triggering task: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "task not found or not enabled"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"id":      taskID,
		"message": "task scheduled to run on next tick (~30 seconds)",
	}}, nil
}
