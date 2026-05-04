// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"sort"
)

// Phase 2a (CA-154) — get_changed_symbols.
//
// Thin hydration layer over resolveDiffTouchedSymbols (Phase 2a.1 helper in
// mcp_compound.go). Given a diff scope (commit_range and/or files), returns
// every code symbol touched by the diff in two projections:
//
//   changed_files   — symbols grouped by file path (for file-centric views)
//   changed_symbols — flat deduped list (for symbol-centric tools)
//
// CRITICAL (per dexter M4): the FLAT symbol ID list (second return value of
// resolveDiffTouchedSymbols) is used for hydration — NOT diffReviewFile.Symbols
// (those are names, not IDs). The flat list goes through GetSymbolsByIDs so each
// symbol gets its full StoredSymbol record before any cross-repo guard or cap.
//
// Processing order:
//  1. Validate: at least one of commit_range / files required.
//  2. Call resolveDiffTouchedSymbols → ([]diffReviewFile, []string IDs, error).
//  3. Hydrate the flat []string via GetSymbolsByIDs → map[id]*StoredSymbol.
//  4. Cross-repo guard: filter any symbol where sym.RepoID != repoID.
//  5. Apply max_symbols cap (global) BEFORE building either projection.
//     Set truncated: true when capped.
//  6. Build both projections from the same hydrated, capped, deduped set.

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

