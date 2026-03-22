/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/markdr-hue/HO/db"
)

// --- Plan: unified output of the PLAN stage (replaces Analysis + Blueprint) ---

// Plan is the single build specification produced by the PLAN stage.
type Plan struct {
	AppType           string         `json:"app_type"`
	Architecture      string         `json:"architecture,omitempty"`
	AuthStrategy      string         `json:"auth_strategy,omitempty"`
	DesignSystem      *DesignSystem  `json:"design_system,omitempty"`
	Layout            *LayoutSpec    `json:"layout,omitempty"`
	Tables            []TableSpec    `json:"tables,omitempty"`
	Endpoints         []EndpointSpec `json:"endpoints,omitempty"`
	Pages             []PageSpec     `json:"pages"`
	ExternalLibraries []LibrarySpec  `json:"external_libraries,omitempty"`
	Exclusions        []string       `json:"exclusions,omitempty"`
	Webhooks          []WebhookSpec  `json:"webhooks,omitempty"`
	Actions           []ActionSpec    `json:"actions,omitempty"`
	Components        []ComponentSpec `json:"components,omitempty"`
	ScheduledTasks    []TaskSpec      `json:"scheduled_tasks,omitempty"`
	Questions         []PlanQuestion  `json:"questions,omitempty"`
}

// LibrarySpec declares an external CDN library the site needs.
type LibrarySpec struct {
	Name    string `json:"name"`              // e.g. "three.js", "gsap", "chart.js"
	URL     string `json:"url"`               // CDN URL for the script/stylesheet
	Purpose string `json:"purpose,omitempty"` // what it's used for in this project
}

// DesignSystem defines concrete design tokens that drive CSS generation.
type DesignSystem struct {
	Colors         map[string]string `json:"colors"`                    // primary, secondary, bg, surface, text, muted, accent, error, success
	ExtendedColors map[string]string `json:"extended_colors,omitempty"` // project-specific tokens beyond the core 9 (e.g., "highlight", "brand-gradient-start")
	Typography     *TypographySpec   `json:"typography,omitempty"`
	DesignIntent   string            `json:"design_intent,omitempty"` // Freeform aesthetic vision (2-3 sentences)
	DarkMode       bool              `json:"dark_mode,omitempty"`
	Components     map[string]string `json:"components,omitempty"` // Reusable HTML structures (e.g., {"card": "<div class='card'>...</div>"})
}

// TypographySpec defines font choices for the design system.
type TypographySpec struct {
	HeadingFont string `json:"heading_font,omitempty"` // e.g. "Inter", "Playfair Display"
	BodyFont    string `json:"body_font,omitempty"`
}

// LayoutSpec defines the site's layout topology.
type LayoutSpec struct {
	Style    string   `json:"style"` // e.g. topnav, sidebar, minimal, split, dashboard, full-bleed — freeform
	NavItems []string `json:"nav_items,omitempty"`
	Header   string   `json:"header,omitempty"` // "full" (default if omitted), "minimal", "none"
	Footer   string   `json:"footer,omitempty"` // "full" (default if omitted), "minimal", "none"
}

type PageSpec struct {
	Path      string        `json:"path"`
	Title     string        `json:"title"`
	Purpose   string        `json:"purpose"`
	Sections  []SectionSpec `json:"sections,omitempty"`
	Endpoints []string      `json:"endpoints,omitempty"` // which endpoints this page uses
	Auth      bool          `json:"auth,omitempty"`      // page requires authentication
	Notes     string        `json:"notes,omitempty"`     // technical build notes
}

// SectionSpec describes a section within a page.
type SectionSpec struct {
	Name       string   `json:"name"`
	Purpose    string   `json:"purpose,omitempty"`
	LayoutHint string   `json:"layout_hint,omitempty"` // e.g. "3-col grid", "full-width", "centered", "alternating rows"
	Endpoints  []string `json:"endpoints,omitempty"`
	DataFlow   string   `json:"data_flow,omitempty"` // e.g. "fetch /api/posts on load, render as card grid"
}

// UnmarshalJSON accepts both a plain string ("hero") and an object ({"name":"hero","purpose":"..."}).
func (ss *SectionSpec) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		ss.Name = s
		return nil
	}
	type alias SectionSpec
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*ss = SectionSpec(a)
	return nil
}

