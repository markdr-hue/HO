/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package actions

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/security"
)

// Runner executes server-side actions when subscribed events fire.
// Similar to the webhook Dispatcher but runs actions locally (send email,
// HTTP request, insert/update data) instead of calling external URLs.
type Runner struct {
	siteDBMgr *db.SiteDBManager
	bus       *events.Bus
	encryptor *security.Encryptor
	logger    *slog.Logger
	client    *http.Client
}

// NewRunner creates an action runner and subscribes to all events.
func NewRunner(siteDBMgr *db.SiteDBManager, bus *events.Bus, encryptor *security.Encryptor) *Runner {
	r := &Runner{
		siteDBMgr: siteDBMgr,
		bus:       bus,
		encryptor: encryptor,
		logger:    slog.With("component", "action_runner"),
		client:    &http.Client{Timeout: 15 * time.Second},
	}

	bus.SubscribeAll(r.handleEvent)

	return r
}

// handleEvent checks if any actions are subscribed to this event type.
func (r *Runner) handleEvent(event events.Event) {
	if event.SiteID == 0 {
		return
	}

	siteDB := r.siteDBMgr.Get(event.SiteID)
	if siteDB == nil {
		return
	}

	rows, err := siteDB.Query(
		`SELECT id, name, event_filter, action_type, action_config
		 FROM ho_actions
		 WHERE event_type = ? AND is_enabled = 1`,
		string(event.Type),
	)
	if err != nil {
		// Table might not exist yet on older sites — that's fine.
		return
	}
	defer rows.Close()

	type action struct {
		id           int
		name         string
		eventFilter  string
		actionType   string
		actionConfig string
	}

	var matched []action
	for rows.Next() {
		var a action
		var filter sql.NullString
		if err := rows.Scan(&a.id, &a.name, &filter, &a.actionType, &a.actionConfig); err != nil {
			continue
		}
		if filter.Valid {
			a.eventFilter = filter.String
		}
		matched = append(matched, a)
	}

	for _, a := range matched {
		// Check event filter if present.
		if a.eventFilter != "" {
			if !matchesFilter(a.eventFilter, event.Payload) {
				continue
			}
		}
		go r.execute(siteDB.Writer(), event, a.id, a.name, a.actionType, a.actionConfig)
	}
}

// matchesFilter checks if the event payload matches the filter criteria.
// Filter is a JSON object like {"table":"users"} — all keys must match.
func matchesFilter(filterJSON string, payload map[string]interface{}) bool {
	var filter map[string]interface{}
	if err := json.Unmarshal([]byte(filterJSON), &filter); err != nil {
		return false
	}
	for key, expected := range filter {
		actual, ok := payload[key]
		if !ok {
			return false
		}
		if fmt.Sprintf("%v", actual) != fmt.Sprintf("%v", expected) {
			return false
		}
	}
	return true
}

// validIdentifier validates table and column names (alphanumeric + underscore, starts with letter).
var validIdentifier = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// templateVarRe matches {{variable_name}} placeholders.
var templateVarRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// resolveTemplate replaces {{field}} placeholders with values from the event payload.
func resolveTemplate(template string, payload map[string]interface{}) string {
	return templateVarRe.ReplaceAllStringFunc(template, func(match string) string {
		key := match[2 : len(match)-2] // strip {{ and }}
		if val, ok := payload[key]; ok {
			return fmt.Sprintf("%v", val)
		}
		return match // leave unresolved placeholders as-is
	})
}

// execute runs a single action based on its type.
func (r *Runner) execute(siteDB *sql.DB, event events.Event, actionID int, name, actionType, configJSON string) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("action panic", "action", name, "panic", rec)
		}
	}()

	// Resolve template variables in the config.
	resolved := resolveTemplate(configJSON, event.Payload)

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(resolved), &config); err != nil {
		r.logger.Error("action: invalid config JSON", "action", name, "error", err)
		return
	}

	var err error
	switch actionType {
	case "send_email":
		err = r.executeSendEmail(siteDB, config)
	case "http_request":
		err = r.executeHTTPRequest(config)
	case "insert_data":
		err = r.executeInsertData(siteDB, config)
	case "update_data":
		err = r.executeUpdateData(siteDB, config)
	default:
		r.logger.Warn("action: unknown action_type", "action", name, "type", actionType)
		return
	}

	if err != nil {
		r.logger.Error("action failed", "action", name, "type", actionType, "error", err, "resolved_config", resolved)
	} else {
		r.logger.Info("action executed", "action", name, "type", actionType)
	}
}

