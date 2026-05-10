// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// Phase 2b (CA-154) — find_importers.
//
// Returns the set of packages that import a given file's package. Resolution
// is package-level only (file-level granularity is deferred to a follow-up CA).
//
// Algorithm:
//  1. Validate file_path is provided.
//  2. Derive dir = path.Dir(file_path) — strip the filename to get the package
//     directory. A root-level file ("main.go") produces dir = "." which is
//     handled gracefully (empty result, not an error).
//  3. Iterate all StoredPackageDependencies for the repo via
//     GetPackageDependencies(repoID). Find the entry where dep.Package matches
//     dir. Matching uses suffix-match (dep.Package == dir OR
//     strings.HasSuffix(dep.Package, "/"+dir)) so that Go module-qualified
//     import paths (e.g. "github.com/foo/bar/internal/auth") stored as
//     dep.Package will still match a repo-relative dir ("internal/auth").
//  4. Discriminate _meta.reason:
//     - No deps at all for the repo → "package_dependencies_not_computed"
//     - Deps present but no matching record → "package_has_no_recorded_importers"
//     - Matching record found, ImportedBy empty → no reason (empty importers is valid)
//  5. Cross-repo guard: dep.RepoID must equal the request's repoID.
//  6. Sort importers for stable pagination.
//  7. Apply paginateSlice(importers, offset, params.Limit, 50, 200).
//  8. Return dep.ImportedBy page with next_cursor and total_count.
//     ImportedBy contains the directory paths of importing packages as computed
//     by RecomputePackageDependencies.

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

func (h *mcpHandler) dependenciesToolDefs() []mcpToolDefinition {
	paginationProps := paginationToolProps(50, 200)
	return []mcpToolDefinition{
		{
			Name: "find_importers",
			Description: "Return the packages that import the package containing a given file. " +
				"Resolves the file's directory (package) and returns the set of packages that " +
				"depend on it. No LLM call — all data is sourced from the indexed package dependency graph. " +
				"Package-level granularity only; file-level attribution is deferred to a follow-up release. " +
				"For runtime call relationships, use `get_callers` instead. " +
				"Each entry is a raw import-path string as it appears in the importer's source — " +
				"these are not necessarily repo-relative file paths.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": mergeProps(map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Repo-relative file path (e.g. \"internal/auth/handler.go\"). The package is derived from the file's directory.",
					},
				}, paginationProps),
				"required": []string{"repository_id", "file_path"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Response shape
// ---------------------------------------------------------------------------

// findImportersResult is the full find_importers response.
type findImportersResult struct {
	RepositoryID  string                 `json:"repository_id"`
	FilePath      string                 `json:"file_path"`
	Package       string                 `json:"package"`
	Importers     []string               `json:"importers"`
	ImporterCount int                    `json:"importer_count"`
	TotalCount    int                    `json:"total_count"`
	NextCursor    string                 `json:"next_cursor,omitempty"`
	Meta          map[string]interface{} `json:"_meta,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (h *mcpHandler) callFindImporters(ctx context.Context, session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		FilePath     string `json:"file_path"`
		paginationArgs
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	if params.FilePath == "" {
		return nil, errInvalidArguments("file_path is required")
	}

	// Decode pagination cursor.
	offset, err := decodeCursor(params.Cursor)
	if err != nil {
		return nil, errInvalidArguments(err.Error())
	}

	repoID := params.RepositoryID

	// Verify the repository exists.
	if h.store.GetRepository(ctx, repoID) == nil {
		return nil, errRepositoryNotIndexed(repoID)
	}

	// Derive the package directory from the file path. path.Dir strips the
	// filename; a root-level file ("main.go") returns ".".
	dir := path.Dir(params.FilePath)

	// Find the matching package dependency record.
	deps := h.store.GetPackageDependencies(ctx, repoID)

	// Discriminate reasons before matching:
	//   - No deps at all → "package_dependencies_not_computed"
	//   - Deps present but no matching record → "package_has_no_recorded_importers"
	//   - Match found, ImportedBy empty → no reason (valid empty importers)
	if len(deps) == 0 {
		result := findImportersResult{
			RepositoryID:  repoID,
			FilePath:      params.FilePath,
			Package:       dir,
			Importers:     []string{},
			ImporterCount: 0,
			TotalCount:    0,
			Meta: map[string]interface{}{
				"reason": "package_dependencies_not_computed",
				"note":   "importers are raw import path strings (Go module paths or equivalent); package-level granularity only — file-level deferred to follow-up CA",
			},
		}
		return result, nil
	}

	var matched []string
	found := false
	for _, dep := range deps {
		if !packageDirMatch(dep.Package, dir) {
			continue
		}
		// Cross-repo guard: ensure the record belongs to this repo.
		if dep.RepoID != repoID {
			continue
		}
		matched = dep.ImportedBy
		found = true
		break
	}

	meta := map[string]interface{}{
		"note": "importers are raw import path strings (Go module paths or equivalent); package-level granularity only — file-level deferred to follow-up CA",
	}

	if !found {
		meta["reason"] = "package_has_no_recorded_importers"
		result := findImportersResult{
			RepositoryID:  repoID,
			FilePath:      params.FilePath,
			Package:       dir,
			Importers:     []string{},
			ImporterCount: 0,
			TotalCount:    0,
			Meta:          meta,
		}
		return result, nil
	}

	// Normalise nil slice to empty slice so the JSON renders as [] not null.
	if matched == nil {
		matched = []string{}
	}

	// Sort for stable pagination.
	sort.Strings(matched)

	// Apply pagination.
	page, nextCursor, total := paginateSlice(matched, offset, params.Limit, 50, 200)
	if page == nil {
		page = []string{}
	}

	result := findImportersResult{
		RepositoryID:  repoID,
		FilePath:      params.FilePath,
		Package:       dir,
		Importers:     page,
		ImporterCount: len(page),
		TotalCount:    total,
		NextCursor:    nextCursor,
		Meta:          meta,
	}
	return result, nil
}

// packageDirMatch returns true if pkgKey (a stored dep.Package) matches the
// repo-relative directory dir derived from the request's file_path.
//
// Two forms are supported:
//  1. Exact: pkgKey == dir (repo-relative paths match directly)
//  2. Suffix: strings.HasSuffix(pkgKey, "/"+dir) — covers Go module-qualified
//     import paths (e.g. "github.com/foo/bar/internal/auth" matches "internal/auth")
func packageDirMatch(pkgKey, dir string) bool {
	if pkgKey == dir {
		return true
	}
	return strings.HasSuffix(pkgKey, "/"+dir)
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// dependenciesToolsList returns []mcpTool pairing the Phase 2b find_importers
// definition with its handler. Used by registerDependenciesTools.
func (h *mcpHandler) dependenciesToolsList() []mcpTool {
	defs := h.dependenciesToolDefs()
	defByName := make(map[string]mcpToolDefinition, len(defs))
	for _, d := range defs {
		defByName[d.Name] = d
	}
	return []mcpTool{
		{Definition: defByName["find_importers"], Handler: withCtxHandler((*mcpHandler).callFindImporters)},
	}
}

// registerDependenciesTools registers the Phase 2b find_importers tool into
// the handler's dispatch map. Called from newMCPHandlerWithEdition after
// registerChangedSymbolsTools.
func registerDependenciesTools(h *mcpHandler) {
	for _, t := range h.dependenciesToolsList() {
		h.registerTool(t)
	}
}
