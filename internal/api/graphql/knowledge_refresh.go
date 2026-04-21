// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// ---------------------------------------------------------------------------
// Phase 2: delta-driven regeneration pipeline
//
// After a reindex has selectively marked artifacts stale (Phase 1), the
// driver below decides which of those artifacts should be auto-regenerated
// in the background, applies rate limits and understanding gating, and then
// either enqueues them (live mode) or just emits telemetry (shadow mode).
//
// Shadow mode is the recommended first rollout: it runs every decision
// step identically to live mode, then skips the actual enqueue. This lets
// operators observe real-world cost, rate-limit hit rates, and decision
// quality before committing to automatic regeneration.
// ---------------------------------------------------------------------------

// regenTypePriority controls the order artifacts are considered against the
// per-reindex cap. Artifacts users look at first get priority.
var regenTypePriority = map[knowledgepkg.ArtifactType]int{
	knowledgepkg.ArtifactCliffNotes:          0,
	knowledgepkg.ArtifactArchitectureDiagram: 1,
	knowledgepkg.ArtifactLearningPath:        2,
	knowledgepkg.ArtifactCodeTour:            3,
	knowledgepkg.ArtifactWorkflowStory:       4,
}

// regenRateLimiter is an in-process sliding-window counter keyed by
// repository ID. A single instance is shared per mutationResolver via
// deltaRegenRateLimiter() below so all reindexes against a repo share the
// same ceiling. No persistent storage — on restart the window resets.
type regenRateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
}

func newRegenRateLimiter() *regenRateLimiter {
	return &regenRateLimiter{windows: make(map[string][]time.Time)}
}

// attempt returns true if the repo is under cap in the last 1 hour AND
// records this attempt. Returns false without recording if over cap.
func (l *regenRateLimiter) attempt(repoID string, cap int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-time.Hour)
	events := l.windows[repoID]
	// Trim stale entries first.
	trimmed := events[:0]
	for _, t := range events {
		if t.After(cutoff) {
			trimmed = append(trimmed, t)
		}
	}
	if len(trimmed) >= cap {
		l.windows[repoID] = trimmed
		return false
	}
	trimmed = append(trimmed, time.Now())
	l.windows[repoID] = trimmed
	return true
}

