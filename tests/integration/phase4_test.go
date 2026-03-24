// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package integration

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/requirements"
)

// setupPhase4Store creates a store with indexed fixture repo and imported requirements.
func setupPhase4Store(t *testing.T) (*graph.Store, *graph.Repository) {
	t.Helper()
	store := graph.NewStore()

	idx := indexer.NewIndexer(nil)
	result, err := idx.IndexRepository(context.Background(), fixtureRepoPath())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatal(err)
	}

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
	store.StoreRequirements(repo.ID, storedReqs)

	return store, repo
}

func TestLinkStorage(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	if req == nil {
		t.Fatal("REQ-001 not found")
	}

	// Get a symbol
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)
	if len(syms) == 0 {
		t.Fatal("no symbols found")
	}
	sym := syms[0]

	// Store a link
	link := store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      sym.ID,
		Confidence:    0.95,
		Source:        "comment",
		LinkType:      "implements",
		Rationale:     "test link",
	})

	if link.ID == "" {
		t.Fatal("expected link ID to be set")
	}
	if link.RepoID != repo.ID {
		t.Errorf("expected repo ID %s, got %s", repo.ID, link.RepoID)
	}

	// Retrieve
	got := store.GetLink(link.ID)
	if got == nil {
		t.Fatal("link not found")
	}
	if got.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", got.Confidence)
	}

	// Stats should include link
	stats := store.Stats()
	if stats["links"] != 1 {
		t.Errorf("expected 1 link in stats, got %d", stats["links"])
	}
}

func TestLinksForRequirement(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 3, 0)
	if len(syms) < 2 {
		t.Fatal("need at least 2 symbols")
	}

	// Store two links to same requirement
	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      syms[0].ID,
		Confidence:    0.9,
		Source:        "comment",
		LinkType:      "implements",
	})
	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      syms[1].ID,
		Confidence:    0.7,
		Source:        "semantic",
		LinkType:      "implements",
	})

	links := store.GetLinksForRequirement(req.ID, false)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
}

func TestLinksForSymbol(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req1 := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	req2 := store.GetRequirementByExternalID(repo.ID, "REQ-003")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)
	sym := syms[0]

	// Two requirements linked to same symbol
	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req1.ID,
		SymbolID:      sym.ID,
		Confidence:    0.9,
		Source:        "comment",
		LinkType:      "implements",
	})
	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req2.ID,
		SymbolID:      sym.ID,
		Confidence:    0.8,
		Source:        "semantic",
		LinkType:      "implements",
	})

	links := store.GetLinksForSymbol(sym.ID, false)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
}

func TestVerifyLink(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)

	link := store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      syms[0].ID,
		Confidence:    0.7,
		Source:        "semantic",
		LinkType:      "implements",
	})

	// Verify
	verified := store.VerifyLink(link.ID, true, "test-user")
	if verified == nil {
		t.Fatal("expected non-nil result")
	}
	if !verified.Verified {
		t.Error("expected verified=true")
	}
	if verified.Confidence != 1.0 {
		t.Errorf("expected confidence 1.0 after verification, got %f", verified.Confidence)
	}
	if verified.VerifiedBy != "test-user" {
		t.Errorf("expected verified_by=test-user, got %q", verified.VerifiedBy)
	}

	// Reject
	rejected := store.VerifyLink(link.ID, false, "test-user")
	if !rejected.Rejected {
		t.Error("expected rejected=true")
	}
	if rejected.Verified {
		t.Error("expected verified=false after rejection")
	}
}

func TestRejectExcludesFromQuery(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)

	link := store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      syms[0].ID,
		Confidence:    0.7,
		Source:        "semantic",
		LinkType:      "implements",
	})

	// Before rejection
	links := store.GetLinksForRequirement(req.ID, false)
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}

	// Reject
	store.VerifyLink(link.ID, false, "test-user")

	// After rejection — should be excluded
	links = store.GetLinksForRequirement(req.ID, false)
	if len(links) != 0 {
		t.Fatalf("expected 0 links after rejection, got %d", len(links))
	}

	// But include rejected should return it
	links = store.GetLinksForRequirement(req.ID, true)
	if len(links) != 1 {
		t.Fatalf("expected 1 link with includeRejected, got %d", len(links))
	}
}

