// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package llmcall provides Caller, the LLM-aware adapter every worker LLM
// RPC must flow through. Caller wraps each protected RPC, resolves the
// runtime LLM config via the resolver, attaches gRPC metadata, emits the
// per-call structured log line, and delegates to the underlying worker
// client.
//
// This package is the mechanical guarantee that no caller can perform a
// worker LLM RPC without resolved metadata. The AST lint test in
// internal/llm/resolution/lint_test.go enforces that no source file
// outside this package and internal/worker/client.go calls a method in
// the WorkerLLM interface.
package llmcall

import (
	"context"
	"log/slog"
	"strconv"

	"google.golang.org/grpc/metadata"

	enterprisev1 "github.com/sourcebridge/sourcebridge/gen/go/enterprise/v1"
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	requirementsv1 "github.com/sourcebridge/sourcebridge/gen/go/requirements/v1"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// WorkerLLM is the narrow set of LLM-bearing worker RPCs that Caller
// wraps. *worker.Client satisfies this interface; tests can fake it
// directly without dragging in the real gRPC client.
//
// IMPORTANT: every LLM-bearing RPC on *worker.Client must be added here
// AND its wrapper added to Caller. The AST lint test fails if any caller
// outside the llmcall package or internal/worker/client.go invokes one of
// these methods directly. To add a new RPC:
//  1. Add the method to this interface.
//  2. Add a wrapper method on *Caller.
//  3. Add the method name to internal/llm/resolution/lint_test.go's
//     protectedWorkerMethods constant.
//  4. Add a corresponding op constant in internal/llm/resolution/ops.go.
type WorkerLLM interface {
	// Reasoning service
	AnswerQuestion(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error)
	AnswerQuestionStream(ctx context.Context, req *reasoningv1.AnswerQuestionRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error)
	AnswerQuestionWithTools(ctx context.Context, req *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error)
	ClassifyQuestion(ctx context.Context, req *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error)
	DecomposeQuestion(ctx context.Context, req *reasoningv1.DecomposeQuestionRequest) (*reasoningv1.DecomposeQuestionResponse, error)
	SynthesizeDecomposedAnswer(ctx context.Context, req *reasoningv1.SynthesizeDecomposedAnswerRequest) (*reasoningv1.SynthesizeDecomposedAnswerResponse, error)
	GetProviderCapabilities(ctx context.Context) (*reasoningv1.GetProviderCapabilitiesResponse, error)
	AnalyzeSymbol(ctx context.Context, req *reasoningv1.AnalyzeSymbolRequest) (*reasoningv1.AnalyzeSymbolResponse, error)
	ReviewFile(ctx context.Context, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error)

	// Knowledge service
	GenerateCliffNotes(ctx context.Context, req *knowledgev1.GenerateCliffNotesRequest) (*knowledgev1.GenerateCliffNotesResponse, error)
	GenerateLearningPath(ctx context.Context, req *knowledgev1.GenerateLearningPathRequest) (*knowledgev1.GenerateLearningPathResponse, error)
	GenerateArchitectureDiagram(ctx context.Context, req *knowledgev1.GenerateArchitectureDiagramRequest) (*knowledgev1.GenerateArchitectureDiagramResponse, error)
	GenerateWorkflowStory(ctx context.Context, req *knowledgev1.GenerateWorkflowStoryRequest) (*knowledgev1.GenerateWorkflowStoryResponse, error)
	GenerateCodeTour(ctx context.Context, req *knowledgev1.GenerateCodeTourRequest) (*knowledgev1.GenerateCodeTourResponse, error)
	ExplainSystem(ctx context.Context, req *knowledgev1.ExplainSystemRequest) (*knowledgev1.ExplainSystemResponse, error)

	// Requirements service
	EnrichRequirement(ctx context.Context, req *requirementsv1.EnrichRequirementRequest) (*requirementsv1.EnrichRequirementResponse, error)
	ExtractSpecs(ctx context.Context, req *requirementsv1.ExtractSpecsRequest) (*requirementsv1.ExtractSpecsResponse, error)

	// Enterprise reports
	GenerateReport(ctx context.Context, req *enterprisev1.GenerateReportRequest) (*enterprisev1.GenerateReportResponse, error)
}

// Caller is the LLM-aware adapter around a WorkerLLM. Every protected RPC
// has a wrapper method that takes (ctx, repoID, op, req) up front, runs
// the resolver, attaches the resolved gRPC metadata to ctx, emits the
// structured log line, and delegates to the inner WorkerLLM.
type Caller struct {
	inner    WorkerLLM
	resolver resolution.Resolver
	log      *slog.Logger
	// Subsystem is stamped into the x-sb-subsystem metadata header. Mostly
	// historical compatibility with the legacy withModelMetadata helper.
	Subsystem string
}

// New constructs a Caller. resolver must be non-nil. inner may be nil for
// degraded-mode operation; in that case every RPC method returns the
// zero-value typed *Response and a nil error so existing nil checks at
// call sites continue to compile, but callers should still guard against
// a nil inner via the IsAvailable helper.
func New(inner WorkerLLM, resolver resolution.Resolver, log *slog.Logger) *Caller {
	if log == nil {
		log = slog.Default()
	}
	return &Caller{
		inner:     inner,
		resolver:  resolver,
		log:       log,
		Subsystem: "knowledge",
	}
}

// IsAvailable returns true when the inner WorkerLLM is non-nil. Callers
// gate AI features on this just like they used to gate on r.Worker != nil.
func (c *Caller) IsAvailable() bool {
	return c != nil && c.inner != nil
}

// Inner exposes the underlying WorkerLLM for the small number of
// non-LLM-bearing methods (CheckHealth, Address, GenerateEmbedding,
// linking, contracts, parsing, simulate-change) that don't go through
// Caller. AST lint allowlist permits this single method on the package
// boundary.
func (c *Caller) Inner() WorkerLLM { return c.inner }

// withResolved attaches the resolved metadata onto ctx and emits the
// per-call structured log line. Returns the new ctx and the snapshot so
// callers that care about model name (e.g. for telemetry) can read it.
func (c *Caller) withResolved(ctx context.Context, repoID, op string, jobID, artifactID, jobType string) (context.Context, resolution.Snapshot, error) {
	snap, err := c.resolver.Resolve(ctx, repoID, op)
	if err != nil {
		return ctx, resolution.Snapshot{}, err
	}
	resolution.LogResolved(c.log, op, repoID, snap)

	pairs := []string{
		"x-sb-llm-provider", snap.Provider,
		"x-sb-llm-base-url", snap.BaseURL,
		"x-sb-llm-api-key", snap.APIKey,
		"x-sb-llm-draft-model", snap.DraftModel,
		"x-sb-operation", snap.OperationGroup,
	}
	if snap.TimeoutSecs > 0 {
		pairs = append(pairs, "x-sb-llm-timeout-seconds", strconv.Itoa(snap.TimeoutSecs))
	}
	if snap.Model != "" {
		pairs = append(pairs, "x-sb-model", snap.Model)
	}
	if jobID != "" {
		pairs = append(pairs, "x-sb-job-id", jobID)
	}
	if repoID != "" {
		pairs = append(pairs, "x-sb-repo-id", repoID)
	}
	if artifactID != "" {
		pairs = append(pairs, "x-sb-artifact-id", artifactID)
	}
	if jobType != "" {
		pairs = append(pairs, "x-sb-job-type", jobType)
	}
	subsystem := c.Subsystem
	if subsystem == "" {
		subsystem = "knowledge"
	}
	pairs = append(pairs, "x-sb-subsystem", subsystem)
	resolvedMD := metadata.Pairs(pairs...)

	// Merge resolved metadata onto any pre-existing outgoing metadata
	// (e.g. x-sb-cliff-render-only stamped by withCliffNotesRenderMetadata
	// upstream). Resolved fields take precedence: this is the whole
	// point of the adapter — the caller's own pass at withModelMetadata
	// (if it ran) is overwritten by the resolver's authoritative values.
	if existing, ok := metadata.FromOutgoingContext(ctx); ok {
		merged := existing.Copy()
		for k, v := range resolvedMD {
			merged[k] = v
		}
		return metadata.NewOutgoingContext(ctx, merged), snap, nil
	}
	return metadata.NewOutgoingContext(ctx, resolvedMD), snap, nil
}

// JobMetadata is the optional set of job/artifact identifiers a caller
// can attach to a single RPC. Most callers leave this zero; the living-
// wiki and knowledge pipelines use it to thread orchestrator metadata
// through to the worker for per-job tracing.
type JobMetadata struct {
	JobID      string
	ArtifactID string
	JobType    string
}

// ─────────────────────────────────────────────────────────────────────────
// Wrapper methods — one per WorkerLLM RPC.
// ─────────────────────────────────────────────────────────────────────────

func (c *Caller) AnswerQuestion(ctx context.Context, repoID, op string, req *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.AnswerQuestion(ctx, req)
}

func (c *Caller) AnswerQuestionStream(ctx context.Context, repoID, op string, req *reasoningv1.AnswerQuestionRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, func() {}, err
	}
	return c.inner.AnswerQuestionStream(ctx, req)
}

