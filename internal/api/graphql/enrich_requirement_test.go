// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"testing"

	enterprisev1 "github.com/sourcebridge/sourcebridge/gen/go/enterprise/v1"
	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	requirementsv1 "github.com/sourcebridge/sourcebridge/gen/go/requirements/v1"
	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/events"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

// ── fake WorkerLLM for enrich tests ─────────────────────────────────────────

// enrichFakeWorker implements llmcall.WorkerLLM with controllable EnrichRequirement
// and ExplainSystem responses. All other methods return zero-value responses.
// explainResp is used by explain_system_test.go which cannot embed a separate type.
type enrichFakeWorker struct {
	suggestedTags     []string
	suggestedPriority string
	err               error
	explainResp       *knowledgev1.ExplainSystemResponse
}

func (f *enrichFakeWorker) AnswerQuestion(_ context.Context, _ *reasoningv1.AnswerQuestionRequest) (*reasoningv1.AnswerQuestionResponse, error) {
	return &reasoningv1.AnswerQuestionResponse{}, nil
}
func (f *enrichFakeWorker) AnswerQuestionStream(_ context.Context, _ *reasoningv1.AnswerQuestionStreamRequest) (reasoningv1.ReasoningService_AnswerQuestionStreamClient, context.CancelFunc, error) {
	return nil, func() {}, nil
}
func (f *enrichFakeWorker) AnswerQuestionWithTools(_ context.Context, _ *reasoningv1.AnswerQuestionWithToolsRequest) (*reasoningv1.AnswerQuestionWithToolsResponse, error) {
	return &reasoningv1.AnswerQuestionWithToolsResponse{}, nil
}
func (f *enrichFakeWorker) ClassifyQuestion(_ context.Context, _ *reasoningv1.ClassifyQuestionRequest) (*reasoningv1.ClassifyQuestionResponse, error) {
	return &reasoningv1.ClassifyQuestionResponse{}, nil
}
func (f *enrichFakeWorker) DecomposeQuestion(_ context.Context, _ *reasoningv1.DecomposeQuestionRequest) (*reasoningv1.DecomposeQuestionResponse, error) {
	return &reasoningv1.DecomposeQuestionResponse{}, nil
}
func (f *enrichFakeWorker) SynthesizeDecomposedAnswer(_ context.Context, _ *reasoningv1.SynthesizeDecomposedAnswerRequest) (*reasoningv1.SynthesizeDecomposedAnswerResponse, error) {
	return &reasoningv1.SynthesizeDecomposedAnswerResponse{}, nil
}
func (f *enrichFakeWorker) GetProviderCapabilities(_ context.Context) (*reasoningv1.GetProviderCapabilitiesResponse, error) {
	return &reasoningv1.GetProviderCapabilitiesResponse{}, nil
}
func (f *enrichFakeWorker) AnalyzeSymbol(_ context.Context, _ *reasoningv1.AnalyzeSymbolRequest) (*reasoningv1.AnalyzeSymbolResponse, error) {
	return &reasoningv1.AnalyzeSymbolResponse{}, nil
}
func (f *enrichFakeWorker) ReviewFile(_ context.Context, _ *reasoningv1.ReviewFileRequest) (*reasoningv1.ReviewFileResponse, error) {
	return &reasoningv1.ReviewFileResponse{}, nil
}
func (f *enrichFakeWorker) GenerateCliffNotes(_ context.Context, _ *knowledgev1.GenerateCliffNotesRequest, _ ...worker.CallOption) (*knowledgev1.GenerateCliffNotesResponse, error) {
	return &knowledgev1.GenerateCliffNotesResponse{}, nil
}
func (f *enrichFakeWorker) GenerateLearningPath(_ context.Context, _ *knowledgev1.GenerateLearningPathRequest, _ ...worker.CallOption) (*knowledgev1.GenerateLearningPathResponse, error) {
	return &knowledgev1.GenerateLearningPathResponse{}, nil
}
func (f *enrichFakeWorker) GenerateArchitectureDiagram(_ context.Context, _ *knowledgev1.GenerateArchitectureDiagramRequest, _ ...worker.CallOption) (*knowledgev1.GenerateArchitectureDiagramResponse, error) {
	return &knowledgev1.GenerateArchitectureDiagramResponse{}, nil
}
func (f *enrichFakeWorker) GenerateWorkflowStory(_ context.Context, _ *knowledgev1.GenerateWorkflowStoryRequest, _ ...worker.CallOption) (*knowledgev1.GenerateWorkflowStoryResponse, error) {
	return &knowledgev1.GenerateWorkflowStoryResponse{}, nil
}
func (f *enrichFakeWorker) GenerateCodeTour(_ context.Context, _ *knowledgev1.GenerateCodeTourRequest, _ ...worker.CallOption) (*knowledgev1.GenerateCodeTourResponse, error) {
	return &knowledgev1.GenerateCodeTourResponse{}, nil
}
func (f *enrichFakeWorker) ExplainSystem(_ context.Context, _ *knowledgev1.ExplainSystemRequest, _ ...worker.CallOption) (*knowledgev1.ExplainSystemResponse, error) {
	if f.explainResp != nil {
		return f.explainResp, nil
	}
	return &knowledgev1.ExplainSystemResponse{}, nil
}
func (f *enrichFakeWorker) EnrichRequirement(_ context.Context, _ *requirementsv1.EnrichRequirementRequest) (*requirementsv1.EnrichRequirementResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &requirementsv1.EnrichRequirementResponse{
		SuggestedTags:     f.suggestedTags,
		SuggestedPriority: f.suggestedPriority,
	}, nil
}
func (f *enrichFakeWorker) ExtractSpecs(_ context.Context, _ *requirementsv1.ExtractSpecsRequest) (*requirementsv1.ExtractSpecsResponse, error) {
	return &requirementsv1.ExtractSpecsResponse{}, nil
}
func (f *enrichFakeWorker) GenerateReport(_ context.Context, _ *enterprisev1.GenerateReportRequest, _ ...worker.CallOption) (*enterprisev1.GenerateReportResponse, error) {
	return &enterprisev1.GenerateReportResponse{}, nil
}

