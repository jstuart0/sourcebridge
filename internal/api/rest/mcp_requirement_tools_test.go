// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
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

// ===========================================================================
// Phase 1b — gap-audit tools
// ===========================================================================

// ---------------------------------------------------------------------------
// get_orphan_symbols
// ---------------------------------------------------------------------------

// TestMCP_GetOrphanSymbols_HappyPath verifies that only symbols with no links
// are returned; linked symbols (Sym1, Sym2, Sym3) must not appear.
func TestMCP_GetOrphanSymbols_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 20, "tools/call", map[string]interface{}{
		"name": "get_orphan_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	orphans, _ := result["orphan_symbols"].([]interface{})
	// Sym4 and Sym5 are orphans; Sym1, Sym2, Sym3 are linked.
	if len(orphans) != 2 {
		t.Fatalf("expected 2 orphan symbols, got %d: %v", len(orphans), orphans)
	}

	// Collect orphan IDs and confirm they match expected orphans.
	gotIDs := make(map[string]bool)
	for _, o := range orphans {
		sym := o.(map[string]interface{})
		id, _ := sym["symbol_id"].(string)
		gotIDs[id] = true
		if sym["symbol_name"] == "" {
			t.Error("symbol_name must not be empty")
		}
		if sym["file_path"] == "" {
			t.Error("file_path must not be empty")
		}
	}
	if !gotIDs[fix.Sym4ID] {
		t.Errorf("Sym4 (Orphan) should be in orphan_symbols")
	}
	if !gotIDs[fix.Sym5ID] {
		t.Errorf("Sym5 (internalHelper) should be in orphan_symbols")
	}

	if tc, _ := result["total_count"].(float64); tc != 2 {
		t.Errorf("total_count: got %v, want 2", tc)
	}
}

// TestMCP_GetOrphanSymbols_NoOrphans verifies that when every symbol has at
// least one link, orphan_symbols is empty and total_count is 0.
func TestMCP_GetOrphanSymbols_NoOrphans(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Link the two remaining orphan symbols (Sym4 and Sym5) so none are orphans.
	h.store.StoreLink(fix.RepoAID, &graphstore.StoredLink{
		RequirementID: fix.Req1ID,
		SymbolID:      fix.Sym4ID,
		Confidence:    0.5,
		Source:        "manual",
	})
	h.store.StoreLink(fix.RepoAID, &graphstore.StoredLink{
		RequirementID: fix.Req2ID,
		SymbolID:      fix.Sym5ID,
		Confidence:    0.5,
		Source:        "manual",
	})

	resp := h.sendRPC(sess, 21, "tools/call", map[string]interface{}{
		"name": "get_orphan_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	orphans, _ := result["orphan_symbols"].([]interface{})
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphan symbols, got %d", len(orphans))
	}
	if tc, _ := result["total_count"].(float64); tc != 0 {
		t.Errorf("total_count: got %v, want 0", tc)
	}
	if result["next_cursor"] != nil {
		t.Errorf("next_cursor should be null when result is empty, got %v", result["next_cursor"])
	}
}

// TestMCP_GetOrphanSymbols_AllOrphans verifies that when no requirements exist
// (and thus no links), all symbols are returned as orphans.
func TestMCP_GetOrphanSymbols_AllOrphans(t *testing.T) {
	h := newTestHarness(t)
	// Use a fresh harness with the standard fixture but a repo that has
	// no links seeded at all — create a new repo for this purpose.
	sess := h.createSession()

	result2 := &indexer.IndexResult{
		RepoName: "orphan-all-repo",
		RepoPath: "/tmp/orphan-all-repo",
		Files: []indexer.FileResult{
			{
				Path:     "a.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "FuncA", Kind: "function", Language: "go", FilePath: "a.go", StartLine: 1, EndLine: 5},
					{Name: "FuncB", Kind: "function", Language: "go", FilePath: "a.go", StartLine: 6, EndLine: 10},
				},
			},
		},
	}
	repo, err := h.store.StoreIndexResult(result2)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}
	// No requirements, no links — all symbols are orphans.

	resp := h.sendRPC(sess, 22, "tools/call", map[string]interface{}{
		"name": "get_orphan_symbols",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)
	orphans, _ := result["orphan_symbols"].([]interface{})
	if len(orphans) != 2 {
		t.Fatalf("expected 2 orphan symbols (all symbols are orphans), got %d", len(orphans))
	}
	if tc, _ := result["total_count"].(float64); tc != 2 {
		t.Errorf("total_count: got %v, want 2", tc)
	}
}

