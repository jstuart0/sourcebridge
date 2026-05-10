// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/config"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/qa"
)

type stubSynth struct {
	available bool
	resp      *reasoningv1.AnswerQuestionResponse
	err       error
}

func (s *stubSynth) IsAvailable() bool { return s.available }
func (s *stubSynth) AnswerQuestion(_ context.Context, _, _ string, _ *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	return s.resp, s.err
}

// newAskTestServer produces a Server instance with only the fields the
// handleAsk path touches. Avoids standing up the full NewServer stack.
// store may be nil for tests that do not exercise the repo-access path
// (e.g. flag-off, bad-JSON checks that return before reaching checkRepoAccess).
func newAskTestServer(t *testing.T, enabled bool, orch *qa.Orchestrator, store *graphstore.Store) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.QA.ServerSideEnabled = enabled
	cfg.QA.QuestionMaxBytes = 4096
	return &Server{cfg: cfg, qaOrchestrator: orch, store: store}
}

func TestHandleAsk_FlagOffReturns503(t *testing.T) {
	s := newAskTestServer(t, false, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask",
		strings.NewReader(`{"repositoryId":"r","question":"q"}`))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", rec.Code)
	}
}

func TestHandleAsk_BadJSON(t *testing.T) {
	orch := qa.New(&stubSynth{available: true}, nil, nil, qa.DefaultConfig())
	s := newAskTestServer(t, true, orch, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask",
		strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

// TestHandleAsk_MissingRepo verifies that an empty repositoryId is rejected.
// The checkRepoAccess guard fires before the orchestrator, so the response is
// 403 (forbidden / no access) rather than 400 (bad input).
func TestHandleAsk_MissingRepo(t *testing.T) {
	orch := qa.New(&stubSynth{available: true}, nil, nil, qa.DefaultConfig())
	s := newAskTestServer(t, true, orch, graphstore.NewStore())
	body, _ := json.Marshal(askRequest{Question: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403 (empty repositoryId blocked by repo-access check)", rec.Code)
	}
}

func TestHandleAsk_HappyPath(t *testing.T) {
	synth := &stubSynth{
		available: true,
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "42",
			Usage:  &commonv1.LLMUsage{Model: "m", InputTokens: 1, OutputTokens: 2},
		},
	}
	orch := qa.New(synth, nil, nil, qa.DefaultConfig())

	store := graphstore.NewStore()
	repo, err := store.CreateRepository(t.Context(), "test-repo", "/tmp/test")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	s := newAskTestServer(t, true, orch, store)

	body, _ := json.Marshal(askRequest{
		RepositoryID: repo.ID,
		Question:     "What is the answer?",
		Mode:         "fast",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var out qa.AskResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if out.Answer != "42" {
		t.Errorf("answer = %q", out.Answer)
	}
	if out.Diagnostics.QuestionType == "" {
		t.Errorf("diagnostics not populated: %+v", out.Diagnostics)
	}
}

// TestAskHandlerRequiresRepoAccess confirms that a caller supplying a
// repositoryId they cannot see (not present in the store) receives 403.
// This is the SEC-5 regression guard: cross-repo data leak via QA reasoning.
func TestAskHandlerRequiresRepoAccess(t *testing.T) {
	synth := &stubSynth{available: true}
	orch := qa.New(synth, nil, nil, qa.DefaultConfig())

	// Store has no repositories — any repositoryId is unknown to the server.
	s := newAskTestServer(t, true, orch, graphstore.NewStore())

	body, _ := json.Marshal(askRequest{
		RepositoryID: "repo-other-tenant",
		Question:     "What secrets does this repo contain?",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("SEC-5: expected 403 for inaccessible repo, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestAskHandlerAllowsAccessibleRepo confirms that a caller supplying a
// repositoryId that IS present in the store (accessible) reaches the
// orchestrator and receives a successful response.
func TestAskHandlerAllowsAccessibleRepo(t *testing.T) {
	synth := &stubSynth{
		available: true,
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "found it",
			Usage:  &commonv1.LLMUsage{Model: "m", InputTokens: 1, OutputTokens: 1},
		},
	}
	orch := qa.New(synth, nil, nil, qa.DefaultConfig())

	store := graphstore.NewStore()
	repo, err := store.CreateRepository(t.Context(), "my-repo", "/src/my-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	s := newAskTestServer(t, true, orch, store)

	body, _ := json.Marshal(askRequest{
		RepositoryID: repo.ID,
		Question:     "How does auth work?",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("SEC-5: expected 200 for accessible repo, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var out qa.AskResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Answer != "found it" {
		t.Errorf("answer = %q, want %q", out.Answer, "found it")
	}
}
