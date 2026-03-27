/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// SEOTool — manage_seo
// ---------------------------------------------------------------------------

// SEOTool provides SEO meta tag management, sitemap generation, robots.txt,
// and validation for published pages.
type SEOTool struct{}

func (t *SEOTool) Name() string { return "manage_seo" }
func (t *SEOTool) Description() string {
	return "Manage SEO meta tags, sitemap, robots.txt, and validation."
}

func (t *SEOTool) Guide() string {
	return `### SEO (manage_seo)
- **set_meta**: Set meta tags for a page. Params: page_path, title, description, og_title, og_description, og_image, canonical_url, robots ("noindex,nofollow"), structured_data (JSON-LD string).
- **get_meta**: Get stored meta tags for a page.
- **generate_sitemap**: Auto-generate sitemap.xml from all published pages. Returns the URL.
- **set_robots**: Write custom robots.txt content. Param: content.
- **validate**: Check all pages for missing SEO essentials (title, description, og tags). Returns issues list.`
}

func (t *SEOTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"set_meta", "get_meta", "generate_sitemap", "set_robots", "validate"},
				"description": "Action to perform",
			},
			"page_path": map[string]interface{}{
				"type":        "string",
				"description": "Page path (e.g., '/' or '/about'). For set_meta, get_meta.",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Page title for <title> and og:title fallback. For set_meta.",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Meta description. For set_meta.",
			},
			"og_title": map[string]interface{}{
				"type":        "string",
				"description": "Open Graph title (defaults to title if empty). For set_meta.",
			},
			"og_description": map[string]interface{}{
				"type":        "string",
				"description": "Open Graph description (defaults to description if empty). For set_meta.",
			},
			"og_image": map[string]interface{}{
				"type":        "string",
				"description": "Open Graph image URL. For set_meta.",
			},
			"canonical_url": map[string]interface{}{
				"type":        "string",
				"description": "Canonical URL for the page. For set_meta.",
			},
			"robots": map[string]interface{}{
				"type":        "string",
				"description": "Robots directive (e.g., 'noindex,nofollow'). For set_meta.",
			},
			"structured_data": map[string]interface{}{
				"type":        "string",
				"description": "JSON-LD structured data string. For set_meta.",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "robots.txt content. For set_robots.",
			},
		},
		"required": []string{},
	}
}

func (t *SEOTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"set_meta":         t.setMeta,
		"get_meta":         t.getMeta,
		"generate_sitemap": t.generateSitemap,
		"set_robots":       t.setRobots,
		"validate":         t.validate,
	}, nil)
}

