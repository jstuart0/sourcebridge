// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// TestTierFixtures_ThresholdsBehaveAsDocumented verifies that for every
// changed row in the tierOverrides table, the fixture text produces the
// expected RetryDecision at each tier.
//
// Design rules for fixtures (per plan D11 / codex r1b Medium #5):
//   - Each fixture exercises exactly ONE changed threshold across tiers.
//   - Paragraphs that make behavioral assertions carry a citation so
//     factual_grounding does not trip on other validators' fixtures.
//   - Vagueness fixtures contain enough citations to satisfy
//     citation_density at all tiers so only vagueness changes.
//   - Reading-level fixtures maintain safe citation density and no vague
//     terms so the fixture only exercises reading_level.
//
// The fixture markdown files live in testdata/tier_fixtures/.
package quality_test

import (
	"math"
	"os"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// loadFixture reads a file from testdata/tier_fixtures/ and returns a
// NewMarkdownInput for it. Fails the test if the file cannot be read.
func loadFixture(t *testing.T, name string) quality.ValidationInput {
	t.Helper()
	data, err := os.ReadFile("testdata/tier_fixtures/" + name)
	if err != nil {
		t.Fatalf("loadFixture(%q): %v", name, err)
	}
	return quality.NewMarkdownInput(string(data))
}

// runFixture calls quality.Run for the given profile and input at attempt 2
// (the attempt at which gate failures produce RetryReject rather than
// RetryWithReasons). Returns the result.
func runFixture(t *testing.T, tmpl quality.Template, aud quality.Audience, tier modeltier.QualityGateTier, input quality.ValidationInput, baseCfg quality.ValidatorConfig) quality.ValidationResult {
	t.Helper()
	profile, ok := quality.DefaultProfile(tmpl, aud, tier)
	if !ok {
		t.Fatalf("DefaultProfile(%s, %s, %s) returned false", tmpl, aud, tier)
	}
	return quality.Run(profile, input, baseCfg, 2 /* attempt=2 produces RetryReject on gate failure */)
}

// assertDecision asserts result.Decision equals want and prints a descriptive
// failure message including tier and the gate/warning lists.
func assertDecision(t *testing.T, tier modeltier.QualityGateTier, result quality.ValidationResult, want quality.RetryDecision) {
	t.Helper()
	if result.Decision != want {
		t.Errorf("tier=%s: Decision = %q, want %q (gates=%v warnings=%v)",
			tier, result.Decision, want, ruleIDs(result.Gates), ruleIDs(result.Warnings))
	}
}

// assertGatesPass asserts that no gate-level violations fired.
func assertGatesPass(t *testing.T, tier modeltier.QualityGateTier, result quality.ValidationResult) {
	t.Helper()
	if !result.GatesPassed {
		t.Errorf("tier=%s: expected gates to pass, got gate violations: %v",
			tier, ruleIDs(result.Gates))
	}
}

// assertGatesFire asserts that at least one gate-level violation fired.
func assertGatesFire(t *testing.T, tier modeltier.QualityGateTier, result quality.ValidationResult) {
	t.Helper()
	if result.GatesPassed {
		t.Errorf("tier=%s: expected at least one gate violation, but gates all passed", tier)
	}
}

// assertWarningPresent asserts that a specific validator fired as a warning.
func assertWarningPresent(t *testing.T, tier modeltier.QualityGateTier, result quality.ValidationResult, id quality.ValidatorID) {
	t.Helper()
	for _, w := range result.Warnings {
		if w.ValidatorID == id {
			return
		}
	}
	t.Errorf("tier=%s: expected warning from %s; warnings=%v gates=%v",
		tier, id, ruleIDs(result.Warnings), ruleIDs(result.Gates))
}

// assertNoGateFor asserts that the specified validator did NOT fire at gate level.
func assertNoGateFor(t *testing.T, tier modeltier.QualityGateTier, result quality.ValidationResult, id quality.ValidatorID) {
	t.Helper()
	for _, g := range result.Gates {
		if g.ValidatorID == id {
			t.Errorf("tier=%s: expected %s NOT to fire as gate, but it did", tier, id)
		}
	}
}

// ruleIDs extracts ValidatorID strings from a slice of RuleResults for
// diagnostic output.
func ruleIDs(rrs []quality.RuleResult) []string {
	out := make([]string, 0, len(rrs))
	for _, r := range rrs {
		out = append(out, string(r.ValidatorID))
	}
	return out
}

// TestTierFixtures_ThresholdsBehaveAsDocumented is the merge-blocking gate
// for Phase 5 (CA-150). It covers every changed row in the tierOverrides
// table and proves that the frontier/mid/local threshold deltas behave as
// documented for each (template, audience) combination.
func TestTierFixtures_ThresholdsBehaveAsDocumented(t *testing.T) {
	t.Parallel()

	// citation_density / architecture/engineers
	//
	// Frontier (gate, 200): requires ceil(words/200) citations.
	// Mid      (gate, 300): requires ceil(words/300) citations.
	// Local    (warn, 400): citation density is a warning, not a gate.
	//
	// Fixture: exactly 1 parseable citation; ~230 prose words.
	//   ceil(230/200)=2 > 1 => frontier gates fire.
	//   ceil(230/300)=1 = 1 => mid gates pass.
	//   local: warning level, citation density passes => gates pass.
	t.Run("citation_density/architecture/engineers", func(t *testing.T) {
		t.Parallel()
		input := loadFixture(t, "arch_eng_citation_density.md")

		wc := input.WordCount()
		citCount := countValidCitations(input.Citations())
		if citCount != 1 {
			t.Fatalf("fixture must have exactly 1 parseable citation; got %d", citCount)
		}
		// Guard: frontier needs ceil(wc/200)>1 to fire. wc must be >200.
		if wc <= 200 {
			t.Fatalf("fixture word count %d must be >200 so frontier needs 2 citations", wc)
		}
		// Guard: mid needs ceil(wc/300)=1. wc must be <=300.
		if wc > 300 {
			t.Fatalf("fixture word count %d must be <=300 so mid only needs 1 citation", wc)
		}

		base := quality.ValidatorConfig{}

		// Frontier: citation_density is a gate. ceil(wc/200)>=2, have 1 => fires.
		frontierResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
		assertGatesFire(t, modeltier.TierFrontier, frontierResult)
		assertDecision(t, modeltier.TierFrontier, frontierResult, quality.RetryReject)

		// Mid: citation_density gate at threshold=300. ceil(wc/300)=1 => passes.
		midResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierMid, input, base)
		assertNoGateFor(t, modeltier.TierMid, midResult, quality.ValidatorCitationDensity)
		assertGatesPass(t, modeltier.TierMid, midResult)
		assertDecision(t, modeltier.TierMid, midResult, quality.RetryPass)

		// Local: citation_density is a warning at threshold=400. 1 citation
		// satisfies ceil(wc/400)=1 => no violation at all.
		localResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal, input, base)
		assertNoGateFor(t, modeltier.TierLocal, localResult, quality.ValidatorCitationDensity)
		assertGatesPass(t, modeltier.TierLocal, localResult)
		assertDecision(t, modeltier.TierLocal, localResult, quality.RetryPass)
	})

	// citation_density / api_reference/engineers
	//
	// Frontier (gate, 100): requires ceil(words/100) citations.
	// Mid      (gate, 200): requires ceil(words/200) citations.
	// Local    (gate, 300): requires ceil(words/300) citations.
	//
	// Fixture: exactly 1 parseable citation; ~230 prose words.
	//   ceil(230/100)=3 > 1 => frontier fires.
	//   ceil(230/200)=2 > 1 => mid fires.
	//   ceil(230/300)=1 = 1 => local passes.
	t.Run("citation_density/api_reference/engineers", func(t *testing.T) {
		t.Parallel()
		input := loadFixture(t, "api_eng_citation_density.md")

		wc := input.WordCount()
		citCount := countValidCitations(input.Citations())
		if citCount != 1 {
			t.Fatalf("fixture must have exactly 1 parseable citation; got %d", citCount)
		}
		// Guard: mid needs ceil(wc/200)>1 => wc >200.
		if wc <= 200 {
			t.Fatalf("fixture word count %d must be >200 so mid needs 2 citations", wc)
		}
		// Guard: local needs ceil(wc/300)=1 => wc <=300.
		if wc > 300 {
			t.Fatalf("fixture word count %d must be <=300 so local only needs 1 citation", wc)
		}

		base := quality.ValidatorConfig{}

		// Frontier: gate at threshold=100. ceil(wc/100)>=3 > 1 => fires.
		frontierResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
		assertGatesFire(t, modeltier.TierFrontier, frontierResult)
		assertDecision(t, modeltier.TierFrontier, frontierResult, quality.RetryReject)

		// Mid: gate at threshold=200. ceil(wc/200)>=2 > 1 => fires.
		midResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierMid, input, base)
		assertGatesFire(t, modeltier.TierMid, midResult)
		assertDecision(t, modeltier.TierMid, midResult, quality.RetryReject)

		// Local: gate at threshold=300. ceil(wc/300)=1 = 1 => passes.
		localResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierLocal, input, base)
		assertNoGateFor(t, modeltier.TierLocal, localResult, quality.ValidatorCitationDensity)
		assertDecision(t, modeltier.TierLocal, localResult, quality.RetryPass)
	})

	// vagueness / architecture/engineers
	//
	// Frontier (gate):   vague terms without adjacent numeral => gates fire.
	// Mid      (gate):   same — mid inherits frontier vagueness gate.
	// Local    (warning): vagueness is demoted to warning => gates pass.
	//
	// Fixture: >=3 vague quantifiers without nearby digits; >=4 parseable
	// citations so citation_density passes at all tiers.
	t.Run("vagueness/architecture/engineers", func(t *testing.T) {
		t.Parallel()
		input := loadFixture(t, "arch_eng_vagueness.md")

		citCount := countValidCitations(input.Citations())
		wc := input.WordCount()
		// citation_density at frontier (threshold=200): need ceil(wc/200) cits.
		minCits := ceilDiv(wc, 200)
		if citCount < minCits {
			t.Fatalf("vagueness fixture: need >=%d citations to pass citation_density "+
				"at frontier (threshold=200), got %d (wc=%d)", minCits, citCount, wc)
		}

		base := quality.ValidatorConfig{}

		// Frontier: vagueness is a gate.
		frontierResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
		assertGatesFire(t, modeltier.TierFrontier, frontierResult)
		foundVaguenessGate := false
		for _, g := range frontierResult.Gates {
			if g.ValidatorID == quality.ValidatorVagueness {
				foundVaguenessGate = true
			}
		}
		if !foundVaguenessGate {
			t.Errorf("tier=frontier: expected vagueness gate violation; gates=%v",
				ruleIDs(frontierResult.Gates))
		}
		assertDecision(t, modeltier.TierFrontier, frontierResult, quality.RetryReject)

		// Mid: vagueness is also a gate.
		midResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierMid, input, base)
		assertGatesFire(t, modeltier.TierMid, midResult)
		assertDecision(t, modeltier.TierMid, midResult, quality.RetryReject)

		// Local: vagueness is a warning. Gates must pass.
		localResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal, input, base)
		assertNoGateFor(t, modeltier.TierLocal, localResult, quality.ValidatorVagueness)
		assertGatesPass(t, modeltier.TierLocal, localResult)
		assertWarningPresent(t, modeltier.TierLocal, localResult, quality.ValidatorVagueness)
		assertDecision(t, modeltier.TierLocal, localResult, quality.RetryPass)
	})

	// reading_level / architecture/engineers
	//
	// reading_level is always LevelWarning (never a gate) at every tier.
	// Thresholds: Frontier floor=50, Mid floor=45, Local floor=40.
	//
	// The fixture uses dense technical prose that scores below all three
	// floors. Since reading_level is always a warning (not a gate), all
	// tiers produce RetryPass. The key invariant: a low reading-ease score
	// never causes a gate rejection at any tier, only a warning.
	//
	// The differentiated threshold values (50/45/40) are locked mechanically
	// by TestDefaultProfile_ThresholdTable in profile_test.go. This test
	// proves the end-to-end Run() path honours the warning-only treatment.
	//
	// Fixture invariant: >=3 parseable citations; no vague quantifiers.
	t.Run("reading_level/architecture/engineers", func(t *testing.T) {
		t.Parallel()
		input := loadFixture(t, "arch_eng_reading_level.md")

		citCount := countValidCitations(input.Citations())
		if citCount < 3 {
			t.Fatalf("reading_level/arch/eng fixture: need >=3 citations to pass "+
				"citation_density at frontier (threshold=200); got %d", citCount)
		}

		base := quality.ValidatorConfig{}

		// Frontier: reading_level is a warning at floor=50. Dense prose scores
		// below 50 so the warning fires; gates still pass => RetryPass.
		frontierResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
		assertDecision(t, modeltier.TierFrontier, frontierResult, quality.RetryPass)
		assertWarningPresent(t, modeltier.TierFrontier, frontierResult, quality.ValidatorReadingLevel)

		// Mid: reading_level is a warning at floor=45. Same outcome.
		midResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierMid, input, base)
		assertDecision(t, modeltier.TierMid, midResult, quality.RetryPass)
		assertWarningPresent(t, modeltier.TierMid, midResult, quality.ValidatorReadingLevel)

		// Local: reading_level is a warning at floor=40 (relaxed). Dense prose
		// may still cross this lower floor. Either way the decision is RetryPass;
		// reading_level never gates at any tier.
		localResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal, input, base)
		assertDecision(t, modeltier.TierLocal, localResult, quality.RetryPass)
	})

	// reading_level / api_reference/engineers
	//
	// Same tier thresholds as architecture: floor 50/45/40.
	// api_reference also gates code_example_present at frontier (no code
	// blocks in the dense-prose fixture => that gate fires). We verify
	// reading_level appears as a warning alongside the gate violations.
	t.Run("reading_level/api_reference/engineers", func(t *testing.T) {
		t.Parallel()
		input := loadFixture(t, "arch_eng_reading_level.md")

		citCount := countValidCitations(input.Citations())
		if citCount < 3 {
			t.Fatalf("reading_level/api/eng fixture: need >=3 citations; got %d", citCount)
		}

		base := quality.ValidatorConfig{}

		// Frontier: code_example_present gates (no code blocks in fixture).
		// reading_level still appears as a warning alongside the gate.
		frontierResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
		assertWarningPresent(t, modeltier.TierFrontier, frontierResult, quality.ValidatorReadingLevel)

		// Mid: code_example_present is a warning at mid/local; reading_level is
		// also a warning. Gates pass if citation_density passes.
		midResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierMid, input, base)
		assertWarningPresent(t, modeltier.TierMid, midResult, quality.ValidatorReadingLevel)

		// Local: both code_example_present and reading_level are warnings.
		// Gates pass => RetryPass.
		localResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierLocal, input, base)
		assertDecision(t, modeltier.TierLocal, localResult, quality.RetryPass)
	})

	// code_example_present / api_reference/engineers
	//
	// Frontier (gate):   no code block => gates fire => RetryReject.
	// Mid      (warning): no code block => warning only, gates pass.
	// Local    (warning): same => gates pass.
	//
	// Fixture invariant: no fenced code blocks; sufficient citations for
	// citation_density at local tier (threshold=300); no vague quantifiers.
	t.Run("code_example_present/api_reference/engineers", func(t *testing.T) {
		t.Parallel()
		input := loadFixture(t, "api_eng_code_example.md")

		if len(input.CodeBlocks()) > 0 {
			t.Fatalf("code_example_present fixture must contain no code blocks, found %d",
				len(input.CodeBlocks()))
		}

		wc := input.WordCount()
		citCount := countValidCitations(input.Citations())
		requiredAtLocal := ceilDiv(wc, 300)
		if citCount < requiredAtLocal {
			t.Fatalf("code_example_present/api/eng fixture: need %d citations for %d words "+
				"at local threshold=300, got %d", requiredAtLocal, wc, citCount)
		}

		base := quality.ValidatorConfig{}

		// Frontier: code_example_present is a gate.
		frontierResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
		assertGatesFire(t, modeltier.TierFrontier, frontierResult)
		foundCodeGate := false
		for _, g := range frontierResult.Gates {
			if g.ValidatorID == quality.ValidatorCodeExamplePresent {
				foundCodeGate = true
			}
		}
		if !foundCodeGate {
			t.Errorf("tier=frontier: expected code_example_present gate violation")
		}
		assertDecision(t, modeltier.TierFrontier, frontierResult, quality.RetryReject)

		// Mid: code_example_present is a warning.
		midResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierMid, input, base)
		assertNoGateFor(t, modeltier.TierMid, midResult, quality.ValidatorCodeExamplePresent)
		assertWarningPresent(t, modeltier.TierMid, midResult, quality.ValidatorCodeExamplePresent)
		assertDecision(t, modeltier.TierMid, midResult, quality.RetryPass)

		// Local: code_example_present is a warning.
		localResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierLocal, input, base)
		assertNoGateFor(t, modeltier.TierLocal, localResult, quality.ValidatorCodeExamplePresent)
		assertWarningPresent(t, modeltier.TierLocal, localResult, quality.ValidatorCodeExamplePresent)
		assertDecision(t, modeltier.TierLocal, localResult, quality.RetryPass)
	})

	// block_count / architecture/engineers
	//
	// Frontier (warning, min=2): 1 top-level block => warning fires.
	// Mid      (warning, min=2): same => warning fires.
	// Local    (warning, min=1): 1 block >= 1 => no warning.
	//
	// block_count is always a warning (never a gate), so Decision=RetryPass
	// at all tiers. The fixture has exactly 1 top-level block (H1 title only,
	// no H2+ headings, no code blocks). Citation count must satisfy frontier
	// citation_density (threshold=200).
	t.Run("block_count/architecture/engineers", func(t *testing.T) {
		t.Parallel()
		input := loadFixture(t, "arch_eng_block_count.md")

		blocks := input.TopLevelBlocks()
		if blocks != 1 {
			t.Fatalf("block_count fixture: expected 1 top-level block, got %d "+
				"(fixture must have only an H1 and no H2+ headings or code blocks)", blocks)
		}

		wc := input.WordCount()
		citCount := countValidCitations(input.Citations())
		requiredAtFrontier := ceilDiv(wc, 200)
		if citCount < requiredAtFrontier {
			t.Fatalf("block_count/arch/eng fixture: need %d citations for %d words "+
				"at frontier threshold=200, got %d", requiredAtFrontier, wc, citCount)
		}

		base := quality.ValidatorConfig{}

		// Frontier: block_count warning fires (1 block < min=2).
		// reading_level is never a gate, Decision=RetryPass.
		frontierResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
		assertDecision(t, modeltier.TierFrontier, frontierResult, quality.RetryPass)
		assertWarningPresent(t, modeltier.TierFrontier, frontierResult, quality.ValidatorBlockCount)

		// Mid: block_count warning at min=2. Same outcome.
		midResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierMid, input, base)
		assertDecision(t, modeltier.TierMid, midResult, quality.RetryPass)
		assertWarningPresent(t, modeltier.TierMid, midResult, quality.ValidatorBlockCount)

		// Local: block_count warning at min=1. 1 block >= 1 => no warning.
		localResult := runFixture(t, quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierLocal, input, base)
		assertDecision(t, modeltier.TierLocal, localResult, quality.RetryPass)
		for _, w := range localResult.Warnings {
			if w.ValidatorID == quality.ValidatorBlockCount {
				t.Errorf("tier=local: block_count warning fired with 1 block " +
					"and min=1; expected no warning")
			}
		}
	})

	// architectural_relevance / system_overview/engineers
	//
	// Frontier (gate, refs>=2 rels>=5): 1 ref + 3 rels => fires.
	// Mid      (gate, refs>=2 rels>=4): 1 ref + 3 rels => fires (refs<2).
	// Local    (gate, refs>=1 rels>=3): 1 ref + 3 rels => passes.
	//
	// Note: architectural_relevance reads PageReferenceCount and
	// GraphRelationCount from ValidatorConfig (graph-store injection),
	// not from the page text. The fixture text is minimal; the injected
	// config values control the outcome.
	t.Run("architectural_relevance/system_overview/engineers", func(t *testing.T) {
		t.Parallel()
		input := loadFixture(t, "sysoverview_eng_arch_relevance.md")

		// Inject graph-store values: 1 page reference, 3 graph relations.
		// Satisfies local (refs>=1, rels>=3) but not mid (refs>=2) or frontier.
		base := quality.ValidatorConfig{
			PageReferenceCount: 1,
			GraphRelationCount: 3,
		}

		// Frontier: requires refs>=2 AND rels>=5. refs=1 < 2 => fires.
		frontierResult := runFixture(t, quality.TemplateSystemOverview, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
		assertGatesFire(t, modeltier.TierFrontier, frontierResult)
		assertDecision(t, modeltier.TierFrontier, frontierResult, quality.RetryReject)

		// Mid: requires refs>=2 AND rels>=4. refs=1 < 2 => fires.
		midResult := runFixture(t, quality.TemplateSystemOverview, quality.AudienceEngineers, modeltier.TierMid, input, base)
		assertGatesFire(t, modeltier.TierMid, midResult)
		assertDecision(t, modeltier.TierMid, midResult, quality.RetryReject)

		// Local: requires refs>=1 AND rels>=3. refs=1 >= 1 => condition met.
		localResult := runFixture(t, quality.TemplateSystemOverview, quality.AudienceEngineers, modeltier.TierLocal, input, base)
		assertNoGateFor(t, modeltier.TierLocal, localResult, quality.ValidatorArchitecturalRelevance)
		assertDecision(t, modeltier.TierLocal, localResult, quality.RetryPass)
	})
}

