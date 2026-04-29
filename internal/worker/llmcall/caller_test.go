// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package llmcall

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/metadata"

	enterprisev1 "github.com/sourcebridge/sourcebridge/gen/go/enterprise/v1"
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	requirementsv1 "github.com/sourcebridge/sourcebridge/gen/go/requirements/v1"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// fakeWorker captures the outgoing context for the most recent call so
// tests can assert metadata propagation.
type fakeWorker struct {
	lastCtx context.Context
	calls   []string
}

func (f *fakeWorker) record(ctx context.Context, name string) {
	f.lastCtx = ctx
	f.calls = append(f.calls, name)
}

func (f *fakeWorker) AnswerQuestion(ctx context.Context, _ *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	f.record(ctx, "AnswerQuestion")
	return &reasoningv1.AnswerQuestionResponse{Answer: "ok"}, nil
}
func (f *fakeWorker) AnswerQuestionStream(ctx context.Context, _ *reasoningv1.AnswerQuestionRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
	f.record(ctx, "AnswerQuestionStream")
	return nil, func() {}, errors.New("stream not implemented in fake")
}
func (f *fakeWorker) AnswerQuestionWithTools(ctx context.Context, _ *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error) {
	f.record(ctx, "AnswerQuestionWithTools")
	return &reasoningv1.AnswerQuestionWithToolsResponse{}, nil
}
func (f *fakeWorker) ClassifyQuestion(ctx context.Context, _ *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error) {
	f.record(ctx, "ClassifyQuestion")
	return &reasoningv1.ClassifyQuestionResponse{}, nil
}
func (f *fakeWorker) DecomposeQuestion(ctx context.Context, _ *reasoningv1.DecomposeQuestionRequest) (*reasoningv1.DecomposeQuestionResponse, error) {
	f.record(ctx, "DecomposeQuestion")
	return &reasoningv1.DecomposeQuestionResponse{}, nil
}
func (f *fakeWorker) SynthesizeDecomposedAnswer(ctx context.Context, _ *reasoningv1.SynthesizeDecomposedAnswerRequest) (*reasoningv1.SynthesizeDecomposedAnswerResponse, error) {
	f.record(ctx, "SynthesizeDecomposedAnswer")
	return &reasoningv1.SynthesizeDecomposedAnswerResponse{}, nil
}
func (f *fakeWorker) GetProviderCapabilities(ctx context.Context) (*reasoningv1.GetProviderCapabilitiesResponse, error) {
	f.record(ctx, "GetProviderCapabilities")
	return &reasoningv1.GetProviderCapabilitiesResponse{}, nil
}
func (f *fakeWorker) AnalyzeSymbol(ctx context.Context, _ *reasoningv1.AnalyzeSymbolRequest) (*reasoningv1.AnalyzeSymbolResponse, error) {
	f.record(ctx, "AnalyzeSymbol")
	return &reasoningv1.AnalyzeSymbolResponse{}, nil
}
func (f *fakeWorker) ReviewFile(ctx context.Context, _ *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
	f.record(ctx, "ReviewFile")
	return &reasoningv1.ReviewFileResponse{}, nil
}
func (f *fakeWorker) GenerateCliffNotes(ctx context.Context, _ *knowledgev1.GenerateCliffNotesRequest) (*knowledgev1.GenerateCliffNotesResponse, error) {
	f.record(ctx, "GenerateCliffNotes")
	return &knowledgev1.GenerateCliffNotesResponse{}, nil
}
func (f *fakeWorker) GenerateLearningPath(ctx context.Context, _ *knowledgev1.GenerateLearningPathRequest) (*knowledgev1.GenerateLearningPathResponse, error) {
	f.record(ctx, "GenerateLearningPath")
	return &knowledgev1.GenerateLearningPathResponse{}, nil
}
func (f *fakeWorker) GenerateArchitectureDiagram(ctx context.Context, _ *knowledgev1.GenerateArchitectureDiagramRequest) (*knowledgev1.GenerateArchitectureDiagramResponse, error) {
	f.record(ctx, "GenerateArchitectureDiagram")
	return &knowledgev1.GenerateArchitectureDiagramResponse{}, nil
}
func (f *fakeWorker) GenerateWorkflowStory(ctx context.Context, _ *knowledgev1.GenerateWorkflowStoryRequest) (*knowledgev1.GenerateWorkflowStoryResponse, error) {
	f.record(ctx, "GenerateWorkflowStory")
	return &knowledgev1.GenerateWorkflowStoryResponse{}, nil
}
func (f *fakeWorker) GenerateCodeTour(ctx context.Context, _ *knowledgev1.GenerateCodeTourRequest) (*knowledgev1.GenerateCodeTourResponse, error) {
	f.record(ctx, "GenerateCodeTour")
	return &knowledgev1.GenerateCodeTourResponse{}, nil
}
func (f *fakeWorker) ExplainSystem(ctx context.Context, _ *knowledgev1.ExplainSystemRequest) (*knowledgev1.ExplainSystemResponse, error) {
	f.record(ctx, "ExplainSystem")
	return &knowledgev1.ExplainSystemResponse{}, nil
}
func (f *fakeWorker) EnrichRequirement(ctx context.Context, _ *requirementsv1.EnrichRequirementRequest) (*requirementsv1.EnrichRequirementResponse, error) {
	f.record(ctx, "EnrichRequirement")
	return &requirementsv1.EnrichRequirementResponse{}, nil
}
func (f *fakeWorker) ExtractSpecs(ctx context.Context, _ *requirementsv1.ExtractSpecsRequest) (*requirementsv1.ExtractSpecsResponse, error) {
	f.record(ctx, "ExtractSpecs")
	return &requirementsv1.ExtractSpecsResponse{}, nil
}
func (f *fakeWorker) GenerateReport(ctx context.Context, _ *enterprisev1.GenerateReportRequest) (*enterprisev1.GenerateReportResponse, error) {
	f.record(ctx, "GenerateReport")
	return &enterprisev1.GenerateReportResponse{}, nil
}

