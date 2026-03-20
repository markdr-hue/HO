/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/markdr-hue/HO/internal/sqliteretry"
	"github.com/markdr-hue/HO/security"
)

// validTableName matches only alphanumeric and underscore characters.
var validTableName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// validColumnName matches only alphanumeric and underscore characters.
var validColumnName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// allowedColumnTypes are the SQLite types we permit in dynamic tables.
// PASSWORD and ENCRYPTED are stored as TEXT but handled specially on insert/query.
var allowedColumnTypes = map[string]string{
	"TEXT":      "TEXT",
	"INTEGER":   "INTEGER",
	"REAL":      "REAL",
	"BLOB":      "BLOB",
	"BOOLEAN":   "BOOLEAN",
	"PASSWORD":  "TEXT", // stored as bcrypt hash
	"ENCRYPTED": "TEXT", // stored as AES-encrypted base64
}

// secureColumnKinds maps logical types to their security handling.
var secureColumnKinds = map[string]string{
	"PASSWORD":  "hash",
	"ENCRYPTED": "encrypt",
}

// systemTables are internal tables that the LLM must not create, alter, or drop.
var systemTables = map[string]bool{
	"ho_pages": true, "ho_page_versions": true, "ho_assets": true, "ho_files": true,
	"ho_file_versions": true, "ho_brain_log": true, "ho_memory": true,
	"ho_chat_messages": true, "ho_questions": true, "ho_answers": true,
	"ho_dynamic_tables": true, "ho_api_endpoints": true, "ho_auth_endpoints": true,
	"ho_oauth_providers": true, "ho_webhooks": true, "ho_webhook_subscriptions": true,
	"ho_webhook_logs": true, "ho_analytics": true, "ho_secrets": true,
	"ho_scheduled_tasks": true, "ho_task_runs": true, "ho_activity_log": true,
	"ho_approval_rules": true, "ho_site_snapshots": true, "ho_service_providers": true,
	"ho_llm_log": true, "ho_layouts": true, "ho_layout_versions": true,
	"ho_pipeline_state": true, "ho_stage_log": true,
	"ho_upload_endpoints": true, "ho_stream_endpoints": true,
	"ho_redirects": true, "ho_ws_endpoints": true,
	"ho_seo_meta": true, "ho_blobs": true, "ho_jobs": true,
	"ho_actions": true, "ho_llm_endpoints": true,
}

// IsSystemTable returns true if the name is a reserved internal table.
func IsSystemTable(name string) bool {
	return systemTables[name]
}

// sanitizedTableName validates and returns the physical table name.
func sanitizedTableName(name string) (string, error) {
	if !validTableName.MatchString(name) {
		return "", fmt.Errorf("invalid table name: %s (must be alphanumeric/underscore, start with letter)", name)
	}
	if systemTables[name] {
		return "", fmt.Errorf("cannot use reserved system table: %s", name)
	}
	return name, nil
}

// loadSecureColumns loads the secure_columns JSON map for a dynamic table.
func loadSecureColumns(ctx *ToolContext, tableName string) (map[string]string, error) {
	return LoadSecureColumns(ctx.DB, tableName)
}

// LoadSecureColumns reads the secure column types (hash/encrypt) for a dynamic table.
// Exported so admin handlers can share this logic without duplication.
func LoadSecureColumns(db *sql.DB, tableName string) (map[string]string, error) {
	var raw string
	err := db.QueryRow(
		"SELECT secure_columns FROM ho_dynamic_tables WHERE table_name = ?",
		tableName,
	).Scan(&raw)
	if err != nil {
		return nil, nil // table not in registry or no secure columns
	}
	var cols map[string]string
	if err := json.Unmarshal([]byte(raw), &cols); err != nil {
		return nil, nil
	}
	return cols, nil
}

// processSecureValue delegates to the shared security.ProcessSecureValue.
func processSecureValue(kind string, value interface{}, enc *security.Encryptor) (interface{}, error) {
	return security.ProcessSecureValue(kind, value, enc)
}

// allowedOps are the SQL comparison operators permitted in structured filters.
var allowedOps = map[string]bool{
	"=": true, "!=": true, "<": true, ">": true, "<=": true, ">=": true,
	"LIKE": true, "NOT LIKE": true, "IS NULL": true, "IS NOT NULL": true,
}

// buildWhereClause builds a parameterized WHERE clause from structured filters.
// Each filter must have "column" and "op"; "value" is required for most ops.
func buildWhereClause(filters []interface{}) (string, []interface{}, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}

	var clauses []string
	var params []interface{}

	for _, f := range filters {
		fm, ok := f.(map[string]interface{})
		if !ok {
			return "", nil, fmt.Errorf("each filter must be an object with column, op, and value")
		}
		col, _ := fm["column"].(string)
		op, _ := fm["op"].(string)

		if !validColumnName.MatchString(col) {
			return "", nil, fmt.Errorf("invalid column name in filter: %s", col)
		}
		op = strings.ToUpper(strings.TrimSpace(op))
		if !allowedOps[op] {
			return "", nil, fmt.Errorf("unsupported operator: %s", op)
		}

		if op == "IS NULL" || op == "IS NOT NULL" {
			clauses = append(clauses, fmt.Sprintf("%s %s", col, op))
		} else {
			val, hasVal := fm["value"]
			if !hasVal {
				return "", nil, fmt.Errorf("filter on %s requires a value", col)
			}
			clauses = append(clauses, fmt.Sprintf("%s %s ?", col, op))
			params = append(params, val)
		}
	}

	return " WHERE " + strings.Join(clauses, " AND "), params, nil
}

// ---------------------------------------------------------------------------
// SchemaTool — manage_schema
// ---------------------------------------------------------------------------

// SchemaTool consolidates create, alter, describe, list, and drop table operations.
type SchemaTool struct{}

func (t *SchemaTool) Name() string { return "manage_schema" }
func (t *SchemaTool) Description() string {
	return "Create, alter, describe, list, or drop dynamic tables. Supports PASSWORD (bcrypt) and ENCRYPTED (AES) column types, FTS5 full-text search via searchable_columns."
}

func (t *SchemaTool) Guide() string {
	return `### Dynamic Tables (manage_schema)
- Column types: TEXT, INTEGER, REAL, BOOLEAN, PASSWORD (bcrypt), ENCRYPTED (AES).
- id and created_at columns are auto-added — do NOT include them.
- searchable_columns enables FTS5 full-text search (used with manage_data search action).
- PASSWORD columns auto-hash with bcrypt. ENCRYPTED columns auto-encrypt with AES.
- Reserved names (cannot be used): ho_pages, ho_assets, ho_questions, ho_answers, ho_memory, ho_secrets, ho_analytics, ho_layouts, ho_files, ho_chat_messages, ho_brain_log, ho_dynamic_tables, ho_api_endpoints, ho_auth_endpoints, ho_webhooks, ho_scheduled_tasks. Use descriptive prefixes (e.g. survey_questions, quiz_answers).
- Dropping tables or columns requires owner approval via manage_communication.`
}

