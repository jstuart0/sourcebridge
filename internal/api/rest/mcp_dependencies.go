// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"path"
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
//     GetPackageDependencies(repoID). Find the entry where dep.Package == dir.
//     If none found → return empty importers list (not an error — the package
//     may genuinely have no recorded dependencies).
//  4. Cross-repo guard: dep.RepoID must equal the request's repoID.
//  5. Return dep.ImportedBy as-is. ImportedBy contains the directory paths of
//     importing packages as computed by RecomputePackageDependencies.

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

func (h *mcpHandler) dependenciesToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "find_importers",
			Description: "Return the packages that import the package containing a given file. " +
				"Resolves the file's directory (package) and returns the set of packages that " +
				"depend on it. No LLM call — all data is sourced from the indexed package dependency graph. " +
				"Package-level granularity only; file-level attribution is deferred to a follow-up release.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{
						"type":        "string",
						"description": "Repository ID",
					},
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Repo-relative file path (e.g. \"internal/auth/handler.go\"). The package is derived from the file's directory.",
					},
				},
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
	Meta          map[string]interface{} `json:"_meta,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (h *mcpHandler) callFindImporters(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		FilePath     string `json:"file_path"`
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

	repoID := params.RepositoryID

	// Verify the repository exists.
	if h.store.GetRepository(repoID) == nil {
		return nil, errRepositoryNotIndexed(repoID)
	}

	// Derive the package directory from the file path. path.Dir strips the
	// filename; a root-level file ("main.go") returns ".".
	dir := path.Dir(params.FilePath)

	// Find the matching package dependency record.
	deps := h.store.GetPackageDependencies(repoID)
	var matched []string
	for _, dep := range deps {
		if dep.Package != dir {
			continue
		}
		// Cross-repo guard: ensure the record belongs to this repo.
		if dep.RepoID != repoID {
			continue
		}
		matched = dep.ImportedBy
		break
	}

	// Normalise nil slice to empty slice so the JSON renders as [] not null.
	if matched == nil {
		matched = []string{}
	}

	result := findImportersResult{
		RepositoryID:  repoID,
		FilePath:      params.FilePath,
		Package:       dir,
		Importers:     matched,
		ImporterCount: len(matched),
		Meta: map[string]interface{}{
			"note": "importers are raw import path strings (Go module paths or equivalent); package-level granularity only — file-level deferred to follow-up CA",
		},
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// registerDependenciesTools registers the Phase 2b find_importers tool into
// the handler's dispatch map. Called from newMCPHandlerWithEdition after
// registerChangedSymbolsTools.
func registerDependenciesTools(h *mcpHandler) {
	h.registerTool("find_importers", noCtxHandler((*mcpHandler).callFindImporters))
}
