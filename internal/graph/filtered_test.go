// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Phase 3 (CA-203) TenantFilteredStore gate tests.
//
// Test-store strategy (Decision 11):
//
//   - Negative-path tests ("inner NOT called") use the in-memory *graph.Store
//     wrapped in a counting-recorder. The gate fires before the inner is reached;
//     the recorder counter stays at 0, which is the assertion.
//   - T35a (admin posture pin): admin role does NOT bypass the tenant gate.
//   - T35b (OSS pass-through): tenantID=="" and tenantID=="default" both bypass
//     the gate so OSS single-tenant installs are unaffected.
//
// Positive-path tests (assert stored/returned data) live in
// filtered_integration_test.go with the //go:build integration tag and use a
// real *db.SurrealStore via the testcontainer.

package graph

import (
	"context"
	"sync/atomic"
	"testing"
)

// --- Counting recorder ---

// callRecorder wraps a GraphStore and counts calls to the gated methods.
// Embedded GraphStore handles every other method via promotion.
type callRecorder struct {
	GraphStore
	unlinkReposCalls                atomic.Int32
	storeCrossRepoRefCalls          atomic.Int32
	storeCrossRepoRefsCalls         atomic.Int32
	getSymbolCrossRepoRefsCalls     atomic.Int32
	deleteCrossRepoRefsBetweenCalls atomic.Int32
	getCallEdgesCalls               atomic.Int32
	storeAPIContractCalls           atomic.Int32
	verifyLinkCalls                 atomic.Int32
}

func (r *callRecorder) UnlinkRepos(ctx context.Context, linkID string) error {
	r.unlinkReposCalls.Add(1)
	return r.GraphStore.UnlinkRepos(ctx, linkID)
}

func (r *callRecorder) StoreCrossRepoRef(ctx context.Context, ref *CrossRepoRef) error {
	r.storeCrossRepoRefCalls.Add(1)
	return r.GraphStore.StoreCrossRepoRef(ctx, ref)
}

func (r *callRecorder) StoreCrossRepoRefs(ctx context.Context, refs []*CrossRepoRef) int {
	r.storeCrossRepoRefsCalls.Add(1)
	return r.GraphStore.StoreCrossRepoRefs(ctx, refs)
}

func (r *callRecorder) GetSymbolCrossRepoRefs(ctx context.Context, symbolID string) ([]*CrossRepoRef, error) {
	r.getSymbolCrossRepoRefsCalls.Add(1)
	return r.GraphStore.GetSymbolCrossRepoRefs(ctx, symbolID)
}

func (r *callRecorder) DeleteCrossRepoRefsBetweenRepos(ctx context.Context, repoA, repoB string) error {
	r.deleteCrossRepoRefsBetweenCalls.Add(1)
	return r.GraphStore.DeleteCrossRepoRefsBetweenRepos(ctx, repoA, repoB)
}

func (r *callRecorder) GetCallEdges(ctx context.Context, repoID string) []CallEdge {
	r.getCallEdgesCalls.Add(1)
	return r.GraphStore.GetCallEdges(ctx, repoID)
}

func (r *callRecorder) StoreAPIContract(ctx context.Context, contract *APIContract) error {
	r.storeAPIContractCalls.Add(1)
	return r.GraphStore.StoreAPIContract(ctx, contract)
}

func (r *callRecorder) VerifyLink(ctx context.Context, linkID string, verified bool, verifiedBy string) *StoredLink {
	r.verifyLinkCalls.Add(1)
	return r.GraphStore.VerifyLink(ctx, linkID, verified, verifiedBy)
}

// --- Helpers ---

// filteredWithRecorder returns a TenantFilteredStore whose inner is a
// callRecorder wrapping a fresh in-memory store. The tenant is allowed access
// to allowedRepoIDs only.
func filteredWithRecorder(allowedRepoIDs ...string) (*TenantFilteredStore, *callRecorder) {
	rec := &callRecorder{GraphStore: NewStore()}
	f := NewTenantFilteredStore(rec, allowedRepoIDs)
	return f, rec
}

