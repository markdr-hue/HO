/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"encoding/json"
	"fmt"
)

// ---------------------------------------------------------------------------
// manage_plan — mid-build plan amendments
// ---------------------------------------------------------------------------

type PlanAmendTool struct{}

func (t *PlanAmendTool) Name() string { return "manage_plan" }
func (t *PlanAmendTool) Description() string {
	return "Amend the build plan during BUILD by adding tables or endpoints discovered to be needed."
}
func (t *PlanAmendTool) Guide() string {
	return `### Plan Amendments (manage_plan)
- Use during BUILD when you discover the plan is missing a table or endpoint.
- add_table: adds a table definition to the plan (then create it with manage_schema).
- add_endpoint: adds an endpoint to the plan (then create it with manage_endpoints).
- get_plan: returns the current plan JSON for reference.
- Max 5 amendments per build to prevent runaway plan mutation.
- Cannot remove items that are already built — only ADD new items.`
}

func (t *PlanAmendTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"add_table", "add_endpoint", "get_plan"},
				"description": "Amendment action",
			},
			"table": map[string]interface{}{
				"type":        "object",
				"description": "Table definition for add_table: {\"name\":\"...\", \"purpose\":\"...\", \"columns\":[{\"name\":\"col\",\"type\":\"TEXT\"}]}",
			},
			"endpoint": map[string]interface{}{
				"type":        "object",
				"description": "Endpoint definition for add_endpoint: {\"action\":\"create_api\", \"path\":\"...\", \"table_name\":\"...\"}",
			},
		},
		"required": []string{},
	}
}

func (t *PlanAmendTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"add_table":    t.addTable,
		"add_endpoint": t.addEndpoint,
		"get_plan":     t.getPlan,
	}, nil)
}

// maxAmendments limits plan mutations to prevent runaway changes.
const maxAmendments = 5

func (t *PlanAmendTool) countAmendments(ctx *ToolContext) int {
	var count int
	ctx.DB.QueryRow("SELECT COALESCE(JSON_ARRAY_LENGTH(JSON_EXTRACT(plan_json, '$.amendments')), 0) FROM ho_pipeline_state WHERE id = 1").Scan(&count)
	return count
}

func (t *PlanAmendTool) addTable(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	tableRaw, ok := args["table"].(map[string]interface{})
	if !ok || len(tableRaw) == 0 {
		return &Result{Success: false, Error: "table definition is required"}, nil
	}

	name, _ := tableRaw["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "table name is required"}, nil
	}

	if t.countAmendments(ctx) >= maxAmendments {
		return &Result{Success: false, Error: fmt.Sprintf("max %d plan amendments reached per build", maxAmendments)}, nil
	}

	// Load current plan.
	var planJSON string
	err := ctx.DB.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSON)
	if err != nil || planJSON == "" {
		return &Result{Success: false, Error: "no plan loaded"}, nil
	}

	var plan map[string]interface{}
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return &Result{Success: false, Error: "failed to parse plan"}, nil
	}

	// Check for duplicates.
	if tables, ok := plan["tables"].([]interface{}); ok {
		for _, t := range tables {
			if tm, ok := t.(map[string]interface{}); ok {
				if tm["name"] == name {
					return &Result{Success: false, Error: fmt.Sprintf("table '%s' already in plan", name)}, nil
				}
			}
		}
	}

	// Add the table.
	tables, _ := plan["tables"].([]interface{})
	tables = append(tables, tableRaw)
	plan["tables"] = tables

	// Track amendment.
	amendments, _ := plan["amendments"].([]interface{})
	amendments = append(amendments, map[string]interface{}{
		"type": "add_table",
		"name": name,
	})
	plan["amendments"] = amendments

	// Persist.
	updatedJSON, err := json.Marshal(plan)
	if err != nil {
		return &Result{Success: false, Error: "failed to marshal updated plan"}, nil
	}
	ctx.DB.Exec("UPDATE ho_pipeline_state SET plan_json = ? WHERE id = 1", string(updatedJSON))

	return &Result{Success: true, Data: map[string]interface{}{
		"added":           "table",
		"name":            name,
		"total_amendments": len(amendments),
	}}, nil
}

func (t *PlanAmendTool) addEndpoint(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	epRaw, ok := args["endpoint"].(map[string]interface{})
	if !ok || len(epRaw) == 0 {
		return &Result{Success: false, Error: "endpoint definition is required"}, nil
	}

	path, _ := epRaw["path"].(string)
	if path == "" {
		return &Result{Success: false, Error: "endpoint path is required"}, nil
	}

	if t.countAmendments(ctx) >= maxAmendments {
		return &Result{Success: false, Error: fmt.Sprintf("max %d plan amendments reached per build", maxAmendments)}, nil
	}

	// Load current plan.
	var planJSON string
	err := ctx.DB.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSON)
	if err != nil || planJSON == "" {
		return &Result{Success: false, Error: "no plan loaded"}, nil
	}

	var plan map[string]interface{}
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return &Result{Success: false, Error: "failed to parse plan"}, nil
	}

	// Check for duplicates.
	if endpoints, ok := plan["endpoints"].([]interface{}); ok {
		for _, e := range endpoints {
			if em, ok := e.(map[string]interface{}); ok {
				if em["path"] == path {
					return &Result{Success: false, Error: fmt.Sprintf("endpoint '%s' already in plan", path)}, nil
				}
			}
		}
	}

	// Add the endpoint.
	endpoints, _ := plan["endpoints"].([]interface{})
	endpoints = append(endpoints, epRaw)
	plan["endpoints"] = endpoints

	// Track amendment.
	amendments, _ := plan["amendments"].([]interface{})
	amendments = append(amendments, map[string]interface{}{
		"type": "add_endpoint",
		"path": path,
	})
	plan["amendments"] = amendments

	// Persist.
	updatedJSON, err := json.Marshal(plan)
	if err != nil {
		return &Result{Success: false, Error: "failed to marshal updated plan"}, nil
	}
	ctx.DB.Exec("UPDATE ho_pipeline_state SET plan_json = ? WHERE id = 1", string(updatedJSON))

	return &Result{Success: true, Data: map[string]interface{}{
		"added":           "endpoint",
		"path":            path,
		"total_amendments": len(amendments),
	}}, nil
}

func (t *PlanAmendTool) getPlan(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	var planJSON string
	err := ctx.DB.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSON)
	if err != nil || planJSON == "" {
		return &Result{Success: false, Error: "no plan loaded"}, nil
	}

	var plan map[string]interface{}
	json.Unmarshal([]byte(planJSON), &plan)

	return &Result{Success: true, Data: plan}, nil
}
