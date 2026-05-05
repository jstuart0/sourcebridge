// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package quality

import (
	"fmt"
	"log/slog"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
)

// Template identifies the page template.
type Template string

const (
	TemplateArchitecture   Template = "architecture"
	TemplateAPIReference   Template = "api_reference"
	TemplateADR            Template = "adr"
	TemplateGlossary       Template = "glossary"
	TemplateActivityLog    Template = "activity_log"
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

// Profile is the set of validator rules for a specific template+audience+tier
// combination. Customer overrides are applied on top of defaults at
// runtime via a registration API (not implemented yet).
//
// Tier must always be set explicitly on a materialized profile; the zero
// value (TierUnknown) signals that the profile has not been resolved.
// Callers MUST NOT treat Profile{}.Tier as TierFrontier.
type Profile struct {
	Template Template
	Audience Audience
	// Tier is the quality-gate calibration tier that was used when this
	// profile was materialized. TierUnknown (zero value) means "not yet
	// resolved"; callers should call DefaultProfile with an explicit tier
	// rather than relying on zero-value behavior.
	Tier  modeltier.QualityGateTier
	Rules []ValidatorRule
}

// String returns a human-readable identifier for the profile.
func (p Profile) String() string {
	return fmt.Sprintf("%s/%s/%s", p.Template, p.Audience, p.Tier)
}

// --- Base profiles (frontier) ---

// baseProfiles encodes the per-template, per-audience gate/warning table
// from the plan's Q.2 section as Go literals. Every entry is the frontier
// (TierFrontier) base; tier overrides are applied by applyOverrides.
// These are the source of truth for default frontier behavior; customer
// overrides layer on top via a future registration API.
var baseProfiles = []Profile{
	{
		Template: TemplateArchitecture,
		Audience: AudienceEngineers,
		Tier:     modeltier.TierFrontier,
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
		Tier:     modeltier.TierFrontier,
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
		Tier:     modeltier.TierFrontier,
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
		Tier:     modeltier.TierFrontier,
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
		Tier:     modeltier.TierFrontier,
		Rules: []ValidatorRule{
			// Mechanical extraction: only factual_grounding applies.
			// Voice validators (vagueness, reading_level) don't apply.
			{ValidatorID: ValidatorFactualGrounding, Level: LevelGate},
		},
	},
	{
		Template: TemplateActivityLog,
		Audience: AudienceEngineers,
		Tier:     modeltier.TierFrontier,
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
		Tier:     modeltier.TierFrontier,
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
		Tier:     modeltier.TierFrontier,
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

// --- Tier override table ---

// tierRuleOverride describes a delta from the frontier base for one validator.
// Level is a pointer: nil means "no level change"; non-nil means "set this level."
// Config merges field-by-field: zero fields leave base unchanged, non-zero fields
// overwrite base. Per D14a: *GateLevel is mandated, NOT a sentinel string.
type tierRuleOverride struct {
	ValidatorID ValidatorID
	Level       *GateLevel      // nil = no Level change; non-nil = replace Level
	Config      ValidatorConfig // zero fields = no config override; non-zero = override
}

// profileKey is the lookup key for the tierOverrides map.
type profileKey struct {
	Template Template
	Audience Audience
	Tier     modeltier.QualityGateTier
}

// Level value helpers — take addresses of named vars, not literals.
var (
	_levelGate    = LevelGate
	_levelWarning = LevelWarning
)

// tierOverrides maps a (template, audience, tier) triple to the list of
// per-validator deltas from the frontier base. Only entries that DIFFER
// from the frontier base are listed. Per D14a: Level is *GateLevel
// (nil = unchanged); Config merges field-by-field (zero = unchanged).
// NOTE: When this map exceeds ~20 entries, split into a dedicated
// profile_overrides.go in the same package, grouped by template. No API change required.
var tierOverrides = map[profileKey][]tierRuleOverride{
	// --- architecture / for-engineers ---
	{TemplateArchitecture, AudienceEngineers, modeltier.TierMid}: {
		{ValidatorID: ValidatorCitationDensity, Level: &_levelGate,
			Config: ValidatorConfig{CitationDensityWordsPerCitation: 300}},
		// CA-163: factual_grounding demoted to warning at TierMid — Gemini Flash
		// and other mid-tier models don't reliably emit (path:N-N) citations.
		// Production evidence: thoughts/shared/investigations/2026-05-05-living-wiki-broken-on-openrouter.md.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},
	{TemplateArchitecture, AudienceProduct, modeltier.TierMid}: {
		// CA-163: factual_grounding demoted to warning at TierMid for product
		// audience — same root cause as engineers/mid; see Decision 1.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},
	{TemplateArchitecture, AudienceEngineers, modeltier.TierLocal}: {
		{ValidatorID: ValidatorCitationDensity, Level: &_levelWarning,
			Config: ValidatorConfig{CitationDensityWordsPerCitation: 400}},
		{ValidatorID: ValidatorVagueness, Level: &_levelWarning},
		{ValidatorID: ValidatorReadingLevel, Level: &_levelWarning,
			Config: ValidatorConfig{ReadingLevelFloor: 40}},
		// Config-only override: BlockCount stays at base level (warning),
		// only thresholds change.
		{ValidatorID: ValidatorBlockCount,
			Config: ValidatorConfig{BlockCountMin: 1, BlockCountMax: 20}},
		// CA-163 (Decision 1b): factual_grounding demoted to warning at TierLocal
		// for architecture — Ollama users hit this gate today. Bundles the CA-152
		// pattern (api_reference/glossary) to its natural completion.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},
	{TemplateArchitecture, AudienceProduct, modeltier.TierLocal}: {
		// CA-163 (Decision 1b): factual_grounding demoted to warning at TierLocal
		// for architecture/product — mirrors engineers/local bundle above.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},

	// --- api_reference / for-engineers ---
	{TemplateAPIReference, AudienceEngineers, modeltier.TierMid}: {
		{ValidatorID: ValidatorCitationDensity, Level: &_levelGate,
			Config: ValidatorConfig{CitationDensityWordsPerCitation: 200}},
		{ValidatorID: ValidatorCodeExamplePresent, Level: &_levelWarning},
		{ValidatorID: ValidatorReadingLevel,
			Config: ValidatorConfig{ReadingLevelFloor: 45}},
		// CA-163: factual_grounding demoted to warning at TierMid — same prompt-
		// compliance failure mode as architecture/mid. See Decision 1.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},
	{TemplateAPIReference, AudienceEngineers, modeltier.TierLocal}: {
		{ValidatorID: ValidatorCitationDensity, Level: &_levelGate,
			Config: ValidatorConfig{CitationDensityWordsPerCitation: 300}},
		{ValidatorID: ValidatorCodeExamplePresent, Level: &_levelWarning},
		{ValidatorID: ValidatorVagueness, Level: &_levelWarning},
		{ValidatorID: ValidatorReadingLevel,
			Config: ValidatorConfig{ReadingLevelFloor: 40}},
		// CA-152: factual_grounding demoted to warning at TierLocal — local LLMs
		// (qwen3 class) don't reliably emit citations in the strict (path:N-N)
		// format the validator's regex requires. Production evidence: dick's
		// investigation 2026-05-02-qwen3-living-wiki-failures.md. Original
		// CA-150 followup-B.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},

	// --- adr / for-engineers ---
	{TemplateADR, AudienceEngineers, modeltier.TierMid}: {
		{ValidatorID: ValidatorCitationDensity, Level: &_levelWarning,
			Config: ValidatorConfig{CitationDensityWordsPerCitation: 300}},
		{ValidatorID: ValidatorReadingLevel,
			Config: ValidatorConfig{ReadingLevelFloor: 38}},
		// CA-163: factual_grounding demoted to warning at TierMid — profile-
		// completeness extension; same root cause as architecture/api_reference.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},
	{TemplateADR, AudienceEngineers, modeltier.TierLocal}: {
		{ValidatorID: ValidatorCitationDensity, Level: &_levelWarning,
			Config: ValidatorConfig{CitationDensityWordsPerCitation: 400}},
		{ValidatorID: ValidatorVagueness, Level: &_levelWarning},
		{ValidatorID: ValidatorReadingLevel,
			Config: ValidatorConfig{ReadingLevelFloor: 35}},
		// CA-163 (Decision 1b): factual_grounding demoted to warning at TierLocal
		// for adr — Ollama users hit this gate today. Bundles the CA-152 pattern.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},

	// --- system_overview / for-engineers ---
	{TemplateSystemOverview, AudienceEngineers, modeltier.TierMid}: {
		{ValidatorID: ValidatorArchitecturalRelevance, Level: &_levelGate,
			Config: ValidatorConfig{ArchRelevanceMinPageRefs: 2, ArchRelevanceMinGraphRelations: 4}},
		{ValidatorID: ValidatorReadingLevel,
			Config: ValidatorConfig{ReadingLevelFloor: 45}},
	},
	{TemplateSystemOverview, AudienceEngineers, modeltier.TierLocal}: {
		{ValidatorID: ValidatorArchitecturalRelevance, Level: &_levelGate,
			Config: ValidatorConfig{ArchRelevanceMinPageRefs: 1, ArchRelevanceMinGraphRelations: 3}},
		{ValidatorID: ValidatorVagueness, Level: &_levelWarning},
		{ValidatorID: ValidatorReadingLevel,
			Config: ValidatorConfig{ReadingLevelFloor: 40}},
		// Note: factual_grounding is not in the system_overview/engineers base
		// profile (the template doesn't gate on citations). No override needed.
	},

	// --- system_overview / for-product ---
	// CA-152: AudienceProduct was missing from TierLocal entirely — only
	// AudienceEngineers had overrides. A product-audience cold-start would hit
	// frontier thresholds for vagueness. Mirror the engineers entries for
	// validators that appear in both the engineers and product base profiles.
	// (factual_grounding is not in the product base — no override needed.)
	{TemplateSystemOverview, AudienceProduct, modeltier.TierMid}: {
		// CA-163 (Decision 3): close the audience-asymmetry inversion — without
		// this, product/mid inherits frontier-strict graph thresholds (rels>=5),
		// stricter than engineers/mid (rels>=4). Note: vagueness is deliberately
		// NOT included here (remains gate at TierMid for both audiences per Decision 2).
		{ValidatorID: ValidatorArchitecturalRelevance, Level: &_levelGate,
			Config: ValidatorConfig{ArchRelevanceMinPageRefs: 2, ArchRelevanceMinGraphRelations: 4}},
		{ValidatorID: ValidatorReadingLevel,
			Config: ValidatorConfig{ReadingLevelFloor: 45}},
	},
	{TemplateSystemOverview, AudienceProduct, modeltier.TierLocal}: {
		{ValidatorID: ValidatorArchitecturalRelevance, Level: &_levelGate,
			Config: ValidatorConfig{ArchRelevanceMinPageRefs: 1, ArchRelevanceMinGraphRelations: 3}},
		{ValidatorID: ValidatorVagueness, Level: &_levelWarning},
		{ValidatorID: ValidatorReadingLevel,
			Config: ValidatorConfig{ReadingLevelFloor: 40}},
	},

	// --- glossary / for-engineers ---
	// CA-152: factual_grounding demoted to warning at TierLocal. Glossary
	// paragraphs for symbols with empty FilePath emit no citation; doc-comments
	// with assertion verbs then fire the gate. Zero-LLM template cannot self-fix
	// on retry. Production evidence: dick's investigation
	// 2026-05-02-qwen3-living-wiki-failures.md.
	{TemplateGlossary, AudienceEngineers, modeltier.TierLocal}: {
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},
	{TemplateGlossary, AudienceEngineers, modeltier.TierMid}: {
		// CA-163: factual_grounding demoted to warning at TierMid — profile-
		// completeness extension of CA-152's TierLocal fix. Same root cause.
		{ValidatorID: ValidatorFactualGrounding, Level: &_levelWarning},
	},

	// ActivityLog is tier-invariant: numerical extraction where behavioral
	// assertions are always accompanied by data. No overrides needed.
}

// --- Materializer ---

// lookupBaseProfile returns a deep copy of the frontier base profile for the
// given template and audience, or (Profile{}, false) when not defined.
func lookupBaseProfile(template Template, audience Audience) (Profile, bool) {
	for _, p := range baseProfiles {
		if p.Template == template && p.Audience == audience {
			// Deep-copy Rules so callers can safely mutate.
			cp := p
			cp.Rules = make([]ValidatorRule, len(p.Rules))
			copy(cp.Rules, p.Rules)
			return cp, true
		}
	}
	return Profile{}, false
}

// mergeProfileConfig returns a ValidatorConfig that uses override values where
// non-zero and falls back to base otherwise. Named mergeProfileConfig (not
// mergeConfig) to avoid the name collision with run.go's unexported mergeConfig.
func mergeProfileConfig(base, override ValidatorConfig) ValidatorConfig {
	merged := base
	if override.CitationDensityWordsPerCitation > 0 {
		merged.CitationDensityWordsPerCitation = override.CitationDensityWordsPerCitation
	}
	if override.ReadingLevelFloor > 0 {
		merged.ReadingLevelFloor = override.ReadingLevelFloor
	}
	if override.ArchRelevanceMinPageRefs > 0 {
		merged.ArchRelevanceMinPageRefs = override.ArchRelevanceMinPageRefs
	}
	if override.ArchRelevanceMinGraphRelations > 0 {
		merged.ArchRelevanceMinGraphRelations = override.ArchRelevanceMinGraphRelations
	}
	if override.BlockCountMin > 0 {
		merged.BlockCountMin = override.BlockCountMin
	}
	if override.BlockCountMax > 0 {
		merged.BlockCountMax = override.BlockCountMax
	}
	// PageReferenceCount and GraphRelationCount come only from the caller
	// (graph-store injection); they are never overridden by tier config.
	return merged
}

// applyOverrides materializes a tier-effective profile from the frontier base
// by applying each override in overrides. Returns a new Profile with Tier unset
// (caller sets it). Validators not mentioned in overrides keep base behavior.
func applyOverrides(base Profile, overrides []tierRuleOverride) Profile {
	// Deep-copy Rules so mutations are safe.
	effective := base
	effective.Rules = make([]ValidatorRule, len(base.Rules))
	copy(effective.Rules, base.Rules)

	for _, ov := range overrides {
		idx := -1
		for i, r := range effective.Rules {
			if r.ValidatorID == ov.ValidatorID {
				idx = i
				break
			}
		}
		if idx < 0 {
			// Override references a validator that is not in the base profile.
			// Log a warning and skip — do NOT add a new rule (D14a).
			slog.Warn("quality/profile: tier override references absent validator",
				slog.String("validator", string(ov.ValidatorID)),
				slog.String("template", string(base.Template)),
				slog.String("audience", string(base.Audience)))
			continue
		}
		if ov.Level != nil {
			effective.Rules[idx].Level = *ov.Level
		}
		effective.Rules[idx].Config = mergeProfileConfig(effective.Rules[idx].Config, ov.Config)
	}
	return effective
}

// DefaultProfile returns the built-in profile for the given template, audience,
// and quality-gate tier. Returns (Profile{}, false) when no base profile is
// defined for the combination; callers should fall back to a sensible default
// or skip validation for unknown combinations.
//
// If tier is TierUnknown, a slog.Warn is emitted and TierFrontier is used
// as a fallback. Callers MUST normalize the tier before calling this function;
// TierUnknown is an explicit bug signal, not a silent default.
func DefaultProfile(template Template, audience Audience, tier modeltier.QualityGateTier) (Profile, bool) {
	if tier == modeltier.TierUnknown {
		slog.Warn("quality/profile: TierUnknown passed to DefaultProfile; falling back to frontier (callers MUST normalize)",
			slog.String("template", string(template)),
			slog.String("audience", string(audience)),
			slog.String("source", "DefaultProfile_TierUnknown_fallback"))
		tier = modeltier.TierFrontier
	}

	base, ok := lookupBaseProfile(template, audience)
	if !ok {
		return Profile{}, false
	}

	if tier == modeltier.TierFrontier {
		base.Tier = modeltier.TierFrontier
		return base, true
	}

	overrides := tierOverrides[profileKey{template, audience, tier}]
	effective := applyOverrides(base, overrides)
	effective.Tier = tier
	return effective, true
}

// AllDefaultProfiles returns the materialized cross-product: every
// (template, audience, tier) triple's effective profile in a deterministic
// order. The frontier profiles appear first, followed by mid and local.
func AllDefaultProfiles() []Profile {
	tiers := []modeltier.QualityGateTier{
		modeltier.TierFrontier,
		modeltier.TierMid,
		modeltier.TierLocal,
	}
	var out []Profile
	for _, base := range baseProfiles {
		for _, tier := range tiers {
			p, ok := DefaultProfile(base.Template, base.Audience, tier)
			if ok {
				out = append(out, p)
			}
		}
	}
	return out
}
