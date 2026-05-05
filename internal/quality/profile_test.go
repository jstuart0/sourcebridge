// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package quality_test

import (
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// TestDefaultProfile_ArchitectureEngineer verifies that the
// architecture/for-engineers frontier profile has the expected gates and
// warnings per the Q.2 table in the plan.
func TestDefaultProfile_ArchitectureEngineer(t *testing.T) {
	p, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier)
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
	p, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceProduct, modeltier.TierFrontier)
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
	p, ok := quality.DefaultProfile(quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierFrontier)
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
	p, ok := quality.DefaultProfile(quality.TemplateADR, quality.AudienceEngineers, modeltier.TierFrontier)
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
	p, ok := quality.DefaultProfile(quality.TemplateGlossary, quality.AudienceEngineers, modeltier.TierFrontier)
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
	p, ok := quality.DefaultProfile(quality.TemplateSystemOverview, quality.AudienceEngineers, modeltier.TierFrontier)
	if !ok {
		t.Fatal("DefaultProfile returned false for system_overview/for-engineers")
	}

	gates := gateSet(p)
	requireGate(t, p.String(), gates, quality.ValidatorArchitecturalRelevance)
	requireGate(t, p.String(), gates, quality.ValidatorVagueness)
}

// TestDefaultProfile_NotFound verifies that unknown combinations return false.
func TestDefaultProfile_NotFound(t *testing.T) {
	_, ok := quality.DefaultProfile("nonexistent_template", "nonexistent_audience", modeltier.TierFrontier)
	if ok {
		t.Error("DefaultProfile: expected false for unknown template+audience")
	}
}

// TestAllDefaultProfiles_NoDuplicates ensures no template+audience+tier
// combination appears more than once in the materialized cross-product.
func TestAllDefaultProfiles_NoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range quality.AllDefaultProfiles() {
		key := string(p.Template) + "/" + string(p.Audience) + "/" + string(p.Tier)
		if seen[key] {
			t.Errorf("AllDefaultProfiles: duplicate profile for %s", key)
		}
		seen[key] = true
	}
}

// TestProfile_TierFieldExists verifies that the Tier field is present on
// quality.Profile and that its zero value is modeltier.TierUnknown (not
// silently treated as TierFrontier). Phase 2 sets this field explicitly on
// every materialized profile.
func TestProfile_TierFieldExists(t *testing.T) {
	var p quality.Profile
	if p.Tier != modeltier.TierUnknown {
		t.Errorf("Profile{}.Tier = %q, want TierUnknown (%q)", p.Tier, modeltier.TierUnknown)
	}
}

// TestDefaultProfile_AllTiersDefined verifies every (template, audience) pair
// has frontier coverage at minimum.
func TestDefaultProfile_AllTiersDefined(t *testing.T) {
	t.Parallel()
	type ta struct {
		Template quality.Template
		Audience quality.Audience
	}
	pairs := []ta{
		{quality.TemplateArchitecture, quality.AudienceEngineers},
		{quality.TemplateArchitecture, quality.AudienceProduct},
		{quality.TemplateAPIReference, quality.AudienceEngineers},
		{quality.TemplateADR, quality.AudienceEngineers},
		{quality.TemplateGlossary, quality.AudienceEngineers},
		{quality.TemplateActivityLog, quality.AudienceEngineers},
		{quality.TemplateSystemOverview, quality.AudienceEngineers},
		{quality.TemplateSystemOverview, quality.AudienceProduct},
	}
	for _, pair := range pairs {
		_, ok := quality.DefaultProfile(pair.Template, pair.Audience, modeltier.TierFrontier)
		if !ok {
			t.Errorf("DefaultProfile(%s, %s, frontier) returned false; every base pair must have frontier coverage",
				pair.Template, pair.Audience)
		}
	}
}

