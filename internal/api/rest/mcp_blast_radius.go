// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"math"
	"sort"
)

// Phase 3 (CA-154) — get_blast_radius.
//
// Multi-hop blast radius for a single symbol. Performs BFS over the caller
// graph up to `depth` hops (default 3, max 5), groups results by depth, and
// attaches a weighted risk score.
//
// Key design decisions:
//
//  1. repoSymbolSet built ONCE (per bob C2): cross-repo isolation happens at
//     frontier expansion so large cross-repo subtrees never pollute the
//     500-node cap.
//
//  2. First-discovery wins (per bob H1): a node that is reachable via multiple
//     paths is placed at the shallowest depth. `visited` records the hop count
//     on first visit.
//
//  3. Cap check at hop boundary, AFTER full frontier expansion (per bob H3):
//     the cap is checked once per hop, after all callers for the current
//     frontier have been walked. This ensures no shallow node is evicted by
//     cap firing mid-hop.
//
//  4. Root excluded from impact_by_depth (per bob M4): visited[root.ID] = 0
//     and hop-0 nodes are skipped during hydration.
//
//  5. testSymsByID pre-resolved ONCE (per dexter H1): GetSymbols is called
//     exactly once per invocation, outside the BFS loop.
//
//  6. Risk score: per-layer depth_risk_score = (1/depth^0.7 × caller_count) /
//     total_affected_count. overall_risk_score = weighted sum / total_affected.
//     Formula documented in-code (per bob M1 — field was declared but never
//     assigned in early pseudocode).
//
//  7. aggregateTestsForLayer / aggregateRequirementsForLayer are private helpers
//     in this file, NOT in mcp_change_impact.go (per bob r2 advisory). The
//     includeNameHeuristic flag is hardcoded false (per bob r2 — was a
//     copy-paste artifact in the original plan; name-heuristic is only
//     meaningful for predict_change_impact's per-target test matching).

// ---------------------------------------------------------------------------
// File-level types
// ---------------------------------------------------------------------------

// brTestSymInfo is the per-test-symbol info pre-resolved by callGetBlastRadius
// and passed into aggregateTestsForLayer. Kept separate from the similarly
// named function-local type in mcp_change_impact.go to avoid shadowing.
type brTestSymInfo struct {
	FilePath string
	Name     string
}

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

