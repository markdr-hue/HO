/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package prompt

import (
	"regexp"
	"sort"
	"strings"
)

// ExtractCSSReference scans CSS source for class selectors and returns a
// compact reference of class names with their top properties.
func ExtractCSSReference(css string) string {
	var entries []string
	seen := map[string]bool{}

	for i := 0; i < len(css); i++ {
		if css[i] != '.' {
			continue
		}
		// Extract class name.
		j := i + 1
		for j < len(css) && (css[j] == '-' || css[j] == '_' || (css[j] >= 'a' && css[j] <= 'z') || (css[j] >= 'A' && css[j] <= 'Z') || (css[j] >= '0' && css[j] <= '9')) {
			j++
		}
		if j <= i+1 {
			continue
		}
		cls := css[i:j]
		if seen[cls] {
			i = j
			continue
		}
		seen[cls] = true

		// Find the opening brace for this selector's rule block.
		bracePos := strings.IndexByte(css[j:], '{')
		if bracePos == -1 {
			i = j
			continue
		}
		blockStart := j + bracePos + 1

		// Find matching closing brace (handle nesting).
		depth := 1
		pos := blockStart
		for pos < len(css) && depth > 0 {
			switch css[pos] {
			case '{':
				depth++
			case '}':
				depth--
			}
			pos++
		}
		if depth != 0 {
			i = j
			continue
		}
		block := css[blockStart : pos-1]

		// Extract top 4 property names from the block.
		props := extractTopProperties(block, 4)
		if len(props) > 0 {
			entries = append(entries, cls+" { "+strings.Join(props, ", ")+" }")
		} else {
			entries = append(entries, cls)
		}
		i = pos
	}

	if len(entries) == 0 {
		return ""
	}
	result := strings.Join(entries, "\n")
	if len(result) > 6000 {
		result = result[:6000] + "\n..."
	}
	return result
}

// extractTopProperties extracts up to n key CSS property-value summaries from a rule block.
func extractTopProperties(block string, n int) []string {
	var props []string
	for _, line := range strings.Split(block, ";") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "/*") {
			continue
		}
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx == -1 {
			continue
		}
		prop := strings.TrimSpace(line[:colonIdx])
		val := strings.TrimSpace(line[colonIdx+1:])
		if len(val) > 50 {
			val = val[:50] + "..."
		}
		props = append(props, prop+": "+val)
		if len(props) >= n {
			break
		}
	}
	return props
}

