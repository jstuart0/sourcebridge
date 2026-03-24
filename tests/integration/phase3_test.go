// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/requirements"
)

// gqlRawQuery sends a GraphQL request using json.Marshal for proper escaping.
func gqlRawQuery(t *testing.T, ts *httptest.Server, token string, query string, variables map[string]interface{}) map[string]interface{} {
	t.Helper()
	body := map[string]interface{}{
		"query": query,
	}
	if variables != nil {
		body["variables"] = variables
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/graphql", strings.NewReader(string(jsonBody)))
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

func setupPhase3Server(t *testing.T, store *graph.Store) (*httptest.Server, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.CORSOrigins = []string{"http://localhost:3000"}

	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, cfg.Security.JWTTTLMinutes, "")
	localAuth := auth.NewLocalAuth(jwtMgr)

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

func readFixtureFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureRepoPath(), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestGoMarkdownParser(t *testing.T) {
	content := readFixtureFile(t, "requirements.md")
	result := requirements.ParseMarkdown(content)

	if len(result.Requirements) != 14 {
		t.Fatalf("expected 14 requirements, got %d", len(result.Requirements))
	}

	first := result.Requirements[0]
	if first.ExternalID != "REQ-001" {
		t.Errorf("expected REQ-001, got %s", first.ExternalID)
	}
	if first.Priority != "High" {
		t.Errorf("expected High, got %q", first.Priority)
	}
	if len(first.AcceptanceCriteria) != 3 {
		t.Errorf("expected 3 acceptance criteria, got %d", len(first.AcceptanceCriteria))
	}
}

func TestGoCSVParser(t *testing.T) {
	content := readFixtureFile(t, "requirements.csv")
	result := requirements.ParseCSV(content, nil)

	if len(result.Requirements) != 4 {
		t.Fatalf("expected 4 requirements, got %d", len(result.Requirements))
	}

	if result.Requirements[3].ExternalID != "REQ-010" {
		t.Errorf("expected REQ-010, got %s", result.Requirements[3].ExternalID)
	}
	if result.Requirements[3].Priority != "Critical" {
		t.Errorf("expected Critical, got %q", result.Requirements[3].Priority)
	}
}

func TestRequirementStorage(t *testing.T) {
	store := graph.NewStore()

	// Index a repo first
	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := store.StoreIndexResult(result)

	// Parse and store requirements
	content := readFixtureFile(t, "requirements.md")
	parsed := requirements.ParseMarkdown(content)

	var storedReqs []*graph.StoredRequirement
	for _, req := range parsed.Requirements {
		storedReqs = append(storedReqs, &graph.StoredRequirement{
			ExternalID:         req.ExternalID,
			Title:              req.Title,
			Description:        req.Description,
			Source:             "requirements.md",
			Priority:           req.Priority,
			AcceptanceCriteria: req.AcceptanceCriteria,
		})
	}

	imported := store.StoreRequirements(repo.ID, storedReqs)
	if imported != 14 {
		t.Fatalf("expected 14 imported, got %d", imported)
	}

	// Retrieve requirements
	reqs, total := store.GetRequirements(repo.ID, 100, 0)
	if total != 14 {
		t.Fatalf("expected total 14, got %d", total)
	}
	if len(reqs) != 14 {
		t.Fatalf("expected 14 requirements, got %d", len(reqs))
	}

	// Verify first requirement
	if reqs[0].ExternalID != "REQ-001" {
		t.Errorf("expected REQ-001, got %s", reqs[0].ExternalID)
	}

	// Test GetRequirementByExternalID
	found := store.GetRequirementByExternalID(repo.ID, "REQ-010")
	if found == nil {
		t.Fatal("expected to find REQ-010")
	}
	if found.Priority != "Critical" {
		t.Errorf("expected Critical, got %q", found.Priority)
	}

	// Test pagination
	reqs, total = store.GetRequirements(repo.ID, 5, 0)
	if total != 14 {
		t.Errorf("expected total 14, got %d", total)
	}
	if len(reqs) != 5 {
		t.Errorf("expected 5 requirements, got %d", len(reqs))
	}

	// Test stats
	stats := store.Stats()
	if stats["requirements"] != 14 {
		t.Errorf("expected 14 requirements in stats, got %d", stats["requirements"])
	}
}

func TestRequirementRemovalWithRepo(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	// Store requirements
	store.StoreRequirements(repo.ID, []*graph.StoredRequirement{
		{ExternalID: "REQ-001", Title: "Test", Description: "Desc"},
	})

	// Verify requirement exists
	reqs, _ := store.GetRequirements(repo.ID, 100, 0)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 requirement, got %d", len(reqs))
	}

	// Remove repo should also remove requirements
	store.RemoveRepository(repo.ID)

	_, total := store.GetRequirements(repo.ID, 100, 0)
	if total != 0 {
		t.Errorf("expected 0 requirements after repo removal, got %d", total)
	}

	stats := store.Stats()
	if stats["requirements"] != 0 {
		t.Errorf("expected 0 requirements in stats after removal, got %d", stats["requirements"])
	}
}

func TestGraphQLImportRequirements(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := store.StoreIndexResult(result)

	ts, token := setupPhase3Server(t, store)

	content := readFixtureFile(t, "requirements.md")

	mutation := `mutation($input: ImportRequirementsInput!) { importRequirements(input: $input) { imported skipped warnings } }`
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"repositoryId": repo.ID,
			"content":      content,
			"format":       "MARKDOWN",
			"sourcePath":   "requirements.md",
		},
	}

	gqlResult := gqlRawQuery(t, ts, token, mutation, variables)

	data := gqlResult["data"].(map[string]interface{})
	importResult := data["importRequirements"].(map[string]interface{})

	imported := int(importResult["imported"].(float64))
	if imported != 14 {
		t.Errorf("expected 14 imported, got %d", imported)
	}
}

