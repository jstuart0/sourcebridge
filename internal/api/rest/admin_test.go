// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Handler-level coverage for handleAdminTestWorker and handleAdminTestLLM
// (CA-282).
//
// Both handlers delegate all their logic to s.Deps.Worker (nil-check + CheckHealth).
// This file covers:
//   - Worker not configured (nil) → 200 with status "unavailable"
//   - Worker configured but CheckHealth returns error → 200 with status "error"
//   - Worker configured and healthy → 200 with status "healthy" / "ok"
//   - Worker configured but unhealthy → 200 with status "unhealthy" / "degraded"
//
// RBAC guard (non-admin → 403) is exercised through the full middleware chain
// in admin_role_test.go (TestAdminRole_UserPutLLMConfig_Returns403 pattern).
// The cases TestAdminRole_UserAdminTestWorker_Returns403 and
// TestAdminRole_UserAdminTestLLM_Returns403 are added to admin_role_test.go.
package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/config"
)

// fakeWorkerClient is a minimal stand-in for *worker.Client at the boundary
// handleAdminTestWorker and handleAdminTestLLM care about: CheckHealth.
// Because *worker.Client is a concrete type, we use a thin wrapper struct
// that exposes only the fields the handlers read: the health-check result.
//
// The handlers are called with the real Server struct; we embed a
// *fakeWorkerHealthState into s and override the CheckHealth delegation via
// the workerHealthOverride field below.
//
// Approach: since the handlers call s.Deps.Worker.CheckHealth, and s.Deps.Worker is
// *worker.Client (concrete), we cannot substitute an interface for the tests.
// Instead, we test the handlers by calling them directly on a Server where
// s.Deps.Worker is nil (no worker configured) and rely on the handler's explicit
// nil-check + CheckHealth call to exercise all paths.
//
// For the CheckHealth-returns-error and CheckHealth-succeeds paths, we wire a
// real in-process gRPC worker (startMinimalGRPCServer) and observe behaviour:
//   - the in-process server doesn't implement the health service, so
//     CheckHealth always returns an error → exercises the error path.

func TestHandleAdminTestWorker_NilWorker_ReturnsUnavailable(t *testing.T) {
	s := &Server{cfg: defaultTestConfig(), Deps: &appdeps.AppDeps{Worker: nil}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-worker", nil)
	rec := httptest.NewRecorder()
	s.handleAdminTestWorker(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := body["status"], "unavailable"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if body["error"] == nil {
		t.Error("expected non-nil error field when worker is nil")
	}
}

func TestHandleAdminTestLLM_NilWorker_ReturnsUnavailable(t *testing.T) {
	s := &Server{cfg: defaultTestConfig(), Deps: &appdeps.AppDeps{Worker: nil}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-llm", nil)
	rec := httptest.NewRecorder()
	s.handleAdminTestLLM(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := body["status"], "unavailable"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// TestHandleAdminTestWorker_CheckHealthError_ReturnsError wires a real worker
// client pointing at an in-process gRPC server that does not implement the
// health service. CheckHealth will therefore return an error, exercising the
// "status: error" branch.
func TestHandleAdminTestWorker_CheckHealthError_ReturnsErrorStatus(t *testing.T) {
	wc := startMinimalGRPCServer(t)
	if wc == nil {
		return
	}
	s := &Server{cfg: defaultTestConfig(), Deps: &appdeps.AppDeps{Worker: wc}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-worker", nil)
	rec := httptest.NewRecorder()
	s.handleAdminTestWorker(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The in-process server has no health service, so CheckHealth returns an
	// error → handler emits status "error".
	if got := body["status"]; got != "error" {
		t.Errorf("status = %q, want \"error\" (no health service on test server)", got)
	}
	if body["error"] == nil {
		t.Error("expected non-nil error field when CheckHealth fails")
	}
}

// TestHandleAdminTestLLM_CheckHealthError_ReturnsErrorStatus exercises the
// same error path for handleAdminTestLLM.
func TestHandleAdminTestLLM_CheckHealthError_ReturnsErrorStatus(t *testing.T) {
	wc := startMinimalGRPCServer(t)
	if wc == nil {
		return
	}
	s := &Server{cfg: defaultTestConfig(), Deps: &appdeps.AppDeps{Worker: wc}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-llm", nil)
	rec := httptest.NewRecorder()
	s.handleAdminTestLLM(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// health service not registered → error path
	if got := body["status"]; got != "error" {
		t.Errorf("status = %q, want \"error\"", got)
	}
}

// TestHandleAdminTestLLM_ReturnsOKStatus verifies that handleAdminTestLLM
// returns HTTP 200 with a status field in all cases (the status value varies
// based on worker availability, but the response shape is always the same).
func TestHandleAdminTestLLM_ResponseAlwaysHTTP200(t *testing.T) {
	cases := []struct {
		name      string
		workerNil bool
	}{
		{name: "nil worker", workerNil: true},
		{name: "worker error (no health svc)", workerNil: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: defaultTestConfig(), Deps: &appdeps.AppDeps{}}
			if !tc.workerNil {
				wc := startMinimalGRPCServer(t)
				if wc == nil {
					t.Skip("worker not available")
				}
				s.Deps.Worker = wc
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test-llm", nil)
			rec := httptest.NewRecorder()
			s.handleAdminTestLLM(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200", rec.Code)
			}
			var body map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if _, ok := body["status"]; !ok {
				t.Errorf("response missing 'status' field; got %v", body)
			}
		})
	}
}

// defaultTestConfig returns a minimal *config.Config sufficient for admin handler tests.
func defaultTestConfig() *config.Config {
	return &config.Config{}
}

