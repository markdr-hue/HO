/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/prompt"
)

const cssPromptLimit = 4096 // max CSS chars injected into prompts

// SectionContext carries stage-specific data needed by context section writers.
// Not all fields are used by all sections — each writer picks what it needs.
type SectionContext struct {
	SiteDB     *sql.DB
	GlobalDB   *sql.DB // only needed by SCHEDULED_TASK for site lookup
	SiteID     int     // only needed by SCHEDULED_TASK
	Plan       *Plan
	Site       *models.Site
	OwnerName  string
	ToolGuide  string // pre-built tool guide text (for SectionPlatformContracts)
	HeaderRole string // role text for SectionHeader
}

// writePromptHeader writes the standard prompt opening: role description,
// owner name (if set), and current date.
func writePromptHeader(b *strings.Builder, role, ownerName string) {
	b.WriteString(role)
	if ownerName != "" {
		b.WriteString(fmt.Sprintf("\nSite owner: %s\n", ownerName))
	}
	b.WriteString(fmt.Sprintf("Current date: %s\n\n", time.Now().UTC().Format("2006-01-02")))
}

// writeContextSections injects standard context blocks into a prompt based
// on the stage's declared Sections list. Every section type is handled here —
// no more manual prompt.WriteXXX calls scattered across prompt builders.
func writeContextSections(b *strings.Builder, sc *SectionContext, sections []PromptSection) {
	for _, sec := range sections {
		switch sec {
		case SectionHeader:
			if sc.HeaderRole != "" {
				writePromptHeader(b, sc.HeaderRole, sc.OwnerName)
			}

		case SectionSiteInfo:
			if sc.Site != nil {
				b.WriteString("## Site\n")
				b.WriteString(fmt.Sprintf("- Name: %s\n", sc.Site.Name))
				if sc.Site.Description != nil && *sc.Site.Description != "" {
					b.WriteString(fmt.Sprintf("- Description: %s\n", *sc.Site.Description))
				}
				if sc.Site.Mode != "" {
					b.WriteString(fmt.Sprintf("- Mode: %s\n", sc.Site.Mode))
				}
				if sc.Plan != nil {
					b.WriteString(fmt.Sprintf("- Type: %s, Architecture: %s, Auth: %s\n", sc.Plan.AppType, sc.Plan.Architecture, sc.Plan.AuthStrategy))
					b.WriteString(fmt.Sprintf("- Plan: %d pages, %d endpoints, %d tables\n", len(sc.Plan.Pages), len(sc.Plan.Endpoints), len(sc.Plan.Tables)))
				}
				b.WriteString("\n")
			}

		case SectionSiteManifest:
			prompt.WriteSiteManifest(b, sc.SiteDB)

		case SectionDesignTokens:
			if sc.Plan != nil && sc.Plan.DesignSystem != nil && len(sc.Plan.DesignSystem.Colors) > 0 {
				dsJSON, _ := json.MarshalIndent(sc.Plan.DesignSystem, "", "  ")
				prompt.WriteDesignTokens(b, dsJSON, prompt.DetailCompact)
			}

		case SectionCSSReference:
			prompt.WriteCSSReference(b, sc.SiteDB)

		case SectionJSReference:
			prompt.WriteJSReference(b, sc.SiteDB)

		case SectionMemories:
			prompt.WriteMemories(b, sc.SiteDB)

		case SectionPlatformContracts:
			if sc.ToolGuide != "" {
				b.WriteString("## Platform Contracts\n\n")
				b.WriteString(sc.ToolGuide)
			}
			// Pass explicit nil interface when Plan is nil to avoid
			// non-nil interface wrapping a nil *Plan pointer.
			if sc.Plan != nil {
				prompt.WritePlatformContracts(b, sc.Plan)
			} else {
				prompt.WritePlatformContracts(b, nil)
			}

		case SectionDataLayer:
			prompt.WriteDataLayerSummary(b, sc.SiteDB)

		case SectionAnalytics:
			analytics := prompt.LoadAnalyticsSummary(sc.SiteDB)
			if analytics != "" {
				b.WriteString("## Analytics (Last 7 Days)\n")
				b.WriteString(analytics + "\n")
			}

		case SectionRecentErrors:
			errors := prompt.LoadRecentErrors(sc.SiteDB)
			if len(errors) > 0 {
				b.WriteString("## Recent Errors\n")
				for _, e := range errors {
					b.WriteString("- " + e + "\n")
				}
				b.WriteString("\n")
			}
		}
	}
}

// platformRules is the standard platform rules block shared by both the
// minimal and full page build guidance in buildBuildPrompt.
const platformRules = `### Platform Rules
- Page content replaces {{content}} in the layout template (no DOCTYPE/html/head/body). Do NOT add <header>, <nav>, or <footer> — the layout provides these.
- All CSS belongs in the global stylesheet. No <style> blocks in pages or layout templates.
- /assets/ file tags are auto-injected — never add them manually.
- Page-scope JS: create via manage_files(scope="page"), then IMMEDIATELY set the page's assets array.
- Reusable HTML: use {{include:component_name}} in page content to include a saved component. Create components with manage_components.
- On tool failure: read current state before retrying.
`

// patchRules re-exports the shared constant from the prompt package.
// Single source of truth — also used by chat/prompt.go.
var patchRules = prompt.PatchRules

// planJSONSchema is the complete JSON shape definition for the Plan object.
// Extracted as a constant so it can be reviewed independently and isn't buried
// in the middle of buildPlanPrompt's string builder logic.
const planJSONSchema = `### JSON Shape
{
  "app_type": "string (dashboard, marketplace, portfolio, community, tool, cms, etc.)",
  "architecture": "spa | multi-page | single-page | hybrid | (any architecture that fits)",
  "auth_strategy": "jwt | localStorage-only | none",
  "design_system": {
    "colors": {"primary": "#hex", "secondary": "#hex", "bg": "#hex", "surface": "#hex", "text": "#hex", "muted": "#hex", "accent": "#hex", "error": "#hex", "success": "#hex"},
    "extended_colors": {"highlight": "#hex", "brand-gradient-start": "#hex"},
    "typography": {"heading_font": "Font Name", "body_font": "Font Name"},
    "design_intent": "Brief description of the aesthetic vision, vibe, and personality — e.g. 'Clean and minimal with generous whitespace, sharp corners, and a monospace accent font. Feels like a developer tool, not a marketing site.'",
    "dark_mode": true
  },
  "layout": {"style": "topnav|sidebar|minimal|split|dashboard|full-bleed|any style that fits", "nav_items": ["/", "/about"], "header": "full|minimal|none", "footer": "full|minimal|none"},
  "tables": [
    {"name": "table_name", "purpose": "what it stores", "columns": [{"name": "col", "type": "TEXT|INTEGER|REAL|BOOLEAN|PASSWORD|ENCRYPTED", "required": true, "references": "other_table(id)"}], "searchable_columns": ["col"], "seed_data": [{"col": "example1"}, {"col": "example2"}]}
  ],
  "endpoints": [
    {"action": "create_api|create_auth|create_websocket|create_stream|create_upload|create_llm", "path": "resource", "table_name": "table_name", "streaming": true, "owner_column": "user_id (optional: scopes data per user)", "cache_ttl": 60, ...}
  ],
  "pages": [
    {"path": "/", "title": "Home", "purpose": "what this page does", "sections": [
      {"name": "introduction", "purpose": "Engage visitors and communicate the core value proposition"},
      {"name": "features", "purpose": "Display key features fetched from the API", "endpoints": ["GET /api/features"]}
    ], "endpoints": ["GET /api/features"], "auth": false, "notes": "technical build details"}
  ],
  "external_libraries": [
    {"name": "three.js", "url": "https://cdn.jsdelivr.net/npm/three@0.170.0/build/three.module.js", "purpose": "3D hero animation"}
  ],
  "exclusions": ["things NOT to build"],
  "webhooks": [{"name": "...", "direction": "incoming|outgoing", "event_types": [...]}],
  "components": [{"name": "component-name", "purpose": "Reusable HTML block included via {{include:component-name}} in pages"}],
  "actions": [{"name": "...", "event_type": "auth.register|auth.login|data.insert|data.update|data.delete|payment.completed|payment.failed|webhook.received|file.uploaded|page.published|page.updated|schema.created|scheduled.completed|scheduled.failed|scheduled.*", "action_type": "send_email|http_request|insert_data|update_data|trigger_webhook|ws_broadcast|run_sql|enqueue_job", "action_config": {"to": "{{email}}", "..."}, "event_filter": {"table": "users"}}],
  "scheduled_tasks": [{"name": "...", "description": "...", "prompt": "...", "cron": "0 8 * * *"}],
  "questions": [
    {"question": "...", "type": "single_choice|multiple_choice|open", "options": ["..."]},
    {"question": "Enter your Stripe API key", "type": "secret", "secret_name": "stripe_api_key"},
    {"question": "Configure payment provider", "type": "open", "fields": [
      {"name": "api_key", "label": "API Key", "type": "secret", "secret_name": "stripe_api_key"},
      {"name": "webhook_secret", "label": "Webhook Secret", "type": "secret", "secret_name": "stripe_webhook_secret"}
    ]}
  ]
}
`

