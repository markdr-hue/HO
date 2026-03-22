/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package actions

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
)

// AuditLogger subscribes to key events and logs them to ho_activity_log.
type AuditLogger struct {
	siteDBMgr *db.SiteDBManager
	logger    *slog.Logger
}

// NewAuditLogger creates an audit logger and subscribes to relevant events.
func NewAuditLogger(siteDBMgr *db.SiteDBManager, bus *events.Bus) *AuditLogger {
	al := &AuditLogger{
		siteDBMgr: siteDBMgr,
		logger:    slog.With("component", "audit_logger"),
	}

	// Subscribe to security and data-relevant events.
	for _, et := range []events.EventType{
		events.EventAuthRegister,
		events.EventAuthLogin,
		events.EventPaymentCompleted,
		events.EventPaymentFailed,
		events.EventDataInsert,
		events.EventDataUpdate,
		events.EventDataDelete,
		events.EventSchemaCreated,
		events.EventSchemaAltered,
		events.EventSecretStored,
		events.EventSettingsChanged,
	} {
		bus.Subscribe(et, al.handleEvent)
	}

	return al
}

func (al *AuditLogger) handleEvent(event events.Event) {
	if event.SiteID == 0 {
		return
	}

	siteDB := al.siteDBMgr.Get(event.SiteID)
	if siteDB == nil {
		return
	}

	summary := buildAuditSummary(event)
	details, _ := json.Marshal(event.Payload)

	_, err := siteDB.ExecWrite(
		"INSERT INTO ho_activity_log (event_type, summary, details) VALUES (?, ?, ?)",
		string(event.Type), summary, string(details),
	)
	if err != nil {
		al.logger.Debug("audit log write failed", "event", event.Type, "error", err)
	}
}

// buildAuditSummary creates a human-readable summary for common event types.
func buildAuditSummary(event events.Event) string {
	p := event.Payload

	switch event.Type {
	case events.EventAuthRegister:
		return fmt.Sprintf("New user registered in %v", p["table"])
	case events.EventAuthLogin:
		return fmt.Sprintf("User logged in (table: %v)", p["table"])
	case events.EventPaymentCompleted:
		return fmt.Sprintf("Payment completed: %v", p["session_id"])
	case events.EventPaymentFailed:
		return fmt.Sprintf("Payment failed: %v", p["session_id"])
	case events.EventDataInsert:
		return fmt.Sprintf("Row inserted in %v", p["table"])
	case events.EventDataUpdate:
		return fmt.Sprintf("Row updated in %v (id: %v)", p["table"], p["id"])
	case events.EventDataDelete:
		return fmt.Sprintf("Row deleted from %v (id: %v)", p["table"], p["id"])
	case events.EventSchemaCreated:
		return fmt.Sprintf("Table created: %v", p["table"])
	case events.EventSchemaAltered:
		return fmt.Sprintf("Table altered: %v", p["table"])
	case events.EventSecretStored:
		return fmt.Sprintf("Secret stored: %v", p["name"])
	case events.EventSettingsChanged:
		return "Settings changed"
	default:
		return string(event.Type)
	}
}
