// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
)

// Phase 1a — requirement-linking tools.
//
// Two fast-read tools that expose the bidirectional traceability graph
// between code symbols and tracked requirements:
//
//   get_requirements_for_symbol — given a symbol, return all requirements
//     linked to it. The inverse direction is get_symbols_for_requirement.
//     Together they let an agent answer "what does this function
//     implement?" and "which functions implement this requirement?"
//     without an LLM call.
//
//   get_symbols_for_requirement — given a requirement (by UUID or
//     external ID), return all symbols linked to it.
//
// Both tools share the requirementSummary and symbolSummary structs
// defined here. Phases 2c, 2d, and 3 reuse these structs to avoid
// duplicating the traceability field set.

// ---------------------------------------------------------------------------
// Shared response sub-structs
// ---------------------------------------------------------------------------

// requirementSummary is the canonical summary shape for a requirement
// returned by traceability tools. Phases 2c/2d/3 reuse this struct.
type requirementSummary struct {
	ID         string  `json:"id"`
	ExternalID string  `json:"external_id,omitempty"`
	Title      string  `json:"title"`
	Priority   string  `json:"priority,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
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

// requirementToolDefs returns the tool definitions for the requirement-linking
// tools. Phase 1b will add the gap-audit tool defs (get_orphan_symbols,
// get_uncovered_requirements) here once that phase ships.
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
	}
}

// ---------------------------------------------------------------------------
// get_requirements_for_symbol
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetRequirementsForSymbol(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		symbolRefParams
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
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
	sym, err := h.resolveSymbol(params.symbolRefParams)
	if err != nil {
		return nil, err
	}

	// Fetch non-rejected links for this symbol and collect requirement IDs.
	links := h.store.GetLinksForSymbol(sym.ID, false /* includeRejected */)

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
	reqMap := h.store.GetRequirementsByIDs(reqIDs)

	requirements := make([]requirementSummary, 0, len(reqIDs))
	for _, id := range reqIDs {
		req, ok := reqMap[id]
		if !ok {
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

func (h *mcpHandler) callGetSymbolsForRequirement(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID  string `json:"repository_id"`
		RequirementID string `json:"requirement_id"`
		Limit         int    `json:"limit"`
		Offset        int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
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
	req := h.resolveRequirement(params.RepositoryID, params.RequirementID)
	if req == nil {
		return nil, &mcpToolError{
			Code:        MCPErrSymbolNotFound,
			Message:     fmt.Sprintf("Requirement %q not found in repository %s.", params.RequirementID, params.RepositoryID),
			Remediation: "Verify the requirement_id; call get_requirements to list available requirements and their IDs.",
		}
	}

	// Fetch non-rejected links for this requirement and collect symbol IDs.
	links := h.store.GetLinksForRequirement(req.ID, false /* includeRejected */)

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
	allSymMap := h.store.GetSymbolsByIDs(allSymIDs)
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
// resolveRequirement — shared helper for requirement-linking tools
// ---------------------------------------------------------------------------

// resolveRequirement looks up a requirement by UUID or external ID within
// the given repository. Returns nil if not found. Accepts:
//   - A bare UUID (checked via GetRequirement, then validated against repoID)
//   - An external ID string (e.g. "PROJ-101") via GetRequirementByExternalID
func (h *mcpHandler) resolveRequirement(repoID, requirementID string) *graphstore.StoredRequirement {
	// Try UUID lookup first.
	if req := h.store.GetRequirement(requirementID); req != nil {
		// Cross-repo isolation: the UUID must belong to this repository.
		if req.RepoID == repoID {
			return req
		}
		// Don't fall through — if the UUID matched another repo, external-ID
		// resolution for the same string would be meaningless.
		return nil
	}
	// Fall back to external ID lookup (scoped to the repository).
	return h.store.GetRequirementByExternalID(repoID, requirementID)
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// registerRequirementLinkingTools registers the Phase 1a requirement-linking
// tools into the handler's dispatch map. Called from newMCPHandlerWithEdition
// after registerCoreTools.
func registerRequirementLinkingTools(h *mcpHandler) {
	h.registerTool("get_requirements_for_symbol", noCtxHandler((*mcpHandler).callGetRequirementsForSymbol))
	h.registerTool("get_symbols_for_requirement", noCtxHandler((*mcpHandler).callGetSymbolsForRequirement))
}
