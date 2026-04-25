// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package ast defines the canonical Page AST for the living-wiki feature
// (Workstream D.2). Every generated wiki page is internally represented as
// a [Page] containing a slice of typed [Block] values. The AST is the
// source of truth from which all sink adapters (markdown, Confluence, Notion)
// render output.
//
// # Two stored states
//
// Each page has two distinct stored states:
//
//   - [ASTKindCanonical] — the merged-and-published state, updated only when
//     a wiki PR merges.
//   - [ASTKindProposed] — the in-flight state on the open wiki PR branch.
//     Reflects bot regenerations + reviewer edits. Discarded on PR rejection.
//     Promoted to canonical on PR merge (via [Promote]).
//
// # Block ownership
//
// Every block carries an [Owner] that records whether SourceBridge generated
// the content or a human edited it. The ownership determines whether the
// block will be overwritten on the next regeneration. See the Owner validity
// table on [Owner] for which states are legal in each AST kind.
//
// # Block identity
//
// Block IDs are stable and sticky to logical position. See [GenerateBlockID]
// for the derivation rules. The reconciliation algorithm ([ReconcileBlocks])
// attempts to preserve IDs across regenerations using exact-ID match,
// content-fingerprint match, and structural-anchor match, in that order.
//
// # Sink overlays
//
// Per-sink content divergence is tracked in [SinkOverlay] alongside the
// canonical AST. Use [ComposeForSink] to compute the effective content for a
// specific sink.
package ast

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
)

// ASTKind discriminates canonical vs proposed state.
type ASTKind string

const (
	// ASTKindCanonical is the merged-and-published wiki state.
	ASTKindCanonical ASTKind = "canonical"

	// ASTKindProposed is the in-flight state on the open wiki PR branch.
	ASTKindProposed ASTKind = "proposed"
)

// Owner records who last set the content of a block.
//
// Validity by AST kind:
//
//	Owner                      | canonical | proposed
//	OwnerGenerated             |    yes    |   yes
//	OwnerHumanEdited           |    yes    |   yes
//	OwnerHumanEditedOnPRBranch |    no     |   yes
//	OwnerHumanOnly             |    yes    |   yes
type Owner string

const (
	// OwnerGenerated means SourceBridge last wrote this block and it
	// matches the last-known auto content. Safe to overwrite on regen.
	OwnerGenerated Owner = "generated"

	// OwnerHumanEdited means a human edited this block after it was
	// merged to canonical. SourceBridge will not overwrite it.
	OwnerHumanEdited Owner = "human-edited"

	// OwnerHumanEditedOnPRBranch means a reviewer edited this block on
	// the open PR branch. Valid only in proposed_ast. Becomes
	// OwnerHumanEdited when the PR merges.
	OwnerHumanEditedOnPRBranch Owner = "human-edited-on-pr-branch"

	// OwnerHumanOnly marks blocks wrapped in <!-- sourcebridge:freeform -->.
	// SourceBridge never touches these blocks in any context.
	OwnerHumanOnly Owner = "human-only"
)

// ValidIn reports whether this ownership state is valid within the given
// AST kind. An invalid state indicates a data integrity problem.
func (o Owner) ValidIn(kind ASTKind) bool {
	switch o {
	case OwnerGenerated:
		return true // valid in both
	case OwnerHumanEdited:
		return true // valid in both
	case OwnerHumanEditedOnPRBranch:
		return kind == ASTKindProposed // proposed only
	case OwnerHumanOnly:
		return true // valid in both
	}
	return false // unknown owner
}

// BlockKind is the type tag for a Block. The set is an explicit enum so
// sink adapters can handle each kind with a type-safe switch. Unknown kinds
// encountered in stored data should fall back to BlockKindFreeform.
type BlockKind string

const (
	BlockKindHeading    BlockKind = "heading"
	BlockKindParagraph  BlockKind = "paragraph"
	BlockKindCode       BlockKind = "code"
	BlockKindTable      BlockKind = "table"
	BlockKindCallout    BlockKind = "callout"
	BlockKindEmbed      BlockKind = "embed"
	BlockKindFreeform   BlockKind = "freeform"
	BlockKindStaleBanner BlockKind = "stale_banner"
)

// SinkName is the identifier for a sink integration (e.g. "confluence-acme-space").
type SinkName string

