/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"database/sql"
	"encoding/json"

	"github.com/markdr-hue/HO/llm"
)

// --- Checkpoint helpers ---

// saveCheckpointMessages persists the current conversation state to ho_pipeline_state.
func (w *PipelineWorker) saveCheckpointMessages(messages []llm.Message) {
	data, err := json.Marshal(messages)
	if err != nil {
		w.logger.Warn("failed to serialize checkpoint messages", "error", err)
		return
	}
	if _, err := w.siteDB.ExecWrite(
		"UPDATE ho_pipeline_state SET checkpoint_messages = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
		string(data),
	); err != nil {
		w.logger.Warn("failed to save checkpoint messages", "error", err)
	} else {
		w.logger.Info("checkpoint saved", "messages", len(messages))
	}
}

// loadCheckpointMessages restores conversation state from ho_pipeline_state.
func (w *PipelineWorker) loadCheckpointMessages() []llm.Message {
	var raw sql.NullString
	if err := w.siteDB.Reader().QueryRow(
		"SELECT checkpoint_messages FROM ho_pipeline_state WHERE id = 1",
	).Scan(&raw); err != nil || !raw.Valid || raw.String == "" {
		return nil
	}
	var messages []llm.Message
	if err := json.Unmarshal([]byte(raw.String), &messages); err != nil {
		w.logger.Warn("failed to deserialize checkpoint messages", "error", err)
		return nil
	}
	w.logger.Info("checkpoint restored", "messages", len(messages))
	return messages
}

// persistBuildTokens saves cumulative token count to pipeline state.
func (w *PipelineWorker) persistBuildTokens(tokens int) {
	w.siteDB.ExecWrite(
		"UPDATE ho_pipeline_state SET total_build_tokens = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1",
		tokens,
	)
}
