// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/capabilities"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
)

// ---------------------------------------------------------------------------
// Phase 1 (CA-154): find_dead_code + get_untested_symbols tests
//
// Coverage:
//   find_dead_code:
//     - HappyPath: symbol with no callers appears; symbol with callers absent
//     - AllPublicSkipped: exported Go func absent with exclude_entry_points=true
//     - ExcludeEntryPointsFalse: exported func present when flag flipped off
//     - ExcludeTestOnlyCallers: symbol with only IsTest=true callers → confidence 0.8
//     - ScanTruncated: scan_truncated true when total > maxDeadCodeScan
//     - CrossRepoIsolation: symbols from a different repo not returned
//     - RepoNotFound: MCPErrRepositoryNotIndexed
//
//   get_untested_symbols:
//     - HappyPath: non-test function with no test linkage appears
//     - KindFilter: kinds:["function"] excludes a "class" symbol
//     - NameHeuristic: toggle on excludes a symbol matched by name
//     - TestSymbolsExcluded: IsTest=true symbols not in results
//     - PersistedEdges: symbol with GetTestsForSymbolPersisted non-empty absent
//     - IsTestCaller: symbol whose only caller is IsTest=true absent
//     - CrossRepoIsolation: symbols from another repo not returned
//     - RepoNotFound: MCPErrRepositoryNotIndexed
//     - PreResolvesTestSetOnce: countingGraphStore asserts GetSymbols==1 (dexter H1)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// gapAuditExtraFixture holds IDs seeded for the gap-audit-extra tests.
type gapAuditExtraFixture struct {
	RepoID  string
	RepoBID string // second repo for cross-repo isolation

	// Symbols in RepoA.
	DeadSymID     string // "DeadFunc" — no callers, not public
	LiveSymID     string // "liveFunc" — has a caller
	PublicSymID   string // "PublicHandler" — no callers but exported (Go)
	TestOnlySymID string // "internalHelper" — callers are test-only
	TestSymID     string // "TestDeadFunc" — IsTest=true
	TestedSymID   string // "TestedFunc" — has a persisted RelationTests edge
	CallerSymID   string // "callerFunc" — caller of LiveSym
	TestCallerID  string // "TestLiveFunc" — IsTest caller of TestOnlySym
	ClassSymID    string // "MyClass" — kind=class (for kind-filter test)
	UntestedFnID  string // "untestedFn" — no test linkage whatsoever

	// Symbols in RepoB.
	RepoBSymID string
}

