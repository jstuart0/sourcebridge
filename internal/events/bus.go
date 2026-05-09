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

// Subscription is an opaque handle returned by Subscribe. Pass it to
// Unsubscribe to deregister the handler. The zero value is invalid.
type Subscription struct {
	eventType string
}

// Bus is an in-process event bus.
//
// Lock semantics (Decision 8 / CA-205):
//   - Subscribe and Unsubscribe take mu.Lock().
//   - Publish takes mu.RLock(), snapshots handlers into a local slice,
//     releases the lock, then dispatches goroutines from the snapshot.
//     The lock is never held across goroutine dispatch; handlers are
//     never called while the lock is held, avoiding deadlock.
//   - Callers MUST NOT call Unsubscribe from within a handler — the
//     handler runs outside the lock and Unsubscribe takes mu.Lock(),
//     which would be fine structurally, but the behavior is undefined
//     as to whether the current event is the last one dispatched.
type Bus struct {
	mu       sync.RWMutex
	handlers map[string]map[*Subscription]Handler
	wg       sync.WaitGroup
	closed   atomic.Bool
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{
		handlers: make(map[string]map[*Subscription]Handler),
	}
}

// Subscribe registers a handler for an event type and returns a
// *Subscription handle. Pass the handle to Unsubscribe to deregister.
// Handlers subscribed with "*" receive all events.
func (b *Bus) Subscribe(eventType string, handler Handler) *Subscription {
	sub := &Subscription{eventType: eventType}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handlers[eventType] == nil {
		b.handlers[eventType] = make(map[*Subscription]Handler)
	}
	b.handlers[eventType][sub] = handler
	return sub
}

// Unsubscribe removes the handler associated with sub. It is idempotent:
// calling Unsubscribe twice on the same handle is a no-op. Calling
// Unsubscribe on a nil handle is also a no-op.
func (b *Bus) Unsubscribe(sub *Subscription) {
	if sub == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.handlers[sub.eventType], sub)
}

// Publish emits an event to all registered handlers.
// Handlers subscribed with "*" receive all events.
func (b *Bus) Publish(event Event) {
	if b.closed.Load() {
		return
	}
	b.mu.RLock()
	// Snapshot handlers under RLock to avoid holding the lock while
	// dispatching goroutines (which could call Unsubscribe → deadlock).
	// Deduplicate by pointer in case both topic and "*" match.
	seen := make(map[*Subscription]bool)
	handlers := make([]Handler, 0, len(b.handlers[event.Type])+len(b.handlers["*"]))
	for sub, h := range b.handlers[event.Type] {
		if !seen[sub] {
			seen[sub] = true
			handlers = append(handlers, h)
		}
	}
	if event.Type != "*" {
		for sub, h := range b.handlers["*"] {
			if !seen[sub] {
				seen[sub] = true
				handlers = append(handlers, h)
			}
		}
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
