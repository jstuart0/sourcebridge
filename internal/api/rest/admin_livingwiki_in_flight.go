// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// inFlightPageView is the JSON shape for one in-flight page.
type inFlightPageView struct {
	PageID     string    `json:"page_id"`
	TemplateID string    `json:"template_id"`
	Attempt    int       `json:"attempt"`
	StartedAt  time.Time `json:"started_at"`
	ElapsedMs  int64     `json:"elapsed_ms"`
}

// livingWikiInFlightResponse is the JSON payload for
// GET /api/v1/admin/llm/jobs/{id}/livingwiki/in-flight.
type livingWikiInFlightResponse struct {
	JobID                  string             `json:"job_id"`
	AsOf                   time.Time          `json:"as_of"`
	MedianCompletedMs      int64              `json:"median_completed_ms"`
	MedianCompletedMsKnown bool               `json:"median_completed_ms_known"`
	Pages                  []inFlightPageView `json:"pages"`
}

// handleLivingWikiInFlight handles
// GET /api/v1/admin/llm/jobs/{id}/livingwiki/in-flight.
//
// Returns 503 when the living-wiki orchestrator is unavailable. Returns 200
// with an empty pages list when the job ID is not currently tracked (e.g. a
// completed job, or a job that has not yet started page generation). Clients
// should NOT treat an empty list as a 404 — it simply means no pages are
// currently in-flight for this job.
func (s *Server) handleLivingWikiInFlight(w http.ResponseWriter, r *http.Request) {
	if s.livingWikiLiveOrchestrator == nil {
		http.Error(w, "living wiki orchestrator unavailable", http.StatusServiceUnavailable)
		return
	}

	jobID := chi.URLParam(r, "id")
	asOf := time.Now()

	pages := s.livingWikiLiveOrchestrator.InFlightPages(jobID)
	medianMs, medianKnown := s.livingWikiLiveOrchestrator.MedianCompletedPageMs(jobID)

	views := make([]inFlightPageView, len(pages))
	for i, p := range pages {
		views[i] = inFlightPageView{
			PageID:     p.PageID,
			TemplateID: p.TemplateID,
			Attempt:    p.Attempt,
			StartedAt:  p.StartedAt,
			ElapsedMs:  asOf.Sub(p.StartedAt).Milliseconds(),
		}
	}

	resp := livingWikiInFlightResponse{
		JobID:                  jobID,
		AsOf:                   asOf,
		MedianCompletedMs:      medianMs,
		MedianCompletedMsKnown: medianKnown,
		Pages:                  views,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Headers already sent; log is the only option.
		_ = err
	}
}