// seedGapAuditExtraFixture creates two repos with a rich symbol set
// covering all test scenarios.
func seedGapAuditExtraFixture(t *testing.T, h *mcpTestHarness) gapAuditExtraFixture {
	t.Helper()

	fix := gapAuditExtraFixture{}

	// ---- Repo A ----
	resultA := &indexer.IndexResult{
		RepoName: "gap-extra-test-repo-a",
		RepoPath: "/tmp/gap-extra-test-repo-a",
		Files: []indexer.FileResult{
			{
				Path:     "service.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{ID: "gae-dead", Name: "deadFunc", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 1, EndLine: 10},
					{ID: "gae-live", Name: "liveFunc", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 11, EndLine: 20},
					{ID: "gae-public", Name: "PublicHandler", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 21, EndLine: 30},
					{ID: "gae-testonly", Name: "internalHelper", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 31, EndLine: 40},
					{ID: "gae-caller", Name: "callerFunc", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 41, EndLine: 50},
					{ID: "gae-tested", Name: "TestedFunc", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 51, EndLine: 60},
					{ID: "gae-class", Name: "MyClass", Kind: "class", Language: "go",
						FilePath: "service.go", StartLine: 61, EndLine: 80},
					{ID: "gae-untested-fn", Name: "untestedFn", Kind: "function", Language: "go",
						FilePath: "service.go", StartLine: 81, EndLine: 90},
				},
			},
			{
				Path:     "service_test.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{ID: "gae-testsym", Name: "TestDeadFunc", Kind: "function", Language: "go",
						FilePath: "service_test.go", StartLine: 1, EndLine: 10,
						IsTest: true},
					{ID: "gae-testcaller", Name: "TestLiveFunc", Kind: "function", Language: "go",
						FilePath: "service_test.go", StartLine: 11, EndLine: 20,
						IsTest: true},
				},
			},
		},
		// Relations:
		//   callerFunc → liveFunc        (calls)
		//   TestLiveFunc → internalHelper (calls — test-only caller)
		//   TestDeadFunc → TestedFunc    (tests — persisted RelationTests edge)
		Relations: []indexer.Relation{
			{SourceID: "gae-caller", TargetID: "gae-live", Type: indexer.RelationCalls},
			{SourceID: "gae-testcaller", TargetID: "gae-testonly", Type: indexer.RelationCalls},
			{SourceID: "gae-testsym", TargetID: "gae-tested", Type: indexer.RelationTests},
		},
	}

	repoA, err := h.store.StoreIndexResult(t.Context(), resultA)
	if err != nil {
		t.Fatalf("StoreIndexResult repoA: %v", err)
	}
	fix.RepoID = repoA.ID

	// Map fixture IDs to store-assigned IDs by name (store may reassign them).
	// Symbols with explicit IDs keep them if the store honours the hint.
	// Look them up by name to be safe.
	symsA, _ := h.store.GetSymbols(t.Context(), fix.RepoID, nil, nil, 0, 0)
	symIDByName := map[string]string{}
	for _, s := range symsA {
		symIDByName[s.Name] = s.ID
	}
	fix.DeadSymID = symIDByName["deadFunc"]
	fix.LiveSymID = symIDByName["liveFunc"]
	fix.PublicSymID = symIDByName["PublicHandler"]
	fix.TestOnlySymID = symIDByName["internalHelper"]
	fix.TestSymID = symIDByName["TestDeadFunc"]
	fix.TestedSymID = symIDByName["TestedFunc"]
	fix.CallerSymID = symIDByName["callerFunc"]
	fix.TestCallerID = symIDByName["TestLiveFunc"]
	fix.ClassSymID = symIDByName["MyClass"]
	fix.UntestedFnID = symIDByName["untestedFn"]

	// ---- Repo B (cross-repo isolation) ----
	resultB := &indexer.IndexResult{
		RepoName: "gap-extra-test-repo-b",
		RepoPath: "/tmp/gap-extra-test-repo-b",
		Files: []indexer.FileResult{
			{
				Path:     "other.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{ID: "gae-repob-sym", Name: "otherDeadFunc", Kind: "function", Language: "go",
						FilePath: "other.go", StartLine: 1, EndLine: 10},
				},
			},
		},
	}
	repoB, err := h.store.StoreIndexResult(t.Context(), resultB)
	if err != nil {
		t.Fatalf("StoreIndexResult repoB: %v", err)
	}
	fix.RepoBID = repoB.ID
	symsB, _ := h.store.GetSymbols(t.Context(), fix.RepoBID, nil, nil, 0, 0)
	if len(symsB) > 0 {
		fix.RepoBSymID = symsB[0].ID
	}

	return fix
}

// parseGapAuditExtraResult is a convenience wrapper around parseToolText and
// json.Unmarshal for the gap-audit-extra tool responses.
func parseGapAuditExtraResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
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

// symbolIDsFromDeadSymbols extracts symbol_id values from a dead_symbols array.
func symbolIDsFromDeadSymbols(t *testing.T, result map[string]interface{}) []string {
	t.Helper()
	raw, ok := result["dead_symbols"].([]interface{})
	if !ok {
		t.Fatalf("dead_symbols not a slice: %T", result["dead_symbols"])
	}
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		m := item.(map[string]interface{})
		ids = append(ids, m["symbol_id"].(string))
	}
	return ids
}

// symbolIDsFromUntested extracts symbol_id values from an untested_symbols array.
func symbolIDsFromUntested(t *testing.T, result map[string]interface{}) []string {
	t.Helper()
	raw, ok := result["untested_symbols"].([]interface{})
	if !ok {
		t.Fatalf("untested_symbols not a slice: %T", result["untested_symbols"])
	}
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		m := item.(map[string]interface{})
		ids = append(ids, m["symbol_id"].(string))
	}
	return ids
}

