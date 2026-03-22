/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/markdr-hue/HO/security"
)

// ---------------------------------------------------------------------------
// EndpointsTool — unified manage_endpoints tool
// ---------------------------------------------------------------------------

// EndpointsTool consolidates all endpoint management into a single tool.
// Actions: create/update/list/delete for api, auth, upload, stream, websocket,
// and llm endpoints, plus create/list/delete for oauth, verify_password, and
// generate_docs.
type EndpointsTool struct{}

func (t *EndpointsTool) Name() string { return "manage_endpoints" }
func (t *EndpointsTool) Description() string {
	return "Create, update, list, or delete API, auth, upload, stream, websocket, LLM, and oauth endpoints. Generate OpenAPI docs."
}

func (t *EndpointsTool) Guide() string {
	return `### manage_endpoints

**Creating endpoints:**
- **create_api**: CRUD REST. GET /api/{path} (list: {data, count, limit, offset}), GET /{id}, POST, PUT, DELETE. Filtering: ?col=val, ?col__like=, ?col__gt=, ?q=. Column selection: ?fields=col1,col2 (returns only requested columns + id).
- **create_auth**: JWT auth. POST /api/{path}/login, /register -> {token}. GET /api/{path}/me -> user. Bearer token required.
- **create_llm**: AI endpoint with TWO routes. POST /api/{path}/chat streams SSE tokens (for chatbots/assistants). POST /api/{path}/complete returns full JSON {content, model, usage, stop_reason} (for content generators, code generators, classifiers). Choose the route based on the use case — use /chat when tokens should appear in real time, use /complete when you need the full response before rendering or processing it. Params: system_prompt (required), max_tokens (default 4096; use 8192+ for code generation), temperature (default 0.7), max_history (default 20), rate_limit (requests/min/IP, default 10), requires_auth, cors_origins.
- **create_websocket**: Bidirectional relay. Connect: ws(s)://host/api/{path}/ws?room=X. The server sends TWO kinds of messages that your JS must handle separately:
  1. **System messages** (from the server): have _type field. {_type:"join", _sender:"UUID", _clients:N} when someone joins, {_type:"leave", _sender:"UUID", _clients:N} when someone leaves. Use _clients to know how many are connected.
  2. **User messages** (from other clients): have whatever fields the sender included, plus _sender:"UUID" injected by the server.
  **Echo suppression**: your own messages are NOT sent back to you. Update UI optimistically after send. Only incoming messages are from OTHER clients.
  **Multiplayer pattern**: on connect, send {type:"join", playerId:"...", name:"..."}. On receiving messages, check if msg._type exists (system) vs msg.type (user). For matchmaking, use _clients count from system join messages to know when enough players are connected.
- **create_stream**: SSE. new EventSource('/api/{path}/stream').
- **create_upload**: Multipart POST /api/{path}/upload -> {url, filename, size, type}. Optional table_name auto-persists uploads.
- **create_llm does NOT provide CRUD**. If a page needs data listing AND AI features, create BOTH a create_api (for CRUD) and a create_llm (for AI chat/completion).
- **create_oauth**: Requires provider_name, client_id, client_secret, authorize_url, token_url, userinfo_url, scopes, auth_path (the auth endpoint to link).
- **CORS**: Set cors_origins, cors_methods, cors_headers on create_api/update_api to enable cross-origin requests. Use cors_origins="*" for any origin. Preflight OPTIONS requests are handled automatically.
- **Row-Level Security**: Set owner_column on create_api (e.g. owner_column="user_id") to scope data per user. GET returns only rows where owner_column matches the JWT user. POST auto-sets the column. PUT/DELETE only affect owned rows. Admin role bypasses the filter.

**Updating endpoints:**
- **update_api/update_auth/update_upload/update_stream/update_websocket**: Pass the same path + only the fields you want to change (methods, requires_auth, public_read, rate_limit, etc.). Omitted fields keep their current values. You cannot change table_name or action type after creation.

**Required params:** Always provide action + path. For create_api/create_auth: also provide table_name. For create_auth: also username_column.

**Frontend JS — how to call these endpoints:**
- List: fetch('/api/{path}') -> {data: [...], count, limit, offset}
- Get one: fetch('/api/{path}/123') -> row object
- Create: fetch('/api/{path}', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({...})})
- Update: fetch('/api/{path}/123', {method:'PUT', headers:{'Content-Type':'application/json'}, body: JSON.stringify({...})})
- Delete: fetch('/api/{path}/123', {method:'DELETE'})
- Login/Register: fetch('/api/{path}/login', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({[username_column]: value, [password_column]: value})}) -> {token}. IMPORTANT: Use the EXACT column names from create_auth (e.g., if username_column="email", send {"email":"...", "password":"..."}).
- Auth header: {headers: {'Authorization': 'Bearer ' + token}}
- Filter/search: fetch('/api/{path}?col=val&col__like=search&q=text') — always use query params, never the filters=[{...}] syntax (that's for tools only).
- Upload: const fd = new FormData(); fd.append('file', fileInput.files[0]); fetch('/api/{path}/upload', {method:'POST', body: fd})

**Auto-generated API docs:**
- **generate_docs**: Scans all endpoints, generates OpenAPI 3.0 spec, saves as api-docs.json asset, and creates a Swagger UI page at /api/docs. No parameters needed.

**During build:** To insert/query data, use manage_data — not make_http_request against your own endpoints.`
}

func (t *EndpointsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"create_api", "update_api", "list_api", "delete_api", "create_auth", "update_auth", "list_auth", "delete_auth", "create_upload", "update_upload", "list_upload", "delete_upload", "create_stream", "update_stream", "list_stream", "delete_stream", "create_websocket", "update_websocket", "list_websocket", "delete_websocket", "verify_password", "create_oauth", "list_oauth", "delete_oauth", "generate_docs", "create_llm", "update_llm", "list_llm", "delete_llm"},
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "API path (e.g. 'contacts' becomes /api/contacts, 'auth' becomes /api/auth/register etc.)",
			},
			"table_name": map[string]interface{}{
				"type":        "string",
				"description": "Dynamic table to map to (must already exist). Required for create_api, create_auth, and verify_password. Optional for create_upload: auto-inserts (filename, url, content_type, size) per upload — table must have those columns.",
			},
			"methods": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string", "enum": []string{"GET", "POST", "PUT", "DELETE"}},
				"description": "Allowed HTTP methods (default: GET, POST, PUT, DELETE). Only for create_api.",
			},
			"public_columns": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Columns visible in responses. PASSWORD columns are always hidden. For create_auth: columns for JWT claims and /me.",
			},
			"requires_auth": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, requests must include a valid bearer token (default: false).",
			},
			"public_read": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, GET requests are allowed without auth even when requires_auth=true. Use for public-readable data (forums, products, articles). Default: false. Only for create_api.",
			},
			"rate_limit": map[string]interface{}{
				"type":        "number",
				"description": "Max requests per minute per IP. Default: 60 for API, 10 for LLM. For create_api and create_llm.",
			},
			"cache_ttl": map[string]interface{}{
				"type":        "number",
				"description": "Response cache TTL in seconds for GET requests (0 = no caching, default). Cache auto-invalidates on POST/PUT/DELETE. For create_api.",
			},
			"username_column": map[string]interface{}{
				"type":        "string",
				"description": "Column used as the unique username/email for login (e.g. 'email'). Required for create_auth.",
			},
			"password_column": map[string]interface{}{
				"type":        "string",
				"description": "Column with PASSWORD type (default: 'password'). For create_auth and verify_password.",
			},
			"allowed_types": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Allowed MIME types for uploads (e.g. [\"image/*\", \"application/pdf\"]). Glob patterns supported. Only for create_upload.",
			},
			"event_types": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Event types to broadcast (e.g. [\"data.insert\", \"data.update\", \"data.delete\"]). For create_stream only.",
			},
			"max_size_mb": map[string]interface{}{
				"type":        "number",
				"description": "Max file size in MB (default: 5). Only for create_upload.",
			},
			"id": map[string]interface{}{
				"type":        "number",
				"description": "Row ID to check. Required for verify_password.",
			},
			"password": map[string]interface{}{
				"type":        "string",
				"description": "Plaintext password to verify. Required for verify_password.",
			},
			"required_role": map[string]interface{}{
				"type":        "string",
				"description": "Role required to access API endpoint (e.g. 'admin'). Implies requires_auth=true. Only for create_api.",
			},
			"owner_column": map[string]interface{}{
				"type":        "string",
				"description": "Column for row-level security (e.g. 'user_id'). GET returns only owned rows, POST auto-sets it, PUT/DELETE scope to owned rows. Admin role bypasses. Implies requires_auth. Only for create_api/update_api.",
			},
			"default_role": map[string]interface{}{
				"type":        "string",
				"description": "Default role assigned to new users on registration (default: 'user'). Only for create_auth.",
			},
			"role_column": map[string]interface{}{
				"type":        "string",
				"description": "Column in user table storing the role (default: 'role'). Only for create_auth.",
			},
			"provider_name": map[string]interface{}{
				"type":        "string",
				"description": "OAuth provider identifier (e.g. 'google', 'github'). For create_oauth, delete_oauth.",
			},
			"display_name": map[string]interface{}{
				"type":        "string",
				"description": "Display name for OAuth button (e.g. 'Google'). For create_oauth.",
			},
			"client_id": map[string]interface{}{
				"type":        "string",
				"description": "OAuth client ID. For create_oauth.",
			},
			"client_secret": map[string]interface{}{
				"type":        "string",
				"description": "OAuth client secret (will be encrypted and stored). For create_oauth.",
			},
			"authorize_url": map[string]interface{}{
				"type":        "string",
				"description": "OAuth authorization endpoint URL. For create_oauth.",
			},
			"token_url": map[string]interface{}{
				"type":        "string",
				"description": "OAuth token exchange endpoint URL. For create_oauth.",
			},
			"userinfo_url": map[string]interface{}{
				"type":        "string",
				"description": "OAuth user info endpoint URL. For create_oauth.",
			},
			"scopes": map[string]interface{}{
				"type":        "string",
				"description": "Space-separated OAuth scopes (default: 'openid email profile'). For create_oauth.",
			},
			"username_field": map[string]interface{}{
				"type":        "string",
				"description": "Field from OAuth user info to use as username (default: 'email'). For create_oauth.",
			},
			"auth_path": map[string]interface{}{
				"type":        "string",
				"description": "Auth endpoint path this OAuth links to (must already exist). For create_oauth.",
			},
			"write_to_table": map[string]interface{}{
				"type":        "string",
				"description": "Table to auto-insert incoming WebSocket messages into. JSON fields are mapped to table columns automatically. Only for create_websocket.",
			},
			"room_column": map[string]interface{}{
				"type":        "string",
				"description": "Column name used for room scoping in WebSocket connections. Only for create_websocket.",
			},
			"cors_origins": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated allowed CORS origins (e.g. 'https://example.com,https://app.example.com'). Use '*' for any origin. Only for create_api/update_api.",
			},
			"cors_methods": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated allowed CORS methods (e.g. 'GET,POST,PUT,DELETE'). Defaults to endpoint methods. Only for create_api/update_api.",
			},
			"cors_headers": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated allowed CORS headers (e.g. 'Content-Type,Authorization'). Only for create_api/update_api.",
			},
			"system_prompt": map[string]interface{}{
				"type":        "string",
				"description": "System prompt for the LLM (defines AI personality/role). For create_llm/update_llm.",
			},
			"max_tokens": map[string]interface{}{
				"type":        "integer",
				"description": "Max tokens in LLM response (default: 4096). For create_llm/update_llm.",
			},
			"temperature": map[string]interface{}{
				"type":        "number",
				"description": "LLM temperature 0-1 (default: 0.7). For create_llm/update_llm.",
			},
			"max_history": map[string]interface{}{
				"type":        "integer",
				"description": "Max conversation turns to keep (default: 20). For create_llm/update_llm.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *EndpointsTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"create_api":       t.createAPI,
		"update_api":       t.updateAPI,
		"list_api":         t.listAPI,
		"delete_api":       t.deleteAPI,
		"create_auth":      t.createAuth,
		"update_auth":      t.updateAuth,
		"list_auth":        t.listAuth,
		"delete_auth":      t.deleteAuth,
		"create_upload":    t.createUpload,
		"update_upload":    t.updateUpload,
		"list_upload":      t.listUpload,
		"delete_upload":    t.deleteUpload,
		"create_stream":    t.createStream,
		"update_stream":    t.updateStream,
		"list_stream":      t.listStream,
		"delete_stream":    t.deleteStream,
		"create_websocket": t.createWebSocket,
		"update_websocket": t.updateWebSocket,
		"list_websocket":   t.listWebSocket,
		"delete_websocket": t.deleteWebSocket,
		"verify_password":  t.verifyPassword,
		"create_oauth":     t.createOAuth,
		"list_oauth":       t.listOAuth,
		"delete_oauth":     t.deleteOAuth,
		"generate_docs":    t.generateDocs,
		"create_llm":       t.createLLM,
		"update_llm":       t.updateLLM,
		"list_llm":         t.listLLM,
		"delete_llm":       t.deleteLLM,
	}, nil)
}