// TestTierFixtures_FactualGrounding_QwenShapedOutput is the CA-152 lock-test.
// It proves that the production failure mode (qwen3.6-shaped output with zero
// citations and behavioral assertion verbs) is REJECTED at TierFrontier and
// PASSES with a warning at TierLocal after the followup-B override is applied.
//
// The fixture (api_reference_qwen3_no_citations.md) is representative of
// qwen3.6:27b-q4_K_M output: plain prose, behavioral verbs (accepts, returns,
// validates, stores, etc.), no parseable (path:N-N) citations.
func TestTierFixtures_FactualGrounding_QwenShapedOutput(t *testing.T) {
	t.Parallel()
	input := loadFixture(t, "api_reference_qwen3_no_citations.md")

	// Guard: fixture must contain behavioral assertion verbs so the validator
	// has something to flag. We verify by checking that frontier rejects it.
	base := quality.ValidatorConfig{}

	// TierFrontier: factual_grounding is a gate. Paragraphs with assertion
	// verbs and no citations => gate fires => RetryReject.
	frontierResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierFrontier, input, base)
	assertGatesFire(t, modeltier.TierFrontier, frontierResult)
	foundFGGate := false
	for _, g := range frontierResult.Gates {
		if g.ValidatorID == quality.ValidatorFactualGrounding {
			foundFGGate = true
		}
	}
	if !foundFGGate {
		t.Errorf("tier=frontier: expected factual_grounding gate violation on qwen3-shaped fixture; gates=%v",
			ruleIDs(frontierResult.Gates))
	}
	assertDecision(t, modeltier.TierFrontier, frontierResult, quality.RetryReject)

	// TierLocal: factual_grounding is demoted to warning (CA-152). The key
	// assertion is that factual_grounding does NOT fire as a gate — it appears
	// as a warning. citation_density may still gate if the fixture has no
	// citations, but factual_grounding specifically must not be the blocker.
	localResult := runFixture(t, quality.TemplateAPIReference, quality.AudienceEngineers, modeltier.TierLocal, input, base)
	assertNoGateFor(t, modeltier.TierLocal, localResult, quality.ValidatorFactualGrounding)
	assertWarningPresent(t, modeltier.TierLocal, localResult, quality.ValidatorFactualGrounding)
}

// countValidCitations counts how many strings in the raw citation slice
// are valid parseable citations (file_range or symbol form). The input
// package pre-filters via reCitation, so every entry is a candidate; we
// check the path:N or path:N-M structure here.
func countValidCitations(raw []string) int {
	count := 0
	for _, s := range raw {
		if isValidCitationString(s) {
			count++
		}
	}
	return count
}

// isValidCitationString returns true for file-range ("path:N-M") and
// symbol ("sym_*") citation forms.
func isValidCitationString(s string) bool {
	if len(s) == 0 {
		return false
	}
	if len(s) > 4 && s[:4] == "sym_" {
		return true
	}
	for i, ch := range s {
		if ch == ':' && i > 0 {
			rest := s[i+1:]
			return len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9'
		}
	}
	return false
}

// ceilDiv returns ceil(a/b) for positive integers.
func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return int(math.Ceil(float64(a) / float64(b)))
}
