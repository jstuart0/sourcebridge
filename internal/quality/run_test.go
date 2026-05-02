// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package quality_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// TestRun_Pass verifies the full pipeline returns RetryPass when all
// gates pass.
func TestRun_Pass(t *testing.T) {
	// Architecture/engineer page that satisfies all gates:
	// - has citation (density ok)
	// - no vague quantifiers
	// - behavioral assertion has a citation (factual grounding)
	src := `## Authentication middleware

The middleware validates JWTs on every inbound request. (internal/auth/middleware.go:1-40)
It returns 401 for missing or invalid tokens. (internal/auth/errors.go:5-15)
Token expiry is enforced by comparing the exp claim to server clock.
(internal/auth/middleware.go:88-102)
The RS256 public key set is fetched on startup from the JWKS endpoint.
(internal/auth/jwks.go:10-55)

` + "```go\nmiddleware := auth.New(auth.Config{JWKSEndpoint: cfg.JWKS})\n```\n"

	input := quality.NewMarkdownInput(src)
	profile, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier)
	if !ok {
		t.Fatal("profile not found")
	}

	result := quality.Run(profile, input, quality.ValidatorConfig{
		PageReferenceCount: 5,
		GraphRelationCount: 10,
	}, 1)

	if result.Decision != quality.RetryPass {
		t.Errorf("Run: expected RetryPass, got %s; gates=%+v", result.Decision, result.Gates)
	}
	if !result.GatesPassed {
		t.Errorf("Run: expected GatesPassed=true, got false; gates=%+v", result.Gates)
	}
}

// TestRun_RetryWithReasons verifies attempt==1, gates fire → RetryWithReasons.
func TestRun_RetryWithReasons(t *testing.T) {
	// Page with no citations and vague language.
	src := `## Overview

Various configuration options are available. The system handles various
error states gracefully in some cases. No code example. No citations.
`
	input := quality.NewMarkdownInput(src)
	profile, _ := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier)
	result := quality.Run(profile, input, quality.ValidatorConfig{}, 1)

	if result.Decision != quality.RetryWithReasons {
		t.Errorf("Run: expected RetryWithReasons on attempt 1 with gate failures, got %s", result.Decision)
	}
	if len(result.Gates) == 0 {
		t.Error("Run: expected gate violations, got 0")
	}
}

// TestRun_Reject verifies attempt==2, gates fire → Reject.
func TestRun_Reject(t *testing.T) {
	src := `## Overview

Various options exist. No citations. No code.
`
	input := quality.NewMarkdownInput(src)
	profile, _ := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, modeltier.TierFrontier)
	result := quality.Run(profile, input, quality.ValidatorConfig{}, 2)

	if result.Decision != quality.RetryReject {
		t.Errorf("Run: expected RetryReject on attempt 2 with gate failures, got %s", result.Decision)
	}
}

// TestRetryPromptFragment_Empty verifies empty string when gates passed.
func TestRetryPromptFragment_Empty(t *testing.T) {
	result := quality.ValidationResult{GatesPassed: true}
	frag := result.RetryPromptFragment()
	if frag != "" {
		t.Errorf("RetryPromptFragment: expected empty string when gates passed, got %q", frag)
	}
}

// TestRetryPromptFragment_Content verifies that gate messages appear in the fragment.
func TestRetryPromptFragment_Content(t *testing.T) {
	result := quality.ValidationResult{
		GatesPassed: false,
		Gates: []quality.RuleResult{
			{
				ValidatorID: quality.ValidatorVagueness,
				Level:       quality.LevelGate,
				Violations: []quality.Violation{
					{Message: "vague quantifier \"various\" without adjacent numeral"},
				},
			},
		},
	}
	frag := result.RetryPromptFragment()
	if !strings.Contains(frag, "vagueness") {
		t.Errorf("RetryPromptFragment: expected 'vagueness' in output, got: %q", frag)
	}
	if !strings.Contains(frag, "various") {
		t.Errorf("RetryPromptFragment: expected violation message in output, got: %q", frag)
	}
}

