/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// manage_testing — self-testing tool for built endpoints and pages
// ---------------------------------------------------------------------------

type TestingTool struct{}

func (t *TestingTool) Name() string { return "manage_testing" }
func (t *TestingTool) Description() string {
	return "Test built endpoints and pages by making HTTP requests to the public server."
}
func (t *TestingTool) Guide() string {
	return `### Self-Testing (manage_testing)
- test_endpoint: GET/POST/PUT/DELETE to /api/{path}, checks status and response shape.
- test_auth_flow: Registers a test user, logs in, calls /me — validates full JWT flow.
- test_page: Fetches a page by path, checks it returns 200 and has content.
- Use auth_token (from test_auth_flow result) for authenticated endpoint tests.
- Only use during BUILD to verify or during VALIDATE to confirm fixes.`
}

func (t *TestingTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"test_endpoint", "test_auth_flow", "test_page"},
				"description": "Test action to perform",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Endpoint path (e.g. 'posts') or page path (e.g. '/about')",
			},
			"method": map[string]interface{}{
				"type":        "string",
				"description": "HTTP method for test_endpoint (default: GET)",
				"enum":        []string{"GET", "POST", "PUT", "DELETE"},
			},
			"body": map[string]interface{}{
				"type":        "object",
				"description": "Request body for POST/PUT requests",
			},
			"expected_status": map[string]interface{}{
				"type":        "number",
				"description": "Expected HTTP status code (default: 200)",
			},
			"expected_fields": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Fields that must exist in the JSON response",
			},
			"auth_token": map[string]interface{}{
				"type":        "string",
				"description": "Bearer token for authenticated requests",
			},
		},
		"required": []string{},
	}
}

func (t *TestingTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"test_endpoint":  t.testEndpoint,
		"test_auth_flow": t.testAuthFlow,
		"test_page":      t.testPage,
	}, nil)
}

func (t *TestingTool) resolveDomain(ctx *ToolContext) (string, int, error) {
	port := ctx.PublicPort
	if port == 0 {
		port = 5000
	}
	var domain sql.NullString
	ctx.GlobalDB.QueryRow("SELECT domain FROM sites WHERE id = ?", ctx.SiteID).Scan(&domain)
	if !domain.Valid || domain.String == "" {
		return "", 0, fmt.Errorf("no domain configured for site — cannot test")
	}
	return domain.String, port, nil
}

func (t *TestingTool) makeRequest(domain string, port int, method, urlPath string, body interface{}, token string) (int, []byte, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, urlPath)
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshaling body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Host = domain
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, respBody, nil
}

func (t *TestingTool) testEndpoint(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	domain, port, err := t.resolveDomain(ctx)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	method := "GET"
	if m, ok := args["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}

	expectedStatus := 200
	if es, ok := args["expected_status"].(float64); ok && es > 0 {
		expectedStatus = int(es)
	}

	token, _ := args["auth_token"].(string)
	body := args["body"]

	urlPath := "/api/" + path
	status, respBody, err := t.makeRequest(domain, port, method, urlPath, body, token)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("request failed: %v", err)}, nil
	}

	result := map[string]interface{}{
		"url":             urlPath,
		"method":          method,
		"status":          status,
		"expected_status": expectedStatus,
		"response_size":   len(respBody),
	}

	if status != expectedStatus {
		result["response_preview"] = string(respBody[:min(len(respBody), 500)])
		return &Result{Success: false, Error: fmt.Sprintf("expected status %d, got %d", expectedStatus, status), Data: result}, nil
	}

	// Check expected fields in JSON response.
	if fields, ok := args["expected_fields"].([]interface{}); ok && len(fields) > 0 {
		var parsed map[string]interface{}
		if json.Unmarshal(respBody, &parsed) == nil {
			var missing []string
			for _, f := range fields {
				if fname, ok := f.(string); ok {
					if _, exists := parsed[fname]; !exists {
						missing = append(missing, fname)
					}
				}
			}
			if len(missing) > 0 {
				result["missing_fields"] = missing
				return &Result{Success: false, Error: fmt.Sprintf("missing fields: %s", strings.Join(missing, ", ")), Data: result}, nil
			}
		}
	}

	result["passed"] = true
	return &Result{Success: true, Data: result}, nil
}