// SectionNames returns section names as a string slice (for backward-compat formatting).
func (p *PageSpec) SectionNames() []string {
	names := make([]string, len(p.Sections))
	for i, s := range p.Sections {
		names[i] = s.Name
	}
	return names
}

type WebhookSpec struct {
	Name      string   `json:"name"`
	Direction string   `json:"direction"`             // "incoming" or "outgoing"
	URL       string   `json:"url,omitempty"`         // outgoing only
	Events    []string `json:"event_types,omitempty"` // events to subscribe to
}

type ActionSpec struct {
	Name         string                 `json:"name"`
	EventType    string                 `json:"event_type"`             // auth.register, data.insert, etc.
	ActionType   string                 `json:"action_type"`            // send_email, http_request, insert_data, update_data
	ActionConfig map[string]interface{} `json:"action_config"`          // JSON config with {{template}} vars
	EventFilter  map[string]interface{} `json:"event_filter,omitempty"` // match against event payload
}

type ComponentSpec struct {
	Name    string `json:"name"`              // component name (alphanumeric, hyphens, underscores)
	Purpose string `json:"purpose,omitempty"` // what this component renders
}

type TaskSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`                     // what the brain should do
	Cron        string `json:"cron,omitempty"`             // "0 8 * * *"
	IntervalSec int    `json:"interval_seconds,omitempty"` // alternative: every N seconds
}

type EndpointSpec struct {
	Action       string   `json:"action"`               // "create_api", "create_auth", "create_websocket", "create_stream", "create_upload", "create_llm"
	Path         string   `json:"path"`                 // "messages", "chat", "uploads"
	TableName    string   `json:"table_name,omitempty"` // which table to bind
	RequiresAuth bool     `json:"requires_auth,omitempty"`
	PublicRead   bool     `json:"public_read,omitempty"`
	Streaming    *bool    `json:"streaming,omitempty"`      // LLM: true = use /chat (SSE), false = use /complete (JSON). Default: true for chatbots, false for generators.
	RoomColumn   string   `json:"room_column,omitempty"`    // WebSocket: column for room scoping
	WriteTable   string   `json:"write_to_table,omitempty"` // WebSocket: table to write messages to
	EventTypes   []string `json:"event_types,omitempty"`    // Stream/WebSocket: event types
	// Auth-specific
	UsernameCol    string   `json:"username_column,omitempty"`
	PasswordCol    string   `json:"password_column,omitempty"`
	DefaultRole    string   `json:"default_role,omitempty"`
	RoleColumn     string   `json:"role_column,omitempty"`
	JWTExpiryHours int      `json:"jwt_expiry_hours,omitempty"`
	PublicColumns  []string `json:"public_columns,omitempty"`
	OwnerColumn    string   `json:"owner_column,omitempty"` // Row-level security: scopes data per user
}

type TableSpec struct {
	Name              string      `json:"name"`
	Purpose           string      `json:"purpose"`
	Columns           []ColumnDef `json:"columns"`
	SeedData          interface{} `json:"seed_data,omitempty"`          // bool or []map — LLM may output either
	SearchableColumns []string    `json:"searchable_columns,omitempty"` // FTS5-indexed columns
}

type ColumnDef struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Primary  bool   `json:"primary,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// PlanPatch describes incremental changes for an UPDATE_PLAN flow.
type PlanPatch struct {
	AddPages           []PageSpec     `json:"add_pages,omitempty"`
	ModifyPages        []PageSpec     `json:"modify_pages,omitempty"`
	RemovePages        []string       `json:"remove_pages,omitempty"`
	AddEndpoints       []EndpointSpec `json:"add_endpoints,omitempty"`
	ModifyEndpoints    []EndpointSpec `json:"modify_endpoints,omitempty"`
	AddTables          []TableSpec    `json:"add_tables,omitempty"`
	ModifyTables       []TableSpec    `json:"modify_tables,omitempty"`
	AddWebhooks        []WebhookSpec   `json:"add_webhooks,omitempty"`
	AddComponents      []ComponentSpec `json:"add_components,omitempty"`
	AddTasks           []TaskSpec      `json:"add_scheduled_tasks,omitempty"`
	UpdateCSS          bool            `json:"update_css"`
	UpdateNav          bool           `json:"update_nav"`
	UpdateAuthStrategy string         `json:"update_auth_strategy,omitempty"`
	UpdateDesignSystem *DesignSystem  `json:"update_design_system,omitempty"`
	UpdateLayout       *LayoutSpec    `json:"update_layout,omitempty"`
}

