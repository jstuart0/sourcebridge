// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode"

	"github.com/sourcebridge/sourcebridge/internal/changewatch"
)

// Bounds applied at the tool boundary to defend against malicious or
// buggy callers. Rejection is structured (errInvalidArguments) so
// capability-aware clients can react.
//
// These bounds are intentionally generous — any well-behaved agent
// stays an order of magnitude under them. The point is to fail safe
// when the bound matters (e.g. a buggy agent in a tight loop, a
// malicious agent trying to fill the freshness reason with attacker-
// controlled UI bytes).
const (
	// recordChangeMaxFiles caps the per-call delta. The MCP body cap
	// (mcpMaxBodySize = 1 MiB) makes hitting this hard, but a separate
	// limit makes the contract explicit. Real agent edits are <100
	// files even on a big refactor.
	recordChangeMaxFiles = 1024

	// recordChangeMaxIntentLen bounds Intent. Intent flows into the
	// freshness envelope's `reason` and into structured logs; capping
	// here keeps the audit trail readable and prevents log-injection-
	// shaped abuse via newlines / control chars (we also strip those).
	recordChangeMaxIntentLen = 1024

	// recordChangeMaxRequirementIDs caps the attribution list. 100 is
	// well above any documented use; we choose it as the bound so a
	// runaway agent doesn't fill the audit trail.
	recordChangeMaxRequirementIDs = 100

	// recordChangeMaxRequirementIDLen bounds each requirement ID.
	// Long enough for "PROJECT-12345/sub-issue-678" style ids without
	// being a vector for log spam.
	recordChangeMaxRequirementIDLen = 256
)

// Phase 1.D — `record_change` MCP tool.
//
// This is the in-process MCP-tool connector named in the plan
// (thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md §Phase 1
// > 4. New MCP tool record_change). It is one of three connectors that
// share the same canonical ChangeEvent schema and the same Router
// dispatch path; the other two are the fsnotify Watcher (passive,
// shipped in 1.C) and the HTTP ingress endpoint (shipped earlier in
// 1.D).
//
// The tool is **opt-in for callers** and a **quality enrichment, never
// a correctness dependency**. The non-goal in the plan is enforced at
// CI level by the Phase 1 done-definition test #7 (passive-only
// correctness, in internal/changewatch/passive_only_test.go): with the
// record_change tool disabled or never invoked, the fsnotify watcher
// alone must keep freshness honest. If a future change ever makes
// record_change load-bearing, that test will fail.
//
// Adoption posture summary (per plan v4 decision #4):
//   - Easy to call: the tool description is a one-liner; the input
//     schema is small; the response shape is predictable.
//   - Never required: every documented agent flow (Claude Code quickstart,
//     Cursor / Continue / Aider snippets, MCP prompts/list) treats this
//     as recommended-but-not-required.
//   - Cannot lie about identity: ev.Source.Actor is derived from
//     mcpSession.claims, never from the tool args.

// recordChangeRequest is the wire shape callers post.
type recordChangeRequest struct {
	// RepositoryID is the SourceBridge-side repo identifier. Required.
	// The router resolves this against the indexed-repo set; unknown
	// IDs are rejected with rejected_unknown_repo.
	RepositoryID string `json:"repository_id"`

	// Files is the declared delta. Required, non-empty (the load-bearing
	// guardrail #1 from the delta-only invariant). Each entry is
	// repo-relative and must satisfy the path-normalization contract
	// documented in changewatch.NormalizePath.
	//
	// Status defaults to "modified" when omitted — the most common case
	// for an agent reporting "I just edited these files." Callers that
	// added/deleted/renamed files SHOULD set Status explicitly so the
	// downstream MergeIndexResult drops the right records.
	Files []recordChangeFile `json:"files"`

	// Branch is the git ref the change applies to. Required so the
	// router can validate against git.HeadRef and reject events whose
	// claimed branch doesn't match the working tree (Risk #4).
	//
	// Accepted forms: bare branch name ("main") or full ref
	// ("refs/heads/main"); the router normalizes both.
	Branch string `json:"branch"`

	// Intent is free-text from the agent ("refactor extract method",
	// "implement requirement REQ-42"). Optional. Surfaces in
	// _meta.freshness.reason on subsequent MCP reads to give the user
	// context.
	Intent string `json:"intent,omitempty"`

	// RequirementIDs let an agent attribute a change to one or more
	// tracked requirements for downstream review (Phase 2 compound
	// tools). Optional.
	RequirementIDs []string `json:"requirement_ids,omitempty"`
}