func (t *TestingTool) testAuthFlow(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "auth" // default auth path
	}

	domain, port, err := t.resolveDomain(ctx)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Look up auth endpoint config to get column names.
	var usernameCol, passwordCol string
	ctx.DB.QueryRow("SELECT username_column, password_column FROM ho_auth_endpoints WHERE path = ?", path).Scan(&usernameCol, &passwordCol)
	if usernameCol == "" {
		usernameCol = "email"
	}
	if passwordCol == "" {
		passwordCol = "password"
	}

	testUser := map[string]interface{}{
		usernameCol: fmt.Sprintf("test_%d@test.local", time.Now().UnixNano()%100000),
		passwordCol: "TestPass123!",
	}

	steps := []map[string]interface{}{}

	// Step 1: Register
	regStatus, regBody, regErr := t.makeRequest(domain, port, "POST", "/api/"+path+"/register", testUser, "")
	regStep := map[string]interface{}{"step": "register", "status": regStatus}
	if regErr != nil {
		regStep["error"] = regErr.Error()
		steps = append(steps, regStep)
		return &Result{Success: false, Error: "register failed: " + regErr.Error(), Data: steps}, nil
	}
	if regStatus != 200 && regStatus != 201 {
		regStep["response"] = string(regBody[:min(len(regBody), 300)])
		steps = append(steps, regStep)
		return &Result{Success: false, Error: fmt.Sprintf("register returned %d", regStatus), Data: steps}, nil
	}
	regStep["passed"] = true
	steps = append(steps, regStep)

	// Step 2: Login
	loginStatus, loginBody, loginErr := t.makeRequest(domain, port, "POST", "/api/"+path+"/login", testUser, "")
	loginStep := map[string]interface{}{"step": "login", "status": loginStatus}
	if loginErr != nil {
		loginStep["error"] = loginErr.Error()
		steps = append(steps, loginStep)
		return &Result{Success: false, Error: "login failed: " + loginErr.Error(), Data: steps}, nil
	}
	if loginStatus != 200 {
		loginStep["response"] = string(loginBody[:min(len(loginBody), 300)])
		steps = append(steps, loginStep)
		return &Result{Success: false, Error: fmt.Sprintf("login returned %d", loginStatus), Data: steps}, nil
	}

	var loginResp map[string]interface{}
	json.Unmarshal(loginBody, &loginResp)
	token, _ := loginResp["token"].(string)
	if token == "" {
		loginStep["response"] = string(loginBody[:min(len(loginBody), 300)])
		steps = append(steps, loginStep)
		return &Result{Success: false, Error: "login response missing 'token' field", Data: steps}, nil
	}
	loginStep["passed"] = true
	steps = append(steps, loginStep)

	// Step 3: /me
	meStatus, meBody, meErr := t.makeRequest(domain, port, "GET", "/api/"+path+"/me", nil, token)
	meStep := map[string]interface{}{"step": "me", "status": meStatus}
	if meErr != nil {
		meStep["error"] = meErr.Error()
		steps = append(steps, meStep)
		return &Result{Success: false, Error: "/me failed: " + meErr.Error(), Data: steps}, nil
	}
	if meStatus != 200 {
		meStep["response"] = string(meBody[:min(len(meBody), 300)])
		steps = append(steps, meStep)
		return &Result{Success: false, Error: fmt.Sprintf("/me returned %d", meStatus), Data: steps}, nil
	}
	meStep["passed"] = true
	steps = append(steps, meStep)

	return &Result{Success: true, Data: map[string]interface{}{
		"steps": steps,
		"token": token,
	}}, nil
}

func (t *TestingTool) testPage(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	domain, port, err := t.resolveDomain(ctx)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	expectedStatus := 200
	if es, ok := args["expected_status"].(float64); ok && es > 0 {
		expectedStatus = int(es)
	}

	status, body, err := t.makeRequest(domain, port, "GET", path, nil, "")
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("page request failed: %v", err)}, nil
	}

	result := map[string]interface{}{
		"path":            path,
		"status":          status,
		"expected_status": expectedStatus,
		"response_size":   len(body),
	}

	if status != expectedStatus {
		result["response_preview"] = string(body[:min(len(body), 500)])
		return &Result{Success: false, Error: fmt.Sprintf("expected status %d, got %d", expectedStatus, status), Data: result}, nil
	}

	if len(body) < 50 {
		return &Result{Success: false, Error: "page response too short (< 50 bytes)", Data: result}, nil
	}

	result["passed"] = true
	return &Result{Success: true, Data: result}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
