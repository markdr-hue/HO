/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package public

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/internal/sqliteretry"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/llm/anthropic"
	"github.com/markdr-hue/HO/llm/openai"
	"github.com/markdr-hue/HO/security"
	"github.com/markdr-hue/HO/server/middleware"
)

type contextKey string

const siteContextKey contextKey = "resolved_site"

// Handler provides the main public request handlers.
type Handler struct {
	deps *Deps
}

// SiteResolver is middleware that resolves the site from the Host header
// and injects it into the request context.
func (h *Handler) SiteResolver(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// Strip port if present.
		if idx := strings.LastIndex(host, ":"); idx != -1 {
			host = host[:idx]
		}

		site, err := models.GetSiteByDomain(h.deps.DB.DB, host)
		if err != nil {
			// If no site matches the domain, try to serve the first active site
			// as a fallback for single-site setups.
			sites, listErr := models.ListActiveSites(h.deps.DB.DB)
			if listErr != nil || len(sites) == 0 {
				if wantsHTML(r) {
					serveErrorPage(w, http.StatusNotFound, "Site Not Found", "There is no site configured for this domain.")
				} else {
					writePublicError(w, http.StatusNotFound, "site not found")
				}
				return
			}
			site = &sites[0]
		}

		// Block inactive sites — serve a branded page for browsers, JSON for API clients.
		if site.Status != "active" {
			if wantsHTML(r) {
				serveErrorPage(w, http.StatusNotFound, "Site Unavailable", "This site is currently unavailable. Please check back later.")
			} else {
				writePublicError(w, http.StatusNotFound, "site not found")
			}
			return
		}

		ctx := context.WithValue(r.Context(), siteContextKey, site)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// getSite extracts the resolved site from the context.
func getSite(r *http.Request) *models.Site {
	site, _ := r.Context().Value(siteContextKey).(*models.Site)
	return site
}

// ---------------------------------------------------------------------------
// Page JSON API (used by SPA router for client-side navigation)
// ---------------------------------------------------------------------------

// Page serves page content as JSON for the given path query parameter.
// GET /api/page?path=/about
func (h *Handler) Page(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		writePublicError(w, http.StatusNotFound, "site not found")
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "database error")
		return
	}

	pagePath := r.URL.Query().Get("path")
	if pagePath == "" {
		pagePath = "/"
	}

	// Check redirects for SPA navigation.
	if target, code, found := checkRedirect(siteDB.Reader(), pagePath); found {
		writePublicJSON(w, http.StatusOK, map[string]interface{}{
			"redirect":    target,
			"status_code": code,
		})
		return
	}

	var content, title, metadata, layoutName, pageAssets sql.NullString
	var routeParams map[string]string
	err = siteDB.QueryRow(
		"SELECT title, content, metadata, layout, assets FROM ho_pages WHERE path = ? AND status = 'published' AND is_deleted = 0",
		pagePath,
	).Scan(&title, &content, &metadata, &layoutName, &pageAssets)
	if err != nil {
		// Try parameterized route match: /thread/4 → /thread/:id
		templatePath, params, paramErr := findParamPage(siteDB.Reader(), pagePath)
		if paramErr != nil {
			writePublicError(w, http.StatusNotFound, "page not found")
			return
		}
		err = siteDB.QueryRow(
			"SELECT title, content, metadata, layout, assets FROM ho_pages WHERE path = ? AND status = 'published' AND is_deleted = 0",
			templatePath,
		).Scan(&title, &content, &metadata, &layoutName, &pageAssets)
		if err != nil {
			writePublicError(w, http.StatusNotFound, "page not found")
			return
		}
		routeParams = params
	}

	// Strip shared asset refs the brain may have embedded, then strip external
	// scripts and CSS links for SPA — extracting their URLs so the router
	// can load them dynamically.
	cleaned := stripSharedAssetRefs(content.String, siteDB.Reader())
	cleanContent, contentCSS, contentJS := stripForSPA(cleaned)

	// Normalize layout name for the SPA router (NULL/empty → "default").
	respLayout := layoutName.String
	if respLayout == "" {
		respLayout = "default"
	}

	resp := map[string]interface{}{
		"path":     pagePath,
		"title":    title.String,
		"content":  strings.TrimSpace(cleanContent),
		"metadata": metadata.String,
		"site_id":  site.ID,
		"layout":   respLayout,
	}
	if routeParams != nil {
		resp["params"] = routeParams
	}

	// Merge DB-declared page assets with URLs extracted from content.
	if pageAssets.String != "" && pageAssets.String != "null" {
		dbCSS, dbJS := parsePageAssetURLs(pageAssets.String)
		contentCSS = append(dbCSS, contentCSS...)
		contentJS = append(dbJS, contentJS...)
	}
	// Append version hash to asset URLs for cache-busting.
	versionHash := getAssetVersionHash(siteDB.Reader())
	if versionHash != "" {
		vSuffix := "?v=" + versionHash
		for i, url := range contentCSS {
			if strings.HasPrefix(url, "/assets/") && !strings.Contains(url, "?") {
				contentCSS[i] = url + vSuffix
			}
		}
		for i, url := range contentJS {
			if strings.HasPrefix(url, "/assets/") && !strings.Contains(url, "?") {
				contentJS[i] = url + vSuffix
			}
		}
	}
	if len(contentCSS) > 0 {
		resp["page_css"] = dedupStrings(contentCSS)
	}
	if len(contentJS) > 0 {
		resp["page_js"] = dedupStrings(contentJS)
	}
	w.Header().Set("Cache-Control", "no-store")
	writePublicJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// SSR — Server-Side Rendered Pages
// ---------------------------------------------------------------------------

type pageMetadata struct {
	Description string `json:"description"`
	OGImage     string `json:"og_image"`
	Canonical   string `json:"canonical"`
	Keywords    string `json:"keywords"`
}

// ServePage serves a fully-rendered HTML page by its URL path.
func (h *Handler) ServePage(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		writePublicError(w, http.StatusNotFound, "site not found")
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "database error")
		return
	}

	pagePath := r.URL.Path
	if pagePath == "" {
		pagePath = "/"
	}

	// Check redirects before page lookup.
	if target, code, found := checkRedirect(siteDB.Reader(), pagePath); found {
		http.Redirect(w, r, target, code)
		return
	}

	var content, title, metadata, layoutName, pageAssets sql.NullString
	err = siteDB.QueryRow(
		"SELECT title, content, metadata, layout, assets FROM ho_pages WHERE path = ? AND status = 'published' AND is_deleted = 0",
		pagePath,
	).Scan(&title, &content, &metadata, &layoutName, &pageAssets)
	var routeParams map[string]string
	if err != nil {
		// Try common homepage variants: the brain often saves as /index.html instead of /
		found := false
		for _, alt := range homeFallbacks(pagePath) {
			if siteDB.QueryRow(
				"SELECT title, content, metadata, layout, assets FROM ho_pages WHERE path = ? AND status = 'published' AND is_deleted = 0",
				alt,
			).Scan(&title, &content, &metadata, &layoutName, &pageAssets) == nil {
				found = true
				pagePath = alt
				break
			}
		}
		// Try parameterized route match: /thread/4 → /thread/:id
		if !found {
			if templatePath, params, paramErr := findParamPage(siteDB.Reader(), pagePath); paramErr == nil {
				if siteDB.QueryRow(
					"SELECT title, content, metadata, layout, assets FROM ho_pages WHERE path = ? AND status = 'published' AND is_deleted = 0",
					templatePath,
				).Scan(&title, &content, &metadata, &layoutName, &pageAssets) == nil {
					found = true
					routeParams = params
				}
			}
		}
		if !found {
			h.serve404(w, site)
			return
		}
	}

	// Track analytics (fire-and-forget).
	go func() {
		defer func() {
			if rv := recover(); rv != nil {
				h.deps.Logger.Error("trackPageView panic", "error", rv, "site_id", site.ID)
			}
		}()
		h.trackPageView(site.ID, pagePath, r)
	}()

	// Render the full HTML document with layout wrapping.
	doc := h.renderDocument(site, siteDB.Reader(), pagePath, title.String, content.String, metadata.String, layoutName.String, pageAssets.String, routeParams)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(doc))
}

func (h *Handler) serve404(w http.ResponseWriter, site *models.Site) {
	// Check for a custom 404 page.
	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err == nil {
		var content, title, metadata, layoutName, pageAssets sql.NullString
		err = siteDB.QueryRow(
			"SELECT title, content, metadata, layout, assets FROM ho_pages WHERE path = '/404' AND status = 'published' AND is_deleted = 0",
		).Scan(&title, &content, &metadata, &layoutName, &pageAssets)
		if err == nil {
			doc := h.renderDocument(site, siteDB.Reader(), "/404", title.String, content.String, metadata.String, layoutName.String, pageAssets.String, nil)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(doc))
			return
		}
	}

	serveErrorPage(w, http.StatusNotFound, "Page Not Found", "The page you're looking for doesn't exist or has been moved.")
}

// layoutData holds a loaded layout from the layouts table.
type layoutData struct {
	HeadContent string
	Template    string
}

// loadLayout loads a layout by name from the site database. Returns nil if not found.
func loadLayout(siteDB *sql.DB, name string) *layoutData {
	var ld layoutData
	var tmpl sql.NullString
	err := siteDB.QueryRow(
		"SELECT head_content, template FROM ho_layouts WHERE name = ?",
		name,
	).Scan(&ld.HeadContent, &tmpl)
	if err != nil {
		return nil
	}
	if tmpl.Valid {
		ld.Template = tmpl.String
	}
	return &ld
}

// getAssetVersionHash returns a short content-based hash for cache-busting.
// It uses the most recent created_at timestamp across all assets so URLs
// change whenever any asset is updated, forcing browsers to re-fetch.
func getAssetVersionHash(siteDB *sql.DB) string {
	var maxCreatedAt sql.NullString
	siteDB.QueryRow("SELECT MAX(created_at) FROM ho_assets").Scan(&maxCreatedAt)
	if !maxCreatedAt.Valid || maxCreatedAt.String == "" {
		return ""
	}
	h := sha256.Sum256([]byte(maxCreatedAt.String))
	return fmt.Sprintf("%x", h[:4]) // 8 hex chars
}

// autoInjectAssets queries global-scoped CSS/JS assets and returns link/script tags
// with versioned URLs for cache-busting.
func autoInjectAssets(siteDB *sql.DB) (cssLinks, jsScripts string) {
	versionHash := getAssetVersionHash(siteDB)
	vSuffix := ""
	if versionHash != "" {
		vSuffix = "?v=" + versionHash
	}

	rows, err := siteDB.Query(
		"SELECT filename FROM ho_assets WHERE scope = 'global' AND (filename LIKE '%.css' OR filename LIKE '%.js') ORDER BY filename",
	)
	if err != nil {
		return "", ""
	}
	defer rows.Close()

	var css, js strings.Builder
	for rows.Next() {
		var fn string
		if rows.Scan(&fn) != nil {
			continue
		}
		if strings.HasSuffix(strings.ToLower(fn), ".css") {
			css.WriteString(`  <link rel="stylesheet" href="/assets/` + fn + vSuffix + `">` + "\n")
		} else if strings.HasSuffix(strings.ToLower(fn), ".js") {
			js.WriteString(`  <script src="/assets/` + fn + vSuffix + `"></script>` + "\n")
		}
	}
	return css.String(), js.String()
}

// injectPageAssets generates link/script tags for page-scoped assets with versioned URLs.
func injectPageAssets(pageAssetsJSON, versionHash string) (cssLinks, jsScripts string) {
	if pageAssetsJSON == "" || pageAssetsJSON == "null" {
		return "", ""
	}
	var filenames []string
	if err := json.Unmarshal([]byte(pageAssetsJSON), &filenames); err != nil {
		return "", ""
	}
	vSuffix := ""
	if versionHash != "" {
		vSuffix = "?v=" + versionHash
	}
	var css, js strings.Builder
	for _, fn := range filenames {
		fn = strings.TrimSpace(fn)
		if fn == "" {
			continue
		}
		lower := strings.ToLower(fn)
		if strings.HasSuffix(lower, ".css") {
			css.WriteString(`  <link rel="stylesheet" href="/assets/` + fn + vSuffix + `">` + "\n")
		} else if strings.HasSuffix(lower, ".js") {
			js.WriteString(`  <script src="/assets/` + fn + vSuffix + `"></script>` + "\n")
		}
	}
	return css.String(), js.String()
}