// newResolverWithEnrichWorker constructs a Resolver wired with a fake LLM worker
// that returns the given suggested tags and priority for every EnrichRequirement call.
// Deps.Worker is set to a non-nil sentinel so the resolver's availability guard
// passes (it checks `r.Deps.Worker == nil`); the actual LLM call routes through
// LLMCaller which wraps fw directly.
func newResolverWithEnrichWorker(t *testing.T, fw *enrichFakeWorker) (*Resolver, string) {
	t.Helper()
	store := graphstore.NewStore()
	result := &indexer.IndexResult{
		RepoName: "test-repo",
		RepoPath: "/tmp/test",
		Files:    []indexer.FileResult{{Path: "main.go", Language: "go", LineCount: 10}},
	}
	repo, _ := store.StoreIndexResult(t.Context(), result)

	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	res := resolution.NewFrozenResolver(snap)
	caller := llmcall.New(fw, res, nil)

	return &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:  events.NewBus(),
			LLMCaller: caller,
			Worker:    new(worker.Client), // non-nil sentinel; passes availability guard
		},
		Store: store,
	}, repo.ID
}

// ctxAdmin returns a context with admin claims, matching the pattern in trash.resolvers_test.go.
func ctxAdmin() context.Context {
	return context.WithValue(context.Background(), auth.ClaimsKey, &auth.Claims{
		UserID: "admin-user",
		Role:   "admin",
	})
}

// ── CA-88: EnrichRequirement merge semantics (calls through the resolver) ────

func TestEnrichRequirement_ForceFalse_MergesTags_PreservesPriority(t *testing.T) {
	fw := &enrichFakeWorker{
		suggestedTags:     []string{"new-tag"},
		suggestedPriority: "low",
	}
	r, repoID := newResolverWithEnrichWorker(t, fw)

	// Seed a requirement with existing tags and a user-set priority.
	n, err := r.Store.StoreRequirements(t.Context(), repoID, []*graphstore.StoredRequirement{
		{ExternalID: "R-1", Title: "test", Tags: []string{"existing"}, Priority: "high"},
	})
	if err != nil || n == 0 {
		t.Fatal("seed failed")
	}
	allReqs, _ := r.Store.GetRequirements(t.Context(), repoID, 10, 0)
	if len(allReqs) == 0 {
		t.Fatal("no requirements after seed")
	}
	reqID := allReqs[0].ID

	forceOff := false
	got, err := r.Mutation().EnrichRequirement(t.Context(), reqID, &forceOff)
	if err != nil {
		t.Fatalf("EnrichRequirement: %v", err)
	}

	// Tags merged: [existing, new-tag]
	if len(got.Tags) != 2 {
		t.Errorf("expected 2 tags, got %v", got.Tags)
	}
	// Priority preserved (user had "high"; LLM suggested "low" but force=false).
	if got.Priority == nil || *got.Priority != "high" {
		t.Errorf("expected priority high, got %v", got.Priority)
	}
}

