// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

// repo_llm_monitor.go — per-repository LLM job endpoints accessible to any
// authenticated user who has read access to the repository.
//
// These are the non-admin counterparts to the /api/v1/admin/llm/* endpoints.
// The admin routes remain intact for cross-repo / global views. The routes
// here are scoped to a single repository and enforce:
//
//  1. Authentication — via the shared authMiddleware() on the outer group.
//  2. Tenant-level repo filtering — via lazyRepoAccessMiddleware on the outer
//     group (no-op in OSS single-tenant; enforces tenant→repo membership in
//     enterprise).
//  3. Per-job ownership — each per-job handler verifies job.RepoID == repoID
//     before responding, returning 404 on mismatch so job existence from other
//     repos is not leaked.
//
// The shared business logic (listRepoLLMActivity, getRepoLLMJob) is factored
// out so that neither the admin handlers nor these handlers duplicate the
// query+conversion pipeline.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/api/graphql"
	"github.com/sourcebridge/sourcebridge/internal/events"
	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// ---- Shared helpers --------------------------------------------------------

// listRepoLLMActivity returns the active and recent job views for a single
// repository, plus the pending-queue snapshot needed for stats.
// The caller is responsible for validating repoID before passing it here.
func (s *Server) listRepoLLMActivity(repoID string, limit int, since time.Time) (active []monitorJobView, recent []monitorJobView, pending []*llm.Job) {
	filter := llm.ListFilter{
		RepoID: repoID,
		Limit:  0, // no cap on active
	}
	rawActive := s.Deps.Orchestrator.ListActive(filter)
	active = make([]monitorJobView, 0, len(rawActive))
	for _, j := range rawActive {
		active = append(active, toMonitorJobView(j))
	}
	pending = s.Deps.Orchestrator.PendingSnapshot(filter)
	enrichQueueMetadata(active, pending, s.Deps.Orchestrator.Metrics(), s.Deps.Orchestrator.MaxConcurrency())

	recentFilter := llm.ListFilter{
		RepoID: repoID,
		Limit:  limit,
	}
	rawRecent := s.Deps.Orchestrator.ListRecent(recentFilter, since)
	recent = make([]monitorJobView, 0, len(rawRecent))
	for _, j := range rawRecent {
		recent = append(recent, toMonitorJobView(j))
	}
	return active, recent, pending
}

// getRepoLLMJob looks up a job by id and verifies it belongs to repoID.
// Returns (job, true) on success. Writes a 404 response and returns (_, false)
// when the job is not found or belongs to a different repo.
func (s *Server) getRepoLLMJob(w http.ResponseWriter, repoID, jobID string) (*llm.Job, bool) {
	job := s.Deps.Orchestrator.GetJob(jobID)
	if job == nil || job.RepoID != repoID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return nil, false
	}
	return job, true
}

// ---- Route handlers --------------------------------------------------------

