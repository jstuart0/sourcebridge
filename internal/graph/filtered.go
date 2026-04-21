// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	"fmt"

	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// TenantFilteredStore wraps a GraphStore and restricts access to repositories
// belonging to a specific tenant. Methods that take a repoID parameter are
// checked against the allow list; methods operating on child entities (symbols,
// links, requirements by ID) pass through because the caller has already
// validated repo access at a higher level.
type TenantFilteredStore struct {
	inner      GraphStore
	allowedIDs map[string]bool
}

// NewTenantFilteredStore creates a filtered store that only allows access to
// the given repository IDs.
func NewTenantFilteredStore(inner GraphStore, repoIDs []string) *TenantFilteredStore {
	allowed := make(map[string]bool, len(repoIDs))
	for _, id := range repoIDs {
		allowed[id] = true
	}
	return &TenantFilteredStore{inner: inner, allowedIDs: allowed}
}

func (f *TenantFilteredStore) hasAccess(repoID string) bool {
	return f.allowedIDs[repoID]
}

// --- Repository operations ---

func (f *TenantFilteredStore) CreateRepository(name, path string) (*Repository, error) {
	return f.inner.CreateRepository(name, path)
}

func (f *TenantFilteredStore) StoreIndexResult(result *indexer.IndexResult) (*Repository, error) {
	// New repos are always allowed — the caller is responsible for
	// associating the new repo with the tenant after creation.
	return f.inner.StoreIndexResult(result)
}

func (f *TenantFilteredStore) ReplaceIndexResult(repoID string, result *indexer.IndexResult) (*Repository, error) {
	if !f.hasAccess(repoID) {
		return nil, fmt.Errorf("repository not found")
	}
	return f.inner.ReplaceIndexResult(repoID, result)
}

