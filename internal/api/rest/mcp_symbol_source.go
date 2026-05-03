// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/source"
)

// mcpSymbolLargeLineThreshold is the symbol line-count above which a
// source_note warning is appended to the get_symbol_source response.
// Decision D7: 500 lines.
const mcpSymbolLargeLineThreshold = 500

// mcpSymbolContextLinesCap is the silent upper clamp for context_lines
// (Decision D7 — matches max_hops/limit precedent).
const mcpSymbolContextLinesCap = 10

// ---------------------------------------------------------------------------
// Result shape
// ---------------------------------------------------------------------------

type getSymbolSourceResult struct {
	SymbolID      string `json:"symbol_id"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	Language      string `json:"language"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Signature     string `json:"signature,omitempty"`
	Source        string `json:"source"`
	SourceNote    string `json:"source_note,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetSymbolSource(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		symbolRefParams
		ContextLines int `json:"context_lines"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	// Silent-clamp context_lines to [0, mcpSymbolContextLinesCap].
	contextLines := params.ContextLines
	if contextLines < 0 {
		contextLines = 0
	}
	if contextLines > mcpSymbolContextLinesCap {
		contextLines = mcpSymbolContextLinesCap
	}

	result, _, err := h.buildSymbolSource(session, params.symbolRefParams, contextLines)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// buildSymbolSource is the shared helper for get_symbol_source and
// get_symbol_context (Phase 3). It executes steps 4–9 from the plan:
// repo-access check, symbol resolution, file read, line slicing, and
// result assembly. The context_lines validation (step 3) stays in each
// caller so each tool can document its own clamping behaviour.
//
// Returns the result struct, the resolved StoredSymbol (for Phase 3's
// bundled caller/callee assembly), and any error.
func (h *mcpHandler) buildSymbolSource(session *mcpSession, params symbolRefParams, contextLines int) (getSymbolSourceResult, *graphstore.StoredSymbol, error) {
	// Step 4 — defense-in-depth repo access guard.
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return getSymbolSourceResult{}, nil, err
	}

	// Step 5 — resolve the symbol.
	sym, err := h.resolveSymbol(params)
	if err != nil {
		return getSymbolSourceResult{}, nil, err
	}

	// Step 6 — nil-check fileReader; absent means the server isn't
	// configured with a repo clone path, which we surface as stale.
	if h.fileReader == nil {
		return getSymbolSourceResult{}, nil, errRepositoryStale(sym.FilePath)
	}

	// Step 7 — read the file; map any read error to stale.
	content, err := h.fileReader.ReadRepoFile(params.RepositoryID, sym.FilePath)
	if err != nil {
		return getSymbolSourceResult{}, nil, errRepositoryStale(sym.FilePath)
	}

	// Step 8 — slice lines with context buffer; source.SliceLines clamps
	// at file boundaries automatically.
	sliceStart := sym.StartLine - contextLines
	if sliceStart < 1 {
		sliceStart = 1
	}
	sliceEnd := sym.EndLine + contextLines
	src := source.SliceLines(content, sliceStart, sliceEnd)

	if src == "" {
		return getSymbolSourceResult{}, nil, errRepositoryStale(sym.FilePath)
	}

	// Step 9 — build response.
	result := getSymbolSourceResult{
		SymbolID:      sym.ID,
		FilePath:      sym.FilePath,
		StartLine:     sym.StartLine,
		EndLine:       sym.EndLine,
		Language:      sym.Language,
		Name:          sym.Name,
		QualifiedName: sym.QualifiedName,
		Kind:          sym.Kind,
		Signature:     sym.Signature,
		Source:        src,
	}

	// Append source_note for unusually large symbols.
	if (sym.EndLine - sym.StartLine + 1) > mcpSymbolLargeLineThreshold {
		result.SourceNote = fmt.Sprintf(
			"Symbol %q is %d lines long. Consider narrowing via search_symbols or get_callers to find a specific sub-section.",
			sym.Name, sym.EndLine-sym.StartLine+1,
		)
	}

	return result, sym, nil
}

// symbolSourceToolDefs returns the tool definitions for get_symbol_source and
// get_symbol_context. They are registered here so the capability registry drift
// test (TestRegistry_AllMCPToolsExistInBaseTools) passes from Phase 1 onward.
// Handler logic is wired in Phase 2 (get_symbol_source) and Phase 3
// (get_symbol_context).
func (h *mcpHandler) symbolSourceToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "get_symbol_source",
			Description: "Return a symbol's source bytes plus identity (name, qualified_name, kind, " +
				"signature, language) and 1-based inclusive line range. " +
				"Provide either `symbol_id` (fast path) or the `(file_path, symbol_name)` pair, " +
				"optionally disambiguated with `line_start`. " +
				"`context_lines` (0–10, silently clamped) returns surrounding lines. " +
				"Symbols longer than 500 lines include a `source_note` warning string.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"file_path":     map[string]interface{}{"type": "string", "description": "Repo-relative file path containing the target symbol (required unless symbol_id is provided)"},
					"symbol_name":   map[string]interface{}{"type": "string", "description": "Name of the target symbol (required unless symbol_id is provided)"},
					"line_start":    map[string]interface{}{"type": "integer", "description": "Optional disambiguator when the same name appears multiple times in the file"},
					"symbol_id":     map[string]interface{}{"type": "string", "description": "Optional fast-path identifier — bypasses (file_path, symbol_name) resolution"},
					"context_lines": map[string]interface{}{"type": "integer", "description": "Lines of surrounding context to prepend/append (default 0; values > 10 are silently clamped to 10)"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name: "get_symbol_context",
			Description: "Return a single-call bundle of a symbol's source plus first-hop callers, callees, " +
				"and file imports. Eliminates the 2-round-trip tax of search_symbols → file read. " +
				"Provide either `symbol_id` or `(file_path, symbol_name)`. " +
				"When `call_graph` or `file_imports` capabilities are disabled, the bundle degrades " +
				"gracefully: the `symbol` and source are always returned; missing sections are signalled " +
				"by `degraded: true` and `degraded_reason`.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"file_path":     map[string]interface{}{"type": "string", "description": "Repo-relative file path containing the target symbol (required unless symbol_id is provided)"},
					"symbol_name":   map[string]interface{}{"type": "string", "description": "Name of the target symbol (required unless symbol_id is provided)"},
					"line_start":    map[string]interface{}{"type": "integer", "description": "Optional disambiguator when the same name appears multiple times in the file"},
					"symbol_id":     map[string]interface{}{"type": "string", "description": "Optional fast-path identifier — bypasses (file_path, symbol_name) resolution"},
					"context_lines": map[string]interface{}{"type": "integer", "description": "Lines of surrounding context to prepend/append (default 0; values > 10 are silently clamped to 10)"},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}
