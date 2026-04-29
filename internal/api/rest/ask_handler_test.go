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
func newAskTestServer(t *testing.T, enabled bool, orch *qa.Orchestrator) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.QA.ServerSideEnabled = enabled
	cfg.QA.QuestionMaxBytes = 4096
	return &Server{cfg: cfg, qaOrchestrator: orch}
}

func TestHandleAsk_FlagOffReturns503(t *testing.T) {
	s := newAskTestServer(t, false, nil)
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
	s := newAskTestServer(t, true, orch)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask",
		strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleAsk_MissingRepo(t *testing.T) {
	orch := qa.New(&stubSynth{available: true}, nil, nil, qa.DefaultConfig())
	s := newAskTestServer(t, true, orch)
	body, _ := json.Marshal(askRequest{Question: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ask", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAsk(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
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
	s := newAskTestServer(t, true, orch)

	body, _ := json.Marshal(askRequest{
		RepositoryID: "repo-1",
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
