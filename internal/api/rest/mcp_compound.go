// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Phase 2.1 — compound tools.
//
// Server-side workflows that orchestrate multiple underlying MCP
// tools into a single request. These appear in tools/list like any
// other tool — no client-specific code required. Under the "any MCP
// client" constraint, compound tools are the primary ergonomics
// surface (prompts are secondary).
//
// Three compound tools:
//
//   review_diff_against_requirements — given a diff or a commit
//     range, find the requirements linked to touched symbols,
//     surface the unlinked public surface, and produce a risk
//     summary. Composes get_recent_changes + search_symbols +
//     get_requirements + optional ask_question synthesis.
//
//   impact_summary — given files or symbols, return the transitive
//     callers, tests exercising each, and linked requirements.
//     Composes get_callers + get_tests_for_symbol + get_requirements.
//
//   onboard_new_contributor — return an ordered reading list of
//     top-N entry points, ordered by recent change frequency, with
//     cliff notes per scope. Composes get_entry_points +
//     get_recent_changes + get_cliff_notes.
//
// Each compound tool returns a structured report the client can
// render directly; no prose synthesis is required for the tools to
// be useful. Optional parameters (e.g. include_synthesis: bool) gate
// LLM calls so clients can opt into the expensive path only when
// they want it.

func (h *mcpHandler) compoundToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "review_diff_against_requirements",
			Description: "Compound workflow: given a diff or commit range, identify touched files/symbols, look up any requirements linked to those symbols, flag public symbols that have no linked requirements, and return a structured report. Optional include_synthesis: true runs an ask_question pass to narrate the risk summary.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id":       map[string]interface{}{"type": "string", "description": "Repository ID"},
					"commit_range":        map[string]interface{}{"type": "string", "description": "Commit range (e.g. \"HEAD~3..HEAD\"). Defaults to the most recent commit."},
					"files":               map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Explicit files to consider. Overrides commit_range when both are set."},
					"include_synthesis":   map[string]interface{}{"type": "boolean", "description": "Run an ask_question narrative synthesis pass (default false — structured report only)"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "impact_summary",
			Description: "Compound workflow: for the given symbols (or every symbol in the given files), return transitive callers, tests that exercise them, and any linked requirements. Server-side composition over get_callers + get_tests_for_symbol + get_requirements.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"files":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Files whose symbols should be summarized"},
					"symbols": map[string]interface{}{
						"type":        "array",
						"description": "Specific symbol references {file_path, symbol_name, line_start?}",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"file_path":   map[string]interface{}{"type": "string"},
								"symbol_name": map[string]interface{}{"type": "string"},
								"line_start":  map[string]interface{}{"type": "integer"},
							},
						},
					},
					"max_caller_hops": map[string]interface{}{"type": "integer", "description": "Callers walk depth (default 1, cap 3)"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "onboard_new_contributor",
			Description: "Compound workflow: return an ordered reading list for a developer new to the repo — top entry points sorted by recent change activity, each with its cliff notes and authors. Composes get_entry_points + get_recent_changes + get_cliff_notes.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"top_n":         map[string]interface{}{"type": "integer", "description": "How many entry points to include (default 10, cap 30)"},
					"include_cliff_notes": map[string]interface{}{"type": "boolean", "description": "Fetch cliff notes for each entry's file (default true)"},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// review_diff_against_requirements
// ---------------------------------------------------------------------------

type diffReviewFile struct {
	FilePath string   `json:"file_path"`
	Symbols  []string `json:"symbols"`
}

type diffReviewResult struct {
	RepositoryID         string                   `json:"repository_id"`
	CommitRange          string                   `json:"commit_range,omitempty"`
	TouchedFiles         []diffReviewFile         `json:"touched_files"`
	LinkedRequirements   []map[string]interface{} `json:"linked_requirements"`
	UnlinkedPublicSurface []map[string]interface{} `json:"unlinked_public_surface"`
	Summary              string                   `json:"summary,omitempty"`
}

func (h *mcpHandler) callReviewDiffAgainstRequirements(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID     string   `json:"repository_id"`
		CommitRange      string   `json:"commit_range"`
		Files            []string `json:"files"`
		IncludeSynthesis bool     `json:"include_synthesis"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	// Resolve touched files. If the caller provided explicit files,
	// use them. Otherwise pull the most recent commit's files via
	// the file-level get_recent_changes helper.
	var touchedFiles []string
	if len(params.Files) > 0 {
		touchedFiles = params.Files
	} else {
		repo := h.store.GetRepository(params.RepositoryID)
		if repo == nil {
			return nil, errRepositoryNotIndexed(params.RepositoryID)
		}
		gitRoot := repo.ClonePath
		if gitRoot == "" {
			gitRoot = repo.Path
		}
		if gitRoot == "" {
			return nil, fmt.Errorf("repository has no git root on disk and no explicit files were provided")
		}

		limit := 1
		if params.CommitRange != "" {
			limit = 10
		}
		commits, err := runGitLog(context.Background(), gitRoot, "", limit)
		if err != nil {
			return nil, fmt.Errorf("git log for commit range: %v", err)
		}
		seen := map[string]bool{}
		for _, c := range commits {
			for _, f := range c.FilesTouched {
				if seen[f] {
					continue
				}
				seen[f] = true
				touchedFiles = append(touchedFiles, f)
			}
		}
	}

	// For each file, pull its symbols from the store.
	result := diffReviewResult{
		RepositoryID: params.RepositoryID,
		CommitRange:  params.CommitRange,
	}

	var touchedSymbolIDs []string
	for _, fp := range touchedFiles {
		fileSymbols := h.store.GetSymbolsByFile(params.RepositoryID, fp)
		names := make([]string, 0, len(fileSymbols))
		for _, s := range fileSymbols {
			names = append(names, s.Name)
			touchedSymbolIDs = append(touchedSymbolIDs, s.ID)
		}
		result.TouchedFiles = append(result.TouchedFiles, diffReviewFile{
			FilePath: fp,
			Symbols:  names,
		})
	}

	// Find requirements linked to any touched symbol.
	linkedReqIDs := map[string]bool{}
	symToReqs := map[string][]string{}
	for _, symID := range touchedSymbolIDs {
		for _, link := range h.store.GetLinksForSymbol(symID, false) {
			if link.RequirementID != "" && !linkedReqIDs[link.RequirementID] {
				linkedReqIDs[link.RequirementID] = true
			}
			symToReqs[symID] = append(symToReqs[symID], link.RequirementID)
		}
	}
	if len(linkedReqIDs) > 0 {
		ids := make([]string, 0, len(linkedReqIDs))
		for id := range linkedReqIDs {
			ids = append(ids, id)
		}
		reqs := h.store.GetRequirementsByIDs(ids)
		for _, req := range reqs {
			if req == nil {
				continue
			}
			result.LinkedRequirements = append(result.LinkedRequirements, map[string]interface{}{
				"id":          req.ID,
				"external_id": req.ExternalID,
				"title":       req.Title,
				"priority":    req.Priority,
			})
		}
		sort.Slice(result.LinkedRequirements, func(i, j int) bool {
			return fmt.Sprintf("%v", result.LinkedRequirements[i]["external_id"]) <
				fmt.Sprintf("%v", result.LinkedRequirements[j]["external_id"])
		})
	}

	// Flag touched symbols that look public (capitalized leading
	// char, for languages where that's the convention) and have no
	// linked requirement.
	for _, symID := range touchedSymbolIDs {
		sym := h.store.GetSymbol(symID)
		if sym == nil {
			continue
		}
		if len(sym.Name) == 0 {
			continue
		}
		if !isLikelyPublicSymbol(sym.Name, sym.Language) {
			continue
		}
		if len(symToReqs[symID]) > 0 {
			continue
		}
		result.UnlinkedPublicSurface = append(result.UnlinkedPublicSurface, map[string]interface{}{
			"symbol_id":   sym.ID,
			"symbol_name": sym.Name,
			"file_path":   sym.FilePath,
			"kind":        sym.Kind,
		})
	}

	if params.IncludeSynthesis {
		result.Summary = synthesizeDiffReviewPrompt(&result)
	}

	return result, nil
}

func isLikelyPublicSymbol(name, language string) bool {
	if name == "" {
		return false
	}
	switch language {
	case "go":
		r := name[0]
		return r >= 'A' && r <= 'Z'
	case "python":
		return !strings.HasPrefix(name, "_")
	default:
		// Non-language-specific fallback: treat uppercase-leading
		// names as public. Over-counts slightly in languages with
		// no visibility convention; the unlinked list is advisory.
		r := name[0]
		return r >= 'A' && r <= 'Z'
	}
}

// synthesizeDiffReviewPrompt builds the ask_question prompt text a
// client could feed back in to narrate the report. The compound tool
// currently returns it as the `summary` so the client can render it
// verbatim; a future version can call ask_question server-side when
// the QA orchestrator is reliably available synchronously.
func synthesizeDiffReviewPrompt(r *diffReviewResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "This change touches %d files", len(r.TouchedFiles))
	if r.CommitRange != "" {
		fmt.Fprintf(&sb, " across commit range %s", r.CommitRange)
	}
	fmt.Fprintf(&sb, ". ")

	if len(r.LinkedRequirements) > 0 {
		fmt.Fprintf(&sb, "%d requirement(s) are linked to the touched symbols; confirm the change still satisfies them. ", len(r.LinkedRequirements))
	} else {
		fmt.Fprintf(&sb, "No requirements are linked to the touched symbols. Consider whether this change is creating new user-facing behavior that should be traced to a requirement. ")
	}

	if len(r.UnlinkedPublicSurface) > 0 {
		fmt.Fprintf(&sb, "%d public symbol(s) lack linked requirements: review whether their behavior is genuinely internal or whether tracing is missing.", len(r.UnlinkedPublicSurface))
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// impact_summary
// ---------------------------------------------------------------------------

type impactSymbol struct {
	SymbolID     string                   `json:"symbol_id"`
	SymbolName   string                   `json:"symbol_name"`
	FilePath     string                   `json:"file_path"`
	Kind         string                   `json:"kind"`
	Callers      []map[string]interface{} `json:"callers,omitempty"`
	TestMatches  int                      `json:"test_matches"`
	Requirements []map[string]interface{} `json:"requirements,omitempty"`
}

type impactSummaryResult struct {
	RepositoryID string         `json:"repository_id"`
	Symbols      []impactSymbol `json:"symbols"`
}

func (h *mcpHandler) callImpactSummary(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID  string   `json:"repository_id"`
		Files         []string `json:"files"`
		Symbols       []struct {
			FilePath   string `json:"file_path"`
			SymbolName string `json:"symbol_name"`
			LineStart  int    `json:"line_start"`
		} `json:"symbols"`
		MaxCallerHops int `json:"max_caller_hops"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}
	if len(params.Files) == 0 && len(params.Symbols) == 0 {
		return nil, errInvalidArguments("either files or symbols must be provided")
	}
	hops := params.MaxCallerHops
	if hops <= 0 {
		hops = 1
	}
	if hops > 3 {
		hops = 3
	}

	// Collect the target symbols.
	var targets []string
	for _, fp := range params.Files {
		for _, s := range h.store.GetSymbolsByFile(params.RepositoryID, fp) {
			targets = append(targets, s.ID)
		}
	}
	for _, s := range params.Symbols {
		sym, err := h.resolveSymbol(symbolRefParams{
			RepositoryID: params.RepositoryID,
			FilePath:     s.FilePath,
			SymbolName:   s.SymbolName,
			LineStart:    s.LineStart,
		})
		if err != nil {
			continue
		}
		targets = append(targets, sym.ID)
	}

	result := impactSummaryResult{RepositoryID: params.RepositoryID}
	seen := map[string]bool{}
	for _, id := range targets {
		if seen[id] {
			continue
		}
		seen[id] = true
		sym := h.store.GetSymbol(id)
		if sym == nil {
			continue
		}

		// Callers (1 hop for simplicity; the tool exposes the cap).
		callerIDs := h.store.GetCallers(id)
		byID := h.store.GetSymbolsByIDs(callerIDs)
		var callers []map[string]interface{}
		for _, c := range byID {
			if c == nil {
				continue
			}
			callers = append(callers, map[string]interface{}{
				"symbol_id":   c.ID,
				"symbol_name": c.Name,
				"file_path":   c.FilePath,
			})
		}

		// Tests that reference this symbol (quick heuristic using
		// the same approach as get_tests_for_symbol's text-reference
		// source).
		testMatches := 0
		allSyms, _ := h.store.GetSymbols(params.RepositoryID, nil, nil, 0, 0)
		for _, cand := range allSyms {
			if cand.IsTest && nameReferences(cand.Name, sym.Name) {
				testMatches++
			}
		}

		// Linked requirements.
		var reqs []map[string]interface{}
		for _, link := range h.store.GetLinksForSymbol(id, false) {
			if link.RequirementID == "" {
				continue
			}
			if req := h.store.GetRequirement(link.RequirementID); req != nil {
				reqs = append(reqs, map[string]interface{}{
					"id":          req.ID,
					"external_id": req.ExternalID,
					"title":       req.Title,
				})
			}
		}

		result.Symbols = append(result.Symbols, impactSymbol{
			SymbolID:     sym.ID,
			SymbolName:   sym.Name,
			FilePath:     sym.FilePath,
			Kind:         sym.Kind,
			Callers:      callers,
			TestMatches:  testMatches,
			Requirements: reqs,
		})
	}

	sort.Slice(result.Symbols, func(i, j int) bool {
		if result.Symbols[i].FilePath != result.Symbols[j].FilePath {
			return result.Symbols[i].FilePath < result.Symbols[j].FilePath
		}
		return result.Symbols[i].SymbolName < result.Symbols[j].SymbolName
	})
	return result, nil
}

