/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"fmt"
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
	ID           int       `json:"id"`
	SiteID       int       `json:"site_id"`
	Filename     string    `json:"filename"`
	ContentType  *string   `json:"content_type"`
	Size         *int      `json:"size"`
	StoragePath  string    `json:"storage_path"`
	CreatedAt    time.Time `json:"created_at"`
	VersionCount int       `json:"version_count"`
}

// List returns all assets for a site.
func (h *SiteAssetsHandler) List(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, filename, content_type, size, storage_path, created_at,
		        (SELECT COUNT(*) FROM ho_file_versions WHERE storage_type = 'ho_assets' AND filename = ho_assets.filename) AS version_count
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
		if err := rows.Scan(&a.ID, &a.Filename, &a.ContentType, &a.Size, &a.StoragePath, &a.CreatedAt, &a.VersionCount); err != nil {
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

type assetVersion struct {
	VersionNumber int       `json:"version_number"`
	Size          *int      `json:"size"`
	ChangedBy     string    `json:"changed_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListVersions returns version history for an asset.
func (h *SiteAssetsHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	assetID, err := strconv.Atoi(chi.URLParam(r, "assetID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid asset ID")
		return
	}

	// Look up the filename for this asset ID.
	var filename string
	err = siteDB.QueryRow("SELECT filename FROM ho_assets WHERE id = ?", assetID).Scan(&filename)
	if err != nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}

	rows, err := siteDB.Query(
		"SELECT version_number, size, changed_by, created_at FROM ho_file_versions WHERE storage_type = 'ho_assets' AND filename = ? ORDER BY version_number DESC LIMIT 20",
		filename,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []assetVersion{})
		return
	}
	defer rows.Close()

	var versions []assetVersion
	for rows.Next() {
		var v assetVersion
		if err := rows.Scan(&v.VersionNumber, &v.Size, &v.ChangedBy, &v.CreatedAt); err != nil {
			continue
		}
		versions = append(versions, v)
	}
	if versions == nil {
		versions = []assetVersion{}
	}
	writeJSON(w, http.StatusOK, versions)
}

// Revert restores an asset to a previous version. Saves current content as a new version first.
func (h *SiteAssetsHandler) Revert(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	assetID, err := strconv.Atoi(chi.URLParam(r, "assetID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid asset ID")
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid version number")
		return
	}

	db := siteDB.Writer()

	// Look up asset filename and storage path.
	var filename, storagePath string
	var contentType *string
	err = db.QueryRow("SELECT filename, storage_path, content_type FROM ho_assets WHERE id = ?", assetID).Scan(&filename, &storagePath, &contentType)
	if err != nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}

	// Load the target version.
	var versionContent string
	var versionSize *int
	err = db.QueryRow(
		"SELECT content, size FROM ho_file_versions WHERE storage_type = 'ho_assets' AND filename = ? AND version_number = ?",
		filename, version,
	).Scan(&versionContent, &versionSize)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("version %d not found", version))
		return
	}

	// Save current content as new version (so revert is reversible).
	currentContent, readErr := os.ReadFile(storagePath)
	if readErr == nil {
		var maxVer int
		db.QueryRow("SELECT COALESCE(MAX(version_number), 0) FROM ho_file_versions WHERE storage_type = 'ho_assets' AND filename = ?", filename).Scan(&maxVer)
		db.Exec(
			`INSERT INTO ho_file_versions (storage_type, filename, content, content_type, size, version_number, changed_by)
			 VALUES ('ho_assets', ?, ?, ?, ?, ?, 'admin_revert')`,
			filename, string(currentContent), contentType, len(currentContent), maxVer+1,
		)
	}

	// Write the restored content to disk.
	if err := os.WriteFile(storagePath, []byte(versionContent), 0644); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write reverted content")
		return
	}

	// Update size in ho_assets.
	db.Exec("UPDATE ho_assets SET size = ? WHERE id = ?", len(versionContent), assetID)

	writeJSON(w, http.StatusOK, map[string]interface{}{"reverted": assetID, "to_version": version})
}
