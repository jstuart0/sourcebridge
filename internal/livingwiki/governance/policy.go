// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package governance implements the edit-policy framework for the living-wiki
// feature (Workstream G). It defines how out-of-band edits made directly in a
// sink propagate (or don't) back to the canonical AST.
//
// # Three policies
//
//   - [EditPolicyLocalToSink] — the edit stays in the sink's overlay only.
//     Canonical is unchanged; other sinks are unaffected.
//   - [EditPolicyPromoteToCanonical] — the edit is promoted immediately to
//     canonical and propagates to all sinks on next regen.
//   - [EditPolicyRequireReviewBeforePromote] — a sync-PR is opened against the
//     source repo. Engineers review and merge or reject. If merged, the edit
//     becomes canonical. If rejected, the edit falls back to [EditPolicyLocalToSink]
//     for that block only.
//
// # Per-block overrides
//
// A block-level HTML comment `<!-- sourcebridge:promote=local-to-sink -->` in
// the block's content overrides the sink's configured policy for that block.
// Call [BlockPolicyOverride] to parse it.
//
// # Audit trail
//
// Every promotion and every sync-PR disposition must be recorded via [AuditLog].
// The [PromoteToCanonical] and [ResolveSyncPR] functions require a non-nil
// [AuditLog]; pass [MemoryAuditLog] in tests.
package governance

import (
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
)

// EditPolicy controls how an out-of-band sink edit propagates.
type EditPolicy int

const (
	// EditPolicyLocalToSink keeps the edit in the sink's overlay only.
	// The canonical AST is unchanged and no sync-PR is opened.
	// The block is flagged SinkDivergence[sinkName] = true on canonical.
	EditPolicyLocalToSink EditPolicy = iota

	// EditPolicyPromoteToCanonical promotes the edit immediately to the
	// canonical AST. All sinks pick up the change on the next regen.
	EditPolicyPromoteToCanonical

	// EditPolicyRequireReviewBeforePromote opens a sync-PR for engineer
	// review. Merge → canonical; reject → the edit stays local_to_sink
	// for that block in that sink only.
	EditPolicyRequireReviewBeforePromote

	// EditPolicyNotApplicable signals that the sink kind does not accept
	// edits at all (e.g. static_site sinks are read-only outputs).
	EditPolicyNotApplicable
)

// String returns a human-readable name for the policy.
func (p EditPolicy) String() string {
	switch p {
	case EditPolicyLocalToSink:
		return "local_to_sink"
	case EditPolicyPromoteToCanonical:
		return "promote_to_canonical"
	case EditPolicyRequireReviewBeforePromote:
		return "require_review_before_promote"
	case EditPolicyNotApplicable:
		return "not_applicable"
	default:
		return "unknown"
	}
}

// SinkKind classifies the type of a sink integration.
type SinkKind string

const (
	// SinkKindGitRepo is a source-code repository where edits pass PR review.
	SinkKindGitRepo SinkKind = "git_repo"

	// SinkKindGitHubWiki is GitHub's built-in wiki (edits bypass PR review).
	SinkKindGitHubWiki SinkKind = "github_wiki"

	// SinkKindGitLabWiki is GitLab's built-in wiki (edits bypass PR review).
	SinkKindGitLabWiki SinkKind = "gitlab_wiki"

	// SinkKindConfluence is Atlassian Confluence, typically product-edited.
	SinkKindConfluence SinkKind = "confluence"

	// SinkKindNotion is Notion, typically product-edited.
	SinkKindNotion SinkKind = "notion"

	// SinkKindStaticSite covers Backstage TechDocs, MkDocs, Docusaurus, VitePress.
	// These sinks are read-only outputs; edits are not accepted.
	SinkKindStaticSite SinkKind = "static_site"
)

// DefaultPolicy returns the default [EditPolicy] for a given sink kind as
// specified in the plan's G.2 table.
//
// Defaults by kind:
//
//	git_repo     → promote_to_canonical  (PR review already happened)
//	github_wiki  → require_review_before_promote
//	gitlab_wiki  → require_review_before_promote
//	confluence   → local_to_sink
//	notion       → local_to_sink
//	static_site  → not_applicable (read-only sink; edits are not accepted)
func DefaultPolicy(kind SinkKind) EditPolicy {
	switch kind {
	case SinkKindGitRepo:
		return EditPolicyPromoteToCanonical
	case SinkKindGitHubWiki, SinkKindGitLabWiki:
		return EditPolicyRequireReviewBeforePromote
	case SinkKindConfluence, SinkKindNotion:
		return EditPolicyLocalToSink
	case SinkKindStaticSite:
		return EditPolicyNotApplicable
	default:
		// Unknown sink kinds default to the safest policy.
		return EditPolicyLocalToSink
	}
}

