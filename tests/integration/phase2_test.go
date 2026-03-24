// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func fixtureRepoPath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "fixtures", "multi-lang-repo")
}

func setupPhase2Server(t *testing.T, store *graph.Store) (*httptest.Server, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.CORSOrigins = []string{"http://localhost:3000"}

	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, cfg.Security.JWTTTLMinutes, "")
	localAuth := auth.NewLocalAuth(jwtMgr)

	srv := rest.NewServer(cfg, localAuth, jwtMgr, store, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Setup auth and get token
	resp, err := http.Post(ts.URL+"/auth/setup", "application/json",
		strings.NewReader(`{"password":"testpassword123"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	return ts, result["token"].(string)
}

func gqlQuery(t *testing.T, ts *httptest.Server, token, query string) map[string]interface{} {
	t.Helper()
	body := `{"query":"` + strings.ReplaceAll(query, `"`, `\"`) + `"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/graphql", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GraphQL query failed: %d %s", resp.StatusCode, b)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}

func TestIndexFixtureRepo(t *testing.T) {
	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}

	if result.TotalFiles < 5 {
		t.Errorf("expected at least 5 files, got %d", result.TotalFiles)
	}
	if result.TotalSymbols < 15 {
		t.Errorf("expected at least 15 symbols, got %d", result.TotalSymbols)
	}

	// Verify all 5 languages are present
	languages := make(map[string]bool)
	for _, f := range result.Files {
		languages[f.Language] = true
	}
	for _, lang := range []string{"go", "python", "typescript", "java", "rust"} {
		if !languages[lang] {
			t.Errorf("expected language %s in results", lang)
		}
	}

	t.Logf("Indexed: %d files, %d symbols, %d modules across %d languages",
		result.TotalFiles, result.TotalSymbols, len(result.Modules), len(languages))
}

func TestIndexPerformance(t *testing.T) {
	start := time.Now()
	idx := indexer.NewIndexer(nil)
	_, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Errorf("indexing took %v, expected < 10s", elapsed)
	}
	t.Logf("Indexing completed in %v", elapsed)
}

func TestGraphQLRepositories(t *testing.T) {
	store := graph.NewStore()

	// Index and store
	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.StoreIndexResult(result)
	if err != nil {
		t.Fatal(err)
	}

	ts, token := setupPhase2Server(t, store)
	gqlResult := gqlQuery(t, ts, token, "{ repositories { id name fileCount functionCount } }")

	data := gqlResult["data"].(map[string]interface{})
	repos := data["repositories"].([]interface{})

	if len(repos) != 1 {
		t.Fatalf("expected 1 repository, got %d", len(repos))
	}

	repo := repos[0].(map[string]interface{})
	name := repo["name"].(string)
	if name != "multi-lang-repo" {
		t.Errorf("expected repo name 'multi-lang-repo', got %q", name)
	}

	fileCount := int(repo["fileCount"].(float64))
	if fileCount < 5 {
		t.Errorf("expected fileCount >= 5, got %d", fileCount)
	}

	funcCount := int(repo["functionCount"].(float64))
	if funcCount < 15 {
		t.Errorf("expected functionCount >= 15, got %d", funcCount)
	}

	t.Logf("Repository: %s (files=%d, functions=%d)", name, fileCount, funcCount)
}

func TestGraphQLSymbolSearch(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := store.StoreIndexResult(result)

	ts, token := setupPhase2Server(t, store)

	// Search for processPayment
	query := `{ symbols(repositoryId: "` + repo.ID + `", query: "ProcessPayment") { nodes { name kind filePath startLine } totalCount } }`
	gqlResult := gqlQuery(t, ts, token, query)

	data := gqlResult["data"].(map[string]interface{})
	symbols := data["symbols"].(map[string]interface{})
	nodes := symbols["nodes"].([]interface{})

	if len(nodes) == 0 {
		t.Fatal("expected to find ProcessPayment symbol")
	}

	first := nodes[0].(map[string]interface{})
	t.Logf("Found symbol: %s (%s) at %s:%v",
		first["name"], first["kind"], first["filePath"], first["startLine"])
}

