// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator_test

// retry_resume_test.go — locks the CA-145+CA-143 contract:
//
//   "OnPageDone fires after persistence, so the set of page IDs that
//    fired OnPageDone equals the set of page IDs that are durably stored.
//    On interruption + retry, smart-resume sees exactly the pages
//    OnPageDone reported."
//
// These tests use rejectAfterNStore (defined in coldstart_resilience_test.go,
// same package) rather than duplicating the fixture. The store-error is
// injected AFTER all goroutines complete generation (generation uses
// erroringTemplate with successResp set so every page succeeds); only the
// post-Wait persistence-loop write fails. This exercises the new
// OnPageDone callsite (orchestrator.go: post-Wait loop) rather than the
// PartialGenerationError path.
//
// Determinism guarantee: the post-Wait persistence loop iterates
// generated pages in the same order as req.Pages (outcomes[idx] captured
// inside the per-page goroutine, then reconstructed in req.Pages order at
// lines ~712-723 of orchestrator.go). With MaxConcurrency=1, completion
// order equals page order, ensuring the first K pages in req.Pages are
// the ones durably stored before the K+1 write fails.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// TestRetryResume_ProgressMatchesPersistedSet_PR — PR mode.
//
// N=8 pages all succeed in generation. The store rejects SetProposed on call
// K+1 (K=5), simulating a mid-persistence-loop interrupt. After Generate
// returns with a non-nil error:
//
//   - result.Generated must have exactly K pages (durably stored).
//   - OnPageDone must have fired exactly K times (one per durably stored page).
//   - The K page IDs from OnPageDone must equal the K page IDs in result.Generated.
func TestRetryResume_ProgressMatchesPersistedSet_PR(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const N, K = 8, 5

	pages := makeGlossaryPages(N, "rr-pr")
	tmpl := &erroringTemplate{
		id:          "glossary",
		successResp: glossaryPassMarkdown, // always succeeds; only the store write fails
	}

	reg := orchestrator.NewMapRegistry(tmpl)
	inner := orchestrator.NewMemoryPageStore()
	// rejectAfterN=K: accept calls 1..K, reject call K+1 and beyond.
	// CRITICAL (bob M1): this is a STORE-WRITE error, not a generation error.
	// Generation completes for all N pages before the persistence loop begins.
	// The injected error fires only during the post-Wait persistence loop when
	// the orchestrator calls SetProposed for the (K+1)-th page.
	store := &rejectAfterNStore{
		inner:        inner,
		rejectAfterN: K,
	}
	pr := orchestrator.NewMemoryWikiPR("pr-rr")

	var mu sync.Mutex
	var doneIDs []string

	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "test",
		MaxConcurrency: 1, // serialize so first K pages are always the same K (deterministic)
	}, reg, store)

	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
		OnPageDone: func(id string, excluded bool, _ string) {
			mu.Lock()
			defer mu.Unlock()
			doneIDs = append(doneIDs, id)
		},
	})

	if err == nil {
		t.Fatal("expected error from rejected SetProposed; got nil")
	}

	if got := len(result.Generated); got != K {
		t.Errorf("result.Generated count: got %d, want %d (durably persisted before rejection)", got, K)
	}
	if got := len(doneIDs); got != K {
		t.Errorf("OnPageDone fire count: got %d, want %d", got, K)
	}

	// OnPageDone IDs must exactly match the durably persisted IDs.
	persistedIDs := make([]string, len(result.Generated))
	for i, p := range result.Generated {
		persistedIDs[i] = p.ID
	}
	if !sameElements(persistedIDs, doneIDs) {
		t.Errorf("OnPageDone IDs %v != persisted IDs %v", doneIDs, persistedIDs)
	}
}

