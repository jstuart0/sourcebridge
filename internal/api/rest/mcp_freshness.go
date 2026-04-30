// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"time"

	"github.com/sourcebridge/sourcebridge/internal/changewatch"
)

// FreshnessProvider is the package-boundary contract the MCP layer
// queries to populate the _meta.freshness envelope on every tool
// response. The production wiring backs this with the change-watch
// router's per-repo freshness state; tests pass stubs.
//
// In Phase 1.C the change-watch router is the only authoritative
// source of freshness state, but the envelope ships even when the
// SOURCEBRIDGE_CHANGE_WATCH_ENABLED flag is off — in that case the
// envelope reports state="fresh" with an unknown verification time,
// matching the "no detection ever happened, but everything you read
// was indexed as part of the last operator-driven reindex" semantics.
type FreshnessProvider interface {
	// FreshnessForRepo returns the most recent freshness record for
	// repoID. The zero value is acceptable when the repo has never
	// routed a change-watch event; the envelope reports state="fresh"
	// in that case.
	FreshnessForRepo(repoID string) FreshnessRecord
}

// FreshnessRecord is the envelope-shaped subset of changewatch's
// internal record. We mirror it here to keep the MCP layer's wire
// types free of internal/changewatch coupling — internal/api/rest
// imports internal/changewatch only via this small interface boundary.
type FreshnessRecord struct {
	State          string // fresh | stale | suspect | invalidated
	Tier           string // T0 | T1 | T2 | T3
	Branch         string
	IndexedCommit  string
	LastVerifiedAt time.Time
	Reason         string
	PartialRefresh bool
}

// freshnessEnvelope returns the _meta.freshness payload for repoID.
// repoID may be empty when the calling tool does not target a single
// repo (e.g. cross-repo tools); in that case we emit the envelope's
// "unknown" shape so consumers can rely on the field always existing.
//
// Shape per plan §Freshness envelope shape:
//
//	{
//	  "freshness": {
//	    "state": "fresh|stale|suspect|invalidated",
//	    "tier": "T0|T1|T2|T3",
//	    "last_verified_at": "RFC3339",
//	    "branch": "...",
//	    "indexed_commit": "...",
//	    "partial_refresh": false,
//	    "reason": "..."
//	  }
//	}
//
// The envelope is additive on every MCP response. Existing consumers
// that do not parse `_meta` are unaffected; consumers that opt in get
// honest freshness on every read from Phase 1.C onward.
func freshnessEnvelope(provider FreshnessProvider, repoID string) map[string]interface{} {
	rec := FreshnessRecord{
		State: "fresh", // default for unprimed repos (operator just indexed; no events yet)
		Tier:  "T0",
	}
	if provider != nil && repoID != "" {
		got := provider.FreshnessForRepo(repoID)
		if got.State != "" {
			rec.State = got.State
		}
		if got.Tier != "" {
			rec.Tier = got.Tier
		}
		rec.Branch = got.Branch
		rec.IndexedCommit = got.IndexedCommit
		rec.LastVerifiedAt = got.LastVerifiedAt
		rec.Reason = got.Reason
		rec.PartialRefresh = got.PartialRefresh
	}
	out := map[string]interface{}{
		"state":           rec.State,
		"tier":            rec.Tier,
		"partial_refresh": rec.PartialRefresh,
	}
	if rec.Branch != "" {
		out["branch"] = rec.Branch
	}
	if rec.IndexedCommit != "" {
		out["indexed_commit"] = rec.IndexedCommit
	}
	if !rec.LastVerifiedAt.IsZero() {
		out["last_verified_at"] = rec.LastVerifiedAt.UTC().Format(time.RFC3339)
	}
	if rec.Reason != "" {
		out["reason"] = rec.Reason
	}
	return out
}

// routerFreshnessProvider is the production-side adapter that bridges
// changewatch.Router to FreshnessProvider. The router exposes its
// per-repo freshness record via FreshnessFor; we re-shape it into the
// envelope-friendly FreshnessRecord here.
type routerFreshnessProvider struct {
	router *changewatch.Router
}

// FreshnessForRepo implements FreshnessProvider. Returns the zero
// FreshnessRecord when the router has never observed an event for
// repoID — the envelope handler uses default state="fresh" in that
// case, matching the "no events ever, you're reading the operator-
// indexed state" semantics.
//
// This adapter is the only place internal/api/rest reaches into
// changewatch's internal type. The reverse direction (changewatch
// reaching into internal/api/rest) is forbidden by the package-
// boundary discipline established in router.go's godoc.
func (p *routerFreshnessProvider) FreshnessForRepo(repoID string) FreshnessRecord {
	if p.router == nil {
		return FreshnessRecord{}
	}
	src := p.router.FreshnessForExport(repoID)
	return FreshnessRecord{
		State:          src.State,
		Tier:           src.Tier,
		Branch:         src.Branch,
		IndexedCommit:  src.IndexedCommit,
		LastVerifiedAt: src.LastVerifiedAt,
		Reason:         src.Reason,
		PartialRefresh: src.PartialRefresh,
	}
}

// NewRouterFreshnessProvider wires a changewatch.Router as a
// FreshnessProvider. Returns an envelope provider that reports the
// default fresh state for every repo when router is nil, so the
// envelope contract still holds in flag-off / not-yet-wired
// deployments.
func NewRouterFreshnessProvider(router *changewatch.Router) FreshnessProvider {
	return &routerFreshnessProvider{router: router}
}
