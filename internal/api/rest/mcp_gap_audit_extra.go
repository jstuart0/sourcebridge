// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Phase 1 (CA-154) — gap-audit cluster.
//
// Two new tools that extend the gap_audit capability surface:
//
//   find_dead_code        — symbols with no callers
//   get_untested_symbols  — symbols with no test linkage
//
// Both mirror callGetOrphanSymbols structurally: full repo scan → predicate
// filter → paginateSlice. All share the same paginationArgs/paginateSlice
// primitives from mcp_pagination.go and the symbolSummary struct from
// mcp_requirement_tools.go.

// ---------------------------------------------------------------------------
// Scan caps (per dexter L1 — never inline a magic number)
// ---------------------------------------------------------------------------

// maxDeadCodeScan is the maximum number of symbols loaded from the store in a
// single find_dead_code scan. Repos with more symbols still return partial
// results, but scan_truncated is set to true in the response so callers know
// the result set is incomplete.
const maxDeadCodeScan = 10000

// maxUntestedScan is the maximum number of symbols loaded from the store in a
// single get_untested_symbols scan. Same semantics as maxDeadCodeScan.
const maxUntestedScan = 10000

// ---------------------------------------------------------------------------
// gapAuditExtraToolDefs
// ---------------------------------------------------------------------------