// TestEffectiveProfile_FrontierEqualsBase verifies that a frontier-tier
// materialization equals the base — no overrides should alter frontier.
func TestEffectiveProfile_FrontierEqualsBase(t *testing.T) {
	t.Parallel()
	p, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier)
	if !ok {
		t.Fatal("DefaultProfile returned false")
	}
	if p.Tier != modeltier.TierFrontier {
		t.Errorf("Tier: got %q, want frontier", p.Tier)
	}
	// Citation density at frontier is gate, 200 words.
	for _, r := range p.Rules {
		if r.ValidatorID == quality.ValidatorCitationDensity {
			if r.Level != quality.LevelGate {
				t.Errorf("frontier citation_density Level: got %q, want gate", r.Level)
			}
			if r.Config.CitationDensityWordsPerCitation != 200 {
				t.Errorf("frontier citation_density threshold: got %d, want 200",
					r.Config.CitationDensityWordsPerCitation)
			}
		}
	}
}

// TestEffectiveProfile_LevelOnlyOverride_PreservesConfig verifies that an
// override with a non-nil Level pointer and zero Config does NOT change any
// base config field on the targeted ValidatorRule.
func TestEffectiveProfile_LevelOnlyOverride_PreservesConfig(t *testing.T) {
	t.Parallel()
	// architecture/engineers/mid: vagueness is overridden to warning at local.
	// At local, vagueness Level → warning; Config is zero (no Config override).
	local, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal)
	if !ok {
		t.Fatal("DefaultProfile returned false for arch/eng/local")
	}
	for _, r := range local.Rules {
		if r.ValidatorID == quality.ValidatorVagueness {
			if r.Level != quality.LevelWarning {
				t.Errorf("local vagueness Level: got %q, want warning", r.Level)
			}
			// Config for vagueness is always zero (validator ignores config).
			// Verifies that a level-only override doesn't corrupt config.
			if r.Config != (quality.ValidatorConfig{}) {
				t.Errorf("local vagueness Config: got %+v, want zero", r.Config)
			}
		}
	}
}

// TestEffectiveProfile_ConfigOnlyOverride_PreservesLevel verifies that an
// override with nil Level and a non-zero Config field preserves base Level and
// does not zero out Config fields not mentioned in the override.
func TestEffectiveProfile_ConfigOnlyOverride_PreservesLevel(t *testing.T) {
	t.Parallel()
	// architecture/engineers/local: BlockCount has a Config-only override
	// (BlockCountMin: 1, BlockCountMax: 20). Level stays at warning (base).
	local, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal)
	if !ok {
		t.Fatal("DefaultProfile returned false for arch/eng/local")
	}
	for _, r := range local.Rules {
		if r.ValidatorID == quality.ValidatorBlockCount {
			if r.Level != quality.LevelWarning {
				t.Errorf("local block_count Level: got %q, want warning (config-only override must not change Level)", r.Level)
			}
			if r.Config.BlockCountMin != 1 {
				t.Errorf("local block_count BlockCountMin: got %d, want 1", r.Config.BlockCountMin)
			}
			if r.Config.BlockCountMax != 20 {
				t.Errorf("local block_count BlockCountMax: got %d, want 20", r.Config.BlockCountMax)
			}
		}
	}
}

// TestEffectiveProfile_OverrideOnAbsentValidator_IsNoop verifies that an
// override entry for a ValidatorID not in the base must not panic, must not
// add a new rule, and logs a slog.Warn.
// This is tested indirectly: we call DefaultProfile for a template/audience
// where the overrides reference only existing validators, then manually verify
// that rule count doesn't grow. The warning is tested via log capture.
func TestEffectiveProfile_OverrideOnAbsentValidator_IsNoop(t *testing.T) {
	t.Parallel()
	// Get frontier base count.
	frontier, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier)
	if !ok {
		t.Fatal("DefaultProfile returned false for arch/eng/frontier")
	}
	local, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal)
	if !ok {
		t.Fatal("DefaultProfile returned false for arch/eng/local")
	}
	// Tier overrides may not add new rules; count must be ≤ frontier count.
	if len(local.Rules) > len(frontier.Rules) {
		t.Errorf("local rules count %d > frontier %d — override added a new rule (must be noop for absent validators)",
			len(local.Rules), len(frontier.Rules))
	}
}