// BlockID is the stable identifier for a block within a page.
type BlockID string

// BlockContent is the typed payload for a block. Exactly one of the fields
// is non-nil depending on the block's Kind. The sealed-interface pattern is
// not available in Go, so callers must use Kind as the discriminant and
// treat the wrong field being non-nil as a data error.
type BlockContent struct {
	// Heading is populated when Kind == BlockKindHeading.
	Heading *HeadingContent
	// Paragraph is populated when Kind == BlockKindParagraph.
	Paragraph *ParagraphContent
	// Code is populated when Kind == BlockKindCode.
	Code *CodeContent
	// Table is populated when Kind == BlockKindTable.
	Table *TableContent
	// Callout is populated when Kind == BlockKindCallout.
	Callout *CalloutContent
	// Embed is populated when Kind == BlockKindEmbed.
	Embed *EmbedContent
	// Freeform is populated when Kind == BlockKindFreeform.
	Freeform *FreeformContent
	// StaleBanner is populated when Kind == BlockKindStaleBanner.
	StaleBanner *StaleBannerContent
}

// HeadingContent is the payload for a heading block.
type HeadingContent struct {
	// Level is the heading depth: 1–6.
	Level int
	// Text is the plain-text heading title without markup.
	Text string
}

// ParagraphContent is the payload for a paragraph block.
type ParagraphContent struct {
	// Markdown is the paragraph body in CommonMark markdown.
	Markdown string
}

// CodeContent is the payload for a fenced code block.
type CodeContent struct {
	// Language is the syntax-highlighting hint (e.g. "go", "bash").
	// May be empty.
	Language string
	// Body is the raw code without fence markers.
	Body string
}

// TableContent is the payload for a markdown table block.
type TableContent struct {
	// Headers is the ordered list of column header strings.
	Headers []string
	// Rows is the list of rows; each row has the same length as Headers.
	Rows [][]string
}

// CalloutContent is the payload for an alert/callout block.
type CalloutContent struct {
	// Kind classifies the callout: "note", "warning", "tip", "danger".
	Kind string
	// Body is the callout prose in markdown.
	Body string
}

// EmbedContent is the payload for an embedded reference block (e.g. a
// transcluded subsection from another page).
type EmbedContent struct {
	// TargetPageID is the page from which content is embedded.
	TargetPageID string
	// TargetBlockID is the specific block, or empty for the whole page.
	TargetBlockID BlockID
}

// FreeformContent is the payload for a human-only freeform block.
// SourceBridge never modifies blocks with this content type.
type FreeformContent struct {
	// Raw is the raw markdown exactly as written by the human.
	Raw string
}

// StaleBannerContent is the payload for a stale-banner block injected by
// the stale-detection subsystem (A1.P7). Rendered as visible content per
// sink — a blockquote in markdown, an info macro in Confluence, a callout
// in Notion.
type StaleBannerContent struct {
	// TriggeringCommit is the commit SHA that caused staleness.
	TriggeringCommit string
	// TriggeringSymbols are the symbol names that changed.
	TriggeringSymbols []string
	// ConditionKind is "signature_change_in" or "new_caller_added_to".
	ConditionKind string
	// RefreshURL is a link to the SourceBridge UI for ad-hoc regen.
	RefreshURL string
	// NextRegenWindow is a human-readable description of the next scheduled
	// regeneration window.
	NextRegenWindow string
}

// BlockChange records the last mutation to a block.
type BlockChange struct {
	// SHA is the git commit SHA of the source change that caused this block
	// to be written (for generated blocks) or the wiki-branch SHA where the
	// human edit landed.
	SHA string
	// Timestamp is when the change was recorded.
	Timestamp time.Time
	// Source describes who made the change: "sourcebridge" for bot-generated
	// content, a user identifier for human edits.
	Source string
}

// Block is one typed node in a page's AST.
type Block struct {
	// ID is the stable identifier for this block. Once assigned it never
	// changes for the lifetime of the page (barring explicit migrations).
	ID BlockID

	// Kind is the type tag that discriminates Content.
	Kind BlockKind

	// Content holds the typed payload for this block. The field matching
	// Kind must be non-nil; all others must be nil.
	Content BlockContent

	// Owner records who last set the content.
	Owner Owner

	// LastChange records when and by whom the block was last modified.
	LastChange BlockChange

	// SinkDivergence tracks which sinks have a per-sink overlay that differs
	// from canonical content. The actual divergent content lives in the
	// SinkOverlay associated with that sink. A true value means "this sink
	// has diverged from canonical for this block."
	SinkDivergence map[SinkName]bool
}

