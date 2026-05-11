// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package rest — CA-142 graceful-drain endpoints.
//
// POST /api/v1/admin/llm/server-drain (public, admin-authed)
//   Initiates graceful server drain. Idempotent — safe to call multiple
//   times; subsequent calls return the current drain state without
//   starting a second drain.
//
// POST /api/v1/admin/debug/slow-job?seconds=N (public, admin-authed)
//   Enqueues a real orchestrator job that sleeps for N seconds.
//   Only registered when SOURCEBRIDGE_DEBUG_ENDPOINTS=true.
//   Used for drain validation in Phase 4 of CA-142.
//
// POST /internal/begin-drain (internal :8081 listener, loopback-only)
//   Called by the Kubernetes preStop hook via curl to initiate drain
//   before SIGTERM is delivered. Bound exclusively on 127.0.0.1:8081
//   so no auth middleware is required — loopback enforces locality.

package rest

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"log/slog"

	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// serverDrainAdmitter wraps *Server to implement appdeps.DrainAdmitter.
// The appdeps package defines the interface; we implement it here in the
// rest package to avoid an import cycle (graphql imports rest for types
// would be circular — we pass the interface value at wiring time instead).
//
// *serverDrainAdmitter satisfies appdeps.DrainAdmitter:
//
//	TryAdmitOnDemand() (interface{ Release() }, bool)
type serverDrainAdmitter struct {
	s *Server
}

// IsDraining implements appdeps.DrainAdmitter. Used by cold-start mutations
// that check drain state but do not count toward the on-demand tracker total.
func (a *serverDrainAdmitter) IsDraining() bool {
	return a.s.IsDraining()
}

// TryAdmitOnDemand implements appdeps.DrainAdmitter. Delegates to the
// OnDemandTracker's atomic TryAdmit so the draining check and counter
// increment happen under the same mutex as BeginDrain's MarkDraining call.
// Returns (nil, false) when the server is draining; (admission, true) otherwise.
func (a *serverDrainAdmitter) TryAdmitOnDemand() (interface{ Release() }, bool) {
	if a.s.OnDemand == nil {
		// No tracker configured (e.g. minimal test server) — admit unconditionally.
		return noopRelease{}, true
	}
	return a.s.OnDemand.TryAdmit()
}

// noopRelease is a Release() token used when the OnDemand tracker is not
// configured. Calling Release is a no-op.
type noopRelease struct{}

func (noopRelease) Release() {}

// DrainAdmitterFor returns a *serverDrainAdmitter. Callers assign it
// to the appdeps.DrainAdmitter field; the assignment compiles because
// *serverDrainAdmitter implements appdeps.DrainAdmitter structurally.
func (s *Server) DrainAdmitterFor() *serverDrainAdmitter {
	return &serverDrainAdmitter{s: s}
}

// handleAdminServerDrain initiates graceful server drain via the public
// admin API. Idempotent: returns the current drain state whether or not
// this call was the first.
//
// Response 200:
//
//	{"status":"drain_initiated","draining_since":"<RFC3339>"}
//	{"status":"already_draining","draining_since":"<RFC3339>"}
func (s *Server) handleAdminServerDrain(w http.ResponseWriter, r *http.Request) {
	first := s.BeginDrain("admin_api")
	status := "drain_initiated"
	if !first {
		status = "already_draining"
	}
	s.drainingMu.Lock()
	since := s.drainingAt
	s.drainingMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         status,
		"draining_since": since.UTC().Format(time.RFC3339),
	})
}

// handleBeginDrainInternal is the preStop hook target. Bound only on the
// internal :8081 listener (127.0.0.1) so no auth is required.
// Returns 204 No Content.
func (s *Server) handleBeginDrainInternal(w http.ResponseWriter, r *http.Request) {
	first := s.BeginDrain("prestop_hook")
	if first {
		slog.Info("prestop hook triggered begin_drain", "event", "begindrain_via_prestop")
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDebugSlowJob enqueues a real orchestrator job that sleeps for N
// seconds. Registered only when SOURCEBRIDGE_DEBUG_ENDPOINTS=true.
// Used for drain validation (CA-142 Phase 4).
//
// Query param: seconds (int, 1-3600, default 30).
func (s *Server) handleDebugSlowJob(w http.ResponseWriter, r *http.Request) {
	if s.orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "orchestrator not configured"})
		return
	}
	seconds := 30
	if raw := r.URL.Query().Get("seconds"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 3600 {
			seconds = n
		}
	}
	sleepDur := time.Duration(seconds) * time.Second
	req := &llm.EnqueueRequest{
		Subsystem: "debug",
		JobType:   "slow_job",
		TargetKey: "debug:slow_job:" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Priority:  llm.PriorityMaintenance,
		RunWithContext: func(ctx context.Context, _ llm.Runtime) error {
			start := time.Now()
			slog.Info("debug slow-job: sleeping", "seconds", seconds, "event", "debug_slow_job_start")
			select {
			case <-time.After(sleepDur):
				slog.Info("debug slow-job: done", "seconds", seconds, "event", "debug_slow_job_done")
			case <-ctx.Done():
				// Return the cancellation error so the orchestrator records a
				// cancelled (non-nil) terminal state. Returning nil here would
				// mark the job as successful, which breaks Phase 4.7 timeout
				// validation that expects a cancelled terminal state. CA-142.
				slog.Info("debug slow-job: cancelled",
					"event", "debug_slow_job_cancelled",
					"elapsed_ms", time.Since(start).Milliseconds())
				return ctx.Err()
			}
			return nil
		},
	}
	job, err := s.orchestrator.Enqueue(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "enqueued",
		"job_id":  job.ID,
		"seconds": seconds,
	})
}
