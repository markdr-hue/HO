/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// textAssetRequest is the JSON body for creating a text-based asset (JS/CSS/HTML).
type textAssetRequest struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// Create handles both JSON (text asset creation) and multipart (file upload).
func (h *SiteAssetsHandler) Create(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "siteID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid site ID")
		return
	}

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		h.handleAssetUpload(w, r, siteID)
	} else {
		h.handleTextAsset(w, r, siteID)
	}
}

func (h *SiteAssetsHandler) handleTextAsset(w http.ResponseWriter, r *http.Request, siteID int) {
	var req textAssetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Filename == "" {
		writeError(w, http.StatusBadRequest, "filename is required")
		return
	}

	filename := filepath.Base(req.Filename)
	if filename == "." || filename == ".." {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	dir := filepath.Join("data", "sites", fmt.Sprintf("%d", siteID), "assets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create directory")
		return
	}

	storagePath := filepath.Join(dir, filename)
	data := []byte(req.Content)
	if err := os.WriteFile(storagePath, data, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file")
		return
	}

	ct := inferContentType(filename)

	siteDB, err := h.deps.SiteDBManager.Open(siteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open site database")
		return
	}

	_, err = siteDB.ExecWrite(
		`INSERT INTO ho_assets (filename, content_type, size, storage_path)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(filename) DO UPDATE SET
		   content_type = excluded.content_type,
		   size = excluded.size,
		   storage_path = excluded.storage_path`,
		filename, ct, len(data), storagePath,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save asset record")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"filename":     filename,
		"content_type": ct,
		"size":         len(data),
	})
}

func (h *SiteAssetsHandler) handleAssetUpload(w http.ResponseWriter, r *http.Request, siteID int) {
	const maxUploadSize = 32 << 20 // 32 MB
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
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

	dir := filepath.Join("data", "sites", fmt.Sprintf("%d", siteID), "assets")
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
	if written > maxUploadSize {
		os.Remove(storagePath)
		writeError(w, http.StatusBadRequest, "file too large (max 32 MB)")
		return
	}

	ct := inferContentType(filename)

	siteDB, err := h.deps.SiteDBManager.Open(siteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open site database")
		return
	}

	_, err = siteDB.ExecWrite(
		`INSERT INTO ho_assets (filename, content_type, size, storage_path)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(filename) DO UPDATE SET
		   content_type = excluded.content_type,
		   size = excluded.size,
		   storage_path = excluded.storage_path`,
		filename, ct, int(written), storagePath,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save asset record")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"filename":     filename,
		"content_type": ct,
		"size":         written,
	})
}

func inferContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".html", ".htm":
		return "text/html"
	case ".json":
		return "application/json"
	case ".pdf":
		return "application/pdf"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	default:
		return "application/octet-stream"
	}
}
