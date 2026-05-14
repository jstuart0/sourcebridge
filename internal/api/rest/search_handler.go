// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"net/http"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/search"
)

// searchRequest is the JSON shape accepted by POST /api/v1/search.
// Fields map directly onto search.Request; the REST endpoint remains
// a thin transport over the shared retrieval service so clients that
// don't speak GraphQL (CLI, Python worker deep-QA, ad-hoc tooling)
// can consume ranked results through the same backbone.
type searchRequest struct {
	Repo         string `json:"repo"`
	Query        string `json:"query"`
	Limit        int    `json:"limit,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Language     string `json:"language,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
	Mode         string `json:"mode,omitempty"`
	IncludeDebug bool   `json:"include_debug,omitempty"`
}

type searchResponse struct {
	Results []searchResult `json:"results"`
	Debug   *search.Debug  `json:"debug,omitempty"`
}

type searchResult struct {
	EntityType string         `json:"entity_type"`
	EntityID   string         `json:"entity_id"`
	Title      string         `json:"title"`
	Subtitle   string         `json:"subtitle,omitempty"`
	FilePath   string         `json:"file_path,omitempty"`
	Line       int            `json:"line,omitempty"`
	RepoID     string         `json:"repo_id"`
	Score      float64        `json:"score"`
	Signals    search.Signals `json:"signals"`
}

// handleSearch implements POST /api/v1/search. Authorization is handled
// by the surrounding middleware (the protected route group applies JWT
// + tenant repo filtering). The handler itself adds a single explicit
// access check so a client can never ask for a repo they cannot see.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if s.Deps.SearchSvc == nil {
		http.Error(w, "search service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req searchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Repo == "" || req.Query == "" {
		http.Error(w, "repo and query are required", http.StatusBadRequest)
		return
	}
	// Tenant filtering: if the request came through
	// RepoAccessMiddleware a tenant-filtered store is in the context.
	// GetRepository on that store returns nil when the caller can't
	// see the repo, which is our authoritative access check.
	store := s.getStore(r)
	if store.GetRepository(r.Context(), req.Repo) == nil {
		http.Error(w, "repository not found or access denied", http.StatusNotFound)
		return
	}
	// Apply the tenant-filtered store scope to the call as well, so
	// any store-hop inside the service sees only authorized symbols.
	resp, err := s.Deps.SearchSvc.Search(withTenantStore(r.Context(), store), &search.Request{
		Repo:         req.Repo,
		Query:        req.Query,
		Limit:        req.Limit,
		Mode:         req.Mode,
		IncludeDebug: req.IncludeDebug,
		Filters: search.Filters{
			Kind:     req.Kind,
			Language: req.Language,
			FilePath: req.FilePath,
		},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	out := searchResponse{
		Results: make([]searchResult, 0, len(resp.Results)),
		Debug:   resp.Debug,
	}
	for _, r := range resp.Results {
		out.Results = append(out.Results, searchResult{
			EntityType: r.EntityType,
			EntityID:   r.EntityID,
			Title:      r.Title,
			Subtitle:   r.Subtitle,
			FilePath:   r.FilePath,
			Line:       r.Line,
			RepoID:     r.RepoID,
			Score:      r.Score,
			Signals:    r.Signals,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// withTenantStore is a no-op today because the search service reads
// the store that was passed into NewService. Kept as a seam so a
// future refactor can propagate the per-request tenant-filtered store
// through ctx without touching call sites.
func withTenantStore(ctx context.Context, _ graphstore.GraphStore) context.Context {
	return ctx
}
