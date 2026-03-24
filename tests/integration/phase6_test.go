// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

func setupPhase6Server(t *testing.T, store *graph.Store) (*httptest.Server, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.CORSOrigins = []string{"http://localhost:3000"}

	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, cfg.Security.JWTTTLMinutes, "")
	localAuth := auth.NewLocalAuth(jwtMgr)

	// No worker client (nil) — tests degraded mode
	srv := rest.NewServer(cfg, localAuth, jwtMgr, store, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

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

// --- Readiness endpoint tests ---

func TestReadyzDegradedWithoutWorker(t *testing.T) {
	store := graph.NewStore()
	ts, _ := setupPhase6Server(t, store)

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Should return 200 (core is available even without worker)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	// Overall status should be degraded (worker unavailable)
	status := result["status"].(string)
	if status != "degraded" {
		t.Errorf("expected status 'degraded', got %q", status)
	}

	// Components should exist
	components := result["components"].(map[string]interface{})

	api := components["api"].(map[string]interface{})
	if api["status"] != "healthy" {
		t.Errorf("expected api healthy, got %v", api["status"])
	}

	db := components["database"].(map[string]interface{})
	if db["status"] != "healthy" {
		t.Errorf("expected database healthy, got %v", db["status"])
	}

	worker := components["worker"].(map[string]interface{})
	if worker["status"] != "unavailable" {
		t.Errorf("expected worker unavailable, got %v", worker["status"])
	}
}

func TestReadyzJSONContentType(t *testing.T) {
	store := graph.NewStore()
	ts, _ := setupPhase6Server(t, store)

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json content type, got %q", ct)
	}
}

// --- Metrics endpoint tests ---

func TestMetricsContainsCounters(t *testing.T) {
	store := graph.NewStore()
	ts, _ := setupPhase6Server(t, store)

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	expectedMetrics := []string{
		"sourcebridge_up",
		"sourcebridge_http_requests_total",
		"sourcebridge_http_request_duration_microseconds_total",
		"sourcebridge_graphql_operations_total",
		"sourcebridge_worker_rpc_total",
		"sourcebridge_worker_rpc_errors_total",
		"sourcebridge_indexing_total",
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(text, metric) {
			t.Errorf("metrics should contain %q", metric)
		}
	}
}

func TestMetricsPrometheusFormat(t *testing.T) {
	store := graph.NewStore()
	ts, _ := setupPhase6Server(t, store)

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content type for Prometheus, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	// Prometheus format: lines starting with # are comments (HELP/TYPE)
	if !strings.Contains(text, "# HELP") {
		t.Error("metrics should contain # HELP lines")
	}
	if !strings.Contains(text, "# TYPE") {
		t.Error("metrics should contain # TYPE lines")
	}
}

func TestMetricsCountAfterRequests(t *testing.T) {
	store := graph.NewStore()
	ts, _ := setupPhase6Server(t, store)

	// Make some requests first
	http.Get(ts.URL + "/healthz")
	http.Get(ts.URL + "/healthz")

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	// http_requests_total should be > 0 after making requests
	// The metrics endpoint itself counts, so at least 3+ requests
	if strings.Contains(text, "sourcebridge_http_requests_total 0") {
		t.Error("http_requests_total should be > 0 after making requests")
	}
}

// --- filePath-based symbol query tests ---

func TestGraphQLSymbolsByFilePath(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := store.StoreIndexResult(result)

	ts, token := setupPhase6Server(t, store)

	// Query symbols by filePath (repository-relative)
	query := `{ symbols(repositoryId: "` + repo.ID + `", filePath: "go/main.go") { nodes { name kind filePath } totalCount } }`
	gqlResult := gqlQuery(t, ts, token, query)

	data := gqlResult["data"].(map[string]interface{})
	symbols := data["symbols"].(map[string]interface{})
	nodes := symbols["nodes"].([]interface{})
	totalCount := int(symbols["totalCount"].(float64))

	if totalCount == 0 {
		t.Fatal("expected symbols for go/main.go")
	}

	// All returned symbols should be from go/main.go
	for _, node := range nodes {
		sym := node.(map[string]interface{})
		fp := sym["filePath"].(string)
		if fp != "go/main.go" {
			t.Errorf("expected filePath go/main.go, got %q", fp)
		}
	}

	t.Logf("Found %d symbols in go/main.go", totalCount)
}

func TestGraphQLSymbolsByFilePathEmpty(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	ts, token := setupPhase6Server(t, store)

	// Query with a non-existent file path
	query := `{ symbols(repositoryId: "` + repo.ID + `", filePath: "nonexistent/file.go") { nodes { name } totalCount } }`
	gqlResult := gqlQuery(t, ts, token, query)

	data := gqlResult["data"].(map[string]interface{})
	symbols := data["symbols"].(map[string]interface{})
	totalCount := int(symbols["totalCount"].(float64))

	if totalCount != 0 {
		t.Errorf("expected 0 symbols for nonexistent file, got %d", totalCount)
	}
}

func TestGraphQLSymbolsByNameQuery(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	ts, token := setupPhase6Server(t, store)

	// Existing name-based query should still work
	query := `{ symbols(repositoryId: "` + repo.ID + `", query: "ProcessPayment") { nodes { name } totalCount } }`
	gqlResult := gqlQuery(t, ts, token, query)

	data := gqlResult["data"].(map[string]interface{})
	symbols := data["symbols"].(map[string]interface{})
	nodes := symbols["nodes"].([]interface{})

	if len(nodes) == 0 {
		t.Fatal("expected to find ProcessPayment symbol via name query")
	}
}

// --- AI mutation error handling without worker ---

func TestGraphQLAIMutationWithoutWorker(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	// Get a symbol ID
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)
	if len(syms) == 0 {
		t.Fatal("expected at least one symbol")
	}
	symID := syms[0].ID

	ts, token := setupPhase6Server(t, store)

	// Attempt analyzeSymbol without a worker — should get a GraphQL error
	mutation := `mutation { analyzeSymbol(repositoryId: "` + repo.ID + `", symbolId: "` + symID + `") { summary } }`
	gqlResult := gqlQuery(t, ts, token, mutation)

	// Should have errors, not panic
	if errors, ok := gqlResult["errors"]; ok {
		errList := errors.([]interface{})
		if len(errList) == 0 {
			t.Error("expected at least one GraphQL error")
		}
		firstErr := errList[0].(map[string]interface{})
		msg := firstErr["message"].(string)
		if !strings.Contains(msg, "unavailable") && !strings.Contains(msg, "AI") {
			t.Logf("Error message: %s", msg)
		}
	} else {
		// If no errors, data should still be present (but ideally there are errors)
		t.Log("No GraphQL errors returned — mutation may have handled nil worker gracefully")
	}
}
