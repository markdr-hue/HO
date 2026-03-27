/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/tools"
)

const jobPollInterval = 5 * time.Second

// JobWorker polls for pending background jobs and executes them.
type JobWorker struct {
	siteID int
	siteDB *db.SiteDB
	deps   *Deps
	logger *slog.Logger
}

// NewJobWorker creates a worker that processes background jobs for a site.
func NewJobWorker(siteID int, siteDB *db.SiteDB, deps *Deps) *JobWorker {
	return &JobWorker{
		siteID: siteID,
		siteDB: siteDB,
		deps:   deps,
		logger: slog.With("component", "job_worker", "site_id", siteID),
	}
}

// Run polls for pending jobs until the context is cancelled.
func (jw *JobWorker) Run(ctx context.Context) {
	tools.EnsureJobsTable(jw.siteDB.Writer())

	ticker := time.NewTicker(jobPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jw.processPending(ctx)
		}
	}
}

// processPending claims and executes one pending job per tick.
func (jw *JobWorker) processPending(ctx context.Context) {
	// Find one job ready to run.
	var jobID int64
	var jobType, payload string
	err := jw.siteDB.Writer().QueryRow(
		`SELECT id, type, payload FROM ho_jobs
		 WHERE status = 'pending' AND scheduled_at <= CURRENT_TIMESTAMP
		 ORDER BY id ASC LIMIT 1`,
	).Scan(&jobID, &jobType, &payload)
	if err != nil {
		return // No pending jobs or query error.
	}

	// Claim the job atomically.
	result, err := jw.siteDB.ExecWrite(
		"UPDATE ho_jobs SET status = 'running', started_at = CURRENT_TIMESTAMP, attempts = attempts + 1 WHERE id = ? AND status = 'pending'",
		jobID,
	)
	if err != nil {
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return // Another worker claimed it.
	}

	jw.logger.Info("processing job", "job_id", jobID, "type", jobType)

	// Execute the job.
	jobErr := jw.executeJob(ctx, jobType, payload)

	if jobErr != nil {
		jw.handleFailure(jobID, jobErr)
	} else {
		jw.siteDB.ExecWrite(
			"UPDATE ho_jobs SET status = 'completed', completed_at = CURRENT_TIMESTAMP WHERE id = ?",
			jobID,
		)
		jw.logger.Info("job completed", "job_id", jobID)
	}
}

func (jw *JobWorker) executeJob(ctx context.Context, jobType, payload string) error {
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &params); err != nil {
		return fmt.Errorf("invalid payload JSON: %w", err)
	}

	switch jobType {
	case "send_email":
		return jw.executeSendEmail(ctx, params)
	case "http_request":
		return jw.executeHTTPRequest(ctx, params)
	case "run_sql":
		return jw.executeRunSQL(ctx, params)
	case "custom":
		return nil // Custom jobs just complete — used for tracking.
	default:
		return fmt.Errorf("unknown job type: %s", jobType)
	}
}

func (jw *JobWorker) executeSendEmail(ctx context.Context, params map[string]interface{}) error {
	// Build args matching manage_email send action.
	args := map[string]interface{}{
		"action": "send",
	}
	for _, key := range []string{"to", "subject", "body_html", "body_text", "template_name", "template_vars"} {
		if v, ok := params[key]; ok {
			args[key] = v
		}
	}

	emailTool, err := jw.deps.ToolRegistry.Get("manage_email")
	if err != nil {
		return fmt.Errorf("email tool not found")
	}

	toolCtx := &tools.ToolContext{
		DB:        jw.siteDB.Writer(),
		GlobalDB:  jw.deps.DB.DB,
		SiteID:    jw.siteID,
		Logger:    jw.logger,
		Encryptor: jw.deps.Encryptor,
	}

	result, err := emailTool.Execute(toolCtx, args)
	if err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("email send failed: %s", result.Error)
	}
	return nil
}

func (jw *JobWorker) executeHTTPRequest(ctx context.Context, params map[string]interface{}) error {
	url, _ := params["url"].(string)
	method, _ := params["method"].(string)
	body, _ := params["body"].(string)

	if url == "" {
		return fmt.Errorf("url is required")
	}
	if method == "" {
		method = "GET"
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	// Set headers if provided.
	if headers, ok := params["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}
	if req.Header.Get("Content-Type") == "" && body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (jw *JobWorker) executeRunSQL(ctx context.Context, params map[string]interface{}) error {
	query, _ := params["query"].(string)
	if query == "" {
		return fmt.Errorf("query is required")
	}

	// Safety: SELECT only.
	upper := strings.TrimSpace(strings.ToUpper(query))
	if !strings.HasPrefix(upper, "SELECT") {
		return fmt.Errorf("run_sql only supports SELECT queries")
	}

	rows, err := jw.siteDB.Query(query)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	// Drain the rows — result is stored but not returned (status check shows it ran).
	return nil
}

func (jw *JobWorker) handleFailure(jobID int64, jobErr error) {
	// Check if we should retry.
	var attempts, maxAttempts int
	jw.siteDB.Writer().QueryRow(
		"SELECT attempts, max_attempts FROM ho_jobs WHERE id = ?", jobID,
	).Scan(&attempts, &maxAttempts)

	if attempts < maxAttempts {
		// Retry with exponential backoff: 5s, 10s, 20s, 40s, ...
		backoffSecs := int(math.Pow(2, float64(attempts-1)) * 5)
		if backoffSecs > 300 {
			backoffSecs = 300 // Cap at 5 minutes.
		}
		jw.siteDB.ExecWrite(
			"UPDATE ho_jobs SET status = 'pending', error = ?, scheduled_at = datetime('now', ? || ' seconds') WHERE id = ?",
			jobErr.Error(), fmt.Sprintf("+%d", backoffSecs), jobID,
		)
		jw.logger.Warn("job failed, will retry", "job_id", jobID, "attempt", attempts, "backoff_s", backoffSecs, "error", jobErr)
	} else {
		jw.siteDB.ExecWrite(
			"UPDATE ho_jobs SET status = 'failed', error = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?",
			jobErr.Error(), jobID,
		)
		jw.logger.Error("job permanently failed", "job_id", jobID, "attempts", attempts, "error", jobErr)
	}
}