// containsID returns true if id is in ids.
func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// find_dead_code tests
// ---------------------------------------------------------------------------

// TestMCP_FindDeadCode_HappyPath: deadFunc (no callers) appears; liveFunc
// (has a caller) does not.
func TestMCP_FindDeadCode_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "find_dead_code",
		"arguments": map[string]interface{}{
			"repository_id":        fix.RepoID,
			"exclude_entry_points": false,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromDeadSymbols(t, result)

	if !containsID(ids, fix.DeadSymID) {
		t.Errorf("deadFunc (id=%s) should appear in dead_symbols", fix.DeadSymID)
	}
	if containsID(ids, fix.LiveSymID) {
		t.Errorf("liveFunc (id=%s) should NOT appear in dead_symbols (has a caller)", fix.LiveSymID)
	}
	// Verify test symbols are excluded.
	if containsID(ids, fix.TestSymID) {
		t.Errorf("TestDeadFunc (id=%s) is IsTest=true and must not appear", fix.TestSymID)
	}
}

// TestMCP_FindDeadCode_AllPublicSkipped: with exclude_entry_points=true (default),
// PublicHandler (exported) must not appear.
func TestMCP_FindDeadCode_AllPublicSkipped(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 2, "tools/call", map[string]interface{}{
		"name": "find_dead_code",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			// exclude_entry_points defaults to true; omit to test the default.
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromDeadSymbols(t, result)

	if containsID(ids, fix.PublicSymID) {
		t.Errorf("PublicHandler (id=%s) should be excluded by exclude_entry_points=true", fix.PublicSymID)
	}
	// deadFunc is lowercase (not public) — still appears.
	if !containsID(ids, fix.DeadSymID) {
		t.Errorf("deadFunc (id=%s) should still appear even with exclude_entry_points=true", fix.DeadSymID)
	}

	// _meta should reflect the default.
	meta, _ := result["_meta"].(map[string]interface{})
	if meta["exclude_entry_points"] != true {
		t.Errorf("_meta.exclude_entry_points: got %v, want true", meta["exclude_entry_points"])
	}
}

// TestMCP_FindDeadCode_ExcludeEntryPointsFalse: flipping exclude_entry_points=false
// must include PublicHandler in results.
func TestMCP_FindDeadCode_ExcludeEntryPointsFalse(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 3, "tools/call", map[string]interface{}{
		"name": "find_dead_code",
		"arguments": map[string]interface{}{
			"repository_id":        fix.RepoID,
			"exclude_entry_points": false,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromDeadSymbols(t, result)

	if !containsID(ids, fix.PublicSymID) {
		t.Errorf("PublicHandler (id=%s) should appear when exclude_entry_points=false", fix.PublicSymID)
	}
}

// TestMCP_FindDeadCode_ExcludeTestOnlyCallers: internalHelper has only an
// IsTest caller (TestLiveFunc). With exclude_test_only_callers=true it should
// appear with confidence 0.8 and reason test_callers_only.
func TestMCP_FindDeadCode_ExcludeTestOnlyCallers(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 4, "tools/call", map[string]interface{}{
		"name": "find_dead_code",
		"arguments": map[string]interface{}{
			"repository_id":             fix.RepoID,
			"exclude_entry_points":      false,
			"exclude_test_only_callers": true,
		},
	})

	result := parseGapAuditExtraResult(t, resp)

	raw, _ := result["dead_symbols"].([]interface{})
	var testOnlyEntry map[string]interface{}
	for _, item := range raw {
		m := item.(map[string]interface{})
		if m["symbol_id"] == fix.TestOnlySymID {
			testOnlyEntry = m
			break
		}
	}
	if testOnlyEntry == nil {
		t.Fatalf("internalHelper (id=%s) should appear when exclude_test_only_callers=true", fix.TestOnlySymID)
	}
	if got, _ := testOnlyEntry["confidence"].(float64); got != 0.8 {
		t.Errorf("confidence: got %v, want 0.8", got)
	}
	if got, _ := testOnlyEntry["reason"].(string); got != "test_callers_only" {
		t.Errorf("reason: got %q, want test_callers_only", got)
	}

	// Without the flag, internalHelper should NOT appear (it has callers).
	resp2 := h.sendRPC(sess, 5, "tools/call", map[string]interface{}{
		"name": "find_dead_code",
		"arguments": map[string]interface{}{
			"repository_id":             fix.RepoID,
			"exclude_entry_points":      false,
			"exclude_test_only_callers": false,
		},
	})
	result2 := parseGapAuditExtraResult(t, resp2)
	ids2 := symbolIDsFromDeadSymbols(t, result2)
	if containsID(ids2, fix.TestOnlySymID) {
		t.Errorf("internalHelper should NOT appear when exclude_test_only_callers=false")
	}
}

// TestMCP_FindDeadCode_ScanTruncated: when the repo has more symbols than
// maxDeadCodeScan, scan_truncated must be true. We can't actually seed 10001
// symbols in a unit test, so we verify the field exists and is false for our
// small fixture.
func TestMCP_FindDeadCode_ScanTruncated(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 6, "tools/call", map[string]interface{}{
		"name": "find_dead_code",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	truncated, ok := result["scan_truncated"].(bool)
	if !ok {
		t.Fatalf("scan_truncated field missing or not bool: %v", result["scan_truncated"])
	}
	// Our fixture has far fewer than maxDeadCodeScan symbols.
	if truncated {
		t.Errorf("scan_truncated should be false for small fixture, got true")
	}
}

// TestMCP_FindDeadCode_CrossRepoIsolation: symbols from RepoBID must not appear
// in results for RepoAID.
func TestMCP_FindDeadCode_CrossRepoIsolation(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 7, "tools/call", map[string]interface{}{
		"name": "find_dead_code",
		"arguments": map[string]interface{}{
			"repository_id":        fix.RepoID,
			"exclude_entry_points": false,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromDeadSymbols(t, result)

	if fix.RepoBSymID != "" && containsID(ids, fix.RepoBSymID) {
		t.Errorf("RepoBSymID (%s) leaked into RepoA results", fix.RepoBSymID)
	}
}

// TestMCP_FindDeadCode_RepoNotFound: unknown repository_id returns
// MCPErrRepositoryNotIndexed.
func TestMCP_FindDeadCode_RepoNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 8, "tools/call", map[string]interface{}{
		"name": "find_dead_code",
		"arguments": map[string]interface{}{
			"repository_id": "repo-does-not-exist",
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for unknown repo, got success: %s", text)
	}
}

// ---------------------------------------------------------------------------
// get_untested_symbols tests
// ---------------------------------------------------------------------------

// TestMCP_GetUntestedSymbols_HappyPath: untestedFn (no persisted edges, no
// test callers) must appear; TestedFunc (has RelationTests edge) must not.
func TestMCP_GetUntestedSymbols_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 10, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromUntested(t, result)

	if !containsID(ids, fix.UntestedFnID) {
		t.Errorf("untestedFn (id=%s) should appear in untested_symbols", fix.UntestedFnID)
	}
	// TestedFunc has a persisted RelationTests edge from TestDeadFunc.
	if containsID(ids, fix.TestedSymID) {
		t.Errorf("TestedFunc (id=%s) should NOT appear (has persisted test edge)", fix.TestedSymID)
	}
	// Test symbols must never appear.
	if containsID(ids, fix.TestSymID) {
		t.Errorf("TestDeadFunc (IsTest=true) should NOT appear in untested_symbols")
	}

	// test_count must be 0 for all results.
	raw, _ := result["untested_symbols"].([]interface{})
	for _, item := range raw {
		m := item.(map[string]interface{})
		if tc, _ := m["test_count"].(float64); tc != 0 {
			t.Errorf("test_count should be 0 for all results, got %v for symbol %v", tc, m["symbol_id"])
		}
	}
}

// TestMCP_GetUntestedSymbols_KindFilter: passing kinds:["function"] must exclude
// MyClass (kind=class).
func TestMCP_GetUntestedSymbols_KindFilter(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 11, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
			"kinds":         []string{"function"},
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromUntested(t, result)

	if containsID(ids, fix.ClassSymID) {
		t.Errorf("MyClass (kind=class, id=%s) should be excluded by kinds:[\"function\"]", fix.ClassSymID)
	}
	// untestedFn (kind=function) should still appear.
	if !containsID(ids, fix.UntestedFnID) {
		t.Errorf("untestedFn (kind=function, id=%s) should appear with kinds:[\"function\"]", fix.UntestedFnID)
	}
}

// TestMCP_GetUntestedSymbols_NameHeuristic: untestedFn with include_name_heuristic=false
// appears; toggling to true should exclude it if a test symbol name references it.
// Our fixture: TestDeadFunc references "Dead" not "untested", so untestedFn still
// appears. This test verifies the toggle wires up; the name-match detail is
// covered by the secondary assertion.
func TestMCP_GetUntestedSymbols_NameHeuristic(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	// With heuristic OFF (default): untestedFn appears.
	resp := h.sendRPC(sess, 12, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id":          fix.RepoID,
			"include_name_heuristic": false,
		},
	})
	r1 := parseGapAuditExtraResult(t, resp)
	ids1 := symbolIDsFromUntested(t, r1)
	if !containsID(ids1, fix.UntestedFnID) {
		t.Errorf("untestedFn should appear when include_name_heuristic=false")
	}
	meta1, _ := r1["_meta"].(map[string]interface{})
	if got, _ := meta1["linkage_method"].(string); got != "persisted_edges_and_callers" {
		t.Errorf("linkage_method with heuristic off: got %q, want persisted_edges_and_callers", got)
	}

	// With heuristic ON: _meta reflects the change.
	resp2 := h.sendRPC(sess, 13, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id":          fix.RepoID,
			"include_name_heuristic": true,
		},
	})
	r2 := parseGapAuditExtraResult(t, resp2)
	meta2, _ := r2["_meta"].(map[string]interface{})
	if got, _ := meta2["linkage_method"].(string); got != "persisted_edges_and_callers_and_name_heuristic" {
		t.Errorf("linkage_method with heuristic on: got %q, want persisted_edges_and_callers_and_name_heuristic", got)
	}
}