func (t *SchemaTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"create", "alter", "describe", "list", "drop", "add_index"},
				"description": "Schema operation to perform",
			},
			"table_name": map[string]interface{}{
				"type":        "string",
				"description": "Logical table name (alphanumeric and underscores only)",
			},
			"columns": map[string]interface{}{
				"type":        "object",
				"description": "Column definitions as {name: type} for create. Types: TEXT, INTEGER, REAL, BOOLEAN, PASSWORD (auto-hashed), ENCRYPTED (auto-encrypted)",
			},
			"add_columns": map[string]interface{}{
				"type":        "object",
				"description": "Columns to add for alter: {name: type}",
			},
			"drop_columns": map[string]interface{}{
				"type":        "array",
				"description": "Column names to drop for alter",
				"items":       map[string]interface{}{"type": "string"},
			},
			"searchable_columns": map[string]interface{}{
				"type":        "array",
				"description": "TEXT column names to index for full-text search (FTS5). Creates a virtual table and sync triggers. Used with manage_data search action.",
				"items":       map[string]interface{}{"type": "string"},
			},
			"index_columns": map[string]interface{}{
				"type":        "array",
				"description": "Column names to index together (for add_index action). Creates a B-tree index for query performance.",
				"items":       map[string]interface{}{"type": "string"},
			},
			"unique": map[string]interface{}{
				"type":        "boolean",
				"description": "Create a UNIQUE index (for add_index action). Default: false.",
			},
		},
		"required": []string{},
	}
}

func (t *SchemaTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"create":    t.create,
		"alter":     t.alter,
		"describe":  t.describe,
		"list":      t.list,
		"drop":      t.drop,
		"add_index": t.addIndex,
	}, func(a map[string]interface{}) string {
		if _, has := a["columns"]; has {
			return "create"
		}
		if _, has := a["table_name"]; has {
			return "describe"
		}
		return "list"
	})
}

func (t *SchemaTool) create(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	columnsRaw, _ := args["columns"].(map[string]interface{})
	if tableName == "" || len(columnsRaw) == 0 {
		return &Result{Success: false, Error: "table_name and columns are required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Validate and build column definitions, track secure columns.
	var colDefs []string
	secureCols := map[string]string{}

	for colName, colTypeRaw := range columnsRaw {
		if !validColumnName.MatchString(colName) {
			return &Result{Success: false, Error: fmt.Sprintf("invalid column name: %s", colName)}, nil
		}
		colType, ok := colTypeRaw.(string)
		if !ok {
			return &Result{Success: false, Error: fmt.Sprintf("column type must be a string for %s", colName)}, nil
		}
		colType = strings.ToUpper(colType)
		sqlType, allowed := allowedColumnTypes[colType]
		if !allowed {
			return &Result{Success: false, Error: fmt.Sprintf("unsupported column type: %s (allowed: TEXT, INTEGER, REAL, BOOLEAN, PASSWORD, ENCRYPTED)", colType)}, nil
		}
		colDefs = append(colDefs, fmt.Sprintf("%s %s", colName, sqlType))

		// Track secure columns.
		if kind, isSecure := secureColumnKinds[colType]; isSecure {
			secureCols[colName] = kind
		}
	}

	createSQL := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (id INTEGER PRIMARY KEY AUTOINCREMENT, %s, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)",
		physicalName, strings.Join(colDefs, ", "),
	)

	if _, err := ctx.DB.Exec(createSQL); err != nil {
		return nil, fmt.Errorf("creating table: %w", err)
	}

	// Record the table in ho_dynamic_tables registry with secure column info.
	schemaDef, err := json.Marshal(columnsRaw)
	if err != nil {
		return nil, fmt.Errorf("marshaling schema: %w", err)
	}
	secureColsJSON, err := json.Marshal(secureCols)
	if err != nil {
		return nil, fmt.Errorf("marshaling secure columns: %w", err)
	}
	_, err = ctx.DB.Exec(
		`INSERT INTO ho_dynamic_tables (table_name, schema_def, secure_columns)
		 VALUES (?, ?, ?)
		 ON CONFLICT(table_name) DO UPDATE SET schema_def = excluded.schema_def, secure_columns = excluded.secure_columns`,
		tableName, string(schemaDef), string(secureColsJSON),
	)
	if err != nil {
		return nil, fmt.Errorf("recording dynamic table: %w", err)
	}

	// Create FTS5 full-text search index if searchable_columns provided.
	var searchableCols []string
	if rawSearchable, ok := args["searchable_columns"].([]interface{}); ok && len(rawSearchable) > 0 {
		for _, sc := range rawSearchable {
			colName, ok := sc.(string)
			if !ok || !validColumnName.MatchString(colName) {
				continue
			}
			// Verify the column exists and is TEXT type.
			if colType, exists := columnsRaw[colName]; exists {
				if ct, ok := colType.(string); ok && strings.EqualFold(ct, "TEXT") {
					searchableCols = append(searchableCols, colName)
				}
			}
		}
		if len(searchableCols) > 0 {
			if err := createFTSIndex(ctx.DB, physicalName, searchableCols); err != nil {
				// Non-fatal — FTS is a bonus, not a requirement.
				return &Result{Success: true, Data: map[string]interface{}{
					"table":          tableName,
					"columns":        columnsRaw,
					"secure_columns": secureCols,
					"fts_warning":    fmt.Sprintf("FTS5 index creation failed: %v", err),
				}}, nil
			}
		}
	}

	resultData := map[string]interface{}{
		"table":          tableName,
		"columns":        columnsRaw,
		"secure_columns": secureCols,
	}
	if len(searchableCols) > 0 {
		resultData["fts_columns"] = searchableCols
	}
	return &Result{Success: true, Data: resultData}, nil
}

// createFTSIndex creates an FTS5 virtual table and sync triggers for the given columns.
func createFTSIndex(db *sql.DB, tableName string, columns []string) error {
	ftsTable := tableName + "_fts"
	colList := strings.Join(columns, ", ")

	// Create FTS5 virtual table.
	ftsSQL := fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(%s, content=%s, content_rowid=id)",
		ftsTable, colList, tableName,
	)
	if _, err := db.Exec(ftsSQL); err != nil {
		return fmt.Errorf("creating FTS5 table: %w", err)
	}

	// Build column references for triggers.
	var newCols, oldCols []string
	for _, c := range columns {
		newCols = append(newCols, "new."+c)
		oldCols = append(oldCols, "old."+c)
	}
	newColList := strings.Join(newCols, ", ")
	oldColList := strings.Join(oldCols, ", ")

	// AFTER INSERT trigger.
	_, err := db.Exec(fmt.Sprintf(
		`CREATE TRIGGER IF NOT EXISTS %s_ai AFTER INSERT ON %s BEGIN
			INSERT INTO %s(rowid, %s) VALUES (new.id, %s);
		END`,
		tableName, tableName, ftsTable, colList, newColList,
	))
	if err != nil {
		return fmt.Errorf("creating INSERT trigger: %w", err)
	}

	// AFTER DELETE trigger.
	_, err = db.Exec(fmt.Sprintf(
		`CREATE TRIGGER IF NOT EXISTS %s_ad AFTER DELETE ON %s BEGIN
			INSERT INTO %s(%s, rowid, %s) VALUES('delete', old.id, %s);
		END`,
		tableName, tableName, ftsTable, ftsTable, colList, oldColList,
	))
	if err != nil {
		return fmt.Errorf("creating DELETE trigger: %w", err)
	}

	// AFTER UPDATE trigger.
	_, err = db.Exec(fmt.Sprintf(
		`CREATE TRIGGER IF NOT EXISTS %s_au AFTER UPDATE ON %s BEGIN
			INSERT INTO %s(%s, rowid, %s) VALUES('delete', old.id, %s);
			INSERT INTO %s(rowid, %s) VALUES (new.id, %s);
		END`,
		tableName, tableName,
		ftsTable, ftsTable, colList, oldColList,
		ftsTable, colList, newColList,
	))
	if err != nil {
		return fmt.Errorf("creating UPDATE trigger: %w", err)
	}

	return nil
}

