// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// R5 + sink-dispatch wiring: cold-start job goroutine for living-wiki.
//
// This file provides:
//   - [buildColdStartRunner] — the RunWithContext closure injected into
//     llm.EnqueueRequest by EnableLivingWikiForRepo and RetryLivingWikiJob.
//   - [dispatchGeneratedPages] — calls sinks.BuildSinkWriters and
//     sinks.DispatchPagesToSinks after generation, pushing pages to every
//     sink configured on the repo.
//   - Port adapters ([graphStoreSymbolGraph], [coldStartLLMCaller]) that bridge
//     the resolver's dependencies into the living-wiki orchestrator's narrow
//     interfaces, so the cold-start goroutine can call TaxonomyResolver.Resolve
//     without a full assembly.AssemblerDeps dependency.
//   - [atomicStringSlice] — concurrency-safe string accumulator for page IDs
//     collected from parallel OnPageDone callbacks.

package graphql

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	lwmetrics "github.com/sourcebridge/sourcebridge/internal/livingwiki/metrics"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/worker"
)

const (
	// maxSymbolBodyLines is the maximum number of source lines included per
	// symbol body to keep prompts manageable.
	maxSymbolBodyLines = 200
	// maxSymbolBodyBytes is the maximum byte size per symbol body.
	maxSymbolBodyBytes = 8 * 1024
	// coldStartTimeBudget is the maximum wall-clock time for a single
	// cold-start (or retry-excluded) generation run. The orchestrator's
	// default of 5 minutes is intended for incremental updates; cold starts
	// over large repos (~150+ pages) routinely need much longer.
	coldStartTimeBudget = 60 * time.Minute
	// coldStartMaxConcurrency bumps the orchestrator's per-job parallelism
	// from its default (5) so large cold starts complete in a reasonable
	// wall-clock time. Each slot is one outstanding LLM call to the worker;
	// rate limits and provider concurrency are the actual ceilings, so this
	// just removes the artificial floor.
	coldStartMaxConcurrency = 12
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
//
// broker and repoSettingsStore power the post-generation sink dispatch phase.
// When either is nil the dispatch phase is skipped; pages remain in the
// proposed_ast store only (same behaviour as before this wiring landed).
func buildColdStartRunner(
	lwOrch *lworch.Orchestrator,
	repoID string,
	tenantID string,
	graphStore graphstore.GraphStore,
	workerClient *worker.Client,
	excludedPageIDs []string, // non-nil+non-empty ⇒ retryExcludedOnly path
	sinkKind string,
	jobResultStore livingwiki.JobResultStore,
	broker credentials.Broker,
	repoSettingsStore livingwiki.RepoSettingsStore,
	clusterStore clustering.ClusterStore,
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
			full, err := resolveTaxonomy(runCtx, repoID, graphStore, workerClient, clusterStore)
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
			pages, err = resolveTaxonomy(runCtx, repoID, graphStore, workerClient, clusterStore)
			if err != nil {
				return fmt.Errorf("living-wiki: taxonomy resolution failed: %w", err)
			}
		}

		total := len(pages)
		if total == 0 {
			rt.ReportProgress(1.0, "ok", "No pages to generate for this repository")
			return nil
		}

		// ── Step 1.5: Smart resume — skip pages already published ─────────────
		//
		// On a fresh cold-start every page is generated. On a retry after a
		// time-out (or any failure that left some pages already in the sink),
		// we don't want to redo all the work — just generate what's missing.
		// Query each configured sink for the set of SourceBridge-tagged pages
		// it currently holds, and skip any planned page whose ID is already
		// present in every sink. The skipped IDs flow through to the dispatch
		// step so the orphan-GC pass treats them as "still wanted" and doesn't
		// delete them. PagesGenerated in the persisted result includes these
		// skips so the UI shows the true sink state.
		alreadyPublished := listAlreadyPublishedAcrossSinks(
			runCtx, repoID, broker, repoSettingsStore,
		)
		var skippedPageIDs []string
		if len(alreadyPublished) > 0 {
			filtered := pages[:0]
			for _, p := range pages {
				if _, done := alreadyPublished[p.ID]; done {
					skippedPageIDs = append(skippedPageIDs, p.ID)
					continue
				}
				filtered = append(filtered, p)
			}
			pages = filtered
		}
		toGenerate := len(pages)
		slog.Info("livingwiki/coldstart: smart resume",
			"repo_id", repoID,
			"taxonomy_total", total,
			"already_published", len(skippedPageIDs),
			"to_generate", toGenerate)

		if toGenerate == 0 {
			rt.ReportProgress(1.0, "ok", fmt.Sprintf(
				"All %d pages already up to date — nothing to regenerate", total))
			if jobResultStore != nil {
				now := time.Now()
				_ = jobResultStore.Save(runCtx, tenantID, &livingwiki.LivingWikiJobResult{
					RepoID:         repoID,
					JobID:          jobID,
					StartedAt:      start,
					CompletedAt:    &now,
					PagesPlanned:   total,
					PagesGenerated: total,
					Status:         "ok",
				})
			}
			lwmetrics.Default.RecordJob("ok", sinkKind, time.Since(start).Seconds())
			return nil
		}

		rt.ReportProgress(0.05, "generating", fmt.Sprintf(
			"Generating %d pages (%d already up to date)", toGenerate, len(skippedPageIDs)))

		// ── Step 2: Generate pages with progress reporting ────────────────────
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
			if toGenerate > 0 {
				// Reserve 0–5% for planning, 5–90% for generation, 90–100% for sink push.
				progress = 0.05 + 0.85*float64(done)/float64(toGenerate)
			}
			rt.ReportProgress(progress, "generating",
				fmt.Sprintf("%d/%d pages complete", done, toGenerate))
		}

		// Heartbeat: tick a progress update every 60s while Generate runs so
		// the LLM-orchestrator stale-reaper sees fresh UpdatedAt timestamps
		// even if no page completes for a long stretch (e.g. all parallel
		// workers happen to be on slow architecture pages simultaneously).
		// Without this, sourcebridge-sized cold starts get reaped at the
		// 30-minute "no progress" threshold even though the goroutines are
		// still actively producing.
		hbCtx, hbStop := context.WithCancel(runCtx)
		defer hbStop()
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-hbCtx.Done():
					return
				case <-ticker.C:
					done := int(atomic.LoadInt32(&generated)) + int(atomic.LoadInt32(&excludedCount))
					var p float64
					if total > 0 {
						p = 0.05 + 0.85*float64(done)/float64(total)
					}
					rt.ReportProgress(p, "generating",
						fmt.Sprintf("%d/%d pages complete", done, total))
				}
			}
		}()

		// Use an in-memory WikiPR so pages are stored as proposed_ast.
		// A future workstream will replace this with a per-job snapshot from the
		// broker once git-based PR creation is wired.
		pr := lworch.NewMemoryWikiPR(fmt.Sprintf("pr-%s", jobID))

		// Cold-start runs can include hundreds of pages; the orchestrator's
		// default 5-minute TimeBudget targets fast incremental updates, not
		// initial generation. Override to 60 min so large repos (~150+ pages)
		// can finish without hitting ErrTimeBudgetExceeded mid-run.
		genReq := lworch.GenerateRequest{
			Config: lworch.Config{
				RepoID: repoID,
				// Cold starts run hundreds of independent page-generations.
				// The orchestrator's default MaxConcurrency=5 is fine for
				// incremental updates but bottlenecks large first runs;
				// bump to 12 so a 169-page run can stay inside the
				// 60-minute TimeBudget. Each parallel slot makes its own
				// gRPC AnswerQuestion call so the worker / Anthropic side
				// remain the natural rate-limiters.
				MaxConcurrency: coldStartMaxConcurrency,
				TimeBudget:     coldStartTimeBudget,
			},
			Pages:      pages,
			PR:         pr,
			OnPageDone: onPageDone,
		}

		result, err := lwOrch.Generate(runCtx, genReq)
		elapsed := time.Since(start)

		// ── Step 3: Classify generation outcome ───────────────────────────────
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

		// ── Step 4: Dispatch generated pages to configured sinks ──────────────
		var sinkResults []livingwiki.SinkWriteResult

		if err == nil && (len(result.Generated) > 0 || len(skippedPageIDs) > 0) {
			rt.ReportProgress(0.92, "pushing", fmt.Sprintf(
				"Pushing %d pages to sinks", len(result.Generated)))

			// Resolve the repository name for the Confluence root page title.
			// Best-effort: fall back to an empty string if the store is nil or
			// the repo is not found; the sink writer will substitute repoID.
			var repoName string
			if graphStore != nil {
				if repo := graphStore.GetRepository(repoID); repo != nil {
					repoName = repo.Name
				}
			}

			sinkResults = dispatchGeneratedPages(
				runCtx, repoID, tenantID,
				result.Generated, skippedPageIDs,
				broker, repoSettingsStore,
				repoName,
				&status, &failCat, &errMsg,
			)
		}

		// PagesGenerated counts pages successfully present in the sink as of
		// the end of this run, so it includes both newly-generated pages and
		// the smart-resume skips. The UI uses this to render the "X of Y
		// pages" summary, and a follow-up retry uses it to decide whether
		// any work remains.
		finalGen += len(skippedPageIDs)

		rt.ReportProgress(1.0, status, fmt.Sprintf(
			"Generation complete: %d generated, %d excluded",
			finalGen, finalExcl,
		))

		// ── Step 5: Persist LivingWikiJobResult ───────────────────────────────
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
				SinkWriteResults: sinkResults,
				Status:           status,
				FailureCategory:  string(failCat),
				ErrorMessage:     errMsg,
			}
			if saveErr := jobResultStore.Save(runCtx, tenantID, jobResult); saveErr != nil {
				slog.Warn("living-wiki: failed to persist job result",
					"job_id", jobID, "repo_id", repoID, "error", saveErr)
			}
		}

		// ── Step 6: Prometheus counter ────────────────────────────────────────
		lwmetrics.Default.RecordJob(status, sinkKind, elapsed.Seconds())

		if err != nil {
			return fmt.Errorf("living-wiki generation failed: %w", err)
		}
		return nil
	}
}