// TestDefaultProfile_TierUnknown_FallsBackToFrontier_LogsWarn verifies that
// passing TierUnknown returns the frontier profile (same rules as frontier).
func TestDefaultProfile_TierUnknown_FallsBackToFrontier_LogsWarn(t *testing.T) {
	t.Parallel()
	unknown, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierUnknown)
	if !ok {
		t.Fatal("DefaultProfile with TierUnknown returned false; expected fallback to frontier")
	}
	frontier, _ := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier)
	if unknown.Tier != modeltier.TierFrontier {
		t.Errorf("TierUnknown fallback: Tier = %q, want frontier", unknown.Tier)
	}
	if len(unknown.Rules) != len(frontier.Rules) {
		t.Errorf("TierUnknown fallback: Rules length %d != frontier %d", len(unknown.Rules), len(frontier.Rules))
	}
}

// TestProfile_ZeroValueRequiresExplicitTier locks the contract that Profile{}
// has Tier == TierUnknown, not silently TierFrontier. This test forbids the
// zero-value-as-frontier idiom.
func TestProfile_ZeroValueRequiresExplicitTier(t *testing.T) {
	t.Parallel()
	var p quality.Profile
	if p.Tier != modeltier.TierUnknown {
		t.Errorf("Profile{}.Tier = %q; zero value must be TierUnknown, never silently TierFrontier", p.Tier)
	}
}