func (t *SchemaTool) alter(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	if tableName == "" {
		return &Result{Success: false, Error: "table_name is required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Load existing secure columns.
	secureCols, _ := loadSecureColumns(ctx, tableName)
	if secureCols == nil {
		secureCols = map[string]string{}
	}

	var addedCols []string
	var droppedCols []string

	// Add columns.
	if addColumnsRaw, ok := args["add_columns"].(map[string]interface{}); ok {
		for colName, colTypeRaw := range addColumnsRaw {
			if !validColumnName.MatchString(colName) {
				return &Result{Success: false, Error: fmt.Sprintf("invalid column name: %s", colName)}, nil
			}
			colType, ok := colTypeRaw.(string)
			if !ok {
				return &Result{Success: false, Error: fmt.Sprintf("column type must be a string for %s", colName)}, nil
			}
			colType = strings.ToUpper(colType)
			sqlType, allowed := allowedColumnTypes[colType]
			if !allowed {
				return &Result{Success: false, Error: fmt.Sprintf("unsupported column type: %s", colType)}, nil
			}

			alterSQL := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", physicalName, colName, sqlType)
			if _, err := ctx.DB.Exec(alterSQL); err != nil {
				return nil, fmt.Errorf("adding column %s: %w", colName, err)
			}

			// Track secure columns.
			if kind, isSecure := secureColumnKinds[colType]; isSecure {
				secureCols[colName] = kind
			}

			addedCols = append(addedCols, colName)
		}
	}

	// Drop columns (SQLite supports ALTER TABLE DROP COLUMN since 3.35.0).
	if dropColumnsRaw, ok := args["drop_columns"].([]interface{}); ok {
		protectedCols := map[string]bool{"id": true, "created_at": true}

		// Collect valid column names first for the approval check.
		var colsToDrop []string
		for _, colRaw := range dropColumnsRaw {
			colName, ok := colRaw.(string)
			if !ok || colName == "" {
				continue
			}
			if protectedCols[colName] {
				return &Result{Success: false, Error: fmt.Sprintf("cannot drop protected column: %s", colName)}, nil
			}
			colsToDrop = append(colsToDrop, colName)
		}

		// Approval gate for dropping columns (data loss).
		if len(colsToDrop) > 0 {
			approvalKey := fmt.Sprintf("drop_columns:%s:%s", tableName, strings.Join(colsToDrop, ","))
			if !CheckApproval(ctx.DB, approvalKey) {
				question := fmt.Sprintf(
					"The AI wants to drop column(s) %s from table '%s'. Data in these columns will be permanently lost. Approve?",
					strings.Join(colsToDrop, ", "), tableName,
				)
				return RequestApproval(ctx, approvalKey, question,
					fmt.Sprintf("Dropping columns [%s] from table '%s'.", strings.Join(colsToDrop, ", "), tableName),
				), nil
			}
		}

		for _, colName := range colsToDrop {
			alterSQL := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", physicalName, colName)
			if _, err := ctx.DB.Exec(alterSQL); err != nil {
				return nil, fmt.Errorf("dropping column %s: %w", colName, err)
			}

			// Remove from secure columns if present.
			delete(secureCols, colName)
			droppedCols = append(droppedCols, colName)
		}

		if len(droppedCols) > 0 {
			LogDestructiveAction(ctx, "manage_schema", "alter_drop_columns",
				fmt.Sprintf("%s:[%s]", tableName, strings.Join(droppedCols, ",")))
		}
	}

	if len(addedCols) == 0 && len(droppedCols) == 0 {
		return &Result{Success: false, Error: "no columns to add or drop"}, nil
	}

	// Update secure_columns in the registry — fail loudly since losing
	// security metadata means future inserts may store passwords/secrets as plaintext.
	secureColsJSON, err := json.Marshal(secureCols)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to marshal secure columns: %v", err)}, nil
	}
	if _, err := ctx.DB.Exec(
		"UPDATE ho_dynamic_tables SET secure_columns = ? WHERE table_name = ?",
		string(secureColsJSON), tableName,
	); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to update secure columns registry: %v", err)}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"table":   tableName,
		"added":   addedCols,
		"dropped": droppedCols,
	}}, nil
}

func (t *SchemaTool) describe(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	if tableName == "" {
		return &Result{Success: false, Error: "table_name is required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Load secure columns FIRST — before opening the PRAGMA cursor.
	// ctx.DB has MaxOpenConns=1; opening a cursor and then querying
	// the same pool deadlocks (the QueryRow blocks waiting for the
	// connection held by the open rows cursor).
	secureCols, _ := loadSecureColumns(ctx, tableName)

	// Get column info via PRAGMA.
	rows, err := ctx.DB.Query(fmt.Sprintf("PRAGMA table_info(%s)", physicalName))
	if err != nil {
		return nil, fmt.Errorf("describing table: %w", err)
	}
	defer rows.Close()

	var columns []map[string]interface{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			continue
		}

		col := map[string]interface{}{
			"name": name,
			"type": colType,
		}
		if pk == 1 {
			col["primary_key"] = true
		}
		if secureCols != nil {
			if kind, ok := secureCols[name]; ok {
				switch kind {
				case "hash":
					col["secure"] = "PASSWORD"
				case "encrypt":
					col["secure"] = "ENCRYPTED"
				}
			}
		}
		columns = append(columns, col)
	}

	if len(columns) == 0 {
		return &Result{Success: false, Error: "table not found"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"table":   tableName,
		"columns": columns,
	}}, nil
}

func (t *SchemaTool) list(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT table_name, schema_def, created_at FROM ho_dynamic_tables ORDER BY table_name",
	)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()

	var tables []map[string]interface{}
	for rows.Next() {
		var name, schemaDef string
		var createdAt interface{}
		if err := rows.Scan(&name, &schemaDef, &createdAt); err != nil {
			continue
		}
		tables = append(tables, map[string]interface{}{
			"table_name": name,
			"schema":     schemaDef,
			"created_at": createdAt,
		})
	}

	return &Result{Success: true, Data: tables}, nil
}

