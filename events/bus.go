/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package events

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

type HandlerFunc func(Event)

type subscription struct {
	id      uint64
	handler HandlerFunc
}

type Bus struct {
	mu       sync.RWMutex
	handlers map[EventType][]subscription
	global   []subscription
	nextID   atomic.Uint64
}

func NewBus() *Bus {
	return &Bus{
		handlers: make(map[EventType][]subscription),
	}
}

// Subscribe registers a handler for a specific event type and returns a
// subscription ID that can be used to unsubscribe.
func (b *Bus) Subscribe(eventType EventType, handler HandlerFunc) uint64 {
	id := b.nextID.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], subscription{id: id, handler: handler})
	return id
}

// SubscribeAll registers a handler that receives all events and returns a
// subscription ID that can be used to unsubscribe.
func (b *Bus) SubscribeAll(handler HandlerFunc) uint64 {
	id := b.nextID.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.global = append(b.global, subscription{id: id, handler: handler})
	return id
}

// Unsubscribe removes a handler by its subscription ID.
func (b *Bus) Unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for eventType, subs := range b.handlers {
		for i, s := range subs {
			if s.id == id {
				b.handlers[eventType] = append(subs[:i], subs[i+1:]...)
				return
			}
		}
	}

	for i, s := range b.global {
		if s.id == id {
			b.global = append(b.global[:i], b.global[i+1:]...)
			return
		}
	}
}

// Publish sends an event to all matching subscribers. Handlers run inline
// in a single goroutine — handlers must be non-blocking.
func (b *Bus) Publish(event Event) {
	b.mu.RLock()
	// Copy slices under read lock to avoid holding the lock during handler execution.
	typed := make([]subscription, len(b.handlers[event.Type]))
	copy(typed, b.handlers[event.Type])
	global := make([]subscription, len(b.global))
	copy(global, b.global)
	b.mu.RUnlock()

	for _, s := range typed {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("event handler panic", "type", event.Type, "panic", r)
				}
			}()
			s.handler(event)
		}()
	}

	for _, s := range global {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("global event handler panic", "type", event.Type, "panic", r)
				}
			}()
			s.handler(event)
		}()
	}
}
