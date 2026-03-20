/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package chat

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/markdr-hue/HO/prompt"
	"github.com/markdr-hue/HO/tools"
)

// BuildChatSystemPrompt creates a compact system prompt for user chat sessions
// that includes site context (memory keys, pages, pending questions).
// This gives the chat LLM enough awareness to answer user questions about their site.
// If registry is non-nil, it generates a compact tool guide from the registry;
// otherwise, no tool guide section is emitted.
func BuildChatSystemPrompt(globalDB, siteDB *sql.DB, siteID int, registry *tools.Registry, allowed map[string]bool) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Current date: %s\n\n", time.Now().UTC().Format("2006-01-02")))

	// Owner personalization.
	var ownerName string
	if err := globalDB.QueryRow("SELECT display_name FROM users WHERE role = 'admin' ORDER BY id LIMIT 1").Scan(&ownerName); err != nil && err != sql.ErrNoRows {
		slog.Debug("prompt: failed to fetch owner name", "error", err)
	}
	if ownerName != "" {
		sb.WriteString(fmt.Sprintf("The user's name is %s. Address them by name when appropriate.\n\n", ownerName))
	}

	// Site info.
	var siteName, siteMode, siteDesc sql.NullString
	if err := globalDB.QueryRow("SELECT name, mode, description FROM sites WHERE id = ?", siteID).Scan(&siteName, &siteMode, &siteDesc); err != nil && err != sql.ErrNoRows {
		slog.Debug("prompt: failed to fetch site info", "site_id", siteID, "error", err)
	}
	if siteName.Valid {
		sb.WriteString(fmt.Sprintf("## Current Site: %s\n", siteName.String))
		if siteMode.Valid {
			sb.WriteString(fmt.Sprintf("Mode: %s\n", siteMode.String))
		}
		if siteDesc.Valid && siteDesc.String != "" {
			sb.WriteString(siteDesc.String + "\n")
		}
		sb.WriteString("\n")
	}

	// Pages summary.
	var pageCount int
	if err := siteDB.QueryRow("SELECT COUNT(*) FROM ho_pages WHERE is_deleted = 0").Scan(&pageCount); err != nil {
		slog.Debug("prompt: failed to fetch page count", "error", err)
	}
	if pageCount > 0 {
		sb.WriteString(fmt.Sprintf("## Pages (%d)\n", pageCount))
		if rows, err := siteDB.Query("SELECT path, title FROM ho_pages WHERE is_deleted = 0 ORDER BY path LIMIT 15"); err == nil {
			defer rows.Close()
			for rows.Next() {
				var path string
				var title sql.NullString
				if rows.Scan(&path, &title) == nil {
					if title.Valid && title.String != "" {
						sb.WriteString(fmt.Sprintf("- %s (%s)\n", path, title.String))
					} else {
						sb.WriteString(fmt.Sprintf("- %s\n", path))
					}
				}
			}
			if pageCount > 15 {
				sb.WriteString(fmt.Sprintf("- ... and %d more\n", pageCount-15))
			}
		}
		sb.WriteString("\n")
	}

	// Data layer — API endpoints, WebSocket, SSE, uploads.
	prompt.WriteDataLayerSummary(&sb, siteDB)

	// Pending questions.
	if rows, err := siteDB.Query("SELECT question FROM ho_questions WHERE status = 'pending' ORDER BY id LIMIT 5"); err == nil {
		defer rows.Close()
		var pending []string
		for rows.Next() {
			var q string
			if rows.Scan(&q) == nil {
				pending = append(pending, "- "+q)
			}
		}
		if len(pending) > 0 {
			sb.WriteString("## Pending Questions (awaiting owner)\n")
			sb.WriteString(strings.Join(pending, "\n"))
			sb.WriteString("\n\n")
		}
	}

	// Inject persistent memories from past sessions.
	prompt.WriteMemories(&sb, siteDB)

	// Inject design tokens so the chat LLM can use them for styling changes.
	var planJSON sql.NullString
	if err := siteDB.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSON); err != nil && err != sql.ErrNoRows {
		slog.Debug("prompt: failed to fetch plan JSON", "error", err)
	}
	if planJSON.Valid && planJSON.String != "" {
		var planMap map[string]json.RawMessage
		if json.Unmarshal([]byte(planJSON.String), &planMap) == nil {
			if ds, ok := planMap["design_system"]; ok && string(ds) != "null" {
				prompt.WriteDesignTokens(&sb, ds, prompt.DetailCompact)
			}
		}
	}

	// CSS/JS references — compact structured summaries to save tokens.
	prompt.WriteCSSReference(&sb, siteDB)
	prompt.WriteJSReference(&sb, siteDB)

	// Tool guide — compact one-liners from registry.
	if registry != nil {
		prompt.WriteToolGuide(&sb, registry, allowed, prompt.GuideCompact)
	}

	sb.WriteString(`## Rules
- Prefer patch actions for targeted fixes — avoid rewriting entire files
- Fix only what the owner asked for — no bonus improvements
- Use design system tokens (CSS custom properties) for any new styling
- Use @media queries in CSS for responsive breakpoints.
- After making changes, briefly confirm what you did
- Do NOT rebuild the entire site — make targeted fixes only
`)

	return sb.String()
}

// BuildDataLayerSummary is kept as a convenience wrapper for backward compatibility.
// Deprecated: use prompt.WriteDataLayerSummary instead.
func BuildDataLayerSummary(siteDB *sql.DB) string {
	var sb strings.Builder
	prompt.WriteDataLayerSummary(&sb, siteDB)
	return sb.String()
}
