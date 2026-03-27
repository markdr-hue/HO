/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package openai

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

const defaultEndpoint = "https://api.openai.com/v1/chat/completions"

// Client implements llm.Provider for the OpenAI-compatible API.
// Works with OpenAI, Google Gemini, Ollama, Z.ai, and other compatible endpoints.
type Client struct {
	name       string
	apiKey     string
	endpoint   string // full URL to the chat completions endpoint
	httpClient *http.Client
}

// Option configures the OpenAI client.
type Option func(*Client)

// WithBaseURL sets the API endpoint URL. If the URL ends with "/chat/completions",
// it's used as-is. Otherwise, "/v1/chat/completions" is appended.
func WithBaseURL(url string) Option {
	return func(c *Client) {
		url = strings.TrimRight(url, "/")
		if strings.HasSuffix(url, "/chat/completions") {
			c.endpoint = url
		} else {
			c.endpoint = url + "/v1/chat/completions"
		}
	}
}

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// New creates a new OpenAI-compatible provider.
func New(name, apiKey string, opts ...Option) *Client {
	c := &Client{
		name:       name,
		apiKey:     apiKey,
		endpoint:   defaultEndpoint,
		httpClient: &http.Client{Timeout: 6 * time.Minute},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) Name() string { return c.name }
func (c *Client) Type() string { return "openai" }

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
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	return c.parseResponse(&apiResp), nil
}

// Stream sends a streaming completion request and calls the callback for each chunk.
func (c *Client) Stream(ctx context.Context, req llm.CompletionRequest, callback llm.StreamCallback) error {
	body := c.buildRequestBody(req, true)

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("openai: create request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("openai: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.readError(resp)
	}

	return c.parseSSEStream(resp.Body, callback)
}

// setHeaders sets common headers for OpenAI-compatible API requests.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// buildRequestBody converts our generic request into the OpenAI API format.
func (c *Client) buildRequestBody(req llm.CompletionRequest, stream bool) map[string]interface{} {
	body := map[string]interface{}{
		"model": req.Model,
	}

	// Build messages array with OpenAI format
	var msgs []map[string]interface{}

	// If there's a system prompt, add it as a system message
	if req.System != "" {
		msgs = append(msgs, map[string]interface{}{
			"role":    "system",
			"content": req.System,
		})
	}

	for _, m := range req.Messages {
		msg := map[string]interface{}{
			"role": string(m.Role),
		}

		if m.Role == llm.RoleTool {
			// Tool result message
			msg["role"] = "tool"
			msg["content"] = m.Content
			msg["tool_call_id"] = m.ToolCallID
		} else if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
			// Assistant message with tool calls
			if m.Content != "" {
				msg["content"] = m.Content
			} else {
				msg["content"] = nil
			}
			var toolCalls []map[string]interface{}
			for _, tc := range m.ToolCalls {
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				})
			}
			msg["tool_calls"] = toolCalls
		} else {
			msg["content"] = m.Content
		}

		msgs = append(msgs, msg)
	}
	body["messages"] = msgs

	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopWords) > 0 {
		body["stop"] = req.StopWords
	}

	// Tools
	if len(req.Tools) > 0 {
		var tools []map[string]interface{}
		for _, t := range req.Tools {
			if t.Type == "web_search" {
				// OpenAI uses web_search_options as a top-level parameter
				// (works with search models like gpt-4o-search-preview, gpt-5-search-api).
				body["web_search_options"] = map[string]interface{}{}
				continue
			}
			tool := map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			}
			tools = append(tools, tool)
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
	}

	if stream {
		body["stream"] = true
	}

	return body
}

// OpenAI API response types.
type apiResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Model   string      `json:"model"`
	Choices []apiChoice `json:"choices"`
	Usage   apiUsage    `json:"usage"`
}

type apiChoice struct {
	Index        int        `json:"index"`
	Message      apiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

type apiMessage struct {
	Role      string        `json:"role"`
	Content   *string       `json:"content"`
	ToolCalls []apiToolCall `json:"tool_calls,omitempty"`
}

type apiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function apiFunction `json:"function"`
}

type apiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type apiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type apiErrorResponse struct {
	Error apiErrorDetail `json:"error"`
}

type apiErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// parseResponse converts an OpenAI API response to our format.
func (c *Client) parseResponse(resp *apiResponse) *llm.CompletionResponse {
	out := &llm.CompletionResponse{
		Model: resp.Model,
		Usage: llm.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		out.StopReason = choice.FinishReason

		if choice.Message.Content != nil {
			out.Content = *choice.Message.Content
		}

		for _, tc := range choice.Message.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}

	return out
}

// readError reads an error response from the API.
func (c *Client) readError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	var apiErr apiErrorResponse
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("openai: API error %d: %s: %s", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
	}

	return fmt.Errorf("openai: API error %d: %s", resp.StatusCode, string(body))
}

// Streaming SSE types for OpenAI.
type streamChunkResponse struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Model   string              `json:"model"`
	Choices []streamChunkChoice `json:"choices"`
	Usage   *apiUsage           `json:"usage,omitempty"`
}

type streamChunkChoice struct {
	Index        int              `json:"index"`
	Delta        streamChunkDelta `json:"delta"`
	FinishReason *string          `json:"finish_reason"`
}

type streamChunkDelta struct {
	Role      string        `json:"role,omitempty"`
	Content   *string       `json:"content,omitempty"`
	ToolCalls []apiToolCall `json:"tool_calls,omitempty"`
}

// parseSSEStream parses Server-Sent Events from the OpenAI streaming API.
func (c *Client) parseSSEStream(body io.Reader, callback llm.StreamCallback) error {
	scanner := bufio.NewScanner(body)

	// Track tool calls being built across multiple chunks
	toolCalls := make(map[int]*llm.ToolCall) // index -> partial tool call
	toolArgs := make(map[int]*strings.Builder)
	var usage *llm.Usage
	var stopReason string

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			// Flush any remaining tool calls
			for idx, tc := range toolCalls {
				if b, ok := toolArgs[idx]; ok {
					tc.Arguments = b.String()
					if tc.Arguments == "" {
						tc.Arguments = "{}"
					}
				}
				callback(llm.StreamChunk{ToolCall: tc})
			}
			callback(llm.StreamChunk{Done: true, StopReason: stopReason, Usage: usage})
			return nil
		}

		var chunk streamChunkResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Capture usage if present (OpenAI sends it in the final chunks when stream_options is set)
		if chunk.Usage != nil {
			usage = &llm.Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		// Text content
		if delta.Content != nil && *delta.Content != "" {
			callback(llm.StreamChunk{Delta: *delta.Content})
		}

		// Tool calls (built up incrementally)
		for _, tc := range delta.ToolCalls {
			// If this chunk has an ID, it's the start of a new tool call.
			if tc.ID != "" {
				toolCalls[len(toolCalls)] = &llm.ToolCall{
					ID:   tc.ID,
					Name: tc.Function.Name,
				}
				toolArgs[len(toolCalls)-1] = &strings.Builder{}
			}
			// Append function arguments.
			if tc.Function.Arguments != "" {
				lastIdx := len(toolCalls) - 1
				if lastIdx >= 0 {
					if b, ok := toolArgs[lastIdx]; ok {
						b.WriteString(tc.Function.Arguments)
					}
				}
			}
		}

		// Check for finish
		if choice.FinishReason != nil {
			// Map OpenAI finish reasons to our normalized stop_reason.
			switch *choice.FinishReason {
			case "length":
				stopReason = "max_tokens"
			case "stop":
				stopReason = "end"
			case "tool_calls":
				stopReason = "tool_use"
			default:
				stopReason = *choice.FinishReason
			}
			// Flush tool calls on stop
			if *choice.FinishReason == "tool_calls" {
				for idx, tc := range toolCalls {
					if b, ok := toolArgs[idx]; ok {
						tc.Arguments = b.String()
						if tc.Arguments == "" {
							tc.Arguments = "{}"
						}
					}
					callback(llm.StreamChunk{ToolCall: tc})
				}
				// Clear for next round
				toolCalls = make(map[int]*llm.ToolCall)
				toolArgs = make(map[int]*strings.Builder)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("openai: reading stream: %w", err)
	}

	// If we got here without [DONE], send done anyway
	callback(llm.StreamChunk{Done: true, StopReason: stopReason, Usage: usage})
	return nil
}
