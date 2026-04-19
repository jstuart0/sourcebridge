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

	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
)

// newMonitorTestServer builds a Server instance wired to an isolated
// orchestrator + in-memory JobStore, sufficient for testing the
// monitor HTTP handlers without pulling in the full server stack.
func newMonitorTestServer(t *testing.T) *Server {
	t.Helper()
	store := llm.NewMemStore()
	orch := orchestrator.New(store, orchestrator.Config{
		MaxConcurrency: 2,
	})
	t.Cleanup(func() { _ = orch.Shutdown(time.Second) })
	return &Server{
		jobStore:     store,
		orchestrator: orch,
	}
}

func TestErrorTitleForCodeCoversKnownCodes(t *testing.T) {
	cases := []struct {
		code     string
		hasTitle bool
	}{
		{"LLM_EMPTY", true},
		{"SNAPSHOT_TOO_LARGE", true},
		{"DEADLINE_EXCEEDED", true},
		{"WORKER_UNAVAILABLE", true},
		{"INTERNAL", true},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			title, hint := errorTitleForCode(tc.code)
			if tc.hasTitle {
				if title == "" {
					t.Fatalf("expected non-empty title for code %q", tc.code)
				}
				if hint == "" {
					t.Fatalf("expected non-empty hint for code %q", tc.code)
				}
			} else {
				if title != "" || hint != "" {
					t.Fatalf("expected empty title/hint for empty code, got %q / %q", title, hint)
				}
			}
		})
	}
}

func TestComputeMonitorHealthHappyPath(t *testing.T) {
	// Worker up, 3 active, 5 succeeded, 0 failed -> healthy with active summary.
	h := computeMonitorHealth(true, 3, []monitorJobView{
		{Status: string(llm.StatusReady)},
		{Status: string(llm.StatusReady)},
		{Status: string(llm.StatusReady)},
		{Status: string(llm.StatusReady)},
		{Status: string(llm.StatusReady)},
	})
	if h.Status != "healthy" {
		t.Fatalf("expected healthy status, got %q", h.Status)
	}
	if h.Summary == "" {
		t.Fatal("expected a non-empty summary")
	}
}

func TestComputeMonitorHealthDegraded(t *testing.T) {
	// 1 failed out of 4 -> degraded (25%)
	h := computeMonitorHealth(true, 0, []monitorJobView{
		{Status: string(llm.StatusReady)},
		{Status: string(llm.StatusReady)},
		{Status: string(llm.StatusReady)},
		{Status: string(llm.StatusFailed), ErrorCode: "DEADLINE_EXCEEDED"},
	})
	if h.Status != "degraded" {
		t.Fatalf("expected degraded status, got %q (%s)", h.Status, h.Summary)
	}
}

func TestComputeMonitorHealthUnhealthy(t *testing.T) {
	// 4 failed out of 5 -> unhealthy
	h := computeMonitorHealth(true, 0, []monitorJobView{
		{Status: string(llm.StatusReady)},
		{Status: string(llm.StatusFailed), ErrorCode: "DEADLINE_EXCEEDED"},
		{Status: string(llm.StatusFailed), ErrorCode: "DEADLINE_EXCEEDED"},
		{Status: string(llm.StatusFailed), ErrorCode: "INTERNAL"},
		{Status: string(llm.StatusFailed), ErrorCode: "INTERNAL"},
	})
	if h.Status != "unhealthy" {
		t.Fatalf("expected unhealthy status, got %q (%s)", h.Status, h.Summary)
	}
}

func TestComputeMonitorHealthWorkerDown(t *testing.T) {
	h := computeMonitorHealth(false, 0, []monitorJobView{
		{Status: string(llm.StatusReady)},
	})
	if h.Status != "unhealthy" {
		t.Fatalf("expected unhealthy when worker is down, got %q", h.Status)
	}
}

func TestComputeMonitorHealthTreatsRestartNoiseAsDegraded(t *testing.T) {
	h := computeMonitorHealth(true, 0, []monitorJobView{
		{Status: string(llm.StatusFailed), ErrorCode: "WORKER_UNAVAILABLE"},
		{Status: string(llm.StatusFailed), ErrorCode: "WORKER_UNAVAILABLE"},
		{Status: string(llm.StatusReady)},
	})
	if h.Status != "degraded" {
		t.Fatalf("expected degraded status for restart noise, got %q (%s)", h.Status, h.Summary)
	}
}

