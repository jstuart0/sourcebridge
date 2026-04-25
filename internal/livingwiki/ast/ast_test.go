// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package ast_test

import (
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
)

// --- Owner validity table ---

func TestOwnerValidIn_AllCombinations(t *testing.T) {
	cases := []struct {
		owner        ast.Owner
		kind         ast.ASTKind
		wantValid    bool
	}{
		{ast.OwnerGenerated, ast.ASTKindCanonical, true},
		{ast.OwnerGenerated, ast.ASTKindProposed, true},
		{ast.OwnerHumanEdited, ast.ASTKindCanonical, true},
		{ast.OwnerHumanEdited, ast.ASTKindProposed, true},
		{ast.OwnerHumanEditedOnPRBranch, ast.ASTKindCanonical, false},
		{ast.OwnerHumanEditedOnPRBranch, ast.ASTKindProposed, true},
		{ast.OwnerHumanOnly, ast.ASTKindCanonical, true},
		{ast.OwnerHumanOnly, ast.ASTKindProposed, true},
	}

	for _, tc := range cases {
		got := tc.owner.ValidIn(tc.kind)
		if got != tc.wantValid {
			t.Errorf("Owner(%q).ValidIn(%q) = %v, want %v", tc.owner, tc.kind, got, tc.wantValid)
		}
	}
}

// --- Block ID stability ---

func TestGenerateBlockID_Stability(t *testing.T) {
	// Same inputs must always produce the same ID.
	id1 := ast.GenerateBlockID("arch.auth", "Authentication", ast.BlockKindParagraph, 0)
	id2 := ast.GenerateBlockID("arch.auth", "Authentication", ast.BlockKindParagraph, 0)
	if id1 != id2 {
		t.Errorf("same inputs produced different IDs: %q vs %q", id1, id2)
	}

	// Different inputs must produce different IDs.
	id3 := ast.GenerateBlockID("arch.auth", "Authentication", ast.BlockKindParagraph, 1)
	if id1 == id3 {
		t.Errorf("different ordinal produced same ID: %q", id1)
	}

	id4 := ast.GenerateBlockID("arch.auth", "Authorization", ast.BlockKindParagraph, 0)
	if id1 == id4 {
		t.Errorf("different heading path produced same ID: %q", id1)
	}

	id5 := ast.GenerateBlockID("arch.billing", "Authentication", ast.BlockKindParagraph, 0)
	if id1 == id5 {
		t.Errorf("different pageID produced same ID: %q", id1)
	}

	// IDs must start with "b" and be 13 chars (b + 12 hex).
	if len(id1) != 13 || id1[0] != 'b' {
		t.Errorf("ID format unexpected: %q (len %d)", id1, len(id1))
	}
}

// --- Content fingerprint ---

func TestContentFingerprint_SameContent(t *testing.T) {
	c := ast.BlockContent{
		Paragraph: &ast.ParagraphContent{Markdown: "Hello world."},
	}
	fp1 := ast.ContentFingerprint(c, ast.BlockKindParagraph)
	fp2 := ast.ContentFingerprint(c, ast.BlockKindParagraph)
	if fp1 != fp2 {
		t.Errorf("fingerprint not stable: %q vs %q", fp1, fp2)
	}
}

func TestContentFingerprint_WhitespaceNormalized(t *testing.T) {
	c1 := ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Hello world."}}
	c2 := ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Hello  world."}} // extra space
	fp1 := ast.ContentFingerprint(c1, ast.BlockKindParagraph)
	fp2 := ast.ContentFingerprint(c2, ast.BlockKindParagraph)
	if fp1 != fp2 {
		t.Errorf("whitespace normalization failed: %q vs %q", fp1, fp2)
	}
}

func TestContentFingerprint_DifferentContent(t *testing.T) {
	c1 := ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Hello world."}}
	c2 := ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Goodbye world."}}
	fp1 := ast.ContentFingerprint(c1, ast.BlockKindParagraph)
	fp2 := ast.ContentFingerprint(c2, ast.BlockKindParagraph)
	if fp1 == fp2 {
		t.Error("different content produced same fingerprint")
	}
}

