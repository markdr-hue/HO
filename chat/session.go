/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/security"
	"github.com/markdr-hue/HO/tools"
)

const (
	chatMaxToolIterations = 10
	historyLimit          = 50
)

// SessionDeps bundles the external dependencies needed by a chat session.
type SessionDeps struct {
	DB            *sql.DB // global database (users, providers, sites)
	SiteDBManager *db.SiteDBManager
	LLMRegistry   *llm.Registry
	ToolRegistry  *tools.Registry
	ToolExecutor  *tools.Executor
	Bus           *events.Bus
	Logger        *slog.Logger
	Encryptor     *security.Encryptor
}

// Session represents an ongoing chat conversation for a specific site.
type Session struct {
	ID           string
	SiteID       int
	siteDB       *sql.DB // per-site read pool
	siteDBWriter *sql.DB // per-site write pool
	deps         SessionDeps
}

// NewSession creates a new Session bound to the given site.
func NewSession(id string, siteID int, deps SessionDeps) (*Session, error) {
	siteDB, err := deps.SiteDBManager.Open(siteID)
	if err != nil {
		return nil, fmt.Errorf("chat: open site db %d: %w", siteID, err)
	}
	return &Session{
		ID:           id,
		SiteID:       siteID,
		siteDB:       siteDB.Reader(),
		siteDBWriter: siteDB.Writer(),
		deps:         deps,
	}, nil
}

// Send processes a user message: it persists the message, resolves the LLM
// provider, streams the response (handling tool-call loops), persists the
// assistant reply, and publishes a chat.message event.
func (s *Session) Send(ctx context.Context, message string, onChunk llm.StreamCallback) error {
	logger := s.deps.Logger.With("session_id", s.ID, "site_id", s.SiteID)

	// 1. Save the incoming user message.
	userMsg := llm.Message{Role: llm.RoleUser, Content: message}
	if err := SaveMessage(s.siteDBWriter, s.ID, userMsg); err != nil {
		return fmt.Errorf("chat: save user message: %w", err)
	}

	// Publish user message event so the brain can react (e.g. chat-wake).
	if s.deps.Bus != nil {
		s.deps.Bus.Publish(events.NewEvent(events.EventChatMessage, s.SiteID, map[string]interface{}{
			"session_id": s.ID,
			"role":       "user",
			"content":    message,
		}))
	}

	// In monitoring mode with admin session, the brain's handleChatWake handles
	// the request. Skip the chat LLM to avoid duplicate work, conflicting edits,
	// and keeping the SSE stream open (which blocks the chat input).
	if s.ID == "admin" {
		site, err := models.GetSiteByID(s.deps.DB, s.SiteID)
		if err == nil && site.Mode == "monitoring" {
			onChunk(llm.StreamChunk{Done: true})
			return nil
		}
	}

	// 2. Load recent chat history.
	history, err := LoadHistory(s.siteDB, s.ID, historyLimit)
	if err != nil {
		return fmt.Errorf("chat: load history: %w", err)
	}

	// 3. Build system prompt.
	systemPrompt := s.buildSystemPrompt()

	// 4. Convert history to messages (already in llm.Message format).
	messages := history

	// 5. Get tool definitions from registry (filtered to chat-safe set).
	toolDefs := s.deps.ToolRegistry.ToLLMToolsFiltered(tools.ChatToolSet)

	// 6. Resolve the default LLM provider from the database.
	provider, modelID, err := s.resolveProvider()
	if err != nil {
		return fmt.Errorf("chat: resolve provider: %w", err)
	}

	// 7-8. Stream with tool-call loop.
	if err := s.streamWithTools(ctx, provider, modelID, systemPrompt, messages, toolDefs, onChunk, logger); err != nil {
		return fmt.Errorf("chat: stream: %w", err)
	}

	return nil
}