// fakeStore yields a fixed workspace record at version 1.
type fakeStore struct{ rec *resolution.WorkspaceRecord }

func (f *fakeStore) LoadLLMConfig() (*resolution.WorkspaceRecord, error) {
	if f.rec == nil {
		return nil, nil
	}
	cp := *f.rec
	cp.Version = 1
	return &cp, nil
}
func (f *fakeStore) LoadLLMConfigVersion() (uint64, error) { return 1, nil }

func TestCaller_AttachesMetadataAndDelegates(t *testing.T) {
	store := &fakeStore{
		rec: &resolution.WorkspaceRecord{
			Provider:     "anthropic",
			APIKey:       "ws-secret",
			SummaryModel: "claude-sonnet-4",
			TimeoutSecs:  90,
		},
	}
	res := resolution.New(store, nil, config.LLMConfig{}, nil)
	fw := &fakeWorker{}
	c := New(fw, res, nil)

	_, err := c.AnswerQuestion(context.Background(), "repo-1", resolution.OpDiscussion, &reasoningv1.AnswerQuestionRequest{Question: "q"})
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	md, ok := metadata.FromOutgoingContext(fw.lastCtx)
	if !ok {
		t.Fatal("expected outgoing metadata on inner ctx")
	}
	if got := md.Get("x-sb-llm-provider"); len(got) != 1 || got[0] != "anthropic" {
		t.Errorf("provider header: got %v, want [anthropic]", got)
	}
	if got := md.Get("x-sb-llm-api-key"); len(got) != 1 || got[0] != "ws-secret" {
		t.Errorf("api-key header: got %v, want [ws-secret]", got)
	}
	if got := md.Get("x-sb-model"); len(got) != 1 || got[0] != "claude-sonnet-4" {
		t.Errorf("model header: got %v, want [claude-sonnet-4]", got)
	}
	if got := md.Get("x-sb-llm-timeout-seconds"); len(got) != 1 || got[0] != "90" {
		t.Errorf("timeout header: got %v, want [90]", got)
	}
	if got := md.Get("x-sb-repo-id"); len(got) != 1 || got[0] != "repo-1" {
		t.Errorf("repo-id header: got %v, want [repo-1]", got)
	}
	if got := md.Get("x-sb-operation"); len(got) != 1 || got[0] != "discussion" {
		t.Errorf("operation header: got %v, want [discussion]", got)
	}
}

