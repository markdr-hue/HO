/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// lineEdit represents a single line-based edit operation.
type lineEdit struct {
	Line    int    `json:"line,omitempty"`    // replace a single line (1-indexed)
	From    int    `json:"from,omitempty"`    // replace range start (1-indexed, inclusive)
	To      int    `json:"to,omitempty"`      // replace range end (1-indexed, inclusive)
	After   int    `json:"after,omitempty"`   // insert after this line (1-indexed)
	Content string `json:"content,omitempty"` // new content (for line/from-to/after)
	Delete  []int  `json:"delete,omitempty"`  // line numbers to delete (1-indexed)
}

// regexReplacement represents a single regex replacement operation.
type regexReplacement struct {
	Pattern string `json:"pattern"`
	Replace string `json:"replace"`
	Count   int    `json:"count"` // 0 = replace all, >0 = replace first N
}

// applyLineEdits applies line-number-based edits to content.
// Returns the modified content, number of edits applied, and any error.
func applyLineEdits(content string, edits []lineEdit) (string, int, error) {
	lines := strings.Split(content, "\n")
	applied := 0

	// Process deletes first (collect all lines to delete).
	deleteSet := map[int]bool{}
	for _, e := range edits {
		for _, d := range e.Delete {
			if d < 1 || d > len(lines) {
				return "", 0, fmt.Errorf("delete line %d out of range (1-%d)", d, len(lines))
			}
			deleteSet[d] = true
		}
	}

	// Process replacements and insertions (sort by line number descending
	// so insertions don't shift subsequent line numbers).
	type indexedEdit struct {
		index int
		edit  lineEdit
	}
	var sortedEdits []indexedEdit
	for i, e := range edits {
		if e.Line > 0 || e.From > 0 || e.After > 0 {
			sortedEdits = append(sortedEdits, indexedEdit{i, e})
		}
		if len(e.Delete) > 0 {
			applied++
		}
	}

	// Sort descending by effective line number so later edits don't shift earlier ones.
	sort.Slice(sortedEdits, func(i, j int) bool {
		li := sortedEdits[i].edit.Line
		if li == 0 {
			li = sortedEdits[i].edit.From
		}
		if li == 0 {
			li = sortedEdits[i].edit.After
		}
		lj := sortedEdits[j].edit.Line
		if lj == 0 {
			lj = sortedEdits[j].edit.From
		}
		if lj == 0 {
			lj = sortedEdits[j].edit.After
		}
		return li > lj
	})

	for _, se := range sortedEdits {
		e := se.edit

		if e.Line > 0 {
			// Replace a single line.
			if e.Line < 1 || e.Line > len(lines) {
				return "", 0, fmt.Errorf("line %d out of range (1-%d)", e.Line, len(lines))
			}
			lines[e.Line-1] = e.Content
			applied++
		} else if e.From > 0 {
			// Replace a range of lines.
			to := e.To
			if to == 0 {
				to = e.From
			}
			if e.From < 1 || to > len(lines) || e.From > to {
				return "", 0, fmt.Errorf("range %d-%d out of range (1-%d)", e.From, to, len(lines))
			}
			newLines := strings.Split(e.Content, "\n")
			result := make([]string, 0, len(lines)-((to-e.From)+1)+len(newLines))
			result = append(result, lines[:e.From-1]...)
			result = append(result, newLines...)
			result = append(result, lines[to:]...)
			lines = result
			applied++
		} else if e.After > 0 {
			// Insert after a line.
			if e.After < 0 || e.After > len(lines) {
				return "", 0, fmt.Errorf("after line %d out of range (0-%d)", e.After, len(lines))
			}
			newLines := strings.Split(e.Content, "\n")
			result := make([]string, 0, len(lines)+len(newLines))
			result = append(result, lines[:e.After]...)
			result = append(result, newLines...)
			result = append(result, lines[e.After:]...)
			lines = result
			applied++
		}
	}

	// Apply deletes (after replacements, working from highest to lowest).
	if len(deleteSet) > 0 {
		sorted := make([]int, 0, len(deleteSet))
		for d := range deleteSet {
			sorted = append(sorted, d)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(sorted)))
		for _, d := range sorted {
			if d >= 1 && d <= len(lines) {
				lines = append(lines[:d-1], lines[d:]...)
			}
		}
	}

	return strings.Join(lines, "\n"), applied, nil
}

