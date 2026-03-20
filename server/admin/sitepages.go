/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"database/sql"
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
	ID        int       `json:"id"`
	SiteID    int       `json:"site_id"`
	Path      string    `json:"path"`
	Title     *string   `json:"title"`
	Content   *string   `json:"content,omitempty"`
	Template  *string   `json:"template"`
	Status    string    `json:"status"`
	Metadata  string    `json:"metadata"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// List returns all pages for a site (without content to keep response small).
func (h *SitePagesHandler) List(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, path, title, template, status, metadata, created_at, updated_at
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
		if err := rows.Scan(&p.ID, &p.Path, &p.Title, &p.Template, &p.Status, &p.Metadata, &p.CreatedAt, &p.UpdatedAt); err != nil {
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
