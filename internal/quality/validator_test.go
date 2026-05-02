// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package quality_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// --- vagueness ---

func TestVagueness_Clean(t *testing.T) {
	src := `## How requests are routed

The load balancer distributes traffic across 3 backend instances using
round-robin. Each instance handles up to 1,000 concurrent connections.
(internal/lb/router.go:42-60)
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorVagueness)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("Vagueness: expected 0 violations, got %d: %+v", len(got), got)
	}
}

func TestVagueness_Fires(t *testing.T) {
	src := `## Behavior

Various configuration options are available. In some cases the system
may behave differently depending on several factors.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorVagueness)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) == 0 {
		t.Error("Vagueness: expected violations, got 0")
	}
}

func TestVagueness_DigitNearby(t *testing.T) {
	// "several" followed by a digit within the window should not fire.
	src := `## Instances

The pool maintains several 3-replica groups.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorVagueness)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("Vagueness: expected 0 violations when digit nearby, got %d: %+v", len(got), got)
	}
}

func TestVagueness_InCodeBlock(t *testing.T) {
	// "various" inside a code block must not fire.
	src := "## Example\n\n```go\n// various options\nvar opts []Option\n```\n"
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorVagueness)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("Vagueness: expected 0 violations inside code block, got %d", len(got))
	}
}

// --- empty_headline ---

func TestEmptyHeadline_Clean(t *testing.T) {
	src := `## Overview

This package provides JWT-based authentication middleware. It validates
tokens on every request and injects the parsed claims into the context.
Claims are accessible via auth.FromContext(ctx). (internal/auth/auth.go:1-20)
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorEmptyHeadline)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("EmptyHeadline: expected 0 violations, got %d: %+v", len(got), got)
	}
}

func TestEmptyHeadline_Fires(t *testing.T) {
	src := `## Overview

See above.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorEmptyHeadline)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) == 0 {
		t.Error("EmptyHeadline: expected violations for one-sentence overview, got 0")
	}
}

func TestEmptyHeadline_NonSuspectTitle(t *testing.T) {
	// "Key types" is not in the suspect list — should never fire.
	src := `## Key types

Only one sentence here.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorEmptyHeadline)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("EmptyHeadline: expected 0 violations for non-suspect title, got %d", len(got))
	}
}

// --- code_example_present ---

func TestCodeExamplePresent_Clean(t *testing.T) {
	src := "## Usage\n\n```go\nfmt.Println(\"hello\")\n```\n"
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorCodeExamplePresent)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("CodeExamplePresent: expected 0 violations, got %d", len(got))
	}
}

func TestCodeExamplePresent_Fires(t *testing.T) {
	src := "## Usage\n\nNo code block here at all.\n"
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorCodeExamplePresent)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) == 0 {
		t.Error("CodeExamplePresent: expected violation, got 0")
	}
}

func TestCodeExamplePresent_EmptyPage(t *testing.T) {
	src := ""
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorCodeExamplePresent)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) == 0 {
		t.Error("CodeExamplePresent: expected violation for empty page, got 0")
	}
}

// --- citation_density ---

func TestCitationDensity_Clean(t *testing.T) {
	// ~60 words with 1 citation → 60 words/citation, well under 200.
	src := `## Auth middleware

The middleware validates the JWT on every request and injects claims
into the context via auth.FromContext. (internal/auth/middleware.go:10-40)
It returns 401 on missing or invalid tokens. The signing algorithm is
RS256 and the key set is fetched on startup. Token expiry is enforced
by comparing exp to the server clock.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorCitationDensity)
	got := v.Validate(input, quality.ValidatorConfig{CitationDensityWordsPerCitation: 200})
	if len(got) != 0 {
		t.Errorf("CitationDensity: expected 0 violations, got %d: %+v", len(got), got)
	}
}

func TestCitationDensity_Fires(t *testing.T) {
	// ~80 words with 0 citations → fires when threshold is 50.
	src := `## Auth middleware

The middleware validates the JWT on every request and injects claims
into the context. It returns 401 on missing or invalid tokens.
The signing algorithm is RS256 and the key set is fetched on startup.
Token expiry is enforced by comparing exp to the server clock.
Sessions are stateless. Refresh tokens are not supported.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorCitationDensity)
	got := v.Validate(input, quality.ValidatorConfig{CitationDensityWordsPerCitation: 50})
	if len(got) == 0 {
		t.Error("CitationDensity: expected violation, got 0")
	}
}

