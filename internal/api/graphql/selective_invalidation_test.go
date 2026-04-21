// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"encoding/json"
	"testing"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// Exercises the selective-invalidation flag contract end-to-end at the
// package level: the resolver reindex path uses selectiveInvalidationEnabled
// to pick between MarkStaleForImpact and legacy MarkAllStale. The test seeds
// artifacts into a MemStore, drives the same branching the resolver does,
// and asserts the two paths behave as advertised.
func TestReindexSelectiveInvalidation_SurgicalPath(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "true")

	store := knowledgepkg.NewMemStore()
	const repoID = "repo-sel-1"

	// Artifact 1: evidence references the symbol we will modify.
	// Artifact 2: evidence references a different symbol.
	// Artifact 3: no sections/evidence at all.
	// Artifact 4: evidence with only source_type=repository.
	// Artifact 5: file-path-based evidence.
	artifacts := map[string]knowledgepkg.ArtifactType{
		"hit":        knowledgepkg.ArtifactCliffNotes,
		"miss":       knowledgepkg.ArtifactLearningPath,
		"no-evid":    knowledgepkg.ArtifactCodeTour,
		"repo-only":  knowledgepkg.ArtifactArchitectureDiagram,
		"file-based": knowledgepkg.ArtifactWorkflowStory,
	}
	ids := make(map[string]string, len(artifacts))
	for label, typ := range artifacts {
		a, err := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
			RepositoryID: repoID,
			Type:         typ,
			Audience:     knowledgepkg.AudienceDeveloper,
			Depth:        knowledgepkg.DepthMedium,
			Status:       knowledgepkg.StatusPending,
		})
		if err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
		if err := store.UpdateKnowledgeArtifactStatus(a.ID, knowledgepkg.StatusReady); err != nil {
			t.Fatalf("ready %s: %v", label, err)
		}
		ids[label] = a.ID
	}

	// Sections + evidence layout.
	seed := func(label string, sections []knowledgepkg.Section) {
		evCopy := make([][]knowledgepkg.Evidence, len(sections))
		for i := range sections {
			evCopy[i] = sections[i].Evidence
			sections[i].Evidence = nil
		}
		if err := store.StoreKnowledgeSections(ids[label], sections); err != nil {
			t.Fatalf("sections %s: %v", label, err)
		}
		stored := store.GetKnowledgeSections(ids[label])
		for i, sec := range stored {
			if len(evCopy[i]) == 0 {
				continue
			}
			if err := store.StoreKnowledgeEvidence(sec.ID, evCopy[i]); err != nil {
				t.Fatalf("evidence %s: %v", label, err)
			}
		}
	}
	seed("hit", []knowledgepkg.Section{
		{Title: "Hit", Evidence: []knowledgepkg.Evidence{{SourceType: knowledgepkg.EvidenceSymbol, SourceID: "sym-A"}}},
	})
	seed("miss", []knowledgepkg.Section{
		{Title: "Miss", Evidence: []knowledgepkg.Evidence{{SourceType: knowledgepkg.EvidenceSymbol, SourceID: "sym-Z"}}},
	})
	// "no-evid" deliberately left with no sections.
	seed("repo-only", []knowledgepkg.Section{
		{Title: "Whole-Repo", Evidence: []knowledgepkg.Evidence{
			{SourceType: knowledgepkg.EvidenceRepository, SourceID: repoID},
		}},
	})
	seed("file-based", []knowledgepkg.Section{
		{Title: "File", Evidence: []knowledgepkg.Evidence{
			{SourceType: knowledgepkg.EvidenceFile, SourceID: "ev-1", FilePath: "pkg/touched.go"},
		}},
	})

	// Invoke with the modified symbol + touched file, matching what the
	// resolver feeds into MarkStaleForImpact.
	reasons := knowledgepkg.MarkStaleForImpact(
		store,
		repoID,
		[]string{"sym-A"},
		[]string{"pkg/touched.go"},
		"imp-report-xyz",
		selectiveInvalidationMaxChanges(),
	)

	stale := map[string]*graphstore.StaleArtifactReason{}
	for i := range reasons {
		stale[reasons[i].ArtifactID] = &reasons[i]
	}

	// "miss" should NOT be staled. Everything else should be.
	if _, ok := stale[ids["miss"]]; ok {
		t.Fatalf("miss artifact should stay fresh, got staled: %+v", stale[ids["miss"]])
	}
	if _, ok := stale[ids["hit"]]; !ok {
		t.Fatalf("hit artifact should be staled")
	}
	if _, ok := stale[ids["no-evid"]]; !ok {
		t.Fatalf("no-evidence artifact should fall back to blanket-stale")
	}
	if _, ok := stale[ids["repo-only"]]; !ok {
		t.Fatalf("repository-only artifact should fall back to blanket-stale")
	}
	if _, ok := stale[ids["file-based"]]; !ok {
		t.Fatalf("file-based artifact should be staled via file-path evidence")
	}

	// Attribution: hit carries the specific symbol, file-based carries the path.
	if r := stale[ids["hit"]]; r.Blanket || len(r.Symbols) != 1 || r.Symbols[0] != "sym-A" {
		t.Fatalf("hit reason wrong: %+v", r)
	}
	if r := stale[ids["file-based"]]; r.Blanket || len(r.Files) != 1 || r.Files[0] != "pkg/touched.go" {
		t.Fatalf("file reason wrong: %+v", r)
	}
	if r := stale[ids["no-evid"]]; !r.Blanket {
		t.Fatalf("expected Blanket=true for no-evidence fallback")
	}
	if r := stale[ids["repo-only"]]; !r.Blanket {
		t.Fatalf("expected Blanket=true for repository-only fallback")
	}

	// Persisted per-artifact reason JSON round-trips.
	hit := store.GetKnowledgeArtifact(ids["hit"])
	if hit == nil || hit.StaleReasonJSON == "" || hit.StaleReportID != "imp-report-xyz" {
		t.Fatalf("expected persisted reason on hit artifact: %+v", hit)
	}
	var parsed graphstore.StaleArtifactReason
	if err := json.Unmarshal([]byte(hit.StaleReasonJSON), &parsed); err != nil {
		t.Fatalf("parse persisted reason: %v", err)
	}
	if len(parsed.Symbols) != 1 || parsed.Symbols[0] != "sym-A" {
		t.Fatalf("persisted reason symbols wrong: %+v", parsed)
	}
}

