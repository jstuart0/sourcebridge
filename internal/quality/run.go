// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package quality

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
)

// RetryDecision is returned alongside a ValidationResult to tell the
// caller how to proceed. The generation loop in A1.P1 will consume this;
// for now it is produced here and the caller acts on it.
type RetryDecision string

const (
	// RetryPass means all gates passed. The page may ship (warnings
	// are attached to the PR description but do not block shipping).
	RetryPass RetryDecision = "pass"

	// RetryWithReasons means one or more gates fired on the first
	// attempt. The caller should retry the generation, passing the
	// gate violation messages back into the LLM prompt to guide the
	// next attempt.
	RetryWithReasons RetryDecision = "retry_with_reasons"

	// RetryReject means gates fired on a second (or later) attempt.
	// The page is excluded from the run; an entry is added to the
	// page-not-generated log and a PR comment explains what failed.
	RetryReject RetryDecision = "reject"
)

// ValidationResult is the complete output of a validation run.
// It is JSON-serializable for storage and PR-description rendering.
type ValidationResult struct {
	// Profile identifies the template+audience+tier combination that was applied.
	ProfileTemplate Template                  `json:"profile_template"`
	ProfileAudience Audience                  `json:"profile_audience"`
	ProfileTier     modeltier.QualityGateTier `json:"profile_tier"`

	// Gates contains rules that fired at gate level.
	Gates []RuleResult `json:"gates,omitempty"`
	// Warnings contains rules that fired at warning level.
	Warnings []RuleResult `json:"warnings,omitempty"`

	// GatesPassed is true when no gate-level violations were found.
	GatesPassed bool `json:"gates_passed"`

	// AttemptNumber is 1 for the first generation attempt, 2 for the
	// first retry, etc. The caller increments this.
	AttemptNumber int `json:"attempt_number"`

	// Decision is the retry policy outcome.
	Decision RetryDecision `json:"decision"`

	// RunAt is the wall-clock time the validation completed.
	RunAt time.Time `json:"run_at"`
}

// RuleResult is one validator's output within a ValidationResult.
type RuleResult struct {
	ValidatorID ValidatorID `json:"validator_id"`
	Level       GateLevel   `json:"level"`
	Violations  []Violation `json:"violations,omitempty"`
}

// Violation is JSON-serializable (re-declared at package top; this
// ensures the json tags are available).
// The struct definition lives in validator.go.

// Run executes the profile against input and returns a ValidationResult.
// attempt is 1 for the first call, 2 for the first retry, etc.
// The caller is responsible for providing cfg.PageReferenceCount and
// cfg.GraphRelationCount from the graph store before calling Run.
func Run(profile Profile, input ValidationInput, baseConfig ValidatorConfig, attempt int) ValidationResult {
	result := ValidationResult{
		ProfileTemplate: profile.Template,
		ProfileAudience: profile.Audience,
		ProfileTier:     profile.Tier,
		AttemptNumber:   attempt,
		RunAt:           time.Now().UTC(),
	}

	gatesPassed := true

	for _, rule := range profile.Rules {
		if rule.Level == LevelOff {
			continue
		}

		v, ok := ValidatorByID(rule.ValidatorID)
		if !ok {
			continue
		}

		// Merge base config with rule-specific overrides.
		// Rule config fields take precedence over base config fields when
		// non-zero; base config supplies graph-store fields (PageReferenceCount,
		// GraphRelationCount) that rule config cannot know.
		cfg := mergeConfig(baseConfig, rule.Config)
		violations := v.Validate(input, cfg)

		if len(violations) == 0 {
			continue
		}

		rr := RuleResult{
			ValidatorID: rule.ValidatorID,
			Level:       rule.Level,
			Violations:  violations,
		}
		switch rule.Level {
		case LevelGate:
			result.Gates = append(result.Gates, rr)
			gatesPassed = false
		case LevelWarning:
			result.Warnings = append(result.Warnings, rr)
		}
	}

	result.GatesPassed = gatesPassed
	result.Decision = decisionFor(gatesPassed, attempt)
	return result
}

// decisionFor maps (passed, attemptNumber) → RetryDecision per the plan:
//   - pass: RetryPass
//   - first attempt, gates fired: RetryWithReasons
//   - second+ attempt, gates fired: RetryReject
func decisionFor(gatesPassed bool, attempt int) RetryDecision {
	if gatesPassed {
		return RetryPass
	}
	if attempt <= 1 {
		return RetryWithReasons
	}
	return RetryReject
}

