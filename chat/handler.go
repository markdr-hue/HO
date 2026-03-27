/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package chat

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/markdr-hue/HO/llm"
)

const maxChatBodySize = 1 << 20 // 1 MB

const sessionMaxIdle = 2 * time.Hour

// sessionEntry pairs a session with its last-used timestamp and a reference
// count to prevent cleanup during active streaming.
type sessionEntry struct {
	session  *Session
	lastUsed time.Time
	inUse    int32 // atomic ref count of active streams
}

// ChatHandler exposes HTTP endpoints for the streaming chat interface.
type ChatHandler struct {
	deps     SessionDeps
	mu       sync.Mutex
	sessions map[string]*sessionEntry // keyed by "siteID:sessionID"
}

// NewChatHandler creates a ChatHandler with the given dependencies.
func NewChatHandler(deps SessionDeps) *ChatHandler {
	ch := &ChatHandler{
		deps:     deps,
		sessions: make(map[string]*sessionEntry),
	}
	go ch.cleanupLoop()
	return ch
}

// cleanupLoop removes idle sessions every 30 minutes.
func (h *ChatHandler) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		now := time.Now()
		for key, entry := range h.sessions {
			if atomic.LoadInt32(&entry.inUse) == 0 && now.Sub(entry.lastUsed) > sessionMaxIdle {
				delete(h.sessions, key)
			}
		}
		h.mu.Unlock()
	}
}

// streamRequest is the JSON body sent by the client to the stream endpoint.
type streamRequest struct {
	Message string `json:"message"`
}

// HandleStream is the SSE endpoint for streaming chat. It reads a user message
// from the POST body, streams the LLM response as Server-Sent Events, and
// transparently handles tool-call loops.
//
// Expected request: POST with JSON body {"message":"..."} and query params
// site_id and session_id.
func (h *ChatHandler) HandleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract parameters.
	siteIDStr := r.URL.Query().Get("site_id")
	sessionID := r.URL.Query().Get("session_id")
	if siteIDStr == "" || sessionID == "" {
		http.Error(w, "site_id and session_id query parameters are required", http.StatusBadRequest)
		return
	}

	var siteID int
	if _, err := fmt.Sscanf(siteIDStr, "%d", &siteID); err != nil {
		http.Error(w, "site_id must be an integer", http.StatusBadRequest)
		return
	}

	// Read the message from the request body (size-limited).
	var req streamRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxChatBodySize)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // for nginx

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Disable write deadline for this SSE connection.
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})

	// Get or create the session and track usage.
	entry, err := h.getOrCreateSession(siteID, sessionID)
	if err != nil {
		http.Error(w, "failed to open site database", http.StatusInternalServerError)
		return
	}
	atomic.AddInt32(&entry.inUse, 1)
	defer atomic.AddInt32(&entry.inUse, -1)
	session := entry.session
	logger := h.deps.Logger.With("handler", "chat_stream", "site_id", siteID, "session_id", sessionID)

	// Define the SSE callback.
	callback := func(chunk llm.StreamChunk) {
		if chunk.Error != nil {
			writeSSE(w, "error", map[string]string{"error": chunk.Error.Error()})
			flusher.Flush()
			return
		}
		if chunk.Delta != "" {
			writeSSE(w, "token", map[string]string{"text": chunk.Delta})
			flusher.Flush()
		}
		if chunk.ToolCall != nil {
			writeSSE(w, "tool_start", map[string]string{
				"name": chunk.ToolCall.Name,
				"id":   chunk.ToolCall.ID,
			})
			flusher.Flush()
		}
		if chunk.ToolResult != nil {
			writeSSE(w, "tool_result", chunk.ToolResult)
			flusher.Flush()
		}
		if chunk.Done {
			writeSSE(w, "done", map[string]interface{}{})
			flusher.Flush()
		}
	}

	// Run the send loop; errors are reported inline as SSE events.
	if err := session.Send(r.Context(), req.Message, callback); err != nil {
		logger.Error("session send error", "error", err)
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
	}
}

// HandleHistory returns the chat history for a session as JSON.
// GET with query params site_id and session_id.
func (h *ChatHandler) HandleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	siteIDStr := r.URL.Query().Get("site_id")
	sessionID := r.URL.Query().Get("session_id")
	if siteIDStr == "" || sessionID == "" {
		http.Error(w, "site_id and session_id query parameters are required", http.StatusBadRequest)
		return
	}

	var siteID int
	if _, err := fmt.Sscanf(siteIDStr, "%d", &siteID); err != nil {
		http.Error(w, "site_id must be an integer", http.StatusBadRequest)
		return
	}

	// Parse optional before parameter for pagination.
	beforeStr := r.URL.Query().Get("before")

	// Open the site DB for history queries.
	siteDB, err := h.deps.SiteDBManager.Open(siteID)
	if err != nil {
		h.deps.Logger.Error("failed to open site db", "site_id", siteID, "error", err)
		http.Error(w, "failed to open site database", http.StatusInternalServerError)
		return
	}

	// For the admin session, return merged history (admin + brain messages)
	// so the chat UI shows brain activity alongside user-initiated chat.
	if sessionID == "admin" {
		merged, err := LoadMergedHistory(siteDB.Reader(), historyLimit, beforeStr)
		if err != nil {
			h.deps.Logger.Error("load merged history error", "error", err)
			http.Error(w, "failed to load history", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(merged); err != nil {
			h.deps.Logger.Error("encode merged history error", "error", err)
		}
		return
	}

	messages, err := LoadHistory(siteDB.Reader(), sessionID, historyLimit)
	if err != nil {
		h.deps.Logger.Error("load history error", "error", err)
		http.Error(w, "failed to load history", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(messages); err != nil {
		h.deps.Logger.Error("encode history error", "error", err)
	}
}

// getOrCreateSession returns an existing session or creates a new one.
func (h *ChatHandler) getOrCreateSession(siteID int, sessionID string) (*sessionEntry, error) {
	key := fmt.Sprintf("%d:%s", siteID, sessionID)

	h.mu.Lock()
	defer h.mu.Unlock()

	if entry, ok := h.sessions[key]; ok {
		entry.lastUsed = time.Now()
		return entry, nil
	}

	s, err := NewSession(sessionID, siteID, h.deps)
	if err != nil {
		return nil, err
	}
	entry := &sessionEntry{session: s, lastUsed: time.Now()}
	h.sessions[key] = entry
	return entry, nil
}

// writeSSE writes a single SSE event to the writer.
func writeSSE(w http.ResponseWriter, event string, data interface{}) {
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("chat: failed to marshal SSE data", "error", err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
}
