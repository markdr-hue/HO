/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	hocdn "github.com/markdr-hue/HO/caddy"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
)

// SitesHandler handles site CRUD endpoints.
type SitesHandler struct {
	deps *Deps
}

type createSiteRequest struct {
	Name        string  `json:"name"`
	Domain      *string `json:"domain"`
	Description *string `json:"description"`
	LLMModelID  int     `json:"llm_model_id"`
}

type updateSiteRequest struct {
	Name        string  `json:"name"`
	Domain      *string `json:"domain"`
	Description *string `json:"description"`
	LLMModelID  int     `json:"llm_model_id"`
}

// List returns all sites, with optional pagination via ?limit=&offset= query params.
func (h *SitesHandler) List(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	// If limit is provided, use paginated query.
	if limitStr != "" {
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit <= 0 {
			limit = 50
		}
		offset := 0
		if offsetStr != "" {
			offset, _ = strconv.Atoi(offsetStr)
			if offset < 0 {
				offset = 0
			}
		}

		sites, total, err := models.ListSitesPaginated(h.deps.DB.DB, limit, offset)
		if err != nil {
			h.deps.Logger.Error("failed to list sites", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list sites")
			return
		}
		if sites == nil {
			sites = []models.Site{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items": sites,
			"total": total,
		})
		return
	}

	// Default: return all sites (used by dashboard, sidebar, etc.)
	sites, err := models.ListSites(h.deps.DB.DB)
	if err != nil {
		h.deps.Logger.Error("failed to list sites", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list sites")
		return
	}

	if sites == nil {
		sites = []models.Site{}
	}

	writeJSON(w, http.StatusOK, sites)
}

// Create creates a new site.
func (h *SitesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSiteRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.LLMModelID <= 0 {
		// No model specified — try to use the system default.
		defModel, _, defErr := models.GetDefaultModel(h.deps.DB.DB)
		if defErr != nil {
			writeError(w, http.StatusBadRequest, "no model selected and no system default configured — please select a provider and model")
			return
		}
		req.LLMModelID = defModel.ID
	} else {
		// Verify the specified model actually exists.
		if _, err := models.GetModelByID(h.deps.DB.DB, req.LLMModelID); err != nil {
			writeError(w, http.StatusBadRequest, "selected model does not exist")
			return
		}
	}

	// Normalize empty domain to NULL so the UNIQUE constraint doesn't conflict.
	if req.Domain != nil && *req.Domain == "" {
		req.Domain = nil
	}

	site, err := models.CreateSite(h.deps.DB.DB, req.Name, req.Domain, req.Description, req.LLMModelID)
	if err != nil {
		h.deps.Logger.Error("failed to create site", "error", err)
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "a site with this domain already exists")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to create site")
		}
		return
	}

	// Create the per-site database.
	if _, err := h.deps.SiteDBManager.Create(site.ID); err != nil {
		h.deps.Logger.Error("failed to create site db", "site_id", site.ID, "error", err)
		// Clean up the global row since the site DB failed.
		_ = models.DeleteSite(h.deps.DB.DB, site.ID)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create site database: %v", err))
		return
	}

	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventSiteCreated, site.ID, map[string]interface{}{
			"name": site.Name,
		}))
	}

	writeJSON(w, http.StatusCreated, site)
}

// Get returns a single site by ID.
func (h *SitesHandler) Get(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	site, err := models.GetSiteByID(h.deps.DB.DB, siteID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	writeJSON(w, http.StatusOK, site)
}

// Update updates an existing site.
func (h *SitesHandler) Update(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	var req updateSiteRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	if req.LLMModelID <= 0 {
		writeError(w, http.StatusBadRequest, "llm_model_id is required")
		return
	}

	if req.Domain != nil && *req.Domain == "" {
		req.Domain = nil
	}

	if err := models.UpdateSite(h.deps.DB.DB, siteID, req.Name, req.Domain, req.Description, req.LLMModelID); err != nil {
		h.deps.Logger.Error("failed to update site", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update site")
		return
	}

	site, err := models.GetSiteByID(h.deps.DB.DB, siteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site updated but failed to reload")
		return
	}

	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventSiteUpdated, siteID, map[string]interface{}{
			"name": site.Name,
		}))
	}

	writeJSON(w, http.StatusOK, site)
}

// Summary returns aggregated stats for the site home page.
func (h *SitesHandler) Summary(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	type summary struct {
		TotalTokens    int `json:"total_tokens"`
		BrainActions   int `json:"brain_actions"`
		PageViews      int `json:"page_views"`
		UniqueVisitors int `json:"unique_visitors"`
		PagesCount     int `json:"pages_count"`
	}

	var s summary

	// Token usage + LLM call count (per-site DB)
	_ = siteDB.QueryRow(
		`SELECT COALESCE(SUM(input_tokens + output_tokens), 0), COUNT(*)
		 FROM ho_llm_log`,
	).Scan(&s.TotalTokens, &s.BrainActions)

	// Page views + unique visitors (last 24h) (per-site DB)
	_ = siteDB.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT visitor_hash)
		 FROM ho_analytics WHERE created_at > datetime('now', '-1 day')`,
	).Scan(&s.PageViews, &s.UniqueVisitors)

	// Published pages count (per-site DB)
	_ = siteDB.QueryRow(
		`SELECT COUNT(*) FROM ho_pages WHERE is_deleted = 0`,
	).Scan(&s.PagesCount)

	writeJSON(w, http.StatusOK, s)
}

// Delete removes a site and all its data.
func (h *SitesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	// Stop the brain worker before deleting data.
	if h.deps.BrainManager != nil {
		_ = h.deps.BrainManager.StopSite(siteID) // ignore error if not running
	}

	// Delete via serialized writer to ensure the row is actually removed
	// before we clean up files. Using raw Exec can race with other writes.
	res, err := h.deps.DB.ExecWrite("DELETE FROM sites WHERE id = ?", siteID)
	if err != nil {
		h.deps.Logger.Error("failed to delete site", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete site")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Remove the per-site database.
	if err := h.deps.SiteDBManager.Delete(siteID); err != nil {
		h.deps.Logger.Error("failed to delete site db", "site_id", siteID, "error", err)
		// Site row already deleted; log but don't fail the request.
	}

	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventSiteDeleted, siteID, nil))
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ToggleStatus flips a site between active and inactive.
func (h *SitesHandler) ToggleStatus(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	site, err := models.GetSiteByID(h.deps.DB.DB, siteID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	newStatus := "active"
	if site.Status == "active" {
		newStatus = "inactive"
	}

	if err := models.UpdateSiteStatus(h.deps.DB.DB, siteID, newStatus); err != nil {
		h.deps.Logger.Error("failed to toggle site status", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update site status")
		return
	}

	// Reload to get updated_at etc.
	site, _ = models.GetSiteByID(h.deps.DB.DB, siteID)

	// Publish so Caddy reloads (inactive sites disappear from config).
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventSiteUpdated, siteID, map[string]interface{}{
			"name":   site.Name,
			"status": newStatus,
		}))
	}

	writeJSON(w, http.StatusOK, site)
}

// ValidateDomain checks if a domain is properly configured for HTTPS.
// Returns the validation result with any warnings or errors.
func (h *SitesHandler) ValidateDomain(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, http.StatusBadRequest, "domain query parameter is required")
		return
	}

	check := hocdn.ValidateDomain(domain)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"domain":   check.Domain,
		"valid":    check.Valid,
		"warnings": check.Warnings,
		"error":    check.Error,
		"message":  check.FormatCheckResult(),
	})
}
