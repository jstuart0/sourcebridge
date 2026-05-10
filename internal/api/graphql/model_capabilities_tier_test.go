// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// newModelCapabilityMutationResolver returns a mutation resolver backed by a
// MemStore for model capability tests. No LLM orchestrator required.
func newModelCapabilityMutationResolver(t *testing.T) *mutationResolver {
	t.Helper()
	return &mutationResolver{
		&Resolver{
			Deps: &appdeps.AppDeps{
				ComprehensionStore: comprehension.NewMemStore(),
			},
		},
	}
}

// TestUpdateModelCapabilities_QualityGateTier_RoundTrip verifies that a
// mutation that sets qualityGateTier="local" is reflected in a subsequent
// query resolver call.
func TestUpdateModelCapabilities_QualityGateTier_RoundTrip(t *testing.T) {
	r := newModelCapabilityMutationResolver(t)
	ctx := context.Background()
	qr := &queryResolver{r.Resolver}

	modelID := "qwen3:32b"
	tier := "local"

	_, err := r.UpdateModelCapabilities(ctx, UpdateModelCapabilitiesInput{
		ModelID:         modelID,
		Provider:        strPtr("ollama"),
		QualityGateTier: &tier,
	})
	if err != nil {
		t.Fatalf("UpdateModelCapabilities: %v", err)
	}

	got, err := qr.ModelCapability(ctx, modelID)
	if err != nil {
		t.Fatalf("ModelCapability query: %v", err)
	}
	if got == nil {
		t.Fatal("ModelCapability returned nil")
	}
	if got.QualityGateTier == nil {
		t.Fatal("expected qualityGateTier to be set, got nil")
	}
	if *got.QualityGateTier != "local" {
		t.Errorf("qualityGateTier: got %q, want \"local\"", *got.QualityGateTier)
	}
}

// TestUpdateModelCapabilities_QualityGateTier_PremiumRejected verifies that
// an unknown tier value ("premium") is rejected with a GraphQL error and no
// write occurs.
func TestUpdateModelCapabilities_QualityGateTier_PremiumRejected(t *testing.T) {
	r := newModelCapabilityMutationResolver(t)
	ctx := context.Background()
	qr := &queryResolver{r.Resolver}

	modelID := "gpt-4o"
	badTier := "premium"

	_, err := r.UpdateModelCapabilities(ctx, UpdateModelCapabilitiesInput{
		ModelID:         modelID,
		Provider:        strPtr("openai"),
		QualityGateTier: &badTier,
	})
	if err == nil {
		t.Fatal("expected error for invalid qualityGateTier \"premium\", got nil")
	}

	// Nothing should have been written.
	got, err := qr.ModelCapability(ctx, modelID)
	if err != nil {
		t.Fatalf("ModelCapability query after rejected mutation: %v", err)
	}
	if got != nil {
		t.Errorf("expected no stored profile after rejection, but got one (modelId=%q)", got.ModelID)
	}
}
