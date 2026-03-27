/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"fmt"
	"time"

	"github.com/markdr-hue/HO/chat"
	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/llm"
)

// handleChatWake runs a targeted LLM call with write tools in response to
// a user chat message during monitoring. This lets the brain fix issues
// the owner reports without restarting the full pipeline.
func (w *PipelineWorker) handleChatWake(ctx context.Context, userMessage string) {
	start := time.Now()

	// Acquire semaphore to prevent concurrent execution with monitoring ticks.
	select {
	case w.semaphore <- struct{}{}:
		defer func() { <-w.semaphore }()
	case <-ctx.Done():
		return
	}

	w.publishBrainMessage("Working on your request...")

	site, _ := models.GetSiteByID(w.deps.DB.DB, w.siteID)
	plan, _ := w.loadPlan()

	// Pick lite or full mode based on the user's message content.
	fullMode := chatWakeNeedsFullMode(userMessage)
	var cfg StageConfig
	var sysPrompt string
	if fullMode {
		cfg = ChatWakeConfig
		sysPrompt = buildChatWakePrompt(site, w.siteDB.Reader(), userMessage, plan, w.ownerName())
		w.logger.Info("chat-wake: using full mode")
	} else {
		cfg = ChatWakeLiteConfig
		sysPrompt = buildChatWakeLitePrompt(site, w.siteDB.Reader(), userMessage, plan, w.ownerName())
		w.logger.Info("chat-wake: using lite mode")
	}

	// Load recent conversation history so the LLM has context about prior exchanges.
	recentHistory, _ := chat.LoadHistory(w.siteDB.Reader(), "admin", 3)
	messages := make([]llm.Message, 0, len(recentHistory)+1)
	for _, msg := range recentHistory {
		// Only include user and assistant text messages (skip tool call/result noise).
		if (msg.Role == llm.RoleUser || msg.Role == llm.RoleAssistant) && msg.Content != "" {
			messages = append(messages, msg)
		}
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: userMessage})

	w.maxToolResultOverride = 4000
	result, iterErr := w.runStage(ctx, StageExecution{
		Config:       cfg,
		SystemPrompt: sysPrompt,
		Messages:     messages,
	})
	w.maxToolResultOverride = 0
	if iterErr != nil {
		w.logger.Error("chat-wake: tool loop error", "error", iterErr)
		w.publishBrainMessage("Something went wrong while processing your request. Try again or rephrase.")
	} else if result.Content == "" && result.ToolCalls > 0 {
		// The LLM made changes but didn't produce a final text summary
		// (ran out of iterations while still calling tools). Let the owner know.
		w.publishBrainMessage("Done. Changes have been applied.")
	}

	wakeMode := "lite"
	if fullMode {
		wakeMode = "full"
	}
	w.logBrainEvent("chat_wake", fmt.Sprintf("Responded to owner chat message (mode=%s)", wakeMode), userMessage, result.Tokens, result.Model, time.Since(start).Milliseconds())
}