// SinkConfig is the per-integration configuration for a sink.
// The EditPolicy field overrides the default for the sink's Kind.
// Customers can configure this per integration in the UI.
type SinkConfig struct {
	// Kind is the category of sink integration.
	Kind SinkKind

	// Name is the unique integration ID, e.g. "confluence-acme-space" or
	// "github-wiki-myorg-myrepo". Used as the SinkName key in overlays.
	Name ast.SinkName

	// EditPolicy overrides the default for this integration. When zero-valued
	// (which equals EditPolicyLocalToSink), callers should use DefaultPolicy(Kind)
	// instead of this field directly — use EffectivePolicy to resolve.
	EditPolicy EditPolicy

	// policyExplicit marks whether EditPolicy was explicitly set by the caller.
	// When false, EffectivePolicy returns DefaultPolicy(Kind).
	policyExplicit bool
}

// NewSinkConfig creates a SinkConfig with the default policy for kind.
func NewSinkConfig(kind SinkKind, name ast.SinkName) SinkConfig {
	return SinkConfig{Kind: kind, Name: name}
}

// WithPolicy returns a copy of c with an explicit EditPolicy override.
func (c SinkConfig) WithPolicy(p EditPolicy) SinkConfig {
	c.EditPolicy = p
	c.policyExplicit = true
	return c
}

// EffectivePolicy returns the policy that applies to this integration.
// If the policy was explicitly set via [WithPolicy], it is returned.
// Otherwise [DefaultPolicy] for the kind is returned.
func (c SinkConfig) EffectivePolicy() EditPolicy {
	if c.policyExplicit {
		return c.EditPolicy
	}
	return DefaultPolicy(c.Kind)
}

// blockPolicyMarker is the prefix of the HTML comment that encodes a
// per-block policy override.
const blockPolicyMarker = "<!-- sourcebridge:promote="

// BlockPolicyOverride parses a per-block policy override from the block's
// content. The override is encoded as an HTML comment of the form:
//
//	<!-- sourcebridge:promote=local-to-sink -->
//
// Only "local-to-sink" is supported as a block-level override; it prevents
// a block from being promoted even when the sink's policy would normally
// promote it. Other values are rejected and ok is false.
//
// Returns (policy, true) when a valid override is found.
// Returns (0, false) when no marker is present or the value is unrecognised.
func BlockPolicyOverride(block ast.Block) (EditPolicy, bool) {
	raw := extractText(block)
	return parseBlockPolicyMarker(raw)
}

// parseBlockPolicyMarker is the internal parser, exposed for unit-testing
// without constructing a full Block.
func parseBlockPolicyMarker(s string) (EditPolicy, bool) {
	idx := strings.Index(s, blockPolicyMarker)
	if idx < 0 {
		return 0, false
	}
	rest := s[idx+len(blockPolicyMarker):]
	end := strings.Index(rest, " -->")
	if end < 0 {
		// Malformed — closing marker missing.
		return 0, false
	}
	value := rest[:end]
	switch value {
	case "local-to-sink":
		return EditPolicyLocalToSink, true
	default:
		// Unrecognised value — treat as absent so unexpected values don't
		// silently apply the wrong policy.
		return 0, false
	}
}

// extractText returns the raw text content of a block for marker scanning.
// For paragraph and freeform blocks this is the markdown body.
// For other kinds it returns an empty string (markers are not meaningful there).
func extractText(block ast.Block) string {
	switch block.Kind {
	case ast.BlockKindParagraph:
		if block.Content.Paragraph != nil {
			return block.Content.Paragraph.Markdown
		}
	case ast.BlockKindFreeform:
		if block.Content.Freeform != nil {
			return block.Content.Freeform.Raw
		}
	case ast.BlockKindCallout:
		if block.Content.Callout != nil {
			return block.Content.Callout.Body
		}
	}
	return ""
}
