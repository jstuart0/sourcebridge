// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
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
	CreateRepository(name, path string) (*Repository, error)
	StoreIndexResult(result *indexer.IndexResult) (*Repository, error)
	ReplaceIndexResult(repoID string, result *indexer.IndexResult) (*Repository, error)
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
	MergeIndexResult(repoID string, affectedPaths []string, result *indexer.IndexResult) (*Repository, error)
	ListRepositories() []*Repository
	GetRepository(id string) *Repository
	GetRepositoryByPath(path string) *Repository
	RemoveRepository(id string) bool
	SetRepositoryError(id string, err error)
	UpdateRepositoryMeta(id string, meta RepositoryMeta)
	CacheUnderstandingScore(id string, overall float64)

	// File operations
	GetFiles(repoID string) []*File
	GetFilesPaginated(repoID string, pathPrefix *string, limit, offset int) ([]*File, int)
	GetFileSymbols(fileID string) []*StoredSymbol

	// Symbol operations
	GetSymbols(repoID string, query *string, kind *string, limit, offset int) ([]*StoredSymbol, int)
	GetSymbol(id string) *StoredSymbol
	GetSymbolsByIDs(ids []string) map[string]*StoredSymbol
	GetSymbolsByFile(repoID string, filePath string) []*StoredSymbol

	// Module operations
	GetModules(repoID string) []*StoredModule

	// Call graph
	GetCallers(symbolID string) []string
	GetCallees(symbolID string) []string
	GetCallEdges(repoID string) []CallEdge
	GetImports(repoID string) []*StoredImport

	// Package dependency aggregation.
	// RecomputePackageDependencies rebuilds the package-level caller/callee
	// edge map from raw imports. Call once at the end of each index run.
	RecomputePackageDependencies(repoID string)
	// GetPackageDependencies returns the pre-computed package dependency
	// records for a repository. Returns an empty slice for repos that have
	// not been indexed since this feature was added.
	GetPackageDependencies(repoID string) []*StoredPackageDependencies

	// Test linkage — "given a non-test symbol ID, return the IDs of
	// test symbols that exercise it." Populated from
	// IndexResult.Relations with Type=RelationTests. See the
	// in-memory Store.testedByGraph for the backing structure.
	GetTestsForSymbolPersisted(symbolID string) []string

	// Search
	SearchContent(repoID, query string, limit int) []SearchResult

	// Stats
	Stats() map[string]int

	// Requirement operations
	StoreRequirement(repoID string, req *StoredRequirement)
	StoreRequirements(repoID string, reqs []*StoredRequirement) int
	GetRequirements(repoID string, limit, offset int) ([]*StoredRequirement, int)
	GetRequirement(id string) *StoredRequirement
	GetRequirementsByIDs(ids []string) map[string]*StoredRequirement
	GetRequirementByExternalID(repoID, externalID string) *StoredRequirement
	// UpdateRequirementFields updates a requirement in place. Non-empty
	// string fields in `fields` replace the stored value; unset fields
	// are preserved. Returns the updated row or nil when the target is
	// missing/trashed.
	UpdateRequirementFields(id string, fields RequirementUpdate) *StoredRequirement

	// Link operations
	StoreLink(repoID string, link *StoredLink) *StoredLink
	StoreLinks(repoID string, links []*StoredLink) int
	GetLink(id string) *StoredLink
	GetLinksForRequirement(reqID string, includeRejected bool) []*StoredLink
	GetLinksForSymbol(symID string, includeRejected bool) []*StoredLink
	GetLinksForFile(fileID string, startLine, endLine int, minConfidence float64) []*StoredLink
	VerifyLink(linkID string, verified bool, verifiedBy string) *StoredLink
	GetLinksForRepo(repoID string) []*StoredLink

	// LLM usage tracking
	StoreLLMUsage(record *LLMUsageRecord)
	GetLLMUsage(repoID string, limit int) []LLMUsageRecord

	// Embedding cache
	StoreEmbedding(record *EmbeddingRecord)
	GetEmbedding(targetID string) *EmbeddingRecord

	// Review results
	StoreReviewResult(record *ReviewResultRecord)
	GetReviewResults(targetID string) []*ReviewResultRecord
	GetReviewResultsForRepo(repoID string) []*ReviewResultRecord

	// Understanding score helpers
	GetPublicSymbolDocCoverage(repoID string) (withDocs int, total int)
	GetTestSymbolRatio(repoID string) (tests int, total int)
	GetAICodeFileRatio(repoID string) (aiFiles int, totalFiles int)

	// Impact reports
	StoreImpactReport(repoID string, report *ImpactReport)
	GetLatestImpactReport(repoID string) *ImpactReport
	GetImpactReports(repoID string, limit int) ([]*ImpactReport, int)

	// Discovered requirement operations (spec extraction)
	StoreDiscoveredRequirement(repoID string, req *DiscoveredRequirement)
	StoreDiscoveredRequirements(repoID string, reqs []*DiscoveredRequirement) int
	GetDiscoveredRequirements(repoID string, status *string, confidence *string, limit, offset int) ([]*DiscoveredRequirement, int)
	GetDiscoveredRequirement(id string) *DiscoveredRequirement
	PromoteDiscoveredRequirement(id string, requirementID string) *DiscoveredRequirement
	DismissDiscoveredRequirement(id string, dismissedBy string, reason string) *DiscoveredRequirement
	DeleteDiscoveredRequirementsByRepo(repoID string) int

	// Cross-repo federation (OSS)
	LinkRepos(sourceRepoID, targetRepoID string) (*RepoLink, error)
	UnlinkRepos(linkID string) error
	GetRepoLinks(repoID string) ([]*RepoLink, error)

	StoreCrossRepoRef(ref *CrossRepoRef) error
	StoreCrossRepoRefs(refs []*CrossRepoRef) int
	GetCrossRepoRefs(repoID string, refType *string, limit int) ([]*CrossRepoRef, error)
	GetSymbolCrossRepoRefs(symbolID string) ([]*CrossRepoRef, error)
	DeleteCrossRepoRefsForRepo(repoID string) error
	DeleteCrossRepoRefsBetweenRepos(repoA, repoB string) error

	StoreAPIContract(contract *APIContract) error
	GetAPIContracts(repoID string) ([]*APIContract, error)
	DeleteAPIContractsForRepo(repoID string) error
}

// Verify at compile time that *Store satisfies GraphStore.
var _ GraphStore = (*Store)(nil)