// ApplyPatch applies an incremental PlanPatch to this Plan. It filters
// endpoints that conflict with exclusions, then mutates pages, endpoints,
// tables, webhooks, tasks, design system, layout, and auth strategy in place.
func (p *Plan) ApplyPatch(patch *PlanPatch) {
	// Filter endpoints against exclusions.
	if len(p.Exclusions) > 0 {
		exclusionSet := strings.Join(p.Exclusions, " ")
		var filtered []EndpointSpec
		for _, ep := range patch.AddEndpoints {
			if ep.Action == "create_auth" && strings.Contains(exclusionSet, "no auth") {
				continue
			}
			filtered = append(filtered, ep)
		}
		patch.AddEndpoints = filtered
	}

	// Remove pages first (before add, so re-add works).
	for _, rm := range patch.RemovePages {
		for i, pg := range p.Pages {
			if pg.Path == rm {
				p.Pages = append(p.Pages[:i], p.Pages[i+1:]...)
				break
			}
		}
	}

	// Modify pages (replace in-place).
	for _, mod := range patch.ModifyPages {
		for i, pg := range p.Pages {
			if pg.Path == mod.Path {
				p.Pages[i] = mod
				break
			}
		}
	}

	// Add pages.
	p.Pages = append(p.Pages, patch.AddPages...)

	// Modify endpoints (replace in-place by path match).
	for _, mod := range patch.ModifyEndpoints {
		found := false
		for i, ep := range p.Endpoints {
			if ep.Path == mod.Path && ep.Action == mod.Action {
				p.Endpoints[i] = mod
				found = true
				break
			}
		}
		if !found {
			// Path matches but action changed, or new — append as addition.
			p.Endpoints = append(p.Endpoints, mod)
		}
	}

	// Modify tables (replace in-place by name match).
	for _, mod := range patch.ModifyTables {
		found := false
		for i, t := range p.Tables {
			if t.Name == mod.Name {
				p.Tables[i] = mod
				found = true
				break
			}
		}
		if !found {
			p.Tables = append(p.Tables, mod)
		}
	}

	// Append new items.
	p.Endpoints = append(p.Endpoints, patch.AddEndpoints...)
	p.Tables = append(p.Tables, patch.AddTables...)
	p.Webhooks = append(p.Webhooks, patch.AddWebhooks...)
	p.Components = append(p.Components, patch.AddComponents...)
	p.ScheduledTasks = append(p.ScheduledTasks, patch.AddTasks...)

	if patch.UpdateAuthStrategy != "" {
		p.AuthStrategy = patch.UpdateAuthStrategy
	}
	if patch.UpdateLayout != nil {
		p.Layout = patch.UpdateLayout
	}
	if patch.UpdateDesignSystem != nil {
		if p.DesignSystem == nil {
			p.DesignSystem = patch.UpdateDesignSystem
		} else {
			for k, v := range patch.UpdateDesignSystem.Colors {
				p.DesignSystem.Colors[k] = v
			}
			if patch.UpdateDesignSystem.Typography != nil {
				p.DesignSystem.Typography = patch.UpdateDesignSystem.Typography
			}
			if patch.UpdateDesignSystem.DesignIntent != "" {
				p.DesignSystem.DesignIntent = patch.UpdateDesignSystem.DesignIntent
			}
			p.DesignSystem.DarkMode = patch.UpdateDesignSystem.DarkMode
			if patch.UpdateDesignSystem.ExtendedColors != nil {
				if p.DesignSystem.ExtendedColors == nil {
					p.DesignSystem.ExtendedColors = make(map[string]string)
				}
				for k, v := range patch.UpdateDesignSystem.ExtendedColors {
					p.DesignSystem.ExtendedColors[k] = v
				}
			}
			if patch.UpdateDesignSystem.Components != nil {
				if p.DesignSystem.Components == nil {
					p.DesignSystem.Components = make(map[string]string)
				}
				for k, v := range patch.UpdateDesignSystem.Components {
					p.DesignSystem.Components[k] = v
				}
			}
		}
	}
}