// When the flag is off, the legacy blanket path runs and every ready artifact
// is staled regardless of evidence shape.
func TestReindexSelectiveInvalidation_FlagOff_Blanket(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION", "false")
	if selectiveInvalidationEnabled() {
		t.Fatalf("expected flag off, got true")
	}

	store := knowledgepkg.NewMemStore()
	const repoID = "repo-sel-off"

	// Two artifacts; one would stay fresh under selective, both stale under blanket.
	a1, _ := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		RepositoryID: repoID, Type: knowledgepkg.ArtifactCliffNotes,
		Audience: knowledgepkg.AudienceDeveloper, Depth: knowledgepkg.DepthMedium,
		Status: knowledgepkg.StatusPending,
	})
	a2, _ := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		RepositoryID: repoID, Type: knowledgepkg.ArtifactLearningPath,
		Audience: knowledgepkg.AudienceDeveloper, Depth: knowledgepkg.DepthMedium,
		Status: knowledgepkg.StatusPending,
	})
	_ = store.UpdateKnowledgeArtifactStatus(a1.ID, knowledgepkg.StatusReady)
	_ = store.UpdateKnowledgeArtifactStatus(a2.ID, knowledgepkg.StatusReady)
	_ = store.StoreKnowledgeSections(a1.ID, []knowledgepkg.Section{
		{Title: "S", Evidence: nil},
	})
	_ = store.StoreKnowledgeSections(a2.ID, []knowledgepkg.Section{
		{Title: "S", Evidence: []knowledgepkg.Evidence{{SourceType: knowledgepkg.EvidenceSymbol, SourceID: "sym-Unrelated"}}},
	})

	// Simulate the resolver's blanket branch.
	knowledgepkg.MarkAllStale(store, repoID)

	if !store.GetKnowledgeArtifact(a1.ID).Stale || !store.GetKnowledgeArtifact(a2.ID).Stale {
		t.Fatalf("expected both artifacts staled under blanket path")
	}
}

// Guards that the env-var max-changes override is honored.
func TestSelectiveInvalidationMaxChangesOverride(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION_MAX_CHANGES", "50")
	if got := selectiveInvalidationMaxChanges(); got != 50 {
		t.Fatalf("expected 50, got %d", got)
	}
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION_MAX_CHANGES", "not-an-int")
	if got := selectiveInvalidationMaxChanges(); got != 200 {
		t.Fatalf("expected default 200 on parse error, got %d", got)
	}
	t.Setenv("SOURCEBRIDGE_SELECTIVE_INVALIDATION_MAX_CHANGES", "")
	if got := selectiveInvalidationMaxChanges(); got != 200 {
		t.Fatalf("expected default 200 on empty, got %d", got)
	}
}
