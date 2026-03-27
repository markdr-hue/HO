/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package prompt

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// LoadRows is a generic helper for scanning SQL rows into a typed slice.
func LoadRows[T any](db *sql.DB, query string, args []interface{}, scan func(*sql.Rows) (T, bool)) []T {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []T
	for rows.Next() {
		if v, ok := scan(rows); ok {
			result = append(result, v)
		}
	}
	return result
}

// SiteContext holds basic site metadata for prompt construction.
type SiteContext struct {
	Name, Domain, Mode, Description string
}

// LoadSiteContext fetches site metadata from the global database.
func LoadSiteContext(db *sql.DB, siteID int) *SiteContext {
	var s SiteContext
	var domain, description sql.NullString
	err := db.QueryRow(
		"SELECT name, domain, mode, description FROM sites WHERE id = ?",
		siteID,
	).Scan(&s.Name, &domain, &s.Mode, &description)
	if err != nil {
		return nil
	}
	s.Domain = domain.String
	s.Description = description.String
	return &s
}

// LoadRecentErrors returns the most recent error summaries from ho_brain_log.
func LoadRecentErrors(db *sql.DB) []string {
	return LoadRows(db,
		"SELECT COALESCE(summary, '') FROM ho_brain_log WHERE event_type = 'error' AND summary != '' ORDER BY created_at DESC LIMIT 5",
		nil, func(r *sql.Rows) (string, bool) {
			var s string
			return s, r.Scan(&s) == nil && s != ""
		})
}

// LoadAnalyticsSummary returns a compact analytics summary for the past 7 days.
func LoadAnalyticsSummary(db *sql.DB) string {
	var totalViews, uniqueVisitors int
	db.QueryRow(
		"SELECT COUNT(*), COUNT(DISTINCT visitor_hash) FROM ho_analytics WHERE created_at >= datetime('now', '-7 days')",
	).Scan(&totalViews, &uniqueVisitors)

	if totalViews == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("- Views: %d, Unique: %d\n", totalViews, uniqueVisitors))

	topPages := LoadRows(db,
		"SELECT page_path, COUNT(*) as views FROM ho_analytics WHERE created_at >= datetime('now', '-7 days') GROUP BY page_path ORDER BY views DESC LIMIT 5",
		nil, func(r *sql.Rows) (string, bool) {
			var path string
			var views int
			if r.Scan(&path, &views) == nil {
				return fmt.Sprintf("%s (%d)", path, views), true
			}
			return "", false
		})
	if len(topPages) > 0 {
		sb.WriteString("- Top: " + strings.Join(topPages, ", ") + "\n")
	}
	return sb.String()
}

// LoadSiteManifest returns a compact summary of pages and assets for prompts.
func LoadSiteManifest(db *sql.DB) string {
	var b strings.Builder
	hasContent := false

	type pageEntry struct{ path, title, status string }
	pages := LoadRows(db,
		"SELECT path, COALESCE(title, ''), status FROM ho_pages WHERE is_deleted = 0 ORDER BY path LIMIT 50",
		nil, func(r *sql.Rows) (pageEntry, bool) {
			var p pageEntry
			return p, r.Scan(&p.path, &p.title, &p.status) == nil
		})
	if len(pages) > 0 {
		if !hasContent {
			b.WriteString("## Site Map\n")
			hasContent = true
		}
		b.WriteString("Pages: ")
		for i, p := range pages {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(p.path)
			if p.title != "" {
				b.WriteString(fmt.Sprintf(" \"%s\"", p.title))
			}
			if p.status == "draft" {
				b.WriteString(" [draft]")
			}
		}
		b.WriteString("\n")
	}

	type assetEntry struct{ filename, scope string }
	assets := LoadRows(db,
		"SELECT filename, COALESCE(scope, 'global') FROM ho_assets ORDER BY scope, filename LIMIT 50",
		nil, func(r *sql.Rows) (assetEntry, bool) {
			var a assetEntry
			return a, r.Scan(&a.filename, &a.scope) == nil
		})
	if len(assets) > 0 {
		if !hasContent {
			b.WriteString("## Site Map\n")
			hasContent = true
		}
		var global, paged []string
		for _, a := range assets {
			if a.scope == "page" {
				paged = append(paged, a.filename)
			} else {
				global = append(global, a.filename)
			}
		}
		if len(global) > 0 {
			b.WriteString("Global assets: " + strings.Join(global, ", ") + "\n")
		}
		if len(paged) > 0 {
			b.WriteString("Page assets: " + strings.Join(paged, ", ") + "\n")
		}
	}

	if hasContent {
		b.WriteString("\n")
	}
	return b.String()
}

