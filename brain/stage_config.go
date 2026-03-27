/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/tools"
)

// Stage Tool Matrix (quick reference)
//
// Stage           | Callable Tools   | Guide Shows     | Mismatch?
// --------------- | ---------------- | --------------- | ---------
// EXPAND          | none             | none            | N/A: pure text expansion
// PLAN            | planToolSet(1)   | fullWrite(23)   | Yes: planner sees all capabilities
// BUILD           | dynamic(9-23)    | dynamic         | No: guide matches callable tools
// VALIDATE        | validate(8)      | validate(8)     | No
// COMPLETE        | none             | none            | N/A
// UPDATE_PLAN     | none             | fullWrite(23)   | Yes: planner needs capability awareness
// MONITORING      | monitoring(4)    | none(hardcoded) | N/A
// CHAT_WAKE       | chatWake(21)     | chatWake(21)    | No
// CHAT_WAKE_LITE  | wakeLite(6)      | wakeLite(6)     | No (one-liner guide)
// SCHEDULED_TASK  | fullWrite(23)    | fullWrite(23)   | No
// CHAT            | fullWrite(23)    | fullWrite(23)   | No (lives in chat package)
//
// Prompt Section Matrix — shows which context each stage's prompt includes.
// [auto] = injected by writeContextSections, [*] = custom logic in prompt builder.
//
// Section              | PLAN | BUILD | VALIDATE | UPDATE | MONITOR | WAKE | WKLITE | SCHED | CHAT
// -------------------- | ---- | ----- | -------- | ------ | ------- | ---- | ------ | ----- | ------
// Header               |  *   |   *   |          |   *    |    *    |   *  |   *    |   *   |   *
// SiteManifest         |      |       |    *     |   *    |  auto   | auto |  auto  |  auto |
// DesignTokens         |      |   *   |  auto    |        |         | auto |        |   *   |   *
// CSSReference         |      |       |  auto    |        |         | auto |  auto  |       |   *
// JSReference          |      |       |  auto    |        |         | auto |  auto  |       |   *
// Memories             |      |       |          |        |  auto   | auto |  auto  |       |   *
// PlatformContracts    |  *   |   *   |          |   *    |         |      |        |   *   |
// DataLayer            |      |       |          |        |         | auto |        |  auto |   *
// Analytics            |      |       |          |        |  auto   |      |        |       |
// RecentErrors         |      |       |          |        |  auto   |      |        |       |
// ? = conditionally included based on task content

// PromptSection identifies a context block that can be injected into prompts.
type PromptSection int

const (
	SectionHeader            PromptSection = iota // "You are HO..." + owner + date
	SectionSiteInfo                               // Site name, description, plan summary
	SectionSiteManifest                           // What's built (pages, endpoints, tables, files)
	SectionDesignTokens                           // Design system CSS custom properties
	SectionCSSReference                           // Compact CSS class reference
	SectionJSReference                            // Compact JS API reference
	SectionMemories                               // Persistent memories from past sessions
	SectionPlatformContracts                      // Tool guide + API shapes, endpoint contracts
	SectionDataLayer                              // API endpoints, WebSocket, SSE, uploads
	SectionAnalytics                              // Last 7 days analytics
	SectionRecentErrors                           // Recent error log
	sectionCount                                  // sentinel for iteration in tests
)

// GuideMode controls how tool documentation appears in system prompts.
type GuideMode int

const (
	GuideModeNone GuideMode = iota // No tool docs in prompt (COMPLETE)
	GuideModePlan                  // One-liner descriptions only (PLAN, UPDATE_PLAN)
	GuideModeFull                  // Full behavioral Guide() text (BUILD, VALIDATE, etc.)
)

// StageConfig bundles the tool set, guide configuration, and LLM parameters
// for a pipeline stage. This is the single place to check when adding or
// modifying tools — every stage's configuration is declared here.
type StageConfig struct {
	// ToolSet is the set of tool names the LLM can call at this stage.
	// nil means no tools (e.g., COMPLETE, UPDATE_PLAN).
	ToolSet map[string]bool

	// GuideToolSet is the tool set used when generating prompt documentation.
	// When nil, defaults to ToolSet. When explicitly set, allows the guide to
	// reference a broader set than what is actually callable (e.g., PLAN stage
	// shows fullWriteToolSet capabilities for planning, but only planToolSet
	// is callable). A non-nil GuideToolSet that differs from ToolSet must have
	// GuideReason set.
	GuideToolSet map[string]bool

	// GuideMode controls the style of tool documentation injected into prompts.
	GuideMode GuideMode

	// GuideReason documents WHY GuideToolSet differs from ToolSet, if it does.
	// Required when GuideToolSet is non-nil and differs from ToolSet.
	GuideReason string

	// Dynamic means ToolSet is computed at runtime (e.g., BUILD uses
	// buildToolSetForPlan). The static ToolSet field serves as a baseline
	// for validation but is overridden at runtime via the override parameter.
	Dynamic bool

	// MaxIterations is the default max tool-loop iterations for this stage.
	MaxIterations int

	// MaxTokens is the default max output tokens per LLM call.
	MaxTokens int

	// Temperature is the default temperature for this stage.
	Temperature float64

	// Sections declares which context blocks this stage's prompt includes.
	// Used by writeContextSections to inject standard context. Every section
	// a stage needs should be listed here — this is the single source of truth.
	Sections []PromptSection

	// HeaderRole is the role description for the SectionHeader writer.
	// Example: "You are HO, monitoring a live website. Be brief and only act if needed."
	HeaderRole string

	// WebSearch enables provider-native web search (Anthropic web_search,
	// OpenAI web_search_options). When true, a synthetic web_search ToolDef
	// is appended to the tool definitions sent to the LLM. The provider
	// handles search internally — no client-side tool execution needed.
	WebSearch bool
}

