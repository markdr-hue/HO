/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// EmailTool — manage_email
// ---------------------------------------------------------------------------

// EmailTool provides email configuration, template management, and sending
// via any configured email service provider (SendGrid, Mailgun, Resend, etc.).
type EmailTool struct{}

func (t *EmailTool) Name() string { return "manage_email" }
func (t *EmailTool) Description() string {
	return "Configure, send, save templates, or list templates for email."
}

func (t *EmailTool) Guide() string {
	return `### Email (manage_email)
Setup flow: 1) Store API key via manage_secrets, 2) Create service provider via manage_providers, 3) Configure email via manage_email(action="configure").
- **configure**: Link a service provider. Set provider_name, provider_type (sendgrid/mailgun/resend/ses/generic), from_address, from_name.
- **send**: Send an email. Provide to, subject, and html_body or text_body. Optionally use template_name with variables.
- **save_template**: Store reusable email templates with {{variable}} placeholders.
- **list_templates**: List saved templates.`
}

func (t *EmailTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"configure", "send", "save_template", "list_templates"},
				"description": "Action to perform",
			},
			"provider_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the service provider to use (must already exist in manage_providers). For configure.",
			},
			"provider_type": map[string]interface{}{
				"type":        "string",
				"description": "Provider type hint for request formatting: sendgrid, mailgun, resend, ses, generic. For configure.",
				"enum":        []string{"sendgrid", "mailgun", "resend", "ses", "generic"},
			},
			"from_address": map[string]interface{}{
				"type":        "string",
				"description": "Default sender email address. For configure.",
			},
			"from_name": map[string]interface{}{
				"type":        "string",
				"description": "Default sender display name. For configure.",
			},
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Recipient email address. For send.",
			},
			"subject": map[string]interface{}{
				"type":        "string",
				"description": "Email subject line. For send.",
			},
			"body_html": map[string]interface{}{
				"type":        "string",
				"description": "HTML email body. For send.",
			},
			"body_text": map[string]interface{}{
				"type":        "string",
				"description": "Plain text email body (fallback). For send.",
			},
			"template_name": map[string]interface{}{
				"type":        "string",
				"description": "Template name to use or save. For send/save_template.",
			},
			"template_vars": map[string]interface{}{
				"type":        "object",
				"description": "Variables to substitute in template ({{key}} → value). For send.",
			},
		},
		"required": []string{},
	}
}

func (t *EmailTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"configure":      t.configure,
		"send":           t.send,
		"save_template":  t.saveTemplate,
		"list_templates": t.listTemplates,
	}, nil)
}

func (t *EmailTool) ensureTables(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS email_config (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		provider_name TEXT NOT NULL,
		provider_type TEXT NOT NULL DEFAULT 'generic',
		from_address TEXT NOT NULL,
		from_name TEXT DEFAULT '',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS email_templates (
		id INTEGER PRIMARY KEY,
		name TEXT UNIQUE NOT NULL,
		subject TEXT NOT NULL,
		body_html TEXT NOT NULL,
		body_text TEXT DEFAULT '',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
}

func (t *EmailTool) configure(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	providerName, _ := args["provider_name"].(string)
	providerType, _ := args["provider_type"].(string)
	fromAddress, _ := args["from_address"].(string)
	fromName, _ := args["from_name"].(string)

	if providerName == "" || fromAddress == "" {
		return &Result{Success: false, Error: "provider_name and from_address are required"}, nil
	}
	if providerType == "" {
		providerType = "generic"
	}

	// Verify the provider exists.
	var exists int
	err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_service_providers WHERE name = ?", providerName).Scan(&exists)
	if err != nil || exists == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("provider '%s' not found — create it first with manage_providers", providerName)}, nil
	}

	t.ensureTables(ctx.DB)

	_, err = ctx.DB.Exec(
		`INSERT INTO email_config (id, provider_name, provider_type, from_address, from_name)
		 VALUES (1, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   provider_name = excluded.provider_name,
		   provider_type = excluded.provider_type,
		   from_address = excluded.from_address,
		   from_name = excluded.from_name`,
		providerName, providerType, fromAddress, fromName,
	)
	if err != nil {
		return nil, fmt.Errorf("configuring email: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"provider":     providerName,
		"type":         providerType,
		"from_address": fromAddress,
		"from_name":    fromName,
	}}, nil
}

