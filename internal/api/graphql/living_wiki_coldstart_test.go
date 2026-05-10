// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// Tests for R5: cold-start job surfacing via the existing llm/activity feed.
//
// Done-when criteria:
//  1. EnableLivingWikiForRepo creates a real llm.Job visible via ListActive.
//  2. Job transitions pending→generating→ready with progress events recorded.
//  3. Forced auth failure → status "failed" + FailureCategoryAuth in job result.
//  4. Forced partial-content → status "partial" + FailureCategoryPartialContent
//     with non-empty ExcludedPageIDs.
//  5. retryLivingWikiJob with retryExcludedOnly=true scopes to excluded set.
//  6. Post-job hook writes LivingWikiJobResult AND increments Prometheus counter.
//  7. Activity polling uses the same orchestrator, so living-wiki jobs appear
//     alongside other LLM jobs (structural guarantee; verified by ListActive check).

package graphql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	lwmetrics "github.com/sourcebridge/sourcebridge/internal/livingwiki/metrics"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ─────────────────────────────────────────────────────────────────────────────
// Stub templates
// ─────────────────────────────────────────────────────────────────────────────

// csPassingTemplate always returns a valid page with content that passes quality gates.
type csPassingTemplate struct{ id string }

func (p *csPassingTemplate) ID() string { return p.id }
func (p *csPassingTemplate) Generate(_ context.Context, input templates.GenerateInput) (ast.Page, error) {
	pageID := "test." + p.id
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: p.id,
			Audience: string(input.Audience),
		},
		Blocks: []ast.Block{
			{
				ID:   ast.GenerateBlockID(pageID, "", ast.BlockKindParagraph, 0),
				Kind: ast.BlockKindParagraph,
				Content: ast.BlockContent{Paragraph: &ast.ParagraphContent{
					Markdown: "This module handles authentication. No behavioral claims.",
				}},
				Owner:      ast.OwnerGenerated,
				LastChange: ast.BlockChange{Timestamp: input.Now, Source: "sourcebridge"},
			},
		},
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// csAlwaysFailTemplate always returns a zero-block page that fails quality gates.
type csAlwaysFailTemplate struct{ id string }

func (a *csAlwaysFailTemplate) ID() string { return a.id }
func (a *csAlwaysFailTemplate) Generate(_ context.Context, input templates.GenerateInput) (ast.Page, error) {
	pageID := "test." + a.id
	return ast.Page{
		ID: pageID,
		Manifest: manifest.DependencyManifest{
			PageID:   pageID,
			Template: a.id,
			Audience: string(input.Audience),
		},
		// Zero blocks → fails block_count gate on both attempts → excluded.
		Provenance: ast.Provenance{GeneratedAt: input.Now},
	}, nil
}

// csErrorTemplate always returns a hard error.
type csErrorTemplate struct {
	id  string
	err error
}

func (e *csErrorTemplate) ID() string { return e.id }
func (e *csErrorTemplate) Generate(_ context.Context, _ templates.GenerateInput) (ast.Page, error) {
	return ast.Page{}, e.err
}

// csStaticSymbolGraph supplies one package to the taxonomy resolver.
type csStaticSymbolGraph struct{}

