// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package clustering provides label-propagation subsystem clustering over
// the symbol call graph. Clusters are computed asynchronously after each
// index run and exposed via three MCP tools: get_subsystems,
// get_subsystem_by_id, and get_subsystem (by symbol).
package clustering

import "time"

// Cluster is a named group of symbols discovered by label propagation.
type Cluster struct {
	// ID is the SurrealDB record ID (e.g. "cluster:abc123").
	ID string `json:"id"`
	// RepoID is the repository this cluster belongs to.
	RepoID string `json:"repo_id"`
	// Label is a heuristic name derived from the dominant package path prefix
	// among member symbols. Set immediately; may be overridden by LLM rename.
	Label string `json:"label"`
	// LLMLabel is an optional LLM-generated name. Nil until the rename job runs.
	LLMLabel *string `json:"llm_label,omitempty"`
	// Size is the number of member symbols.
	Size int `json:"size"`
	// EdgeHash is the SHA-256 of sorted intra-cluster (src, dst) edge tuples.
	// Used to detect per-cluster structural changes.
	EdgeHash string `json:"edge_hash"`
	// Partial is true if the LPA run hit the iteration cap before convergence.
	Partial bool `json:"partial"`
	// Members holds the symbols in this cluster. Populated only when
	// explicitly requested (e.g. get_subsystem_by_id).
	Members []ClusterMember `json:"members,omitempty"`
	// CreatedAt is the timestamp of the first run that produced this cluster.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the timestamp of the most recent run.
	UpdatedAt time.Time `json:"updated_at"`
}

// ClusterMember associates a symbol with a cluster.
type ClusterMember struct {
	// ClusterID is the owning cluster's ID.
	ClusterID string `json:"cluster_id"`
	// SymbolID is the symbol's DB ID.
	SymbolID string `json:"symbol_id"`
	// RepoID is denormalized for efficient cleanup.
	RepoID string `json:"repo_id"`
}

// ClusterSummary is a lightweight view used by MCP tools and the setup CLI.
// It contains representative symbols ranked by in-degree and cross-cluster
// call counts but omits the full member list.
type ClusterSummary struct {
	// ID is the cluster's DB record ID.
	ID string `json:"id"`
	// Label is the heuristic or LLM-assigned name.
	Label string `json:"label"`
	// MemberCount is the total number of member symbols.
	MemberCount int `json:"member_count"`
	// RepresentativeSymbols holds the top symbols by in-degree within the cluster.
	RepresentativeSymbols []string `json:"representative_symbols"`
	// SelectionMethod documents the ranking strategy so MCP clients can reason
	// about it and callers can swap strategies without breaking the contract.
	SelectionMethod string `json:"selection_method"`
	// CrossClusterCalls maps cluster label → call count for edges that leave
	// this cluster and enter another.
	CrossClusterCalls map[string]int `json:"cross_cluster_calls,omitempty"`
	// MemberPackages is the de-duplicated set of file-system package paths
	// (e.g. "internal/auth") for all member symbols. Populated by the
	// living-wiki cold-start pipeline when it resolves architecture pages;
	// may be nil when retrieved via the MCP clustering tools which don't
	// need package-level dependency data.
	MemberPackages []string `json:"member_packages,omitempty"`
	// Partial is true if the run hit the iteration cap before convergence.
	Partial bool `json:"partial"`
}

// RunMetrics captures quality and performance statistics for a single
// clustering run. Logged to structured output; not persisted to DB.
type RunMetrics struct {
	// RepoID identifies the clustered repository.
	RepoID string
	// ClusterCount is the number of clusters produced.
	ClusterCount int
	// Iterations is the number of LPA iterations executed.
	Iterations int
	// Partial is true if the run hit the 50-iteration cap.
	Partial bool
	// ModularityQ is the Newman–Girvan modularity score (−1..1).
	// Higher is better; >0.3 indicates meaningful community structure.
	ModularityQ float64
	// SizeMin is the smallest cluster's member count.
	SizeMin int
	// SizeMax is the largest cluster's member count.
	SizeMax int
	// SizeP50 is the median cluster size.
	SizeP50 int
	// SizeP95 is the 95th-percentile cluster size.
	SizeP95 int
	// Unchanged is true when the edge hash matched and clustering was skipped.
	Unchanged bool
}
