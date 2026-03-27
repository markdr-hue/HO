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
	"regexp"
	"strings"
)

// captureLayoutVersion saves the current layout state into version history.
func captureLayoutVersion(db *sql.DB, layoutID int, name string, head, tmpl sql.NullString, changedBy string) {
	var maxVer int
	if err := db.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_layout_versions WHERE layout_id = ?", layoutID).Scan(&maxVer); err != nil {
		slog.Warn("captureLayoutVersion: failed to read max version", "layout_id", layoutID, "error", err)
		return
	}
	if _, err := db.Exec(
		`INSERT INTO ho_layout_versions (layout_id, name, head_content, template, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		layoutID, name, head, tmpl, maxVer+1, changedBy,
	); err != nil {
		slog.Warn("captureLayoutVersion: failed to insert version", "layout_id", layoutID, "error", err)
	}
}

// ---------------------------------------------------------------------------
// LayoutTool — manage site layouts (nav, footer, shared structure)
// ---------------------------------------------------------------------------

type LayoutTool struct{}

func (t *LayoutTool) Name() string { return "manage_layout" }
func (t *LayoutTool) Description() string {
	return "Save, get, list, or revert page layouts."
}

func (t *LayoutTool) Guide() string {
	return `### Layout System (manage_layout)
- template: HTML shell with {{content}} marker (see Platform Rules for how pages and layouts interact).
- head_content: extra HTML for <head> (fonts, meta tags, CDN links). Shared CSS/JS auto-injected — don't duplicate.
- "default" layout applies to all pages unless overridden per-page.
- Pages using layout="none" render content as-is without layout wrapping.
- patch action: apply targeted search/replace. Use patches=[{"search":"old","replace":"new"}] and field=template|head_content.`
}

func (t *LayoutTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"save", "patch", "get", "list", "history", "revert"},
			},
			"version": map[string]interface{}{
				"type":        "integer",
				"description": "Version number to restore (revert action)",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": `Layout name. Use "default" for the main site layout.`,
			},
			"head_content": map[string]interface{}{
				"type":        "string",
				"description": "Extra HTML for <head> (Google Fonts, custom meta, favicons). Shared CSS/JS from /assets/ are auto-injected — do NOT include them here.",
			},
			"template": map[string]interface{}{
				"type":        "string",
				"description": "Complete HTML shell with {{content}} marker. Include header/nav, <main>{{content}}</main>, and footer. The server replaces {{content}} with page HTML.",
			},
			"patches": map[string]interface{}{
				"type":        "string",
				"description": `JSON array of search/replace pairs for patch action: [{"search":"old","replace":"new"}]. Applies to template by default, or specify field.`,
			},
			"field": map[string]interface{}{
				"type":        "string",
				"description": "Which layout field to patch: template (default) or head_content.",
				"enum":        []string{"template", "head_content"},
			},
		},
		"required": []string{},
	}
}

func (t *LayoutTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"save":    t.save,
		"patch":   t.patch,
		"get":     t.get,
		"list":    t.list,
		"history": t.history,
		"revert":  t.revert,
	}, func(a map[string]interface{}) string {
		if _, has := a["patches"]; has {
			return "patch"
		}
		if _, has := a["version"]; has {
			return "revert"
		}
		if _, has := a["template"]; has {
			return "save"
		}
		if _, has := a["head_content"]; has {
			return "save"
		}
		if _, has := a["name"]; has {
			return "get"
		}
		return "list"
	})
}

// Regexes for stripping content that shouldn't be in layout fields.
var (
	layoutHeadBlockRe = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`)
	layoutDocShellRe  = regexp.MustCompile(`(?i)</?(!DOCTYPE|html|head|body)[^>]*>`)
	layoutAssetLinkRe = regexp.MustCompile(`(?i)<link[^>]*href=["']/assets/[^"']*["'][^>]*/?>`)
	layoutAssetSrcRe  = regexp.MustCompile(`(?i)<script[^>]*src=["']/assets/[^"']*["'][^>]*>[\s\S]*?</script>`)
)