func (c *Caller) AnswerQuestionWithTools(ctx context.Context, repoID, op string, req *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.AnswerQuestionWithTools(ctx, req)
}

func (c *Caller) ClassifyQuestion(ctx context.Context, repoID, op string, req *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.ClassifyQuestion(ctx, req)
}

func (c *Caller) DecomposeQuestion(ctx context.Context, repoID, op string, req *reasoningv1.DecomposeQuestionRequest) (*reasoningv1.DecomposeQuestionResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.DecomposeQuestion(ctx, req)
}

func (c *Caller) SynthesizeDecomposedAnswer(ctx context.Context, repoID, op string, req *reasoningv1.SynthesizeDecomposedAnswerRequest) (*reasoningv1.SynthesizeDecomposedAnswerResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.SynthesizeDecomposedAnswer(ctx, req)
}

// CapabilitiesResponse pairs the worker probe response with the resolver
// snapshot version it observed. Callers that cache capabilities should
// invalidate when Version changes — that's how a workspace save (which
// bumps the version) forces a re-probe on the next agentic turn.
type CapabilitiesResponse struct {
	Resp    *reasoningv1.GetProviderCapabilitiesResponse
	Version uint64
}

func (c *Caller) GetProviderCapabilities(ctx context.Context, repoID, op string) (*CapabilitiesResponse, error) {
	ctx, snap, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	resp, err := c.inner.GetProviderCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	return &CapabilitiesResponse{Resp: resp, Version: snap.Version}, nil
}

