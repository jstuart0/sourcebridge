// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/usage"
)

// TestMarkArtifactReady_IncrementsCounter verifies that a successful call to
// markArtifactReady increments ArtifactsCounter by exactly 1, and that a
// failed call (store returns an error for an unknown ID) does NOT increment
// the counter.
func TestMarkArtifactReady_IncrementsCounter(t *testing.T) {
	t.Cleanup(usage.ResetCountersForTest)

	mem := knowledgepkg.NewMemStore()
	r := &Resolver{Deps: &appdeps.AppDeps{KnowledgeStore: mem}}

	// Seed an artifact so UpdateKnowledgeArtifactStatus has something to update.
	artifact := &knowledgepkg.Artifact{
		ID:     "art-001",
		Status: knowledgepkg.StatusGenerating,
	}
	if _, err := mem.StoreKnowledgeArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}

	// Happy path: markArtifactReady should succeed and increment the counter.
	if err := r.markArtifactReady(context.Background(), artifact.ID); err != nil {
		t.Fatalf("markArtifactReady: unexpected error: %v", err)
	}
	if got := usage.ArtifactsCounter.Total(); got != 1 {
		t.Fatalf("after success: expected ArtifactsCounter.Total() == 1, got %d", got)
	}

	// Verify the artifact is actually in READY state.
	stored := mem.GetKnowledgeArtifact(context.Background(), artifact.ID)
	if stored == nil {
		t.Fatal("GetKnowledgeArtifact: returned nil after markArtifactReady")
	}
	if stored.Status != knowledgepkg.StatusReady {
		t.Fatalf("artifact status: expected %q, got %q", knowledgepkg.StatusReady, stored.Status)
	}

	// Failure path: a non-existent artifact ID should return an error and
	// must NOT increment the counter.
	if err := r.markArtifactReady(context.Background(), "does-not-exist"); err == nil {
		t.Fatal("markArtifactReady on unknown ID: expected error, got nil")
	}
	if got := usage.ArtifactsCounter.Total(); got != 1 {
		t.Fatalf("after failure: expected ArtifactsCounter.Total() == 1 (unchanged), got %d", got)
	}
}