func (h *mcpHandler) changedSymbolsToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "get_changed_symbols",
			Description: "Return the code symbols touched by a diff. " +
				"Accepts a diff scope (commit_range and/or files) and returns two projections " +
				"of the same symbol set: changed_files (symbols grouped by file path) and " +
				"changed_symbols (flat deduped list). " +
				"Requires at least one of commit_range or files. " +
				"No LLM call — all data is sourced from the indexed symbol graph. " +
				"Note: does NOT distinguish added/modified/removed (change_type deferred — symbol-level diff fingerprints are not yet stored).",
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
						"description": "Repo-relative file paths to analyse. When provided, commit_range is ignored.",
					},
					"max_symbols": map[string]interface{}{
						"type":        "integer",
						"description": "Global cap on the number of symbols returned across both projections. When the diff touches more symbols than this cap, the result is truncated and truncated: true is set in the response. Applies before either projection is built.",
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

// changedFile is a file entry in the changed_files projection.
type changedFile struct {
	FilePath string          `json:"file_path"`
	Symbols  []symbolSummary `json:"symbols"`
}

// changedSymbolsResult is the full get_changed_symbols response.
type changedSymbolsResult struct {
	RepositoryID       string                 `json:"repository_id"`
	CommitRange        string                 `json:"commit_range,omitempty"`
	Files              []string               `json:"files,omitempty"`
	ChangedFiles       []changedFile          `json:"changed_files"`
	ChangedSymbols     []symbolSummary        `json:"changed_symbols"`
	ChangedSymbolCount int                    `json:"changed_symbol_count"`
	ChangedFileCount   int                    `json:"changed_file_count"`
	Truncated          bool                   `json:"truncated"`
	Meta               map[string]interface{} `json:"_meta,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetChangedSymbols(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string   `json:"repository_id"`
		CommitRange  string   `json:"commit_range"`
		Files        []string `json:"files"`
		MaxSymbols   int      `json:"max_symbols"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	// Validate: at least one diff anchor required.
	if params.CommitRange == "" && len(params.Files) == 0 {
		return nil, errInvalidArguments("at least one of commit_range or files is required")
	}

	repoID := params.RepositoryID

	// Step 1: resolve touched symbols via the shared helper.
	//
	// The first return value (file-level summaries with symbol NAMES) is
	// intentionally discarded — we build both projections from the flat
	// symbol ID list (second return) so every entry is fully hydrated from
	// the store.
	_, touchedSymbolIDs, err := h.resolveDiffTouchedSymbols(repoID, params.CommitRange, params.Files)
	if err != nil {
		return nil, err
	}

	// Step 2: batch-hydrate the flat ID list.
	//
	// GetSymbolsByIDs returns a map[id]*StoredSymbol. IDs not found in the
	// store are absent from the map; we skip them silently.
	symMap := h.store.GetSymbolsByIDs(touchedSymbolIDs)

	// Step 3: dedupe + cross-repo guard.
	//
	// touchedSymbolIDs may contain duplicates (the same symbol referenced from
	// multiple file passes). We walk the list in order to maintain stable
	// ordering while building a deduped, in-repo slice of *StoredSymbol.
	seen := make(map[string]struct{}, len(touchedSymbolIDs))
	inRepoSymbols := make([]symbolSummary, 0, len(touchedSymbolIDs))
	for _, id := range touchedSymbolIDs {
		if _, already := seen[id]; already {
			continue
		}
		seen[id] = struct{}{}

		sym, ok := symMap[id]
		if !ok || sym == nil {
			continue
		}
		// Cross-repo isolation: skip symbols that belong to another repo.
		if sym.RepoID != repoID {
			continue
		}
		inRepoSymbols = append(inRepoSymbols, symbolSummary{
			SymbolID:   sym.ID,
			SymbolName: sym.Name,
			FilePath:   sym.FilePath,
			Kind:       sym.Kind,
			Language:   sym.Language,
		})
	}

	// Step 4: apply max_symbols global cap BEFORE building either projection.
	touchedCount := len(inRepoSymbols)
	truncated := false
	if params.MaxSymbols > 0 && len(inRepoSymbols) > params.MaxSymbols {
		inRepoSymbols = inRepoSymbols[:params.MaxSymbols]
		truncated = true
	}

	// Step 5: build both projections from the same (possibly capped) slice.
	//
	// changed_files: group by FilePath, preserving stable order (first
	// appearance of each file determines its position).
	fileOrder := make([]string, 0)
	byFile := make(map[string][]symbolSummary)
	for _, ss := range inRepoSymbols {
		if _, exists := byFile[ss.FilePath]; !exists {
			fileOrder = append(fileOrder, ss.FilePath)
		}
		byFile[ss.FilePath] = append(byFile[ss.FilePath], ss)
	}

	changedFiles := make([]changedFile, 0, len(fileOrder))
	for _, fp := range fileOrder {
		syms := byFile[fp]
		// Sort symbols within each file by name for deterministic output.
		sort.Slice(syms, func(i, j int) bool {
			return syms[i].SymbolName < syms[j].SymbolName
		})
		changedFiles = append(changedFiles, changedFile{
			FilePath: fp,
			Symbols:  syms,
		})
	}

	// changed_symbols: flat deduped list (already deduped above; sort by
	// file_path then symbol_name for determinism).
	changedSymbols := make([]symbolSummary, len(inRepoSymbols))
	copy(changedSymbols, inRepoSymbols)
	sort.Slice(changedSymbols, func(i, j int) bool {
		if changedSymbols[i].FilePath != changedSymbols[j].FilePath {
			return changedSymbols[i].FilePath < changedSymbols[j].FilePath
		}
		return changedSymbols[i].SymbolName < changedSymbols[j].SymbolName
	})

	result := changedSymbolsResult{
		RepositoryID:       repoID,
		CommitRange:        params.CommitRange,
		Files:              params.Files,
		ChangedFiles:       changedFiles,
		ChangedSymbols:     changedSymbols,
		ChangedSymbolCount: len(changedSymbols),
		ChangedFileCount:   len(changedFiles),
		Truncated:          truncated,
		Meta: map[string]interface{}{
			"touched_symbol_count": touchedCount,
			"touched_file_count":   len(fileOrder),
		},
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// changedSymbolsToolsList returns []mcpTool pairing the Phase 2a
// get_changed_symbols definition with its handler. Used by registerChangedSymbolsTools.
func (h *mcpHandler) changedSymbolsToolsList() []mcpTool {
	defs := h.changedSymbolsToolDefs()
	defByName := make(map[string]mcpToolDefinition, len(defs))
	for _, d := range defs {
		defByName[d.Name] = d
	}
	return []mcpTool{
		{Definition: defByName["get_changed_symbols"], Handler: noCtxHandler((*mcpHandler).callGetChangedSymbols)},
	}
}

// registerChangedSymbolsTools registers the Phase 2a get_changed_symbols tool
// into the handler's dispatch map. Called from newMCPHandlerWithEdition after
// registerGapAuditExtraTools.
func registerChangedSymbolsTools(h *mcpHandler) {
	for _, t := range h.changedSymbolsToolsList() {
		h.registerTool(t)
	}
}