func (c *Caller) AnalyzeSymbol(ctx context.Context, repoID, op string, req *reasoningv1.AnalyzeSymbolRequest) (*reasoningv1.AnalyzeSymbolResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.AnalyzeSymbol(ctx, req)
}

func (c *Caller) ReviewFile(ctx context.Context, repoID, op string, req *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.ReviewFile(ctx, req)
}

// GenerateCliffNotesWithJob is the JobMetadata-aware wrapper used by the
// knowledge pipeline; non-knowledge callers can use GenerateCliffNotes
// which omits job metadata.
func (c *Caller) GenerateCliffNotesWithJob(ctx context.Context, repoID, op string, jm JobMetadata, req *knowledgev1.GenerateCliffNotesRequest) (*knowledgev1.GenerateCliffNotesResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, jm.JobID, jm.ArtifactID, jm.JobType)
	if err != nil {
		return nil, err
	}
	return c.inner.GenerateCliffNotes(ctx, req)
}

func (c *Caller) GenerateCliffNotes(ctx context.Context, repoID, op string, req *knowledgev1.GenerateCliffNotesRequest) (*knowledgev1.GenerateCliffNotesResponse, error) {
	return c.GenerateCliffNotesWithJob(ctx, repoID, op, JobMetadata{}, req)
}