// TestRetryResume_ProgressMatchesPersistedSet_DirectPublish — DirectPublish mode.
//
// Same shape as the PR-mode test but exercises the DirectPublish persistence
// branch (store.SetCanonical). Without explicit coverage, a future change that
// diverges the two branches' OnPageDone callsites would not be caught. (dexter F4)
func TestRetryResume_ProgressMatchesPersistedSet_DirectPublish(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const N, K = 8, 5

	pages := makeGlossaryPages(N, "rr-dp")
	tmpl := &erroringTemplate{
		id:          "glossary",
		successResp: glossaryPassMarkdown,
	}

	reg := orchestrator.NewMapRegistry(tmpl)
	inner := orchestrator.NewMemoryPageStore()
	store := &rejectAfterNStore{
		inner:        inner,
		rejectAfterN: K,
	}

	dir := t.TempDir()
	writer := orchestrator.NewFilesystemRepoWriter(dir)

	var mu sync.Mutex
	var doneIDs []string

	orch := orchestrator.New(orchestrator.Config{
		RepoID:         "test-dp",
		MaxConcurrency: 1,
		DirectPublish:  true,
	}, reg, store)

	result, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Config:  orchestrator.Config{DirectPublish: true},
		Pages:   pages,
		Writer:  writer,
		LLMTier: modeltier.TierFrontier,
		OnPageDone: func(id string, excluded bool, _ string) {
			mu.Lock()
			defer mu.Unlock()
			doneIDs = append(doneIDs, id)
		},
	})

	if err == nil {
		t.Fatal("expected error from rejected SetCanonical; got nil")
	}

	if got := len(result.Generated); got != K {
		t.Errorf("result.Generated count: got %d, want %d", got, K)
	}
	if got := len(doneIDs); got != K {
		t.Errorf("OnPageDone fire count: got %d, want %d", got, K)
	}

	persistedIDs := make([]string, len(result.Generated))
	for i, p := range result.Generated {
		persistedIDs[i] = p.ID
	}
	if !sameElements(persistedIDs, doneIDs) {
		t.Errorf("OnPageDone IDs %v != persisted IDs %v", doneIDs, persistedIDs)
	}
}

// TestRetryResume_NoProgressOnHardError — hard-error path.
//
// A non-PartialGenerationError (template not found) bypasses the persistence
// loop entirely (shouldPersist=false). OnPageDone must fire zero times.
// On retry, smart-resume would find zero durably stored pages and re-run all.
// (Locks Decision D3 from the CA-145+CA-143 plan.)
func TestRetryResume_NoProgressOnHardError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// One page referencing a template not in the registry — triggers a fatal
	// (non-IsPartialGenerationError) error so shouldPersist=false.
	pages := []orchestrator.PlannedPage{
		{
			ID:         "rr-hard.nonexistent",
			TemplateID: "nonexistent", // not registered — fatal, not soft-fail
			Audience:   quality.AudienceEngineers,
			Input: templates.GenerateInput{
				RepoID:   "rr-hard-0",
				Audience: quality.AudienceEngineers,
				Now:      time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC),
			},
		},
	}

	// Register "glossary" so the registry is non-empty, but the planned page
	// requests "nonexistent" — still fatal.
	tmpl := &erroringTemplate{id: "glossary", successResp: glossaryPassMarkdown}
	reg := orchestrator.NewMapRegistry(tmpl)
	store := orchestrator.NewMemoryPageStore()
	pr := orchestrator.NewMemoryWikiPR("pr-rr-hard")

	var doneCount int

	orch := orchestrator.New(orchestrator.Config{RepoID: "test"}, reg, store)
	_, err := orch.Generate(ctx, orchestrator.GenerateRequest{
		Pages:   pages,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
		OnPageDone: func(_ string, _ bool, _ string) {
			doneCount++
		},
	})

	if err == nil {
		t.Fatal("expected hard error for unknown template; got nil")
	}
	if orchestrator.IsPartialGenerationError(err) {
		t.Errorf("expected non-partial error; got IsPartialGenerationError=true: %v", err)
	}
	if doneCount != 0 {
		t.Errorf("OnPageDone fire count: got %d, want 0 (hard-error path never fires OnPageDone)", doneCount)
	}
}

// sameElements returns true when a and b contain the same strings (order-independent).
func sameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}
