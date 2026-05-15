// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Regression tests for CA-324 Fix C: dispatchDiscussThroughOrchestrator must
// never return an empty answer string. When the QA pipeline produces no text,
// the adapter surfaces an explicit human-readable message instead.

package graphql

import (
	"context"
	"strings"
	"testing"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

// testSynthesizer is a minimal qa.Synthesizer that returns a canned response.
type testSynthesizer struct {
	resp *reasoningv1.AnswerQuestionResponse
}

func (s *testSynthesizer) IsAvailable() bool { return true }
func (s *testSynthesizer) AnswerQuestion(
	_ context.Context, _, _ string,
	_ *reasoningv1.AnswerQuestionRequest,
) (*reasoningv1.AnswerQuestionResponse, error) {
	return s.resp, nil
}

// newDiscussOrchestrator builds a qa.Orchestrator wired to the given synth.
func newDiscussOrchestrator(synth qa.Synthesizer) *qa.Orchestrator {
	return qa.New(synth, nil, worker.NewLanes(), qa.DefaultConfig())
}

// newDiscussDispatcher builds a mutationResolver with an in-memory store seeded
// with a repo. Returns the resolver and the seeded repo ID.
func newDiscussDispatcher(t *testing.T, synth qa.Synthesizer) (*mutationResolver, string) {
	t.Helper()
	s := graph.NewStore()
	repo, err := s.CreateRepository(t.Context(), "test-repo", "/tmp/test-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	r := &mutationResolver{&Resolver{
		Deps:  &appdeps.AppDeps{QA: newDiscussOrchestrator(synth)},
		Store: s,
	}}
	return r, repo.ID
}

// TestDispatchDiscuss_EmptyAnswerWithFallbackSignal pins Fix C1:
// when the pipeline sets FallbackUsed and the worker returns no answer,
// the adapter must surface a message that includes the fallback reason
// rather than returning an empty string.
func TestDispatchDiscuss_EmptyAnswerWithFallbackSignal(t *testing.T) {
	t.Parallel()

	// Synthesizer returns an empty answer; FallbackUsed is set by the
	// orchestrator's "worker unavailable" path when synthesizer.IsAvailable()
	// returns false.  To exercise C1 directly we need a synth that
	// returns empty AND have the pipeline set FallbackUsed.  The simplest
	// path is to make the synthesizer unavailable (o.synthesizer.IsAvailable()
	// returns false) — the orchestrator sets FallbackUsed="worker_unavailable"
	// and returns an answer it already populated itself. That path doesn't
	// exercise C1 because the answer is non-empty.
	//
	// Instead, feed an empty answer from an available synth, which hits the
	// truly-silent path (C2). For C1, we verify via synthesizer returning
	// empty and ensuring the non-empty fallback message flows out.
	//
	// We test C1 indirectly via the fast path: synthesizer returns empty
	// answer but FallbackUsed has already been populated by the orchestrator
	// before the synthesis call (impossible in the current implementation
	// without a special synthesizer). The cleanest regression test for C1 is
	// therefore to verify the output when a valid non-empty FallbackUsed exists
	// in the AskResult — we do that through the synthesizer returning "" so the
	// orchestrator doesn't overwrite the answer, and FallbackUsed is set
	// through the synthesizer-unavailable gate.
	//
	// Practical approach: use an unavailable synthesizer so the orchestrator
	// itself sets FallbackUsed + Answer, then verify the non-empty message
	// comes out. That tests the orchestrator path; Fix C targets the *adapter*
	// layer below, so we must test the adapter's own guard separately.

	// C1 path via adapter: synthesizer returns empty answer, and we inject
	// a fake AskResult with FallbackUsed set. Since qa.Orchestrator is concrete
	// we cannot inject an AskResult directly. We use an unavailable synth to
	// force FallbackUsed="worker_unavailable" in the orchestrator, which also
	// sets a non-empty answer — confirming the path still emits a real string.
	unavailSynth := &unavailableSynth{}
	r, repoID := newDiscussDispatcher(t, unavailSynth)

	result, err := r.dispatchDiscussThroughOrchestrator(context.Background(), DiscussCodeInput{
		RepositoryID: repoID,
		Question:     "What does NewUUID return?",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer == "" {
		t.Error("Fix C: answer must never be empty; got empty string even on worker_unavailable path")
	}
	if !strings.Contains(result.Answer, "not available") && !strings.Contains(result.Answer, "worker") {
		// The orchestrator itself returns "The reasoning worker is not available right now…"
		// — verify the message is user-facing, not an internal stack trace.
		t.Errorf("expected human-readable unavailability message, got: %q", result.Answer)
	}
}

// unavailableSynth implements qa.Synthesizer with IsAvailable() == false.
type unavailableSynth struct{}

func (s *unavailableSynth) IsAvailable() bool { return false }
func (s *unavailableSynth) AnswerQuestion(
	_ context.Context, _, _ string,
	_ *reasoningv1.AnswerQuestionRequest,
) (*reasoningv1.AnswerQuestionResponse, error) {
	return &reasoningv1.AnswerQuestionResponse{}, nil
}

// TestDispatchDiscuss_EmptyAnswerSilentPath pins Fix C2:
// when the synthesizer returns an empty answer with no FallbackUsed signal,
// the adapter must surface "synthesis completed but returned an empty answer"
// rather than propagating the empty string to the caller.
func TestDispatchDiscuss_EmptyAnswerSilentPath(t *testing.T) {
	t.Parallel()

	// Synth that is available but returns an empty answer and empty usage —
	// the proto3 zero-value message that triggers the nil-usage path (Fix B)
	// and the silent-empty-answer path (Fix C2).
	emptySynth := &testSynthesizer{
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: "", // no answer
			// Usage omitted → nil from GetUsage()
		},
	}
	r, repoID := newDiscussDispatcher(t, emptySynth)

	result, err := r.dispatchDiscussThroughOrchestrator(context.Background(), DiscussCodeInput{
		RepositoryID: repoID,
		Question:     "What does NewUUID return?",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer == "" {
		t.Error("Fix C2: adapter must never surface an empty answer; got empty string")
	}
	if !strings.Contains(result.Answer, "empty") && !strings.Contains(result.Answer, "synthesis") {
		t.Errorf("Fix C2: expected 'synthesis completed but returned an empty answer' message, got: %q", result.Answer)
	}
}

// TestApplyEmptyAnswerGuard_C1_FallbackReason directly tests Fix C1 (CA-324 /
// CA-392): when Answer is empty and FallbackUsed is set, applyEmptyAnswerGuard
// must surface the reason in the answer text. This pins the guard independently
// of qa.Orchestrator (which previously prevented direct injection of an AskResult
// with FallbackUsed set and an empty Answer at the same time).
func TestApplyEmptyAnswerGuard_C1_FallbackReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		fallbackUsed string
		wantContains string
	}{
		{
			name:         "worker_unavailable",
			fallbackUsed: "worker_unavailable",
			wantContains: "worker_unavailable",
		},
		{
			name:         "understanding_not_ready",
			fallbackUsed: "understanding_not_ready",
			wantContains: "understanding_not_ready",
		},
		{
			name:         "synthesis_failed",
			fallbackUsed: "synthesis_failed",
			wantContains: "synthesis_failed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			res := &qa.AskResult{
				Diagnostics: qa.AskDiagnostics{FallbackUsed: c.fallbackUsed},
			}
			out := &DiscussionResult{Answer: "", References: []string{}}
			applyEmptyAnswerGuard(res, out)
			if out.Answer == "" {
				t.Fatal("C1: applyEmptyAnswerGuard must not leave Answer empty when FallbackUsed is set")
			}
			if !strings.Contains(out.Answer, c.wantContains) {
				t.Errorf("C1: expected answer to contain %q, got: %q", c.wantContains, out.Answer)
			}
		})
	}
}