func (c *Caller) GenerateLearningPathWithJob(ctx context.Context, repoID, op string, jm JobMetadata, req *knowledgev1.GenerateLearningPathRequest) (*knowledgev1.GenerateLearningPathResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, jm.JobID, jm.ArtifactID, jm.JobType)
	if err != nil {
		return nil, err
	}
	return c.inner.GenerateLearningPath(ctx, req)
}

func (c *Caller) GenerateLearningPath(ctx context.Context, repoID, op string, req *knowledgev1.GenerateLearningPathRequest) (*knowledgev1.GenerateLearningPathResponse, error) {
	return c.GenerateLearningPathWithJob(ctx, repoID, op, JobMetadata{}, req)
}

func (c *Caller) GenerateArchitectureDiagramWithJob(ctx context.Context, repoID, op string, jm JobMetadata, req *knowledgev1.GenerateArchitectureDiagramRequest) (*knowledgev1.GenerateArchitectureDiagramResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, jm.JobID, jm.ArtifactID, jm.JobType)
	if err != nil {
		return nil, err
	}
	return c.inner.GenerateArchitectureDiagram(ctx, req)
}

func (c *Caller) GenerateArchitectureDiagram(ctx context.Context, repoID, op string, req *knowledgev1.GenerateArchitectureDiagramRequest) (*knowledgev1.GenerateArchitectureDiagramResponse, error) {
	return c.GenerateArchitectureDiagramWithJob(ctx, repoID, op, JobMetadata{}, req)
}

func (c *Caller) GenerateWorkflowStoryWithJob(ctx context.Context, repoID, op string, jm JobMetadata, req *knowledgev1.GenerateWorkflowStoryRequest) (*knowledgev1.GenerateWorkflowStoryResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, jm.JobID, jm.ArtifactID, jm.JobType)
	if err != nil {
		return nil, err
	}
	return c.inner.GenerateWorkflowStory(ctx, req)
}

func (c *Caller) GenerateWorkflowStory(ctx context.Context, repoID, op string, req *knowledgev1.GenerateWorkflowStoryRequest) (*knowledgev1.GenerateWorkflowStoryResponse, error) {
	return c.GenerateWorkflowStoryWithJob(ctx, repoID, op, JobMetadata{}, req)
}

func (c *Caller) GenerateCodeTourWithJob(ctx context.Context, repoID, op string, jm JobMetadata, req *knowledgev1.GenerateCodeTourRequest) (*knowledgev1.GenerateCodeTourResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, jm.JobID, jm.ArtifactID, jm.JobType)
	if err != nil {
		return nil, err
	}
	return c.inner.GenerateCodeTour(ctx, req)
}

func (c *Caller) GenerateCodeTour(ctx context.Context, repoID, op string, req *knowledgev1.GenerateCodeTourRequest) (*knowledgev1.GenerateCodeTourResponse, error) {
	return c.GenerateCodeTourWithJob(ctx, repoID, op, JobMetadata{}, req)
}

func (c *Caller) ExplainSystem(ctx context.Context, repoID, op string, req *knowledgev1.ExplainSystemRequest) (*knowledgev1.ExplainSystemResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.ExplainSystem(ctx, req)
}

func (c *Caller) EnrichRequirement(ctx context.Context, repoID, op string, req *requirementsv1.EnrichRequirementRequest) (*requirementsv1.EnrichRequirementResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.EnrichRequirement(ctx, req)
}

func (c *Caller) ExtractSpecs(ctx context.Context, repoID, op string, req *requirementsv1.ExtractSpecsRequest) (*requirementsv1.ExtractSpecsResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.ExtractSpecs(ctx, req)
}

func (c *Caller) GenerateReport(ctx context.Context, repoID, op string, req *enterprisev1.GenerateReportRequest) (*enterprisev1.GenerateReportResponse, error) {
	ctx, _, err := c.withResolved(ctx, repoID, op, "", "", "")
	if err != nil {
		return nil, err
	}
	return c.inner.GenerateReport(ctx, req)
}
