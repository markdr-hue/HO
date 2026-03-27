/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
)

// pruneMessages keeps the first user message (build instructions), anchor messages
// ([ANCHOR] prefix), and the most recent messages. This prevents
// losing critical early context (table structures, CSS class names) that later pages need.
func pruneMessages(messages []llm.Message, maxRecent int) []llm.Message {
	total := len(messages)
	if total <= maxRecent+1 {
		return messages
	}

	// Remove stale [PAGE_CTX] messages: only the latest one matters (it
	// describes the NEXT page to build). Older ones are for already-built pages.
	latestPageCtx := -1
	for i := total - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleUser && strings.HasPrefix(messages[i].Content, "[PAGE_CTX]") {
			if latestPageCtx == -1 {
				latestPageCtx = i
			} else {
				// Mark older [PAGE_CTX] for removal by clearing content.
				messages[i] = llm.Message{Role: llm.RoleUser, Content: ""}
			}
		}
	}
	// Rebuild without empty messages.
	if latestPageCtx > 0 {
		cleaned := make([]llm.Message, 0, len(messages))
		for _, m := range messages {
			if m.Content != "" || m.Role != llm.RoleUser {
				cleaned = append(cleaned, m)
			}
		}
		messages = cleaned
		total = len(messages)
		if total <= maxRecent+1 {
			return messages
		}
	}

	// Mark anchor indices: messages with [ANCHOR] prefix survive pruning.
	// These are synthetic summaries injected after key tool calls.
	anchors := map[int]bool{}
	middle := messages[1 : total-maxRecent]
	for i, msg := range middle {
		if msg.Role == llm.RoleUser && strings.HasPrefix(msg.Content, "[ANCHOR]") {
			anchors[i+1] = true // +1 because middle starts at index 1
		}
	}

	// No need to keep assistant messages before anchors — [ANCHOR] messages
	// are self-contained user messages, not tool results.
	anchorPairs := map[int]bool{}
	for idx := range anchors {
		anchorPairs[idx] = true
	}

	// Dynamic anchor budget — more tables/endpoints need more context preserved.
	maxAnchors := 14 + len(anchors)/3
	if maxAnchors > 24 {
		maxAnchors = 24
	}
	if len(anchorPairs) > maxAnchors {
		sorted := make([]int, 0, len(anchorPairs))
		for idx := range anchorPairs {
			sorted = append(sorted, idx)
		}
		sort.Ints(sorted)
		trimmed := make(map[int]bool, maxAnchors)
		for _, idx := range sorted[:maxAnchors] {
			trimmed[idx] = true
		}
		anchorPairs = trimmed
	}

	recentStart := total - maxRecent
	// Don't split tool call/result groups — the API requires every tool_use
	// to have a matching tool_result. Back up to the nearest non-tool message.
	for recentStart > 1 && messages[recentStart].Role == llm.RoleTool {
		recentStart--
	}

	pruned := make([]llm.Message, 0, 1+len(anchorPairs)+maxRecent+4)
	pruned = append(pruned, messages[0]) // first user message

	// Build summary zone: collapse non-anchor middle messages into compact summaries.
	// This preserves awareness of what was built without consuming many tokens.
	var summaryParts []string
	for idx := 1; idx < recentStart; idx++ {
		if anchorPairs[idx] {
			// Flush any pending summary before the anchor.
			if len(summaryParts) > 0 {
				pruned = append(pruned, llm.Message{
					Role:    llm.RoleUser,
					Content: "[Progress] " + strings.Join(summaryParts, ", "),
				})
				summaryParts = nil
			}
			pruned = append(pruned, messages[idx])
			continue
		}

		// Summarize tool calls from assistant messages.
		msg := messages[idx]
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				summary := summarizeToolCall(tc.Name, tc.Arguments)
				if summary != "" {
					summaryParts = append(summaryParts, summary)
				}
			}
		}
		// Skip tool result and other messages — they're captured by the tool call summary.
	}
	// Flush remaining summary.
	if len(summaryParts) > 0 {
		pruned = append(pruned, llm.Message{
			Role:    llm.RoleUser,
			Content: "[Progress] " + strings.Join(summaryParts, ", "),
		})
	}

	// Add the recent window.
	pruned = append(pruned, messages[recentStart:]...)
	return pruned
}