// ---------------------------------------------------------------------------
// API endpoint actions
// ---------------------------------------------------------------------------

func (t *EndpointsTool) createAPI(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	tableName, _ := args["table_name"].(string)
	if path == "" || tableName == "" {
		return &Result{Success: false, Error: "path and table_name are required"}, nil
	}

	// Verify the dynamic table exists.
	var exists int
	err := ctx.DB.QueryRow(
		"SELECT COUNT(*) FROM ho_dynamic_tables WHERE table_name = ?",
		tableName,
	).Scan(&exists)
	if err != nil || exists == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("dynamic table '%s' does not exist — create it first with manage_schema", tableName)}, nil
	}

	// Skip if endpoint already exists (prevents duplicate creation during BUILD).
	var existingCount int
	if err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_api_endpoints WHERE path = ?", path).Scan(&existingCount); err == nil && existingCount > 0 {
		return &Result{Success: true, Data: map[string]interface{}{
			"path":    path,
			"message": "Endpoint /api/" + path + " already exists — skipping creation.",
		}}, nil
	}

	// Parse methods — default to full CRUD since endpoints are table-bound.
	methods := []string{"GET", "POST", "PUT", "DELETE"}
	if methodsRaw, ok := args["methods"].([]interface{}); ok && len(methodsRaw) > 0 {
		methods = nil
		for _, m := range methodsRaw {
			if ms, ok := m.(string); ok {
				methods = append(methods, ms)
			}
		}
	}
	methodsJSON, _ := json.Marshal(methods)

	// Parse public_columns.
	var publicColsJSON *string
	if colsRaw, ok := args["public_columns"].([]interface{}); ok && len(colsRaw) > 0 {
		var cols []string
		for _, c := range colsRaw {
			if cs, ok := c.(string); ok {
				cols = append(cols, cs)
			}
		}
		j, _ := json.Marshal(cols)
		s := string(j)
		publicColsJSON = &s
	}

	requiresAuth := false
	if ra, ok := args["requires_auth"].(bool); ok {
		requiresAuth = ra
	}

	publicRead := false
	if pr, ok := args["public_read"].(bool); ok {
		publicRead = pr
	}

	// Role-based access control: if required_role is set, implies requires_auth.
	var requiredRole *string
	if rr, ok := args["required_role"].(string); ok && rr != "" {
		requiredRole = &rr
		requiresAuth = true
	}

	rateLimit := 60
	if rl, ok := args["rate_limit"].(float64); ok && rl > 0 {
		rateLimit = int(rl)
	}

	corsOrigins := OptionalString(args, "cors_origins", "")
	corsMethods := OptionalString(args, "cors_methods", "")
	corsHeaders := OptionalString(args, "cors_headers", "")

	// Row-level security: when owner_column is set, queries are scoped to the
	// authenticated user's ID. Requires requires_auth to be effective.
	var ownerColumn *string
	if oc, ok := args["owner_column"].(string); ok && oc != "" {
		ownerColumn = &oc
		requiresAuth = true // owner scoping implies auth required
	}

	// Response caching: cache_ttl in seconds (0 = no caching).
	cacheTTL := 0
	if ct, ok := args["cache_ttl"].(float64); ok && ct > 0 {
		cacheTTL = int(ct)
	}

	_, err = ctx.DB.Exec(
		`INSERT INTO ho_api_endpoints (path, table_name, methods, public_columns, requires_auth, public_read, required_role, rate_limit, cors_origins, cors_methods, cors_headers, owner_column, cache_ttl)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   table_name = excluded.table_name,
		   methods = excluded.methods,
		   public_columns = excluded.public_columns,
		   requires_auth = excluded.requires_auth,
		   public_read = excluded.public_read,
		   required_role = excluded.required_role,
		   rate_limit = excluded.rate_limit,
		   cors_origins = excluded.cors_origins,
		   cors_methods = excluded.cors_methods,
		   cors_headers = excluded.cors_headers,
		   owner_column = excluded.owner_column,
		   cache_ttl = excluded.cache_ttl`,
		path, tableName, string(methodsJSON), publicColsJSON, requiresAuth, publicRead, requiredRole, rateLimit, corsOrigins, corsMethods, corsHeaders, ownerColumn, cacheTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("creating API endpoint: %w", err)
	}

	data := map[string]interface{}{
		"path":          path,
		"table_name":    tableName,
		"methods":       methods,
		"requires_auth": requiresAuth,
		"public_read":   publicRead,
		"rate_limit":    rateLimit,
	}
	if requiredRole != nil {
		data["required_role"] = *requiredRole
	}
	if ownerColumn != nil {
		data["owner_column"] = *ownerColumn
	}
	return &Result{Success: true, Data: data}, nil
}