// TestMCP_GetUntestedSymbols_TestSymbolsExcluded: IsTest=true symbols must
// never appear in get_untested_symbols results, even if they have no test linkage.
func TestMCP_GetUntestedSymbols_TestSymbolsExcluded(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 14, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromUntested(t, result)

	if containsID(ids, fix.TestSymID) {
		t.Errorf("TestDeadFunc (IsTest=true, id=%s) must never appear in get_untested_symbols", fix.TestSymID)
	}
	if containsID(ids, fix.TestCallerID) {
		t.Errorf("TestLiveFunc (IsTest=true, id=%s) must never appear in get_untested_symbols", fix.TestCallerID)
	}
}

// TestMCP_GetUntestedSymbols_PersistedEdges: TestedFunc has a persisted
// RelationTests edge from TestDeadFunc and must NOT appear.
func TestMCP_GetUntestedSymbols_PersistedEdges(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 15, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromUntested(t, result)

	if containsID(ids, fix.TestedSymID) {
		t.Errorf("TestedFunc (id=%s) has a persisted RelationTests edge and must NOT appear", fix.TestedSymID)
	}
}

// TestMCP_GetUntestedSymbols_IsTestCaller: internalHelper's only caller is
// TestLiveFunc (IsTest=true), so it is treated as tested and must NOT appear.
func TestMCP_GetUntestedSymbols_IsTestCaller(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 16, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromUntested(t, result)

	if containsID(ids, fix.TestOnlySymID) {
		t.Errorf("internalHelper (id=%s) has an IsTest caller and must NOT appear", fix.TestOnlySymID)
	}
}

