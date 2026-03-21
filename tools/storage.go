/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxFileSize = 10 << 20 // 10 MB
const maxFileVersions = 10   // keep last N versions per file

// storageConfig holds the table/path/URL differences between "assets" and "files".
type storageConfig struct {
	table    string // DB table name
	dirName  string // subdirectory under data/sites/{id}/
	urlBase  string // URL prefix for serving
	metaCol  string // metadata column name (alt_text for assets, description for files)
	metaDesc string // human label for the metadata field
}

var storageConfigs = map[string]storageConfig{
	"assets": {table: "ho_assets", dirName: "ho_assets", urlBase: "/assets/", metaCol: "alt_text", metaDesc: "Alt text for images"},
	"files":  {table: "ho_files", dirName: "ho_files", urlBase: "/files/", metaCol: "description", metaDesc: "Description of the file"},
}

func getStorageConfig(args map[string]interface{}) (storageConfig, error) {
	s, _ := args["storage"].(string)
	if s == "" {
		s = "assets" // default
	}
	cfg, ok := storageConfigs[s]
	if !ok {
		return storageConfig{}, fmt.Errorf("invalid storage: %q (use \"assets\" or \"files\")", s)
	}
	return cfg, nil
}

// storageDir returns the storage directory for a site, creating it if needed.
func storageDir(siteID int, dirName string) (string, error) {
	dir := filepath.Join("data", "sites", fmt.Sprintf("%d", siteID), dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s directory: %w", dirName, err)
	}
	return dir, nil
}

// sanitizePath cleans a filename/path for safe storage.
// Allows forward-slash-separated subdirectories but rejects path traversal.
func sanitizePath(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") {
		name = strings.TrimLeft(name, "/")
	}
	parts := strings.Split(name, "/")
	var clean []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			continue
		}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return ""
	}
	return strings.Join(clean, "/")
}

// sanitizeFilename sanitizes a path and strips a redundant storage prefix.
// LLMs often send "assets/css/styles.css" when the filename should be
// "css/styles.css" (the "/assets/" prefix is added by the URL routing).
func sanitizeFilename(name, storageDir string) string {
	name = sanitizePath(name)
	if prefix := storageDir + "/"; strings.HasPrefix(name, prefix) {
		name = name[len(prefix):]
	}
	return name
}

// inferContentType returns a MIME type based on the file extension.
func inferContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html"
	case ".xml":
		return "application/xml"
	case ".txt":
		return "text/plain"
	case ".csv":
		return "text/csv"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	case ".pdf":
		return "application/pdf"
	case ".ico":
		return "image/x-icon"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}

// isTextContentType returns true if the content type is text-based.
func isTextContentType(ct string) bool {
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/javascript", "application/json", "application/xml", "image/svg+xml":
		return true
	}
	return false
}

// decodeFileData decodes content (text) or data (base64) into bytes.
func decodeFileData(content, dataB64 string) ([]byte, error) {
	if content != "" {
		return []byte(content), nil
	}
	// Strip data URI prefix if present.
	if idx := strings.Index(dataB64, ","); idx != -1 && strings.Contains(dataB64[:idx], "base64") {
		dataB64 = dataB64[idx+1:]
	}
	return base64.StdEncoding.DecodeString(dataB64)
}

// writeFileToDisk writes data to the storage directory, creating subdirs as needed.
func writeFileToDisk(siteID int, dirName, filename string, data []byte) (string, error) {
	dir, err := storageDir(siteID, dirName)
	if err != nil {
		return "", err
	}
	storagePath := filepath.Join(dir, filepath.FromSlash(filename))
	if subDir := filepath.Dir(storagePath); subDir != dir {
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			return "", fmt.Errorf("creating subdirectory: %w", err)
		}
	}
	if err := os.WriteFile(storagePath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	return storagePath, nil
}

// storageParam is the shared parameter definition for the storage field.
var storageParam = map[string]interface{}{
	"type":        "string",
	"description": "Storage type: \"assets\" (design files at /assets/) or \"files\" (downloads at /files/). Default: assets.",
	"enum":        []string{"assets", "files"},
}

// ---------------------------------------------------------------------------
// FilesTool — unified manage_files tool
// ---------------------------------------------------------------------------

// FilesTool consolidates save, get, list, delete, and rename into a single tool.
type FilesTool struct{}

func (t *FilesTool) Name() string { return "manage_files" }
func (t *FilesTool) Description() string {
	return "Save CSS/JS/SVG assets, fetch images from URLs, or manage stored files. Use scope='global' for site-wide (auto-injected), scope='page' for page-specific."
}

func (t *FilesTool) Guide() string {
	return `### Asset System (manage_files)
- Saves any text file: .css, .js, .svg, .html, .json — use content parameter.
- scope="global": CSS/JS auto-injected on ALL pages. scope="page": only when listed in page assets array.
- Load order: Global CSS -> Page CSS -> Layout -> [content] -> Global JS -> Page JS.
- SVG files: save inline SVG markup as .svg files. Reference as <img src="/assets/icon.svg"> or embed inline.
- patch action: apply targeted search/replace without rewriting the whole file. Use patches=[{"search":"old","replace":"new"}].
- fetch_image action downloads images from URLs and saves locally (JPEG, PNG, GIF, WebP, SVG, ICO, AVIF).`
}

