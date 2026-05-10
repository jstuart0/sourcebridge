// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"math"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// ptr is a tiny generic helper that returns a pointer to any value.
func ptr[T any](v T) *T { return &v }

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
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("SetModelCapabilities (tier=%q): %v", tier, err)
		}

		got, err := store.GetModelCapabilities(t.Context(), modelID)
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
	if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
		t.Fatalf("initial SetModelCapabilities: %v", err)
	}

	// Update with different tier only.
	mc.QualityGateTier = modeltier.TierFrontier
	if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
		t.Fatalf("update SetModelCapabilities: %v", err)
	}

	got, err := store.GetModelCapabilities(t.Context(), "tier-update-test")
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

// TestIntegration_ModelCapabilities_NonNilCosts_RoundTrip verifies that
// non-nil cost pointer fields persist correctly through the full SurrealDB
// stack (Case A — "field present" branch).
func TestIntegration_ModelCapabilities_NonNilCosts_RoundTrip(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	mc := &comprehension.ModelCapabilities{
		ModelID:         "cost-roundtrip-test",
		Provider:        "openai",
		Source:          "builtin",
		QualityGateTier: modeltier.TierFrontier,
		CostPer1kInput:  ptr(0.002),
		CostPer1kOutput: ptr(0.006),
	}
	if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
		t.Fatalf("SetModelCapabilities: %v", err)
	}

	got, err := store.GetModelCapabilities(t.Context(), "cost-roundtrip-test")
	if err != nil || got == nil {
		t.Fatalf("GetModelCapabilities: %v / %v", err, got)
	}
	if got.CostPer1kInput == nil {
		t.Fatal("CostPer1kInput: expected non-nil, got nil")
	}
	if math.Abs(*got.CostPer1kInput-0.002) > 1e-9 {
		t.Errorf("CostPer1kInput: want 0.002, got %v", *got.CostPer1kInput)
	}
	if got.CostPer1kOutput == nil {
		t.Fatal("CostPer1kOutput: expected non-nil, got nil")
	}
	if math.Abs(*got.CostPer1kOutput-0.006) > 1e-9 {
		t.Errorf("CostPer1kOutput: want 0.006, got %v", *got.CostPer1kOutput)
	}
}

// TestIntegration_ModelCapabilities_NilCosts_RoundTripPreservesNil is the
// regression guard for the v2.6.5 NULL-rejection bug: when CostPer1kInput and
// CostPer1kOutput are nil, no JSON null must reach the wire (Case B).
func TestIntegration_ModelCapabilities_NilCosts_RoundTripPreservesNil(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	mc := &comprehension.ModelCapabilities{
		ModelID:         "nil-cost-test",
		Provider:        "anthropic",
		Source:          "builtin",
		QualityGateTier: modeltier.TierFrontier,
		CostPer1kInput:  nil,
		CostPer1kOutput: nil,
	}
	if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
		t.Fatalf("SetModelCapabilities: %v", err)
	}

	got, err := store.GetModelCapabilities(t.Context(), "nil-cost-test")
	if err != nil || got == nil {
		t.Fatalf("GetModelCapabilities: %v / %v", err, got)
	}
	if got.CostPer1kInput != nil {
		t.Errorf("CostPer1kInput: expected nil, got %v", *got.CostPer1kInput)
	}
	if got.CostPer1kOutput != nil {
		t.Errorf("CostPer1kOutput: expected nil, got %v", *got.CostPer1kOutput)
	}
}

// TestIntegration_ModelCapabilities_LastProbedAt_NonNilRoundTrip verifies that
// a non-nil LastProbedAt persists and is read back within 1ms tolerance (Case C).
func TestIntegration_ModelCapabilities_LastProbedAt_NonNilRoundTrip(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	// Truncate to microsecond — SurrealDB datetime precision is 1µs and the
	// round-trip would otherwise fail on sub-microsecond differences.
	probed := time.Now().UTC().Truncate(time.Microsecond)

	mc := &comprehension.ModelCapabilities{
		ModelID:         "last-probed-at-test",
		Provider:        "ollama",
		Source:          "probe",
		QualityGateTier: modeltier.TierLocal,
		LastProbedAt:    &probed,
	}
	if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
		t.Fatalf("SetModelCapabilities: %v", err)
	}

	got, err := store.GetModelCapabilities(t.Context(), "last-probed-at-test")
	if err != nil || got == nil {
		t.Fatalf("GetModelCapabilities: %v / %v", err, got)
	}
	if got.LastProbedAt == nil {
		t.Fatal("LastProbedAt: expected non-nil, got nil")
	}
	diff := got.LastProbedAt.Sub(probed)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Millisecond {
		t.Errorf("LastProbedAt round-trip diff %v exceeds 1ms tolerance", diff)
	}
}

