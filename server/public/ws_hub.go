package public

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// wsHub manages WebSocket rooms. Each room is identified by a key
// formatted as "siteID:endpointPath:room" ensuring full site isolation.
type wsHub struct {
	mu    sync.RWMutex
	rooms map[string]map[string]*wsClient // roomKey → clientID → client
}

// wsClient represents a single WebSocket connection in a room.
type wsClient struct {
	id string
	ch chan []byte // buffered outbound messages
}

// NewWSHub creates a new WebSocket hub for managing rooms.
func NewWSHub() *wsHub {
	return &wsHub{
		rooms: make(map[string]map[string]*wsClient),
	}
}

// join adds a client to a room, broadcasts a JOIN event, and returns the client handle.
func (h *wsHub) join(roomKey, clientID string) *wsClient {
	c := &wsClient{
		id: clientID,
		ch: make(chan []byte, 128),
	}
	h.mu.Lock()
	room, ok := h.rooms[roomKey]
	if !ok {
		room = make(map[string]*wsClient)
		h.rooms[roomKey] = room
	}
	room[clientID] = c

	// Count clients for the join event.
	clientCount := len(room)
	h.mu.Unlock()

	// Broadcast join event to all existing clients (including the joiner).
	joinMsg, _ := json.Marshal(map[string]interface{}{
		"_type":    "join",
		"_sender":  clientID,
		"_room":    roomKey,
		"_clients": clientCount,
	})
	h.broadcastAll(roomKey, joinMsg)

	return c
}

// leave removes a client from a room, broadcasts a LEAVE event, and cleans up empty rooms.
func (h *wsHub) leave(roomKey, clientID string) {
	h.mu.Lock()
	room, ok := h.rooms[roomKey]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(room, clientID)
	clientCount := len(room)
	empty := clientCount == 0
	if empty {
		delete(h.rooms, roomKey)
	}
	h.mu.Unlock()

	if !empty {
		leaveMsg, _ := json.Marshal(map[string]interface{}{
			"_type":    "leave",
			"_sender":  clientID,
			"_room":    roomKey,
			"_clients": clientCount,
		})
		h.broadcastAll(roomKey, leaveMsg)
	}
}

// broadcast sends msg to all clients in the room except the sender.
func (h *wsHub) broadcast(roomKey, senderID string, msg []byte) {
	h.mu.RLock()
	room := h.rooms[roomKey]
	if room == nil {
		h.mu.RUnlock()
		return
	}
	targets := make([]*wsClient, 0, len(room))
	for _, c := range room {
		if c.id != senderID {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range targets {
		select {
		case c.ch <- msg:
		default:
			slog.Warn("ws: message dropped (channel full)", "room", roomKey, "client", c.id)
			h.sendError(c, "buffer full, message dropped")
		}
	}
}

// broadcastAll sends msg to ALL clients in the room (including sender).
func (h *wsHub) broadcastAll(roomKey string, msg []byte) {
	h.mu.RLock()
	room := h.rooms[roomKey]
	if room == nil {
		h.mu.RUnlock()
		return
	}
	targets := make([]*wsClient, 0, len(room))
	for _, c := range room {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		select {
		case c.ch <- msg:
		default:
			// Don't warn for system messages on full buffers.
		}
	}
}

// sendTo sends msg to a specific client in a room. Returns false if client not found.
func (h *wsHub) sendTo(roomKey, targetID string, msg []byte) bool {
	h.mu.RLock()
	room := h.rooms[roomKey]
	if room == nil {
		h.mu.RUnlock()
		return false
	}
	target := room[targetID]
	h.mu.RUnlock()

	if target == nil {
		return false
	}
	select {
	case target.ch <- msg:
		return true
	default:
		slog.Warn("ws: direct message dropped (channel full)", "room", roomKey, "target", targetID)
		return false
	}
}

// sendError sends a system error message to a single client.
func (h *wsHub) sendError(c *wsClient, msg string) {
	errMsg, _ := json.Marshal(map[string]interface{}{
		"_type":    "error",
		"_message": msg,
	})
	select {
	case c.ch <- errMsg:
	default:
		// Can't send error if buffer is full — nothing more to do.
	}
}

// roomClients returns the list of client IDs in a room.
func (h *wsHub) roomClients(roomKey string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	room := h.rooms[roomKey]
	if room == nil {
		return nil
	}
	ids := make([]string, 0, len(room))
	for id := range room {
		ids = append(ids, id)
	}
	return ids
}

// BroadcastToRoom implements events.WSBroadcaster, allowing server-side
// components (Actions Runner) to push messages to connected WebSocket clients.
func (h *wsHub) BroadcastToRoom(roomKey string, msg json.RawMessage) {
	h.broadcastAll(roomKey, []byte(msg))
}

// activeRooms returns all active room keys matching a prefix (e.g. "1:chat:").
func (h *wsHub) activeRooms(prefix string) map[string]int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make(map[string]int)
	for key, room := range h.rooms {
		if len(prefix) == 0 || len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			result[key] = len(room)
		}
	}
	return result
}