func (t *FilesTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"save", "patch", "edit_lines", "regex_replace", "get", "list", "delete", "rename", "history", "rollback", "fetch_image", "search"},
			},
			"url":          map[string]interface{}{"type": "string", "description": "URL to download image from (for fetch_image action). Supports JPEG, PNG, GIF, WebP, SVG, ICO, AVIF."},
			"storage":      storageParam,
			"filename":     map[string]interface{}{"type": "string", "description": "Path (e.g. styles.css, css/main.css)"},
			"content_type": map[string]interface{}{"type": "string", "description": "MIME type. Auto-detected if omitted."},
			"content":      map[string]interface{}{"type": "string", "description": "Text content (CSS, JS, SVG, HTML)"},
			"data":         map[string]interface{}{"type": "string", "description": "Base64 content for binary files"},
			"scope":        map[string]interface{}{"type": "string", "description": "Asset scope: \"global\" (auto-injected on every page, default) or \"page\" (only injected when a page lists it in its assets array)", "enum": []string{"global", "page"}},
			"description":  map[string]interface{}{"type": "string", "description": "Alt text (images) or description"},
			"old_filename": map[string]interface{}{"type": "string", "description": "Current filename (rename)"},
			"new_filename": map[string]interface{}{"type": "string", "description": "New filename (rename)"},
			"version":      map[string]interface{}{"type": "integer", "description": "Version number to restore (rollback)"},
			"limit":        map[string]interface{}{"type": "number", "description": "Max entries to return (for history action, default: 10)"},
			"patches":      map[string]interface{}{"type": "string", "description": `JSON array of search/replace pairs for patch action: [{"search":"old text","replace":"new text"}]. Only for text files (CSS, JS, etc.).`},
			"edits":        map[string]interface{}{"type": "string", "description": `JSON array of line edits: [{"line":5,"content":"new"},{"from":10,"to":12,"content":"replacement"},{"after":20,"content":"inserted"},{"delete":[30,31]}]`},
			"replacements": map[string]interface{}{"type": "string", "description": `JSON array of regex replacements: [{"pattern":"old_pattern","replace":"new_value","count":0}]`},
			"query":        map[string]interface{}{"type": "string", "description": "Search query for the search action — matches against filename and content"},
		},
		"required": []string{},
	}
}

func (t *FilesTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"save":          t.executeSave,
		"patch":         t.executePatch,
		"edit_lines":    t.executeEditLines,
		"regex_replace": t.executeRegexReplace,
		"get":           t.executeGet,
		"list":          t.executeList,
		"delete":        t.executeDelete,
		"rename":        t.executeRename,
		"history":       t.executeHistory,
		"rollback":      t.executeRollback,
		"fetch_image":   t.executeFetchImage,
		"search":        t.executeSearch,
	}, func(a map[string]interface{}) string {
		if _, has := a["query"]; has {
			return "search"
		}
		if _, has := a["url"]; has {
			return "fetch_image"
		}
		if _, has := a["edits"]; has {
			return "edit_lines"
		}
		if _, has := a["replacements"]; has {
			return "regex_replace"
		}
		if _, has := a["patches"]; has {
			return "patch"
		}
		if _, has := a["version"]; has {
			return "rollback"
		}
		if _, has := a["content"]; has {
			return "save"
		}
		if _, has := a["data"]; has {
			return "save"
		}
		if _, has := a["new_filename"]; has {
			return "rename"
		}
		if _, has := a["filename"]; has {
			return "get"
		}
		return "list"
	})
}

func (t *FilesTool) executeSave(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	filename, _ := args["filename"].(string)
	if filename == "" {
		return &Result{Success: false, Error: "filename is required"}, nil
	}
	content, _ := args["content"].(string)
	dataB64, _ := args["data"].(string)
	if content == "" && dataB64 == "" {
		return &Result{Success: false, Error: "either content or data (base64) is required"}, nil
	}
	contentType, _ := args["content_type"].(string)
	desc, _ := args["description"].(string)
	scope, _ := args["scope"].(string)
	if scope == "" {
		scope = "global"
	}

	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
	}

	fileData, err := decodeFileData(content, dataB64)
	if err != nil {
		return &Result{Success: false, Error: "invalid base64 data"}, nil
	}
	if len(fileData) > maxFileSize {
		return &Result{Success: false, Error: fmt.Sprintf("file too large (%d bytes, max %d)", len(fileData), maxFileSize)}, nil
	}

	// Always infer from extension for known types — LLMs sometimes pass
	// content_type: "text/plain" for CSS/JS/SVG which breaks browser rendering.
	if inferred := inferContentType(filename); inferred != "application/octet-stream" {
		contentType = inferred
	} else if contentType == "" {
		contentType = inferred
	}

	// Use context-aware DB calls so the pipeline's tool timeout can cancel stuck writes.
	dbCtx := ctx.Context()

	// Before overwrite: capture existing text file into version history.
	if isTextContentType(contentType) {
		var oldStoragePath sql.NullString
		qErr := ctx.DB.QueryRowContext(dbCtx,
			fmt.Sprintf("SELECT storage_path FROM %s WHERE filename = ?", cfg.table),
			filename,
		).Scan(&oldStoragePath)
		if qErr == nil {
			// File exists — read current content from disk and save version.
			oldPath := oldStoragePath.String
			if oldPath == "" {
				oldPath = filepath.Join("data", "sites", fmt.Sprintf("%d", ctx.SiteID), cfg.dirName, filepath.FromSlash(filename))
			}
			if oldData, readErr := os.ReadFile(oldPath); readErr == nil {
				var maxVer int
				ctx.DB.QueryRowContext(dbCtx,
					"SELECT COALESCE(MAX(version_number), 0) FROM ho_file_versions WHERE storage_type = ? AND filename = ?",
					cfg.table, filename,
				).Scan(&maxVer)
				ctx.DB.ExecContext(dbCtx,
					`INSERT INTO ho_file_versions (storage_type, filename, content, content_type, size, version_number, changed_by)
					 VALUES (?, ?, ?, ?, ?, ?, 'brain')`,
					cfg.table, filename, string(oldData), contentType, len(oldData), maxVer+1,
				)
				// Prune: keep only last 10 versions.
				if maxVer+1 > maxFileVersions {
					ctx.DB.ExecContext(dbCtx,
						"DELETE FROM ho_file_versions WHERE storage_type = ? AND filename = ? AND version_number <= ?",
						cfg.table, filename, maxVer+1-maxFileVersions,
					)
				}
			}
		}
	}

	storagePath, err := writeFileToDisk(ctx.SiteID, cfg.dirName, filename, fileData)
	if err != nil {
		return nil, err
	}

	if cfg.table == "ho_assets" {
		_, err = ctx.DB.ExecContext(dbCtx,
			`INSERT INTO ho_assets (filename, content_type, size, storage_path, alt_text, scope)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(filename) DO UPDATE SET
			   content_type = excluded.content_type,
			   size = excluded.size,
			   storage_path = excluded.storage_path,
			   alt_text = excluded.alt_text,
			   scope = excluded.scope`,
			filename, contentType, len(fileData), storagePath, desc, scope,
		)
	} else {
		_, err = ctx.DB.ExecContext(dbCtx,
			fmt.Sprintf(
				`INSERT INTO %s (filename, content_type, size, storage_path, %s)
				 VALUES (?, ?, ?, ?, ?)
				 ON CONFLICT(filename) DO UPDATE SET
				   content_type = excluded.content_type,
				   size = excluded.size,
				   storage_path = excluded.storage_path,
				   %s = excluded.%s`,
				cfg.table, cfg.metaCol, cfg.metaCol, cfg.metaCol,
			),
			filename, contentType, len(fileData), storagePath, desc,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("saving record: %w", err)
	}

	resultData := map[string]interface{}{
		"filename":     filename,
		"content_type": contentType,
		"size":         len(fileData),
		"url":          cfg.urlBase + filename,
	}
	if cfg.table == "ho_assets" {
		resultData["scope"] = scope
	}

	// Post-save validation and summary for CSS files.
	if strings.HasSuffix(strings.ToLower(filename), ".css") {
		if warnings := validateCSSContent(filename, fileData); len(warnings) > 0 {
			resultData["warnings"] = warnings
		}
		// Extract CSS classes and variables so the AI remembers them for page creation.
		if summary := extractCSSSummary(string(fileData)); summary != "" {
			resultData["css_classes"] = summary
		}
	}

	// Inject endpoint catalog after JS saves so the LLM has correct URLs for the next file.
	if strings.HasSuffix(strings.ToLower(filename), ".js") {
		if catalog := buildAPICatalog(ctx.DB); catalog != "" {
			resultData["available_endpoints"] = catalog
		}
	}

	// Remind the LLM to wire page-scope assets into the page's assets array.
	if scope == "page" {
		resultData["reminder"] = fmt.Sprintf("Page-scope asset saved. Add '%s' to the page's assets array via manage_pages.", filename)
	}

	return &Result{Success: true, Data: resultData}, nil
}