func TestGraphQLQueryRequirements(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := store.StoreIndexResult(result)

	// Store requirements directly
	store.StoreRequirements(repo.ID, []*graph.StoredRequirement{
		{ExternalID: "REQ-001", Title: "System Startup", Description: "Desc 1", Source: "test.md", Priority: "High"},
		{ExternalID: "REQ-002", Title: "Data Processing", Description: "Desc 2", Source: "test.md", Priority: "Critical"},
		{ExternalID: "REQ-003", Title: "User Auth", Description: "Desc 3", Source: "test.md", Priority: "Medium"},
	})

	ts, token := setupPhase3Server(t, store)

	// Query all requirements
	query := `{ requirements(repositoryId: "` + repo.ID + `") { nodes { id externalId title description source priority tags } totalCount } }`
	gqlResult := gqlQuery(t, ts, token, query)

	data := gqlResult["data"].(map[string]interface{})
	reqConn := data["requirements"].(map[string]interface{})
	nodes := reqConn["nodes"].([]interface{})
	totalCount := int(reqConn["totalCount"].(float64))

	if totalCount != 3 {
		t.Fatalf("expected totalCount 3, got %d", totalCount)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	first := nodes[0].(map[string]interface{})
	if first["externalId"] != "REQ-001" {
		t.Errorf("expected REQ-001, got %v", first["externalId"])
	}
	if first["title"] != "System Startup" {
		t.Errorf("expected 'System Startup', got %v", first["title"])
	}
	if first["priority"] != "High" {
		t.Errorf("expected 'High', got %v", first["priority"])
	}
}

func TestGraphQLQuerySingleRequirement(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	store.StoreRequirements(repo.ID, []*graph.StoredRequirement{
		{ExternalID: "REQ-100", Title: "Test Req", Description: "A test", Source: "test.md", Priority: "Low"},
	})

	reqs, _ := store.GetRequirements(repo.ID, 1, 0)
	reqID := reqs[0].ID

	ts, token := setupPhase3Server(t, store)

	query := `{ requirement(id: "` + reqID + `") { id externalId title description priority } }`
	gqlResult := gqlQuery(t, ts, token, query)

	data := gqlResult["data"].(map[string]interface{})
	req := data["requirement"].(map[string]interface{})

	if req["externalId"] != "REQ-100" {
		t.Errorf("expected REQ-100, got %v", req["externalId"])
	}
	if req["title"] != "Test Req" {
		t.Errorf("expected 'Test Req', got %v", req["title"])
	}
}

func TestGraphQLImportDuplicateSkip(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	// Pre-store a requirement
	store.StoreRequirements(repo.ID, []*graph.StoredRequirement{
		{ExternalID: "REQ-001", Title: "Existing", Description: "Already here", Source: "old.md"},
	})

	ts, token := setupPhase3Server(t, store)

	content := "## REQ-001: System Startup\nThe system must start.\n- **Priority:** High\n## REQ-002: New One\nNew requirement.\n- **Priority:** Medium"

	mutation := `mutation($input: ImportRequirementsInput!) { importRequirements(input: $input) { imported skipped } }`
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"repositoryId": repo.ID,
			"content":      content,
			"format":       "MARKDOWN",
		},
	}
	gqlResult := gqlRawQuery(t, ts, token, mutation, variables)

	data := gqlResult["data"].(map[string]interface{})
	importResult := data["importRequirements"].(map[string]interface{})

	imported := int(importResult["imported"].(float64))
	skipped := int(importResult["skipped"].(float64))

	if imported != 1 {
		t.Errorf("expected 1 imported, got %d", imported)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
}

