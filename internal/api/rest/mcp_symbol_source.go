// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/capabilities"
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
			Description: "Single-call bundle of a symbol's source plus first-hop callers, callees " +
				"(capped at 20 each), and the file's imports (capped at 50). " +
				"Eliminates the 2x token tax of search_symbols → file read. " +
				"Provide either `symbol_id` (fast path) or the `(file_path, symbol_name)` pair. " +
				"When the operator has disabled `call_graph` or `file_imports`, the corresponding " +
				"arrays are empty and the response carries `degraded: true` with `degraded_reason` " +
				"explaining which capability is off — check this field before treating empty arrays " +
				"as \"no callers exist.\" Only depth=1 is supported in this release; " +
				"`depth > 1` returns INVALID_ARGUMENTS.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"file_path":     map[string]interface{}{"type": "string", "description": "Repo-relative file path containing the target symbol (required unless symbol_id is provided)"},
					"symbol_name":   map[string]interface{}{"type": "string", "description": "Name of the target symbol (required unless symbol_id is provided)"},
					"line_start":    map[string]interface{}{"type": "integer", "description": "Optional disambiguator when the same name appears multiple times in the file"},
					"symbol_id":     map[string]interface{}{"type": "string", "description": "Optional fast-path identifier — bypasses (file_path, symbol_name) resolution"},
					"depth":         map[string]interface{}{"type": "integer", "description": "Walk depth (default 1; only depth=1 is supported in this release)"},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Phase 3 — get_symbol_context: bundled symbol + callers + callees + imports
// ---------------------------------------------------------------------------

// Truncation caps for the bundled response (D4 / plan Phase 3.3).
const (
	mcpContextCallersTruncLimit = 20
	mcpContextCalleesTruncLimit = 20
	mcpContextImportsTruncLimit = 50
)

// getSymbolContextResult is the wire shape returned by get_symbol_context.
// The Symbol payload is always present when no error is returned. When a
// contributing capability (call_graph, file_imports) is disabled, the
// corresponding array is empty and Degraded is true. DegradedReason is
// always set when Degraded is true; it is omitted (omitempty) when Degraded
// is false.
type getSymbolContextResult struct {
	Symbol  getSymbolSourceResult `json:"symbol"`
	Callers []callGraphSymbol     `json:"callers"`
	Callees []callGraphSymbol     `json:"callees"`
	Imports []fileImport          `json:"imports"`
	Depth   int                   `json:"depth"`
	// Truncated is true when any list was cut to its cap.
	Truncated bool `json:"truncated"`
	// Degraded is true when call_graph and/or file_imports is disabled.
	Degraded bool `json:"degraded"`
	// DegradedReason is always set when Degraded is true; omitted otherwise.
	DegradedReason string `json:"degraded_reason,omitempty"`
}