// applyRegexReplacements applies regex-based replacements to content.
// Returns the modified content, number of replacements applied, and any error.
func applyRegexReplacements(content string, replacements []regexReplacement) (string, int, error) {
	modified := content
	totalApplied := 0

	for _, r := range replacements {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return "", 0, fmt.Errorf("invalid regex pattern %q: %w", r.Pattern, err)
		}

		if r.Count <= 0 {
			// Replace all.
			before := modified
			modified = re.ReplaceAllString(modified, r.Replace)
			if modified != before {
				totalApplied++
			}
		} else {
			// Replace first N occurrences.
			count := 0
			modified = re.ReplaceAllStringFunc(modified, func(match string) string {
				if count >= r.Count {
					return match
				}
				count++
				return re.ReplaceAllString(match, r.Replace)
			})
			if count > 0 {
				totalApplied++
			}
		}
	}

	return modified, totalApplied, nil
}

// searchReplacePatch represents a single search/replace edit operation.
// Used by pages, layout, and storage patch actions.
type searchReplacePatch struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
	Count   int    `json:"count,omitempty"` // 0 = first match only (default), -1 = all matches
	Near    string `json:"near,omitempty"`  // context hint to disambiguate which occurrence
}

// patchResult holds the outcome of applying search/replace patches.
type patchResult struct {
	Applied  []string // short labels of applied patches
	NotFound []string // search strings that weren't found
	Warnings []string // e.g. "matched 3 times, replaced first"
}

// applyPatches applies search/replace patches to content.
// By default replaces only the FIRST occurrence to prevent accidental duplication.
// Set count=-1 for replace-all, or count=N for first N occurrences.
func applyPatches(content string, patches []searchReplacePatch) (string, patchResult) {
	modified := content
	var result patchResult

	for _, p := range patches {
		if p.Search == "" {
			continue
		}

		occurrences := strings.Count(modified, p.Search)
		if occurrences == 0 {
			result.NotFound = append(result.NotFound, p.Search)
			continue
		}

		// Determine which occurrence to replace when near-context is provided.
		if p.Near != "" && occurrences > 1 {
			modified = replaceNear(modified, p.Search, p.Replace, p.Near)
		} else {
			count := 1 // default: first match only
			if p.Count == -1 {
				count = -1 // all
			} else if p.Count > 0 {
				count = p.Count
			}
			modified = strings.Replace(modified, p.Search, p.Replace, count)
		}

		label := p.Search
		if len(label) > 60 {
			label = label[:60] + "..."
		}
		result.Applied = append(result.Applied, label)

		if occurrences > 1 && p.Count != -1 && p.Near == "" {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%q matched %d times — replaced first only. Use count:-1 for all, or add near:\"context\" to target a specific one.", label, occurrences))
		}
	}

	return modified, result
}

// replaceNear replaces the occurrence of search that is closest to the near-context string.
// This helps the LLM target a specific instance when the same pattern appears multiple times.
func replaceNear(content, search, replace, near string) string {
	nearIdx := strings.Index(content, near)
	if nearIdx == -1 {
		// Near context not found — fall back to first occurrence.
		return strings.Replace(content, search, replace, 1)
	}
	nearCenter := nearIdx + len(near)/2

	// Find all occurrences and pick the closest to nearCenter.
	bestIdx := -1
	bestDist := len(content)
	offset := 0
	for {
		idx := strings.Index(content[offset:], search)
		if idx == -1 {
			break
		}
		absIdx := offset + idx
		matchCenter := absIdx + len(search)/2
		dist := matchCenter - nearCenter
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist = dist
			bestIdx = absIdx
		}
		offset = absIdx + 1
	}

	if bestIdx == -1 {
		return content
	}

	return content[:bestIdx] + replace + content[bestIdx+len(search):]
}