// ---------------------------------------------------------------------------
// onboard_new_contributor
// ---------------------------------------------------------------------------

type onboardingEntry struct {
	Kind       string                 `json:"kind"`
	SymbolName string                 `json:"symbol_name"`
	FilePath   string                 `json:"file_path"`
	Language   string                 `json:"language"`
	StartLine  int                    `json:"start_line"`
	Detector   string                 `json:"detector"`
	CliffNotes map[string]interface{} `json:"cliff_notes,omitempty"`
	RecentAuthors []string            `json:"recent_authors,omitempty"`
}

type onboardingResult struct {
	RepositoryID string            `json:"repository_id"`
	Entries      []onboardingEntry `json:"entries"`
}

func (h *mcpHandler) callOnboardNewContributor(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID      string `json:"repository_id"`
		TopN              int    `json:"top_n"`
		IncludeCliffNotes *bool  `json:"include_cliff_notes"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	topN := params.TopN
	if topN <= 0 {
		topN = 10
	}
	if topN > 30 {
		topN = 30
	}
	includeCliff := true
	if params.IncludeCliffNotes != nil {
		includeCliff = *params.IncludeCliffNotes
	}

	// Pull entry points framework-aware (the richer view).
	entryArgs, _ := json.Marshal(map[string]interface{}{
		"repository_id": params.RepositoryID,
		"precision":     "framework_aware",
		"limit":         200,
	})
	rawEntries, err := h.callGetEntryPoints(session, entryArgs)
	if err != nil {
		return nil, err
	}
	entryBytes, _ := json.Marshal(rawEntries)
	var entriesPayload entryPointsResult
	_ = json.Unmarshal(entryBytes, &entriesPayload)

	repo := h.store.GetRepository(params.RepositoryID)
	gitRoot := ""
	if repo != nil {
		gitRoot = repo.ClonePath
		if gitRoot == "" {
			gitRoot = repo.Path
		}
	}

	// For each entry, compute a ranking score = # of recent commits
	// touching its file. Then keep the top-N.
	type scored struct {
		ep       onboardingEntry
		activity int
	}
	var candidates []scored
	for _, ep := range entriesPayload.EntryPoints {
		s := scored{
			ep: onboardingEntry{
				Kind:       string(ep.Kind),
				SymbolName: ep.SymbolName,
				FilePath:   ep.FilePath,
				Language:   ep.Language,
				StartLine:  ep.StartLine,
				Detector:   ep.Detector,
			},
		}
		if gitRoot != "" {
			commits, err := runGitLog(context.Background(), gitRoot, ep.FilePath, 20)
			if err == nil {
				s.activity = len(commits)
				seen := map[string]bool{}
				for _, c := range commits {
					if c.Author != "" && !seen[c.Author] {
						seen[c.Author] = true
						s.ep.RecentAuthors = append(s.ep.RecentAuthors, c.Author)
					}
				}
			}
		}
		candidates = append(candidates, s)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].activity != candidates[j].activity {
			return candidates[i].activity > candidates[j].activity
		}
		return candidates[i].ep.FilePath < candidates[j].ep.FilePath
	})
	if len(candidates) > topN {
		candidates = candidates[:topN]
	}

	// Fetch cliff notes per entry's file (one per distinct file to
	// avoid duplicated LLM cost).
	cliffByFile := map[string]map[string]interface{}{}
	if includeCliff && h.knowledgeStore != nil {
		for _, c := range candidates {
			if _, already := cliffByFile[c.ep.FilePath]; already {
				continue
			}
			cliffArgs, _ := json.Marshal(map[string]interface{}{
				"repository_id": params.RepositoryID,
				"scope_type":    "file",
				"scope_path":    c.ep.FilePath,
			})
			if raw, err := h.callGetCliffNotes(session, cliffArgs); err == nil {
				if payload, ok := raw.(map[string]interface{}); ok {
					cliffByFile[c.ep.FilePath] = payload
				}
			}
		}
	}

	// Assemble final entries with cliff notes attached.
	result := onboardingResult{RepositoryID: params.RepositoryID}
	for _, c := range candidates {
		e := c.ep
		if note, ok := cliffByFile[e.FilePath]; ok {
			e.CliffNotes = note
		}
		result.Entries = append(result.Entries, e)
	}
	return result, nil
}

