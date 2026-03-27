/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/markdr-hue/HO/llm"
)

const (
	defaultEndpoint         = "https://api.anthropic.com/v1/messages"
	defaultAnthropicVersion = "2023-06-01"
)

// Client implements llm.Provider for the Anthropic API.
type Client struct {
	name       string
	apiKey     string
	endpoint   string // full URL to the messages endpoint
	apiVersion string
	httpClient *http.Client
}

// Option configures the Anthropic client.
type Option func(*Client)

// WithBaseURL sets the API endpoint URL. If the URL ends with "/messages",
// it's used as-is. Otherwise, "/v1/messages" is appended.
func WithBaseURL(url string) Option {
	return func(c *Client) {
		url = strings.TrimRight(url, "/")
		if strings.HasSuffix(url, "/messages") {
			c.endpoint = url
		} else {
			c.endpoint = url + "/v1/messages"
		}
	}
}

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithAPIVersion overrides the default anthropic-version header value.
func WithAPIVersion(version string) Option {
	return func(c *Client) {
		c.apiVersion = version
	}
}

// New creates a new Anthropic provider.
func New(name, apiKey string, opts ...Option) *Client {
	c := &Client{
		name:       name,
		apiKey:     apiKey,
		endpoint:   defaultEndpoint,
		apiVersion: defaultAnthropicVersion,
		httpClient: &http.Client{Timeout: 6 * time.Minute},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) Name() string { return c.name }
func (c *Client) Type() string { return "anthropic" }

// Ping checks connectivity by sending a minimal completion request.
func (c *Client) Ping(ctx context.Context, model string) error {
	req := llm.CompletionRequest{
		Model:     model,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: "ping"}},
		MaxTokens: 1,
	}
	_, err := c.Complete(ctx, req)
	return err
}

// Complete sends a non-streaming completion request.
func (c *Client) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	body := c.buildRequestBody(req, false)

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	c.setHeaders(httpReq)
	if req.CacheSystem {
		httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	return c.parseResponse(&apiResp), nil
}

// Stream sends a streaming completion request and calls the callback for each chunk.
func (c *Client) Stream(ctx context.Context, req llm.CompletionRequest, callback llm.StreamCallback) error {
	body := c.buildRequestBody(req, true)

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("anthropic: create request: %w", err)
	}
	c.setHeaders(httpReq)
	if req.CacheSystem {
		httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("anthropic: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.readError(resp)
	}

	return c.parseSSEStream(resp.Body, callback)
}

// setHeaders sets common headers for Anthropic API requests.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", c.apiVersion)
}

// buildRequestBody converts our generic request into the Anthropic API format.
func (c *Client) buildRequestBody(req llm.CompletionRequest, stream bool) map[string]interface{} {
	body := map[string]interface{}{
		"model": req.Model,
	}

	// Anthropic uses a separate "system" field, not a system message in the array.
	// When caching is enabled, format as content blocks with cache_control.
	if req.System != "" {
		if req.CacheSystem {
			body["system"] = []map[string]interface{}{
				{
					"type":          "text",
					"text":          req.System,
					"cache_control": map[string]string{"type": "ephemeral"},
				},
			}
		} else {
			body["system"] = req.System
		}
	}

	// Convert messages (skip system role since we handle it above)
	var msgs []map[string]interface{}
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			// If system wasn't set in the request but there's a system message, use it
			if req.System == "" {
				body["system"] = m.Content
			}
			continue
		}

		msg := map[string]interface{}{
			"role": string(m.Role),
		}

		if m.Role == llm.RoleTool {
			// Tool results in Anthropic format
			msg["role"] = "user"
			msg["content"] = []map[string]interface{}{
				{
					"type":        "tool_result",
					"tool_use_id": m.ToolCallID,
					"content":     m.Content,
				},
			}
		} else if len(m.ToolCalls) > 0 {
			// Assistant message with tool calls
			content := []map[string]interface{}{}
			if m.Content != "" {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				var args interface{}
				if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
					args = map[string]interface{}{}
				}
				content = append(content, map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": args,
				})
			}
			msg["content"] = content
		} else {
			msg["content"] = m.Content
		}

		msgs = append(msgs, msg)
	}
	body["messages"] = msgs

	// Max tokens (required by Anthropic)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body["max_tokens"] = maxTokens

	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopWords) > 0 {
		body["stop_sequences"] = req.StopWords
	}

	// Tools
	if len(req.Tools) > 0 {
		var tools []map[string]interface{}
		for _, t := range req.Tools {
			if t.Type != "" {
				// Server-side tool (e.g., web_search).
				apiType := t.Type
				if apiType == "web_search" {
					apiType = "web_search_20250305" // Anthropic-specific version
				}
				tools = append(tools, map[string]interface{}{
					"type": apiType,
					"name": t.Name,
				})
			} else {
				tool := map[string]interface{}{
					"name":         t.Name,
					"description":  t.Description,
					"input_schema": t.Parameters,
				}
				tools = append(tools, tool)
			}
		}
		// Add cache_control to the last tool so the entire tool set is cached.
		if req.CacheSystem && len(tools) > 0 {
			tools[len(tools)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
		}
		body["tools"] = tools
	}

	if stream {
		body["stream"] = true
	}

	return body
}

