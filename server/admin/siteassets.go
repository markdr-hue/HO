/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// SiteAssetsHandler handles asset listing for site detail views.
type SiteAssetsHandler struct {
	deps *Deps
}

type siteAsset struct {
	ID          int       `json:"id"`
	SiteID      int       `json:"site_id"`
	Filename    string    `json:"filename"`
	ContentType *string   `json:"content_type"`
	Size        *int      `json:"size"`
	StoragePath string    `json:"storage_path"`
	CreatedAt   time.Time `json:"created_at"`
}

// List returns all assets for a site.
func (h *SiteAssetsHandler) List(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, filename, content_type, size, storage_path, created_at
		 FROM ho_assets ORDER BY created_at DESC`,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []siteAsset{})
		return
	}
	defer rows.Close()

	var assets []siteAsset
	for rows.Next() {
		var a siteAsset
		a.SiteID = siteID
		if err := rows.Scan(&a.ID, &a.Filename, &a.ContentType, &a.Size, &a.StoragePath, &a.CreatedAt); err != nil {
			continue
		}
		assets = append(assets, a)
	}

	if assets == nil {
		assets = []siteAsset{}
	}

	writeJSON(w, http.StatusOK, assets)
}

// Content serves the raw content of a text-based asset.
func (h *SiteAssetsHandler) Content(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	assetID, err := strconv.Atoi(chi.URLParam(r, "assetID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid asset ID")
		return
	}

	var storagePath string
	err = siteDB.QueryRow(
		"SELECT storage_path FROM ho_assets WHERE id = ?",
		assetID,
	).Scan(&storagePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}

	data, err := os.ReadFile(storagePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "asset file not found on disk")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// Delete removes an asset by ID.
func (h *SiteAssetsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	assetID, err := strconv.Atoi(chi.URLParam(r, "assetID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid asset ID")
		return
	}

	_, err = siteDB.ExecWrite(
		"DELETE FROM ho_assets WHERE id = ?",
		assetID,
	)
	if err != nil {
		h.deps.Logger.Error("failed to delete asset", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete asset")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
