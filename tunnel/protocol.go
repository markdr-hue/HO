/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tunnel

// Wire protocol message types exchanged between broker and tunnel client.

const (
	msgRegister    = "register"
	msgRegistered  = "registered"
	msgError       = "error"
	msgHTTPRequest = "http_request"
	msgHTTPResp    = "http_response"

	// WebSocket relay messages.
	msgWSOpen   = "ws_open"   // broker→client: visitor wants a WebSocket
	msgWSOpened = "ws_opened" // client→broker: local WS connected
	msgWSFrame  = "ws_frame"  // bidirectional: relay a WS frame
	msgWSClose  = "ws_close"  // bidirectional: one side closed

	// Streaming response messages (SSE, chunked).
	msgStreamStart = "stream_start" // client→broker: streaming response headers
	msgStreamEnd   = "stream_end"   // client→broker: stream finished
)

type registerMsg struct {
	Type      string `json:"type"`
	Subdomain string `json:"subdomain"`
	Token     string `json:"token"`
}

type registeredMsg struct {
	Type      string `json:"type"`
	Subdomain string `json:"subdomain"`
	URL       string `json:"url"`
}

type errorMsg struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type httpRequestMsg struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Headers     map[string]string `json:"headers"`
	BodyFollows bool              `json:"body_follows"`
}

type httpResponseMsg struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers"`
	BodyFollows bool              `json:"body_follows"`
}

// wsOpenMsg is sent by the broker when a visitor wants a WebSocket connection.
type wsOpenMsg struct {
	Type    string            `json:"type"`
	ConnID  string            `json:"conn_id"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// wsOpenedMsg is sent by the client when the local WS connection succeeds.
type wsOpenedMsg struct {
	Type   string `json:"type"`
	ConnID string `json:"conn_id"`
}

// wsFrameMsg wraps a WebSocket frame for relay.
// The actual payload is sent as a binary frame prefixed with the ConnID (36 bytes).
type wsFrameMsg struct {
	Type     string `json:"type"`
	ConnID   string `json:"conn_id"`
	IsText   bool   `json:"is_text"`
	DataSize int    `json:"data_size"`
}

// wsCloseMsg signals WebSocket closure.
type wsCloseMsg struct {
	Type   string `json:"type"`
	ConnID string `json:"conn_id"`
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// streamStartMsg is like httpResponseMsg but signals a streaming response.
type streamStartMsg struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

// streamEndMsg signals the end of a streaming response.
type streamEndMsg struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
