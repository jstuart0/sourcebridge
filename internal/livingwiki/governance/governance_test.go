// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package governance_test

import (
	"context"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/governance"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func makeBlock(id ast.BlockID, owner ast.Owner, text string) ast.Block {
	return ast.Block{
		ID:   id,
		Kind: ast.BlockKindParagraph,
		Content: ast.BlockContent{
			Paragraph: &ast.ParagraphContent{Markdown: text},
		},
		Owner: owner,
	}
}

func makePage(id string, blocks ...ast.Block) ast.Page {
	return ast.Page{ID: id, Blocks: blocks}
}

func makePageWithManifest(id string, m manifest.DependencyManifest, blocks ...ast.Block) ast.Page {
	p := makePage(id, blocks...)
	p.Manifest = m
	return p
}

func makeOverlay(sinkName ast.SinkName, pageID string, blockID ast.BlockID, content ast.BlockContent) ast.SinkOverlay {
	return ast.SinkOverlay{
		SinkName:   sinkName,
		PageID:     pageID,
		Blocks:     map[ast.BlockID]ast.BlockContent{blockID: content},
		Provenance: map[ast.BlockID]ast.OverlayMeta{blockID: {EditedBy: "user@example.com", EditedAt: time.Now()}},
	}
}

func paraContent(text string) ast.BlockContent {
	return ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: text}}
}

var ctx = context.Background()

// ─────────────────────────────────────────────────────────────────────────────
// G.1 + G.2: DefaultPolicy
// ─────────────────────────────────────────────────────────────────────────────