// --- Reconciliation ---

func makeBlock(id ast.BlockID, kind ast.BlockKind, text string, owner ast.Owner) ast.Block {
	var content ast.BlockContent
	switch kind {
	case ast.BlockKindParagraph:
		content = ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: text}}
	case ast.BlockKindHeading:
		content = ast.BlockContent{Heading: &ast.HeadingContent{Level: 2, Text: text}}
	case ast.BlockKindCode:
		content = ast.BlockContent{Code: &ast.CodeContent{Language: "go", Body: text}}
	}
	return ast.Block{ID: id, Kind: kind, Content: content, Owner: owner}
}

func makeAnchor(heading string, kind ast.BlockKind, ordinal int) ast.StructuralAnchor {
	return ast.StructuralAnchor{
		HeadingPath: heading,
		Kind:        kind,
		Ordinal:     ordinal,
	}
}

func TestReconcileBlocks_ExactIDMatch(t *testing.T) {
	existing := []ast.Block{
		makeBlock("bexisting01234", ast.BlockKindParagraph, "Authentication handles JWT.", ast.OwnerGenerated),
	}

	incoming := []ast.Block{
		makeBlock("bexisting01234", ast.BlockKindParagraph, "Authentication handles JWT.", ast.OwnerGenerated),
	}
	anchors := []ast.StructuralAnchor{
		makeAnchor("root", ast.BlockKindParagraph, 0),
	}

	results, lost := ast.ReconcileBlocks("arch.auth", incoming, anchors, existing)
	if len(lost) != 0 {
		t.Errorf("unexpected human edits lost: %v", lost)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].MatchKind != ast.ReconcileMatchExact {
		t.Errorf("expected exact match, got %q", results[0].MatchKind)
	}
	if results[0].AssignedID != "bexisting01234" {
		t.Errorf("unexpected ID: %q", results[0].AssignedID)
	}
}

func TestReconcileBlocks_FingerprintMatch(t *testing.T) {
	oldID := ast.BlockID("bfp000000001")
	existing := []ast.Block{
		makeBlock(oldID, ast.BlockKindParagraph, "Authentication handles JWT.", ast.OwnerGenerated),
	}

	// Incoming has zero ID but same content — fingerprint match.
	incoming := []ast.Block{
		makeBlock("", ast.BlockKindParagraph, "Authentication handles JWT.", ast.OwnerGenerated),
	}
	anchors := []ast.StructuralAnchor{
		makeAnchor("root", ast.BlockKindParagraph, 0),
	}

	results, lost := ast.ReconcileBlocks("arch.auth", incoming, anchors, existing)
	if len(lost) != 0 {
		t.Errorf("unexpected human edits lost: %v", lost)
	}
	if results[0].MatchKind != ast.ReconcileMatchFingerprint {
		t.Errorf("expected fingerprint match, got %q", results[0].MatchKind)
	}
	if results[0].AssignedID != oldID {
		t.Errorf("expected old ID %q, got %q", oldID, results[0].AssignedID)
	}
}

func TestReconcileBlocks_NewBlock(t *testing.T) {
	existing := []ast.Block{}

	incoming := []ast.Block{
		makeBlock("", ast.BlockKindParagraph, "Brand new content.", ast.OwnerGenerated),
	}
	anchors := []ast.StructuralAnchor{
		makeAnchor("root", ast.BlockKindParagraph, 0),
	}

	results, lost := ast.ReconcileBlocks("arch.auth", incoming, anchors, existing)
	if len(lost) != 0 {
		t.Errorf("unexpected human edits lost: %v", lost)
	}
	if results[0].MatchKind != ast.ReconcileMatchNew {
		t.Errorf("expected new match, got %q", results[0].MatchKind)
	}
	if results[0].AssignedID == "" {
		t.Error("expected non-empty assigned ID for new block")
	}
}