// Provenance records how a page was generated.
type Provenance struct {
	// GeneratedAt is the wall-clock time the page was generated.
	GeneratedAt time.Time
	// GeneratedBySHA is the source commit SHA that triggered generation.
	GeneratedBySHA string
	// ModelID is the LLM model used for generation, or empty for zero-LLM pages.
	ModelID string
}

// Page is the complete in-memory representation of one living-wiki page.
type Page struct {
	// ID matches DependencyManifest.PageID. Primary key for overlay storage.
	ID string

	// Manifest is the dependency manifest parsed from the page's frontmatter.
	Manifest manifest.DependencyManifest

	// Blocks is the ordered list of content blocks.
	Blocks []Block

	// Provenance records generation metadata.
	Provenance Provenance
}

// OverlayMeta records who diverged a block in a specific sink.
type OverlayMeta struct {
	// EditedBy is the sink's user identifier for the human who made the edit.
	EditedBy string
	// EditedAt is when the edit was detected.
	EditedAt time.Time
	// LastSyncSHA is the canonical SHA at the time the edit was detected.
	// Used to detect whether canonical has advanced past the overlay.
	LastSyncSHA string
}

// SinkOverlay stores the sparse per-sink content overrides for one page.
// Only blocks that differ from canonical content appear here. Blocks absent
// from the overlay use canonical content.
//
// Stored alongside the canonical AST, keyed by sink integration ID.
// Deleted atomically when the sink integration is disconnected.
type SinkOverlay struct {
	// SinkName identifies the integration (e.g. "confluence-acme-space").
	SinkName SinkName
	// PageID matches the page this overlay belongs to.
	PageID string
	// Blocks is the sparse map of block ID → divergent block content.
	Blocks map[BlockID]BlockContent
	// Provenance records metadata about each divergent block's edit.
	Provenance map[BlockID]OverlayMeta
}

// MigrationKind is the type of block-level structural change.
type MigrationKind string

const (
	MigrationMoved   MigrationKind = "moved"
	MigrationSplit   MigrationKind = "split"
	MigrationMerged  MigrationKind = "merged"
	MigrationRenamed MigrationKind = "renamed"
)

// BlockMigration records one structural change to a block during a regen pass.
// The migration log is committed alongside the page changes so reviewers can
// see which blocks moved, split, or merged.
type BlockMigration struct {
	// Op is the type of migration.
	Op MigrationKind
	// FromID is the block that was restructured.
	FromID BlockID
	// ToIDs is the set of resulting block IDs. For split, multiple IDs.
	// For move/rename, exactly one ID. For merge, the surviving ID.
	ToIDs []BlockID
	// Rationale is a human-readable explanation for PR descriptions.
	Rationale string
}

// MigrationLog accumulates BlockMigration ops during one regen pass.
type MigrationLog struct {
	Migrations []BlockMigration
}

// Add appends a migration to the log.
func (ml *MigrationLog) Add(m BlockMigration) {
	ml.Migrations = append(ml.Migrations, m)
}

// GenerateBlockID derives a deterministic block ID from the parent heading
// path and the 0-based sibling ordinal of the same Kind at this nesting
// level. Once assigned this ID never changes; subsequent regens look it up
// via [ReconcileBlocks] rather than re-deriving it.
//
// The derivation is: sha256(pageID + "/" + headingPath + "/" + kind + "/" + ordinal),
// truncated to 12 hex characters and prefixed with "b".
//
// Examples:
//
//	GenerateBlockID("arch.auth", "Authentication", BlockKindParagraph, 0) → "b3f7a1..."
func GenerateBlockID(pageID, headingPath string, kind BlockKind, ordinal int) BlockID {
	raw := fmt.Sprintf("%s/%s/%s/%d", pageID, headingPath, kind, ordinal)
	sum := sha256.Sum256([]byte(raw))
	return BlockID("b" + hex.EncodeToString(sum[:])[:12])
}

