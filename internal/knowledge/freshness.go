// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// MarkAllStale marks all knowledge artifacts for a repository as stale.
// This should be called after any change that could invalidate generated
// knowledge (reindex, requirements import, link changes).
func MarkAllStale(store KnowledgeStore, repoID string) {
	if store == nil {
		return
	}
	artifacts := store.GetKnowledgeArtifacts(repoID)
	for _, a := range artifacts {
		if a.Status == StatusReady && !a.Stale {
			if err := store.MarkKnowledgeArtifactStale(a.ID, true); err != nil {
				slog.Warn("failed to mark knowledge artifact stale",
					"artifact_id", a.ID,
					"error", err,
				)
			}
		}
	}
	if err := store.MarkRepositoryUnderstandingNeedsRefresh(repoID); err != nil {
		slog.Warn("failed to mark repository understanding refresh-needed",
			"repo_id", repoID,
			"error", err,
		)
	}
}

// MarkStaleForImpact surgically marks knowledge artifacts stale based on the
// set of symbols and files that actually changed in a reindex. Artifacts are
// marked stale when their persisted evidence references a changed source;
// artifacts without usable provenance (no evidence, or only repository-scoped
// evidence) fall back to blanket stale so we never silently leave stale
// content marked fresh.
//
// Always calls store.MarkRepositoryUnderstandingNeedsRefresh — the repository
// understanding is a single repo-scoped document and any reindex should at
// least ask it to refresh (scope, timing, and ordering of the actual rebuild
// are handled elsewhere).
//
// reportID is the ImpactReport.ID that caused the invalidation. It is
// persisted on each stale artifact so the "why is this stale?" UI can stay
// correct across later reindexes that replace the repo's latest report.
//
// Returns a list of per-artifact reasons in the same shape as
// graph.StaleArtifactReason — the repo-level caller can copy these into
// ImpactReport.StaleArtifactReasons.
func MarkStaleForImpact(
	store KnowledgeStore,
	repoID string,
	symbolsChanged []string,
	filesChanged []string,
	reportID string,
	maxChanges int,
) []graph.StaleArtifactReason {
	if store == nil {
		return nil
	}

	symbolsChanged = dedupeNonEmpty(symbolsChanged)
	filesChanged = dedupeNonEmpty(filesChanged)

	// Always signal the repository understanding to refresh. This is cheap,
	// idempotent, and preserves the previous MarkAllStale contract.
	if err := store.MarkRepositoryUnderstandingNeedsRefresh(repoID); err != nil {
		slog.Warn("failed to mark repository understanding refresh-needed",
			"repo_id", repoID,
			"error", err,
		)
	}

	if len(symbolsChanged) == 0 && len(filesChanged) == 0 {
		// No structural change — nothing to mark stale.
		return []graph.StaleArtifactReason{}
	}

	// Guard against pathological change sets. Beyond this threshold the
	// selective query becomes a large IN clause and the result is
	// near-universal-stale anyway; blanket is cheaper and clearer.
	if maxChanges > 0 && len(symbolsChanged)+len(filesChanged) > maxChanges {
		slog.Warn("selective_invalidation_fallback_to_blanket",
			"repo_id", repoID,
			"symbols_changed", len(symbolsChanged),
			"files_changed", len(filesChanged),
			"max_changes", maxChanges,
			"reason", "change_set_exceeds_max",
		)
		return blanketStaleAll(store, repoID, reportID, "change_set_exceeds_max")
	}

	// Resolve artifacts whose evidence references the changed symbols/files.
	sources := make([]SourceRef, 0, len(symbolsChanged))
	for _, id := range symbolsChanged {
		sources = append(sources, SourceRef{SourceType: EvidenceSymbol, SourceID: id})
	}

	directly := make(map[string]*graph.StaleArtifactReason)
	if len(sources) > 0 {
		for _, a := range store.GetArtifactsForSources(repoID, sources) {
			if a == nil {
				continue
			}
			r := directly[a.ID]
			if r == nil {
				r = &graph.StaleArtifactReason{ArtifactID: a.ID, ReportID: reportID}
				directly[a.ID] = r
			}
			r.Symbols = append(r.Symbols, matchingSymbolIDs(a, symbolsChanged)...)
		}
	}
	if len(filesChanged) > 0 {
		for _, a := range store.GetArtifactsForFiles(repoID, filesChanged) {
			if a == nil {
				continue
			}
			r := directly[a.ID]
			if r == nil {
				r = &graph.StaleArtifactReason{ArtifactID: a.ID, ReportID: reportID}
				directly[a.ID] = r
			}
			r.Files = append(r.Files, matchingFilePaths(a, filesChanged)...)
		}
	}
	// Dedupe the attribution slices.
	for _, r := range directly {
		r.Symbols = dedupeNonEmpty(r.Symbols)
		r.Files = dedupeNonEmpty(r.Files)
	}

	// Decide a stale reason for every ready-and-fresh artifact in the repo.
	// The universe of candidates is GetKnowledgeArtifacts; we skip pending,
	// generating, and already-stale artifacts (same contract as MarkAllStale).
	candidates := store.GetKnowledgeArtifacts(repoID)
	reasons := make([]graph.StaleArtifactReason, 0, len(candidates))

	for _, a := range candidates {
		if a == nil {
			continue
		}
		if a.Status != StatusReady || a.Stale {
			continue
		}

		reason, ok := chooseReason(a, directly, reportID)
		if !ok {
			continue
		}

		reasonJSON := mustMarshalReason(reason)
		if err := store.MarkKnowledgeArtifactStaleWithReason(a.ID, reasonJSON, reportID); err != nil {
			slog.Warn("failed to mark knowledge artifact stale",
				"artifact_id", a.ID,
				"error", err,
			)
			continue
		}
		reasons = append(reasons, *reason)
	}

	// Stable ordering — makes tests and logs deterministic.
	sort.Slice(reasons, func(i, j int) bool { return reasons[i].ArtifactID < reasons[j].ArtifactID })

	slog.Info("selective_invalidation_decision",
		"repo_id", repoID,
		"candidates", len(candidates),
		"staled", len(reasons),
		"symbols_changed", len(symbolsChanged),
		"files_changed", len(filesChanged),
	)
	return reasons
}

