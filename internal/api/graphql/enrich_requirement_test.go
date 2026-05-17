// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"strings"
	"testing"
	"time"

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
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
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

// ── CA-88: worker nil guard (T-H1) ───────────────────────────────────────────
//
// Previously this test called EnrichAllRequirements (wrong resolver). It now
// actually calls EnrichRequirement with a seeded requirement and Worker=nil,
// matching the guard at schema.resolvers.go:1077.

func TestEnrichRequirement_WorkerNil_ReturnsError(t *testing.T) {
	// Build a resolver with Worker=nil (the default in newResolverWithRepo).
	r, repoID := newResolverWithRepo(t)

	// Seed one requirement so the ID lookup succeeds — we must reach the
	// worker-nil guard, not the "requirement not found" branch.
	_, err := r.Store.StoreRequirements(t.Context(), repoID, []*graphstore.StoredRequirement{
		{ExternalID: "R-nil-1", Title: "needs enrichment"},
	})
	if err != nil {
		t.Fatal("seed failed:", err)
	}
	allReqs, _ := r.Store.GetRequirements(t.Context(), repoID, 10, 0)
	if len(allReqs) == 0 {
		t.Fatal("no requirements after seed")
	}
	reqID := allReqs[0].ID

	// r.Deps.Worker is nil — must return the "worker not connected" error.
	_, callErr := r.Mutation().EnrichRequirement(context.Background(), reqID, nil)
	if callErr == nil {
		t.Fatal("expected error when worker is nil, got nil")
	}
	if !strings.Contains(callErr.Error(), "worker not connected") {
		t.Errorf("expected 'worker not connected' in error, got: %v", callErr)
	}
}

// TestEnrichRequirement_ReqNotFound_ReturnsError verifies that a nonexistent
// requirement ID returns the "requirement not found" error (resolver line 1082).
func TestEnrichRequirement_ReqNotFound_ReturnsError(t *testing.T) {
	fw := &enrichFakeWorker{}
	r, _ := newResolverWithEnrichWorker(t, fw)

	_, err := r.Mutation().EnrichRequirement(context.Background(), "nonexistent-req-id-xyz", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent requirement, got nil")
	}
	if !strings.Contains(err.Error(), "requirement not found") {
		t.Errorf("expected 'requirement not found' in error, got: %v", err)
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

// TestEnrichAllRequirements_FilterSkipsEnrichedByDefault verifies that when all
// seeded requirements are already enriched (have tags), EnrichAllRequirements
// returns the "none" sentinel without enqueuing a job (T-H2).
//
// Uses a real orchestrator stub (D-023b) so the test would catch any regression
// that reaches the Enqueue path despite finding nothing to enrich.
func TestEnrichAllRequirements_FilterSkipsEnrichedByDefault(t *testing.T) {
	store := graphstore.NewStore()
	result := &indexer.IndexResult{RepoName: "repo", RepoPath: "/tmp"}
	repo, _ := store.StoreIndexResult(t.Context(), result)
	// All three requirements are already enriched: have tags + priority set.
	_, _ = store.StoreRequirements(t.Context(), repo.ID, []*graphstore.StoredRequirement{
		{ExternalID: "R-1", Title: "done", Tags: []string{"done"}, Priority: "high"},
		{ExternalID: "R-2", Title: "also done", Tags: []string{"feature"}, Priority: "medium"},
		{ExternalID: "R-3", Title: "already done", Tags: []string{"bug"}, Priority: "low"},
	})

	fw := &enrichFakeWorker{}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	caller := llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)

	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{
		SkipStartupReconciliation: true,
		Retry:                     orchestrator.RetryPolicy{MaxAttempts: 1},
	})
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	r := &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:     events.NewBus(),
			LLMCaller:    caller,
			Worker:       new(worker.Client),
			Orchestrator: orch,
		},
		Store: store,
	}

	got, err := r.Mutation().EnrichAllRequirements(ctxAdmin(), repo.ID, nil, nil)
	if err != nil {
		t.Fatalf("expected nil error for nothing-to-enrich, got: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.JobID != "none" {
		t.Errorf("expected JobID 'none' for all-enriched repo, got %q", got.JobID)
	}
	if got.RequirementsQueued != 0 {
		t.Errorf("expected RequirementsQueued=0, got %d", got.RequirementsQueued)
	}
}

