// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package changewatch implements the in-process change-watch feedback
// loop (Phase 1.C of the MCP-edits plan,
// thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md).
//
// The package owns three pieces:
//
//   - ChangeEvent — the canonical schema connectors emit. Stable
//     across all current and future connectors; versioned at
//     ChangeEventSchemaVersion. Phase 1 ships 0.1 (internal/unstable);
//     Phase 2 promotes to 1.0 after the schema-stability checkpoint.
//
//   - Router — the single entry point all connectors funnel through.
//     The router enforces three runtime guardrails (non-empty delta,
//     bounded work, branch consistency), runs the per-(repo, source.kind)
//     rate limiter and the per-repo aggregate circuit breaker, and
//     dispatches accepted events to the per-file IndexFiles delta
//     entry point + the existing impact-application helper.
//
//   - Watcher — an fsnotify-driven connector that watches indexed
//     working trees, debounces by mode, filters via git.IsIgnoredPath,
//     and emits ChangeEvents to the router.
//
// The package is the architectural keystone for the closed loop: every
// connector (passive fsnotify, in-process record_change, future GitHub
// webhook + App, future LSP/IDE) feeds into the same router with the
// same canonical event shape and the same delta-only invariant. The
// router is the only place that can violate the invariant, so the only
// place we need to assert it.
//
// Package boundary discipline: this package depends on internal/git,
// internal/indexer, internal/graph (aliased graphstore everywhere it's
// consumed). It MUST NOT depend on internal/api/graphql or
// internal/api/rest — those packages depend on us, not the other way
// around. Cross-package coupling to applyImpactFromChange (the
// post-impact helper that lives on *mutationResolver) goes through the
// ImpactApplier interface defined here, with the resolver-side adapter
// wired at server-assembly time in internal/api/rest/router.go.
package changewatch

import (
	"errors"
	"time"
)

// ChangeEventSchemaVersion is the wire-format version of ChangeEvent.
//
// Phase 1 ships "0.1" — internal/unstable. No external consumer should
// depend on this contract until Phase 2 promotes it to "1.0" after
// the schema-stability checkpoint passes (see plan §Connector API +
// reference connectors > Versioning trajectory).
//
// Versioning policy (active from "1.0" onward):
//   - additive (new optional fields, new enum values with documented
//     unknown-handling) is a minor bump;
//   - breaking (removed/renamed fields, type changes) is a major bump
//     and requires a parallel-version deprecation window.
const ChangeEventSchemaVersion = "0.1"

// ChangeEvent is the canonical wire shape every connector produces and
// the router accepts. JSON-marshalable; the same struct is used by the
// in-process connectors (Watcher, in-process record_change) and by the
// HTTP ingress endpoint (lands in 1.D).
//
// Field ordering and JSON tags match the schema documented in the plan.
type ChangeEvent struct {
	// SchemaVersion is the wire-format version. Connectors stamp this
	// to ChangeEventSchemaVersion; the router validates the major
	// component and rejects events with unknown major versions.
	SchemaVersion string `json:"schema_version"`

	// EventID is a connector-generated stable ID (e.g. ULID). The router
	// uses it as the dedup/idempotency key inside the dedup window so
	// retries collapse. Required.
	EventID string `json:"event_id"`

	// RepositoryID is the SourceBridge-side repo identifier the change
	// applies to. The router resolves this against the indexed-repo set;
	// unknown ids are rejected. Required.
	RepositoryID string `json:"repository_id"`

	// OccurredAt is when the change happened upstream (the connector's
	// best knowledge — fsnotify uses inotify time, record_change uses
	// the agent's wall-clock, GitHub uses the webhook delivery
	// timestamp). Required.
	OccurredAt time.Time `json:"occurred_at"`

	// ReceivedAt is when ingress accepted the event. Stamped by the
	// router on entry; connectors must not set it. Used for
	// staleness/skew telemetry and for the dedup window.
	ReceivedAt time.Time `json:"received_at,omitempty"`

	// Branch is the git ref the change applies to (e.g. "refs/heads/main"
	// or a bare branch name). The router validates this against
	// git.HeadRef(repoPath) at dispatch time; on mismatch the event is
	// rejected with rejected_branch_mismatch (Risk #4 / HIGH fix #6 from
	// the plan v5).
	Branch string `json:"branch"`

	// BeforeRef and AfterRef are the parent/new commit/state when the
	// connector knows them. Optional in 1.C (fsnotify doesn't know;
	// record_change typically doesn't know unless the agent provided
	// them). GitHub webhooks fill them in 2.x.
	BeforeRef string `json:"before_ref,omitempty"`
	AfterRef  string `json:"after_ref,omitempty"`

	// Files is the declared delta. Required, non-empty (the load-bearing
	// guardrail #1 from the delta-only invariant). A connector that
	// cannot determine the delta MUST NOT post the event — see the plan
	// fallback policy.
	Files []FileChange `json:"files"`

	// Source is the attribution block. Required.
	Source ChangeSource `json:"source"`

	// Trust is the verification block populated by ingress. Connectors
	// must not set Trust fields directly; the router populates them
	// based on the connector's auth context (HMAC/JWT/mTLS for HTTP
	// ingress in 1.D; in-process trusted-by-construction for the
	// in-process connectors in 1.C).
	Trust Trust `json:"trust"`
}

