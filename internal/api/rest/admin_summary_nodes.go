// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

// handleGetSummaryNodes returns all cached summary nodes for a corpus.
func (s *Server) handleGetSummaryNodes(w http.ResponseWriter, r *http.Request) {
	if s.summaryNodeStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "summary node store not configured"})
		return
	}
	corpusID := chi.URLParam(r, "corpusId")
	nodes, err := s.summaryNodeStore.GetSummaryNodes(corpusID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"corpus_id": corpusID,
		"count":     len(nodes),
		"nodes":     nodes,
	})
}

// handleStoreSummaryNodes bulk-upserts summary nodes from the Python worker.
func (s *Server) handleStoreSummaryNodes(w http.ResponseWriter, r *http.Request) {
	if s.summaryNodeStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "summary node store not configured"})
		return
	}
	var nodes []comprehension.SummaryNode
	if err := json.NewDecoder(r.Body).Decode(&nodes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if err := s.summaryNodeStore.StoreSummaryNodes(nodes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stored", "count": json.Number(fmt.Sprint(len(nodes))).String()})
}

// handleInvalidateSummaryNodes deletes all cached nodes for a corpus,
// forcing a full rebuild on the next generation.
func (s *Server) handleInvalidateSummaryNodes(w http.ResponseWriter, r *http.Request) {
	if s.summaryNodeStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "summary node store not configured"})
		return
	}
	corpusID := chi.URLParam(r, "corpusId")
	if err := s.summaryNodeStore.InvalidateSummaryNodes(corpusID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "invalidated", "corpus_id": corpusID})
}
