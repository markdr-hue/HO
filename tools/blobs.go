/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// BlobsTool — manage_blobs
// ---------------------------------------------------------------------------

// BlobsTool manages metadata for user-uploaded content (avatars, documents,
// images) separately from code assets (CSS/JS/SVG).
type BlobsTool struct{}

func (t *BlobsTool) Name() string { return "manage_blobs" }
func (t *BlobsTool) Description() string {
	return "Register, list, get, or delete blob metadata for user-uploaded content."
}

func (t *BlobsTool) Guide() string {
	return `### Blob Storage (manage_blobs)
- **register**: Register user-uploaded file metadata. Params: key (path like "avatars/user-1.jpg"), content_type, size (bytes), metadata (JSON string). Returns public URL at /ho_files/ho_blobs/{key}.
- **list**: List blobs. Params: prefix (filter by key prefix), limit (default 50).
- **get**: Get blob metadata by key.
- **delete**: Remove blob metadata and file from disk.
Use upload endpoints to accept file uploads from users. Use manage_blobs to track and organize uploaded files.`
}

func (t *BlobsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"register", "list", "get", "delete"},
				"description": "Action to perform",
			},
			"key": map[string]interface{}{
				"type":        "string",
				"description": "Blob key (path-like, e.g., 'avatars/user-1.jpg'). For register, get, delete.",
			},
			"content_type": map[string]interface{}{
				"type":        "string",
				"description": "MIME type of the blob. For register.",
			},
			"size": map[string]interface{}{
				"type":        "number",
				"description": "File size in bytes. For register.",
			},
			"metadata": map[string]interface{}{
				"type":        "string",
				"description": "JSON string with arbitrary metadata. For register.",
			},
			"prefix": map[string]interface{}{
				"type":        "string",
				"description": "Key prefix filter (e.g., 'avatars/'). For list.",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Max results (default 50). For list.",
			},
		},
		"required": []string{},
	}
}

func (t *BlobsTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"register": t.register,
		"list":     t.list,
		"get":      t.get,
		"delete":   t.del,
	}, nil)
}

func (t *BlobsTool) ensureTables(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS ho_blobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		key TEXT UNIQUE NOT NULL,
		content_type TEXT DEFAULT '',
		size INTEGER DEFAULT 0,
		storage_path TEXT NOT NULL,
		metadata TEXT DEFAULT '{}',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
}

func (t *BlobsTool) register(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	key, errResult := RequireString(args, "key")
	if errResult != nil {
		return errResult, nil
	}

	// Sanitize key — no path traversal.
	key = strings.TrimPrefix(key, "/")
	if strings.Contains(key, "..") {
		return &Result{Success: false, Error: "key must not contain '..'"}, nil
	}

	contentType := OptionalString(args, "content_type", "application/octet-stream")
	size := OptionalInt(args, "size", 0)
	metadata := OptionalString(args, "metadata", "{}")

	t.ensureTables(ctx.DB)

	// Compute storage path and ensure directory exists.
	dir, _ := storageDir(ctx.SiteID, "ho_files")
	storagePath := filepath.Join(dir, "ho_blobs", key)
	os.MkdirAll(filepath.Dir(storagePath), 0755)

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_blobs (key, content_type, size, storage_path, metadata)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   content_type = excluded.content_type,
		   size = excluded.size,
		   storage_path = excluded.storage_path,
		   metadata = excluded.metadata`,
		key, contentType, size, storagePath, metadata,
	)
	if err != nil {
		return nil, fmt.Errorf("registering blob: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"key":          key,
		"content_type": contentType,
		"size":         size,
		"url":          "/files/blobs/" + key,
	}}, nil
}

func (t *BlobsTool) list(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	t.ensureTables(ctx.DB)

	prefix := OptionalString(args, "prefix", "")
	limit := OptionalInt(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}

	var rows *sql.Rows
	var err error
	if prefix != "" {
		rows, err = ctx.DB.Query(
			"SELECT key, content_type, size, metadata, created_at FROM ho_blobs WHERE key LIKE ? ORDER BY key LIMIT ?",
			prefix+"%", limit,
		)
	} else {
		rows, err = ctx.DB.Query(
			"SELECT key, content_type, size, metadata, created_at FROM ho_blobs ORDER BY key LIMIT ?",
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("listing blobs: %w", err)
	}
	defer rows.Close()

	var blobs []map[string]interface{}
	for rows.Next() {
		var key, contentType, metadata, createdAt string
		var size int
		if rows.Scan(&key, &contentType, &size, &metadata, &createdAt) != nil {
			continue
		}
		blobs = append(blobs, map[string]interface{}{
			"key":          key,
			"content_type": contentType,
			"size":         size,
			"metadata":     metadata,
			"url":          "/files/blobs/" + key,
			"created_at":   createdAt,
		})
	}

	return &Result{Success: true, Data: blobs}, nil
}

func (t *BlobsTool) get(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	key, errResult := RequireString(args, "key")
	if errResult != nil {
		return errResult, nil
	}

	t.ensureTables(ctx.DB)

	var contentType, metadata, storagePath, createdAt string
	var size int
	err := ctx.DB.QueryRow(
		"SELECT content_type, size, storage_path, metadata, created_at FROM ho_blobs WHERE key = ?",
		key,
	).Scan(&contentType, &size, &storagePath, &metadata, &createdAt)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("blob '%s' not found", key)}, nil
	}

	// Check if file exists on disk.
	exists := false
	if _, err := os.Stat(storagePath); err == nil {
		exists = true
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"key":          key,
		"content_type": contentType,
		"size":         size,
		"metadata":     metadata,
		"url":          "/files/blobs/" + key,
		"file_exists":  exists,
		"created_at":   createdAt,
	}}, nil
}

func (t *BlobsTool) del(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	key, errResult := RequireString(args, "key")
	if errResult != nil {
		return errResult, nil
	}

	t.ensureTables(ctx.DB)

	// Get storage path before deleting.
	var storagePath string
	err := ctx.DB.QueryRow("SELECT storage_path FROM ho_blobs WHERE key = ?", key).Scan(&storagePath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("blob '%s' not found", key)}, nil
	}

	// Delete from DB.
	ctx.DB.Exec("DELETE FROM ho_blobs WHERE key = ?", key)

	// Delete from disk.
	os.Remove(storagePath)

	return &Result{Success: true, Data: map[string]interface{}{
		"deleted": key,
	}}, nil
}