// TestApplyEmptyAnswerGuard_C2_SilentPath directly tests Fix C2 (CA-324):
// when Answer is empty and FallbackUsed is empty, applyEmptyAnswerGuard must
// surface the generic "synthesis completed but returned an empty answer" message.
func TestApplyEmptyAnswerGuard_C2_SilentPath(t *testing.T) {
	t.Parallel()
	res := &qa.AskResult{
		Diagnostics: qa.AskDiagnostics{FallbackUsed: ""},
	}
	out := &DiscussionResult{Answer: "", References: []string{}}
	applyEmptyAnswerGuard(res, out)
	if out.Answer == "" {
		t.Fatal("C2: applyEmptyAnswerGuard must not leave Answer empty on silent path")
	}
	if !strings.Contains(out.Answer, "synthesis") {
		t.Errorf("C2: expected 'synthesis' in answer, got: %q", out.Answer)
	}
	if !strings.Contains(out.Answer, "empty") {
		t.Errorf("C2: expected 'empty' in answer, got: %q", out.Answer)
	}
}

// TestApplyEmptyAnswerGuard_PassThrough verifies that applyEmptyAnswerGuard is a
// no-op when the Answer is already non-empty — real answers must not be replaced.
func TestApplyEmptyAnswerGuard_PassThrough(t *testing.T) {
	t.Parallel()
	want := "A concrete, non-empty answer from the synthesizer."
	res := &qa.AskResult{
		Diagnostics: qa.AskDiagnostics{FallbackUsed: "understanding_partial"},
	}
	out := &DiscussionResult{Answer: want, References: []string{}}
	applyEmptyAnswerGuard(res, out)
	if out.Answer != want {
		t.Errorf("PassThrough: real answer was replaced; got %q, want %q", out.Answer, want)
	}
}

// TestDispatchDiscuss_RealAnswerPassedThrough verifies that when the synthesizer
// returns a real answer, it is not replaced by the Fix C fallback messages.
func TestDispatchDiscuss_RealAnswerPassedThrough(t *testing.T) {
	t.Parallel()

	want := "NewUUID returns a new randomly-generated UUID."
	goodSynth := &testSynthesizer{
		resp: &reasoningv1.AnswerQuestionResponse{
			Answer: want,
		},
	}
	r, repoID := newDiscussDispatcher(t, goodSynth)

	result, err := r.dispatchDiscussThroughOrchestrator(context.Background(), DiscussCodeInput{
		RepositoryID: repoID,
		Question:     "What does NewUUID return?",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != want {
		t.Errorf("real answer was replaced; got %q, want %q", result.Answer, want)
	}
}
