/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"fmt"
)

// ---------------------------------------------------------------------------
// JobsTool — manage_jobs
// ---------------------------------------------------------------------------

// JobsTool provides background job enqueue, status, list, and cancel operations.
// Jobs are executed by a background worker goroutine in the brain package.
type JobsTool struct{}

func (t *JobsTool) Name() string { return "manage_jobs" }
func (t *JobsTool) Description() string {
	return "Enqueue, list, check status, or cancel background jobs."
}

func (t *JobsTool) Guide() string {
	return `### Background Jobs (manage_jobs)
- **enqueue**: Create a background job. Params: type ("send_email"|"http_request"|"run_sql"|"custom"), payload (JSON with job-specific params), scheduled_at (optional, ISO datetime for delayed execution), max_attempts (default 3).
- **status**: Check job status by ID. Returns status, attempts, result/error.
- **list**: List jobs. Params: status (filter), type (filter), limit (default 50).
- **cancel**: Cancel a pending or running job by ID.
Jobs retry with exponential backoff on failure. send_email payload: {to, subject, body_html}. http_request payload: {url, method, headers, body}. run_sql payload: {query} (SELECT only).`
}

func (t *JobsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"enqueue", "status", "list", "cancel"},
				"description": "Action to perform",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Job type: send_email, http_request, run_sql, custom. For enqueue; also filter for list.",
				"enum":        []string{"send_email", "http_request", "run_sql", "custom"},
			},
			"payload": map[string]interface{}{
				"type":        "string",
				"description": "JSON string with job parameters. For enqueue.",
			},
			"scheduled_at": map[string]interface{}{
				"type":        "string",
				"description": "ISO datetime for delayed execution (optional). For enqueue.",
			},
			"max_attempts": map[string]interface{}{
				"type":        "number",
				"description": "Max retry attempts (default 3). For enqueue.",
			},
			"id": map[string]interface{}{
				"type":        "number",
				"description": "Job ID. For status, cancel.",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"description": "Filter by status (pending, running, completed, failed, cancelled). For list.",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Max results (default 50). For list.",
			},
		},
		"required": []string{},
	}
}

func (t *JobsTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"enqueue": t.enqueue,
		"status":  t.status,
		"list":    t.list,
		"cancel":  t.cancel,
	}, nil)
}

// EnsureJobsTable creates the jobs table if it doesn't exist.
// Exported so the brain job worker can call it too.
func EnsureJobsTable(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS ho_jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		payload TEXT DEFAULT '{}',
		status TEXT DEFAULT 'pending',
		attempts INTEGER DEFAULT 0,
		max_attempts INTEGER DEFAULT 3,
		result TEXT DEFAULT '',
		error TEXT DEFAULT '',
		scheduled_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME,
		completed_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
}