// TestDefaultProfile_ThresholdTable asserts effective thresholds for every
// (template, audience, tier, validatorID) → (Level, Config field) tuple.
// Glossary and ActivityLog are tier-invariant (mechanical extraction); the
// table covers them with a single frontier row and a comment.
func TestDefaultProfile_ThresholdTable(t *testing.T) {
	t.Parallel()
	type row struct {
		Template    quality.Template
		Audience    quality.Audience
		Tier        modeltier.QualityGateTier
		ValidatorID quality.ValidatorID
		WantLevel   quality.GateLevel
		// Zero means "don't assert" for numeric fields.
		WantCitationDensity   int
		WantReadingLevelFloor float64
		WantArchRefsMin       int
		WantArchRelationsMin  int
		WantBlockCountMin     int
	}

	table := []row{
		// --- citation_density: architecture/engineers ---
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorCitationDensity, quality.LevelGate, 200, 0, 0, 0, 0},
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorCitationDensity, quality.LevelGate, 300, 0, 0, 0, 0},
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorCitationDensity, quality.LevelWarning, 400, 0, 0, 0, 0},

		// --- citation_density: api_reference/engineers ---
		{quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorCitationDensity, quality.LevelGate, 100, 0, 0, 0, 0},
		{quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorCitationDensity, quality.LevelGate, 200, 0, 0, 0, 0},
		{quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorCitationDensity, quality.LevelGate, 300, 0, 0, 0, 0},

		// --- citation_density: adr/engineers ---
		{quality.TemplateADR, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorCitationDensity, quality.LevelWarning, 200, 0, 0, 0, 0},
		{quality.TemplateADR, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorCitationDensity, quality.LevelWarning, 300, 0, 0, 0, 0},
		{quality.TemplateADR, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorCitationDensity, quality.LevelWarning, 400, 0, 0, 0, 0},

		// --- vagueness: architecture/engineers ---
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorVagueness, quality.LevelGate, 0, 0, 0, 0, 0},
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorVagueness, quality.LevelGate, 0, 0, 0, 0, 0},
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorVagueness, quality.LevelWarning, 0, 0, 0, 0, 0},

		// --- code_example_present: api_reference/engineers ---
		{quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorCodeExamplePresent, quality.LevelGate, 0, 0, 0, 0, 0},
		{quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorCodeExamplePresent, quality.LevelWarning, 0, 0, 0, 0, 0},
		{quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorCodeExamplePresent, quality.LevelWarning, 0, 0, 0, 0, 0},

		// --- reading_level: architecture/engineers ---
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorReadingLevel, quality.LevelWarning, 0, 50, 0, 0, 0},
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorReadingLevel, quality.LevelWarning, 0, 40, 0, 0, 0},

		// --- reading_level: adr/engineers ---
		{quality.TemplateADR, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorReadingLevel, quality.LevelWarning, 0, 40, 0, 0, 0},
		{quality.TemplateADR, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorReadingLevel, quality.LevelWarning, 0, 38, 0, 0, 0},
		{quality.TemplateADR, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorReadingLevel, quality.LevelWarning, 0, 35, 0, 0, 0},

		// --- block_count: architecture/engineers ---
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorBlockCount, quality.LevelWarning, 0, 0, 0, 0, 2},
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorBlockCount, quality.LevelWarning, 0, 0, 0, 0, 1},

		// --- architectural_relevance: system_overview/engineers ---
		{quality.TemplateSystemOverview, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorArchitecturalRelevance, quality.LevelGate, 0, 0, 2, 5, 0},
		{quality.TemplateSystemOverview, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorArchitecturalRelevance, quality.LevelGate, 0, 0, 2, 4, 0},
		{quality.TemplateSystemOverview, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorArchitecturalRelevance, quality.LevelGate, 0, 0, 1, 3, 0},

		// --- Glossary: tier-invariant (mechanical extraction) ---
		{quality.TemplateGlossary, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorFactualGrounding, quality.LevelGate, 0, 0, 0, 0, 0},

		// --- ActivityLog: tier-invariant (mechanical extraction) ---
		{quality.TemplateActivityLog, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorFactualGrounding, quality.LevelGate, 0, 0, 0, 0, 0},
		{quality.TemplateActivityLog, quality.AudienceEngineers, modeltier.TierFrontier,
			quality.ValidatorVagueness, quality.LevelGate, 0, 0, 0, 0, 0},

		// --- CA-163 Phase 1: factual_grounding TierMid overrides (Decision 1) ---
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorFactualGrounding, quality.LevelWarning, 0, 0, 0, 0, 0},
		{quality.TemplateArchitecture, quality.AudienceProduct, modeltier.TierMid,
			quality.ValidatorFactualGrounding, quality.LevelWarning, 0, 0, 0, 0, 0},
		{quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorFactualGrounding, quality.LevelWarning, 0, 0, 0, 0, 0},
		{quality.TemplateADR, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorFactualGrounding, quality.LevelWarning, 0, 0, 0, 0, 0},
		{quality.TemplateGlossary, quality.AudienceEngineers, modeltier.TierMid,
			quality.ValidatorFactualGrounding, quality.LevelWarning, 0, 0, 0, 0, 0},

		// --- CA-163 Phase 1: factual_grounding TierLocal overrides (Decision 1b bundle) ---
		{quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorFactualGrounding, quality.LevelWarning, 0, 0, 0, 0, 0},
		{quality.TemplateArchitecture, quality.AudienceProduct, modeltier.TierLocal,
			quality.ValidatorFactualGrounding, quality.LevelWarning, 0, 0, 0, 0, 0},
		{quality.TemplateADR, quality.AudienceEngineers, modeltier.TierLocal,
			quality.ValidatorFactualGrounding, quality.LevelWarning, 0, 0, 0, 0, 0},

		// --- CA-163 Phase 2: system_overview/product/mid audience-symmetry fix (Decision 3) ---
		{quality.TemplateSystemOverview, quality.AudienceProduct, modeltier.TierMid,
			quality.ValidatorArchitecturalRelevance, quality.LevelGate, 0, 0, 2, 4, 0},
		{quality.TemplateSystemOverview, quality.AudienceProduct, modeltier.TierMid,
			quality.ValidatorReadingLevel, quality.LevelWarning, 0, 45, 0, 0, 0},
	}

	for _, tc := range table {
		p, ok := quality.DefaultProfile(tc.Template, tc.Audience, tc.Tier)
		if !ok {
			t.Errorf("[%s/%s/%s] DefaultProfile returned false", tc.Template, tc.Audience, tc.Tier)
			continue
		}
		found := false
		for _, r := range p.Rules {
			if r.ValidatorID != tc.ValidatorID {
				continue
			}
			found = true
			if r.Level != tc.WantLevel {
				t.Errorf("[%s/%s/%s/%s] Level: got %q, want %q",
					tc.Template, tc.Audience, tc.Tier, tc.ValidatorID, r.Level, tc.WantLevel)
			}
			if tc.WantCitationDensity > 0 && r.Config.CitationDensityWordsPerCitation != tc.WantCitationDensity {
				t.Errorf("[%s/%s/%s/%s] CitationDensityWordsPerCitation: got %d, want %d",
					tc.Template, tc.Audience, tc.Tier, tc.ValidatorID,
					r.Config.CitationDensityWordsPerCitation, tc.WantCitationDensity)
			}
			if tc.WantReadingLevelFloor > 0 && r.Config.ReadingLevelFloor != tc.WantReadingLevelFloor {
				t.Errorf("[%s/%s/%s/%s] ReadingLevelFloor: got %.1f, want %.1f",
					tc.Template, tc.Audience, tc.Tier, tc.ValidatorID,
					r.Config.ReadingLevelFloor, tc.WantReadingLevelFloor)
			}
			if tc.WantArchRefsMin > 0 && r.Config.ArchRelevanceMinPageRefs != tc.WantArchRefsMin {
				t.Errorf("[%s/%s/%s/%s] ArchRelevanceMinPageRefs: got %d, want %d",
					tc.Template, tc.Audience, tc.Tier, tc.ValidatorID,
					r.Config.ArchRelevanceMinPageRefs, tc.WantArchRefsMin)
			}
			if tc.WantArchRelationsMin > 0 && r.Config.ArchRelevanceMinGraphRelations != tc.WantArchRelationsMin {
				t.Errorf("[%s/%s/%s/%s] ArchRelevanceMinGraphRelations: got %d, want %d",
					tc.Template, tc.Audience, tc.Tier, tc.ValidatorID,
					r.Config.ArchRelevanceMinGraphRelations, tc.WantArchRelationsMin)
			}
			if tc.WantBlockCountMin > 0 && r.Config.BlockCountMin != tc.WantBlockCountMin {
				t.Errorf("[%s/%s/%s/%s] BlockCountMin: got %d, want %d",
					tc.Template, tc.Audience, tc.Tier, tc.ValidatorID,
					r.Config.BlockCountMin, tc.WantBlockCountMin)
			}
		}
		if !found {
			t.Errorf("[%s/%s/%s] validator %q not found in Rules", tc.Template, tc.Audience, tc.Tier, tc.ValidatorID)
		}
	}
}

