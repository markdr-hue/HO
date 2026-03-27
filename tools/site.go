/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// validModes are the allowed site operating modes.
var validModes = map[string]bool{
	"building":   true,
	"monitoring": true,
	"paused":     true,
}

// ---------------------------------------------------------------------------
// manage_site
// ---------------------------------------------------------------------------

// SiteTool consolidates site info and mode management into a single tool.
type SiteTool struct{}

func (t *SiteTool) Name() string { return "manage_site" }
func (t *SiteTool) Description() string {
	return "Get site info, change operating mode, manage URL redirects, or enable PWA support."
}

func (t *SiteTool) Guide() string {
	return `### manage_site
- info: get site name, mode, domain, and config.
- set_mode: switch between building, monitoring, and paused.
- add_redirect/remove_redirect/list_redirects: manage URL redirects (301 permanent, 302 temporary).
- enable_pwa: generate manifest.json, service-worker.js, offline page, and register them in the default layout. Idempotent.`
}

func (t *SiteTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"info", "set_mode", "add_redirect", "remove_redirect", "list_redirects", "enable_pwa"},
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"description": "The mode to set (for set_mode)",
				"enum":        []string{"building", "monitoring", "paused"},
			},
			"source_path": map[string]interface{}{
				"type":        "string",
				"description": "Source URL path for redirect (e.g. /old-page). Must start with /.",
			},
			"target_path": map[string]interface{}{
				"type":        "string",
				"description": "Target URL path or full URL to redirect to (e.g. /new-page).",
			},
			"status_code": map[string]interface{}{
				"type":        "integer",
				"description": "HTTP status code: 301 (permanent, default) or 302 (temporary).",
			},
		},
		"required": []string{},
	}
}

func (t *SiteTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"info":            t.info,
		"set_mode":        t.setMode,
		"add_redirect":    t.addRedirect,
		"remove_redirect": t.removeRedirect,
		"list_redirects":  t.listRedirects,
		"enable_pwa":      t.enablePWA,
	}, nil)
}

func (t *SiteTool) info(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	var name, mode string
	var domain, description sql.NullString
	var createdAt, updatedAt time.Time

	err := ctx.GlobalDB.QueryRow(
		"SELECT name, domain, mode, description, created_at, updated_at FROM sites WHERE id = ?",
		ctx.SiteID,
	).Scan(&name, &domain, &mode, &description, &createdAt, &updatedAt)
	if err != nil {
		return &Result{Success: false, Error: "project not found"}, nil
	}

	result := map[string]interface{}{
		"name":       name,
		"mode":       mode,
		"created_at": createdAt,
		"updated_at": updatedAt,
	}
	if domain.Valid && domain.String != "" {
		result["domain"] = domain.String
	}
	if description.Valid && description.String != "" {
		result["description"] = description.String
	}

	return &Result{Success: true, Data: result}, nil
}

func (t *SiteTool) setMode(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	mode, _ := args["mode"].(string)
	if !validModes[mode] {
		return &Result{Success: false, Error: "mode must be one of: building, monitoring, paused"}, nil
	}

	res, err := ctx.GlobalDB.Exec(
		"UPDATE sites SET mode = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		mode, ctx.SiteID,
	)
	if err != nil {
		return nil, fmt.Errorf("setting site mode: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "project not found"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"site_id": ctx.SiteID,
		"mode":    mode,
	}}, nil
}

// ---------------------------------------------------------------------------
// Redirect management
// ---------------------------------------------------------------------------