// ---------------------------------------------------------------------------
// patch — apply search/replace pairs to a text file without full rewrite
// ---------------------------------------------------------------------------

func (t *FilesTool) executePatch(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	filename, _ := args["filename"].(string)
	if filename == "" {
		return &Result{Success: false, Error: "filename is required"}, nil
	}
	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
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

	// Read current file from disk.
	var storagePath string
	var contentType sql.NullString
	err = ctx.DB.QueryRow(
		fmt.Sprintf("SELECT storage_path, content_type FROM %s WHERE filename = ?", cfg.table),
		filename,
	).Scan(&storagePath, &contentType)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: "file not found"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying %s: %w", cfg.table, err)
	}

	ct := contentType.String
	if !isTextContentType(ct) {
		return &Result{Success: false, Error: fmt.Sprintf("patch only works on text files, got %s", ct)}, nil
	}

	if storagePath == "" {
		storagePath = filepath.Join("data", "sites", fmt.Sprintf("%d", ctx.SiteID), cfg.dirName, filepath.FromSlash(filename))
	}
	fileData, err := os.ReadFile(storagePath)
	if err != nil {
		return &Result{Success: false, Error: "file not found on disk"}, nil
	}

	// Save version history before modifying.
	dbCtx := ctx.Context()
	var maxVer int
	ctx.DB.QueryRowContext(dbCtx,
		"SELECT COALESCE(MAX(version_number), 0) FROM ho_file_versions WHERE storage_type = ? AND filename = ?",
		cfg.table, filename,
	).Scan(&maxVer)
	ctx.DB.ExecContext(dbCtx,
		`INSERT INTO ho_file_versions (storage_type, filename, content, content_type, size, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, ?, 'brain')`,
		cfg.table, filename, string(fileData), ct, len(fileData), maxVer+1,
	)

	// Apply patches (first-match by default to prevent duplication).
	modified, patchRes := applyPatches(string(fileData), patches)

	if len(patchRes.Applied) == 0 && len(patchRes.NotFound) > 0 {
		return &Result{Success: false, Error: "no patches matched", Data: map[string]interface{}{"not_found": patchRes.NotFound}}, nil
	}

	// Write modified content to disk.
	newData := []byte(modified)
	if err := os.WriteFile(storagePath, newData, 0o644); err != nil {
		return nil, fmt.Errorf("writing patched file: %w", err)
	}

	// Update size in DB.
	ctx.DB.ExecContext(dbCtx,
		fmt.Sprintf("UPDATE %s SET size = ? WHERE filename = ?", cfg.table),
		len(newData), filename,
	)

	resultData := map[string]interface{}{
		"filename": filename,
		"applied":  len(patchRes.Applied),
		"size":     len(newData),
	}
	if len(patchRes.NotFound) > 0 {
		resultData["not_found"] = patchRes.NotFound
	}
	if len(patchRes.Warnings) > 0 {
		resultData["patch_warnings"] = patchRes.Warnings
	}

	// Post-patch validation for CSS files.
	if strings.HasSuffix(strings.ToLower(filename), ".css") {
		if warnings := validateCSSContent(filename, newData); len(warnings) > 0 {
			resultData["warnings"] = warnings
		}
		if summary := extractCSSSummary(modified); summary != "" {
			resultData["css_classes"] = summary
		}
	}

	return &Result{Success: true, Data: resultData}, nil
}