// TestQualityReportMarkdown_AllPass verifies clean output when all checks pass
// and that the tier appears in the profile header line.
func TestQualityReportMarkdown_AllPass(t *testing.T) {
	result := quality.ValidationResult{
		ProfileTemplate: quality.TemplateArchitecture,
		ProfileAudience: quality.AudienceEngineers,
		ProfileTier:     modeltier.TierFrontier,
		AttemptNumber:   1,
		GatesPassed:     true,
	}
	md := result.QualityReportMarkdown()
	if !strings.HasPrefix(md, "## Quality report") {
		t.Errorf("QualityReportMarkdown: expected header, got: %q", md[:min(len(md), 50)])
	}
	if !strings.Contains(md, "All quality checks passed") {
		t.Errorf("QualityReportMarkdown: expected pass message, got: %q", md)
	}
	if !strings.Contains(md, "frontier") {
		t.Errorf("QualityReportMarkdown: expected tier 'frontier' in header, got: %q", md)
	}
}

// TestValidationResult_ProfileTier verifies that Run() populates
// ValidationResult.ProfileTier from the profile's Tier field.
func TestValidationResult_ProfileTier(t *testing.T) {
	src := `## Overview

The service handles requests. (internal/service/handler.go:1-10)
`
	input := quality.NewMarkdownInput(src)
	for _, tier := range []modeltier.QualityGateTier{modeltier.TierFrontier, modeltier.TierMid, modeltier.TierLocal} {
		profile, ok := quality.DefaultProfile(quality.TemplateArchitecture, quality.AudienceEngineers, tier)
		if !ok {
			t.Fatalf("DefaultProfile returned false for tier %s", tier)
		}
		result := quality.Run(profile, input, quality.ValidatorConfig{
			PageReferenceCount: 3,
			GraphRelationCount: 5,
		}, 1)
		if result.ProfileTier != tier {
			t.Errorf("Run() ProfileTier: got %q, want %q", result.ProfileTier, tier)
		}
	}
}

// TestQualityReportMarkdown_WithGatesAndWarnings validates the structure
// of a result that has both gate failures and warnings, and that the tier
// line appears in the output (D17).
func TestQualityReportMarkdown_WithGatesAndWarnings(t *testing.T) {
	result := quality.ValidationResult{
		ProfileTemplate: quality.TemplateArchitecture,
		ProfileAudience: quality.AudienceEngineers,
		ProfileTier:     modeltier.TierLocal,
		AttemptNumber:   1,
		GatesPassed:     false,
		Gates: []quality.RuleResult{
			{
				ValidatorID: quality.ValidatorVagueness,
				Level:       quality.LevelGate,
				Violations: []quality.Violation{
					{Line: 12, Message: "vague quantifier \"various\"", Suggestion: "use a number"},
				},
			},
		},
		Warnings: []quality.RuleResult{
			{
				ValidatorID: quality.ValidatorReadingLevel,
				Level:       quality.LevelWarning,
				Violations: []quality.Violation{
					{Message: "Flesch score 45.0 (floor 50)"},
				},
			},
		},
	}

	md := result.QualityReportMarkdown()

	// Must contain both sections.
	if !strings.Contains(md, "Gate violations") {
		t.Errorf("QualityReportMarkdown: expected 'Gate violations' section")
	}
	if !strings.Contains(md, "Warnings") {
		t.Errorf("QualityReportMarkdown: expected 'Warnings' section")
	}
	// Gate violation should include line number.
	if !strings.Contains(md, "Line 12") {
		t.Errorf("QualityReportMarkdown: expected line number in gate violation")
	}
	// Suggestion should appear.
	if !strings.Contains(md, "use a number") {
		t.Errorf("QualityReportMarkdown: expected suggestion in output")
	}
	// Tier annotation should appear (D17).
	if !strings.Contains(md, "local") {
		t.Errorf("QualityReportMarkdown: expected tier 'local' in header, got: %q", md)
	}
}

// TestValidationResult_JSON verifies round-trip JSON serialization.
func TestValidationResult_JSON(t *testing.T) {
	result := quality.ValidationResult{
		ProfileTemplate: quality.TemplateADR,
		ProfileAudience: quality.AudienceEngineers,
		AttemptNumber:   1,
		GatesPassed:     true,
		Decision:        quality.RetryPass,
	}

	data, err := result.JSON()
	if err != nil {
		t.Fatalf("JSON() returned error: %v", err)
	}

	var decoded quality.ValidationResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if decoded.ProfileTemplate != result.ProfileTemplate {
		t.Errorf("JSON round-trip: template mismatch: got %q, want %q", decoded.ProfileTemplate, result.ProfileTemplate)
	}
	if decoded.Decision != result.Decision {
		t.Errorf("JSON round-trip: decision mismatch: got %q, want %q", decoded.Decision, result.Decision)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
