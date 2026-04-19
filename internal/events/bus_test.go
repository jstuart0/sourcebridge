// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package events

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBusShutdownWaitsForHandlers(t *testing.T) {
	bus := NewBus()
	release := make(chan struct{})
	var started atomic.Bool

	bus.Subscribe("test", func(e Event) {
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
