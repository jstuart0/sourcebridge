// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/db"
)

// KnowledgeSettingsStore is the storage contract used by the admin
// timeout endpoint. Concrete impl: *db.KnowledgeSettingsStore.
//
// Defined as an interface here so tests can fake it without dragging
// in a live SurrealDB. Codex r1 M3 — the REST handler is the single
// write boundary; reject out-of-range with HTTP 400.
type KnowledgeSettingsStore interface {
	Get(ctx context.Context) (time.Duration, error)
	Put(ctx context.Context, secs int, updatedBy string) error
}

// WithKnowledgeSettingsStore wires the knowledge-settings persistence
// store into the server. When nil, the GET endpoint returns a 503 and
// PUT is unavailable. The boot path in cli/serve.go always wires this
// in external-Surreal mode; embedded/test modes leave it nil and fall
// back to the boot-time env default in the timeout provider closure.
func WithKnowledgeSettingsStore(store KnowledgeSettingsStore) ServerOption {
	return func(s *Server) { s.knowledgeSettingsStore = store }
}

// knowledgeTimeoutResponse is the shape returned by GET / accepted by PUT.
type knowledgeTimeoutResponse struct {
	Seconds int    `json:"seconds"`
	Notice  string `json:"notice,omitempty"`
}

type knowledgeTimeoutPutRequest struct {
	Seconds int `json:"seconds"`
}

// handleGetKnowledgeTimeout returns the operator-configured cap on
// repository-scoped knowledge RPC calls. This is the OUTER safety net,
// distinct from the per-phase reaper at orchestrator.go:297-348 which
// handles "no progress in 10 min" stuck jobs.
//
// Returns the live value from Surreal. On a transient outage the
// handler returns 503 (the timeout-provider closure in serve.go has
// its own fallback path; the REST endpoint is operator-facing, so
// surfacing a clean error is the right call here rather than masking
// a database problem with a stale value).
func (s *Server) handleGetKnowledgeTimeout(w http.ResponseWriter, r *http.Request) {
	if s.knowledgeSettingsStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "knowledge settings store not configured",
		})
		return
	}
	ctx := r.Context()
	d, err := s.knowledgeSettingsStore.Get(ctx)
	if err != nil {
		if errors.Is(err, db.ErrKnowledgeSettingsNotFound) {
			// No row seeded yet — return the default. The boot path
			// will seed on first successful Get; this lets the UI
			// render a sensible value before that happens.
			writeJSON(w, http.StatusOK, knowledgeTimeoutResponse{
				Seconds: db.KnowledgeTimeoutDefaultSecs,
				Notice:  "no operator value set; showing default (4h)",
			})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "knowledge settings store unavailable: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, knowledgeTimeoutResponse{
		Seconds: int(d / time.Second),
	})
}

// handlePutKnowledgeTimeout writes a new safety-net timeout value.
//
// Validation: the body's `seconds` must be in [1800, 86400]. Out-of-
// range values are rejected with HTTP 400 (codex r1 L1 — write-side
// reject, read-side defensive clamp). Non-int / missing fields are
// rejected with 400.
//
// The provider closure in serve.go has a 5s in-memory cache; expect
// a new value to take effect within 5s of a successful PUT.
func (s *Server) handlePutKnowledgeTimeout(w http.ResponseWriter, r *http.Request) {
	if s.knowledgeSettingsStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "knowledge settings store not configured",
		})
		return
	}
	var req knowledgeTimeoutPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON: " + err.Error(),
		})
		return
	}
	if req.Seconds < db.KnowledgeTimeoutMinSecs || req.Seconds > db.KnowledgeTimeoutMaxSecs {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "seconds must be between 1800 (30 min) and 86400 (24 h)",
			"code":  "OUT_OF_RANGE",
		})
		return
	}
	userID, _ := currentActorIdentity(r)
	ctx := r.Context()
	if err := s.knowledgeSettingsStore.Put(ctx, req.Seconds, userID); err != nil {
		if errors.Is(err, db.ErrKnowledgeTimeoutOutOfRange) {
			// Defense in depth — the store also rejects out-of-range,
			// but we mapped that in the validation block above. If we
			// somehow get here, treat as 400 too.
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "seconds must be between 1800 and 86400",
				"code":  "OUT_OF_RANGE",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to save: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, knowledgeTimeoutResponse{
		Seconds: req.Seconds,
	})
}
