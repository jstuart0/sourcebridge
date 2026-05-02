// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package modeltier_test

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
)

// TestQualityGateTier_Constants verifies that the four constants have the
// string values the plan specifies (they are persisted to SurrealDB and must
// not drift).
func TestQualityGateTier_Constants(t *testing.T) {
	cases := []struct {
		tier modeltier.QualityGateTier
		want string
	}{
		{modeltier.TierUnknown, ""},
		{modeltier.TierFrontier, "frontier"},
		{modeltier.TierMid, "mid"},
		{modeltier.TierLocal, "local"},
	}
	for _, c := range cases {
		if got := c.tier.String(); got != c.want {
			t.Errorf("QualityGateTier(%q).String() = %q, want %q", c.tier, got, c.want)
		}
	}
}

// TestQualityGateTier_IsValid verifies the four valid values and a selection
// of invalid ones.
func TestQualityGateTier_IsValid(t *testing.T) {
	valid := []modeltier.QualityGateTier{
		modeltier.TierUnknown,
		modeltier.TierFrontier,
		modeltier.TierMid,
		modeltier.TierLocal,
	}
	for _, tier := range valid {
		if !tier.IsValid() {
			t.Errorf("IsValid(%q) = false, want true", tier)
		}
	}

	invalid := []modeltier.QualityGateTier{
		"FRONTIER", "Mid", "LOCAL", "unknown", "enterprise", "premium",
	}
	for _, tier := range invalid {
		if tier.IsValid() {
			t.Errorf("IsValid(%q) = true, want false", tier)
		}
	}
}

// TestQualityGateTier_Parse verifies case-insensitive parsing with whitespace
// trimming.
func TestQualityGateTier_Parse(t *testing.T) {
	cases := []struct {
		input string
		want  modeltier.QualityGateTier
		ok    bool
	}{
		// Exact lowercase matches
		{"", modeltier.TierUnknown, true},
		{"frontier", modeltier.TierFrontier, true},
		{"mid", modeltier.TierMid, true},
		{"local", modeltier.TierLocal, true},
		// Case-insensitive
		{"Frontier", modeltier.TierFrontier, true},
		{"FRONTIER", modeltier.TierFrontier, true},
		{"MID", modeltier.TierMid, true},
		{"Local", modeltier.TierLocal, true},
		// Whitespace trimmed
		{"  frontier  ", modeltier.TierFrontier, true},
		{"\tfrontier\n", modeltier.TierFrontier, true},
		// Invalid
		{"enterprise", modeltier.TierUnknown, false},
		{"premium", modeltier.TierUnknown, false},
		{"unknown", modeltier.TierUnknown, false},
	}
	for _, c := range cases {
		got, ok := modeltier.Parse(c.input)
		if ok != c.ok {
			t.Errorf("Parse(%q): ok = %v, want %v", c.input, ok, c.ok)
		}
		if got != c.want {
			t.Errorf("Parse(%q): tier = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestResolution_ZeroValueIsTierUnknownAndEmptySource verifies that the zero
// value of Resolution is safe to pass through code paths that haven't yet
// populated it (Tier == TierUnknown, Source == "").
func TestResolution_ZeroValueIsTierUnknownAndEmptySource(t *testing.T) {
	var r modeltier.Resolution
	if r.Tier != modeltier.TierUnknown {
		t.Errorf("Resolution{}.Tier = %q, want TierUnknown (%q)", r.Tier, modeltier.TierUnknown)
	}
	if r.Source != "" {
		t.Errorf("Resolution{}.Source = %q, want empty string", r.Source)
	}
	if r.Err != nil {
		t.Errorf("Resolution{}.Err = %v, want nil", r.Err)
	}
}
