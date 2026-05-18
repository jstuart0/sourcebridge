// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/db"
)

// TestProfileToResponse_CreatedViaRoundTrips asserts that profileToResponse
// correctly maps db.Profile.CreatedVia into rest.ProfileResponse.CreatedVia
// (bob M4 — mapping test).
func TestProfileToResponse_CreatedViaRoundTrips(t *testing.T) {
	cases := []struct {
		name       string
		createdVia string
	}{
		{"env_bootstrap is preserved", "env_bootstrap"},
		{"legacy_migration is preserved", "legacy_migration"},
		{"empty string is preserved", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := db.Profile{
				ID:         "ca_llm_profile:test-id",
				Name:       "Test",
				CreatedVia: tc.createdVia,
			}
			resp := profileToResponse(p, "ca_llm_profile:other-id")
			if resp.CreatedVia != tc.createdVia {
				t.Errorf("CreatedVia: got %q, want %q", resp.CreatedVia, tc.createdVia)
			}
			// Sanity: is_active is false when IDs don't match.
			if resp.IsActive {
				t.Error("IsActive: expected false when ID doesn't match activeID")
			}
		})
	}

	// is_active=true path.
	t.Run("is_active when ID matches activeID", func(t *testing.T) {
		p := db.Profile{ID: "ca_llm_profile:active", CreatedVia: "env_bootstrap"}
		resp := profileToResponse(p, "ca_llm_profile:active")
		if !resp.IsActive {
			t.Error("IsActive: expected true when ID matches activeID")
		}
		if resp.CreatedVia != "env_bootstrap" {
			t.Errorf("CreatedVia: got %q, want env_bootstrap", resp.CreatedVia)
		}
	})
}
