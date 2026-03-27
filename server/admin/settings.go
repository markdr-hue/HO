/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"net/http"

	"github.com/markdr-hue/HO/events"
)

// SettingsHandler handles system settings get/put.
type SettingsHandler struct {
	deps *Deps
}

// Get returns all settings as a key-value map.
func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	rows, err := h.deps.DB.Query("SELECT key, value FROM settings ORDER BY key")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		settings[key] = value
	}

	writeJSON(w, http.StatusOK, settings)
}

// Update sets one or more settings from a key-value map.
func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var settings map[string]string
	if !decodeJSON(w, r, &settings) {
		return
	}

	for key, value := range settings {
		if err := h.deps.DB.SetSetting(key, value); err != nil {
			h.deps.Logger.Error("failed to set setting", "key", key, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to save settings")
			return
		}
	}

	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventSettingsChanged, 0, nil))
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
