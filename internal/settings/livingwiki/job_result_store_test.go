// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package livingwiki_test

import (
	"context"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// exerciseJobResultStore runs the [livingwiki.JobResultStore] contract against
// any implementation. Tests can call this with a MemJobResultStore or any other
// implementation.
func exerciseJobResultStore(t *testing.T, store livingwiki.JobResultStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("GetByJobID_not_found_returns_nil", func(t *testing.T) {
		result, err := store.GetByJobID(ctx, "does-not-exist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil for missing job ID, got %+v", result)
		}
	})

	t.Run("LastResultForRepo_no_results_returns_nil", func(t *testing.T) {
		result, err := store.LastResultForRepo(ctx, "default", "repo-empty")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil for repo with no results, got %+v", result)
		}
	})

	now := time.Now().Truncate(time.Second) // truncate for DB round-trip parity

	t.Run("Save_and_GetByJobID_round_trip", func(t *testing.T) {
		completed := now.Add(2 * time.Second)
		original := &livingwiki.LivingWikiJobResult{
			RepoID:              "repo-rt",
			JobID:               "job-001",
			StartedAt:           now,
			CompletedAt:         &completed,
			PagesPlanned:        10,
			PagesGenerated:      8,
			PagesExcluded:       2,
			ExcludedPageIDs:     []string{"page-a", "page-b"},
			GeneratedPageTitles: []string{"Overview", "API Reference"},
			ExclusionReasons:    []string{"content_gate", "length"},
			Status:              "ok",
			ErrorMessage:        "",
		}

		if err := store.Save(ctx, "default", original); err != nil {
			t.Fatalf("Save: %v", err)
		}

		got, err := store.GetByJobID(ctx, "job-001")
		if err != nil {
			t.Fatalf("GetByJobID: %v", err)
		}
		if got == nil {
			t.Fatal("GetByJobID returned nil for saved job")
		}
		if got.JobID != original.JobID {
			t.Errorf("JobID: got %q, want %q", got.JobID, original.JobID)
		}
		if got.PagesGenerated != original.PagesGenerated {
			t.Errorf("PagesGenerated: got %d, want %d", got.PagesGenerated, original.PagesGenerated)
		}
		if got.Status != original.Status {
			t.Errorf("Status: got %q, want %q", got.Status, original.Status)
		}
		if len(got.ExcludedPageIDs) != len(original.ExcludedPageIDs) {
			t.Errorf("ExcludedPageIDs len: got %d, want %d", len(got.ExcludedPageIDs), len(original.ExcludedPageIDs))
		}
		if len(got.GeneratedPageTitles) != len(original.GeneratedPageTitles) {
			t.Errorf("GeneratedPageTitles len: got %d, want %d", len(got.GeneratedPageTitles), len(original.GeneratedPageTitles))
		}
	})

	t.Run("LastResultForRepo_returns_most_recent", func(t *testing.T) {
		// Save two results for the same repo; verify the most recent is returned.
		older := &livingwiki.LivingWikiJobResult{
			RepoID:    "repo-last",
			JobID:     "job-last-001",
			StartedAt: now.Add(-10 * time.Minute),
			Status:    "ok",
		}
		newer := &livingwiki.LivingWikiJobResult{
			RepoID:    "repo-last",
			JobID:     "job-last-002",
			StartedAt: now.Add(-1 * time.Minute),
			Status:    "partial",
		}

		if err := store.Save(ctx, "default", older); err != nil {
			t.Fatalf("Save older: %v", err)
		}
		if err := store.Save(ctx, "default", newer); err != nil {
			t.Fatalf("Save newer: %v", err)
		}

		got, err := store.LastResultForRepo(ctx, "default", "repo-last")
		if err != nil {
			t.Fatalf("LastResultForRepo: %v", err)
		}
		if got == nil {
			t.Fatal("LastResultForRepo returned nil")
		}
		if got.JobID != newer.JobID {
			t.Errorf("expected most recent JobID %q, got %q", newer.JobID, got.JobID)
		}
		if got.Status != newer.Status {
			t.Errorf("expected most recent Status %q, got %q", newer.Status, got.Status)
		}
	})

	t.Run("LastResultForRepo_ignores_other_repos", func(t *testing.T) {
		result := &livingwiki.LivingWikiJobResult{
			RepoID:    "repo-other",
			JobID:     "job-other-001",
			StartedAt: now,
			Status:    "ok",
		}
		if err := store.Save(ctx, "default", result); err != nil {
			t.Fatalf("Save: %v", err)
		}

		got, err := store.LastResultForRepo(ctx, "default", "repo-not-this-one")
		if err != nil {
			t.Fatalf("LastResultForRepo: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil for different repo, got %+v", got)
		}
	})

	t.Run("ErrorMessage_preserved", func(t *testing.T) {
		result := &livingwiki.LivingWikiJobResult{
			RepoID:       "repo-err",
			JobID:        "job-err-001",
			StartedAt:    now,
			Status:       "failed",
			ErrorMessage: "LLM context window exceeded",
		}
		if err := store.Save(ctx, "default", result); err != nil {
			t.Fatalf("Save: %v", err)
		}

		got, err := store.GetByJobID(ctx, "job-err-001")
		if err != nil {
			t.Fatalf("GetByJobID: %v", err)
		}
		if got == nil {
			t.Fatal("GetByJobID returned nil")
		}
		if got.ErrorMessage != result.ErrorMessage {
			t.Errorf("ErrorMessage: got %q, want %q", got.ErrorMessage, result.ErrorMessage)
		}
	})
}

// TestMemJobResultStore exercises the in-memory implementation against the
// full contract so any future SurrealDB implementation can be validated
// against the same tests.
func TestMemJobResultStore(t *testing.T) {
	exerciseJobResultStore(t, livingwiki.NewMemJobResultStore())
}