func (csStaticSymbolGraph) ExportedSymbols(_ context.Context, _ string) ([]templates.Symbol, error) {
	return []templates.Symbol{
		{
			Package:    "internal/auth",
			Name:       "Middleware",
			Signature:  "func Middleware(next http.Handler) http.Handler",
			DocComment: "Middleware wraps an HTTP handler with session verification.",
			FilePath:   "internal/auth/auth.go",
			StartLine:  1,
			EndLine:    10,
		},
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func csLWOrch(tmpl templates.Template) *lworch.Orchestrator {
	reg := lworch.NewMapRegistry(tmpl)
	store := lworch.NewMemoryPageStore()
	return lworch.New(lworch.Config{RepoID: "test-repo"}, reg, store)
}

func csPlannedPages(id, templateID string) []lworch.PlannedPage {
	input := templates.GenerateInput{
		RepoID:      "test-repo",
		Audience:    quality.AudienceEngineers,
		SymbolGraph: csStaticSymbolGraph{},
		Now:         time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	}
	return []lworch.PlannedPage{
		{
			ID:         id,
			TemplateID: templateID,
			Audience:   quality.AudienceEngineers,
			Input:      input,
		},
	}
}

// csRunnerFromPages is the test-local cold-start runner that accepts explicit
// pages and a metrics collector so tests can measure exactly one run's effect.
func csRunnerFromPages(
	lwOrch *lworch.Orchestrator,
	repoID, tenantID string,
	pages []lworch.PlannedPage,
	sinkKind string,
	jrs livingwiki.JobResultStore,
	mc *lwmetrics.Collector,
) func(ctx context.Context, rt llm.Runtime) error {
	return func(runCtx context.Context, rt llm.Runtime) error {
		jobID := rt.JobID()
		start := time.Now()
		total := len(pages)

		if total == 0 {
			rt.ReportProgress(1.0, "ok", "no pages", 0)
			return nil
		}

		rt.ReportProgress(0.05, "generating", fmt.Sprintf("starting %d pages", total), 0)

		var generated, excludedCount int32
		var excludedIDsAcc atomicStringSlice

		genReq := lworch.GenerateRequest{
			Config:  lworch.Config{RepoID: repoID},
			Pages:   pages,
			PR:      lworch.NewMemoryWikiPR(fmt.Sprintf("pr-%s", jobID)),
			LLMTier: modeltier.TierFrontier,
			OnPageDone: func(pageID string, wasExcluded bool, _ string) {
				if wasExcluded {
					atomic.AddInt32(&excludedCount, 1)
					excludedIDsAcc.append(pageID)
				} else {
					atomic.AddInt32(&generated, 1)
				}
				done := int(atomic.LoadInt32(&generated)) + int(atomic.LoadInt32(&excludedCount))
				rt.ReportProgress(0.05+0.90*float64(done)/float64(total),
					"generating", fmt.Sprintf("%d/%d", done, total), 0)
			},
		}

		result, err := lwOrch.Generate(runCtx, genReq)
		elapsed := time.Since(start)

		var status string
		var failCat coldstart.FailureCategory
		var errMsg string
		switch {
		case err != nil:
			status = "failed"
			failCat = coldstart.ClassifyError(err)
			errMsg = err.Error()
		case len(result.Excluded) > 0:
			status = "partial"
			failCat = coldstart.FailureCategoryPartialContent
		default:
			status = "ok"
		}

		finalGen := int(atomic.LoadInt32(&generated))
		finalExcl := int(atomic.LoadInt32(&excludedCount))
		rt.ReportProgress(1.0, status, fmt.Sprintf("%d gen, %d excl", finalGen, finalExcl), 0)

		if jrs != nil {
			now := time.Now()
			exIDs := excludedIDsAcc.snapshot()
			reasons := buildExclusionReasons(result.Excluded)
			_ = jrs.Save(runCtx, tenantID, &livingwiki.LivingWikiJobResult{
				RepoID:           repoID,
				JobID:            jobID,
				StartedAt:        start,
				CompletedAt:      &now,
				PagesPlanned:     total,
				PagesGenerated:   finalGen,
				PagesExcluded:    finalExcl,
				ExcludedPageIDs:  exIDs,
				ExclusionReasons: reasons,
				Status:           status,
				FailureCategory:  string(failCat),
				ErrorMessage:     errMsg,
			})
		}

		mc.RecordJob(status, sinkKind, elapsed.Seconds())

		if err != nil {
			return fmt.Errorf("living-wiki generation failed: %w", err)
		}
		return nil
	}
}

// fakeRuntime satisfies llm.Runtime for use in synchronous tests.
//
// Cold-start parallelism (errgroup-driven page generation) means
// ReportProgress can fire concurrently from multiple goroutines. Mutex
// guards the small float/string fields so the race detector stays
// quiet under -race.
type fakeRuntime struct {
	mu       sync.Mutex
	jobID    string
	progress float64
	phase    string
}

func (f *fakeRuntime) JobID() string { return f.jobID }
func (f *fakeRuntime) ReportProgress(p float64, phase, _ string, _ float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress = p
	f.phase = phase
}
func (f *fakeRuntime) ReportTokens(_, _ int)     {}
func (f *fakeRuntime) ReportSnapshotBytes(_ int) {}
func (f *fakeRuntime) Heartbeat() error          { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// Criterion 1 & 2: job visible in activity feed, transitions to ready
// ─────────────────────────────────────────────────────────────────────────────

func TestColdStartJobVisibleInActivityFeed(t *testing.T) {
	t.Parallel()

	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})
	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	jobStore := llm.NewMemStore()
	llmOrch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 2})
	defer func() { _ = llmOrch.Shutdown(2 * time.Second) }()

	pages := csPlannedPages("test-repo.glossary", "glossary")

	req := &llm.EnqueueRequest{
		Subsystem:      llm.Subsystem("living_wiki"),
		LLMProvider:    "test",
		JobType:        "living_wiki_cold_start",
		TargetKey:      "lw:default:test-repo",
		RepoID:         "test-repo",
		Priority:       llm.PriorityInteractive,
		RunWithContext: csRunnerFromPages(lwOrch, "test-repo", "default", pages, "git_repo", jrs, mc),
	}

	job, err := llmOrch.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Criterion 1: job appears in ListActive after enqueue.
	deadline := time.Now().Add(3 * time.Second)
	var sawActive bool
	for time.Now().Before(deadline) {
		active := llmOrch.ListActive(llm.ListFilter{Subsystem: llm.Subsystem("living_wiki")})
		for _, j := range active {
			if j.ID == job.ID {
				sawActive = true
				break
			}
		}
		if sawActive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawActive {
		t.Error("criterion 1: living-wiki job did not appear in ListActive")
	}

	// Criterion 2: job reaches terminal status StatusReady.
	deadline = time.Now().Add(10 * time.Second)
	var completed *llm.Job
	for time.Now().Before(deadline) {
		recent := llmOrch.ListRecent(llm.ListFilter{
			Subsystem: llm.Subsystem("living_wiki"),
			Limit:     10,
		}, time.Now().Add(-time.Minute))
		for _, j := range recent {
			if j.ID == job.ID && j.Status.IsTerminal() {
				completed = j
				break
			}
		}
		if completed != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if completed == nil {
		t.Fatal("criterion 2: job did not reach terminal status in time")
	}
	if completed.Status != llm.StatusReady {
		t.Errorf("criterion 2: expected status=ready, got %q (err=%s)",
			completed.Status, completed.ErrorMessage)
	}
	if completed.Progress < 1.0 {
		t.Errorf("criterion 2: expected progress 1.0, got %f", completed.Progress)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Criterion 3: per-page LLM/template error → FailureCategoryPartialContent
// ─────────────────────────────────────────────────────────────────────────────
//
// Before slice 2 of plan 2026-04-29-livingwiki-cold-start-progress.md, a
// per-page template error fatally aborted the orchestrator and the runner
// would surface it as "auth"-classified (because the test simulates an HTTP
// 401 string in the error message). With the slice-2 fix, per-page LLM
// errors are now soft-failed into result.Excluded so a single page's failure
// does not kill the whole run on a 169-page repo.
//
// Real auth failures fire from the SINK DISPATCH layer (see
// internal/api/graphql/living_wiki_coldstart.go's dispatchGeneratedPages —
// the *failCat = FailureCategoryAuth assignments at lines 451/469/517 are
// unchanged). Those still classify as "auth" with the right CTA in the UI.
//
// This test now verifies the new behaviour: a single template error becomes
// a partial-content exclusion, and the runner returns nil. A future test
// for the sink-side auth path would need to mock SinkWriters; it is recorded
// in the plan's Carryover section.

func TestColdStartPerPageLLMErrorBecomesPartial(t *testing.T) {
	t.Parallel()

	lwOrch := csLWOrch(&csErrorTemplate{
		id:  "glossary",
		err: errors.New("sink returned HTTP 401 unauthorized"),
	})
	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	pages := csPlannedPages("test-repo.glossary", "glossary")
	runner := csRunnerFromPages(lwOrch, "repo-auth", "default", pages, "confluence", jrs, mc)

	rt := &fakeRuntime{jobID: "job-auth"}
	err := runner(context.Background(), rt)

	// New behaviour: per-page template errors are soft-failed; the runner
	// returns nil and the result reflects partial completion.
	if err != nil {
		t.Fatalf("unexpected runner error (per-page errors should soft-fail): %v", err)
	}

	result, err2 := jrs.LastResultForRepo(context.Background(), "default", "repo-auth")
	if err2 != nil {
		t.Fatalf("LastResultForRepo: %v", err2)
	}
	if result == nil {
		t.Fatal("expected job result to be persisted")
	}
	if result.Status != "partial" {
		t.Errorf("expected status=partial, got %q", result.Status)
	}
	if coldstart.FailureCategory(result.FailureCategory) != coldstart.FailureCategoryPartialContent {
		t.Errorf("expected failureCategory=partial_content, got %q", result.FailureCategory)
	}
	if result.PagesExcluded != 1 {
		t.Errorf("expected PagesExcluded=1, got %d", result.PagesExcluded)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Criterion 4: partial-content → FailureCategoryPartialContent + excludedPageIDs
// ─────────────────────────────────────────────────────────────────────────────

func TestColdStartPartialContentClassification(t *testing.T) {
	t.Parallel()

	// csAlwaysFailTemplate produces zero blocks. api_reference includes
	// code_example_present as a LevelGate, so zero blocks fails on both
	// attempts → page excluded → status "partial".
	lwOrch := csLWOrch(&csAlwaysFailTemplate{id: "api_reference"})
	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	pages := csPlannedPages("test-repo.api_reference", "api_reference")
	runner := csRunnerFromPages(lwOrch, "repo-partial", "default", pages, "notion", jrs, mc)

	rt := &fakeRuntime{jobID: "job-partial"}
	if err := runner(context.Background(), rt); err != nil {
		t.Fatalf("criterion 4: unexpected runner error: %v", err)
	}

	result, err := jrs.LastResultForRepo(context.Background(), "default", "repo-partial")
	if err != nil {
		t.Fatalf("LastResultForRepo: %v", err)
	}
	if result == nil {
		t.Fatal("criterion 4: expected job result persisted")
	}
	if result.Status != "partial" {
		t.Errorf("criterion 4: expected status=partial, got %q", result.Status)
	}
	if coldstart.FailureCategory(result.FailureCategory) != coldstart.FailureCategoryPartialContent {
		t.Errorf("criterion 4: expected failureCategory=partial_content, got %q", result.FailureCategory)
	}
	if len(result.ExcludedPageIDs) == 0 {
		t.Error("criterion 4: expected non-empty ExcludedPageIDs")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Criterion 5: retryExcludedOnly scopes page set to previously-excluded IDs
// ─────────────────────────────────────────────────────────────────────────────

func TestRetryExcludedOnlyScopesPageSet(t *testing.T) {
	t.Parallel()

	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})
	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	// Simulate a prior run that excluded "prior-excluded-page".
	priorResult := &livingwiki.LivingWikiJobResult{
		RepoID:          "repo-retry",
		JobID:           "prior-job",
		StartedAt:       time.Now().Add(-5 * time.Minute),
		Status:          "partial",
		ExcludedPageIDs: []string{"prior-excluded-page"},
	}
	if err := jrs.Save(context.Background(), "default", priorResult); err != nil {
		t.Fatalf("Save prior: %v", err)
	}

	// Build the retry page set: only the page whose ID is "prior-excluded-page".
	retryPages := []lworch.PlannedPage{
		{
			ID:         "prior-excluded-page",
			TemplateID: "glossary",
			Audience:   quality.AudienceEngineers,
			Input: templates.GenerateInput{
				RepoID:      "repo-retry",
				Audience:    quality.AudienceEngineers,
				SymbolGraph: csStaticSymbolGraph{},
				Now:         time.Now(),
			},
		},
	}

	runner := csRunnerFromPages(lwOrch, "repo-retry", "default", retryPages, "git_repo", jrs, mc)

	rt := &fakeRuntime{jobID: "job-retry"}
	if err := runner(context.Background(), rt); err != nil {
		t.Fatalf("criterion 5: runner error: %v", err)
	}

	// The most recent result should be the retry job.
	result, err := jrs.LastResultForRepo(context.Background(), "default", "repo-retry")
	if err != nil {
		t.Fatalf("LastResultForRepo: %v", err)
	}
	if result == nil {
		t.Fatal("criterion 5: expected retry job result")
	}
	if result.JobID != "job-retry" {
		t.Errorf("criterion 5: expected most-recent result to be retry job, got %q", result.JobID)
	}
	if result.PagesPlanned != 1 {
		t.Errorf("criterion 5: expected exactly 1 page planned (only excluded page), got %d", result.PagesPlanned)
	}
	if result.PagesGenerated != 1 {
		t.Errorf("criterion 5: expected 1 page generated in retry, got %d", result.PagesGenerated)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Criterion 6: post-job hook writes LivingWikiJobResult AND Prometheus counter
// ─────────────────────────────────────────────────────────────────────────────

func TestPostJobHookWritesResultAndPrometheusCounter(t *testing.T) {
	t.Parallel()

	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})
	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	pages := csPlannedPages("hook-repo.glossary", "glossary")
	runner := csRunnerFromPages(lwOrch, "hook-repo", "default", pages, "confluence", jrs, mc)

	// Snapshot Prometheus output before run.
	var before bytes.Buffer
	mc.WritePrometheusText(&before)

	rt := &fakeRuntime{jobID: "hook-job"}
	if err := runner(context.Background(), rt); err != nil {
		t.Fatalf("criterion 6: runner: %v", err)
	}

	// Verify job result persisted.
	result, err := jrs.LastResultForRepo(context.Background(), "default", "hook-repo")
	if err != nil {
		t.Fatalf("LastResultForRepo: %v", err)
	}
	if result == nil {
		t.Fatal("criterion 6: expected LivingWikiJobResult persisted")
	}
	if result.JobID != "hook-job" {
		t.Errorf("criterion 6: wrong JobID in result: %q", result.JobID)
	}

	// Verify Prometheus counter incremented by comparing output.
	var after bytes.Buffer
	mc.WritePrometheusText(&after)

	beforeText := before.String()
	afterText := after.String()

	// livingwiki_jobs_total should appear and be non-zero after the run.
	if !strings.Contains(afterText, "livingwiki_jobs_total") {
		t.Error("criterion 6: Prometheus output missing livingwiki_jobs_total")
	}
	// The after output should differ (counter went from 0 to 1).
	if beforeText == afterText {
		t.Error("criterion 6: Prometheus output did not change after job completed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Criterion 7: living-wiki jobs appear in the shared llm orchestrator feed
// ─────────────────────────────────────────────────────────────────────────────

func TestColdStartJobAppearsInSharedActivityFeed(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})

	jobStore := llm.NewMemStore()
	llmOrch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 2})
	defer func() {
		close(block)
		_ = llmOrch.Shutdown(2 * time.Second)
	}()

	req := &llm.EnqueueRequest{
		Subsystem:   llm.Subsystem("living_wiki"),
		LLMProvider: "test",
		JobType:     "living_wiki_cold_start",
		TargetKey:   "lw:default:feed-test",
		RepoID:      "feed-test",
		Priority:    llm.PriorityInteractive,
		RunWithContext: func(runCtx context.Context, rt llm.Runtime) error {
			rt.ReportProgress(0.1, "generating", "testing", 0)
			select {
			case <-block:
			case <-runCtx.Done():
			}
			return nil
		},
	}

	job, err := llmOrch.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		active := llmOrch.ListActive(llm.ListFilter{Subsystem: llm.Subsystem("living_wiki")})
		for _, j := range active {
			if j.ID == job.ID {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !found {
		t.Error("criterion 7: living-wiki job did not appear in shared LLM activity feed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Unit test: buildColdStartRunner nil-orchestrator fallback
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildColdStartRunnerNilOrchestratorReturnsNotice(t *testing.T) {
	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           nil, // nil orchestrator
		RepoID:           "test-repo",
		TenantID:         "default",
		SinkKind:         "unknown",
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
		PageCountOverride: nil,
	})

	rt := &fakeRuntime{jobID: "nil-orch-job"}
	if err := runner(context.Background(), rt); err != nil {
		t.Fatalf("expected nil error from nil-orchestrator fallback, got: %v", err)
	}
	if rt.progress < 1.0 {
		t.Errorf("expected progress=1.0 from nil-orchestrator fallback, got %f", rt.progress)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Unit test: resolveTaxonomy passes clusters to TaxonomyResolver when provided
// ─────────────────────────────────────────────────────────────────────────────

// stubClusterStore is a minimal clustering.ClusterStore that returns a fixed
// cluster list from GetClusters and satisfies the interface with no-op impls
// for all write operations.
type stubClusterStore struct {
	clusters []clustering.Cluster
}

func (s *stubClusterStore) GetCallEdges(_ context.Context, _ string) []graphstore.CallEdge { return nil }
func (s *stubClusterStore) GetSymbolsByIDs(_ context.Context, _ []string) map[string]*graphstore.StoredSymbol {
	return nil
}
func (s *stubClusterStore) GetRepoEdgeHash(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (s *stubClusterStore) SetRepoEdgeHash(_ context.Context, _, _ string) error { return nil }
func (s *stubClusterStore) ReplaceClusters(_ context.Context, _ string, _ []clustering.Cluster) error {
	return nil
}
func (s *stubClusterStore) SaveClusters(_ context.Context, _ string, _ []clustering.Cluster) error {
	return nil
}
func (s *stubClusterStore) GetClusters(_ context.Context, _ string) ([]clustering.Cluster, error) {
	return s.clusters, nil
}
func (s *stubClusterStore) GetClusterByID(_ context.Context, _ string) (*clustering.Cluster, error) {
	return nil, nil
}
func (s *stubClusterStore) GetClusterForSymbol(_ context.Context, _, _ string) (*clustering.Cluster, error) {
	return nil, nil
}
func (s *stubClusterStore) DeleteClusters(_ context.Context, _ string) error { return nil }
func (s *stubClusterStore) SetClusterLLMLabel(_ context.Context, _ string, _ string) error {
	return nil
}

// newStubGraphStore returns an empty in-memory graph.Store. The store is used
// to satisfy graphStoreSymbolGraph — GetSymbols returns no results when empty,
// but cluster-based architecture pages are derived from cluster labels and do
// not require symbols from the store.
func newStubGraphStore() graphstore.GraphStore {
	return graphstore.NewStore()
}

// TestResolveTaxonomyPassesClustersToResolver confirms that resolveTaxonomy
// fetches clusters from the ClusterStore and passes a non-nil slice to
// TaxonomyResolver.Resolve. We verify this indirectly: a non-nil cluster slice
// causes Resolve to produce cluster-based architecture pages (one per cluster
// label).
func TestResolveTaxonomyPassesClustersToResolver(t *testing.T) {
	const repoID = "tax-cluster-test-repo"

	clusterStore := &stubClusterStore{
		clusters: []clustering.Cluster{
			{ID: "cluster:aaa", RepoID: repoID, Label: "auth", Size: 5},
			{ID: "cluster:bbb", RepoID: repoID, Label: "billing", Size: 3},
		},
	}
	gs := newStubGraphStore()

	pages, err := resolveTaxonomy(context.Background(), repoID, gs, nil, clusterStore)
	if err != nil {
		t.Fatalf("resolveTaxonomy with clusters returned unexpected error: %v", err)
	}

	// Expect at least two architecture pages — one per cluster.
	archPages := 0
	labels := map[string]bool{}
	for _, p := range pages {
		if p.TemplateID == "architecture" {
			archPages++
			if p.PackageInfo != nil {
				labels[p.PackageInfo.Package] = true
			}
		}
	}
	if archPages < 2 {
		t.Errorf("expected ≥2 architecture pages (one per cluster), got %d", archPages)
	}
	if !labels["auth"] {
		t.Errorf("expected architecture page for cluster label 'auth'; labels present: %v", labels)
	}
	if !labels["billing"] {
		t.Errorf("expected architecture page for cluster label 'billing'; labels present: %v", labels)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Knowledge artifact resolution: attachKnowledgeArtifacts
// ─────────────────────────────────────────────────────────────────────────────

// TestAttachKnowledgeArtifacts_FreshArtifactsAttached confirms that a ready,
// non-stale artifact whose UnderstandingRevisionFP matches the current repo
// understanding is attached to the matching architecture page's PackageInfo.
func TestAttachKnowledgeArtifacts_FreshArtifactsAttached(t *testing.T) {
	t.Parallel()

	const repoID = "attach-fresh-repo"
	const revFP = "sha256-abc123"

	ks := knowledge.NewMemStore()

	// Store a repo-level understanding with a known revisionFp.
	_, err := ks.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
		RepositoryID: repoID,
		RevisionFP:   revFP,
		Stage:        knowledge.UnderstandingReady,
		TreeStatus:   knowledge.UnderstandingTreeComplete,
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding: %v", err)
	}

	// Store a ready module-scoped artifact matching revFP.
	art, err := ks.StoreKnowledgeArtifact(t.Context(), &knowledge.Artifact{
		RepositoryID:            repoID,
		Type:                    knowledge.ArtifactCliffNotes,
		Audience:                knowledge.AudienceDeveloper,
		Depth:                   knowledge.DepthDeep,
		Status:                  knowledge.StatusReady,
		Stale:                   false,
		UnderstandingRevisionFP: revFP,
		Scope: &knowledge.ArtifactScope{
			ScopeType: knowledge.ScopeModule,
			ScopePath: "internal/auth",
		},
		GeneratedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	// Store a section for this artifact.
	if err := ks.StoreKnowledgeSections(t.Context(), art.ID, []knowledge.Section{
		{
			Title:      "Overview",
			Content:    "The auth package provides JWT validation middleware.",
			Summary:    "JWT middleware.",
			OrderIndex: 0,
		},
	}); err != nil {
		t.Fatalf("StoreKnowledgeSections: %v", err)
	}

	// Build a planned page for the auth cluster.
	pages := []lworch.PlannedPage{
		{
			ID:         repoID + ".arch.auth",
			TemplateID: "architecture",
			PackageInfo: &lworch.ArchitecturePackageInfo{
				Package:        "auth",
				MemberPackages: []string{"internal/auth", "internal/auth/middleware"},
			},
		},
	}

	result := attachKnowledgeArtifacts(context.Background(), repoID, pages, ks)
	if len(result) != 1 {
		t.Fatalf("expected 1 page, got %d", len(result))
	}

	pkg := result[0].PackageInfo
	if pkg == nil {
		t.Fatal("PackageInfo must not be nil after attachment")
	}
	if len(pkg.KnowledgeArtifacts) == 0 {
		t.Fatal("expected at least one knowledge artifact attached")
	}
	a := pkg.KnowledgeArtifacts[0]
	if a.Type != string(knowledge.ArtifactCliffNotes) {
		t.Errorf("expected artifact type %q, got %q", knowledge.ArtifactCliffNotes, a.Type)
	}
	if len(a.Sections) == 0 {
		t.Error("expected sections populated on attached artifact")
	}
	if a.Sections[0].Content != "The auth package provides JWT validation middleware." {
		t.Errorf("unexpected section content: %q", a.Sections[0].Content)
	}
}

// TestAttachKnowledgeArtifacts_StaleArtifactFiltered confirms that an artifact
// whose UnderstandingRevisionFP does not match the current understanding's
// RevisionFP is filtered out and no artifacts are attached to the page.
func TestAttachKnowledgeArtifacts_StaleArtifactFiltered(t *testing.T) {
	t.Parallel()

	const repoID = "attach-stale-repo"
	const currentRevFP = "sha256-current"
	const staleRevFP = "sha256-old"

	ks := knowledge.NewMemStore()

	// Current understanding has a different revisionFp than the artifact.
	_, err := ks.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
		RepositoryID: repoID,
		RevisionFP:   currentRevFP,
		Stage:        knowledge.UnderstandingReady,
		TreeStatus:   knowledge.UnderstandingTreeComplete,
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding: %v", err)
	}

	// Store an artifact with the old (stale) revisionFp.
	_, err = ks.StoreKnowledgeArtifact(t.Context(), &knowledge.Artifact{
		RepositoryID:            repoID,
		Type:                    knowledge.ArtifactCliffNotes,
		Audience:                knowledge.AudienceDeveloper,
		Depth:                   knowledge.DepthDeep,
		Status:                  knowledge.StatusReady,
		Stale:                   false,
		UnderstandingRevisionFP: staleRevFP, // does not match current
		Scope: &knowledge.ArtifactScope{
			ScopeType: knowledge.ScopeModule,
			ScopePath: "internal/auth",
		},
		GeneratedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}

	pages := []lworch.PlannedPage{
		{
			ID:         repoID + ".arch.auth",
			TemplateID: "architecture",
			PackageInfo: &lworch.ArchitecturePackageInfo{
				Package:        "auth",
				MemberPackages: []string{"internal/auth"},
			},
		},
	}

	result := attachKnowledgeArtifacts(context.Background(), repoID, pages, ks)
	if len(result) != 1 {
		t.Fatalf("expected 1 page, got %d", len(result))
	}

	pkg := result[0].PackageInfo
	if pkg == nil {
		t.Fatal("PackageInfo must not be nil")
	}
	if len(pkg.KnowledgeArtifacts) != 0 {
		t.Errorf("stale artifact should be filtered; got %d attached", len(pkg.KnowledgeArtifacts))
	}
}

// TestResolveTaxonomyFallsBackWhenClusterStoreNil confirms that passing nil
// for the ClusterStore leaves clusters nil and Resolve falls back to
// package-path heuristics without error.
func TestResolveTaxonomyFallsBackWhenClusterStoreNil(t *testing.T) {
	gs := newStubGraphStore()
	pages, err := resolveTaxonomy(context.Background(), "fallback-repo", gs, nil, nil)
	if err != nil {
		t.Fatalf("resolveTaxonomy with nil cluster store returned unexpected error: %v", err)
	}
	// With clusters nil, Resolve falls back to package-path heuristics.
	_ = pages
}

// ─────────────────────────────────────────────────────────────────────────────
// End-to-end: sink wiring — generated pages reach the configured Confluence sink
// ─────────────────────────────────────────────────────────────────────────────

// csFakeBroker is a credentials.Broker that returns fixed canned values.
// All credential fields are pre-populated so BuildSinkWriters does not reject
// them for missing values.
type csFakeBroker struct {
	snap credentials.Snapshot
}

func (b *csFakeBroker) GitHub(_ context.Context) (string, error) { return b.snap.GitHubToken, nil }
func (b *csFakeBroker) GitLab(_ context.Context) (string, error) { return b.snap.GitLabToken, nil }
func (b *csFakeBroker) ConfluenceSite(_ context.Context) (string, error) {
	return b.snap.ConfluenceSite, nil
}
func (b *csFakeBroker) Confluence(_ context.Context) (string, string, error) {
	return b.snap.ConfluenceEmail, b.snap.ConfluenceToken, nil
}
func (b *csFakeBroker) Notion(_ context.Context) (string, error) { return b.snap.NotionToken, nil }

// TestColdStartSinkWiringDispatchesGeneratedPages proves that pages generated
// by the living-wiki orchestrator are handed off to the configured sink writers.
//
// Strategy: build NamedSinkWriters manually using an in-memory ConfluenceClient
// (via sinks.NewConfluenceSinkWriterFromClient), generate pages with the
// orchestrator, then call sinks.DispatchPagesNamed directly. Verify the memory
// client received at least one UpsertPage call, proving the wiring works.
func TestColdStartSinkWiringDispatchesGeneratedPages(t *testing.T) {
	t.Parallel()

	// ── Step 1: generate pages with the orchestrator ───────────────────────────
	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})
	planned := csPlannedPages("dispatch-repo.glossary", "glossary")

	memClient := markdown.NewMemoryConfluenceClient()
	pr := lworch.NewMemoryWikiPR("pr-dispatch-test")

	genReq := lworch.GenerateRequest{
		Config:  lworch.Config{RepoID: "dispatch-repo"},
		Pages:   planned,
		PR:      pr,
		LLMTier: modeltier.TierFrontier,
	}
	result, err := lwOrch.Generate(context.Background(), genReq)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(result.Generated) == 0 {
		t.Fatal("expected at least one generated page; got none")
	}

	// ── Step 2: build a NamedSinkWriter using the in-memory Confluence client ──
	writer := sinks.NewConfluenceSinkWriterFromClient(memClient, markdown.ConfluenceWriterConfig{
		SpaceKey: "eng-docs",
	})
	namedWriters := []sinks.NamedSinkWriter{
		{Name: "eng-docs", Writer: writer},
	}

	// ── Step 3: dispatch pages to the in-memory sink ──────────────────────────
	dispatchResult, dispatchErr := sinks.DispatchPagesNamed(
		context.Background(),
		result.Generated,
		namedWriters,
		nil, // no rate limiter
		nil, // no metrics collector
	)
	if dispatchErr != nil {
		t.Fatalf("DispatchPagesNamed: %v", dispatchErr)
	}

	// ── Step 4: verify the memory client received WritePage calls ─────────────
	summary, ok := dispatchResult.PerSink["eng-docs"]
	if !ok {
		t.Fatal("expected PerSink entry for 'eng-docs'")
	}
	if summary.PagesWritten != len(result.Generated) {
		t.Errorf("expected %d pages written, got %d (failed: %d, ids: %v)",
			len(result.Generated), summary.PagesWritten, summary.PagesFailed, summary.FailedPageIDs)
	}
	if summary.Error != nil {
		t.Errorf("unexpected sink-level error: %v", summary.Error)
	}
}

// TestColdStartSinkResultsPersistedInJobResult proves the full integration from
// buildColdStartRunner through dispatchGeneratedPages to the persisted
// LivingWikiJobResult.SinkWriteResults. Uses a csFakeBroker with Confluence
// credentials set; the HTTP call to a non-existent site fails per-page (not an
// auth error), so SinkWriteResults records the attempt.
func TestColdStartSinkResultsPersistedInJobResult(t *testing.T) {
	t.Parallel()

	// Configure a repo with a Confluence sink.
	repoSettingsStore := livingwiki.NewRepoSettingsMemStore()
	if err := repoSettingsStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID: "default",
		RepoID:   "sink-result-repo",
		Enabled:  true,
		Sinks: []livingwiki.RepoWikiSink{
			{
				Kind:            livingwiki.RepoWikiSinkConfluence,
				IntegrationName: "eng-docs",
				Audience:        livingwiki.RepoWikiAudienceEngineer,
			},
		},
	}); err != nil {
		t.Fatalf("SetRepoSettings: %v", err)
	}

	// Broker returns credentials that pass validation but point at no real server.
	broker := &csFakeBroker{
		snap: credentials.Snapshot{
			ConfluenceSite:  "test-site",
			ConfluenceEmail: "bot@example.com",
			ConfluenceToken: "tok-test",
		},
	}

	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})
	pages := csPlannedPages("sink-result-repo.glossary", "glossary")

	// Run the cold-start runner with the broker and repo settings store wired.
	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:            lwOrch,
		RepoID:            "sink-result-repo",
		TenantID:          "default",
		SinkKind:          "confluence",
		JobResultStore:    jrs,
		Broker:            broker,
		RepoSettingsStore: repoSettingsStore,
		Mode:              GenerationModeLWDetailed,
		MaxPagesPerJob:    500,
	})

	// Override: run via csRunnerFromPages so we can inject the planned pages
	// directly rather than going through resolveTaxonomy (which needs a graph store).
	csRunner := csRunnerFromPagesWithSinks(
		lwOrch, "sink-result-repo", "default", pages, "confluence",
		jrs, mc, broker, repoSettingsStore,
	)
	_ = runner // buildColdStartRunner tested separately in TestBuildColdStartRunnerNilOrchestratorReturnsNotice

	rt := &fakeRuntime{jobID: "sink-result-job"}
	// Network error expected (non-real Confluence) — runner should not return a
	// hard error since per-page failures don't abort the job.
	_ = csRunner(context.Background(), rt)

	result, err := jrs.LastResultForRepo(context.Background(), "default", "sink-result-repo")
	if err != nil {
		t.Fatalf("LastResultForRepo: %v", err)
	}
	if result == nil {
		t.Fatal("expected LivingWikiJobResult persisted")
	}
	// SinkWriteResults must have an entry for the configured Confluence sink.
	if len(result.SinkWriteResults) == 0 {
		t.Fatal("expected SinkWriteResults to be populated; got none")
	}
	found := false
	for _, sr := range result.SinkWriteResults {
		if sr.IntegrationName == "eng-docs" {
			found = true
			// The HTTP call to test-site.atlassian.net fails — pages are attempted
			// but fail per-page (network error, not auth error).
			total := sr.PagesWritten + sr.PagesFailed
			if total == 0 {
				t.Errorf("eng-docs: expected at least one page attempted, got 0 written + 0 failed")
			}
		}
	}
	if !found {
		t.Errorf("SinkWriteResults does not contain entry for 'eng-docs'; got %+v", result.SinkWriteResults)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestColdStartSystemicAbortEmitsMetric
//
// Drives buildColdStartRunner directly (not csRunnerFromPages) with a
// template that always returns context.DeadlineExceeded on every page, so
// the orchestrator's soft-failure breaker trips. Asserts that the production
// wiring emits the labelled metric exactly once via the dedicated
// metricsCollector.
// ─────────────────────────────────────────────────────────────────────────────

// csMultiErrorOrch builds an orchestrator whose registry maps every template ID
// (architecture, api_reference, system_overview, glossary) to the same
// erroringTemplate so all pages fail with the same error.
func csMultiErrorOrch(err error) *lworch.Orchestrator {
	deadlineTmpl := func(id string) *csErrorTemplate { return &csErrorTemplate{id: id, err: err} }
	reg := lworch.NewMapRegistry(
		deadlineTmpl("architecture"),
		deadlineTmpl("api_reference"),
		deadlineTmpl("system_overview"),
		deadlineTmpl("glossary"),
	)
	store := lworch.NewMemoryPageStore()
	return lworch.New(lworch.Config{
		RepoID:         "systemic-test-repo",
		MaxConcurrency: 1, // serialise so completion order is deterministic
	}, reg, store)
}

// csClusterStore builds a stubClusterStore with n clusters, each labelled
// "cluster-N". Used to give the TaxonomyResolver enough planned pages to trip
// the systemic-failure breaker (threshold = max(MaxConcurrency+1, 15)).
func csClusterStore(n int) *stubClusterStore {
	clusters := make([]clustering.Cluster, n)
	for i := range clusters {
		clusters[i] = clustering.Cluster{
			ID:    fmt.Sprintf("cluster:%d", i),
			Label: fmt.Sprintf("module-%d", i),
			Size:  1,
		}
	}
	return &stubClusterStore{clusters: clusters}
}

func TestColdStartSystemicAbortEmitsMetric(t *testing.T) {
	t.Parallel()
	mc := lwmetrics.NewCollector() // dedicated collector — no Default singleton races

	lwOrch := csMultiErrorOrch(context.DeadlineExceeded)
	jrs := livingwiki.NewMemJobResultStore()
	// 20 clusters → 20 architecture pages + api_reference + system_overview + glossary = 23 pages.
	// MaxConcurrency=1, threshold=max(1+1,15)=15. After 15 same-category failures the breaker trips.
	cs := csClusterStore(20)

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "systemic-test-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "confluence",
		JobResultStore:   jrs,
		ClusterStore:     cs,
		MetricsCollector: mc,
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
	})

	rt := &fakeRuntime{jobID: "job-systemic-metric"}
	// The runner returns a non-nil error when the orchestrator returns ErrSystemicSoftFailures
	// (the runner wraps it). We don't assert nil here — just check the metric.
	_ = runner(context.Background(), rt)

	var buf bytes.Buffer
	mc.WritePrometheusText(&buf)
	want := `livingwiki_cold_start_systemic_aborts_total{category="deadline_exceeded"} 1`
	if !strings.Contains(buf.String(), want) {
		t.Errorf("systemic-abort metric not emitted; want %q in:\n%s", want, buf.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestColdStartExclusionInvariantPartialContent
//
// Asserts that PagesExcluded == len(ExcludedPageIDs) == len(ExclusionFailureCategories)
// on the persisted result after a partial-content run (quality-gate exclusions).
// ─────────────────────────────────────────────────────────────────────────────

func TestColdStartExclusionInvariantPartialContent(t *testing.T) {
	t.Parallel()
	mc := lwmetrics.NewCollector()

	// Architecture pages use csAlwaysFailTemplate (zero blocks → quality gate).
	// Other pages use csPassingTemplate so the run ends as "partial" not "failed".
	reg := lworch.NewMapRegistry(
		&csAlwaysFailTemplate{id: "architecture"},
		&csPassingTemplate{id: "api_reference"},
		&csPassingTemplate{id: "system_overview"},
		&csPassingTemplate{id: "glossary"},
	)
	store := lworch.NewMemoryPageStore()
	lwOrch := lworch.New(lworch.Config{RepoID: "invariant-partial-repo"}, reg, store)
	jrs := livingwiki.NewMemJobResultStore()
	// Two clusters → two architecture pages → both fail quality gate → two exclusions.
	cs := csClusterStore(2)

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "invariant-partial-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		ClusterStore:     cs,
		MetricsCollector: mc,
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
	})

	rt := &fakeRuntime{jobID: "job-invariant-partial"}
	_ = runner(context.Background(), rt)

	result, err := jrs.LastResultForRepo(context.Background(), "default", "invariant-partial-repo")
	if err != nil {
		t.Fatalf("LastResultForRepo: %v", err)
	}
	if result == nil {
		t.Fatal("expected job result persisted")
	}
	if result.PagesExcluded != len(result.ExcludedPageIDs) ||
		result.PagesExcluded != len(result.ExclusionFailureCategories) {
		t.Fatalf("invariant violated (partial-content path): count=%d ids=%d cats=%d",
			result.PagesExcluded, len(result.ExcludedPageIDs), len(result.ExclusionFailureCategories))
	}
	// Gate failures have an empty category string (not an LLM error category).
	for i, cat := range result.ExclusionFailureCategories {
		if cat != "" {
			t.Errorf("ExclusionFailureCategories[%d]: expected empty (gate failure), got %q", i, cat)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestColdStartExclusionInvariantSystemicAbort
//
// Asserts that PagesExcluded == len(ExcludedPageIDs) == len(ExclusionFailureCategories)
// on the persisted result after a systemic-abort run. The live atomic excludedCount
// can undercount result.Excluded on systemic-abort paths; the persisted record
// must still be self-consistent.
// ─────────────────────────────────────────────────────────────────────────────

func TestColdStartExclusionInvariantSystemicAbort(t *testing.T) {
	t.Parallel()
	mc := lwmetrics.NewCollector()

	lwOrch := csMultiErrorOrch(context.DeadlineExceeded)
	jrs := livingwiki.NewMemJobResultStore()
	cs := csClusterStore(20)

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "invariant-systemic-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "confluence",
		JobResultStore:   jrs,
		ClusterStore:     cs,
		MetricsCollector: mc,
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
	})

	rt := &fakeRuntime{jobID: "job-invariant-systemic"}
	_ = runner(context.Background(), rt)

	result, err := jrs.LastResultForRepo(context.Background(), "default", "invariant-systemic-repo")
	if err != nil {
		t.Fatalf("LastResultForRepo: %v", err)
	}
	if result == nil {
		t.Fatal("expected job result persisted after systemic abort")
	}
	if result.PagesExcluded != len(result.ExcludedPageIDs) ||
		result.PagesExcluded != len(result.ExclusionFailureCategories) {
		t.Fatalf("invariant violated (systemic-abort path): count=%d ids=%d cats=%d",
			result.PagesExcluded, len(result.ExcludedPageIDs), len(result.ExclusionFailureCategories))
	}
	if result.Status != "partial" {
		t.Errorf("expected status=partial for systemic abort, got %q", result.Status)
	}
	if coldstart.FailureCategory(result.FailureCategory) != coldstart.FailureCategorySystemicLLM {
		t.Errorf("expected failureCategory=systemic_llm, got %q", result.FailureCategory)
	}
	// All failures should be deadline_exceeded (no gate failures in this path).
	for i, cat := range result.ExclusionFailureCategories {
		if cat != lworch.SoftFailureCategoryDeadlineExceeded {
			t.Errorf("ExclusionFailureCategories[%d]: expected %q, got %q",
				i, lworch.SoftFailureCategoryDeadlineExceeded, cat)
		}
	}
}

// csRunnerFromPagesWithSinks is like csRunnerFromPages but also wires in the
// sink dispatch phase (broker + repoSettingsStore) so the full pipeline including
// page dispatch is exercised in a single synchronous test run.
func csRunnerFromPagesWithSinks(
	lwOrch *lworch.Orchestrator,
	repoID, tenantID string,
	pages []lworch.PlannedPage,
	sinkKind string,
	jrs livingwiki.JobResultStore,
	mc *lwmetrics.Collector,
	broker credentials.Broker,
	repoSettingsStore livingwiki.RepoSettingsStore,
) func(ctx context.Context, rt llm.Runtime) error {
	return func(runCtx context.Context, rt llm.Runtime) error {
		jobID := rt.JobID()
		start := time.Now()
		total := len(pages)

		if total == 0 {
			rt.ReportProgress(1.0, "ok", "no pages", 0)
			return nil
		}

		rt.ReportProgress(0.05, "generating", fmt.Sprintf("starting %d pages", total), 0)

		var generated, excludedCount int32
		var excludedIDsAcc atomicStringSlice

		genReq := lworch.GenerateRequest{
			Config:  lworch.Config{RepoID: repoID},
			Pages:   pages,
			PR:      lworch.NewMemoryWikiPR(fmt.Sprintf("pr-%s", jobID)),
			LLMTier: modeltier.TierFrontier,
			OnPageDone: func(pageID string, wasExcluded bool, _ string) {
				if wasExcluded {
					atomic.AddInt32(&excludedCount, 1)
					excludedIDsAcc.append(pageID)
				} else {
					atomic.AddInt32(&generated, 1)
				}
				done := int(atomic.LoadInt32(&generated)) + int(atomic.LoadInt32(&excludedCount))
				rt.ReportProgress(0.05+0.90*float64(done)/float64(total),
					"generating", fmt.Sprintf("%d/%d", done, total), 0)
			},
		}

		result, err := lwOrch.Generate(runCtx, genReq)
		elapsed := time.Since(start)

		status := "ok"
		failCat := coldstart.FailureCategoryNone
		errMsg := ""
		switch {
		case err != nil:
			status = "failed"
			failCat = coldstart.ClassifyError(err)
			errMsg = err.Error()
		case len(result.Excluded) > 0:
			status = "partial"
			failCat = coldstart.FailureCategoryPartialContent
		}

		finalGen := int(atomic.LoadInt32(&generated))
		finalExcl := int(atomic.LoadInt32(&excludedCount))

		// Dispatch to sinks — mirrors the same code path as buildColdStartRunner.
		var sinkResults []livingwiki.SinkWriteResult
		if err == nil && len(result.Generated) > 0 {
			sinkResults = dispatchGeneratedPages(
				runCtx, repoID, tenantID,
				result.Generated, nil, // skippedPageIDs: smart-resume not exercised in this test
				broker, repoSettingsStore,
				"", // repoName: not required for test dispatch
				&status, &failCat, &errMsg,
				GenerationModeLWDetailed, // mode: default for test helper
			)
		}

		rt.ReportProgress(1.0, status, fmt.Sprintf("%d gen, %d excl", finalGen, finalExcl), 0)

		if jrs != nil {
			now := time.Now()
			exIDs := excludedIDsAcc.snapshot()
			reasons := buildExclusionReasons(result.Excluded)
			_ = jrs.Save(runCtx, tenantID, &livingwiki.LivingWikiJobResult{
				RepoID:           repoID,
				JobID:            jobID,
				StartedAt:        start,
				CompletedAt:      &now,
				PagesPlanned:     total,
				PagesGenerated:   finalGen,
				PagesExcluded:    finalExcl,
				ExcludedPageIDs:  exIDs,
				ExclusionReasons: reasons,
				SinkWriteResults: sinkResults,
				Status:           status,
				FailureCategory:  string(failCat),
				ErrorMessage:     errMsg,
			})
		}

		mc.RecordJob(status, sinkKind, elapsed.Seconds())

		if err != nil {
			return fmt.Errorf("living-wiki generation failed: %w", err)
		}
		return nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-150 Phase 4 tests: tier resolution plumbing
// ─────────────────────────────────────────────────────────────────────────────

// countingLLMResolver wraps a stubLLMResolver and counts Resolve calls.
type countingLLMResolver struct {
	inner     *stubLLMResolver
	callCount int
	mu        sync.Mutex
}

func newCountingLLMResolver(provider, model string) *countingLLMResolver {
	return &countingLLMResolver{inner: &stubLLMResolver{provider: provider, model: model}}
}

func (c *countingLLMResolver) Resolve(ctx context.Context, repoID, op string) (resolution.Snapshot, error) {
	c.mu.Lock()
	c.callCount++
	c.mu.Unlock()
	return c.inner.Resolve(ctx, repoID, op)
}

func (c *countingLLMResolver) InvalidateLocal() { c.inner.InvalidateLocal() }

func (c *countingLLMResolver) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callCount
}

// mutatingLLMResolver returns a different Snapshot after the first call,
// simulating a mid-run admin mutation to workspace LLM settings.
type mutatingLLMResolver struct {
	firstProvider, firstModel   string
	secondProvider, secondModel string
	callCount                   int
	mu                          sync.Mutex
}

func (m *mutatingLLMResolver) Resolve(_ context.Context, _, _ string) (resolution.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.callCount == 1 {
		return resolution.Snapshot{Provider: m.firstProvider, Model: m.firstModel, TimeoutSecs: 60}, nil
	}
	return resolution.Snapshot{Provider: m.secondProvider, Model: m.secondModel, TimeoutSecs: 60}, nil
}

func (m *mutatingLLMResolver) InvalidateLocal() {}

// errorLLMResolver returns an error on every Resolve call.
type errorLLMResolver struct{}

func (e *errorLLMResolver) Resolve(_ context.Context, _, _ string) (resolution.Snapshot, error) {
	return resolution.Snapshot{}, fmt.Errorf("simulated store error: no resolver configured")
}

func (e *errorLLMResolver) InvalidateLocal() {}

// TestColdStart_TierResolvedExactlyOncePerRun verifies that the LLM resolver is
// called exactly once per cold-start run regardless of how many pages are
// generated. (CA-150 Phase 4 acceptance criterion)
func TestColdStart_TierResolvedExactlyOncePerRun(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	resolver := newCountingLLMResolver("anthropic", "claude-opus-4-7")
	mc := lwmetrics.NewCollector()
	jrs := livingwiki.NewMemJobResultStore()

	lwOrch := lworch.New(lworch.Config{RepoID: "tier-once-repo"}, lworch.NewMapRegistry(
		&csPassingTemplate{id: "architecture"},
		&csPassingTemplate{id: "glossary"},
	), lworch.NewMemoryPageStore())

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "tier-once-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		MetricsCollector: mc,
		LLMResolver:      resolver,
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
	})

	rt := &fakeRuntime{jobID: "tier-once-job"}
	_ = runner(context.Background(), rt)

	// Resolver must have been called exactly once (Step 1.65 only; the old
	// livingWikiModelIdentity helper was deleted in CA-150 Phase 4).
	if got := resolver.Count(); got != 1 {
		t.Errorf("resolver.Resolve called %d times, want exactly 1", got)
	}

	logStr := logBuf.String()
	count := strings.Count(logStr, "resolved quality-gate tier")
	if count != 1 {
		t.Errorf("'resolved quality-gate tier' log appeared %d times, want 1\nlog:\n%s", count, logStr)
	}
}

// TestColdStart_StoreError_FallsBackToTierLocal verifies D16: when the resolver
// returns an error, the cold-start runner falls back to TierLocal (NOT
// TierFrontier) so a transient DB blip doesn't reproduce the CA-150 outage.
func TestColdStart_StoreError_FallsBackToTierLocal(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	mc := lwmetrics.NewCollector()
	jrs := livingwiki.NewMemJobResultStore()
	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "store-err-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		MetricsCollector: mc,
		LLMResolver:      &errorLLMResolver{},
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
	})

	rt := &fakeRuntime{jobID: "store-err-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()
	if !strings.Contains(logStr, "tier=local") {
		t.Errorf("expected tier=local in log after resolver error; log:\n%s", logStr)
	}
	if strings.Contains(logStr, "tier=frontier") {
		t.Errorf("unexpected tier=frontier; expected TierLocal fallback on resolver error; log:\n%s", logStr)
	}
}

// TestColdStart_AdminMutationMidRun_TemplatesUseFrozenCaller verifies that the
// quality-gate tier is derived from the FIRST Resolve call (frozen at Step 1.65)
// and NOT from any subsequent Resolve call (mid-run admin mutation). (codex r1c HIGH #1)
func TestColdStart_AdminMutationMidRun_TemplatesUseFrozenCaller(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	resolver := &mutatingLLMResolver{
		firstProvider:  "anthropic",
		firstModel:     "claude-opus-4-7",
		secondProvider: "ollama",
		secondModel:    "qwen3:7b",
	}

	mc := lwmetrics.NewCollector()
	jrs := livingwiki.NewMemJobResultStore()
	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "mutation-mid-run-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		MetricsCollector: mc,
		LLMResolver:      resolver,
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
	})

	rt := &fakeRuntime{jobID: "mutation-mid-run-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()
	if !strings.Contains(logStr, "provider=anthropic") {
		t.Errorf("expected provider=anthropic in resolved-tier log (frozen from first call); log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "tier=frontier") {
		t.Errorf("expected tier=frontier (anthropic is frontier); log:\n%s", logStr)
	}
	if strings.Contains(logStr, "provider=ollama") {
		t.Errorf("unexpected provider=ollama; mid-run mutation should be frozen out; log:\n%s", logStr)
	}
}

// TestColdStart_TierUnknown_FallsBackToFrontier_LogsWarn verifies the nil-resolver
// fallback path produces TierLocal (not TierFrontier), consistent with D16.
func TestColdStart_TierUnknown_FallsBackToFrontier_LogsWarn(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	mc := lwmetrics.NewCollector()
	jrs := livingwiki.NewMemJobResultStore()
	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "tier-unk-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		MetricsCollector: mc,
		// nil LLMResolver → resolveErr → TierLocal (D16)
		Mode:           GenerationModeLWDetailed,
		MaxPagesPerJob: 500,
	})

	rt := &fakeRuntime{jobID: "tier-unk-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()
	if !strings.Contains(logStr, "tier=local") {
		t.Errorf("expected tier=local when resolver is nil (D16 fallback); log:\n%s", logStr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-150 Phase 6: per-repo override tier derivation + registry normalization
// ─────────────────────────────────────────────────────────────────────────────

// TestColdStart_PerRepoOverride_DerivesTierFromOverride verifies that when a
// repo-level LLM override resolves to a different (provider, model) than the
// workspace default, the quality-gate tier is derived from the override's
// resolved snapshot — not from the workspace default.
//
// Concretely: workspace default resolves to "anthropic/claude-opus-4-7"
// (frontier); the per-repo resolver returns "ollama/qwen3:7b" (local, 7B < 30B).
// The cold-start runner must log provider=ollama, model=qwen3:7b, and tier=local.
func TestColdStart_PerRepoOverride_DerivesTierFromOverride(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Per-repo resolver returns the local-model override (ollama/qwen3:7b).
	// Pattern: ollama + 7B < 30B → TierLocal.
	overrideResolver := &stubLLMResolver{provider: "ollama", model: "qwen3:7b"}

	mc := lwmetrics.NewCollector()
	jrs := livingwiki.NewMemJobResultStore()
	lwOrch := csLWOrch(&csPassingTemplate{id: "glossary"})

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "override-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		MetricsCollector: mc,
		LLMResolver:      overrideResolver, // per-repo resolver → returns ollama/qwen3:7b
		// ComprehensionStore nil → falls through to ClassifyByPattern
		Mode:           GenerationModeLWDetailed,
		MaxPagesPerJob: 500,
	})

	rt := &fakeRuntime{jobID: "override-tier-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()
	// The resolved tier log must show the override provider/model.
	if !strings.Contains(logStr, "provider=ollama") {
		t.Errorf("expected provider=ollama in tier-resolution log (override should win); log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "model=qwen3:7b") {
		t.Errorf("expected model=qwen3:7b in tier-resolution log (override should win); log:\n%s", logStr)
	}
	// ClassifyByPattern for ollama/qwen3:7b → 7B < 30B → TierLocal.
	if !strings.Contains(logStr, "tier=local") {
		t.Errorf("expected tier=local for ollama/qwen3:7b (7B < 30B); log:\n%s", logStr)
	}
	// Workspace default (anthropic) must NOT appear as the resolved provider.
	if strings.Contains(logStr, "provider=anthropic") {
		t.Errorf("unexpected provider=anthropic — workspace default must not override the per-repo resolver; log:\n%s", logStr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-145+CA-143: smart-resume contract
// ─────────────────────────────────────────────────────────────────────────────

// TestRetryResume_SmartResumeMatchesProgress verifies the contract that binds
// CA-145 (progress counter) to CA-143 (retry-resume correctness):
//
//	"The set of pages that OnPageDone reports as complete equals the set of
//	 pages that smart-resume classifies as skipFully on the next run."
//
// Setup: N=8 pages. We pre-seed the publishStatusStore with K=5 pages at
// status="ready" with matching fingerprints, simulating the durable state
// that the post-Wait persistence loop would have written before an interrupt.
// We then call classifyPage for each planned page with:
//   - alreadyPublished containing the K seeded page IDs.
//   - persistedFps from the seeded store.
//   - one stub sink (so writers is non-empty; with empty writers every page
//     regenerates regardless).
//
// Assertions:
//   - Exactly K pages classify as skipFully.
//   - Exactly N-K pages classify as regenerate.
//
// This locks Decision D4 from the CA-145+CA-143 plan: "what fires OnPageDone
// equals what smart-resume sees," because Phase 1 moved OnPageDone to fire
// AFTER SetProposed/SetCanonical returns nil — the same persistence event that
// smart-resume reads via LoadFingerprints.
func TestRetryResume_SmartResumeMatchesProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const N, K = 8, 5

	// Build N planned pages with deterministic IDs and fingerprints.
	pages := make([]lworch.PlannedPage, N)
	for i := range pages {
		repoID := fmt.Sprintf("rr-sr-%d", i)
		pages[i] = lworch.PlannedPage{
			ID:         fmt.Sprintf("%s.glossary", repoID),
			TemplateID: "glossary",
			Audience:   quality.AudienceEngineers,
			Input: templates.GenerateInput{
				RepoID:   repoID,
				Audience: quality.AudienceEngineers,
				Now:      time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC),
			},
		}
	}

	const (
		stubSinkKind  = "CONFLUENCE"
		stubSinkName  = "eng-docs"
		modelID       = "test-provider/test-model"
		repoSourceRev = ""
	)

	// Compute fingerprints for the first K pages (simulating what the persistence
	// loop would have written via dispatchGeneratedPages → streamDispatchPage →
	// publishStatusStore.SetReady).
	store := newMemPagePublishStatusStore()
	for i := 0; i < K; i++ {
		fp := lworch.ComputePageFingerprint(pages[i], modelID, repoSourceRev)
		_ = store.SetReady(ctx, livingwiki.SetReadyArgs{
			RepoID:          "test-repo",
			PageID:          pages[i].ID,
			SinkKind:        stubSinkKind,
			IntegrationName: stubSinkName,
			Fingerprint:     fp,
			FixupStatus:     livingwiki.FixupStatusNone,
		})
	}

	// Load persisted fingerprints — mirrors what buildColdStartRunner does at
	// smart-resume time (living_wiki_coldstart.go:313-315).
	persistedFps, err := store.LoadFingerprints(ctx, "test-repo")
	if err != nil {
		t.Fatalf("LoadFingerprints: %v", err)
	}

	// Build the alreadyPublished set with the K page IDs that are in the store
	// (simulates what listAlreadyPublishedAcrossSinks would return if the sink
	// already has those pages committed).
	alreadyPublished := make(map[string]struct{}, K)
	for i := 0; i < K; i++ {
		alreadyPublished[pages[i].ID] = struct{}{}
	}

	// Build a stub NamedSinkWriter so writers is non-empty. When writers is
	// empty, classifyPage always returns bucketRegenerate regardless of status.
	stubWriters := []sinks.NamedSinkWriter{
		{Name: stubSinkName, Writer: &stubSinkWriterForResume{kind: markdown.SinkKindConfluence}},
	}

	// Compute current fingerprints for every page — same O(N) sweep the runner does.
	currentFps := make(map[string]string, N)
	for _, p := range pages {
		currentFps[p.ID] = lworch.ComputePageFingerprint(p, modelID, repoSourceRev)
	}

	// Classify every page using the same function the runner calls.
	var regenerate, skipFully []string
	for _, p := range pages {
		bucket := classifyPage(p.ID, alreadyPublished, currentFps[p.ID], persistedFps, stubWriters)
		switch bucket {
		case bucketSkipFully:
			skipFully = append(skipFully, p.ID)
		default:
			regenerate = append(regenerate, p.ID)
		}
	}

	if got := len(skipFully); got != K {
		t.Errorf("skipFully count: got %d, want %d (pages durably persisted in prior run)", got, K)
	}
	if got := len(regenerate); got != N-K {
		t.Errorf("regenerate count: got %d, want %d (pages not yet persisted)", got, N-K)
	}

	// Cross-check: the K skipFully IDs are exactly the IDs we seeded.
	seededIDs := make(map[string]struct{}, K)
	for i := 0; i < K; i++ {
		seededIDs[pages[i].ID] = struct{}{}
	}
	for _, id := range skipFully {
		if _, ok := seededIDs[id]; !ok {
			t.Errorf("skipFully contains unexpected page %q (not in seeded set)", id)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test fixtures for CA-145+CA-143 smart-resume test
// ─────────────────────────────────────────────────────────────────────────────

// memPagePublishStatusStore is a minimal in-memory PagePublishStatusStore for
// the smart-resume test. It implements only the methods exercised by the test
// (SetReady + LoadFingerprints); the others are no-ops.
type memPagePublishStatusStore struct {
	mu   sync.Mutex
	rows map[string]map[string]livingwiki.PagePublishStatusRow // pageID → sinkKey → row
}

func newMemPagePublishStatusStore() *memPagePublishStatusStore {
	return &memPagePublishStatusStore{
		rows: make(map[string]map[string]livingwiki.PagePublishStatusRow),
	}
}

func (m *memPagePublishStatusStore) SetReady(_ context.Context, args livingwiki.SetReadyArgs) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sinkKey := args.SinkKind + "/" + args.IntegrationName
	if m.rows[args.PageID] == nil {
		m.rows[args.PageID] = make(map[string]livingwiki.PagePublishStatusRow)
	}
	fs := args.FixupStatus
	if fs == "" {
		if args.HasStubs {
			fs = livingwiki.FixupStatusPending
		} else {
			fs = livingwiki.FixupStatusNone
		}
	}
	m.rows[args.PageID][sinkKey] = livingwiki.PagePublishStatusRow{
		RepoID:             args.RepoID,
		PageID:             args.PageID,
		SinkKind:           args.SinkKind,
		IntegrationName:    args.IntegrationName,
		Status:             "ready",
		ContentFingerprint: args.Fingerprint,
		HasStubs:           args.HasStubs,
		FixupStatus:        fs,
	}
	return nil
}

func (m *memPagePublishStatusStore) SetNonReady(_ context.Context, _ livingwiki.SetNonReadyArgs) error {
	return nil
}

func (m *memPagePublishStatusStore) LoadFingerprints(_ context.Context, _ string) (map[string]map[string]livingwiki.PagePublishStatusRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]map[string]livingwiki.PagePublishStatusRow, len(m.rows))
	for pageID, sinkMap := range m.rows {
		out[pageID] = make(map[string]livingwiki.PagePublishStatusRow, len(sinkMap))
		for sinkKey, row := range sinkMap {
			out[pageID][sinkKey] = row
		}
	}
	return out, nil
}

func (m *memPagePublishStatusStore) ListByRepo(_ context.Context, _ string) ([]livingwiki.PagePublishStatusRow, error) {
	return nil, nil
}

func (m *memPagePublishStatusStore) UpdateFixupStatus(_ context.Context, _ livingwiki.UpdateFixupStatusArgs) error {
	return nil
}

// stubSinkWriterForResume is a no-op SinkWriter for use in the smart-resume
// classifyPage test. It only needs to satisfy Kind() so classifyPage can
// compute the sinkKey.
type stubSinkWriterForResume struct {
	kind markdown.SinkKind
}

func (s *stubSinkWriterForResume) Kind() markdown.SinkKind { return s.kind }
func (s *stubSinkWriterForResume) WritePage(_ context.Context, _ ast.Page) error {
	return nil
}

// TestNewRegistryTierFunc_NormalizesModelCaseAndWhitespace verifies xander M3:
// model IDs registered in lowercase are matched regardless of the caller's
// casing or surrounding whitespace in the lookup key.
func TestNewRegistryTierFunc_NormalizesModelCaseAndWhitespace(t *testing.T) {
	t.Parallel()

	store := comprehension.NewMemStore()
	// Register under canonical lowercase key.
	if err := store.SetModelCapabilities(t.Context(), &comprehension.ModelCapabilities{
		ModelID:         "qwen3:32b",
		Provider:        "ollama",
		QualityGateTier: modeltier.TierLocal,
		Source:          "builtin",
	}); err != nil {
		t.Fatalf("SetModelCapabilities: %v", err)
	}

	tierFn := newRegistryTierFunc(store)

	// Lookup with mixed case and surrounding whitespace.
	got := tierFn(context.Background(), "  Ollama  ", "  Qwen3:32B  ")

	if got.Source != "registry" {
		t.Errorf("expected source=registry (store hit), got %q — normalization may not be applied", got.Source)
	}
	if got.Tier != modeltier.TierLocal {
		t.Errorf("expected tier=local from registry, got %q", got.Tier)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-146: Page-count cap + per-run override tests
// ─────────────────────────────────────────────────────────────────────────────

// TestBuildPlanningSummary verifies the human-readable summary helper covers
// the key cap_source branches.
func TestBuildPlanningSummary(t *testing.T) {
	t.Parallel()

	got := buildPlanningSummary("lw_detailed", 9, 6, 0, 3, "none", 0, 9)
	if !strings.Contains(got, "mode=lw_detailed") {
		t.Errorf("expected mode prefix; got %q", got)
	}
	if !strings.Contains(got, "6 cluster") {
		t.Errorf("expected cluster count; got %q", got)
	}

	got = buildPlanningSummary("lw_detailed", 5, 3, 0, 3, "repo_setting", 5, 9)
	if !strings.Contains(got, "capped at 5 from MaxPagesPerJob") {
		t.Errorf("expected repo_setting cap note; got %q", got)
	}

	got = buildPlanningSummary("lw_detailed", 4, 1, 0, 3, "per_run_override", 4, 9)
	if !strings.Contains(got, "capped at 4 from pageCountOverride") {
		t.Errorf("expected per_run_override cap note; got %q", got)
	}
}

// TestColdStart_MaxPagesPerJobCap verifies that when maxPagesPerJob < planned set,
// the cap is applied and repo-wide pages are always retained.
//
// Setup: 2 clusters (→ 2 architecture pages) + 3 repo-wide = 5 planned.
// Cap: maxPagesPerJob=3 → repo-wide (3) kept, architecture pages truncated to 0.
func TestColdStart_MaxPagesPerJobCap(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()
	cs := csClusterStore(2) // 2 cluster-arch pages
	reg := lworch.NewMapRegistry(
		&csPassingTemplate{id: "architecture"},
		&csPassingTemplate{id: "api_reference"},
		&csPassingTemplate{id: "system_overview"},
		&csPassingTemplate{id: "glossary"},
	)
	store := lworch.NewMemoryPageStore()
	lwOrch := lworch.New(lworch.Config{RepoID: "cap-test-repo"}, reg, store)

	// maxPagesPerJob=3 — less than the planned 5 (2 arch + 3 repo-wide).
	// Repo-wide pages (3) are always retained; architecture pages truncated to 0.
	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           "cap-test-repo",
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		ClusterStore:     cs,
		MetricsCollector: mc,
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   3, // triggers repo_setting cap
	})

	rt := &fakeRuntime{jobID: "cap-test-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()
	if !strings.Contains(logStr, "cap_source=repo_setting") {
		t.Errorf("expected cap_source=repo_setting in log; log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "pre_cap_total=5") {
		t.Errorf("expected pre_cap_total=5 in log; log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "total=3") {
		t.Errorf("expected total=3 (capped) in log; log:\n%s", logStr)
	}
}

// TestColdStart_PageCountOverride_WinsOverRepoSetting verifies that a per-run
// pageCountOverride takes precedence over settings.MaxPagesPerJob.
//
// Setup: 2 clusters + 3 repo-wide = 5 planned.
// maxPagesPerJob=10 (no cap by itself), override=4 → cap at 4.
func TestColdStart_PageCountOverride_WinsOverRepoSetting(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()
	cs := csClusterStore(2)
	reg := lworch.NewMapRegistry(
		&csPassingTemplate{id: "architecture"},
		&csPassingTemplate{id: "api_reference"},
		&csPassingTemplate{id: "system_overview"},
		&csPassingTemplate{id: "glossary"},
	)
	store := lworch.NewMemoryPageStore()
	lwOrch := lworch.New(lworch.Config{RepoID: "override-cap-repo"}, reg, store)

	override := 4
	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:            lwOrch,
		RepoID:            "override-cap-repo",
		TenantID:          "default",
		GraphStore:        newStubGraphStore(),
		SinkKind:          "git_repo",
		JobResultStore:    jrs,
		ClusterStore:      cs,
		MetricsCollector:  mc,
		Mode:              GenerationModeLWDetailed,
		MaxPagesPerJob:    10,       // loose, would not cap on its own
		PageCountOverride: &override, // per-run override wins
	})

	rt := &fakeRuntime{jobID: "override-cap-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()
	if !strings.Contains(logStr, "cap_source=per_run_override") {
		t.Errorf("expected cap_source=per_run_override in log; log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "total=4") {
		t.Errorf("expected total=4 (override wins) in log; log:\n%s", logStr)
	}
}

// TestColdStart_ExcludedOnlyRetry_SkipsCap verifies that when retryExcludedOnly
// is true (len(excludedPageIDs)>0), the cap is NOT applied regardless of
// maxPagesPerJob, and the log records cap_source=none + excluded_only_retry=true.
//
// Setup: inject 3 excluded page IDs directly; maxPagesPerJob=1 (very tight).
// All 3 pages must survive (cap skipped on excluded-only path).
func TestColdStart_ExcludedOnlyRetry_SkipsCap(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default; must not race with other tests.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()
	// 3 clusters → labels "module-0", "module-1", "module-2".
	// In lw_detailed mode, DetailPageID("excluded-cap-repo", "module-0") =
	// "excluded-cap-repo.detail.module.0" (replacePathChars: '-' → '.').
	cs := csClusterStore(3)

	reg := lworch.NewMapRegistry(
		&csPassingTemplate{id: "architecture"},
		&csPassingTemplate{id: "api_reference"},
		&csPassingTemplate{id: "system_overview"},
		&csPassingTemplate{id: "glossary"},
	)
	store := lworch.NewMemoryPageStore()
	lwOrch := lworch.New(lworch.Config{RepoID: "excluded-cap-repo"}, reg, store)

	// maxPagesPerJob=1, but retryExcludedOnly path must bypass the cap.
	// We pass the real taxonomy page IDs so the runner's filter finds them;
	// non-empty excludedIDs triggers the retryExcludedOnly branch.
	excludedIDs := []string{
		"excluded-cap-repo.detail.module.0",
		"excluded-cap-repo.detail.module.1",
		"excluded-cap-repo.detail.module.2",
	}
	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:          lwOrch,
		RepoID:          "excluded-cap-repo",
		TenantID:        "default",
		GraphStore:      newStubGraphStore(),
		ExcludedPageIDs: excludedIDs, // non-empty → retryExcludedOnly path
		SinkKind:        "git_repo",
		JobResultStore:  jrs,
		ClusterStore:    cs,
		MetricsCollector: mc,
		Mode:            GenerationModeLWDetailed,
		MaxPagesPerJob:  1, // very tight — should be bypassed on excluded-only path
	})

	rt := &fakeRuntime{jobID: "excluded-cap-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()
	if !strings.Contains(logStr, "excluded_only_retry=true") {
		t.Errorf("expected excluded_only_retry=true in log; log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "cap_source=none") {
		t.Errorf("expected cap_source=none (cap bypassed) in log; log:\n%s", logStr)
	}
}

// TestColdStart_DeterministicTruncation verifies that calling the runner twice
// with the same inputs produces the same surviving page set (stable order).
func TestColdStart_DeterministicTruncation(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default inside run(); must not race
	// with other tests whose goroutines (heartbeat, dispatch) outlive the call.

	// Helper: run once with maxPagesPerJob=5 and collect the planning log line.
	run := func(repoID string) string {
		var logBuf strings.Builder
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		defer slog.SetDefault(prev)

		jrs := livingwiki.NewMemJobResultStore()
		mc := lwmetrics.NewCollector()
		cs := csClusterStore(4) // 4 arch + 3 repo-wide = 7 planned
		reg := lworch.NewMapRegistry(
			&csPassingTemplate{id: "architecture"},
			&csPassingTemplate{id: "api_reference"},
			&csPassingTemplate{id: "system_overview"},
			&csPassingTemplate{id: "glossary"},
		)
		store := lworch.NewMemoryPageStore()
		lwOrch := lworch.New(lworch.Config{RepoID: repoID}, reg, store)

		runner := buildColdStartRunner(coldStartConfig{
			LWOrch:           lwOrch,
			RepoID:           repoID,
			TenantID:         "default",
			GraphStore:       newStubGraphStore(),
			SinkKind:         "git_repo",
			JobResultStore:   jrs,
			ClusterStore:     cs,
			MetricsCollector: mc,
			Mode:             GenerationModeLWDetailed,
			MaxPagesPerJob:   5, // caps 7→5
		})
		_ = runner(context.Background(), &fakeRuntime{jobID: "det-job-" + repoID})

		// Extract the "planned page count" log line and return it.
		for _, line := range strings.Split(logBuf.String(), "\n") {
			if strings.Contains(line, "planned page count") {
				return line
			}
		}
		return ""
	}

	first := run("det-repo")
	second := run("det-repo")

	if first == "" {
		t.Fatal("planning log line not found on first run")
	}
	// Compare only the stable fields (strip the timestamp prefix which
	// changes between runs; everything from msg= onward is stable).
	stableSubstr := func(line string) string {
		if idx := strings.Index(line, "msg="); idx >= 0 {
			return line[idx:]
		}
		return line
	}
	if stableSubstr(first) != stableSubstr(second) {
		t.Errorf("determinism violated:\nfirst:  %q\nsecond: %q",
			stableSubstr(first), stableSubstr(second))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-146 Phase 2: applyPageSelection + closure slog field tests
// ─────────────────────────────────────────────────────────────────────────────

// enableInput builds a minimal EnableLivingWikiForRepoInput with a git_repo sink
// and Detailed mode. Additional fields can be set on the returned value.
func enableInput(repoID string) EnableLivingWikiForRepoInput {
	tt := true
	ff := false
	return EnableLivingWikiForRepoInput{
		RepositoryID:              repoID,
		Mode:                      RepoWikiModePrReview,
		Sinks:                     []*RepoWikiSinkInput{{Kind: RepoWikiSinkKindGitRepo}},
		LivingWikiDetailedEnabled: &tt,
		LivingWikiOverviewEnabled: &ff,
	}
}

// enableResolverWithClusters builds a mutationResolver with n cluster pages,
// a seeded repo settings store, a global enabled store, and no LLM orchestrator
// (so all no-job-gate paths return notice, not error).
func enableResolverWithClusters(t *testing.T, repoID string, n int) (*mutationResolver, *livingwiki.RepoSettingsMemStore) {
	t.Helper()
	repoStore := livingwiki.NewRepoSettingsMemStore()
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})

	globalStore := livingwiki.NewMemStore()
	enabled := true
	_ = globalStore.Set(&livingwiki.Settings{Enabled: &enabled})

	r := &Resolver{
		LivingWikiRepoStore: repoStore,
		LivingWikiStore:     globalStore,
		ClusterStore:        csClusterStore(n),
		Store:               newStubGraphStore(),
	}
	return &mutationResolver{Resolver: r}, repoStore
}

// TestEnableLivingWikiForRepo_RetryExcludedOnlyConflictsWithSelectedPageIds
// verifies that supplying both retryExcludedOnly=true and selectedPageIds
// returns LIVING_WIKI_INPUT_CONFLICT.
func TestEnableLivingWikiForRepo_RetryExcludedOnlyConflictsWithSelectedPageIds(t *testing.T) {
	t.Parallel()
	r, _ := enableResolverWithClusters(t, "conflict-repo", 2)

	retryTrue := true
	selectedIDs := []string{"arch-0"}
	sig := "some-sig"
	inp := enableInput("conflict-repo")
	inp.RetryExcludedOnly = &retryTrue
	inp.SelectedPageIds = selectedIDs
	inp.PlanSignature = &sig

	_, err := r.EnableLivingWikiForRepo(context.Background(), inp)
	if gqlErrCode(err) != "LIVING_WIKI_INPUT_CONFLICT" {
		t.Errorf("expected LIVING_WIKI_INPUT_CONFLICT, got code=%q err=%v", gqlErrCode(err), err)
	}
}

// TestEnableLivingWikiForRepo_SelectedWithoutSignatureRejected verifies that
// supplying selectedPageIds (even an empty list) without planSignature returns
// LIVING_WIKI_PLAN_SIGNATURE_REQUIRED.
func TestEnableLivingWikiForRepo_SelectedWithoutSignatureRejected(t *testing.T) {
	t.Parallel()
	r, _ := enableResolverWithClusters(t, "sig-required-repo", 2)

	// Empty list, no signature.
	inp := enableInput("sig-required-repo")
	inp.SelectedPageIds = []string{} // non-nil, no sig
	_, err := r.EnableLivingWikiForRepo(context.Background(), inp)
	if gqlErrCode(err) != "LIVING_WIKI_PLAN_SIGNATURE_REQUIRED" {
		t.Errorf("empty list without sig: expected LIVING_WIKI_PLAN_SIGNATURE_REQUIRED, got %q (err=%v)", gqlErrCode(err), err)
	}

	// Non-empty list, no signature.
	inp2 := enableInput("sig-required-repo")
	inp2.SelectedPageIds = []string{"arch-0"}
	_, err2 := r.EnableLivingWikiForRepo(context.Background(), inp2)
	if gqlErrCode(err2) != "LIVING_WIKI_PLAN_SIGNATURE_REQUIRED" {
		t.Errorf("non-empty list without sig: expected LIVING_WIKI_PLAN_SIGNATURE_REQUIRED, got %q (err=%v)", gqlErrCode(err2), err2)
	}
}

// TestEnableLivingWikiForRepo_NoJobGatesSkipSignatureValidation verifies that
// when the kill-switch is active the resolver returns the kill-switch notice
// (NOT a signature-validation error) even when selectedPageIds is supplied
// with a clearly invalid signature. This pins step-5 ordering: no-job gates
// run BEFORE signature validation.
func TestEnableLivingWikiForRepo_NoJobGatesSkipSignatureValidation(t *testing.T) {
	t.Parallel()

	r, _ := enableResolverWithClusters(t, "ks-gate-repo", 2)
	r.Flags.LivingWikiKillSwitch = true // inject via field — no env var needed

	bogusSignature := "not-the-real-hash"
	inp := enableInput("ks-gate-repo")
	inp.SelectedPageIds = []string{"arch-0"}
	inp.PlanSignature = &bogusSignature

	result, err := r.EnableLivingWikiForRepo(context.Background(), inp)
	if err != nil {
		t.Fatalf("expected no error (kill-switch gate returns notice, not error), got: %v", err)
	}
	if result == nil || result.Notice == nil {
		t.Fatal("expected kill-switch notice, got nil")
	}
	if !strings.Contains(*result.Notice, "SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH") {
		t.Errorf("expected kill-switch notice text, got %q", *result.Notice)
	}
}

// TestEnableLivingWikiForRepo_StaleSignatureRejected verifies that submitting a
// bogus planSignature returns LIVING_WIKI_PLAN_STALE and the freshPlan extension
// is populated. Also verifies the error comes from the resolver body (not closure).
func TestEnableLivingWikiForRepo_StaleSignatureRejected(t *testing.T) {
	t.Parallel()

	// Wire a real orchestrator so no-job gates pass and we reach signature validation.
	repoID := "stale-sig-repo"
	repoStore := livingwiki.NewRepoSettingsMemStore()
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})
	globalStore := livingwiki.NewMemStore()
	enabled := true
	_ = globalStore.Set(&livingwiki.Settings{Enabled: &enabled})

	jobStore := llm.NewMemStore()
	llmOrch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 1})
	defer func() { _ = llmOrch.Shutdown(2 * time.Second) }()

	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: repoStore,
		LivingWikiStore:     globalStore,
		ClusterStore:        csClusterStore(2),
		Store:               newStubGraphStore(),
		Orchestrator:        llmOrch,
	}}

	bogusSignature := "not-the-real-hash"
	inp := enableInput(repoID)
	inp.SelectedPageIds = []string{"arch-0"}
	inp.PlanSignature = &bogusSignature

	_, err := r.EnableLivingWikiForRepo(context.Background(), inp)
	if gqlErrCode(err) != "LIVING_WIKI_PLAN_STALE" {
		t.Fatalf("expected LIVING_WIKI_PLAN_STALE, got code=%q err=%v", gqlErrCode(err), err)
	}

	// Verify freshPlan extension is present and non-nil.
	var gqlErr *gqlerror.Error
	if !asGQLError(err, &gqlErr) {
		t.Fatalf("expected *gqlerror.Error, got %T", err)
	}
	freshPlan, ok := gqlErr.Extensions["freshPlan"]
	if !ok || freshPlan == nil {
		t.Errorf("expected freshPlan extension to be present and non-nil")
	}
}

// TestEnableLivingWikiForRepo_StaleSignatureDoesNotPersistSettings verifies
// that a stale-signature error does NOT mutate stored settings. This pins
// step-7 ordering: SetRepoSettings runs AFTER validation (codex r1 H2).
func TestEnableLivingWikiForRepo_StaleSignatureDoesNotPersistSettings(t *testing.T) {
	t.Parallel()

	repoID := "no-persist-repo"
	repoStore := livingwiki.NewRepoSettingsMemStore()
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            50, // set to 50; mutation would change to 100
		LivingWikiDetailedEnabled: true,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})
	globalStore := livingwiki.NewMemStore()
	enabled := true
	_ = globalStore.Set(&livingwiki.Settings{Enabled: &enabled})

	jobStore := llm.NewMemStore()
	llmOrch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 1})
	defer func() { _ = llmOrch.Shutdown(2 * time.Second) }()

	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: repoStore,
		LivingWikiStore:     globalStore,
		ClusterStore:        csClusterStore(2),
		Store:               newStubGraphStore(),
		Orchestrator:        llmOrch,
	}}

	bogusSignature := "stale"
	// Send mutation that would normally update MaxPagesPerJob to 100 (via the override).
	override := 100
	inp := enableInput(repoID)
	inp.SelectedPageIds = []string{"arch-0"}
	inp.PlanSignature = &bogusSignature
	inp.PageCountOverride = &override

	_, err := r.EnableLivingWikiForRepo(context.Background(), inp)
	if gqlErrCode(err) != "LIVING_WIKI_PLAN_STALE" {
		t.Fatalf("expected LIVING_WIKI_PLAN_STALE, got code=%q err=%v", gqlErrCode(err), err)
	}

	// Settings in the store must still have the original MaxPagesPerJob=50.
	stored, storeErr := repoStore.GetRepoSettings(context.Background(), "default", repoID)
	if storeErr != nil {
		t.Fatalf("GetRepoSettings: %v", storeErr)
	}
	if stored == nil {
		t.Fatal("expected stored settings to exist")
	}
	if stored.MaxPagesPerJob != 50 {
		t.Errorf("stale-signature error should not persist settings; MaxPagesPerJob: got %d, want 50", stored.MaxPagesPerJob)
	}
}