func (t *EndpointsTool) updateAPI(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	setClauses := []string{}
	var vals []interface{}

	if methods, ok := args["methods"].([]interface{}); ok {
		var ms []string
		for _, m := range methods {
			if s, ok := m.(string); ok {
				ms = append(ms, s)
			}
		}
		if len(ms) > 0 {
			methodsJSON, _ := json.Marshal(ms)
			setClauses = append(setClauses, "methods = ?")
			vals = append(vals, string(methodsJSON))
		}
	}
	if cols, ok := args["public_columns"].([]interface{}); ok {
		var cs []string
		for _, c := range cols {
			if s, ok := c.(string); ok {
				cs = append(cs, s)
			}
		}
		b, _ := json.Marshal(cs)
		setClauses = append(setClauses, "public_columns = ?")
		vals = append(vals, string(b))
	}
	if reqAuth, ok := args["requires_auth"].(bool); ok {
		setClauses = append(setClauses, "requires_auth = ?")
		vals = append(vals, reqAuth)
	}
	if pubRead, ok := args["public_read"].(bool); ok {
		setClauses = append(setClauses, "public_read = ?")
		vals = append(vals, pubRead)
	}
	if rl, ok := args["rate_limit"].(float64); ok {
		setClauses = append(setClauses, "rate_limit = ?")
		vals = append(vals, int(rl))
	}
	if role, ok := args["required_role"].(string); ok {
		setClauses = append(setClauses, "required_role = ?")
		vals = append(vals, role)
	}
	if co, ok := args["cors_origins"].(string); ok {
		setClauses = append(setClauses, "cors_origins = ?")
		vals = append(vals, co)
	}
	if cm, ok := args["cors_methods"].(string); ok {
		setClauses = append(setClauses, "cors_methods = ?")
		vals = append(vals, cm)
	}
	if ch, ok := args["cors_headers"].(string); ok {
		setClauses = append(setClauses, "cors_headers = ?")
		vals = append(vals, ch)
	}
	if oc, ok := args["owner_column"].(string); ok {
		setClauses = append(setClauses, "owner_column = ?")
		if oc == "" {
			vals = append(vals, nil) // clear owner_column
		} else {
			vals = append(vals, oc)
		}
	}

	if len(setClauses) == 0 {
		return &Result{Success: false, Error: "no fields to update"}, nil
	}

	vals = append(vals, path)
	query := fmt.Sprintf("UPDATE ho_api_endpoints SET %s WHERE path = ?", strings.Join(setClauses, ", "))
	res, err := ctx.DB.Exec(query, vals...)
	if err != nil {
		return nil, fmt.Errorf("updating endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("endpoint '%s' not found", path)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"path": path, "updated": true}}, nil
}

func (t *EndpointsTool) listAPI(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT id, path, table_name, methods, public_columns, requires_auth, rate_limit, created_at FROM ho_api_endpoints ORDER BY path",
	)
	if err != nil {
		return nil, fmt.Errorf("listing API endpoints: %w", err)
	}
	defer rows.Close()

	var endpoints []map[string]interface{}
	for rows.Next() {
		var id, rateLimit int
		var path, tableName, methods string
		var publicCols sql.NullString
		var requiresAuth bool
		var createdAt time.Time
		if err := rows.Scan(&id, &path, &tableName, &methods, &publicCols, &requiresAuth, &rateLimit, &createdAt); err != nil {
			continue
		}
		endpoints = append(endpoints, map[string]interface{}{
			"id":             id,
			"path":           path,
			"table_name":     tableName,
			"methods":        methods,
			"public_columns": publicCols.String,
			"requires_auth":  requiresAuth,
			"rate_limit":     rateLimit,
			"created_at":     createdAt,
		})
	}

	return &Result{Success: true, Data: endpoints}, nil
}

func (t *EndpointsTool) deleteAPI(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	res, err := ctx.DB.Exec(
		"DELETE FROM ho_api_endpoints WHERE path = ?",
		path,
	)
	if err != nil {
		return nil, fmt.Errorf("deleting API endpoint: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "endpoint not found"}, nil
	}

	LogDestructiveAction(ctx, "manage_endpoints", "delete_api", path)

	return &Result{Success: true, Data: map[string]interface{}{"deleted": path}}, nil
}

// ---------------------------------------------------------------------------
// Auth endpoint actions
// ---------------------------------------------------------------------------

func (t *EndpointsTool) createAuth(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	path, _ := args["path"].(string)
	usernameCol, _ := args["username_column"].(string)
	passwordCol, _ := args["password_column"].(string)
	if passwordCol == "" {
		passwordCol = "password"
	}

	if tableName == "" || path == "" || usernameCol == "" {
		return &Result{Success: false, Error: "table_name, path, and username_column are required"}, nil
	}

	// Skip if auth endpoint already exists (prevents duplicate creation during BUILD).
	var existingCount int
	if err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_auth_endpoints WHERE path = ?", path).Scan(&existingCount); err == nil && existingCount > 0 {
		return &Result{Success: true, Data: map[string]interface{}{
			"path":    path,
			"message": "Auth endpoint /api/" + path + " already exists — skipping creation.",
		}}, nil
	}

	// Validate column names to prevent SQL injection.
	if !validColumnName.MatchString(usernameCol) {
		return &Result{Success: false, Error: fmt.Sprintf("invalid username_column name: %s", usernameCol)}, nil
	}
	if !validColumnName.MatchString(passwordCol) {
		return &Result{Success: false, Error: fmt.Sprintf("invalid password_column name: %s", passwordCol)}, nil
	}

	// Verify the table exists and has a PASSWORD column.
	secureCols, err := loadSecureColumns(ctx, tableName)
	if err != nil || secureCols == nil {
		return &Result{Success: false, Error: fmt.Sprintf("table %q not found in dynamic tables registry", tableName)}, nil
	}
	if kind, ok := secureCols[passwordCol]; !ok || kind != "hash" {
		return &Result{Success: false, Error: fmt.Sprintf("column %q in table %q is not a PASSWORD column", passwordCol, tableName)}, nil
	}

	// Verify username_column exists in the actual table schema.
	var schemaDef string
	if err := ctx.DB.QueryRow(
		"SELECT schema_def FROM ho_dynamic_tables WHERE table_name = ?", tableName,
	).Scan(&schemaDef); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("could not load schema for table %q", tableName)}, nil
	}
	var schemaMap map[string]interface{}
	if err := json.Unmarshal([]byte(schemaDef), &schemaMap); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("invalid schema_def for table %q", tableName)}, nil
	}
	if _, ok := schemaMap[usernameCol]; !ok {
		var available []string
		for col := range schemaMap {
			if col != passwordCol {
				available = append(available, col)
			}
		}
		return &Result{Success: false, Error: fmt.Sprintf(
			"username_column %q does not exist in table %q — available columns: %s",
			usernameCol, tableName, strings.Join(available, ", "))}, nil
	}

	// Verify the physical SQLite table actually exists (not just the registry entry).
	var colCount int
	if err := ctx.DB.QueryRow(
		fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s')", tableName),
	).Scan(&colCount); err != nil || colCount == 0 {
		return &Result{Success: false, Error: fmt.Sprintf(
			"table %q is registered but does not exist as a physical table — recreate it with manage_schema",
			tableName)}, nil
	}

	// Build public columns JSON.
	publicColumnsJSON := "[]"
	if pubColsRaw, ok := args["public_columns"].([]interface{}); ok && len(pubColsRaw) > 0 {
		var pubCols []string
		for _, c := range pubColsRaw {
			if s, ok := c.(string); ok {
				if !validColumnName.MatchString(s) {
					return &Result{Success: false, Error: fmt.Sprintf("invalid public_column name: %s", s)}, nil
				}
				pubCols = append(pubCols, s)
			}
		}
		data, _ := json.Marshal(pubCols)
		publicColumnsJSON = string(data)
	}

	// RBAC: default role and role column.
	defaultRole := "user"
	if dr, ok := args["default_role"].(string); ok && dr != "" {
		defaultRole = dr
	}
	roleColumn := "role"
	if rc, ok := args["role_column"].(string); ok && rc != "" {
		if !validColumnName.MatchString(rc) {
			return &Result{Success: false, Error: fmt.Sprintf("invalid role_column name: %s", rc)}, nil
		}
		roleColumn = rc
	}

	// Ensure role_column exists in the table — auto-add if missing.
	if _, ok := schemaMap[roleColumn]; !ok {
		_, alterErr := ctx.DB.Exec(
			fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT DEFAULT ''", tableName, roleColumn))
		if alterErr != nil {
			return &Result{Success: false, Error: fmt.Sprintf(
				"role_column %q does not exist in table %q and could not be auto-added: %v",
				roleColumn, tableName, alterErr)}, nil
		}
		schemaMap[roleColumn] = "TEXT"
		updatedSchema, _ := json.Marshal(schemaMap)
		ctx.DB.Exec("UPDATE ho_dynamic_tables SET schema_def = ? WHERE table_name = ?",
			string(updatedSchema), tableName)
	}

	// Insert into ho_auth_endpoints table.
	result, err := ctx.DB.Exec(
		`INSERT INTO ho_auth_endpoints (table_name, path, username_column, password_column, public_columns, default_role, role_column)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   table_name = excluded.table_name,
		   username_column = excluded.username_column,
		   password_column = excluded.password_column,
		   public_columns = excluded.public_columns,
		   default_role = excluded.default_role,
		   role_column = excluded.role_column`,
		tableName, path, usernameCol, passwordCol, publicColumnsJSON, defaultRole, roleColumn,
	)
	if err != nil {
		return nil, fmt.Errorf("creating auth endpoint: %w", err)
	}

	id, _ := result.LastInsertId()
	return &Result{Success: true, Data: map[string]interface{}{
		"id":   id,
		"path": path,
		"routes": []string{
			fmt.Sprintf("POST /api/%s/register", path),
			fmt.Sprintf("POST /api/%s/login", path),
			fmt.Sprintf("GET /api/%s/me", path),
		},
		"table":           tableName,
		"username_column": usernameCol,
		"password_column": passwordCol,
		"default_role":    defaultRole,
		"role_column":     roleColumn,
	}}, nil
}