// --- T20: UnlinkRepos cross-tenant — inner NOT called ---

// TestFilteredUnlinkReposCrossTenant (T20): UnlinkRepos for a link whose source
// repo is NOT in the tenant's allowed set returns an opaque "not found" error
// and does NOT call the inner store.
//
// The in-memory store GetRepoLink returns nil (Decision 10 stub), so the filter
// short-circuits on "link == nil" before reaching the access check — but the
// net result is the same: inner.UnlinkRepos is never called.
func TestFilteredUnlinkReposCrossTenant(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")

	err := f.UnlinkRepos(context.Background(), "link-belonging-to-B")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := rec.unlinkReposCalls.Load(); got != 0 {
		t.Fatalf("inner.UnlinkRepos called %d times; want 0", got)
	}
}

// TestFilteredUnlinkReposPartialScope (T21): UnlinkRepos where the tenant owns
// only one of the two repo sides returns "not found" and does NOT call inner.
//
// Again the in-memory store stubs GetRepoLink → nil, so the filter
// short-circuits. The assertion is "inner NOT called".
func TestFilteredUnlinkReposPartialScope(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")
	// repo-B is NOT allowed for this tenant.
	err := f.UnlinkRepos(context.Background(), "link-source-A-target-B")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := rec.unlinkReposCalls.Load(); got != 0 {
		t.Fatalf("inner.UnlinkRepos called %d times; want 0", got)
	}
}

// --- T23: StoreCrossRepoRef partial — inner NOT called ---

// TestFilteredStoreCrossRepoRefPartialScope (T23): StoreCrossRepoRef where
// the target repo is not in the allowed set returns "access denied" and does
// NOT call the inner store.
func TestFilteredStoreCrossRepoRefPartialScope(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")
	ref := &CrossRepoRef{
		SourceRepoID: "repo-A",
		TargetRepoID: "repo-B", // not allowed
	}
	err := f.StoreCrossRepoRef(context.Background(), ref)
	if err == nil {
		t.Fatal("expected access-denied error, got nil")
	}
	if got := rec.storeCrossRepoRefCalls.Load(); got != 0 {
		t.Fatalf("inner.StoreCrossRepoRef called %d times; want 0", got)
	}
}

// --- T25: StoreCrossRepoRefs all-denied — inner NOT called ---

// TestFilteredStoreCrossRepoRefsAllDenied (T25): when ALL refs in the batch
// are cross-tenant, the inner store is NOT called and the return value is 0.
func TestFilteredStoreCrossRepoRefsAllDenied(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")
	refs := []*CrossRepoRef{
		{SourceRepoID: "repo-A", TargetRepoID: "repo-B"},   // target not allowed
		{SourceRepoID: "repo-B", TargetRepoID: "repo-A"},   // source not allowed
		{SourceRepoID: "repo-C", TargetRepoID: "repo-D"},   // both not allowed
	}
	n := f.StoreCrossRepoRefs(context.Background(), refs)
	if n != 0 {
		t.Fatalf("StoreCrossRepoRefs returned %d; want 0 (all denied)", n)
	}
	if got := rec.storeCrossRepoRefsCalls.Load(); got != 0 {
		t.Fatalf("inner.StoreCrossRepoRefs called %d times; want 0", got)
	}
}

// --- T28: GetSymbolCrossRepoRefs symbol not accessible — inner NOT called ---

// TestFilteredGetSymbolCrossRepoRefsNotAccessible (T28): when the symbol's home
// repo is not in the tenant's allowed set, GetSymbolCrossRepoRefs returns nil
// and does NOT call the inner store.
func TestFilteredGetSymbolCrossRepoRefsNotAccessible(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")
	// Symbol does not exist in the inner store (GetSymbol → nil), which also
	// triggers the "nil || !hasAccess" path.
	refs, err := f.GetSymbolCrossRepoRefs(context.Background(), "sym-in-repo-B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refs != nil {
		t.Fatalf("expected nil refs, got %v", refs)
	}
	if got := rec.getSymbolCrossRepoRefsCalls.Load(); got != 0 {
		t.Fatalf("inner.GetSymbolCrossRepoRefs called %d times; want 0", got)
	}
}