func TestDefaultPolicy_AllSinkKinds(t *testing.T) {
	cases := []struct {
		kind SinkKind
		want governance.EditPolicy
	}{
		{governance.SinkKindGitRepo, governance.EditPolicyPromoteToCanonical},
		{governance.SinkKindGitHubWiki, governance.EditPolicyRequireReviewBeforePromote},
		{governance.SinkKindGitLabWiki, governance.EditPolicyRequireReviewBeforePromote},
		{governance.SinkKindConfluence, governance.EditPolicyLocalToSink},
		{governance.SinkKindNotion, governance.EditPolicyLocalToSink},
		{governance.SinkKindStaticSite, governance.EditPolicyNotApplicable},
	}

	for _, tc := range cases {
		got := governance.DefaultPolicy(tc.kind)
		if got != tc.want {
			t.Errorf("DefaultPolicy(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// SinkKind is the type from the governance package; alias for test clarity.
type SinkKind = governance.SinkKind

func TestSinkConfig_EffectivePolicy_DefaultUsed(t *testing.T) {
	cfg := governance.NewSinkConfig(governance.SinkKindConfluence, "confluence-acme")
	got := cfg.EffectivePolicy()
	if got != governance.EditPolicyLocalToSink {
		t.Errorf("EffectivePolicy with default: got %v, want EditPolicyLocalToSink", got)
	}
}

func TestSinkConfig_EffectivePolicy_ExplicitOverride(t *testing.T) {
	cfg := governance.NewSinkConfig(governance.SinkKindConfluence, "confluence-acme").
		WithPolicy(governance.EditPolicyRequireReviewBeforePromote)
	got := cfg.EffectivePolicy()
	if got != governance.EditPolicyRequireReviewBeforePromote {
		t.Errorf("EffectivePolicy with override: got %v, want EditPolicyRequireReviewBeforePromote", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// G.1: BlockPolicyOverride
// ─────────────────────────────────────────────────────────────────────────────

func TestBlockPolicyOverride_ValidMarker(t *testing.T) {
	blk := ast.Block{
		ID:   "b001",
		Kind: ast.BlockKindParagraph,
		Content: ast.BlockContent{
			Paragraph: &ast.ParagraphContent{
				Markdown: "Some text. <!-- sourcebridge:promote=local-to-sink --> More text.",
			},
		},
	}
	policy, ok := governance.BlockPolicyOverride(blk)
	if !ok {
		t.Fatal("expected ok=true for valid marker")
	}
	if policy != governance.EditPolicyLocalToSink {
		t.Errorf("expected EditPolicyLocalToSink, got %v", policy)
	}
}

func TestBlockPolicyOverride_MissingMarker(t *testing.T) {
	blk := ast.Block{
		ID:   "b002",
		Kind: ast.BlockKindParagraph,
		Content: ast.BlockContent{
			Paragraph: &ast.ParagraphContent{Markdown: "Plain text with no marker."},
		},
	}
	_, ok := governance.BlockPolicyOverride(blk)
	if ok {
		t.Error("expected ok=false when no marker present")
	}
}

func TestBlockPolicyOverride_InvalidValue(t *testing.T) {
	blk := ast.Block{
		ID:   "b003",
		Kind: ast.BlockKindParagraph,
		Content: ast.BlockContent{
			Paragraph: &ast.ParagraphContent{
				Markdown: "Text. <!-- sourcebridge:promote=unknown-value --> More.",
			},
		},
	}
	_, ok := governance.BlockPolicyOverride(blk)
	if ok {
		t.Error("expected ok=false for unrecognised marker value")
	}
}

func TestBlockPolicyOverride_MalformedClosingMarker(t *testing.T) {
	blk := ast.Block{
		ID:   "b004",
		Kind: ast.BlockKindParagraph,
		Content: ast.BlockContent{
			Paragraph: &ast.ParagraphContent{
				Markdown: "Text. <!-- sourcebridge:promote=local-to-sink (no closing)", // no " -->"
			},
		},
	}
	_, ok := governance.BlockPolicyOverride(blk)
	if ok {
		t.Error("expected ok=false when closing marker is absent")
	}
}

func TestBlockPolicyOverride_NonTextBlock(t *testing.T) {
	// Markers on heading blocks are not extracted.
	blk := ast.Block{
		ID:   "b005",
		Kind: ast.BlockKindHeading,
		Content: ast.BlockContent{
			Heading: &ast.HeadingContent{Level: 2, Text: "<!-- sourcebridge:promote=local-to-sink -->"},
		},
	}
	_, ok := governance.BlockPolicyOverride(blk)
	if ok {
		t.Error("expected ok=false for heading block (markers not extracted from headings)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// G.3: PromoteToCanonical
// ─────────────────────────────────────────────────────────────────────────────

func TestPromoteToCanonical_FlipsOwnershipAndStashes(t *testing.T) {
	log := governance.NewMemoryAuditLog()
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Original auto content."),
	)

	newContent := paraContent("Human-edited content.")
	updated, stashed, err := governance.PromoteToCanonical(ctx, page, "git-repo", "human@example.com", "b001", newContent, log)
	if err != nil {
		t.Fatalf("PromoteToCanonical returned error: %v", err)
	}

	// Ownership must flip.
	if updated.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("owner not flipped: got %v, want human-edited", updated.Blocks[0].Owner)
	}
	// Content must be updated.
	if updated.Blocks[0].Content.Paragraph.Markdown != "Human-edited content." {
		t.Errorf("content not updated: %q", updated.Blocks[0].Content.Paragraph.Markdown)
	}
	// Stash must hold the original content.
	if stashed.Paragraph == nil || stashed.Paragraph.Markdown != "Original auto content." {
		t.Errorf("stashed content wrong: %+v", stashed)
	}
	// Original page must be unchanged (no mutation).
	if page.Blocks[0].Owner != ast.OwnerGenerated {
		t.Error("original page was mutated")
	}

	// Audit log must have an entry.
	entries, _ := log.Query(ctx, governance.AuditFilter{})
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].Decision != "promote_to_canonical" {
		t.Errorf("audit decision: got %q, want promote_to_canonical", entries[0].Decision)
	}
}

func TestPromoteToCanonical_BlockNotFound(t *testing.T) {
	log := governance.NewMemoryAuditLog()
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Content."),
	)
	_, _, err := governance.PromoteToCanonical(ctx, page, "git-repo", "u", "b-nonexistent", paraContent("x"), log)
	if err == nil {
		t.Error("expected error when block not found")
	}
}

func TestPromoteToCanonical_IdempotentIfCalledTwice(t *testing.T) {
	log := governance.NewMemoryAuditLog()
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Auto content."),
	)

	content1 := paraContent("First human edit.")
	updated1, _, err := governance.PromoteToCanonical(ctx, page, "git-repo", "u", "b001", content1, log)
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}

	content2 := paraContent("Second human edit.")
	updated2, stashed2, err := governance.PromoteToCanonical(ctx, updated1, "git-repo", "u", "b001", content2, log)
	if err != nil {
		t.Fatalf("second promote: %v", err)
	}

	// After second promote, owner is still human-edited.
	if updated2.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("second promote: owner = %v, want human-edited", updated2.Blocks[0].Owner)
	}
	// Stashed content from second call is the result of the first promote.
	if stashed2.Paragraph == nil || stashed2.Paragraph.Markdown != "First human edit." {
		t.Errorf("second stash wrong: %+v", stashed2)
	}
	// Two audit entries.
	entries, _ := log.Query(ctx, governance.AuditFilter{})
	if len(entries) != 2 {
		t.Errorf("expected 2 audit entries, got %d", len(entries))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// G.3: MarkBlockAuto
// ─────────────────────────────────────────────────────────────────────────────

func TestMarkBlockAuto_FlipsHumanEdited(t *testing.T) {
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerHumanEdited, "Human content."),
	)
	updated := governance.MarkBlockAuto(page, "b001")
	if updated.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("expected generated after MarkBlockAuto, got %v", updated.Blocks[0].Owner)
	}
	// Original unchanged.
	if page.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Error("original page mutated")
	}
}

func TestMarkBlockAuto_NoopForGenerated(t *testing.T) {
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Auto content."),
	)
	updated := governance.MarkBlockAuto(page, "b001")
	if updated.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("expected generated to remain, got %v", updated.Blocks[0].Owner)
	}
}