func (t *SchemaTool) drop(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	if tableName == "" {
		return &Result{Success: false, Error: "table_name is required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Verify the table exists in the registry.
	var exists int
	err = ctx.DB.QueryRow(
		"SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?",
		tableName,
	).Scan(&exists)
	if err != nil || exists == 0 {
		return &Result{Success: false, Error: "table not found"}, nil
	}

	// Count rows so the owner knows how much data will be lost.
	var rowCount int
	ctx.DB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", physicalName)).Scan(&rowCount)

	// --- Approval gate: dropping a table is irreversible ---
	approvalKey := "drop_table:" + tableName
	if !CheckApproval(ctx.DB, approvalKey) {
		question := fmt.Sprintf(
			"The AI wants to DROP the table '%s' (%d rows). This will permanently delete all data in the table and remove related API endpoints. This cannot be undone. Approve?",
			tableName, rowCount,
		)
		return RequestApproval(ctx, approvalKey, question,
			fmt.Sprintf("Dropping table '%s' (%d rows) and its related endpoints.", tableName, rowCount),
		), nil
	}

	// Drop the physical table.
	if _, err := ctx.DB.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", physicalName)); err != nil {
		return nil, fmt.Errorf("dropping table: %w", err)
	}

	// Remove from ho_dynamic_tables registry.
	if _, err := ctx.DB.Exec("DELETE FROM ho_dynamic_tables WHERE table_name = ?", tableName); err != nil {
		return nil, fmt.Errorf("removing table registry entry: %w", err)
	}

	// Remove related API endpoints.
	if _, err := ctx.DB.Exec("DELETE FROM ho_api_endpoints WHERE table_name = ?", tableName); err != nil {
		return nil, fmt.Errorf("removing api endpoints: %w", err)
	}

	// Remove related auth endpoints.
	if _, err := ctx.DB.Exec("DELETE FROM ho_auth_endpoints WHERE table_name = ?", tableName); err != nil {
		return nil, fmt.Errorf("removing auth endpoints: %w", err)
	}

	LogDestructiveAction(ctx, "manage_schema", "drop", tableName)

	return &Result{Success: true, Data: map[string]interface{}{
		"dropped": tableName,
	}}, nil
}

func (t *SchemaTool) addIndex(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	if tableName == "" {
		return nil, fmt.Errorf("table_name is required")
	}
	if !validTableName.MatchString(tableName) {
		return nil, fmt.Errorf("invalid table name: %q", tableName)
	}

	// Validate table exists in ho_dynamic_tables.
	var count int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?", tableName).Scan(&count)
	if count == 0 {
		return nil, fmt.Errorf("table %q not found in ho_dynamic_tables", tableName)
	}

	// Parse index_columns.
	var columns []string
	if raw, ok := args["index_columns"]; ok {
		switch v := raw.(type) {
		case []interface{}:
			for _, c := range v {
				if s, ok := c.(string); ok && validColumnName.MatchString(s) {
					columns = append(columns, s)
				}
			}
		case string:
			if err := json.Unmarshal([]byte(v), &columns); err != nil {
				return nil, fmt.Errorf("index_columns must be an array of column names")
			}
		}
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("index_columns is required (array of column names)")
	}

	unique, _ := args["unique"].(bool)
	indexName := "idx_" + tableName + "_" + strings.Join(columns, "_")
	colList := strings.Join(columns, ", ")

	createStmt := "CREATE INDEX"
	if unique {
		createStmt = "CREATE UNIQUE INDEX"
	}
	stmt := fmt.Sprintf("%s IF NOT EXISTS %s ON %s(%s)", createStmt, indexName, tableName, colList)

	if _, err := ctx.DB.Exec(stmt); err != nil {
		return nil, fmt.Errorf("create index failed: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"index":   indexName,
		"table":   tableName,
		"columns": columns,
		"unique":  unique,
	}}, nil
}

func (t *SchemaTool) MaxResultSize() int { return 8000 }

func (t *SchemaTool) Summarize(result string) string {
	r, dataMap, dataArr, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if dataArr != nil {
		return fmt.Sprintf(`{"success":true,"summary":"Listed %d tables"}`, len(dataArr))
	}
	if tableName, _ := dataMap["table"].(string); tableName != "" {
		if cols, ok := dataMap["columns"].([]interface{}); ok {
			return fmt.Sprintf(`{"success":true,"summary":"Table %s: %d columns"}`, tableName, len(cols))
		}
		return fmt.Sprintf(`{"success":true,"summary":"Table %s created"}`, tableName)
	}
	return summarizeTruncate(result, 300)
}

// ---------------------------------------------------------------------------
// DataTool — manage_data
// ---------------------------------------------------------------------------

// DataTool consolidates query, insert, update, delete, and count operations.
type DataTool struct{}

func (t *DataTool) Name() string { return "manage_data" }
func (t *DataTool) Description() string {
	return "Query, insert, update, delete, count, aggregate, or search table data. Supports bulk insert via 'rows' array. PASSWORD columns auto-hash on insert, ENCRYPTED columns auto-encrypt/decrypt."
}
func (t *DataTool) Guide() string {
	return `### Data Operations (manage_data)
- query: filters=[{"column":"x","op":"=","value":"v"}], order_by={"column":"x","direction":"ASC"}, limit (default 50).
- insert: single row via data={}, bulk via rows=[{},{}]. PASSWORD columns auto-hash, ENCRYPTED auto-encrypt.
- upsert: insert-or-update in one call. Requires data={} and conflict_columns=["col"]. Updates existing row on conflict.
- batch_delete: delete multiple rows by filters=[...]. More efficient than deleting one by one.
- execute_sql: READ-ONLY SELECT queries for complex joins, subqueries, and aggregations. Cannot INSERT/UPDATE/DELETE.
- search: full-text search via query_text on FTS5-indexed columns (set via manage_schema searchable_columns).
- aggregate: function=count|sum|avg|min|max, column, group_by=[]. Use for dashboards and stats.
- export_csv: Export table data as CSV string. Params: table_name, columns (optional array — default all), filters (optional), limit (default 1000, max 5000).
- import_csv: Import CSV data into a table. Params: table_name, csv_data (string with header row + data rows). Max 500 rows. PASSWORD/ENCRYPTED columns are processed automatically.
NOTE: The filters=[{...}] syntax is for tool calls only. In frontend JS, use fetch query params: ?col=val&col__like=search.
`
}
func (t *DataTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"query", "insert", "upsert", "update", "delete", "batch_delete", "count", "aggregate", "search", "execute_sql", "export_csv", "import_csv"},
				"description": "Data operation to perform",
			},
			"query_text": map[string]interface{}{
				"type":        "string",
				"description": "Search query for full-text search action. Searches FTS5-indexed columns.",
			},
			"table_name": map[string]interface{}{
				"type":        "string",
				"description": "Logical table name",
			},
			"filters": map[string]interface{}{
				"type":        "array",
				"description": "Array of filter objects for query/count: [{\"column\": \"name\", \"op\": \"=\", \"value\": \"foo\"}]. Supported ops: =, !=, <, >, <=, >=, LIKE, NOT LIKE, IS NULL, IS NOT NULL",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"column": map[string]interface{}{"type": "string"},
						"op":     map[string]interface{}{"type": "string"},
						"value":  map[string]interface{}{},
					},
				},
			},
			"order_by": map[string]interface{}{
				"type":        "object",
				"description": "Order by specification for query: {\"column\": \"name\", \"direction\": \"ASC\"}",
				"properties": map[string]interface{}{
					"column":    map[string]interface{}{"type": "string"},
					"direction": map[string]interface{}{"type": "string", "enum": []string{"ASC", "DESC"}},
				},
			},
			"limit": map[string]interface{}{"type": "number", "description": "Maximum number of rows for query (default: 50)"},
			"data": map[string]interface{}{
				"type":        "object",
				"description": "Key-value pairs for single-row insert/update",
			},
			"rows": map[string]interface{}{
				"type":        "array",
				"description": "Array of row objects for bulk insert [{col: val}, ...]. Use instead of 'data' to insert multiple rows in one call.",
				"items":       map[string]interface{}{"type": "object"},
			},
			"id":               map[string]interface{}{"type": "number", "description": "Row ID for update/delete"},
			"conflict_columns": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Columns for ON CONFLICT in upsert (e.g. [\"email\"])"},
			"sql":              map[string]interface{}{"type": "string", "description": "Read-only SQL query for execute_sql action (SELECT only)"},
			"function": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"count", "sum", "avg", "min", "max"},
				"description": "Aggregate function for 'aggregate' action",
			},
			"column": map[string]interface{}{
				"type":        "string",
				"description": "Column to aggregate (required for sum/avg/min/max, optional for count)",
			},
			"group_by": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Columns to group by for aggregate action",
			},
			"csv_data": map[string]interface{}{
				"type":        "string",
				"description": "CSV string with header row. For import_csv.",
			},
			"columns": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Columns to include in export. For export_csv (optional — all columns if omitted).",
			},
		},
		"required": []string{"table_name"},
	}
}

