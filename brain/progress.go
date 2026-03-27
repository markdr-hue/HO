/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"fmt"
	"strings"
)

// buildProgressTracker tracks which plan items have been completed during BUILD.
type buildProgressTracker struct {
	plan                *Plan
	tablesTotal         int
	pagesTotal          int
	endpointsTotal      int
	tablesDone          map[string]bool
	pagesDone           map[string]bool
	endpointsDone       map[string]bool
	toolCallsSeen       int
	nudgeSent           bool
	postCompletionIters int
	readOnlyGraceIters  int  // consecutive grace iterations with only read tool calls
	checkpointed        bool // true after infrastructure checkpoint is saved
}

func newBuildProgressTracker(plan *Plan) *buildProgressTracker {
	return &buildProgressTracker{
		plan:           plan,
		tablesTotal:    len(plan.Tables),
		pagesTotal:     len(plan.Pages),
		endpointsTotal: len(plan.Endpoints),
		tablesDone:     make(map[string]bool),
		pagesDone:      make(map[string]bool),
		endpointsDone:  make(map[string]bool),
	}
}

// remainingItems returns a human-readable list of plan items not yet completed.
// Returns empty string if everything is done.
func (bp *buildProgressTracker) remainingItems() string {
	var items []string
	for _, t := range bp.plan.Tables {
		if !bp.tablesDone[t.Name] {
			items = append(items, "table: "+t.Name)
		}
	}
	for _, ep := range bp.plan.Endpoints {
		key := ep.Action + ":" + ep.Path
		if !bp.endpointsDone[key] {
			items = append(items, "endpoint: "+ep.Action+" "+ep.Path)
		}
	}
	for _, pg := range bp.plan.Pages {
		if !bp.pagesDone[pg.Path] {
			items = append(items, "page: "+pg.Path+" ("+pg.Title+")")
		}
	}
	if len(items) == 0 {
		return ""
	}
	return "REMINDER — these plan items are still missing. Create them now:\n- " + strings.Join(items, "\n- ")
}

// allComplete returns true when every plan item (tables, endpoints, pages) has been built.
func (bp *buildProgressTracker) allComplete() bool {
	return len(bp.tablesDone) >= bp.tablesTotal &&
		len(bp.endpointsDone) >= bp.endpointsTotal &&
		len(bp.pagesDone) >= bp.pagesTotal
}

// trackToolResult inspects a successful tool call and marks plan items as done.
func (bp *buildProgressTracker) trackToolResult(toolName string, args map[string]interface{}) {
	bp.toolCallsSeen++
	switch toolName {
	case "manage_schema":
		if action, _ := args["action"].(string); action == "create" {
			if name, _ := args["table_name"].(string); name != "" {
				bp.tablesDone[name] = true
			}
		}
	case "manage_pages":
		if action, _ := args["action"].(string); action == "save" || action == "" {
			if path, _ := args["path"].(string); path != "" {
				bp.pagesDone[path] = true
			}
		}
	case "manage_endpoints":
		if action, _ := args["action"].(string); strings.HasPrefix(action, "create_") {
			if path, _ := args["path"].(string); path != "" {
				bp.endpointsDone[action+":"+path] = true
			}
		}
	}
}

func (bp *buildProgressTracker) toPayload() map[string]interface{} {
	return map[string]interface{}{
		"tables_done":     len(bp.tablesDone),
		"tables_total":    bp.tablesTotal,
		"pages_done":      len(bp.pagesDone),
		"pages_total":     bp.pagesTotal,
		"endpoints_done":  len(bp.endpointsDone),
		"endpoints_total": bp.endpointsTotal,
		"tool_calls":      bp.toolCallsSeen,
	}
}

// infrastructureComplete returns true when all tables and endpoints are done
// but at least one page remains. This marks the boundary between the infrastructure
// phase and the page-building phase — the ideal checkpoint moment.
func (bp *buildProgressTracker) infrastructureComplete() bool {
	tablesReady := bp.tablesTotal == 0 || len(bp.tablesDone) >= bp.tablesTotal
	endpointsReady := bp.endpointsTotal == 0 || len(bp.endpointsDone) >= bp.endpointsTotal
	pagesRemain := len(bp.pagesDone) < bp.pagesTotal
	return tablesReady && endpointsReady && pagesRemain
}

