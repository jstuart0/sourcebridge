// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"

	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// Decomposer splits a multi-hop question into sub-questions.
// Implementations return an empty slice when the question is
// already atomic or the decomposer isn't available — callers then
// run the single-loop path.
type Decomposer interface {
	Decompose(ctx context.Context, in AskInput, kind QuestionKind) ([]string, error)
}

// decomposerClient is the narrow worker surface used by
// WorkerDecomposer. Isolated so tests can inject a fake.
//
// Slice 2 of the workspace-LLM-source-of-truth plan: signature now
// takes (repoID, op) so the underlying *llmcall.Caller can resolve
// workspace-saved settings and attach them to gRPC metadata.
type decomposerClient interface {
	DecomposeQuestion(ctx context.Context, repoID, op string, req *reasoningv1.DecomposeQuestionRequest) (*reasoningv1.DecomposeQuestionResponse, error)
}

// WorkerDecomposer calls the worker's DecomposeQuestion RPC.
type WorkerDecomposer struct {
	worker decomposerClient
}

// NewWorkerDecomposer constructs the adapter. Nil worker means no
// decomposition — Decompose returns an empty slice every call,
// which pushes every question to the single-loop path.
func NewWorkerDecomposer(worker decomposerClient) *WorkerDecomposer {
	return &WorkerDecomposer{worker: worker}
}

// Decompose runs the RPC and maps the response. Empty slice +
// nil error means "run single-loop".
func (d *WorkerDecomposer) Decompose(ctx context.Context, in AskInput, kind QuestionKind) ([]string, error) {
	if d == nil || d.worker == nil {
		return nil, nil
	}
	// llmcall:allow — d.worker is the decomposerClient interface,
	// satisfied in production by *llmcall.Caller. Lint heuristic
	// matches the field name; receiver type is llmcall-aware.
	resp, err := d.worker.DecomposeQuestion(ctx, in.RepositoryID, resolution.OpQADecompose, &reasoningv1.DecomposeQuestionRequest{
		RepositoryId:    in.RepositoryID,
		Question:        in.Question,
		QuestionClass:   string(kind),
		MaxSubQuestions: 4,
	})
	if err != nil || resp == nil || !resp.GetCapabilitySupported() {
		return nil, nil
	}
	return resp.GetSubQuestions(), nil
}

// decomposedSynthesizer is the narrow worker surface used by the
// orchestrator to call SynthesizeDecomposedAnswer. Separate
// interface keeps the decomposer interface small.
//
// Slice 2: signature now takes (repoID, op).
type decomposedSynthesizer interface {
	SynthesizeDecomposedAnswer(ctx context.Context, repoID, op string, req *reasoningv1.SynthesizeDecomposedAnswerRequest) (*reasoningv1.SynthesizeDecomposedAnswerResponse, error)
}
