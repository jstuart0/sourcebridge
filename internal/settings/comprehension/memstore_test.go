// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package comprehension

import (
	"errors"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
)

// TestMemStore_SetModelCapabilities_InvalidTier verifies that SetModelCapabilities
// returns ErrInvalidQualityGateTier for any tier value that is not one of the
// four canonical strings. This is the store-level defense in depth called out
// in CA-150 MED #2.
func TestMemStore_SetModelCapabilities_InvalidTier(t *testing.T) {
	store := NewMemStore()

	mc := &ModelCapabilities{
		ModelID:         "some-model",
		Provider:        "test",
		QualityGateTier: modeltier.QualityGateTier("INVALID"),
	}
	err := store.SetModelCapabilities(mc)
	if err == nil {
		t.Fatal("expected ErrInvalidQualityGateTier, got nil")
	}
	if !errors.Is(err, ErrInvalidQualityGateTier) {
		t.Errorf("expected ErrInvalidQualityGateTier, got %v", err)
	}
}

// TestMemStore_SetModelCapabilities_ValidTiers verifies that all four canonical
// tier values (including the empty sentinel) are accepted by SetModelCapabilities.
func TestMemStore_SetModelCapabilities_ValidTiers(t *testing.T) {
	for _, tier := range []modeltier.QualityGateTier{
		modeltier.TierUnknown,
		modeltier.TierFrontier,
		modeltier.TierMid,
		modeltier.TierLocal,
	} {
		t.Run("tier="+string(tier), func(t *testing.T) {
			store := NewMemStore()
			mc := &ModelCapabilities{
				ModelID:         "test-model",
				Provider:        "test",
				QualityGateTier: tier,
			}
			if err := store.SetModelCapabilities(mc); err != nil {
				t.Errorf("tier=%q: unexpected error: %v", tier, err)
			}
		})
	}
}
