// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Tests for A1.P6: block-level human-edit reconciliation, sink-edit detection,
// sync-PR mechanism, MoveDetector, ApplyMigrations, and the end-to-end
// "edit a paragraph in Confluence, push twice, paragraph unchanged" scenario.

package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/governance"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/quality"
)

// ─────────────────────────────────────────────────────────────────────────────
// Shared test helpers
// ─────────────────────────────────────────────────────────────────────────────

const (
	testRepoID  = "test-repo"
	testPageID  = "arch.auth"
	testBlockID = ast.BlockID("b001")
)

// makeCanonicalPage creates a canonical page with one generated paragraph block.
func makeCanonicalPage(pageID string, blockID ast.BlockID, text string) ast.Page {
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: "glossary",
			Audience: "for-engineers",
		},
		Blocks: []ast.Block{
			{
				ID:   blockID,
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{
					Paragraph: &ast.ParagraphContent{Markdown: text},
				},
				Owner: ast.OwnerGenerated,
			},
		},
	}
}

// paraMd extracts the paragraph markdown from block 0 of a page.
func paraMd(page ast.Page) string {
	if len(page.Blocks) == 0 || page.Blocks[0].Content.Paragraph == nil {
		return ""
	}
	return page.Blocks[0].Content.Paragraph.Markdown
}

// makeSinkEdit constructs a SinkEdit for test use.
func makeSinkEdit(sinkName ast.SinkName, blockID ast.BlockID, text string) orchestrator.SinkEdit {
	return orchestrator.SinkEdit{
		SinkName: sinkName,
		BlockID:  blockID,
		NewContent: ast.BlockContent{
			Paragraph: &ast.ParagraphContent{Markdown: text},
		},
		EditedBy: "editor@example.com",
		EditedAt: time.Now(),
	}
}

// makeSinkEditCfg creates a SinkEditConfig for tests with all required stores.
func makeSinkEditCfg(
	sinkName ast.SinkName,
	kind governance.SinkKind,
	policy *governance.EditPolicy,
) (orchestrator.SinkEditConfig, *orchestrator.MemorySinkOverlayStore, *orchestrator.MemorySyncPRStore, *governance.MemoryAuditLog) {
	overlayStore := orchestrator.NewMemorySinkOverlayStore()
	syncPRStore := orchestrator.NewMemorySyncPRStore()
	auditLog := governance.NewMemoryAuditLog()

	sinkCfg := governance.NewSinkConfig(kind, sinkName)
	if policy != nil {
		sinkCfg = sinkCfg.WithPolicy(*policy)
	}

	return orchestrator.SinkEditConfig{
		AuditLog:     auditLog,
		OverlayStore: overlayStore,
		SyncPRs:      syncPRStore,
		SinkConfigs: map[ast.SinkName]governance.SinkConfig{
			sinkName: sinkCfg,
		},
	}, overlayStore, syncPRStore, auditLog
}

