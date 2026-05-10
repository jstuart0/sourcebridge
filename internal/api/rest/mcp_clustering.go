// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// Subsystem clustering MCP tools.
//
// Three tools gated by the subsystem_clustering capability:
//
//   get_subsystems       — list all clusters for a repo with representative
//                          symbols and cross-cluster call counts.
//
//   get_subsystem_by_id  — fetch one cluster by its ID including the full
//                          member list.
//
//   get_subsystem        — find the cluster containing a given symbol and
//                          return 5 peer symbols.

func (h *mcpHandler) clusteringToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name:        "get_subsystems",
			Description: "List the subsystems (clusters) of a repository derived from label-propagation clustering on the call graph. Returns representative symbols ranked by in-degree, cross-cluster call counts, and a status field indicating whether the clustering is ready or still computing.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"limit":   map[string]interface{}{"type": "integer", "description": "Maximum number of clusters to return (default 50, cap 200)"},
				},
				"required": []string{"repo_id"},
			},
		},
		{
			Name:        "get_subsystem_by_id",
			Description: "Fetch a specific subsystem cluster by its ID. Returns the full member list, not just the top representative symbols. Use this when you already have a cluster ID from a prior get_subsystems call.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cluster_id": map[string]interface{}{"type": "string", "description": "Cluster ID (e.g. 'cluster:abc123') returned by get_subsystems"},
				},
				"required": []string{"cluster_id"},
			},
		},
		{
			Name:        "get_subsystem",
			Description: "Find the subsystem cluster that contains a given symbol. Returns the cluster summary plus 5 peer symbols from the same cluster. Use this before refactoring a symbol to understand its module boundaries.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo_id":   map[string]interface{}{"type": "string", "description": "Repository ID"},
					"symbol_id": map[string]interface{}{"type": "string", "description": "Symbol ID to look up"},
				},
				"required": []string{"repo_id", "symbol_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// get_subsystems
// ---------------------------------------------------------------------------

type subsystemsResult struct {
	RepoID      string                    `json:"repo_id"`
	Status      string                    `json:"status"`
	Clusters    []clustering.ClusterSummary `json:"clusters"`
	GeneratedAt string                    `json:"generated_at"`
}

func (h *mcpHandler) callGetSubsystems(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepoID string `json:"repo_id"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepoID); err != nil {
		return nil, err
	}
	if err := clustering.ValidateID(params.RepoID); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	if h.clusterStore == nil {
		return subsystemsResult{
			RepoID:      params.RepoID,
			Status:      "unavailable",
			Clusters:    nil,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		}, nil
	}

	clusters, err := h.clusterStore.GetClusters(ctx, params.RepoID)
	if err != nil || len(clusters) == 0 {
		return subsystemsResult{
			RepoID:      params.RepoID,
			Status:      "pending",
			Clusters:    nil,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		}, nil
	}

	// Build cross-cluster call index.
	edges := h.store.GetCallEdges(ctx, params.RepoID)
	clusterBySymbol := buildClusterBySymbol(clusters)
	crossCalls := buildCrossClusterCalls(edges, clusterBySymbol, clusters)

	// Build in-degree index for representative symbol selection.
	inDegree := buildInDegree(edges)

	// Assemble summaries (cap at limit).
	if limit < len(clusters) {
		clusters = clusters[:limit]
	}
	summaries := make([]clustering.ClusterSummary, 0, len(clusters))
	for _, c := range clusters {
		summaries = append(summaries, buildClusterSummary(c, inDegree, crossCalls))
	}

	// Determine status: if any cluster is partial, indicate that.
	status := "ready"
	for _, c := range clusters {
		if c.Partial {
			status = "partial"
			break
		}
	}

	return subsystemsResult{
		RepoID:      params.RepoID,
		Status:      status,
		Clusters:    summaries,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ---------------------------------------------------------------------------
// get_subsystem_by_id
// ---------------------------------------------------------------------------

type subsystemByIDResult struct {
	Cluster clustering.Cluster `json:"cluster"`
}

func (h *mcpHandler) callGetSubsystemByID(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		ClusterID string `json:"cluster_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if params.ClusterID == "" {
		return nil, errInvalidArguments("cluster_id is required")
	}
	if err := clustering.ValidateID(params.ClusterID); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if h.clusterStore == nil {
		return nil, fmt.Errorf("clustering not available on this deployment")
	}

	c, err := h.clusterStore.GetClusterByID(ctx, params.ClusterID)
	if err != nil || c == nil {
		return nil, fmt.Errorf("cluster %q not found", params.ClusterID)
	}

	// Auth: ensure the caller can access the repo this cluster belongs to.
	if err := h.checkRepoAccess(session, c.RepoID); err != nil {
		return nil, err
	}

	return subsystemByIDResult{Cluster: *c}, nil
}

// ---------------------------------------------------------------------------
// get_subsystem (by symbol)
// ---------------------------------------------------------------------------

type subsystemForSymbolResult struct {
	RepoID      string                    `json:"repo_id"`
	SymbolID    string                    `json:"symbol_id"`
	Cluster     *clustering.ClusterSummary `json:"cluster"`
	PeerSymbols []string                  `json:"peer_symbols"`
	Message     string                    `json:"message,omitempty"`
}

