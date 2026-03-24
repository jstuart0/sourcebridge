// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"net/http"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// knowledgeStats summarizes knowledge artifact status for admin visibility.
type knowledgeStats struct {
	Total      int            `json:"total"`
	Ready      int            `json:"ready"`
	Stale      int            `json:"stale"`
	Generating int            `json:"generating"`
	Failed     int            `json:"failed"`
	Pending    int            `json:"pending"`
	ByType     map[string]int `json:"by_type"`
}

func (s *Server) collectKnowledgeStats(store graphstore.GraphStore) knowledgeStats {
	stats := knowledgeStats{
		ByType: make(map[string]int),
	}

	// Iterate all repositories to collect knowledge artifacts.
	repos := store.ListRepositories()
	for _, repo := range repos {
		artifacts := s.knowledgeStore.GetKnowledgeArtifacts(repo.ID)
		for _, a := range artifacts {
			stats.Total++
			stats.ByType[string(a.Type)]++

			switch a.Status {
			case knowledge.StatusReady:
				stats.Ready++
				if a.Stale {
					stats.Stale++
				}
			case knowledge.StatusGenerating:
				stats.Generating++
			case knowledge.StatusFailed:
				stats.Failed++
			case knowledge.StatusPending:
				stats.Pending++
			}
		}
	}

	return stats
}

func (s *Server) handleAdminKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	if s.knowledgeStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"configured": false,
			"message":    "knowledge store not configured",
		})
		return
	}

	store := s.getStore(r)
	stats := s.collectKnowledgeStats(store)

	// Collect per-repo details.
	type repoKnowledge struct {
		RepoID    string                   `json:"repo_id"`
		RepoName  string                   `json:"repo_name"`
		Artifacts []map[string]interface{} `json:"artifacts"`
		Quality   knowledge.QualityMetrics `json:"quality"`
	}
	var repoDetails []repoKnowledge

	repos := store.ListRepositories()
	for _, repo := range repos {
		artifacts := s.knowledgeStore.GetKnowledgeArtifacts(repo.ID)
		if len(artifacts) == 0 {
			continue
		}
		rk := repoKnowledge{
			RepoID:   repo.ID,
			RepoName: repo.Name,
			Quality:  knowledge.CollectQualityMetrics(s.knowledgeStore, store, repo.ID),
		}
		for _, a := range artifacts {
			entry := map[string]interface{}{
				"id":       a.ID,
				"type":     a.Type,
				"status":   a.Status,
				"stale":    a.Stale,
				"audience": a.Audience,
				"depth":    a.Depth,
			}
			if !a.GeneratedAt.IsZero() {
				entry["generated_at"] = a.GeneratedAt
			}
			if a.SourceRevision.CommitSHA != "" {
				entry["commit_sha"] = a.SourceRevision.CommitSHA
			}
			rk.Artifacts = append(rk.Artifacts, entry)
		}
		repoDetails = append(repoDetails, rk)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"configured":   true,
		"stats":        stats,
		"repositories": repoDetails,
	})
}
