// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import "testing"

func TestKnowledgeArtifactCRUD(t *testing.T) {
	s := NewMemStore()

	artifact := &Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusPending,
		SourceRevision: SourceRevision{
			CommitSHA: "abc123",
			Branch:    "main",
		},
	}
	stored, err := s.StoreKnowledgeArtifact(artifact)
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	if stored.ID == "" {
		t.Fatal("expected ID to be assigned")
	}
	if stored.Status != StatusPending {
		t.Fatalf("expected status pending, got %s", stored.Status)
	}

	fetched := s.GetKnowledgeArtifact(stored.ID)
	if fetched == nil {
		t.Fatal("expected to find artifact")
	}
	if fetched.RepositoryID != "repo-1" {
		t.Fatalf("expected repo-1, got %s", fetched.RepositoryID)
	}
	if fetched.SourceRevision.CommitSHA != "abc123" {
		t.Fatalf("expected commit abc123, got %s", fetched.SourceRevision.CommitSHA)
	}

	artifacts := s.GetKnowledgeArtifacts("repo-1")
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}

	err = s.UpdateKnowledgeArtifactStatus(stored.ID, StatusReady)
	if err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus: %v", err)
	}
	fetched = s.GetKnowledgeArtifact(stored.ID)
	if fetched.Status != StatusReady {
		t.Fatalf("expected status ready, got %s", fetched.Status)
	}
	if fetched.GeneratedAt.IsZero() {
		t.Fatal("expected generated_at to be set when status is ready")
	}

	err = s.MarkKnowledgeArtifactStale(stored.ID, true)
	if err != nil {
		t.Fatalf("MarkKnowledgeArtifactStale: %v", err)
	}
	fetched = s.GetKnowledgeArtifact(stored.ID)
	if !fetched.Stale {
		t.Fatal("expected artifact to be stale")
	}

	err = s.DeleteKnowledgeArtifact(stored.ID)
	if err != nil {
		t.Fatalf("DeleteKnowledgeArtifact: %v", err)
	}
	if s.GetKnowledgeArtifact(stored.ID) != nil {
		t.Fatal("expected artifact to be deleted")
	}
}

func TestKnowledgeSectionsAndEvidence(t *testing.T) {
	s := NewMemStore()

	stored, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceBeginner,
		Depth:        DepthSummary,
		Status:       StatusReady,
	})

	sections := []Section{
		{Title: "System Purpose", Content: "This system does X.", Summary: "Does X.", Confidence: ConfidenceHigh},
		{Title: "Architecture", Content: "Layered architecture.", Summary: "Layers.", Confidence: ConfidenceMedium, Inferred: true},
	}
	err := s.StoreKnowledgeSections(stored.ID, sections)
	if err != nil {
		t.Fatalf("StoreKnowledgeSections: %v", err)
	}

	fetchedSections := s.GetKnowledgeSections(stored.ID)
	if len(fetchedSections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(fetchedSections))
	}
	if fetchedSections[0].Title != "System Purpose" {
		t.Fatalf("expected first section 'System Purpose', got %q", fetchedSections[0].Title)
	}

	evidence := []Evidence{
		{SourceType: EvidenceFile, SourceID: "file-1", FilePath: "main.go", LineStart: 1, LineEnd: 10, Rationale: "Entry point"},
		{SourceType: EvidenceSymbol, SourceID: "sym-1", FilePath: "main.go", LineStart: 5, LineEnd: 8, Metadata: map[string]string{"kind": "function"}},
	}
	err = s.StoreKnowledgeEvidence(fetchedSections[0].ID, evidence)
	if err != nil {
		t.Fatalf("StoreKnowledgeEvidence: %v", err)
	}

	fetchedEvidence := s.GetKnowledgeEvidence(fetchedSections[0].ID)
	if len(fetchedEvidence) != 2 {
		t.Fatalf("expected 2 evidence, got %d", len(fetchedEvidence))
	}
	if fetchedEvidence[1].Metadata["kind"] != "function" {
		t.Fatalf("expected metadata kind=function, got %v", fetchedEvidence[1].Metadata)
	}

	full := s.GetKnowledgeArtifact(stored.ID)
	if len(full.Sections) != 2 {
		t.Fatalf("expected 2 nested sections, got %d", len(full.Sections))
	}
	if len(full.Sections[0].Evidence) != 2 {
		t.Fatalf("expected 2 nested evidence on first section, got %d", len(full.Sections[0].Evidence))
	}
}