func TestEnrichAllRequirements_ForceIncludesEnriched(t *testing.T) {
	// force=true → enriched requirements are included in the toEnrich slice,
	// so RequirementsQueued=1 and a real job ID is returned (not "none").
	// Uses a real orchestrator (pattern from TestEnrichAllRequirements_BulkEnrichCapFromConfig)
	// so the assertion observes actual resolver+orchestrator behavior, not just the error path.
	store := graphstore.NewStore()
	result := &indexer.IndexResult{RepoName: "repo", RepoPath: "/tmp"}
	repo, _ := store.StoreIndexResult(t.Context(), result)
	_, _ = store.StoreRequirements(t.Context(), repo.ID, []*graphstore.StoredRequirement{
		{ExternalID: "R-1", Title: "done", Tags: []string{"done"}, Priority: "high"},
	})

	fw := &enrichFakeWorker{}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	caller := llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)

	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{
		SkipStartupReconciliation: true,
		Retry:                     orchestrator.RetryPolicy{MaxAttempts: 1},
	})
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	r := &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:     events.NewBus(),
			LLMCaller:    caller,
			Worker:       new(worker.Client),
			Orchestrator: orch,
			LLMResolver:  newStubLLMResolver(),
		},
		Store: store,
	}

	forceOn := true
	got, err := r.Mutation().EnrichAllRequirements(ctxAdmin(), repo.ID, &forceOn, nil)
	if err != nil {
		t.Fatalf("EnrichAllRequirements: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	// force=true includes the already-enriched requirement → queued count is 1.
	if got.RequirementsQueued != 1 {
		t.Errorf("expected RequirementsQueued=1 (force includes enriched), got %d", got.RequirementsQueued)
	}
	// A real job was enqueued — not the "nothing to do" synthetic result.
	if got.JobID == "none" {
		t.Errorf("expected a real job ID (force path reached orchestrator), got %q", got.JobID)
	}
	if got.JobID == "" {
		t.Errorf("expected a non-empty job ID, got empty string")
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

// ── T-M1: orchestrator nil guard ─────────────────────────────────────────────
//
// EnrichAllRequirements checks r.Deps.Orchestrator == nil AFTER the worker-nil
// guard and AFTER the admin gate. This test reaches that check by providing a
// non-nil Worker but leaving Orchestrator nil.

func TestEnrichAllRequirements_OrchestratorNil_ReturnsError(t *testing.T) {
	store := graphstore.NewStore()
	result := &indexer.IndexResult{RepoName: "repo", RepoPath: "/tmp"}
	repo, _ := store.StoreIndexResult(t.Context(), result)
	// Seed one unenriched requirement so the filter passes and we reach
	// the Orchestrator guard (not the early-return "nothing to enrich" branch).
	_, _ = store.StoreRequirements(t.Context(), repo.ID, []*graphstore.StoredRequirement{
		{ExternalID: "R-unenriched", Title: "needs enrichment"},
	})

	fw := &enrichFakeWorker{}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	caller := llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)
	r := &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:     events.NewBus(),
			LLMCaller:    caller,
			Worker:       new(worker.Client), // non-nil — passes worker guard
			Orchestrator: nil,                // nil — must trigger orchestrator error
		},
		Store: store,
	}

	_, err := r.Mutation().EnrichAllRequirements(ctxAdmin(), repo.ID, nil, nil)
	if err == nil {
		t.Fatal("expected error when Orchestrator is nil, got nil")
	}
	if !strings.Contains(err.Error(), "orchestrator") {
		t.Errorf("expected 'orchestrator' in error message, got: %v", err)
	}
}

// ── T-H3: BulkEnrichCap from Config + batchSize clamp ────────────────────────