func TestGraphQLRequirementsPagination(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	// Store 10 requirements
	var reqs []*graph.StoredRequirement
	for i := 0; i < 10; i++ {
		reqs = append(reqs, &graph.StoredRequirement{
			ExternalID:  fmt.Sprintf("REQ-%03d", i+1),
			Title:       fmt.Sprintf("Requirement %c", rune('A'+i)),
			Description: "Description",
		})
	}
	store.StoreRequirements(repo.ID, reqs)

	ts, token := setupPhase3Server(t, store)

	// Query with limit
	query := `{ requirements(repositoryId: "` + repo.ID + `", limit: 3) { nodes { id } totalCount } }`
	gqlResult := gqlQuery(t, ts, token, query)

	data := gqlResult["data"].(map[string]interface{})
	reqConn := data["requirements"].(map[string]interface{})
	nodes := reqConn["nodes"].([]interface{})
	totalCount := int(reqConn["totalCount"].(float64))

	if totalCount != 10 {
		t.Errorf("expected totalCount 10, got %d", totalCount)
	}
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes with limit, got %d", len(nodes))
	}
}

func TestGraphQLCSVImport(t *testing.T) {
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, _ := idx.IndexRepository(context.Background(), fixtureRepoPath())
	repo, _ := store.StoreIndexResult(result)

	ts, token := setupPhase3Server(t, store)

	csvContent := "id,title,description,priority,acceptance_criteria\nCSV-001,CSV Req 1,First CSV req,High,Criterion A;Criterion B\nCSV-002,CSV Req 2,Second CSV req,Medium,Criterion C"

	mutation := `mutation($input: ImportRequirementsInput!) { importRequirements(input: $input) { imported skipped } }`
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"repositoryId": repo.ID,
			"content":      csvContent,
			"format":       "CSV",
			"sourcePath":   "test.csv",
		},
	}
	gqlResult := gqlRawQuery(t, ts, token, mutation, variables)

	data := gqlResult["data"].(map[string]interface{})
	importResult := data["importRequirements"].(map[string]interface{})

	imported := int(importResult["imported"].(float64))
	if imported != 2 {
		t.Errorf("expected 2 imported from CSV, got %d", imported)
	}
}