// executeEditLines applies line-number-based edits to a text file.
func (t *FilesTool) executeEditLines(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	filename, _ := args["filename"].(string)
	if filename == "" {
		return &Result{Success: false, Error: "filename is required"}, nil
	}
	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
	}
	editsStr, _ := args["edits"].(string)
	if editsStr == "" {
		return &Result{Success: false, Error: "edits is required"}, nil
	}
	var edits []lineEdit
	if err := json.Unmarshal([]byte(editsStr), &edits); err != nil {
		return &Result{Success: false, Error: "edits must be a JSON array: " + err.Error()}, nil
	}

	content, storagePath, ct, versionErr := t.readAndVersion(ctx, cfg, filename)
	if versionErr != nil {
		return versionErr, nil
	}

	modified, applied, editErr := applyLineEdits(content, edits)
	if editErr != nil {
		return &Result{Success: false, Error: editErr.Error()}, nil
	}

	newData := []byte(modified)
	if err := os.WriteFile(storagePath, newData, 0o644); err != nil {
		return nil, fmt.Errorf("writing edited file: %w", err)
	}
	if _, err := ctx.DB.Exec(fmt.Sprintf("UPDATE %s SET size = ? WHERE filename = ?", cfg.table), len(newData), filename); err != nil {
		slog.Warn("failed to update file size in DB", "table", cfg.table, "filename", filename, "error", err)
	}

	resultData := map[string]interface{}{
		"filename": filename,
		"applied":  applied,
		"lines":    len(strings.Split(modified, "\n")),
		"size":     len(newData),
	}
	if strings.HasSuffix(strings.ToLower(filename), ".css") {
		if summary := extractCSSSummary(modified); summary != "" {
			resultData["css_classes"] = summary
		}
	}
	_ = ct // content type already validated
	return &Result{Success: true, Data: resultData}, nil
}

// executeRegexReplace applies regex-based replacements to a text file.
func (t *FilesTool) executeRegexReplace(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	filename, _ := args["filename"].(string)
	if filename == "" {
		return &Result{Success: false, Error: "filename is required"}, nil
	}
	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
	}
	replStr, _ := args["replacements"].(string)
	if replStr == "" {
		return &Result{Success: false, Error: "replacements is required"}, nil
	}
	var replacements []regexReplacement
	if err := json.Unmarshal([]byte(replStr), &replacements); err != nil {
		return &Result{Success: false, Error: "replacements must be a JSON array: " + err.Error()}, nil
	}

	content, storagePath, ct, versionErr := t.readAndVersion(ctx, cfg, filename)
	if versionErr != nil {
		return versionErr, nil
	}

	modified, applied, replErr := applyRegexReplacements(content, replacements)
	if replErr != nil {
		return &Result{Success: false, Error: replErr.Error()}, nil
	}

	newData := []byte(modified)
	if err := os.WriteFile(storagePath, newData, 0o644); err != nil {
		return nil, fmt.Errorf("writing replaced file: %w", err)
	}
	if _, err := ctx.DB.Exec(fmt.Sprintf("UPDATE %s SET size = ? WHERE filename = ?", cfg.table), len(newData), filename); err != nil {
		slog.Warn("failed to update file size in DB", "table", cfg.table, "filename", filename, "error", err)
	}

	resultData := map[string]interface{}{
		"filename": filename,
		"applied":  applied,
		"size":     len(newData),
	}
	if strings.HasSuffix(strings.ToLower(filename), ".css") {
		if summary := extractCSSSummary(modified); summary != "" {
			resultData["css_classes"] = summary
		}
	}
	_ = ct
	return &Result{Success: true, Data: resultData}, nil
}

// readAndVersion reads a text file, saves a version, and returns the content.
// Returns (content, storagePath, contentType, errorResult).
func (t *FilesTool) readAndVersion(ctx *ToolContext, cfg storageConfig, filename string) (string, string, string, *Result) {
	var storagePath string
	var contentType sql.NullString
	err := ctx.DB.QueryRow(
		fmt.Sprintf("SELECT storage_path, content_type FROM %s WHERE filename = ?", cfg.table),
		filename,
	).Scan(&storagePath, &contentType)
	if err == sql.ErrNoRows {
		return "", "", "", &Result{Success: false, Error: "file not found"}
	}
	if err != nil {
		return "", "", "", &Result{Success: false, Error: fmt.Sprintf("querying %s: %v", cfg.table, err)}
	}

	ct := contentType.String
	if !isTextContentType(ct) {
		return "", "", "", &Result{Success: false, Error: fmt.Sprintf("edit only works on text files, got %s", ct)}
	}

	if storagePath == "" {
		storagePath = filepath.Join("data", "sites", fmt.Sprintf("%d", ctx.SiteID), cfg.dirName, filepath.FromSlash(filename))
	}
	fileData, err := os.ReadFile(storagePath)
	if err != nil {
		return "", "", "", &Result{Success: false, Error: "file not found on disk"}
	}

	// Save version history.
	dbCtx := ctx.Context()
	var maxVer int
	ctx.DB.QueryRowContext(dbCtx,
		"SELECT COALESCE(MAX(version_number), 0) FROM ho_file_versions WHERE storage_type = ? AND filename = ?",
		cfg.table, filename,
	).Scan(&maxVer)
	ctx.DB.ExecContext(dbCtx,
		`INSERT INTO ho_file_versions (storage_type, filename, content, content_type, size, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, ?, 'brain')`,
		cfg.table, filename, string(fileData), ct, len(fileData), maxVer+1,
	)

	return string(fileData), storagePath, ct, nil
}

