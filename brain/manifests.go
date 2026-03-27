/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// --- Manifest types: ground-truth snapshots extracted from DB after each build sub-phase ---

type SchemaManifest struct {
	Tables []ManifestTable `json:"tables"`
}

type ManifestTable struct {
	Name    string           `json:"name"`
	Columns []ManifestColumn `json:"columns"`
}

type ManifestColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type EndpointManifest struct {
	API    []ManifestAPI            `json:"api"`
	Auth   []ManifestAuth           `json:"auth,omitempty"`
	WS     []ManifestSimpleEndpoint `json:"ws,omitempty"`
	Stream []ManifestSimpleEndpoint `json:"stream,omitempty"`
	Upload []ManifestSimpleEndpoint `json:"upload,omitempty"`
}

type ManifestAPI struct {
	Path          string   `json:"path"`
	Table         string   `json:"table"`
	Columns       []string `json:"columns"`
	Methods       string   `json:"methods,omitempty"`
	PublicColumns []string `json:"public_columns,omitempty"`
	RequiresAuth  bool     `json:"requires_auth,omitempty"`
	PublicRead    bool     `json:"public_read,omitempty"`
}

type ManifestAuth struct {
	Path           string `json:"path"`
	Table          string `json:"table"`
	UsernameColumn string `json:"username_column"`
}

// ManifestSimpleEndpoint represents a path+table endpoint (used for WS, Stream, Upload).
type ManifestSimpleEndpoint struct {
	Path  string `json:"path"`
	Table string `json:"table,omitempty"`
}

// --- Extraction functions: query real DB state after each sub-phase ---

// extractSchemaManifest reads all dynamic tables and their columns from the DB.
func extractSchemaManifest(db *sql.DB) (*SchemaManifest, error) {
	rows, err := db.Query("SELECT table_name FROM ho_dynamic_tables ORDER BY table_name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var manifest SchemaManifest
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			continue
		}
		cols, err := getTableColumnsTyped(db, tableName)
		if err != nil {
			slog.Warn("failed to read columns", "table", tableName, "error", err)
			continue
		}
		manifest.Tables = append(manifest.Tables, ManifestTable{
			Name:    tableName,
			Columns: cols,
		})
	}
	return &manifest, nil
}

// getTableColumnsTyped returns column names and types for a table via PRAGMA.
func getTableColumnsTyped(db *sql.DB, tableName string) ([]ManifestColumn, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return nil, fmt.Errorf("PRAGMA table_info(%s): %w", tableName, err)
	}
	defer rows.Close()

	var cols []ManifestColumn
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			continue
		}
		cols = append(cols, ManifestColumn{Name: name, Type: colType})
	}
	return cols, nil
}

// extractEndpointManifest reads all endpoint types from the DB and resolves their bound table columns.
func extractEndpointManifest(db *sql.DB) (*EndpointManifest, error) {
	var manifest EndpointManifest

	// API endpoints.
	apiRows, err := db.Query("SELECT path, table_name, methods, requires_auth, public_read, COALESCE(public_columns, '') FROM ho_api_endpoints ORDER BY path")
	if err == nil {
		defer apiRows.Close()
		for apiRows.Next() {
			var path, tableName string
			var methods, publicColumnsJSON sql.NullString
			var requiresAuth, publicRead bool
			apiRows.Scan(&path, &tableName, &methods, &requiresAuth, &publicRead, &publicColumnsJSON)
			typedCols, _ := getTableColumnsTyped(db, tableName)
			colNames := make([]string, 0, len(typedCols))
			for _, c := range typedCols {
				colNames = append(colNames, c.Name)
			}
			api := ManifestAPI{
				Path:         path,
				Table:        tableName,
				Columns:      colNames,
				RequiresAuth: requiresAuth,
				PublicRead:   publicRead,
			}
			if methods.Valid {
				api.Methods = methods.String
			}
			// Parse public_columns JSON array.
			if publicColumnsJSON.Valid && publicColumnsJSON.String != "" {
				var pubCols []string
				if json.Unmarshal([]byte(publicColumnsJSON.String), &pubCols) == nil && len(pubCols) > 0 {
					api.PublicColumns = pubCols
				}
			}
			manifest.API = append(manifest.API, api)
		}
	}

	// Auth endpoints.
	authRows, err := db.Query("SELECT path, table_name, username_column FROM ho_auth_endpoints ORDER BY path")
	if err == nil {
		defer authRows.Close()
		for authRows.Next() {
			var path, tableName, usernameCol string
			authRows.Scan(&path, &tableName, &usernameCol)
			manifest.Auth = append(manifest.Auth, ManifestAuth{
				Path:           path,
				Table:          tableName,
				UsernameColumn: usernameCol,
			})
		}
	}

	// Simple path+table endpoints (WS, Stream, Upload).
	type simpleEndpointQuery struct {
		query string
		dest  *[]ManifestSimpleEndpoint
	}
	simpleQueries := []simpleEndpointQuery{
		{"SELECT path, COALESCE(write_to_table, '') FROM ho_ws_endpoints ORDER BY path", &manifest.WS},
		{"SELECT path, '' FROM ho_stream_endpoints ORDER BY path", &manifest.Stream},
		{"SELECT path, COALESCE(table_name, '') FROM ho_upload_endpoints ORDER BY path", &manifest.Upload},
	}
	for _, sq := range simpleQueries {
		rows, err := db.Query(sq.query)
		if err != nil {
			continue
		}
		for rows.Next() {
			var path, table string
			rows.Scan(&path, &table)
			*sq.dest = append(*sq.dest, ManifestSimpleEndpoint{Path: path, Table: table})
		}
		rows.Close()
	}

	return &manifest, nil
}

