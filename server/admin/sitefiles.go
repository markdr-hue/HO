/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// SiteFilesHandler handles user file uploads for site detail views.
type SiteFilesHandler struct {
	deps *Deps
}

type siteFile struct {
	ID          int       `json:"id"`
	SiteID      int       `json:"site_id"`
	Filename    string    `json:"filename"`
	ContentType *string   `json:"content_type"`
	Size        *int      `json:"size"`
	Description *string   `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// List returns all files for a site.
func (h *SiteFilesHandler) List(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, filename, content_type, size, description, created_at
		 FROM ho_files ORDER BY created_at DESC`,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []siteFile{})
		return
	}
	defer rows.Close()

	var files []siteFile
	for rows.Next() {
		var f siteFile
		f.SiteID = siteID
		if err := rows.Scan(&f.ID, &f.Filename, &f.ContentType, &f.Size, &f.Description, &f.CreatedAt); err != nil {
			continue
		}
		files = append(files, f)
	}

	if files == nil {
		files = []siteFile{}
	}

	writeJSON(w, http.StatusOK, files)
}

// Create handles multipart file uploads.
func (h *SiteFilesHandler) Create(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	filename := filepath.Base(header.Filename)
	if filename == "." || filename == ".." {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	dir := filepath.Join("data", "sites", fmt.Sprintf("%d", siteID), "files")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create directory")
		return
	}

	storagePath := filepath.Join(dir, filename)
	dst, err := os.Create(storagePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create file")
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file")
		return
	}

	ct := inferContentType(filename)
	if header.Header.Get("Content-Type") != "" && header.Header.Get("Content-Type") != "application/octet-stream" {
		ct = header.Header.Get("Content-Type")
	}

	description := r.FormValue("description")

	_, err = siteDB.ExecWrite(
		`INSERT INTO ho_files (filename, content_type, size, storage_path, description)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(filename) DO UPDATE SET
		   content_type = excluded.content_type,
		   size = excluded.size,
		   storage_path = excluded.storage_path,
		   description = excluded.description`,
		filename, ct, int(written), storagePath, description,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save file record")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"filename":     filename,
		"content_type": ct,
		"size":         written,
	})
}

// Delete removes a file by ID.
func (h *SiteFilesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	fileID, err := strconv.Atoi(chi.URLParam(r, "fileID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file ID")
		return
	}

	// Get storage path before deleting.
	var storagePath string
	siteDB.QueryRow(
		"SELECT storage_path FROM ho_files WHERE id = ?",
		fileID,
	).Scan(&storagePath)

	_, err = siteDB.ExecWrite(
		"DELETE FROM ho_files WHERE id = ?",
		fileID,
	)
	if err != nil {
		h.deps.Logger.Error("failed to delete file", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete file")
		return
	}

	// Remove the file from disk (best effort).
	if storagePath != "" {
		os.Remove(storagePath)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Content serves the raw content of a file.
func (h *SiteFilesHandler) Content(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	fileID, err := strconv.Atoi(chi.URLParam(r, "fileID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file ID")
		return
	}

	var storagePath string
	err = siteDB.QueryRow(
		"SELECT storage_path FROM ho_files WHERE id = ?",
		fileID,
	).Scan(&storagePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	data, err := os.ReadFile(storagePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found on disk")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// UpdateContent updates the content of a text file on disk.
func (h *SiteFilesHandler) UpdateContent(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	fileID, err := strconv.Atoi(chi.URLParam(r, "fileID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file ID")
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var storagePath string
	err = siteDB.QueryRow(
		"SELECT storage_path FROM ho_files WHERE id = ?",
		fileID,
	).Scan(&storagePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	if err := os.WriteFile(storagePath, []byte(req.Content), 0644); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file")
		return
	}

	siteDB.ExecWrite("UPDATE ho_files SET size = ? WHERE id = ?", len(req.Content), fileID)

	writeJSON(w, http.StatusOK, map[string]interface{}{"updated": fileID})
}
