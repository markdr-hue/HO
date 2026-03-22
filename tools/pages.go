/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/markdr-hue/HO/events"
)

// ---------------------------------------------------------------------------
// PagesTool — unified manager for all page operations
// ---------------------------------------------------------------------------

type PagesTool struct{}

func (t *PagesTool) Name() string { return "manage_pages" }
func (t *PagesTool) Description() string {
	return "Save, patch, get, list, delete, restore, search, or revert pages."
}

func (t *PagesTool) Guide() string {
	return `### manage_pages
- Content = main content only (server wraps in full HTML with layout nav/header/footer).
- Do NOT add <style> blocks — all CSS belongs in the global stylesheet. If you need new classes for a page, patch the CSS file first via manage_files.
- Use var(--color-*) and classes from the global CSS.
- **Traditional sites/apps**: Do NOT add <header>, <nav>, or <footer> tags — the layout provides these globally. Wrap each section's inner content in <div class="container"> to match layout header/footer alignment.
- **Chromeless apps (games, canvas, fullscreen)**: Pages fill the viewport. Use layout="none" if the page shouldn't be wrapped. No .container needed — structure the page however the app requires (canvas, single root div, etc.).
- Pages must be functional: fetch from APIs, handle forms, wire interactive elements.
- For complex JS, create .js files via manage_files(scope="page") and list them in the page's assets array. Small inline <script> tags are fine for simple interactivity.
- **Editing**: Prefer patch (search/replace pairs) for targeted changes. Use edit_lines for line-number edits, regex_replace for pattern-based bulk changes.`
}

func (t *PagesTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":       map[string]interface{}{"type": "string", "description": "Action to perform", "enum": []string{"save", "patch", "edit_lines", "regex_replace", "get", "list", "delete", "restore", "history", "revert", "search"}},
			"path":         map[string]interface{}{"type": "string", "description": "URL path for the page (e.g. /about)"},
			"title":        map[string]interface{}{"type": "string", "description": "Page title"},
			"content":      map[string]interface{}{"type": "string", "description": "HTML content for the page. Pages using a layout will have this content placed within the layout structure."},
			"template":     map[string]interface{}{"type": "string", "description": "Template name to use for rendering"},
			"status":       map[string]interface{}{"type": "string", "description": "Page status: published or draft", "enum": []string{"published", "draft"}},
			"layout":       map[string]interface{}{"type": "string", "description": `Layout name for this page. Default: uses "default" layout. Use "none" for no layout wrapping.`},
			"assets":       map[string]interface{}{"type": "string", "description": `JSON array of page-scoped asset filenames to include on this page (e.g. ["charts.js","maps.css"]). Global-scope assets are auto-injected on all pages.`},
			"metadata":     map[string]interface{}{"type": "string", "description": "JSON string of additional metadata (description, og_image, canonical, keywords)"},
			"limit":        map[string]interface{}{"type": "number", "description": "Maximum number of results to return"},
			"version":      map[string]interface{}{"type": "integer", "description": "Version number to restore (revert action)"},
			"query":        map[string]interface{}{"type": "string", "description": "Search query for full-text search"},
			"patches":      map[string]interface{}{"type": "string", "description": `JSON array of search/replace pairs for patch action: [{"search":"old text","replace":"new text"}]. Works on HTML and JS content.`},
			"edits":        map[string]interface{}{"type": "string", "description": `JSON array of line edits for edit_lines action: [{"line":5,"content":"new line 5"},{"from":10,"to":12,"content":"replaces lines 10-12"},{"after":20,"content":"insert after line 20"},{"delete":[30,31]}]`},
			"replacements": map[string]interface{}{"type": "string", "description": `JSON array of regex replacements: [{"pattern":"color:\\s*#[0-9a-f]+","replace":"color: var(--primary)","count":0}]. count=0 replaces all.`},
		},
		"required": []string{},
	}
}

func (t *PagesTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"save":          t.save,
		"patch":         t.patch,
		"edit_lines":    t.editLines,
		"regex_replace": t.regexReplace,
		"get":           t.get,
		"list":          t.list,
		"delete":        t.delete,
		"restore":       t.restore,
		"history":       t.history,
		"revert":        t.revert,
		"search":        t.search,
	}, func(a map[string]interface{}) string {
		if _, has := a["version"]; has {
			return "revert"
		}
		if _, has := a["content"]; has {
			return "save"
		}
		if _, has := a["query"]; has {
			return "search"
		}
		if _, has := a["path"]; has {
			return "get"
		}
		return "list"
	})
}

// ---------------------------------------------------------------------------
// save — create or update a page with version history capture
// ---------------------------------------------------------------------------

