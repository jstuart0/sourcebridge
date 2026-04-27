// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// R5: cold-start job goroutine for living-wiki.
//
// This file provides:
//   - [buildColdStartRunner] — the RunWithContext closure injected into
//     llm.EnqueueRequest by EnableLivingWikiForRepo and RetryLivingWikiJob.
//   - Port adapters ([graphStoreSymbolGraph], [coldStartLLMCaller]) that bridge
//     the resolver's dependencies into the living-wiki orchestrator's narrow
//     interfaces, so the cold-start goroutine can call TaxonomyResolver.Resolve
//     without a full assembly.AssemblerDeps dependency.
//   - [atomicStringSlice] — concurrency-safe string accumulator for page IDs
//     collected from parallel OnPageDone callbacks.

package graphql

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"
	lwmetrics "github.com/sourcebridge/sourcebridge/internal/livingwiki/metrics"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

// buildColdStartRunner returns the RunWithContext closure for a living-wiki
// cold-start (or retry-excluded) job. It is injected into llm.EnqueueRequest
// by EnableLivingWikiForRepo and RetryLivingWikiJob.
//
// When lwOrch is nil the function returns a fallback that immediately marks
// the job complete with a notice — so callers do not need to guard.
//
// retryExcludedOnly: when true, only pages whose IDs appear in
// excludedPageIDs are included in the generation run (the "Retry excluded
// pages" CTA path). When false, TaxonomyResolver derives the full page set.
//
// sinkKind is the label recorded in Prometheus (e.g. "confluence", "git_repo").
// Pass "" when the sink kind is unknown.
func buildColdStartRunner(
	lwOrch *lworch.Orchestrator,
	repoID string,
	tenantID string,
	graphStore graphstore.GraphStore,
	workerClient *worker.Client,
	excludedPageIDs []string, // non-nil+non-empty ⇒ retryExcludedOnly path
	sinkKind string,
	jobResultStore livingwiki.JobResultStore,
) func(ctx context.Context, rt llm.Runtime) error {
	if lwOrch == nil {
		return func(_ context.Context, rt llm.Runtime) error {
			rt.ReportProgress(1.0, "unavailable", "Living-wiki orchestrator not configured")
			return nil
		}
	}

	return func(runCtx context.Context, rt llm.Runtime) error {
		jobID := rt.JobID()
		start := time.Now()

		rt.ReportProgress(0.0, "planning", "Resolving page taxonomy")

		// ── Step 1: Resolve the page taxonomy ─────────────────────────────────
		var pages []lworch.PlannedPage

		if len(excludedPageIDs) > 0 {
			// retryExcludedOnly path: scope to previously-excluded pages.
			// Build a minimal PlannedPage for each excluded ID using the default
			// template set. TaxonomyResolver is not needed for this path because
			// the excluded IDs already encode template choice (via their naming
			// convention). We produce a filtered full taxonomy and keep only
			// the IDs in excludedPageIDs.
			full, err := resolveTaxonomy(runCtx, repoID, graphStore, workerClient)
			if err != nil {
				return fmt.Errorf("living-wiki: taxonomy resolution failed: %w", err)
			}
			wanted := make(map[string]bool, len(excludedPageIDs))
			for _, id := range excludedPageIDs {
				wanted[id] = true
			}
			for _, p := range full {
				if wanted[p.ID] {
					pages = append(pages, p)
				}
			}
			if len(pages) == 0 {
				rt.ReportProgress(1.0, "ok", "No previously-excluded pages found; nothing to retry")
				return nil
			}
		} else {
			// Full cold-start path.
			var err error
			pages, err = resolveTaxonomy(runCtx, repoID, graphStore, workerClient)
			if err != nil {
				return fmt.Errorf("living-wiki: taxonomy resolution failed: %w", err)
			}
		}

		total := len(pages)
		if total == 0 {
			rt.ReportProgress(1.0, "ok", "No pages to generate for this repository")
			return nil
		}

		rt.ReportProgress(0.05, "generating", fmt.Sprintf("Starting generation of %d pages", total))

		// ── Step 2: Generate pages with progress reporting ────────────────────
		// Counters updated atomically from OnPageDone callbacks (called from
		// parallel generation goroutines inside lwOrch.Generate).
		var generated, excludedCount int32
		var excludedIDsAcc atomicStringSlice

		onPageDone := func(pageID string, wasExcluded bool, _ string) {
			if wasExcluded {
				atomic.AddInt32(&excludedCount, 1)
				excludedIDsAcc.append(pageID)
			} else {
				atomic.AddInt32(&generated, 1)
			}
			done := int(atomic.LoadInt32(&generated)) + int(atomic.LoadInt32(&excludedCount))
			var progress float64
			if total > 0 {
				// Reserve 0–5% for planning, 5–95% for generation, 95–100% for finish.
				progress = 0.05 + 0.90*float64(done)/float64(total)
			}
			rt.ReportProgress(progress, "generating",
				fmt.Sprintf("%d/%d pages complete", done, total))
		}

		// WikiPR: R6 will replace this with a per-job snapshot from the broker.
		// Until then we use an in-memory PR so the orchestrator can complete
		// the full pipeline and pages are stored as proposed_ast.
		pr := lworch.NewMemoryWikiPR(fmt.Sprintf("pr-%s", jobID))

		genReq := lworch.GenerateRequest{
			Config:     lworch.Config{RepoID: repoID},
			Pages:      pages,
			PR:         pr,
			OnPageDone: onPageDone,
		}

		result, err := lwOrch.Generate(runCtx, genReq)
		elapsed := time.Since(start)

		// ── Step 3: Classify outcome ──────────────────────────────────────────
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
			failCat = coldstart.FailureCategoryNone
		}

		finalGen := int(atomic.LoadInt32(&generated))
		finalExcl := int(atomic.LoadInt32(&excludedCount))
		rt.ReportProgress(1.0, status, fmt.Sprintf(
			"Generation complete: %d generated, %d excluded",
			finalGen, finalExcl,
		))

		// ── Step 4: Persist LivingWikiJobResult ───────────────────────────────
		if jobResultStore != nil {
			now := time.Now()
			exIDs := excludedIDsAcc.snapshot()
			reasons := buildExclusionReasons(result.Excluded)

			jobResult := &livingwiki.LivingWikiJobResult{
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
			}
			if saveErr := jobResultStore.Save(runCtx, tenantID, jobResult); saveErr != nil {
				slog.Warn("living-wiki: failed to persist job result",
					"job_id", jobID, "repo_id", repoID, "error", saveErr)
			}
		}

		// ── Step 5: Prometheus counter ────────────────────────────────────────
		lwmetrics.Default.RecordJob(status, sinkKind, elapsed.Seconds())

		if err != nil {
			return fmt.Errorf("living-wiki generation failed: %w", err)
		}
		return nil
	}
}

