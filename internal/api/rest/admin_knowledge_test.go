// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

func TestCollectKnowledgeStatsCountsErrorCodes(t *testing.T) {
	store := graphstore.NewStore()
	repo := mustStoreAdminKnowledgeTestRepo(t, store)
	ks := knowledge.NewMemStore()

	ready, err := ks.StoreKnowledgeArtifact(&knowledge.Artifact{
		RepositoryID: repo.ID,
		Type:         knowledge.ArtifactCliffNotes,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Status:       knowledge.StatusReady,
		Scope:        &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact ready: %v", err)
	}
	if err := ks.SetArtifactFailed(ready.ID, "INTERNAL", "should be cleared on recovery"); err != nil {
		t.Fatalf("SetArtifactFailed ready: %v", err)
	}
	if err := ks.UpdateKnowledgeArtifactStatus(ready.ID, knowledge.StatusReady); err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus ready: %v", err)
	}

	failed, err := ks.StoreKnowledgeArtifact(&knowledge.Artifact{
		RepositoryID: repo.ID,
		Type:         knowledge.ArtifactWorkflowStory,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Status:       knowledge.StatusGenerating,
		Scope:        &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact failed: %v", err)
	}
	if err := ks.SetArtifactFailed(failed.ID, "LLM_EMPTY", "provider returned no content"); err != nil {
		t.Fatalf("SetArtifactFailed failed: %v", err)
	}

	unknown, err := ks.StoreKnowledgeArtifact(&knowledge.Artifact{
		RepositoryID: repo.ID,
		Type:         knowledge.ArtifactCodeTour,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Status:       knowledge.StatusFailed,
		Scope:        &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact unknown: %v", err)
	}
	if err := ks.UpdateKnowledgeArtifactStatus(unknown.ID, knowledge.StatusFailed); err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus unknown: %v", err)
	}

	server := &Server{store: store, knowledgeStore: ks}
	stats := server.collectKnowledgeStats(store)

	if stats.Total != 3 {
		t.Fatalf("expected total=3, got %d", stats.Total)
	}
	if stats.Ready != 1 {
		t.Fatalf("expected ready=1, got %d", stats.Ready)
	}
	if stats.Failed != 2 {
		t.Fatalf("expected failed=2, got %d", stats.Failed)
	}
	if stats.ByType[string(knowledge.ArtifactWorkflowStory)] != 1 {
		t.Fatalf("expected workflow story count, got %+v", stats.ByType)
	}
	if stats.ByErrorCode["LLM_EMPTY"] != 1 {
		t.Fatalf("expected LLM_EMPTY count, got %+v", stats.ByErrorCode)
	}
	if stats.ByErrorCode["UNKNOWN"] != 1 {
		t.Fatalf("expected UNKNOWN count, got %+v", stats.ByErrorCode)
	}
}

func TestHandleAdminKnowledgeStatusIncludesFailureDetails(t *testing.T) {
	store := graphstore.NewStore()
	repo := mustStoreAdminKnowledgeTestRepo(t, store)
	ks := knowledge.NewMemStore()

	generating, err := ks.StoreKnowledgeArtifact(&knowledge.Artifact{
		RepositoryID: repo.ID,
		Type:         knowledge.ArtifactLearningPath,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Status:       knowledge.StatusGenerating,
		Progress:     0.4,
		Scope:        &knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact generating: %v", err)
	}
	if err := ks.UpdateKnowledgeArtifactProgress(generating.ID, 0.4); err != nil {
		t.Fatalf("UpdateKnowledgeArtifactProgress: %v", err)
	}

	failed, err := ks.StoreKnowledgeArtifact(&knowledge.Artifact{
		RepositoryID: repo.ID,
		Type:         knowledge.ArtifactWorkflowStory,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Status:       knowledge.StatusGenerating,
		Scope:        &knowledge.ArtifactScope{ScopeType: knowledge.ScopeFile, ScopePath: "cmd/server/main.go"},
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact failed: %v", err)
	}
	if err := ks.SetArtifactFailed(failed.ID, "WORKER_UNAVAILABLE", "connection refused"); err != nil {
		t.Fatalf("SetArtifactFailed: %v", err)
	}

	server := &Server{store: store, knowledgeStore: ks}
	req := httptest.NewRequest("GET", "/api/v1/admin/knowledge", nil)
	rec := httptest.NewRecorder()

	server.handleAdminKnowledgeStatus(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload struct {
		Configured   bool            `json:"configured"`
		Stats        knowledgeStats  `json:"stats"`
		Repositories []repoKnowledge `json:"repositories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if !payload.Configured {
		t.Fatal("expected configured=true")
	}
	if payload.Stats.Generating != 1 || payload.Stats.Failed != 1 {
		t.Fatalf("unexpected stats: %+v", payload.Stats)
	}
	if len(payload.Repositories) != 1 {
		t.Fatalf("expected one repository entry, got %d", len(payload.Repositories))
	}
	artifacts := payload.Repositories[0].Artifacts
	if len(artifacts) != 2 {
		t.Fatalf("expected two artifacts, got %d", len(artifacts))
	}
	if artifacts[0].Status != knowledge.StatusGenerating {
		t.Fatalf("expected in-flight artifact first, got %+v", artifacts[0])
	}
	if artifacts[1].ErrorCode != "WORKER_UNAVAILABLE" {
		t.Fatalf("expected failed artifact error code, got %+v", artifacts[1])
	}
	if artifacts[1].ErrorMessage != "connection refused" {
		t.Fatalf("expected failed artifact error message, got %+v", artifacts[1])
	}
	if artifacts[1].ScopeType != string(knowledge.ScopeFile) || artifacts[1].ScopePath != "cmd/server/main.go" {
		t.Fatalf("expected scope details, got %+v", artifacts[1])
	}
}

func mustStoreAdminKnowledgeTestRepo(t *testing.T, store *graphstore.Store) *graphstore.Repository {
	t.Helper()

	repo, err := store.StoreIndexResult(&indexer.IndexResult{
		RepoName: "knowledge-admin-repo",
		RepoPath: "/tmp/knowledge-admin-repo",
		Files: []indexer.FileResult{
			{
				Path:      "cmd/server/main.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{
						ID:            "sym-main",
						Name:          "main",
						QualifiedName: "main.main",
						Kind:          "function",
						Language:      "go",
						FilePath:      "cmd/server/main.go",
						StartLine:     1,
						EndLine:       20,
					},
				},
			},
		},
		TotalFiles:   1,
		TotalSymbols: 1,
	})
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}
	return repo
}
