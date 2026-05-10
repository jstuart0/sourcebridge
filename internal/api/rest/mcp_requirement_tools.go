// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// Phase 1a — requirement-linking tools.
// Phase 1b — gap-audit tools.
//
// Two fast-read tools that expose the bidirectional traceability graph
// between code symbols and tracked requirements:
//
//   get_requirements_for_symbol — given a symbol, return all requirements
//     linked to it. The inverse direction is get_symbols_for_requirement.
//     Together they let an agent answer "what does this function
//     implements?" and "which functions implement this requirement?"
//     without an LLM call.
//
//   get_symbols_for_requirement — given a requirement (by UUID or
//     external ID), return all symbols linked to it.
//
// Two O(n) gap-audit tools (Phase 1b):
//
//   get_orphan_symbols — repo-wide scan returning symbols with no linked
//     requirement. Cursor-paginated over the post-filter result set.
//
//   get_uncovered_requirements — repo-wide scan returning requirements
//     with no linked symbol. Cursor-paginated. Hard-caps the scan at
//     maxUncoveredReqScan rows; sets scan_truncated when hit.
//
// All four tools share the requirementSummary and symbolSummary structs
// defined here. Phases 2c, 2d, and 3 reuse these structs to avoid
// duplicating the traceability field set.

// maxUncoveredReqScan is the maximum number of requirements loaded from the
// store in a single get_uncovered_requirements scan. Repos with more
// requirements than this threshold still return partial results, but
// scan_truncated is set to true in the response so callers know the result
// set is incomplete.
//
// Named constant per dexter L1 — never use the raw number inline.
const maxUncoveredReqScan = 10000

// ---------------------------------------------------------------------------
// Shared response sub-structs
// ---------------------------------------------------------------------------

// requirementSummary is the canonical summary shape for a requirement
// returned by traceability tools. Phases 2c/2d/3 reuse this struct.
//
// LinkedToSymbols is populated only by get_changed_requirements (Phase 2d) —
// it lists the symbol IDs from the diff that triggered this requirement's
// inclusion. Other tools leave it nil so the field is omitted from JSON.
type requirementSummary struct {
	ID              string   `json:"id"`
	ExternalID      string   `json:"external_id,omitempty"`
	Title           string   `json:"title"`
	Priority        string   `json:"priority,omitempty"`
	Confidence      float64  `json:"confidence,omitempty"`
	LinkedToSymbols []string `json:"linked_to_symbols,omitempty"`
}

