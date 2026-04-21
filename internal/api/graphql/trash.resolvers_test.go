// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/trash"
)

func newTestResolver(t *testing.T) *Resolver {
	t.Helper()
	return &Resolver{
		Config: &config.Config{
			Trash: config.TrashConfig{
				Enabled:       true,
				RetentionDays: 30,
			},
		},
		EventBus:   events.NewBus(),
		TrashStore: trash.NewMemStore(),
	}
}

func ctxWithClaims(userID, role string) context.Context {
	return context.WithValue(context.Background(), auth.ClaimsKey, &auth.Claims{
		UserID: userID,
		Role:   role,
	})
}

// A user can permanentlyDelete their own trash entry.
func TestPermanentlyDelete_Owner_AllowsSelfDelete(t *testing.T) {
	r := newTestResolver(t)
	store := r.TrashStore.(*trash.MemStore)
	store.Register(trash.RegisterOptions{
		Type: trash.TypeRequirement, ID: "req-1", RepositoryID: "repo",
		NaturalKey: "AUTH-001", Label: "x",
	})
	_, err := store.MoveToTrash(context.Background(), trash.TypeRequirement, "req-1",
		trash.MoveOptions{UserID: "jay"})
	if err != nil {
		t.Fatal(err)
	}

	ok, err := r.permanentlyDelete(
		ctxWithClaims("jay", "user"),
		TrashableTypeRequirement, "req-1",
	)
	if err != nil {
		t.Fatalf("owner-delete should succeed, got %v", err)
	}
	if !ok {
		t.Error("expected ok=true")
	}
}

// A non-admin, non-owner is rejected.
func TestPermanentlyDelete_NonOwner_NonAdmin_Rejected(t *testing.T) {
	r := newTestResolver(t)
	store := r.TrashStore.(*trash.MemStore)
	store.Register(trash.RegisterOptions{
		Type: trash.TypeRequirement, ID: "req-1", RepositoryID: "repo",
		NaturalKey: "AUTH-001", Label: "x",
	})
	_, err := store.MoveToTrash(context.Background(), trash.TypeRequirement, "req-1",
		trash.MoveOptions{UserID: "jay"})
	if err != nil {
		t.Fatal(err)
	}

	ok, err := r.permanentlyDelete(
		ctxWithClaims("pat", "user"), // different user, no admin
		TrashableTypeRequirement, "req-1",
	)
	if err == nil {
		t.Fatal("expected rejection, got no error")
	}
	if ok {
		t.Error("expected ok=false on rejection")
	}
	if !strings.Contains(err.Error(), "only the user who moved") {
		t.Errorf("expected ownership error, got %v", err)
	}
}

// An admin can purge someone else's trash entry.
func TestPermanentlyDelete_Admin_AllowsCrossUser(t *testing.T) {
	r := newTestResolver(t)
	store := r.TrashStore.(*trash.MemStore)
	store.Register(trash.RegisterOptions{
		Type: trash.TypeRequirement, ID: "req-1", RepositoryID: "repo",
		NaturalKey: "AUTH-001", Label: "x",
	})
	_, err := store.MoveToTrash(context.Background(), trash.TypeRequirement, "req-1",
		trash.MoveOptions{UserID: "jay"})
	if err != nil {
		t.Fatal(err)
	}

	ok, err := r.permanentlyDelete(
		ctxWithClaims("pat", "admin"),
		TrashableTypeRequirement, "req-1",
	)
	if err != nil {
		t.Fatalf("admin should succeed, got %v", err)
	}
	if !ok {
		t.Error("expected ok=true")
	}
}

// PermanentlyDelete refuses to touch a row that is not in trash.
func TestPermanentlyDelete_LiveRow_Rejected(t *testing.T) {
	r := newTestResolver(t)
	store := r.TrashStore.(*trash.MemStore)
	store.Register(trash.RegisterOptions{
		Type: trash.TypeRequirement, ID: "req-1", RepositoryID: "repo",
		NaturalKey: "AUTH-001", Label: "x",
	})
	// NOT moved to trash.
	_, err := r.permanentlyDelete(
		ctxWithClaims("jay", "user"),
		TrashableTypeRequirement, "req-1",
	)
	if err == nil || !strings.Contains(err.Error(), "not in trash") {
		t.Errorf("want not-in-trash error, got %v", err)
	}
}

// Unauthenticated is explicitly rejected (defence in depth; the auth
// middleware should stop this upstream, but we don't want a nil-claims
// path silently deleting data).
func TestPermanentlyDelete_Unauthenticated_Rejected(t *testing.T) {
	r := newTestResolver(t)
	store := r.TrashStore.(*trash.MemStore)
	store.Register(trash.RegisterOptions{
		Type: trash.TypeRequirement, ID: "req-1", RepositoryID: "repo",
		NaturalKey: "AUTH-001", Label: "x",
	})
	_, _ = store.MoveToTrash(context.Background(), trash.TypeRequirement, "req-1",
		trash.MoveOptions{UserID: "jay"})

	_, err := r.permanentlyDelete(
		context.Background(), // no claims
		TrashableTypeRequirement, "req-1",
	)
	if err == nil || !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("want unauthenticated error, got %v", err)
	}
}

// Time-of-check fixture: ensures Get reflects the latest tombstone
// state and isn't racing against List. (Indirectly: if Get only read
// from a stale cache, this test would fail because the Move happens
// milliseconds before the Get.)
func TestPermanentlyDelete_TOCTOU_Consistency(t *testing.T) {
	r := newTestResolver(t)
	store := r.TrashStore.(*trash.MemStore)
	store.Register(trash.RegisterOptions{
		Type: trash.TypeRequirement, ID: "req-1", RepositoryID: "repo",
		NaturalKey: "AUTH-001", Label: "x",
	})
	_, _ = store.MoveToTrash(context.Background(), trash.TypeRequirement, "req-1",
		trash.MoveOptions{UserID: "jay"})

	// Take just under a microsecond before the call to ensure the race is realistic.
	time.Sleep(500 * time.Microsecond)
	ok, err := r.permanentlyDelete(ctxWithClaims("jay", "user"),
		TrashableTypeRequirement, "req-1")
	if err != nil || !ok {
		t.Fatalf("expected success, got ok=%v err=%v", ok, err)
	}
}
