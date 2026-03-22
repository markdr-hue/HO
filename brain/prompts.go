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

// writePromptHeader writes the standard prompt opening: role description,
// owner name (if set), and current date. Used by PLAN, UPDATE_PLAN,
// MONITORING, and SCHEDULED_TASK stages. BUILD and CHAT-WAKE have custom
// openings and should NOT use this.
func writePromptHeader(b *strings.Builder, role, ownerName string) {
	b.WriteString(role)
	if ownerName != "" {
		b.WriteString(fmt.Sprintf("\nSite owner: %s\n", ownerName))
	}
	b.WriteString(fmt.Sprintf("Current date: %s\n\n", time.Now().UTC().Format("2006-01-02")))
}

// writeContextSections injects standard context blocks into a prompt based
// on the stage's declared Sections list. This replaces ad-hoc calls to
// prompt.WriteSiteManifest, prompt.WriteDesignTokens, etc. scattered across
// prompt builders. SectionHeader is skipped here — use writePromptHeader
// directly for headers since they need a role string. SectionPlatformContracts
// is also skipped since it requires plan context and tool guide text.
func writeContextSections(b *strings.Builder, siteDB *sql.DB, plan *Plan, sections []PromptSection) {
	for _, sec := range sections {
		switch sec {
		case SectionSiteManifest:
			prompt.WriteSiteManifest(b, siteDB)
		case SectionDesignTokens:
			if plan != nil && plan.DesignSystem != nil && len(plan.DesignSystem.Colors) > 0 {
				dsJSON, _ := json.MarshalIndent(plan.DesignSystem, "", "  ")
				prompt.WriteDesignTokens(b, dsJSON, prompt.DetailCompact)
			}
		case SectionCSSReference:
			prompt.WriteCSSReference(b, siteDB)
		case SectionJSReference:
			prompt.WriteJSReference(b, siteDB)
		case SectionMemories:
			prompt.WriteMemories(b, siteDB)
		case SectionDataLayer:
			prompt.WriteDataLayerSummary(b, siteDB)
		case SectionAnalytics:
			analytics := prompt.LoadAnalyticsSummary(siteDB)
			if analytics != "" {
				b.WriteString("## Analytics (Last 7 Days)\n")
				b.WriteString(analytics + "\n")
			}
		case SectionRecentErrors:
			errors := prompt.LoadRecentErrors(siteDB)
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
- Page content replaces {{content}} in the layout template (no DOCTYPE/html/head/body).
- /assets/ file tags are auto-injected — never add them manually.
- Reusable HTML: use {{include:component_name}} in page content to include a saved component. Create components with manage_components.
- On tool failure: read current state before retrying.
`

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
      {"name": "hero", "purpose": "Full-width intro with CTA button"},
      {"name": "features", "purpose": "Showcase key features from API", "endpoints": ["GET /api/features"]}
    ], "endpoints": ["GET /api/features"], "auth": false, "notes": "technical build details"}
  ],
  "external_libraries": [
    {"name": "three.js", "url": "https://cdn.jsdelivr.net/npm/three@latest/build/three.module.js", "purpose": "3D hero animation"}
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

// planGuidelines contains the stable instruction text for the PLAN stage.
const planGuidelines = `## When to Ask Questions
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
- create_llm endpoints: for AI-powered features (chatbots, assistants, content generators). Provide system_prompt to define the AI's role. Creates POST /api/{path}/chat (SSE streaming) and POST /api/{path}/complete (JSON response {content, model, usage, stop_reason}). Set "streaming": true (default) for chatbots/assistants that benefit from real-time token display, or "streaming": false for one-shot generators/classifiers where the full response is needed at once. SSE format: event "token" with data {"text":"chunk"}, event "done" with data {}. Frontend must use parsed.text (NOT parsed.content). When streaming is false, page JS should POST to /api/{path}/complete and read the JSON response. **max_tokens guidance**: default is 4096. For LLM endpoints that generate code (HTML/CSS/JS, games, components), set max_tokens to 8192 or higher — code generation routinely needs 4000-8000+ tokens. For simple text/chat, 2048-4096 is fine. Frontend should check response.stop_reason — if "max_tokens", the response was truncated. Optional: max_tokens, temperature, max_history, rate_limit, requires_auth.
- create_upload endpoints: set path="resource" to create POST /api/{resource}/upload. In page endpoint references, use "POST /api/{resource}/upload" (upload comes AFTER the resource name). Default allowed types include image/*, text/*, PDF, JSON, ZIP. Specify allowed_types to restrict or expand (e.g. ["text/csv", "application/json"]).
- create_websocket endpoints: IMPORTANT: this is a stateless message relay, NOT a game server. The server only forwards messages between clients in the same room. It does NOT run matchmaking, manage game state, or send game events. For multiplayer games/apps, ALL game logic (matchmaking, state sync, win conditions) must be in client-side JS. Clients coordinate peer-to-peer through the relay. In pages.notes, specify: "WebSocket relay pattern: clients coordinate directly, lowest-ID player acts as host."
- Every table that pages read/write via fetch() needs a create_api endpoint. LLM endpoints (create_llm) only serve /chat and /complete sub-routes — they do NOT provide CRUD (GET list, GET by id, POST, PUT, DELETE). If a page needs standard REST operations on a resource, you MUST include a separate create_api endpoint for that table in addition to any create_llm endpoint.
- Let the content and audience drive the aesthetic, layout, and page structure.
- Row-level security: set owner_column on create_api endpoints (e.g. "owner_column": "user_id") to scope data per authenticated user. GET returns only owned rows, POST auto-sets the column, PUT/DELETE affect only owned rows. Admin role bypasses the filter. Use for multi-user apps (dashboards, marketplaces, social platforms).
- Match complexity to the request. Do NOT add authentication unless the user explicitly asks for accounts, login, or multi-user features. Do NOT add extra pages unless the user's description implies them. When in doubt, build less — the owner can always ask for more via chat.
- **Architecture guide**: "spa" = single HTML page with client-side routing in JS (use history.pushState). "single-page" = one page, no routing (games, tools, visualizations). "multi-page" = separate HTML pages served by the platform. "hybrid" = mix of server-rendered and SPA sections. Choose based on the app's nature, not a default.
- pages.notes: actionable build instructions — API calls, state management, key interactions.
- seed_data: a few example rows showing data shape only — keep minimal to save tokens. The BUILD stage decides whether to expand with realistic data or skip seeding (e.g. for user-generated tables like messages or orders).
- actions: server-side hooks that fire on events without the LLM (e.g. send welcome email on registration, log to audit table on data changes). Only include if the user explicitly requests event-driven behavior or it's clearly implied (e.g. "email confirmation on signup"). Do NOT add actions by default.
  - update_data actions require: table (string), set (object), where (object). For counters use {"$increment":1} or {"$decrement":1} as the value. Event payload includes all request body fields — use {{field_name}} templates.
  - ws_broadcast actions push real-time messages to WebSocket clients: {"endpoint_path":"/ws/chat", "room":"general", "message":{"type":"new_item","data":{"id":"{{id}}"}}}. Use for live dashboards, notifications, and real-time feeds.
  - run_sql actions execute SQL on events (INSERT/UPDATE/SELECT allowed, DROP/ALTER/DELETE blocked): {"sql":"UPDATE stats SET count = (SELECT COUNT(*) FROM orders)"}. Use for computed aggregations.
  - enqueue_job actions create background jobs: {"type":"send_email", "payload":{"to":"{{email}}"}, "max_attempts":3}. Use for deferred/retryable work.
- components: reusable HTML snippets shared across pages. Include in page content with {{include:component_name}}. Use for repeated UI blocks (cards, forms, navigation sections). Only plan components when multiple pages share the same HTML structure.
- columns.references: foreign key constraint linking to another table — format "other_table(id)". Enforced by SQLite. Use for relational data (orders→users, comments→posts).
- cache_ttl on create_api endpoints: response cache in seconds for GET requests (e.g. 60 = cache for 1 minute). Cache auto-invalidates on POST/PUT/DELETE. Use for read-heavy endpoints like product listings or dashboards.

## Layout & Chrome
- header and footer are REQUIRED fields — you must set them explicitly for every plan. Do not omit them.
- Think about whether a traditional header makes sense for this specific app:
  - "full": sites where users navigate between multiple distinct pages — portfolios, e-commerce, blogs, community sites, documentation, multi-page dashboards
  - "minimal": apps that benefit from a small brand/title bar but don't need full navigation — focused tools, single-purpose utilities, onboarding flows
  - "none": apps where a header would waste screen space or feel out of place — chat/messaging UIs, games, canvas apps, 3D experiences, fullscreen tools, immersive single-screen apps, visualizations, animations, art pieces
- Ask yourself: "Would a user of this app expect a navigation bar at the top?" If not, use "none".
- When architecture is "single-page", the app rarely needs a full header — there's nothing to navigate to.
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

// --- PLAN stage prompt ---

func buildPlanPrompt(site *models.Site, ownerName, answers, capabilitiesRef string) string {
	var b strings.Builder

	writePromptHeader(&b, "You are HO, an AI that plans and designs sites and applications. Your job is to understand what the user wants, map it to platform capabilities, and produce a complete build plan. Respond with ONLY a raw JSON object — no markdown code fences, no explanation text, no ```json blocks.", ownerName)

	b.WriteString("## Site Info\n")
	b.WriteString(fmt.Sprintf("- Name: %s\n", site.Name))
	if site.Description != nil && *site.Description != "" {
		b.WriteString(fmt.Sprintf("- Description: \"%s\"\n", *site.Description))
	}
	b.WriteString("\n")

	if answers != "" {
		b.WriteString("## Owner's Answers to Your Questions\n")
		b.WriteString(fmt.Sprintf("\"%s\"\n\n", answers))
	}

	b.WriteString("## Platform Capabilities Reference\nThis platform can build any kind of web application. The capabilities below are your building blocks.\n\n")
	b.WriteString(capabilitiesRef)
	prompt.WritePlatformContracts(&b, nil)

	b.WriteString("## Instructions\n\nAnalyze the requirements and produce a complete Plan JSON — the build specification that drives everything. Think creatively about the best way to build this using the platform's capabilities.\n\nYou have access to make_http_request for research — use it to verify CDN URLs, check external API availability, or fetch schema information when planning integrations. When you're ready, output the Plan JSON as your final response.\n\n")
	b.WriteString(planJSONSchema)
	b.WriteString(planGuidelines)

	return b.String()
}

// --- BUILD prompt ---

// buildOrderChecklist computes a deterministic build order from the Plan.
// Tables → Endpoints → Seed Data → Layout → CSS → Shared JS → Pages. Pure Go, zero LLM tokens.
// When progress is non-nil, completed items are marked [DONE] so the LLM skips them.
func buildOrderChecklist(plan *Plan, progress *buildProgressTracker) string {
	var b strings.Builder
	b.WriteString("## Recommended Build Order (tables before endpoints, layout before CSS so header/footer styles are included, CSS before pages — but you may reorder if you have a good reason)\n")
	step := 1

	for _, t := range plan.Tables {
		if progress != nil && progress.tablesDone[t.Name] {
			b.WriteString(fmt.Sprintf("%d. [DONE] Create table: %s (already created — skip)\n", step, t.Name))
		} else {
			b.WriteString(fmt.Sprintf("%d. Create table: %s\n", step, t.Name))
		}
		step++
	}
	for _, ep := range plan.Endpoints {
		done := progress != nil && progress.endpointsDone[ep.Action+":"+ep.Path]
		entry := fmt.Sprintf("%d. ", step)
		if done {
			entry += "[DONE] "
		}
		entry += fmt.Sprintf("Create endpoint: %s %s", ep.Action, ep.Path)
		if ep.TableName != "" {
			entry += fmt.Sprintf(" (table: %s)", ep.TableName)
		}
		if ep.Action == "create_llm" {
			if ep.Streaming != nil && !*ep.Streaming {
				entry += " (use /complete — non-streaming generator)"
			} else {
				entry += " (use /chat — streaming chatbot/assistant)"
			}
		}
		if done {
			entry += " (already created — skip)"
		}
		b.WriteString(entry + "\n")
		step++
	}
	if len(plan.Tables) > 0 {
		b.WriteString(fmt.Sprintf("%d. Populate all tables with rich, realistic seed data\n", step))
		step++
	}
	// Layout BEFORE CSS so the brain knows the header/footer structure when writing CSS.
	header, footer := layoutHeaderFooter(plan)
	layoutDesc := "Create layout"
	if header == "none" && footer == "none" {
		layoutDesc = "Create layout (head_content only — no header or footer)"
	} else if header == "none" {
		layoutDesc = "Create layout (no header, footer only)"
	} else if footer == "none" {
		layoutDesc = "Create layout (header only, no footer)"
	}
	if len(plan.ExternalLibraries) > 0 {
		layoutDesc += " (include CDN links for external libraries in head_content)"
	}
	b.WriteString(fmt.Sprintf("%d. %s\n", step, layoutDesc))
	step++
	switch {
	case header == "none" && footer == "none":
		b.WriteString(fmt.Sprintf("%d. Create global CSS (full-viewport layout — no .site-header or .site-footer needed)\n", step))
	case header == "none":
		b.WriteString(fmt.Sprintf("%d. Create global CSS (include .site-footer styles — no .site-header needed)\n", step))
	case footer == "none":
		b.WriteString(fmt.Sprintf("%d. Create global CSS (include .site-header styles — no .site-footer needed)\n", step))
	default:
		b.WriteString(fmt.Sprintf("%d. Create global CSS (include .site-header and .site-footer styles to match the layout template)\n", step))
	}
	step++
	// Only include shared JS step if there are endpoints or auth (otherwise nothing to share).
	if len(plan.Endpoints) > 0 || plan.AuthStrategy != "" {
		b.WriteString(fmt.Sprintf("%d. Create shared JS utilities (auth, fetch helpers, etc.) with scope=\"global\"\n", step))
		step++
	}
	for _, comp := range plan.Components {
		b.WriteString(fmt.Sprintf("%d. Create component: %s (%s) — use manage_components, include in pages with {{include:%s}}\n", step, comp.Name, comp.Purpose, comp.Name))
		step++
	}
	for _, pg := range plan.Pages {
		if len(pg.Endpoints) > 0 || len(plan.Endpoints) > 0 {
			b.WriteString(fmt.Sprintf("%d. Create page: %s (%s) — then immediately create its page-specific .js file (scope=\"page\") and list it in the page's assets array\n", step, pg.Path, pg.Title))
		} else {
			b.WriteString(fmt.Sprintf("%d. Create page: %s (%s)\n", step, pg.Path, pg.Title))
		}
		step++
	}
	for _, a := range plan.Actions {
		b.WriteString(fmt.Sprintf("%d. Create action: %s (on %s → %s)\n", step, a.Name, a.EventType, a.ActionType))
		step++
	}
	b.WriteString("\n")
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
}

// minimalKeywords are app_type substrings that should default to minimal chrome.
var minimalKeywords = []string{
	"calculator", "timer", "clock", "converter", "reader", "viewer",
	"player", "editor", "terminal", "ide", "notebook",
	"tool", "utility",
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

	if header == "none" && footer == "none" {
		b.WriteString("This app is chromeless — no header, no footer, no navbar. The template should be minimal:\n")
		b.WriteString("```\n<main>{{content}}</main>\n```\n")
		b.WriteString("Pages fill the viewport — design them as complete, self-contained experiences.\n")
	} else {
		b.WriteString("The template should include the full page shell. Example structure:\n```\n")
		switch header {
		case "full":
			b.WriteString("<header class=\"site-header\">\n  <div class=\"container\">\n    <nav>...branding, nav links, auth controls...</nav>\n  </div>\n</header>\n")
		case "minimal":
			b.WriteString("<header class=\"site-header\">\n  <div class=\"container\">\n    ...logo/brand mark or back-navigation...\n  </div>\n</header>\n")
		}
		b.WriteString("<main>\n  {{content}}\n</main>\n")
		switch footer {
		case "full":
			b.WriteString("<footer class=\"site-footer\">\n  <div class=\"container\">\n    ...links, copyright...\n  </div>\n</footer>\n")
		case "minimal":
			b.WriteString("<footer class=\"site-footer\">\n  <div class=\"container\">\n    ...copyright...\n  </div>\n</footer>\n")
		}
		b.WriteString("```\n")
		b.WriteString("Always use semantic `<header>` and `<footer>` tags for the site header and footer — not plain `<div>` wrappers.\n")
		b.WriteString("The header/footer must feel like a natural part of the design: use the same design tokens, typography, and spacing as the rest of the app.\n")
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

// buildBuildPrompt creates the prompt for the single unified BUILD session.
// When compact is true, the Build Guide section is omitted (used after infrastructure
// phase is done and only pages remain — saves ~1,600 tokens per call).
func buildBuildPrompt(plan *Plan, site *models.Site, ownerName, existingManifest, toolGuide string, progress *buildProgressTracker, compact ...bool) string {
	isCompact := len(compact) > 0 && compact[0]
	var b strings.Builder
	header, footer := layoutHeaderFooter(plan)

	// Scale-aware opening: don't mention tables/endpoints/design system for tiny sites.
	isMinimal := len(plan.Tables) == 0 && len(plan.Endpoints) == 0 && len(plan.Pages) <= 2
	if isMinimal {
		b.WriteString("You are HO. Build this site in one session. It's a small project — keep CSS minimal (only styles actually used), skip component utility classes you won't need. Build exactly what the plan specifies, nothing more. Use the recommended build order below as a guide.\n\n")
	} else {
		b.WriteString("You are HO. Build this complete site in one session: database tables, API endpoints, CSS design system, page layout, and all pages. Build what the plan specifies — no extra pages, tables, or endpoints beyond the plan. You MAY add supporting UI elements (modals, toasts, loading states, empty states, utility classes) that serve planned features — but do not add new pages, data tables, or user-facing features beyond the plan. Creative expression goes into *how* you build it (design, interactions, copy, component choices), not *what* you build. Use the recommended build order below as a guide.\n\n")
	}

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

	// Inject design thesis to re-anchor aesthetics at a high-attention point.
	if !isMinimal && plan.AppType != "" {
		b.WriteString(fmt.Sprintf("## Design Thesis\nThis is a %s.", plan.AppType))
		if plan.DesignSystem != nil && plan.DesignSystem.DesignIntent != "" {
			b.WriteString(fmt.Sprintf("\n\nDesign intent: %s", plan.DesignSystem.DesignIntent))
		}
		b.WriteString("\nLet this personality inform every page — avoid generic card-grid layouts unless they match the app's purpose.\n\n")
	}

	if existingManifest != "" {
		b.WriteString("## Already Built (crash recovery — do NOT recreate these)\n")
		b.WriteString(existingManifest)
		b.WriteString("\n\n")
	}

	b.WriteString("## Platform Contracts\n\n")
	b.WriteString(toolGuide)
	prompt.WritePlatformContracts(&b, plan)

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
	b.WriteString("- No placeholder text or TODO comments. Minimal console.logs (error handlers only).\n\n")

	if !isCompact {
		b.WriteString("## Build Guide\n\n")

		// Seed data section — only when tables exist.
		if len(plan.Tables) > 0 {
			b.WriteString(`### Seed Data
Seed data is optional — only populate tables when demo content makes the site feel real (blogs, products, listings, testimonials). Skip seeding for tables that are purely user-generated (messages, orders, form submissions) or when the app works better empty on first visit.
When seeding: use manage_data(action="insert", rows=[{...}, {...}, ...]) to bulk-insert all rows for a table in ONE call (max 100 rows). Do NOT insert rows one at a time. Use your judgement on how many rows each table needs based on its purpose. The plan's seed_data is a SHAPE HINT only — generate richer, more varied data.
- For placeholder images use services like picsum.photos/{width}/{height}?random={n} or generate SVG placeholders. For avatars: i.pravatar.cc/150?u={unique} or similar.

`)
		}

		// CSS section — scale-aware.
		if isMinimal {
			b.WriteString(`### CSS
Create one global CSS file. Only define styles you actually use.
Use design tokens (custom properties) for colors and fonts so the page is easy to theme.
`)
		} else {
			b.WriteString(`### CSS
Create one global CSS file with design tokens as custom properties and ALL component/page classes the site needs — including page-specific styles.
Do NOT put <style> blocks in page HTML. ALL CSS goes in the global stylesheet. If you need styles for a new page, use manage_files(action="patch") to append them to the existing CSS file.
Design the CSS to match the personality of this specific app — a recipe site should feel different from a SaaS dashboard.
`)
		}

		if chromeless {
			b.WriteString("This is a chromeless app — pages fill the viewport. Structure the CSS for full-viewport layouts (canvas, flex/grid containers, absolute positioning). No .container or max-width constraints needed unless the design calls for them.\n")
		} else {
			b.WriteString("Use a consistent max-width container pattern for content alignment across header, footer, and page sections.\n")
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

		// Page chrome instructions depend on whether layout provides header/footer.
		var pageChrome string
		if chromeless {
			pageChrome = "- This app is chromeless — no layout header/footer. Pages render full-viewport. Do NOT add <header>, <nav>, or <footer> elements in page content unless the page spec explicitly requires inline navigation."
		} else {
			pageChrome = "- Page content replaces {{content}} in the layout template. Do NOT add <header>, <nav>, or <footer> — the template provides these."
		}

		if isMinimal {
			// Lightweight page/JS guidance for small sites.
			containerHint := "\n- Wrap each section's inner content in a <div class=\"container\"> for consistent horizontal alignment."
			if chromeless {
				containerHint = ""
			}
			b.WriteString(fmt.Sprintf(`
### Pages
%s%s
- For complex interactivity, create a page-scope .js file and list it in the page's assets array. Small inline <script> tags are fine for simple interactions (toggles, counters, etc.).

`, pageChrome, containerHint))
			b.WriteString(platformRules)
		} else {
			// Full guidance for multi-page apps.
			b.WriteString(fmt.Sprintf(`
### Pages
For each page: (a) if needed, patch the global CSS file to add page-specific classes, (b) create HTML with manage_pages (pure HTML structure), (c) create its page-scope .js file for complex logic, (d) set assets array to include the .js filename. Small inline <script> tags are fine for simple interactions — use your judgement on when a separate file is warranted.
- Before building each page, re-read that page's plan entry (sections, endpoints, notes). Sections describe *what content goes there* — adapt the visual layout to fit the design system. Do not carry layout assumptions from the previous page.
- Page JS must use the EXACT function/method names from global JS files. The system injects the API reference after each global JS save — follow it.
%s
- For consistency: if unsure how a later page should look, use manage_pages(action="get") to review an earlier page's structure.

### JavaScript
- Shared logic → global-scope .js (auto-injected). Page logic → page-scope .js (listed in assets).
- Route params: window.__routeParams.id for dynamic pages.
- Auth: store JWT in localStorage key 'auth_token'. Header: Authorization: 'Bearer ' + token.
- Auth: set path="auth" in create_auth endpoint. This creates /api/auth/register, /api/auth/login, /api/auth/me, /api/auth/refresh.
- Token refresh: POST /api/{auth_path}/refresh with Authorization header returns a new token. Use to extend sessions without re-login.
- **Auth field names**: The register/login body must use the EXACT column names from the auth endpoint config. If username_column="email", send {"email": "...", "password": "..."} — NOT {"username": "..."}. The server matches field names to column names.
- **API patterns**: See the manage_endpoints guide above for response shapes, auth headers, and frontend fetch patterns. Filtering: ?col=val, ?col__like=, ?col__gt=, ?q=term. Sorting: ?sort=col&order=asc|desc. Column selection: ?fields=col1,col2. The filters=[{...}] syntax is for tools only, never for frontend. Only sort by columns that exist — tables auto-add "id" and "created_at" (NOT "updated_at"). Do NOT use "order_direction" — the correct param names are "order" or "direction".
- **LLM endpoints**: POST /api/{path}/chat for SSE streaming (parse event "token" → data.text, NOT data.content), POST /api/{path}/complete for full JSON response (use response.content, NOT .text). Check stop_reason — "max_tokens" means truncated. For generated HTML: use innerHTML. For generated data: JSON.parse(response.content).
- **WebSocket is a RELAY, not a game server**. The server only forwards messages between clients in the same room. It does NOT process messages, manage game state, run matchmaking, or send game events. ALL game logic must be in client-side JS.
  **Connection**: Always include ?room=ROOMNAME. All players must use the same room. Example: new WebSocket(wsUrl + '?room=lobby')
  **Three system message types** (have _type field, check it first in onmessage):
  - _type:"welcome" (sent only to you on connect): {_clientId:"YOUR-UUID", _clients:N, _clientIds:[...]}. Store _clientId as your player ID. Use _clientIds to know who's already in the room.
  - _type:"join": {_sender:"UUID", _clients:N}. A new client joined.
  - _type:"leave": {_sender:"UUID", _clients:N}. A client left.
  **User messages** (no _type, your custom type field): relayed from other clients with _sender:"UUID" added. Your own sends are NOT echoed back (update UI optimistically).
  **Multiplayer game pattern**:
  1. On _type:"welcome": store _clientId as your player ID. Broadcast: send({type:'player_join', id:myClientId, name:myName}).
  2. On _type:"join": re-broadcast your player info so the newcomer sees you.
  3. On user type:'player_join': add sender to local player list.
  4. Matchmaking: when _clients >= required, compare _clientIds alphabetically; first ID = "host" who broadcasts {type:'game_start', seed:..., players:[...]}.
  5. NEVER wait for server-sent "match_found", "player_list", or "game_start". The server does not generate these. Clients coordinate peer-to-peer through the relay.
  6. Use _clientId from welcome as your player ID everywhere (NOT a self-generated ID). This ensures _sender fields match your ID.

`, pageChrome))
			b.WriteString(platformRules)
		}

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
			b.WriteString(`### Fullscreen/Canvas App
- This is a chromeless app — pages fill the entire viewport.
- Structure your page around a single root element (canvas, div#app, etc.).
- All interactivity lives in JavaScript — the page HTML is the scaffold, JS is the engine.
- Use requestAnimationFrame for render loops, not setInterval.
- Handle window resize if the viewport size matters to the app.
- Game state, animations, and input handling all go in page-scope JS files.

`)
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
	var b strings.Builder

	writePromptHeader(&b, "You are HO, planning incremental changes to an existing site. Respond with ONLY a JSON PlanPatch object.", ownerName)

	if site != nil {
		b.WriteString("## Site\n")
		b.WriteString(fmt.Sprintf("- Name: %s\n", site.Name))
		if site.Description != nil && *site.Description != "" {
			b.WriteString(fmt.Sprintf("- About: \"%s\"\n", *site.Description))
		}
		b.WriteString("\n")
	}

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

	b.WriteString("## Platform Capabilities Reference\n\n")
	b.WriteString(capabilitiesRef)
	prompt.WritePlatformContracts(&b, existingPlan)

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

	writePromptHeader(&b, "You are HO, monitoring a live website. Be brief and only act if needed.", ownerName)

	b.WriteString("## Site\n")
	b.WriteString(fmt.Sprintf("- Name: %s\n", site.Name))
	b.WriteString("- Mode: monitoring\n")
	if plan != nil {
		b.WriteString(fmt.Sprintf("- Type: %s, Architecture: %s, Auth: %s\n", plan.AppType, plan.Architecture, plan.AuthStrategy))
		b.WriteString(fmt.Sprintf("- Plan: %d pages, %d endpoints, %d tables\n", len(plan.Pages), len(plan.Endpoints), len(plan.Tables)))
	}
	b.WriteString("\n")

	writeContextSections(&b, siteDB, plan, MonitoringConfig.Sections)

	b.WriteString(`## Instructions
- Review the reported issues and assess severity
- Use manage_diagnostics for system health details
- Use manage_analytics for traffic patterns
- Do NOT modify pages, layout, files, or site settings. You may notify the owner via manage_communication if a critical issue is found.
- Monitoring is read-only for content — only use diagnostic and analytics tools
- Be brief in your response
`)

	return b.String()
}

// --- CHAT-WAKE prompt ---

func buildChatWakePrompt(site *models.Site, siteDB *sql.DB, userMessage string, plan *Plan, ownerName string) string {
	var b strings.Builder

	b.WriteString("You are HO, responding to the site owner's message. The site is live and in monitoring mode.\n")
	if ownerName != "" {
		b.WriteString(fmt.Sprintf("The owner (%s) has sent you a message — read it carefully and take action if needed.\n\n", ownerName))
	} else {
		b.WriteString("The owner has sent you a message — read it carefully and take action if needed.\n\n")
	}
	b.WriteString(fmt.Sprintf("Current date: %s\n\n", time.Now().UTC().Format("2006-01-02")))

	b.WriteString("## Site\n")
	b.WriteString(fmt.Sprintf("- Name: %s\n", site.Name))
	if plan != nil {
		b.WriteString(fmt.Sprintf("- Type: %s, Architecture: %s, Auth: %s\n", plan.AppType, plan.Architecture, plan.AuthStrategy))
		b.WriteString(fmt.Sprintf("- Plan: %d pages, %d endpoints, %d tables\n", len(plan.Pages), len(plan.Endpoints), len(plan.Tables)))
	}
	b.WriteString("\n")

	writeContextSections(&b, siteDB, plan, ChatWakeConfig.Sections)

	b.WriteString(`## Instructions
The owner's message (below) describes what to change. Do not exceed its scope.
1. Read the relevant files — you may read multiple files if needed to understand the issue
2. Apply targeted patches:
   - manage_pages(action="patch", patches='[{"search":"...","replace":"..."}]') for page HTML/JS
   - manage_files(action="patch", patches='[{"search":"...","replace":"..."}]') for CSS/JS files
   - manage_layout(action="patch", patches='[{"search":"...","replace":"..."}]', field=template|head_content) for layout template/nav/footer
3. Briefly confirm what you changed

Determine whether this is a targeted fix (broken functionality, bugs, visual errors) or an improvement (redesign, new features, enhancements). For fixes: apply the minimum targeted change — no bonus improvements or refactoring. For improvements: you have creative latitude for coherent adjacent changes that support the owner's goal (e.g., adjusting spacing on neighboring sections when redesigning a hero). Stay focused on the requested area.
Use the CSS/JS references above to understand the codebase without reading everything.
If the request requires a major restructure, use manage_communication to suggest a rebuild.
`)

	return b.String()
}

// --- VALIDATE fix-up prompt ---

func buildValidatePrompt(siteDB *sql.DB, plan *Plan, issues string) string {
	var b strings.Builder
	b.WriteString("You are HO. The build is done but validation found a few missing pieces. Your job is SURGICAL: fix ONLY the listed issues with minimal changes. Do NOT rewrite, restructure, or touch anything that already works.\n\n")

	// Show what already exists so the LLM knows the current state.
	manifest := prompt.LoadSiteManifest(siteDB)
	if manifest != "" {
		b.WriteString("## What Already Exists (DO NOT MODIFY)\n")
		b.WriteString(manifest)
		b.WriteString("\n")
	}

	writeContextSections(&b, siteDB, plan, StageConfigs[StageValidate].Sections)

	// Compact plan reference — enough to know what should exist, not full specs.
	b.WriteString("## Plan Reference\n")
	b.WriteString(buildCompactPlanRef(plan))
	b.WriteString("\n")

	b.WriteString(`## Rules
1. READ before writing: use manage_pages(action="get") or manage_files(action="get") to see what exists before changing it.
2. PATCH, don't rewrite: use action="patch" with targeted search/replace. Never use action="save" to rewrite a full page unless the page is completely empty.
3. Fix ONLY the listed issues — if a page works but is missing a JS asset wire, add the asset wire. Don't touch the HTML.
4. Match existing patterns: use the CSS classes and JS API shown above. Don't invent new patterns.
5. One issue at a time: fix one issue, move to the next. Stop calling tools when all issues are fixed.

NEVER do these:
- Rewrite an entire page that already has content
- Rewrite CSS or JS files that already work
- Change layout templates, endpoints, or tables that already exist (layout head_content may be updated if flagged)
- Add features, pages, or endpoints not in the issue list
`)
	return b.String()
}

// --- Scheduled task prompt ---

func buildScheduledTaskPrompt(globalDB, siteDB *sql.DB, siteID int, toolGuide, taskPrompt, ownerName string) string {
	var b strings.Builder

	writePromptHeader(&b, "You are HO, executing a scheduled task. Use the available tools to complete the task described in the user message.", ownerName)

	site := loadSiteContext(globalDB, siteID)
	if site != nil {
		b.WriteString(fmt.Sprintf("## Site: %s (mode: %s)\n", site.name, site.mode))
		if site.description != "" {
			b.WriteString(fmt.Sprintf("- About: %s\n", site.description))
		}
		b.WriteString("\n")
	}

	// Plan summary so the LLM knows the site's structure.
	planSummary := prompt.LoadPlanSummary(siteDB)
	if planSummary != "" {
		b.WriteString(planSummary)
	}

	writeContextSections(&b, siteDB, nil, ScheduledTaskConfig.Sections)

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
	}

	// Include design tokens and exclusions from plan if available.
	var parsedPlan *Plan
	var planJSONStr sql.NullString
	siteDB.QueryRow("SELECT plan_json FROM ho_pipeline_state WHERE id = 1").Scan(&planJSONStr)
	if planJSONStr.Valid && planJSONStr.String != "" {
		if p, err := ParsePlan(planJSONStr.String); err == nil {
			parsedPlan = p
			if needsVisualContext && p.DesignSystem != nil && len(p.DesignSystem.Colors) > 0 {
				dsJSON, _ := json.MarshalIndent(p.DesignSystem, "", "  ")
				prompt.WriteDesignTokens(&b, dsJSON, prompt.DetailCompact)
			}
			if len(p.Exclusions) > 0 {
				b.WriteString("## Exclusions — do NOT create these\n")
				for _, ex := range p.Exclusions {
					b.WriteString("- " + ex + "\n")
				}
				b.WriteString("\n")
			}
		}
	}

	b.WriteString("## Platform Contracts\n\n")
	b.WriteString(toolGuide)
	prompt.WritePlatformContracts(&b, parsedPlan)

	b.WriteString(`## Instructions
- Execute the task described in the user message
- Use tools as needed: query data, send emails, update pages, make HTTP requests
- Be concise — scheduled tasks run without an audience
- If you need information from the owner, use manage_communication to ask
`)

	return b.String()
}

// --- Context loaders ---

type siteContext struct {
	name, domain, mode, description string
}

func loadSiteContext(db *sql.DB, siteID int) *siteContext {
	var s siteContext
	var domain, description sql.NullString
	err := db.QueryRow(
		"SELECT name, domain, mode, description FROM sites WHERE id = ?",
		siteID,
	).Scan(&s.name, &domain, &s.mode, &description)
	if err != nil {
		return nil
	}
	s.domain = domain.String
	s.description = description.String
	return &s
}

// Context loaders (loadSiteManifest, loadGlobalCSS, etc.) have moved to prompt package.
// See prompt.LoadSiteManifest, prompt.LoadGlobalCSS, prompt.LoadGlobalJS, etc.