// GetArchitecture returns the plan's architecture (e.g. "spa", "mpa").
// Implements prompt.PlanContext.
func (p *Plan) GetArchitecture() string { return p.Architecture }

// HasWebSocketEndpoints returns true if the plan contains any WebSocket endpoints.
// Implements prompt.PlanContext.
func (p *Plan) HasWebSocketEndpoints() bool {
	for _, ep := range p.Endpoints {
		if ep.Action == "create_websocket" {
			return true
		}
	}
	return false
}

type PlanQuestion struct {
	Question   string          `json:"question"`
	Type       string          `json:"type"` // "single_choice", "multiple_choice", "open", "secret"
	Options    []string        `json:"options,omitempty"`
	SecretName string          `json:"secret_name,omitempty"` // for type="secret": name under which to store the encrypted value
	Fields     json.RawMessage `json:"fields,omitempty"`      // structured multi-field questions: [{"name":"...", "label":"...", "type":"secret", "secret_name":"..."}]
}

// UnmarshalJSON accepts both a plain string ("question text") and
// an object ({"question":"...","type":"...","options":[...]}) so LLM responses
// that return questions as a string array still parse correctly.
func (pq *PlanQuestion) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		pq.Question = s
		pq.Type = "open"
		return nil
	}
	type alias PlanQuestion
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*pq = PlanQuestion(a)
	// Default type based on whether options are present.
	if pq.Type == "" {
		if len(pq.Options) > 0 {
			pq.Type = "single_choice"
		} else {
			pq.Type = "open"
		}
	}
	return nil
}

// --- Pipeline state ---

// PipelineState is the singleton row in ho_pipeline_state tracking build progress.
type PipelineState struct {
	Stage              PipelineStage
	PlanJSON           string
	ToolCallsCompleted int // BUILD crash-recovery checkpoint
	ErrorCount         int
	LastError          string
	Paused             bool
	PauseReason        string
	UpdateDescription  string // what the owner wants changed (for UPDATE_PLAN)
	StartedAt          time.Time
	UpdatedAt          time.Time
}

// --- JSON parsing ---

var jsonFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*\n?(.*?)```")
var trailingCommaRe = regexp.MustCompile(`,\s*([\]}])`)

// findJSONEnd walks s (which must start with '{' or '[') tracking bracket depth,
// and returns the byte position just past the closing bracket of the top-level
// structure. Returns -1 if the structure is never closed.
func findJSONEnd(s string) int {
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// extractJSON strips markdown code fences and any leading/trailing non-JSON text
// from raw LLM output, returning only the JSON object.
func extractJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 {
		return raw
	}

	// Find ALL code fences — prefer the one that starts with { or [.
	if allMatches := jsonFenceRe.FindAllStringSubmatch(raw, -1); len(allMatches) > 0 {
		for _, m := range allMatches {
			candidate := strings.TrimSpace(m[1])
			if len(candidate) > 0 && (candidate[0] == '{' || candidate[0] == '[') {
				return candidate
			}
		}
		// No fence started with JSON — use the first one anyway.
		return strings.TrimSpace(allMatches[0][1])
	}

	// Already a JSON object or array — trim any trailing garbage after the
	// top-level structure closes (LLMs sometimes append extra brackets).
	if raw[0] == '{' || raw[0] == '[' {
		if end := findJSONEnd(raw); end > 0 && end < len(raw) {
			return raw[:end]
		}
		return raw
	}

	// Fallback: find the first { and last } to extract JSON object.
	if start := strings.Index(raw, "{"); start >= 0 {
		if end := strings.LastIndex(raw, "}"); end > start {
			return raw[start : end+1]
		}
	}

	return raw
}

// repairJSON fixes common LLM JSON mistakes (trailing commas, // comments, truncation).
func repairJSON(s string) string {
	// Strip single-line // comments (only full-line comments to avoid mangling URLs).
	lines := strings.Split(s, "\n")
	cleaned := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	s = strings.Join(cleaned, "\n")

	// Remove trailing commas before } or ].
	s = trailingCommaRe.ReplaceAllString(s, "$1")

	// Repair truncated JSON (from max_tokens cutoff).
	s = repairTruncatedJSON(s)

	return s
}

// countBracketStack walks a JSON string and returns the stack of unclosed
// brackets/braces plus whether the scan ended inside a quoted string.
func countBracketStack(s string) (stack []byte, inString bool) {
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{', '[':
			stack = append(stack, c)
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				stack = stack[:len(stack)-1]
			}
		case ']':
			if len(stack) > 0 && stack[len(stack)-1] == '[' {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return
}

// repairTruncatedJSON closes unclosed brackets and braces from a max_tokens cutoff.
func repairTruncatedJSON(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 0 || (s[0] != '{' && s[0] != '[') {
		return s
	}

	stack, inString := countBracketStack(s)

	if len(stack) == 0 {
		return s // Already balanced.
	}

	// Truncated mid-string: find last complete value boundary.
	if inString {
		lastQuote := strings.LastIndex(s, `"`)
		if lastQuote > 0 {
			s = s[:lastQuote]
			s = strings.TrimRight(s, " \t\n\r")
			// Remove trailing colon (dangling key with no value).
			if len(s) > 0 && s[len(s)-1] == ':' {
				s = s[:len(s)-1]
				s = strings.TrimRight(s, " \t\n\r")
				if len(s) > 0 && s[len(s)-1] == '"' {
					prevQuote := strings.LastIndex(s[:len(s)-1], `"`)
					if prevQuote >= 0 {
						s = s[:prevQuote]
					}
				}
			}
			s = strings.TrimRight(s, " \t\n\r,")
		}
	} else {
		s = strings.TrimRight(s, " \t\n\r,:")
	}

	// Re-count the stack after trimming.
	stack, _ = countBracketStack(s)

	// Close remaining open brackets in reverse order.
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			s += "}"
		} else {
			s += "]"
		}
	}

	return s
}

