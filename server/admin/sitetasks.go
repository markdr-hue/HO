/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/internal/cron"
)

// SiteTasksHandler handles scheduled task listing for site detail views.
type SiteTasksHandler struct {
	deps *Deps
}

type scheduledTask struct {
	ID              int        `json:"id"`
	SiteID          int        `json:"site_id"`
	Name            string     `json:"name"`
	Description     *string    `json:"description"`
	CronExpression  *string    `json:"cron_expression"`
	IntervalSeconds *int       `json:"interval_seconds"`
	Prompt          *string    `json:"prompt"`
	IsEnabled       bool       `json:"is_enabled"`
	LastRun         *time.Time `json:"last_run"`
	NextRun         *time.Time `json:"next_run"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// List returns all scheduled tasks for a site.
func (h *SiteTasksHandler) List(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, name, description, cron_expression, interval_seconds, prompt, is_enabled, last_run, next_run, created_at, updated_at
		 FROM ho_scheduled_tasks ORDER BY created_at DESC`,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []scheduledTask{})
		return
	}
	defer rows.Close()

	var tasks []scheduledTask
	for rows.Next() {
		var t scheduledTask
		t.SiteID = siteID
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.CronExpression, &t.IntervalSeconds, &t.Prompt, &t.IsEnabled, &t.LastRun, &t.NextRun, &t.CreatedAt, &t.UpdatedAt); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	if tasks == nil {
		tasks = []scheduledTask{}
	}

	writeJSON(w, http.StatusOK, tasks)
}

// Toggle enables or disables a scheduled task.
func (h *SiteTasksHandler) Toggle(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	taskID, err := strconv.Atoi(chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task ID")
		return
	}

	// Get current state and flip it.
	var isEnabled bool
	if err := siteDB.QueryRow("SELECT is_enabled FROM ho_scheduled_tasks WHERE id = ?", taskID).Scan(&isEnabled); err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	newState := !isEnabled
	if _, err := siteDB.ExecWrite(
		"UPDATE ho_scheduled_tasks SET is_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		newState, taskID,
	); err != nil {
		h.deps.Logger.Error("failed to toggle task", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to toggle task")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"is_enabled": newState})
}

// Delete removes a scheduled task.
func (h *SiteTasksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	taskID, err := strconv.Atoi(chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task ID")
		return
	}

	result, err := siteDB.ExecWrite("DELETE FROM ho_scheduled_tasks WHERE id = ?", taskID)
	if err != nil {
		h.deps.Logger.Error("failed to delete task", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete task")
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Create adds a new scheduled task for a site.
func (h *SiteTasksHandler) Create(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	var req struct {
		Name            string `json:"name"`
		Description     string `json:"description"`
		CronExpression  string `json:"cron_expression"`
		IntervalSeconds int    `json:"interval_seconds"`
		Prompt          string `json:"prompt"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "name and prompt are required")
		return
	}
	// Validate cron if provided
	if req.CronExpression != "" {
		if err := cron.Validate(req.CronExpression); err != nil {
			writeError(w, http.StatusBadRequest, "invalid cron expression: "+err.Error())
			return
		}
	}
	now := time.Now()
	var nextRun *time.Time
	if req.CronExpression != "" {
		nr := cron.NextTime(req.CronExpression, now)
		nextRun = &nr
	} else if req.IntervalSeconds > 0 {
		nr := now.Add(time.Duration(req.IntervalSeconds) * time.Second)
		nextRun = &nr
	}
	result, err := siteDB.ExecWrite(
		"INSERT INTO ho_scheduled_tasks (name, description, cron_expression, interval_seconds, prompt, next_run) VALUES (?, ?, ?, ?, ?, ?)",
		req.Name, req.Description, req.CronExpression, req.IntervalSeconds, req.Prompt, nextRun,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task")
		return
	}
	id, _ := result.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": id, "name": req.Name})
}

// Update modifies an existing scheduled task.
func (h *SiteTasksHandler) Update(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	taskID, err := strconv.Atoi(chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task ID")
		return
	}

	var req struct {
		Name            *string `json:"name"`
		Description     *string `json:"description"`
		CronExpression  *string `json:"cron_expression"`
		IntervalSeconds *int    `json:"interval_seconds"`
		Prompt          *string `json:"prompt"`
		IsEnabled       *bool   `json:"is_enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	setClauses := []string{"updated_at = CURRENT_TIMESTAMP"}
	var values []interface{}
	scheduleChanged := false
	var newCron string
	var newInterval int
	if req.Name != nil {
		setClauses = append(setClauses, "name = ?")
		values = append(values, *req.Name)
	}
	if req.Description != nil {
		setClauses = append(setClauses, "description = ?")
		values = append(values, *req.Description)
	}
	if req.CronExpression != nil {
		if *req.CronExpression != "" {
			if err := cron.Validate(*req.CronExpression); err != nil {
				writeError(w, http.StatusBadRequest, "invalid cron expression: "+err.Error())
				return
			}
		}
		setClauses = append(setClauses, "cron_expression = ?")
		values = append(values, *req.CronExpression)
		newCron = *req.CronExpression
		scheduleChanged = true
	}
	if req.IntervalSeconds != nil {
		setClauses = append(setClauses, "interval_seconds = ?")
		values = append(values, *req.IntervalSeconds)
		newInterval = *req.IntervalSeconds
		scheduleChanged = true
	}
	if req.Prompt != nil {
		setClauses = append(setClauses, "prompt = ?")
		values = append(values, *req.Prompt)
	}
	if req.IsEnabled != nil {
		setClauses = append(setClauses, "is_enabled = ?")
		values = append(values, *req.IsEnabled)
	}
	values = append(values, taskID)
	query := fmt.Sprintf("UPDATE ho_scheduled_tasks SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	_, err = siteDB.ExecWrite(query, values...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update task")
		return
	}
	if scheduleChanged {
		now := time.Now()
		var nextRun time.Time
		if newCron != "" {
			nextRun = cron.NextTime(newCron, now)
		} else if newInterval > 0 {
			nextRun = now.Add(time.Duration(newInterval) * time.Second)
		}
		if !nextRun.IsZero() {
			siteDB.ExecWrite("UPDATE ho_scheduled_tasks SET next_run = ? WHERE id = ?", nextRun, taskID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": taskID, "status": "updated"})
}

// TaskRuns returns recent runs for a scheduled task.
func (h *SiteTasksHandler) TaskRuns(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	taskID, err := strconv.Atoi(chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task ID")
		return
	}
	rows, err := siteDB.Query(
		`SELECT id, status, error_message, started_at, completed_at
		 FROM ho_task_runs WHERE task_id = ? ORDER BY started_at DESC LIMIT 50`, taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task runs")
		return
	}
	defer rows.Close()
	var runs []map[string]interface{}
	for rows.Next() {
		var id int
		var status string
		var errorMsg sql.NullString
		var startedAt time.Time
		var completedAt sql.NullTime
		if err := rows.Scan(&id, &status, &errorMsg, &startedAt, &completedAt); err != nil {
			continue
		}
		run := map[string]interface{}{"id": id, "status": status, "started_at": startedAt}
		if errorMsg.Valid {
			run["error_message"] = errorMsg.String
		}
		if completedAt.Valid {
			run["completed_at"] = completedAt.Time
		}
		runs = append(runs, run)
	}
	if runs == nil {
		runs = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, runs)
}
