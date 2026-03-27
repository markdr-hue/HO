/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/tunnel"
)

// TunnelHandler handles tunnel start/stop/status API calls.
type TunnelHandler struct {
	manager *tunnel.Manager
}

func (h *TunnelHandler) Start(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")

	var body struct {
		Subdomain string `json:"subdomain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Subdomain == "" {
		http.Error(w, `{"error":"subdomain is required"}`, http.StatusBadRequest)
		return
	}

	url, err := h.manager.Start(siteID, body.Subdomain)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": url, "subdomain": body.Subdomain})
}

func (h *TunnelHandler) Stop(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	h.manager.Stop(siteID)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (h *TunnelHandler) Status(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	status := h.manager.GetStatus(siteID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