// ContentFingerprint computes a stable hash of the block content. The hash
// is over normalized content — whitespace is collapsed and heading-marker
// variants are unified — so cosmetic edits do not change the fingerprint.
// This is the fallback matcher in the reconciliation algorithm.
func ContentFingerprint(content BlockContent, kind BlockKind) string {
	normalized := normalizeContent(content, kind)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:16])
}

// normalizeContent serializes block content to a canonical string for
// fingerprinting. Whitespace is collapsed; heading levels are unified to a
// single "#" prefix so "#" vs "##" doesn't change the fingerprint for the
// same heading text.
func normalizeContent(c BlockContent, kind BlockKind) string {
	switch kind {
	case BlockKindHeading:
		if c.Heading != nil {
			return "heading:" + normalizeWhitespace(c.Heading.Text)
		}
	case BlockKindParagraph:
		if c.Paragraph != nil {
			return "paragraph:" + normalizeWhitespace(c.Paragraph.Markdown)
		}
	case BlockKindCode:
		if c.Code != nil {
			return "code:" + c.Code.Language + ":" + normalizeWhitespace(c.Code.Body)
		}
	case BlockKindTable:
		if c.Table != nil {
			var sb strings.Builder
			sb.WriteString("table:")
			sb.WriteString(strings.Join(c.Table.Headers, "|"))
			for _, row := range c.Table.Rows {
				sb.WriteString(";")
				sb.WriteString(strings.Join(row, "|"))
			}
			return sb.String()
		}
	case BlockKindCallout:
		if c.Callout != nil {
			return "callout:" + c.Callout.Kind + ":" + normalizeWhitespace(c.Callout.Body)
		}
	case BlockKindEmbed:
		if c.Embed != nil {
			return "embed:" + c.Embed.TargetPageID + ":" + string(c.Embed.TargetBlockID)
		}
	case BlockKindFreeform:
		if c.Freeform != nil {
			return "freeform:" + c.Freeform.Raw
		}
	case BlockKindStaleBanner:
		if c.StaleBanner != nil {
			return "stale_banner:" + c.StaleBanner.TriggeringCommit
		}
	}
	return fmt.Sprintf("unknown:%s", kind)
}

// normalizeWhitespace collapses runs of whitespace (spaces, tabs, newlines)
// to single spaces and trims leading/trailing whitespace.
func normalizeWhitespace(s string) string {
	var sb strings.Builder
	inSpace := false
	for _, r := range strings.TrimSpace(s) {
		switch r {
		case ' ', '\t', '\r', '\n':
			if !inSpace {
				sb.WriteRune(' ')
				inSpace = true
			}
		default:
			sb.WriteRune(r)
			inSpace = false
		}
	}
	return sb.String()
}

// StructuralAnchor is the structural position of a block within a page:
// the parent heading path + sibling ordinal of the same kind at that level.
// Used as the third-tier fallback in the reconciliation algorithm.
type StructuralAnchor struct {
	HeadingPath string
	Kind        BlockKind
	Ordinal     int
}

// ReconcileResult is the outcome of reconciling one incoming block against
// existing blocks.
type ReconcileResult struct {
	// AssignedID is the block ID assigned to the incoming block.
	AssignedID BlockID
	// MatchKind records how the ID was resolved.
	MatchKind ReconcileMatchKind
	// ReboundFrom is set when MatchKind == ReconcileMatchFingerprint or
	// ReconcileMatchStructural, recording the old ID that was re-linked.
	ReboundFrom BlockID
}

// ReconcileMatchKind is how an ID was resolved during reconciliation.
type ReconcileMatchKind string

const (
	// ReconcileMatchExact means the incoming block has an ID that exactly
	// matches an existing block. No re-linking needed.
	ReconcileMatchExact ReconcileMatchKind = "exact"

	// ReconcileMatchFingerprint means the incoming block's content matched
	// an existing block's fingerprint within the structural window.
	// The old ID was re-linked to the new block.
	ReconcileMatchFingerprint ReconcileMatchKind = "fingerprint"

	// ReconcileMatchStructural means the incoming block matched by structural
	// anchor with high prose similarity. Re-linked with a "rebound" annotation.
	ReconcileMatchStructural ReconcileMatchKind = "structural"

	// ReconcileMatchNew means no existing block matched; this is a new block
	// with a freshly generated ID.
	ReconcileMatchNew ReconcileMatchKind = "new"
)