func (t *PagesTool) save(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	content, _ := args["content"].(string)
	title, _ := args["title"].(string)
	template, _ := args["template"].(string)
	status, _ := args["status"].(string)
	if status == "" {
		status = "published"
	}
	metadata, _ := args["metadata"].(string)
	if metadata == "" {
		metadata = "{}"
	}
	layout, _ := args["layout"].(string)     // "" = default layout, "none" = no layout
	pageAssets, _ := args["assets"].(string) // JSON array of page-scoped asset filenames

	// Before upsert: capture existing page into version history.
	var existingID int
	var oldTitle, oldContent, oldTemplate, oldStatus, oldMeta sql.NullString
	err := ctx.DB.QueryRow(
		"SELECT id, title, content, template, status, metadata FROM ho_pages WHERE path = ? AND is_deleted = 0",
		path,
	).Scan(&existingID, &oldTitle, &oldContent, &oldTemplate, &oldStatus, &oldMeta)
	if err == nil {
		// Page exists — save current version before overwriting.
		var maxVer int
		ctx.DB.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_page_versions WHERE page_id = ?", existingID).Scan(&maxVer)
		ctx.DB.Exec(
			`INSERT INTO ho_page_versions (page_id, path, title, content, template, status, metadata, version_number, changed_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'brain')`,
			existingID, path, oldTitle, oldContent, oldTemplate, oldStatus, oldMeta, maxVer+1,
		)
	}

	// Layout column: NULL means use "default" layout. Store NULL unless explicitly set.
	var layoutArg interface{}
	if layout != "" {
		layoutArg = layout
	}
	// Assets column: NULL means no page-scoped assets. Store NULL unless explicitly set.
	var assetsArg interface{}
	if pageAssets != "" {
		assetsArg = pageAssets
	}

	// Sanitize: strip dangerous event handlers and javascript: URIs from content.
	content = sanitizePageHTML(content)

	// Upsert the page.
	_, err = ctx.DB.Exec(
		`INSERT INTO ho_pages (path, title, content, template, status, metadata, layout, assets, is_deleted, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, CURRENT_TIMESTAMP)
		 ON CONFLICT(path) DO UPDATE SET
		   title = excluded.title,
		   content = excluded.content,
		   template = excluded.template,
		   status = excluded.status,
		   metadata = excluded.metadata,
		   layout = excluded.layout,
		   assets = excluded.assets,
		   is_deleted = 0,
		   deleted_at = NULL,
		   updated_at = CURRENT_TIMESTAMP`,
		path, title, content, template, status, metadata, layoutArg, assetsArg,
	)
	if err != nil {
		return nil, fmt.Errorf("saving page: %w", err)
	}

	// Post-save validation: warn about missing asset references, inline styles, JS issues.
	warnings := validatePageContent(content)

	// Cross-validate JS function calls against global JS API.
	scriptContent := extractScriptContent(content)
	if jsWarnings := validateJSInterop(scriptContent, ctx.DB); len(jsWarnings) > 0 {
		warnings = append(warnings, jsWarnings...)
	}

	// Classify into hard errors (must fix) and soft warnings (informational).
	var hardErrors, softWarnings []string
	for _, w := range warnings {
		if strings.Contains(w, "but no id=") || strings.Contains(w, "syntax error") || strings.Contains(w, "brace") || strings.Contains(w, "did you mean") {
			hardErrors = append(hardErrors, w)
		} else {
			softWarnings = append(softWarnings, w)
		}
	}

	// Coherence hints: check layout nav and internal links.
	hints := checkCoherence(path, content, ctx.DB)

	resultData := map[string]interface{}{"path": path, "title": title, "status": status}
	if len(softWarnings) > 0 {
		resultData["ATTENTION_JS_ISSUES"] = softWarnings
	}
	if len(hints) > 0 {
		resultData["hints"] = hints
	}
	// Inject API, CSS, and page skeleton catalogs for cross-page coherence.
	if apiCatalog := buildAPICatalog(ctx.DB); apiCatalog != "" {
		resultData["available_endpoints"] = apiCatalog
	}
	if css := loadGlobalCSSForCatalog(ctx.DB); css != "" {
		if summary := extractCSSSummary(css); summary != "" {
			resultData["css_classes"] = summary
		}
	}
	if skeletons := loadRecentPageSkeletons(ctx.DB, path, 3); skeletons != "" {
		resultData["page_skeletons"] = skeletons
	}
	if len(hardErrors) > 0 {
		resultData["errors"] = hardErrors
		return &Result{
			Success: false,
			Error:   "Page saved with errors that MUST be fixed: " + strings.Join(hardErrors, "; "),
			Data:    resultData,
		}, nil
	}

	// Publish page lifecycle event.
	if ctx.Bus != nil {
		eventPayload := map[string]interface{}{
			"path":   path,
			"title":  title,
			"status": status,
		}
		if existingID > 0 {
			ctx.Bus.Publish(events.NewEvent(events.EventPageUpdated, ctx.SiteID, eventPayload))
		} else if status == "published" {
			ctx.Bus.Publish(events.NewEvent(events.EventPagePublished, ctx.SiteID, eventPayload))
		}
	}

	return &Result{Success: true, Data: resultData}, nil
}

// ---------------------------------------------------------------------------
// patch — apply search/replace pairs to a page without full rewrite
// ---------------------------------------------------------------------------