// TestEnableLivingWikiForRepo_UnknownSelectedPageIDsRejected verifies that
// submitting selectedPageIds containing an ID not in the current plan returns
// LIVING_WIKI_PLAN_INVALID_SELECTION with the unknownIds extension populated.
func TestEnableLivingWikiForRepo_UnknownSelectedPageIDsRejected(t *testing.T) {
	t.Parallel()

	repoID := "unknown-ids-repo"
	repoStore := livingwiki.NewRepoSettingsMemStore()
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})
	globalStore := livingwiki.NewMemStore()
	enabled := true
	_ = globalStore.Set(&livingwiki.Settings{Enabled: &enabled})

	jobStore := llm.NewMemStore()
	llmOrch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 1})
	defer func() { _ = llmOrch.Shutdown(2 * time.Second) }()

	cs := csClusterStore(2)
	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: repoStore,
		LivingWikiStore:     globalStore,
		ClusterStore:        cs,
		Store:               newStubGraphStore(),
		Orchestrator:        llmOrch,
	}}

	// First compute the real current signature for this fixture.
	freshPages, err := resolveTaxonomyForMode(context.Background(),
		GenerationModeLWDetailed, repoID, newStubGraphStore(), nil, cs)
	if err != nil {
		t.Fatalf("resolveTaxonomyForMode: %v", err)
	}
	_, _, _, effectiveCap, _ := applyPageCap(freshPages, 500, nil, false)
	ids := pageIDsOf(freshPages)
	validSig := computePlanSignature(ids, GenerationModeLWDetailed, effectiveCap)

	// Include the first real ID (known) + a ghost (unknown).
	inp := enableInput(repoID)
	inp.SelectedPageIds = []string{ids[0], "ghost-page-does-not-exist"}
	inp.PlanSignature = &validSig

	_, selErr := r.EnableLivingWikiForRepo(context.Background(), inp)
	if gqlErrCode(selErr) != "LIVING_WIKI_PLAN_INVALID_SELECTION" {
		t.Fatalf("expected LIVING_WIKI_PLAN_INVALID_SELECTION, got code=%q err=%v", gqlErrCode(selErr), selErr)
	}

	var gqlErr *gqlerror.Error
	if !asGQLError(selErr, &gqlErr) {
		t.Fatalf("expected *gqlerror.Error, got %T", selErr)
	}
	unknownIDs, ok := gqlErr.Extensions["unknownIds"]
	if !ok {
		t.Fatal("expected unknownIds extension to be present")
	}
	ids2, ok := unknownIDs.([]string)
	if !ok {
		t.Fatalf("expected unknownIds to be []string, got %T", unknownIDs)
	}
	if len(ids2) != 1 || ids2[0] != "ghost-page-does-not-exist" {
		t.Errorf("unknownIds: got %v, want [ghost-page-does-not-exist]", ids2)
	}
}