// RetryPromptFragment returns a string suitable for injection into the
// LLM retry prompt. It lists each gate violation with its message and
// suggestion so the model can address the specific issues.
// Returns an empty string when there are no gate violations.
func (r ValidationResult) RetryPromptFragment() string {
	if len(r.Gates) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("The previous generation attempt failed the following quality gates. ")
	sb.WriteString("Address each issue in your next attempt:\n\n")
	for _, gate := range r.Gates {
		sb.WriteString(fmt.Sprintf("**%s**\n", gate.ValidatorID))
		for _, v := range gate.Violations {
			sb.WriteString(fmt.Sprintf("- %s", v.Message))
			if v.Suggestion != "" {
				sb.WriteString(fmt.Sprintf(" — Suggestion: %s", v.Suggestion))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// QualityReportMarkdown renders the ValidationResult as a "Quality report"
// markdown section suitable for inclusion in a PR description.
func (r ValidationResult) QualityReportMarkdown() string {
	var sb strings.Builder

	sb.WriteString("## Quality report\n\n")
	sb.WriteString(fmt.Sprintf(
		"**Profile:** `%s` / `%s` / `%s` | **Attempt:** %d | **Gates passed:** %v\n\n",
		r.ProfileTemplate, r.ProfileAudience, r.ProfileTier, r.AttemptNumber, r.GatesPassed,
	))

	if r.GatesPassed && len(r.Warnings) == 0 {
		sb.WriteString("All quality checks passed. No warnings.\n")
		return sb.String()
	}

	if !r.GatesPassed {
		sb.WriteString("### Gate violations\n\n")
		sb.WriteString("The following issues **must** be resolved before this page ships:\n\n")
		for _, gate := range r.Gates {
			sb.WriteString(fmt.Sprintf("#### `%s`\n\n", gate.ValidatorID))
			for _, v := range gate.Violations {
				if v.Line > 0 {
					sb.WriteString(fmt.Sprintf("- **Line %d:** %s\n", v.Line, v.Message))
				} else {
					sb.WriteString(fmt.Sprintf("- %s\n", v.Message))
				}
				if v.Suggestion != "" {
					sb.WriteString(fmt.Sprintf("  - _Suggestion:_ %s\n", v.Suggestion))
				}
				if v.Excerpt != "" {
					sb.WriteString(fmt.Sprintf("  - _Excerpt:_ `%s`\n", v.Excerpt))
				}
			}
			sb.WriteString("\n")
		}
	}

	if len(r.Warnings) > 0 {
		sb.WriteString("### Warnings\n\n")
		sb.WriteString("The following issues were noted but do not block shipping:\n\n")
		for _, warn := range r.Warnings {
			sb.WriteString(fmt.Sprintf("#### `%s`\n\n", warn.ValidatorID))
			for _, v := range warn.Violations {
				if v.Line > 0 {
					sb.WriteString(fmt.Sprintf("- **Line %d:** %s\n", v.Line, v.Message))
				} else {
					sb.WriteString(fmt.Sprintf("- %s\n", v.Message))
				}
				if v.Suggestion != "" {
					sb.WriteString(fmt.Sprintf("  - _Suggestion:_ %s\n", v.Suggestion))
				}
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// JSON serializes the result to compact JSON for storage.
func (r ValidationResult) JSON() ([]byte, error) {
	return json.Marshal(r)
}

// mergeConfig returns a ValidatorConfig that uses rule values where
// non-zero and falls back to base otherwise.
func mergeConfig(base, rule ValidatorConfig) ValidatorConfig {
	merged := base // start with base (carries graph-store fields)

	// Override with rule-specific non-zero values.
	if rule.CitationDensityWordsPerCitation > 0 {
		merged.CitationDensityWordsPerCitation = rule.CitationDensityWordsPerCitation
	}
	if rule.ReadingLevelFloor > 0 {
		merged.ReadingLevelFloor = rule.ReadingLevelFloor
	}
	if rule.ArchRelevanceMinPageRefs > 0 {
		merged.ArchRelevanceMinPageRefs = rule.ArchRelevanceMinPageRefs
	}
	if rule.ArchRelevanceMinGraphRelations > 0 {
		merged.ArchRelevanceMinGraphRelations = rule.ArchRelevanceMinGraphRelations
	}
	if rule.BlockCountMin > 0 {
		merged.BlockCountMin = rule.BlockCountMin
	}
	if rule.BlockCountMax > 0 {
		merged.BlockCountMax = rule.BlockCountMax
	}
	// PageReferenceCount and GraphRelationCount come only from base
	// (supplied by the caller from the graph store).
	return merged
}
