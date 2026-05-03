// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"strings"
	"testing"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ---------------------------------------------------------------------------
// Shared fixture
// ---------------------------------------------------------------------------

// RequirementLinkingFixture holds the seeded IDs used across Phase 1a and
// Phase 1b tests. Built by seedRequirementLinkingFixture; named fields let
// each test explicitly reference what it needs.
type RequirementLinkingFixture struct {
	// Repository A (the primary test repo).
	RepoAID string

	// Requirements in repo A.
	Req1ID string // linked to Sym1 and Sym2 (two links)
	Req2ID string // linked to Sym3 only
	Req3ID string // no links — uncovered requirement (Phase 1b case)

	// Symbols in repo A.
	Sym1ID string // FilePath: "handler.go", linked to Req1
	Sym2ID string // FilePath: "handler.go", linked to Req1
	Sym3ID string // FilePath: "utils.go",   linked to Req2
	Sym4ID string // FilePath: "utils.go",   no links — orphan symbol (Phase 1b case)
	Sym5ID string // FilePath: "internal.go", no links, non-public — orphan but not public

	// Repository B (for cross-repo leakage tests).
	RepoBID string
	// Sym6 is in Repo B; it is purposely linked to Req1 (which is in Repo A)
	// to simulate a cross-repo link that the handler must not expose.
	Sym6ID string
}

