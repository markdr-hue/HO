/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// ServiceProvidersHandler handles service provider listing for the admin UI.
type ServiceProvidersHandler struct {
	deps *Deps
}

type serviceProvider struct {
	ID          int       `json:"id"`
	SiteID      int       `json:"site_id"`
	Name        string    `json:"name"`
	BaseURL     string    `json:"base_url"`
	AuthType    string    `json:"auth_type"`
	SecretName  *string   `json:"secret_name,omitempty"`
	Description string    `json:"description"`
	ApiDocs     string    `json:"api_docs"`
	IsEnabled   bool      `json:"is_enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

// List returns service providers for a site.
func (h *ServiceProvidersHandler) List(w http.ResponseWriter, r *http.Request) {
	siteIDStr := chi.URLParam(r, "siteID")
	if siteIDStr == "" {
		siteIDStr = r.URL.Query().Get("site_id")
	}
	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(siteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open site database")
		return
	}
	rows, err := siteDB.Query(
		`SELECT id, name, base_url, auth_type, secret_name, description, api_docs, is_enabled, created_at
		 FROM ho_service_providers ORDER BY name`)
	if err != nil {
		h.deps.Logger.Error("failed to list service providers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list service providers")
		return
	}
	defer rows.Close()

	var providers []serviceProvider
	for rows.Next() {
		var p serviceProvider
		var secretName sql.NullString
		p.SiteID = siteID
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.AuthType, &secretName, &p.Description, &p.ApiDocs, &p.IsEnabled, &p.CreatedAt); err != nil {
			continue
		}
		if secretName.Valid {
			p.SecretName = &secretName.String
		}
		providers = append(providers, p)
	}
	if providers == nil {
		providers = []serviceProvider{}
	}
	writeJSON(w, http.StatusOK, providers)
}

// Toggle flips the is_enabled flag on a service provider.
func (h *ServiceProvidersHandler) Toggle(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	provID, err := strconv.Atoi(chi.URLParam(r, "provID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}
	_, err = siteDB.ExecWrite(
		"UPDATE ho_service_providers SET is_enabled = NOT is_enabled WHERE id = ?",
		provID,
	)
	if err != nil {
		h.deps.Logger.Error("failed to toggle service provider", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to toggle provider")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"id": provID, "toggled": true})
}

// Delete removes a service provider.
func (h *ServiceProvidersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	provID, err := strconv.Atoi(chi.URLParam(r, "provID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider ID")
		return
	}
	res, err := siteDB.ExecWrite(
		"DELETE FROM ho_service_providers WHERE id = ?",
		provID,
	)
	if err != nil {
		h.deps.Logger.Error("failed to delete service provider", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete provider")
		return
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": true})
}
