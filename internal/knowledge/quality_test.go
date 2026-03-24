// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func TestCollectQualityMetrics(t *testing.T) {
	gstore := graph.NewStore()
	result := &indexer.IndexResult{
		RepoName: "quality-repo",
		RepoPath: "/tmp/quality-repo",
		Files: []indexer.FileResult{
			{
				Path:      "main.go",
				Language:  "go",
				LineCount: 50,
				Symbols: []indexer.Symbol{
					{ID: "sym-1", Name: "main", QualifiedName: "main.main", Kind: "function", Language: "go", FilePath: "main.go", StartLine: 1, EndLine: 20},
				},
			},
			{
				Path:      "auth.go",
				Language:  "go",
				LineCount: 80,
				Symbols: []indexer.Symbol{
					{ID: "sym-2", Name: "handleLogin", QualifiedName: "auth.handleLogin", Kind: "function", Language: "go", FilePath: "auth.go", StartLine: 10, EndLine: 40},
				},
			},
		},
		Modules: []indexer.Module{{ID: "mod-1", Name: "root", Path: ".", FileCount: 2}},
	}
	repo, err := gstore.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	kstore := NewMemStore()
	repoArtifact, _, err := kstore.ClaimArtifact(ArtifactKey{
		RepositoryID: repo.ID,
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Scope:        ArtifactScope{ScopeType: ScopeRepository},
	}, SourceRevision{})
	if err != nil {
		t.Fatalf("ClaimArtifact repo: %v", err)
	}
	repoArtifact.CreatedAt = time.Now().Add(-2 * time.Minute)
	_ = kstore.SupersedeArtifact(repoArtifact.ID, []Section{{
		Title:   "System Purpose",
		Content: "This repository handles login and request entry.",
		Summary: "Login and request entry.",
		Evidence: []Evidence{{
			SourceType: EvidenceFile,
			FilePath:   "main.go",
			LineStart:  1,
			LineEnd:    20,
		}},
	}})

	fileArtifact, _, err := kstore.ClaimArtifact(ArtifactKey{
		RepositoryID: repo.ID,
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Scope:        ArtifactScope{ScopeType: ScopeFile, ScopePath: "auth.go"},
	}, SourceRevision{})
	if err != nil {
		t.Fatalf("ClaimArtifact file: %v", err)
	}
	fileArtifact.CreatedAt = time.Now().Add(-time.Minute)
	_ = kstore.SupersedeArtifact(fileArtifact.ID, []Section{{
		Title:   "File Purpose",
		Content: "This file owns login-specific request handling.",
		Summary: "Login request handling.",
		Evidence: []Evidence{{
			SourceType: EvidenceSymbol,
			SourceID:   "sym-2",
			FilePath:   "auth.go",
			LineStart:  10,
			LineEnd:    40,
		}},
	}})

	failedArtifact, _, err := kstore.ClaimArtifact(ArtifactKey{
		RepositoryID: repo.ID,
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Scope:        ArtifactScope{ScopeType: ScopeSymbol, ScopePath: "auth.go#handleLogin"},
	}, SourceRevision{})
	if err != nil {
		t.Fatalf("ClaimArtifact symbol: %v", err)
	}
	if err := kstore.UpdateKnowledgeArtifactStatus(failedArtifact.ID, StatusFailed); err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus: %v", err)
	}

	metrics := CollectQualityMetrics(kstore, gstore, repo.ID)

	if metrics.ByScope[string(ScopeRepository)].Coverage != 1 {
		t.Fatalf("expected repository coverage to be 1, got %v", metrics.ByScope[string(ScopeRepository)].Coverage)
	}
	if metrics.ByScope[string(ScopeFile)].Coverage <= 0 {
		t.Fatalf("expected file coverage > 0, got %v", metrics.ByScope[string(ScopeFile)].Coverage)
	}
	if metrics.ByScope[string(ScopeSymbol)].FailedArtifacts != 1 {
		t.Fatalf("expected symbol failure count 1, got %v", metrics.ByScope[string(ScopeSymbol)].FailedArtifacts)
	}
	if metrics.EvidenceSourceMix[string(EvidenceFile)] != 1 {
		t.Fatalf("expected file evidence mix count 1, got %v", metrics.EvidenceSourceMix[string(EvidenceFile)])
	}
	if metrics.EvidenceSourceMix[string(EvidenceSymbol)] != 1 {
		t.Fatalf("expected symbol evidence mix count 1, got %v", metrics.EvidenceSourceMix[string(EvidenceSymbol)])
	}
}
