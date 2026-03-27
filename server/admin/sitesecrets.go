/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// SiteSecretsHandler handles secret listing/deletion for admin.
type SiteSecretsHandler struct {
	deps *Deps
}

type secretEntry struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// List returns all secrets for a site (names only, never values).
func (h *SiteSecretsHandler) List(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	rows, err := siteDB.Query(
		"SELECT id, name, created_at, updated_at FROM ho_secrets ORDER BY name",
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []secretEntry{})
		return
	}
	defer rows.Close()

	var secrets []secretEntry
	for rows.Next() {
		var s secretEntry
		if err := rows.Scan(&s.ID, &s.Name, &s.CreatedAt, &s.UpdatedAt); err != nil {
			continue
		}
		secrets = append(secrets, s)
	}

	if secrets == nil {
		secrets = []secretEntry{}
	}

	writeJSON(w, http.StatusOK, secrets)
}

// Delete removes a secret by ID.
func (h *SiteSecretsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	secretID, err := strconv.Atoi(chi.URLParam(r, "secretID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid secret ID")
		return
	}

	res, err := siteDB.ExecWrite(
		"DELETE FROM ho_secrets WHERE id = ?",
		secretID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete secret")
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": secretID})
}

type createSecretRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Create stores a new encrypted secret.
func (h *SiteSecretsHandler) Create(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	var req createSecretRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.Value == "" {
		writeError(w, http.StatusBadRequest, "name and value are required")
		return
	}

	encrypted, err := h.deps.Encryptor.Encrypt(req.Value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt secret")
		return
	}

	_, err = siteDB.ExecWrite(
		`INSERT INTO ho_secrets (name, value_encrypted, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(name) DO UPDATE SET value_encrypted = excluded.value_encrypted, updated_at = CURRENT_TIMESTAMP`,
		req.Name, encrypted,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store secret")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"stored": req.Name})
}