func (t *LayoutTool) save(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required (use \"default\" for the main layout)"}, nil
	}

	// Track which fields were explicitly provided (vs omitted).
	headContent, headGiven := args["head_content"].(string)
	template, templateGiven := args["template"].(string)

	var warnings []string

	// Strip entire <head>...</head> block from template (content between head tags
	// would otherwise render as visible text inside <body>).
	if templateGiven && layoutHeadBlockRe.MatchString(template) {
		template = layoutHeadBlockRe.ReplaceAllString(template, "")
		warnings = append(warnings, "Stripped <head>...</head> block from template — use the head_content field for fonts/meta/favicons")
	}

	// Strip document shell tags and shared asset references from provided fields.
	for _, field := range []*string{&headContent, &template} {
		if layoutDocShellRe.MatchString(*field) {
			*field = layoutDocShellRe.ReplaceAllString(*field, "")
			warnings = append(warnings, "Stripped <!DOCTYPE>/<html>/<head>/<body> tags — the server handles document structure")
		}
	}
	for _, field := range []*string{&headContent, &template} {
		if layoutAssetLinkRe.MatchString(*field) {
			*field = layoutAssetLinkRe.ReplaceAllString(*field, "")
			warnings = append(warnings, "Stripped <link> for /assets/ — shared CSS is auto-injected by the server")
		}
		if layoutAssetSrcRe.MatchString(*field) {
			*field = layoutAssetSrcRe.ReplaceAllString(*field, "")
			warnings = append(warnings, "Stripped <script src='/assets/'> — shared JS is auto-injected by the server")
		}
	}

	// Strip literal {{head_content}} from template — it's not a valid placeholder.
	// head_content is a separate field injected into <head> by the server.
	if templateGiven && strings.Contains(template, "{{head_content}}") {
		template = strings.ReplaceAll(template, "{{head_content}}", "")
		warnings = append(warnings, "Stripped {{head_content}} from template — head_content is injected into <head> automatically")
	}

	// Validate template contains {{content}} marker.
	if templateGiven && !strings.Contains(template, "{{content}}") {
		warnings = append(warnings, "Template is missing {{content}} marker — page content will be appended after the template instead of replacing a placeholder")
	}

	// Before upsert: capture existing layout into version history.
	var existingID int
	var oldHead, oldTemplate sql.NullString
	qErr := ctx.DB.QueryRow(
		"SELECT id, head_content, template FROM ho_layouts WHERE name = ?", name,
	).Scan(&existingID, &oldHead, &oldTemplate)
	if qErr == nil {
		captureLayoutVersion(ctx.DB, existingID, name, oldHead, oldTemplate, "brain")

		// Merge: keep existing values for fields not provided in this call.
		if !headGiven {
			headContent = oldHead.String
		}
		if !templateGiven {
			template = oldTemplate.String
		}
	}

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_layouts (name, head_content, template, updated_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(name) DO UPDATE SET
		   head_content = excluded.head_content,
		   template = excluded.template,
		   updated_at = CURRENT_TIMESTAMP`,
		name, headContent, template,
	)
	if err != nil {
		return nil, fmt.Errorf("saving layout: %w", err)
	}

	// Tell the LLM what this layout provides so pages don't duplicate nav/header/footer.
	lowerTemplate := strings.ToLower(template)
	resultData := map[string]interface{}{
		"name": name,
		"layout_provides": map[string]interface{}{
			"has_nav":    strings.Contains(lowerTemplate, "<nav"),
			"has_header": strings.Contains(lowerTemplate, "<header"),
			"has_footer": strings.Contains(lowerTemplate, "<footer"),
			"note":       "Pages replace {{content}} in the template — do not duplicate nav/header/footer in page content.",
		},
	}
	if len(warnings) > 0 {
		resultData["warnings"] = warnings
	}
	return &Result{Success: true, Data: resultData}, nil
}

// ---------------------------------------------------------------------------
// patch — apply search/replace pairs to a layout field without full rewrite
// ---------------------------------------------------------------------------

func (t *LayoutTool) patch(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		name = "default"
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

	field, _ := args["field"].(string)
	if field == "" {
		field = "template"
	}

	// Load the existing layout.
	var layoutID int
	var headContent, tmpl sql.NullString
	err := ctx.DB.QueryRow(
		"SELECT id, head_content, template FROM ho_layouts WHERE name = ?", name,
	).Scan(&layoutID, &headContent, &tmpl)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("layout %q not found", name)}, nil
	}

	// Save version history before modifying.
	captureLayoutVersion(ctx.DB, layoutID, name, headContent, tmpl, "brain")

	// Select the target field content.
	var target string
	switch field {
	case "template":
		target = tmpl.String
	case "head_content":
		target = headContent.String
	default:
		return &Result{Success: false, Error: fmt.Sprintf("invalid field %q — use template or head_content", field)}, nil
	}

	// Apply patches (first-match by default to prevent duplication).
	target, patchRes := applyPatches(target, patches)

	if len(patchRes.Applied) == 0 && len(patchRes.NotFound) > 0 {
		return &Result{Success: false, Error: "no patches matched", Data: map[string]interface{}{"not_found": patchRes.NotFound}}, nil
	}

	// Write back the patched field.
	_, err = ctx.DB.Exec(
		fmt.Sprintf("UPDATE ho_layouts SET %s = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", field),
		target, layoutID,
	)
	if err != nil {
		return nil, fmt.Errorf("saving patched layout: %w", err)
	}

	resultData := map[string]interface{}{
		"name":    name,
		"field":   field,
		"applied": len(patchRes.Applied),
	}
	if len(patchRes.NotFound) > 0 {
		resultData["not_found"] = patchRes.NotFound
	}
	if len(patchRes.Warnings) > 0 {
		resultData["patch_warnings"] = patchRes.Warnings
	}
	return &Result{Success: true, Data: resultData}, nil
}

func (t *LayoutTool) get(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		name = "default"
	}

	var headContent, template string
	var createdAt, updatedAt string
	err := ctx.DB.QueryRow(
		"SELECT head_content, template, created_at, updated_at FROM ho_layouts WHERE name = ?",
		name,
	).Scan(&headContent, &template, &createdAt, &updatedAt)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("layout %q not found", name)}, nil
	}

	data := map[string]interface{}{
		"name":         name,
		"head_content": headContent,
		"template":     template,
		"created_at":   createdAt,
		"updated_at":   updatedAt,
	}

	return &Result{Success: true, Data: data}, nil
}

func (t *LayoutTool) list(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query("SELECT name, length(COALESCE(template, '')) AS size, created_at, updated_at FROM ho_layouts ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("listing layouts: %w", err)
	}
	defer rows.Close()

	var layouts []map[string]interface{}
	for rows.Next() {
		var name, createdAt, updatedAt string
		var size int
		if rows.Scan(&name, &size, &createdAt, &updatedAt) == nil {
			layouts = append(layouts, map[string]interface{}{
				"name":       name,
				"size":       size,
				"created_at": createdAt,
				"updated_at": updatedAt,
			})
		}
	}
	return &Result{Success: true, Data: map[string]interface{}{
		"layouts": layouts,
		"count":   len(layouts),
	}}, nil
}

// ---------------------------------------------------------------------------
// history — view version history for a layout
// ---------------------------------------------------------------------------

func (t *LayoutTool) history(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		name = "default"
	}

	var layoutID int
	err := ctx.DB.QueryRow("SELECT id FROM ho_layouts WHERE name = ?", name).Scan(&layoutID)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("layout %q not found", name)}, nil
	}

	rows, err := ctx.DB.Query(
		"SELECT version_number, changed_by, created_at FROM ho_layout_versions WHERE layout_id = ? ORDER BY version_number DESC LIMIT 20",
		layoutID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying layout history: %w", err)
	}
	defer rows.Close()

	var versions []map[string]interface{}
	for rows.Next() {
		var ver int
		var changedBy sql.NullString
		var createdAt string
		if rows.Scan(&ver, &changedBy, &createdAt) == nil {
			versions = append(versions, map[string]interface{}{
				"version":    ver,
				"changed_by": changedBy.String,
				"created_at": createdAt,
			})
		}
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"name":     name,
		"versions": versions,
	}}, nil
}

// ---------------------------------------------------------------------------
// revert — restore a layout to a previous version
// ---------------------------------------------------------------------------

func (t *LayoutTool) revert(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		name = "default"
	}

	version, ok := args["version"].(float64)
	if !ok || version < 1 {
		return &Result{Success: false, Error: "version (number) is required"}, nil
	}

	// Find the layout.
	var layoutID int
	var oldHead, oldTemplate sql.NullString
	err := ctx.DB.QueryRow(
		"SELECT id, head_content, template FROM ho_layouts WHERE name = ?", name,
	).Scan(&layoutID, &oldHead, &oldTemplate)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("layout %q not found", name)}, nil
	}

	// Load the requested version.
	var verHead, verTemplate sql.NullString
	err = ctx.DB.QueryRow(
		"SELECT head_content, template FROM ho_layout_versions WHERE layout_id = ? AND version_number = ?",
		layoutID, int(version),
	).Scan(&verHead, &verTemplate)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("version %d not found for layout %q", int(version), name)}, nil
	}

	// Save current state as a new version (so revert is reversible).
	captureLayoutVersion(ctx.DB, layoutID, name, oldHead, oldTemplate, "revert")

	// Restore the old version.
	_, err = ctx.DB.Exec(
		"UPDATE ho_layouts SET head_content = ?, template = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		verHead, verTemplate, layoutID,
	)
	if err != nil {
		return nil, fmt.Errorf("reverting layout: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"name":             name,
		"restored_version": int(version),
	}}, nil
}

func (t *LayoutTool) MaxResultSize() int { return 16000 }

func (t *LayoutTool) Summarize(result string) string {
	r, data, _, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if data == nil {
		return summarizeTruncate(result, 300)
	}
	name, _ := data["name"].(string)
	// Preserve layout_provides and warnings for the LLM context.
	var parts []string
	parts = append(parts, fmt.Sprintf(`"success":true,"layout":"%s"`, name))
	if provides, ok := data["layout_provides"]; ok {
		pJSON, _ := json.Marshal(provides)
		parts = append(parts, fmt.Sprintf(`"layout_provides":%s`, pJSON))
	}
	if warnings, ok := data["warnings"]; ok {
		wJSON, _ := json.Marshal(warnings)
		parts = append(parts, fmt.Sprintf(`"warnings":%s`, wJSON))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