func (h *Handler) renderDocument(site *models.Site, siteDB *sql.DB, pagePath, title, content, metadataJSON, layoutName, pageAssetsJSON string, routeParams map[string]string) string {
	// Parse page metadata.
	var meta pageMetadata
	if metadataJSON != "" && metadataJSON != "{}" {
		json.Unmarshal([]byte(metadataJSON), &meta)
	}

	// Text direction attribute (always LTR for now).
	dir := "ltr"

	// Build the page title.
	pageTitle := html.EscapeString(site.Name)
	if title != "" {
		pageTitle = html.EscapeString(title) + " | " + html.EscapeString(site.Name)
	}

	// Build canonical URL.
	canonicalURL := meta.Canonical
	if canonicalURL == "" && site.Domain != nil && *site.Domain != "" {
		canonicalURL = "https://" + *site.Domain + pagePath
	}

	// Description.
	description := meta.Description
	if description == "" && site.Description != nil {
		description = *site.Description
	}

	// Load layout (NULL or empty → "default", "none" → no layout wrapping).
	// Even "none" pages still get the default head_content (fonts, meta, CDN scripts).
	var layout *layoutData
	noLayoutWrap := layoutName == "none"
	if layoutName == "" || layoutName == "none" {
		layoutName = "default"
	}
	layout = loadLayout(siteDB, layoutName)

	// Auto-inject global-scoped CSS/JS assets from the assets table.
	cssLinks, jsScripts := autoInjectAssets(siteDB)

	// Page-scoped assets (only for this page) — with version hash for cache-busting.
	versionHash := getAssetVersionHash(siteDB)
	pageCSSLinks, pageJSScripts := injectPageAssets(pageAssetsJSON, versionHash)

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString(`<html lang="en" dir="` + html.EscapeString(dir) + `">` + "\n")
	b.WriteString("<head>\n")
	b.WriteString(`  <meta charset="utf-8">` + "\n")
	b.WriteString(`  <meta name="viewport" content="width=device-width, initial-scale=1">` + "\n")
	b.WriteString("  <title>" + pageTitle + "</title>\n")

	if description != "" {
		b.WriteString(`  <meta name="description" content="` + html.EscapeString(description) + `">` + "\n")
	}
	if meta.Keywords != "" {
		b.WriteString(`  <meta name="keywords" content="` + html.EscapeString(meta.Keywords) + `">` + "\n")
	}

	// Open Graph tags.
	b.WriteString(`  <meta property="og:title" content="` + html.EscapeString(title) + `">` + "\n")
	if description != "" {
		b.WriteString(`  <meta property="og:description" content="` + html.EscapeString(description) + `">` + "\n")
	}
	if meta.OGImage != "" {
		b.WriteString(`  <meta property="og:image" content="` + html.EscapeString(meta.OGImage) + `">` + "\n")
	}
	if canonicalURL != "" {
		b.WriteString(`  <meta property="og:url" content="` + html.EscapeString(canonicalURL) + `">` + "\n")
		b.WriteString(`  <link rel="canonical" href="` + html.EscapeString(canonicalURL) + `">` + "\n")
	}

	// Base structural CSS — guarantees correct page skeleton regardless of LLM output.
	b.WriteString("  <style>body{display:flex;flex-direction:column;min-height:100vh;margin:0}main{flex:1}</style>\n")

	// Auto-injected global CSS assets.
	if cssLinks != "" {
		b.WriteString(cssLinks)
	}
	// Page-scoped CSS assets.
	if pageCSSLinks != "" {
		b.WriteString(pageCSSLinks)
	}

	// Layout head_content (extra fonts, meta, etc.).
	if layout != nil && layout.HeadContent != "" {
		b.WriteString("  ")
		b.WriteString(layout.HeadContent)
		b.WriteString("\n")
	}

	// Strip any document-level tags the LLM may have included (DOCTYPE, html, head, body).
	content = stripDocumentShell(content)

	// Strip shared asset references that the LLM accidentally included in page content.
	// This prevents duplicate CSS/JS since assets are auto-injected above.
	content = stripSharedAssetRefs(content, siteDB)

	// Extract page-specific CSS and JS tags from page content:
	// page <style> blocks → <head>, inline <script> blocks → end of <body>.
	headTags, bodyEndTags, cleanContent := extractAssetTags(content)
	if headTags != "" {
		b.WriteString(headTags)
		b.WriteString("\n")
	}

	b.WriteString("</head>\n")

	b.WriteString("<body>\n")

	if noLayoutWrap || layout == nil {
		// No layout wrapping (layout="none" or layout not found) — render content as-is.
		b.WriteString(cleanContent)
		b.WriteString("\n")
	} else if layout.Template != "" && strings.Contains(layout.Template, "{{content}}") {
		// Template mode: replace {{content}} with page HTML.
		rendered := strings.Replace(layout.Template, "{{content}}", cleanContent, 1)
		b.WriteString(rendered)
		b.WriteString("\n")
	} else {
		// No template or missing {{content}} — wrap in <main> as fallback.
		b.WriteString("<main>\n")
		b.WriteString(cleanContent)
		b.WriteString("\n</main>\n")
	}

	// Inject route params for parameterized pages (e.g. /thread/:id → {id: "4"}).
	if len(routeParams) > 0 {
		paramsJSON, _ := json.Marshal(routeParams)
		b.WriteString(fmt.Sprintf("<script>window.__routeParams=%s;</script>\n", paramsJSON))
	} else {
		b.WriteString("<script>window.__routeParams={};</script>\n")
	}

	// Auto-injected global JS assets (after footer/layout).
	if jsScripts != "" {
		b.WriteString(jsScripts)
	}
	// Page-scoped JS assets.
	if pageJSScripts != "" {
		b.WriteString(pageJSScripts)
	}

	// Page-specific inline scripts.
	if bodyEndTags != "" {
		b.WriteString(bodyEndTags)
		b.WriteString("\n")
	}
	b.WriteString("</body>\n</html>")

	return b.String()
}

// stripSharedAssetRefs removes <link> and <script src> tags referencing /assets/
// from page content. These are auto-injected by the server, so duplicates in
// page content would cause double-loading.
func stripSharedAssetRefs(content string, siteDB *sql.DB) string {
	// Collect known asset filenames for matching.
	assetNames := map[string]bool{}
	rows, err := siteDB.Query("SELECT filename FROM ho_assets WHERE (filename LIKE '%.css' OR filename LIKE '%.js')")
	if err != nil {
		return content
	}
	defer rows.Close()
	for rows.Next() {
		var fn string
		if rows.Scan(&fn) == nil {
			assetNames[strings.ToLower(fn)] = true
		}
	}
	if len(assetNames) == 0 {
		return content
	}

	// Remove <link rel="stylesheet" href="/assets/..."> for known assets.
	content = cssLinkRe.ReplaceAllStringFunc(content, func(match string) string {
		lower := strings.ToLower(match)
		for name := range assetNames {
			if strings.Contains(lower, "/assets/"+name) {
				return ""
			}
		}
		return match
	})

	// Remove <script src="/assets/...">...</script> for known assets.
	// Only check the opening <script> tag for src=, not the script body.
	content = scriptBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		if !isExternalScript(match) {
			return match // inline script — keep it
		}
		lower := strings.ToLower(match)
		for name := range assetNames {
			if strings.Contains(lower, "/assets/"+name) {
				return ""
			}
		}
		return match
	})

	// Remove self-closing <script src="/assets/..." /> for known assets.
	content = scriptSelfCloseRe.ReplaceAllStringFunc(content, func(match string) string {
		lower := strings.ToLower(match)
		for name := range assetNames {
			if strings.Contains(lower, "/assets/"+name) {
				return ""
			}
		}
		return match
	})

	return content
}

// ---------------------------------------------------------------------------
// Asset tag extraction — hoists CSS to <head> and JS to end of <body>.
// ---------------------------------------------------------------------------

// Regex patterns for extracting asset tags from page content.
var (
	// Matches <link ... rel="stylesheet" ... > or <link ... rel='stylesheet' ... >
	cssLinkRe = regexp.MustCompile(`(?i)<link[^>]*rel=["']stylesheet["'][^>]*/?>`)
	// Matches <style> ... </style> blocks (including attributes)
	styleBlockRe = regexp.MustCompile(`(?isU)<style[\s>].*</style>`)
	// Matches <script ...> ... </script> blocks (inline or src-based)
	scriptBlockRe = regexp.MustCompile(`(?isU)<script[\s>].*</script>`)
	// Matches self-closing <script ... /> (rare but valid)
	scriptSelfCloseRe = regexp.MustCompile(`(?i)<script[^>]*/\s*>`)
	// Matches the opening <script ...> tag (up to the first '>').
	scriptOpenTagRe = regexp.MustCompile(`(?i)<script[^>]*>`)
	// Matches a src attribute inside a tag.
	scriptSrcRe = regexp.MustCompile(`(?i)\bsrc\s*=`)
	// Attribute extractors for SPA asset URL extraction.
	hrefAttrRe = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)
	srcAttrRe  = regexp.MustCompile(`(?i)src\s*=\s*["']([^"']+)["']`)
)

// extractHref returns the href attribute value from an HTML tag, or "".
func extractHref(tag string) string {
	m := hrefAttrRe.FindStringSubmatch(tag)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// extractSrc returns the src attribute value from an HTML tag, or "".
func extractSrc(tag string) string {
	m := srcAttrRe.FindStringSubmatch(tag)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// isExternalScript checks whether a <script>...</script> block has a src
// attribute on the opening tag (making it an external script). This avoids
// false positives from 'src=' appearing inside JavaScript code — e.g.
// template literals like `<img src="${imgSrc}">`.
func isExternalScript(block string) bool {
	openTag := scriptOpenTagRe.FindString(block)
	if openTag == "" {
		return false
	}
	return scriptSrcRe.MatchString(openTag)
}

// extractScriptSrc extracts the src URL from a <script> block's opening tag only.
func extractScriptSrc(block string) string {
	openTag := scriptOpenTagRe.FindString(block)
	if openTag == "" {
		return ""
	}
	return extractSrc(openTag)
}

// dedupStrings returns a slice with duplicate strings removed, preserving order.
func dedupStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// extractAssetTags separates CSS and JS tags from page content so they can be
// placed in the correct locations: CSS in <head>, JS at end of <body>.
func extractAssetTags(content string) (headTags, bodyEndTags, cleanContent string) {
	var head []string
	var bodyEnd []string

	// Extract <link rel="stylesheet"> tags → head
	content = cssLinkRe.ReplaceAllStringFunc(content, func(match string) string {
		head = append(head, match)
		return ""
	})

	// Extract <style> blocks → head
	content = styleBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		head = append(head, match)
		return ""
	})

	// Extract <script> blocks → body end
	content = scriptBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		bodyEnd = append(bodyEnd, match)
		return ""
	})

	// Extract self-closing <script /> → body end
	content = scriptSelfCloseRe.ReplaceAllStringFunc(content, func(match string) string {
		bodyEnd = append(bodyEnd, match)
		return ""
	})

	headTags = strings.Join(head, "\n")
	bodyEndTags = strings.Join(bodyEnd, "\n")
	cleanContent = content
	return
}

// parsePageAssetURLs splits a JSON array of filenames into CSS and JS URL arrays.
func parsePageAssetURLs(assetsJSON string) (cssURLs, jsURLs []string) {
	var filenames []string
	if err := json.Unmarshal([]byte(assetsJSON), &filenames); err != nil {
		return nil, nil
	}
	for _, fn := range filenames {
		fn = strings.TrimSpace(fn)
		if fn == "" {
			continue
		}
		lower := strings.ToLower(fn)
		if strings.HasSuffix(lower, ".css") {
			cssURLs = append(cssURLs, "/assets/"+fn)
		} else if strings.HasSuffix(lower, ".js") {
			jsURLs = append(jsURLs, "/assets/"+fn)
		}
	}
	return
}

