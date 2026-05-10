// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

// Phase 3 (CA-203) TenantFilteredStore positive-path integration tests.
//
// These tests assert "stored / returned data" against a real *db.SurrealStore
// via the testcontainer (image surrealdb/surrealdb:v2.6.5). They use the
// external package graph_test to avoid the circular-import cycle between
// internal/graph and internal/db.
//
// Test-store strategy: Decision 11 in the P8 plan. Positive-path tests for
// federation methods live here because the in-memory *graph.Store stubs all
// federation methods as "federation not supported" (Decision 10); only the
// SurrealStore implements real semantics. The negative-path tests
// ("inner NOT called") live in filtered_test.go and use the in-memory store
// wrapped in a counting recorder.

package graph_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startSurrealStore launches a SurrealDB container and returns a connected,
// migrated *db.SurrealStore wrapping it. Torn down via t.Cleanup.
func startSurrealStore(t *testing.T) *db.SurrealStore {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        "surrealdb/surrealdb:v2.6.5",
		ExposedPorts: []string{"8000/tcp"},
		Cmd: []string{
			"start",
			"--user", "root",
			"--pass", "root",
			"memory",
		},
		WaitingFor: wait.ForAll(
			wait.ForLog("Started web server").WithStartupTimeout(30*time.Second),
			wait.ForListeningPort("8000/tcp").WithStartupTimeout(30*time.Second),
		),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start surreal container: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "8000")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}

	cfg := config.StorageConfig{
		SurrealMode:      "external",
		SurrealURL:       fmt.Sprintf("ws://%s:%s/rpc", host, port.Port()),
		SurrealUser:      "root",
		SurrealPass:      "root",
		SurrealNamespace: "test_ns",
		SurrealDatabase:  "test_db",
	}
	s := db.NewSurrealDB(cfg)

	connectCtx, connectCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer connectCancel()
	if err := s.Connect(connectCtx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Resolve migrations dir relative to this source file.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "db", "migrations")
	if _, statErr := os.Stat(migrationsDir); statErr != nil {
		t.Fatalf("migrations dir %s: %v", migrationsDir, statErr)
	}

	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer migrateCancel()
	if err := s.Migrate(migrateCtx, migrationsDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return db.NewSurrealStore(s)
}

// seedRepo seeds a minimal repository record and returns its ID.
func seedRepo(t *testing.T, store graph.GraphStore, name string) string {
	t.Helper()
	result := &indexer.IndexResult{
		RepoName:   name,
		RepoPath:   "/tmp/" + name,
		TotalFiles: 1,
		Files: []indexer.FileResult{
			{Path: "main.go", Language: "go", LineCount: 5},
		},
	}
	repo, err := store.StoreIndexResult(t.Context(), result)
	if err != nil {
		t.Fatalf("StoreIndexResult(%s): %v", name, err)
	}
	return repo.ID
}

// filteredFor wraps inner in a TenantFilteredStore that allows access to the
// given repo IDs only.
func filteredFor(inner graph.GraphStore, repoIDs ...string) *graph.TenantFilteredStore {
	return graph.NewTenantFilteredStore(inner, repoIDs)
}

// --- T19: UnlinkRepos legitimate ---

// TestFilteredIntegrationUnlinkReposLegitimate (T19): UnlinkRepos for a link
// whose source and target repos are both in the tenant's allowed set succeeds
// and the row is deleted.
func TestFilteredIntegrationUnlinkReposLegitimate(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T19-repo-A")
	repoB := seedRepo(t, store, "T19-repo-B")

	// Create a link using the unfiltered store.
	link, err := store.LinkRepos(t.Context(), repoA, repoB)
	if err != nil {
		t.Fatalf("LinkRepos: %v", err)
	}

	// The tenant owns both repos.
	f := filteredFor(store, repoA, repoB)
	if err := f.UnlinkRepos(t.Context(), link.ID); err != nil {
		t.Fatalf("UnlinkRepos via filtered store: %v", err)
	}

	// Verify the link is gone.
	links, err := store.GetRepoLinks(t.Context(), repoA)
	if err != nil {
		t.Fatalf("GetRepoLinks: %v", err)
	}
	for _, l := range links {
		if l.ID == link.ID {
			t.Fatalf("link %s still present after UnlinkRepos", link.ID)
		}
	}
}

