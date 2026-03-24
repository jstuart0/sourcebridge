// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"strings"
	"time"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

type ScopeQualityMetrics struct {
	TotalArtifacts        int     `json:"total_artifacts"`
	ReadyArtifacts        int     `json:"ready_artifacts"`
	FailedArtifacts       int     `json:"failed_artifacts"`
	SuccessRate           float64 `json:"success_rate"`
	Coverage              float64 `json:"coverage"`
	AvgGenerationSeconds  float64 `json:"avg_generation_seconds"`
	AvgWordsPerArtifact   float64 `json:"avg_words_per_artifact"`
	AvgWordsPerSection    float64 `json:"avg_words_per_section"`
	AvgEvidencePerSection float64 `json:"avg_evidence_per_section"`
}

type QualityMetrics struct {
	RepositoryID      string                         `json:"repository_id"`
	EvidenceSourceMix map[string]int                 `json:"evidence_source_mix"`
	ByScope           map[string]ScopeQualityMetrics `json:"by_scope"`
}

type scopeAccumulator struct {
	totalArtifacts    int
	readyArtifacts    int
	failedArtifacts   int
	generationTotal   time.Duration
	generationCount   int
	artifactWordTotal int
	sectionWordTotal  int
	sectionCount      int
	evidenceCount     int
}

// CollectQualityMetrics summarizes knowledge quality for a repository.
func CollectQualityMetrics(kstore KnowledgeStore, gstore graphstore.GraphStore, repoID string) QualityMetrics {
	metrics := QualityMetrics{
		RepositoryID:      repoID,
		EvidenceSourceMix: make(map[string]int),
		ByScope:           make(map[string]ScopeQualityMetrics),
	}
	if kstore == nil || gstore == nil {
		return metrics
	}

	accs := map[string]*scopeAccumulator{
		string(ScopeRepository): {},
		string(ScopeFile):       {},
		string(ScopeSymbol):     {},
		string(ScopeModule):     {},
	}

	readyFileScopes := map[string]bool{}
	readySymbolScopes := map[string]bool{}
	readyModuleScopes := map[string]bool{}
	repositoryReady := false

	for _, artifact := range kstore.GetKnowledgeArtifacts(repoID) {
		scopeType := string(ScopeRepository)
		scopeKey := "repository:"
		if artifact.Scope != nil {
			scope := artifact.Scope.Normalize()
			scopeType = string(scope.ScopeType)
			scopeKey = scope.ScopeKey()
		}
		acc := accs[scopeType]
		if acc == nil {
			acc = &scopeAccumulator{}
			accs[scopeType] = acc
		}
		acc.totalArtifacts++

		switch artifact.Status {
		case StatusReady:
			acc.readyArtifacts++
			if !artifact.GeneratedAt.IsZero() {
				acc.generationTotal += artifact.GeneratedAt.Sub(artifact.CreatedAt)
				acc.generationCount++
			}
			switch scopeType {
			case string(ScopeRepository):
				repositoryReady = true
			case string(ScopeFile):
				readyFileScopes[scopeKey] = true
			case string(ScopeSymbol):
				readySymbolScopes[scopeKey] = true
			case string(ScopeModule):
				readyModuleScopes[scopeKey] = true
			}
		case StatusFailed:
			acc.failedArtifacts++
		}

		sections := artifact.Sections
		if len(sections) == 0 {
			sections = kstore.GetKnowledgeSections(artifact.ID)
		}
		artifactWords := 0
		for _, section := range sections {
			words := wordCount(section.Content) + wordCount(section.Summary)
			artifactWords += words
			acc.sectionWordTotal += words
			acc.sectionCount++

			evidence := section.Evidence
			if len(evidence) == 0 {
				evidence = kstore.GetKnowledgeEvidence(section.ID)
			}
			acc.evidenceCount += len(evidence)
			for _, ev := range evidence {
				metrics.EvidenceSourceMix[string(ev.SourceType)]++
			}
		}
		acc.artifactWordTotal += artifactWords
	}

	totalFiles := len(gstore.GetFiles(repoID))
	totalSymbols, _ := gstore.GetSymbols(repoID, nil, nil, 0, 0)
	totalModules := len(gstore.GetModules(repoID))

	for scopeType, acc := range accs {
		if acc == nil {
			continue
		}
		scopeMetrics := ScopeQualityMetrics{
			TotalArtifacts:  acc.totalArtifacts,
			ReadyArtifacts:  acc.readyArtifacts,
			FailedArtifacts: acc.failedArtifacts,
		}
		if acc.totalArtifacts > 0 {
			scopeMetrics.SuccessRate = float64(acc.readyArtifacts) / float64(acc.totalArtifacts)
		}
		if acc.generationCount > 0 {
			scopeMetrics.AvgGenerationSeconds = acc.generationTotal.Seconds() / float64(acc.generationCount)
		}
		if acc.readyArtifacts > 0 {
			scopeMetrics.AvgWordsPerArtifact = float64(acc.artifactWordTotal) / float64(acc.readyArtifacts)
		}
		if acc.sectionCount > 0 {
			scopeMetrics.AvgWordsPerSection = float64(acc.sectionWordTotal) / float64(acc.sectionCount)
			scopeMetrics.AvgEvidencePerSection = float64(acc.evidenceCount) / float64(acc.sectionCount)
		}
		switch scopeType {
		case string(ScopeRepository):
			if repositoryReady {
				scopeMetrics.Coverage = 1
			}
		case string(ScopeFile):
			if totalFiles > 0 {
				scopeMetrics.Coverage = float64(len(readyFileScopes)) / float64(totalFiles)
			}
		case string(ScopeSymbol):
			if len(totalSymbols) > 0 {
				scopeMetrics.Coverage = float64(len(readySymbolScopes)) / float64(len(totalSymbols))
			}
		case string(ScopeModule):
			if totalModules > 0 {
				scopeMetrics.Coverage = float64(len(readyModuleScopes)) / float64(totalModules)
			}
		}
		metrics.ByScope[scopeType] = scopeMetrics
	}

	return metrics
}

func wordCount(text string) int {
	return len(strings.Fields(text))
}
