/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	pingInterval    = 30 * time.Second
	reconnectMin    = 1 * time.Second
	reconnectMax    = 60 * time.Second
	maxResponseBody = 50 * 1024 * 1024
	streamChunkSize = 32 * 1024 // 32 KB chunks for streaming
)

// Config holds the settings for a tunnel connection.
type Config struct {
	BrokerURL string // e.g. "wss://tunnel.humansout.com/register"
	Token     string
	Subdomain string
	LocalAddr string // e.g. "127.0.0.1:5000"
	SiteID    string // used to route to the correct project
}

// Client manages a single tunnel connection to the broker.
type Client struct {
	cfg  Config
	conn *websocket.Conn
	url  string
	done chan struct{}
	mu   sync.Mutex // protects writes to conn

	// Active WebSocket relay connections: connID → *websocket.Conn.
	wsConns sync.Map
}

// Connect dials the broker, registers the subdomain, and starts proxying.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, cfg.BrokerURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial broker: %w", err)
	}

	// Increase read limit for large responses.
	conn.SetReadLimit(maxResponseBody + 1024)

	// Send register message.
	reg := registerMsg{Type: msgRegister, Subdomain: cfg.Subdomain, Token: cfg.Token}
	data, _ := json.Marshal(reg)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		conn.Close(websocket.StatusGoingAway, "register failed")
		return nil, fmt.Errorf("send register: %w", err)
	}

	// Read response.
	_, respData, err := conn.Read(ctx)
	if err != nil {
		conn.Close(websocket.StatusGoingAway, "no response")
		return nil, fmt.Errorf("read register response: %w", err)
	}

	// Try parsing as error first.
	var baseMsg struct{ Type string }
	json.Unmarshal(respData, &baseMsg)

	if baseMsg.Type == msgError {
		var errResp errorMsg
		json.Unmarshal(respData, &errResp)
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("%s: %s", errResp.Code, errResp.Message)
	}

	var registered registeredMsg
	if err := json.Unmarshal(respData, &registered); err != nil || registered.Type != msgRegistered {
		conn.Close(websocket.StatusProtocolError, "unexpected response")
		return nil, fmt.Errorf("unexpected register response")
	}

	c := &Client{
		cfg:  cfg,
		conn: conn,
		url:  registered.URL,
		done: make(chan struct{}),
	}

	go c.readLoop()
	go c.pingLoop()

	slog.Info("tunnel connected", "subdomain", cfg.Subdomain, "url", registered.URL)
	return c, nil
}

// URL returns the public URL of the tunnel.
func (c *Client) URL() string { return c.url }

// Subdomain returns the registered subdomain.
func (c *Client) Subdomain() string { return c.cfg.Subdomain }

// Done returns a channel that is closed when the tunnel disconnects.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close disconnects the tunnel.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return nil // already closed
	default:
		close(c.done)
	}

	// Close all active WS relay connections.
	c.wsConns.Range(func(key, value any) bool {
		if conn, ok := value.(*websocket.Conn); ok {
			conn.Close(websocket.StatusGoingAway, "tunnel closing")
		}
		c.wsConns.Delete(key)
		return true
	})

	return c.conn.Close(websocket.StatusNormalClosure, "client closing")
}

// readLoop is the SOLE reader from the broker connection. All incoming messages
// are dispatched from here — no other goroutine may call c.conn.Read().
func (c *Client) readLoop() {
	defer c.Close()

	for {
		select {
		case <-c.done:
			return
		default:
		}

		typ, data, err := c.conn.Read(context.Background())
		if err != nil {
			slog.Debug("tunnel read error", "error", err)
			return
		}

		if typ == websocket.MessageText {
			var base struct{ Type string }
			if err := json.Unmarshal(data, &base); err != nil {
				continue
			}

			switch base.Type {
			case msgHTTPRequest:
				var req httpRequestMsg
				if err := json.Unmarshal(data, &req); err != nil {
					continue
				}

				// If there's a body, read it NOW (in readLoop) before
				// dispatching, so we don't have concurrent readers.
				var body []byte
				if req.BodyFollows {
					_, bodyData, err := c.conn.Read(context.Background())
					if err != nil {
						slog.Debug("failed to read request body", "error", err)
						return
					}
					if len(bodyData) > 36 {
						body = bodyData[36:]
					}
				}

				go c.handleRequest(req, body)

			case msgWSOpen:
				var msg wsOpenMsg
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				go c.handleWSOpen(msg)

			case msgWSFrame:
				var msg wsFrameMsg
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				// The frame payload follows as the next binary message.
				// Read it here in readLoop.
				_, frameData, err := c.conn.Read(context.Background())
				if err != nil {
					slog.Debug("failed to read ws frame data", "error", err)
					return
				}
				// Strip the 36-byte connID prefix.
				var payload []byte
				if len(frameData) > 36 {
					payload = frameData[36:]
				}
				// Write to the local WebSocket.
				if val, ok := c.wsConns.Load(msg.ConnID); ok {
					localConn := val.(*websocket.Conn)
					msgType := websocket.MessageBinary
					if msg.IsText {
						msgType = websocket.MessageText
					}
					localConn.Write(context.Background(), msgType, payload)
				}

			case msgWSClose:
				var msg wsCloseMsg
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				if val, ok := c.wsConns.LoadAndDelete(msg.ConnID); ok {
					if conn, ok := val.(*websocket.Conn); ok {
						conn.Close(websocket.StatusCode(msg.Code), msg.Reason)
					}
				}
			}
		}
		// Binary frames that arrive without a preceding text frame are
		// unexpected at this point — ignore them.
	}
}