// --- T22: StoreCrossRepoRef legitimate ---

// TestFilteredIntegrationStoreCrossRepoRefLegitimate (T22): StoreCrossRepoRef
// for a ref where both source and target repos are in the allowed set stores
// the row.
func TestFilteredIntegrationStoreCrossRepoRefLegitimate(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T22-repo-A")
	repoB := seedRepo(t, store, "T22-repo-B")

	f := filteredFor(store, repoA, repoB)
	ref := &graph.CrossRepoRef{
		SourceRepoID: repoA,
		TargetRepoID: repoB,
		RefType:      "import",
		Confidence:   0.9,
	}
	if err := f.StoreCrossRepoRef(t.Context(), ref); err != nil {
		t.Fatalf("StoreCrossRepoRef: %v", err)
	}

	// Verify the row is present via unfiltered store.
	refs, err := store.GetCrossRepoRefs(t.Context(), repoA, nil, 10)
	if err != nil {
		t.Fatalf("GetCrossRepoRefs: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected stored cross-repo ref, got empty result")
	}
}

// --- T24: StoreCrossRepoRefs all-allowed batch ---

// TestFilteredIntegrationStoreCrossRepoRefsAllAllowed (T24): StoreCrossRepoRefs
// with all refs in the allowed set persists the full slice and returns N.
func TestFilteredIntegrationStoreCrossRepoRefsAllAllowed(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T24-repo-A")
	repoB := seedRepo(t, store, "T24-repo-B")

	f := filteredFor(store, repoA, repoB)
	refs := []*graph.CrossRepoRef{
		{SourceRepoID: repoA, TargetRepoID: repoB, RefType: "import", Confidence: 0.8},
		{SourceRepoID: repoA, TargetRepoID: repoB, RefType: "call", Confidence: 0.7},
		{SourceRepoID: repoB, TargetRepoID: repoA, RefType: "import", Confidence: 0.6},
	}
	n := f.StoreCrossRepoRefs(t.Context(), refs)
	if n != 3 {
		t.Fatalf("StoreCrossRepoRefs returned %d; want 3", n)
	}
}

// --- T26: StoreCrossRepoRefs mixed batch ---

// TestFilteredIntegrationStoreCrossRepoRefsMixed (T26): StoreCrossRepoRefs
// with 3 allowed refs and 2 cross-tenant refs persists only the 3 allowed
// and returns 3.
func TestFilteredIntegrationStoreCrossRepoRefsMixed(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T26-repo-A")
	repoB := seedRepo(t, store, "T26-repo-B")

	f := filteredFor(store, repoA, repoB)
	refs := []*graph.CrossRepoRef{
		// 3 allowed
		{SourceRepoID: repoA, TargetRepoID: repoB, RefType: "import", Confidence: 0.9},
		{SourceRepoID: repoA, TargetRepoID: repoB, RefType: "call", Confidence: 0.8},
		{SourceRepoID: repoB, TargetRepoID: repoA, RefType: "import", Confidence: 0.7},
		// 2 cross-tenant (repoC not in allowed set)
		{SourceRepoID: repoA, TargetRepoID: "repo-C-not-allowed", RefType: "import", Confidence: 0.5},
		{SourceRepoID: "repo-C-not-allowed", TargetRepoID: repoB, RefType: "call", Confidence: 0.4},
	}
	n := f.StoreCrossRepoRefs(t.Context(), refs)
	if n != 3 {
		t.Fatalf("StoreCrossRepoRefs returned %d; want 3 (2 cross-tenant filtered out)", n)
	}

	// The cross-tenant refs must not be in the store.
	allRefs, err := store.GetCrossRepoRefs(t.Context(), repoA, nil, 20)
	if err != nil {
		t.Fatalf("GetCrossRepoRefs: %v", err)
	}
	for _, r := range allRefs {
		if r.TargetRepoID == "repo-C-not-allowed" || r.SourceRepoID == "repo-C-not-allowed" {
			t.Fatalf("cross-tenant ref leaked into store: %+v", r)
		}
	}
}