// summarizeToolCall creates a one-line summary of a tool call for the progress zone.
func summarizeToolCall(toolName, argsJSON string) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}

	action, _ := args["action"].(string)
	switch toolName {
	case "manage_pages":
		path, _ := args["path"].(string)
		if action == "save" && path != "" {
			return "built page " + path
		}
	case "manage_schema":
		name, _ := args["table_name"].(string)
		if action == "create" && name != "" {
			return "created table " + name
		}
	case "manage_endpoints":
		path, _ := args["path"].(string)
		if strings.HasPrefix(action, "create_") && path != "" {
			return action + " /api/" + path
		}
	case "manage_files":
		fn, _ := args["filename"].(string)
		if fn != "" {
			return "saved " + fn
		}
	case "manage_layout":
		if action == "save" {
			return "saved layout"
		}
	case "manage_data":
		table, _ := args["table_name"].(string)
		if action == "insert" && table != "" {
			return "seeded " + table
		}
	}
	return ""
}

// --- Persistence helpers ---

func (w *PipelineWorker) saveChatMessage(msg llm.Message) {
	var toolCallsJSON *string
	if len(msg.ToolCalls) > 0 {
		data, err := json.Marshal(msg.ToolCalls)
		if err == nil {
			s := string(data)
			toolCallsJSON = &s
		}
	}

	var toolCallID *string
	if msg.ToolCallID != "" {
		toolCallID = &msg.ToolCallID
	}

	_, err := w.siteDB.ExecWrite(
		`INSERT INTO ho_chat_messages (session_id, role, content, tool_calls, tool_call_id) VALUES ('brain', ?, ?, ?, ?)`,
		string(msg.Role), msg.Content, toolCallsJSON, toolCallID,
	)
	if err != nil {
		w.logger.Error("failed to save chat message", "error", err)
	}
}

// saveChatMessageOnce saves a user message only if an identical one hasn't been
// saved recently (within 30 minutes). Prevents duplicate messages on stage retries.
func (w *PipelineWorker) saveChatMessageOnce(msg llm.Message) {
	if msg.Role == llm.RoleUser {
		var exists int
		w.siteDB.Reader().QueryRow(
			"SELECT COUNT(*) FROM ho_chat_messages WHERE session_id = 'brain' AND role = 'user' AND content = ? AND created_at > datetime('now', '-30 minutes')",
			msg.Content,
		).Scan(&exists)
		if exists > 0 {
			return
		}
	}
	w.saveChatMessage(msg)
}

func (w *PipelineWorker) logBrainEvent(eventType, summary, details string, tokens int, model string, durationMs int64) {
	_, err := w.siteDB.ExecWrite(
		"INSERT INTO ho_brain_log (event_type, summary, details, tokens_used, model, duration_ms) VALUES (?, ?, ?, ?, ?, ?)",
		eventType, summary, details, tokens, model, durationMs,
	)
	if err != nil {
		w.logger.Error("failed to log brain event", "error", err)
	}
}

func (w *PipelineWorker) publishBrainMessage(content string) {
	w.saveChatMessage(llm.Message{Role: llm.RoleAssistant, Content: content})
	if w.deps.Bus != nil {
		w.deps.Bus.Publish(events.NewEvent(events.EventBrainMessage, w.siteID, map[string]interface{}{
			"session_id": "brain",
			"role":       "assistant",
			"content":    content,
		}))
	}
}

func (w *PipelineWorker) publishBrainError(errMsg string) {
	w.publishBrainMessage("**Pipeline Error:** " + errMsg)
	if w.deps.Bus != nil {
		w.deps.Bus.Publish(events.NewEvent(events.EventBrainError, w.siteID, map[string]interface{}{
			"error": errMsg,
		}))
	}
}