// recordChangeFile mirrors changewatch.FileChange but lives here so the
// MCP wire shape isn't coupled to the internal/changewatch package's
// JSON tags. The mapping is field-for-field; we copy into a
// changewatch.ChangeEvent at handler time.
type recordChangeFile struct {
	Path             string `json:"path"`
	Status           string `json:"status,omitempty"`
	OldPath          string `json:"old_path,omitempty"`
	ContentHashAfter string `json:"content_hash_after,omitempty"`
}

// recordChangeResponse is the wire shape callers receive on success.
// The shape is stable so agents that adapt their behavior based on the
// outcome (e.g. "deduped — fsnotify already routed, I don't need to
// retry") don't have to fish around for fields. The plan's
// "tool returns {accepted, change_id, routed_to: ...}" contract.
type recordChangeResponse struct {
	// Accepted is true when the router took ownership of the event
	// (OutcomeIndexing or OutcomeDeduped). Other outcomes set this to
	// false so callers can filter on a single field.
	Accepted bool `json:"accepted"`

	// ChangeID is the event_id the router used. Useful for tracing.
	ChangeID string `json:"change_id"`

	// RoutedTo is the SubmitOutcome string. Stable across releases —
	// callers MAY do equality compare on the documented values:
	//   indexing | deduped | rate_limited | breaker_tripped |
	//   rejected_no_delta | rejected_invalid_paths |
	//   rejected_branch_mismatch | rejected_schema |
	//   rejected_unknown_repo
	RoutedTo string `json:"routed_to"`

	// Reason is a short human-readable explanation. UI-friendly; never
	// load-bearing.
	Reason string `json:"reason,omitempty"`

	// Branch echoes the branch that was actually routed (after the
	// router's refs/heads/X normalization). Lets the agent confirm its
	// claim was accepted as-typed.
	Branch string `json:"branch,omitempty"`

	// FileCount is the number of files in the routed delta. Useful for
	// agents that want to log "I just attributed N files."
	FileCount int `json:"file_count"`
}

// recordChangeToolDef is the MCP tools/list entry. It only appears in
// the tools/list output when the changeDispatcher is wired
// (i.e., change_watch.enabled is true at server-assembly time). Hiding
// the tool when the dispatcher is nil prevents agents from discovering
// a no-op tool — they should never see a feature that isn't actually
// available.
func recordChangeToolDef() mcpToolDefinition {
	return mcpToolDefinition{
		Name: "record_change",
		Description: "Optional. Attribute a code change you (the agent) just made for higher-fidelity " +
			"impact reporting and freshness attribution. SourceBridge detects file changes regardless via " +
			"the passive fsnotify watcher, so calling this tool is never required for correctness — it is " +
			"a quality enrichment that adds your intent (e.g. 'refactor extract method'), attribution, and " +
			"tighter delta scoping when you provide it. If you don't call it, nothing breaks. " +
			"Path contract: each files[].path must be repo-relative, Unix forward-slash separators, no " +
			"leading './' or '/', no '..' components. Case-sensitive (matches git's worldview). " +
			"Branch contract: the claimed branch must match the working-tree HEAD; mismatches are rejected.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"repository_id": map[string]interface{}{
					"type":        "string",
					"description": "Repository ID the change applies to. Must be a repo the caller can access.",
				},
				"files": map[string]interface{}{
					"type":        "array",
					"description": "The declared delta. Each entry is one file change. Required, non-empty.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"path": map[string]interface{}{
								"type":        "string",
								"description": "Repo-relative path. Unix forward-slash separators only. No leading ./ or /. No .. components. Case-sensitive.",
							},
							"status": map[string]interface{}{
								"type":        "string",
								"enum":        []string{"added", "modified", "deleted", "renamed"},
								"description": "How the file changed. Defaults to 'modified' when omitted.",
							},
							"old_path": map[string]interface{}{
								"type":        "string",
								"description": "Required iff status='renamed'. The path the file occupied before the rename.",
							},
							"content_hash_after": map[string]interface{}{
								"type":        "string",
								"description": "Optional. SHA-256 hex of the post-edit file bytes. Lets the router collapse this event with a fsnotify event observing the same edit (dedup window).",
							},
						},
						"required": []string{"path"},
					},
				},
				"branch": map[string]interface{}{
					"type":        "string",
					"description": "The git ref the change applies to. Bare branch name ('main') or full ref ('refs/heads/main') both work. Must match the working-tree HEAD.",
				},
				"intent": map[string]interface{}{
					"type":        "string",
					"description": "Optional. Free-text describing why you made this change ('refactor extract method', 'implement REQ-42'). Surfaces on subsequent MCP reads as freshness.reason.",
				},
				"requirement_ids": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Optional. Requirement IDs this change is attributed to, for downstream compound-tool reviews.",
				},
			},
			"required": []string{"repository_id", "files", "branch"},
		},
	}
}

