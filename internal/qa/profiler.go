// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package qa

import (
	"context"
	"fmt"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"

	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// QuestionProfiler is the narrow surface the orchestrator uses to
// produce a QuestionProfile. Implementations:
//
//   - keywordProfiler: regex-only, never fails. Always wired as a
//     fallback even when the LLM profiler is on.
//   - WorkerQuestionProfiler: calls the ClassifyQuestion RPC. Used
//     when SmartClassifierEnabled=true and the provider supports
//     it (probed via capability_supported on the response).
//
// Profile must return a well-formed QuestionProfile on every call;
// errors are unrecoverable (nil worker, context cancel).
type QuestionProfiler interface {
	Profile(ctx context.Context, in AskInput) (QuestionProfile, error)
}

// keywordProfiler is the always-available fallback. Wraps
// ClassifyQuestion + defaultHintsForKind.
type keywordProfiler struct{}

func (keywordProfiler) Profile(_ context.Context, in AskInput) (QuestionProfile, error) {
	return DefaultProfile(in.Question), nil
}

// NewKeywordProfiler returns the no-LLM profiler.
func NewKeywordProfiler() QuestionProfiler { return keywordProfiler{} }

// questionProfilerClient is the narrow worker surface used by the
// LLM-backed profiler. Decoupled from *llmcall.Caller so tests can
// inject a fake.
//
// Slice 2 of the workspace-LLM-source-of-truth plan: signature now
// takes (repoID, op) so the underlying *llmcall.Caller can resolve
// workspace-saved settings and attach them to gRPC metadata.
type questionProfilerClient interface {
	ClassifyQuestion(ctx context.Context, repoID, op string, req *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error)
}

// WorkerQuestionProfiler dispatches the ClassifyQuestion RPC and
// translates the response into QuestionProfile. Wraps a keyword
// profiler so the fail-open path is always a well-formed profile.
type WorkerQuestionProfiler struct {
	worker   questionProfilerClient
	fallback QuestionProfiler
}

// NewWorkerQuestionProfiler constructs the LLM-backed profiler.
// When `worker` is nil, every Profile call delegates to the keyword
// fallback.
func NewWorkerQuestionProfiler(worker questionProfilerClient) *WorkerQuestionProfiler {
	return &WorkerQuestionProfiler{
		worker:   worker,
		fallback: keywordProfiler{},
	}
}

// Profile runs the LLM classifier. On any error — nil worker,
// capability_supported=false, RPC failure, parse trouble — returns
// the keyword profile. Never returns an error in practice; the
// error return is kept for interface symmetry.
func (p *WorkerQuestionProfiler) Profile(ctx context.Context, in AskInput) (QuestionProfile, error) {
	if p == nil || p.worker == nil {
		return p.fallback.Profile(ctx, in)
	}

	// llmcall:allow — p.worker is the questionProfilerClient interface,
	// satisfied in production by *llmcall.Caller.
	resp, err := p.worker.ClassifyQuestion(ctx, in.RepositoryID, resolution.OpQAClassify, &reasoningv1.ClassifyQuestionRequest{
		RepositoryId: in.RepositoryID,
		Question:     in.Question,
		FilePath:     in.FilePath,
		PinnedCode:   in.Code,
	})
	if err != nil || resp == nil || !resp.GetCapabilitySupported() {
		return p.fallback.Profile(ctx, in)
	}

	kind := QuestionKind(resp.GetQuestionClass())
	if !isKnownKind(kind) {
		// Unknown class string from the worker — fall back so the
		// orchestrator sees a valid kind.
		return p.fallback.Profile(ctx, in)
	}

	return QuestionProfile{
		Kind: kind,
		EvidenceHints: EvidenceKindHints{
			NeedsCallGraph:    resp.GetNeedsCallGraph(),
			NeedsRequirements: resp.GetNeedsRequirements(),
			NeedsTests:        resp.GetNeedsTests(),
			NeedsSummaries:    resp.GetNeedsSummaries(),
			SymbolCandidates:  resp.GetSymbolCandidates(),
			FileCandidates:    resp.GetFileCandidates(),
			TopicTerms:        resp.GetTopicTerms(),
		},
	}, nil
}

// isKnownKind guards against unknown strings from the worker.
// Keeps the Go side authoritative about which classes exist.
func isKnownKind(k QuestionKind) bool {
	switch k {
	case KindArchitecture,
		KindExecutionFlow,
		KindRequirementCoverage,
		KindOwnership,
		KindDataModel,
		KindRiskReview,
		KindBehavior,
		KindCrossCutting:
		return true
	}
	return false
}

// compile-time check that the worker client satisfies the narrow
// interface we need. Catches drift if the generated proto changes.
var _ questionProfilerClient = (*fakeProfilerClient)(nil) //nolint:unused,deadcode

// fakeProfilerClient is declared here only to keep the compile-time
// check above honest in places where we don't import the real
// worker client. Tests supply their own fakes.
type fakeProfilerClient struct{}

func (fakeProfilerClient) ClassifyQuestion(_ context.Context, _, _ string, _ *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error) {
	return nil, fmt.Errorf("fake")
}
