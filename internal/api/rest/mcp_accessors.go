// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// Phase 1a accessor tools for the MCP server. These are thin reads over
// data the indexer already produces and stores — call graph, imports,
// architecture-diagram artifacts, and file-level git history. They
// exist to let MCP clients get structured answers to questions like
// "who calls this?" / "what does this file depend on?" without
// round-tripping through ask_question (which is slow and expensive).
//
// Public contract for symbol references across these tools:
//   - Primary identifier is {file_path, symbol_name, line_start?}.
//     Stable across re-indexes of unchanged code.
//   - symbol_id is accepted as an optional optimization hint for
//     clients that kept one from a prior call, but is not load-bearing.
//
// The tools here are deliberately narrow — merge heuristics, classifier
// work, and composed workflows live in later phases of the plan.

// ---------------------------------------------------------------------------
// Tool definitions (registered in baseTools())
// ---------------------------------------------------------------------------

func (h *mcpHandler) phase1aToolDefs() []mcpToolDefinition {
	symbolRefProps := func() map[string]interface{} {
		return map[string]interface{}{
			"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
			"file_path":     map[string]interface{}{"type": "string", "description": "Repo-relative file path containing the target symbol"},
			"symbol_name":   map[string]interface{}{"type": "string", "description": "Name of the target symbol"},
			"line_start":    map[string]interface{}{"type": "integer", "description": "Optional disambiguator when the same name appears multiple times in the file"},
			"symbol_id":     map[string]interface{}{"type": "string", "description": "Optional optimization hint — skips resolution if valid"},
		}
	}

	return []mcpToolDefinition{
		{
			Name:        "get_callers",
			Description: "Return symbols that call the target symbol. Optionally walks the call graph outward up to max_hops. Data is sourced from the stored call graph — no LLM call. Use this instead of ask_question when the question is \"who calls X?\". Supports pagination via cursor/limit.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": mergeProps(
					mergeProps(symbolRefProps(), map[string]interface{}{
						"max_hops":             map[string]interface{}{"type": "integer", "description": "Walk depth outward from the target (default 1, cap 3)"},
						"include_test_callers": map[string]interface{}{"type": "boolean", "description": "Include symbols marked IsTest (default false)"},
					}),
					paginationToolProps(100, 500),
				),
				"required": []string{"repository_id", "file_path", "symbol_name"},
			},
		},
		{
			Name:        "get_callees",
			Description: "Return symbols called by the target symbol. Optionally walks the call graph inward up to max_hops. Data is sourced from the stored call graph — no LLM call. Supports pagination via cursor/limit.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": mergeProps(
					mergeProps(symbolRefProps(), map[string]interface{}{
						"max_hops": map[string]interface{}{"type": "integer", "description": "Walk depth inward from the target (default 1, cap 3)"},
					}),
					paginationToolProps(100, 500),
				),
				"required": []string{"repository_id", "file_path", "symbol_name"},
			},
		},
		{
			Name:        "get_file_imports",
			Description: "Return the direct imports declared by a file. Optionally walks the import graph transitively.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"file_path":     map[string]interface{}{"type": "string", "description": "Repo-relative file path"},
					"transitive":    map[string]interface{}{"type": "boolean", "description": "Follow imports transitively (default false)"},
					"max_depth":     map[string]interface{}{"type": "integer", "description": "Max transitive depth (default 2, cap 5)"},
				},
				"required": []string{"repository_id", "file_path"},
			},
		},
		{
			Name:        "get_architecture_diagram",
			Description: "Return the architecture diagram artifact directly (Mermaid or structured JSON), without cliff-notes prose wrapping. Returns nil if no diagram has been generated for the scope.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"scope_type":    map[string]interface{}{"type": "string", "enum": []string{"repository", "module", "file"}, "description": "Scope of the diagram (default repository)"},
					"scope_path":    map[string]interface{}{"type": "string", "description": "Required for module or file scopes"},
					"format":        map[string]interface{}{"type": "string", "enum": []string{"mermaid", "structured_json"}, "description": "Output format (default mermaid)"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "get_recent_changes",
			Description: "Return the last N git commits affecting a path (file-level) or a specific symbol (line-range filter via git log -L). Provide either `path` OR {file_path + symbol_name} — symbol-level mode uses the symbol's line range at current HEAD to narrow commits to those that touched those lines.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"path":          map[string]interface{}{"type": "string", "description": "Repo-relative path (file or directory) — file-level mode. Default = repo root."},
					"file_path":     map[string]interface{}{"type": "string", "description": "Repo-relative file path for symbol-level mode. Pair with symbol_name."},
					"symbol_name":   map[string]interface{}{"type": "string", "description": "Symbol name for symbol-level mode. Pair with file_path."},
					"line_start":    map[string]interface{}{"type": "integer", "description": "Optional disambiguator when the same name appears multiple times in file_path."},
					"symbol_id":     map[string]interface{}{"type": "string", "description": "Optional optimization hint for symbol-level mode."},
					"limit":         map[string]interface{}{"type": "integer", "description": "Max commits to return (default 20, cap 200)"},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}

// mergeProps merges two JSON-schema property maps. Later values override earlier.
func mergeProps(a, b map[string]interface{}) map[string]interface{} {
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
// Shared symbol-ref resolution
// ---------------------------------------------------------------------------

type symbolRefParams struct {
	RepositoryID string `json:"repository_id"`
	FilePath     string `json:"file_path"`
	SymbolName   string `json:"symbol_name"`
	LineStart    int    `json:"line_start,omitempty"`
	SymbolID     string `json:"symbol_id,omitempty"`
}

// resolveSymbol picks a single symbol from the store using the public
// contract (file_path + symbol_name + optional line_start), falling back
// to symbol_id when provided and the contract fields are ambiguous or
// empty. Returns nil with a non-nil error when no matching symbol is
// found.
func (h *mcpHandler) resolveSymbol(p symbolRefParams) (*graph.StoredSymbol, error) {
	// symbol_id optimization path — validated against repository_id to
	// prevent cross-repo leakage.
	if p.SymbolID != "" {
		sym := h.store.GetSymbol(p.SymbolID)
		if sym != nil && sym.RepoID == p.RepositoryID {
			return sym, nil
		}
		// Fall through to contract resolution — a stale symbol_id is
		// not fatal.
	}

	if p.FilePath == "" || p.SymbolName == "" {
		return nil, fmt.Errorf("must provide either symbol_id or {file_path, symbol_name}")
	}

	fileSymbols := h.store.GetSymbolsByFile(p.RepositoryID, p.FilePath)
	var candidates []*graph.StoredSymbol
	for _, s := range fileSymbols {
		if s.Name == p.SymbolName {
			candidates = append(candidates, s)
		}
	}

	if len(candidates) == 0 {
		return nil, errSymbolNotFound(p.SymbolName, p.FilePath)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	// Multiple matches — use line_start as disambiguator if provided.
	if p.LineStart > 0 {
		for _, s := range candidates {
			if s.StartLine == p.LineStart {
				return s, nil
			}
		}
		return nil, fmt.Errorf("symbol %q at line %d not found in %s (found %d other occurrences)", p.SymbolName, p.LineStart, p.FilePath, len(candidates))
	}

	// Ambiguous — return the first by line number (deterministic) and
	// let the caller decide whether that's acceptable.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].StartLine < candidates[j].StartLine })
	return candidates[0], nil
}

// ---------------------------------------------------------------------------
// call graph — get_callers / get_callees
// ---------------------------------------------------------------------------

type callGraphEdge struct {
	FromID     string `json:"from_id"`
	FromName   string `json:"from_name,omitempty"`
	ToID       string `json:"to_id"`
	ToName     string `json:"to_name,omitempty"`
	HopsFromRoot int  `json:"hops_from_root"`
}

type callGraphSymbol struct {
	SymbolID     string `json:"symbol_id"`
	FilePath     string `json:"file_path"`
	SymbolName   string `json:"symbol_name"`
	Kind         string `json:"kind"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	IsTest       bool   `json:"is_test"`
	HopsFromRoot int    `json:"hops_from_root"`
}

type callGraphResult struct {
	Root       callGraphSymbol   `json:"root"`
	Symbols    []callGraphSymbol `json:"symbols"`
	Edges      []callGraphEdge   `json:"edges"`
	Total      int               `json:"total"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// callGetCallers / callGetCallees share most of the walk logic — direction
// is the only thing that changes.

func (h *mcpHandler) callGetCallers(session *mcpSession, args json.RawMessage) (interface{}, error) {
	return h.walkCallGraph(session, args, "callers")
}

func (h *mcpHandler) callGetCallees(session *mcpSession, args json.RawMessage) (interface{}, error) {
	return h.walkCallGraph(session, args, "callees")
}

func (h *mcpHandler) walkCallGraph(session *mcpSession, args json.RawMessage, direction string) (interface{}, error) {
	var params struct {
		symbolRefParams
		paginationArgs
		MaxHops            int  `json:"max_hops"`
		IncludeTestCallers bool `json:"include_test_callers"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	hops := params.MaxHops
	if hops <= 0 {
		hops = 1
	}
	if hops > 3 {
		hops = 3
	}

	root, err := h.resolveSymbol(params.symbolRefParams)
	if err != nil {
		return nil, err
	}

	visited := map[string]int{root.ID: 0}
	var edges []callGraphEdge

	// BFS outward. `frontier` holds symbol IDs discovered at the
	// current hop; at each hop we expand and accumulate edges.
	frontier := []string{root.ID}
	for hop := 1; hop <= hops; hop++ {
		var next []string
		for _, id := range frontier {
			var neighbors []string
			switch direction {
			case "callers":
				neighbors = h.store.GetCallers(id)
			case "callees":
				neighbors = h.store.GetCallees(id)
			}
			for _, nid := range neighbors {
				if _, seen := visited[nid]; seen {
					// Still record the edge even if the node was
					// already visited — a cycle should show up in
					// edges so the caller can reason about it.
					edges = append(edges, callGraphEdge{
						FromID: edgeFrom(id, nid, direction),
						ToID:   edgeTo(id, nid, direction),
						HopsFromRoot: hop,
					})
					continue
				}
				visited[nid] = hop
				next = append(next, nid)
				edges = append(edges, callGraphEdge{
					FromID: edgeFrom(id, nid, direction),
					ToID:   edgeTo(id, nid, direction),
					HopsFromRoot: hop,
				})
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}

	// Hydrate all visited symbols and filter by include_test_callers.
	ids := make([]string, 0, len(visited))
	for id := range visited {
		ids = append(ids, id)
	}
	byID := h.store.GetSymbolsByIDs(ids)

	symbols := make([]callGraphSymbol, 0, len(visited))
	for id, hopFromRoot := range visited {
		sym, ok := byID[id]
		if !ok || sym == nil {
			continue
		}
		if direction == "callers" && !params.IncludeTestCallers && sym.IsTest {
			continue
		}
		symbols = append(symbols, callGraphSymbol{
			SymbolID:     sym.ID,
			FilePath:     sym.FilePath,
			SymbolName:   sym.Name,
			Kind:         sym.Kind,
			StartLine:    sym.StartLine,
			EndLine:      sym.EndLine,
			IsTest:       sym.IsTest,
			HopsFromRoot: hopFromRoot,
		})
	}
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].HopsFromRoot != symbols[j].HopsFromRoot {
			return symbols[i].HopsFromRoot < symbols[j].HopsFromRoot
		}
		if symbols[i].FilePath != symbols[j].FilePath {
			return symbols[i].FilePath < symbols[j].FilePath
		}
		return symbols[i].StartLine < symbols[j].StartLine
	})

	// Fill in edge symbol names for readability.
	for i := range edges {
		if s, ok := byID[edges[i].FromID]; ok && s != nil {
			edges[i].FromName = s.Name
		}
		if s, ok := byID[edges[i].ToID]; ok && s != nil {
			edges[i].ToName = s.Name
		}
	}

	rootView := callGraphSymbol{
		SymbolID:   root.ID,
		FilePath:   root.FilePath,
		SymbolName: root.Name,
		Kind:       root.Kind,
		StartLine:  root.StartLine,
		EndLine:    root.EndLine,
		IsTest:     root.IsTest,
	}

	// Paginate the symbols list. Edges aren't paginated today — they
	// reference pageless symbol IDs, so full edges + paginated nodes
	// is the honest contract (clients can filter edges against the
	// current page's symbols on their side).
	offset, err := decodeCursor(params.Cursor)
	if err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	page, nextCursor, total := paginateSlice(symbols, offset, params.Limit, 100, 500)

	return callGraphResult{
		Root:       rootView,
		Symbols:    page,
		Edges:      edges,
		Total:      total,
		NextCursor: nextCursor,
	}, nil
}

// edgeFrom / edgeTo orient the stored (caller, callee) pair relative
// to the walk direction. In a "callers" walk, the edge points from
// the caller (neighbor) to the current node; in a "callees" walk,
// from the current node to the callee (neighbor).
func edgeFrom(cur, neighbor, direction string) string {
	if direction == "callers" {
		return neighbor
	}
	return cur
}

func edgeTo(cur, neighbor, direction string) string {
	if direction == "callers" {
		return cur
	}
	return neighbor
}

// ---------------------------------------------------------------------------
// imports — get_file_imports
// ---------------------------------------------------------------------------

type fileImport struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Depth    int    `json:"depth"`
	ViaFile  string `json:"via_file,omitempty"`
}

type fileImportsResult struct {
	FilePath string       `json:"file_path"`
	Imports  []fileImport `json:"imports"`
}

func (h *mcpHandler) callGetFileImports(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		FilePath     string `json:"file_path"`
		Transitive   bool   `json:"transitive"`
		MaxDepth     int    `json:"max_depth"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}
	if params.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	maxDepth := 1
	if params.Transitive {
		maxDepth = params.MaxDepth
		if maxDepth <= 0 {
			maxDepth = 2
		}
		if maxDepth > 5 {
			maxDepth = 5
		}
	}

	all := h.store.GetImports(params.RepositoryID)

	// Build a file-path → []import index keyed by the file *declaring*
	// the import. GetImports returns StoredImport with only FileID —
	// we need to join to file path. The cheapest path today is a
	// single pass over repo files.
	files := h.store.GetFiles(params.RepositoryID)
	fileIDToPath := make(map[string]string, len(files))
	pathToFileID := make(map[string]string, len(files))
	for _, f := range files {
		fileIDToPath[f.ID] = f.Path
		pathToFileID[f.Path] = f.ID
	}

	importsByFile := make(map[string][]*graph.StoredImport)
	for _, imp := range all {
		p := fileIDToPath[imp.FileID]
		if p == "" {
			continue
		}
		importsByFile[p] = append(importsByFile[p], imp)
	}

	// Walk outward from params.FilePath up to maxDepth.
	result := fileImportsResult{FilePath: params.FilePath}
	visited := map[string]bool{params.FilePath: true}
	frontier := []string{params.FilePath}

	for depth := 1; depth <= maxDepth; depth++ {
		var next []string
		for _, fp := range frontier {
			for _, imp := range importsByFile[fp] {
				fi := fileImport{Path: imp.Path, Line: imp.Line, Depth: depth}
				if fp != params.FilePath {
					fi.ViaFile = fp
				}
				result.Imports = append(result.Imports, fi)

				// Attempt to resolve the import path to a repo file for
				// transitive walk. Cheap heuristic: look for any file
				// whose path ends with the import path (language-agnostic).
				if depth < maxDepth {
					if target := matchImportToFile(imp.Path, files); target != "" {
						if !visited[target] {
							visited[target] = true
							next = append(next, target)
						}
					}
				}
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}

	// Deterministic order — by depth, then path.
	sort.Slice(result.Imports, func(i, j int) bool {
		if result.Imports[i].Depth != result.Imports[j].Depth {
			return result.Imports[i].Depth < result.Imports[j].Depth
		}
		return result.Imports[i].Path < result.Imports[j].Path
	})

	return result, nil
}

// matchImportToFile resolves an import string to a repo-relative file
// path using a suffix-match heuristic. Good enough for Go (module
// paths end with package dir), Python (dotted paths map to file
// paths), and most others; doesn't try to be perfect.
func matchImportToFile(importPath string, files []*graph.File) string {
	// Strip quotes and any trailing slash.
	importPath = strings.Trim(importPath, `"' /`)
	if importPath == "" {
		return ""
	}
	// Try full-path match first (some languages store "foo/bar/baz" as
	// the import and the repo has exactly that).
	for _, f := range files {
		if f.Path == importPath {
			return f.Path
		}
	}
	// Fall back to suffix match — strip any leading slashes, split
	// on "/", and look for any file whose path ends with the tail.
	parts := strings.Split(importPath, "/")
	if len(parts) == 0 {
		return ""
	}
	tail := parts[len(parts)-1]
	if tail == "" {
		return ""
	}
	for _, f := range files {
		base := filepath.Base(f.Path)
		if strings.HasPrefix(base, tail) {
			return f.Path
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// architecture diagram — get_architecture_diagram
// ---------------------------------------------------------------------------

type architectureDiagramResult struct {
	Found      bool                   `json:"found"`
	ScopeType  string                 `json:"scope_type"`
	ScopePath  string                 `json:"scope_path,omitempty"`
	Format     string                 `json:"format"`
	Mermaid    string                 `json:"mermaid,omitempty"`
	Structured map[string]interface{} `json:"structured,omitempty"`
	Status     string                 `json:"status,omitempty"`
	Message    string                 `json:"message,omitempty"`
}

func (h *mcpHandler) callGetArchitectureDiagram(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		ScopeType    string `json:"scope_type"`
		ScopePath    string `json:"scope_path"`
		Format       string `json:"format"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}
	if h.knowledgeStore == nil {
		return nil, fmt.Errorf("Knowledge store is not configured. Architecture diagrams require knowledge persistence.")
	}

	scopeType := knowledge.ScopeType(params.ScopeType)
	if scopeType == "" {
		scopeType = knowledge.ScopeRepository
	}
	format := params.Format
	if format == "" {
		format = "mermaid"
	}

	key := knowledge.ArtifactKey{
		RepositoryID: params.RepositoryID,
		Type:         knowledge.ArtifactArchitectureDiagram,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Scope: knowledge.ArtifactScope{
			ScopeType: scopeType,
			ScopePath: params.ScopePath,
		},
	}

	artifact := h.knowledgeStore.GetArtifactByKey(key)
	if artifact == nil {
		return architectureDiagramResult{
			Found:     false,
			ScopeType: string(scopeType),
			ScopePath: params.ScopePath,
			Format:    format,
			Message:   "No architecture diagram has been generated for this scope. Call generate_artifact from the web UI or trigger via GraphQL.",
		}, nil
	}

	result := architectureDiagramResult{
		Found:     true,
		ScopeType: string(scopeType),
		ScopePath: params.ScopePath,
		Format:    format,
		Status:    string(artifact.Status),
	}

	if artifact.Status != knowledge.StatusReady {
		result.Message = fmt.Sprintf("Artifact is in %q state — content may be missing or incomplete.", artifact.Status)
	}

	// The architecture_diagram artifact stores its content in its
	// sections. The Mermaid body is the primary section content; a
	// structured_json body, when present, lives in the section Summary
	// or in a separate section. Return whatever the artifact actually has.
	for _, s := range artifact.Sections {
		if format == "mermaid" && s.Content != "" {
			result.Mermaid = s.Content
			break
		}
		if format == "structured_json" && s.Summary != "" {
			var decoded map[string]interface{}
			if err := json.Unmarshal([]byte(s.Summary), &decoded); err == nil {
				result.Structured = decoded
				break
			}
		}
	}

	if result.Mermaid == "" && result.Structured == nil {
		result.Message = "Artifact exists but does not carry a body in the requested format."
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// recent changes — get_recent_changes
// ---------------------------------------------------------------------------

type recentChange struct {
	SHA         string   `json:"sha"`
	Author      string   `json:"author"`
	AuthorEmail string   `json:"author_email"`
	Date        string   `json:"date"`
	Subject     string   `json:"subject"`
	FilesTouched []string `json:"files_touched,omitempty"`
}

type recentChangesResult struct {
	RepositoryID string         `json:"repository_id"`
	Path         string         `json:"path,omitempty"`
	Commits      []recentChange `json:"commits"`
	Truncated    bool           `json:"truncated,omitempty"`
}

func (h *mcpHandler) callGetRecentChanges(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		Path         string `json:"path"`
		// Symbol-level filter (Phase 1b). When both file_path and
		// symbol_name are set, the tool resolves the symbol, reads
		// its line range, and runs `git log -L start,end:file` instead
		// of a plain path-filtered log.
		FilePath   string `json:"file_path"`
		SymbolName string `json:"symbol_name"`
		LineStart  int    `json:"line_start,omitempty"`
		SymbolID   string `json:"symbol_id,omitempty"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	repo := h.store.GetRepository(params.RepositoryID)
	if repo == nil {
		return nil, fmt.Errorf("repository not found")
	}

	// Pick the git working tree. A remote-cloned repo uses ClonePath;
	// a locally-rooted repo uses Path. Both should contain a .git.
	gitRoot := repo.ClonePath
	if gitRoot == "" {
		gitRoot = repo.Path
	}
	if gitRoot == "" {
		return nil, fmt.Errorf("repository has no resolved git path on disk")
	}

	// Symbol-level mode: resolve the symbol and run git log -L.
	if params.SymbolName != "" || params.SymbolID != "" {
		sym, err := h.resolveSymbol(symbolRefParams{
			RepositoryID: params.RepositoryID,
			FilePath:     params.FilePath,
			SymbolName:   params.SymbolName,
			LineStart:    params.LineStart,
			SymbolID:     params.SymbolID,
		})
		if err != nil {
			return nil, err
		}
		commits, err := runGitLogSymbol(context.Background(), gitRoot, sym.FilePath, sym.StartLine, sym.EndLine, limit)
		if err != nil {
			return nil, fmt.Errorf("git log -L failed: %v", err)
		}
		return recentChangesResult{
			RepositoryID: params.RepositoryID,
			Path:         fmt.Sprintf("%s:%d-%d", sym.FilePath, sym.StartLine, sym.EndLine),
			Commits:      commits,
		}, nil
	}

	// File / directory mode.
	commits, err := runGitLog(context.Background(), gitRoot, params.Path, limit)
	if err != nil {
		return nil, fmt.Errorf("git log failed: %v", err)
	}

	return recentChangesResult{
		RepositoryID: params.RepositoryID,
		Path:         params.Path,
		Commits:      commits,
	}, nil
}

// runGitLogSymbol shells to `git log -L start,end:file` for
// line-range-aware history. The -L mode output is more verbose than
// path-filtered log (it includes the patch for each commit), so we
// pass -s to suppress patches and keep the output parseable with the
// same format as the file-level helper.
func runGitLogSymbol(ctx context.Context, gitRoot, filePath string, startLine, endLine, limit int) ([]recentChange, error) {
	const sep = "\x1f"
	const recSep = "\x1e"
	format := strings.Join([]string{"%H", "%an", "%ae", "%aI", "%s"}, sep) + recSep

	// `git log -L` doesn't accept -n (limit) directly — it always
	// prints full history for the range. We read the output and
	// stop after `limit` records. Also pass -s to suppress the
	// per-commit patch that -L emits by default.
	args := []string{
		"-C", gitRoot,
		"log",
		fmt.Sprintf("--pretty=format:%s", format),
		"-s",
		"-L",
		fmt.Sprintf("%d,%d:%s", startLine, endLine, filePath),
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var commits []recentChange
	for _, block := range strings.Split(string(out), recSep) {
		block = strings.TrimLeft(block, "\n")
		if block == "" {
			continue
		}
		// -s output shouldn't include file blocks, but strip any
		// trailing newline content defensively.
		header := block
		if i := strings.Index(block, "\n"); i != -1 {
			header = block[:i]
		}
		fields := strings.Split(header, sep)
		if len(fields) < 5 {
			continue
		}
		commits = append(commits, recentChange{
			SHA:         fields[0],
			Author:      fields[1],
			AuthorEmail: fields[2],
			Date:        fields[3],
			Subject:     fields[4],
		})
		if len(commits) >= limit {
			break
		}
	}
	return commits, nil
}

// runGitLog shells out to `git log --pretty=... -n LIMIT -- PATH` and
// parses the output. The format string uses a unit-separator so we can
// split commit records reliably even when subjects contain tabs.
func runGitLog(ctx context.Context, gitRoot, pathFilter string, limit int) ([]recentChange, error) {
	const sep = "\x1f" // unit separator
	const recSep = "\x1e" // record separator
	format := strings.Join([]string{"%H", "%an", "%ae", "%aI", "%s"}, sep) + recSep

	args := []string{"-C", gitRoot, "log", fmt.Sprintf("--pretty=format:%s", format), fmt.Sprintf("-n%d", limit), "--name-only"}
	if pathFilter != "" {
		args = append(args, "--", pathFilter)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	// With --name-only, each commit looks like:
	//   <fmt>\x1e\n<file1>\n<file2>\n\n
	// Splitting on recSep gives us commit blocks; the first line of
	// each block is the format line, the rest (if any) are the files.
	raw := strings.Split(string(out), recSep)
	var commits []recentChange
	for _, block := range raw {
		block = strings.TrimLeft(block, "\n")
		if block == "" {
			continue
		}
		newline := strings.Index(block, "\n")
		var header string
		var filesPart string
		if newline == -1 {
			header = block
		} else {
			header = block[:newline]
			filesPart = strings.TrimSpace(block[newline+1:])
		}

		fields := strings.Split(header, sep)
		if len(fields) < 5 {
			continue
		}
		c := recentChange{
			SHA:         fields[0],
			Author:      fields[1],
			AuthorEmail: fields[2],
			Date:        fields[3],
			Subject:     fields[4],
		}
		if filesPart != "" {
			for _, line := range strings.Split(filesPart, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					c.FilesTouched = append(c.FilesTouched, line)
				}
			}
		}
		commits = append(commits, c)
	}
	return commits, nil
}