// gapAuditExtraToolDefs returns the tool definitions for the two CA-154
// gap-audit tools. Called from baseTools() alongside gapAuditToolDefs().
func (h *mcpHandler) gapAuditExtraToolDefs() []mcpToolDefinition {
	paginationProps := paginationToolProps(50, 200)
	return []mcpToolDefinition{
		{
			Name: "find_dead_code",
			Description: "Returns symbols with no callers in the call graph (dead-code candidates). " +
				"Each page performs a full repo scan (capped at " + fmt.Sprintf("%d", maxDeadCodeScan) + " symbols) — " +
				"the cursor slices output, it does not reduce per-page scan cost. " +
				"By default, exported / public-named symbols are excluded because they may be entry " +
				"points called by external packages or frameworks. " +
				"IMPORTANT: `exclude_entry_points: true` is name-based only (`isLikelyPublicSymbol`). " +
				"`init` functions, HTTP handlers, gRPC service methods, and interface-only " +
				"implementations may still appear in results because the heuristic does not understand " +
				"framework dispatch or interface satisfaction. Use the `kinds[]` filter or post-filter " +
				"results when needed.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": mergeMaps(map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID to scan.",
					},
					"exclude_entry_points": map[string]interface{}{
						"type":        "boolean",
						"description": "When true (default), exclude exported/public-named symbols from results. Uses the isLikelyPublicSymbol name-based heuristic — see tool description for limitations.",
					},
					"exclude_test_only_callers": map[string]interface{}{
						"type":        "boolean",
						"description": "When true, symbols whose only callers are test functions (IsTest=true) are included in results with confidence 0.8 and reason test_callers_only. Default false.",
					},
					"kinds": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Optional list of symbol kinds to include (e.g. [\"function\", \"method\"]). When omitted, all kinds are returned.",
					},
				}, paginationProps),
				"required": []string{"repository_id"},
			},
		},
		{
			Name: "get_untested_symbols",
			Description: "Returns non-test symbols with no test linkage (test-coverage gaps). " +
				"By default, only persisted RelationTests edges and IsTest callers (via GetCallers) " +
				"are checked — set `include_name_heuristic: true` to also accept text-reference " +
				"matches (noisier; higher false-positive rate). " +
				"Each page performs a full repo scan (capped at " + fmt.Sprintf("%d", maxUntestedScan) + " symbols) — " +
				"the cursor slices output, it does not reduce per-page scan cost. " +
				"Test symbols (IsTest=true) are always excluded from results.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": mergeMaps(map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID to scan.",
					},
					"kinds": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Optional list of symbol kinds to include (e.g. [\"function\", \"method\"]). When omitted, all non-test symbols are considered.",
					},
					"include_name_heuristic": map[string]interface{}{
						"type":        "boolean",
						"description": "When true, also accept text-reference name matches as evidence of test coverage. Increases recall at the cost of precision. Default false.",
					},
				}, paginationProps),
				"required": []string{"repository_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// find_dead_code
// ---------------------------------------------------------------------------

// deadSymbolResult is the per-symbol shape returned by find_dead_code.
type deadSymbolResult struct {
	SymbolID   string  `json:"symbol_id"`
	SymbolName string  `json:"symbol_name"`
	FilePath   string  `json:"file_path,omitempty"`
	Kind       string  `json:"kind,omitempty"`
	Language   string  `json:"language,omitempty"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

func (h *mcpHandler) callFindDeadCode(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID           string   `json:"repository_id"`
		ExcludeEntryPoints     *bool    `json:"exclude_entry_points"`
		ExcludeTestOnlyCallers bool     `json:"exclude_test_only_callers"`
		Kinds                  []string `json:"kinds"`
		paginationArgs
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	offset, err := decodeCursor(params.Cursor)
	if err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	// Default exclude_entry_points=true (D5).
	excludeEPs := true
	if params.ExcludeEntryPoints != nil {
		excludeEPs = *params.ExcludeEntryPoints
	}

	// kindsSet is non-nil only when the caller specified a filter.
	var kindsSet map[string]struct{}
	if len(params.Kinds) > 0 {
		kindsSet = make(map[string]struct{}, len(params.Kinds))
		for _, k := range params.Kinds {
			kindsSet[k] = struct{}{}
		}
	}

	// Full scan capped at maxDeadCodeScan. GetSymbols returns
	// (symbols_loaded, total_in_store); we use total_in_store to detect
	// truncation without a separate COUNT query.
	syms, totalInStore := h.store.GetSymbols(ctx, params.RepositoryID, nil, nil, maxDeadCodeScan, 0)
	scanTruncated := totalInStore > maxDeadCodeScan

	// Pre-resolve test-symbol set for the exclude_test_only_callers path
	// (dexter H1 — pre-build once, not per-symbol). We only need this when
	// the caller requests test-caller exclusion; avoid the extra scan otherwise.
	var testSymsByID map[string]struct{}
	if params.ExcludeTestOnlyCallers {
		testSymsByID = make(map[string]struct{})
		for _, s := range syms {
			if s.IsTest {
				testSymsByID[s.ID] = struct{}{}
			}
		}
	}

	dead := make([]deadSymbolResult, 0)
	for _, sym := range syms {
		// Cross-repo isolation (codex P1 #3).
		if sym.RepoID != params.RepositoryID {
			continue
		}
		// IsTest symbols before entry-point check (plan requirement: filter
		// IsTest BEFORE isLikelyPublicSymbol).
		if sym.IsTest {
			continue
		}
		// Kind filter.
		if kindsSet != nil {
			if _, ok := kindsSet[sym.Kind]; !ok {
				continue
			}
		}
		// Entry-point filter (name-based heuristic only — see tool description).
		if excludeEPs && isLikelyPublicSymbol(sym.Name, sym.Language) {
			continue
		}

		callers := h.store.GetCallers(ctx, sym.ID)

		if len(callers) == 0 {
			// Definitively no callers.
			dead = append(dead, deadSymbolResult{
				SymbolID:   sym.ID,
				SymbolName: sym.Name,
				FilePath:   sym.FilePath,
				Kind:       sym.Kind,
				Language:   sym.Language,
				Confidence: 1.0,
				Reason:     "no_callers",
			})
			continue
		}

		// When exclude_test_only_callers is set, check whether all callers
		// are test symbols. If so, treat as dead with lower confidence.
		if params.ExcludeTestOnlyCallers {
			allTest := true
			for _, callerID := range callers {
				if _, isTest := testSymsByID[callerID]; !isTest {
					allTest = false
					break
				}
			}
			if allTest {
				dead = append(dead, deadSymbolResult{
					SymbolID:   sym.ID,
					SymbolName: sym.Name,
					FilePath:   sym.FilePath,
					Kind:       sym.Kind,
					Language:   sym.Language,
					Confidence: 0.8,
					Reason:     "test_callers_only",
				})
			}
		}
	}

	// Stable sort (file_path then symbol_name) so pagination is deterministic.
	sort.Slice(dead, func(i, j int) bool {
		if dead[i].FilePath != dead[j].FilePath {
			return dead[i].FilePath < dead[j].FilePath
		}
		return dead[i].SymbolName < dead[j].SymbolName
	})

	page, nextCursor, total := paginateSlice(dead, offset, params.Limit, 50, 200)

	return map[string]interface{}{
		"dead_symbols":   page,
		"total_count":    total,
		"next_cursor":    nullableString(nextCursor),
		"scan_truncated": scanTruncated,
		"_meta": map[string]interface{}{
			"exclude_entry_points":      excludeEPs,
			"exclude_test_only_callers": params.ExcludeTestOnlyCallers,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// get_untested_symbols
// ---------------------------------------------------------------------------

// untestedSymbolResult is the per-symbol shape returned by get_untested_symbols.
type untestedSymbolResult struct {
	SymbolID   string `json:"symbol_id"`
	SymbolName string `json:"symbol_name"`
	FilePath   string `json:"file_path,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Language   string `json:"language,omitempty"`
	TestCount  int    `json:"test_count"`
}

func (h *mcpHandler) callGetUntestedSymbols(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID         string   `json:"repository_id"`
		Kinds                []string `json:"kinds"`
		IncludeNameHeuristic bool     `json:"include_name_heuristic"`
		paginationArgs
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	offset, err := decodeCursor(params.Cursor)
	if err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	// kindsSet is non-nil only when the caller specified a filter.
	var kindsSet map[string]struct{}
	if len(params.Kinds) > 0 {
		kindsSet = make(map[string]struct{}, len(params.Kinds))
		for _, k := range params.Kinds {
			kindsSet[k] = struct{}{}
		}
	}

	// -----------------------------------------------------------------------
	// Step 1: ONE GetSymbols call up front — O(n+k) invariant (dexter H1).
	//
	// Build (a) the master scan list, and (b) testSymsByID for O(1) lookup
	// in the per-symbol caller-filter below.
	// -----------------------------------------------------------------------
	allSyms, totalInStore := h.store.GetSymbols(ctx, params.RepositoryID, nil, nil, maxUntestedScan, 0)
	scanTruncated := totalInStore > maxUntestedScan

	// testSymsByID is keyed by symbol ID. Used for:
	//   - Excluding test symbols from output.
	//   - O(1) IsTest check per caller in the secondary linkage step.
	//   - name-heuristic target set (if include_name_heuristic=true).
	testSymsByID := make(map[string]struct{}, len(allSyms)/4)
	for _, s := range allSyms {
		if s.IsTest {
			testSymsByID[s.ID] = struct{}{}
		}
	}

	// -----------------------------------------------------------------------
	// Step 2: scan loop. Per symbol: persisted-edges + IsTest-caller checks
	// resolve via testSymsByID lookup (O(1)) — no new GetSymbols calls.
	// -----------------------------------------------------------------------
	untested := make([]untestedSymbolResult, 0)
	for _, sym := range allSyms {
		// Cross-repo isolation.
		if sym.RepoID != params.RepositoryID {
			continue
		}
		// Exclude test symbols from results (a test with no tests is a
		// tautology, not a gap).
		if sym.IsTest {
			continue
		}
		// Kind filter.
		if kindsSet != nil {
			if _, ok := kindsSet[sym.Kind]; !ok {
				continue
			}
		}

		// Primary: persisted RelationTests edges.
		if persisted := h.store.GetTestsForSymbolPersisted(ctx, sym.ID); len(persisted) > 0 {
			continue // tested
		}

		// Secondary: IsTest callers via call graph.
		hasTestCaller := false
		for _, callerID := range h.store.GetCallers(ctx, sym.ID) {
			if _, ok := testSymsByID[callerID]; ok {
				hasTestCaller = true
				break
			}
		}
		if hasTestCaller {
			continue // tested
		}

		// Tertiary (optional): name-heuristic — does any test symbol's name
		// reference this symbol? (O(n) over test set per untested symbol;
		// acceptable when the user explicitly opts in.)
		if params.IncludeNameHeuristic {
			matched := false
			for _, s := range allSyms {
				if !s.IsTest {
					continue
				}
				if nameReferences(s.Name, sym.Name) {
					matched = true
					break
				}
			}
			if matched {
				continue // covered by name heuristic
			}
		}

		untested = append(untested, untestedSymbolResult{
			SymbolID:   sym.ID,
			SymbolName: sym.Name,
			FilePath:   sym.FilePath,
			Kind:       sym.Kind,
			Language:   sym.Language,
			TestCount:  0, // always 0 by definition
		})
	}

	// Stable sort (file_path then symbol_name).
	sort.Slice(untested, func(i, j int) bool {
		if untested[i].FilePath != untested[j].FilePath {
			return untested[i].FilePath < untested[j].FilePath
		}
		return untested[i].SymbolName < untested[j].SymbolName
	})

	page, nextCursor, total := paginateSlice(untested, offset, params.Limit, 50, 200)

	linkageMethod := "persisted_edges_and_callers"
	if params.IncludeNameHeuristic {
		linkageMethod = "persisted_edges_and_callers_and_name_heuristic"
	}

	return map[string]interface{}{
		"untested_symbols": page,
		"total_count":      total,
		"next_cursor":      nullableString(nextCursor),
		"scan_truncated":   scanTruncated,
		"_meta": map[string]interface{}{
			"linkage_method": linkageMethod,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// registerGapAuditExtraTools
// ---------------------------------------------------------------------------

// gapAuditExtraTools returns []mcpTool pairing the CA-154 Phase 1 gap-audit
// extra tool definitions with their handlers. Used by registerGapAuditExtraTools.
func (h *mcpHandler) gapAuditExtraTools() []mcpTool {
	defs := h.gapAuditExtraToolDefs()
	defByName := make(map[string]mcpToolDefinition, len(defs))
	for _, d := range defs {
		defByName[d.Name] = d
	}
	return []mcpTool{
		{Definition: defByName["find_dead_code"], Handler: withCtxHandler((*mcpHandler).callFindDeadCode)},
		{Definition: defByName["get_untested_symbols"], Handler: withCtxHandler((*mcpHandler).callGetUntestedSymbols)},
	}
}

// registerGapAuditExtraTools registers the CA-154 Phase 1 gap-audit tools
// into the handler's dispatch map. Called from newMCPHandlerWithEdition after
// registerGapAuditTools.
func registerGapAuditExtraTools(h *mcpHandler) {
	for _, t := range h.gapAuditExtraTools() {
		h.registerTool(t)
	}
}
