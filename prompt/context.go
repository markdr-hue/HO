/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package prompt

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/markdr-hue/HO/tools"
)

// DetailLevel controls how much design-token detail to include in prompts.
type DetailLevel int

const (
	// DetailCompact writes just the JSON dump under a heading.
	DetailCompact DetailLevel = iota
	// DetailFull writes typography scale, component signatures, and dark mode.
	DetailFull
)

// GuideStyle controls the format of tool guidance in prompts.
type GuideStyle int

const (
	// GuideNone produces no tool guide output.
	GuideNone GuideStyle = iota
	// GuideFull uses the registry's full Guide() text.
	GuideFull
	// GuideCompact uses one-liner descriptions (tool: description).
	GuideCompact
	// GuidePlan uses the condensed plan-stage format.
	GuidePlan
)

// PlanContext provides the plan fields needed by platform contract assembly.
// Implemented by brain.Plan.
type PlanContext interface {
	GetArchitecture() string
	HasWebSocketEndpoints() bool
}

// WriteSiteHeader writes the standard owner/date/site header block.
func WriteSiteHeader(b *strings.Builder, ownerName, siteName, siteDesc string) {
	if ownerName != "" {
		b.WriteString(fmt.Sprintf("Site owner: %s\n", ownerName))
	}
	b.WriteString(fmt.Sprintf("Current date: %s\n", time.Now().UTC().Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("Site: %s", siteName))
	if siteDesc != "" {
		b.WriteString(fmt.Sprintf(" — %s", siteDesc))
	}
	b.WriteString("\n\n")
}

// WriteDesignTokens writes the design system tokens block.
// dsJSON should be the marshaled DesignSystem struct.
// With DetailFull, additional BUILD-specific guidance is included.
func WriteDesignTokens(b *strings.Builder, dsJSON []byte, detail DetailLevel) {
	if len(dsJSON) == 0 || string(dsJSON) == "null" || string(dsJSON) == "{}" {
		return
	}
	b.WriteString("## Design System Tokens\n```json\n")
	b.Write(dsJSON)
	b.WriteString("\n```\n\n")

	if detail == DetailFull {
		writeDesignTokensFull(b, dsJSON)
	}
}

// writeDesignTokensFull adds BUILD-specific design token guidance:
// color mapping, typography scale, component signatures, and dark mode.
func writeDesignTokensFull(b *strings.Builder, dsJSON []byte) {
	// Parse to check for specific fields.
	var ds struct {
		Colors       map[string]string `json:"colors"`
		Typography   json.RawMessage   `json:"typography"`
		DesignIntent string            `json:"design_intent"`
		Components   json.RawMessage   `json:"components"`
		DarkMode     bool              `json:"dark_mode"`
	}
	if json.Unmarshal(dsJSON, &ds) != nil {
		return
	}

	if len(ds.Colors) > 0 {
		b.WriteString("Map colors to: --color-primary, --color-secondary, --color-bg, --color-surface, --color-text, --color-muted, etc.\n")
		b.WriteString("Use var(--color-*) throughout all CSS. Hardcoded hex is fine for gradients, shadows, SVG fills, and one-off accents that don't map to a token.\n\n")
	}

	b.WriteString("Define text-size custom properties for your typography scale and use them for all font-size declarations.\n\n")

	if len(ds.Components) > 0 && string(ds.Components) != "null" {
		b.WriteString("## Component HTML Signatures (starting point)\n")
		b.WriteString("Use these HTML structures as your starting point when building pages. You may adapt class names, nesting, or structure during BUILD if the actual design system calls for it — but stay consistent across pages. If a signature contains inline styles, extract them into CSS classes instead.\n```json\n")
		// Pretty-print components.
		var comps interface{}
		if json.Unmarshal(ds.Components, &comps) == nil {
			compJSON, _ := json.MarshalIndent(comps, "", "  ")
			b.Write(compJSON)
		} else {
			b.Write(ds.Components)
		}
		b.WriteString("\n```\n\n")
	}

	if ds.DarkMode {
		b.WriteString(`## Dark Mode Implementation
- Define dark-mode color overrides under [data-theme="dark"] in the global CSS. Override --color-bg, --color-surface, --color-text, and other tokens.
- In a global JS file, implement a theme toggle: on load check localStorage.getItem('theme'), apply data-theme attribute to <html>, and toggle on click.
- Add an accessible theme toggle button in the layout nav.
- Default to user's OS preference via window.matchMedia('(prefers-color-scheme: dark)') if no localStorage value.

`)
	}
}

// WriteCSSReference loads global CSS, extracts class references, and writes them.
func WriteCSSReference(b *strings.Builder, db *sql.DB) {
	css := LoadGlobalCSS(db)
	if css == "" {
		return
	}
	cssRef := ExtractCSSReference(css)
	if cssRef == "" {
		return
	}
	b.WriteString("## CSS Classes Available\n")
	b.WriteString(cssRef)
	b.WriteString("\n\n")
}

// WriteJSReference loads global JS, extracts API references, and writes them.
func WriteJSReference(b *strings.Builder, db *sql.DB) {
	globalJS := LoadGlobalJS(db)
	if globalJS == "" {
		return
	}
	jsRef := ExtractJSReference(globalJS)
	if jsRef == "" {
		return
	}
	b.WriteString("## JS API Available\n")
	b.WriteString(jsRef)
	b.WriteString("\n\n")
}

// WriteToolGuide writes the tool guide section based on the requested style.
func WriteToolGuide(b *strings.Builder, registry *tools.Registry, allowed map[string]bool, style GuideStyle) {
	switch style {
	case GuideNone:
		return
	case GuideFull:
		b.WriteString(registry.BuildGuide(allowed))
	case GuideCompact:
		b.WriteString("## Tool Guide\n")
		b.WriteString(buildCompactGuide(registry, allowed))
		b.WriteByte('\n')
	case GuidePlan:
		b.WriteString(registry.BuildPlanGuide(allowed))
	}
}

// buildCompactGuide generates a short "- tool: description" list from the registry.
func buildCompactGuide(registry *tools.Registry, allowed map[string]bool) string {
	allTools := registry.List()
	var sb strings.Builder
	for _, t := range allTools {
		if !allowed[t.Name()] {
			continue
		}
		fmt.Fprintf(&sb, "- %s: %s\n", t.Name(), t.Description())
	}
	return sb.String()
}

// WriteSiteManifest writes the site map (pages + assets) block.
func WriteSiteManifest(b *strings.Builder, db *sql.DB) {
	manifest := LoadSiteManifest(db)
	if manifest != "" {
		b.WriteString(manifest)
	}
}

// WriteDataLayerSummary writes a concise summary of API, WebSocket, SSE, and upload endpoints.
func WriteDataLayerSummary(b *strings.Builder, siteDB *sql.DB) {
	// CRUD API endpoints.
	var apiLines []string
	if rows, err := siteDB.Query(`
		SELECT e.path, e.requires_auth, e.public_read, COALESCE(t.schema_def, '{}')
		FROM ho_api_endpoints e
		LEFT JOIN ho_dynamic_tables t ON e.table_name = t.table_name
		ORDER BY e.path`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var path, schemaDef string
			var requiresAuth, publicRead bool
			if rows.Scan(&path, &requiresAuth, &publicRead, &schemaDef) != nil {
				continue
			}

			fields := ""
			if schemaDef != "" && schemaDef != "{}" {
				var cols map[string]string
				if json.Unmarshal([]byte(schemaDef), &cols) == nil {
					var ff []string
					for col, typ := range cols {
						if strings.EqualFold(typ, "PASSWORD") {
							continue
						}
						ff = append(ff, col)
					}
					sort.Strings(ff)
					fields = " — fields: " + strings.Join(ff, ", ")
				}
			}

			flags := ""
			if requiresAuth && publicRead {
				flags = " [AUTH] [PUBLIC_READ]"
			} else if requiresAuth {
				flags = " [AUTH]"
			}

			apiLines = append(apiLines, fmt.Sprintf("/api/%s%s%s", path, flags, fields))
		}
	}

	// WebSocket endpoints.
	var wsLines []string
	if rows, err := siteDB.Query("SELECT path, COALESCE(room_column, '') FROM ho_ws_endpoints"); err == nil {
		defer rows.Close()
		for rows.Next() {
			var path, roomCol string
			if rows.Scan(&path, &roomCol) != nil {
				continue
			}
			room := ""
			if roomCol != "" {
				room = fmt.Sprintf(" (rooms by %s)", roomCol)
			}
			wsLines = append(wsLines, fmt.Sprintf("/api/%s/ws%s", path, room))
		}
	}

	// SSE stream endpoints.
	var sseLines []string
	if rows, err := siteDB.Query("SELECT path, COALESCE(event_types, '') FROM ho_stream_endpoints"); err == nil {
		defer rows.Close()
		for rows.Next() {
			var path, events string
			if rows.Scan(&path, &events) != nil {
				continue
			}
			sseLines = append(sseLines, fmt.Sprintf("/api/%s/stream — events: %s", path, events))
		}
	}

	// Upload endpoints.
	var uploadLines []string
	if rows, err := siteDB.Query("SELECT path, COALESCE(allowed_types, '') FROM ho_upload_endpoints"); err == nil {
		defer rows.Close()
		for rows.Next() {
			var path, types string
			if rows.Scan(&path, &types) != nil {
				continue
			}
			uploadLines = append(uploadLines, fmt.Sprintf("POST /api/%s/upload — accepts: %s", path, types))
		}
	}

	// LLM endpoints.
	var llmLines []string
	if rows, err := siteDB.Query("SELECT path, system_prompt, requires_auth FROM ho_llm_endpoints"); err == nil {
		defer rows.Close()
		for rows.Next() {
			var path, systemPrompt string
			var requiresAuth bool
			if rows.Scan(&path, &systemPrompt, &requiresAuth) != nil {
				continue
			}
			flags := ""
			if requiresAuth {
				flags = " [AUTH]"
			}
			desc := systemPrompt
			if len(desc) > 60 {
				desc = desc[:60] + "..."
			}
			llmLines = append(llmLines, fmt.Sprintf("POST /api/%s/chat|complete%s — %s", path, flags, desc))
		}
	}

	if len(apiLines) == 0 && len(wsLines) == 0 && len(sseLines) == 0 && len(uploadLines) == 0 && len(llmLines) == 0 {
		return
	}

	b.WriteString("## Data Layer\n")
	if len(apiLines) > 0 {
		b.WriteString("### API Endpoints\n")
		b.WriteString(strings.Join(apiLines, "\n") + "\n\n")
	}
	if len(wsLines) > 0 {
		b.WriteString("### WebSocket Endpoints\n")
		b.WriteString(strings.Join(wsLines, "\n") + "\n\n")
	}
	if len(sseLines) > 0 {
		b.WriteString("### SSE Stream Endpoints\n")
		b.WriteString(strings.Join(sseLines, "\n") + "\n\n")
	}
	if len(uploadLines) > 0 {
		b.WriteString("### Upload Endpoints\n")
		b.WriteString(strings.Join(uploadLines, "\n") + "\n\n")
	}
	if len(llmLines) > 0 {
		b.WriteString("### LLM Endpoints\n")
		b.WriteString(strings.Join(llmLines, "\n") + "\n\n")
	}
}

