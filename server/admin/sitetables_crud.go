/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/security"
	"github.com/markdr-hue/HO/tools"
)

type rowRequest struct {
	Data map[string]interface{} `json:"data"`
}

// InsertRow inserts a new row into a dynamic table.
func (h *SiteTablesHandler) InsertRow(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	tableName := chi.URLParam(r, "tableName")
	if tableName == "" {
		writeError(w, http.StatusBadRequest, "table name is required")
		return
	}

	// Verify the table exists in this site's database.
	var count int
	err := siteDB.QueryRow(
		"SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?",
		tableName,
	).Scan(&count)
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "table not found")
		return
	}

	var req rowRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Data) == 0 {
		writeError(w, http.StatusBadRequest, "data is required")
		return
	}

	secureCols, _ := tools.LoadSecureColumns(siteDB.Reader(), tableName)

	var colNames, placeholders []string
	var values []interface{}

	for col, val := range req.Data {
		colNames = append(colNames, fmt.Sprintf(`"%s"`, col))
		placeholders = append(placeholders, "?")

		if kind, ok := secureCols[col]; ok {
			processed, err := security.ProcessSecureValue(kind, val, h.deps.Encryptor)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("error processing %s: %v", col, err))
				return
			}
			val = processed
		}
		values = append(values, val)
	}

	query := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`,
		tableName, strings.Join(colNames, ", "), strings.Join(placeholders, ", "))

	res, err := siteDB.ExecWrite(query, values...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to insert row")
		return
	}

	id, err := res.LastInsertId()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get inserted row ID")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id})
}

// UpdateRow updates a row in a dynamic table.
func (h *SiteTablesHandler) UpdateRow(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	tableName := chi.URLParam(r, "tableName")
	rowID, err := strconv.Atoi(chi.URLParam(r, "rowID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid row ID")
		return
	}

	// Verify the table exists in this site's database.
	var count int
	err = siteDB.QueryRow(
		"SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?",
		tableName,
	).Scan(&count)
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "table not found")
		return
	}

	var req rowRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Data) == 0 {
		writeError(w, http.StatusBadRequest, "data is required")
		return
	}

	secureCols, _ := tools.LoadSecureColumns(siteDB.Reader(), tableName)

	var setClauses []string
	var values []interface{}

	for col, val := range req.Data {
		if kind, ok := secureCols[col]; ok {
			// Skip empty password fields (means "no change").
			if kind == "hash" {
				strVal := fmt.Sprintf("%v", val)
				if strVal == "" {
					continue
				}
			}
			processed, err := security.ProcessSecureValue(kind, val, h.deps.Encryptor)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("error processing %s: %v", col, err))
				return
			}
			val = processed
		}
		setClauses = append(setClauses, fmt.Sprintf(`"%s" = ?`, col))
		values = append(values, val)
	}

	if len(setClauses) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "no changes"})
		return
	}

	values = append(values, rowID)
	query := fmt.Sprintf(`UPDATE "%s" SET %s WHERE id = ?`,
		tableName, strings.Join(setClauses, ", "))

	_, err = siteDB.ExecWrite(query, values...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update row")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// DeleteRow deletes a row from a dynamic table.
func (h *SiteTablesHandler) DeleteRow(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	tableName := chi.URLParam(r, "tableName")
	rowID, err := strconv.Atoi(chi.URLParam(r, "rowID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid row ID")
		return
	}

	// Verify the table exists in this site's database.
	var count int
	err = siteDB.QueryRow(
		"SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?",
		tableName,
	).Scan(&count)
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "table not found")
		return
	}

	_, err = siteDB.ExecWrite(
		fmt.Sprintf(`DELETE FROM "%s" WHERE id = ?`, tableName),
		rowID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete row")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
