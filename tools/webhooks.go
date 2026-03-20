/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// manage_webhooks — unified webhook manager
// ---------------------------------------------------------------------------

type WebhooksTool struct{}

func (t *WebhooksTool) Name() string { return "manage_webhooks" }
func (t *WebhooksTool) Description() string {
	return "Create, get, list, delete, update, or subscribe ho_webhooks to events."
}

func (t *WebhooksTool) Guide() string {
	return `### Webhooks (manage_webhooks)
- Incoming: receive at /webhooks/{name}. HMAC-SHA256 validation.
- Outgoing: fire on events to external URLs.
- Events: data.insert, data.update, data.delete, webhook.received, payment.completed, ws.message.`
}

func (t *WebhooksTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":     map[string]interface{}{"type": "string", "enum": []string{"create", "get", "list", "delete", "update", "subscribe", "test"}, "description": "Action to perform"},
			"name":       map[string]interface{}{"type": "string", "description": "Webhook name (for create, get, delete, update, subscribe)"},
			"url":        map[string]interface{}{"type": "string", "description": "URL for outgoing ho_webhooks (omit for incoming)"},
			"is_enabled": map[string]interface{}{"type": "boolean", "description": "Enable or disable the webhook"},
			"event_types": map[string]interface{}{
				"type":        "array",
				"description": "Event types to subscribe to (e.g. site.updated, brain.started, tool.executed)",
				"items":       map[string]interface{}{"type": "string"},
			},
		},
		"required": []string{},
	}
}

func (t *WebhooksTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"create":    t.create,
		"get":       t.get,
		"list":      t.list,
		"delete":    t.delete,
		"update":    t.update,
		"subscribe": t.subscribe,
		"test":      t.test,
	}, func(a map[string]interface{}) string {
		if _, has := a["event_types"]; has {
			return "subscribe"
		}
		if _, has := a["url"]; has {
			return "create"
		}
		if _, has := a["is_enabled"]; has {
			return "update"
		}
		if _, has := a["name"]; has {
			return "get"
		}
		return "list"
	})
}

func (t *WebhooksTool) create(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}
	url, _ := args["url"].(string)

	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("generating secret: %w", err)
	}
	secret := hex.EncodeToString(secretBytes)

	direction := "incoming"
	if url != "" {
		direction = "outgoing"
	}

	result, err := ctx.DB.Exec(
		"INSERT INTO ho_webhooks (name, secret, url, direction) VALUES (?, ?, ?, ?)",
		name, secret, url, direction,
	)
	if err != nil {
		return nil, fmt.Errorf("creating webhook: %w", err)
	}

	id, _ := result.LastInsertId()
	data := map[string]interface{}{
		"id":        id,
		"name":      name,
		"secret":    secret,
		"direction": direction,
	}
	if url != "" {
		data["url"] = url
	}
	return &Result{Success: true, Data: data}, nil
}

func (t *WebhooksTool) get(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	var id int
	var secret, direction string
	var url sql.NullString
	var isEnabled bool
	var lastTriggered sql.NullTime
	var createdAt time.Time

	err := ctx.DB.QueryRow(
		"SELECT id, secret, url, direction, is_enabled, last_triggered, created_at FROM ho_webhooks WHERE name = ?",
		name,
	).Scan(&id, &secret, &url, &direction, &isEnabled, &lastTriggered, &createdAt)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: "webhook not found"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying webhook: %w", err)
	}

	wh := map[string]interface{}{
		"id":         id,
		"name":       name,
		"secret":     secret,
		"direction":  direction,
		"is_enabled": isEnabled,
		"created_at": createdAt,
	}
	if url.Valid && url.String != "" {
		wh["url"] = url.String
	}
	if lastTriggered.Valid {
		wh["last_triggered"] = lastTriggered.Time
	}

	rows, err := ctx.DB.Query(
		"SELECT event_type FROM ho_webhook_subscriptions WHERE webhook_id = ? ORDER BY event_type",
		id,
	)
	if err == nil {
		defer rows.Close()
		var subs []string
		for rows.Next() {
			var et string
			if rows.Scan(&et) == nil {
				subs = append(subs, et)
			}
		}
		if len(subs) > 0 {
			wh["subscriptions"] = subs
		}
	}

	return &Result{Success: true, Data: wh}, nil
}