func TestMarkBlockAuto_NoopForHumanOnly(t *testing.T) {
	blk := makeBlock("b001", ast.OwnerHumanOnly, "Freeform content.")
	page := makePage("arch.auth", blk)
	updated := governance.MarkBlockAuto(page, "b001")
	if updated.Blocks[0].Owner != ast.OwnerHumanOnly {
		t.Errorf("human-only owner should not change, got %v", updated.Blocks[0].Owner)
	}
}

func TestMarkBlockAuto_NoopForMissingBlock(t *testing.T) {
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerHumanEdited, "Content."),
	)
	updated := governance.MarkBlockAuto(page, "b-does-not-exist")
	// Page unchanged.
	if updated.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("missing block should not affect other blocks")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// G.3: InvalidationStaleSignal
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidationStaleSignal_FiresForHumanEdited(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		StaleWhen: []manifest.StaleCondition{
			{SignatureChangeIn: []string{"auth.Middleware"}},
		},
	}
	page := makePageWithManifest("arch.auth", m,
		makeBlock("b001", ast.OwnerHumanEdited, "Human content."),
	)

	fired := governance.InvalidationStaleSignal(page, "b001", []string{"auth.Middleware"})
	if !fired {
		t.Error("expected stale signal to fire for human-edited block with matching symbol")
	}
}

func TestInvalidationStaleSignal_DoesNotFireForGenerated(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		StaleWhen: []manifest.StaleCondition{
			{SignatureChangeIn: []string{"auth.Middleware"}},
		},
	}
	page := makePageWithManifest("arch.auth", m,
		makeBlock("b001", ast.OwnerGenerated, "Auto content."),
	)

	fired := governance.InvalidationStaleSignal(page, "b001", []string{"auth.Middleware"})
	if fired {
		t.Error("stale signal must not fire for generated blocks")
	}
}

func TestInvalidationStaleSignal_DoesNotFireWhenSymbolNotMatched(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		StaleWhen: []manifest.StaleCondition{
			{SignatureChangeIn: []string{"auth.Middleware"}},
		},
	}
	page := makePageWithManifest("arch.auth", m,
		makeBlock("b001", ast.OwnerHumanEdited, "Human content."),
	)

	fired := governance.InvalidationStaleSignal(page, "b001", []string{"billing.Invoice"})
	if fired {
		t.Error("stale signal must not fire when changed symbols don't match any condition")
	}
}