// planGuidelinesCore contains the always-included PLAN stage instructions.
const planGuidelinesCore = `## When to Ask Questions
If the site description is missing details that would significantly affect the build, use the "questions" field to ask the owner. When you have questions, return a JSON with ONLY the "questions" array — omit pages, tables, endpoints, and other plan fields. The pipeline will pause, collect answers, and re-run the plan stage with the answers included so you can produce the full plan.
Ask when: the core purpose is unclear, key features could go multiple ways (e.g. "should users have accounts?", "should this be a single-page app or multi-page?"), the project requires external API keys or credentials, or a design/UX choice would be hard to change later via chat.
Do NOT ask about things you can reasonably decide yourself — color preferences, font choices, exact page count, section ordering. These are easy to tweak later via chat.
Keep questions minimal and focused. Use single_choice or multiple_choice with clear options when possible.

## Asking for API Keys & Secrets
When the site requires an external service (Stripe, SendGrid, OpenAI, etc.), use type="secret" with secret_name to ask for the API key. The system will encrypt and store it automatically. For services needing multiple credentials, use the "fields" array. The secret_name should be a snake_case identifier like "stripe_api_key".

## Guidelines
- sections: describe what each section does (name and purpose). The BUILD stage decides layout and visual structure — keep sections focused on content intent, not visual prescriptions.
- Do NOT include id or created_at in column definitions — they are auto-added and always available for sorting/filtering. Note: there is NO auto-added updated_at column. If you need one, add it explicitly.
- All internal tables use the ho_ prefix (e.g. ho_pages, ho_files). Do not create tables starting with ho_. If your app needs a table with one of these names, prefix it (e.g. user_files, app_assets, site_pages).
- auth_strategy: "jwt" for server-side login/register, "localStorage-only" for client-side preferences, "none" for no identity.
- Every table that pages read/write via fetch() needs a create_api endpoint.
- Let the content and audience drive the aesthetic, layout, and page structure.
- Row-level security: set owner_column on create_api endpoints (e.g. "owner_column": "user_id") to scope data per authenticated user. GET returns only owned rows, POST auto-sets the column, PUT/DELETE affect only owned rows. Admin role bypasses the filter.
- **public_read rule**: If an endpoint has requires_auth but its content should be publicly browsable (feeds, posts, comments, products, articles), you MUST set "public_read": true. Otherwise GET returns 401 for unauthenticated visitors.
- Match complexity to the request. Do NOT add authentication unless the user explicitly asks for accounts, login, or multi-user features. Do NOT add extra pages unless the user's description implies them. When in doubt, build less — the owner can always ask for more via chat.
- **Architecture guide**: "spa" = single HTML page with client-side routing in JS (use history.pushState). "single-page" = one page, no routing (games, tools, visualizations). "multi-page" = separate HTML pages served by the platform. "hybrid" = mix of server-rendered and SPA sections. Choose based on the app's nature, not a default.
- pages.notes: actionable build instructions — API calls, state management, key interactions.
- seed_data: a few example rows showing data shape only — keep minimal to save tokens. The BUILD stage decides whether to expand with realistic data or skip seeding.
- components: reusable HTML snippets shared across pages. Include in page content with {{include:component_name}}. Only plan components when multiple pages share the same HTML structure.
- columns.references: foreign key constraint linking to another table — format "other_table(id)". Enforced by SQLite.

## Layout & Chrome
- header and footer are REQUIRED fields — you must set them explicitly for every plan.
- Decision rule (follow strictly):
  - "none": DEFAULT for single-page apps, fullscreen experiences, interactive tools, games, chat UIs, canvas apps, visualizations, quizzes, simulations, drawing/art tools, music players, and any app where a navbar would waste space. When architecture is "single-page", header MUST be "none" unless the user explicitly requests navigation. When in doubt between "none" and anything else, choose "none".
  - "minimal": Apps with a single-purpose UI that benefits from a small brand/title bar but not full navigation — focused utilities, admin panels, dashboards, onboarding flows.
  - "full": ONLY for sites where users navigate between 3+ distinct content pages — portfolios, e-commerce, blogs, community sites, documentation.
- When header is "none", omit nav_items entirely.
- When header/footer are included, they must feel like a natural part of the design — use the same design tokens, typography, and spacing as the rest of the app. Not a generic disconnected bar.

## Color Palette Rules
- Colors must form a cohesive, accessible palette. text must have 4.5:1 contrast vs bg. muted must have 3:1 contrast vs bg.
- error: red-family. success: green-family.

## Required Fields
- app_type, design_system (with at least colors.primary and colors.bg), pages (at least one at "/"), layout (with at least style, header, footer)

## Optional Fields
- architecture, auth_strategy, tables, endpoints, exclusions, webhooks, scheduled_tasks, questions
`

// Feature-specific plan guidelines — only included when the site description
// suggests the feature is relevant. Saves ~500-800 tokens for simple sites.

const planGuidelinesLLM = `
- create_llm endpoints: for AI-powered features (chatbots, assistants, content generators). Provide system_prompt to define the AI's role. Creates POST /api/{path}/chat (SSE streaming) and POST /api/{path}/complete (JSON response {content, model, usage, stop_reason}). Set "streaming": true (default) for chatbots/assistants that benefit from real-time token display, or "streaming": false for one-shot generators/classifiers where the full response is needed at once. SSE format: event "token" with data {"text":"chunk"}, event "done" with data {}. Frontend must use parsed.text (NOT parsed.content). When streaming is false, page JS should POST to /api/{path}/complete and read the JSON response. **max_tokens guidance**: default is 4096. For LLM endpoints that generate code (HTML/CSS/JS, games, components), set max_tokens to 8192 or higher — code generation routinely needs 4000-8000+ tokens. For simple text/chat, 2048-4096 is fine. Frontend should check response.stop_reason — if "max_tokens", the response was truncated. Optional: max_tokens, temperature, max_history, rate_limit, requires_auth.
- LLM endpoints (create_llm) only serve /chat and /complete sub-routes — they do NOT provide CRUD (GET list, GET by id, POST, PUT, DELETE). If a page needs standard REST operations on a resource, you MUST include a separate create_api endpoint for that table in addition to any create_llm endpoint.
`