// --- T27: GetSymbolCrossRepoRefs legitimate ---

// TestFilteredIntegrationGetSymbolCrossRepoRefsLegitimate (T27): when the
// symbol's home repo is in the allowed set, GetSymbolCrossRepoRefs returns the
// full ref set.
func TestFilteredIntegrationGetSymbolCrossRepoRefsLegitimate(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T27-repo-A")
	repoB := seedRepo(t, store, "T27-repo-B")

	// Seed a symbol in repoA.
	symResult := &indexer.IndexResult{
		RepoName: "T27-repo-A",
		RepoPath: "/tmp/T27-repo-A",
		Files: []indexer.FileResult{
			{
				Path:     "a.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{ID: "sym-T27-A", Name: "FuncA", QualifiedName: "pkg.FuncA", Kind: indexer.SymbolFunction, Language: "go", FilePath: "a.go", StartLine: 1, EndLine: 5},
				},
			},
		},
	}
	// Re-store repoA with a symbol via ReplaceIndexResult (which takes an existing repo ID).
	_, err := store.ReplaceIndexResult(t.Context(), repoA, symResult)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	// Find the actual symbol ID as stored (the store assigns IDs from indexer ID).
	syms := store.GetSymbolsByFile(t.Context(), repoA, "a.go")
	if len(syms) == 0 {
		t.Fatal("no symbols found after ReplaceIndexResult")
	}
	symID := syms[0].ID

	// Store a cross-repo ref sourced from symA → repoB.
	ref := &graph.CrossRepoRef{
		SourceSymbolID: symID,
		SourceRepoID:   repoA,
		TargetRepoID:   repoB,
		RefType:        "import",
		Confidence:     0.9,
	}
	if err := store.StoreCrossRepoRef(t.Context(), ref); err != nil {
		t.Fatalf("StoreCrossRepoRef: %v", err)
	}

	f := filteredFor(store, repoA, repoB)
	refs, err := f.GetSymbolCrossRepoRefs(t.Context(), symID)
	if err != nil {
		t.Fatalf("GetSymbolCrossRepoRefs: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected at least one cross-repo ref for the allowed symbol")
	}
}

// --- T30: DeleteCrossRepoRefsBetweenRepos both-in-scope ---

// TestFilteredIntegrationDeleteCrossRepoRefsBetweenReposBothInScope (T30):
// when both repos are in the allowed set, DeleteCrossRepoRefsBetweenRepos
// succeeds.
func TestFilteredIntegrationDeleteCrossRepoRefsBetweenReposBothInScope(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T30-repo-A")
	repoB := seedRepo(t, store, "T30-repo-B")

	// Seed a ref between them.
	if err := store.StoreCrossRepoRef(t.Context(), &graph.CrossRepoRef{
		SourceRepoID: repoA,
		TargetRepoID: repoB,
		RefType:      "import",
	}); err != nil {
		t.Fatalf("StoreCrossRepoRef: %v", err)
	}

	f := filteredFor(store, repoA, repoB)
	if err := f.DeleteCrossRepoRefsBetweenRepos(t.Context(), repoA, repoB); err != nil {
		t.Fatalf("DeleteCrossRepoRefsBetweenRepos: %v", err)
	}

	refs, err := store.GetCrossRepoRefs(t.Context(), repoA, nil, 10)
	if err != nil {
		t.Fatalf("GetCrossRepoRefs: %v", err)
	}
	for _, r := range refs {
		if r.TargetRepoID == repoB || r.SourceRepoID == repoB {
			t.Fatalf("ref between repoA and repoB still present after delete")
		}
	}
}