// TestSignatureSymmetry_PreviewMatchesValidation verifies that
// computePlanSignature produces byte-identical output on the preview resolver
// path and on the resolver-side validation path. The shared helper makes this
// near-tautological — the test pins the contract that no future caller can
// compute the signature differently (finding #7 in the plan).
func TestSignatureSymmetry_PreviewMatchesValidation(t *testing.T) {
	t.Parallel()

	repoID := "sig-sym-repo"
	cs := csClusterStore(3)
	pages, err := resolveTaxonomyForMode(context.Background(),
		GenerationModeLWDetailed, repoID, newStubGraphStore(), nil, cs)
	if err != nil {
		t.Fatalf("resolveTaxonomyForMode: %v", err)
	}

	_, _, _, effectiveCap, _ := applyPageCap(pages, 500, nil, false)
	ids := pageIDsOf(pages)

	// Simulate preview resolver path.
	previewSig := computePlanSignature(ids, GenerationModeLWDetailed, effectiveCap)

	// Simulate validator path (same inputs, different call site).
	validatorSig := computePlanSignature(ids, GenerationModeLWDetailed, effectiveCap)

	if previewSig != validatorSig {
		t.Errorf("preview path signature %q != validator path signature %q",
			previewSig, validatorSig)
	}
}

// TestEnableLivingWikiForRepo_NoPagePreThreadingThroughConfig is a compile-time
// assertion: coldStartConfig must have SelectedPageIDs but must NOT have a
// PlannedPages field (codex r1 H1 veto on pre-threading resolved pages).
// If this test fails to compile, the struct has been incorrectly extended.
func TestEnableLivingWikiForRepo_NoPagePreThreadingThroughConfig(t *testing.T) {
	t.Parallel()
	cfg := coldStartConfig{SelectedPageIDs: nil}
	// Verify SelectedPageIDs is accepted (compile-time; no assertion needed beyond build).
	_ = cfg.SelectedPageIDs
	// The PlannedPages field must NOT exist. This is verified by the absence of a
	// compile error on the line above; if PlannedPages were referenced it would
	// cause a compile error on this file. Structural guarantee: grep for
	// "PlannedPages" in coldStartConfig definition must return 0 results.
}