func (t *JobsTool) enqueue(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	jobType, errResult := RequireString(args, "type")
	if errResult != nil {
		return errResult, nil
	}

	validTypes := map[string]bool{"send_email": true, "http_request": true, "run_sql": true, "custom": true}
	if !validTypes[jobType] {
		return &Result{Success: false, Error: "type must be one of: send_email, http_request, run_sql, custom"}, nil
	}

	payload := OptionalString(args, "payload", "{}")
	scheduledAt := OptionalString(args, "scheduled_at", "")
	maxAttempts := OptionalInt(args, "max_attempts", 3)
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if maxAttempts > 10 {
		maxAttempts = 10
	}

	EnsureJobsTable(ctx.DB)

	var result sql.Result
	var err error
	if scheduledAt != "" {
		result, err = ctx.DB.Exec(
			"INSERT INTO ho_jobs (type, payload, max_attempts, scheduled_at) VALUES (?, ?, ?, ?)",
			jobType, payload, maxAttempts, scheduledAt,
		)
	} else {
		result, err = ctx.DB.Exec(
			"INSERT INTO ho_jobs (type, payload, max_attempts) VALUES (?, ?, ?)",
			jobType, payload, maxAttempts,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("enqueuing job: %w", err)
	}

	id, _ := result.LastInsertId()

	return &Result{Success: true, Data: map[string]interface{}{
		"id":           id,
		"type":         jobType,
		"status":       "pending",
		"max_attempts": maxAttempts,
	}}, nil
}

func (t *JobsTool) status(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	id := OptionalInt(args, "id", 0)
	if id <= 0 {
		return &Result{Success: false, Error: "id is required"}, nil
	}

	EnsureJobsTable(ctx.DB)

	var jobType, status, payload, jobResult, jobError string
	var attempts, maxAttempts int
	var scheduledAt, startedAt, completedAt, createdAt sql.NullString

	err := ctx.DB.QueryRow(
		`SELECT type, payload, status, attempts, max_attempts, result, error,
		        scheduled_at, started_at, completed_at, created_at
		 FROM ho_jobs WHERE id = ?`, id,
	).Scan(&jobType, &payload, &status, &attempts, &maxAttempts, &jobResult, &jobError,
		&scheduledAt, &startedAt, &completedAt, &createdAt)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("job %d not found", id)}, nil
	}

	data := map[string]interface{}{
		"id":           id,
		"type":         jobType,
		"status":       status,
		"attempts":     attempts,
		"max_attempts": maxAttempts,
	}
	if jobResult != "" {
		data["result"] = jobResult
	}
	if jobError != "" {
		data["error"] = jobError
	}
	if scheduledAt.Valid {
		data["scheduled_at"] = scheduledAt.String
	}
	if startedAt.Valid {
		data["started_at"] = startedAt.String
	}
	if completedAt.Valid {
		data["completed_at"] = completedAt.String
	}

	return &Result{Success: true, Data: data}, nil
}

func (t *JobsTool) list(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	EnsureJobsTable(ctx.DB)

	statusFilter := OptionalString(args, "status", "")
	typeFilter := OptionalString(args, "type", "")
	limit := OptionalInt(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}

	query := "SELECT id, type, status, attempts, max_attempts, error, created_at FROM ho_jobs"
	var conditions []string
	var queryArgs []interface{}

	if statusFilter != "" {
		conditions = append(conditions, "status = ?")
		queryArgs = append(queryArgs, statusFilter)
	}
	if typeFilter != "" {
		conditions = append(conditions, "type = ?")
		queryArgs = append(queryArgs, typeFilter)
	}

	if len(conditions) > 0 {
		query += " WHERE " + fmt.Sprintf("%s", joinAnd(conditions))
	}
	query += " ORDER BY id DESC LIMIT ?"
	queryArgs = append(queryArgs, limit)

	rows, err := ctx.DB.Query(query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	defer rows.Close()

	var jobs []map[string]interface{}
	for rows.Next() {
		var id, attempts, maxAttempts int
		var jobType, status, jobError, createdAt string
		if rows.Scan(&id, &jobType, &status, &attempts, &maxAttempts, &jobError, &createdAt) != nil {
			continue
		}
		job := map[string]interface{}{
			"id":           id,
			"type":         jobType,
			"status":       status,
			"attempts":     attempts,
			"max_attempts": maxAttempts,
			"created_at":   createdAt,
		}
		if jobError != "" {
			job["error"] = jobError
		}
		jobs = append(jobs, job)
	}

	return &Result{Success: true, Data: jobs}, nil
}

func (t *JobsTool) cancel(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	id := OptionalInt(args, "id", 0)
	if id <= 0 {
		return &Result{Success: false, Error: "id is required"}, nil
	}

	EnsureJobsTable(ctx.DB)

	result, err := ctx.DB.Exec(
		"UPDATE ho_jobs SET status = 'cancelled' WHERE id = ? AND status IN ('pending', 'running')",
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("cancelling job: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("job %d not found or already completed/cancelled", id)}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"id":     id,
		"status": "cancelled",
	}}, nil
}

// joinAnd joins conditions with " AND ".
func joinAnd(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	out := conditions[0]
	for _, c := range conditions[1:] {
		out += " AND " + c
	}
	return out
}
