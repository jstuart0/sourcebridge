// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"net/http"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
)

// clusteringRepoStats summarises the clustering state for one repository.
type clusteringRepoStats struct {
	RepoID       string    `json:"repo_id"`
	RepoName     string    `json:"repo_name"`
	ClusterCount int       `json:"cluster_count"`
	Partial      bool      `json:"partial"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type clusteringStatsResponse struct {
	Configured bool                  `json:"configured"`
	Repos      []clusteringRepoStats `json:"repos"`
}

// handleClusteringStats returns the current clustering statistics for every
// indexed repository. Auth: same JWT/API-token middleware as all admin routes.
func (s *Server) handleClusteringStats(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.store.(clustering.ClusterStore)
	if !ok || cs == nil {
		writeJSON(w, http.StatusOK, clusteringStatsResponse{Configured: false})
		return
	}

	store := s.getStore(r)
	repos := store.ListRepositories(r.Context())

	stats := make([]clusteringRepoStats, 0, len(repos))
	for _, repo := range repos {
		clusters, err := cs.GetClusters(context.Background(), repo.ID)
		if err != nil {
			continue
		}
		partial := false
		var updatedAt time.Time
		for _, c := range clusters {
			if c.Partial {
				partial = true
			}
			if c.UpdatedAt.After(updatedAt) {
				updatedAt = c.UpdatedAt
			}
		}
		stats = append(stats, clusteringRepoStats{
			RepoID:       repo.ID,
			RepoName:     repo.Name,
			ClusterCount: len(clusters),
			Partial:      partial,
			UpdatedAt:    updatedAt,
		})
	}

	writeJSON(w, http.StatusOK, clusteringStatsResponse{
		Configured: true,
		Repos:      stats,
	})
}