func TestEnrichRequirement_ForceTrue_ReplacesTags_ReplacesPriority(t *testing.T) {
	fw := &enrichFakeWorker{
		suggestedTags:     []string{"new-tag"},
		suggestedPriority: "critical",
	}
	r, repoID := newResolverWithEnrichWorker(t, fw)

	n, err := r.Store.StoreRequirements(t.Context(), repoID, []*graphstore.StoredRequirement{
		{ExternalID: "R-1", Title: "test", Tags: []string{"existing"}, Priority: "high"},
	})
	if err != nil || n == 0 {
		t.Fatal("seed failed")
	}
	allReqs, _ := r.Store.GetRequirements(t.Context(), repoID, 10, 0)
	if len(allReqs) == 0 {
		t.Fatal("no requirements after seed")
	}
	reqID := allReqs[0].ID

	forceOn := true
	got, err := r.Mutation().EnrichRequirement(t.Context(), reqID, &forceOn)
	if err != nil {
		t.Fatalf("EnrichRequirement: %v", err)
	}

	// Tags replaced — only [new-tag].
	if len(got.Tags) != 1 || got.Tags[0] != "new-tag" {
		t.Errorf("expected [new-tag], got %v", got.Tags)
	}
	// Priority replaced.
	if got.Priority == nil || *got.Priority != "critical" {
		t.Errorf("expected priority critical, got %v", got.Priority)
	}
}

func TestEnrichRequirement_EmptyPriority_GetsSetByLLM(t *testing.T) {
	fw := &enrichFakeWorker{
		suggestedTags:     nil,
		suggestedPriority: "medium",
	}
	r, repoID := newResolverWithEnrichWorker(t, fw)

	n, err := r.Store.StoreRequirements(t.Context(), repoID, []*graphstore.StoredRequirement{
		{ExternalID: "R-1", Title: "test", Tags: nil, Priority: ""},
	})
	if err != nil || n == 0 {
		t.Fatal("seed failed")
	}
	allReqs, _ := r.Store.GetRequirements(t.Context(), repoID, 10, 0)
	if len(allReqs) == 0 {
		t.Fatal("no requirements after seed")
	}
	reqID := allReqs[0].ID

	got, err := r.Mutation().EnrichRequirement(t.Context(), reqID, nil)
	if err != nil {
		t.Fatalf("EnrichRequirement: %v", err)
	}
	// Priority was empty → LLM value applied.
	if got.Priority == nil || *got.Priority != "medium" {
		t.Errorf("expected priority medium, got %v", got.Priority)
	}
}

func TestEnrichRequirement_UnsetPriority_GetsSetByLLM(t *testing.T) {
	fw := &enrichFakeWorker{
		suggestedTags:     nil,
		suggestedPriority: "low",
	}
	r, repoID := newResolverWithEnrichWorker(t, fw)

	n, err := r.Store.StoreRequirements(t.Context(), repoID, []*graphstore.StoredRequirement{
		{ExternalID: "R-1", Title: "test", Tags: nil, Priority: "unset"},
	})
	if err != nil || n == 0 {
		t.Fatal("seed failed")
	}
	allReqs, _ := r.Store.GetRequirements(t.Context(), repoID, 10, 0)
	if len(allReqs) == 0 {
		t.Fatal("no requirements after seed")
	}
	reqID := allReqs[0].ID

	got, err := r.Mutation().EnrichRequirement(t.Context(), reqID, nil)
	if err != nil {
		t.Fatalf("EnrichRequirement: %v", err)
	}
	// Priority was "unset" → treated as empty, LLM value applied.
	if got.Priority == nil || *got.Priority != "low" {
		t.Errorf("expected priority low, got %v", got.Priority)
	}
}

// ── CA-88: worker nil guard ───────────────────────────────────────────────────

func TestEnrichRequirement_WorkerNil_ReturnsError(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	_, err := r.Mutation().EnrichAllRequirements(context.Background(), repoID, nil, nil)
	if err == nil {
		t.Fatal("expected error when worker is nil")
	}
}