// --- Build cost tracker ---

// buildCostTracker accumulates token usage across all stages of a build and
// fires alerts when configurable thresholds are crossed. When a tokenBudget
// is set, the tracker also signals wrap-up (at 90%) and hard-stop (at 100%).
type buildCostTracker struct {
	totalTokens int
	alertedAt   map[int]bool // thresholds already alerted
	tokenBudget int          // if > 0, hard ceiling for the entire build
	wrapUpSent  bool         // true after the 90% nudge has been injected
}

// defaultTokenBudget is the default per-build token ceiling.
// This prevents runaway builds from consuming unbounded API costs.
// A typical complex site (10 pages, 8 endpoints, 5 tables) uses ~500K-800K tokens.
const defaultTokenBudget = 3_000_000

// Token thresholds for owner alerts.
var costAlertThresholds = []int{500_000, 1_000_000, 2_000_000}

func newBuildCostTracker() *buildCostTracker {
	return &buildCostTracker{
		alertedAt:   make(map[int]bool),
		tokenBudget: defaultTokenBudget,
	}
}


// addTokens records token usage and returns a message if a threshold was crossed.
func (ct *buildCostTracker) addTokens(tokens int) string {
	ct.totalTokens += tokens
	for _, threshold := range costAlertThresholds {
		if ct.totalTokens >= threshold && !ct.alertedAt[threshold] {
			ct.alertedAt[threshold] = true
			label := fmt.Sprintf("%dK", threshold/1000)
			if threshold >= 1_000_000 {
				label = fmt.Sprintf("%.1fM", float64(threshold)/1_000_000)
			}
			return fmt.Sprintf("Token usage alert: this build has used %s tokens so far (%d total). Large builds may incur significant API costs.",
				label, ct.totalTokens)
		}
	}
	return ""
}

// fileTypeLabel returns a human-readable label for a filename based on its extension.
func fileTypeLabel(filename string) string {
	ext := ""
	if i := strings.LastIndex(filename, "."); i >= 0 {
		ext = strings.ToLower(filename[i+1:])
	}
	switch ext {
	case "js", "mjs", "ts", "jsx", "tsx":
		return "script"
	case "css", "scss", "sass", "less":
		return "stylesheet"
	case "json", "xml", "yml", "yaml", "toml":
		return "data file"
	case "svg":
		return "SVG"
	case "png", "jpg", "jpeg", "gif", "webp", "ico", "bmp", "avif":
		return "image"
	case "woff", "woff2", "ttf", "otf", "eot":
		return "font"
	case "mp4", "webm", "ogg", "mov":
		return "video"
	case "mp3", "wav", "flac", "aac", "m4a":
		return "audio"
	case "pdf":
		return "document"
	default:
		return "file"
	}
}

// toolProgressMessage returns a human-readable message for successful write operations.
func toolProgressMessage(toolName string, args map[string]interface{}) string {
	action, _ := args["action"].(string)
	switch {
	case toolName == "manage_files" && action == "save":
		if f, _ := args["filename"].(string); f != "" {
			return fmt.Sprintf("Created **%s** (%s)", f, fileTypeLabel(f))
		}
	case toolName == "manage_files" && action == "delete":
		if f, _ := args["filename"].(string); f != "" {
			return fmt.Sprintf("Deleted **%s**", f)
		}
	case toolName == "manage_layout" && action == "save":
		name, _ := args["name"].(string)
		if name == "" {
			name = "default"
		}
		return fmt.Sprintf("Saved layout: **%s**", name)
	case toolName == "manage_schema" && action == "create":
		if t, _ := args["table_name"].(string); t != "" {
			return fmt.Sprintf("Created table: **%s**", t)
		}
	case toolName == "manage_endpoints" && strings.HasPrefix(action, "create_"):
		if p, _ := args["path"].(string); p != "" {
			return fmt.Sprintf("Created endpoint: **/api/%s** (%s)", p, action)
		}
	}
	return ""
}
