// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/skillcard"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// clustersResponse is returned by GET /api/v1/repositories/{repo_id}/clusters.
type clustersResponse struct {
	RepoID      string              `json:"repo_id"`
	Status      string              `json:"status"`
	Clusters    []clusterSummaryDTO `json:"clusters"`
	RetrievedAt string              `json:"retrieved_at"`
}

// clusterSummaryDTO is the wire shape exposed to the web UI and the setup CLI.
type clusterSummaryDTO struct {
	ID                    string         `json:"id"`
	Label                 string         `json:"label"`
	MemberCount           int            `json:"member_count"`
	RepresentativeSymbols []string       `json:"representative_symbols"`
	CrossClusterCalls     map[string]int `json:"cross_cluster_calls,omitempty"`
	Partial               bool           `json:"partial"`
	// Packages is the deduplicated set of top-level package paths derived from
	// member symbol file paths. Sorted lexicographically; capped at 5.
	Packages []string `json:"packages,omitempty"`
	// Warnings contains call-graph-derived advisories for this cluster.
	Warnings []warningDTO `json:"warnings,omitempty"`
}

// warningDTO mirrors skillcard.Warning for the REST wire format.
type warningDTO struct {
	Symbol string `json:"symbol"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// handleListClusters implements GET /api/v1/repositories/{repo_id}/clusters.
// Returns the ClusterSummary list for the given repo, capability-gated on
// subsystem_clustering.
func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	edition := capabilities.NormalizeEdition(s.cfg.Edition)
	if !capabilities.IsAvailable("subsystem_clustering", edition) {
		http.Error(w, "subsystem_clustering capability not available in this edition", http.StatusForbidden)
		return
	}

	repoID := chi.URLParam(r, "repo_id")
	if err := clustering.ValidateID(repoID); err != nil {
		http.Error(w, "invalid repo_id: "+err.Error(), http.StatusBadRequest)
		return
	}

	store := s.getStore(r)
	cs, ok := store.(clustering.ClusterStore)
	if !ok || cs == nil {
		writeJSON(w, http.StatusOK, clustersResponse{
			RepoID: repoID, Status: "unavailable", Clusters: nil,
		})
		return
	}

	clusters, err := cs.GetClusters(r.Context(), repoID)
	if err != nil || len(clusters) == 0 {
		writeJSON(w, http.StatusOK, clustersResponse{
			RepoID: repoID, Status: "pending", Clusters: nil,
		})
		return
	}

	// Determine status.
	status := "ready"
	for _, c := range clusters {
		if c.Partial {
			status = "partial"
			break
		}
	}

	// Build cross-cluster call index using the existing helpers from the MCP handler.
	edges := store.GetCallEdges(repoID)
	inDegree := buildInDegree(edges)

	// Fetch full cluster data (with members) for package and warning derivation.
	clustersWithMembers := make([]clustering.Cluster, 0, len(clusters))
	for _, c := range clusters {
		full, err := cs.GetClusterByID(r.Context(), c.ID)
		if err != nil || full == nil {
			clustersWithMembers = append(clustersWithMembers, c)
		} else {
			clustersWithMembers = append(clustersWithMembers, *full)
		}
	}

	clusterBySymbol := buildClusterBySymbol(clustersWithMembers)
	crossCalls := buildCrossClusterCalls(edges, clusterBySymbol, clustersWithMembers)

	// Build the symbol meta map needed by DeriveWarnings.
	// Include both cluster members (so callees are resolved) AND all symbols
	// that appear as callers in the edge list (so cross-package-callers warnings
	// can identify the caller's package, even when the caller is in a different
	// cluster or is unclustered).
	memberIDSet := make(map[string]struct{})
	for _, c := range clustersWithMembers {
		for _, m := range c.Members {
			memberIDSet[m.SymbolID] = struct{}{}
		}
	}
	for _, e := range edges {
		memberIDSet[e.CallerID] = struct{}{}
	}
	allSymbolIDs := make([]string, 0, len(memberIDSet))
	for id := range memberIDSet {
		allSymbolIDs = append(allSymbolIDs, id)
	}
	symMap := store.GetSymbolsByIDs(allSymbolIDs)

	// Resolve cluster label for each symbol (needed by DeriveWarnings).
	symClusterLabel := make(map[string]string, len(allSymbolIDs))
	for _, c := range clustersWithMembers {
		label := c.Label
		if c.LLMLabel != nil && *c.LLMLabel != "" {
			label = *c.LLMLabel
		}
		for _, m := range c.Members {
			symClusterLabel[m.SymbolID] = label
		}
	}

	// Build the SymbolMeta map for DeriveWarnings.
	skillSymbols := make(map[string]skillcard.SymbolMeta, len(symMap))
	for id, sym := range symMap {
		if sym == nil {
			continue
		}
		label := symClusterLabel[id] // empty for non-member callers (correct — they cross boundaries)
		skillSymbols[id] = skillcard.SymbolMeta{
			QualifiedName: sym.QualifiedName,
			Package:       topLevelPackageFromPath(sym.FilePath),
			ClusterLabel:  label,
		}
	}

	// Convert graph edges to skillcard.CallEdge for DeriveWarnings.
	skillEdges := make([]skillcard.CallEdge, len(edges))
	for i, e := range edges {
		skillEdges[i] = skillcard.CallEdge{CallerID: e.CallerID, CalleeID: e.CalleeID}
	}

	warningsByCluster := skillcard.DeriveWarnings(skillEdges, skillSymbols)

	summaries := make([]clusterSummaryDTO, 0, len(clustersWithMembers))
	for _, c := range clustersWithMembers {
		cs2 := buildClusterSummary(c, inDegree, crossCalls)

		// Derive packages from member file paths.
		pkgSet := make(map[string]struct{})
		for _, m := range c.Members {
			if sym, ok := symMap[m.SymbolID]; ok && sym != nil {
				pkg := topLevelPackageFromPath(sym.FilePath)
				if pkg != "" {
					pkgSet[pkg] = struct{}{}
				}
			}
		}
		pkgs := make([]string, 0, len(pkgSet))
		for p := range pkgSet {
			pkgs = append(pkgs, p)
		}
		sort.Strings(pkgs)
		if len(pkgs) > 5 {
			pkgs = pkgs[:5]
		}

		// Map cluster warnings to wire format.
		clusterLabel := c.Label
		if c.LLMLabel != nil && *c.LLMLabel != "" {
			clusterLabel = *c.LLMLabel
		}
		rawWarnings := warningsByCluster[clusterLabel]
		warnDTOs := make([]warningDTO, 0, len(rawWarnings))
		for _, w := range rawWarnings {
			warnDTOs = append(warnDTOs, warningDTO{
				Symbol: w.Symbol,
				Kind:   w.Kind,
				Detail: w.Detail,
			})
		}

		summaries = append(summaries, clusterSummaryDTO{
			ID:                    cs2.ID,
			Label:                 cs2.Label,
			MemberCount:           cs2.MemberCount,
			RepresentativeSymbols: cs2.RepresentativeSymbols,
			CrossClusterCalls:     cs2.CrossClusterCalls,
			Partial:               cs2.Partial,
			Packages:              pkgs,
			Warnings:              warnDTOs,
		})
	}

	writeJSON(w, http.StatusOK, clustersResponse{
		RepoID:      repoID,
		Status:      status,
		Clusters:    summaries,
		RetrievedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// topLevelPackageFromPath extracts the top-level package name from a file path.
// e.g. "internal/auth/token.go" → "auth", "billing/invoice.go" → "billing".
// Returns an empty string when the path is empty or has no directory component.
func topLevelPackageFromPath(filePath string) string {
	if filePath == "" {
		return ""
	}
	// Normalise separators.
	clean := filepath.ToSlash(filePath)
	// Strip a leading "internal/" segment that acts as an organisational wrapper.
	if after, ok := strings.CutPrefix(clean, "internal/"); ok {
		clean = after
	}
	// The first path segment is the package name.
	idx := strings.IndexByte(clean, '/')
	if idx < 0 {
		// Top-level file — use the base name without extension.
		base := filepath.Base(filePath)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return clean[:idx]
}

// relabelRequest is the optional JSON body for POST /api/v1/repositories/{repo_id}/clusters/relabel.
// When cluster_ids is non-empty, only those clusters are relabeled.
// When empty or absent, all clusters for the repo are relabeled.
type relabelRequest struct {
	ClusterIDs []string `json:"cluster_ids,omitempty"`
}

// relabelResponse is returned on a successful enqueue.
type relabelResponse struct {
	JobID string `json:"job_id"`
}

// handleRelabelClusters implements POST /api/v1/repositories/{repo_id}/clusters/relabel.
// It enqueues a batch LLM rename job that writes llm_label on each cluster.
// Capability-gated on subsystem_clustering.
func (s *Server) handleRelabelClusters(w http.ResponseWriter, r *http.Request) {
	edition := capabilities.NormalizeEdition(s.cfg.Edition)
	if !capabilities.IsAvailable("subsystem_clustering", edition) {
		http.Error(w, "subsystem_clustering capability not available in this edition", http.StatusForbidden)
		return
	}

	repoID := chi.URLParam(r, "repo_id")
	if repoID == "" {
		http.Error(w, "repo_id is required", http.StatusBadRequest)
		return
	}
	if err := clustering.ValidateID(repoID); err != nil {
		http.Error(w, "invalid repo_id: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Verify the repo exists in the store.
	store := s.getStore(r)
	repo := store.GetRepository(repoID)
	if repo == nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}

	var req relabelRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}

	cs, ok := store.(clustering.ClusterStore)
	if !ok || cs == nil {
		http.Error(w, "clustering store not available on this deployment", http.StatusServiceUnavailable)
		return
	}

	// Resolve the cluster IDs to relabel.
	clusterIDs := req.ClusterIDs
	if len(clusterIDs) == 0 {
		// No explicit list — relabel all clusters for this repo.
		clusters, err := cs.GetClusters(r.Context(), repoID)
		if err != nil {
			http.Error(w, "failed to list clusters: "+err.Error(), http.StatusInternalServerError)
			return
		}
		for _, c := range clusters {
			clusterIDs = append(clusterIDs, c.ID)
		}
	}

	if len(clusterIDs) == 0 {
		http.Error(w, "no clusters found for this repository — run indexing first", http.StatusConflict)
		return
	}

	if s.orchestrator == nil {
		http.Error(w, "job orchestrator unavailable", http.StatusServiceUnavailable)
		return
	}

	targetKey := fmt.Sprintf("relabel_clusters:%s", repoID)
	// R3 slice 3: relabel_clusters is the LLM-backed job_type in the
	// clustering subsystem (graph clustering itself is CPU-bound and
	// leaves llm_provider empty). Stamp the resolved provider.
	provider := ""
	if s.llmResolver != nil {
		if snap, err := s.llmResolver.Resolve(r.Context(), repoID, resolution.OpClusteringRelabel); err == nil {
			provider = snap.Provider
		}
	}
	enqReq := &llm.EnqueueRequest{
		Subsystem:   llm.SubsystemClustering,
		JobType:     "relabel_clusters",
		TargetKey:   targetKey,
		Priority:    llm.PriorityInteractive,
		RepoID:      repoID,
		LLMProvider: provider,
		RunWithContext: func(ctx context.Context, rt llm.Runtime) error {
			return runRelabelClusters(ctx, rt, cs, s.llmCaller, repoID, clusterIDs)
		},
	}

	job, err := s.orchestrator.Enqueue(enqReq)
	if err != nil {
		http.Error(w, "failed to enqueue relabel job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, relabelResponse{JobID: job.ID})
}

// runRelabelClusters is the job body for the relabel_clusters job type.
// For each cluster ID it calls the LLM with a compact prompt requesting a
// 1–3 word label derived from the representative symbols and packages, then
// writes the result to cluster.llm_label via SetClusterLLMLabel.
//
// Per-cluster failures are logged and skipped; one failure does not poison
// the whole batch.
func runRelabelClusters(ctx context.Context, rt llm.Runtime, cs clustering.ClusterStore, lc *llmcall.Caller, repoID string, clusterIDs []string) error {
	total := float64(len(clusterIDs))
	for i, clusterID := range clusterIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		progress := float64(i) / total
		rt.ReportProgress(progress, "relabeling", fmt.Sprintf("Relabeling cluster %d of %d", i+1, int(total)))

		c, err := cs.GetClusterByID(ctx, clusterID)
		if err != nil || c == nil {
			slog.Warn("relabel_clusters: cluster not found, skipping",
				"cluster_id", clusterID, "error", err)
			continue
		}

		// Build the prompt.
		syms := make([]string, 0, len(c.Members))
		for _, m := range c.Members {
			if len(syms) >= 5 {
				break
			}
			syms = append(syms, m.SymbolID)
		}

		label, err := relabelWithLLM(ctx, lc, repoID, c.Label, syms)
		if err != nil {
			slog.Warn("relabel_clusters: LLM call failed, skipping",
				"cluster_id", clusterID, "error", err)
			continue
		}

		if err := cs.SetClusterLLMLabel(ctx, clusterID, label); err != nil {
			if errors.Is(err, clustering.ErrClusterNotFound) {
				slog.Warn("relabel_clusters: cluster disappeared mid-relabel (likely due to concurrent reindex)",
					"cluster_id", clusterID, "repo_id", repoID)
			} else {
				slog.Warn("relabel_clusters: failed to persist llm_label, skipping",
					"cluster_id", clusterID, "error", err)
			}
		}
	}
	rt.ReportProgress(1.0, "ready", fmt.Sprintf("Relabeled %d clusters", len(clusterIDs)))
	return nil
}

// relabelWithLLM calls the worker's AnswerQuestion (via the LLM-aware
// llmcall.Caller wrapper) with a compact prompt that asks for a 1–3 word
// subsystem label. When the caller is nil/unavailable (OSS without a
// Python worker), it returns the heuristic label unchanged so the job
// still completes cleanly.
//
// Slice 2 of the workspace-LLM-source-of-truth plan replaced the prior
// direct *worker.Client wiring with the Caller wrapper so workspace-saved
// settings flow through gRPC metadata on every call.
func relabelWithLLM(ctx context.Context, lc *llmcall.Caller, repoID string, heuristicLabel string, symbolIDs []string) (string, error) {
	if lc == nil || !lc.IsAvailable() {
		return heuristicLabel, nil
	}
	symList := strings.Join(symbolIDs, ", ")
	prompt := fmt.Sprintf(
		"You are naming a software subsystem. Given the following representative symbol IDs: [%s] "+
			"and the current heuristic label %q, produce a concise 1–3 word label that describes "+
			"the subsystem's purpose. Respond with ONLY the label text — no punctuation, no explanation.",
		symList, heuristicLabel,
	)
	resp, err := lc.AnswerQuestion(ctx, repoID, resolution.OpClusteringRelabel, &reasoningv1.AnswerQuestionRequest{
		Question: prompt,
	})
	if err != nil {
		return "", fmt.Errorf("relabel LLM call: %w", err)
	}
	label := strings.TrimSpace(resp.GetAnswer())
	if label == "" {
		return heuristicLabel, nil
	}
	return label, nil
}
