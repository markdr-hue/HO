/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package webhooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
)

// Dispatcher delivers outgoing webhook payloads when subscribed events fire.
type Dispatcher struct {
	siteDBMgr *db.SiteDBManager
	bus       *events.Bus
	logger    *slog.Logger
	client    *http.Client
}

// NewDispatcher creates an outgoing webhook dispatcher and subscribes to all events.
func NewDispatcher(siteDBMgr *db.SiteDBManager, bus *events.Bus) *Dispatcher {
	d := &Dispatcher{
		siteDBMgr: siteDBMgr,
		bus:       bus,
		logger:    slog.With("component", "webhook_dispatcher"),
		client:    &http.Client{Timeout: 15 * time.Second},
	}

	// Subscribe to all events and check for matching webhook subscriptions.
	bus.SubscribeAll(d.handleEvent)

	return d
}

// handleEvent checks if any outgoing webhooks are subscribed to this event type.
func (d *Dispatcher) handleEvent(event events.Event) {
	if event.SiteID == 0 {
		return
	}

	eventType := string(event.Type)

	// Get the site DB for this event's site.
	siteDB := d.siteDBMgr.Get(event.SiteID)
	if siteDB == nil {
		return // site DB not open, skip
	}

	// Query for outgoing webhooks subscribed to this event type.
	rows, err := siteDB.Query(
		`SELECT w.id, w.name, w.url, w.secret
		 FROM ho_webhooks w
		 JOIN ho_webhook_subscriptions ws ON ws.webhook_id = w.id
		 WHERE ws.event_type = ? AND w.is_enabled = 1 AND w.url IS NOT NULL AND w.url != ''`,
		eventType,
	)
	if err != nil {
		slog.Warn("webhook dispatch: subscription lookup failed", "event", eventType, "error", err)
		return
	}
	defer rows.Close()

	type target struct {
		id     int
		name   string
		url    string
		secret string
	}

	var targets []target
	for rows.Next() {
		var t target
		var secret sql.NullString
		if err := rows.Scan(&t.id, &t.name, &t.url, &secret); err != nil {
			continue
		}
		if secret.Valid {
			t.secret = secret.String
		}
		targets = append(targets, t)
	}
	// Deliver to each target in a goroutine (non-blocking).
	for _, t := range targets {
		go d.deliver(t.id, t.name, t.url, t.secret, event)
	}
}

// deliver sends the webhook payload with HMAC signature and retry logic.
func (d *Dispatcher) deliver(webhookID int, name, url, secret string, event events.Event) {
	payload, err := json.Marshal(map[string]interface{}{
		"event":     string(event.Type),
		"site_id":   event.SiteID,
		"payload":   event.Payload,
		"timestamp": event.Timestamp,
	})
	if err != nil {
		d.logger.Error("webhook: failed to marshal payload", "webhook", name, "error", err)
		return
	}

	// Retry up to 3 times with exponential backoff.
	var lastErr error
	var statusCode int
	var responseBody string

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 2 * time.Second) // 2s, 8s
		}

		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "HO-Webhook/1.0")

		// Sign with HMAC-SHA256 if secret is available.
		if secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(payload)
			sig := hex.EncodeToString(mac.Sum(nil))
			req.Header.Set("X-Webhook-Signature", "sha256="+sig)
		}

		resp, err := d.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		statusCode = resp.StatusCode
		responseBody = string(body)

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Success — log and update.
			d.logDelivery(webhookID, event.SiteID, string(event.Type), string(payload), statusCode, responseBody, true)
			if sdb := d.siteDBMgr.Get(event.SiteID); sdb != nil {
				sdb.ExecWrite("UPDATE ho_webhooks SET last_triggered = CURRENT_TIMESTAMP WHERE id = ?", webhookID)
			}
			d.logger.Info("webhook delivered", "webhook", name, "url", url, "status", statusCode)
			return
		}

		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// All retries failed.
	d.logDelivery(webhookID, event.SiteID, string(event.Type), string(payload), statusCode, responseBody, false)
	d.logger.Error("webhook delivery failed after retries", "webhook", name, "url", url, "error", lastErr)
}

func (d *Dispatcher) logDelivery(webhookID, siteID int, eventType, payload string, statusCode int, response string, success bool) {
	if sdb := d.siteDBMgr.Get(siteID); sdb != nil {
		sdb.ExecWrite(
			"INSERT INTO ho_webhook_logs (webhook_id, direction, event_type, payload, status_code, response, success) VALUES (?, 'outgoing', ?, ?, ?, ?, ?)",
			webhookID, eventType, payload, statusCode, response, success,
		)
	}
}
