package rest

import (
	"context"
	"strings"
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

func TestGeneratedSessionIDIsRandomAndPrefixed(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := range 1000 {
		id := generateDesktopSessionID()
		if !strings.HasPrefix(id, "ide_") {
			t.Fatalf("expected ide_ prefix, got %q", id)
		}
		suffix := strings.TrimPrefix(id, "ide_")
		if len(suffix) != 22 { // base64url of 16 bytes = 22 chars unpadded
			t.Fatalf("expected 22-char suffix, got %d (%q)", len(suffix), suffix)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("collision in 1000 IDs (round %d): %q", i, id)
		}
		seen[id] = struct{}{}
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
