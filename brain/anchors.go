/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/prompt"
)

// appendAnchorIfNew adds a user message to the conversation only if an identical
// message doesn't already exist. Uses the anchor store's dedup set for O(1)
// lookups when available, falling back to linear scan otherwise.
func (w *PipelineWorker) appendAnchorIfNew(messages []llm.Message, msg llm.Message) []llm.Message {
	// Fast path: use the dedup set if the anchor store is active.
	if w.anchors != nil && w.anchors.seenAnchors != nil {
		if w.anchors.seenAnchors[msg.Content] {
			return messages
		}
		w.anchors.seenAnchors[msg.Content] = true
		return append(messages, msg)
	}
	// Slow path: linear scan (used outside BUILD when no anchor store exists).
	for _, m := range messages {
		if m.Role == llm.RoleUser && m.Content == msg.Content {
			return messages
		}
	}
	return append(messages, msg)
}

// injectAnchors adds context-preserving [ANCHOR] messages after key tool calls.
// Anchors survive message pruning and keep the LLM aware of CSS classes, JS APIs,
// table schemas, and endpoint contracts as the conversation grows.
func (w *PipelineWorker) injectAnchors(toolName string, args map[string]interface{}, messages []llm.Message) []llm.Message {
	action, _ := args["action"].(string)

	switch toolName {
	case "manage_files":
		if action != "save" && action != "" {
			break
		}
		fn, _ := args["filename"].(string)
		content, _ := args["content"].(string)
		if content == "" {
			break
		}
		if strings.HasSuffix(fn, ".css") {
			if ref := prompt.ExtractCSSReference(content); ref != "" {
				msg := "[ANCHOR] CSS design vocabulary (" + fn + "). Prefer var(--color-*) tokens for primary colors.\n" + ref
				messages = w.appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: msg})
				if w.anchors != nil {
					w.anchors.cssReference = ref
					// Extract component families for consistent cross-page usage.
					if groups := prompt.ExtractComponentGroups(content); groups != "" {
						w.anchors.componentGroups = groups
					}
				}
			}
		}
		if strings.HasSuffix(fn, ".js") {
			scope, _ := args["scope"].(string)
			if scope == "global" {
				if ref := prompt.ExtractJSReference(content); ref != "" {
					msg := "[ANCHOR] JS API (" + fn + ", global) — use these exact signatures in page JS:\n" + ref
					messages = w.appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: msg})
					if w.anchors != nil {
						w.anchors.jsReference = ref
					}
				}
			}
		}

	case "manage_schema":
		if action == "create" {
			if summary := buildSchemaAnchor(args); summary != "" {
				messages = w.appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: summary})
				if w.anchors != nil {
					if name, _ := args["table_name"].(string); name != "" {
						w.anchors.tableSchemas[name] = summary
					}
				}
			}
		}

	case "manage_endpoints":
		if strings.HasPrefix(action, "create_") {
			if summary := buildEndpointAnchor(args); summary != "" {
				messages = w.appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: summary})
				if w.anchors != nil {
					if path, _ := args["path"].(string); path != "" {
						w.anchors.endpointAPIs["/api/"+path] = summary
					}
				}
			}
		}

	case "manage_layout":
		if action == "save" {
			template, _ := args["template"].(string)
			if template != "" {
				lower := strings.ToLower(template)
				hasHeader := strings.Contains(lower, "<header")
				hasFooter := strings.Contains(lower, "<footer")
				hasNav := strings.Contains(lower, "<nav")
				var parts []string
				if hasHeader {
					parts = append(parts, "has <header>")
				}
				if hasNav {
					parts = append(parts, "has <nav>")
				}
				if hasFooter {
					parts = append(parts, "has <footer>")
				}
				var summary string
				if len(parts) == 0 {
					summary = "[ANCHOR] Layout is chromeless (no header/nav/footer). Pages fill the viewport — do NOT add <header>, <nav>, or <footer> in page content."
				} else {
					summary = "[ANCHOR] Layout template provides: " + strings.Join(parts, ", ") + ". Do NOT duplicate these in page content — they are already in the layout."
				}
				messages = w.appendAnchorIfNew(messages, llm.Message{Role: llm.RoleUser, Content: summary})
				if w.anchors != nil {
					w.anchors.layoutSummary = summary
				}
			}
		}

	case "manage_pages":
		if action == "save" && w.anchors != nil {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)

			// Track page structure from the first few pages as design reference.
			if content != "" && len(w.anchors.pageStructures) < 3 {
				if structure := prompt.ExtractPageStructure(content); structure != "" {
					w.anchors.pageStructures[path] = structure
				}
			}

			// Record a brief design note for this page.
			if content != "" && path != "" {
				note := buildPageDesignNote(path, content)
				if note != "" {
					w.anchors.designNotes = append(w.anchors.designNotes, note)
				}
			}

			w.anchors.pagesBuiltCount++

			// Inject quality review nudge + next page context.
			if refresh := w.buildPageContextRefresh(); refresh != "" {
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: refresh})
			}
		}
	}

	return messages
}