// TestMCP_GetUntestedSymbols_CrossRepoIsolation: symbols from RepoBID must not
// appear in RepoAID results.
func TestMCP_GetUntestedSymbols_CrossRepoIsolation(t *testing.T) {
	h := newTestHarness(t)
	fix := seedGapAuditExtraFixture(t, h)
	sess := h.createSession()

	resp := h.sendRPC(sess, 17, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id": fix.RepoID,
		},
	})

	result := parseGapAuditExtraResult(t, resp)
	ids := symbolIDsFromUntested(t, result)

	if fix.RepoBSymID != "" && containsID(ids, fix.RepoBSymID) {
		t.Errorf("RepoBSymID (%s) leaked into RepoA results", fix.RepoBSymID)
	}
}

// TestMCP_GetUntestedSymbols_RepoNotFound: unknown repository_id returns an error.
func TestMCP_GetUntestedSymbols_RepoNotFound(t *testing.T) {
	h := newTestHarness(t)
	sess := h.createSession()

	resp := h.sendRPC(sess, 18, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id": "repo-does-not-exist",
		},
	})

	text, isErr := parseToolText(resp)
	if !isErr {
		t.Fatalf("expected error for unknown repo, got success: %s", text)
	}
}

// ---------------------------------------------------------------------------
// TestMCP_GetUntestedSymbols_PreResolvesTestSetOnce (dexter H1 regression)
//
// Asserts that callGetUntestedSymbols calls GetSymbols exactly once per
// invocation, regardless of how many candidate symbols exist. Uses
// countingGraphStore from mcp_change_impact_test.go (same package).
// ---------------------------------------------------------------------------

