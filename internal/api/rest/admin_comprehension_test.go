// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/featureflags"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

func newComprehensionTestServer(t *testing.T, flags featureflags.Flags) *Server {
	t.Helper()
	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 2})
	t.Cleanup(func() { _ = orch.Shutdown(time.Second) })
	return &Server{
		comprehensionStore: comprehension.NewMemStore(),
		orchestrator:       orch,
		flags:              flags,
	}
}

func TestHandleUpdateComprehensionSettingsReconfiguresOrchestratorWhenEnabled(t *testing.T) {
	s := newComprehensionTestServer(t, featureflags.Flags{RuntimeReconfigure: true})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/comprehension/settings", bytes.NewBufferString(`{"scopeType":"workspace","scopeKey":"default","maxConcurrency":5}`))
	w := httptest.NewRecorder()
	s.handleUpdateComprehensionSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := s.orchestrator.MaxConcurrency(); got != 5 {
		t.Fatalf("expected orchestrator max concurrency 5, got %d", got)
	}
}

func TestHandleUpdateComprehensionSettingsLeavesOrchestratorUnchangedWhenDisabled(t *testing.T) {
	s := newComprehensionTestServer(t, featureflags.Flags{})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/comprehension/settings", bytes.NewBufferString(`{"scopeType":"workspace","scopeKey":"default","maxConcurrency":5}`))
	w := httptest.NewRecorder()
	s.handleUpdateComprehensionSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := s.orchestrator.MaxConcurrency(); got != 2 {
		t.Fatalf("expected orchestrator max concurrency to remain 2, got %d", got)
	}
}