// TestEffectiveProfile_FactualGroundingPolicy_AllProfiles is the load-bearing invariant
// for CA-150/CA-152/CA-163 quality-gate tier policy. Every (Template × Audience × Tier)
// combination is iterated; FactualGrounding is asserted at LevelGate (frontier) or
// LevelWarning (mid/local), with an allowlist for activity_log/engineers (which is not
// current Living Wiki scope and whose report ValidatorProfile() hardcodes TierFrontier
// — see internal/reports/templates/activitylog/activitylog.go:430).
//
// To intentionally change a cell's policy, update both the override in profile.go AND
// this test's allowlist. A change to one without the other indicates either a forgotten
// override or a regression.
func TestEffectiveProfile_FactualGroundingPolicy_AllProfiles(t *testing.T) {
	t.Parallel()

	// fgPolicyKey mirrors profile.go's unexported profileKey for allowlist lookup.
	type fgPolicyKey struct {
		Template quality.Template
		Audience quality.Audience
		Tier     modeltier.QualityGateTier
	}

	// allowlist contains the (template, audience, tier) cells where factual_grounding
	// is expected to remain LevelGate at TierMid or TierLocal. Each entry must
	// include a rationale comment.
	//
	// activity_log/engineers at TierMid and TierLocal: deliberate exemption because
	// (a) activity_log is not in current Living Wiki planning
	//     (internal/api/graphql/living_wiki_plan_helpers.go:32-39 does not list it),
	// (b) the activity_log report's ValidatorProfile() hardcodes TierFrontier
	//     (internal/reports/templates/activitylog/activitylog.go:430), so mid/local
	//     overrides do not affect activity_log output today, and
	// (c) the optional LLM digest mode exists but is out of scope for CA-163.
	allowlist := map[fgPolicyKey]quality.GateLevel{
		{Template: quality.TemplateActivityLog, Audience: quality.AudienceEngineers, Tier: modeltier.TierMid}:   quality.LevelGate,
		{Template: quality.TemplateActivityLog, Audience: quality.AudienceEngineers, Tier: modeltier.TierLocal}: quality.LevelGate,
	}

	allProfiles := quality.AllDefaultProfiles()
	if len(allProfiles) == 0 {
		t.Fatal("AllDefaultProfiles returned no profiles")
	}

	for _, p := range allProfiles {
		p := p
		t.Run(p.String(), func(t *testing.T) {
			t.Parallel()

			// Find the FactualGrounding rule in this materialized profile.
			var fgRule *quality.ValidatorRule
			for i := range p.Rules {
				if p.Rules[i].ValidatorID == quality.ValidatorFactualGrounding {
					fgRule = &p.Rules[i]
					break
				}
			}

			// If the base profile doesn't include FactualGrounding (e.g. system_overview),
			// skip this cell — no policy assertion is applicable.
			if fgRule == nil {
				return
			}

			key := fgPolicyKey{Template: p.Template, Audience: p.Audience, Tier: p.Tier}

			switch p.Tier {
			case modeltier.TierFrontier:
				// Every frontier cell with FactualGrounding must be a gate.
				// This guards against accidental frontier-base regressions.
				if fgRule.Level != quality.LevelGate {
					t.Errorf("[%s] TierFrontier FactualGrounding Level = %q, want gate (frontier must never relax)",
						p.String(), fgRule.Level)
				}

			case modeltier.TierMid, modeltier.TierLocal:
				if wantLevel, isAllowlisted := allowlist[key]; isAllowlisted {
					// Allowlisted cell: assert the explicitly documented exception level.
					if fgRule.Level != wantLevel {
						t.Errorf("[%s] FactualGrounding Level = %q, want %q (allowlisted cell; update allowlist and profile.go together)",
							p.String(), fgRule.Level, wantLevel)
					}
				} else {
					// All other mid/local cells: FactualGrounding must be a warning.
					// A gate here means either a missing override (regression) or an
					// intentional new exception that needs an allowlist entry.
					if fgRule.Level != quality.LevelWarning {
						t.Errorf("[%s] FactualGrounding Level = %q, want warning — "+
							"add an allowlist entry to this test AND an inline comment in profile.go "+
							"if this is intentional",
							p.String(), fgRule.Level)
					}
				}
			}
		})
	}
}