func TestKnowledgeArtifactNotFound(t *testing.T) {
	s := NewMemStore()

	if s.GetKnowledgeArtifact("nonexistent") != nil {
		t.Fatal("expected nil for nonexistent artifact")
	}
	if err := s.UpdateKnowledgeArtifactStatus("nonexistent", StatusReady); err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
	if err := s.MarkKnowledgeArtifactStale("nonexistent", true); err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
	if err := s.DeleteKnowledgeArtifact("nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
}

func TestDeleteArtifactCleansUpSectionsAndEvidence(t *testing.T) {
	s := NewMemStore()

	artifact, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactLearningPath,
		Audience:     AudienceDeveloper,
		Depth:        DepthDeep,
		Status:       StatusReady,
	})

	_ = s.StoreKnowledgeSections(artifact.ID, []Section{{Title: "Step 1", Content: "Do this.", Confidence: ConfidenceHigh}})
	fetchedSections := s.GetKnowledgeSections(artifact.ID)
	_ = s.StoreKnowledgeEvidence(fetchedSections[0].ID, []Evidence{
		{SourceType: EvidenceFile, SourceID: "f1", FilePath: "a.go"},
	})

	_ = s.DeleteKnowledgeArtifact(artifact.ID)

	if len(s.GetKnowledgeSections(artifact.ID)) != 0 {
		t.Fatal("expected sections to be cleaned up")
	}
	if len(s.GetKnowledgeEvidence(fetchedSections[0].ID)) != 0 {
		t.Fatal("expected evidence to be cleaned up")
	}
}

func TestSupersedeArtifactRegeneratesEvidenceIDs(t *testing.T) {
	s := NewMemStore()

	artifact, _ := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthDeep,
		Status:       StatusReady,
	})

	sections := []Section{{
		Title:   "Architecture",
		Content: "v1",
		Evidence: []Evidence{{
			ID:         "fixed-ev-id",
			SourceType: EvidenceFile,
			SourceID:   "file-1",
			FilePath:   "main.go",
			LineStart:  1,
			LineEnd:    4,
		}},
	}}
	if err := s.SupersedeArtifact(artifact.ID, sections); err != nil {
		t.Fatalf("SupersedeArtifact initial: %v", err)
	}

	firstSections := s.GetKnowledgeSections(artifact.ID)
	if len(firstSections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(firstSections))
	}
	firstEvidence := s.GetKnowledgeEvidence(firstSections[0].ID)
	if len(firstEvidence) != 1 {
		t.Fatalf("expected 1 evidence row, got %d", len(firstEvidence))
	}
	if firstEvidence[0].ID == "fixed-ev-id" {
		t.Fatal("expected persisted evidence id to be regenerated")
	}

	if err := s.SupersedeArtifact(artifact.ID, sections); err != nil {
		t.Fatalf("SupersedeArtifact repeat: %v", err)
	}

	secondSections := s.GetKnowledgeSections(artifact.ID)
	if len(secondSections) != 1 {
		t.Fatalf("expected 1 section after repeat, got %d", len(secondSections))
	}
	secondEvidence := s.GetKnowledgeEvidence(secondSections[0].ID)
	if len(secondEvidence) != 1 {
		t.Fatalf("expected 1 evidence row after repeat, got %d", len(secondEvidence))
	}
	if secondEvidence[0].ID == "fixed-ev-id" {
		t.Fatal("expected repeated supersede to regenerate evidence ids")
	}
}