// symbolSummary is the canonical summary shape for a symbol returned by
// traceability tools. Phases 2c/2d/3 reuse this struct.
type symbolSummary struct {
	SymbolID   string  `json:"symbol_id"`
	SymbolName string  `json:"symbol_name"`
	FilePath   string  `json:"file_path,omitempty"`
	Kind       string  `json:"kind,omitempty"`
	Language   string  `json:"language,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

// requirementToolDefs returns the tool definitions for the Phase 1a
// requirement-linking tools, plus the Phase 2d get_changed_requirements tool.
func (h *mcpHandler) requirementToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "get_requirements_for_symbol",
			Description: "Return requirements linked to a code symbol (forward traceability: code → spec). " +
				"Data is sourced from persisted requirement-symbol links — no LLM call. " +
				"Supports optional limit/offset for pagination when a symbol has many linked requirements. " +
				"Use get_symbols_for_requirement for the inverse direction (spec → code). " +
				"Supersedes part of review_diff_against_requirements for symbol-level traceability.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"symbol_id": map[string]interface{}{
						"type":        "string",
						"description": "Symbol UUID. Provide symbol_id OR (file_path + symbol_name); symbol_id is the faster path when available.",
					},
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Repo-relative file path containing the symbol (used when symbol_id is not known)",
					},
					"symbol_name": map[string]interface{}{
						"type":        "string",
						"description": "Symbol name (used together with file_path when symbol_id is not known)",
					},
					"line_start": map[string]interface{}{
						"type":        "integer",
						"description": "Optional line disambiguator when the same symbol name appears multiple times in the file",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Max requirements to return (default 20, max 200)",
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"description": "Offset for pagination (default 0)",
					},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name: "get_symbols_for_requirement",
			Description: "Return code symbols linked to a requirement (inverse traceability: spec → code). " +
				"Accepts a requirement by UUID or external ID (e.g. \"PROJ-101\"). " +
				"Data is sourced from persisted requirement-symbol links — no LLM call. " +
				"Use get_requirements_for_symbol for the inverse direction (code → spec).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"requirement_id": map[string]interface{}{
						"type":        "string",
						"description": "Requirement UUID or external ID (e.g. \"PROJ-101\")",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Max symbols to return (default 20, max 200)",
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"description": "Offset for pagination (default 0)",
					},
				},
				"required": []string{"repository_id", "requirement_id"},
			},
		},
		{
			Name: "get_changed_requirements",
			Description: "Return the requirements affected by a code change. " +
				"Given a diff scope (commit_range and/or files), resolves touched symbols " +
				"and returns every requirement linked to those symbols — with each requirement " +
				"annotated by the symbol IDs that triggered its inclusion (linked_to_symbols). " +
				"Symbols that touch no requirement are returned separately as unlinked_touched_symbols " +
				"so callers can identify coverage gaps in the diff. " +
				"Requires at least one of commit_range or files. " +
				"No LLM call — all data is sourced from persisted requirement-symbol links.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"commit_range": map[string]interface{}{
						"type":        "string",
						"description": "Git commit range (e.g. \"HEAD~3..HEAD\"). Used to discover touched files when files is absent.",
					},
					"files": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Repo-relative file paths to analyse. Overrides commit_range when provided.",
					},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// get_requirements_for_symbol
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetRequirementsForSymbol(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		symbolRefParams
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(ctx, session, params.RepositoryID); err != nil {
		return nil, err
	}

	// Normalize pagination params.
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	// Resolve the symbol via the shared helper (symbol_id fast path, then
	// file_path+symbol_name resolution with optional line_start disambiguator).
	sym, err := h.resolveSymbol(ctx, params.symbolRefParams)
	if err != nil {
		return nil, err
	}

	// Fetch non-rejected links for this symbol and collect requirement IDs.
	links := h.store.GetLinksForSymbol(ctx, sym.ID, false /* includeRejected */)

	// Build a confidence map keyed by requirement ID so we can annotate
	// each requirement with the link's confidence score.
	confidenceByReqID := make(map[string]float64, len(links))
	reqIDs := make([]string, 0, len(links))
	for _, l := range links {
		if _, seen := confidenceByReqID[l.RequirementID]; !seen {
			reqIDs = append(reqIDs, l.RequirementID)
		}
		// Take the max confidence when multiple links exist for the same pair.
		if l.Confidence > confidenceByReqID[l.RequirementID] {
			confidenceByReqID[l.RequirementID] = l.Confidence
		}
	}

	totalCount := len(reqIDs)

	// Apply offset + limit before hydration to avoid fetching more than needed.
	if offset >= len(reqIDs) {
		reqIDs = nil
	} else {
		reqIDs = reqIDs[offset:]
		if len(reqIDs) > limit {
			reqIDs = reqIDs[:limit]
		}
	}

	// Batch-hydrate requirements.
	reqMap := h.store.GetRequirementsByIDs(ctx, reqIDs)

	requirements := make([]requirementSummary, 0, len(reqIDs))
	for _, id := range reqIDs {
		req, ok := reqMap[id]
		if !ok {
			continue
		}
		// Cross-repo isolation: only surface requirements that belong to the
		// requested repository. A link row may point to a requirement stored
		// under a different repo (cross-repo mis-data); skip those to prevent
		// leaking other repos' requirement titles and external IDs.
		if req.RepoID != params.RepositoryID {
			continue
		}
		requirements = append(requirements, requirementSummary{
			ID:         req.ID,
			ExternalID: req.ExternalID,
			Title:      req.Title,
			Priority:   req.Priority,
			Confidence: confidenceByReqID[id],
		})
	}

	return map[string]interface{}{
		"symbol_id":    sym.ID,
		"requirements": requirements,
		"total_count":  totalCount,
	}, nil
}

// ---------------------------------------------------------------------------
// get_symbols_for_requirement
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetSymbolsForRequirement(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID  string `json:"repository_id"`
		RequirementID string `json:"requirement_id"`
		Limit         int    `json:"limit"`
		Offset        int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(ctx, session, params.RepositoryID); err != nil {
		return nil, err
	}

	if params.RequirementID == "" {
		return nil, errInvalidArguments("requirement_id is required")
	}

	// Normalize pagination params.
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	// Resolve the requirement — accept either a UUID or an external ID.
	req := h.resolveRequirement(ctx, params.RepositoryID, params.RequirementID)
	if req == nil {
		return nil, &mcpToolError{
			Code:        MCPErrSymbolNotFound,
			Message:     fmt.Sprintf("Requirement %q not found in repository %s.", params.RequirementID, params.RepositoryID),
			Remediation: "Verify the requirement_id; call get_requirements to list available requirements and their IDs.",
		}
	}

	// Fetch non-rejected links for this requirement and collect symbol IDs.
	links := h.store.GetLinksForRequirement(ctx, req.ID, false /* includeRejected */)

	// Build a confidence map keyed by symbol ID.
	confidenceBySymID := make(map[string]float64, len(links))
	allSymIDs := make([]string, 0, len(links))
	for _, l := range links {
		if _, seen := confidenceBySymID[l.SymbolID]; !seen {
			allSymIDs = append(allSymIDs, l.SymbolID)
		}
		if l.Confidence > confidenceBySymID[l.SymbolID] {
			confidenceBySymID[l.SymbolID] = l.Confidence
		}
	}

	// Cross-repo isolation: batch-hydrate all candidates and filter to
	// symbols that belong to the requested repository. total_count reflects
	// only the in-repo symbols so callers can paginate correctly.
	allSymMap := h.store.GetSymbolsByIDs(ctx, allSymIDs)
	inRepoSymIDs := make([]string, 0, len(allSymIDs))
	for _, id := range allSymIDs {
		if sym, ok := allSymMap[id]; ok && sym.RepoID == params.RepositoryID {
			inRepoSymIDs = append(inRepoSymIDs, id)
		}
	}

	totalCount := len(inRepoSymIDs)

	// Apply offset + limit to the filtered (in-repo) list.
	symIDsPage := inRepoSymIDs
	if offset >= len(symIDsPage) {
		symIDsPage = nil
	} else {
		symIDsPage = symIDsPage[offset:]
		if len(symIDsPage) > limit {
			symIDsPage = symIDsPage[:limit]
		}
	}

	symbols := make([]symbolSummary, 0, len(symIDsPage))
	for _, id := range symIDsPage {
		sym := allSymMap[id] // already hydrated and repo-validated above
		symbols = append(symbols, symbolSummary{
			SymbolID:   sym.ID,
			SymbolName: sym.Name,
			FilePath:   sym.FilePath,
			Kind:       sym.Kind,
			Language:   sym.Language,
			Confidence: confidenceBySymID[id],
		})
	}

	return map[string]interface{}{
		"requirement": requirementSummary{
			ID:         req.ID,
			ExternalID: req.ExternalID,
			Title:      req.Title,
			Priority:   req.Priority,
		},
		"symbols":     symbols,
		"total_count": totalCount,
	}, nil
}

// ---------------------------------------------------------------------------
// get_changed_requirements (Phase 2d)
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetChangedRequirements(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string   `json:"repository_id"`
		CommitRange  string   `json:"commit_range"`
		Files        []string `json:"files"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(ctx, session, params.RepositoryID); err != nil {
		return nil, err
	}

	// Require at least one diff anchor.
	if params.CommitRange == "" && len(params.Files) == 0 {
		return nil, errInvalidArguments("at least one of commit_range or files is required")
	}

	repoID := params.RepositoryID

	// Step 1: resolve touched symbols via the shared helper (Phase 2a.1).
	// The first return value (file-level summaries) is discarded — we only
	// need the flat symbol ID list for the requirement-linking pass.
	_, touchedSymbolIDs, err := h.resolveDiffTouchedSymbols(ctx, repoID, params.CommitRange, params.Files)
	if err != nil {
		return nil, err
	}

	// Step 2: for each touched symbol, gather requirement links.
	//
	// reqSymbols maps requirement ID → ordered list of symbol IDs that link to it.
	// The ordering follows the order symbols appear in touchedSymbolIDs so the
	// output is deterministic across calls with the same input.
	reqSymbols := map[string][]string{} // req ID → []sym ID (preserves insertion order)
	reqOrder := []string{}              // stable ordering of first-seen req IDs
	reqSeen := map[string]bool{}
	var unlinkedSymIDs []string

	for _, symID := range touchedSymbolIDs {
		links := h.store.GetLinksForSymbol(ctx, symID, false /* includeRejected */)
		if len(links) == 0 {
			unlinkedSymIDs = append(unlinkedSymIDs, symID)
			continue
		}
		linkedAny := false
		for _, l := range links {
			if l.RequirementID == "" {
				continue
			}
			linkedAny = true
			if !reqSeen[l.RequirementID] {
				reqSeen[l.RequirementID] = true
				reqOrder = append(reqOrder, l.RequirementID)
			}
			// Record this symbol as a trigger for the requirement (dedupe within the list).
			alreadyRecorded := false
			for _, existing := range reqSymbols[l.RequirementID] {
				if existing == symID {
					alreadyRecorded = true
					break
				}
			}
			if !alreadyRecorded {
				reqSymbols[l.RequirementID] = append(reqSymbols[l.RequirementID], symID)
			}
		}
		if !linkedAny {
			unlinkedSymIDs = append(unlinkedSymIDs, symID)
		}
	}

	// Step 3: hydrate requirements (deduped via reqOrder).
	reqMap := h.store.GetRequirementsByIDs(ctx, reqOrder)

	changedReqs := make([]requirementSummary, 0, len(reqOrder))
	for _, reqID := range reqOrder {
		req, ok := reqMap[reqID]
		if !ok {
			continue
		}
		// Cross-repo isolation.
		if req.RepoID != repoID {
			continue
		}
		changedReqs = append(changedReqs, requirementSummary{
			ID:              req.ID,
			ExternalID:      req.ExternalID,
			Title:           req.Title,
			Priority:        req.Priority,
			LinkedToSymbols: reqSymbols[reqID],
		})
	}

	// Step 4: hydrate unlinked symbols.
	unlinkedSummaries := make([]symbolSummary, 0, len(unlinkedSymIDs))
	if len(unlinkedSymIDs) > 0 {
		symMap := h.store.GetSymbolsByIDs(ctx, unlinkedSymIDs)
		for _, symID := range unlinkedSymIDs {
			sym, ok := symMap[symID]
			if !ok {
				continue
			}
			// Cross-repo isolation: only surface symbols belonging to this repo.
			if sym.RepoID != repoID {
				continue
			}
			unlinkedSummaries = append(unlinkedSummaries, symbolSummary{
				SymbolID:   sym.ID,
				SymbolName: sym.Name,
				FilePath:   sym.FilePath,
				Kind:       sym.Kind,
			})
		}
	}

	// Ensure slices are never JSON null.
	if changedReqs == nil {
		changedReqs = []requirementSummary{}
	}
	if unlinkedSummaries == nil {
		unlinkedSummaries = []symbolSummary{}
	}

	return map[string]interface{}{
		"repository_id":            repoID,
		"commit_range":             params.CommitRange,
		"files":                    params.Files,
		"changed_requirements":     changedReqs,
		"unlinked_touched_symbols": unlinkedSummaries,
		"_meta": map[string]interface{}{
			"touched_symbol_count":      len(touchedSymbolIDs),
			"changed_requirement_count": len(changedReqs),
			"unlinked_symbol_count":     len(unlinkedSummaries),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// resolveRequirement — shared helper for requirement-linking tools
// ---------------------------------------------------------------------------

// resolveRequirement looks up a requirement by UUID or external ID within
// the given repository. Returns nil if not found. Accepts:
//   - A bare UUID (checked via GetRequirement, then validated against repoID)
//   - An external ID string (e.g. "PROJ-101") via GetRequirementByExternalID
func (h *mcpHandler) resolveRequirement(ctx context.Context, repoID, requirementID string) *graphstore.StoredRequirement {
	// Try UUID lookup first.
	if req := h.store.GetRequirement(ctx, requirementID); req != nil {
		// Cross-repo isolation: the UUID must belong to this repository.
		if req.RepoID == repoID {
			return req
		}
		// Don't fall through — if the UUID matched another repo, external-ID
		// resolution for the same string would be meaningless.
		return nil
	}
	// Fall back to external ID lookup (scoped to the repository).
	return h.store.GetRequirementByExternalID(ctx, repoID, requirementID)
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// requirementLinkingTools returns []mcpTool pairing the Phase 1a
// requirement-linking and Phase 2d get_changed_requirements definitions with
// their handlers. Used by registerRequirementLinkingTools.
func (h *mcpHandler) requirementLinkingTools() []mcpTool {
	defs := h.requirementToolDefs()
	defByName := make(map[string]mcpToolDefinition, len(defs))
	for _, d := range defs {
		defByName[d.Name] = d
	}
	return []mcpTool{
		{Definition: defByName["get_requirements_for_symbol"], Handler: withCtxHandler((*mcpHandler).callGetRequirementsForSymbol)},
		{Definition: defByName["get_symbols_for_requirement"], Handler: withCtxHandler((*mcpHandler).callGetSymbolsForRequirement)},
		{Definition: defByName["get_changed_requirements"], Handler: withCtxHandler((*mcpHandler).callGetChangedRequirements)},
	}
}

// registerRequirementLinkingTools registers the Phase 1a requirement-linking
// tools and the Phase 2d get_changed_requirements tool into the handler's
// dispatch map. Called from newMCPHandlerWithEdition after registerCoreTools.
func registerRequirementLinkingTools(h *mcpHandler) {
	for _, t := range h.requirementLinkingTools() {
		h.registerTool(t)
	}
}

// ---------------------------------------------------------------------------
// Phase 1b — gap-audit tools
// ---------------------------------------------------------------------------

// gapAuditToolDefs returns the tool definitions for the Phase 1b gap-audit
// tools. Called from baseTools() alongside requirementToolDefs.
func (h *mcpHandler) gapAuditToolDefs() []mcpToolDefinition {
	paginationProps := paginationToolProps(50, 200)
	return []mcpToolDefinition{
		{
			Name: "get_orphan_symbols",
			Description: "Returns symbols with no linked requirements (coverage gap: code not traced to any spec). " +
				"Each page performs a full repo scan — the filtering step cannot be pushed into the store index. " +
				"Use `limit` to set a single-page budget. " +
				"For repos with >5K symbols, consider requesting a large single page rather than paginating, " +
				"to avoid paying the scan cost repeatedly.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": mergeMaps(map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
				}, paginationProps),
				"required": []string{"repository_id"},
			},
		},
		{
			Name: "get_uncovered_requirements",
			Description: "Returns requirements with no linked symbol (coverage gap: spec not traced to any code). " +
				"Each page performs a full repo scan (capped at " + fmt.Sprintf("%d", maxUncoveredReqScan) + " requirements). " +
				"If the repo has more requirements than the scan cap, `scan_truncated` is set to true. " +
				"For repos with >5K requirements, consider requesting a large single page rather than paginating, " +
				"to avoid paying the scan cost repeatedly.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": mergeMaps(map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
				}, paginationProps),
				"required": []string{"repository_id"},
			},
		},
	}
}

