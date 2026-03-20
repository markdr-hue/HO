/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// DiagnosticsTool consolidates health and errors into a single
// manage_diagnostics tool.
type DiagnosticsTool struct{}

func (t *DiagnosticsTool) Name() string { return "manage_diagnostics" }
func (t *DiagnosticsTool) Description() string {
	return "Check system health, recent errors, or page/asset integrity."
}

func (t *DiagnosticsTool) Guide() string {
	return `### manage_diagnostics
- health: system status overview (database, memory, uptime).
- errors: recent error log entries (default 20, configurable via limit).
- integrity: check pages and assets for missing references or broken links.`
}

func (t *DiagnosticsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"health", "errors", "integrity"},
			},
			"limit": map[string]interface{}{"type": "number", "description": "Maximum number of errors to return (default 20, for errors action)"},
		},
		"required": []string{},
	}
}

func (t *DiagnosticsTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"health":    t.executeHealth,
		"errors":    t.executeErrors,
		"integrity": t.executeIntegrity,
	}, nil)
}

func (t *DiagnosticsTool) executeHealth(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// DB stats.
	dbStats := ctx.DB.Stats()

	// Count pages for the site.
	var pageCount int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_pages").Scan(&pageCount)

	return &Result{Success: true, Data: map[string]interface{}{
		"runtime": map[string]interface{}{
			"go_version":    runtime.Version(),
			"num_goroutine": runtime.NumGoroutine(),
			"num_cpu":       runtime.NumCPU(),
			"os":            runtime.GOOS,
			"arch":          runtime.GOARCH,
		},
		"memory": map[string]interface{}{
			"alloc_mb":       m.Alloc / 1024 / 1024,
			"total_alloc_mb": m.TotalAlloc / 1024 / 1024,
			"sys_mb":         m.Sys / 1024 / 1024,
			"num_gc":         m.NumGC,
		},
		"database": map[string]interface{}{
			"open_connections": dbStats.OpenConnections,
			"in_use":           dbStats.InUse,
			"idle":             dbStats.Idle,
		},
		"site_stats": map[string]interface{}{
			"pages": pageCount,
		},
		"timestamp": time.Now(),
	}}, nil
}

func (t *DiagnosticsTool) executeErrors(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	rows, err := ctx.DB.Query(
		"SELECT id, event_type, summary, details, tokens_used, model, duration_ms, created_at FROM ho_brain_log WHERE event_type LIKE '%error%' ORDER BY created_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying errors: %w", err)
	}
	defer rows.Close()

	var errors []map[string]interface{}
	for rows.Next() {
		var id, tokensUsed, durationMs int
		var eventType, createdAt string
		var summary, details, model *string
		if err := rows.Scan(&id, &eventType, &summary, &details, &tokensUsed, &model, &durationMs, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning error: %w", err)
		}
		entry := map[string]interface{}{
			"id":          id,
			"event_type":  eventType,
			"tokens_used": tokensUsed,
			"duration_ms": durationMs,
			"created_at":  createdAt,
		}
		if summary != nil {
			entry["summary"] = *summary
		}
		if details != nil {
			entry["details"] = *details
		}
		if model != nil {
			entry["model"] = *model
		}
		errors = append(errors, entry)
	}

	return &Result{Success: true, Data: errors}, nil
}

func (t *DiagnosticsTool) executeIntegrity(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	var issues []string

	// 1. Check layout exists
	var layoutCount int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_layouts").Scan(&layoutCount)
	if layoutCount == 0 {
		issues = append(issues, "No layouts exist. Create one with manage_layout(action=\"save\", name=\"default\").")
	}

	// 2. Check pages reference valid layouts
	rows, err := ctx.DB.Query(
		"SELECT DISTINCT p.layout FROM ho_pages p WHERE p.is_deleted = 0 AND p.layout IS NOT NULL AND p.layout != '' AND p.layout != 'none' AND p.layout NOT IN (SELECT name FROM ho_layouts)",
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				issues = append(issues, fmt.Sprintf("Pages reference non-existent layout %q.", name))
			}
		}
	}

	// 3. Check pages for layout-level content (nav/footer in page body)
	pageRows, err := ctx.DB.Query(
		"SELECT path, content FROM ho_pages WHERE is_deleted = 0 AND status = 'published' LIMIT 50",
	)
	if err == nil {
		defer pageRows.Close()
		for pageRows.Next() {
			var path, content string
			if pageRows.Scan(&path, &content) == nil {
				lower := strings.ToLower(content)
				if strings.Contains(lower, "<nav") {
					issues = append(issues, fmt.Sprintf("Page %q contains <nav> (should be in layout).", path))
				}
				if strings.Contains(lower, "<footer") {
					issues = append(issues, fmt.Sprintf("Page %q contains <footer> (should be in layout).", path))
				}
			}
		}
	}

	// 4. Check CSS/JS assets exist
	var cssCount, jsCount int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_assets WHERE filename LIKE '%.css'").Scan(&cssCount)
	ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_assets WHERE filename LIKE '%.js'").Scan(&jsCount)
	if cssCount == 0 {
		issues = append(issues, "No CSS assets found. Design system may be incomplete.")
	}
	if jsCount == 0 {
		issues = append(issues, "No JS assets found (may be OK if no interactivity needed).")
	}

	status := "healthy"
	if len(issues) > 0 {
		status = "issues_found"
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"status": status,
		"issues": issues,
		"counts": map[string]interface{}{
			"layouts":    layoutCount,
			"css_assets": cssCount,
			"js_assets":  jsCount,
		},
	}}, nil
}

func (t *DiagnosticsTool) MaxResultSize() int { return 8000 }