// seedRequirementLinkingFixture creates a stable fixture with 2 repos,
// 3 requirements, 5 symbols in repo A and 1 symbol in repo B.
// The fixture covers: linked (happy path), no-links (empty array), and
// cross-repo (leakage-blocked) cases.
func seedRequirementLinkingFixture(t *testing.T, h *mcpTestHarness) RequirementLinkingFixture {
	t.Helper()

	// ---- Repo A ----
	resultA := &indexer.IndexResult{
		RepoName: "link-test-repo-a",
		RepoPath: "/tmp/link-test-repo-a",
		Files: []indexer.FileResult{
			{
				Path:      "handler.go",
				Language:  "go",
				LineCount: 50,
				Symbols: []indexer.Symbol{
					{ID: "lnk-sym1", Name: "HandleCreate", QualifiedName: "main.HandleCreate",
						Kind: "function", Language: "go", FilePath: "handler.go",
						StartLine: 10, EndLine: 30},
					{ID: "lnk-sym2", Name: "HandleDelete", QualifiedName: "main.HandleDelete",
						Kind: "function", Language: "go", FilePath: "handler.go",
						StartLine: 35, EndLine: 50},
				},
			},
			{
				Path:      "utils.go",
				Language:  "go",
				LineCount: 40,
				Symbols: []indexer.Symbol{
					{ID: "lnk-sym3", Name: "ParseID", QualifiedName: "main.ParseID",
						Kind: "function", Language: "go", FilePath: "utils.go",
						StartLine: 5, EndLine: 15},
					{ID: "lnk-sym4", Name: "Orphan", QualifiedName: "main.Orphan",
						Kind: "function", Language: "go", FilePath: "utils.go",
						StartLine: 20, EndLine: 30},
				},
			},
			{
				Path:      "internal.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{ID: "lnk-sym5", Name: "internalHelper", QualifiedName: "main.internalHelper",
						Kind: "function", Language: "go", FilePath: "internal.go",
						StartLine: 1, EndLine: 10},
				},
			},
		},
	}
	repoA, err := h.store.ReplaceIndexResult(h.repoID, resultA)
	if err != nil {
		t.Fatalf("ReplaceIndexResult repo A: %v", err)
	}
	h.repoID = repoA.ID

	// Resolve auto-generated symbol IDs.
	sym1ID := lookupSymID(t, h, repoA.ID, "handler.go", "HandleCreate")
	sym2ID := lookupSymID(t, h, repoA.ID, "handler.go", "HandleDelete")
	sym3ID := lookupSymID(t, h, repoA.ID, "utils.go", "ParseID")
	sym4ID := lookupSymID(t, h, repoA.ID, "utils.go", "Orphan")
	sym5ID := lookupSymID(t, h, repoA.ID, "internal.go", "internalHelper")

	// Store requirements.
	h.store.StoreRequirement(repoA.ID, &graphstore.StoredRequirement{
		ID:         "req-1",
		ExternalID: "TEST-1",
		Title:      "Create resource endpoint",
		Priority:   "high",
	})
	h.store.StoreRequirement(repoA.ID, &graphstore.StoredRequirement{
		ID:         "req-2",
		ExternalID: "TEST-2",
		Title:      "Parse resource IDs",
		Priority:   "medium",
	})
	h.store.StoreRequirement(repoA.ID, &graphstore.StoredRequirement{
		ID:         "req-3",
		ExternalID: "TEST-3",
		Title:      "Uncovered requirement",
		Priority:   "low",
	})

	// Resolve stored requirement IDs (store may re-key).
	req1 := h.store.GetRequirementByExternalID(repoA.ID, "TEST-1")
	req2 := h.store.GetRequirementByExternalID(repoA.ID, "TEST-2")
	req3 := h.store.GetRequirementByExternalID(repoA.ID, "TEST-3")
	if req1 == nil || req2 == nil || req3 == nil {
		t.Fatal("failed to retrieve seeded requirements")
	}

	// Links: Req1 → Sym1 (confidence 0.9), Req1 → Sym2 (confidence 0.7),
	//        Req2 → Sym3 (confidence 0.85).
	h.store.StoreLink(repoA.ID, &graphstore.StoredLink{
		RequirementID: req1.ID,
		SymbolID:      sym1ID,
		Confidence:    0.9,
		Source:        "semantic",
	})
	h.store.StoreLink(repoA.ID, &graphstore.StoredLink{
		RequirementID: req1.ID,
		SymbolID:      sym2ID,
		Confidence:    0.7,
		Source:        "semantic",
	})
	h.store.StoreLink(repoA.ID, &graphstore.StoredLink{
		RequirementID: req2.ID,
		SymbolID:      sym3ID,
		Confidence:    0.85,
		Source:        "comment",
	})

	// ---- Repo B ----
	resultB := &indexer.IndexResult{
		RepoName: "link-test-repo-b",
		RepoPath: "/tmp/link-test-repo-b",
		Files: []indexer.FileResult{
			{
				Path:      "service.go",
				Language:  "go",
				LineCount: 20,
				Symbols: []indexer.Symbol{
					{ID: "lnk-sym6", Name: "ServiceInit", QualifiedName: "svc.ServiceInit",
						Kind: "function", Language: "go", FilePath: "service.go",
						StartLine: 1, EndLine: 10},
				},
			},
		},
	}
	repoB, err := h.store.StoreIndexResult(resultB)
	if err != nil {
		t.Fatalf("StoreIndexResult repo B: %v", err)
	}
	sym6ID := lookupSymID(t, h, repoB.ID, "service.go", "ServiceInit")

	// Cross-repo link: Req1 (from repo A) → Sym6 (from repo B). This is
	// the edge that the handler must not surface when queried via repo A.
	h.store.StoreLink(repoB.ID, &graphstore.StoredLink{
		RequirementID: req1.ID,
		SymbolID:      sym6ID,
		Confidence:    0.6,
		Source:        "manual",
	})

	return RequirementLinkingFixture{
		RepoAID: repoA.ID,
		Req1ID:  req1.ID,
		Req2ID:  req2.ID,
		Req3ID:  req3.ID,
		Sym1ID:  sym1ID,
		Sym2ID:  sym2ID,
		Sym3ID:  sym3ID,
		Sym4ID:  sym4ID,
		Sym5ID:  sym5ID,
		RepoBID: repoB.ID,
		Sym6ID:  sym6ID,
	}
}

// lookupSymID resolves a symbol ID by file path + name within a repository.
func lookupSymID(t *testing.T, h *mcpTestHarness, repoID, filePath, name string) string {
	t.Helper()
	for _, s := range h.store.GetSymbolsByFile(repoID, filePath) {
		if s.Name == name {
			return s.ID
		}
	}
	t.Fatalf("lookupSymID: symbol %q not found in %s:%s", name, repoID, filePath)
	return ""
}

// parseRequirementLinkingResult unmarshals a tools/call response into a generic
// map so tests can assert individual fields without tight coupling to struct types.
func parseRequirementLinkingResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	text, isErr := parseToolText(resp)
	if isErr {
		t.Fatalf("unexpected tool error: %s", text)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("json.Unmarshal result: %v (text: %s)", err, text)
	}
	return result
}

// ---------------------------------------------------------------------------
// get_requirements_for_symbol — happy path
// ---------------------------------------------------------------------------