// --- T32-pos: GetCallEdges legitimate ---

// TestFilteredIntegrationGetCallEdgesLegitimate (T32-pos): GetCallEdges for an
// allowed repo returns the edges.
func TestFilteredIntegrationGetCallEdgesLegitimate(t *testing.T) {
	store := startSurrealStore(t)

	// Seed a repo with call edges via StoreIndexResult with Relations.
	result := &indexer.IndexResult{
		RepoName: "T32-repo-edges",
		RepoPath: "/tmp/T32-repo-edges",
		Files: []indexer.FileResult{
			{
				Path:     "main.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{ID: "sym-caller", Name: "Caller", QualifiedName: "pkg.Caller", Kind: indexer.SymbolFunction, Language: "go", FilePath: "main.go", StartLine: 1, EndLine: 5},
					{ID: "sym-callee", Name: "Callee", QualifiedName: "pkg.Callee", Kind: indexer.SymbolFunction, Language: "go", FilePath: "main.go", StartLine: 7, EndLine: 10},
				},
			},
		},
		Relations: []indexer.Relation{
			{SourceID: "sym-caller", TargetID: "sym-callee", Type: indexer.RelationCalls},
		},
	}
	repo, err := store.StoreIndexResult(t.Context(), result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	f := filteredFor(store, repo.ID)
	edges := f.GetCallEdges(t.Context(), repo.ID)
	if len(edges) == 0 {
		t.Fatal("expected at least one call edge for allowed repo")
	}
}

// --- T33-pos: StoreAPIContract legitimate ---

// TestFilteredIntegrationStoreAPIContractLegitimate (T33-pos): StoreAPIContract
// for a contract whose repo is in the allowed set stores the row.
func TestFilteredIntegrationStoreAPIContractLegitimate(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T33-repo-A")

	f := filteredFor(store, repoA)
	contract := &graph.APIContract{
		RepoID:       repoA,
		FilePath:     "api.yaml",
		ContractType: "openapi",
		Endpoints:    "/v1/repos",
		Version:      "1.0.0",
		ContentHash:  "abc123",
	}
	if err := f.StoreAPIContract(t.Context(), contract); err != nil {
		t.Fatalf("StoreAPIContract: %v", err)
	}

	contracts, err := store.GetAPIContracts(t.Context(), repoA)
	if err != nil {
		t.Fatalf("GetAPIContracts: %v", err)
	}
	if len(contracts) == 0 {
		t.Fatal("expected stored API contract, got empty result")
	}
}

// --- T34-pos: VerifyLink legitimate ---

// TestFilteredIntegrationVerifyLinkLegitimate (T34-pos): VerifyLink for a link
// whose repo is in the allowed set returns the updated link with verified=true.
// Storage-layer assertion only — the publish-layer assertion (EventLinkVerified
// not emitted when VerifyLink returns nil for cross-tenant) lives in T44 in
// Phase 4 tests.
func TestFilteredIntegrationVerifyLinkLegitimate(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T34-repo-A")

	// Seed a requirement and a link for it.
	store.StoreRequirement(t.Context(), repoA, &graph.StoredRequirement{
		ID:         "req-T34",
		ExternalID: "T34-EXT-001",
		Title:      "T34 req",
	})

	// Seed a symbol.
	symResult := &indexer.IndexResult{
		RepoName: "T34-repo-A",
		RepoPath: "/tmp/T34-repo-A",
		Files: []indexer.FileResult{
			{
				Path:     "a.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{ID: "sym-T34-A", Name: "FuncA", QualifiedName: "pkg.FuncA", Kind: indexer.SymbolFunction, Language: "go", FilePath: "a.go", StartLine: 1, EndLine: 5},
				},
			},
		},
	}
	_, err := store.ReplaceIndexResult(t.Context(), repoA, symResult)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	syms := store.GetSymbolsByFile(t.Context(), repoA, "a.go")
	if len(syms) == 0 {
		t.Fatal("no symbols found after ReplaceIndexResult")
	}

	link := store.StoreLink(t.Context(), repoA, &graph.StoredLink{
		RepoID:        repoA,
		RequirementID: "req-T34",
		SymbolID:      syms[0].ID,
		Confidence:    0.85,
		Source:        "test",
	})
	if link == nil {
		t.Fatal("StoreLink returned nil")
	}

	// Verify via the filtered store — link's repo is allowed.
	f := filteredFor(store, repoA)
	updated := f.VerifyLink(t.Context(), link.ID, true, "user-A")
	if updated == nil {
		t.Fatal("VerifyLink returned nil; expected updated link")
	}
	if !updated.Verified {
		t.Fatalf("expected Verified=true, got false")
	}
}

