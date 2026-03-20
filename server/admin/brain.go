/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/brain"
	"github.com/markdr-hue/HO/db/models"
)

// BrainHandler handles brain control endpoints.
type BrainHandler struct {
	deps *Deps
}

// Start starts the brain worker for a site.
func (h *BrainHandler) Start(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	if err := h.deps.BrainManager.StartSite(siteID); err != nil {
		h.deps.Logger.Error("failed to start brain", "site_id", siteID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start brain")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"site_id": siteID,
		"status":  "started",
	})
}

// Stop stops the brain worker for a site.
func (h *BrainHandler) Stop(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	if err := h.deps.BrainManager.StopSite(siteID); err != nil {
		h.deps.Logger.Error("failed to stop brain", "site_id", siteID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to stop brain")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"site_id": siteID,
		"status":  "stopped",
	})
}

// Status returns the brain state for a site.
func (h *BrainHandler) Status(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	state := h.deps.BrainManager.Status(siteID)
	running := h.deps.BrainManager.IsRunning(siteID)

	// Get mode from site record in database.
	mode := "building"
	var siteMode string
	if err := h.deps.DB.QueryRow("SELECT mode FROM sites WHERE id = ?", siteID).Scan(&siteMode); err == nil && siteMode != "" {
		mode = siteMode
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"site_id": siteID,
		"state":   state,
		"mode":    mode,
		"running": running,
	})
}

type modeChangeRequest struct {
	Mode string `json:"mode"`
}

// Mode changes the brain mode for a site.
func (h *BrainHandler) Mode(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	var req modeChangeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	validModes := map[string]bool{"building": true, "monitoring": true, "paused": true}
	if !validModes[req.Mode] {
		writeError(w, http.StatusBadRequest, "invalid mode; must be building, monitoring, or paused")
		return
	}

	// Update the mode in the database.
	if err := models.UpdateSiteMode(h.deps.DB.DB, siteID, req.Mode); err != nil {
		h.deps.Logger.Error("failed to update site mode", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update mode")
		return
	}

	// Send mode change command to the running brain worker.
	cmd := brain.BrainCommand{
		Type:   brain.CommandModeChange,
		SiteID: siteID,
		Payload: map[string]interface{}{
			"mode": req.Mode,
		},
	}
	_ = h.deps.BrainManager.SendCommand(siteID, cmd)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"site_id": siteID,
		"mode":    req.Mode,
	})
}