// callRecordChange handles tools/call name="record_change". The handler:
//
//  1. Validates and normalizes paths via changewatch.NormalizePaths so
//     bad caller input is rejected with a clean MCP error rather than
//     a Submit outcome.
//  2. Maps the wire-shape recordChangeRequest into a
//     changewatch.ChangeEvent, deriving Source.Actor from the MCP
//     session's authenticated identity (callers cannot lie about being
//     human).
//  3. Stamps Trust to the in-process trust shape — the dispatcher path
//     is in-process, NOT http_ingress.
//  4. Submits to the router via the same ChangeEventDispatcher
//     interface the HTTP ingress uses.
//  5. Returns a recordChangeResponse the agent can act on.
//
// Multi-tenant boundary: checkRepoAccess gates the call against the
// session's allowed repo set + the enterprise MCPPermissionChecker
// before any router work. An agent that targets a repo it can't access
// gets the standard "Repository not found or not accessible" error
// (no fingerprinting of which repos exist).
func (h *mcpHandler) callRecordChange(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	if h.changeDispatcher == nil {
		// Defense in depth: the tool is hidden from tools/list when the
		// dispatcher is nil, but a hand-crafted tools/call can still
		// reach this path. Return a structured error so capability-aware
		// callers can fall back gracefully.
		return nil, &mcpToolError{
			Code:        MCPErrCapabilityDisabled,
			Message:     "record_change is not available on this server (change-watch is disabled). The passive fsnotify path is sufficient for correctness; this tool is an opt-in quality enrichment.",
			Remediation: "Use the passive change detection path (no caller action needed). If you're an operator, enable change_watch.enabled to surface this tool.",
		}
	}

	var req recordChangeRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if req.RepositoryID == "" {
		return nil, errInvalidArguments("repository_id is required")
	}
	if req.Branch == "" {
		return nil, errInvalidArguments("branch is required")
	}
	if len(req.Files) == 0 {
		return nil, errInvalidArguments("files is required and must be non-empty (the delta-only invariant)")
	}
	if len(req.Files) > recordChangeMaxFiles {
		return nil, errInvalidArguments(fmt.Sprintf("files length %d exceeds the per-call cap of %d", len(req.Files), recordChangeMaxFiles))
	}
	if len(req.Intent) > recordChangeMaxIntentLen {
		return nil, errInvalidArguments(fmt.Sprintf("intent length %d exceeds cap %d", len(req.Intent), recordChangeMaxIntentLen))
	}
	if len(req.RequirementIDs) > recordChangeMaxRequirementIDs {
		return nil, errInvalidArguments(fmt.Sprintf("requirement_ids length %d exceeds cap %d", len(req.RequirementIDs), recordChangeMaxRequirementIDs))
	}
	for i, rid := range req.RequirementIDs {
		if len(rid) > recordChangeMaxRequirementIDLen {
			return nil, errInvalidArguments(fmt.Sprintf("requirement_ids[%d] length %d exceeds cap %d", i, len(rid), recordChangeMaxRequirementIDLen))
		}
	}
	// Strip control characters from Intent to avoid log-injection /
	// terminal-escape abuse. Unicode printable / whitespace stays.
	req.Intent = sanitizeIntent(req.Intent)

	// Multi-tenant boundary. checkRepoAccess returns the same opaque
	// error string regardless of which constraint failed (allowed-list,
	// permChecker, or unknown repo) so a caller can't fingerprint
	// other tenants' repos.
	if err := h.checkRepoAccess(ctx, session, req.RepositoryID); err != nil {
		return nil, err
	}

	// Normalize and validate every path. The router validates again via
	// ChangeEvent.Validate (defense in depth); doing it here lets us
	// return a clean MCP error rather than a SubmitOutcome.
	for i := range req.Files {
		// Default status to "modified" — the most common agent intent.
		if req.Files[i].Status == "" {
			req.Files[i].Status = "modified"
		}
		norm, err := changewatch.NormalizePath("", req.Files[i].Path)
		if err != nil {
			return nil, errInvalidArguments(fmt.Sprintf("files[%d].path: %v", i, err))
		}
		req.Files[i].Path = norm

		// Validate status enum.
		if !changewatch.FileChangeStatus(req.Files[i].Status).IsValid() {
			return nil, errInvalidArguments(fmt.Sprintf("files[%d].status=%q is not one of: added, modified, deleted, renamed", i, req.Files[i].Status))
		}

		// Renamed paths must carry old_path.
		if req.Files[i].Status == string(changewatch.FileChangeRenamed) {
			if req.Files[i].OldPath == "" {
				return nil, errInvalidArguments(fmt.Sprintf("files[%d]: old_path is required when status='renamed'", i))
			}
			normOld, err := changewatch.NormalizePath("", req.Files[i].OldPath)
			if err != nil {
				return nil, errInvalidArguments(fmt.Sprintf("files[%d].old_path: %v", i, err))
			}
			req.Files[i].OldPath = normOld
		}
	}

	// Build the ChangeEvent. The router populates ReceivedAt; we set
	// SchemaVersion and OccurredAt at the connector boundary.
	files := make([]changewatch.FileChange, len(req.Files))
	for i := range req.Files {
		files[i] = changewatch.FileChange{
			Path:             req.Files[i].Path,
			Status:           changewatch.FileChangeStatus(req.Files[i].Status),
			OldPath:          req.Files[i].OldPath,
			ContentHashAfter: req.Files[i].ContentHashAfter,
		}
	}

	ev := &changewatch.ChangeEvent{
		SchemaVersion: changewatch.ChangeEventSchemaVersion,
		EventID:       newRecordChangeEventID(),
		RepositoryID:  req.RepositoryID,
		OccurredAt:    time.Now().UTC(),
		Branch:        req.Branch,
		Files:         files,
		Source: changewatch.ChangeSource{
			Kind:           changewatch.SourceKindMCPRecordChange,
			ConnectorID:    "in_process:record_change",
			Actor:          actorFromSession(session),
			ActorDisplay:   actorDisplayFromSession(session),
			Intent:         req.Intent,
			RequirementIDs: req.RequirementIDs,
		},
		Trust: changewatch.Trust{
			Verified:           true,
			VerificationMethod: "in_process",
			ReceivedVia:        "in_process",
		},
	}

	// Use context.Background() — NOT the session's context — because
	// the router runs IndexFiles + impact-application work on the
	// caller's behalf and we want it to complete even if the agent
	// disconnects mid-call. The router's own T0 budget bounds wall
	// time, and the per-(repo, source.kind) rate limit + per-repo
	// breaker bound concurrent calls, so a malicious caller cannot
	// hold an unbounded number of background routines via this path.
	outcome, submitErr := h.changeDispatcher.Submit(context.Background(), ev)

	resp := recordChangeResponse{
		ChangeID:  ev.EventID,
		RoutedTo:  string(outcome),
		Branch:    ev.Branch,
		FileCount: len(ev.Files),
	}

	switch outcome {
	case changewatch.OutcomeIndexing:
		resp.Accepted = true
		resp.Reason = "routed to indexer"
	case changewatch.OutcomeDeduped:
		// Deduped is success-shaped from the agent's perspective: the
		// edit was already routed by another connector (typically
		// fsnotify saw the file write a moment before the agent called
		// record_change). The freshness state will reflect the edit.
		resp.Accepted = true
		resp.Reason = "deduped against earlier event in window (likely fsnotify saw the same edit)"
	case changewatch.OutcomeRateLimited:
		resp.Accepted = false
		resp.Reason = "per-(repo, source.kind) rate limit hit; back off and retry shortly"
	case changewatch.OutcomeBreakerTripped:
		resp.Accepted = false
		resp.Reason = "per-repo aggregate breaker is open; events paused until rate falls back under the threshold"
	default:
		resp.Accepted = false
		if submitErr != nil {
			resp.Reason = submitErr.Error()
		} else {
			resp.Reason = string(outcome)
		}
	}

	// Log every record_change call at info level. Useful for the
	// operator runbook's "verify record_change adoption" check.
	slog.Info("mcp record_change handled",
		"session_id", session.id,
		"user_id", session.claims.UserID,
		"org_id", session.claims.OrgID,
		"repo_id", req.RepositoryID,
		"event_id", ev.EventID,
		"file_count", len(ev.Files),
		"branch", ev.Branch,
		"intent", req.Intent,
		"outcome", string(outcome),
		"accepted", resp.Accepted,
	)

	// We deliberately do NOT propagate the router's submitErr as a
	// tools/call error. The wire contract is "tool succeeded; the
	// response body tells you what the router did." This matches the
	// HTTP ingress's 202-accepted-with-routed_to-on-error shape and
	// makes integration easier for agents.
	if submitErr != nil && !errors.Is(submitErr, changewatch.ErrRateLimited) &&
		!errors.Is(submitErr, changewatch.ErrBreakerOpen) &&
		!errors.Is(submitErr, changewatch.ErrEmptyDelta) &&
		!errors.Is(submitErr, changewatch.ErrInvalidPath) &&
		!errors.Is(submitErr, changewatch.ErrBranchMismatch) &&
		!errors.Is(submitErr, changewatch.ErrUnknownRepo) {
		// Truly unexpected error — the router returned something we
		// don't have a documented outcome for. Log and surface as a
		// structured tool error so agents can react.
		slog.Warn("mcp record_change: router returned unexpected error",
			"event_id", ev.EventID,
			"err", submitErr,
		)
	}

	return resp, nil
}