// ExtractComponentGroups groups CSS classes by common prefix into component
// families. For example, .card, .card-header, .card-body become the "card"
// group. This helps the LLM use components consistently across pages.
func ExtractComponentGroups(css string) string {
	// Collect all class names.
	seen := map[string]bool{}
	var allClasses []string
	for i := 0; i < len(css); i++ {
		if css[i] != '.' {
			continue
		}
		j := i + 1
		for j < len(css) && (css[j] == '-' || css[j] == '_' || (css[j] >= 'a' && css[j] <= 'z') || (css[j] >= 'A' && css[j] <= 'Z') || (css[j] >= '0' && css[j] <= '9')) {
			j++
		}
		if j <= i+1 {
			continue
		}
		cls := css[i+1 : j] // without the leading dot
		if !seen[cls] {
			seen[cls] = true
			allClasses = append(allClasses, cls)
		}
		i = j
	}

	// Group by first segment (before first - or _).
	groups := map[string][]string{}
	for _, cls := range allClasses {
		prefix := cls
		if idx := strings.IndexAny(cls, "-_"); idx > 0 {
			prefix = cls[:idx]
		}
		groups[prefix] = append(groups[prefix], "."+cls)
	}

	// Only keep groups with 2+ related classes (single classes aren't component families).
	var entries []string
	for prefix, classes := range groups {
		if len(classes) < 2 {
			continue
		}
		// Skip utility-like prefixes.
		switch prefix {
		case "is", "has", "no", "mt", "mb", "ml", "mr", "mx", "my", "pt", "pb", "pl", "pr", "px", "py", "text", "bg", "flex", "grid", "gap", "w", "h", "min", "max":
			continue
		}
		entries = append(entries, prefix+": "+strings.Join(classes, ", "))
	}

	if len(entries) == 0 {
		return ""
	}
	// Sort for deterministic output.
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

// ExtractPageStructure extracts a compact structural skeleton from page HTML,
// showing the top-level section/div hierarchy with class names. This serves
// as a design pattern reference for subsequent pages.
func ExtractPageStructure(html string) string {
	var parts []string
	// Simple scanner: find top-level <section> and major <div> elements with classes.
	// We don't need a full HTML parser — just the structural bones.
	lower := strings.ToLower(html)
	tags := []string{"section", "article", "aside", "main", "div", "form", "nav"}

	for _, tag := range tags {
		searchStart := 0
		for {
			openTag := "<" + tag
			idx := strings.Index(lower[searchStart:], openTag)
			if idx == -1 {
				break
			}
			pos := searchStart + idx
			// Find the end of the opening tag.
			tagEnd := strings.IndexByte(html[pos:], '>')
			if tagEnd == -1 {
				break
			}
			tagContent := html[pos : pos+tagEnd+1]

			// Extract class attribute.
			classIdx := strings.Index(strings.ToLower(tagContent), "class=\"")
			if classIdx == -1 {
				searchStart = pos + tagEnd + 1
				continue
			}
			classStart := classIdx + 7
			classEnd := strings.IndexByte(tagContent[classStart:], '"')
			if classEnd == -1 {
				searchStart = pos + tagEnd + 1
				continue
			}
			classes := tagContent[classStart : classStart+classEnd]
			if classes != "" {
				entry := "<" + tag + " class=\"" + classes + "\">"
				// Deduplicate.
				found := false
				for _, p := range parts {
					if p == entry {
						found = true
						break
					}
				}
				if !found {
					parts = append(parts, entry)
				}
			}
			searchStart = pos + tagEnd + 1
		}
	}

	if len(parts) == 0 {
		return ""
	}
	// Cap at 15 entries to keep it compact.
	if len(parts) > 15 {
		parts = parts[:15]
	}
	return strings.Join(parts, "\n")
}

// ExtractJSReference extracts the public API surface from JavaScript code with
// function signatures (including parameters) and preceding comments.
func ExtractJSReference(js string) string {
	var lines []string
	seen := map[string]bool{}
	jsLines := strings.Split(js, "\n")

	commentForLine := func(lineIdx int) string {
		if lineIdx > 0 {
			prev := strings.TrimSpace(jsLines[lineIdx-1])
			if strings.HasPrefix(prev, "//") {
				return strings.TrimSpace(strings.TrimPrefix(prev, "//"))
			}
		}
		return ""
	}

	funcDeclRe := regexp.MustCompile(`^(?:export\s+)?function\s+([a-zA-Z_$][\w$]*)\s*\(([^)]*)\)`)
	constFuncRe := regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+([a-zA-Z_$][\w$]*)\s*=\s*(?:function\s*)?\(([^)]*)\)`)
	constArrowRe := regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+([a-zA-Z_$][\w$]*)\s*=\s*(?:\(([^)]*)\)|([a-zA-Z_$][\w$]*))\s*=>`)
	classRe := regexp.MustCompile(`^(?:export\s+)?class\s+([A-Z][\w$]*)`)
	objDeclRe := regexp.MustCompile(`^(?:const|let|var)\s+([A-Z][\w$]*)\s*=\s*\{`)

	for idx, line := range jsLines {
		trimmed := strings.TrimSpace(line)

		if m := funcDeclRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			entry := name + "(" + m[2] + ")"
			if c := commentForLine(idx); c != "" {
				entry += " — " + c
			}
			lines = append(lines, entry)
			continue
		}

		if m := constFuncRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			entry := name + "(" + m[2] + ")"
			if c := commentForLine(idx); c != "" {
				entry += " — " + c
			}
			lines = append(lines, entry)
			continue
		}

		if m := constArrowRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			params := m[2]
			if params == "" {
				params = m[3]
			}
			entry := name + "(" + params + ")"
			if c := commentForLine(idx); c != "" {
				entry += " — " + c
			}
			lines = append(lines, entry)
			continue
		}

		if m := classRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			if !seen[name] {
				seen[name] = true
				lines = append(lines, "class "+name)
			}
			continue
		}

		if m := objDeclRe.FindStringSubmatch(trimmed); m != nil {
			objName := m[1]
			if seen[objName] {
				continue
			}
			seen[objName] = true
			bodyStart := idx
			depth := strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			bodyEnd := idx
			for k := idx + 1; k < len(jsLines) && depth > 0; k++ {
				depth += strings.Count(jsLines[k], "{") - strings.Count(jsLines[k], "}")
				bodyEnd = k
			}
			methodRe := regexp.MustCompile(`^\s+([a-zA-Z_$][\w$]*)\s*\(([^)]*)\)`)
			methodPropRe := regexp.MustCompile(`^\s+([a-zA-Z_$][\w$]*)\s*:\s*(?:function\s*)?\(([^)]*)\)`)
			for k := bodyStart + 1; k <= bodyEnd; k++ {
				ml := jsLines[k]
				var mName, mParams string
				if mm := methodRe.FindStringSubmatch(ml); mm != nil {
					mName, mParams = mm[1], mm[2]
				} else if mm := methodPropRe.FindStringSubmatch(ml); mm != nil {
					mName, mParams = mm[1], mm[2]
				}
				if mName != "" {
					key := objName + "." + mName
					if !seen[key] {
						seen[key] = true
						entry := key + "(" + mParams + ")"
						if c := commentForLine(k); c != "" {
							entry += " — " + c
						}
						lines = append(lines, entry)
					}
				}
			}
			continue
		}
	}

	objMethodRe := regexp.MustCompile(`(?m)^([A-Z][\w$]*)\.([a-zA-Z_$][\w$]*)\s*=\s*(?:function\s*)?\(([^)]*)\)`)
	for _, match := range objMethodRe.FindAllStringSubmatch(js, -1) {
		key := match[1] + "." + match[2]
		if !seen[key] {
			seen[key] = true
			lines = append(lines, key+"("+match[3]+")")
		}
	}

	if len(lines) == 0 {
		return ""
	}
	result := strings.Join(lines, "\n")
	if len(result) > 2500 {
		result = result[:2500] + "\n..."
	}
	return result
}
