/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/tools"
)

// SiteTablesHandler handles dynamic table browsing for site detail views.
type SiteTablesHandler struct {
	deps *Deps
}

type dynamicTable struct {
	ID         int       `json:"id"`
	TableName  string    `json:"table_name"`
	SchemaDef  string    `json:"schema_def"`
	SecureCols string    `json:"secure_columns"`
	RowCount   int       `json:"row_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// List returns all dynamic tables for a site with row counts.
func (h *SiteTablesHandler) List(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, table_name, schema_def, secure_columns, created_at
		 FROM ho_dynamic_tables ORDER BY table_name ASC`,
	)
	if err != nil {
		writeJSON(w, http.StatusOK, []dynamicTable{})
		return
	}
	defer rows.Close()

	var tables []dynamicTable
	for rows.Next() {
		var t dynamicTable
		if err := rows.Scan(&t.ID, &t.TableName, &t.SchemaDef, &t.SecureCols, &t.CreatedAt); err != nil {
			continue
		}

		// Get row count for each table.
		siteDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", t.TableName)).Scan(&t.RowCount)

		tables = append(tables, t)
	}

	if tables == nil {
		tables = []dynamicTable{}
	}

	writeJSON(w, http.StatusOK, tables)
}

// Rows returns rows from a dynamic table with pagination.
// PASSWORD columns are stripped from the response. ENCRYPTED columns show a placeholder.
func (h *SiteTablesHandler) Rows(w http.ResponseWriter, r *http.Request) {
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

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	// Load secure columns to filter PASSWORD columns from results.
	secureCols, _ := tools.LoadSecureColumns(siteDB.Reader(), tableName)
	hashCols := map[string]bool{}
	for col, kind := range secureCols {
		if kind == "hash" {
			hashCols[col] = true
		}
	}

	// Get total count.
	var total int
	siteDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", tableName)).Scan(&total)

	// Query rows.
	rows, err := siteDB.Query(
		fmt.Sprintf("SELECT * FROM \"%s\" ORDER BY id DESC LIMIT ? OFFSET ?", tableName),
		limit, offset,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query table")
		return
	}
	defer rows.Close()

	allColumns, err := rows.Columns()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get columns")
		return
	}

	// Filter out PASSWORD columns from the visible column list.
	var visibleColumns []string
	var visibleIndices []int
	for i, col := range allColumns {
		if !hashCols[col] {
			visibleColumns = append(visibleColumns, col)
			visibleIndices = append(visibleIndices, i)
		}
	}

	var result []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(allColumns))
		ptrs := make([]interface{}, len(allColumns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]interface{})
		for _, idx := range visibleIndices {
			col := allColumns[idx]
			if b, ok := values[idx].([]byte); ok {
				row[col] = string(b)
			} else if f, ok := values[idx].(float64); ok && f == float64(int64(f)) {
				row[col] = int64(f)
			} else {
				row[col] = values[idx]
			}
			// Show placeholder for encrypted columns.
			if secureCols[col] == "encrypt" {
				row[col] = "[encrypted]"
			}
		}
		result = append(result, row)
	}

	if result == nil {
		result = []map[string]interface{}{}
	}

	// Also return schema info for the frontend to build CRUD forms.
	var schemaDef string
	siteDB.QueryRow(
		"SELECT schema_def FROM ho_dynamic_tables WHERE table_name = ?",
		tableName,
	).Scan(&schemaDef)

	var schemaMap map[string]string
	json.Unmarshal([]byte(schemaDef), &schemaMap)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"columns":        visibleColumns,
		"rows":           result,
		"total":          total,
		"schema":         schemaMap,
		"secure_columns": secureCols,
	})
}

// ExportCSV exports all rows of a table as CSV.
func (h *SiteTablesHandler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	tableName := chi.URLParam(r, "tableName")
	if tableName == "" {
		writeError(w, http.StatusBadRequest, "table name is required")
		return
	}

	var count int
	err := siteDB.QueryRow("SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?", tableName).Scan(&count)
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "table not found")
		return
	}

	secureCols, _ := tools.LoadSecureColumns(siteDB.Reader(), tableName)
	hashCols := map[string]bool{}
	for col, kind := range secureCols {
		if kind == "hash" {
			hashCols[col] = true
		}
	}

	rows, err := siteDB.Query(fmt.Sprintf("SELECT * FROM \"%s\" ORDER BY id ASC LIMIT 5000", tableName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query table")
		return
	}
	defer rows.Close()

	allColumns, _ := rows.Columns()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.csv", tableName))

	writer := csv.NewWriter(w)

	// Write header (skip password columns).
	var header []string
	var indices []int
	for i, col := range allColumns {
		if !hashCols[col] {
			header = append(header, col)
			indices = append(indices, i)
		}
	}
	writer.Write(header)

	for rows.Next() {
		values := make([]interface{}, len(allColumns))
		ptrs := make([]interface{}, len(allColumns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		var record []string
		for _, idx := range indices {
			col := allColumns[idx]
			if secureCols[col] == "encrypt" {
				record = append(record, "[encrypted]")
			} else if values[idx] == nil {
				record = append(record, "")
			} else {
				record = append(record, fmt.Sprintf("%v", values[idx]))
			}
		}
		writer.Write(record)
	}
	writer.Flush()
}

var validColName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
var allowedTypes = map[string]string{
	"TEXT": "TEXT", "INTEGER": "INTEGER", "REAL": "REAL", "BOOLEAN": "BOOLEAN",
	"PASSWORD": "TEXT", "ENCRYPTED": "TEXT",
}

type addColumnRequest struct {
	ColumnName string `json:"column_name"`
	ColumnType string `json:"column_type"`
	NotNull    bool   `json:"not_null"`
}

// AddColumn adds a new column to an existing dynamic table.
func (h *SiteTablesHandler) AddColumn(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	tableName := chi.URLParam(r, "tableName")
	if tableName == "" || tools.IsSystemTable(tableName) {
		writeError(w, http.StatusBadRequest, "invalid table name")
		return
	}

	var req addColumnRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if !validColName.MatchString(req.ColumnName) {
		writeError(w, http.StatusBadRequest, "invalid column name")
		return
	}

	colType := strings.ToUpper(req.ColumnType)
	sqlType, ok := allowedTypes[colType]
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported column type: "+req.ColumnType)
		return
	}

	colDef := req.ColumnName + " " + sqlType
	if req.NotNull {
		colDef += " NOT NULL DEFAULT ''"
	}

	_, err := siteDB.ExecWrite(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", tableName, colDef))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add column: "+err.Error())
		return
	}

	// Track secure columns.
	secureKinds := map[string]string{"PASSWORD": "hash", "ENCRYPTED": "encrypt"}
	if kind, isSecure := secureKinds[colType]; isSecure {
		var secureJSON string
		siteDB.QueryRow("SELECT secure_columns FROM ho_dynamic_tables WHERE table_name = ?", tableName).Scan(&secureJSON)
		var secureCols map[string]string
		if json.Unmarshal([]byte(secureJSON), &secureCols) != nil {
			secureCols = map[string]string{}
		}
		secureCols[req.ColumnName] = kind
		newJSON, _ := json.Marshal(secureCols)
		siteDB.ExecWrite("UPDATE ho_dynamic_tables SET secure_columns = ? WHERE table_name = ?", string(newJSON), tableName)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"table": tableName, "column_added": req.ColumnName})
}