func TestGraphQLIntrospectionWithPhase2(t *testing.T) {
	store := graph.NewStore()
	ts, token := setupPhase2Server(t, store)

	gqlResult := gqlQuery(t, ts, token, "{ __schema { types { name } } }")
	data := gqlResult["data"].(map[string]interface{})
	schema := data["__schema"].(map[string]interface{})
	types := schema["types"].([]interface{})

	typeNames := make(map[string]bool)
	for _, typ := range types {
		m := typ.(map[string]interface{})
		typeNames[m["name"].(string)] = true
	}

	for _, expected := range []string{"Repository", "CodeSymbol", "File", "Module"} {
		if !typeNames[expected] {
			t.Errorf("expected type %q in schema", expected)
		}
	}
}

func TestRemoveRepository(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	// Verify it exists
	repos := store.ListRepositories()
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}

	// Remove it
	removed := store.RemoveRepository(repo.ID)
	if !removed {
		t.Fatal("expected removal to succeed")
	}

	// Verify it's gone
	repos = store.ListRepositories()
	if len(repos) != 0 {
		t.Fatalf("expected 0 repos after removal, got %d", len(repos))
	}

	stats := store.Stats()
	if stats["symbols"] != 0 {
		t.Errorf("expected 0 symbols after removal, got %d", stats["symbols"])
	}
}

func TestModuleExtraction(t *testing.T) {
	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Modules) == 0 {
		t.Fatal("expected modules to be extracted")
	}

	t.Logf("Extracted %d modules", len(result.Modules))
	for _, m := range result.Modules {
		t.Logf("  %s (path=%s, files=%d)", m.Name, m.Path, m.FileCount)
	}
}

func TestTestExtraction(t *testing.T) {
	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}

	testSymbols := 0
	for _, f := range result.Files {
		for _, s := range f.Symbols {
			if s.IsTest {
				testSymbols++
			}
		}
	}

	if testSymbols == 0 {
		t.Fatal("expected test symbols to be extracted")
	}

	t.Logf("Found %d test symbols", testSymbols)
}

func TestCLIIndexOutputsProgress(t *testing.T) {
	// Test that the indexer emits progress events
	var events []indexer.ProgressEvent
	progressFn := func(evt indexer.ProgressEvent) {
		events = append(events, evt)
	}

	idx := indexer.NewIndexer(progressFn)
	_, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}

	if len(events) == 0 {
		t.Fatal("expected progress events")
	}

	// Should have at least scanning, parsing, and complete phases
	phases := make(map[string]bool)
	for _, evt := range events {
		phases[evt.Phase] = true
	}

	for _, expected := range []string{"scanning", "parsing", "complete"} {
		if !phases[expected] {
			t.Errorf("expected phase %q in progress events", expected)
		}
	}

	// Last event should be complete with progress 1.0
	last := events[len(events)-1]
	if last.Phase != "complete" {
		t.Errorf("expected last event phase to be 'complete', got %q", last.Phase)
	}
	if last.Progress != 1.0 {
		t.Errorf("expected final progress to be 1.0, got %f", last.Progress)
	}
}

func TestGraphStoreStats(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	store.StoreIndexResult(result)

	stats := store.Stats()
	t.Logf("Graph stats: %+v", stats)

	if stats["files"] < 5 {
		t.Errorf("expected at least 5 files, got %d", stats["files"])
	}
	if stats["symbols"] < 15 {
		t.Errorf("expected at least 15 symbols, got %d", stats["symbols"])
	}
	if stats["contains"] == 0 {
		t.Error("expected contains relations > 0")
	}
	if stats["imports"] == 0 {
		t.Error("expected imports > 0")
	}
	if stats["modules"] == 0 {
		t.Error("expected modules > 0")
	}
}

func TestContentSearch(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	results := store.SearchContent(repo.ID, "ProcessPayment", 10)
	if len(results) == 0 {
		t.Fatal("expected search results for 'ProcessPayment'")
	}

	found := false
	for _, r := range results {
		t.Logf("Search result: %s (%s) at %s:%d", r.Name, r.Type, r.FilePath, r.Line)
		if strings.Contains(r.Name, "ProcessPayment") {
			found = true
		}
	}

	if !found {
		t.Error("expected to find ProcessPayment in search results")
	}
}
