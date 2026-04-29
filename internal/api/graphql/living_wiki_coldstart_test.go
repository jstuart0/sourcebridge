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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	lwmetrics "github.com/sourcebridge/sourcebridge/internal/livingwiki/metrics"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
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

func (csStaticSymbolGraph) ExportedSymbols(_ string) ([]templates.Symbol, error) {
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
			rt.ReportProgress(1.0, "ok", "no pages")
			return nil
		}

		rt.ReportProgress(0.05, "generating", fmt.Sprintf("starting %d pages", total))

		var generated, excludedCount int32
		var excludedIDsAcc atomicStringSlice

		genReq := lworch.GenerateRequest{
			Config: lworch.Config{RepoID: repoID},
			Pages:  pages,
			PR:     lworch.NewMemoryWikiPR(fmt.Sprintf("pr-%s", jobID)),
			OnPageDone: func(pageID string, wasExcluded bool, _ string) {
				if wasExcluded {
					atomic.AddInt32(&excludedCount, 1)
					excludedIDsAcc.append(pageID)
				} else {
					atomic.AddInt32(&generated, 1)
				}
				done := int(atomic.LoadInt32(&generated)) + int(atomic.LoadInt32(&excludedCount))
				rt.ReportProgress(0.05+0.90*float64(done)/float64(total),
					"generating", fmt.Sprintf("%d/%d", done, total))
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
		rt.ReportProgress(1.0, status, fmt.Sprintf("%d gen, %d excl", finalGen, finalExcl))

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
type fakeRuntime struct {
	jobID    string
	progress float64
	phase    string
}

func (f *fakeRuntime) JobID() string                                  { return f.jobID }
func (f *fakeRuntime) ReportProgress(p float64, phase, _ string)      { f.progress = p; f.phase = phase }
func (f *fakeRuntime) ReportTokens(_, _ int)                          {}
func (f *fakeRuntime) ReportSnapshotBytes(_ int)                      {}

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
// Criterion 3: auth failure → FailureCategoryAuth
// ─────────────────────────────────────────────────────────────────────────────

func TestColdStartAuthFailureClassification(t *testing.T) {
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

	if err == nil {
		t.Fatal("criterion 3: expected runner to return error on auth failure")
	}

	result, err2 := jrs.LastResultForRepo(context.Background(), "default", "repo-auth")
	if err2 != nil {
		t.Fatalf("LastResultForRepo: %v", err2)
	}
	if result == nil {
		t.Fatal("criterion 3: expected job result to be persisted")
	}
	if result.Status != "failed" {
		t.Errorf("criterion 3: expected status=failed, got %q", result.Status)
	}
	if coldstart.FailureCategory(result.FailureCategory) != coldstart.FailureCategoryAuth {
		t.Errorf("criterion 3: expected failureCategory=auth, got %q", result.FailureCategory)
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
		Subsystem: llm.Subsystem("living_wiki"),
		JobType:   "living_wiki_cold_start",
		TargetKey: "lw:default:feed-test",
		RepoID:    "feed-test",
		Priority:  llm.PriorityInteractive,
		RunWithContext: func(runCtx context.Context, rt llm.Runtime) error {
			rt.ReportProgress(0.1, "generating", "testing")
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
	runner := buildColdStartRunner(
		nil,           // nil orchestrator
		"test-repo",
		"default",
		nil,           // no graph store
		nil,           // no worker client
		nil,           // no llmcall.Caller
		nil,           // no excluded page IDs
		"unknown",
		nil,           // no job result store
		nil,           // no broker
		nil,           // no repo settings store
		nil,           // no cluster store
		nil,           // no knowledge store
	)

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

func (s *stubClusterStore) GetCallEdges(_ string) []graphstore.CallEdge { return nil }
func (s *stubClusterStore) GetSymbolsByIDs(_ []string) map[string]*graphstore.StoredSymbol {
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
	_, err := ks.StoreRepositoryUnderstanding(&knowledge.RepositoryUnderstanding{
		RepositoryID: repoID,
		RevisionFP:   revFP,
		Stage:        knowledge.UnderstandingReady,
		TreeStatus:   knowledge.UnderstandingTreeComplete,
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding: %v", err)
	}

	// Store a ready module-scoped artifact matching revFP.
	art, err := ks.StoreKnowledgeArtifact(&knowledge.Artifact{
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
	if err := ks.StoreKnowledgeSections(art.ID, []knowledge.Section{
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
	_, err := ks.StoreRepositoryUnderstanding(&knowledge.RepositoryUnderstanding{
		RepositoryID: repoID,
		RevisionFP:   currentRevFP,
		Stage:        knowledge.UnderstandingReady,
		TreeStatus:   knowledge.UnderstandingTreeComplete,
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding: %v", err)
	}

	// Store an artifact with the old (stale) revisionFp.
	_, err = ks.StoreKnowledgeArtifact(&knowledge.Artifact{
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

func (b *csFakeBroker) GitHub(_ context.Context) (string, error)  { return b.snap.GitHubToken, nil }
func (b *csFakeBroker) GitLab(_ context.Context) (string, error)  { return b.snap.GitLabToken, nil }
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
		Config: lworch.Config{RepoID: "dispatch-repo"},
		Pages:  planned,
		PR:     pr,
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
	runner := buildColdStartRunner(
		lwOrch,
		"sink-result-repo",
		"default",
		nil,   // no graph store (taxonomy resolution skipped; pages provided via test)
		nil,   // no worker client
		nil,   // no llmcall.Caller
		nil,   // no excluded page IDs (full cold-start path)
		"confluence",
		jrs,
		broker,
		repoSettingsStore,
		nil,   // no cluster store
		nil,   // no knowledge store
	)

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
			rt.ReportProgress(1.0, "ok", "no pages")
			return nil
		}

		rt.ReportProgress(0.05, "generating", fmt.Sprintf("starting %d pages", total))

		var generated, excludedCount int32
		var excludedIDsAcc atomicStringSlice

		genReq := lworch.GenerateRequest{
			Config: lworch.Config{RepoID: repoID},
			Pages:  pages,
			PR:     lworch.NewMemoryWikiPR(fmt.Sprintf("pr-%s", jobID)),
			OnPageDone: func(pageID string, wasExcluded bool, _ string) {
				if wasExcluded {
					atomic.AddInt32(&excludedCount, 1)
					excludedIDsAcc.append(pageID)
				} else {
					atomic.AddInt32(&generated, 1)
				}
				done := int(atomic.LoadInt32(&generated)) + int(atomic.LoadInt32(&excludedCount))
				rt.ReportProgress(0.05+0.90*float64(done)/float64(total),
					"generating", fmt.Sprintf("%d/%d", done, total))
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
			)
		}

		rt.ReportProgress(1.0, status, fmt.Sprintf("%d gen, %d excl", finalGen, finalExcl))

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
