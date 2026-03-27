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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
)

// TemplatesHandler manages site templates (save, list, clone).
type TemplatesHandler struct {
	deps *Deps
}

type templateResponse struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	CreatedBy   *int      `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// List returns all available templates.
func (h *TemplatesHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.deps.DB.DB.Query("SELECT id, name, description, category, created_by, created_at FROM site_templates ORDER BY created_at DESC")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list templates")
		return
	}
	defer rows.Close()

	var templates []templateResponse
	for rows.Next() {
		var t templateResponse
		var createdBy *int
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Category, &createdBy, &t.CreatedAt); err != nil {
			continue
		}
		t.CreatedBy = createdBy
		templates = append(templates, t)
	}
	writeJSON(w, http.StatusOK, templates)
}

// Get returns a single template with its plan JSON.
func (h *TemplatesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "templateID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template ID")
		return
	}

	var name, description, planJSON, category string
	var createdBy *int
	var createdAt time.Time
	err = h.deps.DB.DB.QueryRow(
		"SELECT name, description, plan_json, category, created_by, created_at FROM site_templates WHERE id = ?", id,
	).Scan(&name, &description, &planJSON, &category, &createdBy, &createdAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":          id,
		"name":        name,
		"description": description,
		"plan_json":   planJSON,
		"category":    category,
		"created_by":  createdBy,
		"created_at":  createdAt,
	})
}

type saveTemplateRequest struct {
	SiteID      int    `json:"site_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

// Save creates a template from an existing site's plan.
func (h *TemplatesHandler) Save(w http.ResponseWriter, r *http.Request) {
	var req saveTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.SiteID <= 0 {
		writeError(w, http.StatusBadRequest, "site_id is required")
		return
	}
	if req.Category == "" {
		req.Category = "general"
	}

	// Get the site's plan JSON.
	siteDB := h.deps.SiteDBManager.Get(req.SiteID)
	if siteDB == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	var planJSON string
	err := siteDB.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSON)
	if err != nil || planJSON == "" {
		writeError(w, http.StatusBadRequest, "site has no plan — build must complete first")
		return
	}

	// Get current user ID from JWT context.
	var createdBy *int
	if userID, ok := r.Context().Value("user_id").(int); ok {
		createdBy = &userID
	}

	result, err := h.deps.DB.ExecWrite(
		"INSERT INTO site_templates (name, description, plan_json, category, created_by) VALUES (?, ?, ?, ?, ?)",
		req.Name, req.Description, planJSON, req.Category, createdBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "a template with this name already exists")
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save template: %v", err))
		}
		return
	}

	id, _ := result.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":   id,
		"name": req.Name,
	})
}

// Delete removes a template.
func (h *TemplatesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "templateID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template ID")
		return
	}

	result, err := h.deps.DB.ExecWrite("DELETE FROM site_templates WHERE id = ?", id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete template")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": id})
}

type cloneRequest struct {
	TemplateID  int     `json:"template_id"`
	Name        string  `json:"name"`
	Domain      *string `json:"domain,omitempty"`
	Description *string `json:"description,omitempty"`
	LLMModelID  int     `json:"llm_model_id"`
}

// Clone creates a new site from a template, pre-populating the plan and
// starting the pipeline at BUILD stage (skipping PLAN).
func (h *TemplatesHandler) Clone(w http.ResponseWriter, r *http.Request) {
	var req cloneRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.TemplateID <= 0 {
		writeError(w, http.StatusBadRequest, "template_id is required")
		return
	}

	// Load template plan.
	var planJSON string
	err := h.deps.DB.DB.QueryRow("SELECT plan_json FROM site_templates WHERE id = ?", req.TemplateID).Scan(&planJSON)
	if err != nil {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	// Resolve LLM model.
	if req.LLMModelID <= 0 {
		defModel, _, defErr := models.GetDefaultModel(h.deps.DB.DB)
		if defErr != nil {
			writeError(w, http.StatusBadRequest, "no model selected and no system default configured")
			return
		}
		req.LLMModelID = defModel.ID
	}

	// Normalize empty domain.
	if req.Domain != nil && *req.Domain == "" {
		req.Domain = nil
	}

	// Create the site.
	site, err := models.CreateSite(h.deps.DB.DB, req.Name, req.Domain, req.Description, req.LLMModelID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "a site with this domain already exists")
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create site: %v", err))
		}
		return
	}

	// Create the per-site database.
	siteDB, err := h.deps.SiteDBManager.Create(site.ID)
	if err != nil {
		_ = models.DeleteSite(h.deps.DB.DB, site.ID)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create site database: %v", err))
		return
	}

	// Pre-populate pipeline state with the template's plan and set stage to BUILD.
	_, err = siteDB.ExecWrite(
		"UPDATE ho_pipeline_state SET stage = 'BUILD', plan_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
		planJSON,
	)
	if err != nil {
		_ = models.DeleteSite(h.deps.DB.DB, site.ID)
		writeError(w, http.StatusInternalServerError, "failed to initialize pipeline state")
		return
	}

	// Publish creation event and start the brain.
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventSiteCreated, site.ID, map[string]interface{}{
			"name":        site.Name,
			"template_id": req.TemplateID,
		}))
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"site":        site,
		"template_id": req.TemplateID,
		"stage":       "BUILD",
		"message":     "Site created from template — build will start at BUILD stage (PLAN skipped)",
	})
}
