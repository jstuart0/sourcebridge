// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package sinks_test

import (
	"context"
	"sort"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
)

// TestRunOrphanCleanup_DeletesOrphan seeds a MemoryConfluenceClient with three
// pages (two in the current set, one orphan) and asserts only the orphan is
// deleted after RunOrphanCleanup runs.
func TestRunOrphanCleanup_DeletesOrphan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoID := "acme-repo"

	client := markdown.NewMemoryConfluenceClient()

	// Seed pages.
	pageInCurrent1 := repoID + ".arch.src.api"
	pageInCurrent2 := repoID + ".arch.src.db"
	orphanPage := repoID + ".arch.src.db.queries" // not in current taxonomy

	for _, extID := range []string{pageInCurrent1, pageInCurrent2, orphanPage} {
		if err := client.UpsertPage(ctx, extID, []byte("<p>content</p>"), markdown.ConfluenceProperties{
			"sourcebridge_page_id": extID,
		}); err != nil {
			t.Fatalf("seed UpsertPage(%q): %v", extID, err)
		}
	}

	// Build a ConfluenceSinkWriter backed by the in-memory client.
	writer := sinks.NewConfluenceSinkWriterFromClient(client, markdown.ConfluenceWriterConfig{})

	currentIDs := []string{pageInCurrent1, pageInCurrent2}
	result := sinks.RunOrphanCleanup(ctx, writer, repoID, currentIDs)

	if result.Deleted != 1 {
		t.Errorf("expected 1 deletion, got %d", result.Deleted)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != orphanPage {
		t.Errorf("expected deleted ID %q, got %v", orphanPage, result.DeletedIDs)
	}

	// Current pages must still exist.
	for _, extID := range currentIDs {
		xhtml, _, err := client.GetPage(ctx, extID)
		if err != nil {
			t.Fatalf("GetPage(%q): %v", extID, err)
		}
		if xhtml == nil {
			t.Errorf("expected page %q to still exist after orphan cleanup", extID)
		}
	}

	// Orphan must be gone.
	xhtml, _, err := client.GetPage(ctx, orphanPage)
	if err != nil {
		t.Fatalf("GetPage(orphan): %v", err)
	}
	if xhtml != nil {
		t.Errorf("expected orphan page %q to be deleted, but it still exists", orphanPage)
	}
}