func (t *PagesTool) patch(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	patchesStr, _ := args["patches"].(string)
	if patchesStr == "" {
		return &Result{Success: false, Error: "patches is required (JSON array of {search, replace} pairs)"}, nil
	}

	var patches []searchReplacePatch
	if err := json.Unmarshal([]byte(patchesStr), &patches); err != nil {
		return &Result{Success: false, Error: "patches must be a JSON array: " + err.Error()}, nil
	}
	if len(patches) == 0 {
		return &Result{Success: false, Error: "patches array is empty"}, nil
	}

	// Read current page.
	var existingID int
	var title, content, template, status, metadata, layout, pageAssets sql.NullString
	err := ctx.DB.QueryRow(
		"SELECT id, title, content, template, status, metadata, layout, assets FROM ho_pages WHERE path = ? AND is_deleted = 0",
		path,
	).Scan(&existingID, &title, &content, &template, &status, &metadata, &layout, &pageAssets)
	if err != nil {
		return &Result{Success: false, Error: "page not found"}, nil
	}

	// Capture version history before modifying.
	var maxVer int
	ctx.DB.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_page_versions WHERE page_id = ?", existingID).Scan(&maxVer)
	ctx.DB.Exec(
		`INSERT INTO ho_page_versions (page_id, path, title, content, template, status, metadata, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'brain')`,
		existingID, path, title, content, template, status, metadata, maxVer+1,
	)

	// Apply patches sequentially (first-match by default to prevent duplication).
	modified, patchRes := applyPatches(content.String, patches)

	if len(patchRes.Applied) == 0 && len(patchRes.NotFound) > 0 {
		return &Result{Success: false, Error: "no patches matched", Data: map[string]interface{}{"not_found": patchRes.NotFound}}, nil
	}

	// Sanitize and save.
	modified = sanitizePageHTML(modified)
	_, err = ctx.DB.Exec(
		"UPDATE ho_pages SET content = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		modified, existingID,
	)
	if err != nil {
		return nil, fmt.Errorf("saving patched page: %w", err)
	}

	// Post-save validation.
	warnings := validatePageContent(modified)
	hints := checkCoherence(path, modified, ctx.DB)

	resultData := map[string]interface{}{
		"path":    path,
		"applied": len(patchRes.Applied),
	}
	if len(patchRes.NotFound) > 0 {
		resultData["not_found"] = patchRes.NotFound
	}
	if len(patchRes.Warnings) > 0 {
		resultData["patch_warnings"] = patchRes.Warnings
	}
	if len(warnings) > 0 {
		resultData["warnings"] = warnings
	}
	if len(hints) > 0 {
		resultData["hints"] = hints
	}
	// Inject API, CSS, and page skeleton catalogs for cross-page coherence.
	if apiCatalog := buildAPICatalog(ctx.DB); apiCatalog != "" {
		resultData["available_endpoints"] = apiCatalog
	}
	if css := loadGlobalCSSForCatalog(ctx.DB); css != "" {
		if summary := extractCSSSummary(css); summary != "" {
			resultData["css_classes"] = summary
		}
	}
	if skeletons := loadRecentPageSkeletons(ctx.DB, path, 3); skeletons != "" {
		resultData["page_skeletons"] = skeletons
	}
	return &Result{Success: true, Data: resultData}, nil
}

// ---------------------------------------------------------------------------
// edit_lines — line-number-based edits (more token-efficient than patch)
// ---------------------------------------------------------------------------

func (t *PagesTool) editLines(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	editsStr, _ := args["edits"].(string)
	if editsStr == "" {
		return &Result{Success: false, Error: "edits is required"}, nil
	}

	var edits []lineEdit
	if err := json.Unmarshal([]byte(editsStr), &edits); err != nil {
		return &Result{Success: false, Error: "edits must be a JSON array: " + err.Error()}, nil
	}
	if len(edits) == 0 {
		return &Result{Success: false, Error: "edits array is empty"}, nil
	}

	// Read current page.
	var existingID int
	var title, content, template, status, metadata sql.NullString
	err := ctx.DB.QueryRow(
		"SELECT id, title, content, template, status, metadata FROM ho_pages WHERE path = ? AND is_deleted = 0",
		path,
	).Scan(&existingID, &title, &content, &template, &status, &metadata)
	if err != nil {
		return &Result{Success: false, Error: "page not found"}, nil
	}

	// Capture version before modifying.
	capturePageVersion(ctx.DB, existingID, path, title, content, template, status, metadata)

	modified, applied, editErr := applyLineEdits(content.String, edits)
	if editErr != nil {
		return &Result{Success: false, Error: editErr.Error()}, nil
	}

	modified = sanitizePageHTML(modified)
	_, err = ctx.DB.Exec(
		"UPDATE ho_pages SET content = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		modified, existingID,
	)
	if err != nil {
		return nil, fmt.Errorf("saving edited page: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"path":    path,
		"applied": applied,
		"lines":   len(strings.Split(modified, "\n")),
	}}, nil
}

// ---------------------------------------------------------------------------
// regex_replace — regex-based replacements
// ---------------------------------------------------------------------------

func (t *PagesTool) regexReplace(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	replStr, _ := args["replacements"].(string)
	if replStr == "" {
		return &Result{Success: false, Error: "replacements is required"}, nil
	}

	var replacements []regexReplacement
	if err := json.Unmarshal([]byte(replStr), &replacements); err != nil {
		return &Result{Success: false, Error: "replacements must be a JSON array: " + err.Error()}, nil
	}
	if len(replacements) == 0 {
		return &Result{Success: false, Error: "replacements array is empty"}, nil
	}

	// Read current page.
	var existingID int
	var title, content, template, status, metadata sql.NullString
	err := ctx.DB.QueryRow(
		"SELECT id, title, content, template, status, metadata FROM ho_pages WHERE path = ? AND is_deleted = 0",
		path,
	).Scan(&existingID, &title, &content, &template, &status, &metadata)
	if err != nil {
		return &Result{Success: false, Error: "page not found"}, nil
	}

	capturePageVersion(ctx.DB, existingID, path, title, content, template, status, metadata)

	modified, applied, replErr := applyRegexReplacements(content.String, replacements)
	if replErr != nil {
		return &Result{Success: false, Error: replErr.Error()}, nil
	}

	modified = sanitizePageHTML(modified)
	_, err = ctx.DB.Exec(
		"UPDATE ho_pages SET content = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		modified, existingID,
	)
	if err != nil {
		return nil, fmt.Errorf("saving regex-replaced page: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"path":    path,
		"applied": applied,
	}}, nil
}

// capturePageVersion saves the current page state into version history.
func capturePageVersion(db *sql.DB, pageID int, path string, title, content, template, status, metadata sql.NullString) {
	var maxVer int
	if err := db.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_page_versions WHERE page_id = ?", pageID).Scan(&maxVer); err != nil {
		slog.Warn("capturePageVersion: failed to read max version", "page_id", pageID, "error", err)
		return
	}
	if _, err := db.Exec(
		`INSERT INTO ho_page_versions (page_id, path, title, content, template, status, metadata, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'brain')`,
		pageID, path, title, content, template, status, metadata, maxVer+1,
	); err != nil {
		slog.Warn("capturePageVersion: failed to insert version", "page_id", pageID, "error", err)
	}
}

// ---------------------------------------------------------------------------
// get — retrieve a page by its path
// ---------------------------------------------------------------------------

