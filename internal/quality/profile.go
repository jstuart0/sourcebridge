// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package quality

import "fmt"

// Template identifies the page template.
type Template string

const (
	TemplateArchitecture  Template = "architecture"
	TemplateAPIReference  Template = "api_reference"
	TemplateADR           Template = "adr"
	TemplateGlossary      Template = "glossary"
	TemplateActivityLog   Template = "activity_log"
	TemplateSystemOverview Template = "system_overview"
)

// Audience identifies the target audience for the page.
type Audience string

const (
	AudienceEngineers Audience = "for-engineers"
	AudienceProduct   Audience = "for-product"
	AudienceOperators Audience = "for-operators"
)

// GateLevel classifies how a validator failure is treated.
type GateLevel string

const (
	// LevelGate means the page must not ship with this violation.
	// Gate failures trigger the retry policy.
	LevelGate GateLevel = "gate"

	// LevelWarning means the page ships but the violation is attached
	// to the PR description for reviewer attention.
	LevelWarning GateLevel = "warning"

	// LevelOff means the validator is not applied for this
	// template+audience combination.
	LevelOff GateLevel = "off"
)

// ValidatorRule binds a validator to its gate level and config overrides
// for a specific template+audience profile.
type ValidatorRule struct {
	ValidatorID ValidatorID
	Level       GateLevel
	Config      ValidatorConfig // zero fields inherit global defaults
}

// Profile is the set of validator rules for a specific template+audience
// combination. Customer overrides are applied on top of defaults at
// runtime via a registration API (not implemented yet).
type Profile struct {
	Template Template
	Audience Audience
	Rules    []ValidatorRule
}

