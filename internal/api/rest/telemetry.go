// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type telemetryEventRequest struct {
	Event        string                 `json:"event"`
	RepositoryID string                 `json:"repositoryId"`
	Metadata     map[string]interface{} `json:"metadata"`
}

func (s *Server) handleTelemetryEvent(w http.ResponseWriter, r *http.Request) {
	var req telemetryEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Event == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event is required"})
		return
	}
	userID, tenantID := currentActorIdentity(r)
	slog.Info("product telemetry",
		"event", req.Event,
		"repository_id", req.RepositoryID,
		"user_id", userID,
		"tenant_id", tenantID,
		"metadata", req.Metadata,
	)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}