// dispatchGeneratedPages takes the credential snapshot, builds SinkWriters
// from the repo's configured sinks, and pushes each generated page to every
// sink. It updates status/failCat/errMsg when sink failures warrant a
// reclassification (e.g. all sinks return 401 → status "failed", cat "auth").
//
// skippedPageIDs lists pages the smart-resume step skipped because they were
// already published. They are NOT pushed (the sink already has them) but they
// ARE added to the orphan-cleanup "still wanted" set so the GC pass doesn't
// delete them.
//
// repoName is the human-readable repository name used as the root page title
// in Confluence ("<repoName> Living Wiki"). Pass "" when unknown.
//
// When broker or repoSettingsStore is nil, dispatch is skipped silently.
func dispatchGeneratedPages(
	ctx context.Context,
	repoID, tenantID string,
	generatedPages []ast.Page,
	skippedPageIDs []string,
	broker credentials.Broker,
	repoSettingsStore livingwiki.RepoSettingsStore,
	repoName string,
	status *string,
	failCat *coldstart.FailureCategory,
	errMsg *string,
) []livingwiki.SinkWriteResult {
	if broker == nil || repoSettingsStore == nil {
		return nil
	}

	// Fetch per-repo sink settings.
	repoSettings, err := repoSettingsStore.GetRepoSettings(ctx, tenantID, repoID)
	if err != nil {
		slog.Warn("living-wiki: could not fetch repo settings for sink dispatch",
			"repo_id", repoID, "error", err)
		return nil
	}
	if repoSettings == nil || len(repoSettings.Sinks) == 0 {
		return nil
	}

	// Take a per-job credential snapshot.
	snap, err := credentials.Take(ctx, broker)
	if err != nil {
		slog.Warn("living-wiki: credential snapshot failed; skipping sink dispatch",
			"repo_id", repoID, "error", err)
		// Classify as auth failure so the UI shows the right CTA.
		*status = "failed"
		*failCat = coldstart.FailureCategoryAuth
		*errMsg = fmt.Sprintf("credential snapshot failed: %s", err)
		return nil
	}

	slog.Info("living-wiki: building sink writers",
		"repo_id", repoID, "sink_count", len(repoSettings.Sinks),
		"has_confluence_site", snap.ConfluenceSite != "",
		"has_confluence_email", snap.ConfluenceEmail != "",
		"has_confluence_token", snap.ConfluenceToken != "")

	// Build SinkWriters from the repo's settings.
	writers, err := sinks.BuildSinkWriters(ctx, repoSettings, snap, repoName)
	if err != nil {
		slog.Warn("living-wiki: could not build sink writers",
			"repo_id", repoID, "error", err)
		if sinks.IsMissingCredentialsError(err) {
			*status = "failed"
			*failCat = coldstart.FailureCategoryAuth
			*errMsg = err.Error()
		} else {
			// Not-implemented sinks are surfaced as partial — not fatal.
			if *status == "ok" {
				*status = "partial"
				*failCat = coldstart.FailureCategoryPartialContent
			}
			*errMsg = err.Error()
		}
		return nil
	}
	slog.Info("living-wiki: built writers", "repo_id", repoID, "writer_count", len(writers))
	if len(writers) == 0 {
		slog.Warn("living-wiki: zero writers built; configured sinks may all be unimplemented",
			"repo_id", repoID, "configured_sinks", len(repoSettings.Sinks))
		return nil
	}

	// Dispatch — per-sink parallel, per-page sequential within each sink.
	rateLimiter := markdown.NewTokenBucketRateLimiter(markdown.DefaultSinkRates())
	dispatchResult, _ := sinks.DispatchPagesNamed(ctx, generatedPages, writers, rateLimiter, lwmetrics.Default)
	slog.Info("living-wiki: dispatch returned",
		"repo_id", repoID,
		"per_sink_count", len(dispatchResult.PerSink))

	// Convert to domain model for persistence.
	results := make([]livingwiki.SinkWriteResult, 0, len(dispatchResult.PerSink))
	for integrationName, summary := range dispatchResult.PerSink {
		r := livingwiki.SinkWriteResult{
			IntegrationName: integrationName,
			Kind:            string(summary.Kind),
			PagesWritten:    summary.PagesWritten,
			PagesFailed:     summary.PagesFailed,
			FailedPageIDs:   summary.FailedPageIDs,
		}
		if summary.Error != nil {
			r.Error = summary.Error.Error()
		}
		results = append(results, r)
	}

	// Reclassify overall status based on sink outcomes.
	dispatchStatus := sinks.DispatchSummaryStatus(dispatchResult)
	switch dispatchStatus {
	case "failed":
		if *status == "ok" {
			*status = "failed"
			*failCat = coldstart.FailureCategoryAuth
			*errMsg = "all sinks failed to write pages"
		}
	case "partial":
		if *status == "ok" {
			*status = "partial"
			*failCat = coldstart.FailureCategoryPartialContent
		}
	}

	// Orphan-cleanup pass: remove pages that were published in a previous job
	// but are absent from the current taxonomy. Runs only on a fully-successful
	// dispatch ("ok") so a transient generation failure cannot nuke live
	// content. The user may disable this per-repo via AutoCleanOrphans=false.
	//
	// Default-on semantics: AutoCleanOrphans is a bool whose zero-value is
	// false, but we treat an unset (zero) value the same as true here because
	// the settings mutation explicitly sets it when the user flips the toggle.
	// Any repo where the field was never touched keeps the default-on behaviour.
	orphanCleanEnabled := !repoSettings.AutoCleanOrphansDisabled()
	if dispatchStatus == "ok" && orphanCleanEnabled {
		// currentIDs is the union of:
		//   - hierarchy pages (root + Architecture section) — always "wanted"
		//   - pages just generated
		//   - pages the smart-resume step skipped (already published)
		// All three sets must be excluded from orphan deletion.
		currentIDs := []string{
			repoID + ".__wiki_root__",
			repoID + ".__section__.architecture",
		}
		for _, p := range generatedPages {
			currentIDs = append(currentIDs, p.ID)
		}
		currentIDs = append(currentIDs, skippedPageIDs...)
		for _, nsw := range writers {
			gcResult := sinks.RunOrphanCleanup(ctx, nsw.Writer, repoID, currentIDs)
			if gcResult.Deleted > 0 || len(gcResult.Errors) > 0 {
				slog.Info("livingwiki/dispatch: orphan cleanup done",
					"sink", nsw.Name, "repo_id", repoID,
					"deleted", gcResult.Deleted, "errors", len(gcResult.Errors))
			}
		}
	}

	return results
}