// buildPageContextRefresh assembles a compact context summary from the anchor
// store to inject after each page save. This ensures the LLM has fresh awareness
// of the JS API, CSS vocabulary, and available endpoints when building the next page.
// Also includes quality review nudges, component patterns, and aesthetic re-anchoring.
func (w *PipelineWorker) buildPageContextRefresh() string {
	if w.anchors == nil {
		return ""
	}
	var b strings.Builder

	// Only include if we have meaningful content.
	hasContent := false

	// --- Quality review nudge ---
	// Brief self-check prompt to catch issues before moving on.
	b.WriteString("**Quality check:** Before moving to the next item, verify the page you just built: Does it have clear visual hierarchy? Do interactive elements work? Is it consistent with the design intent and earlier pages? If anything is off, patch it now.\n\n")
	hasContent = true

	// --- Layout structure reminder ---
	// Re-inject what the layout provides so pages don't duplicate header/footer/nav.
	if w.anchors.layoutSummary != "" {
		b.WriteString(w.anchors.layoutSummary + "\n\n")
	}

	// --- Aesthetic re-anchoring (every 3 pages) ---
	// Re-inject design intent and established patterns to prevent drift.
	if w.anchors.pagesBuiltCount > 0 && w.anchors.pagesBuiltCount%3 == 0 {
		if w.buildProgress != nil && w.buildProgress.plan != nil {
			plan := w.buildProgress.plan
			if plan.DesignSystem != nil && plan.DesignSystem.DesignIntent != "" {
				b.WriteString("### Design Reminder\n")
				b.WriteString("Design intent: " + plan.DesignSystem.DesignIntent + "\n")
				if plan.AppType != "" {
					b.WriteString("App type: " + plan.AppType + "\n")
				}
				// Include design notes from pages built so far.
				if len(w.anchors.designNotes) > 0 {
					b.WriteString("Patterns used so far: " + strings.Join(w.anchors.designNotes, " | ") + "\n")
				}
				// Re-anchor color palette so later pages don't drift on colors.
				if len(plan.DesignSystem.Colors) > 0 {
					pairs := make([]string, 0, len(plan.DesignSystem.Colors))
					for name, hex := range plan.DesignSystem.Colors {
						pairs = append(pairs, name+"="+hex)
					}
					sort.Strings(pairs)
					b.WriteString("Color tokens: " + strings.Join(pairs, ", ") + ". Always use var(--color-*), never hardcode.\n")
				}
				// Compact component family reminder for pages 5+ (full version injected up to page 4).
				if w.anchors.componentGroups != "" && w.anchors.pagesBuiltCount > 4 {
					b.WriteString("Component families: " + extractComponentFamilyNames(w.anchors.componentGroups) + " — reuse these, don't invent new ones.\n")
				}
				b.WriteString("Maintain this aesthetic consistently on the remaining pages.\n\n")
			}
		}
	}

	// --- Component groups (from CSS) ---
	// Inject component families so the LLM uses consistent class groups.
	if w.anchors.componentGroups != "" && w.anchors.pagesBuiltCount <= 4 {
		// Inject for the first several pages to establish patterns.
		b.WriteString("### Component Families (use these class groups together)\n")
		b.WriteString(w.anchors.componentGroups)
		b.WriteString("\n\n")
		hasContent = true
	}

	// --- Page structure reference (from first page) ---
	// Show the HTML patterns established by the first page so later pages stay consistent.
	if len(w.anchors.pageStructures) > 0 && w.anchors.pagesBuiltCount >= 1 && w.anchors.pagesBuiltCount <= 6 {
		b.WriteString("### Established HTML Patterns (match this structure)\n")
		count := 0
		for path, structure := range w.anchors.pageStructures {
			if count >= 2 {
				break
			}
			b.WriteString("From " + path + ":\n" + structure + "\n")
			count++
		}
		b.WriteString("\n")
		hasContent = true
	}

	// Inject the plan spec for the next unbuilt page so the LLM has
	// fresh awareness of what to build next.
	var nextPageEndpoints string
	if w.buildProgress != nil && w.buildProgress.plan != nil {
		for _, pg := range w.buildProgress.plan.Pages {
			if !w.buildProgress.pagesDone[pg.Path] {
				b.WriteString(fmt.Sprintf("## Next Page: %s — %s\n", pg.Path, pg.Title))
				if pg.Purpose != "" {
					b.WriteString("Purpose: " + pg.Purpose + "\n")
				}
				if len(pg.Sections) > 0 {
					b.WriteString("Sections: ")
					for i, s := range pg.Sections {
						if i > 0 {
							b.WriteString(", ")
						}
						b.WriteString(s.Name)
						if s.Purpose != "" {
							b.WriteString(" (" + s.Purpose + ")")
						}
					}
					b.WriteString("\n")
				}
				if len(pg.Endpoints) > 0 {
					nextPageEndpoints = strings.Join(pg.Endpoints, ",")
					b.WriteString("Uses: " + strings.Join(pg.Endpoints, ", ") + "\n")
				}
				if pg.Notes != "" {
					b.WriteString("Notes: " + pg.Notes + "\n")
				}
				b.WriteString("\n")
				hasContent = true
				break
			}
		}
	}

	b.WriteString("**Rules:** Prefer var(--color-*) tokens for primary colors. No TODOs/placeholders/Lorem ipsum. Every interactive element must work.\n\n")

	// Only inject full reference blocks (tables, JS API, endpoints, CSS classes)
	// when the next page uses different endpoints or tables than the last one.
	// This avoids re-processing identical context on consecutive similar pages
	// while ensuring pages that use different data relationships get fresh context.
	endpointsChanged := nextPageEndpoints != w.anchors.lastPageEndpoints
	w.anchors.lastPageEndpoints = nextPageEndpoints

	var nextPageTables string
	if w.buildProgress != nil && w.buildProgress.plan != nil {
		for _, pg := range w.buildProgress.plan.Pages {
			if !w.buildProgress.pagesDone[pg.Path] {
				nextPageTables = collectPageTables(pg, w.buildProgress.plan.Endpoints)
				break
			}
		}
	}
	tablesChanged := nextPageTables != w.anchors.lastPageTables
	w.anchors.lastPageTables = nextPageTables

	if endpointsChanged || tablesChanged {
		// Re-inject table schemas so late pages know column names for fetch/render.
		if len(w.anchors.tableSchemas) > 0 {
			b.WriteString("### Tables (column reference)\n")
			for _, summary := range w.anchors.tableSchemas {
				s := strings.TrimPrefix(summary, "[ANCHOR] ")
				b.WriteString("- " + s + "\n")
			}
			b.WriteString("\n")
			hasContent = true
		}

		if w.anchors.jsReference != "" {
			b.WriteString("### JS API (use exact signatures)\n")
			ref := w.anchors.jsReference
			if len(ref) > 1500 {
				ref = ref[:1500] + "\n..."
			}
			b.WriteString(ref)
			b.WriteString("\n\n")
			hasContent = true
		}

		if len(w.anchors.endpointAPIs) > 0 {
			b.WriteString("### Available Endpoints\n")
			for _, summary := range w.anchors.endpointAPIs {
				s := strings.TrimPrefix(summary, "[ANCHOR] ")
				b.WriteString("- " + s + "\n")
			}
			b.WriteString("\n")
			hasContent = true
		}
	}

	// Always inject CSS reference — compact class+property summaries prevent
	// class drift and help the LLM reuse existing classes correctly.
	if w.anchors.cssReference != "" {
		b.WriteString("### CSS Classes (prefer these — add new ones to global CSS via manage_files patch if needed)\n")
		b.WriteString(w.anchors.cssReference)
		b.WriteString("\n\n")
		hasContent = true
	}

	if !hasContent {
		return ""
	}
	// Tag so pruneMessages can identify and deduplicate page context messages.
	return "[PAGE_CTX]\n" + b.String()
}

