// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package events

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Handler is a function that handles events.
type Handler func(Event)

// Bus is an in-process event bus.
type Bus struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
	wg       sync.WaitGroup
	closed   atomic.Bool
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
	if b.closed.Load() {
		return
	}
	b.mu.RLock()
	handlers := make([]Handler, 0, len(b.handlers[event.Type])+len(b.handlers["*"]))
	handlers = append(handlers, b.handlers[event.Type]...)
	if event.Type != "*" {
		handlers = append(handlers, b.handlers["*"]...)
	}
	b.mu.RUnlock()

	for _, h := range handlers {
		b.wg.Add(1)
		go func(handler Handler) {
			defer b.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("event handler panic", "type", event.Type, "error", r)
					incrementHandlerErrors()
				}
			}()
			handler(event)
		}(h)
	}
}

// Shutdown stops new publishes and waits for in-flight handlers to complete.
func (b *Bus) Shutdown(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.closed.Store(true)

	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var eventBusHandlerErrorsTotal atomic.Int64

func incrementHandlerErrors() {
	eventBusHandlerErrorsTotal.Add(1)
}

func HandlerErrorsTotal() int64 {
	return eventBusHandlerErrorsTotal.Load()
}

func ShutdownTimeout() time.Duration {
	return 5 * time.Second
}