// ---------------------------------------------------------------------------
// Tool set definitions — all derived from tools.ChatToolSet (single source of truth).
// ---------------------------------------------------------------------------

var (
	fullWriteToolSet = tools.ChatToolSet

	buildToolSet    = toolSetExcept(tools.ChatToolSet, "manage_diagnostics", "manage_analytics", "manage_site", "manage_memory")
	chatWakeToolSet     = toolSetExcept(tools.ChatToolSet, "manage_diagnostics", "manage_analytics")
	chatWakeLiteToolSet = map[string]bool{
		"manage_pages":         true,
		"manage_files":         true,
		"manage_layout":        true,
		"manage_components":    true,
		"manage_communication": true,
		"manage_memory":        true,
	}

	monitoringTools = map[string]bool{
		"manage_diagnostics":   true,
		"manage_analytics":     true,
		"manage_communication": true,
		"manage_memory":        true,
	}

	planToolSet = map[string]bool{
		"make_http_request": true, // check CDN URLs, fetch external API schemas
	}

	// validateToolSet is intentionally narrow — only tools needed to fix
	// missing items after BUILD. Excludes destructive/broad tools to prevent
	// the LLM from rewriting things that already work.
	validateToolSet = map[string]bool{
		"manage_pages":      true,
		"manage_files":      true,
		"manage_layout":     true,
		"manage_schema":     true,
		"manage_endpoints":  true,
		"manage_data":       true,
		"manage_search":     true,
		"manage_seo":        true,
		"manage_testing":    true,
		"manage_components": true, // allow fixing component inconsistencies during validation
	}
)

