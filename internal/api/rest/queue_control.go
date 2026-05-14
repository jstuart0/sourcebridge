// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// QueueControlStore persists orchestrator intake controls across API restarts.
// Only durable operator intent belongs here; one-shot actions like drain are
// handled directly by the orchestrator.
type QueueControlStore interface {
	LoadQueueControl(ctx context.Context) (*QueueControlRecord, error)
	SaveQueueControl(ctx context.Context, rec *QueueControlRecord) error
}

type QueueControlRecord struct {
	IntakePaused bool `json:"intake_paused"`
}

type updateQueueControlRequest struct {
	IntakePaused *bool `json:"intake_paused,omitempty"`
}

type queueControlResponse struct {
	IntakePaused bool `json:"intake_paused"`
}

func (s *Server) persistQueueControl() {
	if s.queueControlStore == nil || s.Deps.Orchestrator == nil {
		return
	}
	if err := s.queueControlStore.SaveQueueControl(context.Background(), &QueueControlRecord{
		IntakePaused: s.Deps.Orchestrator.IntakePaused(),
	}); err != nil {
		slog.Warn("failed to persist queue control", "error", err)
	}
}

func (s *Server) handleGetQueueControl(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "llm orchestrator not configured"})
		return
	}
	writeJSON(w, http.StatusOK, queueControlResponse{
		IntakePaused: s.Deps.Orchestrator.IntakePaused(),
	})
}

func (s *Server) handleUpdateQueueControl(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "llm orchestrator not configured"})
		return
	}
	var req updateQueueControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.IntakePaused == nil {
		http.Error(w, `{"error":"intake_paused is required"}`, http.StatusBadRequest)
		return
	}
	s.Deps.Orchestrator.SetIntakePaused(*req.IntakePaused)
	s.persistQueueControl()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "saved",
		"intake_paused": s.Deps.Orchestrator.IntakePaused(),
	})
}

func (s *Server) handleDrainQueue(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "llm orchestrator not configured"})
		return
	}
	cancelled, err := s.Deps.Orchestrator.DrainPending()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":              "drained",
		"cancelled_pending":   cancelled,
		"intake_paused":       s.Deps.Orchestrator.IntakePaused(),
		"remaining_queue_len": s.Deps.Orchestrator.QueueDepth(),
	})
}