// TestMCP_GetOrphanSymbols_Pagination seeds more than 50 orphan symbols and
// verifies that:
//   - Page 1 returns exactly 50 items with next_cursor set.
//   - Page 2 returns the remainder with next_cursor null.
func TestMCP_GetOrphanSymbols_Pagination(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Build a repo with 60 symbols, none linked.
	const symCount = 60
	syms := make([]indexer.Symbol, symCount)
	for i := range syms {
		syms[i] = indexer.Symbol{
			Name:      fmt.Sprintf("OrphanFunc%03d", i),
			Kind:      "function",
			Language:  "go",
			FilePath:  "orphans.go",
			StartLine: i*3 + 1,
			EndLine:   i*3 + 2,
		}
	}
	result := &indexer.IndexResult{
		RepoName: "orphan-paginate-repo",
		RepoPath: "/tmp/orphan-paginate-repo",
		Files: []indexer.FileResult{
			{Path: "orphans.go", Language: "go", Symbols: syms},
		},
	}
	repo, err := h.store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// Page 1: default limit (50).
	resp1 := h.sendRPC(sess, 23, "tools/call", map[string]interface{}{
		"name": "get_orphan_symbols",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
		},
	})
	r1 := parseRequirementLinkingResult(t, resp1)
	page1, _ := r1["orphan_symbols"].([]interface{})
	if len(page1) != 50 {
		t.Errorf("page 1: expected 50 orphans, got %d", len(page1))
	}
	if tc, _ := r1["total_count"].(float64); tc != symCount {
		t.Errorf("page 1: total_count: got %v, want %d", tc, symCount)
	}
	cursor, _ := r1["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("page 1: expected next_cursor to be set, got nil/empty")
	}

	// Page 2: follow the cursor.
	resp2 := h.sendRPC(sess, 24, "tools/call", map[string]interface{}{
		"name": "get_orphan_symbols",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
			"cursor":        cursor,
		},
	})
	r2 := parseRequirementLinkingResult(t, resp2)
	page2, _ := r2["orphan_symbols"].([]interface{})
	if len(page2) != symCount-50 {
		t.Errorf("page 2: expected %d orphans, got %d", symCount-50, len(page2))
	}
	if r2["next_cursor"] != nil {
		t.Errorf("page 2: next_cursor should be null (last page), got %v", r2["next_cursor"])
	}
}

// TestMCP_GetOrphanSymbols_LimitClamp verifies that a limit above 200 is
// silently clamped to 200 (matches the Phase 0 / CA-151 silent-clamp convention).
func TestMCP_GetOrphanSymbols_LimitClamp(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	// Build a repo with 250 unlinked symbols.
	const symCount = 250
	syms := make([]indexer.Symbol, symCount)
	for i := range syms {
		syms[i] = indexer.Symbol{
			Name:      fmt.Sprintf("ClampFunc%03d", i),
			Kind:      "function",
			Language:  "go",
			FilePath:  "clamp.go",
			StartLine: i*2 + 1,
			EndLine:   i*2 + 2,
		}
	}
	result := &indexer.IndexResult{
		RepoName: "clamp-test-repo",
		RepoPath: "/tmp/clamp-test-repo",
		Files: []indexer.FileResult{
			{Path: "clamp.go", Language: "go", Symbols: syms},
		},
	}
	repo, err := h.store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	resp := h.sendRPC(sess, 25, "tools/call", map[string]interface{}{
		"name": "get_orphan_symbols",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
			"limit":         500,
		},
	})

	r := parseRequirementLinkingResult(t, resp)
	page, _ := r["orphan_symbols"].([]interface{})
	if len(page) != 200 {
		t.Errorf("limit clamp: expected 200 results (capped from 500), got %d", len(page))
	}
	if tc, _ := r["total_count"].(float64); tc != symCount {
		t.Errorf("total_count should reflect full orphan count %d, got %v", symCount, tc)
	}
}

