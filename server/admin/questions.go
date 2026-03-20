/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
)

// QuestionsHandler handles questions listing and answering.
type QuestionsHandler struct {
	deps *Deps
}

type question struct {
	ID         int       `json:"id"`
	SiteID     int       `json:"site_id"`
	Question   string    `json:"question"`
	Answer     *string   `json:"answer"`
	Status     string    `json:"status"`
	Type       *string   `json:"type,omitempty"`
	SecretName *string   `json:"secret_name,omitempty"`
	Fields     *string   `json:"fields,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// List returns questions across all sites, optionally filtered by status.
func (h *QuestionsHandler) List(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")

	// Get all site IDs from global DB.
	siteRows, err := h.deps.DB.Query("SELECT id FROM sites ORDER BY id")
	if err != nil {
		writeJSON(w, http.StatusOK, []question{})
		return
	}
	defer siteRows.Close()
	var siteIDs []int
	for siteRows.Next() {
		var id int
		if err := siteRows.Scan(&id); err == nil {
			siteIDs = append(siteIDs, id)
		}
	}

	var questions []question
	for _, siteID := range siteIDs {
		siteDB, err := h.deps.SiteDBManager.Open(siteID)
		if err != nil {
			continue
		}
		var rows *sql.Rows
		if statusFilter != "" {
			rows, err = siteDB.Query(
				`SELECT q.id, q.question,
				        (SELECT a.answer FROM ho_answers a WHERE a.question_id = q.id ORDER BY a.created_at DESC LIMIT 1),
				        q.status, q.type, q.secret_name, q.fields, q.created_at, q.created_at
				 FROM ho_questions q WHERE q.status = ? ORDER BY q.created_at DESC`,
				statusFilter,
			)
		} else {
			rows, err = siteDB.Query(
				`SELECT q.id, q.question,
				        (SELECT a.answer FROM ho_answers a WHERE a.question_id = q.id ORDER BY a.created_at DESC LIMIT 1),
				        q.status, q.type, q.secret_name, q.fields, q.created_at, q.created_at
				 FROM ho_questions q ORDER BY q.created_at DESC`,
			)
		}
		if err != nil {
			continue
		}
		for rows.Next() {
			var q question
			q.SiteID = siteID
			if err := rows.Scan(&q.ID, &q.Question, &q.Answer, &q.Status, &q.Type, &q.SecretName, &q.Fields, &q.CreatedAt, &q.UpdatedAt); err != nil {
				continue
			}
			questions = append(questions, q)
		}
		rows.Close()
	}

	if questions == nil {
		questions = []question{}
	}

	writeJSON(w, http.StatusOK, questions)
}

// ListBySite returns questions for a specific site, optionally filtered by status.
func (h *QuestionsHandler) ListBySite(w http.ResponseWriter, r *http.Request) {
	siteID, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	statusFilter := r.URL.Query().Get("status")

	var (
		rows *sql.Rows
		err  error
	)

	if statusFilter != "" {
		rows, err = siteDB.Query(
			`SELECT q.id, q.question, q.context, q.options, q.urgency, q.status, q.created_at, q.type, q.secret_name, q.fields,
			        (SELECT a.answer FROM ho_answers a WHERE a.question_id = q.id ORDER BY a.created_at DESC LIMIT 1) as answer
			 FROM ho_questions q WHERE q.status = ? ORDER BY q.created_at DESC`,
			statusFilter,
		)
	} else {
		rows, err = siteDB.Query(
			`SELECT q.id, q.question, q.context, q.options, q.urgency, q.status, q.created_at, q.type, q.secret_name, q.fields,
			        (SELECT a.answer FROM ho_answers a WHERE a.question_id = q.id ORDER BY a.created_at DESC LIMIT 1) as answer
			 FROM ho_questions q ORDER BY q.created_at DESC`,
		)
	}

	if err != nil {
		writeJSON(w, http.StatusOK, []siteQuestion{})
		return
	}
	defer rows.Close()

	var questions []siteQuestion
	for rows.Next() {
		var q siteQuestion
		q.SiteID = siteID
		if err := rows.Scan(&q.ID, &q.Question, &q.Context, &q.Options, &q.Urgency, &q.Status, &q.CreatedAt, &q.Type, &q.SecretName, &q.Fields, &q.Answer); err != nil {
			continue
		}
		questions = append(questions, q)
	}

	if questions == nil {
		questions = []siteQuestion{}
	}

	writeJSON(w, http.StatusOK, questions)
}

type siteQuestion struct {
	ID         int       `json:"id"`
	SiteID     int       `json:"site_id"`
	Question   string    `json:"question"`
	Context    *string   `json:"context"`
	Options    *string   `json:"options"`
	Urgency    string    `json:"urgency"`
	Status     string    `json:"status"`
	Answer     *string   `json:"answer"`
	CreatedAt  time.Time `json:"created_at"`
	Type       *string   `json:"type,omitempty"`
	SecretName *string   `json:"secret_name,omitempty"`
	Fields     *string   `json:"fields,omitempty"`
}

type answerRequest struct {
	Answer string `json:"answer"`
	SiteID int    `json:"site_id"`
}

// Answer provides an answer to a pending question.
func (h *QuestionsHandler) Answer(w http.ResponseWriter, r *http.Request) {
	questionID, err := strconv.Atoi(chi.URLParam(r, "questionID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid question ID")
		return
	}

	var req answerRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Answer == "" {
		writeError(w, http.StatusBadRequest, "answer is required")
		return
	}

	// Resolve which site DB contains this question.
	siteID := req.SiteID
	if siteID == 0 {
		// Search across all site DBs for the question.
		for _, id := range h.deps.SiteDBManager.OpenSiteIDs() {
			sdb, err := h.deps.SiteDBManager.Open(id)
			if err != nil {
				continue
			}
			var exists int
			if sdb.QueryRow("SELECT 1 FROM ho_questions WHERE id = ?", questionID).Scan(&exists) == nil {
				siteID = id
				break
			}
		}
	}
	if siteID == 0 {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(siteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open site database")
		return
	}

	// Check if this is a secret-type question or has structured fields.
	var qType, secretName, fieldsJSON sql.NullString
	if err := siteDB.QueryRow(
		"SELECT type, secret_name, fields FROM ho_questions WHERE id = ?", questionID,
	).Scan(&qType, &secretName, &fieldsJSON); err != nil && err != sql.ErrNoRows {
		h.deps.Logger.Error("failed to load question metadata", "question_id", questionID, "error", err)
	}

	// Handle structured multi-field answers.
	hasFields := fieldsJSON.Valid && fieldsJSON.String != "" && fieldsJSON.String != "[]"
	if hasFields {
		req.Answer = h.processFieldAnswer(siteDB, siteID, questionID, fieldsJSON.String, req.Answer)
	} else if qType.Valid && qType.String == "secret" && secretName.Valid && secretName.String != "" {
		// Single secret-type question (legacy path).
		encrypted, encErr := h.deps.Encryptor.Encrypt(req.Answer)
		if encErr != nil {
			h.deps.Logger.Error("failed to encrypt secret", "error", encErr)
			writeError(w, http.StatusInternalServerError, "failed to encrypt secret")
			return
		}
		if err := h.storeSecret(siteDB, secretName.String, encrypted, siteID, questionID); err != nil {
			h.deps.Logger.Error("failed to store secret", "name", secretName.String, "error", err)
		}
		req.Answer = fmt.Sprintf("Secret '%s' configured", secretName.String)
	}

	// Update question status
	_, err = siteDB.ExecWrite(
		"UPDATE ho_questions SET status = 'answered' WHERE id = ?",
		questionID,
	)
	if err != nil {
		h.deps.Logger.Error("failed to answer question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to answer question")
		return
	}

	// Insert answer record.
	if _, err := siteDB.ExecWrite(
		"INSERT INTO ho_answers (question_id, answer) VALUES (?, ?)",
		questionID, req.Answer,
	); err != nil {
		h.deps.Logger.Error("failed to insert answer", "question_id", questionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record answer")
		return
	}

	// Check if all pending questions for this site are now answered.
	// Only wake the brain when every question has been answered.
	var pendingCount int
	row := siteDB.QueryRow("SELECT COUNT(*) FROM ho_questions WHERE status = 'pending'")
	if err := row.Scan(&pendingCount); err != nil {
		pendingCount = -1 // unknown, don't wake
	}

	if h.deps.Bus != nil {
		if pendingCount == 0 {
			// All answered — collect all answers and wake the brain.
			rows, err := siteDB.Query(
				`SELECT q.question, a.answer FROM ho_questions q
				 JOIN ho_answers a ON a.question_id = q.id
				 WHERE q.status = 'answered'
				 ORDER BY q.id`)
			if err == nil {
				var parts []string
				for rows.Next() {
					var question, answer string
					if rows.Scan(&question, &answer) == nil {
						parts = append(parts, question+": "+answer)
					}
				}
				rows.Close()
				combined := strings.Join(parts, "\n")
				h.deps.Bus.Publish(events.NewEvent(events.EventQuestionAnswered, siteID, map[string]interface{}{
					"question_id": questionID,
					"answer":      combined,
				}))
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":      questionID,
		"status":  "answered",
		"pending": pendingCount,
	})
}

// storeSecret upserts an encrypted secret and publishes an event.
func (h *QuestionsHandler) storeSecret(siteDB *db.SiteDB, secretName, encrypted string, siteID, questionID int) error {
	_, err := siteDB.ExecWrite(
		`INSERT INTO ho_secrets (name, value_encrypted, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(name) DO UPDATE SET value_encrypted = excluded.value_encrypted, updated_at = CURRENT_TIMESTAMP`,
		secretName, encrypted,
	)
	if err != nil {
		return err
	}
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventSecretStored, siteID, map[string]interface{}{
			"name":        secretName,
			"question_id": questionID,
		}))
	}
	return nil
}

// processFieldAnswer handles structured multi-field answers, storing secrets
// and memory values for each field. Returns the summary answer string.
func (h *QuestionsHandler) processFieldAnswer(siteDB *db.SiteDB, siteID, questionID int, fieldsJSONStr, rawAnswer string) string {
	var fieldDefs []struct {
		Name       string `json:"name"`
		Label      string `json:"label"`
		Type       string `json:"type"`
		SecretName string `json:"secret_name"`
	}
	if err := json.Unmarshal([]byte(fieldsJSONStr), &fieldDefs); err != nil {
		h.deps.Logger.Error("failed to parse field definitions", "question_id", questionID, "error", err)
		return rawAnswer
	}

	var fieldValues map[string]string
	if err := json.Unmarshal([]byte(rawAnswer), &fieldValues); err != nil {
		h.deps.Logger.Error("failed to parse field values", "question_id", questionID, "error", err)
		return rawAnswer
	}

	var configured []string
	for _, fd := range fieldDefs {
		val, ok := fieldValues[fd.Name]
		if !ok || val == "" {
			continue
		}
		if fd.Type == "secret" && fd.SecretName != "" {
			encrypted, encErr := h.deps.Encryptor.Encrypt(val)
			if encErr != nil {
				h.deps.Logger.Error("failed to encrypt field secret", "field", fd.Name, "error", encErr)
				continue
			}
			if err := h.storeSecret(siteDB, fd.SecretName, encrypted, siteID, questionID); err != nil {
				h.deps.Logger.Error("failed to store field secret", "field", fd.Name, "error", err)
				continue
			}
		} else {
			if _, err := siteDB.ExecWrite(
				`INSERT INTO ho_memory (key, value, category) VALUES (?, ?, 'field_answer')
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
				fd.Name, val,
			); err != nil {
				h.deps.Logger.Error("failed to store field memory", "field", fd.Name, "error", err)
				continue
			}
		}
		configured = append(configured, fd.Label)
	}
	return "Configured: " + strings.Join(configured, ", ")
}
