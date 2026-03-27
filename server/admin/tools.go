/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"net/http"
)

// ToolsHandler lists available tools.
type ToolsHandler struct {
	deps *Deps
}

type toolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// List returns all registered tools.
func (h *ToolsHandler) List(w http.ResponseWriter, r *http.Request) {
	registeredTools := h.deps.ToolRegistry.List()

	result := make([]toolInfo, 0, len(registeredTools))
	for _, t := range registeredTools {
		result = append(result, toolInfo{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}

	writeJSON(w, http.StatusOK, result)
}