// ---------------------------------------------------------------------------
// get_uncovered_requirements
// ---------------------------------------------------------------------------

// TestMCP_GetUncoveredRequirements_HappyPath seeds requirements with a mix of
// covered and uncovered; verifies only uncovered ones are returned.
func TestMCP_GetUncoveredRequirements_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Fixture: Req1 → Sym1+Sym2 (covered), Req2 → Sym3 (covered), Req3 → none (uncovered).
	resp := h.sendRPC(sess, 30, "tools/call", map[string]interface{}{
		"name": "get_uncovered_requirements",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	uncovered, _ := result["uncovered_requirements"].([]interface{})
	if len(uncovered) != 1 {
		t.Fatalf("expected 1 uncovered requirement, got %d: %v", len(uncovered), uncovered)
	}

	req0 := uncovered[0].(map[string]interface{})
	if req0["id"] != fix.Req3ID {
		t.Errorf("uncovered req id: got %v, want %s", req0["id"], fix.Req3ID)
	}
	if req0["title"] != "Uncovered requirement" {
		t.Errorf("title: got %v, want 'Uncovered requirement'", req0["title"])
	}
	if req0["external_id"] != "TEST-3" {
		t.Errorf("external_id: got %v, want TEST-3", req0["external_id"])
	}

	if tc, _ := result["total_count"].(float64); tc != 1 {
		t.Errorf("total_count: got %v, want 1", tc)
	}
	if st, _ := result["scan_truncated"].(bool); st {
		t.Errorf("scan_truncated should be false for a small fixture")
	}
}

// TestMCP_GetUncoveredRequirements_NoUncovered verifies that when every
// requirement is linked, uncovered_requirements is empty.
func TestMCP_GetUncoveredRequirements_NoUncovered(t *testing.T) {
	h := newTestHarness(t)
	fix := seedRequirementLinkingFixture(t, h)
	sess := h.createSession()

	// Link Req3 (the previously uncovered requirement) to Sym4.
	h.store.StoreLink(fix.RepoAID, &graphstore.StoredLink{
		RequirementID: fix.Req3ID,
		SymbolID:      fix.Sym4ID,
		Confidence:    0.5,
		Source:        "manual",
	})

	resp := h.sendRPC(sess, 31, "tools/call", map[string]interface{}{
		"name": "get_uncovered_requirements",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoAID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	uncovered, _ := result["uncovered_requirements"].([]interface{})
	if len(uncovered) != 0 {
		t.Errorf("expected 0 uncovered requirements, got %d", len(uncovered))
	}
	if tc, _ := result["total_count"].(float64); tc != 0 {
		t.Errorf("total_count: got %v, want 0", tc)
	}
	if result["next_cursor"] != nil {
		t.Errorf("next_cursor should be null, got %v", result["next_cursor"])
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetUncoveredRequirements_ScanTruncated
//
// Strategy: the production constant maxUncoveredReqScan = 10000 makes it
// impractical to seed 10001 real rows in a test. Instead we use a thin
// GraphStore wrapper (truncatingGraphStore) that satisfies the GraphStore
// interface by delegating all calls to the underlying *Store except for
// GetRequirements, which reports a total one greater than maxUncoveredReqScan
// when the caller requests maxUncoveredReqScan items. This exercises the
// exact truncation branch in callGetUncoveredRequirements without seeding
// any extra rows.
// ---------------------------------------------------------------------------

// truncatingGraphStore wraps a *graphstore.Store and overrides GetRequirements
// to simulate a repo whose total requirement count exceeds maxUncoveredReqScan.
type truncatingGraphStore struct {
	graphstore.GraphStore
}

func (s truncatingGraphStore) GetRequirements(repoID string, limit, offset int) ([]*graphstore.StoredRequirement, int) {
	reqs, total := s.GraphStore.GetRequirements(repoID, limit, offset)
	// Simulate a store that has one more requirement than the scan cap when
	// the caller asks for exactly maxUncoveredReqScan rows at offset 0.
	if limit == maxUncoveredReqScan && offset == 0 {
		total = maxUncoveredReqScan + 1
	}
	return reqs, total
}

func TestMCP_GetUncoveredRequirements_ScanTruncated(t *testing.T) {
	realStore := graphstore.NewStore()

	// Seed a repo with a handful of requirements (no links — they will all be
	// "uncovered" in the filtered result, but that's irrelevant to this test).
	repo, err := realStore.StoreIndexResult(&indexer.IndexResult{
		RepoName: "truncated-req-repo",
		RepoPath: "/tmp/truncated-req-repo",
	})
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}
	realStore.StoreRequirement(repo.ID, &graphstore.StoredRequirement{
		ID:    "tr-req-1",
		Title: "Truncation test requirement",
	})

	// Wire the handler with the wrapping store.
	wrappedStore := truncatingGraphStore{realStore}
	worker := &mockWorkerCaller{available: true}
	ks := newMockKnowledgeStore()
	h := newMCPHandlerWithEdition(wrappedStore, ks, worker, "", 1*time.Hour, 30*time.Second, 100, nil, capabilities.EditionOSS)

	// Create a session.
	chans := &mcpLocalChans{
		eventCh: make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	sess := &mcpSession{
		id:          "trunc-session-1",
		claims:      &auth.Claims{UserID: "user-1", OrgID: "org-1"},
		initialized: true,
		createdAt:   time.Now(),
		lastUsed:    time.Now(),
		chans:       chans,
	}
	_ = h.sessionStore.Save(context.Background(), sess.toState(), time.Hour)
	h.localChans.Store(sess.id, chans)

	th := &mcpTestHarness{
		handler: h,
		store:   realStore,
		worker:  worker,
		ks:      ks,
		repoID:  repo.ID,
	}

	resp := th.sendRPC(sess, 32, "tools/call", map[string]interface{}{
		"name": "get_uncovered_requirements",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
		},
	})

	result := parseRequirementLinkingResult(t, resp)
	if st, _ := result["scan_truncated"].(bool); !st {
		t.Errorf("scan_truncated should be true when store total > maxUncoveredReqScan")
	}
}

// ===========================================================================
// Phase 2d — get_changed_requirements
// ===========================================================================

// seedChangedRequirementsFixture creates a small repository with:
//   - "service.go" — HandleCreate (linked to Req1), HandleDelete (linked to Req1+Req2)
//   - "utils.go"   — ParseID (linked to Req2), FormatOutput (NO links — unlinked)
//   - Req1, Req2, Req3 (Req3 has no symbol links — uncovered)
//
// Returns the repo ID plus the fixture IDs needed by the tests.
type changedReqsFixture struct {
	RepoID         string
	Req1ID         string
	Req2ID         string
	Req3ID         string
	HandleCreateID string
	HandleDeleteID string
	ParseIDSymID   string
	FormatOutputID string
}

func seedChangedReqsFixture(t *testing.T, h *mcpTestHarness) changedReqsFixture {
	t.Helper()

	result := &indexer.IndexResult{
		RepoName: "changed-reqs-repo",
		RepoPath: "/tmp/changed-reqs-repo",
		Files: []indexer.FileResult{
			{
				Path:     "service.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "HandleCreate", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 1, EndLine: 20},
					{Name: "HandleDelete", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 21, EndLine: 40},
				},
			},
			{
				Path:     "utils.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "ParseID", Kind: "function", Language: "go",
						FilePath: "utils.go", StartLine: 1, EndLine: 15},
					{Name: "FormatOutput", Kind: "function", Language: "go",
						FilePath: "utils.go", StartLine: 16, EndLine: 30},
				},
			},
		},
	}
	repo, err := h.store.ReplaceIndexResult(h.repoID, result)
	if err != nil {
		t.Fatalf("ReplaceIndexResult: %v", err)
	}
	h.repoID = repo.ID

	handleCreateID := lookupSymID(t, h, repo.ID, "service.go", "HandleCreate")
	handleDeleteID := lookupSymID(t, h, repo.ID, "service.go", "HandleDelete")
	parseIDSymID := lookupSymID(t, h, repo.ID, "utils.go", "ParseID")
	formatOutputID := lookupSymID(t, h, repo.ID, "utils.go", "FormatOutput")

	h.store.StoreRequirement(repo.ID, &graphstore.StoredRequirement{
		ID: "cr-req-1", ExternalID: "CR-1", Title: "Create endpoint", Priority: "high",
	})
	h.store.StoreRequirement(repo.ID, &graphstore.StoredRequirement{
		ID: "cr-req-2", ExternalID: "CR-2", Title: "Delete and parse", Priority: "medium",
	})
	h.store.StoreRequirement(repo.ID, &graphstore.StoredRequirement{
		ID: "cr-req-3", ExternalID: "CR-3", Title: "Uncovered req", Priority: "low",
	})

	req1 := h.store.GetRequirementByExternalID(repo.ID, "CR-1")
	req2 := h.store.GetRequirementByExternalID(repo.ID, "CR-2")
	req3 := h.store.GetRequirementByExternalID(repo.ID, "CR-3")
	if req1 == nil || req2 == nil || req3 == nil {
		t.Fatal("failed to retrieve seeded requirements")
	}

	// HandleCreate → Req1
	h.store.StoreLink(repo.ID, &graphstore.StoredLink{
		RequirementID: req1.ID, SymbolID: handleCreateID, Confidence: 0.9, Source: "semantic",
	})
	// HandleDelete → Req1 AND Req2
	h.store.StoreLink(repo.ID, &graphstore.StoredLink{
		RequirementID: req1.ID, SymbolID: handleDeleteID, Confidence: 0.7, Source: "semantic",
	})
	h.store.StoreLink(repo.ID, &graphstore.StoredLink{
		RequirementID: req2.ID, SymbolID: handleDeleteID, Confidence: 0.8, Source: "comment",
	})
	// ParseID → Req2
	h.store.StoreLink(repo.ID, &graphstore.StoredLink{
		RequirementID: req2.ID, SymbolID: parseIDSymID, Confidence: 0.85, Source: "comment",
	})
	// FormatOutput — no links (unlinked touched symbol case)

	return changedReqsFixture{
		RepoID:         repo.ID,
		Req1ID:         req1.ID,
		Req2ID:         req2.ID,
		Req3ID:         req3.ID,
		HandleCreateID: handleCreateID,
		HandleDeleteID: handleDeleteID,
		ParseIDSymID:   parseIDSymID,
		FormatOutputID: formatOutputID,
	}
}

// ---------------------------------------------------------------------------
// HappyPath — file-anchored input
// ---------------------------------------------------------------------------

func TestMCP_GetChangedRequirements_HappyPath_Files(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedReqsFixture(t, h)
	sess := h.createSession()

	// Touch service.go only — HandleCreate (Req1) and HandleDelete (Req1+Req2).
	resp := h.sendRPC(sess, 200, "tools/call", map[string]interface{}{
		"name": "get_changed_requirements",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"files":         []string{"service.go"},
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	changedReqs, _ := result["changed_requirements"].([]interface{})
	// Req1 is reachable from both HandleCreate and HandleDelete;
	// Req2 is reachable from HandleDelete. Req3 has no links → not in result.
	if len(changedReqs) != 2 {
		t.Fatalf("expected 2 changed requirements, got %d: %v", len(changedReqs), changedReqs)
	}

	// Collect returned req IDs.
	gotIDs := map[string]bool{}
	for _, r := range changedReqs {
		req := r.(map[string]interface{})
		id, _ := req["id"].(string)
		gotIDs[id] = true

		// linked_to_symbols must be populated.
		linked, _ := req["linked_to_symbols"].([]interface{})
		if len(linked) == 0 {
			t.Errorf("requirement %s: linked_to_symbols must not be empty", id)
		}
	}
	if !gotIDs[fix.Req1ID] {
		t.Errorf("Req1 should be in changed_requirements")
	}
	if !gotIDs[fix.Req2ID] {
		t.Errorf("Req2 should be in changed_requirements")
	}

	// No unlinked touched symbols — both service.go symbols have links.
	unlinked, _ := result["unlinked_touched_symbols"].([]interface{})
	if len(unlinked) != 0 {
		t.Errorf("expected 0 unlinked_touched_symbols, got %d: %v", len(unlinked), unlinked)
	}

	// Echo fields must be present.
	if got, _ := result["repository_id"].(string); got != fix.RepoID {
		t.Errorf("repository_id: got %q, want %q", got, fix.RepoID)
	}
	files, _ := result["files"].([]interface{})
	if len(files) != 1 {
		t.Errorf("files echo: expected 1 entry, got %v", files)
	}
}

// ---------------------------------------------------------------------------
// HappyPath — commit_range input (real git repo in TempDir)
// ---------------------------------------------------------------------------

func TestMCP_GetChangedRequirements_HappyPath_CommitRange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH; skipping commit_range test")
	}

	repoDir := t.TempDir()

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@test.test")
	runGit("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "service.go"), []byte("package pkg\n"), 0644); err != nil {
		t.Fatalf("WriteFile service.go: %v", err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "add service.go")

	h := newTestHarness(t)
	fix := seedChangedReqsFixture(t, h)

	// Point the stored repo's clone path to our real git checkout.
	h.store.UpdateRepositoryMeta(fix.RepoID, graphstore.RepositoryMeta{
		ClonePath: repoDir,
	})

	sess := h.createSession()

	resp := h.sendRPC(sess, 201, "tools/call", map[string]interface{}{
		"name": "get_changed_requirements",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"commit_range":  "HEAD~0..HEAD",
		},
	})

	// The git root is valid — call must succeed.
	result := parseRequirementLinkingResult(t, resp)

	// Response must echo the commit_range.
	if got, _ := result["commit_range"].(string); got != "HEAD~0..HEAD" {
		t.Errorf("commit_range: got %q, want %q", got, "HEAD~0..HEAD")
	}
	// The git commit touches service.go, which contains HandleCreate and HandleDelete.
	// Both are linked to Req1; HandleDelete is also linked to Req2.
	changedReqs, _ := result["changed_requirements"].([]interface{})
	if len(changedReqs) == 0 {
		t.Errorf("expected at least one changed requirement via commit_range, got 0")
	}
	// Standard shape keys must be present.
	if _, ok := result["unlinked_touched_symbols"]; !ok {
		t.Error("unlinked_touched_symbols key missing from response")
	}
	if _, ok := result["_meta"]; !ok {
		t.Error("_meta key missing from response")
	}
}

// ---------------------------------------------------------------------------
// NoTouchedSymbols — empty file list → empty arrays, no error
// ---------------------------------------------------------------------------

func TestMCP_GetChangedRequirements_NoTouchedSymbols(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedReqsFixture(t, h)
	sess := h.createSession()

	// Pass a file that exists in the repo but has no symbols, or doesn't exist.
	// Using a non-existent file gives us zero symbols without an error.
	resp := h.sendRPC(sess, 202, "tools/call", map[string]interface{}{
		"name": "get_changed_requirements",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"files":         []string{"nonexistent_file.go"},
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	changedReqs, _ := result["changed_requirements"].([]interface{})
	if len(changedReqs) != 0 {
		t.Errorf("expected 0 changed_requirements, got %d", len(changedReqs))
	}
	unlinked, _ := result["unlinked_touched_symbols"].([]interface{})
	if len(unlinked) != 0 {
		t.Errorf("expected 0 unlinked_touched_symbols, got %d", len(unlinked))
	}
}

// ---------------------------------------------------------------------------
// AllUnlinked — touched symbols with no requirement links
// ---------------------------------------------------------------------------

func TestMCP_GetChangedRequirements_AllUnlinked(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedReqsFixture(t, h)
	sess := h.createSession()

	// utils.go: ParseID (linked to Req2) and FormatOutput (no links).
	// Touch ONLY the file containing FormatOutput — but ParseID is also in
	// utils.go, so we seed a repo with a file that has only an unlinked symbol.
	unlinkedOnlyResult := &indexer.IndexResult{
		RepoName: "unlinked-only-repo",
		RepoPath: "/tmp/unlinked-only-repo",
		Files: []indexer.FileResult{
			{
				Path:     "orphan.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "OrphanFunc", Kind: "function", Language: "go",
						FilePath: "orphan.go", StartLine: 1, EndLine: 10},
				},
			},
		},
	}
	repo, err := h.store.StoreIndexResult(unlinkedOnlyResult)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}
	// No links stored for OrphanFunc.

	resp := h.sendRPC(sess, 203, "tools/call", map[string]interface{}{
		"name": "get_changed_requirements",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
			"files":         []string{"orphan.go"},
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	changedReqs, _ := result["changed_requirements"].([]interface{})
	if len(changedReqs) != 0 {
		t.Errorf("expected 0 changed_requirements (all symbols unlinked), got %d", len(changedReqs))
	}
	unlinked, _ := result["unlinked_touched_symbols"].([]interface{})
	if len(unlinked) != 1 {
		t.Fatalf("expected 1 unlinked_touched_symbol, got %d: %v", len(unlinked), unlinked)
	}
	sym := unlinked[0].(map[string]interface{})
	if sym["symbol_name"] != "OrphanFunc" {
		t.Errorf("unlinked symbol name: got %v, want OrphanFunc", sym["symbol_name"])
	}
	if sym["file_path"] != "orphan.go" {
		t.Errorf("unlinked symbol file_path: got %v, want orphan.go", sym["file_path"])
	}
	_ = fix // fixture used only to initialise the harness
}

// ---------------------------------------------------------------------------
// DeduplicatedRequirements — same requirement linked from multiple touched symbols
// ---------------------------------------------------------------------------

func TestMCP_GetChangedRequirements_DeduplicatedRequirements(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedReqsFixture(t, h)
	sess := h.createSession()

	// Touch service.go — HandleCreate and HandleDelete both link to Req1.
	// Req1 should appear exactly once; linked_to_symbols should list both sym IDs.
	resp := h.sendRPC(sess, 204, "tools/call", map[string]interface{}{
		"name": "get_changed_requirements",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"files":         []string{"service.go"},
		},
	})

	result := parseRequirementLinkingResult(t, resp)

	changedReqs, _ := result["changed_requirements"].([]interface{})

	// Find Req1 and verify it appears exactly once.
	var req1Entry map[string]interface{}
	req1Count := 0
	for _, r := range changedReqs {
		req := r.(map[string]interface{})
		if req["id"] == fix.Req1ID {
			req1Count++
			req1Entry = req
		}
	}
	if req1Count != 1 {
		t.Errorf("Req1 should appear exactly once in changed_requirements, got %d", req1Count)
	}

	// linked_to_symbols must contain both HandleCreate and HandleDelete.
	linked, _ := req1Entry["linked_to_symbols"].([]interface{})
	if len(linked) != 2 {
		t.Errorf("Req1 linked_to_symbols: expected 2 (HandleCreate + HandleDelete), got %d: %v", len(linked), linked)
	}
	linkedSet := map[string]bool{}
	for _, l := range linked {
		linkedSet[l.(string)] = true
	}
	if !linkedSet[fix.HandleCreateID] {
		t.Errorf("linked_to_symbols missing HandleCreateID %s", fix.HandleCreateID)
	}
	if !linkedSet[fix.HandleDeleteID] {
		t.Errorf("linked_to_symbols missing HandleDeleteID %s", fix.HandleDeleteID)
	}
}

// ---------------------------------------------------------------------------
// NeitherInputProvided — no commit_range or files → errInvalidArguments
// ---------------------------------------------------------------------------

func TestMCP_GetChangedRequirements_NeitherInputProvided(t *testing.T) {
	h := newTestHarness(t)
	fix := seedChangedReqsFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 205, "tools/call", map[string]interface{}{
		"name": "get_changed_requirements",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			// commit_range and files both absent
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error when neither commit_range nor files provided, got success: %s", text)
	}
	if !strings.Contains(strings.ToLower(text), "commit_range") &&
		!strings.Contains(strings.ToLower(text), "files") &&
		!strings.Contains(strings.ToLower(text), "required") {
		t.Errorf("error message should mention the missing fields, got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// RepoNotFound
// ---------------------------------------------------------------------------

func TestMCP_GetChangedRequirements_RepoNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 206, "tools/call", map[string]interface{}{
		"name": "get_changed_requirements",
		"arguments": map[string]interface{}{
			"repository_id": "nonexistent-repo-id-xyz",
			"files":         []string{"service.go"},
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for nonexistent repo, got success: %s", text)
	}
	// Must surface a meaningful error — not found, not indexed, access denied etc.
	ltext := strings.ToLower(text)
	if !strings.Contains(ltext, "not found") &&
		!strings.Contains(ltext, "not indexed") &&
		!strings.Contains(ltext, "access") &&
		!strings.Contains(ltext, "repository") {
		t.Errorf("error message should reference the repository, got: %s", text)
	}
}