func (t *FilesTool) executeGet(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	filename, _ := args["filename"].(string)
	if filename == "" {
		return &Result{Success: false, Error: "filename is required"}, nil
	}
	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
	}

	var storagePath string
	var contentType sql.NullString
	var size sql.NullInt64
	err = ctx.DB.QueryRow(
		fmt.Sprintf("SELECT storage_path, content_type, size FROM %s WHERE filename = ?", cfg.table),
		filename,
	).Scan(&storagePath, &contentType, &size)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: "not found"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying %s: %w", cfg.table, err)
	}

	if storagePath == "" {
		storagePath = filepath.Join("data", "sites", fmt.Sprintf("%d", ctx.SiteID), cfg.dirName, filepath.FromSlash(filename))
	}
	fileData, err := os.ReadFile(storagePath)
	if err != nil {
		return &Result{Success: false, Error: "file not found on disk"}, nil
	}

	ct := contentType.String
	result := map[string]interface{}{
		"filename":     filename,
		"content_type": ct,
		"size":         len(fileData),
		"url":          cfg.urlBase + filename,
	}
	if isTextContentType(ct) {
		result["content"] = string(fileData)
	} else {
		result["data"] = base64.StdEncoding.EncodeToString(fileData)
	}

	return &Result{Success: true, Data: result}, nil
}

func (t *FilesTool) executeList(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	var items []map[string]interface{}
	if cfg.table == "ho_assets" {
		rows, err := ctx.DB.Query("SELECT id, filename, content_type, size, alt_text, scope, created_at FROM ho_assets ORDER BY filename")
		if err != nil {
			return nil, fmt.Errorf("listing assets: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			var filename string
			var contentType, meta, scope sql.NullString
			var size sql.NullInt64
			var createdAt time.Time
			if err := rows.Scan(&id, &filename, &contentType, &size, &meta, &scope, &createdAt); err != nil {
				return nil, fmt.Errorf("scanning assets: %w", err)
			}
			items = append(items, map[string]interface{}{
				"id":           id,
				"filename":     filename,
				"content_type": contentType.String,
				"size":         size.Int64,
				"description":  meta.String,
				"scope":        scope.String,
				"url":          cfg.urlBase + filename,
				"created_at":   createdAt,
			})
		}
	} else {
		rows, err := ctx.DB.Query(
			fmt.Sprintf("SELECT id, filename, content_type, size, %s, created_at FROM %s ORDER BY filename", cfg.metaCol, cfg.table),
		)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", cfg.table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			var filename string
			var contentType, meta sql.NullString
			var size sql.NullInt64
			var createdAt time.Time
			if err := rows.Scan(&id, &filename, &contentType, &size, &meta, &createdAt); err != nil {
				return nil, fmt.Errorf("scanning %s: %w", cfg.table, err)
			}
			items = append(items, map[string]interface{}{
				"id":           id,
				"filename":     filename,
				"content_type": contentType.String,
				"size":         size.Int64,
				"description":  meta.String,
				"url":          cfg.urlBase + filename,
				"created_at":   createdAt,
			})
		}
	}

	return &Result{Success: true, Data: items}, nil
}

func (t *FilesTool) executeDelete(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	filename, _ := args["filename"].(string)
	if filename == "" {
		return &Result{Success: false, Error: "filename is required"}, nil
	}
	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
	}

	var storagePath string
	err = ctx.DB.QueryRow(
		fmt.Sprintf("SELECT storage_path FROM %s WHERE filename = ?", cfg.table),
		filename,
	).Scan(&storagePath)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: "not found"}, nil
	}

	_, err = ctx.DB.Exec(
		fmt.Sprintf("DELETE FROM %s WHERE filename = ?", cfg.table),
		filename,
	)
	if err != nil {
		return nil, fmt.Errorf("deleting record: %w", err)
	}

	if storagePath != "" {
		if err := os.Remove(storagePath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("removing file from disk: %w", err)
		}
	}

	LogDestructiveAction(ctx, "manage_files", "delete", filename)

	return &Result{Success: true, Data: map[string]interface{}{"deleted": filename}}, nil
}

func (t *FilesTool) executeRename(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	oldName, _ := args["old_filename"].(string)
	newName, _ := args["new_filename"].(string)
	if oldName == "" || newName == "" {
		return &Result{Success: false, Error: "old_filename and new_filename are required"}, nil
	}

	oldName = sanitizeFilename(oldName, cfg.dirName)
	newName = sanitizeFilename(newName, cfg.dirName)
	if oldName == "" || newName == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
	}

	var storagePath string
	err = ctx.DB.QueryRow(
		fmt.Sprintf("SELECT storage_path FROM %s WHERE filename = ?", cfg.table),
		oldName,
	).Scan(&storagePath)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: "not found"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying %s: %w", cfg.table, err)
	}

	dir, err := storageDir(ctx.SiteID, cfg.dirName)
	if err != nil {
		return nil, err
	}
	newStoragePath := filepath.Join(dir, filepath.FromSlash(newName))
	if subDir := filepath.Dir(newStoragePath); subDir != dir {
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating directory: %w", err)
		}
	}

	if storagePath != "" {
		if err := os.Rename(storagePath, newStoragePath); err != nil {
			return nil, fmt.Errorf("renaming file on disk: %w", err)
		}
	}

	newContentType := inferContentType(newName)
	_, err = ctx.DB.Exec(
		fmt.Sprintf("UPDATE %s SET filename = ?, storage_path = ?, content_type = ? WHERE filename = ?", cfg.table),
		newName, newStoragePath, newContentType, oldName,
	)
	if err != nil {
		return nil, fmt.Errorf("updating record: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"old_filename": oldName,
		"new_filename": newName,
		"url":          cfg.urlBase + newName,
	}}, nil
}

// ---------------------------------------------------------------------------
// history — view version history for a file
// ---------------------------------------------------------------------------