// listAlreadyPublishedAcrossSinks queries every configured sink that supports
// the OrphanCleaner contract for the page IDs it currently holds, and returns
// the *intersection* — the set of pages already published to every sink. The
// cold-start runner uses this to skip pages that were published by a previous
// run (typically the run that timed out and left some progress behind).
//
// Returns an empty map on any failure. Smart resume is purely an optimisation;
// failures here just fall through to the regular full-generation path.
//
// Intersection semantics matter when a repo has multiple sinks: a page must be
// in EVERY sink to be skipped, otherwise we'd skip a page and never push it to
// the sink that doesn't have it yet.
func listAlreadyPublishedAcrossSinks(
	ctx context.Context,
	repoID string,
	broker credentials.Broker,
	repoSettingsStore livingwiki.RepoSettingsStore,
) map[string]struct{} {
	empty := map[string]struct{}{}
	if broker == nil || repoSettingsStore == nil {
		return empty
	}

	repoSettings, err := repoSettingsStore.GetRepoSettings(ctx, defaultTenantID, repoID)
	if err != nil || repoSettings == nil || len(repoSettings.Sinks) == 0 {
		return empty
	}

	snap, err := credentials.Take(ctx, broker)
	if err != nil {
		return empty
	}

	// repoName is not needed for listing (only for create); pass "" here.
	writers, err := sinks.BuildSinkWriters(ctx, repoSettings, snap, "")
	if err != nil || len(writers) == 0 {
		return empty
	}

	prefix := repoID + "."
	var perSink []map[string]struct{}
	for _, nsw := range writers {
		cleaner, ok := nsw.Writer.(sinks.OrphanCleaner)
		if !ok {
			// At least one sink can't tell us what it has, so we can't
			// safely skip anything — abort and let the full path run.
			return empty
		}
		listed, err := cleaner.ListPagesByExternalIDPrefix(ctx, prefix)
		if err != nil {
			return empty
		}
		set := make(map[string]struct{}, len(listed))
		for _, id := range listed {
			set[id] = struct{}{}
		}
		perSink = append(perSink, set)
	}
	if len(perSink) == 0 {
		return empty
	}

	// Intersection: start from the smallest set, keep IDs present in all.
	smallest := 0
	for i, s := range perSink {
		if len(s) < len(perSink[smallest]) {
			smallest = i
		}
	}
	result := make(map[string]struct{}, len(perSink[smallest]))
	for id := range perSink[smallest] {
		inAll := true
		for i, s := range perSink {
			if i == smallest {
				continue
			}
			if _, ok := s[id]; !ok {
				inAll = false
				break
			}
		}
		if inAll {
			result[id] = struct{}{}
		}
	}
	return result
}

