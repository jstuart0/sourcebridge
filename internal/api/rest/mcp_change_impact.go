// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"sort"
)

// Phase 2c — predict_change_impact.
//
// Extends impact_summary with:
//   - Diff-anchored input (commit_range / files) via reuse of
//     resolveDiffTouchedSymbols (Phase 2a.1 helper in mcp_compound.go).
//   - Per-symbol test matches as [{file_path, test_name, confidence}]
//     (paths+names only; no source). Confidence is "direct" when the match
//     came from a persisted edge or IsTest caller, "indirect" for text-
//     reference name-overlap.
//   - Per-symbol confidence sourced from ComputeImpact / link.Confidence
//     (bob M5): the Confidence on each AffectedLink already IS the per-link
//     confidence from the graph store — no separate heuristic is applied.
//     When a symbol has no links the confidence defaults to 0.
//   - Affected requirements aggregated and deduped at the top level (bob H2).
//   - unresolved_symbols[] for partial failures (bob H2).
//   - depth=1 cap; depth>1 is rejected with errInvalidArguments.
//
// O(n×k) → O(n+k) fix per dexter H4:
//   GetSymbols (all repo symbols) is called ONCE per predict_change_impact
//   invocation. A pre-built test-symbol index is then used for O(1) per-symbol
//   lookups inside the per-target loop. The earlier impact_summary calls
//   GetSymbols inside the per-symbol loop (O(n×k)); this tool does not.

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

func (h *mcpHandler) changeImpactToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "predict_change_impact",
			Description: "Predict the impact of a code change. Accepts symbol references and/or a diff (commit_range or files). " +
				"Returns per-symbol callers (1 hop), test matches, linked requirements, and confidence — " +
				"plus top-level affected_requirements and affected_tests aggregated across all touched symbols. " +
				"Extends impact_summary with diff-anchored input and richer output. depth must be 1 (only value currently supported).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"commit_range": map[string]interface{}{
						"type":        "string",
						"description": "Git commit range (e.g. \"HEAD~3..HEAD\"). Resolved to touched files when files and symbols are both absent.",
					},
					"files": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Repo-relative file paths to analyse. Overrides commit_range when provided.",
					},
					"symbols": map[string]interface{}{
						"type":        "array",
						"description": "Specific symbol references to analyse (in addition to, or instead of, file/diff anchors).",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"file_path":   map[string]interface{}{"type": "string"},
								"symbol_name": map[string]interface{}{"type": "string"},
								"line_start":  map[string]interface{}{"type": "integer"},
								"symbol_id":   map[string]interface{}{"type": "string", "description": "Optional fast-path ID"},
							},
						},
					},
					"depth": map[string]interface{}{
						"type":        "integer",
						"description": "Caller-walk depth. Only depth=1 is supported (default 1).",
					},
					"max_caller_hops": map[string]interface{}{
						"type":        "integer",
						"description": "Alias for depth (default 1).",
					},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Response shapes
// ---------------------------------------------------------------------------

// changeImpactTestMatch is a test match entry in predict_change_impact.
// Paths+names only — no source bytes (per D3).
type changeImpactTestMatch struct {
	FilePath   string `json:"file_path"`
	TestName   string `json:"test_name"`
	Confidence string `json:"confidence"` // "direct" | "indirect"
}

// changeImpactSymbol is the per-symbol block in predict_change_impact.
type changeImpactSymbol struct {
	SymbolID     string                   `json:"symbol_id"`
	SymbolName   string                   `json:"symbol_name"`
	FilePath     string                   `json:"file_path"`
	Kind         string                   `json:"kind"`
	Callers      []map[string]interface{} `json:"callers"`
	TestMatches  []changeImpactTestMatch  `json:"test_matches"`
	Requirements []requirementSummary     `json:"requirements"`
	Confidence   float64                  `json:"confidence"`
}

// changeImpactScope echoes the inputs that were used to resolve targets.
type changeImpactScope struct {
	CommitRange string   `json:"commit_range,omitempty"`
	Files       []string `json:"files,omitempty"`
	Symbols     []string `json:"symbols,omitempty"`
}

// unresolvedSymbol describes a symbol input that could not be resolved.
type unresolvedSymbol struct {
	Input  string `json:"input"`
	Reason string `json:"reason"`
}