func TestCitationDensity_EmptyPage(t *testing.T) {
	// Word count zero → no violation (nothing to check).
	src := ""
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorCitationDensity)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("CitationDensity: expected 0 violations for empty page, got %d", len(got))
	}
}

// TestCitationDensity_ZeroCitations_CleanMessage asserts that when there are
// zero citations the violation message uses the zero-citations branch ("0
// citations for") and does NOT contain the "1 per ~" ratio text (which would
// be nonsensical when the denominator is 0).
func TestCitationDensity_ZeroCitations_CleanMessage(t *testing.T) {
	// ~80 words, no parseable citations, threshold=50 → fires.
	src := `## Router

The router dispatches incoming HTTP requests to the correct handler based on
the URL path and method. Middleware is applied in registration order.
Each route handler runs in its own goroutine. Panics are recovered and
converted to 500 responses. The router supports prefix-matching and exact
matching simultaneously.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorCitationDensity)
	got := v.Validate(input, quality.ValidatorConfig{CitationDensityWordsPerCitation: 50})
	if len(got) == 0 {
		t.Fatal("CitationDensity: expected at least 1 violation for zero-citation prose")
	}
	msg := got[0].Message
	if strings.Contains(msg, "1 per ~") {
		t.Errorf("CitationDensity zero-citation message must not contain '1 per ~'; got: %q", msg)
	}
	if !strings.Contains(msg, "0 citations for") {
		t.Errorf("CitationDensity zero-citation message must contain '0 citations for'; got: %q", msg)
	}
	// Confirm the fmt.Sprintf formatting is not escaped.
	_ = fmt.Sprintf("message: %s", msg)
}

// --- reading_level ---

func TestReadingLevel_Clean(t *testing.T) {
	// Short sentences, common words → high Flesch score.
	src := `## What it does

This tool reads files. It prints each line. It stops at EOF.
Users can set a limit. The limit caps the line count.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorReadingLevel)
	got := v.Validate(input, quality.ValidatorConfig{ReadingLevelFloor: 50})
	if len(got) != 0 {
		t.Errorf("ReadingLevel: expected 0 violations for simple prose, got %d: %+v", len(got), got)
	}
}

func TestReadingLevel_Fires(t *testing.T) {
	// Very long sentences with polysyllabic words → low Flesch score.
	src := `## Implementation

The asynchronous bidirectional communication infrastructure instantiates
an extensible polymorphic abstraction layer that encapsulates the
heterogeneous integration patterns necessitated by the distributed
microservice orchestration requirements while simultaneously maintaining
transactional consistency guarantees across geographically distributed
deployment environments with negligible performance degradation.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorReadingLevel)
	// Use a high floor to force the violation.
	got := v.Validate(input, quality.ValidatorConfig{ReadingLevelFloor: 60})
	if len(got) == 0 {
		t.Error("ReadingLevel: expected violation for dense technical jargon, got 0")
	}
}

func TestReadingLevel_DefaultFloor(t *testing.T) {
	// With zero config the floor defaults to 50. Check it doesn't panic.
	src := "## Test\n\nSimple text.\n"
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorReadingLevel)
	// Should not panic.
	_ = v.Validate(input, quality.ValidatorConfig{})
}

// --- architectural_relevance ---

func TestArchRelevance_Clean(t *testing.T) {
	input := quality.NewMarkdownInput("## Auth\n\nContent.\n")
	v, _ := quality.ValidatorByID(quality.ValidatorArchitecturalRelevance)
	cfg := quality.ValidatorConfig{
		PageReferenceCount:             5,
		GraphRelationCount:             10,
		ArchRelevanceMinPageRefs:       1,
		ArchRelevanceMinGraphRelations: 3,
	}
	got := v.Validate(input, cfg)
	if len(got) != 0 {
		t.Errorf("ArchRelevance: expected 0 violations, got %d", len(got))
	}
}

func TestArchRelevance_Fires(t *testing.T) {
	input := quality.NewMarkdownInput("## Thin wrapper\n\nContent.\n")
	v, _ := quality.ValidatorByID(quality.ValidatorArchitecturalRelevance)
	cfg := quality.ValidatorConfig{
		PageReferenceCount:             0,
		GraphRelationCount:             1,
		ArchRelevanceMinPageRefs:       2,
		ArchRelevanceMinGraphRelations: 5,
	}
	got := v.Validate(input, cfg)
	if len(got) == 0 {
		t.Error("ArchRelevance: expected violation for low-relevance subject, got 0")
	}
}

func TestArchRelevance_PageRefSatisfies(t *testing.T) {
	// pageRefs alone meeting the threshold is sufficient.
	input := quality.NewMarkdownInput("## Well-referenced\n\nContent.\n")
	v, _ := quality.ValidatorByID(quality.ValidatorArchitecturalRelevance)
	cfg := quality.ValidatorConfig{
		PageReferenceCount:             3,
		GraphRelationCount:             0,
		ArchRelevanceMinPageRefs:       2,
		ArchRelevanceMinGraphRelations: 5,
	}
	got := v.Validate(input, cfg)
	if len(got) != 0 {
		t.Errorf("ArchRelevance: expected 0 violations when pageRefs satisfies threshold, got %d", len(got))
	}
}

// --- factual_grounding ---

func TestFactualGrounding_Clean(t *testing.T) {
	src := `## Auth middleware

The middleware validates the JWT on every request. (internal/auth/middleware.go:10-40)
It returns 401 on missing or invalid tokens. (internal/auth/errors.go:5-15)
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorFactualGrounding)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("FactualGrounding: expected 0 violations with citations, got %d: %+v", len(got), got)
	}
}