// FileChange is one element of the declared delta.
type FileChange struct {
	// Path is repo-relative, Unix-separators, no leading ./ or /,
	// no .. components. Validation lives at ingress (HTTP and the
	// in-process record_change tool). The router enforces the contract
	// regardless of source so violations are caught before the delta
	// reaches IndexFiles.
	Path string `json:"path"`

	// Status is one of: added, modified, deleted, renamed.
	Status FileChangeStatus `json:"status"`

	// OldPath is set iff Status == FileChangeRenamed.
	OldPath string `json:"old_path,omitempty"`

	// ContentHashAfter is the SHA256 of the post-edit file bytes when
	// the connector can compute it cheaply. Used by the router's
	// content-hash dedup window so fsnotify and record_change observing
	// the same edit collapse to one routed event.
	ContentHashAfter string `json:"content_hash_after,omitempty"`
}

// FileChangeStatus enumerates the shape of a single file change.
type FileChangeStatus string

// Recognized FileChangeStatus values. Unknown values are rejected at
// router validation time.
const (
	FileChangeAdded    FileChangeStatus = "added"
	FileChangeModified FileChangeStatus = "modified"
	FileChangeDeleted  FileChangeStatus = "deleted"
	FileChangeRenamed  FileChangeStatus = "renamed"
)

// IsValid reports whether the status is one of the four recognized
// values.
func (s FileChangeStatus) IsValid() bool {
	switch s {
	case FileChangeAdded, FileChangeModified, FileChangeDeleted, FileChangeRenamed:
		return true
	}
	return false
}

// ChangeSource is the attribution block. The router and downstream
// surfaces (impact reports, freshness envelopes) read these fields to
// answer "who changed what."
type ChangeSource struct {
	// Kind enumerates which connector produced the event.
	Kind SourceKind `json:"kind"`

	// ConnectorID is the per-installation identifier for the connector.
	// In 1.C the in-process connectors use stable string identifiers
	// ("in_process:fsnotify", "in_process:record_change"); 1.D adds
	// per-installation IDs for HTTP-ingress connectors.
	ConnectorID string `json:"connector_id,omitempty"`

	// Actor is set by ingress based on the connector's auth context;
	// connectors must not set this from the event body. Format:
	//   human:user@org | agent:tool | ci:system | unknown
	Actor string `json:"actor,omitempty"`

	// ActorDisplay is a UI-friendly label. Optional.
	ActorDisplay string `json:"actor_display,omitempty"`

	// Intent is free-text from record_change ("refactor extract method";
	// "implement requirement REQ-42"). Optional.
	Intent string `json:"intent,omitempty"`

	// RequirementIDs let record_change attribute the change to one or
	// more requirements for downstream review (Phase 2 compound tools).
	// Optional.
	RequirementIDs []string `json:"requirement_ids,omitempty"`
}

// SourceKind enumerates which connector type produced the event.
type SourceKind string

// Recognized SourceKind values. The set grows with each phase as new
// connectors are added; the router rejects unknown kinds in 1.C.
const (
	// SourceKindFsnotifyLocal — passive fsnotify watcher (in-process,
	// 1.C). Detects out-of-band edits (CLI, IDE, git pull, etc.).
	SourceKindFsnotifyLocal SourceKind = "fsnotify_local"

	// SourceKindMCPRecordChange — in-process record_change MCP tool
	// (1.D ships the public tool; 1.C accepts the same kind from
	// direct router callers used by tests).
	SourceKindMCPRecordChange SourceKind = "mcp_record_change"
)

// IsValid reports whether the kind is one of the recognized 1.C
// values. Future phases will add more cases (github_webhook,
// gitlab_webhook, ci_hook, ide_plugin, lsp_bridge, agent_native,
// manual_admin, etc.).
func (k SourceKind) IsValid() bool {
	switch k {
	case SourceKindFsnotifyLocal, SourceKindMCPRecordChange:
		return true
	}
	return false
}

// Trust is the ingress-populated verification block. Connectors must
// not set these fields from the event body.
type Trust struct {
	// Verified is true when the connector's auth check passed
	// (HMAC/JWT/mTLS for HTTP ingress; trusted-by-construction for
	// the in-process connectors).
	Verified bool `json:"verified"`

	// VerificationMethod records which auth scheme was applied (for
	// telemetry / audit). Examples: "hmac-sha256", "jwt", "mtls",
	// "in_process".
	VerificationMethod string `json:"verification_method,omitempty"`

	// ReceivedVia records the transport ("http", "in_process").
	ReceivedVia string `json:"received_via,omitempty"`
}

