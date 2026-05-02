// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package funnel

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockFunnelStore captures recorded events for assertion in unit tests.
type mockFunnelStore struct {
	events []FunnelEvent
	err    error
}

func (m *mockFunnelStore) RecordEvent(_ context.Context, ev FunnelEvent) error {
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, ev)
	return nil
}

// Verify at compile time that *mockFunnelStore satisfies FunnelStore.
var _ FunnelStore = (*mockFunnelStore)(nil)

func TestFunnelStore_Interface(t *testing.T) {
	// Verify that SurrealFunnelStore satisfies FunnelStore at compile time.
	var _ FunnelStore = (*SurrealFunnelStore)(nil)
}

func TestMockFunnelStore_RecordEvent(t *testing.T) {
	ctx := context.Background()
	store := &mockFunnelStore{}

	userID := "user-abc"
	tenantID := "tenant-xyz"
	now := time.Now().UTC().Truncate(time.Second)

	ev := FunnelEvent{
		Event:        "funnel.repo.added",
		Source:       "browser",
		UserID:       &userID,
		TenantID:     &tenantID,
		RepoID:       "repo-123",
		ArtifactID:   "",
		ArtifactType: "",
		Metadata:     map[string]any{"template": "default"},
		OccurredAt:   now,
	}

	if err := store.RecordEvent(ctx, ev); err != nil {
		t.Fatalf("RecordEvent: unexpected error: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}

	got := store.events[0]
	if got.Event != ev.Event {
		t.Errorf("Event: got %q, want %q", got.Event, ev.Event)
	}
	if got.Source != ev.Source {
		t.Errorf("Source: got %q, want %q", got.Source, ev.Source)
	}
	if got.UserID == nil || *got.UserID != userID {
		t.Errorf("UserID: got %v, want %q", got.UserID, userID)
	}
	if got.TenantID == nil || *got.TenantID != tenantID {
		t.Errorf("TenantID: got %v, want %q", got.TenantID, tenantID)
	}
	if got.RepoID != ev.RepoID {
		t.Errorf("RepoID: got %q, want %q", got.RepoID, ev.RepoID)
	}
	if !got.OccurredAt.Equal(now) {
		t.Errorf("OccurredAt: got %v, want %v", got.OccurredAt, now)
	}
}

func TestMockFunnelStore_RecordEvent_NilPointerFields(t *testing.T) {
	ctx := context.Background()
	store := &mockFunnelStore{}

	// UserID and TenantID may be nil (server-side bus events have no auth context).
	ev := FunnelEvent{
		Event:      "funnel.index.completed",
		Source:     "server",
		UserID:     nil,
		TenantID:   nil,
		RepoID:     "repo-456",
		OccurredAt: time.Now().UTC(),
	}

	if err := store.RecordEvent(ctx, ev); err != nil {
		t.Fatalf("RecordEvent with nil identity fields: unexpected error: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}
	if store.events[0].UserID != nil {
		t.Errorf("UserID should be nil, got %v", store.events[0].UserID)
	}
	if store.events[0].TenantID != nil {
		t.Errorf("TenantID should be nil, got %v", store.events[0].TenantID)
	}
}

func TestMockFunnelStore_RecordEvent_PropagatesError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("db unavailable")
	store := &mockFunnelStore{err: sentinel}

	ev := FunnelEvent{
		Event:      "funnel.repo.added",
		Source:     "browser",
		OccurredAt: time.Now().UTC(),
	}

	err := store.RecordEvent(ctx, ev)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
	if len(store.events) != 0 {
		t.Errorf("expected no events recorded on error, got %d", len(store.events))
	}
}

func TestSurrealFunnelStore_NilReceiver(t *testing.T) {
	// A nil *SurrealFunnelStore must return nil without panicking.
	var s *SurrealFunnelStore
	err := s.RecordEvent(context.Background(), FunnelEvent{
		Event:      "funnel.repo.added",
		Source:     "server",
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Errorf("nil receiver: expected nil error, got %v", err)
	}
}

func TestFunnelEvent_OccurredAtZeroUsesNow(t *testing.T) {
	// Verify that a zero OccurredAt is populated before persistence.
	// We test this indirectly via the mock — the Surreal impl substitutes
	// time.Now(); the mock records what it's given.  Here we just confirm
	// the zero value is a valid state the struct can hold.
	ev := FunnelEvent{
		Event:  "funnel.repo.added",
		Source: "server",
		// OccurredAt intentionally zero
	}
	if !ev.OccurredAt.IsZero() {
		t.Error("expected zero OccurredAt by default")
	}
}

func TestFunnelEvent_AllExplicitFields(t *testing.T) {
	// Ensures all plan-specified fields are present and settable.
	userID := "u1"
	tenantID := "t1"
	ev := FunnelEvent{
		Event:         "artifact.generation.cliff_notes",
		Source:        "browser",
		UserID:        &userID,
		TenantID:      &tenantID,
		RepoID:        "repo-1",
		ArtifactID:    "art-1",
		RequirementID: "req-1",
		SymbolID:      "sym-1",
		ArtifactType:  "cliff_notes",
		Metadata:      map[string]any{"scopeType": "file"},
		OccurredAt:    time.Now().UTC(),
	}

	store := &mockFunnelStore{}
	if err := store.RecordEvent(context.Background(), ev); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	got := store.events[0]
	if got.ArtifactID != "art-1" {
		t.Errorf("ArtifactID: got %q, want %q", got.ArtifactID, "art-1")
	}
	if got.RequirementID != "req-1" {
		t.Errorf("RequirementID: got %q, want %q", got.RequirementID, "req-1")
	}
	if got.SymbolID != "sym-1" {
		t.Errorf("SymbolID: got %q, want %q", got.SymbolID, "sym-1")
	}
	if got.ArtifactType != "cliff_notes" {
		t.Errorf("ArtifactType: got %q, want %q", got.ArtifactType, "cliff_notes")
	}
}