// chooseReason returns the StaleArtifactReason to apply to an artifact, or
// (nil, false) if the artifact should stay fresh.
//
// Priority:
//  1. If the artifact has a direct evidence hit on a changed symbol/file
//     (from the directly map) → use that targeted reason.
//  2. Else if the artifact has ZERO persisted evidence rows → blanket
//     (we can't prove freshness, default to safe).
//  3. Else if EVERY evidence row is repository-scoped / commit-scoped
//     (no per-symbol/file grounding) → blanket. This catches whole-repo
//     artifacts like architecture diagrams that were generated without
//     per-symbol provenance.
//  4. Else → leave fresh (this is the selective win).
func chooseReason(
	a *Artifact,
	directly map[string]*graph.StaleArtifactReason,
	reportID string,
) (*graph.StaleArtifactReason, bool) {
	if direct, ok := directly[a.ID]; ok {
		return direct, true
	}
	evidenceCount, allRepoScoped := evidenceShape(a)
	switch {
	case evidenceCount == 0:
		return &graph.StaleArtifactReason{
			ArtifactID: a.ID,
			Blanket:    true,
			ReportID:   reportID,
		}, true
	case allRepoScoped:
		return &graph.StaleArtifactReason{
			ArtifactID: a.ID,
			Blanket:    true,
			ReportID:   reportID,
		}, true
	default:
		return nil, false
	}
}

// evidenceShape summarizes the evidence-row population of an artifact.
// Returns the total evidence row count and whether every row is of a
// "whole-repo" source type (repository or commit) — i.e. carries no per-
// symbol/per-file grounding that selective invalidation can exploit.
func evidenceShape(a *Artifact) (count int, allRepoScoped bool) {
	if a == nil {
		return 0, false
	}
	allRepoScoped = true
	for _, sec := range a.Sections {
		for _, ev := range sec.Evidence {
			count++
			switch ev.SourceType {
			case EvidenceRepository, EvidenceCommit:
				// Still counts as repo-scoped.
			default:
				allRepoScoped = false
			}
		}
	}
	if count == 0 {
		// Having no evidence is a separate condition handled by the caller.
		return 0, false
	}
	return count, allRepoScoped
}

