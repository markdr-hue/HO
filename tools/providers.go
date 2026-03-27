/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProvidersTool consolidates add, list, remove, update, and request into a
// single manage_providers tool.
type ProvidersTool struct{}

func (t *ProvidersTool) Name() string { return "manage_providers" }
func (t *ProvidersTool) Description() string {
	return "Add, list, remove, update, or make authenticated requests through external service providers."
}

func (t *ProvidersTool) Guide() string {
	return `### Service Providers (manage_providers)
- External service providers with authenticated HTTP (bearer, API key, basic auth).
- Use "request" action to make authenticated API calls through a configured provider.`
}

func (t *ProvidersTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"add", "list", "remove", "update", "request"},
			},
			"name":        map[string]interface{}{"type": "string", "description": "Short name for the provider (e.g. 'openai', 'stripe', 'sendgrid')"},
			"base_url":    map[string]interface{}{"type": "string", "description": "Base URL for the API (e.g. 'https://api.openai.com/v1')"},
			"auth_type":   map[string]interface{}{"type": "string", "description": "Authentication type", "enum": []string{"none", "bearer", "api_key_header", "basic"}},
			"auth_header": map[string]interface{}{"type": "string", "description": "Header name for auth (default: Authorization)"},
			"auth_prefix": map[string]interface{}{"type": "string", "description": "Auth value prefix (default: Bearer)"},
			"secret_name": map[string]interface{}{"type": "string", "description": "Name of the stored secret to use for authentication"},
			"description": map[string]interface{}{"type": "string", "description": "What this provider is used for"},
			"api_docs":    map[string]interface{}{"type": "string", "description": "API documentation notes (endpoints, request/response formats)"},
			"id":          map[string]interface{}{"type": "number", "description": "ID of the provider (for remove)"},
			"provider":    map[string]interface{}{"type": "string", "description": "Provider name for request action"},
			"path":        map[string]interface{}{"type": "string", "description": "API path to append to base URL (e.g. '/images/generations')"},
			"method":      map[string]interface{}{"type": "string", "description": "HTTP method (default: POST)", "enum": []string{"GET", "POST", "PUT", "DELETE", "PATCH"}},
			"body":        map[string]interface{}{"type": "string", "description": "Request body (for POST/PUT/PATCH)"},
			"headers":     map[string]interface{}{"type": "object", "description": "Additional request headers"},
		},
		"required": []string{},
	}
}

func (t *ProvidersTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"add":     t.executeAdd,
		"list":    t.executeList,
		"remove":  t.executeRemove,
		"update":  t.executeUpdate,
		"request": t.executeRequest,
	}, nil)
}

func (t *ProvidersTool) executeAdd(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	baseURL, _ := args["base_url"].(string)
	if name == "" || baseURL == "" {
		return &Result{Success: false, Error: "name and base_url are required"}, nil
	}

	authType, _ := args["auth_type"].(string)
	if authType == "" {
		authType = "bearer"
	}
	authHeader, _ := args["auth_header"].(string)
	if authHeader == "" {
		authHeader = "Authorization"
	}
	authPrefix, _ := args["auth_prefix"].(string)
	if authPrefix == "" {
		authPrefix = "Bearer"
	}
	secretName, _ := args["secret_name"].(string)
	description, _ := args["description"].(string)
	apiDocs, _ := args["api_docs"].(string)

	result, err := ctx.DB.Exec(
		`INSERT INTO ho_service_providers (name, base_url, auth_type, auth_header, auth_prefix, secret_name, description, api_docs)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		name, baseURL, authType, authHeader, authPrefix, secretName, description, apiDocs,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return &Result{Success: false, Error: fmt.Sprintf("provider '%s' already exists for this site", name)}, nil
		}
		return nil, fmt.Errorf("adding provider: %w", err)
	}

	id, _ := result.LastInsertId()
	return &Result{Success: true, Data: map[string]interface{}{
		"id":   id,
		"name": name,
	}}, nil
}

func (t *ProvidersTool) executeList(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		`SELECT id, name, base_url, auth_type, secret_name, description, api_docs, is_enabled, created_at
		 FROM ho_service_providers ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing providers: %w", err)
	}
	defer rows.Close()

	var providers []map[string]interface{}
	for rows.Next() {
		var id int
		var name, baseURL, authType, description, apiDocs string
		var secretName sql.NullString
		var isEnabled bool
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &baseURL, &authType, &secretName, &description, &apiDocs, &isEnabled, &createdAt); err != nil {
			ctx.Logger.Warn("scan error in service providers list", "error", err)
			continue
		}
		prov := map[string]interface{}{
			"id":          id,
			"name":        name,
			"base_url":    baseURL,
			"auth_type":   authType,
			"description": description,
			"api_docs":    apiDocs,
			"is_enabled":  isEnabled,
		}
		if secretName.Valid {
			prov["secret_name"] = secretName.String
		}
		providers = append(providers, prov)
	}

	if providers == nil {
		providers = []map[string]interface{}{}
	}
	return &Result{Success: true, Data: providers}, nil
}

