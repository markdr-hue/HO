/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/markdr-hue/HO/events"
)

const approvalKeyPrefix = "approval:"

// CheckApproval checks if an owner approval exists for the given action key.
// Returns true only if the owner explicitly approved.
func CheckApproval(db *sql.DB, actionKey string) bool {
	key := approvalKeyPrefix + actionKey

	var answer string
	err := db.QueryRow(`
		SELECT a.answer FROM ho_questions q
		JOIN ho_answers a ON a.question_id = q.id
		WHERE q.context = ?
		ORDER BY a.created_at DESC LIMIT 1
	`, key).Scan(&answer)
	if err != nil {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(answer))
	return lower == "approve" || lower == "yes" || lower == "approved" ||
		strings.HasPrefix(lower, "yes") || strings.HasPrefix(lower, "approve")
}

// isApprovalDenied checks if the owner explicitly denied an action.
func isApprovalDenied(db *sql.DB, actionKey string) bool {
	key := approvalKeyPrefix + actionKey

	var answer string
	err := db.QueryRow(`
		SELECT a.answer FROM ho_questions q
		JOIN ho_answers a ON a.question_id = q.id
		WHERE q.context = ?
		ORDER BY a.created_at DESC LIMIT 1
	`, key).Scan(&answer)
	if err != nil {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(answer))
	return lower == "deny" || lower == "no" || lower == "denied" ||
		lower == "reject" || lower == "rejected" || lower == "cancel"
}

// RequestApproval creates a high-urgency approval question, pauses the
// pipeline so the brain waits, and returns an error Result. When the owner
// answers via the admin UI the EventQuestionAnswered handler wakes the brain,
// which re-runs the current stage. On retry the tool calls CheckApproval
// again and proceeds if approved.
//
// If the owner already denied the action, a denial result is returned
// without pausing.
func RequestApproval(ctx *ToolContext, actionKey, question, detail string) *Result {
	// Already denied — don't pause, just tell the LLM.
	if isApprovalDenied(ctx.DB, actionKey) {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("ACTION_DENIED: The owner denied this action. %s", detail),
		}
	}

	key := approvalKeyPrefix + actionKey

	// Check if a pending question for this key already exists.
	var existingID int
	err := ctx.DB.QueryRow(
		"SELECT id FROM ho_questions WHERE context = ? AND status = 'pending'", key,
	).Scan(&existingID)

	if err == sql.ErrNoRows {
		// Create the approval question.
		result, insErr := ctx.DB.Exec(
			`INSERT INTO ho_questions (question, context, options, urgency, status, type)
			 VALUES (?, ?, 'Approve, Deny', 'high', 'pending', 'approval')`,
			question, key,
		)
		if insErr == nil {
			id, _ := result.LastInsertId()
			if ctx.Bus != nil {
				ctx.Bus.Publish(events.NewEvent(events.EventQuestionAsked, ctx.SiteID, map[string]interface{}{
					"id":       id,
					"question": question,
					"options":  "Approve, Deny",
					"urgency":  "high",
					"type":     "approval",
				}))
			}
		}
	}

	// Pause the pipeline so the brain waits for the answer.
	if _, err := ctx.DB.Exec(
		"UPDATE ho_pipeline_state SET paused = 1, pause_reason = 'awaiting_approval', updated_at = CURRENT_TIMESTAMP WHERE id = 1",
	); err != nil {
		slog.Error("failed to pause pipeline for approval", "action", actionKey, "error", err)
		return &Result{Success: false, Error: fmt.Sprintf("failed to pause pipeline: %v", err)}
	}

	return &Result{
		Success: false,
		Error: fmt.Sprintf(
			"APPROVAL_REQUIRED: %s This action requires owner approval. A question has been created in the admin panel. The pipeline will resume automatically when the owner responds.",
			detail,
		),
	}
}

// LogDestructiveAction records a destructive tool action in the ho_brain_log
// table for audit trail purposes. Called for Tier 2 actions (delete rows,
// delete files, delete endpoints, etc.) that don't require approval but
// should be tracked.
func LogDestructiveAction(ctx *ToolContext, tool, action, target string) {
	summary := fmt.Sprintf("Destructive: %s → %s on %q", tool, action, target)
	ctx.DB.Exec(
		"INSERT INTO ho_brain_log (event_type, summary) VALUES ('destructive_action', ?)",
		summary,
	)
}