// buildPageDesignNote creates a brief structural description of a page for
// design decision logging. This helps the LLM maintain aesthetic coherence
// across pages by knowing what patterns were used on earlier pages.
func buildPageDesignNote(path, html string) string {
	lower := strings.ToLower(html)
	var patterns []string

	// Detect common section types by class names and content patterns.
	sectionHints := []struct {
		keyword string
		label   string
	}{
		{"hero", "hero"},
		{"banner", "banner"},
		{"feature", "features"},
		{"pricing", "pricing"},
		{"testimonial", "testimonials"},
		{"faq", "FAQ"},
		{"cta", "CTA"},
		{"contact", "contact form"},
		{"footer", "footer"},
		{"sidebar", "sidebar"},
		{"grid", "grid layout"},
		{"carousel", "carousel"},
		{"slider", "slider"},
		{"gallery", "gallery"},
		{"timeline", "timeline"},
		{"stats", "stats"},
		{"metric", "metrics"},
		{"dashboard", "dashboard"},
		{"table", "data table"},
		{"form", "form"},
		{"card", "cards"},
		{"list", "list"},
		{"modal", "modal"},
		{"tab", "tabs"},
		{"accordion", "accordion"},
	}

	seen := map[string]bool{}
	for _, hint := range sectionHints {
		if strings.Contains(lower, hint.keyword) && !seen[hint.label] {
			seen[hint.label] = true
			patterns = append(patterns, hint.label)
		}
	}

	if len(patterns) == 0 {
		return ""
	}
	// Cap at 6 patterns to keep notes compact.
	if len(patterns) > 6 {
		patterns = patterns[:6]
	}
	return path + ": " + strings.Join(patterns, ", ")
}