func (t *DataTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"query":        t.query,
		"insert":       t.insert,
		"upsert":       t.upsert,
		"update":       t.update,
		"delete":       t.del,
		"batch_delete": t.batchDelete,
		"count":        t.count,
		"aggregate":    t.aggregate,
		"search":       t.search,
		"execute_sql":  t.executeSql,
		"export_csv":   t.exportCSV,
		"import_csv":   t.importCSV,
	}, func(a map[string]interface{}) string {
		if _, has := a["sql"]; has {
			return "execute_sql"
		}
		if _, has := a["query_text"]; has {
			return "search"
		}
		if _, has := a["rows"]; has {
			return "insert"
		}
		if _, hasID := a["id"]; hasID {
			if _, hasData := a["data"]; hasData {
				return "update"
			}
			return "delete"
		}
		if _, has := a["data"]; has {
			return "insert"
		}
		if _, has := a["function"]; has {
			return "aggregate"
		}
		return "query"
	})
}

func (t *DataTool) query(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Load secure columns to know which to strip from results.
	secureCols, _ := loadSecureColumns(ctx, tableName)

	query := fmt.Sprintf("SELECT * FROM %s", physicalName)
	var queryParams []interface{}

	// Structured filters (replaces raw WHERE string)
	if filtersRaw, ok := args["filters"].([]interface{}); ok && len(filtersRaw) > 0 {
		whereClause, params, err := buildWhereClause(filtersRaw)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		query += whereClause
		queryParams = params
	}

	// Structured order_by (replaces raw ORDER BY string)
	if orderByRaw, ok := args["order_by"].(map[string]interface{}); ok {
		col, _ := orderByRaw["column"].(string)
		dir, _ := orderByRaw["direction"].(string)
		if col != "" {
			if !validColumnName.MatchString(col) {
				return &Result{Success: false, Error: fmt.Sprintf("invalid order_by column: %s", col)}, nil
			}
			dir = strings.ToUpper(strings.TrimSpace(dir))
			if dir != "ASC" && dir != "DESC" {
				dir = "ASC"
			}
			query += fmt.Sprintf(" ORDER BY %s %s", col, dir)
		}
	}

	if limit, ok := args["limit"].(float64); ok && limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", int(limit))
	} else {
		query += " LIMIT 50" // default cap to prevent unbounded results
	}

	rows, err := ctx.DB.Query(query, queryParams...)
	if err != nil {
		return nil, fmt.Errorf("querying table: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("getting columns: %w", err)
	}

	var results []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		row := make(map[string]interface{})
		for i, col := range cols {
			// Strip PASSWORD columns entirely — never expose hashes.
			if secureCols != nil && secureCols[col] == "hash" {
				continue
			}
			if f, ok := values[i].(float64); ok && f == float64(int64(f)) {
				row[col] = int64(f)
			} else {
				row[col] = values[i]
			}
		}
		results = append(results, row)
	}

	return &Result{Success: true, Data: results}, nil
}

