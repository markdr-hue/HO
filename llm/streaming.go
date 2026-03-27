/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package llm

// StreamChunk represents a single chunk in a streaming response.
type StreamChunk struct {
	Delta      string      `json:"delta,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Done       bool        `json:"done"`
	StopReason string      `json:"stop_reason,omitempty"`
	Usage      *Usage      `json:"usage,omitempty"`
	Error      error       `json:"-"`
}

// ToolResult represents the result of a tool execution, streamed back to the
// frontend so tool cards can update in real-time.
type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error,omitempty"`
}

// Usage represents token usage information.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// StreamCallback is a function called for each chunk during streaming.
type StreamCallback func(StreamChunk)