func toolSetExcept(base map[string]bool, exclude ...string) map[string]bool {
	ex := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		ex[e] = true
	}
	m := make(map[string]bool, len(base))
	for n := range base {
		if !ex[n] {
			m[n] = true
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// Stage configurations
// ---------------------------------------------------------------------------

// StageConfigs maps each pipeline stage to its configuration.
var StageConfigs = map[PipelineStage]StageConfig{
	StageExpand: {
		ToolSet:       nil, // no tools needed — pure text generation
		GuideMode:     GuideModeNone,
		MaxIterations: 1,
		MaxTokens:     1024,
		Temperature:   0.7,
		Sections:      []PromptSection{SectionHeader},
		HeaderRole:    "You are a requirements analyst. Your job is to take a brief project description and expand it into a clear, detailed specification that a planner can use to design the project.",
	},
	StagePlan: {
		ToolSet:       planToolSet,
		GuideToolSet:  fullWriteToolSet,
		GuideMode:     GuideModePlan,
		GuideReason:   "PLAN needs to see all platform capabilities to design the build, but only has research tools for calling",
		MaxIterations: 5,
		MaxTokens:     16384,
		Temperature:   0.7,
		Sections:      []PromptSection{SectionHeader, SectionSiteInfo, SectionPlatformContracts},
		HeaderRole:    "You are HO, an AI that plans and designs sites and applications. Your job is to understand what the user wants, map it to platform capabilities, and produce a complete build plan. Respond with ONLY a raw JSON object — no markdown code fences, no explanation text, no ```json blocks.",
		WebSearch:     true,
	},
	StageBuild: {
		ToolSet:       buildToolSet, // baseline; overridden by buildToolSetForPlan at runtime
		Dynamic:       true,
		GuideMode:     GuideModeFull,
		MaxIterations: 50, // actual is computed dynamically from plan complexity
		MaxTokens:     16384,
		Temperature:   0.4,
	},
	StageValidate: {
		ToolSet:       validateToolSet,
		GuideMode:     GuideModeFull,
		MaxIterations: 4, // actual is computed from issue count
		MaxTokens:     4096,
		Temperature:   0.2,
		Sections:      []PromptSection{SectionSiteManifest, SectionDesignTokens, SectionCSSReference, SectionJSReference},
		HeaderRole:    "You are HO. The build is done but validation found a few missing pieces. Your job is SURGICAL: fix ONLY the listed issues with minimal changes. Do NOT rewrite, restructure, or touch anything that already works.",
	},
	StageComplete: {
		GuideMode: GuideModeNone,
	},
	StageUpdatePlan: {
		ToolSet:       nil,
		GuideToolSet:  fullWriteToolSet,
		GuideMode:     GuideModePlan,
		GuideReason:   "UPDATE_PLAN needs to know all platform capabilities to plan changes, but does not call tools itself",
		MaxIterations: 1,
		MaxTokens:     4096,
		Temperature:   0.5,
		Sections:      []PromptSection{SectionHeader, SectionSiteInfo, SectionPlatformContracts},
		HeaderRole:    "You are HO, planning incremental changes to an existing site. Respond with ONLY a JSON PlanPatch object.",
	},
}

// Non-pipeline stage configs (used outside the linear pipeline).
var (
	MonitoringConfig = StageConfig{
		ToolSet:       monitoringTools,
		GuideMode:     GuideModeNone,
		MaxIterations: 5,
		MaxTokens:     2048,
		Temperature:   0.3,
		Sections:      []PromptSection{SectionHeader, SectionSiteInfo, SectionAnalytics, SectionRecentErrors, SectionSiteManifest, SectionMemories},
		HeaderRole:    "You are HO, monitoring a live website. Be brief and only act if needed.",
	}
	ChatWakeConfig = StageConfig{
		ToolSet:       chatWakeToolSet,
		GuideMode:     GuideModeFull,
		MaxIterations: 15,
		MaxTokens:     8192,
		Temperature:   0.5,
		Sections:      []PromptSection{SectionHeader, SectionSiteInfo, SectionSiteManifest, SectionDesignTokens, SectionCSSReference, SectionJSReference, SectionDataLayer, SectionMemories},
		HeaderRole:    "You are HO, responding to the site owner's message. The site is live and in monitoring mode.",
	}
	ChatWakeLiteConfig = StageConfig{
		ToolSet:       chatWakeLiteToolSet,
		GuideMode:     GuideModePlan,
		MaxIterations: 8,
		MaxTokens:     4096,
		Temperature:   0.4,
		Sections:      []PromptSection{SectionHeader, SectionSiteInfo, SectionSiteManifest, SectionCSSReference, SectionJSReference, SectionMemories},
		HeaderRole:    "You are HO, responding to the site owner's message with frontend-only tools — you can edit pages, files, layout, and components, but NOT backend features (endpoints, database, payments). If the request needs backend changes, tell the owner via manage_communication.",
	}
	ScheduledTaskConfig = StageConfig{
		ToolSet:       fullWriteToolSet,
		GuideMode:     GuideModeFull,
		MaxIterations: 20,
		MaxTokens:     4096,
		Temperature:   0.4,
		Sections:      []PromptSection{SectionHeader, SectionSiteInfo, SectionSiteManifest, SectionDataLayer, SectionPlatformContracts},
		HeaderRole:    "You are HO, executing a scheduled task. Use the available tools to complete the task described in the user message.",
		WebSearch:     true,
	}
)

// ---------------------------------------------------------------------------
// Helper methods — replace manual registry calls in stage runners
// ---------------------------------------------------------------------------

// webSearchToolDef is the synthetic ToolDef that enables provider-native web
// search. Both Anthropic (web_search_20250305) and OpenAI (web_search_options)
// recognize Type:"web_search" and handle it server-side.
var webSearchToolDef = llm.ToolDef{
	Name: "web_search",
	Type: "web_search",
}

// BuildToolDefs returns filtered LLM tool definitions for this stage.
// Pass a non-nil override for dynamic stages (e.g., BUILD).
// When WebSearch is enabled, appends the provider-native web search tool.
func (sc *StageConfig) BuildToolDefs(reg *tools.Registry, override map[string]bool) []llm.ToolDef {
	ts := override
	if ts == nil {
		ts = sc.ToolSet
	}
	var defs []llm.ToolDef
	if ts != nil {
		defs = reg.ToLLMToolsFiltered(ts)
	}
	if sc.WebSearch {
		defs = append(defs, webSearchToolDef)
	}
	return defs
}

// BuildGuide returns the appropriate tool guide string for this stage.
// Pass a non-nil override for dynamic stages (e.g., BUILD).
func (sc *StageConfig) BuildGuide(reg *tools.Registry, override map[string]bool) string {
	guideTS := override
	if guideTS == nil {
		guideTS = sc.GuideToolSet
	}
	if guideTS == nil {
		guideTS = sc.ToolSet
	}
	if guideTS == nil {
		return ""
	}
	switch sc.GuideMode {
	case GuideModePlan:
		return reg.BuildPlanGuide(guideTS)
	case GuideModeFull:
		return reg.BuildGuide(guideTS)
	default:
		return ""
	}
}
