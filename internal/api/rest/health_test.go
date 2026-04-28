// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/health"
)

// --- fakes ---

type fakeDBPinger struct{ err error }

func (f *fakeDBPinger) Ping(_ context.Context) error { return f.err }

type fakeWorkerChecker struct {
	healthy bool
	err     error
}

func (f *fakeWorkerChecker) CheckHealth(_ context.Context) (bool, error) {
	return f.healthy, f.err
}

// newReadyzServer builds a minimal Server with a HealthChecker using the
// provided fakes. It avoids starting the full NewServer stack (which requires
// a live config, gqlgen, etc.).
func newReadyzServer(db health.DBPinger, wc health.WorkerChecker) *Server {
	hc := health.New(db, wc)
	return &Server{healthChecker: hc}
}

// --- tests ---

func TestHandleReadyz_BothHealthy(t *testing.T) {
	s := newReadyzServer(
		&fakeDBPinger{err: nil},
		&fakeWorkerChecker{healthy: true, err: nil},
	)
	rec, body := callReadyz(t, s)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if body.Status != "ready" {
		t.Errorf("status = %q, want ready", body.Status)
	}
	if body.Components["database"].Status != "healthy" {
		t.Errorf("database = %q, want healthy", body.Components["database"].Status)
	}
	if body.Components["worker"].Status != "healthy" {
		t.Errorf("worker = %q, want healthy", body.Components["worker"].Status)
	}
}

func TestHandleReadyz_DBUnreachable_Returns503(t *testing.T) {
	s := newReadyzServer(
		&fakeDBPinger{err: errors.New("connection refused")},
		&fakeWorkerChecker{healthy: true, err: nil},
	)
	rec, body := callReadyz(t, s)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if body.Status != "unavailable" {
		t.Errorf("status = %q, want unavailable", body.Status)
	}
	if body.Components["database"].Status != "unavailable" {
		t.Errorf("database = %q, want unavailable", body.Components["database"].Status)
	}
}

func TestHandleReadyz_WorkerUnreachable_Returns200Degraded(t *testing.T) {
	s := newReadyzServer(
		&fakeDBPinger{err: nil},
		&fakeWorkerChecker{healthy: false, err: errors.New("transport error")},
	)
	rec, body := callReadyz(t, s)

	// Worker degradation must not fail the readiness probe — Kubernetes should
	// keep routing traffic to the pod (the API itself is up).
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if body.Status != "degraded" {
		t.Errorf("status = %q, want degraded", body.Status)
	}
	if body.Components["worker"].Status != "unavailable" {
		t.Errorf("worker = %q, want unavailable", body.Components["worker"].Status)
	}
	// DB must still report healthy
	if body.Components["database"].Status != "healthy" {
		t.Errorf("database = %q, want healthy", body.Components["database"].Status)
	}
}

func TestHandleReadyz_NilHealthChecker_EmbeddedMode(t *testing.T) {
	// When no health checker is wired (embedded/in-memory mode), the handler
	// must still return 200 and "ready" without panicking.
	s := &Server{}
	rec, body := callReadyz(t, s)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if body.Status == "unavailable" {
		t.Errorf("status must not be unavailable in embedded mode, got %q", body.Status)
	}
	_ = body
}

// callReadyz is a helper that fires a GET /readyz against the server and
// parses the JSON response body.
func callReadyz(t *testing.T, s *Server) (*httptest.ResponseRecorder, readinessResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	s.handleReadyz(rec, req)

	var body readinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return rec, body
}