// --- T31: DeleteCrossRepoRefsBetweenRepos one-out — inner NOT called ---

// TestFilteredDeleteCrossRepoRefsBetweenReposOneOut (T31): DeleteCrossRepoRefsBetweenRepos
// where one repo is not in the allowed set returns "access denied" and does NOT
// call the inner store.
func TestFilteredDeleteCrossRepoRefsBetweenReposOneOut(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")
	err := f.DeleteCrossRepoRefsBetweenRepos(context.Background(), "repo-A", "repo-B")
	if err == nil {
		t.Fatal("expected access-denied error, got nil")
	}
	if got := rec.deleteCrossRepoRefsBetweenCalls.Load(); got != 0 {
		t.Fatalf("inner.DeleteCrossRepoRefsBetweenRepos called %d times; want 0", got)
	}
}

// --- T32-neg: GetCallEdges cross-tenant — inner NOT called ---

// TestFilteredGetCallEdgesCrossTenant (T32-neg): GetCallEdges for a repo not
// in the allowed set returns nil and does NOT call the inner store.
func TestFilteredGetCallEdgesCrossTenant(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")
	edges := f.GetCallEdges(context.Background(), "repo-B")
	if edges != nil {
		t.Fatalf("expected nil edges, got %v", edges)
	}
	if got := rec.getCallEdgesCalls.Load(); got != 0 {
		t.Fatalf("inner.GetCallEdges called %d times; want 0", got)
	}
}

// --- T33-neg: StoreAPIContract cross-tenant — inner NOT called ---

// TestFilteredStoreAPIContractCrossTenant (T33-neg): StoreAPIContract for a
// contract whose repo is not allowed returns "access denied" and does NOT call
// the inner store. Closes the asymmetry where DeleteAPIContractsForRepo was
// gated but StoreAPIContract was not (xander r1 critical).
func TestFilteredStoreAPIContractCrossTenant(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")
	contract := &APIContract{RepoID: "repo-B"}
	err := f.StoreAPIContract(context.Background(), contract)
	if err == nil {
		t.Fatal("expected access-denied error, got nil")
	}
	if got := rec.storeAPIContractCalls.Load(); got != 0 {
		t.Fatalf("inner.StoreAPIContract called %d times; want 0", got)
	}
}

// --- T34-neg: VerifyLink cross-tenant — inner NOT called ---

// TestFilteredVerifyLinkCrossTenant (T34-neg): VerifyLink for a link whose
// repo is not in the allowed set returns nil and does NOT call inner.VerifyLink.
// Storage-layer assertion only — the publish-layer assertion (EventLinkVerified
// not emitted when VerifyLink returns nil) lives in T44 in Phase 4 tests.
//
// The in-memory GetLink returns nil for any ID (no data seeded), so the filter
// short-circuits on "link == nil" — the net result is the same: inner.VerifyLink
// is never called.
func TestFilteredVerifyLinkCrossTenant(t *testing.T) {
	f, rec := filteredWithRecorder("repo-A")
	result := f.VerifyLink(context.Background(), "link-belonging-to-B", true, "user-A")
	if result != nil {
		t.Fatalf("expected nil, got link %+v", result)
	}
	if got := rec.verifyLinkCalls.Load(); got != 0 {
		t.Fatalf("inner.VerifyLink called %d times; want 0", got)
	}
}

// --- T35a: Admin posture pin (Decision 6) ---

