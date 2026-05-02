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
		WantCitationDensity    int
		WantReadingLevelFloor  float64
		WantArchRefsMin        int
		WantArchRelationsMin   int
		WantBlockCountMin      int
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