// apiResponse represents the Anthropic messages API response.
type apiResponse struct {
	ID         string       `json:"id"`
	Type       string       `json:"type"`
	Role       string       `json:"role"`
	Content    []apiContent `json:"content"`
	Model      string       `json:"model"`
	StopReason string       `json:"stop_reason"`
	Usage      apiUsage     `json:"usage"`
}

type apiContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type apiUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type apiErrorWrapper struct {
	Type  string   `json:"type"`
	Error apiError `json:"error"`
}

// parseResponse converts an Anthropic API response to our format.
func (c *Client) parseResponse(resp *apiResponse) *llm.CompletionResponse {
	out := &llm.CompletionResponse{
		Model:      resp.Model,
		StopReason: resp.StopReason,
		Usage: llm.Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			out.Content += block.Text
		case "tool_use":
			args := string(block.Input)
			out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		case "server_tool_use", "web_search_tool_result":
			// Server-side tool results — handled internally by Claude.
		}
	}

	return out
}

// readError reads an error response from the API.
func (c *Client) readError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	var apiErr apiErrorWrapper
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("anthropic: API error %d: %s: %s", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
	}

	return fmt.Errorf("anthropic: API error %d: %s", resp.StatusCode, string(body))
}

// parseSSEStream parses Server-Sent Events from the Anthropic streaming API.
func (c *Client) parseSSEStream(body io.Reader, callback llm.StreamCallback) error {
	scanner := bufio.NewScanner(body)

	// Track tool calls being built across chunks
	var currentToolCall *llm.ToolCall
	var toolArgsBuilder strings.Builder
	var usage *llm.Usage
	var stopReason string

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Parse SSE event
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			callback(llm.StreamChunk{Done: true, StopReason: stopReason, Usage: usage})
			return nil
		}

		var event sseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				currentToolCall = &llm.ToolCall{
					ID:   event.ContentBlock.ID,
					Name: event.ContentBlock.Name,
				}
				toolArgsBuilder.Reset()
			}

		case "content_block_delta":
			if event.Delta != nil {
				switch event.Delta.Type {
				case "text_delta":
					callback(llm.StreamChunk{Delta: event.Delta.Text})
				case "input_json_delta":
					toolArgsBuilder.WriteString(event.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			if currentToolCall != nil {
				currentToolCall.Arguments = toolArgsBuilder.String()
				if currentToolCall.Arguments == "" {
					currentToolCall.Arguments = "{}"
				}
				callback(llm.StreamChunk{ToolCall: currentToolCall})
				currentToolCall = nil
				toolArgsBuilder.Reset()
			}

		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" {
				stopReason = event.Delta.StopReason
			}
			if event.Usage != nil {
				usage = &llm.Usage{
					InputTokens:  event.Usage.InputTokens,
					OutputTokens: event.Usage.OutputTokens,
				}
			}

		case "message_start":
			if event.Message != nil && event.Message.Usage.InputTokens > 0 {
				usage = &llm.Usage{
					InputTokens:  event.Message.Usage.InputTokens,
					OutputTokens: event.Message.Usage.OutputTokens,
				}
			}

		case "message_stop":
			callback(llm.StreamChunk{Done: true, StopReason: stopReason, Usage: usage})
			return nil

		case "error":
			errMsg := "unknown error"
			if event.Error != nil {
				errMsg = event.Error.Message
			}
			callback(llm.StreamChunk{Error: fmt.Errorf("anthropic: stream error: %s", errMsg)})
			return fmt.Errorf("anthropic: stream error: %s", errMsg)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("anthropic: reading stream: %w", err)
	}

	// If we got here without a message_stop, send done anyway
	callback(llm.StreamChunk{Done: true, StopReason: stopReason, Usage: usage})
	return nil
}

// SSE event types for Anthropic streaming.
type sseEvent struct {
	Type         string       `json:"type"`
	Delta        *sseDelta    `json:"delta,omitempty"`
	ContentBlock *sseBlock    `json:"content_block,omitempty"`
	Message      *apiResponse `json:"message,omitempty"`
	Usage        *apiUsage    `json:"usage,omitempty"`
	Error        *apiError    `json:"error,omitempty"`
	Index        int          `json:"index,omitempty"`
}

type sseDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type sseBlock struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
}
