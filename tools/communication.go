/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"database/sql"
	"fmt"

	"github.com/markdr-hue/HO/events"
)

// ---------------------------------------------------------------------------
// manage_communication — unified communication manager
// ---------------------------------------------------------------------------

type CommunicationTool struct{}

func (t *CommunicationTool) Name() string { return "manage_communication" }
func (t *CommunicationTool) Description() string {
	return "Ask the site owner a question or check for answers."
}
func (t *CommunicationTool) Guide() string {
	return `### Owner Communication (manage_communication)
- ask: send a question to the site owner. Supports type="secret" with secret_name for API keys (auto-encrypted on answer).
- fields: structured multi-input questions via JSON array of {name, label, type, secret_name}.
- check: poll for answers. Omit question_id to check all pending questions.
`
}
func (t *CommunicationTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":      map[string]interface{}{"type": "string", "enum": []string{"ask", "check"}, "description": "Action to perform"},
			"question":    map[string]interface{}{"type": "string", "description": "The question to ask the admin (required for ask)"},
			"context":     map[string]interface{}{"type": "string", "description": "Additional context to help the admin answer"},
			"options":     map[string]interface{}{"type": "string", "description": "JSON array of suggested answer options"},
			"urgency":     map[string]interface{}{"type": "string", "description": "Urgency level: low, normal, high", "enum": []string{"low", "normal", "high"}},
			"type":        map[string]interface{}{"type": "string", "description": "Question type: 'text' (default) or 'secret' (for API keys/tokens — value will be encrypted)", "enum": []string{"text", "secret"}},
			"secret_name": map[string]interface{}{"type": "string", "description": "For type='secret': the name to store the secret under (e.g. 'openai_api_key')"},
			"fields":      map[string]interface{}{"type": "string", "description": "JSON array of input fields for structured questions: [{\"name\": \"client_id\", \"label\": \"Client ID\", \"type\": \"text\"}, {\"name\": \"client_secret\", \"label\": \"Client Secret\", \"type\": \"secret\", \"secret_name\": \"my_secret\"}]. Each field creates a labeled input box."},
			"question_id": map[string]interface{}{"type": "number", "description": "Specific question ID to check (optional for check, omit to check all pending)"},
		},
		"required": []string{},
	}
}

func (t *CommunicationTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"ask":   t.ask,
		"check": t.check,
	}, nil)
}

func (t *CommunicationTool) ask(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	question, _ := args["question"].(string)
	if question == "" {
		return &Result{Success: false, Error: "question is required"}, nil
	}
	qContext, _ := args["context"].(string)
	options, _ := args["options"].(string)
	urgency, _ := args["urgency"].(string)
	if urgency == "" {
		urgency = "normal"
	}
	qType, _ := args["type"].(string)
	if qType == "" {
		qType = "text"
	}
	secretName, _ := args["secret_name"].(string)
	if qType == "secret" && secretName == "" {
		return &Result{Success: false, Error: "secret_name is required when type is 'secret'"}, nil
	}
	fields, _ := args["fields"].(string)

	result, err := ctx.DB.Exec(
		"INSERT INTO ho_questions (question, context, options, urgency, type, secret_name, fields) VALUES (?, ?, ?, ?, ?, ?, ?)",
		question, qContext, options, urgency, qType, secretName, fields,
	)
	if err != nil {
		return nil, fmt.Errorf("creating question: %w", err)
	}

	id, _ := result.LastInsertId()

	// Publish question.asked event so the chat UI can show an inline answer card.
	if ctx.Bus != nil {
		ctx.Bus.Publish(events.NewEvent(events.EventQuestionAsked, ctx.SiteID, map[string]interface{}{
			"id":          id,
			"question":    question,
			"context":     qContext,
			"options":     options,
			"urgency":     urgency,
			"type":        qType,
			"secret_name": secretName,
			"fields":      fields,
		}))
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"id":       id,
		"question": question,
		"urgency":  urgency,
		"type":     qType,
	}}, nil
}

func (t *CommunicationTool) check(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	// Check for a specific question.
	if qid, ok := args["question_id"].(float64); ok && qid > 0 {
		var question, status string
		var answer sql.NullString
		var answeredAt sql.NullTime

		err := ctx.DB.QueryRow(`
			SELECT q.question, q.status, a.answer, a.created_at
			FROM ho_questions q
			LEFT JOIN ho_answers a ON a.question_id = q.id
			WHERE q.id = ?
			ORDER BY a.created_at DESC LIMIT 1`,
			int64(qid),
		).Scan(&question, &status, &answer, &answeredAt)
		if err == sql.ErrNoRows {
			return &Result{Success: false, Error: "question not found"}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("checking answer: %w", err)
		}

		data := map[string]interface{}{
			"question_id": int64(qid),
			"question":    question,
			"status":      status,
			"answered":    answer.Valid,
		}
		if answer.Valid {
			data["answer"] = answer.String
			data["answered_at"] = answeredAt.Time
		}
		return &Result{Success: true, Data: data}, nil
	}

	// List all answered questions that the brain hasn't seen yet.
	rows, err := ctx.DB.Query(`
		SELECT q.id, q.question, q.status, a.answer, a.created_at
		FROM ho_questions q
		LEFT JOIN ho_answers a ON a.question_id = q.id
		WHERE q.status IN ('pending', 'answered')
		ORDER BY q.created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("checking answers: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var qID int
		var question, status string
		var answer sql.NullString
		var answeredAt sql.NullTime
		if err := rows.Scan(&qID, &question, &status, &answer, &answeredAt); err != nil {
			return nil, fmt.Errorf("scanning answer: %w", err)
		}
		entry := map[string]interface{}{
			"question_id": qID,
			"question":    question,
			"status":      status,
			"answered":    answer.Valid,
		}
		if answer.Valid {
			entry["answer"] = answer.String
			entry["answered_at"] = answeredAt.Time
		}
		results = append(results, entry)
	}

	return &Result{Success: true, Data: results}, nil
}