// unwrapSingleObjectArray handles LLMs wrapping the plan in an array: [{...}] → {...}.
// Also handles wrapper objects like {"plan": {...}} or {"result": {...}}.
func unwrapSingleObjectArray(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}

	// Array wrapping: [{...}]
	if s[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal([]byte(s), &arr) == nil && len(arr) == 1 {
			inner := strings.TrimSpace(string(arr[0]))
			if len(inner) > 0 && inner[0] == '{' {
				return inner
			}
		}
	}

	// Wrapper object: {"plan": {...}, ...} or {"result": {...}, ...}
	if s[0] == '{' {
		var wrapper map[string]json.RawMessage
		if json.Unmarshal([]byte(s), &wrapper) == nil {
			// If the object has a single key containing an object, unwrap it.
			if len(wrapper) == 1 {
				for _, v := range wrapper {
					inner := strings.TrimSpace(string(v))
					if len(inner) > 0 && inner[0] == '{' {
						return inner
					}
					// Single key containing an array with one object: {"plan": [{...}]}
					if len(inner) > 0 && inner[0] == '[' {
						var arr []json.RawMessage
						if json.Unmarshal([]byte(inner), &arr) == nil && len(arr) == 1 {
							elem := strings.TrimSpace(string(arr[0]))
							if len(elem) > 0 && elem[0] == '{' {
								return elem
							}
						}
					}
				}
			}
		}
	}

	return s
}

// ParsePlan parses a Plan from raw LLM output.
func ParsePlan(raw string) (*Plan, error) {
	raw = repairJSON(extractJSON(raw))
	raw = unwrapSingleObjectArray(raw)
	var p Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("invalid plan JSON: %w", err)
	}
	return &p, nil
}

// ParsePlanPatch parses a PlanPatch from raw LLM output.
func ParsePlanPatch(raw string) (*PlanPatch, error) {
	raw = repairJSON(extractJSON(raw))
	raw = unwrapSingleObjectArray(raw)
	var patch PlanPatch
	if err := json.Unmarshal([]byte(raw), &patch); err != nil {
		return nil, fmt.Errorf("invalid plan patch JSON: %w", err)
	}
	return &patch, nil
}