func (t *EndpointsTool) listAuth(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT id, path, table_name, username_column, password_column, public_columns, created_at FROM ho_auth_endpoints ORDER BY path",
	)
	if err != nil {
		return nil, fmt.Errorf("listing auth endpoints: %w", err)
	}
	defer rows.Close()

	var endpoints []map[string]interface{}
	for rows.Next() {
		var id int
		var path, tableName, usernameCol, passwordCol, publicCols string
		var createdAt interface{}
		if err := rows.Scan(&id, &path, &tableName, &usernameCol, &passwordCol, &publicCols, &createdAt); err != nil {
			continue
		}
		endpoints = append(endpoints, map[string]interface{}{
			"id":              id,
			"path":            path,
			"table_name":      tableName,
			"username_column": usernameCol,
			"password_column": passwordCol,
			"public_columns":  publicCols,
			"created_at":      createdAt,
			"routes": []string{
				fmt.Sprintf("POST /api/%s/register", path),
				fmt.Sprintf("POST /api/%s/login", path),
				fmt.Sprintf("GET /api/%s/me", path),
			},
		})
	}

	return &Result{Success: true, Data: endpoints}, nil
}

func (t *EndpointsTool) deleteAuth(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	res, err := ctx.DB.Exec(
		"DELETE FROM ho_auth_endpoints WHERE path = ?",
		path,
	)
	if err != nil {
		return nil, fmt.Errorf("deleting auth endpoint: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "auth endpoint not found"}, nil
	}

	LogDestructiveAction(ctx, "manage_endpoints", "delete_auth", path)

	return &Result{Success: true, Data: map[string]interface{}{"deleted": path}}, nil
}

func (t *EndpointsTool) verifyPassword(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableName, _ := args["table_name"].(string)
	idFloat, _ := args["id"].(float64)
	password, _ := args["password"].(string)
	passwordCol, _ := args["password_column"].(string)
	if passwordCol == "" {
		passwordCol = "password"
	}

	if tableName == "" || password == "" {
		return &Result{Success: false, Error: "table_name, id, and password are required"}, nil
	}

	physicalName, err := sanitizedTableName(tableName)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Validate column name to prevent SQL injection.
	if !validColumnName.MatchString(passwordCol) {
		return &Result{Success: false, Error: fmt.Sprintf("invalid column name: %s", passwordCol)}, nil
	}

	// Fetch the hashed password from the row.
	var hashedPassword string
	err = ctx.DB.QueryRow(
		fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", passwordCol, physicalName),
		int64(idFloat),
	).Scan(&hashedPassword)
	if err != nil {
		return &Result{Success: false, Error: "row not found"}, nil
	}

	// Use the security package to check the password.
	match := checkPasswordHash(password, hashedPassword)

	return &Result{Success: true, Data: map[string]interface{}{
		"match": match,
	}}, nil
}

// checkPasswordHash compares a plaintext password against a bcrypt hash.
func checkPasswordHash(password, hash string) bool {
	return security.CheckPassword(password, hash)
}

// ---------------------------------------------------------------------------
// Upload endpoint action
// ---------------------------------------------------------------------------

func (t *EndpointsTool) createUpload(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	// Skip if upload endpoint already exists (prevents duplicate creation during BUILD).
	var existingCount int
	if err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_upload_endpoints WHERE path = ?", path).Scan(&existingCount); err == nil && existingCount > 0 {
		return &Result{Success: true, Data: map[string]interface{}{
			"path":    path,
			"message": "Upload endpoint /api/" + path + " already exists — skipping creation.",
		}}, nil
	}

	// Parse allowed_types (default: common file types).
	allowedTypes := []string{"image/*", "application/pdf", "text/*", "application/json", "application/zip"}
	if typesRaw, ok := args["allowed_types"].([]interface{}); ok && len(typesRaw) > 0 {
		allowedTypes = nil
		for _, t := range typesRaw {
			if ts, ok := t.(string); ok {
				allowedTypes = append(allowedTypes, ts)
			}
		}
	}
	allowedTypesJSON, _ := json.Marshal(allowedTypes)

	maxSizeMB := 5
	if ms, ok := args["max_size_mb"].(float64); ok && ms > 0 {
		maxSizeMB = int(ms)
	}

	requiresAuth := false
	if ra, ok := args["requires_auth"].(bool); ok {
		requiresAuth = ra
	}

	// Optional: link to a table to store file metadata.
	tableName, _ := args["table_name"].(string)

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_upload_endpoints (path, allowed_types, max_size_mb, requires_auth, table_name)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   allowed_types = excluded.allowed_types,
		   max_size_mb = excluded.max_size_mb,
		   requires_auth = excluded.requires_auth,
		   table_name = excluded.table_name`,
		path, string(allowedTypesJSON), maxSizeMB, requiresAuth, tableName,
	)
	if err != nil {
		return nil, fmt.Errorf("creating upload endpoint: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"path":          path,
		"route":         fmt.Sprintf("POST /api/%s/upload", path),
		"allowed_types": allowedTypes,
		"max_size_mb":   maxSizeMB,
		"requires_auth": requiresAuth,
		"table_name":    tableName,
	}}, nil
}

// ---------------------------------------------------------------------------
// Stream endpoint action
// ---------------------------------------------------------------------------

func (t *EndpointsTool) createStream(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	// Skip if stream endpoint already exists (prevents duplicate creation during BUILD).
	var existingCount int
	if err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_stream_endpoints WHERE path = ?", path).Scan(&existingCount); err == nil && existingCount > 0 {
		return &Result{Success: true, Data: map[string]interface{}{
			"path":    path,
			"message": "Stream endpoint /api/" + path + " already exists — skipping creation.",
		}}, nil
	}

	// Parse event_types (default: all data events).
	eventTypes := []string{"data.insert", "data.update", "data.delete"}
	if typesRaw, ok := args["event_types"].([]interface{}); ok && len(typesRaw) > 0 {
		eventTypes = nil
		for _, et := range typesRaw {
			if ets, ok := et.(string); ok {
				eventTypes = append(eventTypes, ets)
			}
		}
	}
	eventTypesJSON, _ := json.Marshal(eventTypes)

	requiresAuth := false
	if ra, ok := args["requires_auth"].(bool); ok {
		requiresAuth = ra
	}

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_stream_endpoints (path, event_types, requires_auth)
		 VALUES (?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   event_types = excluded.event_types,
		   requires_auth = excluded.requires_auth`,
		path, string(eventTypesJSON), requiresAuth,
	)
	if err != nil {
		return nil, fmt.Errorf("creating stream endpoint: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"path":          path,
		"route":         fmt.Sprintf("GET /api/%s/stream", path),
		"event_types":   eventTypes,
		"requires_auth": requiresAuth,
		"usage":         "const source = new EventSource('/api/" + path + "/stream'); source.addEventListener('data.insert', (e) => { const data = JSON.parse(e.data); ... });",
	}}, nil
}

// ---------------------------------------------------------------------------
// WebSocket endpoint action
// ---------------------------------------------------------------------------

func (t *EndpointsTool) createWebSocket(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	// Skip if WebSocket endpoint already exists (prevents duplicate creation during BUILD).
	var existingCount int
	if err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_ws_endpoints WHERE path = ?", path).Scan(&existingCount); err == nil && existingCount > 0 {
		return &Result{Success: true, Data: map[string]interface{}{
			"path":    path,
			"message": "WebSocket endpoint /api/" + path + " already exists — skipping creation.",
		}}, nil
	}

	writeToTable := ""
	if wtt, ok := args["write_to_table"].(string); ok && wtt != "" {
		if !validColumnName.MatchString(wtt) {
			return &Result{Success: false, Error: fmt.Sprintf("invalid write_to_table name: %s", wtt)}, nil
		}
		writeToTable = wtt
	}

	roomColumn := ""
	if rc, ok := args["room_column"].(string); ok {
		roomColumn = rc
	}

	requiresAuth := false
	if ra, ok := args["requires_auth"].(bool); ok {
		requiresAuth = ra
	}

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_ws_endpoints (path, write_to_table, room_column, requires_auth)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   write_to_table = excluded.write_to_table,
		   room_column = excluded.room_column,
		   requires_auth = excluded.requires_auth`,
		path, writeToTable, roomColumn, requiresAuth,
	)
	if err != nil {
		return nil, fmt.Errorf("creating websocket endpoint: %w", err)
	}

	usage := fmt.Sprintf(`CONNECTION:
  const ws = new WebSocket((location.protocol==='https:'?'wss:':'ws:') + '//' + location.host + '/api/%s/ws?room=' + roomId);
  // ?room= is always available. Clients in the same room see each other's messages.
  // Omit ?room= for a single global channel.

RECEIVED MESSAGES (ws.onmessage):
  Exactly what the other client sent, plus "_sender" (unique client ID).
  Example: other client sends {type: "move", x: 5}
           you receive:       {"type": "move", "x": 5, "_sender": "uuid"}

  ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    // msg has exactly the fields the sender set, plus msg._sender
  };

SENDING:
  ws.send(JSON.stringify({ type: "chat", content: "hello" }));

ECHO SUPPRESSION:
  The server does NOT echo your message back to you.
  Update your own UI immediately after ws.send().

ROOMS:
  All clients with the same ?room= value share a channel.
  Clients in different rooms cannot see each other's messages.`, path)

	result := map[string]interface{}{
		"path":          path,
		"route":         fmt.Sprintf("GET /api/%s/ws", path),
		"requires_auth": requiresAuth,
		"usage":         usage,
	}
	if writeToTable != "" {
		result["write_to_table"] = writeToTable
	}
	if roomColumn != "" {
		result["room_column"] = roomColumn
	}
	return &Result{Success: true, Data: result}, nil
}