func (t *ProvidersTool) executeRemove(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	var res sql.Result
	var err error

	if name, ok := args["name"].(string); ok && name != "" {
		res, err = ctx.DB.Exec(
			"DELETE FROM ho_service_providers WHERE name = ?",
			name,
		)
	} else if idFloat, ok := args["id"].(float64); ok && idFloat > 0 {
		res, err = ctx.DB.Exec(
			"DELETE FROM ho_service_providers WHERE id = ?",
			int64(idFloat),
		)
	} else {
		return &Result{Success: false, Error: "name or id is required"}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("removing provider: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return &Result{Success: false, Error: "provider not found"}, nil
	}
	return &Result{Success: true, Data: "provider removed"}, nil
}

func (t *ProvidersTool) executeUpdate(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	setClauses := []string{}
	values := []interface{}{}

	if v, ok := args["description"].(string); ok {
		setClauses = append(setClauses, "description = ?")
		values = append(values, v)
	}
	if v, ok := args["api_docs"].(string); ok {
		setClauses = append(setClauses, "api_docs = ?")
		values = append(values, v)
	}
	if v, ok := args["base_url"].(string); ok {
		setClauses = append(setClauses, "base_url = ?")
		values = append(values, v)
	}
	if v, ok := args["auth_type"].(string); ok {
		setClauses = append(setClauses, "auth_type = ?")
		values = append(values, v)
	}
	if v, ok := args["secret_name"].(string); ok {
		setClauses = append(setClauses, "secret_name = ?")
		values = append(values, v)
	}

	if len(setClauses) == 0 {
		return &Result{Success: false, Error: "no fields to update"}, nil
	}

	query := fmt.Sprintf("UPDATE ho_service_providers SET %s WHERE name = ?",
		strings.Join(setClauses, ", "))
	values = append(values, name)

	res, err := ctx.DB.Exec(query, values...)
	if err != nil {
		return nil, fmt.Errorf("updating provider: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("provider '%s' not found", name)}, nil
	}
	return &Result{Success: true, Data: "provider updated"}, nil
}

func (t *ProvidersTool) executeRequest(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	providerName, _ := args["provider"].(string)
	if providerName == "" {
		return &Result{Success: false, Error: "provider is required"}, nil
	}

	// Look up provider.
	var baseURL, authType, authHeader, authPrefix string
	var secretName sql.NullString
	err := ctx.DB.QueryRow(
		`SELECT base_url, auth_type, auth_header, auth_prefix, secret_name
		 FROM ho_service_providers
		 WHERE name = ? AND is_enabled = 1`,
		providerName,
	).Scan(&baseURL, &authType, &authHeader, &authPrefix, &secretName)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: fmt.Sprintf("provider '%s' not found or disabled", providerName)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up provider: %w", err)
	}

	// Construct full URL.
	path, _ := args["path"].(string)
	fullURL := strings.TrimRight(baseURL, "/")
	if path != "" {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		fullURL += path
	}

	method, _ := args["method"].(string)
	if method == "" {
		method = "POST"
	}

	// Build request.
	var bodyReader io.Reader
	if body, ok := args["body"].(string); ok && body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, fullURL, bodyReader)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	// Inject authentication from stored secret.
	if authType != "none" && secretName.Valid && secretName.String != "" {
		var encryptedValue string
		err := ctx.DB.QueryRow(
			"SELECT value_encrypted FROM ho_secrets WHERE name = ?",
			secretName.String,
		).Scan(&encryptedValue)
		if err == sql.ErrNoRows {
			return &Result{Success: false, Error: fmt.Sprintf("secret '%s' not found — use ask_question(type='secret') to request it from the admin", secretName.String)}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("looking up secret: %w", err)
		}

		if ctx.Encryptor == nil {
			return &Result{Success: false, Error: "encryption not available"}, nil
		}

		secretValue, err := ctx.Encryptor.Decrypt(encryptedValue)
		if err != nil {
			return &Result{Success: false, Error: "failed to decrypt secret"}, nil
		}

		switch authType {
		case "bearer":
			req.Header.Set(authHeader, authPrefix+" "+secretValue)
		case "api_key_header":
			req.Header.Set(authHeader, secretValue)
		case "basic":
			req.Header.Set(authHeader, "Basic "+secretValue)
		}
	}

	// Add any extra headers from args.
	if headers, ok := args["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("executing request: %v", err)}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	return &Result{Success: resp.StatusCode >= 200 && resp.StatusCode < 300, Data: map[string]interface{}{
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"body":        string(respBody),
		"headers":     flattenHeaders(resp.Header),
	}}, nil
}