// stripForSPA prepares page content for the SPA JSON endpoint (/api/page).
// Strips CSS links and external scripts (src=...) but keeps <style> blocks
// and inline scripts. Returns the extracted CSS and JS URLs so the SPA
// router can load them dynamically.
func stripForSPA(content string) (string, []string, []string) {
	var cssURLs, jsURLs []string

	// Extract CSS link hrefs, then remove the tags.
	for _, match := range cssLinkRe.FindAllString(content, -1) {
		if href := extractHref(match); href != "" {
			cssURLs = append(cssURLs, href)
		}
	}
	content = cssLinkRe.ReplaceAllString(content, "")
	// Keep <style> blocks — they work in innerHTML and may contain page-specific styles.

	// Extract external script srcs, then remove the tags.
	// Keep inline <script>...</script> for page-specific logic.
	// IMPORTANT: Only check the opening <script> tag for src=, NOT the
	// script body — JavaScript code often contains 'src=' in template
	// literals, DOM manipulation, etc.
	content = scriptBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		if isExternalScript(match) {
			if src := extractScriptSrc(match); src != "" {
				jsURLs = append(jsURLs, src)
			}
			return "" // external script — strip it
		}
		return match // inline script — keep it
	})

	// Remove self-closing <script src="..." /> tags, extract src.
	content = scriptSelfCloseRe.ReplaceAllStringFunc(content, func(match string) string {
		if src := extractSrc(match); src != "" {
			jsURLs = append(jsURLs, src)
		}
		return ""
	})

	return strings.TrimSpace(content), cssURLs, jsURLs
}

// ---------------------------------------------------------------------------
// Document shell stripping — removes structural HTML tags from LLM content.
// ---------------------------------------------------------------------------

var (
	doctypeRe   = regexp.MustCompile(`(?i)<!DOCTYPE[^>]*>`)
	htmlOpenRe  = regexp.MustCompile(`(?i)<html[^>]*>`)
	htmlCloseRe = regexp.MustCompile(`(?i)</html\s*>`)
	headBlockRe = regexp.MustCompile(`(?isU)<head[\s>].*</head>`)
	bodyOpenRe  = regexp.MustCompile(`(?i)<body[^>]*>`)
	bodyCloseRe = regexp.MustCompile(`(?i)</body\s*>`)
)

// stripDocumentShell removes structural HTML tags (DOCTYPE, html, head, body)
// from page content. The system wraps content in its own document shell via
// renderDocument, so these tags must not be present in stored content.
// CSS/JS inside <head> are rescued so extractAssetTags can hoist them later.
func stripDocumentShell(content string) string {
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "<!doctype") && !strings.Contains(lower, "<html") {
		return content
	}

	// Rescue <style>, <link rel="stylesheet">, and <script> from inside <head>
	// before we remove the entire <head> block.
	var rescued []string
	for _, headMatch := range headBlockRe.FindAllString(content, -1) {
		for _, m := range cssLinkRe.FindAllString(headMatch, -1) {
			rescued = append(rescued, m)
		}
		for _, m := range styleBlockRe.FindAllString(headMatch, -1) {
			rescued = append(rescued, m)
		}
		for _, m := range scriptBlockRe.FindAllString(headMatch, -1) {
			rescued = append(rescued, m)
		}
	}

	content = doctypeRe.ReplaceAllString(content, "")
	content = headBlockRe.ReplaceAllString(content, "")
	content = htmlOpenRe.ReplaceAllString(content, "")
	content = htmlCloseRe.ReplaceAllString(content, "")
	content = bodyOpenRe.ReplaceAllString(content, "")
	content = bodyCloseRe.ReplaceAllString(content, "")

	if len(rescued) > 0 {
		content = strings.Join(rescued, "\n") + "\n" + content
	}

	return strings.TrimSpace(content)
}

// homeFallbacks returns alternate paths to try when an exact page path isn't found.
// This handles the common case where the brain saves the homepage as "/index.html"
// but the browser requests "/", or vice versa.
// checkRedirect looks up a redirect for the given path.
func checkRedirect(siteDB *sql.DB, path string) (target string, statusCode int, found bool) {
	err := siteDB.QueryRow(
		"SELECT target_path, status_code FROM ho_redirects WHERE source_path = ?",
		path,
	).Scan(&target, &statusCode)
	if err != nil {
		return "", 0, false
	}
	return target, statusCode, true
}

func homeFallbacks(path string) []string {
	switch path {
	case "/":
		return []string{"/index.html", "/index", "/home"}
	case "/index.html", "/index":
		return []string{"/"}
	default:
		// No fallback for non-root paths. Each route must be a real page.
		// SPA client-side routing handles transitions; the server serves
		// each route individually for SSR/SEO.
		return nil
	}
}

// findParamPage matches a request path against parameterized page templates.
// e.g. "/thread/4" matches "/thread/:id" → params {"id": "4"}.
func findParamPage(siteDB *sql.DB, requestPath string) (templatePath string, params map[string]string, err error) {
	rows, err := siteDB.Query(
		"SELECT path FROM ho_pages WHERE path LIKE '%:%' AND status = 'published' AND is_deleted = 0",
	)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()

	reqParts := strings.Split(requestPath, "/")

	for rows.Next() {
		var candidate string
		rows.Scan(&candidate)

		candParts := strings.Split(candidate, "/")
		if len(candParts) != len(reqParts) {
			continue
		}

		match := true
		extracted := map[string]string{}
		for i, cp := range candParts {
			if strings.HasPrefix(cp, ":") {
				extracted[cp[1:]] = reqParts[i]
			} else if cp != reqParts[i] {
				match = false
				break
			}
		}
		if match {
			return candidate, extracted, nil
		}
	}
	return "", nil, fmt.Errorf("no matching parameterized page")
}

func (h *Handler) trackPageView(siteID int, pagePath string, r *http.Request) {
	siteDB, err := h.deps.SiteDBManager.Open(siteID)
	if err != nil {
		return
	}

	ip := middleware.ClientIP(r)
	visitorHash := fmt.Sprintf("%x", sha256.Sum256([]byte(ip+r.UserAgent())))[:16]
	referrer := r.Referer()
	ua := r.UserAgent()

	siteDB.ExecWrite(
		"INSERT INTO ho_analytics (page_path, visitor_hash, referrer, user_agent) VALUES (?, ?, ?, ?)",
		pagePath, visitorHash, referrer, ua,
	)
}

// ---------------------------------------------------------------------------
// Sitemap
// ---------------------------------------------------------------------------

func (h *Handler) Sitemap(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		writePublicError(w, http.StatusNotFound, "site not found")
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	baseURL := "https://" + r.Host
	if site.Domain != nil && *site.Domain != "" {
		baseURL = "https://" + *site.Domain
	}

	rows, err := siteDB.Query(
		"SELECT path, updated_at FROM ho_pages WHERE status = 'published' AND is_deleted = 0 ORDER BY path",
	)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")

	for rows.Next() {
		var path string
		var updatedAt time.Time
		if err := rows.Scan(&path, &updatedAt); err != nil {
			continue
		}
		b.WriteString("  <url>\n")
		b.WriteString("    <loc>" + html.EscapeString(baseURL+path) + "</loc>\n")
		b.WriteString("    <lastmod>" + updatedAt.Format("2006-01-02") + "</lastmod>\n")
		b.WriteString("  </url>\n")
	}

	b.WriteString("</urlset>\n")

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.String()))
}

// ---------------------------------------------------------------------------
// Robots.txt
// ---------------------------------------------------------------------------

func (h *Handler) Robots(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	baseURL := "https://" + r.Host
	if site != nil && site.Domain != nil && *site.Domain != "" {
		baseURL = "https://" + *site.Domain
	}

	body := "User-agent: *\nAllow: /\nSitemap: " + baseURL + "/sitemap.xml\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(body))
}

// ---------------------------------------------------------------------------
// PWA Assets (manifest + service worker served from DB content)
// ---------------------------------------------------------------------------

func (h *Handler) ServeManifest(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		http.NotFound(w, r)
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var content sql.NullString
	err = siteDB.QueryRow(
		"SELECT content FROM ho_assets WHERE filename = 'manifest.json'",
	).Scan(&content)
	if err != nil || !content.Valid {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(content.String))
}

func (h *Handler) ServeServiceWorker(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		http.NotFound(w, r)
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var content sql.NullString
	err = siteDB.QueryRow(
		"SELECT content FROM ho_assets WHERE filename = 'service-worker.js'",
	).Scan(&content)
	if err != nil || !content.Valid {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Service-Worker-Allowed", "/")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(content.String))
}

// ---------------------------------------------------------------------------
// Asset Serving
// ---------------------------------------------------------------------------

func (h *Handler) ServeAsset(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		http.NotFound(w, r)
		return
	}

	// Extract filename from path: /assets/{filename}
	filename := strings.TrimPrefix(r.URL.Path, "/assets/")
	if filename == "" {
		http.NotFound(w, r)
		return
	}
	cleaned := filepath.ToSlash(filepath.Clean(filename))
	if cleaned != filename || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		http.NotFound(w, r)
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Look up the asset in the database.
	var storagePath, contentType sql.NullString
	err = siteDB.QueryRow(
		"SELECT storage_path, content_type FROM ho_assets WHERE filename = ?",
		filename,
	).Scan(&storagePath, &contentType)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Serve the file from disk.
	expectedPrefix := filepath.Join("data", "sites", fmt.Sprintf("%d", site.ID))
	fpath := storagePath.String
	if fpath == "" {
		fpath = filepath.Join(expectedPrefix, "assets", filename)
	}
	if !strings.HasPrefix(filepath.Clean(fpath), expectedPrefix) {
		http.NotFound(w, r)
		return
	}

	data, err := os.ReadFile(fpath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Prefer extension-based MIME type (LLMs sometimes store wrong content_type).
	ct := mimeByExtension(filename)
	if ct == "" {
		ct = contentType.String
	}
	if ct == "" {
		ct = "application/octet-stream"
	}

	// ETag retained for correctness, but versioned URLs (?v=hash) are the
	// primary cache-busting mechanism. Immutable caching means browsers won't
	// even revalidate until the URL changes.
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))[:18] + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// mimeByExtension returns the correct MIME type for common web file extensions.
// This is the source of truth — the DB content_type may be wrong if the LLM set it incorrectly.
func mimeByExtension(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	case ".ico":
		return "image/x-icon"
	case ".xml":
		return "application/xml"
	case ".pdf":
		return "application/pdf"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	default:
		return ""
	}
}

// ServeFile serves user-uploaded files from the files table.
func (h *Handler) ServeFile(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		http.NotFound(w, r)
		return
	}

	// Extract filename from path: /files/{filename}
	filename := strings.TrimPrefix(r.URL.Path, "/files/")
	if filename == "" {
		http.NotFound(w, r)
		return
	}
	cleaned := filepath.ToSlash(filepath.Clean(filename))
	if cleaned != filename || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		http.NotFound(w, r)
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Look up the file in the database.
	var storagePath, contentType sql.NullString
	err = siteDB.QueryRow(
		"SELECT storage_path, content_type FROM ho_files WHERE filename = ?",
		filename,
	).Scan(&storagePath, &contentType)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Serve the file from disk.
	expectedPrefix := filepath.Join("data", "sites", fmt.Sprintf("%d", site.ID))
	fpath := storagePath.String
	if fpath == "" {
		fpath = filepath.Join(expectedPrefix, "files", filename)
	}
	if !strings.HasPrefix(filepath.Clean(fpath), expectedPrefix) {
		http.NotFound(w, r)
		return
	}

	data, err := os.ReadFile(fpath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ct := mimeByExtension(filename)
	if ct == "" {
		ct = contentType.String
	}
	if ct == "" {
		ct = "application/octet-stream"
	}

	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))[:18] + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400, must-revalidate")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// ---------------------------------------------------------------------------
// Dynamic API Handler
// ---------------------------------------------------------------------------