// executeSendEmail sends an email using the site's configured email provider.
// Config: {"to":"{{email}}", "subject":"Welcome!", "template_name":"welcome", "template_vars":{"name":"{{username}}"}}
// Or:     {"to":"{{email}}", "subject":"...", "body_html":"<h1>Hello {{username}}</h1>"}
func (r *Runner) executeSendEmail(siteDB *sql.DB, config map[string]interface{}) error {
	to, _ := config["to"].(string)
	subject, _ := config["subject"].(string)
	bodyHTML, _ := config["body_html"].(string)
	bodyText, _ := config["body_text"].(string)

	if to == "" {
		return fmt.Errorf("send_email: 'to' is required")
	}

	// Load template if specified.
	if tmplName, ok := config["template_name"].(string); ok && tmplName != "" {
		var tmplSubject, tmplHTML, tmplText string
		err := siteDB.QueryRow(
			"SELECT subject, body_html, body_text FROM email_templates WHERE name = ?",
			tmplName,
		).Scan(&tmplSubject, &tmplHTML, &tmplText)
		if err != nil {
			return fmt.Errorf("send_email: template '%s' not found", tmplName)
		}
		if subject == "" {
			subject = tmplSubject
		}
		if bodyHTML == "" {
			bodyHTML = tmplHTML
		}
		if bodyText == "" {
			bodyText = tmplText
		}

		// Apply template_vars from config (already resolved from event payload).
		if vars, ok := config["template_vars"].(map[string]interface{}); ok {
			for key, val := range vars {
				placeholder := "{{" + key + "}}"
				valStr := fmt.Sprintf("%v", val)
				subject = strings.ReplaceAll(subject, placeholder, valStr)
				bodyHTML = strings.ReplaceAll(bodyHTML, placeholder, valStr)
				bodyText = strings.ReplaceAll(bodyText, placeholder, valStr)
			}
		}
	}

	if subject == "" || (bodyHTML == "" && bodyText == "") {
		return fmt.Errorf("send_email: subject and body are required")
	}

	// Load email config.
	var providerName, providerType, fromAddress, fromName string
	err := siteDB.QueryRow(
		"SELECT provider_name, provider_type, from_address, from_name FROM email_config WHERE id = 1",
	).Scan(&providerName, &providerType, &fromAddress, &fromName)
	if err != nil {
		return fmt.Errorf("send_email: email not configured")
	}

	// Load provider details.
	var baseURL, authType, authHeader, authPrefix string
	var secretName sql.NullString
	err = siteDB.QueryRow(
		`SELECT base_url, auth_type, auth_header, auth_prefix, secret_name
		 FROM ho_service_providers WHERE name = ? AND is_enabled = 1`,
		providerName,
	).Scan(&baseURL, &authType, &authHeader, &authPrefix, &secretName)
	if err != nil {
		return fmt.Errorf("send_email: provider '%s' not found or disabled", providerName)
	}

	// Build request based on provider type.
	var reqURL, reqBody, contentType string
	switch providerType {
	case "sendgrid":
		reqURL = strings.TrimRight(baseURL, "/") + "/mail/send"
		contentType = "application/json"
		payload := map[string]interface{}{
			"personalizations": []map[string]interface{}{
				{"to": []map[string]string{{"email": to}}},
			},
			"from":    map[string]string{"email": fromAddress, "name": fromName},
			"subject": subject,
			"content": []map[string]string{
				{"type": "text/html", "value": bodyHTML},
			},
		}
		data, _ := json.Marshal(payload)
		reqBody = string(data)

	case "mailgun":
		reqURL = strings.TrimRight(baseURL, "/") + "/messages"
		contentType = "application/x-www-form-urlencoded"
		reqBody = fmt.Sprintf("from=%s <%s>&to=%s&subject=%s&html=%s",
			fromName, fromAddress, to, subject, bodyHTML)

	case "resend":
		reqURL = strings.TrimRight(baseURL, "/") + "/emails"
		contentType = "application/json"
		payload := map[string]interface{}{
			"from":    fmt.Sprintf("%s <%s>", fromName, fromAddress),
			"to":      []string{to},
			"subject": subject,
			"html":    bodyHTML,
		}
		data, _ := json.Marshal(payload)
		reqBody = string(data)

	default:
		reqURL = strings.TrimRight(baseURL, "/") + "/send"
		contentType = "application/json"
		payload := map[string]interface{}{
			"from":    fromAddress,
			"to":      to,
			"subject": subject,
			"html":    bodyHTML,
			"text":    bodyText,
		}
		data, _ := json.Marshal(payload)
		reqBody = string(data)
	}

	req, err := http.NewRequest("POST", reqURL, strings.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("send_email: creating request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	// Inject auth from stored secret.
	if authType != "none" && secretName.Valid && secretName.String != "" {
		var encryptedValue string
		err := siteDB.QueryRow("SELECT value_encrypted FROM ho_secrets WHERE name = ?", secretName.String).Scan(&encryptedValue)
		if err != nil {
			return fmt.Errorf("send_email: secret '%s' not found", secretName.String)
		}
		if r.encryptor == nil {
			return fmt.Errorf("send_email: encryption not available")
		}
		secretValue, err := r.encryptor.Decrypt(encryptedValue)
		if err != nil {
			return fmt.Errorf("send_email: failed to decrypt secret")
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

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("send_email: %w", err)
	}
	defer resp.Body.Close()
	io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("send_email: provider returned %d", resp.StatusCode)
	}

	return nil
}

// executeHTTPRequest sends an HTTP request to an external URL.
// Config: {"method":"POST", "url":"https://...", "headers":{"X-Key":"val"}, "body":{...}}
func (r *Runner) executeHTTPRequest(config map[string]interface{}) error {
	method, _ := config["method"].(string)
	url, _ := config["url"].(string)
	if method == "" {
		method = "POST"
	}
	if url == "" {
		return fmt.Errorf("http_request: 'url' is required")
	}

	var bodyReader io.Reader
	if body, ok := config["body"]; ok {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("http_request: marshaling body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(strings.ToUpper(method), url, bodyReader)
	if err != nil {
		return fmt.Errorf("http_request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "HO-Action/1.0")

	if headers, ok := config["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("http_request: %w", err)
	}
	defer resp.Body.Close()
	io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("http_request: returned %d", resp.StatusCode)
	}

	return nil
}

// executeInsertData inserts a row into a table.
// Config: {"table":"audit_log", "data":{"user_id":"{{user_id}}", "action":"registered"}}
func (r *Runner) executeInsertData(siteDB *sql.DB, config map[string]interface{}) error {
	table, _ := config["table"].(string)
	data, _ := config["data"].(map[string]interface{})
	if table == "" || len(data) == 0 {
		return fmt.Errorf("insert_data: 'table' and 'data' are required")
	}
	if !validIdentifier.MatchString(table) {
		return fmt.Errorf("insert_data: invalid table name: %s", table)
	}

	cols := make([]string, 0, len(data))
	placeholders := make([]string, 0, len(data))
	vals := make([]interface{}, 0, len(data))
	for col, val := range data {
		if !validIdentifier.MatchString(col) {
			return fmt.Errorf("insert_data: invalid column name: %s", col)
		}
		cols = append(cols, col)
		placeholders = append(placeholders, "?")
		vals = append(vals, val)
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "))

	_, err := siteDB.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("insert_data: %w", err)
	}
	return nil
}

// executeUpdateData updates rows in a table matching a WHERE clause.
// Config: {"table":"users", "set":{"email_verified":true}, "where":{"id":"{{user_id}}"}}
// Increment support: {"set":{"likes":{"$increment":1}}} generates "likes = likes + 1"
// Decrement support: {"set":{"stock":{"$decrement":1}}} generates "stock = stock - 1"
func (r *Runner) executeUpdateData(siteDB *sql.DB, config map[string]interface{}) error {
	table, _ := config["table"].(string)
	setData, _ := config["set"].(map[string]interface{})
	whereData, _ := config["where"].(map[string]interface{})
	if table == "" || len(setData) == 0 || len(whereData) == 0 {
		return fmt.Errorf("update_data: 'table', 'set', and 'where' are required (got table=%q, set keys=%d, where keys=%d)", table, len(setData), len(whereData))
	}
	if !validIdentifier.MatchString(table) {
		return fmt.Errorf("update_data: invalid table name: %s", table)
	}

	setClauses := make([]string, 0, len(setData))
	vals := make([]interface{}, 0, len(setData)+len(whereData))
	for col, val := range setData {
		if !validIdentifier.MatchString(col) {
			return fmt.Errorf("update_data: invalid column name in set: %s", col)
		}
		// Support $increment and $decrement operators.
		if obj, ok := val.(map[string]interface{}); ok {
			if inc, ok := obj["$increment"]; ok {
				setClauses = append(setClauses, col+" = "+col+" + ?")
				vals = append(vals, inc)
				continue
			}
			if dec, ok := obj["$decrement"]; ok {
				setClauses = append(setClauses, col+" = "+col+" - ?")
				vals = append(vals, dec)
				continue
			}
		}
		setClauses = append(setClauses, col+" = ?")
		vals = append(vals, val)
	}

	whereClauses := make([]string, 0, len(whereData))
	for col, val := range whereData {
		if !validIdentifier.MatchString(col) {
			return fmt.Errorf("update_data: invalid column name in where: %s", col)
		}
		whereClauses = append(whereClauses, col+" = ?")
		vals = append(vals, val)
	}

	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
		table, strings.Join(setClauses, ", "), strings.Join(whereClauses, " AND "))

	_, err := siteDB.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("update_data: %w", err)
	}
	return nil
}
