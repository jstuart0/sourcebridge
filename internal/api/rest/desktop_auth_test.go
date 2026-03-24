package rest

import (
	"context"
	"testing"
	"time"
)

func TestMemoryDesktopAuthPollConsumesTokenOnce(t *testing.T) {
	store := NewMemoryDesktopAuthStore()
	session, err := store.Create(context.Background(), "state-1")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if ok := store.Complete(context.Background(), "state-1", "ca_test_token"); !ok {
		t.Fatal("Complete() should succeed")
	}

	first, ok := store.Poll(context.Background(), session.ID)
	if !ok {
		t.Fatal("first Poll() should succeed")
	}
	if first.Token != "ca_test_token" {
		t.Fatalf("expected token on first poll, got %q", first.Token)
	}
	if first.ConsumedAt == nil {
		t.Fatal("first poll should mark session consumed")
	}

	second, ok := store.Poll(context.Background(), session.ID)
	if ok || second != nil {
		t.Fatal("second Poll() should fail after consumption")
	}
}

func TestMemoryDesktopAuthPendingPollRemainsPending(t *testing.T) {
	store := NewMemoryDesktopAuthStore()
	session, err := store.Create(context.Background(), "state-2")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	pending, ok := store.Poll(context.Background(), session.ID)
	if !ok {
		t.Fatal("pending Poll() should succeed")
	}
	if pending.Token != "" {
		t.Fatal("pending session should not expose a token")
	}

	lookup, ok := store.LookupByState(context.Background(), "state-2")
	if !ok || lookup == nil {
		t.Fatal("LookupByState() should still work for pending session")
	}
}

func TestMemoryDesktopAuthExpiredSessionIsRemoved(t *testing.T) {
	store := NewMemoryDesktopAuthStore().(*memoryDesktopAuthStore)
	store.sessionTTL = -1 * time.Second

	session, err := store.Create(context.Background(), "state-3")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	if got, ok := store.Poll(context.Background(), session.ID); ok || got != nil {
		t.Fatal("expired session should not be returned")
	}
}