// WritePlatformContracts writes cross-cutting platform behaviors that are
// not owned by any single tool. When plan is nil (e.g. PLAN stage), all
// sections are included. When a plan is provided, sections are conditionally
// included based on plan content.
func WritePlatformContracts(b *strings.Builder, plan PlanContext) {
	b.WriteString(`### Parameterized Routes
- Paths like /thread/:id match /thread/42. Server injects window.__routeParams = {id: "42"}.

`)

	// SPA JSON API — include when no plan or when architecture is SPA.
	if plan == nil || plan.GetArchitecture() == "spa" || plan.GetArchitecture() == "" {
		b.WriteString(`### SPA JSON API
- GET /api/page?path=/foo -> {content, title, layout, page_css, page_js, params}.
- SPA router: intercept clicks -> fetch -> replace main -> load assets -> update title -> handle popstate.

`)
	}

	// WebSocket — include when no plan or when plan has websocket endpoints.
	includeWS := plan == nil || plan.HasWebSocketEndpoints()
	if includeWS {
		b.WriteString(`### WebSocket Architecture
The WS server is a pure relay — it broadcasts messages to the room, not back to the sender. There is no server-side game logic. All coordination must happen client-side.
- The server broadcasts {_type: "join", _sender: "UUID"} and {_type: "leave", _sender: "UUID"} when clients join/leave a room.
- Use a "type" field in every message to distinguish message kinds.

`)
	}
}
