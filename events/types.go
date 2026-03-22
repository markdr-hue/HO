/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package events

import (
	"encoding/json"
	"time"
)

// WSBroadcaster is implemented by the WebSocket hub to allow server-side
// components (e.g. the Actions Runner) to push messages to connected clients.
type WSBroadcaster interface {
	// BroadcastToRoom sends a JSON message to all clients in the specified room.
	// roomKey format: "siteID:endpointPath:roomName"
	BroadcastToRoom(roomKey string, msg json.RawMessage)
}

type EventType string

const (
	EventSiteCreated      EventType = "site.created"
	EventSiteUpdated      EventType = "site.updated"
	EventSiteDeleted      EventType = "site.deleted"
	EventBrainStarted     EventType = "brain.started"
	EventBrainStopped     EventType = "brain.stopped"
	EventBrainError       EventType = "brain.error"
	EventBrainModeChanged EventType = "brain.mode_changed"
	EventToolExecuted     EventType = "tool.executed"
	EventToolFailed       EventType = "tool.failed"
	EventChatMessage      EventType = "chat.message"
	EventProviderAdded    EventType = "provider.added"
	EventProviderUpdated  EventType = "provider.updated"
	EventQuestionAsked    EventType = "question.asked"
	EventQuestionAnswered EventType = "question.answered"
	EventBrainMessage     EventType = "brain.message"
	EventBrainToolStart   EventType = "brain.tool_start"
	EventBrainToolResult  EventType = "brain.tool_result"
	EventBrainStageChange EventType = "brain.stage_change"
	EventBrainProgress    EventType = "brain.progress"
	EventWebhookReceived  EventType = "webhook.received"
	EventWebhookDelivered EventType = "webhook.delivered"
	EventWebhookFailed    EventType = "webhook.failed"
	EventSecretStored     EventType = "secret.stored"
	EventDataInsert       EventType = "data.insert"
	EventDataUpdate       EventType = "data.update"
	EventDataDelete       EventType = "data.delete"
	EventAuthRegister     EventType = "auth.register"
	EventAuthLogin        EventType = "auth.login"
	EventPaymentCompleted EventType = "payment.completed"
	EventPaymentFailed    EventType = "payment.failed"
	EventWSMessage        EventType = "ws.message"
	EventSettingsChanged  EventType = "settings.changed"

	// Content lifecycle events.
	EventFileUploaded      EventType = "file.uploaded"
	EventPagePublished     EventType = "page.published"
	EventPageUpdated       EventType = "page.updated"
	EventSchemaCreated     EventType = "schema.created"
	EventSchemaAltered     EventType = "schema.altered"

	// Scheduled task lifecycle events.
	EventScheduledCompleted EventType = "scheduled.completed"
	EventScheduledFailed    EventType = "scheduled.failed"
)

type Event struct {
	Type      EventType              `json:"type"`
	SiteID    int                    `json:"site_id,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

func NewEvent(eventType EventType, siteID int, payload map[string]interface{}) Event {
	return Event{
		Type:      eventType,
		SiteID:    siteID,
		Payload:   payload,
		Timestamp: time.Now(),
	}
}