func (h *mcpHandler) blastRadiusToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "get_blast_radius",
			Description: "Multi-hop blast radius for a symbol: BFS over the caller graph up to `depth` hops (default 3, max 5). " +
				"Returns a depth-grouped view of all callers, a per-depth risk score, and an overall weighted risk score. " +
				"Optionally aggregates test matches and linked requirements per depth layer. " +
				"Complements predict_change_impact (which is diff-anchored, depth=1); use get_blast_radius when " +
				"you want to understand the full multi-hop impact of changing a single well-known symbol.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"symbol_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional fast-path symbol ID. When provided and valid, skips file_path+symbol_name resolution.",
					},
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Repo-relative file path containing the target symbol.",
					},
					"symbol_name": map[string]interface{}{
						"type":        "string",
						"description": "Name of the target symbol.",
					},
					"line_start": map[string]interface{}{
						"type":        "integer",
						"description": "Optional disambiguator when the same symbol name appears multiple times in the file.",
					},
					"depth": map[string]interface{}{
						"type":        "integer",
						"description": "BFS depth (default 3, max 5). depth=1 returns only direct callers.",
					},
					"include_tests": map[string]interface{}{
						"type":        "boolean",
						"description": "Aggregate test matches per depth layer (default true).",
					},
					"include_requirements": map[string]interface{}{
						"type":        "boolean",
						"description": "Aggregate linked requirements per depth layer (default true).",
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

// callGraphSymbolBR is the per-symbol entry inside a blast-radius depth layer.
type callGraphSymbolBR struct {
	SymbolID   string `json:"symbol_id"`
	SymbolName string `json:"symbol_name"`
	FilePath   string `json:"file_path"`
	Kind       string `json:"kind"`
}

// blastRadiusTestMatch mirrors changeImpactTestMatch but lives here because
// the aggregation helpers for get_blast_radius are private to this file.
type blastRadiusTestMatch struct {
	FilePath   string `json:"file_path"`
	TestName   string `json:"test_name"`
	Confidence string `json:"confidence"`
}

// blastRadiusDepthLayer is one depth slice in impact_by_depth.
type blastRadiusDepthLayer struct {
	Depth          int                    `json:"depth"`
	Callers        []callGraphSymbolBR    `json:"callers"`
	CallerCount    int                    `json:"caller_count"`
	DepthRiskScore float64                `json:"depth_risk_score"`
	TestMatches    []blastRadiusTestMatch `json:"test_matches,omitempty"`
	Requirements   []requirementSummary   `json:"requirements,omitempty"`
}

// blastRadiusResult is the full get_blast_radius response.
type blastRadiusResult struct {
	RepositoryID      string                  `json:"repository_id"`
	SymbolID          string                  `json:"symbol_id"`
	SymbolName        string                  `json:"symbol_name"`
	FilePath          string                  `json:"file_path"`
	Depth             int                     `json:"depth"`
	OverallRiskScore  float64                 `json:"overall_risk_score"`
	DirectCallerCount int                     `json:"direct_caller_count"`
	TotalAffectedCount int                    `json:"total_affected_count"`
	Truncated         bool                    `json:"truncated"`
	ImpactByDepth     []blastRadiusDepthLayer `json:"impact_by_depth"`
	Meta              map[string]interface{}  `json:"_meta,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetBlastRadius(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		SymbolID     string `json:"symbol_id"`
		FilePath     string `json:"file_path"`
		SymbolName   string `json:"symbol_name"`
		LineStart    int    `json:"line_start"`
		Depth        int    `json:"depth"`
		// Default true; explicit false opts out.
		IncludeTests        *bool `json:"include_tests"`
		IncludeRequirements *bool `json:"include_requirements"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	// Validate depth.
	depth := params.Depth
	if depth == 0 {
		depth = 3
	}
	if depth > 5 {
		return nil, errInvalidArguments("depth must be between 1 and 5 (inclusive)")
	}

	// Default include_tests / include_requirements to true when not supplied.
	includeTests := true
	if params.IncludeTests != nil {
		includeTests = *params.IncludeTests
	}
	includeRequirements := true
	if params.IncludeRequirements != nil {
		includeRequirements = *params.IncludeRequirements
	}

	repoID := params.RepositoryID

	// Verify repo exists.
	if h.store.GetRepository(repoID) == nil {
		return nil, errRepositoryNotIndexed(repoID)
	}

	// -----------------------------------------------------------------------
	// Resolve root symbol.
	// -----------------------------------------------------------------------
	root, err := h.resolveSymbol(symbolRefParams{
		RepositoryID: repoID,
		SymbolID:     params.SymbolID,
		FilePath:     params.FilePath,
		SymbolName:   params.SymbolName,
		LineStart:    params.LineStart,
	})
	if err != nil {
		return nil, err
	}
	// Cross-repo guard.
	if root.RepoID != repoID {
		return nil, errInvalidArguments("symbol does not belong to the requested repository")
	}

	// -----------------------------------------------------------------------
	// Pre-build repoSymbolSet ONCE (per bob C2).
	//
	// Cross-repo isolation happens during BFS frontier expansion: any caller
	// ID not in repoSymbolSet is dropped before it enters `visited` or `next`.
	// This prevents large cross-repo subtrees from eating into the 500-node
	// cap and ensures in-repo descendants reachable only through cross-repo
	// intermediaries are NOT visited.
	// -----------------------------------------------------------------------
	allSyms, _ := h.store.GetSymbols(repoID, nil, nil, 0, 0)
	repoSymbolSet := make(map[string]bool, len(allSyms))
	for _, sym := range allSyms {
		repoSymbolSet[sym.ID] = true
	}

	// -----------------------------------------------------------------------
	// Pre-build testSymsByID ONCE (per dexter H1).
	//
	// Avoids O(n×k) GetSymbols calls inside per-symbol loops.
	// -----------------------------------------------------------------------
	testSymsByID := make(map[string]brTestSymInfo, len(allSyms))
	for _, sym := range allSyms {
		if sym.IsTest {
			testSymsByID[sym.ID] = brTestSymInfo{FilePath: sym.FilePath, Name: sym.Name}
		}
	}

	// -----------------------------------------------------------------------
	// BFS — first-discovery wins.
	//
	// visited[id] = hop records the depth at which a node was first seen.
	// hop 0 = root (excluded from output per bob M4).
	//
	// Cap check happens at hop boundary AFTER full frontier expansion (per
	// bob H3): the cap is NOT applied inside the inner caller loop, so all
	// callers of the current hop's frontier are always fully walked before
	// the total is checked.
	// -----------------------------------------------------------------------
	const maxAffected = 500

	visited := make(map[string]int, 64)
	visited[root.ID] = 0
	frontier := []string{root.ID}
	truncated := false

	for hop := 1; hop <= depth; hop++ {
		next := []string{}
		for _, id := range frontier {
			callers := h.store.GetCallers(id)
			for _, nid := range callers {
				// Cross-repo isolation at expansion time (per bob C2).
				if !repoSymbolSet[nid] {
					continue
				}
				// First-discovery wins: skip if already in visited (bob H1).
				if _, seen := visited[nid]; seen {
					continue
				}
				visited[nid] = hop
				next = append(next, nid)
			}
		}
		frontier = next

		// Cap check at hop boundary, AFTER frontier fully expanded (per bob H3).
		if len(visited) > maxAffected {
			truncated = true
			break
		}
	}

	// -----------------------------------------------------------------------
	// Hydration + projection — group by depth (root excluded).
	// -----------------------------------------------------------------------
	byDepth := make(map[int][]callGraphSymbolBR)
	for id, hop := range visited {
		if hop == 0 {
			continue // exclude root from impact_by_depth (per bob M4)
		}
		sym := h.store.GetSymbol(id)
		if sym == nil {
			continue
		}
		byDepth[hop] = append(byDepth[hop], callGraphSymbolBR{
			SymbolID:   sym.ID,
			SymbolName: sym.Name,
			FilePath:   sym.FilePath,
			Kind:       sym.Kind,
		})
	}

	// Sort callers within each layer for deterministic output.
	for d := range byDepth {
		sort.Slice(byDepth[d], func(i, j int) bool {
			if byDepth[d][i].FilePath != byDepth[d][j].FilePath {
				return byDepth[d][i].FilePath < byDepth[d][j].FilePath
			}
			return byDepth[d][i].SymbolName < byDepth[d][j].SymbolName
		})
	}

	// -----------------------------------------------------------------------
	// Assemble layers + compute risk score (per bob M1).
	//
	// Risk formula:
	//   raw_weight_d  = (1 / d^0.7) × caller_count_at_d
	//   weighted_sum  = Σ raw_weight_d
	//   overall_risk  = weighted_sum / total_affected_count
	//   depth_risk_score_d = raw_weight_d / total_affected_count
	//
	// Both values are in [0, ∞) before normalization; after normalization
	// overall_risk = Σ depth_risk_score. A deeper graph with many callers
	// per layer yields a higher score.
	// -----------------------------------------------------------------------
	totalAffected := len(visited) - 1 // subtract root (hop=0)

	var layers []blastRadiusDepthLayer
	rawWeights := make([]float64, depth+1) // index = depth

	for d := 1; d <= depth; d++ {
		callers := byDepth[d]
		if callers == nil {
			callers = []callGraphSymbolBR{}
		}
		raw := (1.0 / math.Pow(float64(d), 0.7)) * float64(len(callers))
		rawWeights[d] = raw
		layers = append(layers, blastRadiusDepthLayer{
			Depth:       d,
			Callers:     callers,
			CallerCount: len(callers),
		})
	}

	overallRisk := 0.0
	if totalAffected > 0 {
		weightedSum := 0.0
		for d := 1; d <= depth; d++ {
			weightedSum += rawWeights[d]
		}
		overallRisk = weightedSum / float64(totalAffected)
		for i := range layers {
			layers[i].DepthRiskScore = rawWeights[layers[i].Depth] / float64(totalAffected)
		}
	}

	// -----------------------------------------------------------------------
	// Aggregate tests + requirements per layer (per bob r2 advisory).
	//
	// Private helpers below; includeNameHeuristic is hardcoded false.
	// -----------------------------------------------------------------------
	for i := range layers {
		layer := &layers[i]
		if includeTests {
			layer.TestMatches = h.aggregateTestsForLayer(layer.Callers, testSymsByID)
		}
		if includeRequirements {
			layer.Requirements = h.aggregateRequirementsForLayer(layer.Callers, repoID)
		}
	}

	// -----------------------------------------------------------------------
	// Build _meta.
	// -----------------------------------------------------------------------
	meta := map[string]interface{}{
		"confidence": 0.95,
	}
	if totalAffected == 0 {
		meta["note"] = "no callers in graph"
	}

	directCallerCount := 0
	if len(layers) > 0 {
		directCallerCount = layers[0].CallerCount
	}

	result := blastRadiusResult{
		RepositoryID:       repoID,
		SymbolID:           root.ID,
		SymbolName:         root.Name,
		FilePath:           root.FilePath,
		Depth:              depth,
		OverallRiskScore:   overallRisk,
		DirectCallerCount:  directCallerCount,
		TotalAffectedCount: totalAffected,
		Truncated:          truncated,
		ImpactByDepth:      layers,
		Meta:               meta,
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Private aggregation helpers (per bob r2 advisory — live here, NOT in
// mcp_change_impact.go; includeNameHeuristic = false, hardcoded).
// ---------------------------------------------------------------------------

// aggregateTestsForLayer collects test matches for all callers in a depth
// layer. Returns a deduped, sorted slice.
//
// Sources:
//   - Persisted edges via GetTestsForSymbolPersisted → "direct" confidence.
//   - IsTest callers already in the layer → "direct" confidence.
//
// Name-heuristic (nameReferences) is deliberately excluded (includeNameHeuristic
// = false hardcoded; see bob r2 advisory).
func (h *mcpHandler) aggregateTestsForLayer(callers []callGraphSymbolBR, testSymsByID map[string]brTestSymInfo) []blastRadiusTestMatch {
	type testKey struct{ filePath, testName string }
	seen := map[testKey]string{} // key → best confidence

	for _, caller := range callers {
		// Source 1: persisted edges.
		for _, tsID := range h.store.GetTestsForSymbolPersisted(caller.SymbolID) {
			ts := h.store.GetSymbol(tsID)
			if ts == nil {
				continue
			}
			k := testKey{ts.FilePath, ts.Name}
			if _, already := seen[k]; !already {
				seen[k] = "direct"
			}
		}
		// Source 2: IsTest callers already in the layer.
		if _, isTest := testSymsByID[caller.SymbolID]; isTest {
			k := testKey{caller.FilePath, caller.SymbolName}
			if _, already := seen[k]; !already {
				seen[k] = "direct"
			}
		}
	}

	matches := make([]blastRadiusTestMatch, 0, len(seen))
	for k, conf := range seen {
		matches = append(matches, blastRadiusTestMatch{
			FilePath:   k.filePath,
			TestName:   k.testName,
			Confidence: conf,
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].FilePath != matches[j].FilePath {
			return matches[i].FilePath < matches[j].FilePath
		}
		return matches[i].TestName < matches[j].TestName
	})
	return matches
}

// aggregateRequirementsForLayer collects linked requirements for all callers
// in a depth layer. Returns a deduped (max-confidence), sorted slice.
func (h *mcpHandler) aggregateRequirementsForLayer(callers []callGraphSymbolBR, repoID string) []requirementSummary {
	reqConfByID := map[string]float64{}
	reqByID := map[string]requirementSummary{}

	for _, caller := range callers {
		for _, link := range h.store.GetLinksForSymbol(caller.SymbolID, false) {
			if link.RequirementID == "" {
				continue
			}
			req := h.store.GetRequirement(link.RequirementID)
			if req == nil || req.RepoID != repoID {
				continue
			}
			if link.Confidence > reqConfByID[req.ID] {
				reqConfByID[req.ID] = link.Confidence
				reqByID[req.ID] = requirementSummary{
					ID:         req.ID,
					ExternalID: req.ExternalID,
					Title:      req.Title,
					Priority:   req.Priority,
					Confidence: link.Confidence,
				}
			}
		}
	}

	reqs := make([]requirementSummary, 0, len(reqByID))
	for _, rs := range reqByID {
		reqs = append(reqs, rs)
	}
	sort.Slice(reqs, func(i, j int) bool {
		if reqs[i].ExternalID != reqs[j].ExternalID {
			return reqs[i].ExternalID < reqs[j].ExternalID
		}
		return reqs[i].ID < reqs[j].ID
	})
	return reqs
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// registerBlastRadiusTools registers the Phase 3 get_blast_radius tool into
// the handler's dispatch map. Called from newMCPHandlerWithEdition after
// registerDependenciesTools.
func registerBlastRadiusTools(h *mcpHandler) {
	h.registerTool("get_blast_radius", noCtxHandler((*mcpHandler).callGetBlastRadius))
}