func TestInvalidationStaleSignal_DoesNotFireWhenNoStaleWhen(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		// No StaleWhen conditions.
	}
	page := makePageWithManifest("arch.auth", m,
		makeBlock("b001", ast.OwnerHumanEdited, "Human content."),
	)

	fired := governance.InvalidationStaleSignal(page, "b001", []string{"auth.Middleware"})
	if fired {
		t.Error("stale signal must not fire when manifest has no stale_when conditions")
	}
}

func TestInvalidationStaleSignal_BlockNotFound(t *testing.T) {
	m := manifest.DependencyManifest{
		PageID: "arch.auth",
		StaleWhen: []manifest.StaleCondition{
			{SignatureChangeIn: []string{"auth.Middleware"}},
		},
	}
	page := makePageWithManifest("arch.auth", m,
		makeBlock("b001", ast.OwnerHumanEdited, "Human content."),
	)

	fired := governance.InvalidationStaleSignal(page, "b-nonexistent", []string{"auth.Middleware"})
	if fired {
		t.Error("stale signal must not fire when block is not found")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// G.4: ResolveSyncPR
// ─────────────────────────────────────────────────────────────────────────────

func TestResolveSyncPR_Merge(t *testing.T) {
	log := governance.NewMemoryAuditLog()
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Canonical auto content."),
	)
	overlay := makeOverlay("notion-acme", "arch.auth", "b001", paraContent("Notion editor's version."))

	newCanonical, newOverlay, err := governance.ResolveSyncPR(
		ctx, page, overlay, "notion-acme", "editor@example.com", "b001",
		governance.SyncPRDecisionMerge, "reviewer@example.com", log,
	)
	if err != nil {
		t.Fatalf("ResolveSyncPR merge: %v", err)
	}

	// Canonical must now have the Notion editor's content.
	if newCanonical.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("merge: canonical owner = %v, want human-edited", newCanonical.Blocks[0].Owner)
	}
	if newCanonical.Blocks[0].Content.Paragraph.Markdown != "Notion editor's version." {
		t.Errorf("merge: canonical content wrong: %q", newCanonical.Blocks[0].Content.Paragraph.Markdown)
	}

	// Overlay must be empty.
	if _, inOverlay := newOverlay.Blocks["b001"]; inOverlay {
		t.Error("merge: block should be removed from overlay after merge")
	}

	// Audit log: PromoteToCanonical appends "promote_to_canonical",
	// ResolveSyncPR appends "sync_pr_merge" on top.
	entries, _ := log.Query(ctx, governance.AuditFilter{})
	if len(entries) != 2 {
		t.Fatalf("merge: expected 2 audit entries, got %d", len(entries))
	}
	decisions := map[string]bool{}
	for _, e := range entries {
		decisions[e.Decision] = true
	}
	if !decisions["promote_to_canonical"] || !decisions["sync_pr_merge"] {
		t.Errorf("merge: unexpected audit decisions: %v", decisions)
	}
	// Reviewer should be on the sync_pr_merge entry.
	for _, e := range entries {
		if e.Decision == "sync_pr_merge" && e.Reviewer != "reviewer@example.com" {
			t.Errorf("sync_pr_merge reviewer = %q, want reviewer@example.com", e.Reviewer)
		}
	}
}

func TestResolveSyncPR_Reject(t *testing.T) {
	log := governance.NewMemoryAuditLog()
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Canonical auto content."),
	)
	overlay := makeOverlay("github-wiki", "arch.auth", "b001", paraContent("Wiki editor's version."))

	newCanonical, newOverlay, err := governance.ResolveSyncPR(
		ctx, page, overlay, "github-wiki", "wiki-user", "b001",
		governance.SyncPRDecisionReject, "reviewer@example.com", log,
	)
	if err != nil {
		t.Fatalf("ResolveSyncPR reject: %v", err)
	}

	// Canonical must be unchanged.
	if newCanonical.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("reject: canonical owner changed unexpectedly: %v", newCanonical.Blocks[0].Owner)
	}
	if newCanonical.Blocks[0].Content.Paragraph.Markdown != "Canonical auto content." {
		t.Errorf("reject: canonical content changed: %q", newCanonical.Blocks[0].Content.Paragraph.Markdown)
	}

	// Overlay block must be preserved (stays as local_to_sink override).
	if _, inOverlay := newOverlay.Blocks["b001"]; !inOverlay {
		t.Error("reject: overlay block should be preserved after rejection (local_to_sink override)")
	}
	if newOverlay.Blocks["b001"].Paragraph.Markdown != "Wiki editor's version." {
		t.Errorf("reject: overlay content wrong: %q", newOverlay.Blocks["b001"].Paragraph.Markdown)
	}

	// Audit entry.
	entries, _ := log.Query(ctx, governance.AuditFilter{Decision: "sync_pr_reject"})
	if len(entries) != 1 {
		t.Fatalf("reject: expected 1 sync_pr_reject entry, got %d", len(entries))
	}
}