// handleRequest processes a proxied HTTP request. Body is already read by readLoop.
func (c *Client) handleRequest(req httpRequestMsg, body []byte) {
	ctx := context.Background()

	// Build local HTTP request.
	localURL := fmt.Sprintf("http://%s%s", c.cfg.LocalAddr, req.Path)
	localReq, err := http.NewRequest(req.Method, localURL, bytes.NewReader(body))
	if err != nil {
		c.sendErrorResponse(ctx, req.ID, 502, "Failed to create request")
		return
	}

	// Copy headers.
	for k, v := range req.Headers {
		localReq.Header.Set(k, v)
	}
	// Tell the public server which site this request is for.
	if c.cfg.SiteID != "" {
		localReq.Header.Set("X-Tunnel-Site-ID", c.cfg.SiteID)
	}

	// Use a transport directly so we can detect streaming responses
	// without a client-level timeout killing them.
	transport := &http.Transport{}
	resp, err := transport.RoundTrip(localReq)
	if err != nil {
		c.sendErrorResponse(ctx, req.ID, 502, "Local server error")
		return
	}
	defer resp.Body.Close()

	// Build response headers.
	headers := make(map[string]string)
	for k, v := range resp.Header {
		headers[k] = strings.Join(v, ", ")
	}

	// Check if this is a streaming response (SSE or chunked).
	ct := resp.Header.Get("Content-Type")
	te := resp.Header.Get("Transfer-Encoding")
	isSSE := strings.HasPrefix(ct, "text/event-stream")
	isChunked := strings.Contains(te, "chunked")

	if isSSE || isChunked {
		c.handleStreamingResponse(ctx, req.ID, resp, headers)
		return
	}

	// Normal buffered response.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		c.sendErrorResponse(ctx, req.ID, 502, "Failed to read response")
		return
	}

	respMsg := httpResponseMsg{
		Type:        msgHTTPResp,
		ID:          req.ID,
		Status:      resp.StatusCode,
		Headers:     headers,
		BodyFollows: len(respBody) > 0,
	}

	// Send response JSON + body.
	c.mu.Lock()
	defer c.mu.Unlock()

	respData, _ := json.Marshal(respMsg)
	if err := c.conn.Write(ctx, websocket.MessageText, respData); err != nil {
		return
	}

	if len(respBody) > 0 {
		frame := append([]byte(req.ID), respBody...)
		c.conn.Write(ctx, websocket.MessageBinary, frame)
	}
}

// handleStreamingResponse reads a streaming HTTP response (SSE, chunked) and
// sends it through the tunnel as stream_start + binary chunks + stream_end.
func (c *Client) handleStreamingResponse(ctx context.Context, reqID string, resp *http.Response, headers map[string]string) {
	startMsg := streamStartMsg{
		Type:    msgStreamStart,
		ID:      reqID,
		Status:  resp.StatusCode,
		Headers: headers,
	}

	c.mu.Lock()
	data, _ := json.Marshal(startMsg)
	err := c.conn.Write(ctx, websocket.MessageText, data)
	c.mu.Unlock()
	if err != nil {
		return
	}

	// Read body incrementally and send chunks.
	reader := bufio.NewReaderSize(resp.Body, streamChunkSize)
	buf := make([]byte, streamChunkSize)

	for {
		select {
		case <-c.done:
			return
		default:
		}

		n, err := reader.Read(buf)
		if n > 0 {
			frame := append([]byte(reqID), buf[:n]...)
			c.mu.Lock()
			writeErr := c.conn.Write(ctx, websocket.MessageBinary, frame)
			c.mu.Unlock()
			if writeErr != nil {
				return
			}
		}
		if err != nil {
			break // EOF or error
		}
	}

	// Send stream_end.
	endMsg := streamEndMsg{
		Type: msgStreamEnd,
		ID:   reqID,
	}
	c.mu.Lock()
	data, _ = json.Marshal(endMsg)
	c.conn.Write(ctx, websocket.MessageText, data)
	c.mu.Unlock()
}