// apiRequestContext holds per-request context for row-level security.
type apiRequestContext struct {
	OwnerFilter    string      // e.g. " AND user_id = ?" or ""
	OwnerFilterVal interface{} // the user ID value, nil if no filter
	AuthUserID     int         // authenticated user's ID (0 = none)
}

type apiEndpoint struct {
	ID            int
	Path          string
	TableName     string
	Methods       []string
	PublicColumns []string // nil means all non-secure
	RequiresAuth  bool
	PublicRead    bool   // GET allowed without auth even when RequiresAuth=true
	RequiredRole  string // empty = any authenticated user, "admin" = admin only
	RateLimit     int
	SecureCols    map[string]string // column → "hash" or "encrypt"
	CORSOrigins   string            // comma-separated allowed origins, "*" for any
	CORSMethods   string            // comma-separated allowed methods
	CORSHeaders   string            // comma-separated allowed headers
	OwnerColumn   string            // when set, queries are scoped to authenticated user's ID
}

func (h *Handler) DynamicAPI(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		writePublicError(w, http.StatusNotFound, "site not found")
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Extract the endpoint path: /api/{path...}
	// The full URL path is /api/something or /api/something/123
	fullPath := strings.TrimPrefix(r.URL.Path, "/api/")
	if fullPath == "" {
		writePublicError(w, http.StatusNotFound, "endpoint not found")
		return
	}

	// Serve OpenAPI spec JSON from ho_assets.
	if fullPath == "docs.json" && r.Method == http.MethodGet {
		h.serveDocsJSON(w, site, siteDB.Reader())
		return
	}

	// Try auth endpoint first (handles /api/{path}/register, /api/{path}/login, /api/{path}/me).
	if h.handleAuthRequest(w, r, site.ID, siteDB.Reader(), siteDB.Writer(), fullPath) {
		return
	}

	// Split into endpoint path and optional ID or sub-route.
	// e.g. "contacts" → path="contacts", id=""
	// e.g. "contacts/5" → path="contacts", id="5"
	// e.g. "contacts/_stats" → path="contacts", handle stats
	parts := strings.SplitN(fullPath, "/", 2)
	endpointPath := parts[0]
	var rowID string
	if len(parts) > 1 {
		rowID = parts[1]
	}

	// Handle /_stats aggregation route before regular CRUD.
	if rowID == "_stats" && r.Method == http.MethodGet {
		h.apiStats(w, r, siteDB.Reader(), endpointPath)
		return
	}

	// Handle /upload file upload route.
	if rowID == "upload" && r.Method == http.MethodPost {
		h.apiUpload(w, r, site.ID, siteDB, endpointPath)
		return
	}

	// Handle /stream SSE route.
	if rowID == "stream" && r.Method == http.MethodGet {
		h.apiStream(w, r, site.ID, siteDB.Reader())
		return
	}

	// Handle /chat and /complete LLM routes.
	if (rowID == "chat" || rowID == "complete") && r.Method == http.MethodPost {
		h.apiLLM(w, r, site, siteDB.Reader(), endpointPath, rowID)
		return
	}

	// Handle /ws WebSocket route.
	if rowID == "ws" {
		h.apiWebSocket(w, r, site.ID, siteDB)
		return
	}

	// Handle /rooms WebSocket room roster.
	if rowID == "rooms" && r.Method == http.MethodGet {
		h.apiWSRooms(w, r, site.ID, endpointPath)
		return
	}

	// Look up the endpoint config.
	ep, err := h.loadEndpoint(siteDB.Reader(), endpointPath)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "endpoint not found")
		return
	}

	// CORS: set headers if configured.
	if ep.CORSOrigins != "" {
		origin := r.Header.Get("Origin")
		if ep.CORSOrigins == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			for _, allowed := range strings.Split(ep.CORSOrigins, ",") {
				if strings.TrimSpace(allowed) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					break
				}
			}
		}
		allowMethods := ep.CORSMethods
		if allowMethods == "" {
			allowMethods = strings.Join(ep.Methods, ", ")
		}
		w.Header().Set("Access-Control-Allow-Methods", allowMethods)
		if ep.CORSHeaders != "" {
			w.Header().Set("Access-Control-Allow-Headers", ep.CORSHeaders)
		} else {
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		// Handle preflight.
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	// Check method is allowed.
	if !methodAllowed(ep.Methods, r.Method) {
		writePublicError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Check auth if required (accepts site API key or user JWT).
	// Skip auth for GET when public_read is enabled.
	var authUserID int    // authenticated user's ID (0 = unauthenticated or site API key)
	var authUserRole string // authenticated user's role
	isSiteKey := false
	if ep.RequiresAuth && !(r.Method == http.MethodGet && ep.PublicRead) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Site API keys bypass role checks (owner-level access).
		if h.validateSiteToken(site.ID, token) {
			isSiteKey = true
		} else {
			// Try user JWT — must be scoped to this site.
			if h.deps.JWTManager == nil {
				writePublicError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			claims, err := h.deps.JWTManager.Validate(token)
			if err != nil || claims.SiteID != site.ID {
				writePublicError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			// Role check if endpoint requires a specific role.
			if ep.RequiredRole != "" && claims.Role != ep.RequiredRole {
				writePublicError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			authUserID = claims.UserID
			authUserRole = claims.Role
		}
	} else if ep.OwnerColumn != "" {
		// Owner-scoped endpoints: try to extract user ID from optional token for GET with public_read.
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "" && h.deps.JWTManager != nil {
			if claims, err := h.deps.JWTManager.Validate(token); err == nil && claims.SiteID == site.ID {
				authUserID = claims.UserID
				authUserRole = claims.Role
			}
		}
	}

	// Row-level security: compute owner filter for scoped endpoints.
	// Admin role and site API keys bypass the filter.
	var ownerFilter string       // SQL WHERE fragment: "AND user_id = ?"
	var ownerFilterVal interface{} // the user ID value
	if ep.OwnerColumn != "" && !isSiteKey && authUserRole != "admin" && authUserID > 0 {
		ownerFilter = fmt.Sprintf(" AND %s = ?", ep.OwnerColumn)
		ownerFilterVal = authUserID
	}
	_ = ownerFilter     // used by apiList, apiGetOne, apiUpdate, apiDelete
	_ = ownerFilterVal  // used alongside ownerFilter

	// Physical table name (per-site DB, no prefix needed).
	physTable := ep.TableName

	rc := &apiRequestContext{
		OwnerFilter:    ownerFilter,
		OwnerFilterVal: ownerFilterVal,
		AuthUserID:     authUserID,
	}

	switch r.Method {
	case http.MethodGet:
		if rowID != "" {
			h.apiGetOne(w, r, siteDB.Reader(), physTable, rowID, ep, rc)
		} else {
			h.apiList(w, r, siteDB.Reader(), physTable, ep, rc)
		}
	case http.MethodPost:
		h.apiInsert(w, r, siteDB.Writer(), physTable, ep, site.ID, endpointPath, rc)
	case http.MethodPut:
		if rowID == "" {
			writePublicError(w, http.StatusBadRequest, "ID required for PUT")
			return
		}
		h.apiUpdate(w, r, siteDB.Writer(), physTable, rowID, ep, site.ID, endpointPath, rc)
	case http.MethodDelete:
		if rowID == "" {
			writePublicError(w, http.StatusBadRequest, "ID required for DELETE")
			return
		}
		h.apiDelete(w, siteDB.Writer(), physTable, rowID, ep, site.ID, endpointPath, rc)
	default:
		writePublicError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) loadEndpoint(siteDB *sql.DB, path string) (*apiEndpoint, error) {
	var ep apiEndpoint
	var methodsJSON string
	var publicColsJSON, requiredRole, ownerColumn sql.NullString
	var corsOrigins, corsMethods, corsHeaders sql.NullString
	err := siteDB.QueryRow(
		"SELECT id, path, table_name, methods, public_columns, requires_auth, public_read, required_role, rate_limit, COALESCE(cors_origins,''), COALESCE(cors_methods,''), COALESCE(cors_headers,''), COALESCE(owner_column,'') FROM ho_api_endpoints WHERE path = ?",
		path,
	).Scan(&ep.ID, &ep.Path, &ep.TableName, &methodsJSON, &publicColsJSON, &ep.RequiresAuth, &ep.PublicRead, &requiredRole, &ep.RateLimit, &corsOrigins, &corsMethods, &corsHeaders, &ownerColumn)
	if err != nil {
		return nil, err
	}

	ep.RequiredRole = requiredRole.String
	ep.OwnerColumn = ownerColumn.String
	ep.CORSOrigins = corsOrigins.String
	ep.CORSMethods = corsMethods.String
	ep.CORSHeaders = corsHeaders.String
	json.Unmarshal([]byte(methodsJSON), &ep.Methods)
	if publicColsJSON.Valid && publicColsJSON.String != "" {
		json.Unmarshal([]byte(publicColsJSON.String), &ep.PublicColumns)
	}

	// Load secure columns for this table.
	var secureRaw string
	err = siteDB.QueryRow(
		"SELECT secure_columns FROM ho_dynamic_tables WHERE table_name = ?",
		ep.TableName,
	).Scan(&secureRaw)
	if err == nil {
		json.Unmarshal([]byte(secureRaw), &ep.SecureCols)
	}

	return &ep, nil
}

func methodAllowed(methods []string, method string) bool {
	for _, m := range methods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

func (h *Handler) validateSiteToken(siteID int, token string) bool {
	if token == "" {
		return false
	}
	// Check if the token matches the site's configured API key.
	// The API key is stored in the site's config JSON as "api_key".
	site, err := models.GetSiteByID(h.deps.DB.DB, siteID)
	if err != nil {
		return false
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(site.Config), &cfg); err != nil {
		return false
	}
	apiKey, _ := cfg["api_key"].(string)
	return apiKey != "" && subtle.ConstantTimeCompare([]byte(apiKey), []byte(token)) == 1
}

// reservedParams are query parameters with special meaning that are not column filters.
var reservedParams = map[string]bool{
	"limit": true, "offset": true, "sort": true, "order": true,
	"order_by": true, "direction": true, "order_direction": true, "page": true,
	"search": true, "stats": true, "q": true, "fields": true,
}

// parseFilterParam extracts column name and SQL operator from a query param key.
// Supports: col (exact), col__like, col__gt, col__gte, col__lt, col__lte, col__ne.
func parseFilterParam(key string) (column, op string, isLike bool, ok bool) {
	parts := strings.SplitN(key, "__", 2)
	column = parts[0]
	if len(parts) == 1 {
		return column, "=", false, true
	}
	switch parts[1] {
	case "like":
		return column, "LIKE", true, true
	case "gt":
		return column, ">", false, true
	case "gte":
		return column, ">=", false, true
	case "lt":
		return column, "<", false, true
	case "lte":
		return column, "<=", false, true
	case "ne":
		return column, "!=", false, true
	default:
		return "", "", false, false
	}
}

// columnExists checks if a column name exists in the given table.
func columnExists(siteDB *sql.DB, physTable, colName string) bool {
	rows, err := siteDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", physTable))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		if rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk) != nil {
			continue
		}
		if name == colName {
			return true
		}
	}
	return false
}

// getTextColumns returns all TEXT column names from a table, excluding secure columns.
func getTextColumns(siteDB *sql.DB, physTable string, ep *apiEndpoint) []string {
	rows, err := siteDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", physTable))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		if rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk) != nil {
			continue
		}
		if strings.EqualFold(colType, "TEXT") && name != "id" && name != "created_at" {
			if kind, ok := ep.SecureCols[name]; ok && kind == "hash" {
				continue
			}
			cols = append(cols, name)
		}
	}
	return cols
}

// apiList handles GET /api/{path} — list rows with pagination and filtering.
// Column filtering: ?column=value adds WHERE column = value.
// Sorting: ?sort=column&order=asc|desc (default: id DESC).
func (h *Handler) apiList(w http.ResponseWriter, r *http.Request, siteDB *sql.DB, physTable string, ep *apiEndpoint, rc *apiRequestContext) {
	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	cols := h.visibleColumns(ep)
	if cols == "" {
		cols = "*"
	}
	// ?fields=col1,col2 narrows the SELECT to only requested columns.
	if fieldsCols := parseFieldsParam(r, ep); fieldsCols != "" {
		cols = fieldsCols
	}

	// Build WHERE clause from query parameters (column filters).
	// Supports: ?col=val (exact), ?col__like=term, ?col__gt=5, ?col__gte=5,
	// ?col__lt=10, ?col__lte=10, ?col__ne=val, ?q=term (full-text across TEXT columns).
	var whereClauses []string
	var whereArgs []interface{}
	for key, vals := range r.URL.Query() {
		if reservedParams[key] || len(vals) == 0 {
			continue
		}
		col, op, isLike, valid := parseFilterParam(key)
		if !valid {
			continue
		}
		if err := security.ValidateColumnName(col); err != nil {
			continue
		}
		if kind, ok := ep.SecureCols[col]; ok && kind == "hash" {
			continue
		}
		if isLike {
			whereClauses = append(whereClauses, col+" LIKE ? COLLATE NOCASE")
			whereArgs = append(whereArgs, "%"+vals[0]+"%")
		} else {
			whereClauses = append(whereClauses, col+" "+op+" ?")
			whereArgs = append(whereArgs, vals[0])
		}
	}

	// ?q=term — search across all TEXT columns.
	if qTerm := r.URL.Query().Get("q"); qTerm != "" {
		textCols := getTextColumns(siteDB, physTable, ep)
		if len(textCols) > 0 {
			var orClauses []string
			for _, tc := range textCols {
				orClauses = append(orClauses, tc+" LIKE ? COLLATE NOCASE")
				whereArgs = append(whereArgs, "%"+qTerm+"%")
			}
			whereClauses = append(whereClauses, "("+strings.Join(orClauses, " OR ")+")")
		}
	}

	// Row-level security: inject owner filter.
	if rc.OwnerFilter != "" {
		whereClauses = append(whereClauses, ep.OwnerColumn+" = ?")
		whereArgs = append(whereArgs, rc.OwnerFilterVal)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Sorting: accept sort/order or order_by/direction.
	orderSQL := "id DESC"
	sortCol := r.URL.Query().Get("sort")
	if sortCol == "" {
		sortCol = r.URL.Query().Get("order_by")
	}
	if sortCol != "" {
		if security.ValidateColumnName(sortCol) == nil && columnExists(siteDB, physTable, sortCol) {
			dir := "ASC"
			orderDir := r.URL.Query().Get("order")
			if orderDir == "" {
				orderDir = r.URL.Query().Get("direction")
			}
			if orderDir == "" {
				orderDir = r.URL.Query().Get("order_direction")
			}
			if strings.EqualFold(orderDir, "desc") {
				dir = "DESC"
			}
			orderSQL = sortCol + " " + dir
		}
	}

	query := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT ? OFFSET ?", cols, physTable, whereSQL, orderSQL)
	args := append(whereArgs, limit, offset)
	rows, err := siteDB.Query(query, args...)
	if err != nil {
		slog.Error("public API list query failed", "table", physTable, "error", err)
		if strings.Contains(err.Error(), "no such column") {
			writePublicError(w, http.StatusBadRequest, "invalid filter column: "+err.Error())
		} else {
			writePublicError(w, http.StatusInternalServerError, "query error")
		}
		return
	}
	defer rows.Close()

	results := h.scanRowsToMaps(rows)
	stripSecureColumns(results, ep.SecureCols)
	writePublicJSON(w, http.StatusOK, map[string]interface{}{
		"data":   results,
		"count":  len(results),
		"limit":  limit,
		"offset": offset,
	})
}

// rowIDColumn returns the column name and value to use for single-row lookups.
// Numeric IDs match the auto-increment "id" column; non-numeric values (UUIDs,
// slugs) are looked up against a "uuid" or "slug" column if one exists.
func rowIDColumn(siteDB *sql.DB, physTable, rowID string) (col string, val interface{}) {
	if _, err := strconv.ParseInt(rowID, 10, 64); err == nil {
		return "id", rowID
	}
	// Non-numeric — check for uuid/slug columns in the table.
	for _, candidate := range []string{"uuid", "slug"} {
		var cnt int
		err := siteDB.QueryRow(
			fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = ?", physTable),
			candidate,
		).Scan(&cnt)
		if err == nil && cnt > 0 {
			return candidate, rowID
		}
	}
	// Fallback to id (will likely return 0 rows, yielding a 404).
	return "id", rowID
}

// apiGetOne handles GET /api/{path}/{id} — get single row.
func (h *Handler) apiGetOne(w http.ResponseWriter, r *http.Request, siteDB *sql.DB, physTable, rowID string, ep *apiEndpoint, rc *apiRequestContext) {
	cols := h.visibleColumns(ep)
	if cols == "" {
		cols = "*"
	}
	if fieldsCols := parseFieldsParam(r, ep); fieldsCols != "" {
		cols = fieldsCols
	}

	lookupCol, lookupVal := rowIDColumn(siteDB, physTable, rowID)
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", cols, physTable, lookupCol)
	args := []interface{}{lookupVal}
	// Row-level security: restrict to owned rows.
	if rc.OwnerFilter != "" {
		query += " AND " + ep.OwnerColumn + " = ?"
		args = append(args, rc.OwnerFilterVal)
	}
	rows, err := siteDB.Query(query, args...)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "query error")
		return
	}
	defer rows.Close()

	results := h.scanRowsToMaps(rows)
	if len(results) == 0 {
		writePublicError(w, http.StatusNotFound, "not found")
		return
	}

	stripSecureColumns(results, ep.SecureCols)
	writePublicJSON(w, http.StatusOK, results[0])
}

