// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/llm"
)

// newRepoMonitorTestServer reuses the shared monitor test helper.
func newRepoMonitorTestServer(t *testing.T) *Server {
	t.Helper()
	return newMonitorTestServer(t)
}

// TestRepoLLMActivitySignedInUserWithAccess tests that an authenticated user
// (non-admin) receives a 200 with activity data for a repo they can access.
// Auth enforcement is at the middleware layer; the handler itself is tested
// here for correct business logic — it does not check auth directly (the
// outer integration test covers the full middleware stack).
func TestRepoLLMActivitySignedInUserWithAccess(t *testing.T) {
	s := newRepoMonitorTestServer(t)

	done := make(chan struct{})
	_, err := s.Deps.Orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:activity-test",
		RepoID:      "repo-abc",
		Run: func(rt llm.Runtime) error {
			close(done)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("job did not start in time")
	}
	// Wait for the job to appear in recent history.
	time.Sleep(20 * time.Millisecond)

	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{id}/llm-activity", s.handleRepoLLMActivity)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/repo-abc/llm-activity?limit=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Active []monitorJobView `json:"active"`
		Recent []monitorJobView `json:"recent"`
		Health monitorHealth    `json:"health"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// Health field must be present.
	if body.Health.Status == "" {
		t.Fatal("expected non-empty health status in response")
	}
}

// TestRepoLLMActivityNoOrchestratorReturns503 tests the graceful degradation
// when the orchestrator is not configured.
func TestRepoLLMActivityNoOrchestratorReturns503(t *testing.T) {
	s := &Server{Deps: &appdeps.AppDeps{}}

	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{id}/llm-activity", s.handleRepoLLMActivity)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/repo-abc/llm-activity", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

// TestRepoLLMJobDetailMatchingRepo tests that a job is returned when the
// repo_id matches the path parameter.
func TestRepoLLMJobDetailMatchingRepo(t *testing.T) {
	s := newRepoMonitorTestServer(t)

	job, err := s.Deps.Orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:detail-match",
		RepoID:      "repo-abc",
		Run: func(rt llm.Runtime) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{id}/llm-jobs/{job_id}", s.handleRepoLLMJobDetail)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/repo-abc/llm-jobs/"+job.ID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var view monitorJobView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if view.ID != job.ID {
		t.Fatalf("expected job id %q, got %q", job.ID, view.ID)
	}
}

// TestRepoLLMJobDetailMismatchedRepo tests that a 404 is returned when the
// job exists but belongs to a different repo — so jobs from other repos are
// not leaked through this endpoint.
func TestRepoLLMJobDetailMismatchedRepo(t *testing.T) {
	s := newRepoMonitorTestServer(t)

	job, err := s.Deps.Orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:detail-mismatch",
		RepoID:      "repo-xyz", // job belongs to repo-xyz
		Run: func(rt llm.Runtime) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{id}/llm-jobs/{job_id}", s.handleRepoLLMJobDetail)

	// Caller claims repo-abc but job belongs to repo-xyz.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/repo-abc/llm-jobs/"+job.ID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on repo mismatch, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRepoLLMJobDetailNotFound tests 404 for a nonexistent job id.
func TestRepoLLMJobDetailNotFound(t *testing.T) {
	s := newRepoMonitorTestServer(t)

	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{id}/llm-jobs/{job_id}", s.handleRepoLLMJobDetail)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/repo-abc/llm-jobs/nonexistent-job", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// TestRepoLLMJobLogsMatchingRepo tests that logs are returned for a job
// belonging to the requested repo.
func TestRepoLLMJobLogsMatchingRepo(t *testing.T) {
	s := newRepoMonitorTestServer(t)

	job, err := s.Deps.Orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:logs-match",
		RepoID:      "repo-abc",
		Run: func(rt llm.Runtime) error {
			rt.ReportProgress(0.25, "snapshot", "Snapshot assembled", 0)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{id}/llm-jobs/{job_id}/logs", s.handleRepoLLMJobLogs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/repo-abc/llm-jobs/"+job.ID+"/logs", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Logs []monitorJobLogView `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Logs) == 0 {
		t.Fatal("expected at least one log entry")
	}
}

// TestRepoLLMJobLogsMismatchedRepo tests that 404 is returned when the job
// exists but belongs to a different repo.
func TestRepoLLMJobLogsMismatchedRepo(t *testing.T) {
	s := newRepoMonitorTestServer(t)

	job, err := s.Deps.Orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:logs-mismatch",
		RepoID:      "repo-xyz",
		Run: func(rt llm.Runtime) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{id}/llm-jobs/{job_id}/logs", s.handleRepoLLMJobLogs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/repo-abc/llm-jobs/"+job.ID+"/logs", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on repo mismatch, got %d", w.Code)
	}
}

// TestRepoLLMJobCancelMatchingRepo tests successful cancellation of a job
// belonging to the requested repo.
func TestRepoLLMJobCancelMatchingRepo(t *testing.T) {
	s := newRepoMonitorTestServer(t)

	block := make(chan struct{})
	started := make(chan struct{})
	job, err := s.Deps.Orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:cancel-match",
		RepoID:      "repo-abc",
		Run: func(rt llm.Runtime) error {
			close(started)
			<-block
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start in time")
	}

	r := chi.NewRouter()
	r.Post("/api/v1/repositories/{id}/llm-jobs/{job_id}/cancel", s.handleRepoLLMJobCancel)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/repo-abc/llm-jobs/"+job.ID+"/cancel", strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	close(block)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRepoLLMJobCancelMismatchedRepo tests that cancellation is rejected with
// 404 when the job belongs to a different repo.
func TestRepoLLMJobCancelMismatchedRepo(t *testing.T) {
	s := newRepoMonitorTestServer(t)

	block := make(chan struct{})
	started := make(chan struct{})
	job, err := s.Deps.Orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem:   llm.SubsystemKnowledge,
		LLMProvider: "test",
		JobType:     "cliff_notes",
		TargetKey:   "repo-1:cancel-mismatch",
		RepoID:      "repo-xyz",
		Run: func(rt llm.Runtime) error {
			close(started)
			<-block
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start in time")
	}

	r := chi.NewRouter()
	r.Post("/api/v1/repositories/{id}/llm-jobs/{job_id}/cancel", s.handleRepoLLMJobCancel)

	// Caller claims repo-abc but job belongs to repo-xyz.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/repo-abc/llm-jobs/"+job.ID+"/cancel", strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	close(block)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on repo mismatch, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRepoLLMUnauthenticatedIsHandledByMiddleware documents that unauthenticated
// requests are rejected by the authMiddleware layer (not the handler itself).
// The handler receives only authenticated requests. This test verifies that
// calling the handler directly (without middleware) still returns a valid
// response — the 401 gate is the router's responsibility.
func TestRepoLLMHandlerAccessibleToAnyAuthenticatedUser(t *testing.T) {
	// Calling the handler directly (bypassing auth middleware) should return
	// a normal 200 — authentication is enforced by the outer middleware, not
	// duplicated inside the handler.
	s := newRepoMonitorTestServer(t)

	r := chi.NewRouter()
	r.Get("/api/v1/repositories/{id}/llm-activity", s.handleRepoLLMActivity)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/any-repo/llm-activity", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (auth is middleware responsibility), got %d", w.Code)
	}
}