// handleRepoLLMActivity handles GET /api/v1/repositories/{id}/llm-activity.
// Returns active and recent jobs scoped to the given repository.
func (s *Server) handleRepoLLMActivity(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}

	repoID := chi.URLParam(r, "id")
	if repoID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	since := time.Now().Add(-1 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			since = parsed
		} else if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			since = parsed
		}
	}

	active, recent, pending := s.listRepoLLMActivity(repoID, limit, since)

	workerConnected := s.Deps.Worker != nil && s.Deps.Worker.IsAvailable()
	health := computeMonitorHealth(workerConnected, len(active), recent)

	// Match the shape of monitorActivityResponse from handleLLMActivity
	// so the web client can use one normalizer for both paths. The web's
	// RepoJobActivityResponse interface in repositories/[id]/page.tsx
	// requires `stats` (queue_depth/in_flight/max_concurrency); without
	// it the page crashes with "Cannot read properties of undefined".
	resp := monitorActivityResponse{
		Health:  health,
		Active:  active,
		Recent:  recent,
		Metrics: s.Deps.Orchestrator.Metrics(),
		Modes:   modeRollups(recent),
		Control: monitorQueueControl{
			IntakePaused: s.Deps.Orchestrator.IntakePaused(),
		},
		ErrorCounters: monitorErrorCounters{
			KnowledgeProgressWriteErrors: graphql.KnowledgeProgressWriteErrorsTotal(),
			KnowledgeJobLogWriteErrors:   graphql.KnowledgeJobLogWriteErrorsTotal(),
			EventBusHandlerErrors:        events.HandlerErrorsTotal(),
		},
		Stats: monitorStats{
			InFlight:              len(active),
			QueueDepth:            s.Deps.Orchestrator.QueueDepth(),
			GateWaiting:           gateWaitingCount(active),
			TotalWaiting:          s.Deps.Orchestrator.QueueDepth() + gateWaitingCount(active),
			MaxConcurrency:        s.Deps.Orchestrator.MaxConcurrency(),
			ActivePoolSize:        s.Deps.Orchestrator.ActiveWorkerCount(),
			ConfiguredPoolSize:    s.Deps.Orchestrator.MaxConcurrency(),
			RecentReusedSummaries: totalReusedSummaries(recent),
			ActiveClassic:         countGenerationMode(active, "classic"),
			ActiveUnderstanding:   countGenerationMode(active, "understanding_first"),
			RecentClassic:         countGenerationMode(recent, "classic"),
			RecentUnderstanding:   countGenerationMode(recent, "understanding_first"),
			PendingInteractive:    countPendingPriority(pending, llm.PriorityInteractive),
			PendingMaintenance:    countPendingPriority(pending, llm.PriorityMaintenance),
			PendingPrewarm:        countPendingPriority(pending, llm.PriorityPrewarm),
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRepoLLMJobDetail handles GET /api/v1/repositories/{id}/llm-jobs/{job_id}.
// Returns the full job record for a single job belonging to this repository.
func (s *Server) handleRepoLLMJobDetail(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}

	repoID := chi.URLParam(r, "id")
	jobID := chi.URLParam(r, "job_id")
	if repoID == "" || jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and job_id required"})
		return
	}

	job, ok := s.getRepoLLMJob(w, repoID, jobID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toMonitorJobView(job))
}

// handleRepoLLMJobLogs handles GET /api/v1/repositories/{id}/llm-jobs/{job_id}/logs.
// Returns persisted structured log entries for one job belonging to this repository.
func (s *Server) handleRepoLLMJobLogs(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}

	repoID := chi.URLParam(r, "id")
	jobID := chi.URLParam(r, "job_id")
	if repoID == "" || jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and job_id required"})
		return
	}

	if _, ok := s.getRepoLLMJob(w, repoID, jobID); !ok {
		return
	}

	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	var afterSequence int64
	if v := r.URL.Query().Get("after_sequence"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			afterSequence = n
		}
	}

	rows := s.Deps.Orchestrator.ListJobLogs(jobID, llm.JobLogFilter{
		Limit:         limit,
		AfterSequence: afterSequence,
	})
	logs := make([]monitorJobLogView, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, toMonitorJobLogView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

// handleRepoLLMJobCancel handles POST /api/v1/repositories/{id}/llm-jobs/{job_id}/cancel.
// Cancels a job that belongs to this repository.
func (s *Server) handleRepoLLMJobCancel(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "llm orchestrator not configured",
		})
		return
	}

	repoID := chi.URLParam(r, "id")
	jobID := chi.URLParam(r, "job_id")
	if repoID == "" || jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and job_id required"})
		return
	}

	if _, ok := s.getRepoLLMJob(w, repoID, jobID); !ok {
		return
	}

	if err := s.Deps.Orchestrator.Cancel(jobID); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	job := s.Deps.Orchestrator.GetJob(jobID)
	if s.Deps.KnowledgeStore != nil && job != nil && job.ArtifactID != "" {
		_ = s.Deps.KnowledgeStore.SetArtifactFailed(r.Context(), job.ArtifactID, "CANCELLED", "Generation was cancelled before completion.")
		job.Progress = 0
		job.ProgressPhase = ""
		job.ProgressMessage = ""
	}
	if job == nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancellation_requested"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "cancellation_requested",
		"job":    toMonitorJobView(job),
	})
}