// apiInsert handles POST /api/{path} — insert a row.
func (h *Handler) apiInsert(w http.ResponseWriter, r *http.Request, siteDB *sql.DB, physTable string, ep *apiEndpoint, siteID int, endpointPath string, rc *apiRequestContext) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writePublicError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Remove id field if provided (auto-generated).
	delete(body, "id")

	// Row-level security: auto-set owner column on insert.
	if ep.OwnerColumn != "" && rc.AuthUserID > 0 {
		body[ep.OwnerColumn] = rc.AuthUserID
	}

	// Process secure columns.
	for col, kind := range ep.SecureCols {
		if val, ok := body[col]; ok {
			processed, err := processPublicSecureValue(kind, val, h.deps.Encryptor)
			if err != nil {
				writePublicError(w, http.StatusBadRequest, fmt.Sprintf("error processing %s: %s", col, err))
				return
			}
			body[col] = processed
		}
	}

	if len(body) == 0 {
		writePublicError(w, http.StatusBadRequest, "no data provided")
		return
	}

	// Validate that columns in the request body actually exist in the table.
	rows, err := siteDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", physTable))
	if err == nil {
		validCols := make(map[string]bool)
		for rows.Next() {
			var cid int
			var name, colType string
			var notNull int
			var dfltValue sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err == nil {
				validCols[name] = true
			}
		}
		rows.Close()
		if len(validCols) > 0 {
			for col := range body {
				if col == "id" || col == "created_at" {
					continue
				}
				if !validCols[col] {
					slog.Warn("public API insert: unknown column ignored", "table", physTable, "column", col)
					delete(body, col)
				}
			}
			if len(body) == 0 {
				writePublicError(w, http.StatusBadRequest, "no valid columns provided")
				return
			}
		}
	}

	columns := make([]string, 0, len(body))
	placeholders := make([]string, 0, len(body))
	values := make([]interface{}, 0, len(body))
	for col, val := range body {
		if err := security.ValidateColumnName(col); err != nil {
			writePublicError(w, http.StatusBadRequest, "invalid field name: "+col)
			return
		}
		columns = append(columns, col)
		placeholders = append(placeholders, "?")
		values = append(values, val)
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", physTable, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	result, err := sqliteretry.Exec(siteDB, query, values...)
	if err != nil {
		slog.Error("public API insert failed", "table", physTable, "error", err)
		writePublicError(w, http.StatusBadRequest, "insert failed")
		return
	}

	id, _ := result.LastInsertId()

	// Publish data.insert event for real-time streams and actions.
	if h.deps.Bus != nil {
		payload := map[string]interface{}{"table": endpointPath, "id": id}
		for k, v := range body {
			if k != "table" && k != "id" {
				payload[k] = v
			}
		}
		h.deps.Bus.Publish(events.NewEvent(events.EventDataInsert, siteID, payload))
	}

	// Read the created row back from the same write connection so the
	// response contains all columns (including defaults and triggers).
	// This matches the contract documented in the brain prompts.
	cols := h.visibleColumns(ep)
	if cols == "" {
		cols = "*"
	}
	readBack, err := siteDB.Query(fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", cols, physTable), id)
	if err == nil {
		rows := h.scanRowsToMaps(readBack)
		if len(rows) > 0 {
			stripSecureColumns(rows, ep.SecureCols)
			writePublicJSON(w, http.StatusCreated, rows[0])
			return
		}
	}

	// Fallback if read-back fails for any reason.
	writePublicJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      id,
		"success": true,
	})
}

// apiUpdate handles PUT /api/{path}/{id} — update a row.
func (h *Handler) apiUpdate(w http.ResponseWriter, r *http.Request, siteDB *sql.DB, physTable, rowID string, ep *apiEndpoint, siteID int, endpointPath string, rc *apiRequestContext) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writePublicError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	delete(body, "id")

	// Process secure columns.
	for col, kind := range ep.SecureCols {
		if val, ok := body[col]; ok {
			processed, err := processPublicSecureValue(kind, val, h.deps.Encryptor)
			if err != nil {
				writePublicError(w, http.StatusBadRequest, fmt.Sprintf("error processing %s: %s", col, err))
				return
			}
			body[col] = processed
		}
	}

	if len(body) == 0 {
		writePublicError(w, http.StatusBadRequest, "no data provided")
		return
	}

	setClauses := make([]string, 0, len(body))
	values := make([]interface{}, 0, len(body)+1)
	for col, val := range body {
		if err := security.ValidateColumnName(col); err != nil {
			writePublicError(w, http.StatusBadRequest, "invalid field name: "+col)
			return
		}
		setClauses = append(setClauses, col+" = ?")
		values = append(values, val)
	}
	lookupCol, lookupVal := rowIDColumn(siteDB, physTable, rowID)
	values = append(values, lookupVal)

	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?", physTable, strings.Join(setClauses, ", "), lookupCol)
	// Row-level security: restrict update to owned rows.
	if rc.OwnerFilter != "" {
		query += " AND " + ep.OwnerColumn + " = ?"
		values = append(values, rc.OwnerFilterVal)
	}
	result, err := sqliteretry.Exec(siteDB, query, values...)
	if err != nil {
		slog.Error("public API update failed", "table", physTable, "error", err)
		writePublicError(w, http.StatusBadRequest, "update failed")
		return
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		writePublicError(w, http.StatusNotFound, "not found")
		return
	}

	// Publish data.update event for real-time streams and actions.
	if h.deps.Bus != nil {
		payload := map[string]interface{}{"table": endpointPath, "id": rowID}
		for k, v := range body {
			if k != "table" && k != "id" {
				payload[k] = v
			}
		}
		h.deps.Bus.Publish(events.NewEvent(events.EventDataUpdate, siteID, payload))
	}

	// Read the updated row back from the same write connection so the
	// response contains the full current state of the record.
	cols := h.visibleColumns(ep)
	if cols == "" {
		cols = "*"
	}
	readBack, err := siteDB.Query(fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", cols, physTable, lookupCol), lookupVal)
	if err == nil {
		rows := h.scanRowsToMaps(readBack)
		if len(rows) > 0 {
			stripSecureColumns(rows, ep.SecureCols)
			writePublicJSON(w, http.StatusOK, rows[0])
			return
		}
	}

	// Fallback if read-back fails for any reason.
	writePublicJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// apiDelete handles DELETE /api/{path}/{id} — delete a row.
func (h *Handler) apiDelete(w http.ResponseWriter, siteDB *sql.DB, physTable, rowID string, ep *apiEndpoint, siteID int, endpointPath string, rc *apiRequestContext) {
	lookupCol, lookupVal := rowIDColumn(siteDB, physTable, rowID)
	query := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", physTable, lookupCol)
	args := []interface{}{lookupVal}
	// Row-level security: restrict delete to owned rows.
	if rc.OwnerFilter != "" {
		query += " AND " + ep.OwnerColumn + " = ?"
		args = append(args, rc.OwnerFilterVal)
	}
	result, err := sqliteretry.Exec(siteDB, query, args...)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "delete failed")
		return
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		writePublicError(w, http.StatusNotFound, "not found")
		return
	}

	// Publish data.delete event for real-time streams.
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventDataDelete, siteID, map[string]interface{}{
			"table": endpointPath, "id": rowID,
		}))
	}

	writePublicJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// visibleColumns returns a SQL column list based on the endpoint's public_columns
