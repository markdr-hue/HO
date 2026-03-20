/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/db"
)

type errorResponse struct {
	Error string `json:"error"`
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// decodeJSON decodes a JSON request body and writes an error response on failure.
// Returns false if decoding failed (error response already sent).
func decodeJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// requireSiteDB extracts the siteID from the URL, opens the site database,
// and returns both. Writes an error response and returns (0, nil) on failure.
func requireSiteDB(w http.ResponseWriter, r *http.Request, mgr *db.SiteDBManager) (int, *db.SiteDB) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return 0, nil
	}
	siteDB, err := mgr.Open(siteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open site database")
		return 0, nil
	}
	return siteID, siteDB
}
