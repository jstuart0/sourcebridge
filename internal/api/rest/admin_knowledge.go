// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"net/http"
	"sort"
	"time"

	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// knowledgeStats summarizes knowledge artifact status for admin visibility.
type knowledgeStats struct {
	Total       int            `json:"total"`
	Ready       int            `json:"ready"`
	Stale       int            `json:"stale"`
	Generating  int            `json:"generating"`
	Failed      int            `json:"failed"`
	Pending     int            `json:"pending"`
	ByType      map[string]int `json:"by_type"`
	ByErrorCode map[string]int `json:"by_error_code"`
}

type knowledgeArtifactSummary struct {
	ID           string                   `json:"id"`
	Type         knowledge.ArtifactType   `json:"type"`
	Status       knowledge.ArtifactStatus `json:"status"`
	Stale        bool                     `json:"stale"`
	Audience     knowledge.Audience       `json:"audience"`
	Depth        knowledge.Depth          `json:"depth"`
	Progress     float64                  `json:"progress"`
	ScopeType    string                   `json:"scope_type,omitempty"`
	ScopePath    string                   `json:"scope_path,omitempty"`
	ErrorCode    string                   `json:"error_code,omitempty"`
	ErrorMessage string                   `json:"error_message,omitempty"`
	GeneratedAt  *time.Time               `json:"generated_at,omitempty"`
	UpdatedAt    time.Time                `json:"updated_at"`
	CommitSHA    string                   `json:"commit_sha,omitempty"`
}

type repoKnowledge struct {
	RepoID    string                     `json:"repo_id"`
	RepoName  string                     `json:"repo_name"`
	Artifacts []knowledgeArtifactSummary `json:"artifacts"`
	Quality   knowledge.QualityMetrics   `json:"quality"`
}

func (s *Server) collectKnowledgeStats(store graphstore.GraphStore) knowledgeStats {
	stats := knowledgeStats{
		ByType:      make(map[string]int),
		ByErrorCode: make(map[string]int),
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
				code := a.ErrorCode
				if code == "" {
					code = "UNKNOWN"
				}
				stats.ByErrorCode[code]++
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
			entry := knowledgeArtifactSummary{
				ID:           a.ID,
				Type:         a.Type,
				Status:       a.Status,
				Stale:        a.Stale,
				Audience:     a.Audience,
				Depth:        a.Depth,
				Progress:     a.Progress,
				ErrorCode:    a.ErrorCode,
				ErrorMessage: a.ErrorMessage,
				UpdatedAt:    a.UpdatedAt,
			}
			if a.Scope != nil {
				entry.ScopeType = string(a.Scope.ScopeType)
				entry.ScopePath = a.Scope.ScopePath
			}
			if !a.GeneratedAt.IsZero() {
				entry.GeneratedAt = &a.GeneratedAt
			}
			if a.SourceRevision.CommitSHA != "" {
				entry.CommitSHA = a.SourceRevision.CommitSHA
			}
			rk.Artifacts = append(rk.Artifacts, entry)
		}
		sort.Slice(rk.Artifacts, func(i, j int) bool {
			left := rk.Artifacts[i]
			right := rk.Artifacts[j]
			leftInFlight := left.Status == knowledge.StatusGenerating || left.Status == knowledge.StatusPending
			rightInFlight := right.Status == knowledge.StatusGenerating || right.Status == knowledge.StatusPending
			if leftInFlight != rightInFlight {
				return leftInFlight
			}
			if left.Status != right.Status {
				if left.Status == knowledge.StatusFailed || right.Status == knowledge.StatusFailed {
					return left.Status == knowledge.StatusFailed
				}
			}
			if !left.UpdatedAt.Equal(right.UpdatedAt) {
				return left.UpdatedAt.After(right.UpdatedAt)
			}
			return left.ID < right.ID
		})
		repoDetails = append(repoDetails, rk)
	}
	sort.Slice(repoDetails, func(i, j int) bool { return repoDetails[i].RepoName < repoDetails[j].RepoName })

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"configured":   true,
		"stats":        stats,
		"repositories": repoDetails,
	})
}
