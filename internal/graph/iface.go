// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import "github.com/sourcebridge/sourcebridge/internal/indexer"

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
	UpdateRequirement(id string, priority string, tags []string) *StoredRequirement

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