// changeImpactResult is the full predict_change_impact response.
type changeImpactResult struct {
	RepositoryID         string                   `json:"repository_id"`
	Scope                changeImpactScope        `json:"scope"`
	Symbols              []changeImpactSymbol     `json:"symbols"`
	AffectedRequirements []requirementSummary     `json:"affected_requirements"`
	AffectedTests        []changeImpactTestMatch  `json:"affected_tests"`
	UnresolvedSymbols    []unresolvedSymbol       `json:"unresolved_symbols"`
	Depth                int                      `json:"depth"`
	Meta                 map[string]interface{}   `json:"_meta,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (h *mcpHandler) callPredictChangeImpact(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string   `json:"repository_id"`
		CommitRange  string   `json:"commit_range"`
		Files        []string `json:"files"`
		Symbols      []struct {
			FilePath   string `json:"file_path"`
			SymbolName string `json:"symbol_name"`
			LineStart  int    `json:"line_start"`
			SymbolID   string `json:"symbol_id"`
		} `json:"symbols"`
		Depth         int `json:"depth"`
		MaxCallerHops int `json:"max_caller_hops"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	// Resolve effective depth — depth and max_caller_hops are aliases.
	depth := params.Depth
	if depth == 0 && params.MaxCallerHops > 0 {
		depth = params.MaxCallerHops
	}
	if depth == 0 {
		depth = 1
	}
	if depth > 1 {
		return nil, errInvalidArguments("depth > 1 is not supported; only depth=1 is currently implemented")
	}

	repoID := params.RepositoryID

	// -----------------------------------------------------------------------
	// Step 1: Resolve target symbol IDs.
	//
	// Diff-anchored path: commit_range or files → resolveDiffTouchedSymbols.
	// Symbol-anchored path: explicit symbols[] → resolveSymbol per entry.
	// Both paths can contribute to the same invocation; targets are deduped.
	// -----------------------------------------------------------------------

	seenTargets := map[string]bool{}
	var targetIDs []string
	var unresolved []unresolvedSymbol
	var scopeFiles []string
	var scopeSymbolNames []string

	// Diff-anchored: files or commit_range.
	if len(params.Files) > 0 || params.CommitRange != "" {
		_, diffSymIDs, err := h.resolveDiffTouchedSymbols(repoID, params.CommitRange, params.Files)
		if err != nil {
			return nil, err
		}
		for _, id := range diffSymIDs {
			if seenTargets[id] {
				continue
			}
			seenTargets[id] = true
			targetIDs = append(targetIDs, id)
		}
		if len(params.Files) > 0 {
			scopeFiles = params.Files
		}
	}

	// Symbol-anchored: explicit symbols[].
	for _, s := range params.Symbols {
		inputDesc := s.SymbolName
		if s.FilePath != "" {
			inputDesc = s.FilePath + ":" + s.SymbolName
		}

		sym, err := h.resolveSymbol(symbolRefParams{
			RepositoryID: repoID,
			SymbolID:     s.SymbolID,
			FilePath:     s.FilePath,
			SymbolName:   s.SymbolName,
			LineStart:    s.LineStart,
		})
		if err != nil {
			unresolved = append(unresolved, unresolvedSymbol{
				Input:  inputDesc,
				Reason: err.Error(),
			})
			continue
		}
		scopeSymbolNames = append(scopeSymbolNames, sym.Name)
		if seenTargets[sym.ID] {
			continue
		}
		seenTargets[sym.ID] = true
		targetIDs = append(targetIDs, sym.ID)
	}

	// -----------------------------------------------------------------------
	// Step 2: Pre-resolve test-symbol set ONCE (dexter H4).
	//
	// GetSymbols is called exactly once per predict_change_impact invocation,
	// outside the per-target loop. All test symbols are indexed into a map
	// keyed by ID so per-target lookup is O(1).
	// -----------------------------------------------------------------------

	// testSymsByID holds every IsTest=true symbol for this repo.
	type testSymInfo struct {
		FilePath string
		Name     string
	}
	testSymsByID := map[string]testSymInfo{}

	allSyms, _ := h.store.GetSymbols(repoID, nil, nil, 0, 0)
	for _, sym := range allSyms {
		if sym.IsTest {
			testSymsByID[sym.ID] = testSymInfo{FilePath: sym.FilePath, Name: sym.Name}
		}
	}

	// -----------------------------------------------------------------------
	// Step 3: Per-symbol impact.
	// -----------------------------------------------------------------------

	// Requirement aggregation across all touched symbols.
	// reqConfidenceByID tracks the max confidence seen for each requirement.
	reqConfidenceByID := map[string]float64{}
	reqByID := map[string]requirementSummary{}

	// Top-level test aggregation: file_path+test_name dedupe key.
	type testKey struct{ filePath, testName string }
	topLevelTestByKey := map[testKey]changeImpactTestMatch{}

	var symbolResults []changeImpactSymbol

	for _, targetID := range targetIDs {
		sym := h.store.GetSymbol(targetID)
		if sym == nil {
			continue
		}
		// Cross-repo isolation.
		if sym.RepoID != repoID {
			continue
		}

		// Callers (1 hop).
		callerIDs := h.store.GetCallers(targetID)
		callerMap := h.store.GetSymbolsByIDs(callerIDs)
		var callers []map[string]interface{}
		for _, c := range callerMap {
			if c == nil || c.RepoID != repoID {
				continue
			}
			callers = append(callers, map[string]interface{}{
				"symbol_id":   c.ID,
				"symbol_name": c.Name,
				"file_path":   c.FilePath,
				"kind":        c.Kind,
			})
		}
		sort.Slice(callers, func(i, j int) bool {
			fi, _ := callers[i]["file_path"].(string)
			fj, _ := callers[j]["file_path"].(string)
			if fi != fj {
				return fi < fj
			}
			ni, _ := callers[i]["symbol_name"].(string)
			nj, _ := callers[j]["symbol_name"].(string)
			return ni < nj
		})

		// Test matches (O(1) per symbol; test set was pre-resolved above).
		//
		// Confidence:
		//   "direct"   — persisted RelationTests edge (GetTestsForSymbolPersisted)
		//                OR IsTest caller (call-graph edge where callee=target).
		//   "indirect" — name-overlap heuristic (nameReferences).
		var testMatches []changeImpactTestMatch
		testMatchSeen := map[string]bool{}

		addTestMatch := func(filePath, testName, confidence string) {
			k := testKey{filePath, testName}
			if testMatchSeen[filePath+"|"+testName] {
				return
			}
			testMatchSeen[filePath+"|"+testName] = true
			m := changeImpactTestMatch{
				FilePath:   filePath,
				TestName:   testName,
				Confidence: confidence,
			}
			testMatches = append(testMatches, m)
			// Also aggregate into top-level (prefer "direct" over "indirect").
			existing, already := topLevelTestByKey[k]
			if !already || (existing.Confidence == "indirect" && confidence == "direct") {
				topLevelTestByKey[k] = m
			}
		}

		// Source 1 — persisted edges (highest confidence).
		if directIDs := h.store.GetTestsForSymbolPersisted(targetID); len(directIDs) > 0 {
			directMap := h.store.GetSymbolsByIDs(directIDs)
			for _, ts := range directMap {
				if ts != nil && ts.RepoID == repoID {
					addTestMatch(ts.FilePath, ts.Name, "direct")
				}
			}
		}
		// Source 1b — IsTest callers (belt-and-suspenders for languages where persisted edges miss).
		for _, c := range callerMap {
			if c != nil && c.IsTest && c.RepoID == repoID {
				addTestMatch(c.FilePath, c.Name, "direct")
			}
		}
		// Source 2 — pre-resolved test-symbol set: name-overlap heuristic.
		for id, ts := range testSymsByID {
			if testMatchSeen[ts.FilePath+"|"+ts.Name] {
				continue
			}
			_ = id
			if nameReferences(ts.Name, sym.Name) {
				addTestMatch(ts.FilePath, ts.Name, "indirect")
			}
		}

		sort.Slice(testMatches, func(i, j int) bool {
			if testMatches[i].FilePath != testMatches[j].FilePath {
				return testMatches[i].FilePath < testMatches[j].FilePath
			}
			return testMatches[i].TestName < testMatches[j].TestName
		})

		// Linked requirements + per-symbol confidence (bob M5).
		//
		// ComputeImpact.AffectedLink.Confidence IS the link.Confidence from the
		// store — it is the per-link confidence already recorded at link time.
		// We use that value directly; no separate heuristic is applied.
		// Aggregation: take the max confidence across all links for this symbol.
		var symReqs []requirementSummary
		symConfidence := 0.0
		for _, link := range h.store.GetLinksForSymbol(targetID, false) {
			if link.RequirementID == "" {
				continue
			}
			// Per-symbol confidence: max of all link confidences.
			if link.Confidence > symConfidence {
				symConfidence = link.Confidence
			}
			req := h.store.GetRequirement(link.RequirementID)
			if req == nil {
				continue
			}
			// Cross-repo isolation.
			if req.RepoID != repoID {
				continue
			}
			rs := requirementSummary{
				ID:         req.ID,
				ExternalID: req.ExternalID,
				Title:      req.Title,
				Priority:   req.Priority,
				Confidence: link.Confidence,
			}
			symReqs = append(symReqs, rs)

			// Aggregate into top-level requirements (dedupe by req ID, max confidence).
			if link.Confidence > reqConfidenceByID[req.ID] {
				reqConfidenceByID[req.ID] = link.Confidence
				reqByID[req.ID] = requirementSummary{
					ID:         req.ID,
					ExternalID: req.ExternalID,
					Title:      req.Title,
					Priority:   req.Priority,
					Confidence: link.Confidence,
				}
			}
		}
		if symReqs == nil {
			symReqs = []requirementSummary{}
		}
		if testMatches == nil {
			testMatches = []changeImpactTestMatch{}
		}
		if callers == nil {
			callers = []map[string]interface{}{}
		}

		symbolResults = append(symbolResults, changeImpactSymbol{
			SymbolID:     sym.ID,
			SymbolName:   sym.Name,
			FilePath:     sym.FilePath,
			Kind:         sym.Kind,
			Callers:      callers,
			TestMatches:  testMatches,
			Requirements: symReqs,
			Confidence:   symConfidence,
		})
	}

	// Sort symbol results for deterministic output.
	sort.Slice(symbolResults, func(i, j int) bool {
		if symbolResults[i].FilePath != symbolResults[j].FilePath {
			return symbolResults[i].FilePath < symbolResults[j].FilePath
		}
		return symbolResults[i].SymbolName < symbolResults[j].SymbolName
	})

	// -----------------------------------------------------------------------
	// Step 4: Assemble top-level aggregations.
	// -----------------------------------------------------------------------

	// Affected requirements — sorted by external_id then id for determinism.
	affectedReqs := make([]requirementSummary, 0, len(reqByID))
	for _, rs := range reqByID {
		affectedReqs = append(affectedReqs, rs)
	}
	sort.Slice(affectedReqs, func(i, j int) bool {
		if affectedReqs[i].ExternalID != affectedReqs[j].ExternalID {
			return affectedReqs[i].ExternalID < affectedReqs[j].ExternalID
		}
		return affectedReqs[i].ID < affectedReqs[j].ID
	})

	// Affected tests — collect from top-level map, sorted by file+name.
	affectedTests := make([]changeImpactTestMatch, 0, len(topLevelTestByKey))
	for _, m := range topLevelTestByKey {
		affectedTests = append(affectedTests, m)
	}
	sort.Slice(affectedTests, func(i, j int) bool {
		if affectedTests[i].FilePath != affectedTests[j].FilePath {
			return affectedTests[i].FilePath < affectedTests[j].FilePath
		}
		return affectedTests[i].TestName < affectedTests[j].TestName
	})

	// Aggregate overall confidence for _meta: max of all symbol confidences.
	overallConfidence := 0.0
	for _, sr := range symbolResults {
		if sr.Confidence > overallConfidence {
			overallConfidence = sr.Confidence
		}
	}

	// Ensure slices are never JSON null.
	if symbolResults == nil {
		symbolResults = []changeImpactSymbol{}
	}
	if affectedReqs == nil {
		affectedReqs = []requirementSummary{}
	}
	if affectedTests == nil {
		affectedTests = []changeImpactTestMatch{}
	}
	if unresolved == nil {
		unresolved = []unresolvedSymbol{}
	}

	result := changeImpactResult{
		RepositoryID: repoID,
		Scope: changeImpactScope{
			CommitRange: params.CommitRange,
			Files:       scopeFiles,
			Symbols:     scopeSymbolNames,
		},
		Symbols:              symbolResults,
		AffectedRequirements: affectedReqs,
		AffectedTests:        affectedTests,
		UnresolvedSymbols:    unresolved,
		Depth:                depth,
		Meta: map[string]interface{}{
			"confidence": overallConfidence,
		},
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// registerChangeImpactTools registers the Phase 2c predict_change_impact tool
// into the handler's dispatch map. Called from newMCPHandlerWithEdition after
// registerFieldGuideTools.
func registerChangeImpactTools(h *mcpHandler) {
	h.registerTool("predict_change_impact", noCtxHandler((*mcpHandler).callPredictChangeImpact))
}