func TestResolveSyncPR_ForceOverwrite(t *testing.T) {
	log := governance.NewMemoryAuditLog()
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Canonical auto content."),
	)
	overlay := makeOverlay("confluence-acme", "arch.auth", "b001", paraContent("Confluence editor's version."))

	newCanonical, newOverlay, err := governance.ResolveSyncPR(
		ctx, page, overlay, "confluence-acme", "admin", "b001",
		governance.SyncPRDecisionForceOverwrite, "", log,
	)
	if err != nil {
		t.Fatalf("ResolveSyncPR force_overwrite: %v", err)
	}

	// Canonical unchanged.
	if newCanonical.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("force_overwrite: canonical owner changed: %v", newCanonical.Blocks[0].Owner)
	}

	// Overlay entry must be removed.
	if _, inOverlay := newOverlay.Blocks["b001"]; inOverlay {
		t.Error("force_overwrite: overlay block should be removed")
	}

	// Audit entry.
	entries, _ := log.Query(ctx, governance.AuditFilter{Decision: "sync_pr_force_overwrite"})
	if len(entries) != 1 {
		t.Fatalf("force_overwrite: expected 1 audit entry, got %d", len(entries))
	}
}

func TestResolveSyncPR_BlockNotInOverlay(t *testing.T) {
	log := governance.NewMemoryAuditLog()
	page := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Content."),
	)
	emptyOverlay := ast.SinkOverlay{
		SinkName:   "notion-acme",
		PageID:     "arch.auth",
		Blocks:     map[ast.BlockID]ast.BlockContent{},
		Provenance: map[ast.BlockID]ast.OverlayMeta{},
	}

	_, _, err := governance.ResolveSyncPR(
		ctx, page, emptyOverlay, "notion-acme", "u", "b001",
		governance.SyncPRDecisionMerge, "", log,
	)
	if err == nil {
		t.Error("expected error when block is not in overlay")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// G.5: MemoryAuditLog
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryAuditLog_AppendAndQuery(t *testing.T) {
	log := governance.NewMemoryAuditLog()

	now := time.Now()
	e1 := governance.AuditEntry{
		BlockID:    "b001",
		SourceSink: "confluence-acme",
		Decision:   "promote_to_canonical",
		Timestamp:  now,
	}
	e2 := governance.AuditEntry{
		BlockID:    "b002",
		SourceSink: "notion-acme",
		Decision:   "sync_pr_merge",
		Timestamp:  now.Add(time.Second),
	}
	e3 := governance.AuditEntry{
		BlockID:    "b001",
		SourceSink: "confluence-acme",
		Decision:   "sync_pr_reject",
		Timestamp:  now.Add(2 * time.Second),
	}

	for _, e := range []governance.AuditEntry{e1, e2, e3} {
		if err := log.Append(ctx, e); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	// Query all.
	all, _ := log.Query(ctx, governance.AuditFilter{})
	if len(all) != 3 {
		t.Fatalf("Query all: expected 3, got %d", len(all))
	}

	// Filter by BlockID.
	byBlock, _ := log.Query(ctx, governance.AuditFilter{BlockID: "b001"})
	if len(byBlock) != 2 {
		t.Errorf("Query by BlockID=b001: expected 2, got %d", len(byBlock))
	}

	// Filter by SourceSink.
	bySink, _ := log.Query(ctx, governance.AuditFilter{SourceSink: "notion-acme"})
	if len(bySink) != 1 || bySink[0].Decision != "sync_pr_merge" {
		t.Errorf("Query by SourceSink=notion-acme: unexpected results: %+v", bySink)
	}

	// Filter by Decision.
	byDecision, _ := log.Query(ctx, governance.AuditFilter{Decision: "sync_pr_reject"})
	if len(byDecision) != 1 {
		t.Errorf("Query by Decision=sync_pr_reject: expected 1, got %d", len(byDecision))
	}

	// Filter by time range.
	byTime, _ := log.Query(ctx, governance.AuditFilter{Since: now.Add(time.Second)})
	if len(byTime) != 2 {
		t.Errorf("Query by Since: expected 2, got %d", len(byTime))
	}
}

func TestMemoryAuditLog_EmptyQueryReturnsAll(t *testing.T) {
	log := governance.NewMemoryAuditLog()
	entries, err := log.Query(ctx, governance.AuditFilter{})
	if err != nil {
		t.Fatalf("Query on empty log: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Query on empty log: expected 0 entries, got %d", len(entries))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Integration test: 3-sink scenario
// ─────────────────────────────────────────────────────────────────────────────

// TestIntegration_ThreeSinkScenario simulates:
//
//	- git_repo with promote_to_canonical policy
//	- confluence-acme with local_to_sink policy
//	- notion-acme with require_review_before_promote policy
//
// Each sink makes an edit. We assert the canonical state and per-sink overlay
// state are correct after each edit is processed.
func TestIntegration_ThreeSinkScenario(t *testing.T) {
	log := governance.NewMemoryAuditLog()

	// Canonical page starts with one generated block.
	canonical := makePage("arch.auth",
		makeBlock("b001", ast.OwnerGenerated, "Original auto content."),
	)

	sinkGit := governance.NewSinkConfig(governance.SinkKindGitRepo, "git-acme-repo")
	sinkConfluence := governance.NewSinkConfig(governance.SinkKindConfluence, "confluence-acme-space")
	// Notion's default is local_to_sink; this customer has overridden it to
	// require_review_before_promote so that product edits are surfaced for
	// engineering review before becoming canonical.
	sinkNotion := governance.NewSinkConfig(governance.SinkKindNotion, "notion-acme").
		WithPolicy(governance.EditPolicyRequireReviewBeforePromote)

	// Verify effective policies.
	if sinkGit.EffectivePolicy() != governance.EditPolicyPromoteToCanonical {
		t.Errorf("git_repo effective policy wrong: %v", sinkGit.EffectivePolicy())
	}
	if sinkConfluence.EffectivePolicy() != governance.EditPolicyLocalToSink {
		t.Errorf("confluence effective policy wrong: %v", sinkConfluence.EffectivePolicy())
	}
	if sinkNotion.EffectivePolicy() != governance.EditPolicyRequireReviewBeforePromote {
		t.Errorf("notion effective policy wrong: %v", sinkNotion.EffectivePolicy())
	}

	// --- Edit 1: git_repo → promote_to_canonical ---
	// A git PR was merged, so the edit goes directly to canonical.
	gitContent := paraContent("Git-reviewed canonical content.")
	var err error
	canonical, _, err = governance.PromoteToCanonical(
		ctx, canonical, sinkGit.Name, "git-user", "b001", gitContent, log,
	)
	if err != nil {
		t.Fatalf("git promote: %v", err)
	}
	// Canonical now has human-edited content from git.
	if canonical.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("after git promote: owner = %v, want human-edited", canonical.Blocks[0].Owner)
	}
	if canonical.Blocks[0].Content.Paragraph.Markdown != "Git-reviewed canonical content." {
		t.Errorf("after git promote: content wrong: %q", canonical.Blocks[0].Content.Paragraph.Markdown)
	}

	// --- Edit 2: confluence → local_to_sink ---
	// Confluence editor modifies b001; per local_to_sink the edit stays in overlay.
	confluenceContent := paraContent("Confluence product-team edit.")
	confluenceOverlay := ast.SinkOverlay{
		SinkName:   "confluence-acme-space",
		PageID:     "arch.auth",
		Blocks:     map[ast.BlockID]ast.BlockContent{"b001": confluenceContent},
		Provenance: map[ast.BlockID]ast.OverlayMeta{"b001": {EditedBy: "pm@acme.com", EditedAt: time.Now()}},
	}
	// Set divergence flag on canonical.
	canonical.Blocks[0].SinkDivergence = map[ast.SinkName]bool{"confluence-acme-space": true}

	// Canonical content must be unchanged — the edit is local to the overlay.
	if canonical.Blocks[0].Content.Paragraph.Markdown != "Git-reviewed canonical content." {
		t.Errorf("after confluence local edit: canonical content changed unexpectedly")
	}
	// Sink output = canonical + overlay.
	confluencePage := ast.ComposeForSink(canonical, confluenceOverlay)
	if confluencePage.Blocks[0].Content.Paragraph.Markdown != "Confluence product-team edit." {
		t.Errorf("confluence sink output wrong: %q", confluencePage.Blocks[0].Content.Paragraph.Markdown)
	}
	// Other sinks get canonical.
	if canonical.Blocks[0].SinkDivergence["confluence-acme-space"] != true {
		t.Error("SinkDivergence should be set for confluence")
	}

	// --- Edit 3: notion → require_review_before_promote ---
	// Notion editor modifies b001. A sync-PR is opened; engineer merges it.
	notionContent := paraContent("Notion editor's version — approved.")
	notionOverlay := makeOverlay("notion-acme", "arch.auth", "b001", notionContent)

	canonical, notionOverlay, err = governance.ResolveSyncPR(
		ctx, canonical, notionOverlay, "notion-acme", "notion-user", "b001",
		governance.SyncPRDecisionMerge, "eng-reviewer@acme.com", log,
	)
	if err != nil {
		t.Fatalf("notion sync-PR merge: %v", err)
	}

	// Canonical must now have the Notion-approved content.
	if canonical.Blocks[0].Content.Paragraph.Markdown != "Notion editor's version — approved." {
		t.Errorf("after notion merge: canonical content wrong: %q", canonical.Blocks[0].Content.Paragraph.Markdown)
	}
	if canonical.Blocks[0].Owner != ast.OwnerHumanEdited {
		t.Errorf("after notion merge: canonical owner wrong: %v", canonical.Blocks[0].Owner)
	}
	// Notion overlay must be cleared.
	if _, inOverlay := notionOverlay.Blocks["b001"]; inOverlay {
		t.Error("after notion merge: notion overlay should be cleared")
	}

	// Confluence overlay is still active — its local edit is unaffected by the notion promotion.
	if _, inConfluence := confluenceOverlay.Blocks["b001"]; !inConfluence {
		t.Error("confluence overlay should still be present after notion merge")
	}

	// --- Verify audit trail ---
	allEntries, _ := log.Query(ctx, governance.AuditFilter{})
	// Entries: git promote (1) + notion merge via PromoteToCanonical (1) + sync_pr_merge (1) = 3
	if len(allEntries) != 3 {
		t.Errorf("expected 3 audit entries total, got %d: %+v", len(allEntries), allEntries)
	}

	gitEntries, _ := log.Query(ctx, governance.AuditFilter{SourceSink: string(sinkGit.Name)})
	if len(gitEntries) != 1 {
		t.Errorf("expected 1 git audit entry, got %d", len(gitEntries))
	}

	notionEntries, _ := log.Query(ctx, governance.AuditFilter{SourceSink: string(sinkNotion.Name)})
	if len(notionEntries) != 2 { // promote_to_canonical + sync_pr_merge
		t.Errorf("expected 2 notion audit entries, got %d", len(notionEntries))
	}

	// Confluence made no governance action (edit is local_to_sink — no audit entry).
	confluenceEntries, _ := log.Query(ctx, governance.AuditFilter{SourceSink: string(sinkConfluence.Name)})
	if len(confluenceEntries) != 0 {
		t.Errorf("confluence local_to_sink should generate no audit entries, got %d", len(confluenceEntries))
	}
}