func (t *DataTool) insert(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	if tableName == "" {
		return &Result{Success: false, Error: "table_name is required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Load secure columns once for all rows.
	secureCols, _ := loadSecureColumns(ctx, tableName)

	// Bulk insert via "rows" parameter.
	if rowsRaw, ok := args["rows"].([]interface{}); ok && len(rowsRaw) > 0 {
		return t.insertBulk(ctx, physicalName, tableName, rowsRaw, secureCols)
	}

	// Single row via "data" parameter.
	data, _ := args["data"].(map[string]interface{})
	if len(data) == 0 {
		return &Result{Success: false, Error: "data or rows parameter is required for insert"}, nil
	}

	return t.insertSingle(ctx, physicalName, tableName, data, secureCols)
}

func (t *DataTool) insertSingle(ctx *ToolContext, physicalName, tableName string, data map[string]interface{}, secureCols map[string]string) (*Result, error) {
	var colNames []string
	var placeholders []string
	var values []interface{}

	for col, val := range data {
		if !validColumnName.MatchString(col) {
			return &Result{Success: false, Error: fmt.Sprintf("invalid column name: %s", col)}, nil
		}
		colNames = append(colNames, col)
		placeholders = append(placeholders, "?")

		if secureCols != nil {
			if kind, isSecure := secureCols[col]; isSecure {
				processed, err := processSecureValue(kind, val, ctx.Encryptor)
				if err != nil {
					return nil, fmt.Errorf("processing secure column %s: %w", col, err)
				}
				val = processed
			}
		}
		values = append(values, val)
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		physicalName, strings.Join(colNames, ", "), strings.Join(placeholders, ", "))

	res, err := ctx.DB.Exec(query, values...)
	if err != nil {
		return nil, fmt.Errorf("inserting row: %w", err)
	}

	id, _ := res.LastInsertId()
	return &Result{Success: true, Data: map[string]interface{}{
		"id":    id,
		"table": tableName,
	}}, nil
}

func (t *DataTool) insertBulk(ctx *ToolContext, physicalName, tableName string, rowsRaw []interface{}, secureCols map[string]string) (*Result, error) {
	if len(rowsRaw) > 100 {
		return &Result{Success: false, Error: "maximum 100 rows per bulk insert"}, nil
	}

	tx, err := sqliteretry.BeginTx(ctx.DB)
	if err != nil {
		return nil, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	var insertedIDs []int64

	for i, rowRaw := range rowsRaw {
		row, ok := rowRaw.(map[string]interface{})
		if !ok {
			return &Result{Success: false, Error: fmt.Sprintf("row %d is not a valid object", i)}, nil
		}

		var colNames []string
		var placeholders []string
		var values []interface{}

		for col, val := range row {
			if !validColumnName.MatchString(col) {
				return &Result{Success: false, Error: fmt.Sprintf("invalid column name in row %d: %s", i, col)}, nil
			}
			colNames = append(colNames, col)
			placeholders = append(placeholders, "?")

			if secureCols != nil {
				if kind, isSecure := secureCols[col]; isSecure {
					processed, err := processSecureValue(kind, val, ctx.Encryptor)
					if err != nil {
						return nil, fmt.Errorf("processing secure column %s in row %d: %w", col, i, err)
					}
					val = processed
				}
			}
			values = append(values, val)
		}

		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			physicalName, strings.Join(colNames, ", "), strings.Join(placeholders, ", "))

		res, err := tx.Exec(query, values...)
		if err != nil {
			return nil, fmt.Errorf("inserting row %d: %w", i, err)
		}

		id, _ := res.LastInsertId()
		insertedIDs = append(insertedIDs, id)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing bulk insert: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"table":    tableName,
		"inserted": len(insertedIDs),
		"first_id": insertedIDs[0],
		"last_id":  insertedIDs[len(insertedIDs)-1],
	}}, nil
}

func (t *DataTool) update(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	idFloat, _ := args["id"].(float64)
	data, _ := args["data"].(map[string]interface{})
	if tableName == "" || len(data) == 0 {
		return &Result{Success: false, Error: "table_name, id, and data are required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Load secure columns to handle hashing/encryption.
	secureCols, _ := loadSecureColumns(ctx, tableName)

	var setClauses []string
	var values []interface{}

	for col, val := range data {
		if !validColumnName.MatchString(col) {
			return &Result{Success: false, Error: fmt.Sprintf("invalid column name: %s", col)}, nil
		}

		// Process secure columns.
		if secureCols != nil {
			if kind, isSecure := secureCols[col]; isSecure {
				processed, err := processSecureValue(kind, val, ctx.Encryptor)
				if err != nil {
					return nil, fmt.Errorf("processing secure column %s: %w", col, err)
				}
				val = processed
			}
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = ?", col))
		values = append(values, val)
	}
	values = append(values, int64(idFloat))

	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?",
		physicalName, strings.Join(setClauses, ", "))

	res, err := ctx.DB.Exec(query, values...)
	if err != nil {
		return nil, fmt.Errorf("updating row: %w", err)
	}

	n, _ := res.RowsAffected()
	return &Result{Success: true, Data: map[string]interface{}{
		"rows_affected": n,
	}}, nil
}

func (t *DataTool) del(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	idFloat, _ := args["id"].(float64)
	if tableName == "" {
		return &Result{Success: false, Error: "table_name and id are required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	res, err := ctx.DB.Exec(fmt.Sprintf("DELETE FROM %s WHERE id = ?", physicalName), int64(idFloat))
	if err != nil {
		return nil, fmt.Errorf("deleting row: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "row not found"}, nil
	}

	LogDestructiveAction(ctx, "manage_data", "delete",
		fmt.Sprintf("%s id=%d", tableName, int64(idFloat)))

	return &Result{Success: true, Data: map[string]interface{}{
		"deleted_id": int64(idFloat),
	}}, nil
}

func (t *DataTool) count(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	if tableName == "" {
		return &Result{Success: false, Error: "table_name is required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", physicalName)
	var queryParams []interface{}

	if filtersRaw, ok := args["filters"].([]interface{}); ok && len(filtersRaw) > 0 {
		whereClause, params, err := buildWhereClause(filtersRaw)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		query += whereClause
		queryParams = params
	}

	var count int
	err = ctx.DB.QueryRow(query, queryParams...).Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("counting rows: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"table": tableName,
		"count": count,
	}}, nil
}

// allowedAggFuncs is the whitelist of SQL aggregate functions.
var allowedAggFuncs = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
}

func (t *DataTool) aggregate(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	if tableName == "" {
		return &Result{Success: false, Error: "table_name is required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	fn, _ := args["function"].(string)
	fn = strings.ToLower(strings.TrimSpace(fn))
	if !allowedAggFuncs[fn] {
		return &Result{Success: false, Error: "function is required: count, sum, avg, min, or max"}, nil
	}

	col, _ := args["column"].(string)
	if fn != "count" && col == "" {
		return &Result{Success: false, Error: fmt.Sprintf("%s requires a column parameter", fn)}, nil
	}
	if col != "" && !validColumnName.MatchString(col) {
		return &Result{Success: false, Error: fmt.Sprintf("invalid column name: %s", col)}, nil
	}

	// Build aggregate expression.
	aggExpr := "COUNT(*)"
	if fn != "count" {
		aggExpr = fmt.Sprintf("%s(%s)", strings.ToUpper(fn), col)
	} else if col != "" {
		aggExpr = fmt.Sprintf("COUNT(%s)", col)
	}

	// Parse group_by columns.
	var groupCols []string
	if groupByRaw, ok := args["group_by"].([]interface{}); ok {
		for _, g := range groupByRaw {
			gc, _ := g.(string)
			if gc == "" || !validColumnName.MatchString(gc) {
				return &Result{Success: false, Error: fmt.Sprintf("invalid group_by column: %v", g)}, nil
			}
			groupCols = append(groupCols, gc)
		}
	}

	// Build query.
	var selectParts []string
	for _, gc := range groupCols {
		selectParts = append(selectParts, gc)
	}
	selectParts = append(selectParts, aggExpr+" AS result")

	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectParts, ", "), physicalName)
	var queryParams []interface{}

	// Apply filters.
	if filtersRaw, ok := args["filters"].([]interface{}); ok && len(filtersRaw) > 0 {
		whereClause, params, err := buildWhereClause(filtersRaw)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		query += whereClause
		queryParams = params
	}

	if len(groupCols) > 0 {
		query += " GROUP BY " + strings.Join(groupCols, ", ")
		query += " ORDER BY " + strings.Join(groupCols, ", ")
	}

	// Execute.
	if len(groupCols) == 0 {
		// Single result (no grouping).
		var result float64
		err := ctx.DB.QueryRow(query, queryParams...).Scan(&result)
		if err != nil {
			return nil, fmt.Errorf("aggregate query: %w", err)
		}
		return &Result{Success: true, Data: map[string]interface{}{
			"table":    tableName,
			"function": fn,
			"result":   result,
		}}, nil
	}

	// Grouped results.
	rows, err := ctx.DB.Query(query, queryParams...)
	if err != nil {
		return nil, fmt.Errorf("aggregate query: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		// Build scan targets: one per group_by column + the result.
		scanTargets := make([]interface{}, len(groupCols)+1)
		values := make([]interface{}, len(groupCols)+1)
		for i := range scanTargets {
			scanTargets[i] = &values[i]
		}

		if err := rows.Scan(scanTargets...); err != nil {
			continue
		}

		row := make(map[string]interface{})
		for i, gc := range groupCols {
			row[gc] = values[i]
		}
		row["result"] = values[len(groupCols)]
		results = append(results, row)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"table":    tableName,
		"function": fn,
		"data":     results,
	}}, nil
}

func (t *DataTool) upsert(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	data, _ := args["data"].(map[string]interface{})
	if len(data) == 0 {
		return &Result{Success: false, Error: "data is required for upsert"}, nil
	}

	conflictCols, _ := args["conflict_columns"].([]interface{})
	if len(conflictCols) == 0 {
		return &Result{Success: false, Error: "conflict_columns is required for upsert"}, nil
	}

	// Validate conflict columns.
	var conflictNames []string
	for _, c := range conflictCols {
		if s, ok := c.(string); ok && validColumnName.MatchString(s) {
			conflictNames = append(conflictNames, s)
		}
	}
	if len(conflictNames) == 0 {
		return &Result{Success: false, Error: "invalid conflict_columns"}, nil
	}

	// Process secure values.
	secureCols, _ := loadSecureColumns(ctx, tableName)
	for col, kind := range secureCols {
		if val, ok := data[col]; ok {
			processed, err := processSecureValue(kind, val, ctx.Encryptor)
			if err != nil {
				return &Result{Success: false, Error: fmt.Sprintf("processing %s: %v", col, err)}, nil
			}
			data[col] = processed
		}
	}

	var cols, placeholders, updateClauses []string
	var vals []interface{}
	for col, val := range data {
		if !validColumnName.MatchString(col) || col == "id" {
			continue
		}
		cols = append(cols, col)
		placeholders = append(placeholders, "?")
		vals = append(vals, val)
		// Don't update conflict columns themselves.
		isConflict := false
		for _, cc := range conflictNames {
			if cc == col {
				isConflict = true
				break
			}
		}
		if !isConflict {
			updateClauses = append(updateClauses, fmt.Sprintf("%s = excluded.%s", col, col))
		}
	}

	if len(updateClauses) == 0 {
		// All columns are conflict columns — just do INSERT OR IGNORE.
		query := fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)",
			physicalName, strings.Join(cols, ","), strings.Join(placeholders, ","))
		_, err := ctx.DB.Exec(query, vals...)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("upsert error: %v", err)}, nil
		}
		return &Result{Success: true, Data: "upserted (insert or ignore)"}, nil
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(%s) DO UPDATE SET %s",
		physicalName,
		strings.Join(cols, ","),
		strings.Join(placeholders, ","),
		strings.Join(conflictNames, ","),
		strings.Join(updateClauses, ","),
	)

	result, err := ctx.DB.Exec(query, vals...)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("upsert error: %v", err)}, nil
	}
	id, _ := result.LastInsertId()
	return &Result{Success: true, Data: map[string]interface{}{"id": id}}, nil
}