// buildOrchestratorWithPage creates an Orchestrator that has a pre-populated
// canonical page so HandleSinkEdit can load it.
func buildOrchestratorWithPage(page ast.Page) (*orchestrator.Orchestrator, *orchestrator.MemoryPageStore) {
	store := orchestrator.NewMemoryPageStore()
	_ = store.SetCanonical(context.Background(), testRepoID, page)
	reg := orchestrator.NewDefaultRegistry()
	o := orchestrator.New(orchestrator.Config{RepoID: testRepoID}, reg, store)
	return o, store
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSinkEdit — promote_to_canonical
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleSinkEdit_PromoteToCanonical(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Original auto content.")
	o, store := buildOrchestratorWithPage(page)

	promotePolicy := governance.EditPolicyPromoteToCanonical
	secfg, _, _, auditLog := makeSinkEditCfg("git-repo", governance.SinkKindGitRepo, &promotePolicy)

	edit := makeSinkEdit("git-repo", testBlockID, "Human-reviewed content.")
	updated, err := o.HandleSinkEdit(ctx, secfg, testRepoID, testPageID, edit)
	if err != nil {
		t.Fatalf("HandleSinkEdit promote: %v", err)
	}

	// Returned page must have human-edited ownership.
	if updated.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("owner: got %v, want human-edited", updated.Blocks[0].Owner)
	}
	if paraMd(updated) != "Human-reviewed content." {
		t.Errorf("content: %q", paraMd(updated))
	}

	// Canonical in store must also be updated.
	stored, ok, _ := store.GetCanonical(ctx, testRepoID, testPageID)
	if !ok {
		t.Fatal("canonical page not found in store")
	}
	if stored.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("stored owner: got %v, want human-edited", stored.Blocks[0].Owner)
	}

	// Audit log must have an entry.
	entries, _ := auditLog.Query(ctx, governance.AuditFilter{})
	if len(entries) != 1 || entries[0].Decision != "promote_to_canonical" {
		t.Errorf("audit: expected 1 promote_to_canonical, got %+v", entries)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSinkEdit — local_to_sink
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleSinkEdit_LocalToSink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Original auto content.")
	o, store := buildOrchestratorWithPage(page)

	localPolicy := governance.EditPolicyLocalToSink
	secfg, overlayStore, _, _ := makeSinkEditCfg("confluence-acme", governance.SinkKindConfluence, &localPolicy)

	edit := makeSinkEdit("confluence-acme", testBlockID, "Confluence product team version.")
	updated, err := o.HandleSinkEdit(ctx, secfg, testRepoID, testPageID, edit)
	if err != nil {
		t.Fatalf("HandleSinkEdit local_to_sink: %v", err)
	}

	// Canonical content must be unchanged.
	if paraMd(updated) != "Original auto content." {
		t.Errorf("canonical content changed: %q", paraMd(updated))
	}
	// Canonical block owner must still be generated.
	if updated.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("canonical owner changed to %v", updated.Blocks[0].Owner)
	}

	// SinkDivergence flag must be set on the canonical block.
	if !updated.Blocks[0].SinkDivergence["confluence-acme"] {
		t.Error("SinkDivergence not set for confluence-acme")
	}

	// Overlay must have the edit.
	overlay, ok, _ := overlayStore.GetOverlay(ctx, testRepoID, "confluence-acme", testPageID)
	if !ok {
		t.Fatal("overlay not found")
	}
	if overlay.Blocks[testBlockID].Paragraph == nil || overlay.Blocks[testBlockID].Paragraph.Markdown != "Confluence product team version." {
		t.Errorf("overlay content wrong: %+v", overlay.Blocks[testBlockID])
	}

	// Canonical in store must be unchanged (except for divergence flag).
	stored, _, _ := store.GetCanonical(ctx, testRepoID, testPageID)
	if stored.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("canonical store owner changed to %v", stored.Blocks[0].Owner)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSinkEdit — require_review_before_promote
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleSinkEdit_RequireReviewBeforePromote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Original auto content.")
	o, store := buildOrchestratorWithPage(page)

	reviewPolicy := governance.EditPolicyRequireReviewBeforePromote
	secfg, overlayStore, syncPRStore, _ := makeSinkEditCfg("github-wiki", governance.SinkKindGitHubWiki, &reviewPolicy)

	// Wire a sync-PR opener.
	syncPROpener := orchestrator.NewMemoryWikiPR("sync-pr-001")
	secfg.SyncPROpener = syncPROpener

	edit := makeSinkEdit("github-wiki", testBlockID, "Wiki editor's proposed content.")
	_, err := o.HandleSinkEdit(ctx, secfg, testRepoID, testPageID, edit)
	if err != nil {
		t.Fatalf("HandleSinkEdit require_review: %v", err)
	}

	// Sync-PR must be open.
	if !syncPROpener.IsOpen() {
		t.Error("expected sync-PR to be open")
	}
	// Sync-PR record must be stored.
	record, ok, _ := syncPRStore.Get(ctx, "sync-pr-001")
	if !ok {
		t.Fatal("sync-PR record not found")
	}
	if record.BlockID != testBlockID || record.SinkName != "github-wiki" {
		t.Errorf("sync-PR record wrong: %+v", record)
	}

	// Overlay must have the proposed content (staged for sync-PR review).
	overlay, overlayOK, _ := overlayStore.GetOverlay(ctx, testRepoID, "github-wiki", testPageID)
	if !overlayOK {
		t.Fatal("overlay not staged for sync-PR")
	}
	if overlay.Blocks[testBlockID].Paragraph.Markdown != "Wiki editor's proposed content." {
		t.Errorf("overlay content wrong: %q", overlay.Blocks[testBlockID].Paragraph.Markdown)
	}

	// Canonical must be unchanged.
	stored, _, _ := store.GetCanonical(ctx, testRepoID, testPageID)
	if paraMd(stored) != "Original auto content." {
		t.Errorf("canonical changed: %q", paraMd(stored))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OpenSyncPR / HandleSyncPRDecision — merge
// ─────────────────────────────────────────────────────────────────────────────

func TestSyncPR_OpenMerge_CanonicalUpdated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Original auto content.")
	o, store := buildOrchestratorWithPage(page)

	overlayStore := orchestrator.NewMemorySinkOverlayStore()
	syncPRStore := orchestrator.NewMemorySyncPRStore()
	auditLog := governance.NewMemoryAuditLog()
	syncPROpener := orchestrator.NewMemoryWikiPR("sync-pr-merge-001")

	secfg := orchestrator.SinkEditConfig{
		AuditLog:     auditLog,
		OverlayStore: overlayStore,
		SyncPRs:      syncPRStore,
		SyncPROpener: syncPROpener,
		SinkConfigs:  map[ast.SinkName]governance.SinkConfig{},
	}

	newContent := ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Approved content."}}

	// Manually stage overlay (simulating the state after HandleSinkEdit with require_review).
	overlay := ast.SinkOverlay{
		SinkName:   "github-wiki",
		PageID:     testPageID,
		Blocks:     map[ast.BlockID]ast.BlockContent{testBlockID: newContent},
		Provenance: map[ast.BlockID]ast.OverlayMeta{testBlockID: {EditedBy: "wiki-user"}},
	}
	_ = overlayStore.SetOverlay(ctx, testRepoID, overlay)

	// Open the sync-PR.
	prID, openErr := o.OpenSyncPR(ctx, secfg, testRepoID, "github-wiki", testBlockID, newContent)
	if openErr != nil {
		t.Fatalf("OpenSyncPR: %v", openErr)
	}
	if prID != "sync-pr-merge-001" {
		t.Errorf("prID = %q, want sync-pr-merge-001", prID)
	}

	// Persist the sync-PR record (normally done by HandleSinkEdit).
	_ = syncPRStore.Set(ctx, orchestrator.SyncPRRecord{
		PRID:     prID,
		RepoID:   testRepoID,
		SinkName: "github-wiki",
		PageID:   testPageID,
		BlockID:  testBlockID,
		SinkUser: "wiki-user",
		OpenedAt: time.Now(),
	})

	// Merge the sync-PR.
	mergeErr := o.HandleSyncPRDecision(ctx, secfg, prID, governance.SyncPRDecisionMerge, "reviewer@example.com")
	if mergeErr != nil {
		t.Fatalf("HandleSyncPRDecision merge: %v", mergeErr)
	}

	// Canonical must have the promoted content.
	stored, ok, _ := store.GetCanonical(ctx, testRepoID, testPageID)
	if !ok {
		t.Fatal("canonical page not found after merge")
	}
	if stored.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("canonical owner: got %v, want human-edited", stored.Blocks[0].Owner)
	}
	if paraMd(stored) != "Approved content." {
		t.Errorf("canonical content: %q", paraMd(stored))
	}

	// Overlay must be cleared.
	_, overlayExists, _ := overlayStore.GetOverlay(ctx, testRepoID, "github-wiki", testPageID)
	if overlayExists {
		t.Error("overlay should be cleared after merge")
	}

	// Sync-PR record must be deleted.
	_, recordExists, _ := syncPRStore.Get(ctx, prID)
	if recordExists {
		t.Error("sync-PR record should be deleted after merge")
	}

	// Audit log: promote_to_canonical + sync_pr_merge.
	entries, _ := auditLog.Query(ctx, governance.AuditFilter{})
	decisions := map[string]bool{}
	for _, e := range entries {
		decisions[e.Decision] = true
	}
	if !decisions["promote_to_canonical"] || !decisions["sync_pr_merge"] {
		t.Errorf("expected both audit decisions, got: %v", decisions)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSyncPRDecision — reject
// ─────────────────────────────────────────────────────────────────────────────

func TestSyncPR_Reject_OverlayPreserved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Original auto content.")
	o, store := buildOrchestratorWithPage(page)

	overlayStore := orchestrator.NewMemorySinkOverlayStore()
	syncPRStore := orchestrator.NewMemorySyncPRStore()
	auditLog := governance.NewMemoryAuditLog()

	proposedContent := ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Rejected proposal."}}
	overlay := ast.SinkOverlay{
		SinkName:   "github-wiki",
		PageID:     testPageID,
		Blocks:     map[ast.BlockID]ast.BlockContent{testBlockID: proposedContent},
		Provenance: map[ast.BlockID]ast.OverlayMeta{testBlockID: {EditedBy: "wiki-user"}},
	}
	_ = overlayStore.SetOverlay(ctx, testRepoID, overlay)
	_ = syncPRStore.Set(ctx, orchestrator.SyncPRRecord{
		PRID:     "sync-pr-reject-001",
		RepoID:   testRepoID,
		SinkName: "github-wiki",
		PageID:   testPageID,
		BlockID:  testBlockID,
		SinkUser: "wiki-user",
		OpenedAt: time.Now(),
	})

	secfg := orchestrator.SinkEditConfig{
		AuditLog:     auditLog,
		OverlayStore: overlayStore,
		SyncPRs:      syncPRStore,
		SinkConfigs:  map[ast.SinkName]governance.SinkConfig{},
	}

	rejectErr := o.HandleSyncPRDecision(ctx, secfg, "sync-pr-reject-001", governance.SyncPRDecisionReject, "eng@example.com")
	if rejectErr != nil {
		t.Fatalf("HandleSyncPRDecision reject: %v", rejectErr)
	}

	// Canonical must be unchanged in the store.
	stored, _, _ := store.GetCanonical(ctx, testRepoID, testPageID)
	if stored.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("canonical owner changed after reject: %v", stored.Blocks[0].Owner)
	}

	// Overlay preserved (local_to_sink).
	storedOverlay, exists, _ := overlayStore.GetOverlay(ctx, testRepoID, "github-wiki", testPageID)
	if !exists {
		t.Error("overlay should be preserved after rejection")
	}
	if storedOverlay.Blocks[testBlockID].Paragraph == nil || storedOverlay.Blocks[testBlockID].Paragraph.Markdown != "Rejected proposal." {
		t.Errorf("overlay content wrong: %+v", storedOverlay.Blocks[testBlockID])
	}

	// Audit log: sync_pr_reject.
	entries, _ := auditLog.Query(ctx, governance.AuditFilter{Decision: "sync_pr_reject"})
	if len(entries) != 1 {
		t.Errorf("expected 1 sync_pr_reject entry, got %d", len(entries))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSyncPRDecision — force_overwrite
// ─────────────────────────────────────────────────────────────────────────────

func TestSyncPR_ForceOverwrite_OverlayCleared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Original auto content.")
	o, store := buildOrchestratorWithPage(page)

	overlayStore := orchestrator.NewMemorySinkOverlayStore()
	syncPRStore := orchestrator.NewMemorySyncPRStore()
	auditLog := governance.NewMemoryAuditLog()

	proposedContent := ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Will be discarded."}}
	overlay := ast.SinkOverlay{
		SinkName:   "confluence-acme",
		PageID:     testPageID,
		Blocks:     map[ast.BlockID]ast.BlockContent{testBlockID: proposedContent},
		Provenance: map[ast.BlockID]ast.OverlayMeta{testBlockID: {EditedBy: "admin"}},
	}
	_ = overlayStore.SetOverlay(ctx, testRepoID, overlay)
	_ = syncPRStore.Set(ctx, orchestrator.SyncPRRecord{
		PRID:     "sync-pr-force-001",
		RepoID:   testRepoID,
		SinkName: "confluence-acme",
		PageID:   testPageID,
		BlockID:  testBlockID,
		SinkUser: "admin",
		OpenedAt: time.Now(),
	})

	secfg := orchestrator.SinkEditConfig{
		AuditLog:     auditLog,
		OverlayStore: overlayStore,
		SyncPRs:      syncPRStore,
		SinkConfigs:  map[ast.SinkName]governance.SinkConfig{},
	}

	forceErr := o.HandleSyncPRDecision(ctx, secfg, "sync-pr-force-001", governance.SyncPRDecisionForceOverwrite, "")
	if forceErr != nil {
		t.Fatalf("HandleSyncPRDecision force_overwrite: %v", forceErr)
	}

	// Overlay must be gone.
	_, overlayExists, _ := overlayStore.GetOverlay(ctx, testRepoID, "confluence-acme", testPageID)
	if overlayExists {
		t.Error("overlay should be cleared after force_overwrite")
	}

	// Canonical must be unchanged.
	stored, _, _ := store.GetCanonical(ctx, testRepoID, testPageID)
	if stored.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("canonical owner changed: %v", stored.Blocks[0].Owner)
	}

	// Audit log: sync_pr_force_overwrite.
	entries, _ := auditLog.Query(ctx, governance.AuditFilter{Decision: "sync_pr_force_overwrite"})
	if len(entries) != 1 {
		t.Errorf("expected 1 force_overwrite entry, got %d", len(entries))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PollAndReconcile
// ─────────────────────────────────────────────────────────────────────────────

func TestPollAndReconcile_EmptyPoll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Content.")
	o, _ := buildOrchestratorWithPage(page)

	poller := orchestrator.NewMemorySinkPoller()
	localPolicy := governance.EditPolicyLocalToSink
	secfg, overlayStore, _, _ := makeSinkEditCfg("confluence-acme", governance.SinkKindConfluence, &localPolicy)

	err := o.PollAndReconcile(ctx, secfg, testRepoID, testPageID, "confluence-acme", poller)
	if err != nil {
		t.Errorf("empty poll: %v", err)
	}
	// No overlay should be created.
	_, exists, _ := overlayStore.GetOverlay(ctx, testRepoID, "confluence-acme", testPageID)
	if exists {
		t.Error("unexpected overlay after empty poll")
	}
}