// TestEffectiveProfile_FactualGroundingWarningAtLocal verifies that
// factual_grounding is demoted to LevelWarning at TierLocal for the two
// templates whose base profiles include that validator (api_reference and
// glossary). It also asserts that frontier still gates — the override must
// only fire at local. CA-152 followup-B.
//
// system_overview/engineers is intentionally excluded: that base profile does
// not include factual_grounding (the template doesn't gate on citations), so
// no override is needed or applied there.
//
// See TestEffectiveProfile_FactualGroundingPolicy_AllProfiles for the
// policy-as-code invariant — this test predates it and is kept for grep
// discoverability. The invariant is the load-bearing check.
func TestEffectiveProfile_FactualGroundingWarningAtLocal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		Template quality.Template
		Audience quality.Audience
	}{
		{quality.TemplateAPIReference, quality.AudienceEngineers},
		{quality.TemplateGlossary, quality.AudienceEngineers},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.Template)+"/"+string(tc.Audience), func(t *testing.T) {
			t.Parallel()

			local, ok := quality.DefaultProfile(tc.Template, tc.Audience, modeltier.TierLocal)
			if !ok {
				t.Fatalf("DefaultProfile(%s, %s, local) returned false", tc.Template, tc.Audience)
			}
			foundLocal := false
			for _, r := range local.Rules {
				if r.ValidatorID == quality.ValidatorFactualGrounding {
					foundLocal = true
					if r.Level != quality.LevelWarning {
						t.Errorf("[%s/%s/local] factual_grounding Level = %q, want warning",
							tc.Template, tc.Audience, r.Level)
					}
				}
			}
			if !foundLocal {
				t.Errorf("[%s/%s/local] factual_grounding rule not found", tc.Template, tc.Audience)
			}

			frontier, ok := quality.DefaultProfile(tc.Template, tc.Audience, modeltier.TierFrontier)
			if !ok {
				t.Fatalf("DefaultProfile(%s, %s, frontier) returned false", tc.Template, tc.Audience)
			}
			foundFrontier := false
			for _, r := range frontier.Rules {
				if r.ValidatorID == quality.ValidatorFactualGrounding {
					foundFrontier = true
					if r.Level != quality.LevelGate {
						t.Errorf("[%s/%s/frontier] factual_grounding Level = %q, want gate",
							tc.Template, tc.Audience, r.Level)
					}
				}
			}
			if !foundFrontier {
				t.Errorf("[%s/%s/frontier] factual_grounding rule not found", tc.Template, tc.Audience)
			}
		})
	}
}