func (t *SiteTool) addRedirect(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	source, _ := args["source_path"].(string)
	target, _ := args["target_path"].(string)
	if source == "" || target == "" {
		return &Result{Success: false, Error: "source_path and target_path are required"}, nil
	}
	if source[0] != '/' {
		return &Result{Success: false, Error: "source_path must start with /"}, nil
	}

	statusCode := 301
	if sc, ok := args["status_code"].(float64); ok {
		statusCode = int(sc)
	}
	if statusCode != 301 && statusCode != 302 {
		return &Result{Success: false, Error: "status_code must be 301 (permanent) or 302 (temporary)"}, nil
	}

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_redirects (source_path, target_path, status_code) VALUES (?, ?, ?)
		 ON CONFLICT(source_path) DO UPDATE SET target_path = excluded.target_path, status_code = excluded.status_code`,
		source, target, statusCode,
	)
	if err != nil {
		return nil, fmt.Errorf("adding redirect: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"source_path": source,
		"target_path": target,
		"status_code": statusCode,
	}}, nil
}

func (t *SiteTool) removeRedirect(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	source, _ := args["source_path"].(string)
	if source == "" {
		return &Result{Success: false, Error: "source_path is required"}, nil
	}

	res, err := ctx.DB.Exec("DELETE FROM ho_redirects WHERE source_path = ?", source)
	if err != nil {
		return nil, fmt.Errorf("removing redirect: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "redirect not found"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{"deleted": source}}, nil
}

func (t *SiteTool) listRedirects(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query("SELECT source_path, target_path, status_code, created_at FROM ho_redirects ORDER BY source_path")
	if err != nil {
		return nil, fmt.Errorf("listing redirects: %w", err)
	}
	defer rows.Close()

	var redirects []map[string]interface{}
	for rows.Next() {
		var source, target string
		var statusCode int
		var createdAt time.Time
		if err := rows.Scan(&source, &target, &statusCode, &createdAt); err != nil {
			continue
		}
		redirects = append(redirects, map[string]interface{}{
			"source_path": source,
			"target_path": target,
			"status_code": statusCode,
			"created_at":  createdAt,
		})
	}

	return &Result{Success: true, Data: redirects}, nil
}

// ---------------------------------------------------------------------------
// PWA support
// ---------------------------------------------------------------------------

func (t *SiteTool) enablePWA(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	// 1. Read site name from global DB.
	var siteName string
	err := ctx.GlobalDB.QueryRow(
		"SELECT name FROM sites WHERE id = ?", ctx.SiteID,
	).Scan(&siteName)
	if err != nil {
		return &Result{Success: false, Error: "could not read site name"}, nil
	}

	// 2. Read design system colors from pipeline state.
	themeColor := "#2563eb" // default blue
	bgColor := "#ffffff"    // default white

	var planJSON sql.NullString
	ctx.DB.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSON)
	if planJSON.Valid && planJSON.String != "" {
		var plan struct {
			DesignSystem struct {
				Colors struct {
					Primary string `json:"primary"`
					Bg      string `json:"bg"`
				} `json:"colors"`
			} `json:"design_system"`
		}
		if json.Unmarshal([]byte(planJSON.String), &plan) == nil {
			if plan.DesignSystem.Colors.Primary != "" {
				themeColor = plan.DesignSystem.Colors.Primary
			}
			if plan.DesignSystem.Colors.Bg != "" {
				bgColor = plan.DesignSystem.Colors.Bg
			}
		}
	}

	// 3. Generate manifest.json.
	manifest := map[string]interface{}{
		"name":             siteName,
		"short_name":       siteName,
		"start_url":        "/",
		"display":          "standalone",
		"theme_color":      themeColor,
		"background_color": bgColor,
		"icons":            []interface{}{},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling manifest: %w", err)
	}

	_, err = ctx.DB.Exec(
		`INSERT INTO ho_assets (filename, content, scope, content_type)
		 VALUES (?, ?, 'global', 'application/manifest+json')
		 ON CONFLICT(filename) DO UPDATE SET content = excluded.content`,
		"manifest.json", string(manifestBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("saving manifest.json: %w", err)
	}

	// 4. Generate service-worker.js.
	swJS := `const CACHE_NAME = 'ho-pwa-v1';
const OFFLINE_URL = '/offline';

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll([OFFLINE_URL]))
  );
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET') return;

  const url = new URL(event.request.url);

  // Cache-first for assets
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(
      caches.match(event.request).then((cached) => cached || fetch(event.request).then((response) => {
        const clone = response.clone();
        caches.open(CACHE_NAME).then((cache) => cache.put(event.request, clone));
        return response;
      }))
    );
    return;
  }

  // Network-first for everything else, fall back to offline page
  event.respondWith(
    fetch(event.request).catch(() => {
      if (event.request.mode === 'navigate') {
        return caches.match(OFFLINE_URL);
      }
    })
  );
});`

	_, err = ctx.DB.Exec(
		`INSERT INTO ho_assets (filename, content, scope, content_type)
		 VALUES (?, ?, 'global', 'application/javascript')
		 ON CONFLICT(filename) DO UPDATE SET content = excluded.content`,
		"service-worker.js", swJS,
	)
	if err != nil {
		return nil, fmt.Errorf("saving service-worker.js: %w", err)
	}

	// 5. Generate offline page.
	offlineHTML := `<section style="display:flex;align-items:center;justify-content:center;min-height:80vh;text-align:center;padding:2rem;">
  <div>
    <h1>You're offline</h1>
    <p>Please check your internet connection and try again.</p>
  </div>
</section>`

	_, err = ctx.DB.Exec(
		`INSERT INTO ho_pages (path, title, content)
		 VALUES (?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET content = excluded.content`,
		"/offline", "Offline", offlineHTML,
	)
	if err != nil {
		return nil, fmt.Errorf("saving offline page: %w", err)
	}

	// 6. Patch default layout head_content with manifest link + SW registration.
	var headContent sql.NullString
	ctx.DB.QueryRow("SELECT head_content FROM ho_layouts WHERE name = 'default'").Scan(&headContent)

	pwaSnippet := "\n<link rel=\"manifest\" href=\"/manifest.json\">\n<script>if('serviceWorker' in navigator){navigator.serviceWorker.register('/service-worker.js')}</script>"

	if !strings.Contains(headContent.String, "/manifest.json") {
		_, err = ctx.DB.Exec(
			"UPDATE ho_layouts SET head_content = COALESCE(head_content, '') || ? WHERE name = 'default'",
			pwaSnippet,
		)
		if err != nil {
			return nil, fmt.Errorf("patching layout head_content: %w", err)
		}
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"files":   []string{"manifest.json", "service-worker.js", "/offline"},
		"message": "PWA support enabled: manifest, service worker, and offline page created; default layout updated.",
	}}, nil
}
