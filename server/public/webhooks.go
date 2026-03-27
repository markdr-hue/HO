/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package public

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/events"
)

// IncomingWebhook handles POST /webhooks/{name} for receiving external webhook payloads.
// Validates HMAC-SHA256 signature from X-Webhook-Signature header.
func (h *Handler) IncomingWebhook(w http.ResponseWriter, r *http.Request) {
	site := getSite(r)
	if site == nil {
		writePublicError(w, http.StatusNotFound, "project not found")
		return
	}

	webhookName := chi.URLParam(r, "name")
	if webhookName == "" {
		writePublicError(w, http.StatusNotFound, "webhook name required")
		return
	}

	siteDB, err := h.deps.SiteDBManager.Open(site.ID)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Look up the webhook.
	var webhookID int
	var secret string
	var isEnabled bool
	err = siteDB.QueryRow(
		"SELECT id, secret, is_enabled FROM ho_webhooks WHERE name = ?",
		webhookName,
	).Scan(&webhookID, &secret, &isEnabled)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "webhook not found")
		return
	}

	if !isEnabled {
		writePublicError(w, http.StatusForbidden, "webhook is disabled")
		return
	}

	// Read the body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		writePublicError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	// Validate HMAC-SHA256 signature.
	signature := r.Header.Get("X-Webhook-Signature")
	if signature == "" {
		signature = r.Header.Get("X-Hub-Signature-256") // GitHub webhook format support
	}
	if secret != "" && signature != "" {
		// Strip "sha256=" prefix if present (GitHub format).
		sig := strings.TrimPrefix(signature, "sha256=")
		expectedMAC := computeHMAC(body, []byte(secret))
		providedMAC, err := hex.DecodeString(sig)
		if err != nil || !hmac.Equal(providedMAC, expectedMAC) {
			h.deps.Logger.Warn("webhook signature validation failed",
				"webhook", webhookName, "site_id", site.ID, "remote_addr", r.RemoteAddr)
			writePublicError(w, http.StatusUnauthorized, "invalid signature")
			return
		}
	}

	// Update last_triggered.
	siteDB.ExecWrite(
		"UPDATE ho_webhooks SET last_triggered = CURRENT_TIMESTAMP WHERE id = ?",
		webhookID,
	)

	// Log the webhook receipt.
	siteDB.ExecWrite(
		"INSERT INTO ho_webhook_logs (webhook_id, direction, event_type, payload, success) VALUES (?, 'incoming', 'webhook.received', ?, 1)",
		webhookID, string(body),
	)

	// Parse payload for brain context.
	var payload map[string]interface{}
	json.Unmarshal(body, &payload)

	// Publish event so the brain can react.
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventWebhookReceived, site.ID, map[string]interface{}{
			"webhook_id":   webhookID,
			"webhook_name": webhookName,
			"payload":      payload,
		}))
	}

	writePublicJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "webhook received",
	})
}

func computeHMAC(message, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}