// mergeMaps returns a shallow merge of two map[string]interface{} values.
// Keys in b overwrite keys in a. Used to compose JSON schema fragments.
func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// get_orphan_symbols
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetOrphanSymbols(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		paginationArgs
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(ctx, session, params.RepositoryID); err != nil {
		return nil, err
	}

	offset, err := decodeCursor(params.Cursor)
	if err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	// Full repo scan — limit=0, offset=0 fetches all symbols. The second
	// return value (total before pagination) is discarded because we need the
	// post-filter count, not the raw symbol count.
	allSyms, _ := h.store.GetSymbols(ctx, params.RepositoryID, nil, nil, 0, 0)

	// Filter to symbols with no non-rejected links.
	orphans := make([]*graphstore.StoredSymbol, 0)
	for _, sym := range allSyms {
		links := h.store.GetLinksForSymbol(ctx, sym.ID, false /* includeRejected */)
		if len(links) == 0 {
			orphans = append(orphans, sym)
		}
	}

	// Cursor-paginate the post-filter result. Silent clamp to cap=200.
	page, nextCursor, total := paginateSlice(orphans, offset, params.Limit, 50, 200)

	summaries := make([]symbolSummary, 0, len(page))
	for _, sym := range page {
		summaries = append(summaries, symbolSummary{
			SymbolID:   sym.ID,
			SymbolName: sym.Name,
			FilePath:   sym.FilePath,
			Kind:       sym.Kind,
			Language:   sym.Language,
		})
	}

	return map[string]interface{}{
		"orphan_symbols": summaries,
		"total_count":    total,
		"next_cursor":    nullableString(nextCursor),
	}, nil
}