func (t *FilesTool) executeHistory(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	filename, _ := args["filename"].(string)
	if filename == "" {
		return &Result{Success: false, Error: "filename is required"}, nil
	}
	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	rows, err := ctx.DB.Query(
		"SELECT version_number, content_type, size, changed_by, created_at FROM ho_file_versions WHERE storage_type = ? AND filename = ? ORDER BY version_number DESC LIMIT ?",
		cfg.table, filename, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying file history: %w", err)
	}
	defer rows.Close()

	var versions []map[string]interface{}
	for rows.Next() {
		var ver, size int
		var contentType, changedBy sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&ver, &contentType, &size, &changedBy, &createdAt); err != nil {
			continue
		}
		versions = append(versions, map[string]interface{}{
			"version":      ver,
			"content_type": contentType.String,
			"size":         size,
			"changed_by":   changedBy.String,
			"created_at":   createdAt,
		})
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"filename": filename,
		"versions": versions,
	}}, nil
}

// ---------------------------------------------------------------------------
// rollback — restore a file to a previous version
// ---------------------------------------------------------------------------

func (t *FilesTool) executeRollback(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	filename, _ := args["filename"].(string)
	if filename == "" {
		return &Result{Success: false, Error: "filename is required"}, nil
	}
	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		return &Result{Success: false, Error: "invalid filename"}, nil
	}

	version, ok := args["version"].(float64)
	if !ok || version < 1 {
		return &Result{Success: false, Error: "version (number) is required"}, nil
	}

	// Load the requested version content.
	var versionContent string
	var versionCT sql.NullString
	err = ctx.DB.QueryRow(
		"SELECT content, content_type FROM ho_file_versions WHERE storage_type = ? AND filename = ? AND version_number = ?",
		cfg.table, filename, int(version),
	).Scan(&versionContent, &versionCT)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: fmt.Sprintf("version %d not found for %s", int(version), filename)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying version: %w", err)
	}

	// Save current file as a new version first (so rollback is reversible).
	oldPath := filepath.Join("data", "sites", fmt.Sprintf("%d", ctx.SiteID), cfg.dirName, filepath.FromSlash(filename))
	if oldData, readErr := os.ReadFile(oldPath); readErr == nil {
		var maxVer int
		ctx.DB.QueryRow(
			"SELECT COALESCE(MAX(version_number), 0) FROM ho_file_versions WHERE storage_type = ? AND filename = ?",
			cfg.table, filename,
		).Scan(&maxVer)
		ctx.DB.Exec(
			`INSERT INTO ho_file_versions (storage_type, filename, content, content_type, size, version_number, changed_by)
			 VALUES (?, ?, ?, ?, ?, ?, 'rollback')`,
			cfg.table, filename, string(oldData), versionCT.String, len(oldData), maxVer+1,
		)
	}

	// Write restored content to disk.
	restoredData := []byte(versionContent)
	storagePath, err := writeFileToDisk(ctx.SiteID, cfg.dirName, filename, restoredData)
	if err != nil {
		return nil, fmt.Errorf("writing restored file: %w", err)
	}

	// Update the main table record.
	contentType := versionCT.String
	if contentType == "" {
		contentType = inferContentType(filename)
	}
	ctx.DB.Exec(
		fmt.Sprintf("UPDATE %s SET size = ?, storage_path = ?, content_type = ? WHERE filename = ?", cfg.table),
		len(restoredData), storagePath, contentType, filename,
	)

	return &Result{Success: true, Data: map[string]interface{}{
		"filename":         filename,
		"restored_version": int(version),
		"size":             len(restoredData),
		"url":              cfg.urlBase + filename,
	}}, nil
}

// ---------------------------------------------------------------------------
// fetch_image — download image from URL and save as asset
// ---------------------------------------------------------------------------

// allowedImageTypes maps Content-Type prefixes to file extensions.
var allowedImageTypes = map[string]string{
	"image/jpeg":    ".jpg",
	"image/png":     ".png",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
	"image/x-icon":  ".ico",
	"image/avif":    ".avif",
}

const maxImageSize = 5 << 20 // 5 MB

func (t *FilesTool) executeFetchImage(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	rawURL, _ := args["url"].(string)
	if rawURL == "" {
		return &Result{Success: false, Error: "url is required"}, nil
	}

	// SSRF protection — reuse the same validation as make_http_request.
	if err := validateExternalURL(rawURL); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("blocked URL: %v", err)}, nil
	}

	cfg, err := getStorageConfig(args)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	resp, err := client.Get(rawURL)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("fetching image: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return &Result{Success: false, Error: fmt.Sprintf("HTTP %d from image URL", resp.StatusCode)}, nil
	}

	// Validate Content-Type.
	ct := resp.Header.Get("Content-Type")
	// Normalize: "image/jpeg; charset=utf-8" → "image/jpeg"
	if idx := strings.Index(ct, ";"); idx != -1 {
		ct = strings.TrimSpace(ct[:idx])
	}
	ct = strings.ToLower(ct)

	ext, ok := allowedImageTypes[ct]
	if !ok {
		return &Result{Success: false, Error: fmt.Sprintf("unsupported image type %q — supported: JPEG, PNG, GIF, WebP, SVG, ICO, AVIF", ct)}, nil
	}

	// Read body with size limit.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize+1))
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("reading image: %v", err)}, nil
	}
	if len(data) > maxImageSize {
		return &Result{Success: false, Error: fmt.Sprintf("image too large (>%d MB)", maxImageSize>>20)}, nil
	}

	// Determine filename.
	filename, _ := args["filename"].(string)
	if filename == "" {
		filename = filenameFromURL(rawURL, ext)
	}
	filename = sanitizeFilename(filename, cfg.dirName)
	if filename == "" {
		filename = "image" + ext
	}
	// Ensure correct extension if not already present.
	if filepath.Ext(filename) == "" {
		filename += ext
	}

	// Write to disk.
	storagePath, err := writeFileToDisk(ctx.SiteID, cfg.dirName, filename, data)
	if err != nil {
		return nil, fmt.Errorf("saving image: %w", err)
	}

	// Upsert into DB.
	desc, _ := args["description"].(string)
	scope, _ := args["scope"].(string)
	if scope == "" {
		scope = "global"
	}

	if cfg.table == "ho_assets" {
		_, err = ctx.DB.Exec(
			`INSERT INTO ho_assets (filename, content_type, size, storage_path, alt_text, scope)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(filename) DO UPDATE SET
			   content_type = excluded.content_type,
			   size = excluded.size,
			   storage_path = excluded.storage_path,
			   alt_text = excluded.alt_text,
			   scope = excluded.scope`,
			filename, ct, len(data), storagePath, desc, scope,
		)
	} else {
		_, err = ctx.DB.Exec(
			fmt.Sprintf(
				`INSERT INTO %s (filename, content_type, size, storage_path, %s)
				 VALUES (?, ?, ?, ?, ?)
				 ON CONFLICT(filename) DO UPDATE SET
				   content_type = excluded.content_type,
				   size = excluded.size,
				   storage_path = excluded.storage_path,
				   %s = excluded.%s`,
				cfg.table, cfg.metaCol, cfg.metaCol, cfg.metaCol,
			),
			filename, ct, len(data), storagePath, desc,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("saving image record: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"filename":     filename,
		"content_type": ct,
		"size":         len(data),
		"url":          cfg.urlBase + filename,
	}}, nil
}

