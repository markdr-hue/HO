/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"fmt"
)

// AnalyticsTool consolidates query and summary into a single
// manage_analytics tool.
type AnalyticsTool struct{}

func (t *AnalyticsTool) Name() string { return "manage_analytics" }
func (t *AnalyticsTool) Description() string {
	return "Query raw analytics data or get a traffic summary."
}

func (t *AnalyticsTool) Guide() string {
	return `### Analytics (manage_analytics)
- **query**: Fetch raw analytics rows (page_path, visitor_hash, referrer, user_agent, country, created_at). Requires start_date and end_date (YYYY-MM-DD). Optional page_path filter. Max 1000 rows.
- **summary**: Get an overview for a date range: total views, unique visitors, top 10 pages by views, top 10 referrers, and daily view counts. Use this for dashboards and monitoring.
- Analytics are auto-collected for every page view — no setup required.
- visitor_hash is an anonymous hash (IP + user agent), not PII.
- Use summary for monitoring health checks. Use query for detailed investigation.`
}

func (t *AnalyticsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"query", "summary"},
			},
			"start_date": map[string]interface{}{"type": "string", "description": "Start date in YYYY-MM-DD format"},
			"end_date":   map[string]interface{}{"type": "string", "description": "End date in YYYY-MM-DD format"},
			"page_path":  map[string]interface{}{"type": "string", "description": "Filter by specific page path (optional, for query action)"},
		},
		"required": []string{"start_date", "end_date"},
	}
}

func (t *AnalyticsTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"query":   t.executeQuery,
		"summary": t.executeSummary,
	}, nil)
}

func (t *AnalyticsTool) executeQuery(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	startDate, _ := args["start_date"].(string)
	endDate, _ := args["end_date"].(string)
	if startDate == "" || endDate == "" {
		return &Result{Success: false, Error: "start_date and end_date are required"}, nil
	}

	query := `SELECT page_path, visitor_hash, referrer, user_agent, country, created_at
		FROM ho_analytics WHERE created_at >= ? AND created_at <= ?`
	queryArgs := []interface{}{startDate, endDate + " 23:59:59"}

	if pagePath, ok := args["page_path"].(string); ok && pagePath != "" {
		query += " AND page_path = ?"
		queryArgs = append(queryArgs, pagePath)
	}
	query += " ORDER BY created_at DESC LIMIT 1000"

	rows, err := ctx.DB.Query(query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("querying analytics: %w", err)
	}
	defer rows.Close()

	var records []map[string]interface{}
	for rows.Next() {
		var pagePath, createdAt string
		var visitorHash, referrer, userAgent, country *string
		if err := rows.Scan(&pagePath, &visitorHash, &referrer, &userAgent, &country, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning analytics: %w", err)
		}
		record := map[string]interface{}{
			"page_path":  pagePath,
			"created_at": createdAt,
		}
		if visitorHash != nil {
			record["visitor_hash"] = *visitorHash
		}
		if referrer != nil {
			record["referrer"] = *referrer
		}
		if country != nil {
			record["country"] = *country
		}
		records = append(records, record)
	}

	return &Result{Success: true, Data: records}, nil
}

func (t *AnalyticsTool) executeSummary(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	startDate, _ := args["start_date"].(string)
	endDate, _ := args["end_date"].(string)
	if startDate == "" || endDate == "" {
		return &Result{Success: false, Error: "start_date and end_date are required"}, nil
	}

	dateFilter := " AND created_at >= ? AND created_at <= ?"
	dateArgs := []interface{}{startDate, endDate + " 23:59:59"}

	// Total page views.
	var totalViews int
	err := ctx.DB.QueryRow(
		"SELECT COUNT(*) FROM ho_analytics WHERE 1=1"+dateFilter,
		dateArgs...,
	).Scan(&totalViews)
	if err != nil {
		return nil, fmt.Errorf("counting views: %w", err)
	}

	// Unique visitors.
	var uniqueVisitors int
	err = ctx.DB.QueryRow(
		"SELECT COUNT(DISTINCT visitor_hash) FROM ho_analytics WHERE 1=1"+dateFilter,
		dateArgs...,
	).Scan(&uniqueVisitors)
	if err != nil {
		return nil, fmt.Errorf("counting unique visitors: %w", err)
	}

	// Top pages.
	pageRows, err := ctx.DB.Query(
		"SELECT page_path, COUNT(*) as views FROM ho_analytics WHERE 1=1"+dateFilter+" GROUP BY page_path ORDER BY views DESC LIMIT 10",
		dateArgs...,
	)
	if err != nil {
		return nil, fmt.Errorf("querying top pages: %w", err)
	}
	defer pageRows.Close()

	var topPages []map[string]interface{}
	for pageRows.Next() {
		var path string
		var views int
		if err := pageRows.Scan(&path, &views); err != nil {
			return nil, fmt.Errorf("scanning top page: %w", err)
		}
		topPages = append(topPages, map[string]interface{}{
			"page_path": path,
			"views":     views,
		})
	}

	// Top referrers.
	refRows, err := ctx.DB.Query(
		"SELECT referrer, COUNT(*) as count FROM ho_analytics WHERE referrer IS NOT NULL AND referrer != ''"+dateFilter+" GROUP BY referrer ORDER BY count DESC LIMIT 10",
		dateArgs...,
	)
	if err != nil {
		return nil, fmt.Errorf("querying top referrers: %w", err)
	}
	defer refRows.Close()

	var topReferrers []map[string]interface{}
	for refRows.Next() {
		var referrer string
		var count int
		if err := refRows.Scan(&referrer, &count); err != nil {
			return nil, fmt.Errorf("scanning referrer: %w", err)
		}
		topReferrers = append(topReferrers, map[string]interface{}{
			"referrer": referrer,
			"count":    count,
		})
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"total_views":     totalViews,
		"unique_visitors": uniqueVisitors,
		"top_pages":       topPages,
		"top_referrers":   topReferrers,
		"start_date":      startDate,
		"end_date":        endDate,
	}}, nil
}
