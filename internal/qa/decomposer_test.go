// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"errors"
	"testing"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
)

type fakeDecomposerClient struct {
	resp *reasoningv1.DecomposeQuestionResponse
	err  error
}

func (f *fakeDecomposerClient) DecomposeQuestion(_ context.Context, _, _ string, _ *reasoningv1.DecomposeQuestionRequest) (*reasoningv1.DecomposeQuestionResponse, error) {
	return f.resp, f.err
}

func TestDecomposerReturnsSubQuestions(t *testing.T) {
	fake := &fakeDecomposerClient{
		resp: &reasoningv1.DecomposeQuestionResponse{
			CapabilitySupported: true,
			SubQuestions: []string{
				"Where are credentials validated?",
				"How are sessions issued?",
				"Where is the session token checked?",
			},
		},
	}
	d := NewWorkerDecomposer(fake)
	subs, err := d.Decompose(context.Background(),
		AskInput{RepositoryID: "r", Question: "How does auth work?"},
		KindCrossCutting,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 {
		t.Fatalf("expected 3 sub-questions, got %d", len(subs))
	}
}

func TestDecomposerFallsBackOnError(t *testing.T) {
	d := NewWorkerDecomposer(&fakeDecomposerClient{err: errors.New("boom")})
	subs, err := d.Decompose(context.Background(),
		AskInput{Question: "what does X do"}, KindBehavior,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 0 {
		t.Errorf("expected empty sub-questions on error, got %v", subs)
	}
}

func TestDecomposerFallsBackWhenUnsupported(t *testing.T) {
	d := NewWorkerDecomposer(&fakeDecomposerClient{
		resp: &reasoningv1.DecomposeQuestionResponse{CapabilitySupported: false},
	})
	subs, err := d.Decompose(context.Background(),
		AskInput{Question: "what"}, KindBehavior,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 0 {
		t.Errorf("expected empty sub-questions when unsupported, got %v", subs)
	}
}

func TestIsDecomposableKind(t *testing.T) {
	// Post-Phase-5: architecture is the only class that showed a
	// decomposition-driven quality win large enough to justify the
	// latency cost. Keep the gate tight until the decomposer prompt
	// is revised for cross_cutting and execution_flow.
	if !isDecomposableKind(KindArchitecture) {
		t.Error("expected architecture to be decomposable")
	}
	for _, k := range []QuestionKind{
		KindCrossCutting, KindExecutionFlow, KindBehavior,
		KindOwnership, KindDataModel, KindRequirementCoverage,
		KindRiskReview,
	} {
		if isDecomposableKind(k) {
			t.Errorf("expected %s to NOT be decomposable after Phase-5 narrowing", k)
		}
	}
}

func TestHandleFromReferenceAndPath(t *testing.T) {
	// FileRange handle reconstruction.
	got := handleFromReference(AskReference{
		Kind: RefKindFileRange,
		FileRange: &FileRangeRef{
			FilePath:  "a/b.go",
			StartLine: 10,
			EndLine:   20,
		},
	})
	if got != "a/b.go:10-20" {
		t.Errorf("got %q, want a/b.go:10-20", got)
	}

	// Symbol handle.
	got = handleFromReference(AskReference{
		Kind:   RefKindSymbol,
		Symbol: &SymbolRef{SymbolID: "abc"},
	})
	if got != "sym_abc" {
		t.Errorf("got %q, want sym_abc", got)
	}

	// Already-prefixed symbol handle.
	got = handleFromReference(AskReference{
		Kind:   RefKindSymbol,
		Symbol: &SymbolRef{SymbolID: "sym_xyz"},
	})
	if got != "sym_xyz" {
		t.Errorf("got %q, want sym_xyz", got)
	}

	// handlePath strips the :start-end suffix.
	if handlePath("a/b.go:10-20") != "a/b.go" {
		t.Errorf("handlePath didn't strip range suffix")
	}
	if handlePath("sym_abc") != "sym_abc" {
		t.Errorf("handlePath should passthrough symbols")
	}
}
