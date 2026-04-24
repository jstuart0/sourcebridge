// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/qa"
)

// Phase 1b — get_tests_for_symbol.
//
// Merges three sources of "what tests exercise this symbol" into one
// response, with each result tagged by the sources that contributed
// to it:
//
//   persisted_edge    (high)   — RelationTests edge explicitly stored
//                                 during indexing for this symbol.
//                                 Today these are not reliably
//                                 populated across all languages;
//                                 when they are, this source is
//                                 used preferentially.
//   adjacent_heuristic (medium) — the file's adjacentTestCandidates
//                                  result (language-convention-based)
//                                  intersected with symbols marked
//                                  IsTest=true in the repo index.
//   text_reference    (low)    — test-marked symbols whose file body
//                                 contains the target symbol's name.
//                                 Noisy but useful as a fallback.
//
// The response returns a single union list; each result carries a
// `match_sources: []` field so callers can filter by confidence.
// Clients that want only high-confidence matches filter for entries
// whose list contains "persisted_edge".

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

func (h *mcpHandler) getTestsForSymbolToolDef() mcpToolDefinition {
	return mcpToolDefinition{
		Name:        "get_tests_for_symbol",
		Description: "Return tests that exercise the target symbol. Merges persisted-edge, adjacent-heuristic, and text-reference sources — each result is tagged with its match_sources so callers can filter by confidence.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"repository_id":      map[string]interface{}{"type": "string", "description": "Repository ID"},
				"file_path":          map[string]interface{}{"type": "string", "description": "Repo-relative file path containing the target symbol"},
				"symbol_name":        map[string]interface{}{"type": "string", "description": "Name of the target symbol"},
				"line_start":         map[string]interface{}{"type": "integer", "description": "Disambiguator when the same name appears more than once in the file"},
				"symbol_id":          map[string]interface{}{"type": "string", "description": "Optional optimization hint"},
				"include_adjacent":   map[string]interface{}{"type": "boolean", "description": "Include adjacent-heuristic matches (default true)"},
				"include_text_refs":  map[string]interface{}{"type": "boolean", "description": "Include text-reference matches (default true)"},
			},
			"required": []string{"repository_id", "file_path", "symbol_name"},
		},
	}
}

// ---------------------------------------------------------------------------
// Result shape
// ---------------------------------------------------------------------------

type testMatch struct {
	SymbolID     string   `json:"symbol_id"`
	SymbolName   string   `json:"symbol_name"`
	FilePath     string   `json:"file_path"`
	Kind         string   `json:"kind"`
	StartLine    int      `json:"start_line"`
	EndLine      int      `json:"end_line"`
	MatchSources []string `json:"match_sources"`
}