// filenameFromURL extracts a clean filename from a URL path, with fallback.
func filenameFromURL(rawURL, fallbackExt string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "image" + fallbackExt
	}
	base := filepath.Base(u.Path)
	// Strip query params that might have leaked into the base.
	if idx := strings.Index(base, "?"); idx != -1 {
		base = base[:idx]
	}
	// If the base is empty, just a slash, or has no useful name, use fallback.
	if base == "" || base == "." || base == "/" {
		return "image" + fallbackExt
	}
	// Sanitize: keep only alphanumeric, dashes, underscores, dots.
	var clean strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			clean.WriteRune(r)
		}
	}
	result := clean.String()
	if result == "" {
		return "image" + fallbackExt
	}
	// Truncate very long filenames (Unsplash IDs can be long).
	if len(result) > 80 {
		ext := filepath.Ext(result)
		result = result[:80-len(ext)] + ext
	}
	return result
}

// validateCSSContent performs basic CSS syntax validation:
// brace matching, unclosed strings, and empty file detection.
func validateCSSContent(filename string, content []byte) []string {
	css := string(content)

	if strings.TrimSpace(css) == "" {
		return []string{fmt.Sprintf("CSS file %s is empty", filename)}
	}

	var warnings []string

	// Check brace matching (skip braces inside strings and comments).
	depth := 0
	inSingleQuote := false
	inDoubleQuote := false
	inComment := false
	for i := 0; i < len(css); i++ {
		c := css[i]
		if inComment {
			if c == '*' && i+1 < len(css) && css[i+1] == '/' {
				inComment = false
				i++ // skip /
			}
			continue
		}
		if c == '/' && i+1 < len(css) && css[i+1] == '*' {
			inComment = true
			i++ // skip *
			continue
		}
		if inSingleQuote {
			if c == '\'' && (i == 0 || css[i-1] != '\\') {
				inSingleQuote = false
			}
			continue
		}
		if inDoubleQuote {
			if c == '"' && (i == 0 || css[i-1] != '\\') {
				inDoubleQuote = false
			}
			continue
		}
		switch c {
		case '\'':
			inSingleQuote = true
		case '"':
			inDoubleQuote = true
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				warnings = append(warnings, fmt.Sprintf("CSS syntax error in %s: unexpected closing brace '}'", filename))
				depth = 0
			}
		}
	}
	if depth > 0 {
		warnings = append(warnings, fmt.Sprintf("CSS syntax error in %s: %d unclosed brace(s) '{'", filename, depth))
	}
	if inComment {
		warnings = append(warnings, fmt.Sprintf("CSS syntax error in %s: unclosed comment /* ... */", filename))
	}

	return warnings
}

// cssClassRe matches class selectors like .classname at the start of a rule, ignoring pseudo-selectors.
var cssClassRe = regexp.MustCompile(`(?m)^[^{}]*?\.([a-zA-Z0-9_-]+)(?:[:\s]|$)`)

// cssVarRe matches CSS custom property declarations like --color-primary: #fff.
var cssVarRe = regexp.MustCompile(`(--[\w-]+)\s*:\s*([^;}]+)`)