// and secure column configuration. PASSWORD columns are always excluded.
func (h *Handler) visibleColumns(ep *apiEndpoint) string {
	if len(ep.PublicColumns) > 0 {
		// Filter out any password columns from the explicit public list.
		var safe []string
		for _, col := range ep.PublicColumns {
			if kind, ok := ep.SecureCols[col]; ok && kind == "hash" {
				continue // never expose password hashes
			}
			safe = append(safe, col)
		}
		if len(safe) == 0 {
			return "id"
		}
		return strings.Join(safe, ", ")
	}
	// No explicit public columns — return * and post-filter secure columns.
	return "*"
}

// parseFieldsParam reads ?fields=col1,col2 from the request and returns a
// validated, safe column list. Returns "" if the param is absent or empty,
// which signals the caller to use the default visibleColumns() result.
// Security: validates each name, strips hash columns, intersects with
// public_columns if configured, and always includes "id".
func parseFieldsParam(r *http.Request, ep *apiEndpoint) string {
	raw := r.URL.Query().Get("fields")
	if raw == "" {
		return ""
	}

	// Build allowed set from public_columns (if configured).
	var allowed map[string]bool
	if len(ep.PublicColumns) > 0 {
		allowed = make(map[string]bool, len(ep.PublicColumns))
		for _, col := range ep.PublicColumns {
			allowed[col] = true
		}
	}

	hasID := false
	seen := make(map[string]bool)
	var cols []string
	for _, f := range strings.Split(raw, ",") {
		col := strings.TrimSpace(f)
		if col == "" || seen[col] {
			continue
		}
		if security.ValidateColumnName(col) != nil {
			continue // reject SQL injection attempts
		}
		if kind, ok := ep.SecureCols[col]; ok && kind == "hash" {
			continue // never expose password hashes
		}
		if allowed != nil && !allowed[col] {
			continue // not in public_columns whitelist
		}
		seen[col] = true
		cols = append(cols, col)
		if col == "id" {
			hasID = true
		}
	}

	if len(cols) == 0 {
		return ""
	}
	if !hasID {
		cols = append([]string{"id"}, cols...)
	}
	return strings.Join(cols, ", ")
}

// scanRowsToMaps converts sql.Rows into a slice of maps.
func (h *Handler) scanRowsToMaps(rows *sql.Rows) []map[string]interface{} {
	defer rows.Close() // safe even if caller also defers Close

	columns, err := rows.Columns()
	if err != nil {
		return nil
	}

	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			// Convert []byte to string for JSON serialization.
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else if f, ok := val.(float64); ok && f == float64(int64(f)) {
				row[col] = int64(f)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
	}

	return results
}

// stripSecureColumns removes PASSWORD (hash) and ENCRYPTED columns from API
// results when no explicit public_columns filter was set on the endpoint.
func stripSecureColumns(results []map[string]interface{}, secureCols map[string]string) {
	if len(secureCols) == 0 {
		return
	}
	for _, row := range results {
		for col, kind := range secureCols {
			if kind == "hash" {
				delete(row, col)
			}
		}
	}
}

// processPublicSecureValue delegates to the shared security.ProcessSecureValue.
func processPublicSecureValue(kind string, value interface{}, enc *security.Encryptor) (interface{}, error) {
	return security.ProcessSecureValue(kind, value, enc)
}

// ---------------------------------------------------------------------------
// File Upload handler
// ---------------------------------------------------------------------------

type uploadEndpointConfig struct {
	Path         string
	AllowedTypes []string
	MaxSizeMB    int
	RequiresAuth bool
	TableName    string
}

func (h *Handler) loadUploadEndpoint(siteDB *sql.DB, path string) (*uploadEndpointConfig, error) {
	var ue uploadEndpointConfig
	var allowedTypesJSON string
	var tableName sql.NullString
	err := siteDB.QueryRow(
		"SELECT path, allowed_types, max_size_mb, requires_auth, table_name FROM ho_upload_endpoints WHERE path = ?",
		path,
	).Scan(&ue.Path, &allowedTypesJSON, &ue.MaxSizeMB, &ue.RequiresAuth, &tableName)
	if err != nil {
		return nil, err
	}
	if allowedTypesJSON != "" {
		if err := json.Unmarshal([]byte(allowedTypesJSON), &ue.AllowedTypes); err != nil {
			slog.Warn("invalid allowed_types JSON in upload endpoint", "path", path, "error", err)
		}
	}
	if ue.MaxSizeMB == 0 {
		ue.MaxSizeMB = 5
	}
	ue.TableName = tableName.String
	return &ue, nil
}

// mimeTypeMatches checks if a content type matches a glob pattern (e.g. "image/*").
func mimeTypeMatches(contentType string, pattern string) bool {
	if pattern == "*/*" || pattern == "*" {
		return true
	}
	// Try exact match first.
	if strings.EqualFold(contentType, pattern) {
		return true
	}
	// Glob match: "image/*" matches "image/png".
	if strings.HasSuffix(pattern, "/*") {
		prefix := pattern[:len(pattern)-1] // "image/"
		return strings.HasPrefix(strings.ToLower(contentType), strings.ToLower(prefix))
	}
	return false
}

// apiUpload handles POST /api/{path}/upload — multipart file uploads.
func (h *Handler) apiUpload(w http.ResponseWriter, r *http.Request, siteID int, siteDB *db.SiteDB, endpointPath string) {
	ue, err := h.loadUploadEndpoint(siteDB.Reader(), endpointPath)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "upload endpoint not found")
		return
	}

	// Wrap the body with MaxBytesReader BEFORE any early returns.
	// This prevents ERR_CONNECTION_RESET: if we reject early (auth, type),
	// MaxBytesReader ensures Go's HTTP server drains the body gracefully
	// instead of closing the connection while the client is still sending.
	maxBytes := int64(ue.MaxSizeMB) * 1024 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1024)

	// Check auth if required (accepts both site API keys and user JWTs).
	if ue.RequiresAuth {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !h.validateSiteTokenOrJWT(siteID, token) {
			writePublicError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}

	// Parse multipart form.
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writePublicError(w, http.StatusBadRequest, fmt.Sprintf("file too large (max %d MB)", ue.MaxSizeMB))
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writePublicError(w, http.StatusBadRequest, "file field is required (use multipart form with field name 'file')")
		return
	}
	defer file.Close()

	// Validate content type (empty AllowedTypes = accept all).
	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	if len(ue.AllowedTypes) > 0 {
		typeAllowed := false
		for _, pattern := range ue.AllowedTypes {
			if mimeTypeMatches(ct, pattern) {
				typeAllowed = true
				break
			}
		}
		if !typeAllowed {
			writePublicError(w, http.StatusBadRequest, fmt.Sprintf("file type %q not allowed (accepted: %v)", ct, ue.AllowedTypes))
			return
		}
	}

	// Read file data.
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		writePublicError(w, http.StatusBadRequest, fmt.Sprintf("file too large (max %d MB)", ue.MaxSizeMB))
		return
	}

	// Generate unique filename.
	ext := filepath.Ext(header.Filename)
	if ext == "" {
		// Try to get extension from content type.
		exts, _ := mime.ExtensionsByType(ct)
		if len(exts) > 0 {
			ext = exts[0]
		}
	}
	uniqueName := fmt.Sprintf("%d_%s%s", time.Now().UnixNano(), strings.TrimSuffix(filepath.Base(header.Filename), ext), ext)

	// Optional subfolder from form field (e.g. "user_5" or "post_42/images").
	// Sanitized to prevent path traversal.
	subFolder := sanitizeUploadFolder(r.FormValue("folder"))
	dirParts := []string{"data", "sites", fmt.Sprintf("%d", siteID), "files", "uploads"}
	storageName := "uploads/"
	if subFolder != "" {
		dirParts = append(dirParts, subFolder)
		storageName = "uploads/" + subFolder + "/"
	}
	storageName += uniqueName

	// Write to disk.
	dir := filepath.Join(dirParts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writePublicError(w, http.StatusInternalServerError, "storage error")
		return
	}
	storagePath := filepath.Join(dir, uniqueName)
	if err := os.WriteFile(storagePath, data, 0o644); err != nil {
		writePublicError(w, http.StatusInternalServerError, "storage error")
		return
	}

	// Insert metadata into files table.
	_, err = siteDB.Writer().Exec(
		`INSERT INTO ho_files (filename, content_type, size, storage_path) VALUES (?, ?, ?, ?)`,
		storageName, ct, len(data), storagePath,
	)
	if err != nil {
		slog.Error("upload: failed to insert file metadata", "error", err)
		os.Remove(storagePath) // clean up orphaned file
		writePublicError(w, http.StatusInternalServerError, "storage error")
		return
	}

	// If linked to a table, also insert a row there.
	fileURL := "/files/" + storageName
	if ue.TableName != "" {
		if _, err := siteDB.Writer().Exec(
			`INSERT INTO `+ue.TableName+` (filename, url, content_type, size) VALUES (?, ?, ?, ?)`,
			header.Filename, fileURL, ct, len(data),
		); err != nil {
			slog.Warn("upload: linked table insert failed", "table", ue.TableName, "error", err)
		}
	}

	writePublicJSON(w, http.StatusCreated, map[string]interface{}{
		"url":      fileURL,
		"filename": header.Filename,
		"size":     len(data),
		"type":     ct,
	})
}

// sanitizeUploadFolder cleans a user-provided subfolder path for safe storage.
// Allows forward-slash-separated subdirectories but rejects path traversal.
func sanitizeUploadFolder(folder string) string {
	if folder == "" {
		return ""
	}
	folder = strings.ReplaceAll(folder, "\\", "/")
	parts := strings.Split(folder, "/")
	var clean []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			continue
		}
		clean = append(clean, p)
	}
	return strings.Join(clean, "/")
}

// ---------------------------------------------------------------------------
// SSE Stream handler
// ---------------------------------------------------------------------------

type streamEndpointConfig struct {
	Path         string
	EventTypes   []string
	RequiresAuth bool
}

func (h *Handler) loadStreamEndpoint(siteDB *sql.DB, path string) (*streamEndpointConfig, error) {
	var se streamEndpointConfig
	var eventTypesJSON string
	err := siteDB.QueryRow(
		"SELECT path, event_types, requires_auth FROM ho_stream_endpoints WHERE path = ?",
		path,
	).Scan(&se.Path, &eventTypesJSON, &se.RequiresAuth)
	if err != nil {
		return nil, err
	}
	if eventTypesJSON != "" {
		if err := json.Unmarshal([]byte(eventTypesJSON), &se.EventTypes); err != nil {
			slog.Warn("invalid event_types JSON in stream endpoint", "path", path, "error", err)
		}
	}
	// Default to standard data events if none configured.
	if len(se.EventTypes) == 0 {
		se.EventTypes = []string{"data.insert", "data.update", "data.delete"}
	}
	return &se, nil
}

