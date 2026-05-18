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
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
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
	return &Server{cfg: cfg, store: store, Deps: &appdeps.AppDeps{QA: orch}}
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

// TestHandleAsk_MissingRepo verifies that omitting both repositoryId and
// repository_id returns 400 with an actionable message. Previously this
// fell through to the tenant-filter gate and returned a misleading 403.
func TestHandleAsk_MissingRepo(t *testing.T) {
	orch := qa.New(&stubSynth{available: true}, nil, nil, qa.DefaultConfig())
	s := newAskTestServer(t, true, orch, graphstore.NewStore())
	body, _ := json.Marshal(askRequest{Question: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 (empty repositoryId returns bad-request, not forbidden)", rec.Code)
	}
}

// TestHandleAsk_SnakeCaseRepositoryID verifies that the snake_case alias
// repository_id is accepted and resolves correctly. REST clients using the
// conventional Go/REST naming convention should not need to use camelCase.
func TestHandleAsk_SnakeCaseRepositoryID(t *testing.T) {
	synth := &stubSynth{
		available: true,
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "snake works",
			Usage:  &commonv1.LLMUsage{Model: "m", InputTokens: 1, OutputTokens: 1},
		},
	}
	orch := qa.New(synth, nil, nil, qa.DefaultConfig())

	store := graphstore.NewStore()
	repo, err := store.CreateRepository(t.Context(), "snake-repo", "/src/snake-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	s := newAskTestServer(t, true, orch, store)

	// Use raw JSON with snake_case key — cannot use askRequest struct here
	// because it would marshal to camelCase.
	body := []byte(`{"repository_id":"` + repo.ID + `","question":"does snake work?"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("snake_case repository_id: code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out qa.AskResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Answer != "snake works" {
		t.Errorf("answer = %q, want %q", out.Answer, "snake works")
	}
}

// TestHandleAsk_CamelTakesPrecedenceOverSnake verifies that when both
// repositoryId and repository_id are present, camelCase wins.
func TestHandleAsk_CamelTakesPrecedenceOverSnake(t *testing.T) {
	synth := &stubSynth{
		available: true,
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "camel wins",
			Usage:  &commonv1.LLMUsage{Model: "m", InputTokens: 1, OutputTokens: 1},
		},
	}
	orch := qa.New(synth, nil, nil, qa.DefaultConfig())

	store := graphstore.NewStore()
	repo, err := store.CreateRepository(t.Context(), "camel-repo", "/src/camel-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	s := newAskTestServer(t, true, orch, store)

	// Supply camelCase with a valid ID and snake_case with a bogus ID.
	// The handler should use the camelCase value (resolving to the real repo).
	body := []byte(`{"repositoryId":"` + repo.ID + `","repository_id":"bogus-id","question":"q"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("camel-over-snake precedence: code = %d, body = %s", rec.Code, rec.Body.String())
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