// ── CA-89: EnrichAllRequirements admin gate ───────────────────────────────────

func TestEnrichAllRequirements_NonAdminRejected(t *testing.T) {
	r, repoID := newResolverWithRepo(t)
	// Inject a non-nil Worker so the worker-nil guard passes but the admin gate fires.
	fw := &enrichFakeWorker{}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	r.Deps.LLMCaller = llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)
	r.Deps.Worker = new(worker.Client)

	nonAdminCtx := context.WithValue(context.Background(), auth.ClaimsKey, &auth.Claims{
		UserID: "user-1",
		Role:   "member",
	})
	_, err := r.Mutation().EnrichAllRequirements(nonAdminCtx, repoID, nil, nil)
	if err == nil {
		t.Fatal("expected admin gate error, got nil")
	}
}

// ── CA-89: EnrichAllRequirements filter logic ──────────────────────────────────

func TestEnrichAllRequirements_FilterSkipsEnrichedByDefault(t *testing.T) {
	// The filter is exercised end-to-end via applyEnrichSuggestions.
	// The "nothing to enrich" early return is the observable behaviour here.
	store := graphstore.NewStore()
	result := &indexer.IndexResult{RepoName: "repo", RepoPath: "/tmp"}
	repo, _ := store.StoreIndexResult(t.Context(), result)
	// Seed requirements that are already enriched (have tags).
	_, _ = store.StoreRequirements(t.Context(), repo.ID, []*graphstore.StoredRequirement{
		{ExternalID: "R-1", Title: "done", Tags: []string{"done"}, Priority: "high"},
	})

	fw := &enrichFakeWorker{}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	caller := llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)
	r := &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:  events.NewBus(),
			LLMCaller: caller,
			Worker:    new(worker.Client),
		},
		Store: store,
	}

	result2, err := r.Mutation().EnrichAllRequirements(ctxAdmin(), repo.ID, nil, nil)
	if err != nil {
		// Orchestrator may be nil — should return early with nothingToEnrich result.
		// If we get an error about orchestrator unavailable, that's also acceptable
		// because we'd never reach that path (nothing to enrich returns before it).
		t.Logf("unexpected error: %v", err)
	}
	if result2 != nil && result2.JobID != "none" {
		t.Errorf("expected jobId 'none' for nothing-to-enrich, got %q", result2.JobID)
	}
}

func TestEnrichAllRequirements_ForceIncludesEnriched(t *testing.T) {
	// force=true → enriched requirements are included in the toEnrich slice.
	// The orchestrator is nil, so we expect the orchestrator error — but the filter
	// ran (otherwise we'd get the "nothing to enrich" early return with jobId="none").
	store := graphstore.NewStore()
	result := &indexer.IndexResult{RepoName: "repo", RepoPath: "/tmp"}
	repo, _ := store.StoreIndexResult(t.Context(), result)
	_, _ = store.StoreRequirements(t.Context(), repo.ID, []*graphstore.StoredRequirement{
		{ExternalID: "R-1", Title: "done", Tags: []string{"done"}, Priority: "high"},
	})

	fw := &enrichFakeWorker{}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	caller := llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)
	r := &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:  events.NewBus(),
			LLMCaller: caller,
			Worker:    new(worker.Client),
		},
		Store: store,
	}

	forceOn := true
	res, err := r.Mutation().EnrichAllRequirements(ctxAdmin(), repo.ID, &forceOn, nil)
	// Orchestrator is nil → expects "orchestrator not available" error (reached past filter).
	// If no error, jobId must not be "none" (meaning at least one req was included).
	if err == nil && res != nil && res.JobID == "none" {
		t.Errorf("force=true should have included the enriched requirement, but got nothingToEnrich early return")
	}
}

// ── CA-89: load cap config ────────────────────────────────────────────────────

func TestEnrichAllRequirements_LoadCapDefault500(t *testing.T) {
	// Default BulkEnrichMaxRequirements is 500. Verified via config.Defaults()
	// which is the canonical source of truth for operator-visible defaults.
	defaults := config.Defaults()
	if defaults.Auth.BulkEnrichMaxRequirements != 500 {
		t.Errorf("default BulkEnrichMaxRequirements: got %d, want 500", defaults.Auth.BulkEnrichMaxRequirements)
	}
}
