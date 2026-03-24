// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// UnderstandingScore is a composite metric for how well a codebase is understood.
type UnderstandingScore struct {
	Overall               float64   `json:"overall"`               // 0-100 composite
	TraceabilityCoverage  float64   `json:"traceabilityCoverage"`  // 0-100: % of requirements linked
	DocumentationCoverage float64   `json:"documentationCoverage"` // 0-100: % of public symbols with doc comments
	ReviewCoverage        float64   `json:"reviewCoverage"`        // 0-100: % of files reviewed
	TestCoverage          float64   `json:"testCoverage"`          // 0-100: ratio of test symbols to non-test symbols
	KnowledgeFreshness    float64   `json:"knowledgeFreshness"`    // 0-100: % of knowledge artifacts not stale
	AICodeRatio           float64   `json:"aiCodeRatio"`           // 0-100: informational, not in composite
	ComputedAt            time.Time `json:"computedAt"`
}

// KnowledgeFreshnessProvider abstracts the knowledge store to avoid an import
// cycle between graph and knowledge packages.
type KnowledgeFreshnessProvider interface {
	// GetFreshnessRatio returns (fresh, total) counts of knowledge artifacts for a repo.
	// "Fresh" means status=ready and stale=false.
	GetFreshnessRatio(repoID string) (fresh int, total int)
}

// ComputeUnderstandingScore computes the understanding score for a repository.
// Weights: traceability 25%, documentation 25%, review 20%, test 15%, knowledge 15%.
func ComputeUnderstandingScore(store GraphStore, kfp KnowledgeFreshnessProvider, repoID string) *UnderstandingScore {
	score := &UnderstandingScore{
		ComputedAt: time.Now(),
	}

	// 1. Traceability coverage: % of requirements with at least one non-rejected link
	_, reqTotal := store.GetRequirements(repoID, 0, 0)
	if reqTotal > 0 {
		// Fetch all non-rejected links for the repo in one query instead of
		// calling GetLinksForRequirement per requirement (N+1).
		allLinks := store.GetLinksForRepo(repoID)
		linkedReqs := make(map[string]bool, len(allLinks))
		for _, link := range allLinks {
			linkedReqs[link.RequirementID] = true
		}
		score.TraceabilityCoverage = float64(len(linkedReqs)) / float64(reqTotal) * 100
	}

	// 2. Documentation coverage: % of public symbols with doc comments
	withDocs, totalPublic := store.GetPublicSymbolDocCoverage(repoID)
	if totalPublic > 0 {
		score.DocumentationCoverage = float64(withDocs) / float64(totalPublic) * 100
	}

	// 3. Review coverage: % of files with at least one review
	reviewResults := store.GetReviewResultsForRepo(repoID)
	if len(reviewResults) > 0 {
		files := store.GetFiles(repoID)
		if len(files) > 0 {
			// Build file ID set for O(1) lookup
			fileIDSet := make(map[string]bool, len(files))
			for _, f := range files {
				fileIDSet[f.ID] = true
			}

			// Resolve each unique review target to a file — O(review_count) queries
			// instead of O(file_count) queries. Review targets are either file IDs
			// or symbol IDs; we only call GetSymbol for symbol-level targets.
			reviewedFileSet := make(map[string]bool)
			resolved := make(map[string]bool)
			for _, rr := range reviewResults {
				if resolved[rr.TargetID] {
					continue
				}
				resolved[rr.TargetID] = true
				if fileIDSet[rr.TargetID] {
					reviewedFileSet[rr.TargetID] = true
				} else {
					sym := store.GetSymbol(rr.TargetID)
					if sym != nil {
						reviewedFileSet[sym.FileID] = true
					}
				}
			}
			score.ReviewCoverage = float64(len(reviewedFileSet)) / float64(len(files)) * 100
		}
	}

	// 4. Test coverage: ratio of test symbols to non-test symbols
	tests, totalSyms := store.GetTestSymbolRatio(repoID)
	if totalSyms > 0 {
		nonTest := totalSyms - tests
		if nonTest > 0 {
			ratio := float64(tests) / float64(nonTest) * 100
			if ratio > 100 {
				ratio = 100
			}
			score.TestCoverage = ratio
		} else {
			score.TestCoverage = 100
		}
	}

	// 5. Knowledge freshness: % of knowledge artifacts not stale
	if kfp != nil {
		fresh, total := kfp.GetFreshnessRatio(repoID)
		if total > 0 {
			score.KnowledgeFreshness = float64(fresh) / float64(total) * 100
		}
	}

	// 6. AI code ratio (informational, Phase 2A will populate)
	aiFiles, totalFiles := store.GetAICodeFileRatio(repoID)
	if totalFiles > 0 {
		score.AICodeRatio = float64(aiFiles) / float64(totalFiles) * 100
	}

	// Composite: traceability 25%, documentation 25%, review 20%, test 15%, knowledge 15%
	score.Overall = score.TraceabilityCoverage*0.25 +
		score.DocumentationCoverage*0.25 +
		score.ReviewCoverage*0.20 +
		score.TestCoverage*0.15 +
		score.KnowledgeFreshness*0.15

	return score
}

// IsPublicSymbol returns true if a symbol should be considered "public" for
// documentation coverage purposes, based on per-language visibility rules.
func IsPublicSymbol(sym *StoredSymbol) bool {
	if sym.IsTest {
		return false
	}
	switch sym.Kind {
	case "function", "method", "class", "struct", "interface", "enum", "type", "trait":
		// continue
	default:
		return false
	}

	lang := strings.ToLower(sym.Language)
	switch lang {
	case "go":
		r, _ := utf8.DecodeRuneInString(sym.Name)
		return unicode.IsUpper(r)
	case "python":
		return !strings.HasPrefix(sym.Name, "_")
	case "typescript", "javascript":
		return true
	case "java":
		sig := strings.ToLower(sym.Signature)
		return strings.Contains(sig, "public") || strings.Contains(sig, "protected")
	case "rust":
		return strings.HasPrefix(strings.TrimSpace(sym.Signature), "pub ")
	default:
		return true
	}
}