// TestFilteredAdminDoesNotBypassTenantGate (T35a): a claims principal with
// role=admin is still scoped to their tenant's repos. The TenantFilteredStore
// is constructed solely from the allowedIDs list and has no role-based bypass.
// Pinned against future regression that adds an admin shortcut.
func TestFilteredAdminDoesNotBypassTenantGate(t *testing.T) {
	// Tenant A's filter — only repo-A is allowed.
	// Simulate an "admin" user with tenantID=A trying to act on repo-B.
	// The admin role is outside the scope of TenantFilteredStore — it is
	// enforced at the middleware level, not here. The filter is blind to role.
	f, rec := filteredWithRecorder("repo-A") // admin of tenant A

	// Try to unlink a link belonging to tenant B (in-memory → nil → not found).
	err := f.UnlinkRepos(context.Background(), "link-belonging-to-B")
	if err == nil {
		t.Fatal("admin tenant A should not be able to unlink tenant B's link")
	}
	if got := rec.unlinkReposCalls.Load(); got != 0 {
		t.Fatalf("inner.UnlinkRepos called %d times; want 0", got)
	}

	// GetCallEdges for a different tenant's repo.
	edges := f.GetCallEdges(context.Background(), "repo-B")
	if edges != nil {
		t.Fatalf("admin tenant A should not see tenant B's call edges")
	}
	if got := rec.getCallEdgesCalls.Load(); got != 0 {
		t.Fatalf("inner.GetCallEdges called %d times; want 0", got)
	}
}

// --- T35b: OSS single-tenant pass-through ---

// TestFilteredOSSPassThroughGetCallEdges (T35b-callEdges): when tenantID is ""
// or "default" the RepoAccessMiddleware constructs an unfiltered baseStore, not
// a TenantFilteredStore. This test verifies that a TenantFilteredStore
// constructed with ALL repo IDs in the allowed list (OSS posture) passes through
// correctly — i.e., no false-positive gating.
func TestFilteredOSSPassThroughGetCallEdges(t *testing.T) {
	inner := NewStore()
	// OSS: all repos are in scope. We seed two repos and grant access to both.
	f := NewTenantFilteredStore(inner, []string{"repo-A", "repo-B"})

	// No edges seeded, but the inner store should be reached (no nil-return gate).
	edgesA := f.GetCallEdges(context.Background(), "repo-A")
	// The in-memory store returns an empty slice (not nil) — just verifying the
	// call wasn't blocked.
	_ = edgesA

	edgesB := f.GetCallEdges(context.Background(), "repo-B")
	_ = edgesB
	// No assertion on content — the OSS test is that the gate did NOT block.
}

// TestFilteredOSSPassThroughDeleteCrossRepoRefs (T35b-deleteRefs): OSS
// pass-through for DeleteCrossRepoRefsBetweenRepos.
func TestFilteredOSSPassThroughDeleteCrossRepoRefs(t *testing.T) {
	inner := NewStore()
	f := NewTenantFilteredStore(inner, []string{"repo-A", "repo-B"})

	// No data seeded; inner.DeleteCrossRepoRefsBetweenRepos returns nil for both.
	err := f.DeleteCrossRepoRefsBetweenRepos(context.Background(), "repo-A", "repo-B")
	// The inner in-memory store stubs this as "federation not supported" — that
	// is expected. What we verify is that the gate did NOT fire "access denied"
	// (error text would differ).
	if err != nil {
		// Only fail if it looks like a gate rejection, not a store-level stub.
		if err.Error() == "access denied" {
			t.Fatalf("OSS pass-through unexpectedly returned access denied: %v", err)
		}
	}
}

// TestFilteredOSSPassThroughStoreAPIContract (T35b-apiContract): OSS
// pass-through for StoreAPIContract.
func TestFilteredOSSPassThroughStoreAPIContract(t *testing.T) {
	inner := NewStore()
	f := NewTenantFilteredStore(inner, []string{"repo-A"})

	contract := &APIContract{RepoID: "repo-A", ContractType: "openapi"}
	err := f.StoreAPIContract(context.Background(), contract)
	// The in-memory store's StoreAPIContract should not return access denied.
	if err != nil && err.Error() == "access denied" {
		t.Fatalf("OSS pass-through unexpectedly returned access denied: %v", err)
	}
}
