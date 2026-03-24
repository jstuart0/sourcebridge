// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package events

import (
	"log/slog"
	"sync"
)

// Handler is a function that handles events.
type Handler func(Event)

// Bus is an in-process event bus.
type Bus struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{
		handlers: make(map[string][]Handler),
	}
}

// Subscribe registers a handler for an event type.
func (b *Bus) Subscribe(eventType string, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

// Publish emits an event to all registered handlers.
// Handlers subscribed with "*" receive all events.
func (b *Bus) Publish(event Event) {
	b.mu.RLock()
	handlers := make([]Handler, 0, len(b.handlers[event.Type])+len(b.handlers["*"]))
	handlers = append(handlers, b.handlers[event.Type]...)
	if event.Type != "*" {
		handlers = append(handlers, b.handlers["*"]...)
	}
	b.mu.RUnlock()

	for _, h := range handlers {
		go func(handler Handler) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("event handler panic", "type", event.Type, "error", r)
				}
			}()
			handler(event)
		}(h)
	}
}
