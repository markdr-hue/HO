/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package chat

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/markdr-hue/HO/llm"
)

// dbMessage mirrors a row in the ho_chat_messages table.
type dbMessage struct {
	ID         int
	SessionID  string
	Role       string
	Content    string
	ToolCalls  *string // JSON-encoded []llm.ToolCall, nullable
	ToolCallID *string // nullable
	Metadata   string
}

// LoadHistory reads the most recent messages for a session from the database
// and returns them as llm.Message values ordered oldest-first.
func LoadHistory(db *sql.DB, sessionID string, limit int) ([]llm.Message, error) {
	if limit <= 0 {
		limit = 50
	}

	// Select newest N rows, then reverse so we return oldest-first.
	rows, err := db.Query(`
		SELECT role, content, tool_calls, tool_call_id
		FROM ho_chat_messages
		WHERE session_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`,
		sessionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("chat: load history: %w", err)
	}
	defer rows.Close()

	var msgs []llm.Message
	for rows.Next() {
		var (
			role       string
			content    string
			toolCalls  *string
			toolCallID *string
		)
		if err := rows.Scan(&role, &content, &toolCalls, &toolCallID); err != nil {
			return nil, fmt.Errorf("chat: scan message: %w", err)
		}

		msg := llm.Message{
			Role:    llm.Role(role),
			Content: content,
		}
		if toolCallID != nil {
			msg.ToolCallID = *toolCallID
		}
		if toolCalls != nil && *toolCalls != "" {
			var tcs []llm.ToolCall
			if err := json.Unmarshal([]byte(*toolCalls), &tcs); err == nil {
				msg.ToolCalls = tcs
			}
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: iterate history: %w", err)
	}

	// Reverse so the oldest message comes first.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// MessageWithMeta wraps a message with metadata for merged history display.
type MessageWithMeta struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	SessionID  string         `json:"session_id"`
	CreatedAt  string         `json:"created_at"`
}

// LoadMergedHistory reads the most recent messages from both "admin" and "brain"
// sessions for a site, merged by timestamp. Used for the chat UI display to show
// brain activity alongside user-initiated chat.
func LoadMergedHistory(db *sql.DB, limit int, before string) ([]MessageWithMeta, error) {
	if limit <= 0 {
		limit = 100
	}

	var rows *sql.Rows
	var err error
	if before != "" {
		rows, err = db.Query(`
			SELECT role, content, tool_calls, tool_call_id, session_id, created_at
			FROM ho_chat_messages
			WHERE session_id IN ('admin', 'brain') AND created_at < ?
			ORDER BY created_at DESC, id DESC
			LIMIT ?`,
			before, limit,
		)
	} else {
		rows, err = db.Query(`
			SELECT role, content, tool_calls, tool_call_id, session_id, created_at
			FROM ho_chat_messages
			WHERE session_id IN ('admin', 'brain')
			ORDER BY created_at DESC, id DESC
			LIMIT ?`,
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("chat: load merged history: %w", err)
	}
	defer rows.Close()

	var msgs []MessageWithMeta
	for rows.Next() {
		var (
			role       string
			content    string
			toolCalls  *string
			toolCallID *string
			sessionID  string
			createdAt  string
		)
		if err := rows.Scan(&role, &content, &toolCalls, &toolCallID, &sessionID, &createdAt); err != nil {
			return nil, fmt.Errorf("chat: scan merged message: %w", err)
		}

		msg := MessageWithMeta{
			Role:      role,
			Content:   content,
			SessionID: sessionID,
			CreatedAt: createdAt,
		}
		if toolCallID != nil {
			msg.ToolCallID = *toolCallID
		}
		if toolCalls != nil && *toolCalls != "" {
			var tcs []llm.ToolCall
			if err := json.Unmarshal([]byte(*toolCalls), &tcs); err == nil {
				msg.ToolCalls = tcs
			}
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: iterate merged history: %w", err)
	}

	// Reverse so the oldest message comes first.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// SaveMessage persists a single message to the ho_chat_messages table.
func SaveMessage(db *sql.DB, sessionID string, msg llm.Message) error {
	var toolCallsJSON *string
	if len(msg.ToolCalls) > 0 {
		data, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return fmt.Errorf("chat: marshal tool_calls: %w", err)
		}
		s := string(data)
		toolCallsJSON = &s
	}

	var toolCallID *string
	if msg.ToolCallID != "" {
		toolCallID = &msg.ToolCallID
	}

	_, err := db.Exec(`
		INSERT INTO ho_chat_messages (session_id, role, content, tool_calls, tool_call_id)
		VALUES (?, ?, ?, ?, ?)`,
		sessionID, string(msg.Role), msg.Content, toolCallsJSON, toolCallID,
	)
	if err != nil {
		return fmt.Errorf("chat: save message: %w", err)
	}
	return nil
}
