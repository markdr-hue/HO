/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package llm

import "context"

// Provider is the interface that LLM providers must implement.
type Provider interface {
	// Name returns the display name of this provider instance.
	Name() string

	// Type returns the provider type identifier (e.g. "anthropic", "openai").
	Type() string

	// Complete sends a completion request and returns the full response.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)

	// Stream sends a completion request and streams the response via callback.
	Stream(ctx context.Context, req CompletionRequest, callback StreamCallback) error

	// Ping checks connectivity to the provider using the given model ID.
	Ping(ctx context.Context, model string) error
}

// CompletionRequest is the input to a completion call.
type CompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	System      string    `json:"system,omitempty"`
	Tools       []ToolDef `json:"tools,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	StopWords   []string  `json:"stop,omitempty"`
	CacheSystem bool      `json:"-"` // Anthropic prompt caching: cache system prompt + tools
}

// CompletionResponse is the output from a completion call.
type CompletionResponse struct {
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Usage      Usage      `json:"usage"`
	Model      string     `json:"model"`
	StopReason string     `json:"stop_reason,omitempty"`
}