// SubmitOutcome is the disposition the router returns for an accepted
// (or rejected) ChangeEvent. The HTTP ingress in 1.D maps these to the
// 202-payload routed_to field; the in-process callers (Watcher) use
// them for telemetry.
type SubmitOutcome string

// Recognized SubmitOutcome values.
const (
	// OutcomeIndexing — the event passed validation and dispatch is
	// running (or queued); the caller should expect impact-report
	// side effects shortly.
	OutcomeIndexing SubmitOutcome = "indexing"

	// OutcomeDeduped — the event is a duplicate of one inside the
	// dedup window; the prior event is the canonical one.
	OutcomeDeduped SubmitOutcome = "deduped"

	// OutcomeRateLimited — the per-(repo, source.kind) rate limit
	// rejected this event. The connector should back off.
	OutcomeRateLimited SubmitOutcome = "rate_limited"

	// OutcomeBreakerTripped — the per-repo aggregate breaker is open;
	// the router pauses all events for this repo until the breaker
	// recovers.
	OutcomeBreakerTripped SubmitOutcome = "breaker_tripped"

	// OutcomeRejectedNoDelta — guardrail #1 fired (empty Files[]).
	OutcomeRejectedNoDelta SubmitOutcome = "rejected_no_delta"

	// OutcomeRejectedInvalidPaths — one or more Files[].Path entries
	// failed the path-normalization contract.
	OutcomeRejectedInvalidPaths SubmitOutcome = "rejected_invalid_paths"

	// OutcomeRejectedBranchMismatch — Branch != git.HeadRef at dispatch
	// (Risk #4 / HIGH fix #6 from the plan v5). Both branches are
	// recorded in the structured log.
	OutcomeRejectedBranchMismatch SubmitOutcome = "rejected_branch_mismatch"

	// OutcomeRejectedSchema — schema validation failed (unknown major
	// version, missing required field, unknown enum value).
	OutcomeRejectedSchema SubmitOutcome = "rejected_schema"

	// OutcomeRejectedUnknownRepo — RepositoryID does not resolve.
	OutcomeRejectedUnknownRepo SubmitOutcome = "rejected_unknown_repo"
)

// Validate enforces the schema-level contract on the event. Used by
// the router as the first guardrail (non-empty delta + path
// normalization + recognized enum values + required fields). Returns
// nil on success; the matching SubmitOutcome on rejection.
//
// Validate is intentionally side-effect-free and does no I/O. The
// router runs Validate before any rate-limit / dedup / branch checks
// so a malformed event is rejected as cheaply as possible.
func (e *ChangeEvent) Validate() (SubmitOutcome, error) {
	if e == nil {
		return OutcomeRejectedSchema, errors.New("nil event")
	}
	// Schema major-version check. We only ship 0.x in Phase 1; an event
	// claiming a major we don't recognize is rejected. The minor part is
	// ignored because additive minor bumps must remain compatible.
	if e.SchemaVersion == "" {
		return OutcomeRejectedSchema, errors.New("schema_version required")
	}
	major, err := schemaMajor(e.SchemaVersion)
	if err != nil {
		return OutcomeRejectedSchema, err
	}
	if major != 0 && major != 1 {
		// 0.x is internal/unstable; 1.0+ is the post-checkpoint contract.
		// Anything else is from the future and we don't yet handle it.
		return OutcomeRejectedSchema, ErrUnsupportedSchemaMajor
	}
	if e.EventID == "" {
		return OutcomeRejectedSchema, errors.New("event_id required")
	}
	if e.RepositoryID == "" {
		return OutcomeRejectedSchema, errors.New("repository_id required")
	}
	if e.Branch == "" {
		return OutcomeRejectedSchema, errors.New("branch required")
	}
	if e.OccurredAt.IsZero() {
		return OutcomeRejectedSchema, errors.New("occurred_at required")
	}
	if !e.Source.Kind.IsValid() {
		return OutcomeRejectedSchema, ErrUnknownSourceKind
	}
	if len(e.Files) == 0 {
		return OutcomeRejectedNoDelta, ErrEmptyDelta
	}
	for i := range e.Files {
		if !e.Files[i].Status.IsValid() {
			return OutcomeRejectedSchema, ErrUnknownFileStatus
		}
		if err := validateRelPath(e.Files[i].Path); err != nil {
			return OutcomeRejectedInvalidPaths, err
		}
		if e.Files[i].Status == FileChangeRenamed {
			if e.Files[i].OldPath == "" {
				return OutcomeRejectedSchema, errors.New("renamed file: old_path required")
			}
			if err := validateRelPath(e.Files[i].OldPath); err != nil {
				return OutcomeRejectedInvalidPaths, err
			}
		}
	}
	return OutcomeIndexing, nil
}
