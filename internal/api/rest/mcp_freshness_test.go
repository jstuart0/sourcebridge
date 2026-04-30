// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"testing"
	"time"
)

// stubFreshnessProvider returns a configurable record. Used to drive
// the envelope under known shapes.
type stubFreshnessProvider struct {
	rec FreshnessRecord
}

func (s *stubFreshnessProvider) FreshnessForRepo(_ string) FreshnessRecord {
	return s.rec
}

// TestFreshnessEnvelope_DefaultsForNilProvider asserts the contract
// holds even when no FreshnessProvider is wired: the envelope still
// ships state="fresh" and tier="T0" so MCP consumers can rely on the
// keys always being present.
func TestFreshnessEnvelope_DefaultsForNilProvider(t *testing.T) {
	got := freshnessEnvelope(nil, "repo_abc")
	if got["state"] != "fresh" {
		t.Errorf("state = %v, want %q", got["state"], "fresh")
	}
	if got["tier"] != "T0" {
		t.Errorf("tier = %v, want %q", got["tier"], "T0")
	}
	if got["partial_refresh"] != false {
		t.Errorf("partial_refresh = %v, want false", got["partial_refresh"])
	}
	// Optional fields must NOT be present when their source is empty —
	// keeping the wire shape minimal so consumers don't see "" /
	// zero-valued Time / etc.
	if _, ok := got["last_verified_at"]; ok {
		t.Errorf("last_verified_at unexpectedly present: %v", got["last_verified_at"])
	}
	if _, ok := got["branch"]; ok {
		t.Errorf("branch unexpectedly present: %v", got["branch"])
	}
}

// TestFreshnessEnvelope_DefaultsForEmptyRepoID is the cross-repo /
// system-tool case — repoID="" yields the same default envelope
// shape so tools that don't operate on a single repo still emit a
// uniform envelope.
func TestFreshnessEnvelope_DefaultsForEmptyRepoID(t *testing.T) {
	provider := &stubFreshnessProvider{rec: FreshnessRecord{
		State:          "stale",
		Tier:           "T2",
		Branch:         "main",
		LastVerifiedAt: time.Now(),
	}}
	got := freshnessEnvelope(provider, "")
	// repoID is empty → provider is bypassed; default envelope wins.
	if got["state"] != "fresh" {
		t.Errorf("state = %v, want fresh (default for empty repoID)", got["state"])
	}
	if got["tier"] != "T0" {
		t.Errorf("tier = %v, want T0 (default for empty repoID)", got["tier"])
	}
}

// TestFreshnessEnvelope_PopulatedFields asserts every field the
// provider supplies surfaces in the envelope with the right wire-shape.
func TestFreshnessEnvelope_PopulatedFields(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	provider := &stubFreshnessProvider{rec: FreshnessRecord{
		State:          "stale",
		Tier:           "T0",
		Branch:         "feature/x",
		IndexedCommit:  "abc123",
		LastVerifiedAt: now,
		Reason:         "agent:claude-code edited internal/api/rest/mcp.go",
		PartialRefresh: true,
	}}
	got := freshnessEnvelope(provider, "repo_abc")
	if got["state"] != "stale" {
		t.Errorf("state = %v, want stale", got["state"])
	}
	if got["tier"] != "T0" {
		t.Errorf("tier = %v, want T0", got["tier"])
	}
	if got["branch"] != "feature/x" {
		t.Errorf("branch = %v, want feature/x", got["branch"])
	}
	if got["indexed_commit"] != "abc123" {
		t.Errorf("indexed_commit = %v, want abc123", got["indexed_commit"])
	}
	if got["last_verified_at"] != "2026-04-30T12:00:00Z" {
		t.Errorf("last_verified_at = %v, want RFC3339 UTC", got["last_verified_at"])
	}
	if got["reason"] != "agent:claude-code edited internal/api/rest/mcp.go" {
		t.Errorf("reason = %v, want populated", got["reason"])
	}
	if got["partial_refresh"] != true {
		t.Errorf("partial_refresh = %v, want true", got["partial_refresh"])
	}
}

// TestFreshnessEnvelope_PartialRefreshSurfaces asserts the
// budget-exceeded marker is honored when set on the provider record.
// This is the load-bearing semantic for the T0-budget-exceeded case
// in the plan: agents see flag=stale + partial_refresh=true rather
// than blocking on a hung index call.
func TestFreshnessEnvelope_PartialRefreshSurfaces(t *testing.T) {
	provider := &stubFreshnessProvider{rec: FreshnessRecord{
		State:          "stale",
		Tier:           "T0",
		PartialRefresh: true,
	}}
	got := freshnessEnvelope(provider, "repo_abc")
	if got["partial_refresh"] != true {
		t.Errorf("partial_refresh = %v, want true", got["partial_refresh"])
	}
}

// TestFreshnessEnvelope_SuspectStateForUnsupportedBackend asserts the
// path the router takes when MergeIndexResult fails on the SurrealDB
// backend (state="suspect" + partial_refresh=true). Operators on
// SurrealDB who flip the umbrella flag in 1.C see "we tried, the
// backend doesn't support it yet" rather than silent corruption.
func TestFreshnessEnvelope_SuspectStateForUnsupportedBackend(t *testing.T) {
	provider := &stubFreshnessProvider{rec: FreshnessRecord{
		State:          "suspect",
		Tier:           "T0",
		PartialRefresh: true,
	}}
	got := freshnessEnvelope(provider, "repo_abc")
	if got["state"] != "suspect" {
		t.Errorf("state = %v, want suspect", got["state"])
	}
	if got["partial_refresh"] != true {
		t.Errorf("partial_refresh = %v, want true", got["partial_refresh"])
	}
}
