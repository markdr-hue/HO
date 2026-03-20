package brain

import (
	"testing"

	"github.com/markdr-hue/HO/tools"
)

// TestStageConfigConsistency verifies that tool sets across all stage configs
// are consistent with ChatToolSet (the single source of truth). This test
// catches common mistakes when adding or removing tools:
//   - A tool added to ChatToolSet but not referenced by any stage
//   - A tool referenced by a stage but missing from ChatToolSet (typo/phantom)
//   - An intentional guide/tool mismatch without documentation
func TestStageConfigConsistency(t *testing.T) {
	allReferencedTools := make(map[string]bool)

	configs := []struct {
		name string
		cfg  StageConfig
	}{
		{"PLAN", StageConfigs[StagePlan]},
		{"BUILD", StageConfigs[StageBuild]},
		{"VALIDATE", StageConfigs[StageValidate]},
		{"COMPLETE", StageConfigs[StageComplete]},
		{"UPDATE_PLAN", StageConfigs[StageUpdatePlan]},
		{"MONITORING", MonitoringConfig},
		{"CHAT_WAKE", ChatWakeConfig},
		{"SCHEDULED_TASK", ScheduledTaskConfig},
	}

	for _, c := range configs {
		// Collect all tools referenced by this config.
		for tool := range c.cfg.ToolSet {
			allReferencedTools[tool] = true
		}
		for tool := range c.cfg.GuideToolSet {
			allReferencedTools[tool] = true
		}

		// Verify: if GuideToolSet differs from ToolSet, GuideReason must be set.
		if c.cfg.GuideToolSet != nil && !mapsEqual(c.cfg.GuideToolSet, c.cfg.ToolSet) {
			if c.cfg.GuideReason == "" {
				t.Errorf("%s: GuideToolSet differs from ToolSet but GuideReason is empty — document the intentional mismatch", c.name)
			}
		}

		// Verify: all tools in ToolSet exist in ChatToolSet.
		for tool := range c.cfg.ToolSet {
			if !tools.ChatToolSet[tool] {
				t.Errorf("%s: tool %q in ToolSet but not in ChatToolSet (typo or phantom tool?)", c.name, tool)
			}
		}

		// Verify: all tools in GuideToolSet exist in ChatToolSet.
		for tool := range c.cfg.GuideToolSet {
			if !tools.ChatToolSet[tool] {
				t.Errorf("%s: tool %q in GuideToolSet but not in ChatToolSet (typo or phantom tool?)", c.name, tool)
			}
		}
	}

	// Every tool in ChatToolSet should be referenced by at least one stage config.
	for tool := range tools.ChatToolSet {
		if !allReferencedTools[tool] {
			t.Errorf("tool %q is in ChatToolSet but not referenced by any StageConfig — add it to the appropriate stage(s)", tool)
		}
	}
}

// TestPromptSectionCoverage verifies that every defined PromptSection is used
// by at least one stage config. This catches orphaned sections — if you define
// a new SectionFoo but forget to add it to any stage's Sections list, this test
// will fail and remind you.
func TestPromptSectionCoverage(t *testing.T) {
	usedSections := make(map[PromptSection]bool)

	configs := []struct {
		name string
		cfg  StageConfig
	}{
		{"PLAN", StageConfigs[StagePlan]},
		{"BUILD", StageConfigs[StageBuild]},
		{"VALIDATE", StageConfigs[StageValidate]},
		{"COMPLETE", StageConfigs[StageComplete]},
		{"UPDATE_PLAN", StageConfigs[StageUpdatePlan]},
		{"MONITORING", MonitoringConfig},
		{"CHAT_WAKE", ChatWakeConfig},
		{"SCHEDULED_TASK", ScheduledTaskConfig},
	}

	for _, c := range configs {
		for _, sec := range c.cfg.Sections {
			usedSections[sec] = true
		}
	}

	// SectionHeader and SectionPlatformContracts are handled manually by
	// writePromptHeader and custom logic, not by writeContextSections.
	// They don't appear in Sections lists, so skip them.
	manualSections := map[PromptSection]bool{
		SectionHeader:           true,
		SectionPlatformContracts: true,
	}

	for sec := PromptSection(0); sec < sectionCount; sec++ {
		if manualSections[sec] {
			continue
		}
		if !usedSections[sec] {
			t.Errorf("PromptSection %d is defined but not used by any StageConfig.Sections — add it to the appropriate stage(s) or remove it", sec)
		}
	}
}

func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