func TestMCP_GetRequirementsForSymbol_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Sym1 is linked to Req1 (confidence 0.9).
	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_requirements_for_symbol",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
			"symbol_id":     fix.Sym1ID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	// symbol_id should be echoed back.
	if got, _ := result["symbol_id"].(string); got != fix.Sym1ID {
		t.Errorf("symbol_id: got %q, want %q", got, fix.Sym1ID)
	}

	reqs, _ := result["requirements"].([]interface{})
	if len(reqs) != 1 {
		t.Fatalf("expected 1 requirement, got %d: %v", len(reqs), reqs)
	}

	req0 := reqs[0].(map[string]interface{})
	if req0["id"] != fix.Req1ID {
		t.Errorf("requirement id: got %v, want %s", req0["id"], fix.Req1ID)
	}
	if req0["external_id"] != "TEST-1" {
		t.Errorf("external_id: got %v, want TEST-1", req0["external_id"])
	}
	if req0["title"] != "Create resource endpoint" {
		t.Errorf("title: got %v, want 'Create resource endpoint'", req0["title"])
	}
	if conf, _ := req0["confidence"].(float64); conf != 0.9 {
		t.Errorf("confidence: got %v, want 0.9", conf)
	}

	if tc, _ := result["total_count"].(float64); tc != 1 {
		t.Errorf("total_count: got %v, want 1", tc)
	}
}

// TestMCP_GetRequirementsForSymbol_MultipleLinks verifies that a symbol linked
// to multiple requirements returns all of them.
func TestMCP_GetRequirementsForSymbol_MultipleLinks(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Re-seed: link Sym1 to both Req1 AND Req2.
	fix := seedRequirementLinkingFixture(t, h)
	h.store.StoreLink(fix.RepoAID, &graphstore.StoredLink{
		RequirementID: fix.Req2ID,
		SymbolID:      fix.Sym1ID,
		Confidence:    0.5,
		Source:        "manual",
	})

	resp := h.sendRPC(sess, 2, "tools/call", map[string]interface{}{
		"name": "get_requirements_for_symbol",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
			"symbol_id":     fix.Sym1ID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)
	reqs, _ := result["requirements"].([]interface{})
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requirements (Req1 and Req2), got %d", len(reqs))
	}
	if tc, _ := result["total_count"].(float64); tc != 2 {
		t.Errorf("total_count: got %v, want 2", tc)
	}
}

// ---------------------------------------------------------------------------
// get_requirements_for_symbol — symbol not found
// ---------------------------------------------------------------------------