// ---------------------------------------------------------------------------
// get_uncovered_requirements
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetUncoveredRequirements(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		paginationArgs
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(ctx, session, params.RepositoryID); err != nil {
		return nil, err
	}

	offset, err := decodeCursor(params.Cursor)
	if err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	// Fetch up to maxUncoveredReqScan requirements. The second return value
	// is the total number of requirements in the repo (before our limit),
	// which lets us detect truncation without a separate COUNT query.
	reqs, totalInStore := h.store.GetRequirements(ctx, params.RepositoryID, maxUncoveredReqScan, 0)
	scanTruncated := totalInStore > maxUncoveredReqScan

	// Filter to requirements with no non-rejected links.
	uncovered := make([]*graphstore.StoredRequirement, 0)
	for _, req := range reqs {
		links := h.store.GetLinksForRequirement(ctx, req.ID, false /* includeRejected */)
		if len(links) == 0 {
			uncovered = append(uncovered, req)
		}
	}

	// Cursor-paginate the post-filter result. Silent clamp to cap=200.
	page, nextCursor, total := paginateSlice(uncovered, offset, params.Limit, 50, 200)

	summaries := make([]requirementSummary, 0, len(page))
	for _, req := range page {
		summaries = append(summaries, requirementSummary{
			ID:         req.ID,
			ExternalID: req.ExternalID,
			Title:      req.Title,
			Priority:   req.Priority,
		})
	}

	return map[string]interface{}{
		"uncovered_requirements": summaries,
		"total_count":            total,
		"next_cursor":            nullableString(nextCursor),
		"scan_truncated":         scanTruncated,
	}, nil
}

// nullableString returns nil for an empty string so the JSON output is null
// rather than "" for next_cursor when there is no next page.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ---------------------------------------------------------------------------
// registerGapAuditTools
// ---------------------------------------------------------------------------

// gapAuditTools returns []mcpTool pairing the Phase 1b gap-audit definitions
// with their handlers. Used by registerGapAuditTools.
func (h *mcpHandler) gapAuditTools() []mcpTool {
	defs := h.gapAuditToolDefs()
	defByName := make(map[string]mcpToolDefinition, len(defs))
	for _, d := range defs {
		defByName[d.Name] = d
	}
	return []mcpTool{
		{Definition: defByName["get_orphan_symbols"], Handler: withCtxHandler((*mcpHandler).callGetOrphanSymbols)},
		{Definition: defByName["get_uncovered_requirements"], Handler: withCtxHandler((*mcpHandler).callGetUncoveredRequirements)},
	}
}

// registerGapAuditTools registers the Phase 1b gap-audit tools into the
// handler's dispatch map. Called from newMCPHandlerWithEdition after
// registerRequirementLinkingTools.
func registerGapAuditTools(h *mcpHandler) {
	for _, t := range h.gapAuditTools() {
		h.registerTool(t)
	}
}
