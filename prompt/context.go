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
	HasParameterizedRoutes() bool
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
	// In DetailFull mode (BUILD), strip design_intent from the JSON dump
	// since it's already placed at the top of the prompt as "Design vision:".
	outputJSON := dsJSON
	if detail == DetailFull {
		var raw map[string]json.RawMessage
		if json.Unmarshal(dsJSON, &raw) == nil {
			if _, ok := raw["design_intent"]; ok {
				delete(raw, "design_intent")
				if stripped, err := json.MarshalIndent(raw, "", "  "); err == nil {
					outputJSON = stripped
				}
			}
		}
	}
	b.WriteString("## Design System Tokens\n```json\n")
	b.Write(outputJSON)
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

	b.WriteString("Define text-size custom properties for your typography scale and use them for all font-size declarations.\n")
	b.WriteString("Define spacing custom properties (--space-*) for consistent padding, margins, and gaps. Use them for section padding, card grid gaps, and button group spacing.\n\n")

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
// included based on plan content to save tokens on simple sites.
func WritePlatformContracts(b *strings.Builder, plan PlanContext) {
	// Parameterized Routes — only when plan has :param pages or no plan (PLAN stage).
	if plan == nil || plan.HasParameterizedRoutes() {
		b.WriteString(`### Parameterized Routes
- Paths like /thread/:id match /thread/42. Server injects window.__routeParams = {id: "42"}.

`)
	}

	// SPA JSON API — only when architecture is explicitly SPA or hybrid.
	if plan == nil || plan.GetArchitecture() == "spa" || plan.GetArchitecture() == "hybrid" {
		b.WriteString(`### SPA JSON API
- GET /api/page?path=/foo -> {content, title, layout, page_css, page_js, params}.
- SPA router: intercept clicks -> fetch -> replace main -> load assets -> update title -> handle popstate.

`)
	}

	// WebSocket — include when no plan or when plan has websocket endpoints.
	includeWS := plan == nil || plan.HasWebSocketEndpoints()
	if includeWS {
		b.WriteString(`### WebSocket Architecture
The WS server is a pure relay (NOT a game server). It forwards messages to the room, not back to the sender. There is no server-side game logic, matchmaking, or state management. All coordination must happen client-side.
- Connection: ws(s)://host/api/{path}/ws?room=ROOMNAME. The ?room= parameter is REQUIRED.
- System messages (have _type field):
  - {_type:"welcome", _clientId:"YOUR-UUID", _clients:N, _clientIds:[...]} — sent on connect. Use _clientId as your unique ID.
  - {_type:"join", _sender:"UUID", _clients:N} — someone joined.
  - {_type:"leave", _sender:"UUID", _clients:N} — someone left.
- User messages: have whatever fields the sender included, plus _sender:"UUID". Your own messages are NOT echoed back.
- Multiplayer pattern: On welcome, store _clientId and broadcast your player info. On join, re-broadcast so newcomers see you. For matchmaking, compare _clientIds — first alphabetically becomes host. All game state is client-side.
- NEVER wait for server events like "match_found". Clients coordinate directly through the relay.

`)
	}
}

// PatchRules is the shared instruction block for any prompt where the LLM
// should patch existing content rather than rewrite it. Used by ChatWake,
// ChatWakeLite, and the Chat package.
const PatchRules = `1. READ before writing: use manage_pages(action="get") or manage_files(action="get") to see what exists before changing it.
2. Prefer PATCH over rewrite: use action="patch" with targeted search/replace for small-to-medium changes.
   - manage_pages(action="patch", patches='[{"search":"...","replace":"..."}]') for page HTML/JS
   - manage_files(action="patch", patches='[{"search":"...","replace":"..."}]') for CSS/JS files
   - manage_layout(action="patch", patches='[{"search":"...","replace":"..."}]', field=template|head_content) for layout
3. Only use action="save" (full rewrite) when the owner requests a major redesign or restructure that would require more patches than original content. Read first, then save the complete new version.
4. Briefly confirm what you changed.

Scope: Your changes must target the specific elements mentioned. Leave all other code, structure, and features untouched — no refactors, no bonus improvements.
`