// handleWSOpen is called when the broker tells us a visitor wants a WebSocket connection.
func (c *Client) handleWSOpen(msg wsOpenMsg) {
	ctx := context.Background()

	// Dial the local WebSocket.
	localURL := fmt.Sprintf("ws://%s%s", c.cfg.LocalAddr, msg.Path)

	// Build request headers — skip hop-by-hop headers.
	httpHeaders := http.Header{}
	for k, v := range msg.Headers {
		lower := strings.ToLower(k)
		if lower == "upgrade" || lower == "connection" || lower == "sec-websocket-key" ||
			lower == "sec-websocket-version" || lower == "sec-websocket-extensions" {
			continue
		}
		httpHeaders.Set(k, v)
	}
	if c.cfg.SiteID != "" {
		httpHeaders.Set("X-Tunnel-Site-ID", c.cfg.SiteID)
	}

	localConn, _, err := websocket.Dial(ctx, localURL, &websocket.DialOptions{
		HTTPHeader: httpHeaders,
	})
	if err != nil {
		slog.Debug("failed to connect local WebSocket", "path", msg.Path, "error", err)
		c.mu.Lock()
		closeMsg := wsCloseMsg{Type: msgWSClose, ConnID: msg.ConnID, Code: 1011, Reason: "local ws failed"}
		data, _ := json.Marshal(closeMsg)
		c.conn.Write(ctx, websocket.MessageText, data)
		c.mu.Unlock()
		return
	}

	// Store the connection.
	c.wsConns.Store(msg.ConnID, localConn)

	// Tell broker we're connected.
	c.mu.Lock()
	openedMsg := wsOpenedMsg{Type: msgWSOpened, ConnID: msg.ConnID}
	data, _ := json.Marshal(openedMsg)
	c.conn.Write(ctx, websocket.MessageText, data)
	c.mu.Unlock()

	// Relay frames from local WS → broker.
	defer func() {
		c.wsConns.Delete(msg.ConnID)
		localConn.Close(websocket.StatusGoingAway, "")
		c.mu.Lock()
		closeData, _ := json.Marshal(wsCloseMsg{Type: msgWSClose, ConnID: msg.ConnID, Code: 1000})
		c.conn.Write(context.Background(), websocket.MessageText, closeData)
		c.mu.Unlock()
	}()

	for {
		msgType, frameData, err := localConn.Read(ctx)
		if err != nil {
			return
		}

		isText := msgType == websocket.MessageText

		frameMeta := wsFrameMsg{
			Type:     msgWSFrame,
			ConnID:   msg.ConnID,
			IsText:   isText,
			DataSize: len(frameData),
		}

		c.mu.Lock()
		metaData, _ := json.Marshal(frameMeta)
		err = c.conn.Write(ctx, websocket.MessageText, metaData)
		if err != nil {
			c.mu.Unlock()
			return
		}
		frame := append([]byte(msg.ConnID), frameData...)
		err = c.conn.Write(ctx, websocket.MessageBinary, frame)
		c.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *Client) sendErrorResponse(ctx context.Context, reqID string, status int, msg string) {
	resp := httpResponseMsg{
		Type:        msgHTTPResp,
		ID:          reqID,
		Status:      status,
		Headers:     map[string]string{"Content-Type": "text/plain"},
		BodyFollows: true,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, _ := json.Marshal(resp)
	c.conn.Write(ctx, websocket.MessageText, data)
	frame := append([]byte(reqID), []byte(msg)...)
	c.conn.Write(ctx, websocket.MessageBinary, frame)
}

func (c *Client) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				slog.Debug("tunnel ping failed", "error", err)
				c.Close()
				return
			}
		}
	}
}