// TestIntegration_ModelCapabilities_OptionFieldsTableDriven sweeps each
// option<…> field on ca_model_capabilities through nil → non-nil → nil
// in isolation, proving the writer handles every option field correctly
// regardless of the other fields' state (Case D — Tessa's schema-invariant test).
func TestIntegration_ModelCapabilities_OptionFieldsTableDriven(t *testing.T) {
	surreal := startSurrealContainer(t)
	store := &SurrealStore{client: surreal}

	now := time.Now().UTC().Truncate(time.Microsecond)

	baseMC := func(modelID string) *comprehension.ModelCapabilities {
		return &comprehension.ModelCapabilities{
			ModelID:         modelID,
			Provider:        "test",
			Source:          "builtin",
			QualityGateTier: modeltier.TierFrontier,
		}
	}

	t.Run("cost_per_1k_input", func(t *testing.T) {
		const id = "option-table-cost-input"
		// nil → CREATE → read: field must be absent (nil).
		mc := baseMC(id)
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write nil: %v", err)
		}
		got, _ := store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.CostPer1kInput != nil {
			t.Fatalf("nil write: expected nil, got %v", got)
		}

		// non-nil → UPDATE → read: field must match.
		mc.CostPer1kInput = ptr(0.001)
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write non-nil: %v", err)
		}
		got, _ = store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.CostPer1kInput == nil || math.Abs(*got.CostPer1kInput-0.001) > 1e-9 {
			t.Fatalf("non-nil write: want 0.001, got %v", got)
		}

		// nil → DELETE+CREATE → read: field must be absent again.
		// Omitting a field from the UPDATE SET clause leaves the prior value
		// intact (the writer is a partial update, not a clear). To test the
		// "nil on a fresh row" path we delete and re-create.
		if err := store.DeleteModelCapabilities(t.Context(), id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		mc.ID = ""            // force a fresh CREATE
		mc.CostPer1kInput = nil
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write nil on fresh row: %v", err)
		}
		got, _ = store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.CostPer1kInput != nil {
			t.Fatalf("nil on fresh row: expected nil, got %v", got)
		}
	})

	t.Run("cost_per_1k_output", func(t *testing.T) {
		const id = "option-table-cost-output"
		// nil → CREATE → read: field must be absent (nil).
		mc := baseMC(id)
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write nil: %v", err)
		}
		got, _ := store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.CostPer1kOutput != nil {
			t.Fatalf("nil write: expected nil, got %v", got)
		}

		// non-nil → UPDATE → read: field must match.
		mc.CostPer1kOutput = ptr(0.003)
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write non-nil: %v", err)
		}
		got, _ = store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.CostPer1kOutput == nil || math.Abs(*got.CostPer1kOutput-0.003) > 1e-9 {
			t.Fatalf("non-nil write: want 0.003, got %v", got)
		}

		// nil → DELETE+CREATE → read: field must be absent again.
		if err := store.DeleteModelCapabilities(t.Context(), id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		mc.ID = ""
		mc.CostPer1kOutput = nil
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write nil on fresh row: %v", err)
		}
		got, _ = store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.CostPer1kOutput != nil {
			t.Fatalf("nil on fresh row: expected nil, got %v", got)
		}
	})

	t.Run("last_probed_at", func(t *testing.T) {
		const id = "option-table-last-probed-at"
		// nil → CREATE → read: field must be absent (nil).
		mc := baseMC(id)
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write nil: %v", err)
		}
		got, _ := store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.LastProbedAt != nil {
			t.Fatalf("nil write: expected nil, got %v", got)
		}

		// non-nil → UPDATE → read: field must round-trip within 1ms.
		mc.LastProbedAt = &now
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write non-nil: %v", err)
		}
		got, _ = store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.LastProbedAt == nil {
			t.Fatalf("non-nil write: got nil LastProbedAt")
		}
		diff := got.LastProbedAt.Sub(now)
		if diff < 0 {
			diff = -diff
		}
		if diff > time.Millisecond {
			t.Errorf("LastProbedAt diff %v exceeds 1ms", diff)
		}

		// nil → DELETE+CREATE → read: field must be absent again.
		if err := store.DeleteModelCapabilities(t.Context(), id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		mc.ID = ""
		mc.LastProbedAt = nil
		if err := store.SetModelCapabilities(t.Context(), mc); err != nil {
			t.Fatalf("write nil on fresh row: %v", err)
		}
		got, _ = store.GetModelCapabilities(t.Context(), id)
		if got == nil || got.LastProbedAt != nil {
			t.Fatalf("nil on fresh row: expected nil, got %v", got)
		}
	})
}