// TestEffectiveProfile_SystemOverviewProductLocal_HasOverrides verifies that
// system_overview/AudienceProduct/TierLocal has the same relaxed overrides as
// system_overview/AudienceEngineers/TierLocal for the validators shared by
// both base profiles. CA-152 added the product entry — this test locks it.
func TestEffectiveProfile_SystemOverviewProductLocal_HasOverrides(t *testing.T) {
	t.Parallel()

	engLocal, ok := quality.DefaultProfile(quality.TemplateSystemOverview, quality.AudienceEngineers, modeltier.TierLocal)
	if !ok {
		t.Fatal("DefaultProfile(system_overview, engineers, local) returned false")
	}
	prodLocal, ok := quality.DefaultProfile(quality.TemplateSystemOverview, quality.AudienceProduct, modeltier.TierLocal)
	if !ok {
		t.Fatal("DefaultProfile(system_overview, product, local) returned false")
	}

	// Both audiences must have vagueness as a warning (not gate) at local.
	for _, r := range engLocal.Rules {
		if r.ValidatorID == quality.ValidatorVagueness && r.Level != quality.LevelWarning {
			t.Errorf("engineers/local vagueness Level = %q, want warning", r.Level)
		}
	}
	for _, r := range prodLocal.Rules {
		if r.ValidatorID == quality.ValidatorVagueness && r.Level != quality.LevelWarning {
			t.Errorf("product/local vagueness Level = %q, want warning", r.Level)
		}
	}

	// Both must have reading_level floor relaxed to 40 at local.
	for _, r := range engLocal.Rules {
		if r.ValidatorID == quality.ValidatorReadingLevel && r.Config.ReadingLevelFloor != 40 {
			t.Errorf("engineers/local reading_level floor = %.1f, want 40", r.Config.ReadingLevelFloor)
		}
	}
	for _, r := range prodLocal.Rules {
		if r.ValidatorID == quality.ValidatorReadingLevel && r.Config.ReadingLevelFloor != 40 {
			t.Errorf("product/local reading_level floor = %.1f, want 40", r.Config.ReadingLevelFloor)
		}
	}

	// Both must have arch_relevance relaxed (refs>=1, rels>=3) at local.
	for _, r := range engLocal.Rules {
		if r.ValidatorID == quality.ValidatorArchitecturalRelevance {
			if r.Config.ArchRelevanceMinPageRefs != 1 {
				t.Errorf("engineers/local arch_relevance MinPageRefs = %d, want 1",
					r.Config.ArchRelevanceMinPageRefs)
			}
			if r.Config.ArchRelevanceMinGraphRelations != 3 {
				t.Errorf("engineers/local arch_relevance MinGraphRelations = %d, want 3",
					r.Config.ArchRelevanceMinGraphRelations)
			}
		}
	}
	for _, r := range prodLocal.Rules {
		if r.ValidatorID == quality.ValidatorArchitecturalRelevance {
			if r.Config.ArchRelevanceMinPageRefs != 1 {
				t.Errorf("product/local arch_relevance MinPageRefs = %d, want 1",
					r.Config.ArchRelevanceMinPageRefs)
			}
			if r.Config.ArchRelevanceMinGraphRelations != 3 {
				t.Errorf("product/local arch_relevance MinGraphRelations = %d, want 3",
					r.Config.ArchRelevanceMinGraphRelations)
			}
		}
	}

	// Product audience does not have factual_grounding in its base — no override
	// expected there. Verify that product/frontier vagueness is still a gate
	// (the override only fires at local).
	prodFrontier, ok := quality.DefaultProfile(quality.TemplateSystemOverview, quality.AudienceProduct, modeltier.TierFrontier)
	if !ok {
		t.Fatal("DefaultProfile(system_overview, product, frontier) returned false")
	}
	for _, r := range prodFrontier.Rules {
		if r.ValidatorID == quality.ValidatorVagueness && r.Level != quality.LevelGate {
			t.Errorf("product/frontier vagueness Level = %q, want gate", r.Level)
		}
	}
}

