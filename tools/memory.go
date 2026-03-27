/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"fmt"
	"time"
)

const (
	memoryMaxEntries  = 100
	memoryMaxValueLen = 500
)

// ---------------------------------------------------------------------------
// manage_memory
// ---------------------------------------------------------------------------

// MemoryTool provides persistent key-value memory that survives across chat
// sessions. The brain uses this to remember user preferences, past decisions,
// and site history.
type MemoryTool struct{}

func (t *MemoryTool) Name() string { return "manage_memory" }
func (t *MemoryTool) Description() string {
	return "Store and recall persistent memories across chat sessions — preferences, decisions, history."
}

func (t *MemoryTool) Guide() string {
	return `### Memory (manage_memory)
- store: Save a memory (key, value, category). Upserts — existing keys are updated.
- recall: Retrieve a single memory by key.
- list: List all memories, optionally filtered by category. Max 50 returned.
- forget: Delete a memory by key.
Categories: preferences, decisions, history, facts, general.
Use this to remember owner preferences, design decisions, past issues, and site context across sessions.`
}

func (t *MemoryTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"store", "recall", "list", "forget"},
				"description": "Action to perform",
			},
			"key": map[string]interface{}{
				"type":        "string",
				"description": "Memory key (unique identifier). Required for store, recall, forget.",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "Memory value (max 500 chars). Required for store.",
			},
			"category": map[string]interface{}{
				"type":        "string",
				"description": "Memory category: preferences, decisions, history, facts, general. Optional for store (default: general), optional filter for list.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *MemoryTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"store":  t.store,
		"recall": t.recall,
		"list":   t.list,
		"forget": t.forget,
	}, nil)
}

func (t *MemoryTool) store(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	key, errR := RequireString(args, "key")
	if errR != nil {
		return errR, nil
	}
	value, errR := RequireString(args, "value")
	if errR != nil {
		return errR, nil
	}
	category := OptionalString(args, "category", "general")

	// Enforce value length limit.
	if len(value) > memoryMaxValueLen {
		value = value[:memoryMaxValueLen]
	}

	// Check entry count before inserting a new key.
	var existing int
	ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_memory WHERE key = ?", key).Scan(&existing)
	if existing == 0 {
		var total int
		ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_memory").Scan(&total)
		if total >= memoryMaxEntries {
			return &Result{Success: false, Error: fmt.Sprintf("memory limit reached (%d entries) — use forget to remove old memories first", memoryMaxEntries)}, nil
		}
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := ctx.DB.Exec(
		"INSERT INTO ho_memory (key, value, category, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, category = excluded.category, updated_at = excluded.updated_at",
		key, value, category, now,
	)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to store memory: %v", err)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"key": key, "category": category}}, nil
}

func (t *MemoryTool) recall(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	key, errR := RequireString(args, "key")
	if errR != nil {
		return errR, nil
	}

	var value, category string
	var updatedAt string
	err := ctx.DB.QueryRow("SELECT value, category, updated_at FROM ho_memory WHERE key = ?", key).Scan(&value, &category, &updatedAt)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("memory not found: %s", key)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{
		"key": key, "value": value, "category": category, "updated_at": updatedAt,
	}}, nil
}

func (t *MemoryTool) list(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	category := OptionalString(args, "category", "")

	var query string
	var queryArgs []interface{}
	if category != "" {
		query = "SELECT key, value, category, updated_at FROM ho_memory WHERE category = ? ORDER BY updated_at DESC LIMIT 50"
		queryArgs = []interface{}{category}
	} else {
		query = "SELECT key, value, category, updated_at FROM ho_memory ORDER BY updated_at DESC LIMIT 50"
	}

	rows, err := ctx.DB.Query(query, queryArgs...)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to list memories: %v", err)}, nil
	}
	defer rows.Close()

	var memories []map[string]interface{}
	for rows.Next() {
		var key, value, cat, updatedAt string
		if rows.Scan(&key, &value, &cat, &updatedAt) == nil {
			memories = append(memories, map[string]interface{}{
				"key": key, "value": value, "category": cat, "updated_at": updatedAt,
			})
		}
	}
	return &Result{Success: true, Data: map[string]interface{}{"memories": memories, "count": len(memories)}}, nil
}

func (t *MemoryTool) forget(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	key, errR := RequireString(args, "key")
	if errR != nil {
		return errR, nil
	}

	result, err := ctx.DB.Exec("DELETE FROM ho_memory WHERE key = ?", key)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to forget: %v", err)}, nil
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("memory not found: %s", key)}, nil
	}
	return &Result{Success: true, Data: map[string]interface{}{"key": key, "deleted": true}}, nil
}