// LoadPlanSummary returns a compact plan summary for prompts.
func LoadPlanSummary(db *sql.DB) string {
	var planJSON sql.NullString
	db.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSON)
	if !planJSON.Valid || planJSON.String == "" {
		return ""
	}

	// Parse enough plan fields to build a summary without importing brain.Plan.
	var plan struct {
		AppType      string `json:"app_type"`
		Architecture string `json:"architecture"`
		AuthStrategy string `json:"auth_strategy"`
		Endpoints    []struct {
			Action string `json:"action"`
			Path   string `json:"path"`
		} `json:"endpoints"`
		Tables []struct {
			Name string `json:"name"`
		} `json:"tables"`
	}
	if err := json.Unmarshal([]byte(planJSON.String), &plan); err != nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Plan Summary\n")
	b.WriteString(fmt.Sprintf("- Type: %s, Architecture: %s, Auth: %s\n", plan.AppType, plan.Architecture, plan.AuthStrategy))

	if len(plan.Endpoints) > 0 {
		b.WriteString("- Endpoints: ")
		for i, ep := range plan.Endpoints {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%s /api/%s", ep.Action, ep.Path))
		}
		b.WriteString("\n")
	}
	if len(plan.Tables) > 0 {
		b.WriteString("- Tables: ")
		for i, t := range plan.Tables {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(t.Name)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// LoadGlobalCSS returns the content of the first global CSS asset.
func LoadGlobalCSS(db *sql.DB) string {
	var content sql.NullString
	db.QueryRow(
		"SELECT content FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.css' ORDER BY filename LIMIT 1",
	).Scan(&content)
	if content.Valid {
		return content.String
	}
	return ""
}

// LoadGlobalJS returns the concatenated content of all global JS assets.
func LoadGlobalJS(db *sql.DB) string {
	rows, err := db.Query(
		"SELECT content FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.js' ORDER BY filename LIMIT 3",
	)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var content string
		if rows.Scan(&content) == nil && content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n// --- next file ---\n\n")
}

// WriteMemories loads persistent memories from ho_memory and appends them
// to the prompt builder. Returns the number of memories written.
func WriteMemories(b *strings.Builder, db *sql.DB) int {
	rows, err := db.Query("SELECT key, value, category FROM ho_memory ORDER BY updated_at DESC LIMIT 20")
	if err != nil {
		return 0
	}
	defer rows.Close()

	var memories []string
	for rows.Next() {
		var key, value, category string
		if rows.Scan(&key, &value, &category) == nil {
			memories = append(memories, fmt.Sprintf("[%s] %s: %s", category, key, value))
		}
	}
	if len(memories) == 0 {
		return 0
	}
	b.WriteString("## Memory (from past sessions)\n")
	for _, m := range memories {
		b.WriteString("- " + m + "\n")
	}
	b.WriteString("\n")
	return len(memories)
}

// LoadDesignSystemJSON extracts the raw design_system JSON from the plan.
func LoadDesignSystemJSON(db *sql.DB) []byte {
	var planJSON sql.NullString
	db.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSON)
	if !planJSON.Valid || planJSON.String == "" {
		return nil
	}
	var planMap map[string]json.RawMessage
	if json.Unmarshal([]byte(planJSON.String), &planMap) != nil {
		return nil
	}
	ds, ok := planMap["design_system"]
	if !ok || string(ds) == "null" {
		return nil
	}
	return ds
}