// MarshalJSON serializes any struct to JSON string.
func marshalToJSON(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- Validation ---

// ValidatePlan checks a Plan for structural errors.
func ValidatePlan(p *Plan) []string {
	var errs []string

	if p.AppType == "" {
		errs = append(errs, "app_type is required")
	}

	if len(p.Pages) < 1 {
		errs = append(errs, "plan must include at least 1 page")
	}

	// Build path set for link validation.
	paths := make(map[string]bool, len(p.Pages))
	for _, pg := range p.Pages {
		if paths[pg.Path] {
			errs = append(errs, fmt.Sprintf("duplicate page path: %s", pg.Path))
		}
		paths[pg.Path] = true
	}

	if !paths["/"] {
		errs = append(errs, "plan must include a homepage at path /")
	}

	// Validate design system if present.
	if p.DesignSystem != nil {
		if len(p.DesignSystem.Colors) == 0 {
			errs = append(errs, "design_system.colors is required when design_system is present")
		} else {
			if p.DesignSystem.Colors["primary"] == "" {
				errs = append(errs, "design_system.colors must include 'primary'")
			}
			if p.DesignSystem.Colors["bg"] == "" {
				errs = append(errs, "design_system.colors must include 'bg'")
			}
		}
	}

	// Validate layout.
	if p.Layout == nil {
		errs = append(errs, "layout is required")
	} else {
		validChrome := map[string]bool{"full": true, "minimal": true, "none": true}
		if p.Layout.Header != "" && !validChrome[p.Layout.Header] {
			errs = append(errs, fmt.Sprintf("layout.header %q is not valid (must be full, minimal, or none)", p.Layout.Header))
		}
		if p.Layout.Footer != "" && !validChrome[p.Layout.Footer] {
			errs = append(errs, fmt.Sprintf("layout.footer %q is not valid (must be full, minimal, or none)", p.Layout.Footer))
		}

		// Genre-layout coherence check: warn if app_type suggests chromeless but layout says full.
		if p.AppType != "" {
			defH, defF := inferLayoutDefaults(p.AppType)
			if defH == "none" && p.Layout.Header == "full" {
				slog.Warn("genre-layout mismatch: app_type suggests no header",
					"app_type", p.AppType, "header", p.Layout.Header)
			}
			if defF == "none" && p.Layout.Footer == "full" {
				slog.Warn("genre-layout mismatch: app_type suggests no footer",
					"app_type", p.AppType, "footer", p.Layout.Footer)
			}
		}
	}

	// Validate endpoint specs.
	validActions := map[string]bool{
		"create_api": true, "create_auth": true, "create_websocket": true,
		"create_stream": true, "create_upload": true, "create_llm": true,
	}
	for i, ep := range p.Endpoints {
		if !validActions[ep.Action] {
			errs = append(errs, fmt.Sprintf("endpoints[%d].action %q is not valid", i, ep.Action))
		}
		if ep.Path == "" {
			errs = append(errs, fmt.Sprintf("endpoints[%d].path is required", i))
		}
	}

	// Data table validation.
	for i, t := range p.Tables {
		if t.Name == "" {
			errs = append(errs, fmt.Sprintf("tables[%d].name is required", i))
			continue
		}
		colNames := make(map[string]bool, len(t.Columns))
		nonSystem := 0
		for _, c := range t.Columns {
			if colNames[c.Name] {
				errs = append(errs, fmt.Sprintf("table %s has duplicate column name: %s", t.Name, c.Name))
			}
			colNames[c.Name] = true
			if c.Name != "id" && c.Name != "created_at" && c.Name != "updated_at" {
				nonSystem++
			}
		}
		if nonSystem == 0 && len(t.Columns) > 0 {
			errs = append(errs, fmt.Sprintf("table %s has no non-system columns", t.Name))
		}
	}

	return errs
}

// --- Pipeline state DB operations ---

// LoadPipelineState loads the singleton ho_pipeline_state row.
func LoadPipelineState(d *sql.DB) (*PipelineState, error) {
	var s PipelineState
	var planJSON, lastError, pauseReason, updateDesc sql.NullString
	var startedAt, updatedAt sql.NullString

	err := d.QueryRow(`SELECT stage, plan_json, tool_calls_completed, error_count,
		last_error, paused, pause_reason, COALESCE(update_description, ''), started_at, updated_at
		FROM ho_pipeline_state WHERE id = 1`).Scan(
		&s.Stage, &planJSON, &s.ToolCallsCompleted, &s.ErrorCount,
		&lastError, &s.Paused, &pauseReason, &updateDesc, &startedAt, &updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("load pipeline state: %w", err)
	}

	s.PlanJSON = planJSON.String
	s.LastError = lastError.String
	s.PauseReason = pauseReason.String
	s.UpdateDescription = updateDesc.String
	if startedAt.Valid {
		var err error
		s.StartedAt, err = time.Parse("2006-01-02 15:04:05", startedAt.String)
		if err != nil {
			slog.Warn("plan: malformed started_at timestamp", "value", startedAt.String, "error", err)
		}
	}
	if updatedAt.Valid {
		var err error
		s.UpdatedAt, err = time.Parse("2006-01-02 15:04:05", updatedAt.String)
		if err != nil {
			slog.Warn("plan: malformed updated_at timestamp", "value", updatedAt.String, "error", err)
		}
	}

	return &s, nil
}

// SavePipelineState persists the full pipeline state.
func SavePipelineState(sdb *db.SiteDB, s *PipelineState) error {
	_, err := sdb.ExecWrite(`UPDATE ho_pipeline_state SET
		stage = ?, plan_json = ?, tool_calls_completed = ?,
		error_count = ?, last_error = ?, paused = ?,
		pause_reason = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = 1`,
		s.Stage, s.PlanJSON, s.ToolCallsCompleted,
		s.ErrorCount, s.LastError, s.Paused, s.PauseReason,
	)
	return err
}

// AdvanceStage moves the pipeline to the next stage.
func AdvanceStage(sdb *db.SiteDB, stage PipelineStage) error {
	_, err := sdb.ExecWrite(`UPDATE ho_pipeline_state SET stage = ?, error_count = 0, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, stage)
	return err
}

// ResetPipeline resets pipeline state for a fresh build.
func ResetPipeline(sdb *db.SiteDB) error {
	_, err := sdb.ExecWrite(`UPDATE ho_pipeline_state SET
		stage = 'PLAN', plan_json = NULL, tool_calls_completed = 0,
		error_count = 0, last_error = NULL, paused = 0,
		pause_reason = NULL, started_at = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP
		WHERE id = 1`)
	return err
}

// PausePipeline pauses the pipeline with a reason.
func PausePipeline(sdb *db.SiteDB, reason string) error {
	_, err := sdb.ExecWrite(`UPDATE ho_pipeline_state SET paused = 1, pause_reason = ?,
		updated_at = CURRENT_TIMESTAMP WHERE id = 1`, reason)
	return err
}

// ResumePipeline unpauses the pipeline.
func ResumePipeline(sdb *db.SiteDB) error {
	_, err := sdb.ExecWrite(`UPDATE ho_pipeline_state SET paused = 0, pause_reason = NULL,
		updated_at = CURRENT_TIMESTAMP WHERE id = 1`)
	return err
}

// IncrementErrorCount increments the error counter and records the error.
func IncrementErrorCount(sdb *db.SiteDB, errMsg string) (int, error) {
	_, err := sdb.ExecWrite(`UPDATE ho_pipeline_state SET error_count = error_count + 1,
		last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, errMsg)
	if err != nil {
		return 0, err
	}
	var count int
	err = sdb.QueryRow(`SELECT error_count FROM ho_pipeline_state WHERE id = 1`).Scan(&count)
	return count, err
}

// --- Stage log operations ---

// LogStageStart creates a new ho_stage_log entry and returns its ID.
func LogStageStart(sdb *db.SiteDB, stage PipelineStage) (int64, error) {
	result, err := sdb.ExecWrite(`INSERT INTO ho_stage_log (stage, status) VALUES (?, 'started')`, stage)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// LogStageComplete marks a ho_stage_log entry as completed with metrics.
func LogStageComplete(sdb *db.SiteDB, logID int64, inputTokens, outputTokens, toolCalls int, duration time.Duration) error {
	_, err := sdb.ExecWrite(`UPDATE ho_stage_log SET status = 'completed',
		input_tokens = ?, output_tokens = ?, tool_calls = ?,
		duration_ms = ?, completed_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		inputTokens, outputTokens, toolCalls, duration.Milliseconds(), logID,
	)
	return err
}

// LogStageError marks a ho_stage_log entry as failed.
func LogStageError(sdb *db.SiteDB, logID int64, errMsg string) error {
	_, err := sdb.ExecWrite(`UPDATE ho_stage_log SET status = 'failed',
		error_message = ?, completed_at = CURRENT_TIMESTAMP
		WHERE id = ?`, errMsg, logID)
	return err
}