// --- T29: GetSymbolCrossRepoRefs partial-target access ---

// TestFilteredIntegrationGetSymbolCrossRepoRefsPartialTarget (T29): when a
// symbol's home repo is in the tenant's allowed set but only SOME cross-repo
// ref target repos are, GetSymbolCrossRepoRefs returns ONLY the subset whose
// target repos are accessible. This pins the result-row filter (Decision 5).
func TestFilteredIntegrationGetSymbolCrossRepoRefsPartialTarget(t *testing.T) {
	store := startSurrealStore(t)
	repoA := seedRepo(t, store, "T29-repo-A")
	repoB := seedRepo(t, store, "T29-repo-B")
	repoC := seedRepo(t, store, "T29-repo-C")

	// Seed a symbol in repoA.
	symResult := &indexer.IndexResult{
		RepoName: "T29-repo-A",
		RepoPath: "/tmp/T29-repo-A",
		Files: []indexer.FileResult{
			{
				Path:     "a.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{
						ID:            "sym-T29-A",
						Name:          "FuncA",
						QualifiedName: "pkg.FuncA",
						Kind:          indexer.SymbolFunction,
						Language:      "go",
						FilePath:      "a.go",
						StartLine:     1,
						EndLine:       5,
					},
				},
			},
		},
	}
	_, err := store.ReplaceIndexResult(t.Context(), repoA, symResult)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}

	syms := store.GetSymbolsByFile(t.Context(), repoA, "a.go")
	if len(syms) == 0 {
		t.Fatal("no symbols found after ReplaceIndexResult")
	}
	symID := syms[0].ID

	// Store two cross-repo refs: A→B (allowed) and A→C (not in tenant's set).
	if err := store.StoreCrossRepoRef(t.Context(), &graph.CrossRepoRef{
		SourceSymbolID: symID,
		SourceRepoID:   repoA,
		TargetRepoID:   repoB,
		RefType:        "import",
		Confidence:     0.9,
	}); err != nil {
		t.Fatalf("StoreCrossRepoRef A→B: %v", err)
	}
	if err := store.StoreCrossRepoRef(t.Context(), &graph.CrossRepoRef{
		SourceSymbolID: symID,
		SourceRepoID:   repoA,
		TargetRepoID:   repoC,
		RefType:        "import",
		Confidence:     0.8,
	}); err != nil {
		t.Fatalf("StoreCrossRepoRef A→C: %v", err)
	}

	// Tenant may access A and B but NOT C.
	f := filteredFor(store, repoA, repoB)
	refs, err := f.GetSymbolCrossRepoRefs(t.Context(), symID)
	if err != nil {
		t.Fatalf("GetSymbolCrossRepoRefs: %v", err)
	}

	if len(refs) != 1 {
		t.Fatalf("T29: want exactly 1 ref (A→B), got %d", len(refs))
	}
	if refs[0].TargetRepoID != repoB {
		t.Errorf("T29: want TargetRepoID=%q, got %q", repoB, refs[0].TargetRepoID)
	}
}
