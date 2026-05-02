// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/funnel"
)

// canonicalFunnelEvents is the server-side allowlist for event names emitted
// by the new funnel and adoption instrumentation. Only these names are
// persisted verbatim (metadata included).
var canonicalFunnelEvents = map[string]bool{
	// First-run funnel
	"funnel.setup.started":         true,
	"funnel.setup.completed":       true,
	"funnel.repo.added":            true,
	"funnel.index.started":         true,
	"funnel.index.completed":       true,
	"funnel.index.failed":          true,
	"funnel.first_artifact.viewed": true,

	// Feature adoption
	"feature.explain.used":           true,
	"feature.discuss.used":           true,
	"feature.review.used":            true,
	"feature.requirements.used":      true,
	"feature.execution_path.used":    true,
	"feature.requirement_chat.used":  true,

	// Artifact generation
	"artifact.generation.cliff_notes":         true,
	"artifact.generation.workflow_story":       true,
	"artifact.generation.learning_path":        true,
	"artifact.generation.code_tour":            true,
	"artifact.generation.architecture_diagram": true,
}

// legacyTrackEvents is the sunset snake_case set that is still accepted for
// backwards compatibility with existing frontend calls. Metadata is scrubbed
// (PII fields stripped) before persisting.
var legacyTrackEvents = map[string]bool{
	"repository_added":                    true,
	"repository_index_completed":          true,
	"analyze_symbol_used":                 true,
	"discuss_code_used":                   true,
	"review_code_used":                    true,
	"requirements_imported":               true,
	"spec_extraction_triggered":           true,
	"field_guide_generated":               true,
	"workflow_story_generated":            true,
	"explain_scope_used":                  true,
	"execution_path_requested":            true,
	"requirement_field_guide_timed_out":   true,
	"requirement_field_guide_failed":      true,
	"requirement_field_guide_generated":   true,
	"requirement_field_guide_regenerated": true,
	"requirement_chat_used":               true,
	"requirement_tab_switched":            true,
}

// legacyPIIKeys is the set of metadata keys stripped from legacy events before
// persistence. These are paths, IDs, or free-text values that could identify
// source files, requirements, or user input.
var legacyPIIKeys = map[string]bool{
	"filePath":      true,
	"scopePath":     true,
	"requirementId": true,
	"artifactId":    true,
	"symbolId":      true,
	"entryValue":    true,
}

type telemetryEventRequest struct {
	Event        string                 `json:"event"`
	RepositoryID string                 `json:"repositoryId"`
	Metadata     map[string]interface{} `json:"metadata"`
}

func (s *Server) handleTelemetryEvent(w http.ResponseWriter, r *http.Request) {
	// C1: strict no-op when funnel telemetry is disabled. No log, no DB write,
	// no event details land anywhere. Just 202 so the client doesn't retry.
	if !s.flags.FunnelTelemetry {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Body cap: prevents memory exhaustion from oversized payloads.
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

	var req telemetryEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Event == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event is required"})
		return
	}

	// Server-side event-name allowlist (M4). Unknown event names are rejected
	// with 400 so clients get immediate feedback and accidental PII in
	// undocumented event names never lands in the store.
	isCanonical := canonicalFunnelEvents[req.Event]
	isLegacy := !isCanonical && legacyTrackEvents[req.Event]
	if !isCanonical && !isLegacy {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown event name"})
		return
	}

	userID, tenantID := currentActorIdentity(r)

	// H2: slog must not include metadata — it may contain paths, identifiers,
	// or other PII. Log only the coarse fields needed for operational visibility.
	slog.Info("product telemetry",
		"event", req.Event,
		"repository_id", req.RepositoryID,
		"user_id", userID,
		"tenant_id", tenantID,
	)

	// Persist to funnel store via fire-and-forget goroutine with a 3-second
	// timeout. The goroutine owns the context lifetime; a slow or unavailable
	// SurrealDB cannot block the HTTP response.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		meta := req.Metadata
		if isLegacy {
			// Scrub PII keys from legacy events before persisting.
			meta = scrubbedMeta(req.Metadata)
		}

		uid := userID
		tid := tenantID
		ev := funnel.FunnelEvent{
			Event:      req.Event,
			Source:     "browser",
			UserID:     &uid,
			TenantID:   &tid,
			RepoID:     req.RepositoryID,
			Metadata:   meta,
			OccurredAt: time.Now().UTC(),
		}

		if s.funnelStore != nil {
			_ = s.funnelStore.RecordEvent(ctx, ev)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// scrubbedMeta returns a shallow copy of m with all legacyPIIKeys removed.
// If m is nil, nil is returned. The original map is never modified.
func scrubbedMeta(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if !legacyPIIKeys[k] {
			out[k] = v
		}
	}
	return out
}
