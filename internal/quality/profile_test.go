// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package quality_test

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// TestDefaultProfile_ArchitectureEngineer verifies that the
// architecture/for-engineers profile has the expected gates and warnings
// per the Q.2 table in the plan.
func TestDefaultProfile_ArchitectureEngineer(t *testing.T) {
	p, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers)
	if !ok {
		t.Fatal("DefaultProfile returned false for architecture/for-engineers")
	}

	gates := gateSet(p)
	warnings := warningSet(p)

	// Gates: citation_density, vagueness, factual_grounding
	requireGate(t, p.String(), gates, quality.ValidatorCitationDensity)
	requireGate(t, p.String(), gates, quality.ValidatorVagueness)
	requireGate(t, p.String(), gates, quality.ValidatorFactualGrounding)

	// Warnings: empty_headline, reading_level, code_example_present
	requireWarning(t, p.String(), warnings, quality.ValidatorEmptyHeadline)
	requireWarning(t, p.String(), warnings, quality.ValidatorReadingLevel)
	requireWarning(t, p.String(), warnings, quality.ValidatorCodeExamplePresent)
}

// TestDefaultProfile_ArchitectureProduct verifies that the product
// audience strips code-centric validators.
func TestDefaultProfile_ArchitectureProduct(t *testing.T) {
	p, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceProduct)
	if !ok {
		t.Fatal("DefaultProfile returned false for architecture/for-product")
	}

	gates := gateSet(p)
	warnings := warningSet(p)
	offs := offSet(p)

	// Gates: vagueness, factual_grounding
	requireGate(t, p.String(), gates, quality.ValidatorVagueness)
	requireGate(t, p.String(), gates, quality.ValidatorFactualGrounding)

	// citation_density and code_example_present must be off for product audience
	requireOff(t, p.String(), offs, quality.ValidatorCitationDensity)
	requireOff(t, p.String(), offs, quality.ValidatorCodeExamplePresent)

	_ = warnings
}

// TestDefaultProfile_APIReferenceEngineer verifies higher citation density
// gate for API reference pages.
func TestDefaultProfile_APIReferenceEngineer(t *testing.T) {
	p, ok := quality.DefaultProfile(quality.TemplateAPIReference, quality.AudienceEngineers)
	if !ok {
		t.Fatal("DefaultProfile returned false for api_reference/for-engineers")
	}

	gates := gateSet(p)

	// code_example_present is a gate (not a warning) for API reference.
	requireGate(t, p.String(), gates, quality.ValidatorCodeExamplePresent)
	requireGate(t, p.String(), gates, quality.ValidatorCitationDensity)

	// Verify the citation density threshold is 100 (stricter).
	for _, rule := range p.Rules {
		if rule.ValidatorID == quality.ValidatorCitationDensity {
			if rule.Config.CitationDensityWordsPerCitation != 100 {
				t.Errorf("APIReference/engineers: expected citation density threshold 100, got %d",
					rule.Config.CitationDensityWordsPerCitation)
			}
		}
	}
}

// TestDefaultProfile_ADR verifies ADR-specific thresholds:
// factual_grounding and vagueness are gates; reading_level floor is 40.
func TestDefaultProfile_ADR(t *testing.T) {
	p, ok := quality.DefaultProfile(quality.TemplateADR, quality.AudienceEngineers)
	if !ok {
		t.Fatal("DefaultProfile returned false for adr/for-engineers")
	}

	gates := gateSet(p)
	warnings := warningSet(p)

	requireGate(t, p.String(), gates, quality.ValidatorFactualGrounding)
	requireGate(t, p.String(), gates, quality.ValidatorVagueness)
	requireWarning(t, p.String(), warnings, quality.ValidatorReadingLevel)

	// reading_level floor must be 40 (relaxed for dense ADRs).
	for _, rule := range p.Rules {
		if rule.ValidatorID == quality.ValidatorReadingLevel {
			if rule.Config.ReadingLevelFloor != 40 {
				t.Errorf("ADR: expected reading_level floor 40, got %.1f",
					rule.Config.ReadingLevelFloor)
			}
		}
	}

	// citation_density is a warning, not a gate, for ADRs.
	requireWarning(t, p.String(), warnings, quality.ValidatorCitationDensity)
}

