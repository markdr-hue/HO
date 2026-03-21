/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// SitePagesHandler handles page listing for site detail views.
type SitePagesHandler struct {
	deps *Deps
}

type sitePage struct {
	ID           int       `json:"id"`
	SiteID       int       `json:"site_id"`
	Path         string    `json:"path"`
	Title        *string   `json:"title"`
	Content      *string   `json:"content,omitempty"`
	Template     *string   `json:"template"`
	Status       string    `json:"status"`
	Metadata     string    `json:"metadata"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	VersionCount int       `json:"version_count"`
}

// List returns all pages for a site (without content to keep response small).
func (h *SitePagesHandler) List(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, path, title, template, status, metadata, created_at, updated_at,
		        (SELECT COUNT(*) FROM ho_page_versions WHERE page_id = ho_pages.id) AS version_count
		 FROM ho_pages WHERE is_deleted = 0 ORDER BY path ASC`,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []sitePage{})
		return
	}
	defer rows.Close()

	var pages []sitePage
	for rows.Next() {
		var p sitePage
		if err := rows.Scan(&p.ID, &p.Path, &p.Title, &p.Template, &p.Status, &p.Metadata, &p.CreatedAt, &p.UpdatedAt, &p.VersionCount); err != nil {
			continue
		}
		p.SiteID = siteID
		pages = append(pages, p)
	}

	if pages == nil {
		pages = []sitePage{}
	}

	writeJSON(w, http.StatusOK, pages)
}

// Get returns a single page with its content.
func (h *SitePagesHandler) Get(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	pageID, err := strconv.Atoi(chi.URLParam(r, "pageID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page ID")
		return
	}

	var p sitePage
	err = siteDB.QueryRow(
		`SELECT id, path, title, content, template, status, metadata, created_at, updated_at
		 FROM ho_pages WHERE id = ? AND is_deleted = 0`,
		pageID,
	).Scan(&p.ID, &p.Path, &p.Title, &p.Content, &p.Template, &p.Status, &p.Metadata, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load page")
		return
	}

	p.SiteID = siteID
	writeJSON(w, http.StatusOK, p)
}

type updatePageRequest struct {
	Title   *string `json:"title"`
	Content *string `json:"content"`
	Status  *string `json:"status"`
}

// Update modifies a page's content, title, or status.
func (h *SitePagesHandler) Update(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	pageID, err := strconv.Atoi(chi.URLParam(r, "pageID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page ID")
		return
	}

	var req updatePageRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	var setClauses []string
	var values []interface{}

	if req.Title != nil {
		setClauses = append(setClauses, "title = ?")
		values = append(values, *req.Title)
	}
	if req.Content != nil {
		setClauses = append(setClauses, "content = ?")
		values = append(values, *req.Content)
	}
	if req.Status != nil {
		setClauses = append(setClauses, "status = ?")
		values = append(values, *req.Status)
	}

	if len(setClauses) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	setClauses = append(setClauses, "updated_at = CURRENT_TIMESTAMP")
	values = append(values, pageID)

	query := "UPDATE ho_pages SET " + strings.Join(setClauses, ", ") + " WHERE id = ? AND is_deleted = 0"
	res, err := siteDB.ExecWrite(query, values...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update page")
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"updated": pageID})
}

// Delete soft-deletes a page.
func (h *SitePagesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	pageID, err := strconv.Atoi(chi.URLParam(r, "pageID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page ID")
		return
	}

	res, err := siteDB.ExecWrite(
		"UPDATE ho_pages SET is_deleted = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND is_deleted = 0",
		pageID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete page")
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": pageID})
}

type pageVersion struct {
	VersionNumber int       `json:"version_number"`
	Title         *string   `json:"title"`
	Status        *string   `json:"status"`
	ChangedBy     string    `json:"changed_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListVersions returns version history for a page.
func (h *SitePagesHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	pageID, err := strconv.Atoi(chi.URLParam(r, "pageID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page ID")
		return
	}

	rows, err := siteDB.Query(
		"SELECT version_number, title, status, changed_by, created_at FROM ho_page_versions WHERE page_id = ? ORDER BY version_number DESC LIMIT 20",
		pageID,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []pageVersion{})
		return
	}
	defer rows.Close()

	var versions []pageVersion
	for rows.Next() {
		var v pageVersion
		if err := rows.Scan(&v.VersionNumber, &v.Title, &v.Status, &v.ChangedBy, &v.CreatedAt); err != nil {
			continue
		}
		versions = append(versions, v)
	}
	if versions == nil {
		versions = []pageVersion{}
	}
	writeJSON(w, http.StatusOK, versions)
}

// Revert restores a page to a previous version. Saves current state as a new version first.
func (h *SitePagesHandler) Revert(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	pageID, err := strconv.Atoi(chi.URLParam(r, "pageID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page ID")
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid version number")
		return
	}

	db := siteDB.Writer()

	// Load current page state.
	var path string
	var oldTitle, oldContent, oldTemplate, oldStatus, oldMeta sql.NullString
	err = db.QueryRow(
		"SELECT path, title, content, template, status, metadata FROM ho_pages WHERE id = ? AND is_deleted = 0",
		pageID,
	).Scan(&path, &oldTitle, &oldContent, &oldTemplate, &oldStatus, &oldMeta)
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	// Load the target version.
	var verTitle, verContent, verTemplate, verStatus, verMeta sql.NullString
	err = db.QueryRow(
		"SELECT title, content, template, status, metadata FROM ho_page_versions WHERE page_id = ? AND version_number = ?",
		pageID, version,
	).Scan(&verTitle, &verContent, &verTemplate, &verStatus, &verMeta)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("version %d not found", version))
		return
	}

	// Save current state as new version (so revert is reversible).
	var maxVer int
	db.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_page_versions WHERE page_id = ?", pageID).Scan(&maxVer)
	db.Exec(
		`INSERT INTO ho_page_versions (page_id, path, title, content, template, status, metadata, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'admin_revert')`,
		pageID, path, oldTitle, oldContent, oldTemplate, oldStatus, oldMeta, maxVer+1,
	)

	// Restore the old version.
	_, err = db.Exec(
		"UPDATE ho_pages SET title = ?, content = ?, template = ?, status = ?, metadata = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		verTitle, verContent, verTemplate, verStatus, verMeta, pageID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revert page")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"reverted": pageID, "to_version": version})
}