func TestCaller_AttachesMetadataAcrossAllProtectedRPCs(t *testing.T) {
	store := &fakeStore{rec: &resolution.WorkspaceRecord{Provider: "anthropic", APIKey: "k", SummaryModel: "m"}}
	res := resolution.New(store, nil, config.LLMConfig{}, nil)
	fw := &fakeWorker{}
	c := New(fw, res, nil)

	ctx := context.Background()
	type call struct {
		name string
		fn   func() error
	}
	cases := []call{
		{"AnswerQuestion", func() error { _, e := c.AnswerQuestion(ctx, "", resolution.OpDiscussion, &reasoningv1.AnswerQuestionRequest{}); return e }},
		{"AnswerQuestionWithTools", func() error {
			_, e := c.AnswerQuestionWithTools(ctx, "", resolution.OpQAAgentTurn, &reasoningv1.AnswerQuestionWithToolsRequest{})
			return e
		}},
		{"ClassifyQuestion", func() error {
			_, e := c.ClassifyQuestion(ctx, "", resolution.OpQAClassify, &reasoningv1.ClassifyQuestionRequest{})
			return e
		}},
		{"DecomposeQuestion", func() error {
			_, e := c.DecomposeQuestion(ctx, "", resolution.OpQADecompose, &reasoningv1.DecomposeQuestionRequest{})
			return e
		}},
		{"SynthesizeDecomposedAnswer", func() error {
			_, e := c.SynthesizeDecomposedAnswer(ctx, "", resolution.OpQADeepSynth, &reasoningv1.SynthesizeDecomposedAnswerRequest{})
			return e
		}},
		{"GetProviderCapabilities", func() error {
			_, e := c.GetProviderCapabilities(ctx, "", resolution.OpProviderCapabilities)
			return e
		}},
		{"AnalyzeSymbol", func() error {
			_, e := c.AnalyzeSymbol(ctx, "", resolution.OpAnalysis, &reasoningv1.AnalyzeSymbolRequest{})
			return e
		}},
		{"ReviewFile", func() error { _, e := c.ReviewFile(ctx, "", resolution.OpReview, &reasoningv1.ReviewFileRequest{}); return e }},
		{"GenerateCliffNotes", func() error {
			_, e := c.GenerateCliffNotes(ctx, "", resolution.OpKnowledge, &knowledgev1.GenerateCliffNotesRequest{})
			return e
		}},
		{"GenerateLearningPath", func() error {
			_, e := c.GenerateLearningPath(ctx, "", resolution.OpKnowledge, &knowledgev1.GenerateLearningPathRequest{})
			return e
		}},
		{"GenerateArchitectureDiagram", func() error {
			_, e := c.GenerateArchitectureDiagram(ctx, "", resolution.OpKnowledge, &knowledgev1.GenerateArchitectureDiagramRequest{})
			return e
		}},
		{"GenerateWorkflowStory", func() error {
			_, e := c.GenerateWorkflowStory(ctx, "", resolution.OpKnowledge, &knowledgev1.GenerateWorkflowStoryRequest{})
			return e
		}},
		{"GenerateCodeTour", func() error {
			_, e := c.GenerateCodeTour(ctx, "", resolution.OpKnowledge, &knowledgev1.GenerateCodeTourRequest{})
			return e
		}},
		{"ExplainSystem", func() error {
			_, e := c.ExplainSystem(ctx, "", resolution.OpKnowledge, &knowledgev1.ExplainSystemRequest{})
			return e
		}},
		{"EnrichRequirement", func() error {
			_, e := c.EnrichRequirement(ctx, "", resolution.OpRequirementsEnrich, &requirementsv1.EnrichRequirementRequest{})
			return e
		}},
		{"ExtractSpecs", func() error {
			_, e := c.ExtractSpecs(ctx, "", resolution.OpRequirementsExtract, &requirementsv1.ExtractSpecsRequest{})
			return e
		}},
		{"GenerateReport", func() error {
			_, e := c.GenerateReport(ctx, "", resolution.OpReportGenerate, &enterprisev1.GenerateReportRequest{})
			return e
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err != nil {
				t.Fatalf("call %s: %v", tc.name, err)
			}
			md, ok := metadata.FromOutgoingContext(fw.lastCtx)
			if !ok {
				t.Fatalf("no outgoing metadata for %s", tc.name)
			}
			if got := md.Get("x-sb-llm-api-key"); len(got) != 1 || got[0] != "k" {
				t.Errorf("api-key header on %s: got %v", tc.name, got)
			}
		})
	}
}

func TestCaller_StreamHeadersAttachedBeforeOpen(t *testing.T) {
	// AnswerQuestionStream must attach metadata BEFORE calling the inner
	// stream-open. Since fakeWorker returns an error from the stream open,
	// we just need to confirm fw.lastCtx was set and contains the headers.
	store := &fakeStore{rec: &resolution.WorkspaceRecord{Provider: "openai", APIKey: "stream-key", SummaryModel: "gpt"}}
	res := resolution.New(store, nil, config.LLMConfig{}, nil)
	fw := &fakeWorker{}
	c := New(fw, res, nil)
	_, _, _ = c.AnswerQuestionStream(context.Background(), "", resolution.OpDiscussStream, &reasoningv1.AnswerQuestionRequest{})
	md, ok := metadata.FromOutgoingContext(fw.lastCtx)
	if !ok {
		t.Fatal("expected metadata to be attached before stream open")
	}
	if got := md.Get("x-sb-llm-api-key"); len(got) != 1 || got[0] != "stream-key" {
		t.Errorf("stream api-key header: got %v", got)
	}
}

func TestCaller_GetProviderCapabilities_VersionTracking(t *testing.T) {
	store := &fakeStore{rec: &resolution.WorkspaceRecord{Provider: "anthropic", APIKey: "k", SummaryModel: "m"}}
	res := resolution.New(store, nil, config.LLMConfig{}, nil)
	fw := &fakeWorker{}
	c := New(fw, res, nil)

	resp, err := c.GetProviderCapabilities(context.Background(), "", resolution.OpProviderCapabilities)
	if err != nil {
		t.Fatalf("GetProviderCapabilities: %v", err)
	}
	if resp.Version != 1 {
		t.Errorf("snapshot version: got %d, want 1", resp.Version)
	}
}
