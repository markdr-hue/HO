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
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	HeadContent string    `json:"head_content"`
	Template    string    `json:"template"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// List returns all layouts for a site.
func (h *SiteLayoutsHandler) List(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	rows, err := siteDB.Query(
		"SELECT id, name, head_content, COALESCE(template, '') AS template, created_at, updated_at FROM ho_layouts ORDER BY name",
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []layoutEntry{})
		return
	}
	defer rows.Close()

	var layouts []layoutEntry
	for rows.Next() {
		var l layoutEntry
		if err := rows.Scan(&l.ID, &l.Name, &l.HeadContent, &l.Template, &l.CreatedAt, &l.UpdatedAt); err != nil {
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
