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
)

// ---------------------------------------------------------------------------
// ActionsTool — manage_actions
// ---------------------------------------------------------------------------

// ActionsTool provides CRUD operations for server-side actions (event-driven
// hooks that execute without the LLM at runtime).
type ActionsTool struct{}

func (t *ActionsTool) Name() string { return "manage_actions" }
func (t *ActionsTool) Description() string {
	return "Create, list, update, delete, or test server-side actions triggered by events."
}

func (t *ActionsTool) Guide() string {
	return `### Server-Side Actions (manage_actions)
Actions are event-driven hooks that run automatically at runtime without the LLM.
Use them for things like sending a welcome email on user registration, logging to an audit table on data changes, or calling an external webhook on payment completion.

- **create**: Set name, event_type, action_type, and action_config. Optional event_filter to narrow matches.
  - action_type: send_email, http_request, insert_data, update_data, trigger_webhook.
  - action_config: JSON with {{template}} variables resolved from the event payload at runtime.
  - event_filter: JSON object — all keys must match the event payload (e.g. {"table":"users"}).
- **list**: List all actions.
- **update**: Change name, event_type, event_filter, action_type, action_config, or is_enabled.
- **delete**: Remove an action by name.
- **test**: Dry-run — shows the resolved config for a given event payload without executing.

Event types: auth.register, auth.login, data.insert, data.update, data.delete, payment.completed, webhook.received, scheduled.* (from scheduler trigger_event).

trigger_webhook example — notify Slack on new order:
  manage_actions(action="create", name="notify-slack-order", event_type="data.insert",
    event_filter={"table":"orders"},
    action_type="trigger_webhook",
    action_config={"webhook_name":"slack-notify", "payload":{"text":"New order from {{customer_name}}"}})


Example — welcome email on registration:
  manage_actions(action="create", name="welcome_email", event_type="auth.register",
    event_filter={"table":"users"},
    action_type="send_email",
    action_config={"to":"{{email}}", "subject":"Welcome!", "template_name":"welcome", "template_vars":{"name":"{{username}}"}})

Example — increment likes on insert:
  manage_actions(action="create", name="increment-post-likes", event_type="data.insert",
    event_filter={"table":"post_likes"},
    action_type="update_data",
    action_config={"table":"posts", "set":{"likes":{"$increment":1}}, "where":{"id":"{{post_id}}"}})

update_data config requires: table (string), set (object of column→value), where (object of column→value).
For increments use {"$increment":N}, for decrements use {"$decrement":N}.
Event payload includes all fields from the request body — use {{field_name}} to reference them.

Prerequisite for send_email: email must be configured via manage_email(action="configure") with a provider and API key.`
}

func (t *ActionsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"create", "list", "update", "delete", "test"},
				"description": "Action to perform",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Action name (for create, update, delete, test)",
			},
			"event_type": map[string]interface{}{
				"type":        "string",
				"description": "Event type to trigger on (e.g. auth.register, data.insert)",
			},
			"event_filter": map[string]interface{}{
				"type":        "object",
				"description": "JSON filter — all keys must match event payload (e.g. {\"table\":\"users\"})",
			},
			"action_type": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"send_email", "http_request", "insert_data", "update_data"},
				"description": "Type of action to execute",
			},
			"action_config": map[string]interface{}{
				"type":        "object",
				"description": "Configuration for the action with {{template}} variables from event payload",
			},
			"is_enabled": map[string]interface{}{
				"type":        "boolean",
				"description": "Enable or disable the action (for update)",
			},
			"test_payload": map[string]interface{}{
				"type":        "object",
				"description": "Simulated event payload for dry-run testing (for test action)",
			},
		},
		"required": []string{},
	}
}

func (t *ActionsTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"create": t.create,
		"list":   t.list,
		"update": t.update,
		"delete": t.actionDelete,
		"test":   t.test,
	}, nil)
}

func (t *ActionsTool) create(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	eventType, _ := args["event_type"].(string)
	actionType, _ := args["action_type"].(string)
	actionConfig, _ := args["action_config"].(map[string]interface{})

	if name == "" || eventType == "" || actionType == "" || actionConfig == nil {
		return &Result{Success: false, Error: "name, event_type, action_type, and action_config are required"}, nil
	}

	// Validate action_type.
	validTypes := map[string]bool{"send_email": true, "http_request": true, "insert_data": true, "update_data": true}
	if !validTypes[actionType] {
		return &Result{Success: false, Error: fmt.Sprintf("invalid action_type '%s' — must be send_email, http_request, insert_data, or update_data", actionType)}, nil
	}

	configJSON, err := json.Marshal(actionConfig)
	if err != nil {
		return &Result{Success: false, Error: "invalid action_config JSON"}, nil
	}

	var filterJSON *string
	if eventFilter, ok := args["event_filter"].(map[string]interface{}); ok {
		data, _ := json.Marshal(eventFilter)
		s := string(data)
		filterJSON = &s
	}

	_, err = ctx.DB.Exec(
		`INSERT INTO ho_actions (name, event_type, event_filter, action_type, action_config)
		 VALUES (?, ?, ?, ?, ?)`,
		name, eventType, filterJSON, actionType, string(configJSON),
	)
	if err != nil {
		return nil, fmt.Errorf("creating action: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"name":        name,
		"event_type":  eventType,
		"action_type": actionType,
	}}, nil
}