func (t *SEOTool) ensureTables(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS ho_seo_meta (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		page_path TEXT UNIQUE NOT NULL,
		title TEXT DEFAULT '',
		description TEXT DEFAULT '',
		og_title TEXT DEFAULT '',
		og_description TEXT DEFAULT '',
		og_image TEXT DEFAULT '',
		canonical_url TEXT DEFAULT '',
		robots TEXT DEFAULT '',
		structured_data TEXT DEFAULT '',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
}

func (t *SEOTool) setMeta(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	pagePath, errResult := RequireString(args, "page_path")
	if errResult != nil {
		return errResult, nil
	}

	t.ensureTables(ctx.DB)

	title := OptionalString(args, "title", "")
	desc := OptionalString(args, "description", "")
	ogTitle := OptionalString(args, "og_title", "")
	ogDesc := OptionalString(args, "og_description", "")
	ogImage := OptionalString(args, "og_image", "")
	canonical := OptionalString(args, "canonical_url", "")
	robots := OptionalString(args, "robots", "")
	structured := OptionalString(args, "structured_data", "")

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_seo_meta (page_path, title, description, og_title, og_description, og_image, canonical_url, robots, structured_data, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(page_path) DO UPDATE SET
		   title = excluded.title,
		   description = excluded.description,
		   og_title = excluded.og_title,
		   og_description = excluded.og_description,
		   og_image = excluded.og_image,
		   canonical_url = excluded.canonical_url,
		   robots = excluded.robots,
		   structured_data = excluded.structured_data,
		   updated_at = CURRENT_TIMESTAMP`,
		pagePath, title, desc, ogTitle, ogDesc, ogImage, canonical, robots, structured,
	)
	if err != nil {
		return nil, fmt.Errorf("setting meta: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"page_path": pagePath,
		"title":     title,
	}}, nil
}

func (t *SEOTool) getMeta(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	pagePath, errResult := RequireString(args, "page_path")
	if errResult != nil {
		return errResult, nil
	}

	t.ensureTables(ctx.DB)

	var title, desc, ogTitle, ogDesc, ogImage, canonical, robots, structured string
	err := ctx.DB.QueryRow(
		`SELECT title, description, og_title, og_description, og_image, canonical_url, robots, structured_data
		 FROM ho_seo_meta WHERE page_path = ?`, pagePath,
	).Scan(&title, &desc, &ogTitle, &ogDesc, &ogImage, &canonical, &robots, &structured)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("no meta found for page '%s'", pagePath)}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"page_path":       pagePath,
		"title":           title,
		"description":     desc,
		"og_title":        ogTitle,
		"og_description":  ogDesc,
		"og_image":        ogImage,
		"canonical_url":   canonical,
		"robots":          robots,
		"structured_data": structured,
	}}, nil
}

func (t *SEOTool) generateSitemap(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT path FROM ho_pages WHERE is_deleted = 0 ORDER BY path",
	)
	if err != nil {
		return nil, fmt.Errorf("querying pages: %w", err)
	}
	defer rows.Close()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")

	count := 0
	today := time.Now().Format("2006-01-02")
	for rows.Next() {
		var path string
		if rows.Scan(&path) != nil {
			continue
		}
		b.WriteString("  <url>\n")
		b.WriteString(fmt.Sprintf("    <loc>%s</loc>\n", path))
		b.WriteString(fmt.Sprintf("    <lastmod>%s</lastmod>\n", today))
		if path == "/" {
			b.WriteString("    <priority>1.0</priority>\n")
		} else {
			b.WriteString("    <priority>0.8</priority>\n")
		}
		b.WriteString("  </url>\n")
		count++
	}

	b.WriteString("</urlset>\n")

	// Save to disk.
	dir, _ := storageDir(ctx.SiteID, "ho_files")
	os.MkdirAll(dir, 0755)
	sitemapPath := filepath.Join(dir, "sitemap.xml")
	if err := os.WriteFile(sitemapPath, []byte(b.String()), 0644); err != nil {
		return nil, fmt.Errorf("writing sitemap: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"pages": count,
		"url":   "/files/sitemap.xml",
	}}, nil
}

func (t *SEOTool) setRobots(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	content, errResult := RequireString(args, "content")
	if errResult != nil {
		return errResult, nil
	}

	dir, _ := storageDir(ctx.SiteID, "ho_files")
	os.MkdirAll(dir, 0755)
	robotsPath := filepath.Join(dir, "robots.txt")
	if err := os.WriteFile(robotsPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("writing robots.txt: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"url": "/files/robots.txt",
	}}, nil
}

func (t *SEOTool) validate(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	t.ensureTables(ctx.DB)

	rows, err := ctx.DB.Query(
		"SELECT path, title FROM ho_pages WHERE is_deleted = 0 ORDER BY path",
	)
	if err != nil {
		return nil, fmt.Errorf("querying pages: %w", err)
	}
	defer rows.Close()

	var issues []map[string]interface{}
	for rows.Next() {
		var path, pageTitle string
		if rows.Scan(&path, &pageTitle) != nil {
			continue
		}

		var pageIssues []string

		// Check SEO meta.
		var title, desc, ogTitle, ogDesc string
		err := ctx.DB.QueryRow(
			"SELECT title, description, og_title, og_description FROM ho_seo_meta WHERE page_path = ?",
			path,
		).Scan(&title, &desc, &ogTitle, &ogDesc)
		if err != nil {
			pageIssues = append(pageIssues, "no SEO meta set")
		} else {
			if title == "" && pageTitle == "" {
				pageIssues = append(pageIssues, "missing title")
			}
			if desc == "" {
				pageIssues = append(pageIssues, "missing description")
			}
			if ogTitle == "" && title == "" {
				pageIssues = append(pageIssues, "missing og:title")
			}
			if ogDesc == "" && desc == "" {
				pageIssues = append(pageIssues, "missing og:description")
			}
		}

		if len(pageIssues) > 0 {
			issues = append(issues, map[string]interface{}{
				"page":   path,
				"issues": pageIssues,
			})
		}
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"total_issues": len(issues),
		"pages":        issues,
	}}, nil
}