func TestSetArtifactFailedPersistsErrorMetadata(t *testing.T) {
	s := NewMemStore()

	stored, err := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactWorkflowStory,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Status:       StatusGenerating,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}

	if err := s.SetArtifactFailed(stored.ID, "LLM_EMPTY", "provider returned no content"); err != nil {
		t.Fatalf("SetArtifactFailed: %v", err)
	}

	fetched := s.GetKnowledgeArtifact(stored.ID)
	if fetched.Status != StatusFailed {
		t.Fatalf("expected status failed, got %s", fetched.Status)
	}
	if fetched.ErrorCode != "LLM_EMPTY" {
		t.Fatalf("expected error code LLM_EMPTY, got %q", fetched.ErrorCode)
	}
	if fetched.ErrorMessage != "provider returned no content" {
		t.Fatalf("expected persisted error message, got %q", fetched.ErrorMessage)
	}
}

func TestUpdateKnowledgeArtifactStatusClearsErrorMetadataOnRecovery(t *testing.T) {
	s := NewMemStore()

	stored, err := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCodeTour,
		Audience:     AudienceDeveloper,
		Depth:        DepthDeep,
		Status:       StatusGenerating,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}

	if err := s.SetArtifactFailed(stored.ID, "WORKER_UNAVAILABLE", "dial tcp timeout"); err != nil {
		t.Fatalf("SetArtifactFailed: %v", err)
	}
	if err := s.UpdateKnowledgeArtifactStatus(stored.ID, StatusReady); err != nil {
		t.Fatalf("UpdateKnowledgeArtifactStatus: %v", err)
	}

	fetched := s.GetKnowledgeArtifact(stored.ID)
	if fetched.Status != StatusReady {
		t.Fatalf("expected status ready, got %s", fetched.Status)
	}
	if fetched.ErrorCode != "" {
		t.Fatalf("expected cleared error code, got %q", fetched.ErrorCode)
	}
	if fetched.ErrorMessage != "" {
		t.Fatalf("expected cleared error message, got %q", fetched.ErrorMessage)
	}
	if fetched.Progress != 1.0 {
		t.Fatalf("expected ready artifact progress to be 1.0, got %f", fetched.Progress)
	}
	if fetched.GeneratedAt.IsZero() {
		t.Fatal("expected generated_at to be set on recovery")
	}
}

func TestArtifactsCanCoexistAcrossGenerationModes(t *testing.T) {
	s := NewMemStore()
	key := ArtifactKey{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthMedium,
		Scope:        ArtifactScope{ScopeType: ScopeRepository},
	}

	classic, created, err := s.ClaimArtifactWithMode(key, SourceRevision{CommitSHA: "a"}, GenerationModeClassic)
	if err != nil {
		t.Fatalf("ClaimArtifactWithMode classic: %v", err)
	}
	if !created {
		t.Fatal("expected classic artifact to be created")
	}

	understanding, created, err := s.ClaimArtifactWithMode(key, SourceRevision{CommitSHA: "a"}, GenerationModeUnderstandingFirst)
	if err != nil {
		t.Fatalf("ClaimArtifactWithMode understanding_first: %v", err)
	}
	if !created {
		t.Fatal("expected understanding-first artifact to be created")
	}
	if classic.ID == understanding.ID {
		t.Fatal("expected distinct artifacts per generation mode")
	}

	if got := s.GetArtifactByKeyAndMode(key, GenerationModeClassic); got == nil || got.ID != classic.ID {
		t.Fatalf("expected classic lookup to return %q, got %#v", classic.ID, got)
	}
	if got := s.GetArtifactByKeyAndMode(key, GenerationModeUnderstandingFirst); got == nil || got.ID != understanding.ID {
		t.Fatalf("expected understanding lookup to return %q, got %#v", understanding.ID, got)
	}
}