// windowCount returns the current event count in the rolling hour window.
// Exposed for test introspection; not used in the hot path.
func (l *regenRateLimiter) windowCount(repoID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-time.Hour)
	events := l.windows[repoID]
	n := 0
	for _, t := range events {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

// Global rate limiter instance. One per process — acceptable because a
// single API replica handles reindexes for its own traffic and the cap is
// soft (a bound on regen LLM cost, not a correctness invariant).
var deltaRegenRateLimiterOnce sync.Once
var deltaRegenRateLimiterInst *regenRateLimiter

func deltaRegenRateLimiter() *regenRateLimiter {
	deltaRegenRateLimiterOnce.Do(func() {
		deltaRegenRateLimiterInst = newRegenRateLimiter()
	})
	return deltaRegenRateLimiterInst
}

// enqueueStaleArtifactRefresh is the entry point called from the reindex
// resolver. Respects SOURCEBRIDGE_DELTA_REGEN_MODE (off/shadow/live).
// In shadow mode emits telemetry about what WOULD be enqueued; in live
// mode actually enqueues via RefreshKnowledgeArtifact.
//
// Safe to call with mode=off — returns immediately.
func (r *mutationResolver) enqueueStaleArtifactRefresh(
	repoID string,
	artifactIDs []string,
	reportID string,
) {
	mode := deltaRegenMode()
	if mode == DeltaRegenModeOff {
		return
	}
	if r.KnowledgeStore == nil {
		slog.Warn("delta_regen_skipped: knowledge store unavailable", "repo_id", repoID, "mode", string(mode))
		return
	}
	if len(artifactIDs) == 0 {
		return
	}

	// Rate limit check. Consume the slot even in shadow mode so the observed
	// metrics honestly reflect what live mode would have done.
	cap := deltaRegenMaxPerRepoPerHour()
	if !deltaRegenRateLimiter().attempt(repoID, cap) {
		slog.Warn("delta_regen_rate_limited",
			"repo_id", repoID,
			"mode", string(mode),
			"window_count", deltaRegenRateLimiter().windowCount(repoID),
			"cap", cap,
		)
		return
	}

	// Load + filter candidates.
	candidates := make([]*knowledgepkg.Artifact, 0, len(artifactIDs))
	for _, id := range artifactIDs {
		a := r.KnowledgeStore.GetKnowledgeArtifact(id)
		if a == nil {
			continue
		}
		// Skip non-stale (race: manual refresh completed between Phase 1 mark
		// and us picking it up here).
		if !a.Stale {
			continue
		}
		// Skip if something else already picked it up (in-flight).
		if a.Status != knowledgepkg.StatusReady {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return
	}

	// Understanding gating: an artifact generated in understanding_first mode
	// depends on a fresh ca_repository_understanding record. If the
	// understanding itself is stale (NeedsRefresh / missing / revision-fp
	// mismatch), defer understanding_first artifacts for this reindex so we
	// don't regenerate them on top of stale context. classic-mode artifacts
	// are unaffected.
	deferred, ready := splitForUnderstandingGate(r.KnowledgeStore, repoID, candidates)
	if len(deferred) > 0 {
		ids := make([]string, 0, len(deferred))
		for _, a := range deferred {
			ids = append(ids, a.ID)
		}
		slog.Info("delta_regen_deferred",
			"repo_id", repoID,
			"mode", string(mode),
			"artifact_ids", ids,
			"reason", "understanding_not_fresh",
		)
	}
	if len(ready) == 0 {
		return
	}

	// Sort by type priority, then most recently generated first (proxy for
	// most recently viewed — good enough for the first iteration).
	sort.SliceStable(ready, func(i, j int) bool {
		pi, pj := regenTypePriority[ready[i].Type], regenTypePriority[ready[j].Type]
		if pi != pj {
			return pi < pj
		}
		return ready[i].GeneratedAt.After(ready[j].GeneratedAt)
	})

	// Apply per-reindex cap.
	perIndexCap := deltaRegenMaxPerIndex()
	overCap := 0
	if len(ready) > perIndexCap {
		overCap = len(ready) - perIndexCap
		ready = ready[:perIndexCap]
	}

	selectedIDs := make([]string, 0, len(ready))
	for _, a := range ready {
		selectedIDs = append(selectedIDs, a.ID)
	}
	slog.Info("delta_regen_decision",
		"repo_id", repoID,
		"mode", string(mode),
		"report_id", reportID,
		"candidates", len(candidates),
		"deferred", len(deferred),
		"selected", len(selectedIDs),
		"over_cap", overCap,
		"artifact_ids", selectedIDs,
	)

	// Execute the decision.
	switch mode {
	case DeltaRegenModeShadow:
		for _, a := range ready {
			slog.Info("delta_regen_shadow_would_enqueue",
				"repo_id", repoID,
				"report_id", reportID,
				"artifact_id", a.ID,
				"type", string(a.Type),
				"audience", string(a.Audience),
				"depth", string(a.Depth),
				"generation_mode", string(a.GenerationMode),
				"scope_type", scopeTypeOf(a),
			)
		}
	case DeltaRegenModeLive:
		// Fire-and-forget per artifact via the existing refresh resolver.
		// RefreshKnowledgeArtifact handles the full lifecycle: in-flight
		// dedupe, snapshot assembly, orchestrator enqueue, progress ticker.
		// Use a fresh background context — the reindex HTTP request may
		// already be closed by the time we get here.
		for _, a := range ready {
			artifactID := a.ID
			go func() {
				bgCtx := context.Background()
				if _, err := r.RefreshKnowledgeArtifact(bgCtx, artifactID); err != nil {
					slog.Warn("delta_regen_enqueue_failed",
						"repo_id", repoID,
						"report_id", reportID,
						"artifact_id", artifactID,
						"error", err,
					)
					return
				}
				slog.Info("delta_regen_enqueued",
					"repo_id", repoID,
					"report_id", reportID,
					"artifact_id", artifactID,
					"type", string(a.Type),
				)
			}()
		}
	}
}

// splitForUnderstandingGate partitions candidates into (deferred, ready).
// Artifacts generated in understanding_first mode are deferred when the
// repository understanding for their scope is missing, needs_refresh, or
// has a revision_fp older than the artifact's recorded understanding
// revision. Classic-mode artifacts are always ready.
func splitForUnderstandingGate(
	store knowledgepkg.KnowledgeStore,
	repoID string,
	candidates []*knowledgepkg.Artifact,
) (deferred, ready []*knowledgepkg.Artifact) {
	// Cache one GetRepositoryUnderstanding lookup per distinct scope.
	type scopeKey struct{ st, sp string }
	cache := make(map[scopeKey]*knowledgepkg.RepositoryUnderstanding)

	for _, a := range candidates {
		if knowledgepkg.NormalizeGenerationMode(a.GenerationMode) != knowledgepkg.GenerationModeUnderstandingFirst {
			ready = append(ready, a)
			continue
		}
		scope := knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}
		if a.Scope != nil {
			scope = a.Scope.Normalize()
		}
		key := scopeKey{st: string(scope.ScopeType), sp: scope.ScopePath}
		u, cached := cache[key]
		if !cached {
			u = store.GetRepositoryUnderstanding(repoID, scope)
			cache[key] = u
		}
		if understandingIsReadyForArtifact(u, a) {
			ready = append(ready, a)
			continue
		}
		deferred = append(deferred, a)
	}
	return deferred, ready
}

// understandingIsReadyForArtifact returns true when the given understanding
// is fresh enough to rebuild the artifact against. A nil / needs-refresh /
// failed understanding is never ready. If the artifact carries an
// UnderstandingRevisionFP, the understanding's current revision must match
// — otherwise we'd regenerate against a different snapshot than the one
// recorded when the artifact was originally generated.
func understandingIsReadyForArtifact(
	u *knowledgepkg.RepositoryUnderstanding,
	a *knowledgepkg.Artifact,
) bool {
	if u == nil {
		return false
	}
	switch u.Stage {
	case knowledgepkg.UnderstandingReady, knowledgepkg.UnderstandingFirstPassReady:
		// Continue to the revision check below.
	default:
		return false
	}
	// If the artifact recorded an understanding revision, require the current
	// understanding to be at least as fresh. Empty revision on the artifact
	// side is treated as "don't care" (legacy artifacts).
	if a.UnderstandingRevisionFP != "" && u.RevisionFP != "" && a.UnderstandingRevisionFP != u.RevisionFP {
		return false
	}
	return true
}

// scopeTypeOf returns the scope type as a string, defaulting to "repository"
// when the scope is nil.
func scopeTypeOf(a *knowledgepkg.Artifact) string {
	if a == nil || a.Scope == nil {
		return string(knowledgepkg.ScopeRepository)
	}
	return string(a.Scope.ScopeType)
}