func TestHandleLLMActivityEmptySystem(t *testing.T) {
	s := newMonitorTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm/activity", nil)
	w := httptest.NewRecorder()
	s.handleLLMActivity(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp monitorActivityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.Stats.MaxConcurrency != 2 {
		t.Fatalf("expected max_concurrency=2, got %d", resp.Stats.MaxConcurrency)
	}
	if resp.Stats.ActivePoolSize != 2 {
		t.Fatalf("expected active_pool_size=2, got %d", resp.Stats.ActivePoolSize)
	}
	if resp.Stats.ConfiguredPoolSize != 2 {
		t.Fatalf("expected configured_pool_size=2, got %d", resp.Stats.ConfiguredPoolSize)
	}
	if resp.Stats.RecentReusedSummaries != 0 {
		t.Fatalf("expected recent_reused_summaries=0, got %d", resp.Stats.RecentReusedSummaries)
	}
	if resp.ErrorCounters.KnowledgeProgressWriteErrors != 0 {
		t.Fatalf("expected knowledge progress errors 0, got %d", resp.ErrorCounters.KnowledgeProgressWriteErrors)
	}
	if resp.ErrorCounters.EventBusHandlerErrors != 0 {
		t.Fatalf("expected event bus handler errors 0, got %d", resp.ErrorCounters.EventBusHandlerErrors)
	}
	if len(resp.Active) != 0 {
		t.Fatalf("expected empty active list, got %d", len(resp.Active))
	}
	// Worker is nil in the test fixture, so health should be unhealthy.
	if resp.Health.Status != "unhealthy" {
		t.Fatalf("expected unhealthy (no worker), got %q", resp.Health.Status)
	}
	if resp.Control.IntakePaused {
		t.Fatal("expected intake to be unpaused by default")
	}
}

func TestTotalReusedSummaries(t *testing.T) {
	total := totalReusedSummaries([]monitorJobView{
		{ReusedSummaries: 3},
		{ReusedSummaries: 0},
		{ReusedSummaries: 5},
	})
	if total != 8 {
		t.Fatalf("expected reused total 8, got %d", total)
	}
}

func TestModeRollups(t *testing.T) {
	rollups := modeRollups([]monitorJobView{
		{
			GenerationMode:  "classic",
			Status:          string(llm.StatusReady),
			ElapsedMs:       1200,
			ReusedSummaries: 1,
			LeafCacheHits:   2,
			FileCacheHits:   1,
		},
		{
			GenerationMode:   "classic",
			Status:           string(llm.StatusFailed),
			ElapsedMs:        2400,
			PackageCacheHits: 3,
		},
		{
			GenerationMode:  "understanding_first",
			Status:          string(llm.StatusReady),
			ElapsedMs:       1800,
			ReusedSummaries: 4,
			RootCacheHits:   2,
		},
	})
	classic := rollups["classic"]
	if classic.Total != 2 || classic.Succeeded != 1 || classic.Failed != 1 {
		t.Fatalf("unexpected classic rollup %#v", classic)
	}
	if classic.ReusedSummaries != 1 {
		t.Fatalf("expected classic reused summaries 1, got %d", classic.ReusedSummaries)
	}
	if classic.CacheHits != 6 {
		t.Fatalf("expected classic cache hits 6, got %d", classic.CacheHits)
	}
	if classic.P50LatencyMs == 0 || classic.P95LatencyMs == 0 {
		t.Fatalf("expected classic latencies, got %#v", classic)
	}
	understanding := rollups["understanding_first"]
	if understanding.Total != 1 || understanding.Succeeded != 1 {
		t.Fatalf("unexpected understanding-first rollup %#v", understanding)
	}
	if understanding.ReusedSummaries != 4 || understanding.CacheHits != 2 {
		t.Fatalf("unexpected understanding-first reuse %#v", understanding)
	}
}

func TestHandleLLMActivityShowsCompletedJob(t *testing.T) {
	s := newMonitorTestServer(t)

	done := make(chan struct{})
	_, err := s.orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo-1:activity",
		Run: func(rt llm.Runtime) error {
			rt.ReportProgress(0.5, "midway", "halfway")
			rt.ReportTokens(200, 150)
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
	// Poll until it's marked ready (memstore + orchestrator write back fast).
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm/activity", nil)
		w := httptest.NewRecorder()
		s.handleLLMActivity(w, req)
		var resp monitorActivityResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if len(resp.Recent) > 0 && resp.Recent[0].Status == string(llm.StatusReady) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not appear in recent history as ready")
}

func TestHandleLLMJobDetailRoundTrip(t *testing.T) {
	s := newMonitorTestServer(t)

	job, err := s.orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo-1:detail",
		Run: func(rt llm.Runtime) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	// Wait a beat for the worker to process.
	time.Sleep(50 * time.Millisecond)

	// Set up a router so chi URLParam works.
	r := chi.NewRouter()
	r.Get("/api/v1/admin/llm/jobs/{id}", s.handleLLMJobDetail)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm/jobs/"+job.ID, nil)
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
		t.Fatalf("expected id %q, got %q", job.ID, view.ID)
	}
}

func TestHandleLLMJobLogs(t *testing.T) {
	s := newMonitorTestServer(t)
	job, err := s.orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo-1:logs",
		Run: func(rt llm.Runtime) error {
			rt.ReportProgress(0.25, "snapshot", "Snapshot assembled")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	r := chi.NewRouter()
	r.Get("/api/v1/admin/llm/jobs/{id}/logs", s.handleLLMJobLogs)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm/jobs/"+job.ID+"/logs", nil)
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

func TestHandleLLMJobDetail404(t *testing.T) {
	s := newMonitorTestServer(t)
	r := chi.NewRouter()
	r.Get("/api/v1/admin/llm/jobs/{id}", s.handleLLMJobDetail)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm/jobs/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleLLMJobRetryNonRetryableState(t *testing.T) {
	s := newMonitorTestServer(t)

	// Enqueue a job and let it complete successfully.
	job, err := s.orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo-1:retry-ok",
		Run: func(rt llm.Runtime) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	r := chi.NewRouter()
	r.Post("/api/v1/admin/llm/jobs/{id}/retry", s.handleLLMJobRetry)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/jobs/"+job.ID+"/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for non-retryable (ready) state, got %d", w.Code)
	}
}

func TestHandleUpdateQueueControl(t *testing.T) {
	s := newMonitorTestServer(t)
	r := chi.NewRouter()
	r.Put("/api/v1/admin/llm/control", s.handleUpdateQueueControl)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm/control", strings.NewReader(`{"intake_paused":true}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !s.orchestrator.IntakePaused() {
		t.Fatal("expected intake to be paused")
	}
}

func TestHandleDrainQueue(t *testing.T) {
	s := newMonitorTestServer(t)
	block := make(chan struct{})
	started := make(chan struct{})
	_, err := s.orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		TargetKey: "repo-1:drain-running",
		Run: func(rt llm.Runtime) error {
			close(started)
			<-block
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue running job: %v", err)
	}
	<-started
	_, err = s.orchestrator.Enqueue(&llm.EnqueueRequest{
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "learning_path",
		TargetKey: "repo-1:drain-pending",
		Run: func(rt llm.Runtime) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enqueue pending job: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/api/v1/admin/llm/drain", s.handleDrainQueue)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm/drain", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	close(block)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

func TestParseListFilterBasicFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/llm/activity?subsystem=knowledge&job_type=cliff_notes&repo_id=abc&limit=25", nil)
	f := parseListFilter(req)
	if f.Subsystem != llm.SubsystemKnowledge {
		t.Fatalf("expected subsystem knowledge, got %q", f.Subsystem)
	}
	if f.JobType != "cliff_notes" {
		t.Fatalf("expected job_type cliff_notes, got %q", f.JobType)
	}
	if f.RepoID != "abc" {
		t.Fatalf("expected repo_id abc, got %q", f.RepoID)
	}
	if f.Limit != 25 {
		t.Fatalf("expected limit 25, got %d", f.Limit)
	}
}

func TestEventMatchesFilter(t *testing.T) {
	job := &llm.Job{
		Subsystem: llm.SubsystemKnowledge,
		JobType:   "cliff_notes",
		RepoID:    "repo-1",
		TargetKey: "tk",
	}
	ev := llm.JobEvent{Kind: llm.EventProgress, Job: job}

	// empty filter matches everything
	if !eventMatchesFilter(ev, llm.ListFilter{}) {
		t.Fatal("empty filter should match")
	}
	// subsystem match
	if !eventMatchesFilter(ev, llm.ListFilter{Subsystem: llm.SubsystemKnowledge}) {
		t.Fatal("matching subsystem should pass")
	}
	// subsystem mismatch
	if eventMatchesFilter(ev, llm.ListFilter{Subsystem: llm.SubsystemReasoning}) {
		t.Fatal("mismatched subsystem should not pass")
	}
	// repo_id match
	if !eventMatchesFilter(ev, llm.ListFilter{RepoID: "repo-1"}) {
		t.Fatal("matching repo should pass")
	}
	// repo_id mismatch
	if eventMatchesFilter(ev, llm.ListFilter{RepoID: "repo-2"}) {
		t.Fatal("mismatched repo should not pass")
	}
}
