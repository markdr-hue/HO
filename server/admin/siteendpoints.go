/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// SiteEndpointsHandler handles API endpoint browsing for site detail views.
type SiteEndpointsHandler struct {
	deps *Deps
}

type siteEndpoint struct {
	ID            int       `json:"id"`
	SiteID        int       `json:"site_id"`
	Path          string    `json:"path"`
	TableName     string    `json:"table_name"`
	Methods       string    `json:"methods"`
	PublicColumns *string   `json:"public_columns"`
	RequiresAuth  bool      `json:"requires_auth"`
	PublicRead    bool      `json:"public_read"`
	RequiredRole  *string   `json:"required_role"`
	RateLimit     int       `json:"rate_limit"`
	CreatedAt     time.Time `json:"created_at"`
}

type updateEndpointRequest struct {
	Methods      []string `json:"methods"`
	RequiresAuth *bool    `json:"requires_auth"`
	PublicRead   *bool    `json:"public_read"`
	RequiredRole *string  `json:"required_role"`
	RateLimit    *int     `json:"rate_limit"`
}

// List returns all API endpoints for a site.
func (h *SiteEndpointsHandler) List(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, path, table_name, methods, public_columns, requires_auth, public_read, required_role, rate_limit, created_at
		 FROM ho_api_endpoints ORDER BY path ASC`,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []siteEndpoint{})
		return
	}
	defer rows.Close()

	var endpoints []siteEndpoint
	for rows.Next() {
		var e siteEndpoint
		e.SiteID = siteID
		var publicCols, reqRole sql.NullString
		if err := rows.Scan(&e.ID, &e.Path, &e.TableName, &e.Methods, &publicCols, &e.RequiresAuth, &e.PublicRead, &reqRole, &e.RateLimit, &e.CreatedAt); err != nil {
			continue
		}
		if publicCols.Valid {
			e.PublicColumns = &publicCols.String
		}
		if reqRole.Valid {
			e.RequiredRole = &reqRole.String
		}
		endpoints = append(endpoints, e)
	}

	// Also include LLM endpoints so they appear in the admin panel.
	llmRows, err := siteDB.Query(
		`SELECT id, path, system_prompt, requires_auth, rate_limit, created_at
		 FROM ho_llm_endpoints ORDER BY path ASC`,
	)
	if err == nil {
		defer llmRows.Close()
		for llmRows.Next() {
			var e siteEndpoint
			e.SiteID = siteID
			var systemPrompt string
			if err := llmRows.Scan(&e.ID, &e.Path, &systemPrompt, &e.RequiresAuth, &e.RateLimit, &e.CreatedAt); err != nil {
				continue
			}
			e.Methods = `["POST"]`
			e.TableName = "LLM"
			e.PublicRead = !e.RequiresAuth
			// Offset IDs to avoid collision with API endpoint IDs.
			e.ID = e.ID + 100000
			endpoints = append(endpoints, e)
		}
	}

	if endpoints == nil {
		endpoints = []siteEndpoint{}
	}

	writeJSON(w, http.StatusOK, endpoints)
}

// Delete removes an API endpoint by ID.
func (h *SiteEndpointsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	endpointID, err := strconv.Atoi(chi.URLParam(r, "endpointID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	_, err = siteDB.ExecWrite(
		"DELETE FROM ho_api_endpoints WHERE id = ?",
		endpointID,
	)
	if err != nil {
		h.deps.Logger.Error("failed to delete endpoint", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete endpoint")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Get returns a single API endpoint by ID.
func (h *SiteEndpointsHandler) Get(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	endpointID, err := strconv.Atoi(chi.URLParam(r, "endpointID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	var e siteEndpoint
	e.SiteID = siteID
	var publicCols, reqRole sql.NullString
	err = siteDB.QueryRow(
		`SELECT id, path, table_name, methods, public_columns, requires_auth, public_read, required_role, rate_limit, created_at
		 FROM ho_api_endpoints WHERE id = ?`, endpointID,
	).Scan(&e.ID, &e.Path, &e.TableName, &e.Methods, &publicCols, &e.RequiresAuth, &e.PublicRead, &reqRole, &e.RateLimit, &e.CreatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	if publicCols.Valid {
		e.PublicColumns = &publicCols.String
	}
	if reqRole.Valid {
		e.RequiredRole = &reqRole.String
	}

	writeJSON(w, http.StatusOK, e)
}

// Update modifies an existing API endpoint.
func (h *SiteEndpointsHandler) Update(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	endpointID, err := strconv.Atoi(chi.URLParam(r, "endpointID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	var req updateEndpointRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	var setClauses []string
	var values []interface{}

	if len(req.Methods) > 0 {
		// Validate methods.
		valid := map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true}
		for _, m := range req.Methods {
			if !valid[strings.ToUpper(m)] {
				writeError(w, http.StatusBadRequest, "invalid method: "+m)
				return
			}
		}
		methodsJSON, _ := json.Marshal(req.Methods)
		setClauses = append(setClauses, "methods = ?")
		values = append(values, string(methodsJSON))
	}
	if req.RequiresAuth != nil {
		setClauses = append(setClauses, "requires_auth = ?")
		values = append(values, *req.RequiresAuth)
	}
	if req.PublicRead != nil {
		setClauses = append(setClauses, "public_read = ?")
		values = append(values, *req.PublicRead)
	}
	if req.RequiredRole != nil {
		setClauses = append(setClauses, "required_role = ?")
		if *req.RequiredRole == "" {
			values = append(values, nil)
		} else {
			values = append(values, *req.RequiredRole)
		}
	}
	if req.RateLimit != nil {
		setClauses = append(setClauses, "rate_limit = ?")
		values = append(values, *req.RateLimit)
	}

	if len(setClauses) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	values = append(values, endpointID)
	query := "UPDATE ho_api_endpoints SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"

	res, err := siteDB.ExecWrite(query, values...)
	if err != nil {
		h.deps.Logger.Error("failed to update endpoint", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update endpoint")
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