// resolveProvider looks up the model assigned to this site, decrypts its
// provider's API key, and creates a live llm.Provider instance via the registry.
func (s *Session) resolveProvider() (llm.Provider, string, error) {
	model, dbProvider, err := models.GetModelForSite(s.deps.DB, s.SiteID)
	if err != nil {
		return nil, "", fmt.Errorf("get model for site %d: %w", s.SiteID, err)
	}

	// Try the live registry first (provider may already be registered).
	if p, err := s.deps.LLMRegistry.Get(dbProvider.Name); err == nil {
		return p, model.ModelID, nil
	}

	// Provider not in the live registry -- decrypt the API key and register it.
	var apiKey string
	if dbProvider.APIKeyEncrypted != nil && *dbProvider.APIKeyEncrypted != "" {
		apiKey, err = s.deps.Encryptor.Decrypt(*dbProvider.APIKeyEncrypted)
		if err != nil {
			return nil, "", fmt.Errorf("decrypt API key for %q: %w", dbProvider.Name, err)
		}
	} else if dbProvider.RequiresAPIKey() {
		return nil, "", fmt.Errorf("provider %q has no API key configured", dbProvider.Name)
	}

	var baseURL string
	if dbProvider.BaseURL != nil {
		baseURL = *dbProvider.BaseURL
	}

	// Use the ProviderFactory pattern: create and register on-the-fly.
	provider := createProvider(dbProvider.Name, dbProvider.ProviderType, apiKey, baseURL)
	if provider == nil {
		return nil, "", fmt.Errorf("unsupported provider type: %s", dbProvider.ProviderType)
	}
	s.deps.LLMRegistry.Register(dbProvider.Name, provider)

	return provider, model.ModelID, nil
}

// createProvider builds a live llm.Provider for the given type.
// It imports the concrete provider packages to avoid the need for an external
// factory function at the chat level.
func createProvider(name, providerType, apiKey, baseURL string) llm.Provider {
	switch strings.ToLower(providerType) {
	case "anthropic":
		return createAnthropicProvider(name, apiKey, baseURL)
	case "openai":
		return createOpenAIProvider(name, apiKey, baseURL)
	default:
		return nil
	}
}