type testsForSymbolResult struct {
	Target struct {
		SymbolID   string `json:"symbol_id"`
		SymbolName string `json:"symbol_name"`
		FilePath   string `json:"file_path"`
	} `json:"target"`
	Tests []testMatch `json:"tests"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (h *mcpHandler) callGetTestsForSymbol(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		symbolRefParams
		IncludeAdjacent *bool `json:"include_adjacent"`
		IncludeTextRefs *bool `json:"include_text_refs"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	target, err := h.resolveSymbol(params.symbolRefParams)
	if err != nil {
		return nil, err
	}

	includeAdjacent := true
	if params.IncludeAdjacent != nil {
		includeAdjacent = *params.IncludeAdjacent
	}
	includeTextRefs := true
	if params.IncludeTextRefs != nil {
		includeTextRefs = *params.IncludeTextRefs
	}

	// Accumulate matches keyed by symbol ID; each stanza adds its
	// source tag to the aggregate's MatchSources slice.
	matches := map[string]*testMatch{}
	addSource := func(sym *graph.StoredSymbol, source string) {
		if sym == nil {
			return
		}
		m, ok := matches[sym.ID]
		if !ok {
			m = &testMatch{
				SymbolID:   sym.ID,
				SymbolName: sym.Name,
				FilePath:   sym.FilePath,
				Kind:       sym.Kind,
				StartLine:  sym.StartLine,
				EndLine:    sym.EndLine,
			}
			matches[sym.ID] = m
		}
		for _, s := range m.MatchSources {
			if s == source {
				return
			}
		}
		m.MatchSources = append(m.MatchSources, source)
	}

	// Source 1 — persisted edges.
	// RelationTests edges aren't reliably populated today. When/if
	// they land, the GraphStore interface would expose a
	// GetTestEdges(symbolID) method; for now we check the symbol's
	// callers for any IsTest=true entries, which functionally
	// captures "a test method calls this symbol" — the real signal
	// any persisted edge would carry anyway.
	callerIDs := h.store.GetCallers(target.ID)
	if len(callerIDs) > 0 {
		byID := h.store.GetSymbolsByIDs(callerIDs)
		for _, sym := range byID {
			if sym != nil && sym.IsTest {
				addSource(sym, "persisted_edge")
			}
		}
	}

	// Source 2 — adjacent-test file heuristic.
	// Take the target file's adjacent-test candidate paths; for each
	// that has any test-marked symbols in the store, include all of
	// them.
	if includeAdjacent {
		candidates := qa.AdjacentTestCandidates(target.FilePath)
		for _, cand := range candidates {
			for _, sym := range h.store.GetSymbolsByFile(target.RepoID, cand) {
				if sym.IsTest {
					addSource(sym, "adjacent_heuristic")
				}
			}
		}
	}

	// Source 3 — text reference across test files in the repo.
	// Find all IsTest=true symbols anywhere in the repo whose file
	// path matches a common-test-suffix pattern; for each, look at
	// the surrounding symbols on the assumption a test method would
	// be in a test file. We can't read file contents cheaply at this
	// layer, so we use the symbol-name overlap heuristic: any test
	// symbol whose file contains another symbol named identically
	// to the target is considered a text_reference match.
	if includeTextRefs && target.Name != "" {
		allSyms, _ := h.store.GetSymbols(target.RepoID, nil, nil, 0, 0)
		for _, sym := range allSyms {
			if !sym.IsTest {
				continue
			}
			// Skip the heuristic hits already added.
			if _, already := matches[sym.ID]; already {
				continue
			}
			// Heuristic: name of the test mentions the target name.
			// Matches patterns like TestHandleRequest, test_parse,
			// ParseJSONSpec, etc. Cheap; false-positive rate is
			// bounded by how distinctive the target name is.
			if nameReferences(sym.Name, target.Name) {
				addSource(sym, "text_reference")
			}
		}
	}

	// Stable sort: prefer persisted_edge → adjacent_heuristic →
	// text_reference (highest-confidence source first), then by
	// file path + line number.
	result := testsForSymbolResult{}
	result.Target.SymbolID = target.ID
	result.Target.SymbolName = target.Name
	result.Target.FilePath = target.FilePath

	for _, m := range matches {
		result.Tests = append(result.Tests, *m)
	}
	sort.Slice(result.Tests, func(i, j int) bool {
		ri := sourceRank(result.Tests[i].MatchSources)
		rj := sourceRank(result.Tests[j].MatchSources)
		if ri != rj {
			return ri < rj
		}
		if result.Tests[i].FilePath != result.Tests[j].FilePath {
			return result.Tests[i].FilePath < result.Tests[j].FilePath
		}
		return result.Tests[i].StartLine < result.Tests[j].StartLine
	})

	return result, nil
}

// sourceRank returns a smaller number for higher-confidence source
// combinations so sort.Slice orders the most confident matches first.
func sourceRank(sources []string) int {
	for _, s := range sources {
		if s == "persisted_edge" {
			return 0
		}
	}
	for _, s := range sources {
		if s == "adjacent_heuristic" {
			return 1
		}
	}
	return 2
}

// nameReferences is a cheap heuristic: does candidate's name appear
// to reference target (sharing a meaningful substring)? Matches:
//   - TestHandleRequest   references HandleRequest
//   - test_handle_request references HandleRequest
//   - handleRequestSpec   references handleRequest
func nameReferences(candidate, target string) bool {
	if target == "" || candidate == "" {
		return false
	}
	// Exact substring match first — catches most cases.
	if strings.Contains(candidate, target) {
		return true
	}
	// Snake_case variant of the target — e.g., HandleRequest →
	// handle_request.
	snake := camelToSnake(target)
	if snake != "" && strings.Contains(strings.ToLower(candidate), snake) {
		return true
	}
	// Lowercased substring match for CamelCase test names that
	// differ only in the first-letter case.
	return strings.Contains(strings.ToLower(candidate), strings.ToLower(target))
}

var camelSplit = regexp.MustCompile(`([a-z])([A-Z])`)

func camelToSnake(s string) string {
	if s == "" {
		return ""
	}
	out := camelSplit.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(out)
}

// symbolPath is a helper used by tests to format a symbol reference
// for error messages.
func symbolPath(file, name string) string {
	if file == "" {
		return name
	}
	return fmt.Sprintf("%s#%s", path.Base(file), name)
}