// buildSchemaAnchor creates a compact summary of a schema creation for context preservation.
func buildSchemaAnchor(args map[string]interface{}) string {
	name, _ := args["table_name"].(string)
	if name == "" {
		return ""
	}
	var cols []string
	if colsRaw, ok := args["columns"]; ok {
		if colSlice, ok := colsRaw.([]interface{}); ok {
			for _, c := range colSlice {
				if cm, ok := c.(map[string]interface{}); ok {
					colName, _ := cm["name"].(string)
					colType, _ := cm["type"].(string)
					if colName != "" {
						cols = append(cols, colName+" "+colType)
					}
				}
			}
		}
	}
	summary := fmt.Sprintf("[ANCHOR] Table '%s': id (auto), %s, created_at (auto).", name, strings.Join(cols, ", "))
	if sc, ok := args["searchable_columns"]; ok {
		if scSlice, ok := sc.([]interface{}); ok {
			var scNames []string
			for _, s := range scSlice {
				if sn, ok := s.(string); ok {
					scNames = append(scNames, sn)
				}
			}
			if len(scNames) > 0 {
				summary += " FTS: " + strings.Join(scNames, ", ") + "."
			}
		}
	}
	return summary
}

// buildEndpointAnchor creates a compact summary of an endpoint creation.
func buildEndpointAnchor(args map[string]interface{}) string {
	action, _ := args["action"].(string)
	path, _ := args["path"].(string)
	table, _ := args["table_name"].(string)
	if path == "" {
		return ""
	}

	var summary string
	switch action {
	case "create_api":
		summary = fmt.Sprintf("[ANCHOR] CRUD /api/%s (table: %s). GET ?q=&sort=&order=, POST, PUT/:id, DELETE/:id. Frontend: use query params (?col=val), not filters=[{...}].", path, table)
		if ra, ok := args["requires_auth"].(bool); ok && ra {
			summary += " Auth: required."
		}
		if pr, ok := args["public_read"].(bool); ok && pr {
			summary += " Public read: yes."
		}
	case "create_auth":
		usernameCol, _ := args["username_column"].(string)
		passwordCol, _ := args["password_column"].(string)
		if passwordCol == "" {
			passwordCol = "password"
		}
		summary = fmt.Sprintf("[ANCHOR] Auth /api/%s: login(POST), register(POST), me(GET+Bearer). Table: %s. FIELD NAMES for register/login body: {%q: \"...\", %q: \"...\"}.", path, table, usernameCol, passwordCol)
	case "create_websocket":
		summary = fmt.Sprintf("[ANCHOR] WebSocket /api/%s/ws. Pure relay, echo suppression.", path)
		if room, ok := args["room_column"].(string); ok && room != "" {
			summary += " Rooms via ?room=."
		}
	case "create_stream":
		summary = fmt.Sprintf("[ANCHOR] SSE /api/%s/stream.", path)
	case "create_upload":
		summary = fmt.Sprintf("[ANCHOR] Upload POST /api/%s/upload -> {url, filename, size, type}.", path)
	case "create_llm":
		summary = fmt.Sprintf("[ANCHOR] LLM POST /api/%s/chat (SSE) and POST /api/%s/complete (JSON). No CRUD — use a separate create_api endpoint if page also needs data listing.", path, path)
		if streaming, ok := args["streaming"].(bool); ok && !streaming {
			summary += " Plan says streaming=false: page JS should use /complete (JSON), not /chat (SSE)."
		} else {
			summary += " Plan says streaming=true: page JS should use /chat (SSE) with EventSource pattern."
		}
	default:
		summary = fmt.Sprintf("[ANCHOR] Endpoint %s /api/%s.", action, path)
	}
	return summary
}