func (t *DataTool) batchDelete(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	filters, _ := args["filters"].([]interface{})
	if len(filters) == 0 {
		return &Result{Success: false, Error: "filters required for batch_delete (safety: cannot delete all rows without filters)"}, nil
	}

	query := fmt.Sprintf("DELETE FROM %s", physicalName)
	var conditions []string
	var vals []interface{}
	for _, f := range filters {
		fm, ok := f.(map[string]interface{})
		if !ok {
			continue
		}
		col, _ := fm["column"].(string)
		op, _ := fm["op"].(string)
		if !validColumnName.MatchString(col) || !allowedOps[strings.ToUpper(op)] {
			continue
		}
		op = strings.ToUpper(op)
		if op == "IS NULL" || op == "IS NOT NULL" {
			conditions = append(conditions, fmt.Sprintf("%s %s", col, op))
		} else {
			conditions = append(conditions, fmt.Sprintf("%s %s ?", col, op))
			vals = append(vals, fm["value"])
		}
	}

	if len(conditions) == 0 {
		return &Result{Success: false, Error: "no valid filters provided"}, nil
	}

	query += " WHERE " + strings.Join(conditions, " AND ")
	result, err := ctx.DB.Exec(query, vals...)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("batch delete error: %v", err)}, nil
	}
	affected, _ := result.RowsAffected()

	LogDestructiveAction(ctx, "manage_data", "batch_delete", fmt.Sprintf("%s: %d rows", tableName, affected))

	return &Result{Success: true, Data: map[string]interface{}{
		"table":   tableName,
		"deleted": affected,
	}}, nil
}

func (t *DataTool) executeSql(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	sqlQuery, _ := args["sql"].(string)
	if sqlQuery == "" {
		return &Result{Success: false, Error: "sql is required"}, nil
	}

	// Only allow SELECT queries for safety.
	trimmed := strings.TrimSpace(sqlQuery)
	if !strings.HasPrefix(strings.ToUpper(trimmed), "SELECT") {
		return &Result{Success: false, Error: "execute_sql only supports SELECT queries"}, nil
	}

	// Block access to system tables to prevent leaking secrets or internal state.
	lowerSQL := strings.ToLower(trimmed)
	for sysTable := range systemTables {
		if strings.Contains(lowerSQL, sysTable) {
			return &Result{Success: false, Error: fmt.Sprintf("cannot query system table: %s", sysTable)}, nil
		}
	}

	limit := 100
	if l, ok := args["limit"].(float64); ok && l > 0 && l <= 500 {
		limit = int(l)
	}

	// Append LIMIT if not already present.
	upper := strings.ToUpper(trimmed)
	if !strings.Contains(upper, "LIMIT") {
		sqlQuery = fmt.Sprintf("%s LIMIT %d", trimmed, limit)
	}

	rows, err := ctx.DB.Query(sqlQuery)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("SQL error: %v", err)}, nil
	}
	defer rows.Close()

	results := scanRowsMaps(rows)
	return &Result{Success: true, Data: map[string]interface{}{
		"count":   len(results),
		"results": results,
	}}, nil
}