// apiStream handles GET /api/{path}/stream — SSE for real-time data changes.
func (h *Handler) apiStream(w http.ResponseWriter, r *http.Request, siteID int, siteDB *sql.DB) {
	// Extract the endpoint path from the URL.
	fullPath := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.SplitN(fullPath, "/", 2)
	endpointPath := parts[0]

	se, err := h.loadStreamEndpoint(siteDB, endpointPath)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "stream endpoint not found")
		return
	}

	// Check auth if required.
	if se.RequiresAuth {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			// Also check query param for EventSource (which can't set headers).
			token = r.URL.Query().Get("token")
		}
		if !h.validateSiteTokenOrJWT(siteID, token) {
			writePublicError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writePublicError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Disable write deadline for this long-lived SSE connection.
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Build a set of allowed event types for fast lookup.
	allowedTypes := make(map[events.EventType]bool)
	for _, et := range se.EventTypes {
		allowedTypes[events.EventType(et)] = true
	}

	eventCh := make(chan events.Event, 64)
	subID := h.deps.Bus.SubscribeAll(func(e events.Event) {
		// Filter by site and event type.
		if e.SiteID != siteID {
			return
		}
		if !allowedTypes[e.Type] {
			return
		}
		select {
		case eventCh <- e:
		default:
			slog.Warn("SSE event dropped (channel full)", "site_id", siteID, "event", e.Type)
		}
	})
	defer h.deps.Bus.Unsubscribe(subID)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-eventCh:
			data, err := json.Marshal(e.Payload)
			if err != nil {
				slog.Warn("SSE marshal error", "site_id", siteID, "event", e.Type, "error", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// WebSocket (/ws) handler
// ---------------------------------------------------------------------------

// validTableName matches safe table names for write_to_table.
var validTableName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

type wsEndpointConfig struct {
	Path         string
	WriteToTable string
	RoomColumn   string // JSON field to extract room from messages (e.g. "room")
	RequiresAuth bool
	// Cached column schema for write_to_table (maps column name → type).
	tableColumns map[string]string
}

func (h *Handler) loadWSEndpoint(siteDB *sql.DB, path string) (*wsEndpointConfig, error) {
	var ws wsEndpointConfig
	var writeToTable, roomColumn sql.NullString
	err := siteDB.QueryRow(
		"SELECT path, write_to_table, room_column, requires_auth FROM ho_ws_endpoints WHERE path = ?",
		path,
	).Scan(&ws.Path, &writeToTable, &roomColumn, &ws.RequiresAuth)
	if err != nil {
		return nil, err
	}
	if writeToTable.Valid {
		ws.WriteToTable = writeToTable.String
	}
	if roomColumn.Valid {
		ws.RoomColumn = roomColumn.String
	}

	// Pre-load table column schema for structured write_to_table inserts.
	if ws.WriteToTable != "" && validTableName.MatchString(ws.WriteToTable) {
		var schemaDef sql.NullString
		siteDB.QueryRow("SELECT schema_def FROM ho_dynamic_tables WHERE table_name = ?", ws.WriteToTable).Scan(&schemaDef)
		if schemaDef.Valid && schemaDef.String != "" {
			ws.tableColumns = make(map[string]string)
			json.Unmarshal([]byte(schemaDef.String), &ws.tableColumns)
		}
	}
	return &ws, nil
}

// apiWebSocket handles GET /api/{path}/ws — pure transparent relay with room support.
// Messages are relayed as-sent to all other clients in the same room, with _sender injected.
func (h *Handler) apiWebSocket(w http.ResponseWriter, r *http.Request, siteID int, siteDB *db.SiteDB) {
	// Extract the endpoint path from the URL.
	fullPath := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.SplitN(fullPath, "/", 2)
	endpointPath := parts[0]

	wsCfg, err := h.loadWSEndpoint(siteDB.Reader(), endpointPath)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "websocket endpoint not found")
		return
	}

	// Check auth if required.
	if wsCfg.RequiresAuth {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if !h.validateSiteTokenOrJWT(siteID, token) {
			writePublicError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}

	// Accept the WebSocket connection.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // CORS handled by Caddy
	})
	if err != nil {
		slog.Warn("websocket accept error", "site_id", siteID, "path", endpointPath, "error", err)
		return
	}
	defer conn.CloseNow()

	clientID := uuid.New().String()
	initialRoom := r.URL.Query().Get("room") // default "" = global room
	roomKey := fmt.Sprintf("%d:%s:%s", siteID, endpointPath, initialRoom)

	// Join the hub room.
	client := h.deps.WSHub.join(roomKey, clientID)
	defer h.deps.WSHub.leave(roomKey, clientID)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Track current room key for dynamic room switching via room_column.
	currentRoomKey := roomKey

	// Read pump: reads incoming messages and broadcasts to room.
	go func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}

			// Validate JSON.
			var msgMap map[string]interface{}
			if err := json.Unmarshal(data, &msgMap); err != nil {
				continue // skip non-JSON or non-object messages
			}

			// Dynamic room switching via room_column: if the message contains
			// the configured room field, switch the client to that room.
			if wsCfg.RoomColumn != "" {
				if msgRoom, ok := msgMap[wsCfg.RoomColumn].(string); ok && msgRoom != "" {
					newRoomKey := fmt.Sprintf("%d:%s:%s", siteID, endpointPath, msgRoom)
					if newRoomKey != currentRoomKey {
						h.deps.WSHub.leave(currentRoomKey, clientID)
						h.deps.WSHub.join(newRoomKey, clientID)
						currentRoomKey = newRoomKey
					}
				}
			}

			// Inject _sender so receivers know who sent the message.
			msgMap["_sender"] = clientID

			outBytes, err := json.Marshal(msgMap)
			if err != nil {
				continue
			}

			// Targeted message: if _to is set, send only to that client.
			if to, ok := msgMap["_to"].(string); ok && to != "" {
				h.deps.WSHub.sendTo(currentRoomKey, to, outBytes)
			} else {
				// Broadcast to all other clients in the same room.
				h.deps.WSHub.broadcast(currentRoomKey, clientID, outBytes)
			}

			// Optional: write to table with structured column mapping.
			if wsCfg.WriteToTable != "" && validTableName.MatchString(wsCfg.WriteToTable) {
				wsWriteToTable(siteDB, wsCfg, msgMap)
			}
		}
	}()

	// Write pump: drains the client's outbound channel and sends keepalives.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-client.ch:
			if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := conn.Ping(ctx); err != nil {
				return
			}
		}
	}
}

// apiWSRooms returns the list of active WebSocket rooms and their client counts.
func (h *Handler) apiWSRooms(w http.ResponseWriter, r *http.Request, siteID int, endpointPath string) {
	prefix := fmt.Sprintf("%d:%s:", siteID, endpointPath)
	specificRoom := r.URL.Query().Get("room")

	if specificRoom != "" {
		// Return clients in a specific room.
		roomKey := fmt.Sprintf("%d:%s:%s", siteID, endpointPath, specificRoom)
		clients := h.deps.WSHub.roomClients(roomKey)
		if clients == nil {
			clients = []string{}
		}
		writePublicJSON(w, http.StatusOK, map[string]interface{}{
			"room":    specificRoom,
			"clients": clients,
			"count":   len(clients),
		})
		return
	}

	// Return all rooms for this endpoint.
	activeRooms := h.deps.WSHub.activeRooms(prefix)
	type roomInfo struct {
		Room  string `json:"room"`
		Count int    `json:"count"`
	}
	var rooms []roomInfo
	for key, count := range activeRooms {
		roomName := key[len(prefix):]
		rooms = append(rooms, roomInfo{Room: roomName, Count: count})
	}
	if rooms == nil {
		rooms = []roomInfo{}
	}
	writePublicJSON(w, http.StatusOK, rooms)
}

// wsWriteToTable inserts a WebSocket message into the configured table,
// mapping JSON fields to table columns using the cached schema.
func wsWriteToTable(siteDB *db.SiteDB, ws *wsEndpointConfig, msgMap map[string]interface{}) {
	if len(ws.tableColumns) == 0 {
		// No schema cached — fall back to legacy data column.
		raw, _ := json.Marshal(msgMap)
		siteDB.ExecWrite(
			fmt.Sprintf("INSERT INTO %s (data, created_at) VALUES (?, datetime('now'))", ws.WriteToTable),
			string(raw),
		)
		return
	}

	// Map incoming JSON fields to table columns.
	var cols []string
	var placeholders []string
	var vals []interface{}
	for colName := range ws.tableColumns {
		if colName == "id" || colName == "created_at" {
			continue
		}
		val, exists := msgMap[colName]
		if !exists {
			continue
		}
		cols = append(cols, colName)
		placeholders = append(placeholders, "?")
		vals = append(vals, val)
	}

	if len(cols) == 0 {
		return // no matching fields
	}

	// Always include created_at.
	cols = append(cols, "created_at")
	placeholders = append(placeholders, "datetime('now')")

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		ws.WriteToTable,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	)
	siteDB.ExecWrite(query, vals...)
}

// ---------------------------------------------------------------------------
// Aggregation (/_stats) handler
// ---------------------------------------------------------------------------

// allowedAggFuncsPublic is the whitelist of aggregate functions for the public API.
var allowedAggFuncsPublic = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
}

// apiStats handles GET /api/{path}/_stats — aggregation queries.
// Query params: fn (required), column (required for sum/avg/min/max), group_by (comma-separated).
// Also supports same column=value filters as apiList.
func (h *Handler) apiStats(w http.ResponseWriter, r *http.Request, siteDB *sql.DB, endpointPath string) {
	// Look up the endpoint to get table name and auth requirements.
	ep, err := h.loadEndpoint(siteDB, endpointPath)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "endpoint not found")
		return
	}

	// Check auth if required. Stats are GET-only, so skip when public_read.
	if ep.RequiresAuth && !ep.PublicRead {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !h.validateSiteToken(getSite(r).ID, token) {
			writePublicError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}

	q := r.URL.Query()
	fn := strings.ToLower(strings.TrimSpace(q.Get("fn")))
	if !allowedAggFuncsPublic[fn] {
		writePublicError(w, http.StatusBadRequest, "fn is required: count, sum, avg, min, or max")
		return
	}

	col := q.Get("column")
	if fn != "count" && col == "" {
		writePublicError(w, http.StatusBadRequest, fn+" requires a column parameter")
		return
	}
	if col != "" {
		if err := security.ValidateColumnName(col); err != nil {
			writePublicError(w, http.StatusBadRequest, "invalid column name")
			return
		}
		// Don't allow aggregation on secure columns.
		if kind, ok := ep.SecureCols[col]; ok && kind == "hash" {
			writePublicError(w, http.StatusBadRequest, "cannot aggregate on secure columns")
			return
		}
	}

	// Build aggregate expression.
	aggExpr := "COUNT(*)"
	if fn != "count" {
		aggExpr = fmt.Sprintf("%s(%s)", strings.ToUpper(fn), col)
	} else if col != "" {
		aggExpr = fmt.Sprintf("COUNT(%s)", col)
	}

	// Parse group_by (comma-separated).
	var groupCols []string
	if gb := q.Get("group_by"); gb != "" {
		for _, gc := range strings.Split(gb, ",") {
			gc = strings.TrimSpace(gc)
			if gc == "" {
				continue
			}
			if err := security.ValidateColumnName(gc); err != nil {
				writePublicError(w, http.StatusBadRequest, "invalid group_by column: "+gc)
				return
			}
			if kind, ok := ep.SecureCols[gc]; ok && kind == "hash" {
				writePublicError(w, http.StatusBadRequest, "cannot group by secure columns")
				return
			}
			groupCols = append(groupCols, gc)
		}
	}

	// Build SELECT.
	physTable := ep.TableName
	var selectParts []string
	for _, gc := range groupCols {
		selectParts = append(selectParts, gc)
	}
	selectParts = append(selectParts, aggExpr+" AS result")
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectParts, ", "), physTable)

	// Build WHERE from query params (same logic as apiList).
	var whereClauses []string
	var whereArgs []interface{}
	statsReserved := map[string]bool{"fn": true, "column": true, "group_by": true}
	for key, vals := range q {
		if reservedParams[key] || statsReserved[key] || len(vals) == 0 {
			continue
		}
		if err := security.ValidateColumnName(key); err != nil {
			continue
		}
		if kind, ok := ep.SecureCols[key]; ok && kind == "hash" {
			continue
		}
		whereClauses = append(whereClauses, key+" = ?")
		whereArgs = append(whereArgs, vals[0])
	}
	if len(whereClauses) > 0 {
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	if len(groupCols) > 0 {
		query += " GROUP BY " + strings.Join(groupCols, ", ")
		query += " ORDER BY " + strings.Join(groupCols, ", ")
	}

	// Execute.
	if len(groupCols) == 0 {
		var result float64
		err := siteDB.QueryRow(query, whereArgs...).Scan(&result)
		if err != nil {
			writePublicError(w, http.StatusInternalServerError, "aggregation query failed")
			return
		}
		writePublicJSON(w, http.StatusOK, map[string]interface{}{
			"function": fn,
			"result":   result,
		})
		return
	}

	// Grouped results.
	rows, err := siteDB.Query(query, whereArgs...)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "aggregation query failed")
		return
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		scanTargets := make([]interface{}, len(groupCols)+1)
		values := make([]interface{}, len(groupCols)+1)
		for i := range scanTargets {
			scanTargets[i] = &values[i]
		}
		if err := rows.Scan(scanTargets...); err != nil {
			continue
		}
		row := make(map[string]interface{})
		for i, gc := range groupCols {
			if b, ok := values[i].([]byte); ok {
				row[gc] = string(b)
			} else {
				row[gc] = values[i]
			}
		}
		row["result"] = values[len(groupCols)]
		results = append(results, row)
	}

	writePublicJSON(w, http.StatusOK, map[string]interface{}{
		"function": fn,
		"data":     results,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// serveDocsJSON serves the auto-generated OpenAPI spec from ho_assets.
func (h *Handler) serveDocsJSON(w http.ResponseWriter, site *models.Site, siteDB *sql.DB) {
	var storagePath sql.NullString
	err := siteDB.QueryRow(
		"SELECT storage_path FROM ho_assets WHERE filename = 'api-docs.json' AND scope = 'global'",
	).Scan(&storagePath)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "API docs not generated yet")
		return
	}

	fpath := storagePath.String
	if fpath == "" {
		fpath = filepath.Join("data", "sites", fmt.Sprintf("%d", site.ID), "assets", "api-docs.json")
	}

	data, err := os.ReadFile(fpath)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "API docs not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// writePublicJSON writes a JSON response.
func writePublicJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writePublicError writes a JSON error response.
func writePublicError(w http.ResponseWriter, status int, msg string) {
	writePublicJSON(w, status, map[string]string{"error": msg})
}

// wantsHTML returns true if the Accept header prefers HTML over JSON.
func wantsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}

