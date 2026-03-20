/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// SearchTool — manage_search
// ---------------------------------------------------------------------------

// SearchTool provides FTS5 full-text search index management and querying.
type SearchTool struct{}

func (t *SearchTool) Name() string { return "manage_search" }
func (t *SearchTool) Description() string {
	return "Create, drop, or query full-text search indexes (FTS5)."
}

func (t *SearchTool) Guide() string {
	return `### Full-Text Search (manage_search)
- **create_index**: Create FTS5 search index on a table. Params: table_name, columns (array of TEXT column names to index).
- **search**: Full-text search with relevance ranking. Params: table_name, query (search terms), limit (default 50).
- **drop_index**: Remove search index from a table.
- **list_indexes**: List all active FTS5 search indexes.
FTS5 supports phrase queries ("exact phrase"), prefix queries (term*), AND/OR/NOT operators, and column filters (col:term).
For new tables, prefer manage_schema's searchable_columns instead. Use manage_search for adding FTS to existing tables post-build.`
}

func (t *SearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"create_index", "search", "drop_index", "list_indexes"},
				"description": "Action to perform",
			},
			"table_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the dynamic table. For create_index, search, drop_index.",
			},
			"columns": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "TEXT columns to index. For create_index.",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "FTS5 search query. For search.",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Max results (default 50, max 200). For search.",
			},
		},
		"required": []string{},
	}
}

func (t *SearchTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"create_index": t.createIndex,
		"search":       t.search,
		"drop_index":   t.dropIndex,
		"list_indexes": t.listIndexes,
	}, nil)
}

func (t *SearchTool) createIndex(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, errResult := RequireString(args, "table_name")
	if errResult != nil {
		return errResult, nil
	}
	tableName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Validate table exists in dynamic tables.
	var count int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?", tableName).Scan(&count)
	if count == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("table '%s' not found in dynamic tables", tableName)}, nil
	}

	// Extract columns.
	colsRaw, ok := args["columns"]
	if !ok {
		return &Result{Success: false, Error: "columns is required (array of TEXT column names)"}, nil
	}
	colArr, ok := colsRaw.([]interface{})
	if !ok || len(colArr) == 0 {
		return &Result{Success: false, Error: "columns must be a non-empty array of column names"}, nil
	}
	var columns []string
	for _, c := range colArr {
		col, ok := c.(string)
		if !ok || col == "" {
			return &Result{Success: false, Error: "each column must be a non-empty string"}, nil
		}
		columns = append(columns, col)
	}

	// Check if FTS index already exists.
	ftsTable := tableName + "_fts"
	var ftsCount int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", ftsTable).Scan(&ftsCount)
	if ftsCount > 0 {
		return &Result{Success: false, Error: fmt.Sprintf("FTS index '%s' already exists — drop it first to recreate", ftsTable)}, nil
	}

	// Reuse the existing FTS5 creation logic from tables.go.
	if err := createFTSIndex(ctx.DB, tableName, columns); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Populate FTS with existing rows.
	colList := strings.Join(columns, ", ")
	_, _ = ctx.DB.Exec(fmt.Sprintf(
		"INSERT INTO %s(rowid, %s) SELECT id, %s FROM %s",
		ftsTable, colList, colList, tableName,
	))

	return &Result{Success: true, Data: map[string]interface{}{
		"table":   tableName,
		"index":   ftsTable,
		"columns": columns,
	}}, nil
}

func (t *SearchTool) search(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, errResult := RequireString(args, "table_name")
	if errResult != nil {
		return errResult, nil
	}
	tableName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	query, errResult := RequireString(args, "query")
	if errResult != nil {
		return errResult, nil
	}

	limit := OptionalInt(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}

	ftsTable := tableName + "_fts"

	// Verify FTS table exists.
	var ftsCount int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", ftsTable).Scan(&ftsCount)
	if ftsCount == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("no FTS index for table '%s' — create one first with create_index", tableName)}, nil
	}

	// Query with BM25 ranking.
	sql := fmt.Sprintf(
		`SELECT t.* FROM %s t
		 JOIN %s f ON t.id = f.rowid
		 WHERE %s MATCH ?
		 ORDER BY rank
		 LIMIT ?`,
		tableName, ftsTable, ftsTable,
	)
	rows, err := ctx.DB.Query(sql, query, limit)
	if err != nil {
		return &Result{Success: false, Error: "search failed: " + err.Error()}, nil
	}
	defer rows.Close()

	results := scanRowsMaps(rows)

	// Strip secure columns.
	secureCols, _ := loadSecureColumns(ctx, tableName)
	stripSecureCols(results, secureCols)

	return &Result{Success: true, Data: map[string]interface{}{
		"query":   query,
		"count":   len(results),
		"results": results,
	}}, nil
}

func (t *SearchTool) dropIndex(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, errResult := RequireString(args, "table_name")
	if errResult != nil {
		return errResult, nil
	}
	tableName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	ftsTable := tableName + "_fts"

	// Drop FTS table and triggers.
	ctx.DB.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", ftsTable))
	ctx.DB.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s_ai", tableName))
	ctx.DB.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s_ad", tableName))
	ctx.DB.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s_au", tableName))

	return &Result{Success: true, Data: map[string]interface{}{
		"table":   tableName,
		"dropped": ftsTable,
	}}, nil
}

func (t *SearchTool) listIndexes(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT name FROM sqlite_master WHERE type='table' AND sql LIKE '%fts5%' ORDER BY name",
	)
	if err != nil {
		return &Result{Success: false, Error: "listing indexes: " + err.Error()}, nil
	}
	defer rows.Close()

	var indexes []map[string]interface{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			continue
		}
		// Derive source table from FTS name.
		sourceTable := strings.TrimSuffix(name, "_fts")
		indexes = append(indexes, map[string]interface{}{
			"index":        name,
			"source_table": sourceTable,
		})
	}

	return &Result{Success: true, Data: indexes}, nil
}