func TestFactualGrounding_Fires(t *testing.T) {
	src := `## Auth middleware

The middleware validates the JWT on every request and returns 401 on
invalid tokens. The signing algorithm ensures token integrity.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorFactualGrounding)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) == 0 {
		t.Error("FactualGrounding: expected violation for uncited assertions, got 0")
	}
}

func TestFactualGrounding_DescriptionNoCitation(t *testing.T) {
	// Non-assertion prose should not fire (no assertion verbs).
	src := `## Architecture

This package is the authentication layer of the system.
It is one of three core packages in the service.
`
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorFactualGrounding)
	got := v.Validate(input, quality.ValidatorConfig{})
	if len(got) != 0 {
		t.Errorf("FactualGrounding: expected 0 violations for non-assertion prose, got %d: %+v", len(got), got)
	}
}

// --- block_count ---

func TestBlockCount_Clean(t *testing.T) {
	src := "## A\n\nContent.\n\n## B\n\nMore content.\n\n## C\n\nEven more.\n"
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorBlockCount)
	got := v.Validate(input, quality.ValidatorConfig{BlockCountMin: 2, BlockCountMax: 10})
	if len(got) != 0 {
		t.Errorf("BlockCount: expected 0 violations, got %d", len(got))
	}
}

func TestBlockCount_TooFew(t *testing.T) {
	src := "## Only one section\n\nContent.\n"
	input := quality.NewMarkdownInput(src)
	v, _ := quality.ValidatorByID(quality.ValidatorBlockCount)
	got := v.Validate(input, quality.ValidatorConfig{BlockCountMin: 3, BlockCountMax: 10})
	if len(got) == 0 {
		t.Error("BlockCount: expected violation for too few blocks, got 0")
	}
}

func TestBlockCount_TooMany(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 15; i++ {
		sb.WriteString(fmt.Sprintf("## Section %d\n\nContent.\n\n", i+1))
	}
	input := quality.NewMarkdownInput(sb.String())
	v, _ := quality.ValidatorByID(quality.ValidatorBlockCount)
	got := v.Validate(input, quality.ValidatorConfig{BlockCountMin: 2, BlockCountMax: 10})
	if len(got) == 0 {
		t.Error("BlockCount: expected violation for too many blocks, got 0")
	}
}

func TestBlockCount_NoUpperLimit(t *testing.T) {
	// MaxBlocks == 0 means no upper limit.
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString(fmt.Sprintf("## Section %d\n\nContent.\n\n", i+1))
	}
	input := quality.NewMarkdownInput(sb.String())
	v, _ := quality.ValidatorByID(quality.ValidatorBlockCount)
	got := v.Validate(input, quality.ValidatorConfig{BlockCountMin: 2, BlockCountMax: 0})
	if len(got) != 0 {
		t.Errorf("BlockCount: expected 0 violations when max is 0, got %d", len(got))
	}
}