func (t *PagesTool) get(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	var id int
	var title, content, template, status, metadata sql.NullString
	var createdAt, updatedAt time.Time

	err := ctx.DB.QueryRow(
		"SELECT id, title, content, template, status, metadata, created_at, updated_at FROM ho_pages WHERE path = ? AND is_deleted = 0",
		path,
	).Scan(&id, &title, &content, &template, &status, &metadata, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: "page not found"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting page: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"id":         id,
		"path":       path,
		"title":      title.String,
		"content":    content.String,
		"template":   template.String,
		"status":     status.String,
		"metadata":   metadata.String,
		"created_at": createdAt,
		"updated_at": updatedAt,
	}}, nil
}

// ---------------------------------------------------------------------------
// list — list all pages for the current site
// ---------------------------------------------------------------------------

func (t *PagesTool) list(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT id, path, title, status, updated_at FROM ho_pages WHERE is_deleted = 0 ORDER BY path",
	)
	if err != nil {
		return nil, fmt.Errorf("listing pages: %w", err)
	}
	defer rows.Close()

	var pages []map[string]interface{}
	for rows.Next() {
		var id int
		var path string
		var title, status sql.NullString
		var updatedAt time.Time
		if err := rows.Scan(&id, &path, &title, &status, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning page: %w", err)
		}
		pages = append(pages, map[string]interface{}{
			"id":         id,
			"path":       path,
			"title":      title.String,
			"status":     status.String,
			"updated_at": updatedAt,
		})
	}

	return &Result{Success: true, Data: pages}, nil
}

// ---------------------------------------------------------------------------
// delete — soft-delete a page by its path
// ---------------------------------------------------------------------------