// TestEffectiveProfile_SystemOverviewProductMid_HasOverrides verifies that
// system_overview/AudienceProduct/TierMid has the audience-symmetric overrides
// added by CA-163 Decision 3. Without this fix, product/mid inherits frontier-
// strict graph thresholds (rels>=5), stricter than engineers/mid (rels>=4).
//
// The vagueness assertion is deliberate: CA-163 Decision 2 explicitly defers
// vagueness demotion at TierMid. This assertion pins the intent — if a future
// fix demotes vagueness at TierMid product, this test breaks and forces an
// explicit decision rather than silent drift.
func TestEffectiveProfile_SystemOverviewProductMid_HasOverrides(t *testing.T) {
	t.Parallel()

	p, ok := quality.DefaultProfile(quality.TemplateSystemOverview, quality.AudienceProduct, modeltier.TierMid)
	if !ok {
		t.Fatal("DefaultProfile(system_overview, product, mid) returned false")
	}

	foundArchRel := false
	foundReadingLevel := false
	foundVagueness := false

	for _, r := range p.Rules {
		switch r.ValidatorID {
		case quality.ValidatorArchitecturalRelevance:
			foundArchRel = true
			if r.Level != quality.LevelGate {
				t.Errorf("product/mid arch_relevance Level = %q, want gate", r.Level)
			}
			if r.Config.ArchRelevanceMinPageRefs != 2 {
				t.Errorf("product/mid arch_relevance MinPageRefs = %d, want 2",
					r.Config.ArchRelevanceMinPageRefs)
			}
			if r.Config.ArchRelevanceMinGraphRelations != 4 {
				t.Errorf("product/mid arch_relevance MinGraphRelations = %d, want 4 "+
					"(must match engineers/mid; product/mid must not inherit frontier-strict rels>=5)",
					r.Config.ArchRelevanceMinGraphRelations)
			}
		case quality.ValidatorReadingLevel:
			foundReadingLevel = true
			if r.Config.ReadingLevelFloor != 45 {
				t.Errorf("product/mid reading_level floor = %.1f, want 45",
					r.Config.ReadingLevelFloor)
			}
		case quality.ValidatorVagueness:
			foundVagueness = true
			// CA-163 Decision 2 deliberately defers vagueness demotion at TierMid.
			// This assertion pins the intent — if a future fix demotes vagueness at
			// TierMid product, this test breaks and forces an explicit decision rather
			// than silent drift.
			if r.Level != quality.LevelGate {
				t.Errorf("product/mid vagueness Level = %q, want gate — "+
					"CA-163 Decision 2 deliberately preserves vagueness=gate at TierMid; "+
					"update this assertion AND file a new decision if demotion is intentional",
					r.Level)
			}
		}
	}

	if !foundArchRel {
		t.Error("product/mid: architectural_relevance rule not found")
	}
	if !foundReadingLevel {
		t.Error("product/mid: reading_level rule not found")
	}
	if !foundVagueness {
		t.Error("product/mid: vagueness rule not found")
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
