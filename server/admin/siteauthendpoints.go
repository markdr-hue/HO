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

// SiteAuthEndpointsHandler handles auth endpoint listing/deletion for admin.
type SiteAuthEndpointsHandler struct {
	deps *Deps
}

type authEndpoint struct {
	ID             int       `json:"id"`
	SiteID         int       `json:"site_id"`
	Path           string    `json:"path"`
	TableName      string    `json:"table_name"`
	UsernameColumn string    `json:"username_column"`
	PasswordColumn string    `json:"password_column"`
	PublicColumns  string    `json:"public_columns"`
	CreatedAt      time.Time `json:"created_at"`
	Routes         []string  `json:"routes"`
}

// List returns all auth endpoints for a site.
func (h *SiteAuthEndpointsHandler) List(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	rows, err := siteDB.Query(
		"SELECT id, path, table_name, username_column, password_column, public_columns, created_at FROM ho_auth_endpoints ORDER BY path",
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []authEndpoint{})
		return
	}
	defer rows.Close()

	var endpoints []authEndpoint
	for rows.Next() {
		var e authEndpoint
		e.SiteID = siteID
		if err := rows.Scan(&e.ID, &e.Path, &e.TableName, &e.UsernameColumn, &e.PasswordColumn, &e.PublicColumns, &e.CreatedAt); err != nil {
			continue
		}
		e.Routes = []string{
			fmt.Sprintf("POST /api/%s/register", e.Path),
			fmt.Sprintf("POST /api/%s/login", e.Path),
			fmt.Sprintf("GET /api/%s/me", e.Path),
		}
		endpoints = append(endpoints, e)
	}

	if endpoints == nil {
		endpoints = []authEndpoint{}
	}

	writeJSON(w, http.StatusOK, endpoints)
}

// Delete removes an auth endpoint by ID.
func (h *SiteAuthEndpointsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	endpointID, err := strconv.Atoi(chi.URLParam(r, "endpointID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}
	res, err := siteDB.ExecWrite(
		"DELETE FROM ho_auth_endpoints WHERE id = ?",
		endpointID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete auth endpoint")
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "auth endpoint not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": endpointID})
}