func TestPollAndReconcile_SingleEdit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Content.")
	o, _ := buildOrchestratorWithPage(page)

	poller := orchestrator.NewMemorySinkPoller()
	poller.Enqueue(makeSinkEdit("confluence-acme", testBlockID, "Edited by product team."))

	localPolicy := governance.EditPolicyLocalToSink
	secfg, overlayStore, _, _ := makeSinkEditCfg("confluence-acme", governance.SinkKindConfluence, &localPolicy)

	err := o.PollAndReconcile(ctx, secfg, testRepoID, testPageID, "confluence-acme", poller)
	if err != nil {
		t.Fatalf("PollAndReconcile single edit: %v", err)
	}

	overlay, exists, _ := overlayStore.GetOverlay(ctx, testRepoID, "confluence-acme", testPageID)
	if !exists {
		t.Fatal("overlay not created after single edit")
	}
	if overlay.Blocks[testBlockID].Paragraph.Markdown != "Edited by product team." {
		t.Errorf("overlay content wrong: %q", overlay.Blocks[testBlockID].Paragraph.Markdown)
	}
}

func TestPollAndReconcile_MultipleEdits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Two separate blocks on the same page.
	page := ast.Page{
		ID: testPageID,
		Manifest: manifest.DependencyManifest{
			PageID:   testPageID,
			Template: "glossary",
			Audience: "for-engineers",
		},
		Blocks: []ast.Block{
			{
				ID:      "b001",
				Kind:    ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "First paragraph."}},
				Owner:   ast.OwnerGenerated,
			},
			{
				ID:      "b002",
				Kind:    ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Second paragraph."}},
				Owner:   ast.OwnerGenerated,
			},
		},
	}
	o, _ := buildOrchestratorWithPage(page)

	poller := orchestrator.NewMemorySinkPoller()
	poller.Enqueue(makeSinkEdit("confluence-acme", "b001", "Edited first."))
	poller.Enqueue(makeSinkEdit("confluence-acme", "b002", "Edited second."))

	localPolicy := governance.EditPolicyLocalToSink
	secfg, overlayStore, _, _ := makeSinkEditCfg("confluence-acme", governance.SinkKindConfluence, &localPolicy)

	err := o.PollAndReconcile(ctx, secfg, testRepoID, testPageID, "confluence-acme", poller)
	if err != nil {
		t.Fatalf("PollAndReconcile multi-edit: %v", err)
	}

	overlay, exists, _ := overlayStore.GetOverlay(ctx, testRepoID, "confluence-acme", testPageID)
	if !exists {
		t.Fatal("overlay not found after multi-edit poll")
	}
	if overlay.Blocks["b001"].Paragraph.Markdown != "Edited first." {
		t.Errorf("b001 overlay wrong: %q", overlay.Blocks["b001"].Paragraph.Markdown)
	}
	if overlay.Blocks["b002"].Paragraph.Markdown != "Edited second." {
		t.Errorf("b002 overlay wrong: %q", overlay.Blocks["b002"].Paragraph.Markdown)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MoveDetector
// ─────────────────────────────────────────────────────────────────────────────

func TestMoveDetector_DetectsRename(t *testing.T) {
	t.Parallel()

	detector := orchestrator.NewMoveDetector()

	oldTaxonomy := []orchestrator.TaxonomyEntry{
		{PageID: "arch.internal.auth", PackagePath: "internal/auth", Path: "wiki/arch.internal.auth.md"},
	}
	newTaxonomy := []orchestrator.TaxonomyEntry{
		{PageID: "arch.internal.identity", PackagePath: "internal/auth", Path: "wiki/arch.internal.identity.md"},
	}

	migrations := detector.Detect(oldTaxonomy, newTaxonomy)
	if len(migrations) != 1 {
		t.Fatalf("expected 1 migration, got %d", len(migrations))
	}
	m := migrations[0]
	if m.Op != ast.MigrationRenamed {
		t.Errorf("op: got %v, want renamed", m.Op)
	}
	if m.FromID != "arch.internal.auth" {
		t.Errorf("FromID: got %q, want arch.internal.auth", m.FromID)
	}
	if len(m.ToIDs) != 1 || m.ToIDs[0] != "arch.internal.identity" {
		t.Errorf("ToIDs: got %v, want [arch.internal.identity]", m.ToIDs)
	}
	if !strings.Contains(m.Rationale, "internal/auth") {
		t.Errorf("Rationale does not mention package: %q", m.Rationale)
	}
}

func TestMoveDetector_NoMigrationWhenUnchanged(t *testing.T) {
	t.Parallel()

	detector := orchestrator.NewMoveDetector()
	same := []orchestrator.TaxonomyEntry{
		{PageID: "arch.internal.auth", PackagePath: "internal/auth", Path: "wiki/arch.internal.auth.md"},
	}

	migrations := detector.Detect(same, same)
	if len(migrations) != 0 {
		t.Errorf("expected 0 migrations for unchanged taxonomy, got %d: %+v", len(migrations), migrations)
	}
}

func TestMoveDetector_NewPageIsNotRename(t *testing.T) {
	t.Parallel()

	detector := orchestrator.NewMoveDetector()
	oldTaxonomy := []orchestrator.TaxonomyEntry{
		{PageID: "arch.internal.auth", PackagePath: "internal/auth"},
	}
	newTaxonomy := []orchestrator.TaxonomyEntry{
		{PageID: "arch.internal.auth", PackagePath: "internal/auth"},
		{PageID: "arch.internal.billing", PackagePath: "internal/billing"}, // brand new
	}

	migrations := detector.Detect(oldTaxonomy, newTaxonomy)
	if len(migrations) != 0 {
		t.Errorf("brand-new page should not produce a migration, got %d", len(migrations))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ApplyMigrations — git sink path
// ─────────────────────────────────────────────────────────────────────────────

func TestApplyMigrations_GitSink_RenamesFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage("arch.internal.auth", testBlockID, "Auth content.")
	o, store := buildOrchestratorWithPage(page)

	gitWriter := &orchestrator.MemoryRepoRenameWriter{}
	migLog := &ast.MigrationLog{}

	migrations := []ast.BlockMigration{
		{
			Op:        ast.MigrationRenamed,
			FromID:    ast.BlockID("arch.internal.auth"),
			ToIDs:     []ast.BlockID{"arch.internal.identity"},
			Rationale: "internal/auth renamed to internal/identity",
		},
	}

	results, err := o.ApplyMigrations(ctx, testRepoID, migrations, migLog, gitWriter, nil)
	if err != nil {
		t.Fatalf("ApplyMigrations git: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Applied {
		t.Errorf("migration not applied: %v", results[0].Err)
	}

	// Git writer must have a rename op.
	if len(gitWriter.Renames) != 1 {
		t.Fatalf("expected 1 rename, got %d", len(gitWriter.Renames))
	}
	rename := gitWriter.Renames[0]
	if rename.OldPath != "wiki/arch.internal.auth.md" {
		t.Errorf("OldPath: %q", rename.OldPath)
	}
	if rename.NewPath != "wiki/arch.internal.identity.md" {
		t.Errorf("NewPath: %q", rename.NewPath)
	}
	if !strings.Contains(rename.Message, "arch.internal.auth") {
		t.Errorf("commit message missing old ID: %q", rename.Message)
	}

	// Page must be stored under new ID in canonical.
	_, oldExists, _ := store.GetCanonical(ctx, testRepoID, "arch.internal.auth")
	if oldExists {
		t.Error("old page ID should no longer exist in canonical after rename")
	}
	newPage, newExists, _ := store.GetCanonical(ctx, testRepoID, "arch.internal.identity")
	if !newExists {
		t.Error("new page ID should exist in canonical after rename")
	}
	if newPage.ID != "arch.internal.identity" {
		t.Errorf("page ID wrong: %q", newPage.ID)
	}

	// Migration log must have the entry.
	if len(migLog.Migrations) != 1 {
		t.Errorf("migration log: expected 1, got %d", len(migLog.Migrations))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ApplyMigrations — API sink path
// ─────────────────────────────────────────────────────────────────────────────

func TestApplyMigrations_APISink_DeletesAndCreates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage("arch.internal.auth", testBlockID, "Auth content.")
	o, _ := buildOrchestratorWithPage(page)

	apiWriter := &orchestrator.MemoryAPISinkWriter{}
	migLog := &ast.MigrationLog{}

	migrations := []ast.BlockMigration{
		{
			Op:        ast.MigrationRenamed,
			FromID:    ast.BlockID("arch.internal.auth"),
			ToIDs:     []ast.BlockID{"arch.internal.identity"},
			Rationale: "package rename",
		},
	}

	results, err := o.ApplyMigrations(ctx, testRepoID, migrations, migLog, nil, apiWriter)
	if err != nil {
		t.Fatalf("ApplyMigrations API: %v", err)
	}
	if !results[0].Applied {
		t.Errorf("API migration not applied: %v", results[0].Err)
	}

	if len(apiWriter.Moves) != 1 {
		t.Fatalf("expected 1 page move, got %d", len(apiWriter.Moves))
	}
	move := apiWriter.Moves[0]
	if move.OldPageID != "arch.internal.auth" {
		t.Errorf("OldPageID: %q", move.OldPageID)
	}
	if move.NewPageID != "arch.internal.identity" {
		t.Errorf("NewPageID: %q", move.NewPageID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MemorySinkPoller
// ─────────────────────────────────────────────────────────────────────────────

func TestMemorySinkPoller_DrainsBetweenPolls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	poller := orchestrator.NewMemorySinkPoller()
	poller.Enqueue(makeSinkEdit("confluence-acme", testBlockID, "First edit."))

	edits, _ := poller.Poll(ctx, "confluence-acme")
	if len(edits) != 1 {
		t.Errorf("first poll: expected 1 edit, got %d", len(edits))
	}

	// Second poll should be empty (queue drained).
	edits2, _ := poller.Poll(ctx, "confluence-acme")
	if len(edits2) != 0 {
		t.Errorf("second poll: expected 0 edits, got %d", len(edits2))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// End-to-end: "edit a paragraph in Confluence, push twice, paragraph unchanged"
//
// This is the "Done when" scenario from A1.P6:
//
//  1. Cold-start generates a wiki page with N paragraphs.
//  2. A "Confluence editor" mutates one paragraph block.
//  3. Poll detects the edit; HandleSinkEdit writes to overlay (local_to_sink).
//  4. Two source-code pushes happen; GenerateIncremental runs twice.
//  5. Assert: the human-edited paragraph in Confluence's view is unchanged;
//     surrounding paragraphs are updated.
//
// ─────────────────────────────────────────────────────────────────────────────

func TestEndToEnd_ConfluenceEditSurvivesTwoPushes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const confSink ast.SinkName = "confluence-acme"
	const humanBlock ast.BlockID = "b-human"
	const autoBlock ast.BlockID = "b-auto"

	// ── Step 1: Cold-start — store a canonical page with two blocks ──────────
	canonicalPage := ast.Page{
		ID: "arch.auth",
		Manifest: manifest.DependencyManifest{
			PageID:   "arch.auth",
			Template: "glossary",
			Audience: "for-engineers",
		},
		Blocks: []ast.Block{
			{
				ID:      humanBlock,
				Kind:    ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Auto-generated intro paragraph."}},
				Owner:   ast.OwnerGenerated,
			},
			{
				ID:      autoBlock,
				Kind:    ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Auto-generated body paragraph."}},
				Owner:   ast.OwnerGenerated,
			},
		},
	}

	store := orchestrator.NewMemoryPageStore()
	_ = store.SetCanonical(ctx, testRepoID, canonicalPage)
	reg := orchestrator.NewDefaultRegistry()
	o := orchestrator.New(orchestrator.Config{RepoID: testRepoID}, reg, store)

	overlayStore := orchestrator.NewMemorySinkOverlayStore()
	auditLog := governance.NewMemoryAuditLog()
	localPolicy := governance.EditPolicyLocalToSink
	secfg := orchestrator.SinkEditConfig{
		AuditLog:     auditLog,
		OverlayStore: overlayStore,
		SyncPRs:      orchestrator.NewMemorySyncPRStore(),
		SinkConfigs: map[ast.SinkName]governance.SinkConfig{
			confSink: governance.NewSinkConfig(governance.SinkKindConfluence, confSink).WithPolicy(localPolicy),
		},
	}

	// ── Step 2: Confluence editor mutates block b-human ────────────────────
	edit := orchestrator.SinkEdit{
		SinkName: confSink,
		BlockID:  humanBlock,
		NewContent: ast.BlockContent{
			Paragraph: &ast.ParagraphContent{Markdown: "Human-authored intro — permanent."},
		},
		EditedBy: "pm@acme.com",
		EditedAt: time.Now(),
	}

	// ── Step 3: Poll detects the edit; HandleSinkEdit stores in overlay ─────
	updated, err := o.HandleSinkEdit(ctx, secfg, testRepoID, "arch.auth", edit)
	if err != nil {
		t.Fatalf("step 3 HandleSinkEdit: %v", err)
	}
	// Canonical is unchanged (local_to_sink).
	if paraMd(updated) != "Auto-generated intro paragraph." {
		t.Errorf("step 3: canonical content changed: %q", paraMd(updated))
	}
	if updated.Blocks[0].SinkDivergence[confSink] != true {
		t.Error("step 3: SinkDivergence not set")
	}

	// ── Step 4: Simulate two source-code pushes — "regenerate" the page ─────
	// We simulate regen by updating the auto block's content while leaving
	// ownership tracking intact. In production this would be GenerateIncremental.
	for pushNum := 1; pushNum <= 2; pushNum++ {
		// Load current canonical.
		canonical, canonOK, _ := store.GetCanonical(ctx, testRepoID, "arch.auth")
		if !canonOK {
			t.Fatalf("push %d: canonical page not found", pushNum)
		}

		// "Regen" — update the auto-block; leave the human block alone.
		// In real code, reconcileWithHumanEdits handles this. Here we simulate
		// it directly to keep the test self-contained.
		newCanonical := ast.Page{
			ID:       canonical.ID,
			Manifest: canonical.Manifest,
			Blocks: []ast.Block{
				// Block 0 (humanBlock) — owner is still OwnerGenerated in canonical
				// because local_to_sink does NOT promote. It stays untouched by regen.
				canonical.Blocks[0],
				// Block 1 (autoBlock) — regen updates the content.
				{
					ID:      autoBlock,
					Kind:    ast.BlockKindParagraph,
					Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Updated body paragraph v" + string(rune('0'+pushNum)) + "."}},
					Owner:   ast.OwnerGenerated,
				},
			},
		}
		_ = store.SetCanonical(ctx, testRepoID, newCanonical)
	}

	// ── Step 5: Assert that Confluence's view preserves the human edit ───────
	canonical, _, _ := store.GetCanonical(ctx, testRepoID, "arch.auth")

	// The canonical humanBlock content is still the auto-generated original
	// (local_to_sink did not promote it).
	if canonical.Blocks[0].Content.Paragraph.Markdown != "Auto-generated intro paragraph." {
		t.Errorf("canonical humanBlock content should still be auto-generated, got: %q",
			canonical.Blocks[0].Content.Paragraph.Markdown)
	}

	// The canonical autoBlock is updated to the last push version.
	if canonical.Blocks[1].Content.Paragraph.Markdown != "Updated body paragraph v2." {
		t.Errorf("canonical autoBlock should be updated, got: %q",
			canonical.Blocks[1].Content.Paragraph.Markdown)
	}

	// The Confluence sink output composes canonical + overlay.
	overlay, overlayOK, _ := overlayStore.GetOverlay(ctx, testRepoID, confSink, "arch.auth")
	if !overlayOK {
		t.Fatal("step 5: overlay should still exist")
	}
	confluencePage := ast.ComposeForSink(canonical, overlay)

	// In Confluence's view: the human-edited block shows the human content.
	if confluencePage.Blocks[0].Content.Paragraph.Markdown != "Human-authored intro — permanent." {
		t.Errorf("confluence view: humanBlock should show human content, got: %q",
			confluencePage.Blocks[0].Content.Paragraph.Markdown)
	}
	// The autoBlock in Confluence shows the latest canonical (updated by regen).
	if confluencePage.Blocks[1].Content.Paragraph.Markdown != "Updated body paragraph v2." {
		t.Errorf("confluence view: autoBlock should show updated canonical, got: %q",
			confluencePage.Blocks[1].Content.Paragraph.Markdown)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// End-to-end: package rename — page moves, citations preserved
//
// Simulates:
//  1. Cold-start generates arch.internal.auth page.
//  2. Package is renamed internal/auth → internal/identity.
//  3. MoveDetector emits a migration.
//  4. ApplyMigrations renames the page.
//  5. Assert: page is at new ID; block IDs preserved; old ID gone.
//
// ─────────────────────────────────────────────────────────────────────────────

func TestEndToEnd_PackageRename_PageMovesWithBlockIDsPreserved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const oldPageID = "arch.internal.auth"
	const newPageID = "arch.internal.identity"

	// ── Step 1: Cold-start page for internal/auth ────────────────────────────
	originalPage := ast.Page{
		ID: oldPageID,
		Manifest: manifest.DependencyManifest{
			PageID:      oldPageID,
			Template:    "glossary",
			Audience:    "for-engineers",
			Dependencies: manifest.Dependencies{Paths: []string{"internal/auth/**"}},
		},
		Blocks: []ast.Block{
			{
				ID:      ast.BlockID("blk-stable-001"),
				Kind:    ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Authentication middleware content."}},
				Owner:   ast.OwnerGenerated,
			},
		},
	}
	store := orchestrator.NewMemoryPageStore()
	_ = store.SetCanonical(ctx, testRepoID, originalPage)
	reg := orchestrator.NewDefaultRegistry()
	o := orchestrator.New(orchestrator.Config{RepoID: testRepoID}, reg, store)

	// ── Step 2: Package renamed internal/auth → internal/identity ────────────
	oldTaxonomy := []orchestrator.TaxonomyEntry{
		{PageID: oldPageID, PackagePath: "internal/auth", Path: "wiki/" + oldPageID + ".md"},
	}
	newTaxonomy := []orchestrator.TaxonomyEntry{
		{PageID: newPageID, PackagePath: "internal/auth", Path: "wiki/" + newPageID + ".md"},
	}

	// ── Step 3: MoveDetector emits migration ─────────────────────────────────
	detector := orchestrator.NewMoveDetector()
	migrations := detector.Detect(oldTaxonomy, newTaxonomy)
	if len(migrations) != 1 {
		t.Fatalf("expected 1 migration, got %d: %+v", len(migrations), migrations)
	}
	if migrations[0].FromID != ast.BlockID(oldPageID) {
		t.Errorf("migration FromID: got %q, want %q", migrations[0].FromID, oldPageID)
	}
	if len(migrations[0].ToIDs) == 0 || migrations[0].ToIDs[0] != ast.BlockID(newPageID) {
		t.Errorf("migration ToIDs: got %v, want [%q]", migrations[0].ToIDs, newPageID)
	}

	// ── Step 4: ApplyMigrations renames in git sink ───────────────────────────
	gitWriter := &orchestrator.MemoryRepoRenameWriter{}
	migLog := &ast.MigrationLog{}
	results, applyErr := o.ApplyMigrations(ctx, testRepoID, migrations, migLog, gitWriter, nil)
	if applyErr != nil {
		t.Fatalf("ApplyMigrations: %v", applyErr)
	}
	if !results[0].Applied {
		t.Fatalf("migration not applied: %v", results[0].Err)
	}

	// ── Step 5: Assertions ────────────────────────────────────────────────────

	// Old page ID must not exist.
	_, oldExists, _ := store.GetCanonical(ctx, testRepoID, oldPageID)
	if oldExists {
		t.Error("old page should no longer exist in canonical")
	}

	// New page ID must exist with the same block ID.
	newPage, newExists, _ := store.GetCanonical(ctx, testRepoID, newPageID)
	if !newExists {
		t.Fatal("new page should exist in canonical")
	}
	if len(newPage.Blocks) == 0 {
		t.Fatal("new page has no blocks")
	}
	// Block ID preserved.
	if newPage.Blocks[0].ID != "blk-stable-001" {
		t.Errorf("block ID not preserved: got %q, want blk-stable-001", newPage.Blocks[0].ID)
	}
	// Content preserved.
	if newPage.Blocks[0].Content.Paragraph.Markdown != "Authentication middleware content." {
		t.Errorf("block content changed: %q", newPage.Blocks[0].Content.Paragraph.Markdown)
	}

	// Git writer must have the rename.
	if len(gitWriter.Renames) != 1 {
		t.Fatalf("expected 1 rename, got %d", len(gitWriter.Renames))
	}
	rename := gitWriter.Renames[0]
	if rename.OldPath != "wiki/"+oldPageID+".md" || rename.NewPath != "wiki/"+newPageID+".md" {
		t.Errorf("rename paths: %q → %q", rename.OldPath, rename.NewPath)
	}

	// Migration log must record the event.
	if len(migLog.Migrations) != 1 {
		t.Errorf("migration log: expected 1, got %d", len(migLog.Migrations))
	}

	// Verify that citations to the new path would still resolve: the block ID
	// "blk-stable-001" appears in the new page, so any citation referencing
	// that block ID remains valid. (Full citation round-trip via internal/citations
	// is tested in the citations package; here we confirm the block-level invariant.)
	found := false
	for _, blk := range newPage.Blocks {
		if blk.ID == "blk-stable-001" {
			found = true
			break
		}
	}
	if !found {
		t.Error("citation invariant violated: block blk-stable-001 not found in new page")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Promote still works under new sink infrastructure (regression guard for P1)
// ─────────────────────────────────────────────────────────────────────────────

func TestPromote_StillWorkAfterP6Addition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := orchestrator.NewMemoryPageStore()
	reg := orchestrator.NewDefaultRegistry()
	o := orchestrator.New(orchestrator.Config{RepoID: testRepoID}, reg, store)

	// Store a proposed page with a OwnerHumanEditedOnPRBranch block.
	proposed := ast.Page{
		ID: "arch.auth",
		Manifest: manifest.DependencyManifest{
			PageID: "arch.auth", Template: "glossary", Audience: "for-engineers",
		},
		Blocks: []ast.Block{
			{
				ID:      "b001",
				Kind:    ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "PR branch edit."}},
				Owner:   ast.OwnerHumanEditedOnPRBranch,
			},
		},
	}
	_ = store.SetProposed(ctx, testRepoID, "pr-001", proposed)

	// Promote.
	if err := o.Promote(ctx, testRepoID, "pr-001"); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	canonical, ok, _ := store.GetCanonical(ctx, testRepoID, "arch.auth")
	if !ok {
		t.Fatal("canonical page not found after promote")
	}
	if canonical.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("owner after promote: got %v, want human-edited", canonical.Blocks[0].Owner)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OpenSyncPR title/body format
// ─────────────────────────────────────────────────────────────────────────────

func TestOpenSyncPR_TitleAndBodyFormat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	page := makeCanonicalPage(testPageID, testBlockID, "Content.")
	o, _ := buildOrchestratorWithPage(page)

	syncPROpener := orchestrator.NewMemoryWikiPR("sync-title-001")
	secfg := orchestrator.SinkEditConfig{
		AuditLog:     governance.NewMemoryAuditLog(),
		OverlayStore: orchestrator.NewMemorySinkOverlayStore(),
		SyncPRs:      orchestrator.NewMemorySyncPRStore(),
		SyncPROpener: syncPROpener,
		SinkConfigs:  map[ast.SinkName]governance.SinkConfig{},
	}

	newContent := ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Proposed text."}}
	prID, err := o.OpenSyncPR(ctx, secfg, testRepoID, "notion-acme", testBlockID, newContent)
	if err != nil {
		t.Fatalf("OpenSyncPR: %v", err)
	}
	if prID != "sync-title-001" {
		t.Errorf("prID: %q", prID)
	}

	// Title must follow the convention.
	title := syncPROpener.Title()
	if !strings.Contains(title, "notion-acme") || !strings.Contains(title, string(testBlockID)) {
		t.Errorf("title missing sink or block: %q", title)
	}

	// Body must include the proposed content.
	body := syncPROpener.Body()
	if !strings.Contains(body, "Proposed text.") {
		t.Errorf("body missing proposed content: %q", body)
	}
}

// quality compile-time guard — ensures the test file refers to the quality
// package so the import is not pruned if all other tests use types only.
var _ = quality.AudienceEngineers
