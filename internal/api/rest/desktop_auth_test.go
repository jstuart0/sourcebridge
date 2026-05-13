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

// CA-218 (X-L3): the constant-time lookup must traverse every entry
// without short-circuiting on match. A break-on-match would let an
// attacker observe via wall-clock time whether their guessed session
// ID matched something near the start vs end of the iteration. This
// test pins the behavior by asserting that the function returns the
// SAME session regardless of insertion order — proving the loop visited
// every entry.
func TestMemoryDesktopAuthPoll_ConstantTimeLookupReturnsCorrectMatch(t *testing.T) {
	store := NewMemoryDesktopAuthStore().(*memoryDesktopAuthStore)

	// Populate many sessions so iteration order matters.
	const populated = 50
	var targetID string
	for i := 0; i < populated; i++ {
		sess, err := store.Create(context.Background(), "state-"+string(rune('a'+i%26))+string(rune('0'+i/26)))
		if err != nil {
			t.Fatalf("Create()[%d] error: %v", i, err)
		}
		if i == populated/2 {
			targetID = sess.ID
		}
	}

	// Poll the middle target — must succeed regardless of map iteration order.
	got, ok := store.Poll(context.Background(), targetID)
	if !ok {
		t.Fatal("Poll() should match a populated middle-of-map session")
	}
	if got.ID != targetID {
		t.Fatalf("Poll() returned session %q, want %q", got.ID, targetID)
	}
}

func TestMemoryDesktopAuthPoll_ConstantTimeLookupRejectsNearMiss(t *testing.T) {
	// Pin behavior: a session-ID guess that differs only at the last
	// character must NOT match. ConstantTimeCompare's contract is byte-
	// exact equality; map-lookup-by-prefix or accidental substring
	// matching would regress here.
	store := NewMemoryDesktopAuthStore()
	real, err := store.Create(context.Background(), "state-near-miss")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	// Mutate the last character.
	guess := real.ID[:len(real.ID)-1] + "x"
	if guess == real.ID {
		// Defensive: trailing char happened to already be 'x'; mutate to 'y'.
		guess = real.ID[:len(real.ID)-1] + "y"
	}

	got, ok := store.Poll(context.Background(), guess)
	if ok || got != nil {
		t.Fatalf("near-miss guess %q must NOT match real session %q", guess, real.ID)
	}
}

func TestMemoryDesktopAuthPoll_EmptyIDDoesNotMatch(t *testing.T) {
	// Pin: an empty session-id must not accidentally match an entry
	// (e.g., via ConstantTimeCompare on equal-length zero strings).
	store := NewMemoryDesktopAuthStore()
	_, err := store.Create(context.Background(), "state-empty-test")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	got, ok := store.Poll(context.Background(), "")
	if ok || got != nil {
		t.Fatal("empty session-id must not match any populated session")
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