func (t *WebhooksTool) list(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT id, name, direction, url, is_enabled, last_triggered, created_at FROM ho_webhooks ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("listing ho_webhooks: %w", err)
	}
	defer rows.Close()

	var webhooks []map[string]interface{}
	for rows.Next() {
		var id int
		var name, direction string
		var url sql.NullString
		var isEnabled bool
		var lastTriggered sql.NullTime
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &direction, &url, &isEnabled, &lastTriggered, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning webhook: %w", err)
		}
		wh := map[string]interface{}{
			"id":         id,
			"name":       name,
			"direction":  direction,
			"is_enabled": isEnabled,
			"created_at": createdAt,
		}
		if url.Valid && url.String != "" {
			wh["url"] = url.String
		}
		if lastTriggered.Valid {
			wh["last_triggered"] = lastTriggered.Time
		}
		webhooks = append(webhooks, wh)
	}

	return &Result{Success: true, Data: webhooks}, nil
}

func (t *WebhooksTool) delete(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	res, err := ctx.DB.Exec(
		"DELETE FROM ho_webhooks WHERE name = ?",
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("deleting webhook: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "webhook not found"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{"deleted": name}}, nil
}

func (t *WebhooksTool) update(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	var setClauses []string
	var values []interface{}

	if url, ok := args["url"].(string); ok {
		setClauses = append(setClauses, "url = ?")
		values = append(values, url)
	}
	if isEnabled, ok := args["is_enabled"].(bool); ok {
		setClauses = append(setClauses, "is_enabled = ?")
		values = append(values, isEnabled)
	}

	if len(setClauses) == 0 {
		return &Result{Success: false, Error: "provide at least one field to update (url or is_enabled)"}, nil
	}

	values = append(values, name)
	query := fmt.Sprintf("UPDATE ho_webhooks SET %s WHERE name = ?",
		strings.Join(setClauses, ", "))

	res, err := ctx.DB.Exec(query, values...)
	if err != nil {
		return nil, fmt.Errorf("updating webhook: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return &Result{Success: false, Error: "webhook not found"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{"updated": name}}, nil
}

func (t *WebhooksTool) subscribe(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	eventTypesRaw, _ := args["event_types"].([]interface{})
	if len(eventTypesRaw) == 0 {
		return &Result{Success: false, Error: "event_types is required"}, nil
	}

	var webhookID int
	err := ctx.DB.QueryRow(
		"SELECT id FROM ho_webhooks WHERE name = ?",
		name,
	).Scan(&webhookID)
	if err != nil {
		return &Result{Success: false, Error: "webhook not found"}, nil
	}

	var subscribed []string
	for _, et := range eventTypesRaw {
		eventType, ok := et.(string)
		if !ok || eventType == "" {
			continue
		}
		_, err := ctx.DB.Exec(
			"INSERT INTO ho_webhook_subscriptions (webhook_id, event_type) VALUES (?, ?) ON CONFLICT(webhook_id, event_type) DO NOTHING",
			webhookID, eventType,
		)
		if err == nil {
			subscribed = append(subscribed, eventType)
		}
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"name":       name,
		"subscribed": subscribed,
	}}, nil
}

func (t *WebhooksTool) test(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	var url sql.NullString
	var secret, direction string
	var isEnabled bool
	err := ctx.DB.QueryRow(
		"SELECT url, secret, direction, is_enabled FROM ho_webhooks WHERE name = ?",
		name,
	).Scan(&url, &secret, &direction, &isEnabled)
	if err == sql.ErrNoRows {
		return &Result{Success: false, Error: "webhook not found"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying webhook: %w", err)
	}

	if direction != "outgoing" {
		return &Result{Success: false, Error: "test only works for outgoing ho_webhooks"}, nil
	}
	if !url.Valid || url.String == "" {
		return &Result{Success: false, Error: "webhook has no URL configured"}, nil
	}

	// Send a test payload.
	payload := map[string]interface{}{
		"event":     "webhook.test",
		"site_id":   ctx.SiteID,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]interface{}{
			"test":    true,
			"message": "Test delivery from HO",
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", url.String, bytes.NewReader(body))
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "HO-Webhook/1.0")

	// Sign with HMAC if secret is available.
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Webhook-Signature", "sha256="+sig)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("delivery failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	// Log the test delivery.
	ctx.DB.Exec(
		"INSERT INTO ho_webhook_logs (webhook_id, direction, event_type, payload, status_code, response, success) SELECT id, 'outgoing', 'webhook.test', ?, ?, ?, ? FROM ho_webhooks WHERE name = ?",
		string(body), resp.StatusCode, string(respBody), resp.StatusCode >= 200 && resp.StatusCode < 300, name,
	)

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	return &Result{Success: success, Data: map[string]interface{}{
		"name":        name,
		"url":         url.String,
		"status_code": resp.StatusCode,
		"response":    string(respBody),
	}}, nil
}
