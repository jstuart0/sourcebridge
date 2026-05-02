// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package quality_test — frontier golden test (D14b).
//
// TestDefaultProfile_FrontierMatchesPreCA150 locks the frontier
// materialization against the literal taken from commit 06119b3, the
// pre-CA-150 base commit. This test NEVER changes after Phase 2; it is
// the proof that the materializer doesn't drift frontier behavior.
package quality_test

import (
	"reflect"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// preCA150Profiles is a verbatim capture of the defaultProfiles literal from
// commit 06119b3:internal/quality/profile.go. The Tier field is set to
// TierFrontier because that is what the materializer now sets explicitly;
// the pre-CA-150 code had no Tier field (added in Phase 1), so the captured
// rules are the source of truth. DO NOT modify this literal after Phase 2.
var preCA150Profiles = []struct {
	Template quality.Template
	Audience quality.Audience
	Rules    []quality.ValidatorRule
}{
	{
		Template: quality.TemplateArchitecture,
		Audience: quality.AudienceEngineers,
		Rules: []quality.ValidatorRule{
			{ValidatorID: quality.ValidatorCitationDensity, Level: quality.LevelGate,
				Config: quality.ValidatorConfig{CitationDensityWordsPerCitation: 200}},
			{ValidatorID: quality.ValidatorVagueness, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorFactualGrounding, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorEmptyHeadline, Level: quality.LevelWarning},
			{ValidatorID: quality.ValidatorReadingLevel, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{ReadingLevelFloor: 50}},
			{ValidatorID: quality.ValidatorCodeExamplePresent, Level: quality.LevelWarning},
			{ValidatorID: quality.ValidatorBlockCount, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{BlockCountMin: 2, BlockCountMax: 20}},
		},
	},
	{
		Template: quality.TemplateArchitecture,
		Audience: quality.AudienceProduct,
		Rules: []quality.ValidatorRule{
			{ValidatorID: quality.ValidatorVagueness, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorFactualGrounding, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorEmptyHeadline, Level: quality.LevelWarning},
			{ValidatorID: quality.ValidatorReadingLevel, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{ReadingLevelFloor: 50}},
			{ValidatorID: quality.ValidatorCitationDensity, Level: quality.LevelOff},
			{ValidatorID: quality.ValidatorCodeExamplePresent, Level: quality.LevelOff},
		},
	},
	{
		Template: quality.TemplateAPIReference,
		Audience: quality.AudienceEngineers,
		Rules: []quality.ValidatorRule{
			{ValidatorID: quality.ValidatorCitationDensity, Level: quality.LevelGate,
				Config: quality.ValidatorConfig{CitationDensityWordsPerCitation: 100}},
			{ValidatorID: quality.ValidatorCodeExamplePresent, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorVagueness, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorFactualGrounding, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorReadingLevel, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{ReadingLevelFloor: 50}},
			{ValidatorID: quality.ValidatorBlockCount, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{BlockCountMin: 3, BlockCountMax: 30}},
		},
	},
	{
		Template: quality.TemplateADR,
		Audience: quality.AudienceEngineers,
		Rules: []quality.ValidatorRule{
			{ValidatorID: quality.ValidatorFactualGrounding, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorVagueness, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorReadingLevel, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{ReadingLevelFloor: 40}},
			{ValidatorID: quality.ValidatorCitationDensity, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{CitationDensityWordsPerCitation: 200}},
		},
	},
	{
		Template: quality.TemplateGlossary,
		Audience: quality.AudienceEngineers,
		Rules: []quality.ValidatorRule{
			{ValidatorID: quality.ValidatorFactualGrounding, Level: quality.LevelGate},
		},
	},
	{
		Template: quality.TemplateActivityLog,
		Audience: quality.AudienceEngineers,
		Rules: []quality.ValidatorRule{
			{ValidatorID: quality.ValidatorFactualGrounding, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorVagueness, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorReadingLevel, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{ReadingLevelFloor: 50}},
		},
	},
	{
		Template: quality.TemplateSystemOverview,
		Audience: quality.AudienceEngineers,
		Rules: []quality.ValidatorRule{
			{ValidatorID: quality.ValidatorVagueness, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorArchitecturalRelevance, Level: quality.LevelGate,
				Config: quality.ValidatorConfig{ArchRelevanceMinPageRefs: 2, ArchRelevanceMinGraphRelations: 5}},
			{ValidatorID: quality.ValidatorEmptyHeadline, Level: quality.LevelWarning},
			{ValidatorID: quality.ValidatorReadingLevel, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{ReadingLevelFloor: 50}},
		},
	},
	{
		Template: quality.TemplateSystemOverview,
		Audience: quality.AudienceProduct,
		Rules: []quality.ValidatorRule{
			{ValidatorID: quality.ValidatorVagueness, Level: quality.LevelGate},
			{ValidatorID: quality.ValidatorArchitecturalRelevance, Level: quality.LevelGate,
				Config: quality.ValidatorConfig{ArchRelevanceMinPageRefs: 2, ArchRelevanceMinGraphRelations: 5}},
			{ValidatorID: quality.ValidatorEmptyHeadline, Level: quality.LevelWarning},
			{ValidatorID: quality.ValidatorReadingLevel, Level: quality.LevelWarning,
				Config: quality.ValidatorConfig{ReadingLevelFloor: 55}},
			{ValidatorID: quality.ValidatorCitationDensity, Level: quality.LevelOff},
			{ValidatorID: quality.ValidatorCodeExamplePresent, Level: quality.LevelOff},
		},
	},
}

// TestDefaultProfile_FrontierMatchesPreCA150 asserts that every frontier
// materialization is byte-identical to the pre-CA-150 literal. This test
// NEVER changes after Phase 2 — it locks frontier behavior against any
// future rewrite of profile.go.
func TestDefaultProfile_FrontierMatchesPreCA150(t *testing.T) {
	t.Parallel()

	for _, want := range preCA150Profiles {
		got, ok := quality.DefaultProfile(want.Template, want.Audience, modeltier.TierFrontier)
		if !ok {
			t.Errorf("DefaultProfile(%s, %s, frontier) returned false; expected profile to exist",
				want.Template, want.Audience)
			continue
		}

		if got.Template != want.Template {
			t.Errorf("[%s/%s] Template mismatch: got %q, want %q",
				want.Template, want.Audience, got.Template, want.Template)
		}
		if got.Audience != want.Audience {
			t.Errorf("[%s/%s] Audience mismatch: got %q, want %q",
				want.Template, want.Audience, got.Audience, want.Audience)
		}
		if got.Tier != modeltier.TierFrontier {
			t.Errorf("[%s/%s] Tier mismatch: got %q, want %q",
				want.Template, want.Audience, got.Tier, modeltier.TierFrontier)
		}

		if len(got.Rules) != len(want.Rules) {
			t.Errorf("[%s/%s] Rules length mismatch: got %d, want %d",
				want.Template, want.Audience, len(got.Rules), len(want.Rules))
			continue
		}

		for i, wantRule := range want.Rules {
			gotRule := got.Rules[i]
			if gotRule.ValidatorID != wantRule.ValidatorID {
				t.Errorf("[%s/%s] Rules[%d].ValidatorID: got %q, want %q",
					want.Template, want.Audience, i, gotRule.ValidatorID, wantRule.ValidatorID)
			}
			if gotRule.Level != wantRule.Level {
				t.Errorf("[%s/%s] Rules[%d] (%s) Level: got %q, want %q",
					want.Template, want.Audience, i, wantRule.ValidatorID, gotRule.Level, wantRule.Level)
			}
			if !reflect.DeepEqual(gotRule.Config, wantRule.Config) {
				t.Errorf("[%s/%s] Rules[%d] (%s) Config mismatch:\n  got:  %+v\n  want: %+v",
					want.Template, want.Audience, i, wantRule.ValidatorID, gotRule.Config, wantRule.Config)
			}
		}
	}
}