// serveErrorPage writes a branded HTML error page with inline dark-themed styling.
func serveErrorPage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{min-height:100vh;display:flex;flex-direction:column;justify-content:center;align-items:center;
  background:#0f1117;color:#e1e2e6;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
  text-align:center;padding:2rem}
.code{font-size:6rem;font-weight:700;color:#c0562a;line-height:1}
.title{font-size:1.5rem;margin:.75rem 0 .5rem;color:#fff}
.message{font-size:1rem;color:#9ca3af;max-width:28rem;line-height:1.6}
footer{position:fixed;bottom:0;width:100%%;padding:1.25rem;color:#6b7280;font-size:.8rem}
footer a{color:#c0562a;text-decoration:none}
footer a:hover{text-decoration:underline}
</style>
</head>
<body>
<div class="code">%d</div>
<div class="title">%s</div>
<div class="message">%s</div>
<footer>Powered by <a href="https://github.com/markdr-hue/HO" target="_blank" rel="noopener">HO</a></footer>
</body>
</html>`, html.EscapeString(title), status, html.EscapeString(title), html.EscapeString(message))
}

// ---------------------------------------------------------------------------
// LLM endpoint handler
// ---------------------------------------------------------------------------

// llmEndpointConfig holds the config for an LLM endpoint.
type llmEndpointConfig struct {
	Path         string
	SystemPrompt string
	ModelID      string
	MaxTokens    int
	Temperature  float64
	MaxHistory   int
	RateLimit    int
	RequiresAuth bool
	CORSOrigins  string
}

func (h *Handler) loadLLMEndpoint(siteDB *sql.DB, path string) (*llmEndpointConfig, error) {
	var ep llmEndpointConfig
	err := siteDB.QueryRow(
		"SELECT path, system_prompt, model_id, max_tokens, temperature, max_history, rate_limit, requires_auth, cors_origins FROM ho_llm_endpoints WHERE path = ?",
		path,
	).Scan(&ep.Path, &ep.SystemPrompt, &ep.ModelID, &ep.MaxTokens, &ep.Temperature, &ep.MaxHistory, &ep.RateLimit, &ep.RequiresAuth, &ep.CORSOrigins)
	if err != nil {
		return nil, err
	}
	return &ep, nil
}

// llmRateLimiter tracks request counts per key for simple rate limiting.
var llmRateLimiter = struct {
	sync.Mutex
	counts map[string][]time.Time
}{counts: make(map[string][]time.Time)}

// checkLLMRateLimit returns true if the request is within the rate limit.
func checkLLMRateLimit(key string, maxPerMinute int) bool {
	llmRateLimiter.Lock()
	defer llmRateLimiter.Unlock()

	now := time.Now()
	window := now.Add(-time.Minute)

	// Filter out expired entries.
	timestamps := llmRateLimiter.counts[key]
	valid := timestamps[:0]
	for _, ts := range timestamps {
		if ts.After(window) {
			valid = append(valid, ts)
		}
	}

	if len(valid) >= maxPerMinute {
		llmRateLimiter.counts[key] = valid
		return false
	}

	llmRateLimiter.counts[key] = append(valid, now)
	return true
}

// llmChatRequest is the JSON body for LLM chat/complete requests.
type llmChatRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

const maxLLMBodySize = 64 * 1024 // 64 KB

// apiLLM handles POST /api/{path}/chat and /api/{path}/complete.
func (h *Handler) apiLLM(w http.ResponseWriter, r *http.Request, site *models.Site, siteDB *sql.DB, endpointPath, mode string) {
	ep, err := h.loadLLMEndpoint(siteDB, endpointPath)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "LLM endpoint not found")
		return
	}

	// CORS headers.
	if ep.CORSOrigins != "" {
		origin := r.Header.Get("Origin")
		if ep.CORSOrigins == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			for _, allowed := range strings.Split(ep.CORSOrigins, ",") {
				if strings.TrimSpace(allowed) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					break
				}
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	}

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Auth check.
	if ep.RequiresAuth {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !h.validateSiteTokenOrJWT(site.ID, token) {
			writePublicError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}

	// Rate limit by IP.
	clientIP := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		clientIP = strings.Split(fwd, ",")[0]
	}
	rateLimitKey := fmt.Sprintf("%d:%s:%s", site.ID, ep.Path, strings.TrimSpace(clientIP))
	if !checkLLMRateLimit(rateLimitKey, ep.RateLimit) {
		writePublicError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// Parse request body.
	var req llmChatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxLLMBodySize)).Decode(&req); err != nil {
		writePublicError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Messages) == 0 {
		writePublicError(w, http.StatusBadRequest, "messages array is required and must not be empty")
		return
	}

	// Validate and cap messages — only allow user/assistant roles, cap at max_history.
	var messages []llm.Message
	for _, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue // silently drop system or other roles
		}
		messages = append(messages, llm.Message{Role: llm.Role(m.Role), Content: m.Content})
	}
	if len(messages) > ep.MaxHistory {
		messages = messages[len(messages)-ep.MaxHistory:]
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != llm.RoleUser {
		writePublicError(w, http.StatusBadRequest, "last message must be from user")
		return
	}

	// Resolve LLM provider.
	provider, modelID, err := h.resolveLLMProvider(site.ID)
	if err != nil {
		slog.Error("LLM endpoint: failed to resolve provider", "site_id", site.ID, "error", err)
		writePublicError(w, http.StatusServiceUnavailable, "AI service not available")
		return
	}

	// Override model if endpoint specifies one.
	if ep.ModelID != "" {
		modelID = ep.ModelID
	}

	temp := ep.Temperature
	completionReq := llm.CompletionRequest{
		Model:       modelID,
		System:      ep.SystemPrompt,
		Messages:    messages,
		MaxTokens:   ep.MaxTokens,
		Temperature: &temp,
	}

	if mode == "complete" {
		// Non-streaming: return full response as JSON.
		resp, err := provider.Complete(r.Context(), completionReq)
		if err != nil {
			slog.Error("LLM endpoint: completion failed", "site_id", site.ID, "path", ep.Path, "error", err)
			writePublicError(w, http.StatusInternalServerError, "AI completion failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     resp.Content,
			"model":       resp.Model,
			"stop_reason": resp.StopReason,
			"usage": map[string]int{
				"input_tokens":  resp.Usage.InputTokens,
				"output_tokens": resp.Usage.OutputTokens,
			},
		})
		return
	}

	// Streaming: SSE response.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writePublicError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	err = provider.Stream(r.Context(), completionReq, func(chunk llm.StreamChunk) {
		if chunk.Error != nil {
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": chunk.Error.Error()}))
			flusher.Flush()
			return
		}
		if chunk.Delta != "" {
			fmt.Fprintf(w, "event: token\ndata: %s\n\n", mustJSON(map[string]string{"text": chunk.Delta}))
			flusher.Flush()
		}
		if chunk.Done {
			doneData := map[string]interface{}{}
			if chunk.StopReason != "" {
				doneData["stop_reason"] = chunk.StopReason
			}
			if chunk.Usage != nil {
				doneData["usage"] = map[string]int{
					"input_tokens":  chunk.Usage.InputTokens,
					"output_tokens": chunk.Usage.OutputTokens,
				}
			}
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", mustJSON(doneData))
			flusher.Flush()
		}
	})
	if err != nil {
		slog.Error("LLM endpoint: stream failed", "site_id", site.ID, "path", ep.Path, "error", err)
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": "stream failed"}))
		flusher.Flush()
	}
}

// resolveLLMProvider resolves the LLM provider for a site using the same pattern as chat sessions.
func (h *Handler) resolveLLMProvider(siteID int) (llm.Provider, string, error) {
	model, dbProvider, err := models.GetModelForSite(h.deps.DB.DB, siteID)
	if err != nil {
		return nil, "", fmt.Errorf("get model for site %d: %w", siteID, err)
	}

	// Try the live registry first.
	if p, err := h.deps.LLMRegistry.Get(dbProvider.Name); err == nil {
		return p, model.ModelID, nil
	}

	// Decrypt API key and create provider on the fly.
	var apiKey string
	if dbProvider.APIKeyEncrypted != nil && *dbProvider.APIKeyEncrypted != "" {
		apiKey, err = h.deps.Encryptor.Decrypt(*dbProvider.APIKeyEncrypted)
		if err != nil {
			return nil, "", fmt.Errorf("decrypt API key for %q: %w", dbProvider.Name, err)
		}
	} else if dbProvider.RequiresAPIKey() {
		return nil, "", fmt.Errorf("provider %q has no API key configured", dbProvider.Name)
	}

	var baseURL string
	if dbProvider.BaseURL != nil {
		baseURL = *dbProvider.BaseURL
	}

	provider := createLLMProvider(dbProvider.Name, dbProvider.ProviderType, apiKey, baseURL)
	if provider == nil {
		return nil, "", fmt.Errorf("unsupported provider type: %s", dbProvider.ProviderType)
	}
	h.deps.LLMRegistry.Register(dbProvider.Name, provider)
	return provider, model.ModelID, nil
}

// mustJSON marshals v to JSON string, returning "{}" on error.
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// createLLMProvider builds a live llm.Provider for the given type.
func createLLMProvider(name, providerType, apiKey, baseURL string) llm.Provider {
	switch strings.ToLower(providerType) {
	case "anthropic":
		var opts []anthropic.Option
		if baseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(baseURL))
		}
		return anthropic.New(name, apiKey, opts...)
	case "openai":
		var opts []openai.Option
		if baseURL != "" {
			opts = append(opts, openai.WithBaseURL(baseURL))
		}
		return openai.New(name, apiKey, opts...)
	default:
		return nil
	}
}
