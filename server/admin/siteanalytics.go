/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"log/slog"
	"net/http"
	"time"
)

// SiteAnalyticsHandler handles analytics data for admin.
type SiteAnalyticsHandler struct {
	deps *Deps
}

type analyticsSummary struct {
	TotalViews     int            `json:"total_views"`
	UniqueVisitors int            `json:"unique_visitors"`
	TopPages       []topPageEntry `json:"top_pages"`
	TopReferrers   []topRefEntry  `json:"top_referrers"`
	Daily          []dailyEntry   `json:"daily"`
	Start          string         `json:"start"`
	End            string         `json:"end"`
}

type topPageEntry struct {
	Path  string `json:"path"`
	Views int    `json:"views"`
}

type topRefEntry struct {
	Referrer string `json:"referrer"`
	Count    int    `json:"count"`
}

type dailyEntry struct {
	Date           string `json:"date"`
	Views          int    `json:"views"`
	UniqueVisitors int    `json:"unique_visitors"`
}

// Summary returns analytics data for a date range.
func (h *SiteAnalyticsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	// Parse date range (defaults to last 7 days)
	end := r.URL.Query().Get("end")
	start := r.URL.Query().Get("start")
	if end == "" {
		end = time.Now().Format("2006-01-02")
	}
	if start == "" {
		start = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	}

	// End date is inclusive — add one day for the query
	endExclusive := start // fallback
	if t, err := time.Parse("2006-01-02", end); err == nil {
		endExclusive = t.AddDate(0, 0, 1).Format("2006-01-02")
	}

	summary := analyticsSummary{Start: start, End: end}

	// Totals
	if err := siteDB.QueryRow(
		"SELECT COUNT(*), COUNT(DISTINCT visitor_hash) FROM ho_analytics WHERE created_at >= ? AND created_at < ?",
		start, endExclusive,
	).Scan(&summary.TotalViews, &summary.UniqueVisitors); err != nil {
		slog.Debug("analytics: failed to fetch totals", "error", err)
	}

	// Top pages
	rows, err := siteDB.Query(
		"SELECT page_path, COUNT(*) as views FROM ho_analytics WHERE created_at >= ? AND created_at < ? GROUP BY page_path ORDER BY views DESC LIMIT 10",
		start, endExclusive,
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var p topPageEntry
			if rows.Scan(&p.Path, &p.Views) == nil {
				summary.TopPages = append(summary.TopPages, p)
			}
		}
	}

	// Top referrers
	rows2, err := siteDB.Query(
		"SELECT referrer, COUNT(*) as cnt FROM ho_analytics WHERE referrer IS NOT NULL AND referrer != '' AND created_at >= ? AND created_at < ? GROUP BY referrer ORDER BY cnt DESC LIMIT 10",
		start, endExclusive,
	)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var r topRefEntry
			if rows2.Scan(&r.Referrer, &r.Count) == nil {
				summary.TopReferrers = append(summary.TopReferrers, r)
			}
		}
	}

	// Daily breakdown
	rows3, err := siteDB.Query(
		"SELECT DATE(created_at) as date, COUNT(*) as views, COUNT(DISTINCT visitor_hash) as uniq FROM ho_analytics WHERE created_at >= ? AND created_at < ? GROUP BY DATE(created_at) ORDER BY date",
		start, endExclusive,
	)
	if err == nil {
		defer rows3.Close()
		for rows3.Next() {
			var d dailyEntry
			if rows3.Scan(&d.Date, &d.Views, &d.UniqueVisitors) == nil {
				summary.Daily = append(summary.Daily, d)
			}
		}
	}

	if summary.TopPages == nil {
		summary.TopPages = []topPageEntry{}
	}
	if summary.TopReferrers == nil {
		summary.TopReferrers = []topRefEntry{}
	}
	if summary.Daily == nil {
		summary.Daily = []dailyEntry{}
	}

	writeJSON(w, http.StatusOK, summary)
}