func (t *PagesTool) delete(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	res, err := ctx.DB.Exec(
		"UPDATE ho_pages SET is_deleted = 1, deleted_at = CURRENT_TIMESTAMP WHERE path = ? AND is_deleted = 0",
		path,
	)
	if err != nil {
		return nil, fmt.Errorf("deleting page: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "page not found"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{"deleted": path}}, nil
}

// ---------------------------------------------------------------------------
// restore — restore a soft-deleted page
// ---------------------------------------------------------------------------

func (t *PagesTool) restore(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	res, err := ctx.DB.Exec(
		"UPDATE ho_pages SET is_deleted = 0, deleted_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE path = ? AND is_deleted = 1",
		path,
	)
	if err != nil {
		return nil, fmt.Errorf("restoring page: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "no deleted page found at that path"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{"restored": path}}, nil
}

// ---------------------------------------------------------------------------
// history — view version history for a page
// ---------------------------------------------------------------------------

func (t *PagesTool) history(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	// Find the page ID.
	var pageID int
	err := ctx.DB.QueryRow(
		"SELECT id FROM ho_pages WHERE path = ?",
		path,
	).Scan(&pageID)
	if err != nil {
		return &Result{Success: false, Error: "page not found"}, nil
	}

	rows, err := ctx.DB.Query(
		"SELECT version_number, title, status, changed_by, created_at FROM ho_page_versions WHERE page_id = ? ORDER BY version_number DESC LIMIT ?",
		pageID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying page history: %w", err)
	}
	defer rows.Close()

	var versions []map[string]interface{}
	for rows.Next() {
		var ver int
		var title, status, changedBy sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&ver, &title, &status, &changedBy, &createdAt); err != nil {
			ctx.Logger.Warn("scan error in page history", "page_id", pageID, "error", err)
			continue
		}
		versions = append(versions, map[string]interface{}{
			"version":    ver,
			"title":      title.String,
			"status":     status.String,
			"changed_by": changedBy.String,
			"created_at": createdAt,
		})
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"path":     path,
		"versions": versions,
	}}, nil
}

// ---------------------------------------------------------------------------
// revert — restore a page to a previous version
// ---------------------------------------------------------------------------

func (t *PagesTool) revert(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	version, ok := args["version"].(float64)
	if !ok || version < 1 {
		return &Result{Success: false, Error: "version (number) is required"}, nil
	}

	// Find the page.
	var pageID int
	var oldTitle, oldContent, oldTemplate, oldStatus, oldMeta sql.NullString
	err := ctx.DB.QueryRow(
		"SELECT id, title, content, template, status, metadata FROM ho_pages WHERE path = ?",
		path,
	).Scan(&pageID, &oldTitle, &oldContent, &oldTemplate, &oldStatus, &oldMeta)
	if err != nil {
		return &Result{Success: false, Error: "page not found"}, nil
	}

	// Load the requested version.
	var verTitle, verContent, verTemplate, verStatus, verMeta sql.NullString
	err = ctx.DB.QueryRow(
		"SELECT title, content, template, status, metadata FROM ho_page_versions WHERE page_id = ? AND version_number = ?",
		pageID, int(version),
	).Scan(&verTitle, &verContent, &verTemplate, &verStatus, &verMeta)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("version %d not found for page %s", int(version), path)}, nil
	}

	// Save current state as a new version first (so revert is reversible).
	var maxVer int
	ctx.DB.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_page_versions WHERE page_id = ?", pageID).Scan(&maxVer)
	ctx.DB.Exec(
		`INSERT INTO ho_page_versions (page_id, path, title, content, template, status, metadata, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'revert')`,
		pageID, path, oldTitle, oldContent, oldTemplate, oldStatus, oldMeta, maxVer+1,
	)

	// Restore the old version content.
	_, err = ctx.DB.Exec(
		`UPDATE ho_pages SET title = ?, content = ?, template = ?, status = ?, metadata = ?,
		 is_deleted = 0, deleted_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		verTitle, verContent, verTemplate, verStatus, verMeta, pageID,
	)
	if err != nil {
		return nil, fmt.Errorf("reverting page: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"path":             path,
		"restored_version": int(version),
		"title":            verTitle.String,
	}}, nil
}

// ---------------------------------------------------------------------------
// search — full-text search across pages
// ---------------------------------------------------------------------------

func (t *PagesTool) search(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	q, _ := args["query"].(string)
	if q == "" {
		return &Result{Success: false, Error: "query is required for search"}, nil
	}
	escaped := strings.NewReplacer("%", `\%`, "_", `\_`).Replace(q)
	like := "%" + escaped + "%"
	rows, err := ctx.DB.Query(
		"SELECT id, path, title, status FROM ho_pages WHERE is_deleted = 0 AND (title LIKE ? ESCAPE '\\' OR content LIKE ? ESCAPE '\\') ORDER BY path",
		like, like,
	)
	if err != nil {
		return nil, fmt.Errorf("searching pages: %w", err)
	}
	defer rows.Close()
	var pages []map[string]interface{}
	for rows.Next() {
		var id int
		var path string
		var title, status sql.NullString
		if err := rows.Scan(&id, &path, &title, &status); err != nil {
			ctx.Logger.Warn("scan error in page search", "query", q, "error", err)
			continue
		}
		pages = append(pages, map[string]interface{}{"id": id, "path": path, "title": title.String, "status": status.String})
	}
	return &Result{Success: true, Data: pages}, nil
}

// ---------------------------------------------------------------------------
// Page content validation helpers
// ---------------------------------------------------------------------------

var (
	pageScriptRe   = regexp.MustCompile(`(?isU)<script[^>]*>(.+)</script>`)
	internalLinkRe = regexp.MustCompile(`href\s*=\s*["'](/[^"'#?]*)["'#?]?`)
)

// Regexes for JS DOM reference validation.
var (
	getByIdRe       = regexp.MustCompile(`getElementById\(\s*['"]([^'"]+)['"]\s*\)`)
	querySelectorRe = regexp.MustCompile(`querySelector\(\s*['"]#([^'"]+)['"]\s*\)`)
	htmlIDRe        = regexp.MustCompile(`(?i)\bid\s*=\s*["']([^"']+)["']`)
	// Detects unguarded querySelector/querySelectorAll: result used with . but no ?. and not inside an if-check.
	// Matches: querySelector(...).something or querySelectorAll(...).something (without ?.)
	unguardedQSRe = regexp.MustCompile(`querySelector(?:All)?\([^)]+\)\.(?:[a-zA-Z])`)
)

func extractScriptContent(content string) string {
	matches := pageScriptRe.FindAllStringSubmatch(content, -1)
	var parts []string
	for _, m := range matches {
		if len(m) > 1 {
			parts = append(parts, m[1])
		}
	}
	return strings.Join(parts, "\n")
}

func validatePageContent(content string) []string {
	var warnings []string

	scriptContent := extractScriptContent(content)
	if scriptContent == "" {
		return nil
	}

	// JS brace balance check.
	opens := strings.Count(scriptContent, "{")
	closes := strings.Count(scriptContent, "}")
	if opens != closes {
		warnings = append(warnings, fmt.Sprintf("Possible JS syntax error: %d open braces vs %d close braces", opens, closes))
	}

	// Collect all IDs defined in the HTML.
	htmlIDs := map[string]bool{}
	for _, m := range htmlIDRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			htmlIDs[m[1]] = true
		}
	}

	// Check getElementById('xxx') references against actual IDs.
	referencedIDs := map[string]bool{}
	for _, m := range getByIdRe.FindAllStringSubmatch(scriptContent, -1) {
		if len(m) > 1 {
			referencedIDs[m[1]] = true
		}
	}
	// Check querySelector('#xxx') references.
	for _, m := range querySelectorRe.FindAllStringSubmatch(scriptContent, -1) {
		if len(m) > 1 {
			referencedIDs[m[1]] = true
		}
	}
	for id := range referencedIDs {
		if !htmlIDs[id] {
			warnings = append(warnings, fmt.Sprintf("JS references element #%s but no id=\"%s\" found in HTML — will cause TypeError at runtime", id, id))
		}
	}

	// Detect unguarded querySelector().property (missing ?. operator).
	if unguardedQSRe.MatchString(scriptContent) {
		warnings = append(warnings, "querySelector() result used without ?. — add optional chaining to avoid TypeError when element is missing")
	}

	return warnings
}

// checkCoherence checks for navigation and link consistency after saving a page.
// Returns hints (not warnings) that help the LLM maintain site coherence.
func checkCoherence(pagePath, content string, db *sql.DB) []string {
	var hints []string

	// Check if the layout nav links to this page (skip for / since it's often just the logo link).
	if pagePath != "/" {
		var navHTML sql.NullString
		db.QueryRow("SELECT template FROM ho_layouts WHERE name = 'default'").Scan(&navHTML)
		if navHTML.Valid && navHTML.String != "" {
			if !strings.Contains(navHTML.String, `"`+pagePath+`"`) && !strings.Contains(navHTML.String, `'`+pagePath+`'`) {
				hints = append(hints, fmt.Sprintf("Layout nav does not link to %s — update with manage_layout if this page should be in navigation", pagePath))
			}
		}
	}

	// Check internal links in page content for dead links (pages that don't exist).
	matches := internalLinkRe.FindAllStringSubmatch(content, -1)
	seen := map[string]bool{}
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		target := m[1]
		// Skip asset/api/file paths and self-references.
		if strings.HasPrefix(target, "/assets/") || strings.HasPrefix(target, "/api/") || strings.HasPrefix(target, "/files/") || target == pagePath {
			continue
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		var exists int
		db.QueryRow("SELECT COUNT(*) FROM ho_pages WHERE path = ? AND is_deleted = 0", target).Scan(&exists)
		if exists == 0 {
			hints = append(hints, fmt.Sprintf("Links to %s which does not exist yet — create it or fix the link", target))
		}
	}

	return hints
}