func (t *ActionsTool) list(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT id, name, event_type, event_filter, action_type, action_config, is_enabled, created_at FROM ho_actions ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("listing actions: %w", err)
	}
	defer rows.Close()

	var actions []map[string]interface{}
	for rows.Next() {
		var id int
		var name, eventType, actionType, actionConfig string
		var eventFilter sql.NullString
		var isEnabled bool
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &eventType, &eventFilter, &actionType, &actionConfig, &isEnabled, &createdAt); err != nil {
			continue
		}

		entry := map[string]interface{}{
			"id":            id,
			"name":          name,
			"event_type":    eventType,
			"action_type":   actionType,
			"action_config": actionConfig,
			"is_enabled":    isEnabled,
			"created_at":    createdAt,
		}
		if eventFilter.Valid {
			entry["event_filter"] = eventFilter.String
		}
		actions = append(actions, entry)
	}

	return &Result{Success: true, Data: actions}, nil
}

func (t *ActionsTool) update(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	// Check it exists.
	var id int
	err := ctx.DB.QueryRow("SELECT id FROM ho_actions WHERE name = ?", name).Scan(&id)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("action '%s' not found", name)}, nil
	}

	// Build dynamic update.
	sets := []string{}
	vals := []interface{}{}

	if v, ok := args["event_type"].(string); ok {
		sets = append(sets, "event_type = ?")
		vals = append(vals, v)
	}
	if v, ok := args["action_type"].(string); ok {
		sets = append(sets, "action_type = ?")
		vals = append(vals, v)
	}
	if v, ok := args["action_config"].(map[string]interface{}); ok {
		data, _ := json.Marshal(v)
		sets = append(sets, "action_config = ?")
		vals = append(vals, string(data))
	}
	if v, ok := args["event_filter"].(map[string]interface{}); ok {
		data, _ := json.Marshal(v)
		sets = append(sets, "event_filter = ?")
		vals = append(vals, string(data))
	}
	if v, ok := args["is_enabled"].(bool); ok {
		sets = append(sets, "is_enabled = ?")
		vals = append(vals, v)
	}

	if len(sets) == 0 {
		return &Result{Success: false, Error: "nothing to update"}, nil
	}

	vals = append(vals, id)
	query := fmt.Sprintf("UPDATE ho_actions SET %s WHERE id = ?", strings.Join(sets, ", "))
	_, err = ctx.DB.Exec(query, vals...)
	if err != nil {
		return nil, fmt.Errorf("updating action: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{"name": name, "updated": true}}, nil
}

func (t *ActionsTool) actionDelete(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	res, err := ctx.DB.Exec("DELETE FROM ho_actions WHERE name = ?", name)
	if err != nil {
		return nil, fmt.Errorf("deleting action: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("action '%s' not found", name)}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{"name": name, "deleted": true}}, nil
}

func (t *ActionsTool) test(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	testPayload, _ := args["test_payload"].(map[string]interface{})

	if name == "" || testPayload == nil {
		return &Result{Success: false, Error: "name and test_payload are required"}, nil
	}

	var actionConfig, eventFilter sql.NullString
	var actionType string
	err := ctx.DB.QueryRow(
		"SELECT action_type, action_config, event_filter FROM ho_actions WHERE name = ?", name,
	).Scan(&actionType, &actionConfig, &eventFilter)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("action '%s' not found", name)}, nil
	}

	// Check filter match.
	filterMatch := true
	if eventFilter.Valid && eventFilter.String != "" {
		var filter map[string]interface{}
		if err := json.Unmarshal([]byte(eventFilter.String), &filter); err == nil {
			for key, expected := range filter {
				actual, ok := testPayload[key]
				if !ok || fmt.Sprintf("%v", actual) != fmt.Sprintf("%v", expected) {
					filterMatch = false
					break
				}
			}
		}
	}

	// Resolve template.
	resolved := actionConfig.String
	if actionConfig.Valid {
		resolved = resolveTemplateVars(actionConfig.String, testPayload)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"action_type":     actionType,
		"filter_match":    filterMatch,
		"resolved_config": resolved,
		"note":            "Dry run — no action was executed",
	}}, nil
}

// resolveTemplateVars replaces {{field}} placeholders with values from the payload.
func resolveTemplateVars(template string, payload map[string]interface{}) string {
	for key, val := range payload {
		placeholder := "{{" + key + "}}"
		template = strings.ReplaceAll(template, placeholder, fmt.Sprintf("%v", val))
	}
	return template
}

func (t *ActionsTool) Summarize(result string) string {
	r, dataMap, dataArr, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if dataArr != nil {
		return fmt.Sprintf(`{"success":true,"summary":"Listed %d actions"}`, len(dataArr))
	}
	if name, _ := dataMap["name"].(string); name != "" {
		return fmt.Sprintf(`{"success":true,"summary":"Action '%s' processed"}`, name)
	}
	return summarizeTruncate(result, 300)
}
