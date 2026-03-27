/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// SiteLayoutsHandler handles layout listing/viewing/deletion for admin.
type SiteLayoutsHandler struct {
	deps *Deps
}

type layoutEntry struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	HeadContent  string    `json:"head_content"`
	Template     string    `json:"template"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	VersionCount int       `json:"version_count"`
}

// List returns all layouts for a site.
func (h *SiteLayoutsHandler) List(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	rows, err := siteDB.Query(
		`SELECT id, name, head_content, COALESCE(template, '') AS template, created_at, updated_at,
		        (SELECT COUNT(*) FROM ho_layout_versions WHERE layout_id = ho_layouts.id) AS version_count
		 FROM ho_layouts ORDER BY name`,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []layoutEntry{})
		return
	}
	defer rows.Close()

	var layouts []layoutEntry
	for rows.Next() {
		var l layoutEntry
		if err := rows.Scan(&l.ID, &l.Name, &l.HeadContent, &l.Template, &l.CreatedAt, &l.UpdatedAt, &l.VersionCount); err != nil {
			continue
		}
		layouts = append(layouts, l)
	}

	if layouts == nil {
		layouts = []layoutEntry{}
	}

	writeJSON(w, http.StatusOK, layouts)
}

// Get returns a single layout by ID.
func (h *SiteLayoutsHandler) Get(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	layoutID, err := strconv.Atoi(chi.URLParam(r, "layoutID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid layout ID")
		return
	}

	var l layoutEntry
	err = siteDB.QueryRow(
		"SELECT id, name, head_content, COALESCE(template, '') AS template, created_at, updated_at FROM ho_layouts WHERE id = ?",
		layoutID,
	).Scan(&l.ID, &l.Name, &l.HeadContent, &l.Template, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "layout not found")
		return
	}

	writeJSON(w, http.StatusOK, l)
}

// Delete removes a layout by ID, unless pages reference it.
func (h *SiteLayoutsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	layoutID, err := strconv.Atoi(chi.URLParam(r, "layoutID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid layout ID")
		return
	}

	// Get layout name first
	var name string
	err = siteDB.QueryRow("SELECT name FROM ho_layouts WHERE id = ?", layoutID).Scan(&name)
	if err != nil {
		writeError(w, http.StatusNotFound, "layout not found")
		return
	}

	// Check if pages reference this layout
	var pageCount int
	_ = siteDB.QueryRow(
		"SELECT COUNT(*) FROM ho_pages WHERE layout = ? AND is_deleted = 0",
		name,
	).Scan(&pageCount)
	if pageCount > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("cannot delete: %d page(s) use this layout", pageCount))
		return
	}

	res, err := siteDB.ExecWrite("DELETE FROM ho_layouts WHERE id = ?", layoutID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete layout")
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "layout not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": layoutID})
}

type layoutVersion struct {
	VersionNumber int       `json:"version_number"`
	ChangedBy     string    `json:"changed_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListVersions returns version history for a layout.
func (h *SiteLayoutsHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	layoutID, err := strconv.Atoi(chi.URLParam(r, "layoutID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid layout ID")
		return
	}

	rows, err := siteDB.Query(
		"SELECT version_number, changed_by, created_at FROM ho_layout_versions WHERE layout_id = ? ORDER BY version_number DESC LIMIT 20",
		layoutID,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []layoutVersion{})
		return
	}
	defer rows.Close()

	var versions []layoutVersion
	for rows.Next() {
		var v layoutVersion
		if err := rows.Scan(&v.VersionNumber, &v.ChangedBy, &v.CreatedAt); err != nil {
			continue
		}
		versions = append(versions, v)
	}
	if versions == nil {
		versions = []layoutVersion{}
	}
	writeJSON(w, http.StatusOK, versions)
}

// Revert restores a layout to a previous version. Saves current state as a new version first.
func (h *SiteLayoutsHandler) Revert(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	layoutID, err := strconv.Atoi(chi.URLParam(r, "layoutID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid layout ID")
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid version number")
		return
	}

	db := siteDB.Writer()

	// Load current layout state.
	var name, headContent, template string
	err = db.QueryRow("SELECT name, head_content, COALESCE(template, '') FROM ho_layouts WHERE id = ?", layoutID).Scan(&name, &headContent, &template)
	if err != nil {
		writeError(w, http.StatusNotFound, "layout not found")
		return
	}

	// Load the target version.
	var verHead, verTemplate string
	err = db.QueryRow(
		"SELECT head_content, template FROM ho_layout_versions WHERE layout_id = ? AND version_number = ?",
		layoutID, version,
	).Scan(&verHead, &verTemplate)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("version %d not found", version))
		return
	}

	// Save current state as new version (so revert is reversible).
	var maxVer int
	db.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_layout_versions WHERE layout_id = ?", layoutID).Scan(&maxVer)
	db.Exec(
		`INSERT INTO ho_layout_versions (layout_id, name, head_content, template, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, 'admin_revert')`,
		layoutID, name, headContent, template, maxVer+1,
	)

	// Restore the old version.
	_, err = db.Exec(
		"UPDATE ho_layouts SET head_content = ?, template = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		verHead, verTemplate, layoutID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revert layout")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"reverted": layoutID, "to_version": version})
}

type updateLayoutRequest struct {
	HeadContent *string `json:"head_content"`
	Template    *string `json:"template"`
}

// Update modifies a layout's head_content and/or template.
func (h *SiteLayoutsHandler) Update(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	layoutID, err := strconv.Atoi(chi.URLParam(r, "layoutID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid layout ID")
		return
	}

	var req updateLayoutRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.HeadContent == nil && req.Template == nil {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	db := siteDB.Writer()

	// Save version before updating.
	var name, headContent, template string
	err = db.QueryRow("SELECT name, head_content, COALESCE(template, '') FROM ho_layouts WHERE id = ?", layoutID).Scan(&name, &headContent, &template)
	if err != nil {
		writeError(w, http.StatusNotFound, "layout not found")
		return
	}

	var maxVer int
	db.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_layout_versions WHERE layout_id = ?", layoutID).Scan(&maxVer)
	db.Exec(
		`INSERT INTO ho_layout_versions (layout_id, name, head_content, template, version_number, changed_by)
		 VALUES (?, ?, ?, ?, ?, 'admin')`,
		layoutID, name, headContent, template, maxVer+1,
	)

	// Apply updates.
	var setClauses []string
	var values []interface{}
	if req.HeadContent != nil {
		setClauses = append(setClauses, "head_content = ?")
		values = append(values, *req.HeadContent)
	}
	if req.Template != nil {
		setClauses = append(setClauses, "template = ?")
		values = append(values, *req.Template)
	}
	setClauses = append(setClauses, "updated_at = CURRENT_TIMESTAMP")
	values = append(values, layoutID)

	query := "UPDATE ho_layouts SET " + setClauses[0]
	for _, c := range setClauses[1:] {
		query += ", " + c
	}
	query += " WHERE id = ?"

	_, err = db.Exec(query, values...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update layout")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"updated": layoutID})
}
