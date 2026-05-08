// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"testing"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// TestIntegration_UpdateRequirementFields_NilPriorityNilTags is the Phase 3
// acceptance gate for the SurrealDB v2.6.5 upgrade campaign. It verifies that
// calling UpdateRequirementFields with Priority: nil, Tags: nil (a no-op patch)
// does NOT reject the call and preserves the original field values unchanged.
//
// Before Phase 3, callers used UpdateRequirement (now deleted), which always
// issued an UPDATE — bumping updated_at even when nothing changed. The new path
// short-circuits at the len(sets)==1 guard in store.go and returns the current
// row without touching the database. This test pins that contract.
func TestIntegration_UpdateRequirementFields_NilPriorityNilTags(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	repoID := "repo-req-noop-" + uuid.New().String()

	seed := &graph.StoredRequirement{
		ExternalID:  "REQ-001",
		Title:       "Support dark mode",
		Description: "Users should be able to switch to a dark colour scheme.",
		Source:      "product",
		Priority:    "high",
		Tags:        []string{"ui", "accessibility"},
	}
	store.StoreRequirement(repoID, seed)
	if seed.ID == "" {
		t.Fatal("StoreRequirement did not populate seed.ID")
	}

	// No-op patch: both Priority and Tags are nil — nothing substantive changes.
	updated := store.UpdateRequirementFields(seed.ID, graph.RequirementUpdate{
		Priority: nil,
		Tags:     nil,
	})
	if updated == nil {
		t.Fatal("UpdateRequirementFields returned nil for a no-op patch on a valid row")
	}

	// Original field values must be preserved.
	if updated.Priority != seed.Priority {
		t.Errorf("priority: want %q, got %q", seed.Priority, updated.Priority)
	}
	if len(updated.Tags) != len(seed.Tags) {
		t.Errorf("tags length: want %d, got %d", len(seed.Tags), len(updated.Tags))
	} else {
		for i, tag := range seed.Tags {
			if updated.Tags[i] != tag {
				t.Errorf("tags[%d]: want %q, got %q", i, tag, updated.Tags[i])
			}
		}
	}
	if updated.Title != seed.Title {
		t.Errorf("title: want %q, got %q", seed.Title, updated.Title)
	}

	// Read back from the database to verify the no-op was not persisted as a
	// spurious write (i.e. the row was not modified).
	readback := store.GetRequirement(seed.ID)
	if readback == nil {
		t.Fatal("GetRequirement returned nil after no-op UpdateRequirementFields")
	}
	if readback.Priority != seed.Priority {
		t.Errorf("readback priority: want %q, got %q", seed.Priority, readback.Priority)
	}
	if len(readback.Tags) != len(seed.Tags) {
		t.Errorf("readback tags length: want %d, got %d", len(seed.Tags), len(readback.Tags))
	}
}
