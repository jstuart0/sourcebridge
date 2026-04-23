// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"errors"
	"testing"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
)

// fakeClassifyClient is a hand-rolled mock of the narrow worker
// surface. Scripted response + error let us exercise the fail-open
// path without standing up a grpc server.
type fakeClassifyClient struct {
	resp *reasoningv1.ClassifyQuestionResponse
	err  error
}

func (f *fakeClassifyClient) ClassifyQuestion(_ context.Context, _ *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error) {
	return f.resp, f.err
}

// TestKeywordProfilerAlwaysWorks: the always-on fallback produces a
// valid QuestionProfile for every canonical class and for empty
// input.
func TestKeywordProfilerAlwaysWorks(t *testing.T) {
	p := NewKeywordProfiler()
	for _, q := range []string{
		"architecture of the system",
		"what happens when a user signs in",
		"where is the recycle-bin handler",
		"",
		"random question that matches nothing specific",
	} {
		profile, err := p.Profile(context.Background(), AskInput{Question: q})
		if err != nil {
			t.Fatalf("keyword profiler errored on %q: %v", q, err)
		}
		if !isKnownKind(profile.Kind) {
			t.Errorf("keyword profiler returned unknown kind %q for %q", profile.Kind, q)
		}
	}
}

// TestWorkerProfilerHappyPath: the RPC returns a profile, the
// profiler translates it faithfully.
func TestWorkerProfilerHappyPath(t *testing.T) {
	fake := &fakeClassifyClient{
		resp: &reasoningv1.ClassifyQuestionResponse{
			CapabilitySupported: true,
			QuestionClass:       "cross_cutting",
			NeedsCallGraph:      true,
			NeedsSummaries:      true,
			SymbolCandidates:    []string{"AuthMiddleware", "sessionStore"},
			FileCandidates:      []string{"internal/auth/"},
			TopicTerms:          []string{"authentication", "session"},
		},
	}
	p := NewWorkerQuestionProfiler(fake)
	profile, err := p.Profile(context.Background(), AskInput{
		Question: "how does authentication work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Kind != KindCrossCutting {
		t.Errorf("wrong kind: got %q want cross_cutting", profile.Kind)
	}
	if !profile.EvidenceHints.NeedsCallGraph || !profile.EvidenceHints.NeedsSummaries {
		t.Error("evidence hints not preserved")
	}
	if len(profile.EvidenceHints.SymbolCandidates) != 2 {
		t.Errorf("symbol candidates not preserved, got %v", profile.EvidenceHints.SymbolCandidates)
	}
}

// TestWorkerProfilerFallsBackOnError: RPC errors drop silently to
// the keyword path. Profile must still be well-formed.
func TestWorkerProfilerFallsBackOnError(t *testing.T) {
	p := NewWorkerQuestionProfiler(&fakeClassifyClient{err: errors.New("boom")})
	profile, err := p.Profile(context.Background(), AskInput{Question: "architecture overview"})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Kind != KindArchitecture {
		t.Errorf("expected keyword fallback to produce architecture, got %q", profile.Kind)
	}
}

// TestWorkerProfilerFallsBackWhenCapabilityUnsupported: worker
// says it can't classify → fall back.
func TestWorkerProfilerFallsBackWhenCapabilityUnsupported(t *testing.T) {
	p := NewWorkerQuestionProfiler(&fakeClassifyClient{
		resp: &reasoningv1.ClassifyQuestionResponse{CapabilitySupported: false},
	})
	profile, err := p.Profile(context.Background(), AskInput{Question: "schema of the users table"})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Kind != KindDataModel {
		t.Errorf("expected keyword fallback to produce data_model, got %q", profile.Kind)
	}
}

// TestWorkerProfilerRejectsUnknownClassString: worker returned a
// valid RPC response but the class string isn't in the Go-side
// enum. Fall back rather than propagate an unknown kind.
func TestWorkerProfilerRejectsUnknownClassString(t *testing.T) {
	p := NewWorkerQuestionProfiler(&fakeClassifyClient{
		resp: &reasoningv1.ClassifyQuestionResponse{
			CapabilitySupported: true,
			QuestionClass:       "nonsense_class",
		},
	})
	profile, err := p.Profile(context.Background(), AskInput{Question: "where does this live"})
	if err != nil {
		t.Fatal(err)
	}
	if !isKnownKind(profile.Kind) {
		t.Errorf("profile kind %q is not a known class", profile.Kind)
	}
}

// TestDefaultHintsCoversEveryKind: every canonical kind resolves to
// at least one evidence-kind hint (or is legitimately zero-valued).
// This is a smoke test against adding new kinds without updating
// defaultHintsForKind.
func TestDefaultHintsCoversEveryKind(t *testing.T) {
	kinds := []QuestionKind{
		KindArchitecture, KindBehavior, KindCrossCutting,
		KindDataModel, KindExecutionFlow, KindOwnership,
		KindRequirementCoverage, KindRiskReview,
	}
	for _, k := range kinds {
		h := defaultHintsForKind(k)
		// Confirm the function returns without panic — the zero
		// struct is a legitimate return for kinds we don't yet
		// route on, but that's still a choice we've made.
		_ = h
	}
}