// TestRunOrphanCleanup_NoOrphans verifies that when all listed pages are in the
// current set, nothing is deleted.
func TestRunOrphanCleanup_NoOrphans(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoID := "clean-repo"

	client := markdown.NewMemoryConfluenceClient()
	extID := repoID + ".arch.src.api"
	if err := client.UpsertPage(ctx, extID, []byte("<p>x</p>"), markdown.ConfluenceProperties{
		"sourcebridge_page_id": extID,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	writer := sinks.NewConfluenceSinkWriterFromClient(client, markdown.ConfluenceWriterConfig{})
	result := sinks.RunOrphanCleanup(ctx, writer, repoID, []string{extID})

	if result.Deleted != 0 {
		t.Errorf("expected 0 deletions, got %d", result.Deleted)
	}
}

// TestRunOrphanCleanup_PrefixIsolation verifies that pages belonging to other
// repos are not touched even if they share a space.
func TestRunOrphanCleanup_PrefixIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoA := "repo-a"
	repoB := "repo-b"

	client := markdown.NewMemoryConfluenceClient()

	// Seed one page for each repo.
	pageA := repoA + ".arch.src.api"
	pageB := repoB + ".arch.src.api"
	for _, extID := range []string{pageA, pageB} {
		if err := client.UpsertPage(ctx, extID, []byte("<p>x</p>"), markdown.ConfluenceProperties{
			"sourcebridge_page_id": extID,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Run cleanup for repo-a with no current pages (should delete only pageA).
	writer := sinks.NewConfluenceSinkWriterFromClient(client, markdown.ConfluenceWriterConfig{})
	result := sinks.RunOrphanCleanup(ctx, writer, repoA, nil)

	if result.Deleted != 1 {
		t.Errorf("expected 1 deletion for repo-a, got %d", result.Deleted)
	}

	// repo-b's page must be untouched.
	xhtml, _, err := client.GetPage(ctx, pageB)
	if err != nil {
		t.Fatalf("GetPage(%q): %v", pageB, err)
	}
	if xhtml == nil {
		t.Errorf("repo-b page %q should not have been deleted", pageB)
	}
}

// TestRunOrphanCleanup_NonCleanerSink verifies that sinks that do not implement
// OrphanCleaner are skipped gracefully.
func TestRunOrphanCleanup_NonCleanerSink(t *testing.T) {
	t.Parallel()

	// countingSinkWriter (defined in dispatch_test.go) does not implement OrphanCleaner.
	writer := newCountingWriter(markdown.SinkKindNotion)
	result := sinks.RunOrphanCleanup(context.Background(), writer, "some-repo", nil)

	if result.Deleted != 0 {
		t.Errorf("expected 0 deletions for non-cleaner sink, got %d", result.Deleted)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors for non-cleaner sink, got %v", result.Errors)
	}
}

// TestRunOrphanCleanup_MultipleOrphans verifies that multiple orphan pages are
// all deleted.
func TestRunOrphanCleanup_MultipleOrphans(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoID := "big-repo"
	client := markdown.NewMemoryConfluenceClient()

	current := []string{repoID + ".arch.src.api"}
	orphans := []string{
		repoID + ".arch.src.db.queries",
		repoID + ".arch.src.db.migrations",
		repoID + ".arch.src.cache",
	}

	for _, extID := range append(current, orphans...) {
		if err := client.UpsertPage(ctx, extID, []byte("<p>x</p>"), markdown.ConfluenceProperties{
			"sourcebridge_page_id": extID,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	writer := sinks.NewConfluenceSinkWriterFromClient(client, markdown.ConfluenceWriterConfig{})
	result := sinks.RunOrphanCleanup(ctx, writer, repoID, current)

	if result.Deleted != 3 {
		t.Errorf("expected 3 deletions, got %d", result.Deleted)
	}
	sort.Strings(result.DeletedIDs)
	sort.Strings(orphans)
	for i, id := range orphans {
		if result.DeletedIDs[i] != id {
			t.Errorf("deleted[%d] = %q, want %q", i, result.DeletedIDs[i], id)
		}
	}

	// Current page must still exist.
	for _, extID := range current {
		xhtml, _, err := client.GetPage(ctx, extID)
		if err != nil || xhtml == nil {
			t.Errorf("current page %q should still exist", extID)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 4a: RunOrphanCleanupForPrefix tests
// ─────────────────────────────────────────────────────────────────────────────

// TestRunOrphanCleanupForPrefixScopedDeletion verifies that only pages matching
// the given prefix are candidates for deletion — pages under sibling prefixes
// (e.g. detail.*) are untouched when the caller passes overview.* as the prefix.
func TestRunOrphanCleanupForPrefixScopedDeletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoID := "acme"
	client := markdown.NewMemoryConfluenceClient()

	// Seed one overview orphan, one detail page (not in current), and one repo-wide page.
	overviewOrphan := repoID + ".overview.auth_subsystem"
	detailPage := repoID + ".detail.internal.api"
	repoWidePage := repoID + ".system_overview"
	for _, extID := range []string{overviewOrphan, detailPage, repoWidePage} {
		if err := client.UpsertPage(ctx, extID, []byte("<p>x</p>"), markdown.ConfluenceProperties{
			"sourcebridge_page_id": extID,
		}); err != nil {
			t.Fatalf("seed UpsertPage(%q): %v", extID, err)
		}
	}

	writer := sinks.NewConfluenceSinkWriterFromClient(client, markdown.ConfluenceWriterConfig{})

	// Cleanup the overview prefix with an empty current set — should delete only overviewOrphan.
	result := sinks.RunOrphanCleanupForPrefix(ctx, writer, repoID, repoID+".overview.", nil)

	if result.Deleted != 1 {
		t.Errorf("expected 1 deletion, got %d (errors: %v)", result.Deleted, result.Errors)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != overviewOrphan {
		t.Errorf("expected deleted %q, got %v", overviewOrphan, result.DeletedIDs)
	}

	// detailPage and repoWidePage must be untouched.
	for _, extID := range []string{detailPage, repoWidePage} {
		xhtml, _, err := client.GetPage(ctx, extID)
		if err != nil {
			t.Fatalf("GetPage(%q): %v", extID, err)
		}
		if xhtml == nil {
			t.Errorf("page %q should not have been deleted (wrong prefix scope)", extID)
		}
	}
}

// TestRunOrphanCleanupForPrefixRejectsCrossRepoPrefix verifies that passing a
// prefix that does not start with repoID+"." returns an error and performs no deletes.
func TestRunOrphanCleanupForPrefixRejectsCrossRepoPrefix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoID := "my-repo"
	client := markdown.NewMemoryConfluenceClient()

	// Seed a page under the correct repo.
	if err := client.UpsertPage(ctx, repoID+".arch.api", []byte("<p>x</p>"), markdown.ConfluenceProperties{
		"sourcebridge_page_id": repoID + ".arch.api",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	writer := sinks.NewConfluenceSinkWriterFromClient(client, markdown.ConfluenceWriterConfig{})

	// Pass a prefix belonging to a different repo.
	result := sinks.RunOrphanCleanupForPrefix(ctx, writer, repoID, "other-repo.arch.", nil)

	if result.Deleted != 0 {
		t.Errorf("expected 0 deletions with cross-repo prefix, got %d", result.Deleted)
	}
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error for cross-repo prefix, got %v", result.Errors)
	}
}

// TestRunOrphanCleanupBackwardsCompatible verifies that the RunOrphanCleanup
// wrapper produces identical behavior to a direct RunOrphanCleanupForPrefix call
// with repoID+"." as the prefix.
func TestRunOrphanCleanupBackwardsCompatible(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoID := "compat-repo"
	client := markdown.NewMemoryConfluenceClient()

	keep := repoID + ".arch.src.api"
	orphan := repoID + ".arch.src.db"
	for _, extID := range []string{keep, orphan} {
		if err := client.UpsertPage(ctx, extID, []byte("<p>x</p>"), markdown.ConfluenceProperties{
			"sourcebridge_page_id": extID,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	writer := sinks.NewConfluenceSinkWriterFromClient(client, markdown.ConfluenceWriterConfig{})
	result := sinks.RunOrphanCleanup(ctx, writer, repoID, []string{keep})

	if result.Deleted != 1 || result.DeletedIDs[0] != orphan {
		t.Errorf("RunOrphanCleanup: expected to delete %q, got %v (errors: %v)", orphan, result.DeletedIDs, result.Errors)
	}
}

// Compile-time check: ConfluenceSinkWriter implements OrphanCleaner.
var _ sinks.OrphanCleaner = (*sinks.ConfluenceSinkWriter)(nil)

// Ensure the test uses the ast package (for page construction).
var _ = ast.Page{}
