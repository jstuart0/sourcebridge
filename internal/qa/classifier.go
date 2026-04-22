// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"path/filepath"
	"strings"
)

// QuestionKind enumerates the canonical question classes the
// orchestrator routes on. Values parallel Python _question_type
// (workers/cli_ask.py:150) so diagnostics round-trip unchanged.
type QuestionKind string

const (
	KindArchitecture        QuestionKind = "architecture"
	KindExecutionFlow       QuestionKind = "execution_flow"
	KindRequirementCoverage QuestionKind = "requirement_coverage"
	KindOwnership           QuestionKind = "ownership"
	KindDataModel           QuestionKind = "data_model"
	KindRiskReview          QuestionKind = "risk_review"
	KindBehavior            QuestionKind = "behavior"
)

// ClassifyQuestion mirrors Python _question_type. The keyword sets
// and precedence are preserved bit-for-bit so the Phase 4 parity
// benchmark sees an identical classification decision per question.
func ClassifyQuestion(question string) QuestionKind {
	q := strings.ToLower(question)
	if containsAnyWord(q, []string{"architecture", "high level", "1000 foot", "subsystem"}) {
		return KindArchitecture
	}
	if containsAnyWord(q, []string{"flow", "path", "request", "how does", "what happens when"}) {
		return KindExecutionFlow
	}
	if strings.Contains(q, "requirement") || strings.Contains(q, "req-") {
		return KindRequirementCoverage
	}
	if containsAnyWord(q, []string{"where is", "which file", "where does"}) {
		return KindOwnership
	}
	if containsAnyWord(q, []string{"schema", "model", "table", "entity", "data"}) {
		return KindDataModel
	}
	if containsAnyWord(q, []string{"risk", "bug", "review", "unsafe", "vulnerability"}) {
		return KindRiskReview
	}
	return KindBehavior
}

func containsAnyWord(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// PathBoost is the per-file score delta the deep-mode retriever
// applies after base search ranking. Positive for plausibly
// relevant files; negative for tests / examples / docs / prompt
// templates that usually aren't what the user is asking about.
type PathBoost struct {
	Delta   int
	Reasons []string
}

// PathBoosts mirrors Python _deep_path_boosts
// (workers/cli_ask.py:248). Keep reasons strings in sync — the
// Monitor page surfaces them verbatim.
//
// filePath must be the repo-relative path (forward slashes). The
// `name` we match against is the base name (case-insensitive).
func PathBoosts(filePath, question string, kind QuestionKind) PathBoost {
	pathText := strings.ToLower(strings.ReplaceAll(filePath, "\\", "/"))
	name := strings.ToLower(filepath.Base(pathText))
	var score int
	var reasons []string

	// Penalties (apply to every question kind).
	if containsAnyWord(pathText, []string{"/test", "_test.", ".test.", "tests/"}) {
		score -= 6
		reasons = append(reasons, "penalty:test")
	}
	if containsAnyWord(pathText, []string{"examples/", "benchmark", "docs/"}) {
		score -= 4
		reasons = append(reasons, "penalty:non-product")
	}

	q := strings.ToLower(question)
	switch kind {
	case KindArchitecture:
		if strings.Contains(pathText, "architecture") {
			score += 12
			reasons = append(reasons, "plan:architecture")
		}
		if strings.Contains(pathText, "diagram") {
			score += 10
			reasons = append(reasons, "plan:diagram")
		}
		if strings.Contains(q, "architecture diagram") || (strings.Contains(q, "architecture") && strings.Contains(q, "diagram")) {
			markers := []string{
				"web/src/components/architecture/architecturediagram.tsx",
				"workers/knowledge/architecture_diagram.py",
				"internal/api/graphql/knowledge_support.go",
				"internal/api/graphql/schema.resolvers.go",
				"internal/architecture/diagram.go",
				"web/src/lib/graphql/queries.ts",
			}
			if containsAnyWord(pathText, markers) {
				score += 24
				reasons = append(reasons, "plan:architecture-diagram")
			}
		}
		if strings.Contains(pathText, "workers/knowledge/prompts/architecture_diagram.py") {
			score -= 8
			reasons = append(reasons, "penalty:prompt-template")
		}
		if containsAnyWord(q, []string{"refresh", "regenerate", "generated"}) &&
			containsAnyWord(pathText, []string{"knowledge_support.go", "schema.resolvers.go", "architecturediagram.tsx", "queries.ts"}) {
			score += 14
			reasons = append(reasons, "plan:refresh")
		}
	case KindExecutionFlow:
		if containsAnyWord(pathText, []string{"routes/", "handler", "service", "worker", "job"}) {
			score += 8
			reasons = append(reasons, "plan:flow")
		}
	case KindBehavior:
		if containsAnyWord(pathText, []string{"routes/", "service", "auth", "session", "store"}) {
			score += 6
			reasons = append(reasons, "plan:behavior")
		}
	}

	// Per-file boosts (all kinds).
	switch name {
	case "schema.resolvers.go":
		score += 3
		reasons = append(reasons, "plan:resolver")
	case "knowledge_support.go":
		score += 3
		reasons = append(reasons, "plan:graphql-support")
	case "architecturediagram.tsx":
		score += 6
		reasons = append(reasons, "plan:ui")
	case "queries.ts":
		score += 5
		reasons = append(reasons, "plan:graphql-query")
	}

	return PathBoost{Delta: score, Reasons: reasons}
}
