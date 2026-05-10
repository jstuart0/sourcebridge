// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graph

import (
	"context"
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

func (f *TenantFilteredStore) CreateRepository(ctx context.Context, name, path string) (*Repository, error) {
	return f.inner.CreateRepository(ctx, name, path)
}

func (f *TenantFilteredStore) StoreIndexResult(ctx context.Context, result *indexer.IndexResult) (*Repository, error) {
	// New repos are always allowed — the caller is responsible for
	// associating the new repo with the tenant after creation.
	return f.inner.StoreIndexResult(ctx, result)
}

func (f *TenantFilteredStore) ReplaceIndexResult(ctx context.Context, repoID string, result *indexer.IndexResult) (*Repository, error) {
	if !f.hasAccess(repoID) {
		return nil, fmt.Errorf("repository not found")
	}
	return f.inner.ReplaceIndexResult(ctx, repoID, result)
}

// MergeIndexResult enforces the tenant gate before delegating to the
// inner store. Same access discipline as ReplaceIndexResult — events
// for repos a tenant cannot access are rejected before reaching the
// shared store.
func (f *TenantFilteredStore) MergeIndexResult(ctx context.Context, repoID string, affectedPaths []string, result *indexer.IndexResult) (*Repository, error) {
	if !f.hasAccess(repoID) {
		return nil, fmt.Errorf("repository not found")
	}
	return f.inner.MergeIndexResult(ctx, repoID, affectedPaths, result)
}

func (f *TenantFilteredStore) ListRepositories(ctx context.Context) []*Repository {
	all := f.inner.ListRepositories(ctx)
	filtered := make([]*Repository, 0, len(all))
	for _, r := range all {
		if f.hasAccess(r.ID) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func (f *TenantFilteredStore) GetRepository(ctx context.Context, id string) *Repository {
	if !f.hasAccess(id) {
		return nil
	}
	return f.inner.GetRepository(ctx, id)
}

func (f *TenantFilteredStore) GetRepositoryByPath(ctx context.Context, path string) *Repository {
	repo := f.inner.GetRepositoryByPath(ctx, path)
	if repo == nil || !f.hasAccess(repo.ID) {
		return nil
	}
	return repo
}

func (f *TenantFilteredStore) RemoveRepository(ctx context.Context, id string) bool {
	if !f.hasAccess(id) {
		return false
	}
	return f.inner.RemoveRepository(ctx, id)
}

func (f *TenantFilteredStore) SetRepositoryError(ctx context.Context, id string, err error) {
	if f.hasAccess(id) {
		f.inner.SetRepositoryError(ctx, id, err)
	}
}

func (f *TenantFilteredStore) UpdateRepositoryMeta(ctx context.Context, id string, meta RepositoryMeta) {
	if f.hasAccess(id) {
		f.inner.UpdateRepositoryMeta(ctx, id, meta)
	}
}

func (f *TenantFilteredStore) CacheUnderstandingScore(ctx context.Context, id string, overall float64) {
	if f.hasAccess(id) {
		f.inner.CacheUnderstandingScore(ctx, id, overall)
	}
}

// --- Repo-scoped operations (check access by repoID parameter) ---

func (f *TenantFilteredStore) GetFiles(ctx context.Context, repoID string) []*File {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetFiles(ctx, repoID)
}

func (f *TenantFilteredStore) GetFilesPaginated(ctx context.Context, repoID string, pathPrefix *string, limit, offset int) ([]*File, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetFilesPaginated(ctx, repoID, pathPrefix, limit, offset)
}

func (f *TenantFilteredStore) GetSymbols(ctx context.Context, repoID string, query *string, kind *string, limit, offset int) ([]*StoredSymbol, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetSymbols(ctx, repoID, query, kind, limit, offset)
}

func (f *TenantFilteredStore) GetSymbolsByFile(ctx context.Context, repoID string, filePath string) []*StoredSymbol {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetSymbolsByFile(ctx, repoID, filePath)
}

func (f *TenantFilteredStore) GetModules(ctx context.Context, repoID string) []*StoredModule {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetModules(ctx, repoID)
}

func (f *TenantFilteredStore) GetImports(ctx context.Context, repoID string) []*StoredImport {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetImports(ctx, repoID)
}

func (f *TenantFilteredStore) RecomputePackageDependencies(ctx context.Context, repoID string) {
	if f.hasAccess(repoID) {
		f.inner.RecomputePackageDependencies(ctx, repoID)
	}
}

func (f *TenantFilteredStore) GetPackageDependencies(ctx context.Context, repoID string) []*StoredPackageDependencies {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetPackageDependencies(ctx, repoID)
}

func (f *TenantFilteredStore) SearchContent(ctx context.Context, repoID, query string, limit int) []SearchResult {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.SearchContent(ctx, repoID, query, limit)
}

func (f *TenantFilteredStore) StoreRequirement(ctx context.Context, repoID string, req *StoredRequirement) {
	if f.hasAccess(repoID) {
		f.inner.StoreRequirement(ctx, repoID, req)
	}
}

func (f *TenantFilteredStore) StoreRequirements(ctx context.Context, repoID string, reqs []*StoredRequirement) int {
	if !f.hasAccess(repoID) {
		return 0
	}
	return f.inner.StoreRequirements(ctx, repoID, reqs)
}

func (f *TenantFilteredStore) GetRequirements(ctx context.Context, repoID string, limit, offset int) ([]*StoredRequirement, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetRequirements(ctx, repoID, limit, offset)
}

func (f *TenantFilteredStore) GetRequirementByExternalID(ctx context.Context, repoID, externalID string) *StoredRequirement {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetRequirementByExternalID(ctx, repoID, externalID)
}

func (f *TenantFilteredStore) StoreLink(ctx context.Context, repoID string, link *StoredLink) *StoredLink {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.StoreLink(ctx, repoID, link)
}

func (f *TenantFilteredStore) StoreLinks(ctx context.Context, repoID string, links []*StoredLink) int {
	if !f.hasAccess(repoID) {
		return 0
	}
	return f.inner.StoreLinks(ctx, repoID, links)
}

func (f *TenantFilteredStore) GetLinksForRepo(ctx context.Context, repoID string) []*StoredLink {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetLinksForRepo(ctx, repoID)
}

func (f *TenantFilteredStore) StoreLLMUsage(ctx context.Context, record *LLMUsageRecord) {
	f.inner.StoreLLMUsage(ctx, record)
}

func (f *TenantFilteredStore) GetLLMUsage(ctx context.Context, repoID string, limit int) []LLMUsageRecord {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetLLMUsage(ctx, repoID, limit)
}

// --- Entity-level operations (pass through — repo access validated upstream) ---

func (f *TenantFilteredStore) GetFileSymbols(ctx context.Context, fileID string) []*StoredSymbol {
	return f.inner.GetFileSymbols(ctx, fileID)
}

func (f *TenantFilteredStore) GetSymbol(ctx context.Context, id string) *StoredSymbol {
	return f.inner.GetSymbol(ctx, id)
}

func (f *TenantFilteredStore) GetSymbolsByIDs(ctx context.Context, ids []string) map[string]*StoredSymbol {
	return f.inner.GetSymbolsByIDs(ctx, ids)
}

func (f *TenantFilteredStore) GetCallers(ctx context.Context, symbolID string) []string {
	return f.inner.GetCallers(ctx, symbolID)
}

func (f *TenantFilteredStore) GetCallees(ctx context.Context, symbolID string) []string {
	return f.inner.GetCallees(ctx, symbolID)
}

func (f *TenantFilteredStore) GetTestsForSymbolPersisted(ctx context.Context, symbolID string) []string {
	return f.inner.GetTestsForSymbolPersisted(ctx, symbolID)
}

func (f *TenantFilteredStore) GetCallEdges(ctx context.Context, repoID string) []CallEdge {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetCallEdges(ctx, repoID)
}

func (f *TenantFilteredStore) Stats(ctx context.Context) map[string]int {
	return f.inner.Stats(ctx)
}

func (f *TenantFilteredStore) GetRequirement(ctx context.Context, id string) *StoredRequirement {
	return f.inner.GetRequirement(ctx, id)
}

func (f *TenantFilteredStore) GetRequirementsByIDs(ctx context.Context, ids []string) map[string]*StoredRequirement {
	return f.inner.GetRequirementsByIDs(ctx, ids)
}

func (f *TenantFilteredStore) UpdateRequirementFields(ctx context.Context, id string, fields RequirementUpdate) *StoredRequirement {
	return f.inner.UpdateRequirementFields(ctx, id, fields)
}

func (f *TenantFilteredStore) GetLink(ctx context.Context, id string) *StoredLink {
	return f.inner.GetLink(ctx, id)
}

func (f *TenantFilteredStore) GetLinksForRequirement(ctx context.Context, reqID string, includeRejected bool) []*StoredLink {
	return f.inner.GetLinksForRequirement(ctx, reqID, includeRejected)
}

func (f *TenantFilteredStore) GetLinksForSymbol(ctx context.Context, symID string, includeRejected bool) []*StoredLink {
	return f.inner.GetLinksForSymbol(ctx, symID, includeRejected)
}

func (f *TenantFilteredStore) GetLinksForFile(ctx context.Context, fileID string, startLine, endLine int, minConfidence float64) []*StoredLink {
	return f.inner.GetLinksForFile(ctx, fileID, startLine, endLine, minConfidence)
}

func (f *TenantFilteredStore) StoreEmbedding(ctx context.Context, record *EmbeddingRecord) {
	f.inner.StoreEmbedding(ctx, record)
}

func (f *TenantFilteredStore) GetEmbedding(ctx context.Context, targetID string) *EmbeddingRecord {
	return f.inner.GetEmbedding(ctx, targetID)
}

func (f *TenantFilteredStore) StoreReviewResult(ctx context.Context, record *ReviewResultRecord) {
	f.inner.StoreReviewResult(ctx, record)
}

func (f *TenantFilteredStore) GetReviewResults(ctx context.Context, targetID string) []*ReviewResultRecord {
	return f.inner.GetReviewResults(ctx, targetID)
}

func (f *TenantFilteredStore) GetReviewResultsForRepo(ctx context.Context, repoID string) []*ReviewResultRecord {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetReviewResultsForRepo(ctx, repoID)
}

func (f *TenantFilteredStore) GetPublicSymbolDocCoverage(ctx context.Context, repoID string) (withDocs int, total int) {
	if !f.hasAccess(repoID) {
		return 0, 0
	}
	return f.inner.GetPublicSymbolDocCoverage(ctx, repoID)
}

func (f *TenantFilteredStore) GetTestSymbolRatio(ctx context.Context, repoID string) (tests int, total int) {
	if !f.hasAccess(repoID) {
		return 0, 0
	}
	return f.inner.GetTestSymbolRatio(ctx, repoID)
}

func (f *TenantFilteredStore) GetAICodeFileRatio(ctx context.Context, repoID string) (aiFiles int, totalFiles int) {
	if !f.hasAccess(repoID) {
		return 0, 0
	}
	return f.inner.GetAICodeFileRatio(ctx, repoID)
}

func (f *TenantFilteredStore) StoreImpactReport(ctx context.Context, repoID string, report *ImpactReport) {
	if !f.hasAccess(repoID) {
		return
	}
	f.inner.StoreImpactReport(ctx, repoID, report)
}

func (f *TenantFilteredStore) GetLatestImpactReport(ctx context.Context, repoID string) *ImpactReport {
	if !f.hasAccess(repoID) {
		return nil
	}
	return f.inner.GetLatestImpactReport(ctx, repoID)
}

func (f *TenantFilteredStore) GetImpactReports(ctx context.Context, repoID string, limit int) ([]*ImpactReport, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetImpactReports(ctx, repoID, limit)
}

// --- Discovered Requirement operations ---

func (f *TenantFilteredStore) StoreDiscoveredRequirement(ctx context.Context, repoID string, req *DiscoveredRequirement) {
	if !f.hasAccess(repoID) {
		return
	}
	f.inner.StoreDiscoveredRequirement(ctx, repoID, req)
}

func (f *TenantFilteredStore) StoreDiscoveredRequirements(ctx context.Context, repoID string, reqs []*DiscoveredRequirement) int {
	if !f.hasAccess(repoID) {
		return 0
	}
	return f.inner.StoreDiscoveredRequirements(ctx, repoID, reqs)
}

func (f *TenantFilteredStore) GetDiscoveredRequirements(ctx context.Context, repoID string, status *string, confidence *string, limit, offset int) ([]*DiscoveredRequirement, int) {
	if !f.hasAccess(repoID) {
		return nil, 0
	}
	return f.inner.GetDiscoveredRequirements(ctx, repoID, status, confidence, limit, offset)
}

func (f *TenantFilteredStore) GetDiscoveredRequirement(ctx context.Context, id string) *DiscoveredRequirement {
	return f.inner.GetDiscoveredRequirement(ctx, id)
}

func (f *TenantFilteredStore) PromoteDiscoveredRequirement(ctx context.Context, id string, requirementID string) *DiscoveredRequirement {
	return f.inner.PromoteDiscoveredRequirement(ctx, id, requirementID)
}

func (f *TenantFilteredStore) DismissDiscoveredRequirement(ctx context.Context, id string, dismissedBy string, reason string) *DiscoveredRequirement {
	return f.inner.DismissDiscoveredRequirement(ctx, id, dismissedBy, reason)
}

func (f *TenantFilteredStore) DeleteDiscoveredRequirementsByRepo(ctx context.Context, repoID string) int {
	if !f.hasAccess(repoID) {
		return 0
	}
	return f.inner.DeleteDiscoveredRequirementsByRepo(ctx, repoID)
}

// --- Cross-Repo Federation ---

func (f *TenantFilteredStore) LinkRepos(ctx context.Context, sourceRepoID, targetRepoID string) (*RepoLink, error) {
	if !f.hasAccess(sourceRepoID) || !f.hasAccess(targetRepoID) {
		return nil, fmt.Errorf("access denied")
	}
	return f.inner.LinkRepos(ctx, sourceRepoID, targetRepoID)
}

// UnlinkRepos gates on the repo IDs of the link before delegating.
// The same opaque "not found" error is returned for "doesn't exist" and
// "belongs to another tenant" to prevent cross-tenant enumeration.
func (f *TenantFilteredStore) UnlinkRepos(ctx context.Context, linkID string) error {
	link := f.inner.GetRepoLink(ctx, linkID)
	if link == nil {
		return fmt.Errorf("repo link not found")
	}
	if !f.hasAccess(link.SourceRepoID) || !f.hasAccess(link.TargetRepoID) {
		return fmt.Errorf("repo link not found")
	}
	return f.inner.UnlinkRepos(ctx, linkID)
}

func (f *TenantFilteredStore) GetRepoLinks(ctx context.Context, repoID string) ([]*RepoLink, error) {
	if !f.hasAccess(repoID) {
		return nil, nil
	}
	return f.inner.GetRepoLinks(ctx, repoID)
}

// GetRepoLink is a pass-through used internally by UnlinkRepos and VerifyLink
// as a helper to load a link for access validation. The hasAccess check is NOT
// applied here — the caller is responsible for gating. Applying a second check
// at this level would imply incorrect semantics (double-check that could block
// internal helpers on the allowed path).
func (f *TenantFilteredStore) GetRepoLink(ctx context.Context, linkID string) *RepoLink {
	return f.inner.GetRepoLink(ctx, linkID)
}

// StoreCrossRepoRef gates on both source and target repo IDs.
func (f *TenantFilteredStore) StoreCrossRepoRef(ctx context.Context, ref *CrossRepoRef) error {
	if !f.hasAccess(ref.SourceRepoID) || !f.hasAccess(ref.TargetRepoID) {
		return fmt.Errorf("access denied")
	}
	return f.inner.StoreCrossRepoRef(ctx, ref)
}

// StoreCrossRepoRefs filters the slice to refs where both source and target
// repos are accessible to the tenant. Cross-tenant refs are dropped silently;
// the returned count reflects only persisted refs (the accessible subset).
func (f *TenantFilteredStore) StoreCrossRepoRefs(ctx context.Context, refs []*CrossRepoRef) int {
	allowed := refs[:0:len(refs)] // re-use backing array without allocation
	for _, ref := range refs {
		if f.hasAccess(ref.SourceRepoID) && f.hasAccess(ref.TargetRepoID) {
			allowed = append(allowed, ref)
		}
	}
	if len(allowed) == 0 {
		return 0
	}
	return f.inner.StoreCrossRepoRefs(ctx, allowed)
}

func (f *TenantFilteredStore) GetCrossRepoRefs(ctx context.Context, repoID string, refType *string, limit int) ([]*CrossRepoRef, error) {
	if !f.hasAccess(repoID) {
		return nil, nil
	}
	return f.inner.GetCrossRepoRefs(ctx, repoID, refType, limit)
}

// GetSymbolCrossRepoRefs gates on the symbol's home repo AND filters result
// rows to those where both source and target repos are accessible.
// Decision 5: gate at symbol lookup prevents cross-tenant symbol enumeration;
// result-row filtering prevents leaking refs to inaccessible target repos.
func (f *TenantFilteredStore) GetSymbolCrossRepoRefs(ctx context.Context, symbolID string) ([]*CrossRepoRef, error) {
	sym := f.inner.GetSymbol(ctx, symbolID)
	if sym == nil || !f.hasAccess(sym.RepoID) {
		return nil, nil
	}
	refs, err := f.inner.GetSymbolCrossRepoRefs(ctx, symbolID)
	if err != nil || len(refs) == 0 {
		return refs, err
	}
	filtered := refs[:0:len(refs)]
	for _, ref := range refs {
		if f.hasAccess(ref.SourceRepoID) && f.hasAccess(ref.TargetRepoID) {
			filtered = append(filtered, ref)
		}
	}
	return filtered, nil
}

func (f *TenantFilteredStore) DeleteCrossRepoRefsForRepo(ctx context.Context, repoID string) error {
	if !f.hasAccess(repoID) {
		return fmt.Errorf("access denied")
	}
	return f.inner.DeleteCrossRepoRefsForRepo(ctx, repoID)
}

// DeleteCrossRepoRefsBetweenRepos gates on both repo IDs.
func (f *TenantFilteredStore) DeleteCrossRepoRefsBetweenRepos(ctx context.Context, repoA, repoB string) error {
	if !f.hasAccess(repoA) || !f.hasAccess(repoB) {
		return fmt.Errorf("access denied")
	}
	return f.inner.DeleteCrossRepoRefsBetweenRepos(ctx, repoA, repoB)
}

// StoreAPIContract gates on the contract's repo ID.
// Closes the asymmetry where DeleteAPIContractsForRepo was gated but
// StoreAPIContract was not (xander r1 critical finding).
func (f *TenantFilteredStore) StoreAPIContract(ctx context.Context, contract *APIContract) error {
	if !f.hasAccess(contract.RepoID) {
		return fmt.Errorf("access denied")
	}
	return f.inner.StoreAPIContract(ctx, contract)
}

func (f *TenantFilteredStore) GetAPIContracts(ctx context.Context, repoID string) ([]*APIContract, error) {
	if !f.hasAccess(repoID) {
		return nil, nil
	}
	return f.inner.GetAPIContracts(ctx, repoID)
}

func (f *TenantFilteredStore) DeleteAPIContractsForRepo(ctx context.Context, repoID string) error {
	if !f.hasAccess(repoID) {
		return fmt.Errorf("access denied")
	}
	return f.inner.DeleteAPIContractsForRepo(ctx, repoID)
}

// VerifyLink gates on the link's repo ID before delegating.
// Uses GetLink (already on the interface) since StoredLink.RepoID is available.
// Returns nil for "not found" and "belongs to another tenant" to prevent
// cross-tenant link enumeration. A nil return at the call site (schema.resolvers.go)
// triggers the Phase 4 publish-site nil-check that suppresses EventLinkVerified.
func (f *TenantFilteredStore) VerifyLink(ctx context.Context, linkID string, verified bool, verifiedBy string) *StoredLink {
	link := f.inner.GetLink(ctx, linkID)
	if link == nil || !f.hasAccess(link.RepoID) {
		return nil
	}
	return f.inner.VerifyLink(ctx, linkID, verified, verifiedBy)
}

// Verify at compile time that *TenantFilteredStore satisfies GraphStore.
var _ GraphStore = (*TenantFilteredStore)(nil)
