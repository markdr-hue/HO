/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"fmt"
	"sort"
)

// ActionHandler is the signature for tool action implementations.
type ActionHandler func(ctx *ToolContext, args map[string]interface{}) (*Result, error)

// DispatchAction resolves the action from args, applies optional inference if
// the action field is missing, and routes to the matching handler.
// inferAction may be nil — if so, a missing action returns an error.
func DispatchAction(
	ctx *ToolContext,
	args map[string]interface{},
	routes map[string]ActionHandler,
	inferAction func(map[string]interface{}) string,
) (*Result, error) {
	action, errResult := RequireAction(args)
	if errResult != nil {
		if inferAction != nil {
			action = inferAction(args)
			if action != "" {
				args["action"] = action
			}
		}
		if action == "" {
			return errResult, nil
		}
	}
	if handler, ok := routes[action]; ok {
		return handler(ctx, args)
	}
	// Build list of valid actions for error message.
	var valid []string
	for k := range routes {
		valid = append(valid, k)
	}
	return &Result{Success: false, Error: fmt.Sprintf("unknown action %q — valid actions: %s", action, joinSorted(valid))}, nil
}

// RequireString extracts a required string argument.
// Returns the value and nil on success, or empty string and an error Result.
func RequireString(args map[string]interface{}, key string) (string, *Result) {
	val, _ := args[key].(string)
	if val == "" {
		return "", &Result{Success: false, Error: key + " is required"}
	}
	return val, nil
}

// OptionalString extracts an optional string argument with a default value.
func OptionalString(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok && val != "" {
		return val
	}
	return defaultVal
}

// OptionalInt extracts an optional integer argument with a default value.
// Handles both float64 (JSON numbers) and int types.
func OptionalInt(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	return defaultVal
}

// OptionalBool extracts an optional boolean argument with a default value.
func OptionalBool(args map[string]interface{}, key string, defaultVal bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return defaultVal
}

// joinSorted sorts strings and joins them with ", ".
func joinSorted(s []string) string {
	if len(s) == 0 {
		return ""
	}
	sort.Strings(s)
	out := s[0]
	for _, v := range s[1:] {
		out += ", " + v
	}
	return out
}