// matchingSymbolIDs returns the subset of changedSymbols that are actually
// referenced by this artifact's evidence. Lets the persisted reason record
// the specific trigger(s), not just the whole change set.
func matchingSymbolIDs(a *Artifact, changedSymbols []string) []string {
	if a == nil || len(changedSymbols) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(changedSymbols))
	for _, id := range changedSymbols {
		if id != "" {
			want[id] = struct{}{}
		}
	}
	if len(want) == 0 {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	for _, sec := range a.Sections {
		for _, ev := range sec.Evidence {
			if ev.SourceType != EvidenceSymbol {
				continue
			}
			if ev.SourceID == "" {
				continue
			}
			if _, ok := want[ev.SourceID]; !ok {
				continue
			}
			if _, dup := seen[ev.SourceID]; dup {
				continue
			}
			seen[ev.SourceID] = struct{}{}
			out = append(out, ev.SourceID)
		}
	}
	return out
}

// matchingFilePaths returns the subset of changedFiles that are actually
// referenced (by file_path) in this artifact's evidence.
func matchingFilePaths(a *Artifact, changedFiles []string) []string {
	if a == nil || len(changedFiles) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(changedFiles))
	for _, p := range changedFiles {
		if p != "" {
			want[p] = struct{}{}
		}
	}
	if len(want) == 0 {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	for _, sec := range a.Sections {
		for _, ev := range sec.Evidence {
			if ev.FilePath == "" {
				continue
			}
			if _, ok := want[ev.FilePath]; !ok {
				continue
			}
			if _, dup := seen[ev.FilePath]; dup {
				continue
			}
			seen[ev.FilePath] = struct{}{}
			out = append(out, ev.FilePath)
		}
	}
	return out
}

// blanketStaleAll performs a blanket stale sweep for use when selective
// invalidation is bypassed (e.g. change-set too large). Each affected artifact
// is recorded with Blanket=true so the GraphQL reason field still has data.
func blanketStaleAll(store KnowledgeStore, repoID, reportID, why string) []graph.StaleArtifactReason {
	candidates := store.GetKnowledgeArtifacts(repoID)
	reasons := make([]graph.StaleArtifactReason, 0, len(candidates))
	for _, a := range candidates {
		if a == nil || a.Status != StatusReady || a.Stale {
			continue
		}
		reason := graph.StaleArtifactReason{
			ArtifactID: a.ID,
			Blanket:    true,
			ReportID:   reportID,
		}
		reasonJSON := mustMarshalReason(&reason)
		if err := store.MarkKnowledgeArtifactStaleWithReason(a.ID, reasonJSON, reportID); err != nil {
			slog.Warn("failed to mark knowledge artifact stale",
				"artifact_id", a.ID,
				"error", err,
				"why", why,
			)
			continue
		}
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i].ArtifactID < reasons[j].ArtifactID })
	return reasons
}

func mustMarshalReason(reason *graph.StaleArtifactReason) string {
	if reason == nil {
		return ""
	}
	b, err := json.Marshal(reason)
	if err != nil {
		return ""
	}
	return string(b)
}

func dedupeNonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DecodeStaleReason parses the JSON-serialized reason blob persisted on an
// Artifact. Returns nil if the artifact has no reason or the blob is invalid.
// The returned value is a fresh copy the caller may mutate safely.
func DecodeStaleReason(a *Artifact) *graph.StaleArtifactReason {
	if a == nil || a.StaleReasonJSON == "" {
		return nil
	}
	var r graph.StaleArtifactReason
	if err := json.Unmarshal([]byte(a.StaleReasonJSON), &r); err != nil {
		return nil
	}
	return &r
}
