// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// TestPlatformStats_NonAdminAuthenticatedUserReturnsData verifies that a
// non-admin authenticated user can call PlatformStats without receiving an
// "admin role required" error (regression gate for the Phase 2 reconcile gate
// removed in codex r2 reconcile).
//
// The security invariant is preserved at the TenantFilteredStore layer:
// TenantFilteredStore.Stats() returns an empty map, so non-admin tenants get
// zeros rather than cross-tenant aggregates. No additional resolver-level admin
// gate is required or correct — the web dashboard calls this query for ALL
// authenticated users.
func TestPlatformStats_NonAdminAuthenticatedUserReturnsData(t *testing.T) {
	// Seed a real repo so Stats() on a plain GraphStore returns non-zero counts.
	inner := graphstore.NewStore()
	result := &indexer.IndexResult{RepoName: "repo", RepoPath: "/tmp"}
	repo, err := inner.StoreIndexResult(t.Context(), result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}
	if repo == nil {
		t.Fatal("expected non-nil repo")
	}

	// OSS path: plain GraphStore, non-admin context → real counts (non-zero Stats).
	r := &Resolver{
		Deps:  &appdeps.AppDeps{},
		Store: inner,
	}
	nonAdminCtx := context.WithValue(context.Background(), auth.ClaimsKey, &auth.Claims{
		UserID: "user-1",
		Role:   "member",
	})
	stats, err := r.Query().PlatformStats(nonAdminCtx)
	if err != nil {
		t.Fatalf("PlatformStats returned error for non-admin user: %v (regression: admin gate must be absent)", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil PlatformStats result")
	}
	// Repositories count should be 1 (seeded above); confirms the resolver
	// reached Stats() on the plain store rather than being blocked by a gate.
	if stats.Repositories == 0 {
		t.Errorf("expected Repositories > 0 for OSS path (got 0); Stats() may not be reaching the inner store")
	}

	// Multi-tenant path: TenantFilteredStore wired via r.Store, non-admin context
	// → zeros (empty map from TenantFilteredStore.Stats()), no error.
	tenantStore := graphstore.NewTenantFilteredStore(inner, []string{})
	rTenant := &Resolver{
		Deps:  &appdeps.AppDeps{},
		Store: tenantStore,
	}
	statsTenant, err := rTenant.Query().PlatformStats(nonAdminCtx)
	if err != nil {
		t.Fatalf("PlatformStats (TenantFilteredStore) returned error for non-admin user: %v", err)
	}
	if statsTenant == nil {
		t.Fatal("expected non-nil PlatformStats result (tenant path)")
	}
	// TenantFilteredStore.Stats() always returns empty map → all counts zero.
	if statsTenant.Repositories != 0 || statsTenant.Files != 0 || statsTenant.Symbols != 0 {
		t.Errorf("expected all-zero stats from TenantFilteredStore, got repos=%d files=%d symbols=%d",
			statsTenant.Repositories, statsTenant.Files, statsTenant.Symbols)
	}
}
