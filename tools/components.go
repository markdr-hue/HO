/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"fmt"
	"time"
)

// ComponentsTool — manage_components
// Provides CRUD operations for reusable HTML components that can be
// included in pages via {{include:component_name}} syntax.
type ComponentsTool struct{}

func (t *ComponentsTool) Name() string { return "manage_components" }
func (t *ComponentsTool) Description() string {
	return "Create, update, get, list, or delete reusable HTML components."
}

func (t *ComponentsTool) Guide() string {
	return `### Reusable Components (manage_components)
Components are named HTML snippets that can be reused across multiple pages.
Include them in page content with: {{include:component_name}}

- **save**: Create or update a component by name. Provide name and content (HTML).
- **get**: Retrieve a component's content by name.
- **list**: List all components.
- **delete**: Remove a component by name.

Example workflow:
  1. manage_components(action="save", name="product-card", content="<div class=\"card\">...</div>")
  2. manage_pages(action="save", path="/products", content="<h1>Products</h1>{{include:product-card}}")

Components are resolved at page-serve time — changes to a component instantly affect all pages that include it.
Use components for repeated UI blocks: navigation cards, footers, pricing tables, testimonial cards, etc.`
}

func (t *ComponentsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"save", "get", "list", "delete"},
				"description": "Action to perform",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Component name (alphanumeric, hyphens, underscores)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "HTML content of the component",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Brief description of what this component renders",
			},
		},
		"required": []string{},
	}
}

func (t *ComponentsTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"save":   t.save,
		"get":    t.get,
		"list":   t.list,
		"delete": t.del,
	}, nil)
}

func (t *ComponentsTool) save(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	content, _ := args["content"].(string)
	if name == "" || content == "" {
		return &Result{Success: false, Error: "name and content are required"}, nil
	}

	desc, _ := args["description"].(string)

	_, err := ctx.DB.Exec(
		`INSERT INTO ho_components (name, content, description, updated_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(name) DO UPDATE SET
		   content = excluded.content,
		   description = excluded.description,
		   updated_at = CURRENT_TIMESTAMP`,
		name, content, desc,
	)
	if err != nil {
		return nil, fmt.Errorf("saving component: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"name":    name,
		"message": "Component saved. Include in pages with {{include:" + name + "}}",
	}}, nil
}

func (t *ComponentsTool) get(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	var content, desc string
	err := ctx.DB.QueryRow(
		"SELECT content, description FROM ho_components WHERE name = ?", name,
	).Scan(&content, &desc)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("component '%s' not found", name)}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"name":        name,
		"content":     content,
		"description": desc,
	}}, nil
}

func (t *ComponentsTool) list(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	rows, err := ctx.DB.Query(
		"SELECT name, description, updated_at FROM ho_components ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("listing components: %w", err)
	}
	defer rows.Close()

	var components []map[string]interface{}
	for rows.Next() {
		var name string
		var desc sql.NullString
		var updatedAt time.Time
		if err := rows.Scan(&name, &desc, &updatedAt); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"name":       name,
			"updated_at": updatedAt,
		}
		if desc.Valid {
			entry["description"] = desc.String
		}
		components = append(components, entry)
	}

	return &Result{Success: true, Data: components}, nil
}

func (t *ComponentsTool) del(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return &Result{Success: false, Error: "name is required"}, nil
	}

	res, err := ctx.DB.Exec("DELETE FROM ho_components WHERE name = ?", name)
	if err != nil {
		return nil, fmt.Errorf("deleting component: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("component '%s' not found", name)}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"name":    name,
		"deleted": true,
	}}, nil
}