// resolveTaxonomy builds the TaxonomyResolver from available dependencies and
// returns the full planned-page list for the given repo. graphStore and
// workerClient may be nil; the resolver degrades gracefully (no LLM-dependent
// pages will be generated, but the job won't hard-fail).
func resolveTaxonomy(ctx context.Context, repoID string, gs graphstore.GraphStore, wc *worker.Client) ([]lworch.PlannedPage, error) {
	var sg templates.SymbolGraph
	if gs != nil {
		sg = &graphStoreSymbolGraph{store: gs}
	}
	var llmCaller templates.LLMCaller
	if wc != nil {
		llmCaller = &coldStartLLMCaller{client: wc}
	}
	tr := lworch.NewTaxonomyResolver(repoID, sg, nil /* gitLog */, llmCaller)
	return tr.Resolve(ctx, nil, time.Now())
}

// buildExclusionReasons extracts human-readable gate-violation messages from
// the orchestrator's ExcludedPage slice.
func buildExclusionReasons(excluded []lworch.ExcludedPage) []string {
	reasons := make([]string, 0, len(excluded))
	for _, ex := range excluded {
		vr := ex.SecondResult
		if len(vr.Gates) == 0 {
			vr = ex.FirstResult
		}
		for _, g := range vr.Gates {
			for _, v := range g.Violations {
				if v.Message != "" {
					reasons = append(reasons, fmt.Sprintf("%s: %s", ex.PageID, v.Message))
				}
			}
		}
	}
	return reasons
}

// ─────────────────────────────────────────────────────────────────────────────
// graphStoreSymbolGraph
// ─────────────────────────────────────────────────────────────────────────────

// graphStoreSymbolGraph adapts the graph.GraphStore to the templates.SymbolGraph
// interface. It fetches all (non-test) symbols for the repo and maps them to
// the narrow templates.Symbol shape. Package is derived from the symbol's
// file path directory.
type graphStoreSymbolGraph struct {
	store graphstore.GraphStore
}

func (g *graphStoreSymbolGraph) ExportedSymbols(repoID string) ([]templates.Symbol, error) {
	stored, _ := g.store.GetSymbols(repoID, nil, nil, 10000, 0)
	out := make([]templates.Symbol, 0, len(stored))
	for _, s := range stored {
		if s.IsTest {
			continue
		}
		out = append(out, templates.Symbol{
			Package:    filepath.Dir(s.FilePath),
			Name:       s.Name,
			Signature:  s.Signature,
			DocComment: s.DocComment,
			FilePath:   s.FilePath,
			StartLine:  s.StartLine,
			EndLine:    s.EndLine,
		})
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// coldStartLLMCaller
// ─────────────────────────────────────────────────────────────────────────────

// coldStartLLMCaller adapts worker.Client to the templates.LLMCaller interface
// for use in the cold-start TaxonomyResolver. Equivalent to assembly's private
// workerLLMCaller; kept here to avoid a cross-package dependency on the
// assembly package's unexported type.
type coldStartLLMCaller struct {
	client *worker.Client
}

func (c *coldStartLLMCaller) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	question := userPrompt
	if systemPrompt != "" {
		question = systemPrompt + "\n\n" + userPrompt
	}
	resp, err := c.client.AnswerQuestion(ctx, &reasoningv1.AnswerQuestionRequest{
		Question: question,
	})
	if err != nil {
		return "", fmt.Errorf("cold-start LLM caller: %w", err)
	}
	return resp.GetAnswer(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// atomicStringSlice
// ─────────────────────────────────────────────────────────────────────────────

// atomicStringSlice is a concurrency-safe string accumulator. The living-wiki
// orchestrator calls OnPageDone from multiple goroutines simultaneously, so
// excluded page ID collection requires a lock.
type atomicStringSlice struct {
	mu  sync.Mutex
	val []string
}

func (a *atomicStringSlice) append(s string) {
	a.mu.Lock()
	a.val = append(a.val, s)
	a.mu.Unlock()
}

func (a *atomicStringSlice) snapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.val) == 0 {
		return nil
	}
	cp := make([]string, len(a.val))
	copy(cp, a.val)
	return cp
}
