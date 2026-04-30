// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package indexer

import "fmt"

// RepoIndexFullReason names the legitimate reasons a caller may
// invoke a whole-tree reindex (IndexRepository). The value is
// load-bearing: the change-watch router added in Phase 1.C must NOT
// reach this code path. Forcing every caller to pass a typed reason
// makes accidental invocation from a change-event surface a compile
// error rather than a silent latency disaster.
//
// The enum is closed: only the constants in this file are valid.
// IndexRepository validates the reason at function entry via
// validateFullReindexReason and refuses with ErrInvalidFullReindexReason
// on any unknown or zero value.
//
// Plan reference: thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md
// v5 — "Audit of latent full-reindex paths" + Phase 1 done-definition
// test #10.
type RepoIndexFullReason int

const (
	// ReasonUnspecified is the zero value. It is NOT a valid reason;
	// passing it (or relying on the default) trips the precondition
	// guard on IndexRepository. Kept as an explicit constant only so
	// validation logs can name it clearly in the refusal message.
	ReasonUnspecified RepoIndexFullReason = iota

	// ReasonInitialOnboard names the path where a brand-new
	// repository is being indexed for the first time. Both the
	// AddRepository GraphQL mutation and the shared indexing.Service
	// (which the MCP `index_repository` tool delegates to) use this
	// reason.
	ReasonInitialOnboard

	// ReasonOperatorRebuild names the path where an operator (a human
	// or a scheduled job acting on behalf of one) explicitly asks for
	// a full reindex. The ReindexRepository GraphQL mutation uses
	// this reason when there is no previous-hash data to drive an
	// incremental scan; the operator surface is the explicit gate.
	ReasonOperatorRebuild
)

// String renders a reason as a stable, log-friendly identifier.
// The strings are part of the audit contract: structured logs and
// tickets reference these names directly, so changes here cascade
// through the audit trail.
func (r RepoIndexFullReason) String() string {
	switch r {
	case ReasonInitialOnboard:
		return "initial_onboard"
	case ReasonOperatorRebuild:
		return "operator_rebuild"
	default:
		return "unspecified"
	}
}

// IsValid reports whether r is one of the named, non-zero reasons.
// The zero value (ReasonUnspecified) is NOT valid.
func (r RepoIndexFullReason) IsValid() bool {
	switch r {
	case ReasonInitialOnboard, ReasonOperatorRebuild:
		return true
	default:
		return false
	}
}

// ErrInvalidFullReindexReason is returned by IndexRepository when its
// reason argument is not one of the valid enum values. It is the
// load-bearing failure mode that prevents a change-event-driven
// caller from accidentally walking the whole tree.
var ErrInvalidFullReindexReason = fmt.Errorf("indexer.IndexRepository: invalid RepoIndexFullReason; must be ReasonInitialOnboard or ReasonOperatorRebuild")

// validateFullReindexReason returns ErrInvalidFullReindexReason
// (wrapped with the offending value's String for log clarity) if r
// is not valid, nil otherwise. IndexRepository calls this at function
// entry before any expensive work begins.
func validateFullReindexReason(r RepoIndexFullReason) error {
	if r.IsValid() {
		return nil
	}
	return fmt.Errorf("%w (got %q)", ErrInvalidFullReindexReason, r.String())
}
