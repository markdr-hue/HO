/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// summaryResult is used by Summarize implementations to parse tool results.
type summaryResult struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// parseSummaryResult parses a JSON tool result string into its components.
// Returns the parsed data map (or array), the error message, and whether parsing succeeded.
func parseSummaryResult(result string) (r summaryResult, dataMap map[string]interface{}, dataArr []interface{}, ok bool) {
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		return r, nil, nil, false
	}
	if !r.Success {
		return r, nil, nil, true
	}
	if arr, isArr := r.Data.([]interface{}); isArr {
		return r, nil, arr, true
	}
	if m, isMap := r.Data.(map[string]interface{}); isMap {
		return r, m, nil, true
	}
	return r, nil, nil, true
}

// summarizeError returns a standard error summary.
func summarizeError(errMsg string) string {
	if len(errMsg) > 150 {
		errMsg = errMsg[:150]
	}
	return fmt.Sprintf(`{"success":false,"error":"%s"}`, errMsg)
}

// summarizeTruncate truncates a result string to the given length.
func summarizeTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// GenericSummarize provides a default summarization for tools that don't
// implement the Summarizer interface.
func GenericSummarize(result string) string {
	r, _, dataArr, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if dataArr != nil {
		return fmt.Sprintf(`{"success":true,"summary":"Returned %d items"}`, len(dataArr))
	}
	return summarizeTruncate(result, 300)
}

// pageStructureFingerprint extracts a brief description of a page's HTML
// structure and asset references for use in summaries.
func pageStructureFingerprint(content string) string {
	lower := strings.ToLower(content)
	var elements []string
	for _, tag := range []string{"nav", "header", "main", "section", "article", "aside", "footer"} {
		if strings.Contains(lower, "<"+tag) {
			elements = append(elements, tag)
		}
	}
	var assets []string
	idx := 0
	for {
		pos := strings.Index(lower[idx:], "/assets/")
		if pos == -1 {
			break
		}
		start := idx + pos + len("/assets/")
		end := start
		for end < len(lower) && lower[end] != '"' && lower[end] != '\'' && lower[end] != ')' && lower[end] != ' ' && lower[end] != '>' {
			end++
		}
		if end > start {
			asset := content[start:end]
			found := false
			for _, a := range assets {
				if a == asset {
					found = true
					break
				}
			}
			if !found {
				assets = append(assets, asset)
			}
		}
		idx = end
	}
	parts := ""
	if len(elements) > 0 {
		parts += "Structure: " + strings.Join(elements, ",")
	}
	if len(assets) > 0 {
		if parts != "" {
			parts += ". "
		}
		parts += "Assets: " + strings.Join(assets, ",")
	}
	return parts
}