// defaultProfiles encodes the per-template, per-audience gate/warning
// table from the plan's Q.2 section as Go literals.
// These are the source of truth for default behavior; customer overrides
// layer on top via a future registration API.
var defaultProfiles = []Profile{
	{
		Template: TemplateArchitecture,
		Audience: AudienceEngineers,
		Rules: []ValidatorRule{
			{ValidatorID: ValidatorCitationDensity, Level: LevelGate,
				Config: ValidatorConfig{CitationDensityWordsPerCitation: 200}},
			{ValidatorID: ValidatorVagueness, Level: LevelGate},
			{ValidatorID: ValidatorFactualGrounding, Level: LevelGate},
			{ValidatorID: ValidatorEmptyHeadline, Level: LevelWarning},
			{ValidatorID: ValidatorReadingLevel, Level: LevelWarning,
				Config: ValidatorConfig{ReadingLevelFloor: 50}},
			// code_example_present is a warning, not a gate — some
			// packages are pure interfaces with no runnable example.
			{ValidatorID: ValidatorCodeExamplePresent, Level: LevelWarning},
			{ValidatorID: ValidatorBlockCount, Level: LevelWarning,
				Config: ValidatorConfig{BlockCountMin: 2, BlockCountMax: 20}},
		},
	},
	{
		Template: TemplateArchitecture,
		Audience: AudienceProduct,
		Rules: []ValidatorRule{
			{ValidatorID: ValidatorVagueness, Level: LevelGate},
			{ValidatorID: ValidatorFactualGrounding, Level: LevelGate},
			{ValidatorID: ValidatorEmptyHeadline, Level: LevelWarning},
			{ValidatorID: ValidatorReadingLevel, Level: LevelWarning,
				Config: ValidatorConfig{ReadingLevelFloor: 50}},
			// No citation density gate for product audience: PMs don't
			// read code links and citations would confuse the page.
			{ValidatorID: ValidatorCitationDensity, Level: LevelOff},
			// No code example gate for product audience.
			{ValidatorID: ValidatorCodeExamplePresent, Level: LevelOff},
		},
	},
	{
		Template: TemplateAPIReference,
		Audience: AudienceEngineers,
		Rules: []ValidatorRule{
			// Higher citation density for API reference: inline examples mandatory.
			{ValidatorID: ValidatorCitationDensity, Level: LevelGate,
				Config: ValidatorConfig{CitationDensityWordsPerCitation: 100}},
			{ValidatorID: ValidatorCodeExamplePresent, Level: LevelGate},
			{ValidatorID: ValidatorVagueness, Level: LevelGate},
			{ValidatorID: ValidatorFactualGrounding, Level: LevelGate},
			{ValidatorID: ValidatorReadingLevel, Level: LevelWarning,
				Config: ValidatorConfig{ReadingLevelFloor: 50}},
			{ValidatorID: ValidatorBlockCount, Level: LevelWarning,
				Config: ValidatorConfig{BlockCountMin: 3, BlockCountMax: 30}},
		},
	},
	{
		Template: TemplateADR,
		Audience: AudienceEngineers,
		Rules: []ValidatorRule{
			{ValidatorID: ValidatorFactualGrounding, Level: LevelGate},
			{ValidatorID: ValidatorVagueness, Level: LevelGate},
			// ADRs are short and dense; reading_level floor lowered to 40.
			{ValidatorID: ValidatorReadingLevel, Level: LevelWarning,
				Config: ValidatorConfig{ReadingLevelFloor: 40}},
			// citation_density is a warning, not a gate, for ADRs.
			{ValidatorID: ValidatorCitationDensity, Level: LevelWarning,
				Config: ValidatorConfig{CitationDensityWordsPerCitation: 200}},
		},
	},
	{
		Template: TemplateGlossary,
		Audience: AudienceEngineers,
		Rules: []ValidatorRule{
			// Mechanical extraction: only factual_grounding applies.
			// Voice validators (vagueness, reading_level) don't apply.
			{ValidatorID: ValidatorFactualGrounding, Level: LevelGate},
		},
	},
	{
		Template: TemplateActivityLog,
		Audience: AudienceEngineers,
		Rules: []ValidatorRule{
			{ValidatorID: ValidatorFactualGrounding, Level: LevelGate},
			// Numbers everywhere; vagueness gate matters.
			{ValidatorID: ValidatorVagueness, Level: LevelGate},
			{ValidatorID: ValidatorReadingLevel, Level: LevelWarning,
				Config: ValidatorConfig{ReadingLevelFloor: 50}},
		},
	},
	{
		Template: TemplateSystemOverview,
		Audience: AudienceEngineers,
		Rules: []ValidatorRule{
			{ValidatorID: ValidatorVagueness, Level: LevelGate},
			// architectural_relevance ensures we don't summarize a system
			// that's just a thin wrapper with no real callers.
			{ValidatorID: ValidatorArchitecturalRelevance, Level: LevelGate,
				Config: ValidatorConfig{ArchRelevanceMinPageRefs: 2, ArchRelevanceMinGraphRelations: 5}},
			{ValidatorID: ValidatorEmptyHeadline, Level: LevelWarning},
			{ValidatorID: ValidatorReadingLevel, Level: LevelWarning,
				Config: ValidatorConfig{ReadingLevelFloor: 50}},
		},
	},
	{
		Template: TemplateSystemOverview,
		Audience: AudienceProduct,
		Rules: []ValidatorRule{
			{ValidatorID: ValidatorVagueness, Level: LevelGate},
			{ValidatorID: ValidatorArchitecturalRelevance, Level: LevelGate,
				Config: ValidatorConfig{ArchRelevanceMinPageRefs: 2, ArchRelevanceMinGraphRelations: 5}},
			{ValidatorID: ValidatorEmptyHeadline, Level: LevelWarning},
			{ValidatorID: ValidatorReadingLevel, Level: LevelWarning,
				Config: ValidatorConfig{ReadingLevelFloor: 55}},
			{ValidatorID: ValidatorCitationDensity, Level: LevelOff},
			{ValidatorID: ValidatorCodeExamplePresent, Level: LevelOff},
		},
	},
}

// DefaultProfile returns the built-in profile for the given template and
// audience. Returns (Profile{}, false) when no profile is defined for the
// combination; callers should fall back to a sensible default or skip
// validation for unknown combinations.
func DefaultProfile(template Template, audience Audience) (Profile, bool) {
	for _, p := range defaultProfiles {
		if p.Template == template && p.Audience == audience {
			return p, true
		}
	}
	return Profile{}, false
}

// AllDefaultProfiles returns a copy of all built-in profiles in a
// deterministic order matching the plan's template table.
func AllDefaultProfiles() []Profile {
	out := make([]Profile, len(defaultProfiles))
	copy(out, defaultProfiles)
	return out
}

// (Profile.ruleFor used to live here; the validator dispatch loop now
// iterates Rules directly rather than looking up by ValidatorID, so
// the helper has no callers. Removed to satisfy lint.)

// String returns a human-readable identifier for the profile.
func (p Profile) String() string {
	return fmt.Sprintf("%s/%s", p.Template, p.Audience)
}
