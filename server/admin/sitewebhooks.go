/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type SiteWebhooksHandler struct {
	deps *Deps
}

func (h *SiteWebhooksHandler) List(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	rows, err := siteDB.Query(
		`SELECT id, name, secret, url, direction, is_enabled, last_triggered, created_at
		 FROM ho_webhooks ORDER BY created_at DESC`)
	if err != nil {
		writeJSON(w, http.StatusOK, []map[string]interface{}{})
		return
	}
	defer rows.Close()
	var webhooks []map[string]interface{}
	for rows.Next() {
		var id int
		var name, secret string
		var url, direction sql.NullString
		var isEnabled bool
		var lastTriggered sql.NullTime
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &secret, &url, &direction, &isEnabled, &lastTriggered, &createdAt); err != nil {
			continue
		}
		wh := map[string]interface{}{
			"id": id, "name": name, "secret": secret,
			"is_enabled": isEnabled, "created_at": createdAt,
		}
		if url.Valid {
			wh["url"] = url.String
		}
		if direction.Valid {
			wh["direction"] = direction.String
		} else {
			wh["direction"] = "incoming"
		}
		if lastTriggered.Valid {
			wh["last_triggered"] = lastTriggered.Time
		}
		webhooks = append(webhooks, wh)
	}
	if webhooks == nil {
		webhooks = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, webhooks)
}

func (h *SiteWebhooksHandler) Get(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	webhookID, err := strconv.Atoi(chi.URLParam(r, "webhookID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid webhook ID")
		return
	}
	var id int
	var name, secret string
	var url, direction sql.NullString
	var isEnabled bool
	var lastTriggered sql.NullTime
	var createdAt time.Time
	err = siteDB.QueryRow(
		`SELECT id, name, secret, url, direction, is_enabled, last_triggered, created_at
		 FROM ho_webhooks WHERE id = ?`, webhookID,
	).Scan(&id, &name, &secret, &url, &direction, &isEnabled, &lastTriggered, &createdAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	wh := map[string]interface{}{
		"id": id, "name": name, "secret": secret,
		"is_enabled": isEnabled, "created_at": createdAt,
	}
	if url.Valid {
		wh["url"] = url.String
	}
	if direction.Valid {
		wh["direction"] = direction.String
	} else {
		wh["direction"] = "incoming"
	}
	if lastTriggered.Valid {
		wh["last_triggered"] = lastTriggered.Time
	}

	// Load subscriptions.
	subRows, err := siteDB.Query(
		"SELECT event_type FROM ho_webhook_subscriptions WHERE webhook_id = ?", webhookID)
	if err == nil {
		var subs []string
		for subRows.Next() {
			var eventType string
			if err := subRows.Scan(&eventType); err == nil {
				subs = append(subs, eventType)
			}
		}
		subRows.Close()
		wh["subscriptions"] = subs
	}

	// Load recent logs.
	logRows, err := siteDB.Query(
		`SELECT id, direction, event_type, status_code, success, created_at
		 FROM ho_webhook_logs WHERE webhook_id = ? ORDER BY created_at DESC LIMIT 25`, webhookID)
	if err == nil {
		var logs []map[string]interface{}
		for logRows.Next() {
			var logID int
			var logDir, eventType string
			var statusCode sql.NullInt64
			var success bool
			var logCreatedAt time.Time
			if err := logRows.Scan(&logID, &logDir, &eventType, &statusCode, &success, &logCreatedAt); err == nil {
				log := map[string]interface{}{
					"id": logID, "direction": logDir, "event_type": eventType,
					"success": success, "created_at": logCreatedAt,
				}
				if statusCode.Valid {
					log["status_code"] = statusCode.Int64
				}
				logs = append(logs, log)
			}
		}
		logRows.Close()
		wh["logs"] = logs
	}

	writeJSON(w, http.StatusOK, wh)
}

func (h *SiteWebhooksHandler) Toggle(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	webhookID, err := strconv.Atoi(chi.URLParam(r, "webhookID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid webhook ID")
		return
	}
	_, err = siteDB.ExecWrite(
		"UPDATE ho_webhooks SET is_enabled = NOT is_enabled WHERE id = ?",
		webhookID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to toggle webhook")
		return
	}
	var isEnabled bool
	siteDB.QueryRow("SELECT is_enabled FROM ho_webhooks WHERE id = ?", webhookID).Scan(&isEnabled)
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": webhookID, "is_enabled": isEnabled})
}

func (h *SiteWebhooksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}
	webhookID, err := strconv.Atoi(chi.URLParam(r, "webhookID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid webhook ID")
		return
	}
	_, err = siteDB.ExecWrite(
		"DELETE FROM ho_webhooks WHERE id = ?",
		webhookID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete webhook")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

type createWebhookRequest struct {
	Name      string   `json:"name"`
	Direction string   `json:"direction"` // incoming or outgoing
	URL       string   `json:"url"`       // for outgoing
	Secret    string   `json:"secret"`    // HMAC signing secret
	Events    []string `json:"events"`    // event subscriptions
}

// Create adds a new webhook for a site.
func (h *SiteWebhooksHandler) Create(w http.ResponseWriter, r *http.Request) {
	_, siteDB := requireSiteDB(w, r, h.deps.SiteDBManager)
	if siteDB == nil {
		return
	}

	var req createWebhookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Direction == "" {
		req.Direction = "incoming"
	}
	if req.Direction == "outgoing" && req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required for outgoing webhooks")
		return
	}

	db := siteDB.Writer()
	result, err := db.Exec(
		"INSERT INTO ho_webhooks (name, direction, url, secret) VALUES (?, ?, ?, ?)",
		req.Name, req.Direction, req.URL, req.Secret,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create webhook")
		return
	}
	whID, _ := result.LastInsertId()

	// Add event subscriptions.
	for _, evt := range req.Events {
		db.Exec("INSERT INTO ho_webhook_subscriptions (webhook_id, event_type) VALUES (?, ?)", whID, evt)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"id": whID, "name": req.Name})
}
