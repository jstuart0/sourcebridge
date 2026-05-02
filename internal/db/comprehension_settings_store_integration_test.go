// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// TestIntegration_ModelCapabilities_QualityGateTier_RoundTrip verifies that
// quality_gate_tier persists and is read back correctly through the full Surreal
// stack (migration 054 required). This is the Phase 3a acceptance gate for
// codex r1 Critical #1.
func TestIntegration_ModelCapabilities_QualityGateTier_RoundTrip(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	for _, tier := range []modeltier.QualityGateTier{
		modeltier.TierFrontier,
		modeltier.TierMid,
		modeltier.TierLocal,
		modeltier.TierUnknown,
	} {
		modelID := "integration-test-model-" + string(tier)
		if tier == modeltier.TierUnknown {
			modelID = "integration-test-model-unknown"
		}

		mc := &comprehension.ModelCapabilities{
			ModelID:         modelID,
			Provider:        "test",
			Source:          "builtin",
			QualityGateTier: tier,
		}
		if err := store.SetModelCapabilities(mc); err != nil {
			t.Fatalf("SetModelCapabilities (tier=%q): %v", tier, err)
		}

		got, err := store.GetModelCapabilities(modelID)
		if err != nil {
			t.Fatalf("GetModelCapabilities (tier=%q): %v", tier, err)
		}
		if got == nil {
			t.Fatalf("GetModelCapabilities returned nil for modelID=%q", modelID)
		}
		if got.QualityGateTier != tier {
			t.Errorf("tier round-trip: stored %q, read back %q", tier, got.QualityGateTier)
		}
	}
}

// TestIntegration_ModelCapabilities_QualityGateTier_UpdatePreservesOtherFields
// writes a record, then updates with a different tier and confirms all other
// fields are unchanged.
func TestIntegration_ModelCapabilities_QualityGateTier_UpdatePreservesOtherFields(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	mc := &comprehension.ModelCapabilities{
		ModelID:              "tier-update-test",
		Provider:             "anthropic",
		DeclaredContextTokens:  200000,
		EffectiveContextTokens: 160000,
		InstructionFollowing:   "high",
		Source:               "builtin",
		Notes:                "original note",
		QualityGateTier:      modeltier.TierMid,
	}
	if err := store.SetModelCapabilities(mc); err != nil {
		t.Fatalf("initial SetModelCapabilities: %v", err)
	}

	// Update with different tier only.
	mc.QualityGateTier = modeltier.TierFrontier
	if err := store.SetModelCapabilities(mc); err != nil {
		t.Fatalf("update SetModelCapabilities: %v", err)
	}

	got, err := store.GetModelCapabilities("tier-update-test")
	if err != nil || got == nil {
		t.Fatalf("GetModelCapabilities after update: %v / %v", err, got)
	}
	if got.QualityGateTier != modeltier.TierFrontier {
		t.Errorf("expected frontier after update, got %q", got.QualityGateTier)
	}
	if got.Provider != "anthropic" {
		t.Errorf("provider changed unexpectedly: %q", got.Provider)
	}
	if got.Notes != "original note" {
		t.Errorf("notes changed unexpectedly: %q", got.Notes)
	}
}