// sanitizePageHTML strips dangerous attributes (on* event handlers, javascript: URIs)
// from HTML content to prevent XSS. Preserves all other HTML structure since pages
// are intentionally HTML content built by the AI brain.
var (
	onEventAttrRe   = regexp.MustCompile(`(?i)\s+on[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]*)`)
	jsURIRe         = regexp.MustCompile(`(?i)(href|src|action)\s*=\s*(?:"javascript:[^"]*"|'javascript:[^']*')`)
	dataURIScriptRe = regexp.MustCompile(`(?i)(href|src)\s*=\s*(?:"data:text/html[^"]*"|'data:text/html[^']*')`)
)

// validateJSInterop checks if JS code references functions/objects that exist
// in the global JS files. Returns warnings with "did you mean?" suggestions.
func validateJSInterop(jsCode string, db *sql.DB) []string {
	if jsCode == "" {
		return nil
	}

	// Load all global JS content.
	rows, err := db.Query("SELECT content FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.js' ORDER BY filename")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var globalJS strings.Builder
	for rows.Next() {
		var content sql.NullString
		if err := rows.Scan(&content); err != nil || !content.Valid {
			continue
		}
		globalJS.WriteString(content.String)
		globalJS.WriteString("\n")
	}
	if globalJS.Len() == 0 {
		return nil
	}

	// Extract the public API from global JS: function names and Object.method names.
	globalSrc := globalJS.String()
	knownAPI := map[string]bool{}

	// Top-level functions.
	funcRe := regexp.MustCompile(`(?m)^(?:export\s+)?function\s+([a-zA-Z_$][\w$]*)\s*\(`)
	for _, m := range funcRe.FindAllStringSubmatch(globalSrc, -1) {
		knownAPI[m[1]] = true
	}
	// const/let/var declarations (functions and objects).
	constRe := regexp.MustCompile(`(?m)^(?:export\s+)?(?:const|let|var)\s+([a-zA-Z_$][\w$]*)\s*=`)
	for _, m := range constRe.FindAllStringSubmatch(globalSrc, -1) {
		knownAPI[m[1]] = true
	}
	// class declarations.
	classRe := regexp.MustCompile(`(?m)^(?:export\s+)?class\s+([A-Z][\w$]*)`)
	for _, m := range classRe.FindAllStringSubmatch(globalSrc, -1) {
		knownAPI[m[1]] = true
	}
	// Object.method patterns.
	methodRe := regexp.MustCompile(`(?m)([A-Z][\w$]*)\.([a-zA-Z_$][\w$]*)\s*(?:=\s*(?:function|\()|[\(])`)
	for _, m := range methodRe.FindAllStringSubmatch(globalSrc, -1) {
		knownAPI[m[1]+"."+m[2]] = true
		knownAPI[m[1]] = true // the object itself
	}
	// Methods inside object literals: const Auth = { login(...) { ... } }
	objDeclRe := regexp.MustCompile(`(?m)^(?:const|let|var)\s+([A-Z][\w$]*)\s*=\s*\{`)
	for _, m := range objDeclRe.FindAllStringSubmatchIndex(globalSrc, -1) {
		objName := globalSrc[m[2]:m[3]]
		// Scan ahead to find methods.
		start := m[1] - 1
		depth := 1
		pos := start + 1
		for pos < len(globalSrc) && depth > 0 {
			if globalSrc[pos] == '{' {
				depth++
			} else if globalSrc[pos] == '}' {
				depth--
			}
			pos++
		}
		if depth == 0 {
			body := globalSrc[start:pos]
			innerMethodRe := regexp.MustCompile(`(?m)^\s+([a-zA-Z_$][\w$]*)\s*[\(:]`)
			for _, mm := range innerMethodRe.FindAllStringSubmatch(body, -1) {
				knownAPI[objName+"."+mm[1]] = true
			}
		}
	}

	if len(knownAPI) == 0 {
		return nil
	}

	// Now scan the page JS for calls to Object.method() patterns that don't exist.
	var warnings []string
	seen := map[string]bool{}

	// Check Object.method( calls — these are the most common source of errors.
	callRe := regexp.MustCompile(`([A-Z][\w$]*)\.([a-zA-Z_$][\w$]*)\s*\(`)
	for _, m := range callRe.FindAllStringSubmatch(jsCode, -1) {
		obj := m[1]
		method := m[2]
		fullCall := obj + "." + method

		if seen[fullCall] {
			continue
		}
		seen[fullCall] = true

		// Skip well-known browser APIs.
		if isBrowserGlobal(obj) {
			continue
		}

		// Check if the object exists in global JS.
		if !knownAPI[obj] {
			continue // Object not from global JS, skip (might be locally defined)
		}

		// Object exists — check if method exists.
		if knownAPI[fullCall] {
			continue // method exists, all good
		}

		// Method doesn't exist — suggest closest match.
		suggestion := findClosestMethod(obj, method, knownAPI)
		if suggestion != "" {
			warnings = append(warnings, fmt.Sprintf("JS calls %s() but it doesn't exist — did you mean %s()?", fullCall, suggestion))
		} else {
			warnings = append(warnings, fmt.Sprintf("JS calls %s() but no such method exists in global JS.", fullCall))
		}
	}

	return warnings
}