func (h *mcpHandler) callGetSymbolContext(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		symbolRefParams
		Depth int `json:"depth"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	// Normalise depth: zero means "use default" (1).
	depth := params.Depth
	if depth == 0 {
		depth = 1
	}
	if depth > 1 {
		return nil, errInvalidArguments("depth > 1 is not yet supported (CA-151 ships depth=1 only)")
	}

	// Resolve symbol + read source. context_lines=0 keeps the bundle compact;
	// callers/callees already provide the surrounding context.
	symbolResult, sym, err := h.buildSymbolSource(session, params.symbolRefParams, 0)
	if err != nil {
		return nil, err
	}

	// Capability checks (Decision D4) via the injected checker so tests can
	// flip capabilities without mutating package globals.
	checker := h.capabilityChecker
	if checker == nil {
		// Production default; should always be wired by router.go.
		checker = capabilities.IsAvailable
	}
	callGraphAvailable := checker("call_graph", h.edition)
	fileImportsAvailable := checker("file_imports", h.edition)

	// Build degraded_reason (D4).
	var degraded bool
	var degradedReasons []string
	if !callGraphAvailable {
		degraded = true
		degradedReasons = append(degradedReasons, "call_graph capability disabled for this edition")
	}
	if !fileImportsAvailable {
		degraded = true
		degradedReasons = append(degradedReasons, "file_imports capability disabled for this edition")
	}
	degradedReason := strings.Join(degradedReasons, "; ")
	if !degraded {
		degradedReason = ""
	}

	var truncated bool

	// --- Callers ---
	callers := make([]callGraphSymbol, 0)
	if callGraphAvailable {
		callerIDs := h.store.GetCallers(sym.ID)
		if len(callerIDs) > mcpContextCallersTruncLimit {
			callerIDs = callerIDs[:mcpContextCallersTruncLimit]
			truncated = true
		}
		if len(callerIDs) > 0 {
			symMap := h.store.GetSymbolsByIDs(callerIDs)
			for _, id := range callerIDs {
				s := symMap[id]
				if s == nil {
					// Caller was indexed but the symbol was removed in a
					// partial re-index. Skip silently — don't fail the bundle.
					continue
				}
				callers = append(callers, callGraphSymbol{
					SymbolID:     s.ID,
					FilePath:     s.FilePath,
					SymbolName:   s.Name,
					Kind:         s.Kind,
					StartLine:    s.StartLine,
					EndLine:      s.EndLine,
					IsTest:       s.IsTest,
					HopsFromRoot: 1,
				})
			}
		}
	}

	// --- Callees ---
	callees := make([]callGraphSymbol, 0)
	if callGraphAvailable {
		calleeIDs := h.store.GetCallees(sym.ID)
		if len(calleeIDs) > mcpContextCalleesTruncLimit {
			calleeIDs = calleeIDs[:mcpContextCalleesTruncLimit]
			truncated = true
		}
		if len(calleeIDs) > 0 {
			symMap := h.store.GetSymbolsByIDs(calleeIDs)
			for _, id := range calleeIDs {
				s := symMap[id]
				if s == nil {
					// Callee was indexed but later removed. Skip silently.
					continue
				}
				callees = append(callees, callGraphSymbol{
					SymbolID:     s.ID,
					FilePath:     s.FilePath,
					SymbolName:   s.Name,
					Kind:         s.Kind,
					StartLine:    s.StartLine,
					EndLine:      s.EndLine,
					IsTest:       s.IsTest,
					HopsFromRoot: 1,
				})
			}
		}
	}

	// --- Imports ---
	imports := make([]fileImport, 0)
	if fileImportsAvailable {
		allImports := h.store.GetImports(params.RepositoryID)

		// Build a file-ID → file-path map (same pattern as callGetFileImports).
		// O(repo-files) per call; acceptable for depth=1 — see plan Phase 3.3.
		files := h.store.GetFiles(params.RepositoryID)
		fileIDToPath := make(map[string]string, len(files))
		pathToFileID := make(map[string]string, len(files))
		for _, f := range files {
			fileIDToPath[f.ID] = f.Path
			pathToFileID[f.Path] = f.ID
		}

		targetFileID := pathToFileID[sym.FilePath]

		// Collect all matching imports first, then cap.
		var matchingImports []fileImport
		for _, imp := range allImports {
			if imp.FileID != targetFileID {
				continue
			}
			matchingImports = append(matchingImports, fileImport{
				Path:  imp.Path,
				Line:  imp.Line,
				Depth: 1,
			})
		}
		if len(matchingImports) > mcpContextImportsTruncLimit {
			matchingImports = matchingImports[:mcpContextImportsTruncLimit]
			truncated = true
		}
		imports = matchingImports
	}

	return getSymbolContextResult{
		Symbol:         symbolResult,
		Callers:        callers,
		Callees:        callees,
		Imports:        imports,
		Depth:          depth,
		Truncated:      truncated,
		Degraded:       degraded,
		DegradedReason: degradedReason,
	}, nil
}
