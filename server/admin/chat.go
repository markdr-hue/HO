/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// ChatHandlerAdmin wraps the chat.ChatHandler for admin routes.
type ChatHandlerAdmin struct {
	deps *Deps
}

// Stream delegates to ChatHandler.HandleStream after injecting the siteID
// as a query parameter.
func (h *ChatHandlerAdmin) Stream(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	if _, err := strconv.Atoi(siteID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	// The underlying ChatHandler reads site_id and session_id from query params.
	q := r.URL.Query()
	q.Set("site_id", siteID)
	if q.Get("session_id") == "" {
		q.Set("session_id", "admin")
	}
	r.URL.RawQuery = q.Encode()

	h.deps.ChatHandler.HandleStream(w, r)
}

// History delegates to ChatHandler.HandleHistory.
func (h *ChatHandlerAdmin) History(w http.ResponseWriter, r *http.Request) {
	siteID := chi.URLParam(r, "siteID")
	if _, err := strconv.Atoi(siteID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	q := r.URL.Query()
	q.Set("site_id", siteID)
	if q.Get("session_id") == "" {
		q.Set("session_id", "admin")
	}
	r.URL.RawQuery = q.Encode()

	h.deps.ChatHandler.HandleHistory(w, r)
}
