// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package clustering

import (
	"context"
	"errors"

	"github.com/sourcebridge/sourcebridge/internal/graph"
)

// ErrClusterNotFound is returned by SetClusterLLMLabel when the target cluster
// no longer exists — typically because a concurrent ReplaceClusters call deleted
// it during a re-index. Callers should log a warning and continue rather than
// treating this as a fatal error.
var ErrClusterNotFound = errors.New("cluster not found")

// ClusterStore is the narrow persistence interface consumed by the clustering
// package. The concrete *db.SurrealStore satisfies it implicitly; the
// in-memory *graph.Store also satisfies it via the methods added in
// graph/store.go. No GraphStore interface extension is required.
//
// This interface lives in the clustering package (not in db or graph) so the
// dependency arrow points inward: clustering → store interface, not store →
// clustering.
//
// Method signatures for GetCallEdges and GetSymbolsByIDs intentionally match
// the existing GraphStore interface so both in-memory and SurrealDB stores
// satisfy ClusterStore without any new methods on those paths.
type ClusterStore interface {
	// Call graph access — reuses existing GraphStore signatures.

	// GetCallEdges returns all caller→callee edges for the given repository.
	GetCallEdges(ctx context.Context, repoID string) []graph.CallEdge

	// GetSymbolsByIDs returns a map of symbol ID → symbol for the given IDs.
	GetSymbolsByIDs(ctx context.Context, ids []string) map[string]*graph.StoredSymbol

	// Edge-hash delta check — stored on the ca_repository record.

	// GetRepoEdgeHash returns the previously stored SHA-256 edge hash for
	// the repository's call graph, or an empty string if none has been stored.
	GetRepoEdgeHash(ctx context.Context, repoID string) (string, error)

	// SetRepoEdgeHash stores the SHA-256 edge hash on the repository record.
	SetRepoEdgeHash(ctx context.Context, repoID, hash string) error

	// Cluster persistence.

	// ReplaceClusters atomically deletes all existing clusters for a repository
	// and inserts the new set in a single transaction, so readers never observe
	// an empty window between the delete and the insert.
	//
	// This is the preferred method for the clustering job. DeleteClusters is
	// retained as a separate non-transactional call for repo removal and other
	// one-shot invalidation paths where atomicity with an insert is not needed.
	ReplaceClusters(ctx context.Context, repoID string, clusters []Cluster) error

	// SaveClusters persists a set of clusters and their members. The caller is
	// responsible for calling DeleteClusters or ReplaceClusters to avoid
	// duplicates. Prefer ReplaceClusters when doing a full replace.
	SaveClusters(ctx context.Context, repoID string, clusters []Cluster) error

	// GetClusters returns all clusters for a repository, without member lists.
	GetClusters(ctx context.Context, repoID string) ([]Cluster, error)

	// GetClusterByID returns a single cluster with its full member list.
	// Returns nil if the cluster does not exist.
	GetClusterByID(ctx context.Context, clusterID string) (*Cluster, error)

	// GetClusterForSymbol returns the cluster containing the given symbol in
	// the given repository, or nil if the symbol is not in any cluster.
	GetClusterForSymbol(ctx context.Context, repoID, symbolID string) (*Cluster, error)

	// DeleteClusters removes all clusters and cluster_member records for a
	// repository. Called on repo deletion and at the start of each re-cluster.
	// Prefer ReplaceClusters when the replacement clusters are already available.
	DeleteClusters(ctx context.Context, repoID string) error

	// SetClusterLLMLabel writes an LLM-generated label for a single cluster.
	// Called by the relabel_clusters job after the LLM assigns a name.
	// Errors are logged and skipped by the job; the cluster keeps its heuristic
	// label if this call fails.
	SetClusterLLMLabel(ctx context.Context, clusterID string, label string) error
}
