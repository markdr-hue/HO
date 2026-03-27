/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"strconv"
	"strings"
)

// --- Utility functions ---

// isPermanentError returns true for errors that can never succeed on retry,
// such as missing API keys or invalid provider configuration.
func isPermanentError(err error) bool {
	msg := strings.ToLower(err.Error())
	permanentPatterns := []string{
		"no api key", "no model configured", "provider not available",
		"failed to decrypt", "has no api key", "has no base_url",
	}
	for _, p := range permanentPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	// Connection errors (DNS, refused, TLS) — endpoint unreachable, retrying won't help.
	// But timeouts are transient and should be retried.
	if strings.Contains(msg, "send request:") && !strings.Contains(msg, "api error") &&
		!strings.Contains(msg, "timeout") && !strings.Contains(msg, "context deadline exceeded") {
		return true
	}
	return false
}

func isInteractiveTool(name string) bool {
	switch name {
	case "manage_communication", "manage_pages",
		"manage_files", "manage_layout", "manage_schema",
		"manage_data", "manage_endpoints":
		return true
	}
	return false
}

// payloadInt64 extracts an int64 from a payload map, handling int64, float64,
// and int types (JSON numbers deserialize as float64 in Go's interface{}).
func payloadInt64(payload map[string]interface{}, key string) int64 {
	if v, ok := payload[key].(int64); ok {
		return v
	}
	if v, ok := payload[key].(float64); ok {
		return int64(v)
	}
	if v, ok := payload[key].(int); ok {
		return int64(v)
	}
	if v, ok := payload[key].(string); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Truncate at rune boundary to avoid splitting multi-byte UTF-8 characters.
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
