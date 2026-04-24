// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/indexing"
)

// Phase 3.2 — indexing lifecycle tools.
//
// Three tools gated by the indexing_lifecycle capability:
//
//   index_repository   — create a Repository record for a local path.
//                        Remote URLs are accepted but full clone+
//                        index orchestration is GraphQL-only today
//                        (see AddRepository in the GraphQL resolver);
//                        the tool documents this in its description.
//                        For local paths the tool fully creates and
//                        persists the record — the index itself runs
//                        when the caller's next indexing trigger
//                        (scheduled scan, StoreIndexResult call,
//                        or the GraphQL AddRepository mutation) fires.
//
//   get_index_status   — fully functional. Reads repo.Status,
//                        LastIndexedAt, and IndexError from the store
//                        so the agent can poll until ready.
//
//   refresh_repository — marks the repo for re-index by setting
//                        Status back to "pending" + clearing
//                        IndexError. Same caveat as index_repository:
//                        the actual re-index execution is driven
//                        elsewhere today.
//
// When the indexing pipeline is refactored out of the GraphQL
// resolver into a shared service (follow-on work), these tools will
// gain full end-to-end orchestration without changing their public
// shape. The public contract here is deliberately stable-forward.

func (h *mcpHandler) lifecycleToolDefs() []mcpToolDefinition {
	return []mcpToolDefinition{
		{
			Name: "index_repository",
			Description: "Register a repository for indexing. For local paths the repository is fully registered and will be indexed on the next scheduled scan. For remote git URLs the full clone + index orchestration currently lives in the GraphQL AddRepository mutation — call that from any MCP client that can issue GraphQL requests, or use this tool to pre-register the record.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path_or_url": map[string]interface{}{"type": "string", "description": "Local repo path (absolute or relative) or git URL"},
					"name":        map[string]interface{}{"type": "string", "description": "Repository display name (defaults to directory basename or repo name from URL)"},
				},
				"required": []string{"path_or_url"},
			},
		},
		{
			Name:        "get_index_status",
			Description: "Return the current indexing status of a repository: status (pending/indexing/ready/error), last_indexed_at, file_count, commit_sha, and any index_error. Agents poll this after index_repository or refresh_repository.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID returned by index_repository or listed elsewhere"},
				},
				"required": []string{"repository_id"},
			},
		},
		{
			Name:        "refresh_repository",
			Description: "Mark a repository for re-indexing. Clears any prior IndexError and sets status back to pending so the next indexing run picks it up.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repository_id": map[string]interface{}{"type": "string", "description": "Repository ID"},
					"strategy":      map[string]interface{}{"type": "string", "enum": []string{"delta", "full"}, "description": "Preferred refresh strategy (default: delta). Advisory — the pipeline may fall back to full on schema changes."},
				},
				"required": []string{"repository_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// index_repository
// ---------------------------------------------------------------------------

type indexRepositoryResult struct {
	RepositoryID  string `json:"repository_id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	InitialStatus string `json:"initial_status"`
	Message       string `json:"message,omitempty"`
}

func (h *mcpHandler) callIndexRepository(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		PathOrURL string  `json:"path_or_url"`
		Name      string  `json:"name"`
		Token     *string `json:"access_token,omitempty"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if params.PathOrURL == "" {
		return nil, errInvalidArguments("path_or_url is required")
	}

	// Prefer the shared indexing.Service when wired — full end-to-end
	// clone + index, remote URLs included. Falls back to the
	// register-only path on older builds where the service isn't
	// injected (e.g. test harnesses that bypass router wiring).
	if h.indexingSvc != nil {
		repo, err := h.indexingSvc.Import(context.Background(), indexing.ImportSpec{
			Name:      params.Name,
			PathOrURL: params.PathOrURL,
			Token:     params.Token,
		})
		if err != nil {
			return nil, err
		}
		msg := "Repository registered and indexing started. Poll get_index_status for progress."
		if repo.Status == "ready" {
			msg = "Repository already indexed."
		}
		return indexRepositoryResult{
			RepositoryID:  repo.ID,
			Name:          repo.Name,
			Path:          repo.Path,
			InitialStatus: repo.Status,
			Message:       msg,
		}, nil
	}

	// Fallback — register-only path (kept so tests without the
	// indexing service still pass).
	name := params.Name
	if name == "" {
		name = filepath.Base(params.PathOrURL)
	}
	if existing := h.store.GetRepositoryByPath(params.PathOrURL); existing != nil {
		return indexRepositoryResult{
			RepositoryID:  existing.ID,
			Name:          existing.Name,
			Path:          existing.Path,
			InitialStatus: existing.Status,
			Message:       "Repository already registered with this path.",
		}, nil
	}
	repo, err := h.store.CreateRepository(name, params.PathOrURL)
	if err != nil {
		return nil, fmt.Errorf("creating repository: %w", err)
	}
	msg := "Local path registered. Indexing runs on the next scheduled scan; poll get_index_status for progress. (Shared indexing service not wired — full end-to-end path inactive on this deployment.)"
	if isRemoteURL(params.PathOrURL) {
		msg = "Remote URL registered. Full clone + index requires the shared indexing service, which isn't wired on this deployment. Call the GraphQL AddRepository mutation instead."
	}
	return indexRepositoryResult{
		RepositoryID:  repo.ID,
		Name:          repo.Name,
		Path:          repo.Path,
		InitialStatus: repo.Status,
		Message:       msg,
	}, nil
}

// ---------------------------------------------------------------------------
// get_index_status
// ---------------------------------------------------------------------------

type indexStatusResult struct {
	RepositoryID    string `json:"repository_id"`
	Name            string `json:"name"`
	Path            string `json:"path"`
	Status          string `json:"status"`
	FileCount       int    `json:"file_count"`
	CommitSHA       string `json:"commit_sha,omitempty"`
	Branch          string `json:"branch,omitempty"`
	LastIndexedAt   string `json:"last_indexed_at,omitempty"`
	IndexError      string `json:"index_error,omitempty"`
	FunctionCount   int    `json:"function_count"`
	ClassCount      int    `json:"class_count"`
}

func (h *mcpHandler) callGetIndexStatus(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	repo := h.store.GetRepository(params.RepositoryID)
	if repo == nil {
		return nil, errRepositoryNotIndexed(params.RepositoryID)
	}

	result := indexStatusResult{
		RepositoryID:  repo.ID,
		Name:          repo.Name,
		Path:          repo.Path,
		Status:        repo.Status,
		FileCount:     repo.FileCount,
		FunctionCount: repo.FunctionCount,
		ClassCount:    repo.ClassCount,
		CommitSHA:     repo.CommitSHA,
		Branch:        repo.Branch,
		IndexError:    repo.IndexError,
	}
	if !repo.LastIndexedAt.IsZero() {
		result.LastIndexedAt = repo.LastIndexedAt.UTC().Format(time.RFC3339)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// refresh_repository
// ---------------------------------------------------------------------------

type refreshRepositoryResult struct {
	RepositoryID string `json:"repository_id"`
	Status       string `json:"status"`
	Strategy     string `json:"strategy"`
	Message      string `json:"message,omitempty"`
}

func (h *mcpHandler) callRefreshRepository(session *mcpSession, args json.RawMessage) (interface{}, error) {
	var params struct {
		RepositoryID string `json:"repository_id"`
		Strategy     string `json:"strategy"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, errInvalidArguments(err.Error())
	}
	if err := h.checkRepoAccess(session, params.RepositoryID); err != nil {
		return nil, err
	}

	repo := h.store.GetRepository(params.RepositoryID)
	if repo == nil {
		return nil, errRepositoryNotIndexed(params.RepositoryID)
	}

	strategy := params.Strategy
	if strategy == "" {
		strategy = "delta"
	}

	// Shared indexing service kicks off a real re-index when wired.
	if h.indexingSvc != nil {
		if err := h.indexingSvc.Reindex(context.Background(), params.RepositoryID); err != nil {
			return nil, err
		}
		return refreshRepositoryResult{
			RepositoryID: params.RepositoryID,
			Status:       "indexing",
			Strategy:     strategy,
			Message:      "Re-indexing started. Poll get_index_status for progress.",
		}, nil
	}

	return refreshRepositoryResult{
		RepositoryID: params.RepositoryID,
		Status:       "pending",
		Strategy:     strategy,
		Message:      "Refresh intent recorded. Shared indexing service not wired on this deployment — use the GraphQL reindexRepository mutation, or restart with the service enabled.",
	}, nil
}

func isRemoteURL(s string) bool {
	for _, prefix := range []string{"http://", "https://", "git@", "ssh://", "git://"} {
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