func (f *TenantFilteredStore) ListRepositories() []*Repository {
	all := f.inner.ListRepositories()
	filtered := make([]*Repository, 0, len(all))
	for _, r := range all {
		if f.hasAccess(r.ID) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func (f *TenantFilteredStore) GetRepository(id string) *Repository {
	if !f.hasAccess(id) {
		return nil
	}
	return f.inner.GetRepository(id)
}

func (f *TenantFilteredStore) GetRepositoryByPath(path string) *Repository {
	repo := f.inner.GetRepositoryByPath(path)
	if repo == nil || !f.hasAccess(repo.ID) {
		return nil
	}
	return repo
}

func (f *TenantFilteredStore) RemoveRepository(id string) bool {
	if !f.hasAccess(id) {
		return false
	}
	return f.inner.RemoveRepository(id)
}

func (f *TenantFilteredStore) SetRepositoryError(id string, err error) {
	if f.hasAccess(id) {
		f.inner.SetRepositoryError(id, err)
	}
}

func (f *TenantFilteredStore) UpdateRepositoryMeta(id string, meta RepositoryMeta) {
	if f.hasAccess(id) {
		f.inner.UpdateRepositoryMeta(id, meta)
	}
}

func (f *TenantFilteredStore) CacheUnderstandingScore(id string, overall float64) {
	if f.hasAccess(id) {
		f.inner.CacheUnderstandingScore(id, overall)
	}
}

// --- Repo-scoped operations (check access by repoID parameter) ---

func (f *TenantFilteredStore) GetFiles(repoID string) []*File {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetFiles(repoID)
}

func (f *TenantFilteredStore) GetFilesPaginated(repoID string, pathPrefix *string, limit, offset int) ([]*File, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetFilesPaginated(repoID, pathPrefix, limit, offset)
}

func (f *TenantFilteredStore) GetSymbols(repoID string, query *string, kind *string, limit, offset int) ([]*StoredSymbol, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetSymbols(repoID, query, kind, limit, offset)
}

func (f *TenantFilteredStore) GetSymbolsByFile(repoID string, filePath string) []*StoredSymbol {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetSymbolsByFile(repoID, filePath)
}

func (f *TenantFilteredStore) GetModules(repoID string) []*StoredModule {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetModules(repoID)
}

func (f *TenantFilteredStore) GetImports(repoID string) []*StoredImport {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetImports(repoID)
}

func (f *TenantFilteredStore) SearchContent(repoID, query string, limit int) []SearchResult {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.SearchContent(repoID, query, limit)
}

func (f *TenantFilteredStore) StoreRequirement(repoID string, req *StoredRequirement) {
	if f.hasAccess(repoID) {
		f.inner.StoreRequirement(repoID, req)
	}
}

func (f *TenantFilteredStore) StoreRequirements(repoID string, reqs []*StoredRequirement) int {
	if !f.hasAccess(repoID) {
		return 0
	}
	return f.inner.StoreRequirements(repoID, reqs)
}

func (f *TenantFilteredStore) GetRequirements(repoID string, limit, offset int) ([]*StoredRequirement, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetRequirements(repoID, limit, offset)
}

func (f *TenantFilteredStore) GetRequirementByExternalID(repoID, externalID string) *StoredRequirement {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetRequirementByExternalID(repoID, externalID)
}

func (f *TenantFilteredStore) StoreLink(repoID string, link *StoredLink) *StoredLink {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.StoreLink(repoID, link)
}

func (f *TenantFilteredStore) StoreLinks(repoID string, links []*StoredLink) int {
	if !f.hasAccess(repoID) {
		return 0
	}
	return f.inner.StoreLinks(repoID, links)
}

func (f *TenantFilteredStore) GetLinksForRepo(repoID string) []*StoredLink {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetLinksForRepo(repoID)
}

func (f *TenantFilteredStore) StoreLLMUsage(record *LLMUsageRecord) {
	f.inner.StoreLLMUsage(record)
}

func (f *TenantFilteredStore) GetLLMUsage(repoID string, limit int) []LLMUsageRecord {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetLLMUsage(repoID, limit)
}

// --- Entity-level operations (pass through — repo access validated upstream) ---

func (f *TenantFilteredStore) GetFileSymbols(fileID string) []*StoredSymbol {
	return f.inner.GetFileSymbols(fileID)
}

func (f *TenantFilteredStore) GetSymbol(id string) *StoredSymbol {
	return f.inner.GetSymbol(id)
}

func (f *TenantFilteredStore) GetSymbolsByIDs(ids []string) map[string]*StoredSymbol {
	return f.inner.GetSymbolsByIDs(ids)
}

func (f *TenantFilteredStore) GetCallers(symbolID string) []string {
	return f.inner.GetCallers(symbolID)
}

func (f *TenantFilteredStore) GetCallees(symbolID string) []string {
	return f.inner.GetCallees(symbolID)
}

func (f *TenantFilteredStore) GetCallEdges(repoID string) []CallEdge {
	return f.inner.GetCallEdges(repoID)
}

func (f *TenantFilteredStore) Stats() map[string]int {
	return f.inner.Stats()
}

func (f *TenantFilteredStore) GetRequirement(id string) *StoredRequirement {
	return f.inner.GetRequirement(id)
}

func (f *TenantFilteredStore) GetRequirementsByIDs(ids []string) map[string]*StoredRequirement {
	return f.inner.GetRequirementsByIDs(ids)
}

func (f *TenantFilteredStore) UpdateRequirement(id string, priority string, tags []string) *StoredRequirement {
	return f.inner.UpdateRequirement(id, priority, tags)
}

func (f *TenantFilteredStore) UpdateRequirementFields(id string, fields RequirementUpdate) *StoredRequirement {
	return f.inner.UpdateRequirementFields(id, fields)
}

func (f *TenantFilteredStore) GetLink(id string) *StoredLink {
	return f.inner.GetLink(id)
}

func (f *TenantFilteredStore) GetLinksForRequirement(reqID string, includeRejected bool) []*StoredLink {
	return f.inner.GetLinksForRequirement(reqID, includeRejected)
}

func (f *TenantFilteredStore) GetLinksForSymbol(symID string, includeRejected bool) []*StoredLink {
	return f.inner.GetLinksForSymbol(symID, includeRejected)
}

func (f *TenantFilteredStore) GetLinksForFile(fileID string, startLine, endLine int, minConfidence float64) []*StoredLink {
	return f.inner.GetLinksForFile(fileID, startLine, endLine, minConfidence)
}

func (f *TenantFilteredStore) VerifyLink(linkID string, verified bool, verifiedBy string) *StoredLink {
	return f.inner.VerifyLink(linkID, verified, verifiedBy)
}

func (f *TenantFilteredStore) StoreEmbedding(record *EmbeddingRecord) {
	f.inner.StoreEmbedding(record)
}

func (f *TenantFilteredStore) GetEmbedding(targetID string) *EmbeddingRecord {
	return f.inner.GetEmbedding(targetID)
}

func (f *TenantFilteredStore) StoreReviewResult(record *ReviewResultRecord) {
	f.inner.StoreReviewResult(record)
}

func (f *TenantFilteredStore) GetReviewResults(targetID string) []*ReviewResultRecord {
	return f.inner.GetReviewResults(targetID)
}

func (f *TenantFilteredStore) GetReviewResultsForRepo(repoID string) []*ReviewResultRecord {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetReviewResultsForRepo(repoID)
}

func (f *TenantFilteredStore) GetPublicSymbolDocCoverage(repoID string) (withDocs int, total int) {
	if !f.hasAccess(repoID) {
		return 0, 0
	}
	return f.inner.GetPublicSymbolDocCoverage(repoID)
}

func (f *TenantFilteredStore) GetTestSymbolRatio(repoID string) (tests int, total int) {
	if !f.hasAccess(repoID) {
		return 0, 0
	}
	return f.inner.GetTestSymbolRatio(repoID)
}

func (f *TenantFilteredStore) GetAICodeFileRatio(repoID string) (aiFiles int, totalFiles int) {
	if !f.hasAccess(repoID) {
		return 0, 0
	}
	return f.inner.GetAICodeFileRatio(repoID)
}

func (f *TenantFilteredStore) StoreImpactReport(repoID string, report *ImpactReport) {
	if !f.hasAccess(repoID) {
		return
	}
	f.inner.StoreImpactReport(repoID, report)
}

func (f *TenantFilteredStore) GetLatestImpactReport(repoID string) *ImpactReport {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetLatestImpactReport(repoID)
}

func (f *TenantFilteredStore) GetImpactReports(repoID string, limit int) ([]*ImpactReport, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetImpactReports(repoID, limit)
}

// --- Discovered Requirement operations ---

func (f *TenantFilteredStore) StoreDiscoveredRequirement(repoID string, req *DiscoveredRequirement) {
	if !f.hasAccess(repoID) {
		return
	}
	f.inner.StoreDiscoveredRequirement(repoID, req)
}

func (f *TenantFilteredStore) StoreDiscoveredRequirements(repoID string, reqs []*DiscoveredRequirement) int {
	if !f.hasAccess(repoID) {
		return 0
	}
	return f.inner.StoreDiscoveredRequirements(repoID, reqs)
}

func (f *TenantFilteredStore) GetDiscoveredRequirements(repoID string, status *string, confidence *string, limit, offset int) ([]*DiscoveredRequirement, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetDiscoveredRequirements(repoID, status, confidence, limit, offset)
}

func (f *TenantFilteredStore) GetDiscoveredRequirement(id string) *DiscoveredRequirement {
	return f.inner.GetDiscoveredRequirement(id)
}

func (f *TenantFilteredStore) PromoteDiscoveredRequirement(id string, requirementID string) *DiscoveredRequirement {
	return f.inner.PromoteDiscoveredRequirement(id, requirementID)
}

func (f *TenantFilteredStore) DismissDiscoveredRequirement(id string, dismissedBy string, reason string) *DiscoveredRequirement {
	return f.inner.DismissDiscoveredRequirement(id, dismissedBy, reason)
}

func (f *TenantFilteredStore) DeleteDiscoveredRequirementsByRepo(repoID string) int {
	if !f.hasAccess(repoID) {
		return 0
	}
	return f.inner.DeleteDiscoveredRequirementsByRepo(repoID)
}

// --- Cross-Repo Federation ---

func (f *TenantFilteredStore) LinkRepos(sourceRepoID, targetRepoID string) (*RepoLink, error) {
	if !f.hasAccess(sourceRepoID) || !f.hasAccess(targetRepoID) {
		return nil, fmt.Errorf("access denied")
	}
	return f.inner.LinkRepos(sourceRepoID, targetRepoID)
}
func (f *TenantFilteredStore) UnlinkRepos(linkID string) error {
	return f.inner.UnlinkRepos(linkID)
}
func (f *TenantFilteredStore) GetRepoLinks(repoID string) ([]*RepoLink, error) {
	if !f.hasAccess(repoID) {
		return nil, nil
	}
	return f.inner.GetRepoLinks(repoID)
}
func (f *TenantFilteredStore) StoreCrossRepoRef(ref *CrossRepoRef) error {
	return f.inner.StoreCrossRepoRef(ref)
}
func (f *TenantFilteredStore) StoreCrossRepoRefs(refs []*CrossRepoRef) int {
	return f.inner.StoreCrossRepoRefs(refs)
}
func (f *TenantFilteredStore) GetCrossRepoRefs(repoID string, refType *string, limit int) ([]*CrossRepoRef, error) {
	if !f.hasAccess(repoID) {
		return nil, nil
	}
	return f.inner.GetCrossRepoRefs(repoID, refType, limit)
}
func (f *TenantFilteredStore) GetSymbolCrossRepoRefs(symbolID string) ([]*CrossRepoRef, error) {
	return f.inner.GetSymbolCrossRepoRefs(symbolID)
}
func (f *TenantFilteredStore) DeleteCrossRepoRefsForRepo(repoID string) error {
	if !f.hasAccess(repoID) {
		return fmt.Errorf("access denied")
	}
	return f.inner.DeleteCrossRepoRefsForRepo(repoID)
}
func (f *TenantFilteredStore) DeleteCrossRepoRefsBetweenRepos(repoA, repoB string) error {
	return f.inner.DeleteCrossRepoRefsBetweenRepos(repoA, repoB)
}
func (f *TenantFilteredStore) StoreAPIContract(contract *APIContract) error {
	return f.inner.StoreAPIContract(contract)
}
func (f *TenantFilteredStore) GetAPIContracts(repoID string) ([]*APIContract, error) {
	if !f.hasAccess(repoID) {
		return nil, nil
	}
	return f.inner.GetAPIContracts(repoID)
}
func (f *TenantFilteredStore) DeleteAPIContractsForRepo(repoID string) error {
	if !f.hasAccess(repoID) {
		return fmt.Errorf("access denied")
	}
	return f.inner.DeleteAPIContractsForRepo(repoID)
}

// Verify at compile time that *TenantFilteredStore satisfies GraphStore.
var _ GraphStore = (*TenantFilteredStore)(nil)