// TestEnrichAllRequirements_BulkEnrichCapFromConfig verifies that the resolver
// reads Config.Auth.BulkEnrichMaxRequirements and respects it as the upper bound
// on the number of requirements queued (T-H3 cap assertion).
func TestEnrichAllRequirements_BulkEnrichCapFromConfig(t *testing.T) {
	store := graphstore.NewStore()
	result := &indexer.IndexResult{RepoName: "repo", RepoPath: "/tmp"}
	repo, _ := store.StoreIndexResult(t.Context(), result)
	// Seed 5 unenriched requirements.
	reqs := make([]*graphstore.StoredRequirement, 5)
	for i := range reqs {
		reqs[i] = &graphstore.StoredRequirement{
			ExternalID: strings.Repeat("R", i+1),
			Title:      "unenriched",
		}
	}
	_, _ = store.StoreRequirements(t.Context(), repo.ID, reqs)

	fw := &enrichFakeWorker{}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	caller := llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)

	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{
		SkipStartupReconciliation: true,
		Retry:                     orchestrator.RetryPolicy{MaxAttempts: 1},
	})
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	cfg := config.Defaults()
	cfg.Auth.BulkEnrichMaxRequirements = 2 // cap at 2 of the 5 seeded

	r := &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:     events.NewBus(),
			LLMCaller:    caller,
			Worker:       new(worker.Client),
			Orchestrator: orch,
			Config:       cfg,
			LLMResolver:  newStubLLMResolver(),
		},
		Store: store,
	}

	got, err := r.Mutation().EnrichAllRequirements(ctxAdmin(), repo.ID, nil, nil)
	if err != nil {
		t.Fatalf("EnrichAllRequirements: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	// Only 2 requirements should be queued (BulkEnrichMaxRequirements=2).
	if got.RequirementsQueued != 2 {
		t.Errorf("expected RequirementsQueued=2 (cap enforced), got %d", got.RequirementsQueued)
	}
}

// TestEnrichAllRequirements_BatchSize200_AcceptedWithoutError verifies that a
// batchSize argument > 100 does not cause EnrichAllRequirements to return an
// error and that RequirementsQueued reflects the full eligible set.
//
// The internal batchSize clamp at schema.resolvers.go:1161-1163
// (batch > 100 → batch = 100) is verified by code inspection only — observing
// the runtime batch slice boundaries requires a counting fake that executes
// the RunWithContext closure end-to-end, which in turn requires a fake LLM
// worker capable of running to completion. The clamp lives in the graphql
// resolver layer (not the orchestrator), so it cannot be intercepted via
// orchestrator.Config without either (a) breaking the TestResolverStructureCanary
// or (b) polluting AppDeps with a test-only observer field. Behavioral
// observation is tracked as CA-506 (Deferred).
func TestEnrichAllRequirements_BatchSize200_AcceptedWithoutError(t *testing.T) {
	store := graphstore.NewStore()
	result := &indexer.IndexResult{RepoName: "repo", RepoPath: "/tmp"}
	repo, _ := store.StoreIndexResult(t.Context(), result)
	reqs := []*graphstore.StoredRequirement{
		{ExternalID: "R-a", Title: "unenriched a"},
		{ExternalID: "R-b", Title: "unenriched b"},
		{ExternalID: "R-c", Title: "unenriched c"},
	}
	_, _ = store.StoreRequirements(t.Context(), repo.ID, reqs)

	fw := &enrichFakeWorker{}
	snap := resolution.Snapshot{Provider: "test", Model: "test-model"}
	caller := llmcall.New(fw, resolution.NewFrozenResolver(snap), nil)

	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{
		SkipStartupReconciliation: true,
		Retry:                     orchestrator.RetryPolicy{MaxAttempts: 1},
	})
	t.Cleanup(func() { _ = orch.Shutdown(2 * time.Second) })

	cfg := config.Defaults() // BulkEnrichMaxRequirements=500 (well above 100)

	r := &Resolver{
		Deps: &appdeps.AppDeps{
			EventBus:     events.NewBus(),
			LLMCaller:    caller,
			Worker:       new(worker.Client),
			Orchestrator: orch,
			Config:       cfg,
			LLMResolver:  newStubLLMResolver(),
		},
		Store: store,
	}

	batchSize := 200 // > 100 → will be clamped to 100 inside the resolver
	got, err := r.Mutation().EnrichAllRequirements(ctxAdmin(), repo.ID, nil, &batchSize)
	if err != nil {
		t.Fatalf("EnrichAllRequirements: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	// All 3 requirements are unenriched → queued. The batchSize=200 → clamped to 100
	// internally, but the total queued still reflects all eligible requirements.
	if got.RequirementsQueued != 3 {
		t.Errorf("expected RequirementsQueued=3, got %d", got.RequirementsQueued)
	}
	if got.JobID == "none" || got.JobID == "" {
		t.Errorf("expected a real job ID (batchSize clamp is internal; job should be enqueued), got %q", got.JobID)
	}
	// Note: the internal batchSize clamp (batch=100) is tested structurally via
	// code inspection at schema.resolvers.go:1161-1163. A behavioral test that
	// executes the closure with a real LLM would require an integration harness;
	// that path is out of scope here per D-024b. The observable contract is:
	// passing batchSize=200 does not return an error and does not clamp RequirementsQueued.
}