func (t *DataTool) search(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	queryText, _ := args["query_text"].(string)
	if tableName == "" || queryText == "" {
		return &Result{Success: false, Error: "table_name and query_text are required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	ftsTable := physicalName + "_fts"

	// Check that the FTS table exists.
	var cnt int
	err = ctx.DB.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?",
		ftsTable,
	).Scan(&cnt)
	if err != nil || cnt == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("no full-text search index for table %s — create with searchable_columns in manage_schema", tableName)}, nil
	}

	limit := 50
	if l, ok := args["limit"].(float64); ok && l > 0 && l <= 200 {
		limit = int(l)
	}

	// Query FTS5 with ranking.
	query := fmt.Sprintf(
		"SELECT t.* FROM %s t JOIN %s f ON t.id = f.rowid WHERE %s MATCH ? ORDER BY f.rank LIMIT ?",
		physicalName, ftsTable, ftsTable,
	)

	rows, err := ctx.DB.Query(query, queryText, limit)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("search error: %v", err)}, nil
	}
	defer rows.Close()

	results := scanRowsMaps(rows)

	// Strip secure columns.
	secureCols, _ := loadSecureColumns(ctx, tableName)
	stripSecureCols(results, secureCols)

	return &Result{Success: true, Data: map[string]interface{}{
		"query":   queryText,
		"count":   len(results),
		"results": results,
	}}, nil
}

// scanRowsMaps scans rows into a slice of maps (generic column scanning).
func scanRowsMaps(rows *sql.Rows) []map[string]interface{} {
	cols, err := rows.Columns()
	if err != nil {
		return nil
	}
	var results []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		row := make(map[string]interface{})
		for i, col := range cols {
			if f, ok := values[i].(float64); ok && f == float64(int64(f)) {
				row[col] = int64(f)
			} else {
				row[col] = values[i]
			}
		}
		results = append(results, row)
	}
	return results
}

// stripSecureCols removes hash/encrypt columns from result maps.
func stripSecureCols(results []map[string]interface{}, secureCols map[string]string) {
	if len(secureCols) == 0 {
		return
	}
	for _, row := range results {
		for col, kind := range secureCols {
			if kind == "hash" {
				delete(row, col)
			}
		}
	}
}

func (t *DataTool) MaxResultSize() int { return 12000 }

func (t *DataTool) Summarize(result string) string {
	r, _, dataArr, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if dataArr != nil {
		return fmt.Sprintf(`{"success":true,"summary":"Queried: %d rows returned"}`, len(dataArr))
	}
	return summarizeTruncate(result, 300)
}

// --- CSV export/import ---

func (t *DataTool) exportCSV(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, errResult := RequireString(args, "table_name")
	if errResult != nil {
		return errResult, nil
	}
	tableName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	limit := OptionalInt(args, "limit", 500)
	if limit > 5000 {
		limit = 5000
	}

	// Build column list.
	colStar := "*"
	if colsRaw, ok := args["columns"].([]interface{}); ok && len(colsRaw) > 0 {
		var cols []string
		for _, c := range colsRaw {
			if col, ok := c.(string); ok && col != "" {
				cols = append(cols, col)
			}
		}
		if len(cols) > 0 {
			colStar = strings.Join(cols, ", ")
		}
	}

	// Build optional WHERE clause.
	query := fmt.Sprintf("SELECT %s FROM %s", colStar, tableName)
	var queryArgs []interface{}
	if filtersRaw, ok := args["filters"].([]interface{}); ok && len(filtersRaw) > 0 {
		where, wArgs, wErr := buildWhereClause(filtersRaw)
		if wErr != nil {
			return &Result{Success: false, Error: wErr.Error()}, nil
		}
		query += " WHERE " + where
		queryArgs = append(queryArgs, wArgs...)
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := ctx.DB.Query(query, queryArgs...)
	if err != nil {
		return &Result{Success: false, Error: "query failed: " + err.Error()}, nil
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) == 0 {
		return &Result{Success: false, Error: "no columns in result"}, nil
	}

	// Strip secure columns from output.
	secureCols, _ := loadSecureColumns(ctx, tableName)

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	// Write header (excluding PASSWORD columns).
	var headerCols []string
	var includeCol []bool
	for _, col := range cols {
		if secureCols != nil && secureCols[col] == "hash" {
			includeCol = append(includeCol, false)
		} else {
			headerCols = append(headerCols, col)
			includeCol = append(includeCol, true)
		}
	}
	writer.Write(headerCols)

	// Write data rows.
	rowCount := 0
	values := make([]interface{}, len(cols))
	scanDest := make([]interface{}, len(cols))
	for i := range values {
		scanDest[i] = &values[i]
	}

	for rows.Next() {
		if rows.Scan(scanDest...) != nil {
			continue
		}
		var record []string
		for i, v := range values {
			if !includeCol[i] {
				continue
			}
			record = append(record, fmt.Sprintf("%v", v))
		}
		writer.Write(record)
		rowCount++
	}
	writer.Flush()

	return &Result{Success: true, Data: map[string]interface{}{
		"table": tableName,
		"rows":  rowCount,
		"csv":   buf.String(),
	}}, nil
}

func (t *DataTool) importCSV(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, errResult := RequireString(args, "table_name")
	if errResult != nil {
		return errResult, nil
	}
	tableName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	csvData, errResult := RequireString(args, "csv_data")
	if errResult != nil {
		return errResult, nil
	}

	reader := csv.NewReader(strings.NewReader(csvData))
	records, err := reader.ReadAll()
	if err != nil {
		return &Result{Success: false, Error: "invalid CSV: " + err.Error()}, nil
	}
	if len(records) < 2 {
		return &Result{Success: false, Error: "CSV must have at least a header row and one data row"}, nil
	}

	headers := records[0]
	dataRows := records[1:]

	if len(dataRows) > 500 {
		return &Result{Success: false, Error: fmt.Sprintf("too many rows: %d (max 500)", len(dataRows))}, nil
	}

	// Validate column names.
	for _, col := range headers {
		if err := security.ValidateColumnName(col); err != nil {
			return &Result{Success: false, Error: "invalid column name: " + col}, nil
		}
	}

	// Exclude 'id' column — auto-generated.
	colIndexes := make([]int, 0, len(headers))
	colNames := make([]string, 0, len(headers))
	for i, col := range headers {
		if strings.ToLower(col) == "id" {
			continue
		}
		colIndexes = append(colIndexes, i)
		colNames = append(colNames, col)
	}

	if len(colNames) == 0 {
		return &Result{Success: false, Error: "no valid columns found in CSV header"}, nil
	}

	placeholders := strings.Repeat("?, ", len(colNames))
	placeholders = placeholders[:len(placeholders)-2] // trim trailing ", "
	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		tableName, strings.Join(colNames, ", "), placeholders)

	// Load secure columns for processing.
	secureCols, _ := loadSecureColumns(ctx, tableName)

	// Insert in a transaction.
	tx, err := sqliteretry.BeginTx(ctx.DB)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	inserted := 0
	for _, row := range dataRows {
		values := make([]interface{}, len(colNames))
		for j, idx := range colIndexes {
			var val interface{} = ""
			if idx < len(row) {
				val = row[idx]
			}
			// Process secure columns.
			if secureCols != nil {
				if kind, ok := secureCols[colNames[j]]; ok {
					processed, pErr := processSecureValue(kind, val, ctx.Encryptor)
					if pErr == nil {
						val = processed
					}
				}
			}
			values[j] = val
		}
		if _, err := tx.Exec(insertSQL, values...); err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("row %d: %s", inserted+1, err.Error())}, nil
		}
		inserted++
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"table":    tableName,
		"inserted": inserted,
	}}, nil
}