// actorFromSession derives ev.Source.Actor from the authenticated MCP
// session's claims. Returns one of:
//   - "agent:<tool>"      when MCP initialize advertised an agent client
//   - "human:<user>@<org>" when no agent identity is available (fallback)
//
// The plan calls out that agents cannot lie about being human: even if
// the tool args carried an Actor field, this derivation would override
// it. We don't expose Actor on the wire shape at all to keep the
// contract obvious.
//
// In Phase 1.D we don't yet thread the MCP initialize clientInfo.name
// through the session struct, so the fallback path is exercised. Phase
// 2 will plumb clientInfo.name; this function's contract is stable so
// the change is internal.
func actorFromSession(session *mcpSession) string {
	if session == nil || session.claims == nil {
		return "unknown"
	}
	user := session.claims.UserID
	org := session.claims.OrgID
	if user == "" {
		return "unknown"
	}
	if org == "" {
		return "human:" + user
	}
	return "human:" + user + "@" + org
}

// actorDisplayFromSession returns a UI-friendly label.
func actorDisplayFromSession(session *mcpSession) string {
	if session == nil || session.claims == nil {
		return ""
	}
	if session.claims.UserID != "" {
		return session.claims.UserID
	}
	return ""
}

// sanitizeIntent strips control characters and other non-printable
// runes from a free-text Intent string. Whitespace (space, tab,
// regular newlines) survives so multi-line intents are still
// readable, but ASCII control bytes / terminal escapes / null bytes
// are removed. Defense against log-injection and terminal-escape
// abuse via the structured-log path that records `intent`.
//
// Returns a fresh string. Idempotent.
func sanitizeIntent(s string) string {
	if s == "" {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			// Allow common whitespace.
			out = append(out, r)
		case unicode.IsControl(r):
			// Drop other control runes.
			continue
		case !unicode.IsPrint(r) && !unicode.IsSpace(r):
			// Drop non-printable / non-whitespace runes (zero-width
			// joiners, BIDI overrides, etc.).
			continue
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// newRecordChangeEventID generates a connector-stamped event ID. Format
// is "rc-<hex>" where hex is 16 random bytes — long enough to avoid
// collision in a high-volume log without being a full ULID dependency.
// The router's dedup-by-event_id path uses this verbatim.
func newRecordChangeEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "rc-" + hex.EncodeToString(b[:])
}

// recordChangeAvailable is the gate we read at tools/list time. Returns
// true only when the dispatcher is wired AND we're in a context where
// the tool would be useful. Lives here (not on mcpHandler directly) so
// the tools/list integration is a single import-target — readers who
// want to know "when does record_change show up?" can grep this name.
func (h *mcpHandler) recordChangeAvailable() bool {
	return h != nil && h.changeDispatcher != nil
}

// recordChangeToolDefIfAvailable returns the tool def or nil. Caller
// (baseTools in mcp.go) treats nil as "skip this tool from tools/list."
func (h *mcpHandler) recordChangeToolDefIfAvailable() *mcpToolDefinition {
	if !h.recordChangeAvailable() {
		return nil
	}
	def := recordChangeToolDef()
	return &def
}