// streamWithTools runs the LLM streaming loop, executing tool calls between
// rounds until the model produces a final text response or the iteration
// limit is reached.
func (s *Session) streamWithTools(
	ctx context.Context,
	provider llm.Provider,
	modelID string,
	systemPrompt string,
	messages []llm.Message,
	toolDefs []llm.ToolDef,
	onChunk llm.StreamCallback,
	logger *slog.Logger,
) error {
	iteration := 0
	llmLogger := llm.NewDBLLMLogger(s.siteDBWriter)
	defer llmLogger.Close()
	loggedProvider := llm.NewLoggedProvider(provider, llmLogger, "chat", s.ID, &iteration)

	for iteration = 0; iteration < chatMaxToolIterations; iteration++ {
		req := llm.CompletionRequest{
			Model:     modelID,
			System:    systemPrompt,
			Messages:  messages,
			Tools:     toolDefs,
			MaxTokens: 8192,
		}

		// Collect the full response text and any tool calls during streaming.
		var (
			responseText strings.Builder
			toolCalls    []llm.ToolCall
		)

		err := loggedProvider.Stream(ctx, req, func(chunk llm.StreamChunk) {
			if chunk.Error != nil {
				onChunk(chunk)
				return
			}
			if chunk.Delta != "" {
				responseText.WriteString(chunk.Delta)
				onChunk(chunk) // forward text to caller
			}
			if chunk.ToolCall != nil {
				toolCalls = append(toolCalls, *chunk.ToolCall)
				// Notify caller that a tool call is starting.
				onChunk(chunk)
			}
			if chunk.Done {
				// Do NOT forward "done" yet; we may need another round.
			}
		})
		if err != nil {
			return fmt.Errorf("stream (iteration %d): %w", iteration, err)
		}

		// Append the assistant message to the running conversation.
		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   responseText.String(),
			ToolCalls: toolCalls,
		}
		messages = append(messages, assistantMsg)

		// If no tool calls, we are done.
		if len(toolCalls) == 0 {
			// Save the final assistant response.
			if err := SaveMessage(s.siteDBWriter, s.ID, assistantMsg); err != nil {
				logger.Error("failed to save assistant message", "error", err)
			}

			// Publish event.
			if s.deps.Bus != nil {
				s.deps.Bus.Publish(events.NewEvent(events.EventChatMessage, s.SiteID, map[string]interface{}{
					"session_id": s.ID,
					"role":       "assistant",
					"content":    responseText.String(),
				}))
			}

			// Signal completion to the caller.
			onChunk(llm.StreamChunk{Done: true})
			return nil
		}

		// Save the assistant message that contains tool calls.
		if err := SaveMessage(s.siteDBWriter, s.ID, assistantMsg); err != nil {
			logger.Error("failed to save assistant tool-call message", "error", err)
		}

		// Execute each tool call and build result messages.
		toolCtx := &tools.ToolContext{
			DB:        s.siteDBWriter,
			GlobalDB:  s.deps.DB,
			SiteID:    s.SiteID,
			Logger:    logger,
			Bus:       s.deps.Bus,
			Encryptor: s.deps.Encryptor,
		}

		for _, tc := range toolCalls {
			logger.Info("executing tool", "tool", tc.Name, "id", tc.ID)

			// Validate tool is in the allowed chat set.
			if !tools.ChatToolSet[tc.Name] {
				logger.Warn("tool not allowed in chat", "tool", tc.Name)
				resultJSON := fmt.Sprintf(`{"success":false,"error":"tool '%s' is not available"}`, tc.Name)
				onChunk(llm.StreamChunk{
					ToolResult: &llm.ToolResult{ID: tc.ID, Name: tc.Name, Result: resultJSON, IsError: true},
				})
				messages = append(messages, llm.Message{Role: llm.RoleTool, Content: resultJSON, ToolCallID: tc.ID})
				continue
			}

			// Parse arguments from JSON string.
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				args = map[string]interface{}{}
			}

			toolExecCtx, toolCancel := context.WithTimeout(ctx, 2*time.Minute)
			resultJSON, toolErr := s.deps.ToolExecutor.Execute(toolExecCtx, toolCtx, tc.Name, args)
			toolCancel()
			if toolErr != nil {
				logger.Error("tool execution error", "tool", tc.Name, "error", toolErr)
				resultJSON = fmt.Sprintf(`{"success":false,"error":%q}`, toolErr.Error())
			}

			// Stream tool result to the frontend so cards update in real-time.
			onChunk(llm.StreamChunk{
				ToolResult: &llm.ToolResult{
					ID:      tc.ID,
					Name:    tc.Name,
					Result:  resultJSON,
					IsError: toolErr != nil,
				},
			})

			toolResultMsg := llm.Message{
				Role:       llm.RoleTool,
				Content:    resultJSON,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolResultMsg)

			// Save the tool result message.
			if err := SaveMessage(s.siteDBWriter, s.ID, toolResultMsg); err != nil {
				logger.Error("failed to save tool result message", "error", err)
			}
		}

		// Loop around to call the LLM again with the tool results.
		logger.Debug("tool loop iteration complete", "iteration", iteration, "tool_calls", len(toolCalls))
	}

	// If we exhausted iterations, send done and return an error.
	onChunk(llm.StreamChunk{Done: true})
	return fmt.Errorf("chat: exceeded maximum tool iterations (%d)", chatMaxToolIterations)
}

// buildSystemPrompt constructs the system prompt for the chat session.
// Includes site context (pages, memory) so the LLM can answer
// user questions about their site meaningfully.
func (s *Session) buildSystemPrompt() string {
	return BuildChatSystemPrompt(s.deps.DB, s.siteDB, s.SiteID, s.deps.ToolRegistry, tools.ChatToolSet)
}