func TestMCP_GetRequirementsForSymbol_SymbolNotFound(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "get_requirements_for_symbol",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
			"symbol_id":     "nonexistent-symbol-id",
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for unknown symbol, got success: %s", text)
	}
	if !strings.Contains(strings.ToLower(text), "not found") &&
		!strings.Contains(strings.ToLower(text), "symbol") &&
		!strings.Contains(strings.ToLower(text), "file_path") {
		t.Errorf("error message should mention symbol or resolution hint, got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// get_requirements_for_symbol — cross-repo leakage blocked
// ---------------------------------------------------------------------------

// TestMCP_GetRequirementsForSymbol_CrossRepoLeakageBlocked ensures that a
// symbol from repo B cannot be queried under repo A's repository_id.
func TestMCP_GetRequirementsForSymbol_CrossRepoLeakageBlocked(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Request Sym6 (repo B) under repo A's repository_id.
	resp := h.sendRPC(sess, 4, "tools/call", map[string]interface{}{
		"name": "get_requirements_for_symbol",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
			"symbol_id":     fix.Sym6ID,
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected cross-repo request to be blocked, got success: %s", text)
	}
	// Must not leak information about sym6's existence in repo B.
	if strings.Contains(text, "ServiceInit") {
		t.Errorf("error leaks symbol name from repo B: %q", text)
	}
}

// ---------------------------------------------------------------------------
// get_requirements_for_symbol — no links (empty array, not error)
// ---------------------------------------------------------------------------

func TestMCP_GetRequirementsForSymbol_NoLinks(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Sym4 (Orphan) has no links in the fixture.
	resp := h.sendRPC(sess, 5, "tools/call", map[string]interface{}{
		"name": "get_requirements_for_symbol",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
			"symbol_id":     fix.Sym4ID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	reqs, _ := result["requirements"].([]interface{})
	if len(reqs) != 0 {
		t.Errorf("expected empty requirements array, got %d items", len(reqs))
	}
	if tc, _ := result["total_count"].(float64); tc != 0 {
		t.Errorf("total_count: got %v, want 0", tc)
	}
}

// ---------------------------------------------------------------------------
// get_requirements_for_symbol — resolution via file_path + symbol_name
// ---------------------------------------------------------------------------

func TestMCP_GetRequirementsForSymbol_ByFilePathAndName(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Use file_path + symbol_name instead of symbol_id.
	resp := h.sendRPC(sess, 6, "tools/call", map[string]interface{}{
		"name": "get_requirements_for_symbol",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
			"file_path":     "handler.go",
			"symbol_name":   "HandleCreate",
		},
	})

	result := parseRequirementLinkingResult(t, resp)
	reqs, _ := result["requirements"].([]interface{})
	if len(reqs) != 1 {
		t.Fatalf("expected 1 requirement via file_path/symbol_name, got %d", len(reqs))
	}
}

// ---------------------------------------------------------------------------
// get_symbols_for_requirement — happy path
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolsForRequirement_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Req1 is linked to Sym1 (0.9) and Sym2 (0.7).
	resp := h.sendRPC(sess, 7, "tools/call", map[string]interface{}{
		"name": "get_symbols_for_requirement",
		"arguments": map[string]interface{}{
			"repository_id":  fix.RepoAID,
			"requirement_id": fix.Req1ID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	// Requirement summary must be included.
	reqField, ok := result["requirement"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected requirement field in response, got: %v", result["requirement"])
	}
	if reqField["id"] != fix.Req1ID {
		t.Errorf("requirement.id: got %v, want %s", reqField["id"], fix.Req1ID)
	}
	if reqField["external_id"] != "TEST-1" {
		t.Errorf("requirement.external_id: got %v, want TEST-1", reqField["external_id"])
	}

	syms, _ := result["symbols"].([]interface{})
	if len(syms) != 2 {
		t.Fatalf("expected 2 symbols, got %d: %v", len(syms), syms)
	}

	if tc, _ := result["total_count"].(float64); tc != 2 {
		t.Errorf("total_count: got %v, want 2", tc)
	}

	// Check that symbol fields are populated.
	for _, s := range syms {
		sym := s.(map[string]interface{})
		if sym["symbol_id"] == "" {
			t.Error("symbol_id must not be empty")
		}
		if sym["symbol_name"] == "" {
			t.Error("symbol_name must not be empty")
		}
		if sym["file_path"] == "" {
			t.Error("file_path must not be empty")
		}
	}
}

// TestMCP_GetSymbolsForRequirement_ByExternalID verifies lookup by external ID.
func TestMCP_GetSymbolsForRequirement_ByExternalID(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Use external ID "TEST-1" instead of UUID.
	resp := h.sendRPC(sess, 8, "tools/call", map[string]interface{}{
		"name": "get_symbols_for_requirement",
		"arguments": map[string]interface{}{
			"repository_id":  fix.RepoAID,
			"requirement_id": "TEST-1",
		},
	})

	result := parseRequirementLinkingResult(t, resp)
	syms, _ := result["symbols"].([]interface{})
	if len(syms) != 2 {
		t.Fatalf("expected 2 symbols via external ID, got %d", len(syms))
	}
}

// ---------------------------------------------------------------------------
// get_symbols_for_requirement — requirement not found
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolsForRequirement_RequirementNotFound(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 9, "tools/call", map[string]interface{}{
		"name": "get_symbols_for_requirement",
		"arguments": map[string]interface{}{
			"repository_id":  fix.RepoAID,
			"requirement_id": "NONEXISTENT-999",
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for unknown requirement, got success: %s", text)
	}
	if !strings.Contains(strings.ToLower(text), "not found") {
		t.Errorf("error message should mention not found, got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// get_symbols_for_requirement — no links (empty array, not error)
// ---------------------------------------------------------------------------

func TestMCP_GetSymbolsForRequirement_NoLinks(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Req3 has no linked symbols in the fixture.
	resp := h.sendRPC(sess, 10, "tools/call", map[string]interface{}{
		"name": "get_symbols_for_requirement",
		"arguments": map[string]interface{}{
			"repository_id":  fix.RepoAID,
			"requirement_id": fix.Req3ID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	syms, _ := result["symbols"].([]interface{})
	if len(syms) != 0 {
		t.Errorf("expected empty symbols array, got %d items", len(syms))
	}
	if tc, _ := result["total_count"].(float64); tc != 0 {
		t.Errorf("total_count: got %v, want 0", tc)
	}

	// Requirement summary must still be present.
	reqField, ok := result["requirement"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected requirement field in empty-links response, got: %v", result["requirement"])
	}
	if reqField["id"] != fix.Req3ID {
		t.Errorf("requirement.id: got %v, want %s", reqField["id"], fix.Req3ID)
	}
}

// ---------------------------------------------------------------------------
// get_symbols_for_requirement — cross-repo leakage blocked
// ---------------------------------------------------------------------------

// TestMCP_GetSymbolsForRequirement_CrossRepoLeakageBlocked verifies that
// a requirement UUID from repo A, queried under repo B's repository_id,
// is rejected (the requirement does not belong to repo B).
func TestMCP_GetSymbolsForRequirement_CrossRepoLeakageBlocked(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Req1 belongs to repo A. Query it under repo B's ID.
	resp := h.sendRPC(sess, 11, "tools/call", map[string]interface{}{
		"name": "get_symbols_for_requirement",
		"arguments": map[string]interface{}{
			"repository_id":  fix.RepoBID,
			"requirement_id": fix.Req1ID,
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected cross-repo requirement to be blocked, got success: %s", text)
	}
	// Must not leak the requirement title from repo A.
	if strings.Contains(text, "Create resource endpoint") {
		t.Errorf("error leaks requirement title from repo A: %q", text)
	}
}

// ---------------------------------------------------------------------------
// Pagination tests
// ---------------------------------------------------------------------------

// TestMCP_GetRequirementsForSymbol_Pagination verifies limit/offset work
// and total_count reflects the full unbounded count, not the page size.
func TestMCP_GetRequirementsForSymbol_Pagination(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Link Sym3 to all 3 requirements so we have 3 total links.
	h.store.StoreLink(fix.RepoAID, &graphstore.StoredLink{
		RequirementID: fix.Req1ID,
		SymbolID:      fix.Sym3ID,
		Confidence:    0.8,
		Source:        "semantic",
	})
	h.store.StoreLink(fix.RepoAID, &graphstore.StoredLink{
		RequirementID: fix.Req3ID,
		SymbolID:      fix.Sym3ID,
		Confidence:    0.6,
		Source:        "comment",
	})
	// Sym3 already has Req2 from the fixture → total 3 links.

	// Page 1: limit=2, offset=0 → 2 requirements, total_count=3.
	resp := h.sendRPC(sess, 12, "tools/call", map[string]interface{}{
		"name": "get_requirements_for_symbol",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
			"symbol_id":     fix.Sym3ID,
			"limit":         2,
			"offset":        0,
		},
	})
	result := parseRequirementLinkingResult(t, resp)
	reqs, _ := result["requirements"].([]interface{})
	if len(reqs) != 2 {
		t.Errorf("page 1: expected 2 requirements, got %d", len(reqs))
	}
	if tc, _ := result["total_count"].(float64); tc != 3 {
		t.Errorf("page 1: total_count should be 3 (all links), got %v", tc)
	}

	// Page 2: limit=2, offset=2 → 1 requirement.
	resp2 := h.sendRPC(sess, 13, "tools/call", map[string]interface{}{
		"name": "get_requirements_for_symbol",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
			"symbol_id":     fix.Sym3ID,
			"limit":         2,
			"offset":        2,
		},
	})
	result2 := parseRequirementLinkingResult(t, resp2)
	reqs2, _ := result2["requirements"].([]interface{})
	if len(reqs2) != 1 {
		t.Errorf("page 2: expected 1 requirement, got %d", len(reqs2))
	}
}

// TestMCP_GetSymbolsForRequirement_Pagination verifies pagination for the
// inverse direction.
func TestMCP_GetSymbolsForRequirement_Pagination(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Req1 has Sym1 and Sym2 from the fixture (2 links).
	// Add Sym3 to bring the total to 3.
	h.store.StoreLink(fix.RepoAID, &graphstore.StoredLink{
		RequirementID: fix.Req1ID,
		SymbolID:      fix.Sym3ID,
		Confidence:    0.55,
		Source:        "manual",
	})

	// Page 1: limit=2.
	resp := h.sendRPC(sess, 14, "tools/call", map[string]interface{}{
		"name": "get_symbols_for_requirement",
		"arguments": map[string]interface{}{
			"repository_id":  fix.RepoAID,
			"requirement_id": fix.Req1ID,
			"limit":          2,
			"offset":         0,
		},
	})
	result := parseRequirementLinkingResult(t, resp)
	syms, _ := result["symbols"].([]interface{})
	if len(syms) != 2 {
		t.Errorf("page 1: expected 2 symbols, got %d", len(syms))
	}
	if tc, _ := result["total_count"].(float64); tc != 3 {
		t.Errorf("page 1: total_count should be 3, got %v", tc)
	}
}