func TestReconcileBlocks_DeletionWithHumanEditGuard(t *testing.T) {
	// A human-edited block in existing that has no match in incoming.
	humanBlock := makeBlock("bhuman0000001", ast.BlockKindParagraph, "Human wrote this.", ast.OwnerHumanEdited)
	existing := []ast.Block{humanBlock}

	// Incoming has completely different content — no match.
	incoming := []ast.Block{
		makeBlock("", ast.BlockKindCode, "func main() {}", ast.OwnerGenerated),
	}
	anchors := []ast.StructuralAnchor{
		makeAnchor("root", ast.BlockKindCode, 0),
	}

	_, lost := ast.ReconcileBlocks("arch.auth", incoming, anchors, existing)
	if len(lost) != 1 {
		t.Fatalf("expected 1 lost human-edit block, got %d", len(lost))
	}
	if lost[0].ID != "bhuman0000001" {
		t.Errorf("unexpected lost block ID: %q", lost[0].ID)
	}
}

func TestReconcileBlocks_DeletedGeneratedBlockNotReported(t *testing.T) {
	// A generated block that disappears from regen should not appear in humanEditLost.
	genBlock := makeBlock("bgen000000001", ast.BlockKindParagraph, "Generated content.", ast.OwnerGenerated)
	existing := []ast.Block{genBlock}

	// Incoming has different content — old block deleted but it's generated.
	incoming := []ast.Block{
		makeBlock("", ast.BlockKindCode, "func main() {}", ast.OwnerGenerated),
	}
	anchors := []ast.StructuralAnchor{
		makeAnchor("root", ast.BlockKindCode, 0),
	}

	_, lost := ast.ReconcileBlocks("arch.auth", incoming, anchors, existing)
	if len(lost) != 0 {
		t.Errorf("generated block deletion should not appear in humanEditLost, got %d entries", len(lost))
	}
}

// --- Promote / Discard semantics ---

