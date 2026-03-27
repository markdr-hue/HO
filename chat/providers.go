/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package chat

import (
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/llm/anthropic"
	"github.com/markdr-hue/HO/llm/openai"
)

// createAnthropicProvider builds an Anthropic llm.Provider with optional base URL.
func createAnthropicProvider(name, apiKey, baseURL string) llm.Provider {
	var opts []anthropic.Option
	if baseURL != "" {
		opts = append(opts, anthropic.WithBaseURL(baseURL))
	}
	return anthropic.New(name, apiKey, opts...)
}

// createOpenAIProvider builds an OpenAI llm.Provider with optional base URL.
func createOpenAIProvider(name, apiKey, baseURL string) llm.Provider {
	var opts []openai.Option
	if baseURL != "" {
		opts = append(opts, openai.WithBaseURL(baseURL))
	}
	return openai.New(name, apiKey, opts...)
}