// resolveTaxonomy builds the TaxonomyResolver from available dependencies and
// returns the full planned-page list for the given repo. graphStore, workerClient,
// and clusterStore may be nil; the resolver degrades gracefully (no LLM-dependent
// pages will be generated and the package-path heuristic is used for architecture
// pages, but the job won't hard-fail).
func resolveTaxonomy(ctx context.Context, repoID string, gs graphstore.GraphStore, wc *worker.Client, cs clustering.ClusterStore) ([]lworch.PlannedPage, error) {
	var sg templates.SymbolGraph
	if gs != nil {
		sg = &graphStoreSymbolGraph{store: gs}
	}
	var llmCaller templates.LLMCaller
	if wc != nil {
		llmCaller = &coldStartLLMCaller{client: wc}
	}

	// Fetch clusters to use as the primary area signal for architecture pages.
	// On error or empty result we pass nil and fall back to package-path heuristics.
	var clusterSummaries []clustering.ClusterSummary
	if cs != nil {
		raw, err := cs.GetClusters(ctx, repoID)
		if err != nil || len(raw) == 0 {
			if err != nil {
				slog.Debug("living-wiki: failed to fetch clusters for taxonomy, using package-path fallback",
					"repo_id", repoID, "error", err)
			}
		} else {
			clusterSummaries = make([]clustering.ClusterSummary, len(raw))
			for i, c := range raw {
				label := c.Label
				if c.LLMLabel != nil && *c.LLMLabel != "" {
					label = *c.LLMLabel
				}
				clusterSummaries[i] = clustering.ClusterSummary{
					ID:          c.ID,
					Label:       label,
					MemberCount: c.Size,
				}
			}
		}
	}

	tr := lworch.NewTaxonomyResolver(repoID, sg, nil /* gitLog */, llmCaller)
	return tr.Resolve(ctx, nil, clusterSummaries, time.Now())
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
//
// When the repo has a local clone path on disk, ExportedSymbols populates
// Symbol.Body with the raw source lines for each symbol so the architecture
// template can include concrete code in its LLM prompt.
//
// The architecture template calls ExportedSymbols once per page (it then
// filters to the package it cares about). For a repo with N pages and M
// symbols, that's O(N) calls, each doing M file reads to populate Body. On
// large repos (sourcebridge: ~169 pages, several thousand symbols) this
// dominates the cold-start latency. Cache the per-repo result and reuse it
// across all pages — both the symbol slice and the file reads are
// invariant for the duration of the cold-start.
type graphStoreSymbolGraph struct {
	store graphstore.GraphStore

	cacheMu sync.Mutex
	cache   map[string][]templates.Symbol // repoID → exported symbols
}

func (g *graphStoreSymbolGraph) ExportedSymbols(repoID string) ([]templates.Symbol, error) {
	g.cacheMu.Lock()
	if g.cache != nil {
		if cached, ok := g.cache[repoID]; ok {
			g.cacheMu.Unlock()
			return cached, nil
		}
	}
	g.cacheMu.Unlock()

	stored, _ := g.store.GetSymbols(repoID, nil, nil, 10000, 0)

	// Determine the repo root for source-body reading.
	repoRoot := ""
	if repo := g.store.GetRepository(repoID); repo != nil {
		repoRoot = repo.ClonePath
		if repoRoot == "" {
			repoRoot = repo.Path
		}
	}

	out := make([]templates.Symbol, 0, len(stored))
	for _, s := range stored {
		if s.IsTest {
			continue
		}
		sym := templates.Symbol{
			Package:    filepath.Dir(s.FilePath),
			Name:       s.Name,
			Signature:  s.Signature,
			DocComment: s.DocComment,
			FilePath:   s.FilePath,
			StartLine:  s.StartLine,
			EndLine:    s.EndLine,
		}
		if repoRoot != "" && s.FilePath != "" && s.StartLine > 0 && s.EndLine >= s.StartLine {
			sym.Body = readSourceLines(repoRoot, s.FilePath, s.StartLine, s.EndLine)
		}
		out = append(out, sym)
	}

	g.cacheMu.Lock()
	if g.cache == nil {
		g.cache = make(map[string][]templates.Symbol, 1)
	}
	g.cache[repoID] = out
	g.cacheMu.Unlock()

	return out, nil
}

// readSourceLines reads lines [startLine, endLine] (1-based, inclusive) from
// filePath relative to repoRoot. Returns empty string on any read error so
// callers can proceed without the body. The result is capped at
// maxSymbolBodyLines / maxSymbolBodyBytes to keep prompts manageable.
func readSourceLines(repoRoot, filePath string, startLine, endLine int) string {
	absPath := filepath.Join(repoRoot, filePath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}

	lines := bytes.Split(data, []byte("\n"))
	// Convert 1-based to 0-based indices and clamp.
	lo := startLine - 1
	if lo < 0 {
		lo = 0
	}
	hi := endLine // exclusive in slice terms
	if hi > len(lines) {
		hi = len(lines)
	}
	if lo >= hi {
		return ""
	}

	selected := lines[lo:hi]
	// Cap line count.
	if len(selected) > maxSymbolBodyLines {
		selected = selected[:maxSymbolBodyLines]
	}

	body := bytes.Join(selected, []byte("\n"))
	// Cap byte size.
	if len(body) > maxSymbolBodyBytes {
		body = body[:maxSymbolBodyBytes]
		// Trim to the last newline to avoid cutting mid-line.
		if idx := bytes.LastIndexByte(body, '\n'); idx > 0 {
			body = body[:idx]
		}
	}
	return string(body)
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

// perCallLLMTimeout caps how long any single AnswerQuestion RPC may run.
// Without this cap a hung gRPC call (network blip, worker stall, provider
// timeout) blocks its goroutine indefinitely. With 5 parallel workers and
// 169 pages, all five getting stuck on hung calls produces an ~30-minute
// silence that triggers the LLM-orchestrator stale-reaper, killing the
// whole job. A 5-minute ceiling per call is generous for any legitimate
// page (architecture pages with full source bodies typically finish in
// 30-90 s) and lets the page-level retry loop recover from individual
// hangs without poisoning the whole run.
const perCallLLMTimeout = 5 * time.Minute

func (c *coldStartLLMCaller) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	question := userPrompt
	if systemPrompt != "" {
		question = systemPrompt + "\n\n" + userPrompt
	}
	callCtx, cancel := context.WithTimeout(ctx, perCallLLMTimeout)
	defer cancel()
	resp, err := c.client.AnswerQuestion(callCtx, &reasoningv1.AnswerQuestionRequest{
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