func TestRepositoryUnderstandingLifecycle(t *testing.T) {
	s := NewMemStore()

	u, err := s.StoreRepositoryUnderstanding(&RepositoryUnderstanding{
		RepositoryID: "repo-1",
		Scope:        (&ArtifactScope{ScopeType: ScopeRepository}).NormalizePtr(),
		RevisionFP:   "rev-1",
		Stage:        UnderstandingBuildingTree,
		TreeStatus:   UnderstandingTreePartial,
		CachedNodes:  8,
		TotalNodes:   20,
		Strategy:     "hierarchical",
		ModelUsed:    "qwen3:14b",
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding: %v", err)
	}
	if u.ID == "" {
		t.Fatal("expected repository understanding ID")
	}

	updated, err := s.StoreRepositoryUnderstanding(&RepositoryUnderstanding{
		RepositoryID: "repo-1",
		Scope:        (&ArtifactScope{ScopeType: ScopeRepository}).NormalizePtr(),
		RevisionFP:   "rev-2",
		Stage:        UnderstandingReady,
		TreeStatus:   UnderstandingTreeComplete,
		CachedNodes:  20,
		TotalNodes:   20,
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding update: %v", err)
	}
	if updated.ID != u.ID {
		t.Fatalf("expected update to preserve understanding ID, got %q vs %q", updated.ID, u.ID)
	}
	if updated.RevisionFP != "rev-2" {
		t.Fatalf("expected revision rev-2, got %q", updated.RevisionFP)
	}
	if updated.TreeStatus != UnderstandingTreeComplete {
		t.Fatalf("expected complete tree status, got %q", updated.TreeStatus)
	}

	artifact, err := s.StoreKnowledgeArtifact(&Artifact{
		RepositoryID: "repo-1",
		Type:         ArtifactCliffNotes,
		Audience:     AudienceDeveloper,
		Depth:        DepthDeep,
		Status:       StatusGenerating,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	if err := s.AttachArtifactUnderstanding(artifact.ID, updated.ID, updated.RevisionFP); err != nil {
		t.Fatalf("AttachArtifactUnderstanding: %v", err)
	}

	linked := s.GetKnowledgeArtifact(artifact.ID)
	if linked.UnderstandingID != updated.ID {
		t.Fatalf("expected linked understanding %q, got %q", updated.ID, linked.UnderstandingID)
	}
	if linked.UnderstandingRevisionFP != "rev-2" {
		t.Fatalf("expected linked revision rev-2, got %q", linked.UnderstandingRevisionFP)
	}
	if !ArtifactRefreshAvailable(linked, &RepositoryUnderstanding{RevisionFP: "rev-3"}) {
		t.Fatal("expected refresh to be available when understanding revision advances")
	}
}

// TestStoreRepositoryUnderstandingZerosProgressOnTerminalStage pins down the
// invariant that progress / phase / message fields are only meaningful while
// the build is actively running. Regression guard: a stale "synthesising
// root" / progress=0.78 row paired with stage=FAILED kept the UI showing
// "Building…" indefinitely (April 2026).
func TestStoreRepositoryUnderstandingZerosProgressOnTerminalStage(t *testing.T) {
	t.Parallel()
	s := NewMemStore()
	scope := (&ArtifactScope{ScopeType: ScopeRepository}).NormalizePtr()

	running, err := s.StoreRepositoryUnderstanding(&RepositoryUnderstanding{
		RepositoryID:    "repo-progress",
		Scope:           scope,
		Stage:           UnderstandingBuildingTree,
		TreeStatus:      UnderstandingTreePartial,
		Progress:        0.78,
		ProgressPhase:   "generating",
		ProgressMessage: "synthesising root",
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding(running): %v", err)
	}
	if running.Progress != 0.78 || running.ProgressPhase != "generating" || running.ProgressMessage != "synthesising root" {
		t.Fatalf("running stage should preserve progress fields, got %+v", running)
	}

	terminalStages := []RepositoryUnderstandingStage{
		UnderstandingFailed,
		UnderstandingReady,
		UnderstandingFirstPassReady,
		UnderstandingNeedsRefresh,
	}
	for _, stage := range terminalStages {
		stage := stage
		t.Run(string(stage), func(t *testing.T) {
			out, err := s.StoreRepositoryUnderstanding(&RepositoryUnderstanding{
				RepositoryID:    "repo-progress",
				Scope:           scope,
				Stage:           stage,
				TreeStatus:      UnderstandingTreePartial,
				Progress:        0.78,
				ProgressPhase:   "generating",
				ProgressMessage: "synthesising root",
				ErrorMessage:    "rpc unavailable",
			})
			if err != nil {
				t.Fatalf("StoreRepositoryUnderstanding(%s): %v", stage, err)
			}
			if out.Progress != 0 || out.ProgressPhase != "" || out.ProgressMessage != "" {
				t.Fatalf("stage %s must zero progress fields, got progress=%v phase=%q message=%q",
					stage, out.Progress, out.ProgressPhase, out.ProgressMessage)
			}
			// Late heartbeat must not re-stamp progress on a terminal row.
			if err := s.UpdateRepositoryUnderstandingProgress(out.ID, 0.5, "queued", "rebuilding"); err != nil {
				t.Fatalf("UpdateRepositoryUnderstandingProgress: %v", err)
			}
			again := s.GetRepositoryUnderstanding("repo-progress", *scope)
			if again.Progress != 0 || again.ProgressPhase != "" || again.ProgressMessage != "" {
				t.Fatalf("late heartbeat re-stamped progress on stage %s: %+v", stage, again)
			}
		})
	}
}

// CA-122 codex r2 M1: terminal artifact rows must reject late progress
// writes from the streaming progress driver. Without the guard, a
// stalled stream-driver flush after SupersedeArtifact could resurrect
// the artifact's progress / phase / message and confuse the UI.
func TestUpdateKnowledgeArtifactProgressIgnoresTerminalArtifacts(t *testing.T) {
	for _, status := range []ArtifactStatus{StatusReady, StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			s := NewMemStore()
			stored, err := s.StoreKnowledgeArtifact(&Artifact{
				RepositoryID: "repo-1",
				Type:         ArtifactCliffNotes,
				Audience:     AudienceDeveloper,
				Depth:        DepthMedium,
				Status:       StatusGenerating,
				Progress:     0.5,
			})
			if err != nil {
				t.Fatalf("StoreKnowledgeArtifact: %v", err)
			}
			// Mid-flight progress write should land while still
			// generating.
			if err := s.UpdateKnowledgeArtifactProgressWithPhase(stored.ID, 0.6, "render", "rendering"); err != nil {
				t.Fatalf("mid-flight progress: %v", err)
			}
			midFlight := s.GetKnowledgeArtifact(stored.ID)
			if midFlight.Progress != 0.6 || midFlight.ProgressPhase != "render" || midFlight.ProgressMessage != "rendering" {
				t.Fatalf("expected mid-flight progress to land: %+v", midFlight)
			}
			// Now flip to terminal and try a late write.
			if err := s.UpdateKnowledgeArtifactStatus(stored.ID, status); err != nil {
				t.Fatalf("UpdateKnowledgeArtifactStatus: %v", err)
			}
			terminal := s.GetKnowledgeArtifact(stored.ID)
			beforeLate := *terminal
			if err := s.UpdateKnowledgeArtifactProgressWithPhase(stored.ID, 0.42, "queued", "Late stream-driver flush"); err != nil {
				t.Fatalf("late progress should be a silent no-op, got: %v", err)
			}
			after := s.GetKnowledgeArtifact(stored.ID)
			if after.Progress != beforeLate.Progress || after.ProgressPhase != beforeLate.ProgressPhase || after.ProgressMessage != beforeLate.ProgressMessage {
				t.Fatalf("late progress re-stamped terminal artifact (status %s):\nbefore: %+v\nafter:  %+v", status, beforeLate, *after)
			}
		})
	}
}