func TestLinksForRepo(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req1 := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	req2 := store.GetRequirementByExternalID(repo.ID, "REQ-003")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 2, 0)

	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req1.ID,
		SymbolID:      syms[0].ID,
		Confidence:    0.9,
		Source:        "comment",
		LinkType:      "implements",
	})
	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req2.ID,
		SymbolID:      syms[1].ID,
		Confidence:    0.8,
		Source:        "semantic",
		LinkType:      "implements",
	})

	links := store.GetLinksForRepo(repo.ID)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
}

func TestLinkRemovalWithRepo(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)

	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      syms[0].ID,
		Confidence:    0.9,
		Source:        "comment",
		LinkType:      "implements",
	})

	stats := store.Stats()
	if stats["links"] != 1 {
		t.Fatalf("expected 1 link, got %d", stats["links"])
	}

	store.RemoveRepository(repo.ID)

	stats = store.Stats()
	if stats["links"] != 0 {
		t.Errorf("expected 0 links after repo removal, got %d", stats["links"])
	}
}

func TestGraphQLVerifyLink(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)

	link := store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      syms[0].ID,
		Confidence:    0.7,
		Source:        "semantic",
		LinkType:      "implements",
	})

	ts, token := setupPhase3Server(t, store)

	mutation := `mutation($linkId: ID!, $verified: Boolean!) { verifyLink(linkId: $linkId, verified: $verified) { id confidence verified } }`
	variables := map[string]interface{}{
		"linkId":   link.ID,
		"verified": true,
	}

	result := gqlRawQuery(t, ts, token, mutation, variables)

	if errs, ok := result["errors"]; ok {
		t.Fatalf("GraphQL error: %v", errs)
	}

	data := result["data"].(map[string]interface{})
	verifyResult := data["verifyLink"].(map[string]interface{})

	if verifyResult["verified"] != true {
		t.Errorf("expected verified=true, got %v", verifyResult["verified"])
	}
	if verifyResult["confidence"] != "VERIFIED" {
		t.Errorf("expected confidence=VERIFIED, got %v", verifyResult["confidence"])
	}
}

func TestGraphQLCreateManualLink(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)

	ts, token := setupPhase3Server(t, store)

	mutation := `mutation($input: CreateManualLinkInput!) { createManualLink(input: $input) { id confidence verified requirementId symbolId rationale } }`
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"repositoryId":  repo.ID,
			"requirementId": req.ID,
			"symbolId":      syms[0].ID,
			"rationale":     "Manual link for testing",
		},
	}

	result := gqlRawQuery(t, ts, token, mutation, variables)

	if errs, ok := result["errors"]; ok {
		t.Fatalf("GraphQL error: %v", errs)
	}

	data := result["data"].(map[string]interface{})
	linkResult := data["createManualLink"].(map[string]interface{})

	if linkResult["verified"] != true {
		t.Errorf("expected verified=true for manual link, got %v", linkResult["verified"])
	}
	if linkResult["confidence"] != "VERIFIED" {
		t.Errorf("expected confidence=VERIFIED, got %v", linkResult["confidence"])
	}
	if linkResult["rationale"] != "Manual link for testing" {
		t.Errorf("expected rationale, got %v", linkResult["rationale"])
	}
}

func TestGraphQLRequirementToCode(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 2, 0)

	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      syms[0].ID,
		Confidence:    0.9,
		Source:        "comment",
		LinkType:      "implements",
	})
	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req.ID,
		SymbolID:      syms[1].ID,
		Confidence:    0.8,
		Source:        "semantic",
		LinkType:      "implements",
	})

	ts, token := setupPhase3Server(t, store)

	query := `query($reqId: ID!) { requirementToCode(requirementId: $reqId) { id confidence requirementId symbolId } }`
	variables := map[string]interface{}{
		"reqId": req.ID,
	}

	result := gqlRawQuery(t, ts, token, query, variables)

	if errs, ok := result["errors"]; ok {
		t.Fatalf("GraphQL error: %v", errs)
	}

	data := result["data"].(map[string]interface{})
	links := data["requirementToCode"].([]interface{})
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
}