// ---------------------------------------------------------------------------
// Test action
// ---------------------------------------------------------------------------
// OAuth endpoint actions
// ---------------------------------------------------------------------------

func (t *EndpointsTool) createOAuth(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	providerName, _ := args["provider_name"].(string)
	displayName, _ := args["display_name"].(string)
	clientID, _ := args["client_id"].(string)
	clientSecret, _ := args["client_secret"].(string)
	authorizeURL, _ := args["authorize_url"].(string)
	tokenURL, _ := args["token_url"].(string)
	userinfoURL, _ := args["userinfo_url"].(string)
	authPath, _ := args["auth_path"].(string)

	if providerName == "" || clientID == "" || clientSecret == "" || authorizeURL == "" || tokenURL == "" || userinfoURL == "" || authPath == "" {
		return &Result{Success: false, Error: "provider_name, client_id, client_secret, authorize_url, token_url, userinfo_url, and auth_path are required"}, nil
	}
	if displayName == "" {
		displayName = providerName
	}

	scopes := "openid email profile"
	if s, ok := args["scopes"].(string); ok && s != "" {
		scopes = s
	}
	usernameField := "email"
	if uf, ok := args["username_field"].(string); ok && uf != "" {
		usernameField = uf
	}

	// Skip if OAuth provider already exists (prevents duplicate creation during BUILD).
	var oauthExists int
	if err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_oauth_providers WHERE name = ?", providerName).Scan(&oauthExists); err == nil && oauthExists > 0 {
		return &Result{Success: true, Data: map[string]interface{}{
			"provider": providerName,
			"message":  "OAuth provider " + providerName + " already exists — skipping creation.",
		}}, nil
	}

	// Verify the auth endpoint exists.
	var authExists int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_auth_endpoints WHERE path = ?", authPath).Scan(&authExists)
	if authExists == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("auth endpoint '%s' does not exist — create it first with create_auth", authPath)}, nil
	}

	// Encrypt and store the client secret.
	secretName := "oauth_" + providerName + "_secret"
	if ctx.Encryptor == nil {
		return &Result{Success: false, Error: "encryption not available"}, nil
	}
	encrypted, err := ctx.Encryptor.Encrypt(clientSecret)
	if err != nil {
		return &Result{Success: false, Error: "failed to encrypt client secret"}, nil
	}
	if _, err := ctx.DB.Exec(
		`INSERT INTO ho_secrets (name, value_encrypted) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET value_encrypted = excluded.value_encrypted, updated_at = CURRENT_TIMESTAMP`,
		secretName, encrypted,
	); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to store encrypted secret: %v", err)}, nil
	}

	// Insert OAuth provider.
	_, err = ctx.DB.Exec(
		`INSERT INTO ho_oauth_providers (name, display_name, client_id, client_secret_name, authorize_url, token_url, userinfo_url, scopes, username_field, auth_endpoint_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   display_name = excluded.display_name,
		   client_id = excluded.client_id,
		   client_secret_name = excluded.client_secret_name,
		   authorize_url = excluded.authorize_url,
		   token_url = excluded.token_url,
		   userinfo_url = excluded.userinfo_url,
		   scopes = excluded.scopes,
		   username_field = excluded.username_field,
		   auth_endpoint_path = excluded.auth_endpoint_path`,
		providerName, displayName, clientID, secretName, authorizeURL, tokenURL, userinfoURL, scopes, usernameField, authPath,
	)
	if err != nil {
		return nil, fmt.Errorf("creating OAuth provider: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"provider": providerName,
		"routes": []string{
			fmt.Sprintf("GET /api/%s/oauth/%s (redirect to provider)", authPath, providerName),
			fmt.Sprintf("GET /api/%s/oauth/%s/callback (automatic)", authPath, providerName),
		},
		"display_name": displayName,
		"auth_path":    authPath,
	}}, nil
}

func (t *EndpointsTool) listOAuth(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT id, name, display_name, client_id, authorize_url, token_url, userinfo_url, scopes, username_field, auth_endpoint_path, is_enabled, created_at FROM ho_oauth_providers ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("listing OAuth providers: %w", err)
	}
	defer rows.Close()

	var providers []map[string]interface{}
	for rows.Next() {
		var id int
		var name, displayName, clientID, authorizeURL, tokenURL, userinfoURL, scopes, usernameField, authPath string
		var isEnabled bool
		var createdAt string
		if err := rows.Scan(&id, &name, &displayName, &clientID, &authorizeURL, &tokenURL, &userinfoURL, &scopes, &usernameField, &authPath, &isEnabled, &createdAt); err != nil {
			continue
		}
		providers = append(providers, map[string]interface{}{
			"id":            id,
			"name":          name,
			"display_name":  displayName,
			"client_id":     clientID,
			"authorize_url": authorizeURL,
			"scopes":        scopes,
			"auth_path":     authPath,
			"is_enabled":    isEnabled,
			"created_at":    createdAt,
		})
	}
	return &Result{Success: true, Data: providers}, nil
}

func (t *EndpointsTool) deleteOAuth(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	providerName, _ := args["provider_name"].(string)
	if providerName == "" {
		return &Result{Success: false, Error: "provider_name is required"}, nil
	}

	// Clean up the associated secret.
	secretName := "oauth_" + providerName + "_secret"
	ctx.DB.Exec("DELETE FROM ho_secrets WHERE name = ?", secretName)

	result, err := ctx.DB.Exec("DELETE FROM ho_oauth_providers WHERE name = ?", providerName)
	if err != nil {
		return nil, fmt.Errorf("deleting OAuth provider: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("OAuth provider '%s' not found", providerName)}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"deleted": providerName,
	}}, nil
}

// ---------------------------------------------------------------------------
// generate_docs — auto-generate OpenAPI 3.0 spec + Swagger UI page
// ---------------------------------------------------------------------------