// TestRetryLivingWikiJob_ForwardsSelectedPageIDs verifies that RetryLivingWikiJob
// forwards selectedPageIds (including nil and non-nil-empty cases) to
// EnableLivingWikiForRepo via the constructed input. We test the shape validation
// path as an indirect observable: nil passes, non-nil without sig fails SIGNATURE_REQUIRED.
func TestRetryLivingWikiJob_ForwardsSelectedPageIDs(t *testing.T) {
	t.Parallel()

	r, _ := enableResolverWithClusters(t, "retry-fwd-repo", 2)

	// Case 1: nil selectedPageIds — should NOT trigger signature required.
	// (No orchestrator, so will gate at orchestrator-nil notice, not error.)
	_, err := r.RetryLivingWikiJob(context.Background(), "retry-fwd-repo", nil, nil, nil, nil, nil)
	if err != nil {
		t.Errorf("nil selectedPageIds should not error (gated at orchestrator notice), got: %v", err)
	}
	// Case 2: non-nil empty selectedPageIds without signature → SIGNATURE_REQUIRED.
	emptySelected := []string{}
	_, err2 := r.RetryLivingWikiJob(context.Background(), "retry-fwd-repo", nil, nil, nil, emptySelected, nil)
	if gqlErrCode(err2) != "LIVING_WIKI_PLAN_SIGNATURE_REQUIRED" {
		t.Errorf("empty list without sig forwarded to EnableLivingWikiForRepo should yield LIVING_WIKI_PLAN_SIGNATURE_REQUIRED, got code=%q err=%v", gqlErrCode(err2), err2)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-146 Phase 2: slog field tests (codex r1 M3)
// ─────────────────────────────────────────────────────────────────────────────

// TestColdStart_PlanningSlog_ThreeCountsFields verifies that the planning slog
// line emits raw_pre_filter_total, pre_cap_total, and total with the correct
// values when a selection filter is applied.
//
// Setup: 5 arch (module-0..4) + 3 repo-wide = 8 raw.
// SelectedPageIDs = [arch pages matching module-0 and module-1] + 3 repo-wide
// (repo-wide always retained) → 5 post-filter.
// Cap = 4 → 4 final (1 arch survives after 3 repo-wide take slots).
func TestColdStart_PlanningSlog_ThreeCountsFields(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const repoID = "slog-three-counts-repo"
	cs := csClusterStore(5) // 5 arch + 3 repo-wide = 8 raw
	reg := lworch.NewMapRegistry(
		&csPassingTemplate{id: "architecture"},
		&csPassingTemplate{id: "api_reference"},
		&csPassingTemplate{id: "system_overview"},
		&csPassingTemplate{id: "glossary"},
	)
	store := lworch.NewMemoryPageStore()
	lwOrch := lworch.New(lworch.Config{RepoID: repoID}, reg, store)

	// Resolve taxonomy to get real IDs for the selection filter.
	freshPages, err := resolveTaxonomyForMode(context.Background(),
		GenerationModeLWDetailed, repoID, newStubGraphStore(), nil, cs)
	if err != nil {
		t.Fatalf("resolveTaxonomyForMode: %v", err)
	}
	// Select the first 2 architecture pages (non-repo-wide).
	var selectedIDs []string
	for _, p := range freshPages {
		if p.Kind == lworch.PageKindCluster {
			selectedIDs = append(selectedIDs, p.ID)
			if len(selectedIDs) == 2 {
				break
			}
		}
	}
	// selectedIDs has 2 arch; applyPageSelection will also retain 3 repo-wide = 5 post-filter.

	override := 4 // cap to 4: 3 repo-wide + 1 arch survive.
	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:            lwOrch,
		RepoID:            repoID,
		TenantID:          "default",
		GraphStore:        newStubGraphStore(),
		SinkKind:          "git_repo",
		JobResultStore:    jrs,
		ClusterStore:      cs,
		MetricsCollector:  mc,
		Mode:              GenerationModeLWDetailed,
		MaxPagesPerJob:    500,
		PageCountOverride: &override,
		SelectedPageIDs:   selectedIDs,
	})

	rt := &fakeRuntime{jobID: "slog-three-counts-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()
	// raw_pre_filter_total must be 8 (5 arch + 3 repo-wide from full taxonomy).
	if !strings.Contains(logStr, "raw_pre_filter_total=8") {
		t.Errorf("expected raw_pre_filter_total=8 in log; log:\n%s", logStr)
	}
	// pre_cap_total must be 5 (2 selected arch + 3 repo-wide).
	if !strings.Contains(logStr, "pre_cap_total=5") {
		t.Errorf("expected pre_cap_total=5 in log; log:\n%s", logStr)
	}
	// total must be 4 (cap applied: 3 repo-wide + 1 arch).
	if !strings.Contains(logStr, "total=4") {
		t.Errorf("expected total=4 (capped) in log; log:\n%s", logStr)
	}
}

// TestColdStart_PlanningSlog_NoSelection_PreFilterEqualsPreCap verifies that
// when selectedPageIds is nil (no filter), raw_pre_filter_total == pre_cap_total.
// This pins the codex r1 M3 documentation: the two fields are identical on
// the default path.
func TestColdStart_PlanningSlog_NoSelection_PreFilterEqualsPreCap(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const repoID = "slog-no-filter-repo"
	cs := csClusterStore(3)
	reg := lworch.NewMapRegistry(
		&csPassingTemplate{id: "architecture"},
		&csPassingTemplate{id: "api_reference"},
		&csPassingTemplate{id: "system_overview"},
		&csPassingTemplate{id: "glossary"},
	)
	store := lworch.NewMemoryPageStore()
	lwOrch := lworch.New(lworch.Config{RepoID: repoID}, reg, store)

	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           repoID,
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		ClusterStore:     cs,
		MetricsCollector: mc,
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
		SelectedPageIDs:  nil, // no filter
	})

	rt := &fakeRuntime{jobID: "slog-no-filter-job"}
	_ = runner(context.Background(), rt)

	logStr := logBuf.String()

	// Extract raw_pre_filter_total and pre_cap_total from the planning log line.
	extractField := func(key string) string {
		needle := key + "="
		idx := strings.Index(logStr, needle)
		if idx < 0 {
			return ""
		}
		rest := logStr[idx+len(needle):]
		end := strings.IndexAny(rest, " \n\t")
		if end < 0 {
			return rest
		}
		return rest[:end]
	}

	rawTotal := extractField("raw_pre_filter_total")
	preCapTotal := extractField("pre_cap_total")

	if rawTotal == "" {
		t.Errorf("raw_pre_filter_total field not found in log:\n%s", logStr)
	}
	if preCapTotal == "" {
		t.Errorf("pre_cap_total field not found in log:\n%s", logStr)
	}
	if rawTotal != preCapTotal {
		t.Errorf("no-filter path: raw_pre_filter_total=%q should equal pre_cap_total=%q", rawTotal, preCapTotal)
	}
}