func TestGraphQLCodeToRequirements(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req1 := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	req2 := store.GetRequirementByExternalID(repo.ID, "REQ-003")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 1, 0)
	sym := syms[0]

	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req1.ID,
		SymbolID:      sym.ID,
		Confidence:    0.9,
		Source:        "comment",
		LinkType:      "implements",
	})
	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req2.ID,
		SymbolID:      sym.ID,
		Confidence:    0.8,
		Source:        "semantic",
		LinkType:      "implements",
	})

	ts, token := setupPhase3Server(t, store)

	query := `query($symId: ID!) { codeToRequirements(symbolId: $symId) { id confidence requirementId symbolId } }`
	variables := map[string]interface{}{
		"symId": sym.ID,
	}

	result := gqlRawQuery(t, ts, token, query, variables)

	if errs, ok := result["errors"]; ok {
		t.Fatalf("GraphQL error: %v", errs)
	}

	data := result["data"].(map[string]interface{})
	links := data["codeToRequirements"].([]interface{})
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
}

func TestGraphQLTraceabilityMatrix(t *testing.T) {
	store, repo := setupPhase4Store(t)

	req1 := store.GetRequirementByExternalID(repo.ID, "REQ-001")
	req2 := store.GetRequirementByExternalID(repo.ID, "REQ-003")
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 2, 0)

	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req1.ID,
		SymbolID:      syms[0].ID,
		Confidence:    0.9,
		Source:        "comment",
		LinkType:      "implements",
	})
	store.StoreLink(repo.ID, &graph.StoredLink{
		RequirementID: req2.ID,
		SymbolID:      syms[1].ID,
		Confidence:    0.8,
		Source:        "semantic",
		LinkType:      "implements",
	})

	ts, token := setupPhase3Server(t, store)

	query := `query($repoId: ID!) { traceabilityMatrix(repositoryId: $repoId) { coverage requirements { id } symbols { id } links { id confidence } } }`
	variables := map[string]interface{}{
		"repoId": repo.ID,
	}

	result := gqlRawQuery(t, ts, token, query, variables)

	if errs, ok := result["errors"]; ok {
		t.Fatalf("GraphQL error: %v", errs)
	}

	data := result["data"].(map[string]interface{})
	matrix := data["traceabilityMatrix"].(map[string]interface{})

	reqs := matrix["requirements"].([]interface{})
	if len(reqs) != 14 {
		t.Errorf("expected 14 requirements, got %d", len(reqs))
	}

	matrixLinks := matrix["links"].([]interface{})
	if len(matrixLinks) != 2 {
		t.Errorf("expected 2 links, got %d", len(matrixLinks))
	}

	matrixSyms := matrix["symbols"].([]interface{})
	if len(matrixSyms) != 2 {
		t.Errorf("expected 2 symbols, got %d", len(matrixSyms))
	}

	coverage := matrix["coverage"].(float64)
	// 2 out of 14 requirements have links
	expectedCov := 2.0 / 14.0
	if coverage < expectedCov-0.01 || coverage > expectedCov+0.01 {
		t.Errorf("expected coverage ~%.2f, got %.2f", expectedCov, coverage)
	}
}

func TestGraphQLTraceabilityPerformance(t *testing.T) {
	store, repo := setupPhase4Store(t)

	// Create 100 links
	reqs, _ := store.GetRequirements(repo.ID, 14, 0)
	syms, _ := store.GetSymbols(repo.ID, nil, nil, 100, 0)

	for i := 0; i < 100 && i < len(syms); i++ {
		reqIdx := i % len(reqs)
		store.StoreLink(repo.ID, &graph.StoredLink{
			RequirementID: reqs[reqIdx].ID,
			SymbolID:      syms[i].ID,
			Confidence:    0.8,
			Source:        "comment",
			LinkType:      "implements",
		})
	}

	ts, token := setupPhase3Server(t, store)

	query := `query($repoId: ID!) { traceabilityMatrix(repositoryId: $repoId) { coverage links { id } } }`
	variables := map[string]interface{}{
		"repoId": repo.ID,
	}

	// Should complete quickly
	result := gqlRawQuery(t, ts, token, query, variables)

	if errs, ok := result["errors"]; ok {
		t.Fatalf("GraphQL error: %v", errs)
	}

	data := result["data"].(map[string]interface{})
	matrix := data["traceabilityMatrix"].(map[string]interface{})
	matrixLinks := matrix["links"].([]interface{})

	if len(matrixLinks) == 0 {
		t.Error("expected links in matrix")
	}
}