// collectPageTables returns a comma-separated sorted list of table names
// referenced by a page's endpoints. Used for context diff detection.
func collectPageTables(pg PageSpec, endpoints []EndpointSpec) string {
	if len(pg.Endpoints) == 0 {
		return ""
	}
	// Build a lookup from endpoint path to table name.
	pathToTable := make(map[string]string, len(endpoints))
	for _, ep := range endpoints {
		pathToTable[ep.Path] = ep.TableName
	}
	seen := make(map[string]bool)
	var tables []string
	for _, epRef := range pg.Endpoints {
		path := extractCRUDPath(epRef)
		if path == "" {
			continue
		}
		if tbl := pathToTable[path]; tbl != "" && !seen[tbl] {
			seen[tbl] = true
			tables = append(tables, tbl)
		}
	}
	sort.Strings(tables)
	return strings.Join(tables, ",")
}

// extractComponentFamilyNames extracts the leading class names from a component
// groups string (e.g. "card: .card, .card-header" → ".card"). Returns a compact
// comma-separated list of family prefixes.
func extractComponentFamilyNames(groups string) string {
	lines := strings.Split(groups, "\n")
	var names []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Lines look like "card: .card, .card-header, .card-body" or ".card, .card-header"
		if idx := strings.Index(line, ":"); idx > 0 {
			// Use the family name before the colon
			names = append(names, "."+strings.TrimSpace(line[:idx]))
		} else if strings.HasPrefix(line, ".") {
			// Use the first class name
			if comma := strings.Index(line, ","); comma > 0 {
				names = append(names, strings.TrimSpace(line[:comma]))
			} else {
				names = append(names, strings.TrimSpace(line))
			}
		}
	}
	if len(names) == 0 {
		return groups // fallback to original
	}
	return strings.Join(names, ", ")
}