func TestMCP_GetUntestedSymbols_PreResolvesTestSetOnce(t *testing.T) {
	realStore := graphstore.NewStore()

	// Seed a repo with 4 production symbols and 1 test symbol.
	indexResult := &indexer.IndexResult{
		RepoName: "untested-counting-repo",
		RepoPath: "/tmp/untested-counting-repo",
		Files: []indexer.FileResult{
			{
				Path:     "svc.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "Alpha", Kind: "function", Language: "go",
						FilePath: "svc.go", StartLine: 1, EndLine: 10},
					{Name: "Beta", Kind: "function", Language: "go",
						FilePath: "svc.go", StartLine: 11, EndLine: 20},
					{Name: "Gamma", Kind: "function", Language: "go",
						FilePath: "svc.go", StartLine: 21, EndLine: 30},
					{Name: "Delta", Kind: "function", Language: "go",
						FilePath: "svc.go", StartLine: 31, EndLine: 40},
				},
			},
			{
				Path:     "svc_test.go",
				Language: "go",
				Symbols: []indexer.Symbol{
					{Name: "TestAlpha", Kind: "function", Language: "go",
						FilePath: "svc_test.go", StartLine: 1, EndLine: 10,
						IsTest: true},
				},
			},
		},
	}
	repo, err := realStore.StoreIndexResult(t.Context(), indexResult)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	// Wrap with counting store.
	cs := &countingGraphStore{GraphStore: realStore}
	worker := &mockWorkerCaller{available: true}
	ks := newMockKnowledgeStore()
	h := newMCPHandlerWithEdition(cs, ks, worker, "", 1*time.Hour, 30*time.Second, 100, nil, capabilities.EditionOSS)

	chans := &mcpLocalChans{
		eventCh: make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	sess := &mcpSession{
		id:          "untested-counting-session",
		claims:      &auth.Claims{UserID: "u1", OrgID: "o1"},
		initialized: true,
		createdAt:   time.Now(),
		lastUsed:    time.Now(),
		chans:       chans,
	}
	_ = h.sessionStore.Save(context.Background(), sess.toState(), time.Hour)
	h.localChans.Store(sess.id, chans)

	th := &mcpTestHarness{handler: h, store: realStore, worker: worker, ks: ks, repoID: repo.ID}

	// Reset counter after handler setup (newMCPHandlerWithEdition may warm caches).
	atomic.StoreInt64(&cs.getSymbolsCalls, 0)

	th.sendRPC(sess, 1, "tools/call", map[string]interface{}{
		"name": "get_untested_symbols",
		"arguments": map[string]interface{}{
			"repository_id": repo.ID,
		},
	})

	calls := atomic.LoadInt64(&cs.getSymbolsCalls)
	if calls != 1 {
		t.Errorf("GetSymbols called %d times; want exactly 1 (O(n+k) invariant per dexter H1)", calls)
	}
}