// --- Manifest serialization for prompt injection ---

// queryStringColumn runs a single-column query and collects results into a string slice.
func queryStringColumn(db *sql.DB, query string) []string {
	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var val string
		if rows.Scan(&val) == nil {
			result = append(result, val)
		}
	}
	return result
}

// buildCrashRecoveryManifest queries the DB for everything already built and
// returns a compact text block the BUILD prompt can inject so the LLM knows
// what to skip on resume. Returns "" if nothing has been built yet.
func buildCrashRecoveryManifest(db *sql.DB) string {
	var parts []string

	// Tables.
	if schema, err := extractSchemaManifest(db); err == nil && len(schema.Tables) > 0 {
		var names []string
		for _, t := range schema.Tables {
			var cols []string
			for _, c := range t.Columns {
				cols = append(cols, c.Name)
			}
			names = append(names, fmt.Sprintf("%s(%s)", t.Name, strings.Join(cols, ",")))
		}
		parts = append(parts, "Tables: "+strings.Join(names, "; "))
	}

	// Endpoints.
	if ep, err := extractEndpointManifest(db); err == nil {
		var epParts []string
		for _, a := range ep.API {
			epParts = append(epParts, "API:"+a.Path+"→"+a.Table)
		}
		for _, a := range ep.Auth {
			epParts = append(epParts, "Auth:"+a.Path+"→"+a.Table)
		}
		for _, w := range ep.WS {
			epParts = append(epParts, "WS:"+w.Path)
		}
		for _, s := range ep.Stream {
			epParts = append(epParts, "Stream:"+s.Path)
		}
		for _, u := range ep.Upload {
			epParts = append(epParts, "Upload:"+u.Path)
		}
		if len(epParts) > 0 {
			parts = append(parts, "Endpoints: "+strings.Join(epParts, "; "))
		}
	}

	// Pages.
	pageRows, err := db.Query("SELECT path, title FROM ho_pages WHERE is_deleted = 0 ORDER BY path")
	if err == nil {
		defer pageRows.Close()
		var pages []string
		for pageRows.Next() {
			var path, title string
			pageRows.Scan(&path, &title)
			pages = append(pages, path+" ("+title+")")
		}
		if len(pages) > 0 {
			parts = append(parts, "Pages: "+strings.Join(pages, "; "))
		}
	}

	// Layouts.
	if layouts := queryStringColumn(db, "SELECT name FROM ho_layouts ORDER BY name"); len(layouts) > 0 {
		parts = append(parts, "Layouts: "+strings.Join(layouts, ", "))
	}

	// CSS files.
	if css := queryStringColumn(db, "SELECT filename FROM ho_assets WHERE filename LIKE '%.css' ORDER BY filename"); len(css) > 0 {
		parts = append(parts, "CSS: "+strings.Join(css, ", "))
	}

	if len(parts) == 0 {
		return ""
	}
	return "## Already Built (resume from here — do NOT recreate these)\n" + strings.Join(parts, "\n")
}
