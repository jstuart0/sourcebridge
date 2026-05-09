// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBusShutdownWaitsForHandlers(t *testing.T) {
	bus := NewBus()
	release := make(chan struct{})
	var started atomic.Bool

	_ = bus.Subscribe("test", func(e Event) {
		started.Store(true)
		<-release
	})
	bus.Publish(NewEvent("test", nil))

	deadline := time.Now().Add(time.Second)
	for !started.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !started.Load() {
		t.Fatal("expected handler to start")
	}

	done := make(chan error, 1)
	go func() {
		done <- bus.Shutdown(context.Background())
	}()

	select {
	case <-done:
		t.Fatal("shutdown returned before handler completed")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete after handler release")
	}
}

// T36: subscribe → publish → handler called; subscribe → unsubscribe →
// publish → handler NOT called; double-unsubscribe → no panic.
func TestBusSubscribeUnsubscribeRoundTrip(t *testing.T) {
	bus := NewBus()
	var count atomic.Int32

	// Subscribe → publish → handler called.
	sub := bus.Subscribe("ev", func(e Event) { count.Add(1) })
	bus.Publish(NewEvent("ev", nil))
	deadline := time.Now().Add(200 * time.Millisecond)
	for count.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if count.Load() == 0 {
		t.Fatal("handler not called after Subscribe + Publish")
	}

	// Unsubscribe → publish → handler NOT called again.
	prev := count.Load()
	bus.Unsubscribe(sub)
	bus.Publish(NewEvent("ev", nil))
	time.Sleep(30 * time.Millisecond) // let any in-flight goroutine finish
	if count.Load() != prev {
		t.Fatalf("handler called after Unsubscribe: count went from %d to %d", prev, count.Load())
	}

	// Double-unsubscribe is a no-op (must not panic).
	bus.Unsubscribe(sub)
	bus.Unsubscribe(nil) // nil handle is also safe.
}

// T37: concurrent Subscribe, Publish, and Unsubscribe — run under -race;
// must not data-race or panic.
func TestBusConcurrentRace(t *testing.T) {
	bus := NewBus()
	const goroutines = 4

	var wg sync.WaitGroup
	subs := make([]*Subscription, goroutines)
	for i := range subs {
		subs[i] = bus.Subscribe("*", func(e Event) {})
	}

	// Concurrently publish and unsubscribe on N goroutines.
	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		sub := subs[i]
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				bus.Publish(NewEvent("*", nil))
			}
		}()
		go func() {
			defer wg.Done()
			bus.Unsubscribe(sub)
		}()
	}
	wg.Wait()
	// No assertion needed beyond "not panicked and the race detector is quiet".
}