func (t *EndpointsTool) generateDocs(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	// Get site name for the API title.
	var siteName string
	err := ctx.GlobalDB.QueryRow("SELECT name FROM sites WHERE id = ?", ctx.SiteID).Scan(&siteName)
	if err != nil || siteName == "" {
		siteName = "API"
	}

	spec := map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":       siteName + " API",
			"version":     "1.0.0",
			"description": "Auto-generated API documentation",
		},
		"paths":      map[string]interface{}{},
		"components": map[string]interface{}{"schemas": map[string]interface{}{}, "securitySchemes": map[string]interface{}{"BearerAuth": map[string]interface{}{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"}}},
	}

	paths := spec["paths"].(map[string]interface{})
	schemas := spec["components"].(map[string]interface{})["schemas"].(map[string]interface{})

	// Load all table schemas from ho_dynamic_tables.
	tableSchemas := map[string][]columnDef{}
	schemaRows, err := ctx.DB.Query("SELECT table_name, schema_def FROM ho_dynamic_tables")
	if err == nil {
		defer schemaRows.Close()
		for schemaRows.Next() {
			var tName, schemaDef string
			if schemaRows.Scan(&tName, &schemaDef) == nil {
				var cols []columnDef
				if json.Unmarshal([]byte(schemaDef), &cols) == nil {
					tableSchemas[tName] = cols
				}
			}
		}
	}

	// --- API endpoints ---
	apiRows, err := ctx.DB.Query("SELECT path, table_name, methods, requires_auth, public_read, public_columns FROM ho_api_endpoints")
	if err == nil {
		defer apiRows.Close()
		for apiRows.Next() {
			var path, tableName, methodsJSON string
			var requiresAuth, publicRead bool
			var publicCols sql.NullString
			if apiRows.Scan(&path, &tableName, &methodsJSON, &requiresAuth, &publicRead, &publicCols) != nil {
				continue
			}

			var methods []string
			if json.Unmarshal([]byte(methodsJSON), &methods) != nil {
				methods = []string{"GET", "POST", "PUT", "DELETE"}
			}

			// Build request/response schemas from table columns.
			cols := tableSchemas[tableName]
			reqProps := map[string]interface{}{}
			respProps := map[string]interface{}{"id": map[string]interface{}{"type": "integer"}}
			var requiredFields []string
			for _, c := range cols {
				if strings.ToUpper(c.Type) == "PASSWORD" {
					continue // exclude from response
				}
				prop := map[string]interface{}{"type": sqlTypeToOpenAPI(c.Type)}
				respProps[c.Name] = prop
				reqProps[c.Name] = prop
				if c.Required {
					requiredFields = append(requiredFields, c.Name)
				}
			}
			// Add password columns to request schema only.
			for _, c := range cols {
				if strings.ToUpper(c.Type) == "PASSWORD" {
					reqProps[c.Name] = map[string]interface{}{"type": "string", "format": "password"}
					if c.Required {
						requiredFields = append(requiredFields, c.Name)
					}
				}
			}

			schemaName := strings.Title(path)
			schemas[schemaName] = map[string]interface{}{
				"type":       "object",
				"properties": respProps,
			}
			reqSchemaName := schemaName + "Input"
			reqSchema := map[string]interface{}{
				"type":       "object",
				"properties": reqProps,
			}
			if len(requiredFields) > 0 {
				reqSchema["required"] = requiredFields
			}
			schemas[reqSchemaName] = reqSchema

			apiPath := "/api/" + path
			apiPathID := "/api/" + path + "/{id}"
			pathItem := map[string]interface{}{}
			pathItemID := map[string]interface{}{}

			for _, m := range methods {
				switch strings.ToUpper(m) {
				case "GET":
					getOp := map[string]interface{}{
						"summary":     "List " + path,
						"operationId": "list_" + path,
						"parameters": []interface{}{
							map[string]interface{}{"name": "limit", "in": "query", "schema": map[string]interface{}{"type": "integer", "default": 20}},
							map[string]interface{}{"name": "offset", "in": "query", "schema": map[string]interface{}{"type": "integer", "default": 0}},
							map[string]interface{}{"name": "q", "in": "query", "schema": map[string]interface{}{"type": "string"}, "description": "Full-text search"},
						},
						"responses": map[string]interface{}{
							"200": map[string]interface{}{
								"description": "Success",
								"content": map[string]interface{}{"application/json": map[string]interface{}{
									"schema": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"data":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"$ref": "#/components/schemas/" + schemaName}},
											"count":  map[string]interface{}{"type": "integer"},
											"limit":  map[string]interface{}{"type": "integer"},
											"offset": map[string]interface{}{"type": "integer"},
										},
									},
								}},
							},
						},
					}
					if requiresAuth && !publicRead {
						getOp["security"] = []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}}
					}
					pathItem["get"] = getOp

					getOneOp := map[string]interface{}{
						"summary":     "Get " + path + " by ID",
						"operationId": "get_" + path,
						"parameters":  []interface{}{map[string]interface{}{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "integer"}}},
						"responses": map[string]interface{}{
							"200": map[string]interface{}{
								"description": "Success",
								"content":     map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + schemaName}}},
							},
							"404": map[string]interface{}{"description": "Not found"},
						},
					}
					if requiresAuth && !publicRead {
						getOneOp["security"] = []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}}
					}
					pathItemID["get"] = getOneOp

				case "POST":
					postOp := map[string]interface{}{
						"summary":     "Create " + path,
						"operationId": "create_" + path,
						"requestBody": map[string]interface{}{
							"required": true,
							"content":  map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + reqSchemaName}}},
						},
						"responses": map[string]interface{}{
							"201": map[string]interface{}{
								"description": "Created",
								"content":     map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + schemaName}}},
							},
						},
					}
					if requiresAuth {
						postOp["security"] = []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}}
					}
					pathItem["post"] = postOp

				case "PUT":
					putOp := map[string]interface{}{
						"summary":     "Update " + path,
						"operationId": "update_" + path,
						"parameters":  []interface{}{map[string]interface{}{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "integer"}}},
						"requestBody": map[string]interface{}{
							"required": true,
							"content":  map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + reqSchemaName}}},
						},
						"responses": map[string]interface{}{
							"200": map[string]interface{}{
								"description": "Updated",
								"content":     map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + schemaName}}},
							},
							"404": map[string]interface{}{"description": "Not found"},
						},
					}
					if requiresAuth {
						putOp["security"] = []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}}
					}
					pathItemID["put"] = putOp

				case "DELETE":
					delOp := map[string]interface{}{
						"summary":     "Delete " + path,
						"operationId": "delete_" + path,
						"parameters":  []interface{}{map[string]interface{}{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "integer"}}},
						"responses": map[string]interface{}{
							"200": map[string]interface{}{"description": "Deleted"},
							"404": map[string]interface{}{"description": "Not found"},
						},
					}
					if requiresAuth {
						delOp["security"] = []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}}
					}
					pathItemID["delete"] = delOp
				}
			}

			if len(pathItem) > 0 {
				paths[apiPath] = pathItem
			}
			if len(pathItemID) > 0 {
				paths[apiPathID] = pathItemID
			}
		}
	}

	// --- Auth endpoints ---
	authRows, err := ctx.DB.Query("SELECT path, table_name, username_column FROM ho_auth_endpoints")
	if err == nil {
		defer authRows.Close()
		for authRows.Next() {
			var path, tableName, usernameCol string
			if authRows.Scan(&path, &tableName, &usernameCol) != nil {
				continue
			}

			loginSchema := map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					usernameCol: map[string]interface{}{"type": "string"},
					"password":  map[string]interface{}{"type": "string", "format": "password"},
				},
				"required": []string{usernameCol, "password"},
			}
			schemas[strings.Title(path)+"Login"] = loginSchema

			// Build user response schema from table columns.
			userProps := map[string]interface{}{"id": map[string]interface{}{"type": "integer"}}
			if cols, ok := tableSchemas[tableName]; ok {
				for _, c := range cols {
					if strings.ToUpper(c.Type) == "PASSWORD" {
						continue
					}
					userProps[c.Name] = map[string]interface{}{"type": sqlTypeToOpenAPI(c.Type)}
				}
			}
			schemas[strings.Title(path)+"User"] = map[string]interface{}{"type": "object", "properties": userProps}

			paths["/api/"+path+"/register"] = map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Register new user",
					"operationId": "register_" + path,
					"requestBody": map[string]interface{}{
						"required": true,
						"content":  map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + strings.Title(path) + "Login"}}},
					},
					"responses": map[string]interface{}{
						"201": map[string]interface{}{
							"description": "Registered",
							"content":     map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"token": map[string]interface{}{"type": "string"}}}}},
						},
					},
				},
			}
			paths["/api/"+path+"/login"] = map[string]interface{}{
				"post": map[string]interface{}{
					"summary":     "Login",
					"operationId": "login_" + path,
					"requestBody": map[string]interface{}{
						"required": true,
						"content":  map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + strings.Title(path) + "Login"}}},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Login successful",
							"content":     map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"token": map[string]interface{}{"type": "string"}}}}},
						},
						"401": map[string]interface{}{"description": "Invalid credentials"},
					},
				},
			}
			paths["/api/"+path+"/me"] = map[string]interface{}{
				"get": map[string]interface{}{
					"summary":     "Get current user",
					"operationId": "me_" + path,
					"security":    []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Current user",
							"content":     map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + strings.Title(path) + "User"}}},
						},
						"401": map[string]interface{}{"description": "Unauthorized"},
					},
				},
			}
		}
	}

	// --- Upload endpoints ---
	uploadRows, err := ctx.DB.Query("SELECT path, allowed_types, max_size_mb, requires_auth FROM ho_upload_endpoints")
	if err == nil {
		defer uploadRows.Close()
		for uploadRows.Next() {
			var path string
			var allowedTypes sql.NullString
			var maxSizeMB sql.NullFloat64
			var requiresAuth bool
			if uploadRows.Scan(&path, &allowedTypes, &maxSizeMB, &requiresAuth) != nil {
				continue
			}
			desc := "Upload a file"
			if allowedTypes.Valid && allowedTypes.String != "" {
				desc += " (allowed: " + allowedTypes.String + ")"
			}
			uploadOp := map[string]interface{}{
				"summary":     "Upload file to " + path,
				"operationId": "upload_" + path,
				"description": desc,
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{"multipart/form-data": map[string]interface{}{
						"schema": map[string]interface{}{
							"type":       "object",
							"properties": map[string]interface{}{"file": map[string]interface{}{"type": "string", "format": "binary"}},
							"required":   []string{"file"},
						},
					}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "Upload successful",
						"content": map[string]interface{}{"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"url":      map[string]interface{}{"type": "string"},
									"filename": map[string]interface{}{"type": "string"},
									"size":     map[string]interface{}{"type": "integer"},
									"type":     map[string]interface{}{"type": "string"},
								},
							},
						}},
					},
				},
			}
			if requiresAuth {
				uploadOp["security"] = []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}}
			}
			paths["/api/"+path+"/upload"] = map[string]interface{}{"post": uploadOp}
		}
	}

	// --- Stream endpoints ---
	streamRows, err := ctx.DB.Query("SELECT path, event_types, requires_auth FROM ho_stream_endpoints")
	if err == nil {
		defer streamRows.Close()
		for streamRows.Next() {
			var path string
			var eventTypes sql.NullString
			var requiresAuth bool
			if streamRows.Scan(&path, &eventTypes, &requiresAuth) != nil {
				continue
			}
			streamOp := map[string]interface{}{
				"summary":     "SSE stream for " + path,
				"operationId": "stream_" + path,
				"description": "Server-Sent Events stream for real-time updates",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "SSE event stream",
						"content":     map[string]interface{}{"text/event-stream": map[string]interface{}{"schema": map[string]interface{}{"type": "string"}}},
					},
				},
			}
			if requiresAuth {
				streamOp["security"] = []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}}
			}
			paths["/api/"+path+"/stream"] = map[string]interface{}{"get": streamOp}
		}
	}

	// --- WebSocket endpoints ---
	wsRows, err := ctx.DB.Query("SELECT path, requires_auth FROM ho_ws_endpoints")
	if err == nil {
		defer wsRows.Close()
		for wsRows.Next() {
			var path string
			var requiresAuth bool
			if wsRows.Scan(&path, &requiresAuth) != nil {
				continue
			}
			wsOp := map[string]interface{}{
				"summary":     "WebSocket connection for " + path,
				"operationId": "ws_" + path,
				"description": "Connect via ws(s)://host/api/" + path + "/ws?room=ROOM_NAME",
				"parameters":  []interface{}{map[string]interface{}{"name": "room", "in": "query", "schema": map[string]interface{}{"type": "string"}, "description": "Room name for scoped messaging"}},
				"responses": map[string]interface{}{
					"101": map[string]interface{}{"description": "WebSocket upgrade"},
				},
			}
			if requiresAuth {
				wsOp["security"] = []interface{}{map[string]interface{}{"BearerAuth": []interface{}{}}}
			}
			paths["/api/"+path+"/ws"] = map[string]interface{}{"get": wsOp}
		}
	}

	// Marshal the spec to JSON.
	specJSON, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return &Result{Success: false, Error: "failed to marshal spec: " + err.Error()}, nil
	}

	// Save the spec as a file-based asset (api-docs.json).
	storagePath, err := writeFileToDisk(ctx.SiteID, "assets", "api-docs.json", specJSON)
	if err != nil {
		return &Result{Success: false, Error: "failed to write spec file: " + err.Error()}, nil
	}
	_, err = ctx.DB.Exec(
		`INSERT INTO ho_assets (filename, content_type, size, storage_path, alt_text, scope)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(filename) DO UPDATE SET
		   content_type = excluded.content_type,
		   size = excluded.size,
		   storage_path = excluded.storage_path,
		   alt_text = excluded.alt_text`,
		"api-docs.json", "application/json", len(specJSON), storagePath, "OpenAPI 3.0 specification", "global",
	)
	if err != nil {
		return &Result{Success: false, Error: "failed to save spec asset: " + err.Error()}, nil
	}

	// Save Swagger UI page at /api/docs.
	swaggerHTML := `<div id="swagger-ui"></div>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>
SwaggerUIBundle({
  url: '/api/docs.json',
  dom_id: '#swagger-ui',
  presets: [SwaggerUIBundle.presets.apis],
  layout: 'BaseLayout'
});
</script>`

	_, err = ctx.DB.Exec(
		`INSERT INTO ho_pages (path, title, content) VALUES (?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET title = excluded.title, content = excluded.content`,
		"/api/docs", "API Documentation", swaggerHTML,
	)
	if err != nil {
		return &Result{Success: false, Error: "failed to save docs page: " + err.Error()}, nil
	}

	endpointCount := len(paths)
	schemaCount := len(schemas)

	return &Result{Success: true, Data: map[string]interface{}{
		"message":    fmt.Sprintf("Generated OpenAPI spec with %d paths and %d schemas", endpointCount, schemaCount),
		"spec_asset": "api-docs.json",
		"docs_page":  "/api/docs",
		"spec_url":   "/api/docs.json",
	}}, nil
}