// extractCSSSummary extracts class names and CSS custom properties from CSS content.
// Returns a grouped summary so the LLM can quickly see the design vocabulary:
// COLORS, FONTS, SPACING (vars) and LAYOUT, COMPONENTS, UTILITIES (classes).
func extractCSSSummary(css string) string {
	seen := make(map[string]bool)

	// Categorise CSS custom properties.
	var colors, fonts, spacing, otherVars []string
	for _, m := range cssVarRe.FindAllStringSubmatch(css, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		entry := name + ":" + strings.TrimSpace(m[2])
		switch {
		case strings.HasPrefix(name, "--color"):
			colors = append(colors, entry)
		case strings.HasPrefix(name, "--font"), strings.HasPrefix(name, "--text"):
			fonts = append(fonts, entry)
		case strings.HasPrefix(name, "--spacing"), strings.HasPrefix(name, "--radius"),
			strings.HasPrefix(name, "--gap"):
			spacing = append(spacing, entry)
		default:
			otherVars = append(otherVars, entry)
		}
	}

	// Categorise class selectors.
	var layout, components, utilities, otherClasses []string
	for _, m := range cssClassRe.FindAllStringSubmatch(css, -1) {
		cls := m[1]
		key := "." + cls
		if seen[key] {
			continue
		}
		seen[key] = true
		switch {
		case strings.HasPrefix(cls, "container") || strings.HasPrefix(cls, "grid") ||
			strings.HasPrefix(cls, "flex") || strings.HasPrefix(cls, "col-") ||
			strings.HasPrefix(cls, "row") || strings.HasPrefix(cls, "wrap"):
			layout = append(layout, key)
		case strings.HasPrefix(cls, "card") || strings.HasPrefix(cls, "btn") ||
			strings.HasPrefix(cls, "hero") || strings.HasPrefix(cls, "section") ||
			strings.HasPrefix(cls, "form") || strings.HasPrefix(cls, "modal") ||
			strings.HasPrefix(cls, "badge") || strings.HasPrefix(cls, "alert") ||
			strings.HasPrefix(cls, "nav") || strings.HasPrefix(cls, "footer") ||
			strings.HasPrefix(cls, "header") || strings.HasPrefix(cls, "sidebar") ||
			strings.HasPrefix(cls, "tab") || strings.HasPrefix(cls, "dropdown") ||
			strings.HasPrefix(cls, "input") || strings.HasPrefix(cls, "label"):
			components = append(components, key)
		case strings.HasPrefix(cls, "text-") || strings.HasPrefix(cls, "hidden") ||
			strings.HasPrefix(cls, "sr-") || strings.HasPrefix(cls, "mt-") ||
			strings.HasPrefix(cls, "mb-") || strings.HasPrefix(cls, "ml-") ||
			strings.HasPrefix(cls, "mr-") || strings.HasPrefix(cls, "mx-") ||
			strings.HasPrefix(cls, "my-") || strings.HasPrefix(cls, "p-") ||
			strings.HasPrefix(cls, "pt-") || strings.HasPrefix(cls, "pb-") ||
			strings.HasPrefix(cls, "px-") || strings.HasPrefix(cls, "py-") ||
			strings.HasPrefix(cls, "gap-") || strings.HasPrefix(cls, "w-") ||
			strings.HasPrefix(cls, "h-") || strings.HasPrefix(cls, "d-") ||
			cls == "hidden" || cls == "visible":
			utilities = append(utilities, key)
		default:
			otherClasses = append(otherClasses, key)
		}
	}

	// Build grouped output with caps per group.
	cap := func(s []string, n int) []string {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	var parts []string
	addGroup := func(label string, items []string, max int, sep string) {
		items = cap(items, max)
		if len(items) > 0 {
			parts = append(parts, label+": "+strings.Join(items, sep))
		}
	}
	addGroup("COLORS", colors, 12, ", ")
	addGroup("FONTS", fonts, 4, ", ")
	addGroup("SPACING", spacing, 6, ", ")
	addGroup("VARS", otherVars, 8, ", ")
	addGroup("LAYOUT", layout, 10, " ")
	addGroup("COMPONENTS", components, 15, " ")
	addGroup("UTILITIES", utilities, 10, " ")
	addGroup("OTHER", otherClasses, 10, " ")

	summary := strings.Join(parts, " | ")
	if len(summary) > 1500 {
		summary = summary[:1500]
	}
	return summary
}

func (t *FilesTool) MaxResultSize() int { return 16000 }

func (t *FilesTool) Summarize(result string) string {
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
	if content, ok := data["content"].(string); ok && content != "" {
		filename, _ := data["filename"].(string)
		return fmt.Sprintf(`{"success":true,"summary":"Read file %s (%d chars)"}`, filename, len(content))
	}
	if warnings, ok := data["warnings"]; ok {
		filename, _ := data["filename"].(string)
		wJSON, _ := json.Marshal(warnings)
		return fmt.Sprintf(`{"success":true,"file":"%s","warnings":%s,"ACTION_REQUIRED":"Fix JS errors"}`, filename, wJSON)
	}
	// Preserve CSS class summary so the AI can reference classes when building pages.
	if cssSummary, ok := data["css_classes"].(string); ok && cssSummary != "" {
		filename, _ := data["filename"].(string)
		if len(cssSummary) > 1200 {
			cssSummary = cssSummary[:1200]
		}
		return fmt.Sprintf(`{"success":true,"file":"%s","css_classes":"%s"}`, filename, strings.ReplaceAll(cssSummary, `"`, `'`))
	}
	filename, _ := data["filename"].(string)
	size, _ := data["size"].(float64)
	return fmt.Sprintf(`{"success":true,"summary":"File %s (%d bytes)"}`, filename, int(size))
}

// executeSearch searches files by filename and content.
func (t *FilesTool) executeSearch(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required for search action")
	}
	storage, _ := args["storage"].(string)
	if storage == "" {
		storage = "assets"
	}
	table := "ho_assets"
	if storage == "files" {
		table = "ho_files"
	}

	likeQuery := "%" + query + "%"

	// ho_assets has scope and alt_text; ho_files has description. Neither has a content column.
	var sqlQuery string
	if storage == "files" {
		sqlQuery = fmt.Sprintf(
			`SELECT filename, description, size FROM %s WHERE filename LIKE ? OR description LIKE ? ORDER BY filename LIMIT 20`, table)
	} else {
		sqlQuery = fmt.Sprintf(
			`SELECT filename, scope, size FROM %s WHERE filename LIKE ? OR alt_text LIKE ? ORDER BY filename LIMIT 20`, table)
	}
	rows, err := ctx.DB.Query(sqlQuery, likeQuery, likeQuery)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var filename, extra string
		var size int
		if err := rows.Scan(&filename, &extra, &size); err != nil {
			continue
		}
		results = append(results, map[string]interface{}{
			"filename": filename,
			"scope":    extra,
			"size":     size,
		})
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"query":   query,
		"results": results,
		"count":   len(results),
	}}, nil
}