func TestPromote_TranslatesOwner(t *testing.T) {
	canonical := ast.Page{ID: "arch.auth"}
	proposed := ast.Page{
		ID: "arch.auth",
		Blocks: []ast.Block{
			{ID: "b01", Kind: ast.BlockKindParagraph, Owner: ast.OwnerGenerated,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Generated."}}},
			{ID: "b02", Kind: ast.BlockKindParagraph, Owner: ast.OwnerHumanEditedOnPRBranch,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Human PR edit."}}},
			{ID: "b03", Kind: ast.BlockKindParagraph, Owner: ast.OwnerHumanEdited,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Previous human edit."}}},
		},
	}

	promoted := ast.Promote(canonical, proposed)

	if promoted.Blocks[0].Owner != ast.OwnerGenerated {
		t.Errorf("block 0: expected generated, got %q", promoted.Blocks[0].Owner)
	}
	if promoted.Blocks[1].Owner != ast.OwnerHumanEdited {
		t.Errorf("block 1: HumanEditedOnPRBranch should become HumanEdited, got %q", promoted.Blocks[1].Owner)
	}
	if promoted.Blocks[2].Owner != ast.OwnerHumanEdited {
		t.Errorf("block 2: HumanEdited should stay HumanEdited, got %q", promoted.Blocks[2].Owner)
	}
	// Verify no HumanEditedOnPRBranch survives — it would be invalid in canonical.
	for _, blk := range promoted.Blocks {
		if !blk.Owner.ValidIn(ast.ASTKindCanonical) {
			t.Errorf("promoted block %q has invalid owner %q for canonical", blk.ID, blk.Owner)
		}
	}
}

func TestDiscard_ReturnsCanonical(t *testing.T) {
	canonical := ast.Page{
		ID: "arch.auth",
		Blocks: []ast.Block{
			{ID: "bc01", Kind: ast.BlockKindParagraph, Owner: ast.OwnerGenerated,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Canonical content."}}},
		},
	}
	proposed := ast.Page{
		ID: "arch.auth",
		Blocks: []ast.Block{
			{ID: "bp01", Kind: ast.BlockKindParagraph, Owner: ast.OwnerHumanEditedOnPRBranch,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{Markdown: "Proposed."}}},
		},
	}

	result := ast.Discard(canonical, proposed)
	if len(result.Blocks) != 1 || result.Blocks[0].ID != "bc01" {
		t.Errorf("Discard should return canonical unchanged, got %v", result.Blocks)
	}
}

// --- Sink overlay composition ---

func TestComposeForSink_NoOverlay(t *testing.T) {
	canonical := ast.Page{
		ID: "arch.auth",
		Blocks: []ast.Block{
			makeBlock("b001", ast.BlockKindParagraph, "Original content.", ast.OwnerGenerated),
		},
	}
	overlay := ast.SinkOverlay{
		SinkName: "confluence-acme",
		PageID:   "arch.auth",
		Blocks:   nil,
	}

	result := ast.ComposeForSink(canonical, overlay)
	if len(result.Blocks) != 1 || result.Blocks[0].Content.Paragraph.Markdown != "Original content." {
		t.Error("no overlay: canonical should be unchanged")
	}
}

func TestComposeForSink_PartialOverlay(t *testing.T) {
	canonical := ast.Page{
		ID: "arch.auth",
		Blocks: []ast.Block{
			makeBlock("b001", ast.BlockKindParagraph, "Original para 1.", ast.OwnerGenerated),
			makeBlock("b002", ast.BlockKindParagraph, "Original para 2.", ast.OwnerGenerated),
		},
	}
	overlay := ast.SinkOverlay{
		SinkName: "confluence-acme",
		PageID:   "arch.auth",
		Blocks: map[ast.BlockID]ast.BlockContent{
			"b001": {Paragraph: &ast.ParagraphContent{Markdown: "Confluence-edited para 1."}},
		},
	}

	result := ast.ComposeForSink(canonical, overlay)
	if result.Blocks[0].Content.Paragraph.Markdown != "Confluence-edited para 1." {
		t.Errorf("b001 should use overlay content, got %q", result.Blocks[0].Content.Paragraph.Markdown)
	}
	if result.Blocks[1].Content.Paragraph.Markdown != "Original para 2." {
		t.Errorf("b002 should use canonical content, got %q", result.Blocks[1].Content.Paragraph.Markdown)
	}
}

func TestComposeForSink_AllOverridden(t *testing.T) {
	canonical := ast.Page{
		ID: "arch.auth",
		Blocks: []ast.Block{
			makeBlock("b001", ast.BlockKindParagraph, "Original 1.", ast.OwnerGenerated),
			makeBlock("b002", ast.BlockKindParagraph, "Original 2.", ast.OwnerGenerated),
		},
	}
	overlay := ast.SinkOverlay{
		SinkName: "confluence-acme",
		PageID:   "arch.auth",
		Blocks: map[ast.BlockID]ast.BlockContent{
			"b001": {Paragraph: &ast.ParagraphContent{Markdown: "Override 1."}},
			"b002": {Paragraph: &ast.ParagraphContent{Markdown: "Override 2."}},
		},
	}

	result := ast.ComposeForSink(canonical, overlay)
	if result.Blocks[0].Content.Paragraph.Markdown != "Override 1." {
		t.Errorf("b001 override not applied, got %q", result.Blocks[0].Content.Paragraph.Markdown)
	}
	if result.Blocks[1].Content.Paragraph.Markdown != "Override 2." {
		t.Errorf("b002 override not applied, got %q", result.Blocks[1].Content.Paragraph.Markdown)
	}
}

// Ensure BlockChange fields are usable (just a smoke test on the type).
func TestBlockChange_Fields(t *testing.T) {
	bc := ast.BlockChange{
		SHA:       "abc123",
		Timestamp: time.Now(),
		Source:    "sourcebridge",
	}
	if bc.SHA == "" {
		t.Error("SHA should be set")
	}
}
