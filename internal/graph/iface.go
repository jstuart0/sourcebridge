// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	"context"
	"errors"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ErrMergeNotSupported is returned by GraphStore.MergeIndexResult when
// the implementation does not support per-file merge semantics. The
// SurrealDB-backed store returns this in Phase 1.C; the per-file
// primitives are scheduled for the freshness-state migration in
// Phase 2 of the MCP-edits feedback-loop plan.
//
// Callers in the change-watch path (Phase 1.C) should treat this as
// "the operator is using a backend that doesn't yet support
// change-watch" and surface it through the freshness envelope rather
// than panicking. The umbrella SOURCEBRIDGE_CHANGE_WATCH_ENABLED flag
// is default-off in Phase 1.C precisely so this surface does not get
// exercised in production until Phase 2 lands the SurrealDB merge
// primitives.
var ErrMergeNotSupported = errors.New("graphstore: MergeIndexResult not supported by this backend")

// RepositoryMeta holds mutable metadata fields for a repository.
type RepositoryMeta struct {
	ClonePath             string
	RemoteURL             string
	CommitSHA             string
	Branch                string
	AuthToken             string // personal access token for private HTTPS repos
	GenerationModeDefault string
}

// CallEdge represents a single caller→callee relationship.
type CallEdge struct {
	CallerID string
	CalleeID string
}