// TestDefaultProfile_Glossary verifies that only factual_grounding applies.
func TestDefaultProfile_Glossary(t *testing.T) {
	p, ok := quality.DefaultProfile(quality.TemplateGlossary, quality.AudienceEngineers)
	if !ok {
		t.Fatal("DefaultProfile returned false for glossary/for-engineers")
	}

	gates := gateSet(p)
	requireGate(t, p.String(), gates, quality.ValidatorFactualGrounding)

	// Only one rule should be defined.
	if len(p.Rules) != 1 {
		t.Errorf("Glossary: expected exactly 1 rule, got %d", len(p.Rules))
	}
}

// TestDefaultProfile_SystemOverview verifies architectural_relevance gate.
func TestDefaultProfile_SystemOverview(t *testing.T) {
	p, ok := quality.DefaultProfile(quality.TemplateSystemOverview, quality.AudienceEngineers)
	if !ok {
		t.Fatal("DefaultProfile returned false for system_overview/for-engineers")
	}

	gates := gateSet(p)
	requireGate(t, p.String(), gates, quality.ValidatorArchitecturalRelevance)
	requireGate(t, p.String(), gates, quality.ValidatorVagueness)
}

// TestDefaultProfile_NotFound verifies that unknown combinations return false.
func TestDefaultProfile_NotFound(t *testing.T) {
	_, ok := quality.DefaultProfile("nonexistent_template", "nonexistent_audience")
	if ok {
		t.Error("DefaultProfile: expected false for unknown template+audience")
	}
}

// TestAllDefaultProfiles_NoDuplicates ensures no template+audience
// combination appears more than once in the default profiles.
func TestAllDefaultProfiles_NoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range quality.AllDefaultProfiles() {
		key := string(p.Template) + "/" + string(p.Audience)
		if seen[key] {
			t.Errorf("AllDefaultProfiles: duplicate profile for %s", key)
		}
		seen[key] = true
	}
}

// TestProfile_TierFieldExists verifies that the Tier field is present on
// quality.Profile and that its zero value is modeltier.TierUnknown (not
// silently treated as TierFrontier). Phase 2 will populate this field
// explicitly on every materialized profile.
func TestProfile_TierFieldExists(t *testing.T) {
	var p quality.Profile
	if p.Tier != modeltier.TierUnknown {
		t.Errorf("Profile{}.Tier = %q, want TierUnknown (%q)", p.Tier, modeltier.TierUnknown)
	}
}

// --- helpers ---

func gateSet(p quality.Profile) map[quality.ValidatorID]bool {
	out := map[quality.ValidatorID]bool{}
	for _, r := range p.Rules {
		if r.Level == quality.LevelGate {
			out[r.ValidatorID] = true
		}
	}
	return out
}

func warningSet(p quality.Profile) map[quality.ValidatorID]bool {
	out := map[quality.ValidatorID]bool{}
	for _, r := range p.Rules {
		if r.Level == quality.LevelWarning {
			out[r.ValidatorID] = true
		}
	}
	return out
}

func offSet(p quality.Profile) map[quality.ValidatorID]bool {
	out := map[quality.ValidatorID]bool{}
	for _, r := range p.Rules {
		if r.Level == quality.LevelOff {
			out[r.ValidatorID] = true
		}
	}
	return out
}

func requireGate(t *testing.T, profile string, gates map[quality.ValidatorID]bool, id quality.ValidatorID) {
	t.Helper()
	if !gates[id] {
		t.Errorf("%s: expected %s to be a gate", profile, id)
	}
}

func requireWarning(t *testing.T, profile string, warnings map[quality.ValidatorID]bool, id quality.ValidatorID) {
	t.Helper()
	if !warnings[id] {
		t.Errorf("%s: expected %s to be a warning", profile, id)
	}
}

func requireOff(t *testing.T, profile string, offs map[quality.ValidatorID]bool, id quality.ValidatorID) {
	t.Helper()
	if !offs[id] {
		t.Errorf("%s: expected %s to be off", profile, id)
	}
}