// isBrowserGlobal checks if an identifier is a well-known browser/DOM global.
func isBrowserGlobal(name string) bool {
	globals := map[string]bool{
		"Math": true, "JSON": true, "Date": true, "Array": true, "Object": true,
		"Promise": true, "String": true, "Number": true, "RegExp": true, "Map": true,
		"Set": true, "Error": true, "URL": true, "URLSearchParams": true,
		"FormData": true, "Headers": true, "Response": true, "Request": true,
		"WebSocket": true, "EventSource": true, "XMLHttpRequest": true,
		"Element": true, "HTMLElement": true, "Node": true, "NodeList": true,
		"Document": true, "Window": true, "Event": true, "CustomEvent": true,
		"IntersectionObserver": true, "MutationObserver": true, "ResizeObserver": true,
		"AbortController": true, "TextEncoder": true, "TextDecoder": true,
		"Intl": true, "Proxy": true, "Reflect": true, "Symbol": true,
		"Console": true, "Storage": true, "Performance": true,
	}
	return globals[name]
}

// findClosestMethod finds the closest matching method name for the given object.
func findClosestMethod(obj, method string, knownAPI map[string]bool) string {
	prefix := obj + "."
	var bestMatch string
	bestDist := 999

	for key := range knownAPI {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		candidateMethod := strings.TrimPrefix(key, prefix)
		dist := levenshtein(method, candidateMethod)
		if dist < bestDist && dist <= 3 { // max edit distance of 3
			bestDist = dist
			bestMatch = key
		}
	}
	return bestMatch
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func sanitizePageHTML(content string) string {
	content = onEventAttrRe.ReplaceAllString(content, "")
	content = jsURIRe.ReplaceAllString(content, "")
	content = dataURIScriptRe.ReplaceAllString(content, "")
	return content
}

// buildCSSCatalog returns a summary of all CSS classes and custom properties
// available in the site's stylesheet assets. Injected into page save results
// so the LLM knows which classes to use when building HTML.
func buildCSSCatalog(db *sql.DB) string {
	rows, err := db.Query("SELECT filename, storage_path FROM ho_assets WHERE filename LIKE '%.css' ORDER BY scope DESC, filename")
	if err != nil {
		return ""
	}
	defer rows.Close()

	var combined strings.Builder
	for rows.Next() {
		var filename, storagePath string
		if err := rows.Scan(&filename, &storagePath); err != nil {
			continue
		}
		data, err := os.ReadFile(storagePath)
		if err != nil {
			continue
		}
		summary := extractCSSSummary(string(data))
		if summary != "" {
			if combined.Len() > 0 {
				combined.WriteString(" | ")
			}
			combined.WriteString(filename + ": " + summary)
		}
	}

	result := combined.String()
	if len(result) > 3000 {
		result = result[:3000]
	}
	return result
}

// buildAPICatalog returns a compact reference of all API endpoints and their
// columns. Injected into page save results so the LLM uses correct paths and
// property names in JS code.
func buildAPICatalog(db *sql.DB) string {
	rows, err := db.Query("SELECT path, methods, table_name, public_columns FROM ho_api_endpoints ORDER BY path")
	if err != nil {
		return ""
	}
	defer rows.Close()

	var parts []string
	for rows.Next() {
		var path, methods, tableName string
		var publicCols sql.NullString
		if err := rows.Scan(&path, &methods, &tableName, &publicCols); err != nil {
			continue
		}
		cols := ""
		if publicCols.Valid && publicCols.String != "" && publicCols.String != "[]" {
			// Parse JSON array of column names into compact form.
			var colArr []string
			if json.Unmarshal([]byte(publicCols.String), &colArr) == nil && len(colArr) > 0 {
				cols = strings.Join(colArr, ",")
			}
		}
		entry := methods + " " + path + " -> " + tableName
		if cols != "" {
			entry += "(" + cols + ")"
		}
		parts = append(parts, entry)
	}

	// Also include auth endpoints.
	authRows, err := db.Query("SELECT path, table_name FROM ho_auth_endpoints ORDER BY path")
	if err == nil {
		defer authRows.Close()
		for authRows.Next() {
			var path, tableName string
			if err := authRows.Scan(&path, &tableName); err != nil {
				continue
			}
			parts = append(parts, "AUTH "+path+" -> "+tableName)
		}
	}

	result := strings.Join(parts, "; ")
	if len(result) > 2000 {
		result = result[:2000]
	}
	return result
}

func (t *PagesTool) MaxResultSize() int { return 16000 }

func (t *PagesTool) Summarize(result string) string {
	r, data, dataArr, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if dataArr != nil {
		return fmt.Sprintf(`{"success":true,"summary":"Returned %d items"}`, len(dataArr))
	}
	if data == nil {
		return summarizeTruncate(result, 300)
	}
	if content, ok := data["content"].(string); ok && content != "" {
		path, _ := data["path"].(string)
		fingerprint := pageStructureFingerprint(content)
		return fmt.Sprintf(`{"success":true,"summary":"Read page %s (%d chars). %s"}`, path, len(content), fingerprint)
	}
	// For save/patch results: preserve warnings, hints, and catalogs.
	path, hasPath := data["path"].(string)
	warnings, hasW := data["warnings"]
	hints, hasH := data["hints"]
	apiC, hasAPI := data["available_endpoints"].(string)
	cssC, hasCSS := data["css_classes"].(string)
	skelC, hasSkel := data["page_skeletons"].(string)
	if hasPath && (hasW || hasH || hasAPI || hasCSS || hasSkel) {
		var parts []string
		parts = append(parts, fmt.Sprintf(`"success":true,"path":"%s"`, path))
		if hasW {
			wJSON, _ := json.Marshal(warnings)
			parts = append(parts, fmt.Sprintf(`"warnings":%s`, wJSON))
		}
		if hasH {
			hJSON, _ := json.Marshal(hints)
			parts = append(parts, fmt.Sprintf(`"hints":%s`, hJSON))
		}
		if hasAPI {
			parts = append(parts, fmt.Sprintf(`"available_endpoints":"%s"`, strings.ReplaceAll(apiC, `"`, `\"`)))
		}
		if hasCSS {
			parts = append(parts, fmt.Sprintf(`"css_classes":"%s"`, strings.ReplaceAll(cssC, `"`, `\"`)))
		}
		if hasSkel {
			if len(skelC) > 800 {
				skelC = skelC[:800]
			}
			parts = append(parts, fmt.Sprintf(`"page_skeletons":"%s"`, strings.ReplaceAll(skelC, `"`, `\"`)))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return summarizeTruncate(result, 300)
}

// ---------------------------------------------------------------------------
// Page skeleton extraction — feed structural patterns back to the LLM
// ---------------------------------------------------------------------------

// skeletonLeafTags are tags whose text content is stripped and self-closed.
var skeletonLeafTags = map[string]bool{
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"p": true, "a": true, "img": true, "button": true, "span": true,
	"input": true, "textarea": true, "select": true, "label": true,
	"li": true, "th": true, "td": true, "figcaption": true, "small": true,
}

// skeletonSkipTags are tags whose entire content (including children) is skipped.
var skeletonSkipTags = map[string]bool{
	"script": true, "style": true, "svg": true, "noscript": true,
}

// extractPageSkeleton strips text content from HTML, keeping only the structural
// skeleton with tag names, class, and id attributes. Leaf tags are self-closed.
// Example output: <section class="hero"><div class="container"><h1/><p/><a class="btn"/></div></section>
func extractPageSkeleton(html string) string {
	var b strings.Builder
	i := 0
	n := len(html)
	depth := 0
	maxDepth := 6
	skipUntil := "" // non-empty when inside a skip tag

	for i < n && b.Len() < 700 {
		if html[i] != '<' {
			// Skip text content.
			i++
			continue
		}

		// Find end of tag.
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			break
		}
		tag := html[i : i+end+1]
		i += end + 1

		// Skip comments.
		if strings.HasPrefix(tag, "<!--") {
			if ci := strings.Index(html[i-1:], "-->"); ci >= 0 {
				i = i - 1 + ci + 3
			}
			continue
		}

		// Parse tag name.
		inner := tag[1 : len(tag)-1]
		if len(inner) == 0 {
			continue
		}
		isClosing := inner[0] == '/'
		if isClosing {
			inner = inner[1:]
		}
		// Self-closing tag like <br/>
		isSelfClose := inner[len(inner)-1] == '/'
		if isSelfClose {
			inner = inner[:len(inner)-1]
		}
		inner = strings.TrimSpace(inner)

		// Extract tag name (up to first space or end).
		tagName := inner
		if sp := strings.IndexAny(inner, " \t\n\r"); sp > 0 {
			tagName = inner[:sp]
		}
		tagName = strings.ToLower(tagName)

		// Handle skip tags.
		if skipUntil != "" {
			if isClosing && tagName == skipUntil {
				skipUntil = ""
			}
			continue
		}
		if skeletonSkipTags[tagName] && !isClosing {
			skipUntil = tagName
			continue
		}

		if isClosing {
			if depth > 0 {
				depth--
			}
			if depth < maxDepth {
				b.WriteString("</" + tagName + ">")
			}
			continue
		}

		if depth >= maxDepth {
			if !isSelfClose && !skeletonLeafTags[tagName] {
				depth++
			}
			continue
		}

		// Extract class and id attributes.
		attrs := extractSkeletonAttrs(inner)
		if skeletonLeafTags[tagName] || isSelfClose {
			b.WriteString("<" + tagName + attrs + "/>")
		} else {
			b.WriteString("<" + tagName + attrs + ">")
			depth++
		}
	}

	result := b.String()
	if len(result) > 600 {
		// Truncate at last complete tag.
		if last := strings.LastIndex(result[:600], ">"); last > 0 {
			result = result[:last+1]
		} else {
			result = result[:600]
		}
	}
	return result
}

// extractSkeletonAttrs extracts class and id attributes from a tag's inner string.
func extractSkeletonAttrs(inner string) string {
	var attrs string
	for _, attr := range []string{"class", "id"} {
		idx := strings.Index(inner, attr+"=")
		if idx < 0 {
			continue
		}
		rest := inner[idx+len(attr)+1:]
		if len(rest) == 0 {
			continue
		}
		quote := rest[0]
		if quote != '"' && quote != '\'' {
			continue
		}
		end := strings.IndexByte(rest[1:], quote)
		if end < 0 {
			continue
		}
		val := rest[1 : end+1]
		if val != "" {
			attrs += " " + attr + `="` + val + `"`
		}
	}
	return attrs
}

// loadRecentPageSkeletons loads the last N page skeletons (excluding excludePath)
// so the LLM sees the structural patterns of recently built pages.
func loadRecentPageSkeletons(db *sql.DB, excludePath string, limit int) string {
	rows, err := db.Query(
		"SELECT path, content FROM ho_pages WHERE is_deleted = 0 AND path != ? ORDER BY updated_at DESC LIMIT ?",
		excludePath, limit,
	)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var b strings.Builder
	count := 0
	for rows.Next() {
		var path, content string
		if rows.Scan(&path, &content) != nil {
			continue
		}
		skeleton := extractPageSkeleton(content)
		if skeleton == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		entry := path + ": " + skeleton
		if b.Len()+len(entry) > 1500 {
			break
		}
		b.WriteString(entry)
		count++
	}
	if count == 0 {
		return ""
	}
	return b.String()
}

// loadGlobalCSSForCatalog returns the first global CSS file content (capped at 4000 chars)
// for extracting a class catalog to inject into page save/patch results.
func loadGlobalCSSForCatalog(db *sql.DB) string {
	var content sql.NullString
	db.QueryRow("SELECT content FROM ho_assets WHERE scope = 'global' AND filename LIKE '%.css' ORDER BY filename LIMIT 1").Scan(&content)
	if content.Valid && len(content.String) > 0 {
		css := content.String
		if len(css) > 4000 {
			css = css[:4000]
		}
		return css
	}
	return ""
}