// GraphStore is the interface satisfied by both the in-memory Store and the
// SurrealDB-backed store. All API-layer code should depend on this interface
// rather than on a concrete implementation so that the storage backend can be
// swapped via configuration.
type GraphStore interface {
	// Repository operations
	CreateRepository(ctx context.Context, name, path string) (*Repository, error)
	StoreIndexResult(ctx context.Context, result *indexer.IndexResult) (*Repository, error)
	ReplaceIndexResult(ctx context.Context, repoID string, result *indexer.IndexResult) (*Repository, error)
	// MergeIndexResult applies a per-file delta to the existing graph
	// state for repoID. Only rows whose file path is in affectedPaths
	// are touched; every other file (and its symbols, imports, edges)
	// is preserved with its existing IDs.
	//
	// Semantics per affectedPath:
	//   - If the path appears in result.Files, the existing file row
	//     (and its symbols and imports) is dropped and re-inserted with
	//     fresh UUIDs from the merged result. Edges (call graph,
	//     test linkage) keyed to the dropped symbols are removed.
	//   - If the path does NOT appear in result.Files, the existing
	//     file row (and its symbols, imports, and dependent edges) is
	//     dropped — this is the deletion case.
	//   - If the path is in result.Files but not currently in the store,
	//     the file is inserted as net-new.
	//
	// After per-file delta application, modules are re-derived (cheap)
	// and the relations slice from result is re-inserted; both are
	// derived from the merged file set by indexer.IndexFiles, so this
	// is idempotent. Repository metadata aggregates (FileCount,
	// FunctionCount, ClassCount, LastIndexedAt) are recomputed.
	//
	// MergeIndexResult is the only graphstore mutation the change-watch
	// router (Phase 1.C) calls. ReplaceIndexResult atomically replaces
	// every row for the repo, which would clobber the per-file delta;
	// the router cannot use it.
	//
	// Returns ErrMergeNotSupported when the implementation does not
	// support per-file merge semantics (e.g. the SurrealDB backend in
	// Phase 1.C, which lacks the per-file primitives that land alongside
	// the freshness-state migration in Phase 2).
	MergeIndexResult(ctx context.Context, repoID string, affectedPaths []string, result *indexer.IndexResult) (*Repository, error)
	ListRepositories(ctx context.Context) []*Repository
	GetRepository(ctx context.Context, id string) *Repository
	GetRepositoryByPath(ctx context.Context, path string) *Repository
	RemoveRepository(ctx context.Context, id string) bool
	SetRepositoryError(ctx context.Context, id string, err error)
	UpdateRepositoryMeta(ctx context.Context, id string, meta RepositoryMeta)
	CacheUnderstandingScore(ctx context.Context, id string, overall float64)

	// File operations
	GetFiles(ctx context.Context, repoID string) []*File
	GetFilesPaginated(ctx context.Context, repoID string, pathPrefix *string, limit, offset int) ([]*File, int)
	GetFileSymbols(ctx context.Context, fileID string) []*StoredSymbol

	// Symbol operations
	GetSymbols(ctx context.Context, repoID string, query *string, kind *string, limit, offset int) ([]*StoredSymbol, int)
	GetSymbol(ctx context.Context, id string) *StoredSymbol
	GetSymbolsByIDs(ctx context.Context, ids []string) map[string]*StoredSymbol
	GetSymbolsByFile(ctx context.Context, repoID string, filePath string) []*StoredSymbol

	// Module operations
	GetModules(ctx context.Context, repoID string) []*StoredModule

	// Call graph
	GetCallers(ctx context.Context, symbolID string) []string
	GetCallees(ctx context.Context, symbolID string) []string
	GetCallEdges(ctx context.Context, repoID string) []CallEdge
	GetImports(ctx context.Context, repoID string) []*StoredImport

	// Package dependency aggregation.
	// RecomputePackageDependencies rebuilds the package-level caller/callee
	// edge map from raw imports. Call once at the end of each index run.
	RecomputePackageDependencies(ctx context.Context, repoID string)
	// GetPackageDependencies returns the pre-computed package dependency
	// records for a repository. Returns an empty slice for repos that have
	// not been indexed since this feature was added.
	GetPackageDependencies(ctx context.Context, repoID string) []*StoredPackageDependencies

	// Test linkage — "given a non-test symbol ID, return the IDs of
	// test symbols that exercise it." Populated from
	// IndexResult.Relations with Type=RelationTests. See the
	// in-memory Store.testedByGraph for the backing structure.
	GetTestsForSymbolPersisted(ctx context.Context, symbolID string) []string

	// Search
	SearchContent(ctx context.Context, repoID, query string, limit int) []SearchResult

	// Stats
	Stats(ctx context.Context) map[string]int

	// Requirement operations
	StoreRequirement(ctx context.Context, repoID string, req *StoredRequirement)
	StoreRequirements(ctx context.Context, repoID string, reqs []*StoredRequirement) int
	GetRequirements(ctx context.Context, repoID string, limit, offset int) ([]*StoredRequirement, int)
	GetRequirement(ctx context.Context, id string) *StoredRequirement
	GetRequirementsByIDs(ctx context.Context, ids []string) map[string]*StoredRequirement
	GetRequirementByExternalID(ctx context.Context, repoID, externalID string) *StoredRequirement
	// UpdateRequirementFields updates a requirement in place. Non-empty
	// string fields in `fields` replace the stored value; unset fields
	// are preserved. Returns the updated row or nil when the target is
	// missing/trashed.
	UpdateRequirementFields(ctx context.Context, id string, fields RequirementUpdate) *StoredRequirement

	// Link operations
	StoreLink(ctx context.Context, repoID string, link *StoredLink) *StoredLink
	StoreLinks(ctx context.Context, repoID string, links []*StoredLink) int
	GetLink(ctx context.Context, id string) *StoredLink
	GetLinksForRequirement(ctx context.Context, reqID string, includeRejected bool) []*StoredLink
	GetLinksForSymbol(ctx context.Context, symID string, includeRejected bool) []*StoredLink
	GetLinksForFile(ctx context.Context, fileID string, startLine, endLine int, minConfidence float64) []*StoredLink
	VerifyLink(ctx context.Context, linkID string, verified bool, verifiedBy string) *StoredLink
	GetLinksForRepo(ctx context.Context, repoID string) []*StoredLink

	// LLM usage tracking
	StoreLLMUsage(ctx context.Context, record *LLMUsageRecord)
	GetLLMUsage(ctx context.Context, repoID string, limit int) []LLMUsageRecord

	// Embedding cache
	StoreEmbedding(ctx context.Context, record *EmbeddingRecord)
	GetEmbedding(ctx context.Context, targetID string) *EmbeddingRecord

	// Review results
	StoreReviewResult(ctx context.Context, record *ReviewResultRecord)
	GetReviewResults(ctx context.Context, targetID string) []*ReviewResultRecord
	GetReviewResultsForRepo(ctx context.Context, repoID string) []*ReviewResultRecord

	// Understanding score helpers
	GetPublicSymbolDocCoverage(ctx context.Context, repoID string) (withDocs int, total int)
	GetTestSymbolRatio(ctx context.Context, repoID string) (tests int, total int)
	GetAICodeFileRatio(ctx context.Context, repoID string) (aiFiles int, totalFiles int)

	// Impact reports
	StoreImpactReport(ctx context.Context, repoID string, report *ImpactReport)
	GetLatestImpactReport(ctx context.Context, repoID string) *ImpactReport
	GetImpactReports(ctx context.Context, repoID string, limit int) ([]*ImpactReport, int)

	// Discovered requirement operations (spec extraction)
	StoreDiscoveredRequirement(ctx context.Context, repoID string, req *DiscoveredRequirement)
	StoreDiscoveredRequirements(ctx context.Context, repoID string, reqs []*DiscoveredRequirement) int
	GetDiscoveredRequirements(ctx context.Context, repoID string, status *string, confidence *string, limit, offset int) ([]*DiscoveredRequirement, int)
	GetDiscoveredRequirement(ctx context.Context, id string) *DiscoveredRequirement
	PromoteDiscoveredRequirement(ctx context.Context, id string, requirementID string) *DiscoveredRequirement
	DismissDiscoveredRequirement(ctx context.Context, id string, dismissedBy string, reason string) *DiscoveredRequirement
	DeleteDiscoveredRequirementsByRepo(ctx context.Context, repoID string) int

	// Cross-repo federation (OSS)
	LinkRepos(ctx context.Context, sourceRepoID, targetRepoID string) (*RepoLink, error)
	UnlinkRepos(ctx context.Context, linkID string) error
	GetRepoLinks(ctx context.Context, repoID string) ([]*RepoLink, error)
	// GetRepoLink looks up a single repo link by ID. Used by TenantFilteredStore
	// to validate access before UnlinkRepos and VerifyLink mutations. In the
	// in-memory store, federation is stubbed — returns nil (Decision 10).
	GetRepoLink(ctx context.Context, linkID string) *RepoLink

	StoreCrossRepoRef(ctx context.Context, ref *CrossRepoRef) error
	StoreCrossRepoRefs(ctx context.Context, refs []*CrossRepoRef) int
	GetCrossRepoRefs(ctx context.Context, repoID string, refType *string, limit int) ([]*CrossRepoRef, error)
	GetSymbolCrossRepoRefs(ctx context.Context, symbolID string) ([]*CrossRepoRef, error)
	DeleteCrossRepoRefsForRepo(ctx context.Context, repoID string) error
	DeleteCrossRepoRefsBetweenRepos(ctx context.Context, repoA, repoB string) error

	StoreAPIContract(ctx context.Context, contract *APIContract) error
	GetAPIContracts(ctx context.Context, repoID string) ([]*APIContract, error)
	DeleteAPIContractsForRepo(ctx context.Context, repoID string) error
}

// Verify at compile time that *Store satisfies GraphStore.
var _ GraphStore = (*Store)(nil)