func (t *EmailTool) send(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	to, _ := args["to"].(string)
	subject, _ := args["subject"].(string)
	bodyHTML, _ := args["body_html"].(string)
	bodyText, _ := args["body_text"].(string)

	// Load template if specified.
	if tmplName, ok := args["template_name"].(string); ok && tmplName != "" {
		t.ensureTables(ctx.DB)
		var tmplSubject, tmplHTML, tmplText string
		err := ctx.DB.QueryRow(
			"SELECT subject, body_html, body_text FROM email_templates WHERE name = ?",
			tmplName,
		).Scan(&tmplSubject, &tmplHTML, &tmplText)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("template '%s' not found", tmplName)}, nil
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
	}

	// Apply template variables.
	if vars, ok := args["template_vars"].(map[string]interface{}); ok {
		for key, val := range vars {
			placeholder := "{{" + key + "}}"
			valStr := fmt.Sprintf("%v", val)
			subject = strings.ReplaceAll(subject, placeholder, valStr)
			bodyHTML = strings.ReplaceAll(bodyHTML, placeholder, valStr)
			bodyText = strings.ReplaceAll(bodyText, placeholder, valStr)
		}
	}

	if to == "" || subject == "" || (bodyHTML == "" && bodyText == "") {
		return &Result{Success: false, Error: "to, subject, and body_html (or body_text) are required"}, nil
	}

	// Load email config.
	t.ensureTables(ctx.DB)
	var providerName, providerType, fromAddress, fromName string
	err := ctx.DB.QueryRow(
		"SELECT provider_name, provider_type, from_address, from_name FROM email_config WHERE id = 1",
	).Scan(&providerName, &providerType, &fromAddress, &fromName)
	if err != nil {
		return &Result{Success: false, Error: "email not configured — use manage_email(action='configure') first"}, nil
	}

	// Load provider details.
	var baseURL, authType, authHeader, authPrefix string
	var secretName sql.NullString
	err = ctx.DB.QueryRow(
		`SELECT base_url, auth_type, auth_header, auth_prefix, secret_name
		 FROM ho_service_providers WHERE name = ? AND is_enabled = 1`,
		providerName,
	).Scan(&baseURL, &authType, &authHeader, &authPrefix, &secretName)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("email provider '%s' not found or disabled", providerName)}, nil
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
		// Mailgun uses form-encoded POST to /{domain}/messages.
		reqURL = strings.TrimRight(baseURL, "/") + "/messages"
		contentType = "application/x-www-form-urlencoded"
		form := fmt.Sprintf("from=%s <%s>&to=%s&subject=%s&html=%s",
			fromName, fromAddress, to, subject, bodyHTML)
		reqBody = form

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

	default: // "generic" or "ses"
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

	// Create and execute HTTP request.
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(reqBody))
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", contentType)

	// Inject auth from stored secret.
	if authType != "none" && secretName.Valid && secretName.String != "" {
		var encryptedValue string
		err := ctx.DB.QueryRow("SELECT value_encrypted FROM ho_secrets WHERE name = ?", secretName.String).Scan(&encryptedValue)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("secret '%s' not found", secretName.String)}, nil
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

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("sending email: %v", err)}, nil
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	if resp.StatusCode >= 400 {
		return &Result{Success: false, Error: fmt.Sprintf("email provider returned %d: %s", resp.StatusCode, string(respBody))}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"to":      to,
		"subject": subject,
		"status":  resp.StatusCode,
	}}, nil
}

func (t *EmailTool) saveTemplate(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["template_name"].(string)
	subject, _ := args["subject"].(string)
	bodyHTML, _ := args["body_html"].(string)
	bodyText, _ := args["body_text"].(string)

	if name == "" || subject == "" || bodyHTML == "" {
		return &Result{Success: false, Error: "template_name, subject, and body_html are required"}, nil
	}

	t.ensureTables(ctx.DB)

	_, err := ctx.DB.Exec(
		`INSERT INTO email_templates (name, subject, body_html, body_text)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   subject = excluded.subject,
		   body_html = excluded.body_html,
		   body_text = excluded.body_text`,
		name, subject, bodyHTML, bodyText,
	)
	if err != nil {
		return nil, fmt.Errorf("saving template: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"name":    name,
		"subject": subject,
	}}, nil
}

func (t *EmailTool) listTemplates(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	t.ensureTables(ctx.DB)

	rows, err := ctx.DB.Query("SELECT name, subject, created_at FROM email_templates ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	defer rows.Close()

	var templates []map[string]interface{}
	for rows.Next() {
		var name, subject string
		var createdAt time.Time
		if err := rows.Scan(&name, &subject, &createdAt); err != nil {
			continue
		}
		templates = append(templates, map[string]interface{}{
			"name":       name,
			"subject":    subject,
			"created_at": createdAt,
		})
	}

	return &Result{Success: true, Data: templates}, nil
}

func (t *EmailTool) Summarize(result string) string {
	r, dataMap, dataArr, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if dataArr != nil {
		return fmt.Sprintf(`{"success":true,"summary":"Listed %d email templates"}`, len(dataArr))
	}
	if status, _ := dataMap["status"].(string); status != "" {
		return fmt.Sprintf(`{"success":true,"summary":"Email: %s"}`, status)
	}
	return summarizeTruncate(result, 300)
}