// columnDef represents a column in a dynamic table schema.
type columnDef struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// sqlTypeToOpenAPI maps SQLite column types to OpenAPI types.
func sqlTypeToOpenAPI(sqlType string) string {
	switch strings.ToUpper(sqlType) {
	case "INTEGER":
		return "integer"
	case "REAL":
		return "number"
	case "BOOLEAN":
		return "boolean"
	default:
		return "string"
	}
}

func (t *EndpointsTool) MaxResultSize() int { return 8000 }

// ---------------------------------------------------------------------------
// Update endpoint actions
// ---------------------------------------------------------------------------

func (t *EndpointsTool) updateAuth(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	setClauses := []string{}
	var vals []interface{}

	if dr, ok := args["default_role"].(string); ok {
		setClauses = append(setClauses, "default_role = ?")
		vals = append(vals, dr)
	}
	if cols, ok := args["public_columns"].([]interface{}); ok {
		var cs []string
		for _, c := range cols {
			if s, ok := c.(string); ok {
				cs = append(cs, s)
			}
		}
		b, _ := json.Marshal(cs)
		setClauses = append(setClauses, "public_columns = ?")
		vals = append(vals, string(b))
	}
	if rc, ok := args["role_column"].(string); ok {
		setClauses = append(setClauses, "role_column = ?")
		vals = append(vals, rc)
	}

	if len(setClauses) == 0 {
		return &Result{Success: false, Error: "no fields to update"}, nil
	}

	vals = append(vals, path)
	query := fmt.Sprintf("UPDATE ho_auth_endpoints SET %s WHERE path = ?", strings.Join(setClauses, ", "))
	res, err := ctx.DB.Exec(query, vals...)
	if err != nil {
		return nil, fmt.Errorf("updating auth endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("auth endpoint %q not found", path)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"path": path, "updated": true}}, nil
}

func (t *EndpointsTool) updateWebSocket(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	setClauses := []string{}
	var vals []interface{}

	if wtt, ok := args["write_to_table"].(string); ok {
		setClauses = append(setClauses, "write_to_table = ?")
		vals = append(vals, wtt)
	}
	if rc, ok := args["room_column"].(string); ok {
		setClauses = append(setClauses, "room_column = ?")
		vals = append(vals, rc)
	}
	if ra, ok := args["requires_auth"].(bool); ok {
		setClauses = append(setClauses, "requires_auth = ?")
		vals = append(vals, ra)
	}

	if len(setClauses) == 0 {
		return &Result{Success: false, Error: "no fields to update"}, nil
	}

	vals = append(vals, path)
	query := fmt.Sprintf("UPDATE ho_ws_endpoints SET %s WHERE path = ?", strings.Join(setClauses, ", "))
	res, err := ctx.DB.Exec(query, vals...)
	if err != nil {
		return nil, fmt.Errorf("updating websocket endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("websocket endpoint %q not found", path)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"path": path, "updated": true}}, nil
}

func (t *EndpointsTool) updateStream(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	setClauses := []string{}
	var vals []interface{}

	if typesRaw, ok := args["event_types"].([]interface{}); ok {
		var types []string
		for _, et := range typesRaw {
			if s, ok := et.(string); ok {
				types = append(types, s)
			}
		}
		b, _ := json.Marshal(types)
		setClauses = append(setClauses, "event_types = ?")
		vals = append(vals, string(b))
	}
	if ra, ok := args["requires_auth"].(bool); ok {
		setClauses = append(setClauses, "requires_auth = ?")
		vals = append(vals, ra)
	}

	if len(setClauses) == 0 {
		return &Result{Success: false, Error: "no fields to update"}, nil
	}

	vals = append(vals, path)
	query := fmt.Sprintf("UPDATE ho_stream_endpoints SET %s WHERE path = ?", strings.Join(setClauses, ", "))
	res, err := ctx.DB.Exec(query, vals...)
	if err != nil {
		return nil, fmt.Errorf("updating stream endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("stream endpoint %q not found", path)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"path": path, "updated": true}}, nil
}

func (t *EndpointsTool) updateUpload(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	setClauses := []string{}
	var vals []interface{}

	if types, ok := args["allowed_types"].([]interface{}); ok {
		var ts []string
		for _, t := range types {
			if s, ok := t.(string); ok {
				ts = append(ts, s)
			}
		}
		b, _ := json.Marshal(ts)
		setClauses = append(setClauses, "allowed_types = ?")
		vals = append(vals, string(b))
	}
	if ms, ok := args["max_size_mb"].(float64); ok {
		setClauses = append(setClauses, "max_size_mb = ?")
		vals = append(vals, int(ms))
	}
	if ra, ok := args["requires_auth"].(bool); ok {
		setClauses = append(setClauses, "requires_auth = ?")
		vals = append(vals, ra)
	}

	if len(setClauses) == 0 {
		return &Result{Success: false, Error: "no fields to update"}, nil
	}

	vals = append(vals, path)
	query := fmt.Sprintf("UPDATE ho_upload_endpoints SET %s WHERE path = ?", strings.Join(setClauses, ", "))
	res, err := ctx.DB.Exec(query, vals...)
	if err != nil {
		return nil, fmt.Errorf("updating upload endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("upload endpoint %q not found", path)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"path": path, "updated": true}}, nil
}

// ---------------------------------------------------------------------------
// List/Delete for stream, websocket, upload endpoints
// ---------------------------------------------------------------------------

func (t *EndpointsTool) listStream(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query("SELECT id, path, event_types, requires_auth, created_at FROM ho_stream_endpoints ORDER BY path")
	if err != nil {
		return nil, fmt.Errorf("listing stream endpoints: %w", err)
	}
	defer rows.Close()
	var endpoints []map[string]interface{}
	for rows.Next() {
		var id int
		var path, eventTypes string
		var requiresAuth bool
		var createdAt time.Time
		if rows.Scan(&id, &path, &eventTypes, &requiresAuth, &createdAt) != nil {
			continue
		}
		endpoints = append(endpoints, map[string]interface{}{
			"id": id, "path": path, "event_types": eventTypes,
			"requires_auth": requiresAuth, "created_at": createdAt,
		})
	}
	return &Result{Success: true, Data: endpoints}, nil
}

func (t *EndpointsTool) deleteStream(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	res, err := ctx.DB.Exec("DELETE FROM ho_stream_endpoints WHERE path = ?", path)
	if err != nil {
		return nil, fmt.Errorf("deleting stream endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "stream endpoint not found"}, nil
	}
	LogDestructiveAction(ctx, "manage_endpoints", "delete_stream", path)
	return &Result{Success: true, Data: map[string]interface{}{"deleted": path}}, nil
}

func (t *EndpointsTool) listWebSocket(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query("SELECT id, path, write_to_table, room_column, requires_auth, created_at FROM ho_ws_endpoints ORDER BY path")
	if err != nil {
		return nil, fmt.Errorf("listing websocket endpoints: %w", err)
	}
	defer rows.Close()
	var endpoints []map[string]interface{}
	for rows.Next() {
		var id int
		var path, writeToTable, roomColumn string
		var requiresAuth bool
		var createdAt time.Time
		if rows.Scan(&id, &path, &writeToTable, &roomColumn, &requiresAuth, &createdAt) != nil {
			continue
		}
		endpoints = append(endpoints, map[string]interface{}{
			"id": id, "path": path, "write_to_table": writeToTable,
			"room_column": roomColumn, "requires_auth": requiresAuth, "created_at": createdAt,
		})
	}
	return &Result{Success: true, Data: endpoints}, nil
}

func (t *EndpointsTool) deleteWebSocket(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	res, err := ctx.DB.Exec("DELETE FROM ho_ws_endpoints WHERE path = ?", path)
	if err != nil {
		return nil, fmt.Errorf("deleting websocket endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "websocket endpoint not found"}, nil
	}
	LogDestructiveAction(ctx, "manage_endpoints", "delete_websocket", path)
	return &Result{Success: true, Data: map[string]interface{}{"deleted": path}}, nil
}

func (t *EndpointsTool) listUpload(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query("SELECT id, path, table_name, allowed_types, max_size_mb, requires_auth, created_at FROM ho_upload_endpoints ORDER BY path")
	if err != nil {
		return nil, fmt.Errorf("listing upload endpoints: %w", err)
	}
	defer rows.Close()
	var endpoints []map[string]interface{}
	for rows.Next() {
		var id, maxSizeMB int
		var path string
		var tableName, allowedTypes sql.NullString
		var requiresAuth bool
		var createdAt time.Time
		if rows.Scan(&id, &path, &tableName, &allowedTypes, &maxSizeMB, &requiresAuth, &createdAt) != nil {
			continue
		}
		endpoints = append(endpoints, map[string]interface{}{
			"id": id, "path": path, "table_name": tableName.String,
			"allowed_types": allowedTypes.String, "max_size_mb": maxSizeMB,
			"requires_auth": requiresAuth, "created_at": createdAt,
		})
	}
	return &Result{Success: true, Data: endpoints}, nil
}

func (t *EndpointsTool) deleteUpload(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	res, err := ctx.DB.Exec("DELETE FROM ho_upload_endpoints WHERE path = ?", path)
	if err != nil {
		return nil, fmt.Errorf("deleting upload endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "upload endpoint not found"}, nil
	}
	LogDestructiveAction(ctx, "manage_endpoints", "delete_upload", path)
	return &Result{Success: true, Data: map[string]interface{}{"deleted": path}}, nil
}

func (t *EndpointsTool) Summarize(result string) string {
	r, dataMap, dataArr, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if dataArr != nil {
		return fmt.Sprintf(`{"success":true,"summary":"Listed %d endpoints"}`, len(dataArr))
	}
	// For create/test operations, include key fields.
	if path, _ := dataMap["path"].(string); path != "" {
		return fmt.Sprintf(`{"success":true,"summary":"Endpoint at /api/%s"}`, path)
	}
	return summarizeTruncate(result, 300)
}

// ---------------------------------------------------------------------------
// LLM endpoint actions
// ---------------------------------------------------------------------------

func (t *EndpointsTool) createLLM(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	// Skip if LLM endpoint already exists (prevents duplicate creation during BUILD).
	var existingCount int
	if err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_llm_endpoints WHERE path = ?", path).Scan(&existingCount); err == nil && existingCount > 0 {
		return &Result{Success: true, Data: map[string]interface{}{
			"path":    path,
			"message": "LLM endpoint /api/" + path + " already exists — skipping creation.",
		}}, nil
	}

	systemPrompt, _ := args["system_prompt"].(string)
	modelID, _ := args["model_id"].(string)
	maxTokens := 4096
	if mt, ok := args["max_tokens"].(float64); ok && mt > 0 {
		maxTokens = int(mt)
	}
	temperature := 0.7
	if temp, ok := args["temperature"].(float64); ok {
		temperature = temp
	}
	maxHistory := 20
	if mh, ok := args["max_history"].(float64); ok && mh > 0 {
		maxHistory = int(mh)
	}
	rateLimit := 10
	if rl, ok := args["rate_limit"].(float64); ok && rl > 0 {
		rateLimit = int(rl)
	}
	requiresAuth := false
	if ra, ok := args["requires_auth"].(bool); ok {
		requiresAuth = ra
	}
	corsOrigins, _ := args["cors_origins"].(string)

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_llm_endpoints (path, system_prompt, model_id, max_tokens, temperature, max_history, rate_limit, requires_auth, cors_origins)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   system_prompt = excluded.system_prompt,
		   model_id = excluded.model_id,
		   max_tokens = excluded.max_tokens,
		   temperature = excluded.temperature,
		   max_history = excluded.max_history,
		   rate_limit = excluded.rate_limit,
		   requires_auth = excluded.requires_auth,
		   cors_origins = excluded.cors_origins`,
		path, systemPrompt, modelID, maxTokens, temperature, maxHistory, rateLimit, requiresAuth, corsOrigins,
	)
	if err != nil {
		return nil, fmt.Errorf("creating LLM endpoint: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"path":           path,
		"chat_route":     fmt.Sprintf("POST /api/%s/chat", path),
		"complete_route": fmt.Sprintf("POST /api/%s/complete", path),
		"system_prompt":  systemPrompt,
		"max_tokens":     maxTokens,
		"temperature":    temperature,
		"max_history":    maxHistory,
		"rate_limit":     rateLimit,
		"requires_auth":  requiresAuth,
		"usage_stream":   "fetch('/api/" + path + "/chat', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({messages: [{role:'user', content:'Hello'}]})}).then(r => { const reader = r.body.getReader(); const decoder = new TextDecoder(); function read() { reader.read().then(({done, value}) => { if (done) return; const lines = decoder.decode(value).split('\\n'); for (const line of lines) { if (line.startsWith('data: ')) { try { const parsed = JSON.parse(line.slice(6)); if (parsed.text) output.textContent += parsed.text; } catch(e){} } } read(); }); } read(); });",
		"usage_complete": "fetch('/api/" + path + "/complete', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({messages: [{role:'user', content:'Hello'}]})}).then(r => r.json()).then(data => console.log(data.content));",
		"sse_format":     "SSE events: 'token' with data {\"text\":\"chunk\"} (use parsed.text NOT parsed.content), 'done' with data {}, 'error' with data {\"error\":\"msg\"}",
	}}, nil
}

func (t *EndpointsTool) updateLLM(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	setClauses := []string{}
	vals := []interface{}{}

	if v, ok := args["system_prompt"].(string); ok {
		setClauses = append(setClauses, "system_prompt = ?")
		vals = append(vals, v)
	}
	if v, ok := args["model_id"].(string); ok {
		setClauses = append(setClauses, "model_id = ?")
		vals = append(vals, v)
	}
	if v, ok := args["max_tokens"].(float64); ok {
		setClauses = append(setClauses, "max_tokens = ?")
		vals = append(vals, int(v))
	}
	if v, ok := args["temperature"].(float64); ok {
		setClauses = append(setClauses, "temperature = ?")
		vals = append(vals, v)
	}
	if v, ok := args["max_history"].(float64); ok {
		setClauses = append(setClauses, "max_history = ?")
		vals = append(vals, int(v))
	}
	if v, ok := args["rate_limit"].(float64); ok {
		setClauses = append(setClauses, "rate_limit = ?")
		vals = append(vals, int(v))
	}
	if v, ok := args["requires_auth"].(bool); ok {
		setClauses = append(setClauses, "requires_auth = ?")
		vals = append(vals, v)
	}
	if v, ok := args["cors_origins"].(string); ok {
		setClauses = append(setClauses, "cors_origins = ?")
		vals = append(vals, v)
	}

	if len(setClauses) == 0 {
		return &Result{Success: false, Error: "no fields to update"}, nil
	}

	vals = append(vals, path)
	query := fmt.Sprintf("UPDATE ho_llm_endpoints SET %s WHERE path = ?", strings.Join(setClauses, ", "))
	res, err := ctx.DB.Exec(query, vals...)
	if err != nil {
		return nil, fmt.Errorf("updating LLM endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("LLM endpoint '%s' not found", path)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"path": path, "updated": true}}, nil
}

func (t *EndpointsTool) listLLM(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query("SELECT id, path, system_prompt, model_id, max_tokens, temperature, max_history, rate_limit, requires_auth, cors_origins, created_at FROM ho_llm_endpoints ORDER BY path")
	if err != nil {
		return nil, fmt.Errorf("listing LLM endpoints: %w", err)
	}
	defer rows.Close()
	var endpoints []map[string]interface{}
	for rows.Next() {
		var id int
		var path, systemPrompt, modelID, corsOrigins string
		var maxTokens, maxHistory, rateLimit int
		var temperature float64
		var requiresAuth bool
		var createdAt string
		if err := rows.Scan(&id, &path, &systemPrompt, &modelID, &maxTokens, &temperature, &maxHistory, &rateLimit, &requiresAuth, &corsOrigins, &createdAt); err != nil {
			continue
		}
		endpoints = append(endpoints, map[string]interface{}{
			"id": id, "path": path, "system_prompt": systemPrompt, "model_id": modelID,
			"max_tokens": maxTokens, "temperature": temperature, "max_history": maxHistory,
			"rate_limit": rateLimit, "requires_auth": requiresAuth, "cors_origins": corsOrigins,
			"chat_route":     fmt.Sprintf("POST /api/%s/chat", path),
			"complete_route": fmt.Sprintf("POST /api/%s/complete", path),
			"created_at":     createdAt,
		})
	}
	return &Result{Success: true, Data: endpoints}, nil
}

func (t *EndpointsTool) deleteLLM(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	res, err := ctx.DB.Exec("DELETE FROM ho_llm_endpoints WHERE path = ?", path)
	if err != nil {
		return nil, fmt.Errorf("deleting LLM endpoint: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("LLM endpoint '%s' not found", path)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"path": path, "deleted": true}}, nil
}