const planGuidelinesUpload = `
- create_upload endpoints: set path="resource" to create POST /api/{resource}/upload. In page endpoint references, use "POST /api/{resource}/upload" (upload comes AFTER the resource name). Default allowed types include image/*, text/*, PDF, JSON, ZIP. Specify allowed_types to restrict or expand (e.g. ["text/csv", "application/json"]).
`

const planGuidelinesWebSocket = `
- create_websocket endpoints: IMPORTANT: this is a stateless message relay, NOT a game server. The server only forwards messages between clients in the same room. It does NOT run matchmaking, manage game state, or send game events. For multiplayer games/apps, ALL game logic (matchmaking, state sync, win conditions) must be in client-side JS. Clients coordinate peer-to-peer through the relay. In pages.notes, specify: "WebSocket relay pattern: clients coordinate directly, lowest-ID player acts as host."
`

const planGuidelinesActions = `
- actions: server-side hooks that fire on events without the LLM (e.g. send welcome email on registration, log to audit table on data changes). Only include if the user explicitly requests event-driven behavior or it's clearly implied (e.g. "email confirmation on signup"). Do NOT add actions by default.
  - update_data actions require: table (string), set (object), where (object). For counters use {"$increment":1} or {"$decrement":1} as the value. Event payload includes all request body fields — use {{field_name}} templates.
  - ws_broadcast actions push real-time messages to WebSocket clients: {"endpoint_path":"/ws/chat", "room":"general", "message":{"type":"new_item","data":{"id":"{{id}}"}}}. Use for live dashboards, notifications, and real-time feeds.
  - run_sql actions execute SQL on events (INSERT/UPDATE/SELECT allowed, DROP/ALTER/DELETE blocked): {"sql":"UPDATE stats SET count = (SELECT COUNT(*) FROM orders)"}. Use for computed aggregations.
  - enqueue_job actions create background jobs: {"type":"send_email", "payload":{"to":"{{email}}"}, "max_attempts":3}. Use for deferred/retryable work.
`

const planGuidelinesCaching = `
- cache_ttl on create_api endpoints: response cache in seconds for GET requests (e.g. 60 = cache for 1 minute). Cache auto-invalidates on POST/PUT/DELETE. Use for read-heavy endpoints like product listings or dashboards.
`

// buildPlanGuidelines assembles the PLAN guidelines, including core rules always
// and feature-specific rules only when the site description suggests relevance.
func buildPlanGuidelines(siteDescription string) string {
	var b strings.Builder
	b.WriteString(planGuidelinesCore)

	lower := strings.ToLower(siteDescription)

	// LLM/AI features
	for _, kw := range []string{"ai", "chatbot", "assistant", "llm", "gpt", "generate", "openai", "claude", "gemini"} {
		if strings.Contains(lower, kw) {
			b.WriteString(planGuidelinesLLM)
			break
		}
	}

	// Upload features — "file" alone is too broad ("file a complaint"), require upload-related context.
	for _, kw := range []string{"upload", "file upload", "upload file", "image upload", "photo", "attachment", "csv", "import"} {
		if strings.Contains(lower, kw) {
			b.WriteString(planGuidelinesUpload)
			break
		}
	}

	// WebSocket/real-time features — "live" alone is too broad ("live music"), require real-time context.
	for _, kw := range []string{"websocket", "real-time", "realtime", "multiplayer", "live update", "live feed", "live dashboard", "live sync", "chat", "collaborative", "sync"} {
		if strings.Contains(lower, kw) {
			b.WriteString(planGuidelinesWebSocket)
			break
		}
	}

	// Event-driven/automation features
	for _, kw := range []string{"event", "trigger", "automat", "notification", "webhook", "email on", "when a user"} {
		if strings.Contains(lower, kw) {
			b.WriteString(planGuidelinesActions)
			break
		}
	}

	// Caching/performance
	for _, kw := range []string{"cache", "performance", "fast", "high-traffic", "scalab"} {
		if strings.Contains(lower, kw) {
			b.WriteString(planGuidelinesCaching)
			break
		}
	}

	return b.String()
}

// --- EXPAND stage prompt ---

func buildExpandPrompt(siteName, rawDescription, ownerName string) string {
	cfg := StageConfigs[StageExpand]
	var b strings.Builder

	writeContextSections(&b, &SectionContext{
		HeaderRole: cfg.HeaderRole,
		OwnerName:  ownerName,
	}, cfg.Sections)

	b.WriteString(`## Task

The user wants to build a project. They gave a brief description — your job is to expand it into a clear, actionable specification that a technical planner can use.

**Rules:**
- Infer reasonable defaults for anything the user left unsaid (auth, visual style, key features, user flows)
- Do NOT invent features the user clearly didn't imply — stay faithful to their intent
- Think about what makes this specific project work well (a tetris game needs piece rotation and collision; a blog needs posts and comments)
- Keep it concise: 1-3 short paragraphs, not a full PRD
- Write as if you ARE the user, clarifying what they meant — not as a consultant adding scope
- Include concrete details: what pages/screens exist, what users can do, what the visual feel should be
- Output ONLY the expanded description text — no headings, no JSON, no markdown formatting

## Project
`)
	b.WriteString(fmt.Sprintf("- Name: %s\n", siteName))
	b.WriteString(fmt.Sprintf("- Description: %s\n", rawDescription))

	return b.String()
}

// --- PLAN stage prompt ---

func buildPlanPrompt(site *models.Site, ownerName, answers, capabilitiesRef string) string {
	cfg := StageConfigs[StagePlan]
	var b strings.Builder

	writeContextSections(&b, &SectionContext{
		Site:       site,
		OwnerName:  ownerName,
		ToolGuide:  capabilitiesRef,
		HeaderRole: cfg.HeaderRole,
	}, cfg.Sections)

	if answers != "" {
		b.WriteString("## Owner's Answers to Your Questions\n")
		b.WriteString(fmt.Sprintf("\"%s\"\n\n", answers))
	}

	b.WriteString("## Instructions\n\nAnalyze the requirements and produce a complete Plan JSON — the build specification that drives everything. Think creatively about the best way to build this using the platform's capabilities.\n\nYou have built-in web search for research — use it to look up API documentation, verify library versions, check service availability, or research implementation approaches. You also have make_http_request for direct URL fetching. Do NOT fetch CDN library files (JS/CSS) — trust that well-known CDN URLs (jsdelivr, unpkg, esm.sh, cdnjs) are valid. CDN library versions are validated automatically after planning. When you're ready, output the Plan JSON as your final response.\n\n")
	b.WriteString(planJSONSchema)
	siteDesc := ""
	if site != nil && site.Description != nil {
		siteDesc = *site.Description
	}
	b.WriteString(buildPlanGuidelines(siteDesc))

	return b.String()
}

// --- BUILD prompt ---

