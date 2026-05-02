// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

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

// ---------------------------------------------------------------------------
// Model capabilities tier-validation tests (CA-150 Phase 3a)
// ---------------------------------------------------------------------------

func TestAdminComprehension_RejectsInvalidTier(t *testing.T) {
	s := newComprehensionTestServer(t, featureflags.Flags{})

	body := `{"modelId":"gpt-4o","provider":"openai","qualityGateTier":"premium"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/comprehension/models/gpt-4o", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	// chi router not wired in unit tests — call handler directly.
	s.handleUpdateModelCapabilities(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid tier, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp["error"], "qualityGateTier") {
		t.Errorf("error message should mention qualityGateTier, got: %q", resp["error"])
	}
}

func TestAdminComprehension_AcceptsValidTiers(t *testing.T) {
	for _, tier := range []string{"frontier", "mid", "local", ""} {
		t.Run("tier="+tier, func(t *testing.T) {
			s := newComprehensionTestServer(t, featureflags.Flags{})

			body, _ := json.Marshal(map[string]any{
				"modelId":         "test-model",
				"provider":        "test",
				"qualityGateTier": tier,
				"source":          "builtin",
			})
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/comprehension/models/test-model", bytes.NewBuffer(body))
			w := httptest.NewRecorder()

			s.handleUpdateModelCapabilities(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("tier=%q: expected 200, got %d: %s", tier, w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminComprehension_MissingTierDefaultsToEmpty(t *testing.T) {
	s := newComprehensionTestServer(t, featureflags.Flags{})

	// No qualityGateTier field — should default to "" (TierUnknown) and succeed.
	body := `{"modelId":"llama3:latest","provider":"ollama","source":"builtin"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/comprehension/models/llama3:latest", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	s.handleUpdateModelCapabilities(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for missing tier, got %d: %s", w.Code, w.Body.String())
	}

	var mc comprehension.ModelCapabilities
	if err := json.NewDecoder(w.Body).Decode(&mc); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if mc.QualityGateTier != "" {
		t.Errorf("expected empty tier, got %q", mc.QualityGateTier)
	}
}

func TestAdminComprehension_GetModelCapabilities_RouteParam(t *testing.T) {
	s := newComprehensionTestServer(t, featureflags.Flags{})

	// Pre-seed the store.
	_ = s.comprehensionStore.SetModelCapabilities(&comprehension.ModelCapabilities{
		ModelID:  "claude-sonnet-4-6",
		Provider: "anthropic",
		Source:   "builtin",
	})

	// Wire a minimal chi router so URLParam works.
	router := chi.NewRouter()
	router.Get("/api/v1/admin/comprehension/models/{modelId}", s.handleGetModelCapabilities)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/comprehension/models/claude-sonnet-4-6", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
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