// existingBlockIndex is a lookup structure built from the current page's blocks
// for use during reconciliation.
type existingBlockIndex struct {
	byID          map[BlockID]int            // blockID → slice index
	byFingerprint map[string][]int           // fingerprint → slice indices
	byAnchor      map[string]int             // anchorKey → slice index
}

func buildIndex(blocks []Block) existingBlockIndex {
	idx := existingBlockIndex{
		byID:          make(map[BlockID]int, len(blocks)),
		byFingerprint: make(map[string][]int),
		byAnchor:      make(map[string]int),
	}
	for i, b := range blocks {
		idx.byID[b.ID] = i
		fp := ContentFingerprint(b.Content, b.Kind)
		idx.byFingerprint[fp] = append(idx.byFingerprint[fp], i)
	}
	return idx
}

func anchorKey(a StructuralAnchor) string {
	return fmt.Sprintf("%s|%s|%d", a.HeadingPath, a.Kind, a.Ordinal)
}

// fingerprintWindow is how many sibling positions in each direction we
// consider "within the structural window" for fingerprint matching.
const fingerprintWindow = 3

// ReconcileBlocks matches incoming blocks (from a regen) against existing
// blocks (from the stored AST) to assign stable IDs.
//
// The algorithm (in priority order):
//  1. Exact ID match — the incoming block already has an ID that exists.
//  2. Content fingerprint match within ±fingerprintWindow sibling positions
//     of the same Kind in the same heading — re-link old ID.
//  3. Structural anchor match (same heading path, same kind, same ordinal)
//     with high prose similarity (approximated by fingerprint here) — re-link
//     with "rebound" annotation.
//  4. No match — generate a new ID; log the original block as deleted.
//
// When a deleted block had Owner == OwnerHumanEdited or
// OwnerHumanEditedOnPRBranch, it is appended to humanEditLost so the caller
// can surface the "human edit at block X may be lost" warning.
//
// incoming: the new blocks from this regen, with Kind and Content set but
// possibly zero IDs. existing: the current page's blocks.
// pageID is used when generating new IDs. anchors must be the same length as
// incoming and provide the structural position of each incoming block.
func ReconcileBlocks(
	pageID string,
	incoming []Block,
	anchors []StructuralAnchor,
	existing []Block,
) (results []ReconcileResult, humanEditLost []Block) {
	if len(incoming) != len(anchors) {
		panic("ast: ReconcileBlocks: incoming and anchors must have the same length")
	}

	existIdx := buildIndex(existing)
	usedIDs := make(map[BlockID]bool)

	results = make([]ReconcileResult, len(incoming))

	for i, blk := range incoming {
		anchor := anchors[i]

		// Step 1: Exact ID match.
		if blk.ID != "" {
			if _, found := existIdx.byID[blk.ID]; found && !usedIDs[blk.ID] {
				usedIDs[blk.ID] = true
				results[i] = ReconcileResult{
					AssignedID: blk.ID,
					MatchKind:  ReconcileMatchExact,
				}
				continue
			}
		}

		// Step 2: Fingerprint match within structural window.
		fp := ContentFingerprint(blk.Content, blk.Kind)
		if candidates, ok := existIdx.byFingerprint[fp]; ok {
			matched := false
			for _, candidateIdx := range candidates {
				candidate := existing[candidateIdx]
				if usedIDs[candidate.ID] {
					continue
				}
				if candidate.Kind != blk.Kind {
					continue
				}
				// Check structural window: ordinal within ±fingerprintWindow.
				// We approximate by counting same-kind sibling positions in the
				// existing and incoming slices.
				siblingDist := siblingDistance(existing, candidateIdx, blk.Kind, i, incoming)
				if siblingDist <= fingerprintWindow {
					usedIDs[candidate.ID] = true
					results[i] = ReconcileResult{
						AssignedID:  candidate.ID,
						MatchKind:   ReconcileMatchFingerprint,
						ReboundFrom: candidate.ID,
					}
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}

		// Step 3: Structural anchor match.
		ak := anchorKey(anchor)
		if candidateIdx, ok := existIdx.byAnchor[ak]; ok {
			candidate := existing[candidateIdx]
			if !usedIDs[candidate.ID] && candidate.Kind == blk.Kind {
				usedIDs[candidate.ID] = true
				results[i] = ReconcileResult{
					AssignedID:  candidate.ID,
					MatchKind:   ReconcileMatchStructural,
					ReboundFrom: candidate.ID,
				}
				continue
			}
		}

		// Step 4: New block — generate a fresh ID.
		newID := GenerateBlockID(pageID, anchor.HeadingPath, blk.Kind, anchor.Ordinal)
		// Avoid collisions with already-used generated IDs by appending a suffix.
		base := newID
		suffix := 0
		for usedIDs[newID] {
			suffix++
			newID = BlockID(string(base) + fmt.Sprintf("_%d", suffix))
		}
		usedIDs[newID] = true
		results[i] = ReconcileResult{
			AssignedID: newID,
			MatchKind:  ReconcileMatchNew,
		}
	}

	// Find existing blocks that were not matched — they are deleted.
	for _, existing := range existing {
		if !usedIDs[existing.ID] {
			if existing.Owner == OwnerHumanEdited || existing.Owner == OwnerHumanEditedOnPRBranch {
				humanEditLost = append(humanEditLost, existing)
			}
		}
	}

	return results, humanEditLost
}

// abs returns the absolute value of n.
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// siblingDistance returns the ordinal distance between an existing block at
// existingIdx (in the existing slice) and an incoming block at incomingIdx
// (in the incoming slice), counting only same-Kind blocks at the same nesting.
// This is a coarse approximation — the caller should validate heading path too
// for production use; here we compare ordinals directly.
func siblingDistance(existing []Block, existingIdx int, kind BlockKind, incomingOrdinal int, incoming []Block) int {
	// Count same-kind ordinal of the existing block.
	existOrdinal := 0
	for i := 0; i < existingIdx; i++ {
		if existing[i].Kind == kind {
			existOrdinal++
		}
	}
	// Count same-kind ordinal of the incoming block.
	incomingKindOrdinal := 0
	for i := 0; i < incomingOrdinal && i < len(incoming); i++ {
		if incoming[i].Kind == kind {
			incomingKindOrdinal++
		}
	}
	return abs(existOrdinal - incomingKindOrdinal)
}

// ComposeForSink returns a copy of canonical with overlay blocks applied.
// Blocks not in the overlay use canonical content unchanged.
// This is the canonical + overlay[sink] composition described in the plan.
func ComposeForSink(canonical Page, overlay SinkOverlay) Page {
	if len(overlay.Blocks) == 0 {
		return canonical
	}

	composed := Page{
		ID:         canonical.ID,
		Manifest:   canonical.Manifest,
		Provenance: canonical.Provenance,
		Blocks:     make([]Block, len(canonical.Blocks)),
	}

	for i, blk := range canonical.Blocks {
		if overrideContent, ok := overlay.Blocks[blk.ID]; ok {
			// Apply the overlay content; preserve all other block fields.
			composed.Blocks[i] = blk
			composed.Blocks[i].Content = overrideContent
		} else {
			composed.Blocks[i] = blk
		}
	}

	return composed
}

// Promote returns the new canonical Page by promoting the proposed Page.
// OwnerHumanEditedOnPRBranch blocks are translated to OwnerHumanEdited.
// All other blocks are taken from proposed as-is.
//
// The returned page has ASTKind semantics of canonical: it will be stored as
// the new canonical_ast.
func Promote(_, proposed Page) Page {
	promoted := Page{
		ID:         proposed.ID,
		Manifest:   proposed.Manifest,
		Provenance: proposed.Provenance,
		Blocks:     make([]Block, len(proposed.Blocks)),
	}

	for i, blk := range proposed.Blocks {
		promoted.Blocks[i] = blk
		if blk.Owner == OwnerHumanEditedOnPRBranch {
			promoted.Blocks[i].Owner = OwnerHumanEdited
		}
	}

	return promoted
}

// Discard returns the canonical Page unchanged when a PR is rejected.
// The proposed state is discarded; this function exists so callers have a
// named operation rather than silently ignoring the proposed AST.
func Discard(canonical, _ Page) Page {
	return canonical
}