// buildOrderChecklist computes a deterministic build order from the Plan.
// Tables → Endpoints → Seed Data → Layout → CSS → Shared JS → Pages. Pure Go, zero LLM tokens.
// When progress is non-nil, completed items are marked [DONE] so the LLM skips them.
func buildOrderChecklist(plan *Plan, progress *buildProgressTracker) string {
	var b strings.Builder
	b.WriteString("## Build Order\nComplete each phase before moving to the next.\n\n")

	// Phase 1: Infrastructure
	hasInfra := len(plan.Tables) > 0 || len(plan.Endpoints) > 0
	if hasInfra {
		b.WriteString("**Phase 1 — Infrastructure** (follow this order: ALL tables first, then ALL endpoints, then seed data)\n")
		for _, t := range plan.Tables {
			if progress != nil && progress.tablesDone[t.Name] {
				b.WriteString(fmt.Sprintf("- [DONE] Table: %s\n", t.Name))
			} else {
				b.WriteString(fmt.Sprintf("- Table: %s\n", t.Name))
			}
		}
		for _, ep := range plan.Endpoints {
			done := progress != nil && progress.endpointsDone[ep.Action+":"+ep.Path]
			entry := "- "
			if done {
				entry += "[DONE] "
			}
			entry += fmt.Sprintf("Endpoint: %s %s", ep.Action, ep.Path)
			if ep.TableName != "" {
				entry += fmt.Sprintf(" (table: %s)", ep.TableName)
			}
			if ep.Action == "create_llm" {
				if ep.Streaming != nil && !*ep.Streaming {
					entry += " (use /complete — non-streaming)"
				} else {
					entry += " (use /chat — streaming)"
				}
			}
			if done {
				entry += " (skip)"
			}
			b.WriteString(entry + "\n")
		}
		if len(plan.Tables) > 0 {
			b.WriteString("- Seed data: populate parent tables before child tables (respect foreign keys). Avoid duplicate unique values.\n")
		}
		b.WriteString("\n")
	}

	// Phase 2: Foundation
	b.WriteString("**Phase 2 — Foundation** (layout → CSS → shared JS)\n")
	header, footer := layoutHeaderFooter(plan)
	layoutDesc := "Layout"
	if header == "none" && footer == "none" {
		layoutDesc = "Layout (head_content only — no header or footer)"
	} else if header == "none" {
		layoutDesc = "Layout (no header, footer only)"
	} else if footer == "none" {
		layoutDesc = "Layout (header only, no footer)"
	}
	if len(plan.ExternalLibraries) > 0 {
		layoutDesc += " + CDN links in head_content"
	}
	b.WriteString(fmt.Sprintf("- %s\n", layoutDesc))
	switch {
	case header == "none" && footer == "none":
		b.WriteString("- Global CSS (full-viewport — no .site-header/.site-footer needed)\n")
	case header == "none":
		b.WriteString("- Global CSS (include .site-footer styles)\n")
	case footer == "none":
		b.WriteString("- Global CSS (include .site-header styles)\n")
	default:
		b.WriteString("- Global CSS (include .site-header and .site-footer styles)\n")
	}
	if len(plan.Endpoints) > 0 || plan.AuthStrategy != "" {
		b.WriteString("- Shared JS utilities (auth, fetch helpers) with scope=\"global\"\n")
	}
	for _, comp := range plan.Components {
		b.WriteString(fmt.Sprintf("- Component: %s (%s) — include in pages with {{include:%s}}\n", comp.Name, comp.Purpose, comp.Name))
	}
	b.WriteString("\n")

	// Phase 3: Pages
	b.WriteString("**Phase 3 — Pages** (for each: patch CSS if needed → create HTML → create page JS → set assets)\n")
	for _, pg := range plan.Pages {
		if len(pg.Endpoints) > 0 || len(plan.Endpoints) > 0 {
			b.WriteString(fmt.Sprintf("- %s (%s) — create page-scope .js and set assets array\n", pg.Path, pg.Title))
		} else {
			b.WriteString(fmt.Sprintf("- %s (%s)\n", pg.Path, pg.Title))
		}
	}
	b.WriteString("\n")

	// Phase 4: Extras (only if there are actions)
	if len(plan.Actions) > 0 {
		b.WriteString("**Phase 4 — Extras**\n")
		for _, a := range plan.Actions {
			b.WriteString(fmt.Sprintf("- Action: %s (on %s → %s)\n", a.Name, a.EventType, a.ActionType))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// buildCompactPlanRef produces a compact summary of the plan for stages that
// need to know what should exist (e.g. VALIDATE) without the full JSON.
func buildCompactPlanRef(plan *Plan) string {
	var b strings.Builder
	if len(plan.Pages) > 0 {
		paths := make([]string, len(plan.Pages))
		for i, p := range plan.Pages {
			paths[i] = p.Path
		}
		b.WriteString(fmt.Sprintf("Pages: %s\n", strings.Join(paths, ", ")))
	}
	if len(plan.Tables) > 0 {
		parts := make([]string, len(plan.Tables))
		for i, t := range plan.Tables {
			parts[i] = fmt.Sprintf("%s (%d cols)", t.Name, len(t.Columns))
		}
		b.WriteString(fmt.Sprintf("Tables: %s\n", strings.Join(parts, ", ")))
	}
	if len(plan.Endpoints) > 0 {
		parts := make([]string, len(plan.Endpoints))
		for i, ep := range plan.Endpoints {
			parts[i] = fmt.Sprintf("%s %s", ep.Action, ep.Path)
		}
		b.WriteString(fmt.Sprintf("Endpoints: %s\n", strings.Join(parts, ", ")))
	}
	if plan.AuthStrategy != "" {
		b.WriteString(fmt.Sprintf("Auth: %s\n", plan.AuthStrategy))
	}
	return b.String()
}

// layoutHeaderFooter returns the resolved header and footer values from the plan,
// defaulting to "full" when omitted.
// chromelessKeywords are app_type substrings that should default to no chrome.
var chromelessKeywords = []string{
	"game", "canvas", "art", "immersive", "visuali", "experiment",
	"animation", "3d", "webgl", "demo", "screensaver", "generative",
	"chat", "messenger", "fullscreen",
	"quiz", "survey", "poll", "music", "audio", "meditation", "breathing",
	"typing", "whiteboard", "drawing", "paint", "sketch", "puzzle",
	"simulation", "simulator", "map", "globe", "kiosk",
	"card-game", "board-game", "snake", "tetris", "pong",
}

// minimalKeywords are app_type substrings that should default to minimal chrome.
var minimalKeywords = []string{
	"calculator", "timer", "clock", "converter", "reader", "viewer",
	"player", "editor", "terminal", "ide", "notebook",
	"tool", "utility",
	"dashboard", "admin", "panel", "form", "wizard", "onboarding",
}

// inferLayoutDefaults returns sensible header/footer defaults based on app_type.
// Games and immersive experiences get no chrome; focused tools get minimal chrome;
// everything else gets full chrome. Explicit Plan values always override these.
func inferLayoutDefaults(appType string) (header, footer string) {
	lower := strings.ToLower(appType)
	for _, kw := range chromelessKeywords {
		if strings.Contains(lower, kw) {
			return "none", "none"
		}
	}
	for _, kw := range minimalKeywords {
		if strings.Contains(lower, kw) {
			return "minimal", "none"
		}
	}
	return "full", "full"
}

func layoutHeaderFooter(plan *Plan) (header, footer string) {
	header, footer = inferLayoutDefaults(plan.AppType)
	if plan.Layout != nil {
		if plan.Layout.Header != "" {
			header = plan.Layout.Header
		}
		if plan.Layout.Footer != "" {
			footer = plan.Layout.Footer
		}
	}
	return
}

// buildLayoutInstructions generates conditional layout build instructions
// based on the plan's header/footer settings.
func buildLayoutInstructions(plan *Plan) string {
	var b strings.Builder
	header, footer := layoutHeaderFooter(plan)

	b.WriteString("### Layout\n")
	b.WriteString("Save a layout template via manage_layout with a `template` field. The template is the complete HTML shell between <body> and </body>, with a `{{content}}` marker where page content is injected. The server replaces `{{content}}` with each page's HTML.\n\n")
	b.WriteString("IMPORTANT: `{{content}}` is the ONLY placeholder allowed in the template. Do NOT put `{{head_content}}` in the template — head_content is a separate field that the server injects into <head> automatically. Putting it in the template will render it as literal visible text.\n\n")

	b.WriteString("Your global CSS MUST style body and main — the platform does not inject any base body styles. At minimum, set margin:0 on body and use flex/grid to control the page skeleton.\n\n")

	if header == "none" && footer == "none" {
		b.WriteString("This app is chromeless — no header, no footer, no navbar. The template should be:\n")
		b.WriteString("```\n<main>{{content}}</main>\n```\n")
		b.WriteString("Pages fill the viewport — design them as complete, self-contained experiences.\n")
	} else {
		b.WriteString("The template should contain: ")
		if header != "none" {
			b.WriteString("a semantic `<header class=\"site-header\">` with navigation, ")
		}
		b.WriteString("a `<main>` element with `{{content}}`")
		if footer != "none" {
			b.WriteString(", and a `<footer class=\"site-footer\">`")
		}
		b.WriteString(". Use design tokens for all colors and fonts — the header/footer must feel like a natural part of the design, not a generic bar.\n")
		// Inject concrete design values so the LLM has actual colors/fonts when building the layout
		// (layout is created before CSS, so without this the header ends up looking generic).
		if plan.DesignSystem != nil {
			b.WriteString("\nDesign context for the header/footer:\n")
			if bg := plan.DesignSystem.Colors["bg"]; bg != "" {
				b.WriteString(fmt.Sprintf("- Colors: primary=%s, bg=%s, text=%s, surface=%s\n",
					plan.DesignSystem.Colors["primary"],
					bg,
					plan.DesignSystem.Colors["text"],
					plan.DesignSystem.Colors["surface"]))
			}
			if plan.DesignSystem.Typography != nil && plan.DesignSystem.Typography.HeadingFont != "" {
				b.WriteString(fmt.Sprintf("- Fonts: %s (headings), %s (body)\n",
					plan.DesignSystem.Typography.HeadingFont,
					plan.DesignSystem.Typography.BodyFont))
			}
			if plan.DesignSystem.DesignIntent != "" {
				b.WriteString(fmt.Sprintf("- Design intent: %s\n", plan.DesignSystem.DesignIntent))
			}
			b.WriteString("Use inline styles or class names that align with these values. The CSS file (created next) will define the custom properties — the header should already look intentional, not generic.\n")
		}
	}

	b.WriteString("- Custom fonts: add Google Fonts <link> tags to head_content with &display=swap.\n")
	b.WriteString("- Favicon: use a data URI for emoji favicons — <link rel=\"icon\" href=\"data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><text y='.9em' font-size='90'>EMOJI</text></svg>\">. For custom SVG favicons, save via manage_files and use href=\"/assets/favicon.svg\". NEVER use href=\"/favicon.svg\" — there is no route for it.\n")
	b.WriteString("- External library CDN links go in head_content BEFORE pages that use them.\n")

	return b.String()
}

// BuildPhase controls which Build Guide sections are included in the prompt.
// Phased injection reduces token waste by showing only the guidance relevant
// to the current build stage.
type BuildPhase int

const (
	// BuildPhaseAll includes the full Build Guide (initial call, no progress).
	BuildPhaseAll BuildPhase = iota
	// BuildPhaseInfra shows only seed data, schema, and endpoint guidance.
	BuildPhaseInfra
	// BuildPhaseFoundation shows only CSS, layout, and library guidance.
	BuildPhaseFoundation
	// BuildPhasePages shows only page, JS, SPA, and platform rules guidance.
	BuildPhasePages
)

// buildBuildPrompt creates the prompt for the single unified BUILD session.
// The phase parameter controls which Build Guide sections are included —
// phased injection saves ~800-1,200 tokens per call after the initial phase.
func buildBuildPrompt(plan *Plan, site *models.Site, ownerName, existingManifest, toolGuide string, progress *buildProgressTracker, phase ...BuildPhase) string {
	currentPhase := BuildPhaseAll
	if len(phase) > 0 {
		currentPhase = phase[0]
	}
	var b strings.Builder
	header, footer := layoutHeaderFooter(plan)

	// Scale-aware opening: don't mention tables/endpoints/design system for tiny sites.
	isMinimal := len(plan.Tables) == 0 && len(plan.Endpoints) == 0 && len(plan.Pages) <= 2
	if isMinimal {
		b.WriteString("You are HO. Build this site in one session. It's a small project — keep CSS minimal (only styles actually used), skip component utility classes you won't need. Build exactly what the plan specifies, nothing more.\n")
	} else {
		b.WriteString("You are HO. Build this complete site in one session: database tables, API endpoints, CSS design system, page layout, and all pages. Build what the plan specifies — no extra pages, tables, or endpoints beyond the plan. You MAY add supporting UI elements (modals, toasts, loading states, empty states, utility classes) that serve planned features — but do not add new pages, data tables, or user-facing features beyond the plan.\n")
	}
	// Design intent at the highest-attention position — right after the role definition.
	if plan.DesignSystem != nil && plan.DesignSystem.DesignIntent != "" {
		b.WriteString(fmt.Sprintf("\nDesign vision: %s\n", plan.DesignSystem.DesignIntent))
	}
	b.WriteString("\n")

	siteDesc := ""
	if site.Description != nil {
		siteDesc = *site.Description
	}
	prompt.WriteSiteHeader(&b, ownerName, site.Name, siteDesc)

	// Plan JSON is sent in the first user message, not the system prompt,
	// so it gets pruned from history after early iterations.
	// Include a compact plan reference here for ongoing awareness.
	b.WriteString("## Plan Summary\n")
	b.WriteString(buildCompactPlanRef(plan))
	b.WriteString("\n")

	if isMinimal {
		b.WriteString("## Build Order\nBuild the CSS, layout, and pages in whatever order makes sense for this small project.\n\n")
	} else {
		b.WriteString(buildOrderChecklist(plan, progress))
	}

	// Brief app-type anchor — design_intent is already at the top of the prompt.
	if !isMinimal && plan.AppType != "" {
		b.WriteString(fmt.Sprintf("## Design Thesis\nThis is a %s. Let this personality inform every page — adapt layouts to the content, don't default to generic patterns.\n\n", plan.AppType))
	}

	if existingManifest != "" {
		b.WriteString("## Already Built (crash recovery — do NOT recreate these)\n")
		b.WriteString(existingManifest)
		b.WriteString("\n\n")
	}

	writeContextSections(&b, &SectionContext{
		Plan:      plan,
		ToolGuide: toolGuide,
	}, []PromptSection{SectionPlatformContracts})

	// Design system tokens placed here (attention recovery zone) so the LLM
	// sees them fresh right before building CSS and pages.
	if plan.DesignSystem != nil && len(plan.DesignSystem.Colors) > 0 {
		b.WriteString("## Design System Tokens\n")
		b.WriteString("Implement these as CSS custom properties in the global CSS file. Use them as the primary color palette — they're your source of truth for consistent theming.\n")
		dsJSON, _ := json.MarshalIndent(plan.DesignSystem, "", "  ")
		prompt.WriteDesignTokens(&b, dsJSON, prompt.DetailFull)
	}

	// Quality Bar placed in the recency zone — right before build instructions
	// so it's fresh when the LLM starts building.
	chromeless := header == "none" && footer == "none"
	b.WriteString("## Quality Bar\n")
	if chromeless {
		b.WriteString("- Clear visual design matching this app's personality. Interactive elements have hover/focus/active states.\n")
		b.WriteString("- Chromeless/fullscreen app — responsive breakpoints optional. Prioritize the intended viewport.\n")
	} else {
		b.WriteString("- Clear visual hierarchy with cohesive design matching this app's personality. Interactive elements have hover/focus/active states.\n")
		b.WriteString("- Responsive: layouts must work across mobile and desktop viewports.\n")
	}
	b.WriteString("- Generous spacing: sections need vertical breathing room (padding/margin), card grids need gap, button groups need gap. Cramped layouts feel broken.\n")
	b.WriteString("- Buttons must have adequate padding, clear visual weight, and distinct hover states. A flat unstyled button looks like a bug.\n")
	b.WriteString("- Every CSS class used in HTML must be styled — especially layout/container classes. An unstyled wrapper div breaks the whole section.\n")
	b.WriteString("- No placeholder text or TODO comments. Minimal console.logs (error handlers only).\n")
	b.WriteString("- Add polish: subtle hover transitions, thoughtful empty states, loading skeletons, and micro-interactions that make the site feel alive. Creative expression goes into *how* you build it — design, interactions, copy, animations — not *what* you build.\n\n")

	// Build Guide — phased injection to reduce token waste.
	// Phase All: full guide (first call). Infra: seed/schema only.
	// Foundation: CSS/layout/libraries. Pages: page/JS/SPA/platform rules.
	showInfraGuide := currentPhase == BuildPhaseAll || currentPhase == BuildPhaseInfra
	showFoundationGuide := currentPhase == BuildPhaseAll || currentPhase == BuildPhaseFoundation
	showPagesGuide := currentPhase == BuildPhaseAll || currentPhase == BuildPhasePages

	if showInfraGuide || showFoundationGuide || showPagesGuide {
		b.WriteString("## Build Guide\n\n")

		// --- Infrastructure guidance (seed data) ---
		if showInfraGuide && len(plan.Tables) > 0 {
			b.WriteString(`### Seed Data
Seed data is optional — only populate tables when demo content makes the site feel real (blogs, products, listings, testimonials). Skip seeding for tables that are purely user-generated (messages, orders, form submissions) or when the app works better empty on first visit.
- **NEVER seed auth/user tables** (tables used by create_auth endpoints). Users register themselves — seeded usernames/emails collide with real registrations and cause UNIQUE constraint errors.
When seeding: use manage_data(action="insert", rows=[{...}, {...}, ...]) to bulk-insert all rows for a table in ONE call (max 100 rows). Do NOT insert rows one at a time. Use your judgement on how many rows each table needs based on its purpose. The plan's seed_data is a SHAPE HINT only — generate richer, more varied data.
- **Insertion order matters**: Seed parent tables BEFORE child tables. If comments.user_id references users(id), insert users FIRST. Foreign key constraints are enforced — inserting a row that references a non-existent parent will fail.
- **Unique columns**: If a column has a UNIQUE constraint (e.g. username, email), every seed row MUST have a distinct value. Do NOT insert duplicate values. Do NOT re-seed a table you already inserted into.
- For placeholder images use services like picsum.photos/{width}/{height}?random={n} or generate SVG placeholders. For avatars: i.pravatar.cc/150?u={unique} or similar.

`)
		}

		// --- Foundation guidance (CSS, layout, libraries) ---
		if showFoundationGuide {
			if isMinimal {
				b.WriteString(`### CSS
Create one global CSS file. Only define styles you actually use.
Use design tokens (custom properties) for colors and fonts so the page is easy to theme.
`)
			} else {
				b.WriteString(`### CSS
Create a global CSS file with design tokens as custom properties and base component styles. As you build each page, use manage_files(action="patch") to append that page's classes BEFORE creating the page HTML.
Design the CSS to match the personality of this specific app — a recipe site should feel different from a SaaS dashboard.
Include base layout patterns in the initial CSS: card grids (grid/flex + gap), section spacing (vertical padding between page sections), button groups (flex + gap), and container widths. These are used on almost every page — define them upfront so pages don't ship with unstyled containers.
`)
			}

			if chromeless {
				b.WriteString("This is a chromeless app — pages fill the viewport. Structure the CSS for full-viewport layouts (canvas, flex/grid containers, absolute positioning).\n")
				b.WriteString("Use `100dvh` (dynamic viewport height) instead of `100vh` for the outer container — on mobile, `100vh` includes the browser chrome and causes the page to scroll or the input to hide behind the keyboard.\n")
			} else {
				b.WriteString("Use consistent content alignment that suits the design (max-width containers, full-bleed sections, or other patterns).\n")
				if header != "none" || footer != "none" {
					b.WriteString("Style .site-header/.site-footer using design token custom properties. Define --header-height for layout offset calculations.\n")
				}
			}
			b.WriteString(buildLayoutInstructions(plan))

			b.WriteString(`### Libraries & Frameworks
Choose external libraries when they genuinely improve the result. Add CDN links to head_content via manage_layout.
- Use CDN providers like jsdelivr.net, esm.sh, or unpkg.com for delivery.
- If using a CSS framework (Tailwind Play CDN, etc.), configure its theme to use your design token values so colors stay consistent.
- For SVG graphics (icons, illustrations, decorative elements) — generate SVGs directly when practical. Save as .svg files via manage_files or embed inline in page HTML.
You may add libraries during BUILD even if not listed in the plan — use your judgement on what the project needs.

`)
		}

		// --- Pages guidance (page building, JS, SPA, platform rules) ---
		if showPagesGuide {
			if isMinimal {
				b.WriteString(`
### Pages
- For complex interactivity, create a page-scope .js file with manage_files(scope="page", page="/path") to auto-link it to the page. Small inline <script> tags are fine for simple interactions (toggles, counters, etc.).

`)
			} else {
				b.WriteString(`
### Pages
For each page: (a) if needed, patch the global CSS file to add ALL page-specific classes BEFORE creating the page, (b) create HTML with manage_pages (pure HTML structure), (c) if the page needs complex JS logic, create a page-scope .js file via manage_files(scope="page", page="/path") — the page parameter auto-links the file to that page's assets. Small inline <script> tags are fine for simple interactions — use your judgement on when a separate file is warranted.
- **CSS completeness**: Every CSS class referenced in a page's HTML MUST exist in the global CSS. Do NOT write HTML with classes you haven't defined yet. If a page uses .profile-container, .stat-card, .modal, etc., add those styles to CSS FIRST. Missing styles cause validation failures and wasted fix-up passes.
- Before building each page, re-read that page's plan entry (sections, endpoints, notes). Sections describe *what content goes there* — adapt the visual layout to fit the design system. Do not carry layout assumptions from the previous page.
- Page JS must use the EXACT function/method names from global JS files. The system injects the API reference after each global JS save — follow it.
- For consistency: if unsure how a later page should look, use manage_pages(action="get") to review an earlier page's structure.

### JavaScript
- Organize JS files however suits the project — one file, several modules, or a mini framework. Use global scope for code shared across pages (auto-injected), page scope for page-specific logic (use page parameter to auto-link).
- **SVG icons**: Do NOT use data:image/svg+xml URIs — they break due to encoding issues. Instead, save .svg files via manage_files and reference them as <img src="/assets/icon-name.svg">. For inline icons in HTML, use <svg> elements directly.
- Route params: window.__routeParams.id for dynamic pages.
- **Auth**: This platform has its OWN auth system. Do NOT use Supabase, Firebase, Auth0, or any third-party auth SDK. There is no Auth.getUser(), no createClient(), no supabase object. Instead: store JWT in localStorage key 'auth_token'. Login/register via fetch('/api/{path}/login' or '/register'). Get current user via fetch('/api/{path}/me') with header Authorization: 'Bearer ' + localStorage.getItem('auth_token'). Use EXACT column names from create_auth config for register/login body fields.
- **JWT tokens are opaque strings** — NEVER JSON.parse them. Always guard for missing tokens: if (!localStorage.getItem('auth_token')) return null; before any fetch. The /me endpoint returns the user object as JSON — parse that response, not the token itself.
- **API patterns**: See manage_endpoints guide for response shapes, fetch patterns, and auth headers. Key: use query params for filtering (?col=val), not filters=[{...}]. Only sort by existing columns — tables auto-add "id" and "created_at" (NOT "updated_at").
- **LLM endpoints**: POST /api/{path}/chat for SSE streaming (parse event "token" → data.text), POST /api/{path}/complete for JSON (use response.content). Check stop_reason — "max_tokens" means truncated.
- **WebSocket**: See WebSocket Architecture in Platform Contracts for connection pattern and multiplayer coordination.

`)
			}
			b.WriteString(platformRules)

			// SPA-specific guidance when architecture is "spa".
			if plan.Architecture == "spa" {
				b.WriteString(`### SPA Architecture
- Create a single page at "/" with a root container element (e.g. <div id="app">).
- Implement client-side routing in a global JS file using history.pushState and popstate events.
- Use the SPA JSON API: GET /api/page?path=/foo returns {content, title, layout, page_css, page_js, params}.
- Each "page" in the plan is a route/view rendered client-side — create them all as separate pages via manage_pages so the SPA API can serve them, but navigation is handled in JS without full reloads.
- Handle direct URL access (deep links) — the server will serve the page content if accessed directly.

`)
			}

			// Game/canvas-specific guidance when app type suggests it.
			if chromeless && !isMinimal {
				b.WriteString(`### Fullscreen/Chromeless App
- This is a chromeless app — pages fill the entire viewport with no header or footer.
- Structure your page around a single root container (canvas, div#app, div.chat-container, etc.).
- For chat/messaging UIs: use a flex column layout with a scrollable message area (flex:1; overflow-y:auto) and a fixed input area at the bottom. The outer container must be height:100dvh so the input stays visible when the mobile keyboard opens.
- For games/canvas apps: use requestAnimationFrame for render loops, not setInterval. Handle window resize if the viewport matters.
- All interactivity lives in JavaScript — the page HTML is the scaffold, JS is the engine.
- Game state, animations, chat logic, and input handling all go in page-scope JS files.

`)
			}
		}

		if !isMinimal {
			b.WriteString(`### Owner Communication
If you discover a significant issue with the plan during build (e.g., a missing table needed by multiple pages, conflicting requirements, or a feature that can't work as specified), use manage_communication(action="ask") to flag it to the owner. Continue building what you can — don't block on the answer.
`)
		}
	} // end if !isCompact

	return b.String()
}

// --- UPDATE_PLAN prompt ---

func buildUpdatePlanPrompt(existingPlan *Plan, siteDB *sql.DB, site *models.Site, changeDescription, ownerName, capabilitiesRef string) string {
	cfg := StageConfigs[StageUpdatePlan]
	var b strings.Builder

	writeContextSections(&b, &SectionContext{
		SiteDB:     siteDB,
		Plan:       existingPlan,
		Site:       site,
		OwnerName:  ownerName,
		ToolGuide:  capabilitiesRef,
		HeaderRole: cfg.HeaderRole,
	}, cfg.Sections)

	if changeDescription != "" {
		b.WriteString("## Requested Changes\n")
		b.WriteString(changeDescription + "\n\n")
	}

	b.WriteString("## Current Plan\n```json\n")
	if existingPlan != nil {
		planJSON, _ := json.MarshalIndent(existingPlan, "", "  ")
		b.Write(planJSON)
	}
	b.WriteString("\n```\n\n")

	manifest := prompt.LoadSiteManifest(siteDB)
	if manifest != "" {
		b.WriteString("## What's Actually Built\n")
		b.WriteString(manifest)
		b.WriteString("\n")
	}

	b.WriteString(`## Instructions

Create a PlanPatch JSON describing only the changes needed:

{
  "add_pages": [{"path": "/blog", "title": "Blog", "purpose": "...", "sections": [{"name": "...", "purpose": "..."}], "notes": "..."}],
  "modify_pages": [{"path": "/", "title": "Homepage", "purpose": "...", "notes": "..."}],
  "remove_pages": ["/old-page"],
  "add_endpoints": [{"action": "create_api", "path": "posts", "table_name": "posts"}],
  "modify_endpoints": [{"action": "create_api", "path": "users", "requires_auth": true, "public_read": true}],
  "add_tables": [{"name": "posts", "purpose": "...", "columns": [...]}],
  "modify_tables": [{"name": "users", "purpose": "...", "columns": [...], "add_columns": {"bio": "TEXT"}}],
  "add_webhooks": [{"name": "...", "direction": "incoming|outgoing", "event_types": [...]}],
  "add_scheduled_tasks": [{"name": "...", "description": "...", "prompt": "...", "cron": "..."}],
  "update_nav": true,
  "update_css": false,
  "update_auth_strategy": "jwt|localStorage-only|none",
  "update_design_system": {"colors": {"primary": "#hex", ...}, "dark_mode": true},
  "update_layout": {"style": "sidebar|topnav|minimal|any style", "nav_items": [...]}
}

Rules:
- Only include fields that actually change
- Respect existing exclusions — do NOT add excluded features
- update_design_system: only include if colors or design tokens need changing
`)

	return b.String()
}

// --- MONITORING prompt ---

func buildMonitoringPrompt(site *models.Site, siteDB *sql.DB, plan *Plan, ownerName string) string {
	var b strings.Builder

	writeContextSections(&b, &SectionContext{
		SiteDB:     siteDB,
		Plan:       plan,
		Site:       site,
		OwnerName:  ownerName,
		HeaderRole: MonitoringConfig.HeaderRole,
	}, MonitoringConfig.Sections)

	b.WriteString(`## Instructions
- Review the reported issues and assess severity
- Use manage_diagnostics for system health details
- Use manage_analytics for traffic patterns
- Monitoring is read-only for site content (pages, files, layout, endpoints). Do NOT modify them.
- You MAY use manage_communication to alert the owner and manage_memory to record observations.
- Be brief in your response
`)

	return b.String()
}

// chatWakeNeedsFullMode returns true when the user's message references backend
// concerns that require the full chat-wake tool set (endpoints, data, payments, etc.).
// When false, the lighter config with only page/file/layout tools is used.
func chatWakeNeedsFullMode(msg string) bool {
	lower := strings.ToLower(msg)
	for _, kw := range []string{
		"endpoint", "/api/", "api endpoint", "database", "schema", "table ",
		"webhook", "payment", "email", "secret", "provider",
		"scheduler", "cron", "background job", "upload", "blob",
		"search index", "seo", "sitemap", "server action", "http request",
		"manage_endpoint", "manage_schema", "manage_data", "manage_payment",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// --- CHAT-WAKE prompt ---

func buildChatWakePrompt(site *models.Site, siteDB *sql.DB, userMessage string, plan *Plan, ownerName string) string {
	var b strings.Builder

	writeContextSections(&b, &SectionContext{
		SiteDB:     siteDB,
		Plan:       plan,
		Site:       site,
		OwnerName:  ownerName,
		HeaderRole: ChatWakeConfig.HeaderRole,
	}, ChatWakeConfig.Sections)

	b.WriteString(`## Instructions
First, determine whether the owner's message is a **question** or a **change request**.
- **Question** (e.g. "what does X do?", "how does Y work?", "why is Z like that?"): Read the relevant code to understand, then use manage_communication to answer. Do NOT modify any files.
- **Change request** (e.g. "add X", "fix Y", "change Z to..."): Apply the change described below. Do not exceed its scope.
`)
	b.WriteString(patchRules)
	b.WriteString(`Use the CSS/JS references above to understand the codebase without reading everything.
If the fix is unclear or risky, use manage_communication to ask the owner before changing code.
If the request requires a major restructure, use manage_communication to suggest a rebuild.
`)

	return b.String()
}

// --- CHAT-WAKE LITE prompt ---

func buildChatWakeLitePrompt(site *models.Site, siteDB *sql.DB, userMessage string, plan *Plan, ownerName string) string {
	var b strings.Builder

	writeContextSections(&b, &SectionContext{
		SiteDB:     siteDB,
		Plan:       plan,
		Site:       site,
		OwnerName:  ownerName,
		HeaderRole: ChatWakeLiteConfig.HeaderRole,
	}, ChatWakeLiteConfig.Sections)

	b.WriteString(`## Instructions
First, determine whether the owner's message is a **question** or a **change request**.
- **Question** (e.g. "what does X do?", "how does Y work?", "why is Z like that?"): Read the relevant code to understand, then use manage_communication to answer. Do NOT modify any files.
- **Change request** (e.g. "add X", "fix Y", "change Z to..."): Apply the change described below. Do not exceed its scope.
`)
	b.WriteString(patchRules)
	b.WriteString("If the request requires backend changes (endpoints, database, payments, etc.), use manage_communication to tell the owner.\n")

	return b.String()
}

// --- VALIDATE fix-up prompt ---

func buildValidatePrompt(siteDB *sql.DB, plan *Plan, issues string) string {
	cfg := StageConfigs[StageValidate]
	var b strings.Builder

	writePromptHeader(&b, cfg.HeaderRole, "")
	writeContextSections(&b, &SectionContext{SiteDB: siteDB, Plan: plan}, cfg.Sections)

	// Compact plan reference — enough to know what should exist, not full specs.
	b.WriteString("## Plan Reference\n")
	b.WriteString(buildCompactPlanRef(plan))
	b.WriteString("\n")

	b.WriteString(`## Rules
1. READ before writing: use manage_pages(action="get") or manage_files(action="get") to see what exists before changing it.
2. PATCH, don't rewrite: use action="patch" with targeted search/replace. If a patch fails ("no patches matched"), retry once with a corrected old_string. If it fails again, fall back to action="save" with the full corrected content.
3. Fix ONLY the listed issues — do not touch working content unrelated to the issue list.
4. Asset wiring issues: use manage_pages(action="update", path="...", assets='["filename.js"]') to set the assets array. Do NOT try to inject <script> tags into the page HTML — the platform auto-injects assets listed in the array.
5. Match existing patterns: use the CSS classes and JS API shown above. Don't invent new patterns.
6. One issue at a time: fix one issue, move to the next. Stop calling tools when all issues are fixed.

Important: manage_pages returns a JSON object with metadata fields (path, template, assets, status) and a content field (the actual HTML). When patching, your old_string/new_string must target the HTML inside the content field — never target JSON metadata keys like "template" or "status".

Scope: Fix only the listed issues with minimal, targeted patches. Do not rewrite working pages/CSS/JS, change existing layout/endpoints/tables, or add anything not in the issue list.
`)
	return b.String()
}

// --- Scheduled task prompt ---

func buildScheduledTaskPrompt(globalDB, siteDB *sql.DB, siteID int, toolGuide, taskPrompt, ownerName string) string {
	var b strings.Builder

	// Load plan for context (design tokens, exclusions).
	var parsedPlan *Plan
	var planJSONStr sql.NullString
	siteDB.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSONStr)
	if planJSONStr.Valid && planJSONStr.String != "" {
		parsedPlan, _ = ParsePlan(planJSONStr.String)
	}

	// Load site model for SectionSiteInfo.
	siteModel, _ := models.GetSiteByID(globalDB, siteID)

	writeContextSections(&b, &SectionContext{
		SiteDB:     siteDB,
		GlobalDB:   globalDB,
		SiteID:     siteID,
		Plan:       parsedPlan,
		Site:       siteModel,
		OwnerName:  ownerName,
		ToolGuide:  toolGuide,
		HeaderRole: ScheduledTaskConfig.HeaderRole,
	}, ScheduledTaskConfig.Sections)

	// Plan summary so the LLM knows the site's structure.
	planSummary := prompt.LoadPlanSummary(siteDB)
	if planSummary != "" {
		b.WriteString(planSummary)
	}

	// Only inject CSS and design tokens when the task touches visual elements.
	lp := strings.ToLower(taskPrompt)
	needsVisualContext := strings.Contains(lp, "page") || strings.Contains(lp, "css") ||
		strings.Contains(lp, "design") || strings.Contains(lp, "style") ||
		strings.Contains(lp, "layout") || strings.Contains(lp, "theme")

	if needsVisualContext {
		css := prompt.LoadGlobalCSS(siteDB)
		if css != "" {
			if len(css) > cssPromptLimit {
				css = css[:cssPromptLimit] + "\n/* ... truncated ... */"
			}
			b.WriteString("## CSS Reference\n```css\n")
			b.WriteString(css)
			b.WriteString("\n```\n\n")
		}
		if parsedPlan != nil && parsedPlan.DesignSystem != nil && len(parsedPlan.DesignSystem.Colors) > 0 {
			dsJSON, _ := json.MarshalIndent(parsedPlan.DesignSystem, "", "  ")
			prompt.WriteDesignTokens(&b, dsJSON, prompt.DetailCompact)
		}
	}

	if parsedPlan != nil && len(parsedPlan.Exclusions) > 0 {
		b.WriteString("## Exclusions — do NOT create these\n")
		for _, ex := range parsedPlan.Exclusions {
			b.WriteString("- " + ex + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(`## Instructions
- Execute the task described in the user message
- Use tools as needed: query data, send emails, update pages, make HTTP requests
- Be concise — scheduled tasks run without an audience
- If you need information from the owner, use manage_communication to ask
`)

	return b.String()
}

// Context loaders (loadSiteManifest, loadGlobalCSS, etc.) have moved to prompt package.
// See prompt.LoadSiteManifest, prompt.LoadGlobalCSS, prompt.LoadGlobalJS, etc.