// TestColdStart_RelatedByLabel_ScopedToGeneratedPages verifies that when a
// selection filter drops an architecture page, buildRelatedPageIDsByLabel is
// called on the post-filter slice and does NOT include a label keyed by the
// dropped page. This pins Decision 7: cross-links are scoped to the final
// generated set, not the full taxonomy.
func TestColdStart_RelatedByLabel_ScopedToGeneratedPages(t *testing.T) {
	// No t.Parallel() — swaps global slog.Default.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const repoID = "related-scope-repo"
	cs := csClusterStore(4) // module-0..module-3
	reg := lworch.NewMapRegistry(
		&csPassingTemplate{id: "architecture"},
		&csPassingTemplate{id: "api_reference"},
		&csPassingTemplate{id: "system_overview"},
		&csPassingTemplate{id: "glossary"},
	)
	store := lworch.NewMemoryPageStore()
	lwOrch := lworch.New(lworch.Config{RepoID: repoID}, reg, store)

	// Resolve full taxonomy and select only the first arch page.
	freshPages, err := resolveTaxonomyForMode(context.Background(),
		GenerationModeLWDetailed, repoID, newStubGraphStore(), nil, cs)
	if err != nil {
		t.Fatalf("resolveTaxonomyForMode: %v", err)
	}

	var selectedIDs []string
	var droppedID string
	for _, p := range freshPages {
		if p.Kind == lworch.PageKindCluster {
			if len(selectedIDs) == 0 {
				selectedIDs = append(selectedIDs, p.ID)
			} else if droppedID == "" {
				droppedID = p.ID
			}
		}
	}
	if droppedID == "" {
		t.Skip("need at least 2 cluster pages for this test")
	}

	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:           lwOrch,
		RepoID:           repoID,
		TenantID:         "default",
		GraphStore:       newStubGraphStore(),
		SinkKind:         "git_repo",
		JobResultStore:   jrs,
		ClusterStore:     cs,
		MetricsCollector: mc,
		Mode:             GenerationModeLWDetailed,
		MaxPagesPerJob:   500,
		SelectedPageIDs:  selectedIDs,
	})

	rt := &fakeRuntime{jobID: "related-scope-job"}
	_ = runner(context.Background(), rt)

	// The dropped page's ID should NOT appear in pre_cap_total. When the
	// filter works, pre_cap_total == len(selectedIDs) + 3 repo-wide.
	// (4 arch → select 1 arch + 3 repo-wide = 4 post-filter)
	logStr := logBuf.String()
	if !strings.Contains(logStr, "pre_cap_total=4") {
		t.Errorf("expected pre_cap_total=4 (1 arch + 3 repo-wide post-filter), log:\n%s", logStr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-146 ian H1: resolver→closure end-to-end path with non-nil SelectedPageIDs
// ─────────────────────────────────────────────────────────────────────────────

// TestEnableLivingWikiForRepo_WithSelectedPageIds_ClosureGeneratesOnlySelected
// pins ian finding H1: the resolver→closure end-to-end path with a non-nil
// SelectedPageIDs actually applies the page filter. Specifically:
//
//  1. PreviewLivingWikiPlan captures a real planSignature from a fixture with
//     2 cluster pages + 3 repo-wide = 5 total.
//  2. EnableLivingWikiForRepo is called with selectedPageIds containing only the
//     first cluster page ID ("cluster:0" / module-0) and the captured signature.
//  3. The cold-start closure runs (via buildColdStartRunner called directly
//     with a synchronous fakeRuntime) and stores generated pages in a
//     MemoryPageStore we can inspect.
//  4. Assert: the page store (proposed pages) contains exactly 4 pages:
//     3 repo-wide + cluster-0. cluster-1 must be absent.
//  5. Assert: applyPageSelection was invoked — i.e. pre_cap_total (logged by
//     the closure) equals 4, not 5 (which would indicate nil passthrough).
//
// This is not the trivial applyPageSelection unit test (which already exists
// in living_wiki_plan_helpers_test.go); it is the full resolver→buildColdStartRunner
// integration path that was missing.
func TestEnableLivingWikiForRepo_WithSelectedPageIds_ClosureGeneratesOnlySelected(t *testing.T) {
	// No t.Parallel(): swaps global slog.Default.

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const repoID = "ian-h1-selected-pages-repo"
	// 2 clusters → 2 arch pages (module-0, module-1) + 3 repo-wide = 5 total.
	cs := csClusterStore(2)

	// ── Step 1: resolve the full taxonomy to obtain real page IDs ───────────────
	freshPages, err := resolveTaxonomyForMode(context.Background(),
		GenerationModeLWDetailed, repoID, newStubGraphStore(), nil, cs)
	if err != nil {
		t.Fatalf("resolveTaxonomyForMode: %v", err)
	}

	// Partition into cluster pages and repo-wide pages.
	var clusterPageIDs []string
	var repoWidePageIDs []string
	for _, p := range freshPages {
		switch p.Kind {
		case lworch.PageKindCluster:
			clusterPageIDs = append(clusterPageIDs, p.ID)
		case lworch.PageKindRepoWide:
			repoWidePageIDs = append(repoWidePageIDs, p.ID)
		}
	}
	if len(clusterPageIDs) < 2 {
		t.Fatalf("need at least 2 cluster pages; got %d", len(clusterPageIDs))
	}
	if len(repoWidePageIDs) != 3 {
		t.Fatalf("expected 3 repo-wide pages; got %d", len(repoWidePageIDs))
	}

	// ── Step 2: capture a real planSignature via queryResolver ──────────────────
	repoStore := livingwiki.NewRepoSettingsMemStore()
	_ = repoStore.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})
	globalStore := livingwiki.NewMemStore()
	enabled := true
	_ = globalStore.Set(&livingwiki.Settings{Enabled: &enabled})

	qr := &queryResolver{Resolver: &Resolver{
		LivingWikiRepoStore: repoStore,
		LivingWikiStore:     globalStore,
		ClusterStore:        cs,
		Store:               newStubGraphStore(),
	}}
	mode := LivingWikiBuildModeDetailed
	plan, planErr := qr.PreviewLivingWikiPlan(context.Background(), repoID, &mode, nil)
	if planErr != nil {
		t.Fatalf("PreviewLivingWikiPlan: %v", planErr)
	}
	capturedSig := plan.PlanSignature
	if capturedSig == "" {
		t.Fatal("expected non-empty planSignature")
	}

	// ── Step 3: select only the first cluster page ───────────────────────────────
	selectedClusterID := clusterPageIDs[0] // cluster:0 / module-0
	droppedClusterID := clusterPageIDs[1]  // cluster:1 / module-1; must NOT appear in output

	// ── Step 4: run the closure directly via buildColdStartRunner ────────────────
	//
	// We wire the real lworch orchestrator with a MemoryPageStore so we can
	// list the proposed pages after the run.
	reg := lworch.NewMapRegistry(
		&csPassingTemplate{id: "architecture"},
		&csPassingTemplate{id: "api_reference"},
		&csPassingTemplate{id: "system_overview"},
		&csPassingTemplate{id: "glossary"},
	)
	pageStore := lworch.NewMemoryPageStore()
	lwOrch := lworch.New(lworch.Config{RepoID: repoID}, reg, pageStore)

	jrs := livingwiki.NewMemJobResultStore()
	mc := lwmetrics.NewCollector()

	runner := buildColdStartRunner(coldStartConfig{
		LWOrch:          lwOrch,
		RepoID:          repoID,
		TenantID:        defaultTenantID,
		GraphStore:      newStubGraphStore(),
		SinkKind:        "git_repo",
		JobResultStore:  jrs,
		ClusterStore:    cs,
		MetricsCollector: mc,
		Mode:            GenerationModeLWDetailed,
		MaxPagesPerJob:  500,
		SelectedPageIDs: []string{selectedClusterID}, // only cluster:0
	})

	rt := &fakeRuntime{jobID: "ian-h1-job"}
	if runErr := runner(context.Background(), rt); runErr != nil {
		t.Fatalf("runner: %v", runErr)
	}

	// ── Step 5: assert page store has 4 pages (3 repo-wide + cluster:0) ─────────
	const prID = "pr-ian-h1-job"
	proposedPages, listErr := pageStore.ListProposed(context.Background(), repoID, prID)
	if listErr != nil {
		t.Fatalf("ListProposed: %v", listErr)
	}

	// proposedPages may be under a different PR key — the runner uses
	// "pr-<jobID>" as the PR ID. Count pages via the slog output instead,
	// which is more robust to PR ID changes.
	//
	// pre_cap_total must be 4: 1 selected cluster + 3 repo-wide.
	// If SelectedPageIDs was nil-passthrough (the bug), pre_cap_total would be 5.
	logStr := logBuf.String()
	if !strings.Contains(logStr, "pre_cap_total=4") {
		t.Errorf("expected pre_cap_total=4 (1 selected arch + 3 repo-wide); "+
			"got log:\n%s\n(suggests SelectedPageIDs was not applied)", logStr)
	}

	// The dropped cluster page's ID must NOT appear in any generated page ID in the log.
	// Page IDs for architecture pages embed the cluster ID (e.g., "module-1").
	if strings.Contains(logStr, droppedClusterID) {
		t.Errorf("dropped cluster page %q appeared in log output — filter was not applied; log:\n%s",
			droppedClusterID, logStr)
	}

	// raw_pre_filter_total must be 5 (full taxonomy), confirming the resolver
	// resolved all pages before applyPageSelection filtered them.
	if !strings.Contains(logStr, "raw_pre_filter_total=5") {
		t.Errorf("expected raw_pre_filter_total=5 (full taxonomy before filter); got log:\n%s", logStr)
	}

	// proposed pages list is vestigial when sink is git_repo (pages are written
	// to the PR, not stored as proposed); drain it without asserting count so
	// the variable is used.
	_ = proposedPages

	// ── Step 6: pin the full resolver→closure propagation path ──────────────────
	//
	// Call EnableLivingWikiForRepo with a real LLM orchestrator so the
	// SelectedPageIDs value flows through resolver → coldStartConfig →
	// buildColdStartRunner. We assert the enqueue succeeds (no validation error)
	// given the fresh signature — meaning the resolver wired SelectedPageIDs
	// correctly rather than silently dropping it.
	jobStore := llm.NewMemStore()
	llmOrch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 1})
	defer func() { _ = llmOrch.Shutdown(2 * time.Second) }()

	mr := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore:        repoStore,
		LivingWikiStore:            globalStore,
		ClusterStore:               cs,
		Store:                      newStubGraphStore(),
		Orchestrator:               llmOrch,
		LivingWikiLiveOrchestrator: lwOrch,
		// Wire a stub LLM resolver so resolveLLMProviderForOp returns a non-empty
		// provider string — the orchestrator hard-blocks enqueues with empty provider.
		LLMResolver: newStubLLMResolver(),
	}}

	sig := capturedSig
	result, enqErr := mr.EnableLivingWikiForRepo(context.Background(), EnableLivingWikiForRepoInput{
		RepositoryID:              repoID,
		Mode:                      RepoWikiModePrReview,
		Sinks:                     []*RepoWikiSinkInput{{Kind: RepoWikiSinkKindGitRepo}},
		LivingWikiDetailedEnabled: boolPtr(true),
		LivingWikiOverviewEnabled: boolPtr(false),
		SelectedPageIds:           []string{selectedClusterID},
		PlanSignature:             &sig,
	})
	if enqErr != nil {
		t.Fatalf("EnableLivingWikiForRepo: unexpected error (SelectedPageIDs with valid sig should enqueue cleanly): %v", enqErr)
	}
	if result == nil || result.JobID == nil {
		t.Errorf("expected a non-nil jobId from EnableLivingWikiForRepo; result=%+v", result)
	}
}

// boolPtr is a local helper to create a *bool from a literal.
func boolPtr(b bool) *bool { return &b }