func (h *mcpHandler) callGetSubsystem(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepoID   string `json:"repo_id"`
		SymbolID string `json:"symbol_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepoID); err != nil {
		return nil, err
	}
	if err := clustering.ValidateID(params.RepoID); err != nil {
		return nil, errInvalidArguments("repo_id: " + err.Error())
	}
	if err := clustering.ValidateID(params.SymbolID); err != nil {
		return nil, errInvalidArguments("symbol_id: " + err.Error())
	}

	if h.clusterStore == nil {
		return subsystemForSymbolResult{
			RepoID:   params.RepoID,
			SymbolID: params.SymbolID,
			Message:  "clustering not available on this deployment",
		}, nil
	}

	c, err := h.clusterStore.GetClusterForSymbol(ctx, params.RepoID, params.SymbolID)
	if err != nil || c == nil {
		return subsystemForSymbolResult{
			RepoID:   params.RepoID,
			SymbolID: params.SymbolID,
			Message:  "symbol not found in any cluster — run indexing first",
		}, nil
	}

	// Build summary from the cluster's member list.
	edges := h.store.GetCallEdges(ctx, params.RepoID)
	inDegree := buildInDegree(edges)

	// Gather all clusters for cross-cluster call computation.
	allClusters, _ := h.clusterStore.GetClusters(ctx, params.RepoID)
	clusterBySymbol := buildClusterBySymbol(allClusters)
	crossCalls := buildCrossClusterCalls(edges, clusterBySymbol, allClusters)

	summary := buildClusterSummary(*c, inDegree, crossCalls)

	// Pick 5 peer symbols: members of the same cluster, sorted by in-degree.
	const maxPeers = 5
	var peers []string
	for _, m := range c.Members {
		if m.SymbolID == params.SymbolID {
			continue
		}
		peers = append(peers, m.SymbolID)
	}
	if len(peers) > maxPeers {
		peers = peers[:maxPeers]
	}
	// Resolve symbol names for readability.
	symMap := h.store.GetSymbolsByIDs(ctx, peers)
	peerNames := make([]string, 0, len(peers))
	for _, id := range peers {
		if sym, ok := symMap[id]; ok && sym != nil {
			peerNames = append(peerNames, sym.Name)
		} else {
			peerNames = append(peerNames, id)
		}
	}

	return subsystemForSymbolResult{
		RepoID:      params.RepoID,
		SymbolID:    params.SymbolID,
		Cluster:     &summary,
		PeerSymbols: peerNames,
	}, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// buildClusterBySymbol maps symbol ID → cluster label (string key for the
// cluster map used in cross-cluster call computation).
func buildClusterBySymbol(clusters []clustering.Cluster) map[string]string {
	m := make(map[string]string, len(clusters)*8)
	for _, c := range clusters {
		for _, mem := range c.Members {
			m[mem.SymbolID] = c.ID
		}
	}
	return m
}

// buildInDegree counts in-degree for each callee in the edge set.
func buildInDegree(edges []graph.CallEdge) map[string]int {
	d := make(map[string]int, len(edges))
	for _, e := range edges {
		d[e.CalleeID]++
	}
	return d
}

// buildCrossClusterCalls returns a map: clusterID → (targetClusterLabel → count)
// for edges that cross cluster boundaries.
func buildCrossClusterCalls(
	edges []graph.CallEdge,
	clusterBySymbol map[string]string,
	clusters []clustering.Cluster,
) map[string]map[string]int {
	// Build clusterID → label map for the cross-cluster label display.
	idToLabel := make(map[string]string, len(clusters))
	for _, c := range clusters {
		idToLabel[c.ID] = c.Label
	}

	out := make(map[string]map[string]int)
	for _, e := range edges {
		srcCluster := clusterBySymbol[e.CallerID]
		dstCluster := clusterBySymbol[e.CalleeID]
		if srcCluster == "" || dstCluster == "" || srcCluster == dstCluster {
			continue
		}
		if out[srcCluster] == nil {
			out[srcCluster] = make(map[string]int)
		}
		dstLabel := idToLabel[dstCluster]
		if dstLabel == "" {
			dstLabel = dstCluster
		}
		out[srcCluster][dstLabel]++
	}
	return out
}

// buildClusterSummary creates a ClusterSummary from a Cluster, in-degree index,
// and cross-cluster call map.
func buildClusterSummary(
	c clustering.Cluster,
	inDegree map[string]int,
	crossCalls map[string]map[string]int,
) clustering.ClusterSummary {
	label := c.Label
	if c.LLMLabel != nil && *c.LLMLabel != "" {
		label = *c.LLMLabel
	}

	// Select top 3 representative symbols by in-degree within the cluster.
	const maxRep = 3
	type ranked struct {
		id    string
		score int
	}
	var ranked_ []ranked
	for _, m := range c.Members {
		ranked_ = append(ranked_, ranked{id: m.SymbolID, score: inDegree[m.SymbolID]})
	}
	// Sort descending by in-degree.
	for i := 0; i < len(ranked_) && i < maxRep; i++ {
		for j := i + 1; j < len(ranked_); j++ {
			if ranked_[j].score > ranked_[i].score {
				ranked_[i], ranked_[j] = ranked_[j], ranked_[i]
			}
		}
	}
	repIDs := make([]string, 0, maxRep)
	for i := 0; i < len(ranked_) && i < maxRep; i++ {
		repIDs = append(repIDs, ranked_[i].id)
	}

	return clustering.ClusterSummary{
		ID:                    c.ID,
		Label:                 label,
		MemberCount:           c.Size,
		RepresentativeSymbols: repIDs,
		SelectionMethod:       "highest_in_degree",
		CrossClusterCalls:     crossCalls[c.ID],
		Partial:               c.Partial,
	}
}
