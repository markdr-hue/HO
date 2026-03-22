/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/markdr-hue/HO/llm"
)

// ChatToolSet is the set of tool names available during chat sessions and
// chat-wake. Shared between brain and chat packages to keep tool filtering consistent.
var ChatToolSet = map[string]bool{
	"manage_pages":         true,
	"manage_files":         true,
	"manage_layout":        true,
	"manage_data":          true,
	"manage_endpoints":     true,
	"manage_schema":        true,
	"manage_diagnostics":   true,
	"manage_analytics":     true,
	"manage_communication": true,
	"manage_scheduler":     true,
	"manage_site":          true,
	"manage_secrets":       true,
	"manage_providers":     true,
	"manage_email":         true,
	"manage_payments":      true,
	"manage_webhooks":      true,
	"make_http_request":    true,
	"manage_search":        true,
	"manage_seo":           true,
	"manage_media":         true,
	"manage_blobs":         true,
	"manage_jobs":          true,
	"manage_actions":       true,
	"manage_memory":        true,
	"manage_testing":       true,
	"manage_components":    true,
	"manage_plan":          true,
}

// Registry holds all registered tools keyed by name.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Get retrieves a tool by name, returning an error if not found.
func (r *Registry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}
	return t, nil
}

// sortedNames returns tool names in alphabetical order. Must be called with lock held.
func (r *Registry) sortedNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// List returns all registered tools in deterministic (alphabetical) order.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, name := range r.sortedNames() {
		out = append(out, r.tools[name])
	}
	return out
}

// Has returns true if a tool with the given name is registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// ToLLMTools converts every registered tool into the llm.ToolDef format
// expected by the LLM provider. Output is in deterministic (alphabetical) order.
func (r *Registry) ToLLMTools() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]llm.ToolDef, 0, len(r.tools))
	for _, name := range r.sortedNames() {
		t := r.tools[name]
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return defs
}

// BuildGuide generates a markdown capabilities reference from tools in the
// allowed set. Tools implementing Documented contribute their Guide() text;
// others fall back to a one-liner from Description(). Used by PLAN and other
// stages that need tool documentation in the system prompt.
func (r *Registry) BuildGuide(allowed map[string]bool) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var b strings.Builder
	for _, name := range r.sortedNames() {
		if !allowed[name] {
			continue
		}
		t := r.tools[name]
		if doc, ok := t.(Documented); ok {
			b.WriteString(doc.Guide())
			b.WriteByte('\n')
		} else {
			fmt.Fprintf(&b, "- **%s**: %s\n", name, t.Description())
		}
	}
	return b.String()
}

// BuildPlanGuide generates a condensed capabilities summary for the PLAN stage.
// Uses only Description() one-liners instead of full Guide() text, since the
// PLAN stage has no tools and only needs to know what's possible for planning.
func (r *Registry) BuildPlanGuide(allowed map[string]bool) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var b strings.Builder
	b.WriteString("### Platform Capabilities\n")
	for _, name := range r.sortedNames() {
		if !allowed[name] {
			continue
		}
		t := r.tools[name]
		fmt.Fprintf(&b, "- **%s**: %s\n", name, t.Description())
	}
	b.WriteByte('\n')
	return b.String()
}

// ToLLMToolsFiltered returns tool definitions only for tools whose names
// are in the allowed set. Used to reduce token cost by sending fewer tools
// in modes that don't need the full set.
func (r *Registry) ToLLMToolsFiltered(allowed map[string]bool) []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]llm.ToolDef, 0, len(allowed))
	for _, name := range r.sortedNames() {
		if allowed[name] {
			t := r.tools[name]
			defs = append(defs, llm.ToolDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			})
		}
	}
	return defs
}
