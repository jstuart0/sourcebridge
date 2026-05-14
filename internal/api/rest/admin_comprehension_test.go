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

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
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
		Deps: &appdeps.AppDeps{
			ComprehensionStore: comprehension.NewMemStore(),
			Orchestrator:       orch,
			Flags:              flags,
		},
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
	if got := s.Deps.Orchestrator.MaxConcurrency(); got != 5 {
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
	_ = s.Deps.ComprehensionStore.SetModelCapabilities(t.Context(), &comprehension.ModelCapabilities{
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

// TestAdminComprehension_ModelIDLengthCap_UpdateRejects600Chars verifies xander M1:
// modelId longer than 512 characters is rejected with 400 on PUT, GET, and DELETE.
func TestAdminComprehension_ModelIDLengthCap_UpdateRejects600Chars(t *testing.T) {
	s := newComprehensionTestServer(t, featureflags.Flags{})

	longID := strings.Repeat("a", 600)
	body, _ := json.Marshal(map[string]any{
		"modelId":         longID,
		"provider":        "test",
		"qualityGateTier": "local",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/comprehension/models/"+longID, bytes.NewBuffer(body))
	w := httptest.NewRecorder()

	s.handleUpdateModelCapabilities(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for 600-char modelId, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp["error"], "512") {
		t.Errorf("error message should mention 512-char limit, got: %q", resp["error"])
	}
}

// TestAdminComprehension_ModelIDLengthCap_GetAndDeleteReject verifies the
// 512-char cap is enforced on GET and DELETE URL params as well.
func TestAdminComprehension_ModelIDLengthCap_GetAndDeleteReject(t *testing.T) {
	longID := strings.Repeat("b", 600)

	for _, tc := range []struct {
		name    string
		method  string
		handler func(s *Server) http.HandlerFunc
	}{
		{
			name:   "GET",
			method: http.MethodGet,
			handler: func(s *Server) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) { s.handleGetModelCapabilities(w, r) }
			},
		},
		{
			name:   "DELETE",
			method: http.MethodDelete,
			handler: func(s *Server) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) { s.handleDeleteModelCapabilities(w, r) }
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newComprehensionTestServer(t, featureflags.Flags{})
			// Wire a chi router so URLParam is populated.
			router := chi.NewRouter()
			path := "/api/v1/admin/comprehension/models/{modelId}"
			switch tc.method {
			case http.MethodGet:
				router.Get(path, tc.handler(s))
			case http.MethodDelete:
				router.Delete(path, tc.handler(s))
			}

			req := httptest.NewRequest(tc.method, "/api/v1/admin/comprehension/models/"+longID, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400 for 600-char modelId, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestAdminComprehension_TierNormalization verifies that uppercase / whitespace
// tier values are normalized before store write (MED #2 fix).
func TestAdminComprehension_TierNormalization(t *testing.T) {
	for _, tc := range []struct {
		input    string
		wantTier string
	}{
		{"LOCAL", "local"},
		{"FRONTIER", "frontier"},
		{"MID", "mid"},
		{" local ", "local"},
		{" Frontier\t", "frontier"},
	} {
		t.Run("tier="+tc.input, func(t *testing.T) {
			s := newComprehensionTestServer(t, featureflags.Flags{})

			body, _ := json.Marshal(map[string]any{
				"modelId":         "test-model",
				"provider":        "test",
				"qualityGateTier": tc.input,
				"source":          "manual",
			})
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/comprehension/models/test-model", bytes.NewBuffer(body))
			w := httptest.NewRecorder()

			s.handleUpdateModelCapabilities(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("tier=%q: expected 200, got %d: %s", tc.input, w.Code, w.Body.String())
			}
			// Verify the store holds the normalized value.
			mc, err := s.Deps.ComprehensionStore.GetModelCapabilities(t.Context(), "test-model")
			if err != nil {
				t.Fatalf("store.GetModelCapabilities: %v", err)
			}
			if mc == nil {
				t.Fatal("model not found in store after PUT")
			}
			if string(mc.QualityGateTier) != tc.wantTier {
				t.Errorf("stored tier = %q, want %q", mc.QualityGateTier, tc.wantTier)
			}
		})
	}
}

// TestAdminComprehension_ModelIDNormalization verifies that mixed-case / padded
// modelId values are normalized to lowercase+trim before store write (MED #3 fix).
func TestAdminComprehension_ModelIDNormalization(t *testing.T) {
	s := newComprehensionTestServer(t, featureflags.Flags{})

	body, _ := json.Marshal(map[string]any{
		"modelId":  "  Qwen3:32B  ",
		"provider": "ollama",
		"source":   "manual",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/comprehension/models/Qwen3:32B", bytes.NewBuffer(body))
	w := httptest.NewRecorder()

	s.handleUpdateModelCapabilities(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Should be stored under normalized key.
	mc, err := s.Deps.ComprehensionStore.GetModelCapabilities(t.Context(), "qwen3:32b")
	if err != nil {
		t.Fatalf("store.GetModelCapabilities: %v", err)
	}
	if mc == nil {
		t.Fatal("model not found under normalized key 'qwen3:32b' after PUT with 'Qwen3:32B'")
	}
	if mc.ModelID != "qwen3:32b" {
		t.Errorf("stored ModelID = %q, want %q", mc.ModelID, "qwen3:32b")
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
	if got := s.Deps.Orchestrator.MaxConcurrency(); got != 2 {
		t.Fatalf("expected orchestrator max concurrency to remain 2, got %d", got)
	}
}
