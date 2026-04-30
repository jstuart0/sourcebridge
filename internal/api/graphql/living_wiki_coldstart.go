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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	reasoningv1 "github.com/sourcebridge/sourcebridge/gen/go/reasoning/v1"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/indexpage"
	lwmetrics "github.com/sourcebridge/sourcebridge/internal/livingwiki/metrics"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/markdown"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/sinks"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

const (
	// GenerationMode values for Living Wiki cold-start jobs (CR12 Part B).
	// The lw_ prefix namespaces these away from knowledge-pipeline GenerationMode
	// values ("classic", "deep"). The runner reads the job's GenerationMode field
	// and interprets lw_* prefixed values as LW mode specifiers.
	GenerationModeLWDetailed = "lw_detailed" // per-folder (today's behaviour)
	GenerationModeLWOverview = "lw_overview" // subsystem-level (Phase 4a)

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
	// maxArtifactsPerCluster caps how many knowledge artifacts are passed to
	// the architecture template per cluster. The prompt already has symbol
	// bodies; each additional artifact adds ~1–4 KB. Three deep artifacts
	// cover the curated analysis without approaching model context limits.
	maxArtifactsPerCluster = 3

	// indexUpdateEvery is the number of OnPageDone callbacks (page completions)
	// after which the Living Wiki index page is re-dispatched to all configured
	// sinks. The 30s heartbeat ticker provides the time-based trigger; this
	// constant provides the completion-count-based trigger. Whichever fires
	// first re-dispatches the index (Phase 2).
	indexUpdateEvery = 10
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
//
// knowledgeStore, when non-nil, is queried for pre-computed knowledge artifacts.
// Fresh artifacts (matching the current understanding revisionFp) are attached
// to each architecture cluster's PackageInfo so the template can use curated
// analysis as its primary LLM context instead of generating from scratch.
func buildColdStartRunner(
	lwOrch *lworch.Orchestrator,
	repoID string,
	tenantID string,
	graphStore graphstore.GraphStore,
	workerClient *worker.Client,
	llmCaller *llmcall.Caller, // post-slice-2: LLM-aware adapter (resolved metadata)
	excludedPageIDs []string, // non-nil+non-empty ⇒ retryExcludedOnly path
	sinkKind string,
	jobResultStore livingwiki.JobResultStore,
	broker credentials.Broker,
	repoSettingsStore livingwiki.RepoSettingsStore,
	clusterStore clustering.ClusterStore,
	knowledgeStore knowledge.KnowledgeStore,
	metricsCollector *lwmetrics.Collector, // when nil, falls back to lwmetrics.Default
	llmResolver resolution.Resolver, // for FrozenResolver + fingerprint model identity (CR5, LD-7)
	publishStatusStore livingwiki.PagePublishStatusStore, // for per-page dispatch state (Phase 1)
) func(ctx context.Context, rt llm.Runtime) error {
	if metricsCollector == nil {
		metricsCollector = lwmetrics.Default
	}
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
			full, err := resolveTaxonomy(runCtx, repoID, graphStore, llmCaller, clusterStore)
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
			pages, err = resolveTaxonomy(runCtx, repoID, graphStore, llmCaller, clusterStore)
			if err != nil {
				return fmt.Errorf("living-wiki: taxonomy resolution failed: %w", err)
			}
		}

		total := len(pages)
		if total == 0 {
			rt.ReportProgress(1.0, "ok", "No pages to generate for this repository")
			return nil
		}

		// ── Step 1.5: Attach knowledge artifacts to architecture pages ─────────
		//
		// Pull all ready, non-stale knowledge artifacts for this repo and attach
		// fresh ones to each cluster's ArchitecturePackageInfo so the architecture
		// template can use curated analysis as its primary LLM context. Freshness
		// is determined by an exact match on UnderstandingRevisionFP against the
		// current repo-level understanding's RevisionFP. Artifacts whose
		// revisionFp does not match the current understanding are skipped so the
		// template never presents stale curated data as ground truth.
		pages = attachKnowledgeArtifacts(runCtx, repoID, pages, knowledgeStore)
		logArtifactResolution(repoID, pages)

		// ── Step 1.6: Build label→ID map from the full manifest (CR3) ────────────
		//
		// Must happen BEFORE smart-resume splits the manifest into buckets.
		// Pages in the "regenerate" bucket need links that resolve to the correct
		// IDs for pages in the "skip" buckets — the map must cover all planned pages.
		relatedByLabel := buildRelatedPageIDsByLabel(pages)
		for i := range pages {
			pages[i].RelatedPageIDsByLabel = relatedByLabel
		}

		// ── Step 1.7: Resolve model identity and freeze the LLM caller (CR5) ───
		//
		// Resolve the LLM model identity once at run start and freeze it for the
		// duration of the run, so mid-run workspace settings changes cannot split
		// a run across providers or cause fingerprint drift.
		// The frozen caller is a new *llmcall.Caller instance with a FrozenResolver
		// wired in (CR5: Go methods are not virtual; substituting the Resolver
		// implementation is the correct freeze strategy).
		modelIdentity := livingWikiModelIdentity(runCtx, llmResolver, repoID)
		frozenCaller := llmCaller // fallback: use the original if resolver is nil
		if llmResolver != nil {
			snap, resolveErr := llmResolver.Resolve(runCtx, repoID, resolution.OpLivingWikiColdStart)
			if resolveErr == nil {
				frozenCaller = llmcall.New(llmCaller.Inner(), resolution.NewFrozenResolver(snap), nil)
			}
		}
		_ = frozenCaller // used below in genReq (when the adapter is refactored); for now
		// only the model identity snapshot is used for fingerprinting.

		// ── Step 1.8: Smart resume — fingerprint-aware 3-way bucket split ────────
		//
		// CR4 (3-way split), LD-7 (fingerprint-aware), CR13 (run-start status writes
		// only for the regenerate bucket).
		//
		// Algorithm:
		//   1. List pages already present on every sink (sink-listing intersection).
		//   2. Load persisted fingerprints from lw_page_publish_status.
		//   3. Compute current fingerprints for all pages (one call per page, pure).
		//   4. Split into three buckets:
		//        regenerate      — page absent on any sink, OR fp mismatch, OR status != 'ready'
		//        skipFully       — present everywhere, fp matches, status='ready', fixup done/none
		//        skipNeedsFixup  — present everywhere, fp matches, status='ready', fixup pending
		//   5. Write status='generating' ONLY for the regenerate bucket (CR13).
		alreadyPublished := listAlreadyPublishedAcrossSinks(
			runCtx, repoID, broker, repoSettingsStore,
		)
		repoSourceRev := repoSourceRevFor(graphStore, repoID)

		// Compute current fingerprints for every planned page (pure, O(N)).
		currentFps := make(map[string]string, len(pages))
		for _, p := range pages {
			currentFps[p.ID] = lworch.ComputePageFingerprint(p, modelIdentity, repoSourceRev)
		}

		// Load persisted fingerprints from the status store.
		var persistedFps map[string]map[string]livingwiki.PagePublishStatusRow
		if publishStatusStore != nil {
			persistedFps, _ = publishStatusStore.LoadFingerprints(runCtx, repoID)
		}

		// Build sink-key list from the writers (needed for per-sink completeness checks).
		// We build the writers once here for smart-resume and reuse them for dispatch.
		writers, writerBuildErr := buildSinkWriters(runCtx, repoID, broker, repoSettingsStore)

		// 3-way split (CR4).
		var regenerate, skipFully, skipNeedsFixup []lworch.PlannedPage
		for _, p := range pages {
			bucket := classifyPage(p.ID, alreadyPublished, currentFps[p.ID], persistedFps, writers)
			switch bucket {
			case bucketSkipFully:
				skipFully = append(skipFully, p)
			case bucketSkipNeedsFixup:
				skipNeedsFixup = append(skipNeedsFixup, p)
			default:
				regenerate = append(regenerate, p)
			}
		}

		// All skipped IDs (for orphan-cleanup "still wanted" set).
		skippedPageIDs := make([]string, 0, len(skipFully)+len(skipNeedsFixup))
		for _, p := range skipFully {
			skippedPageIDs = append(skippedPageIDs, p.ID)
		}
		for _, p := range skipNeedsFixup {
			skippedPageIDs = append(skippedPageIDs, p.ID)
		}

		toGenerate := len(regenerate)
		slog.Info("livingwiki/coldstart: smart resume (fingerprint-aware)",
			"repo_id", repoID,
			"taxonomy_total", total,
			"regenerate", toGenerate,
			"skip_fully", len(skipFully),
			"skip_needs_fixup", len(skipNeedsFixup),
			"model_identity", modelIdentity)

		// Write status='generating' ONLY for the regenerate bucket × sinks (CR13).
		// skipFully rows stay 'ready' (untouched); skipNeedsFixup rows stay 'ready'
		// with fixup_status='pending' (untouched until Phase 3's fix-up pass).
		if publishStatusStore != nil {
			for _, p := range regenerate {
				for _, w := range writers {
					_ = publishStatusStore.SetNonReady(runCtx, livingwiki.SetNonReadyArgs{
						RepoID:          repoID,
						PageID:          p.ID,
						SinkKind:        string(w.Writer.Kind()),
						IntegrationName: w.Name,
						Status:          "generating",
						ErrorMsg:        "",
					})
				}
			}
		}

		// ── Phase 2: Initial index dispatch ──────────────────────────────────────
		//
		// Publish the combined Living Wiki index page BEFORE generation starts so
		// the user sees a "Pending" list within seconds of triggering a cold-start.
		// This is the "user sees something within 30 seconds" guarantee from CR1.
		//
		// Serialization: indexMutexFor guards against concurrent Overview + Detailed
		// jobs (LD-12) writing to the same <repoID>.__index__ page. The mutex is
		// package-level (M3) — closure-local would not serialize across parallel
		// buildColdStartRunner closures for the same repo.
		//
		// First-write-fails gating: if the initial dispatch fails (wrong space key,
		// auth error, etc.), we log a warning and skip ALL subsequent index updates
		// for this run. Per-page stream-publish (Phase 1) is unaffected — the index
		// is enhancement, not core. The job does NOT fail due to index errors.
		allPageIDs := make([]string, len(pages))
		for i, p := range pages {
			allPageIDs[i] = p.ID
		}
		var indexFailed int32 // 1 if first write failed; atomic for goroutine access

		dispatchIndex := func(ctx context.Context, label string) {
			if atomic.LoadInt32(&indexFailed) != 0 {
				return
			}
			if len(writers) == 0 {
				return
			}
			var statuses []livingwiki.PagePublishStatusRow
			if publishStatusStore != nil {
				statuses, _ = publishStatusStore.ListByRepo(ctx, repoID)
			}
			indexPage := indexpage.RenderIndexPage(repoID, allPageIDs, statuses, time.Now())
			mu := indexMutexFor(repoID)
			mu.Lock()
			defer mu.Unlock()

			for _, nsw := range writers {
				writeCtx, writeCancel := context.WithTimeout(ctx, 10*time.Second)
				writeErr := nsw.Writer.WritePage(writeCtx, indexPage)
				writeCancel()
				if writeErr != nil {
					slog.Warn("livingwiki/index: failed to write index page",
						"repo_id", repoID, "sink", nsw.Writer.Kind(),
						"label", label, "error", writeErr)
					if label == "initial" {
						atomic.StoreInt32(&indexFailed, 1)
					}
				} else {
					slog.Info("livingwiki/index: index page written",
						"repo_id", repoID, "sink", nsw.Writer.Kind(), "label", label)
				}
			}
		}

		// Dispatch the initial index synchronously before generation begins.
		dispatchIndex(runCtx, "initial")

		if toGenerate == 0 && len(skipNeedsFixup) == 0 {
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
			"Generating %d pages (%d already up to date, %d need fixup)",
			toGenerate, len(skipFully), len(skipNeedsFixup)))

		// ── Step 1.9: Wire async dispatch worker (CR2) ───────────────────────────
		//
		// The OnPageReady callback enqueues a durable page event onto a runner-owned
		// bounded buffered channel and returns immediately (non-blocking). A single
		// dispatcher goroutine drains the channel under dispatchCtx and performs the
		// actual sink writes. This keeps the orchestrator's persistence loop fast
		// (O(N × SetProposed time)) regardless of sink speed.
		//
		// Cancellation policy (CR2 + r4):
		//   dispatchCtx derives from parentCtx (honors job kills).
		//   Status-store writes inside streamDispatchPage use a SEPARATE
		//   WithoutCancel-bounded context so they survive parentCtx cancellation.
		//   Post-cancel drain: once dispatchCtx is done, the worker exits the
		//   for-range immediately (bounded exit: one in-flight call ≤5s + one
		//   status-store write ≤5s ≈ ≤10s total).
		type readyPage struct {
			page        ast.Page
			fingerprint string
		}
		readyCh := make(chan readyPage, len(regenerate)+1) // buffer = regenerate bucket size

		dispatchCtx, dispatchCancel := context.WithCancel(runCtx)
		defer dispatchCancel()

		var dispatchWG sync.WaitGroup
		dispatchWG.Add(1)
		go func() {
			defer dispatchWG.Done()
			for ev := range readyCh {
				if dispatchCtx.Err() != nil {
					// Parent canceled; stop processing further queued events.
					// Remaining pages stay in status='generating' (from the run-start write
					// above); smart-resume on the next run re-dispatches them (CR2 + r3).
					slog.Info("livingwiki/coldstart: dispatch worker exiting after cancel",
						"repo_id", repoID, "queued_remaining", len(readyCh))
					return
				}
				streamDispatchPage(dispatchCtx, runCtx, ev.page, ev.fingerprint,
					writers, markdown.NewTokenBucketRateLimiter(markdown.DefaultSinkRates()),
					lwmetrics.Default, publishStatusStore, repoID, writerBuildErr)
			}
		}()

		// Wire OnPageReady into the orchestrator request.
		genReq := lworch.GenerateRequest{
			Config: lworch.Config{
				RepoID:         repoID,
				MaxConcurrency: coldStartMaxConcurrency,
				TimeBudget:     coldStartTimeBudget,
			},
			Pages:            regenerate,
			PageFingerprints: currentFps,
		}

		// ── Step 2: Generate pages with progress reporting ────────────────────
		var generated, excludedCount int32
		var excludedIDsAcc atomicStringSlice
		// indexUpdateCount tracks how many OnPageDone callbacks have fired;
		// used to trigger a mid-run index update every indexUpdateEvery pages.
		var indexUpdateCount int32

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

			// Phase 2: trigger an index update every indexUpdateEvery page completions.
			// OnPageDone fires from per-page goroutines (mozart's locked surface);
			// dispatchIndex is goroutine-safe (mutex-protected) and runs quickly.
			n := int(atomic.AddInt32(&indexUpdateCount, 1))
			if n%indexUpdateEvery == 0 {
				// Use runCtx (not dispatchCtx) so index updates outlast the
				// per-page dispatch goroutine's cancellation window.
				go dispatchIndex(runCtx, "periodic")
			}
		}

		// Heartbeat: tick liveness every 30s while Generate runs so the
		// LLM-orchestrator stale-reaper sees fresh updated_at timestamps
		// even when no page completes for a long stretch (e.g. all parallel
		// workers happen to be on slow architecture pages simultaneously).
		// Without this, sourcebridge-sized cold starts get reaped at the
		// 30-minute "no progress" threshold even though the goroutines are
		// still actively producing.
		//
		// Slice 3 of plan 2026-04-29-livingwiki-cold-start-progress.md:
		//   - Call rt.Heartbeat() unconditionally on every tick. Heartbeat
		//     bypasses the runtime progress debounce and writes only
		//     updated_at, so a heartbeat blip doesn't depend on whether
		//     ReportProgress thinks the values changed.
		//   - Also call rt.ReportProgress() so the UI bar advances; use
		//     toGenerate (codex r1 [Medium] — using `total` would let the
		//     bar regress after smart-resume skips).
		//   - Bump tick from 60s → 30s. The reaper threshold is 30 min;
		//     30s is well within budget and gives one full retry-grace
		//     window for any transient DB blip.
		slog.Info("livingwiki/coldstart: heartbeat goroutine started",
			"job_id", jobID, "repo_id", repoID, "tick_interval", "30s")
		hbCtx, hbStop := context.WithCancel(runCtx)
		defer hbStop()
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-hbCtx.Done():
					return
				case <-ticker.C:
					// runtime.Heartbeat() already structured-logs on
					// failure with job_id (codex r2 [Low] — avoid
					// double-warn). We log here only at debug to add
					// repo_id for log correlation. Failure does not
					// abort — next tick may succeed and the reaper has
					// a 30-min window before it kills the job.
					if hbErr := rt.Heartbeat(); hbErr != nil {
						slog.Debug("livingwiki/coldstart: heartbeat failed (already warned by runtime)",
							"job_id", jobID, "repo_id", repoID,
							"error", hbErr.Error())
					}
					done := int(atomic.LoadInt32(&generated)) + int(atomic.LoadInt32(&excludedCount))
					var p float64
					if toGenerate > 0 {
						p = 0.05 + 0.85*float64(done)/float64(toGenerate)
					}
					rt.ReportProgress(p, "generating",
						fmt.Sprintf("%d/%d pages complete", done, toGenerate))
					// Phase 2: refresh the index page on every 30s heartbeat tick.
					// Run in a goroutine so a slow sink write does not delay the next
					// heartbeat. runCtx (not hbCtx) so the write can complete even if
					// the heartbeat goroutine is stopped first.
					go dispatchIndex(runCtx, "heartbeat")
				}
			}
		}()

		// Use an in-memory WikiPR so pages are stored as proposed_ast.
		pr := lworch.NewMemoryWikiPR(fmt.Sprintf("pr-%s", jobID))

		// Complete the genReq wiring: PR, OnPageDone, and OnPageReady (CR2).
		genReq.PR = pr
		genReq.OnPageDone = onPageDone
		if publishStatusStore != nil {
			genReq.OnPageReady = func(page ast.Page, fp string) {
				// Non-blocking enqueue. Buffer is sized to regenerate so this
				// never blocks under normal conditions.
				// Defensive overflow path: if the buffer somehow fills, write a
				// 'failed' row directly so smart-resume picks it up next run (CR2).
				select {
				case readyCh <- readyPage{page: page, fingerprint: fp}:
				default:
					slog.Warn("livingwiki/coldstart: dispatch buffer full; recording failed status for resume",
						"repo_id", repoID, "page_id", page.ID)
					statusCtx, statusCancel := context.WithTimeout(
						context.WithoutCancel(runCtx), 5*time.Second)
					defer statusCancel()
					for _, w := range writers {
						_ = publishStatusStore.SetNonReady(statusCtx, livingwiki.SetNonReadyArgs{
							RepoID:          repoID,
							PageID:          page.ID,
							SinkKind:        string(w.Writer.Kind()),
							IntegrationName: w.Name,
							Status:          "failed",
							ErrorMsg:        "dispatch buffer overflow",
						})
					}
				}
			}
		}

		result, err := lwOrch.Generate(runCtx, genReq)

		// Close the readyCh and wait for the dispatcher to drain (CR2).
		// This happens AFTER Generate returns; the persistence loop has finished,
		// no more events will be enqueued.
		close(readyCh)
		dispatchWG.Wait()

		// Phase 2: dispatch the final index page after all stream-dispatch has
		// drained. This ensures the terminal state (all pages Ready/Failed) is
		// reflected before the job is declared complete.
		dispatchIndex(runCtx, "final")

		elapsed := time.Since(start)

		// IsPartialGenerationError matches ErrTimeBudgetExceeded and
		// ErrSystemicSoftFailures — both signal "the run aborted, but pages
		// that completed before the abort have been persisted by the
		// orchestrator and may be dispatched to sinks." Other errors
		// (template-not-found, user cancellation, store failures) leave the
		// store partially-written or untouched and MUST NOT trigger sink
		// dispatch.
		isPartial := lworch.IsPartialGenerationError(err)

		// ── Step 3: Classify generation outcome ───────────────────────────────
		status := "ok"
		failCat := coldstart.FailureCategoryNone
		errMsg := ""

		switch {
		case err != nil && !isPartial:
			// Hard failure — generated set is unreliable; report failed.
			status = "failed"
			failCat = coldstart.ClassifyError(err)
			errMsg = err.Error()
		case errors.Is(err, lworch.ErrSystemicSoftFailures):
			// Codex r2 [Medium]: surface systemic LLM failures with a
			// distinct failure category so the UI can show the right CTA
			// ("provider unreachable; check the LLM config" vs the normal
			// "retry excluded pages"). The orchestrator persisted any
			// pages that completed before the breaker tripped.
			status = "partial"
			failCat = coldstart.FailureCategorySystemicLLM
			errMsg = err.Error()
			// Record the systemic-abort metric with the dominant per-page
			// category that tripped the breaker. SystemicAbortCategory
			// extracts the structured detail from the error chain via
			// errors.As; returns "" if the error doesn't carry one (which
			// is then clamped to "unknown" inside RecordColdStartSystemicAbort).
			metricsCollector.RecordColdStartSystemicAbort(
				lworch.SystemicAbortCategory(err))
		case err != nil && isPartial:
			// Soft abort with partial persistence (time-budget) — surface as partial.
			status = "partial"
			failCat = coldstart.FailureCategoryPartialContent
			errMsg = err.Error()
		case len(result.Excluded) > 0:
			status = "partial"
			failCat = coldstart.FailureCategoryPartialContent
		}

		// Codex r2 [Medium]: on hard-error paths the orchestrator did NOT
		// persist anything, so the OnPageDone-driven `generated` counter
		// overstates the durable state. Use len(result.Generated) instead
		// — Generate now zeroes that on hard errors per its contract.
		finalGen := int(atomic.LoadInt32(&generated))
		finalExcl := int(atomic.LoadInt32(&excludedCount))
		if err != nil && !isPartial {
			finalGen = len(result.Generated) // 0 on hard errors
			finalExcl = len(result.Excluded) // exclusions still surface
		}

		// ── Step 4: Dispatch generated pages to configured sinks ──────────────
		var sinkResults []livingwiki.SinkWriteResult

		// Dispatch gating: success OR a partial-generation error class. Other
		// errors leave sinks untouched. (codex r1b [Medium])
		if (err == nil || isPartial) && (len(result.Generated) > 0 || len(skippedPageIDs) > 0) {
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
			// Codex r1b [High]: derive PagesExcluded, ExcludedPageIDs, and
			// ExclusionFailureCategories all from result.Excluded so the
			// persisted record is self-consistent. The live atomic
			// excludedCount can undercount on systemic-abort paths where
			// the breaker returns before OnPageDone fires for the
			// tripping page. Live progress counters are used only for the
			// in-flight UI; persistence uses the orchestrator's authoritative
			// result.
			persistExIDs := make([]string, len(result.Excluded))
			persistExCats := make([]string, len(result.Excluded))
			for i, ex := range result.Excluded {
				persistExIDs[i] = ex.PageID
				persistExCats[i] = ex.FailureCategory
			}
			persistExclCount := len(result.Excluded)
			reasons := buildExclusionReasons(result.Excluded)

			jobResult := &livingwiki.LivingWikiJobResult{
				RepoID:                     repoID,
				JobID:                      jobID,
				StartedAt:                  start,
				CompletedAt:                &now,
				PagesPlanned:               total,
				PagesGenerated:             finalGen,
				PagesExcluded:              persistExclCount,
				ExcludedPageIDs:            persistExIDs,
				ExclusionReasons:           reasons,
				ExclusionFailureCategories: persistExCats,
				SinkWriteResults:           sinkResults,
				Status:                     status,
				FailureCategory:            string(failCat),
				ErrorMessage:               errMsg,
			}
			if saveErr := jobResultStore.Save(runCtx, tenantID, jobResult); saveErr != nil {
				slog.Warn("living-wiki: failed to persist job result",
					"job_id", jobID, "repo_id", repoID, "error", saveErr)
			}
		}

		// ── Step 6: Prometheus counter ────────────────────────────────────────
		metricsCollector.RecordJob(status, sinkKind, elapsed.Seconds())

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
// returns the full planned-page list for the given repo. graphStore, the
// LLM caller, and clusterStore may be nil; the resolver degrades gracefully
// (no LLM-dependent pages will be generated and the package-path heuristic
// is used for architecture pages, but the job won't hard-fail).
func resolveTaxonomy(ctx context.Context, repoID string, gs graphstore.GraphStore, lc *llmcall.Caller, cs clustering.ClusterStore) ([]lworch.PlannedPage, error) {
	var sg templates.SymbolGraph
	if gs != nil {
		sg = &graphStoreSymbolGraph{store: gs}
	}
	var llmTemplateCaller templates.LLMCaller
	if lc != nil && lc.IsAvailable() {
		llmTemplateCaller = &coldStartLLMCaller{caller: lc, repoID: repoID, op: resolution.OpLivingWikiColdStart}
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
				sum := clustering.ClusterSummary{
					ID:          c.ID,
					Label:       label,
					MemberCount: c.Size,
				}
				// Populate MemberPackages so TaxonomyResolver can derive
				// cross-cluster dependency links. Fetch the full cluster record
				// (includes Members) then map symbol IDs → package paths via gs.
				if gs != nil {
					if full, ferr := cs.GetClusterByID(ctx, c.ID); ferr == nil && full != nil && len(full.Members) > 0 {
						symIDs := make([]string, len(full.Members))
						for j, m := range full.Members {
							symIDs[j] = m.SymbolID
						}
						symMap := gs.GetSymbolsByIDs(symIDs)
						seen := make(map[string]struct{})
						for _, sym := range symMap {
							if sym.FilePath == "" {
								continue
							}
							pkg := sym.FilePath
							if idx := strings.LastIndex(sym.FilePath, "/"); idx >= 0 {
								pkg = sym.FilePath[:idx]
							}
							if _, ok := seen[pkg]; !ok {
								seen[pkg] = struct{}{}
								sum.MemberPackages = append(sum.MemberPackages, pkg)
							}
						}
					}
				}
				clusterSummaries[i] = sum
			}
		}
	}

	tr := lworch.NewTaxonomyResolver(repoID, sg, nil /* gitLog */, llmTemplateCaller)
	if gs != nil {
		tr.WithPackageDeps(gs)
	}
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

// coldStartLLMCaller adapts the LLM-aware llmcall.Caller to the
// templates.LLMCaller interface used by the cold-start TaxonomyResolver.
// Slice 2 of the workspace-LLM-source-of-truth plan replaced the prior
// direct *worker.Client wiring with the Caller wrapper so workspace-saved
// settings flow through gRPC metadata on every cold-start call —
// previously this was the smoking-gun bypass that drained the user's
// Anthropic credit because cold-start pages always ran on the worker's
// bootstrap (configmap) provider regardless of UI settings.
type coldStartLLMCaller struct {
	caller *llmcall.Caller
	repoID string
	op     string
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
	if c == nil || c.caller == nil {
		return "", fmt.Errorf("cold-start LLM caller: not configured")
	}
	question := userPrompt
	if systemPrompt != "" {
		question = systemPrompt + "\n\n" + userPrompt
	}
	callCtx, cancel := context.WithTimeout(ctx, perCallLLMTimeout)
	defer cancel()
	resp, err := c.caller.AnswerQuestion(callCtx, c.repoID, c.op, &reasoningv1.AnswerQuestionRequest{
		Question: question,
	})
	if err != nil {
		return "", fmt.Errorf("cold-start LLM caller: %w", err)
	}
	return resp.GetAnswer(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Knowledge artifact resolution
// ─────────────────────────────────────────────────────────────────────────────

// attachKnowledgeArtifacts queries the knowledge store for ready, non-stale
// artifacts covering this repo and attaches fresh ones to each architecture
// page's PackageInfo. Non-architecture pages and pages without a PackageInfo
// are left untouched.
//
// Freshness rule: exact match on UnderstandingRevisionFP against the current
// repo-level repository understanding's RevisionFP. If the understanding is
// absent or has no revisionFp, all artifacts are considered stale and the
// function is a no-op (the template falls back to raw-symbol generation).
//
// When knowledgeStore is nil the slice is returned unchanged.
func attachKnowledgeArtifacts(
	ctx context.Context,
	repoID string,
	pages []lworch.PlannedPage,
	ks knowledge.KnowledgeStore,
) []lworch.PlannedPage {
	if ks == nil {
		return pages
	}

	// Determine the freshness bar: the revisionFp from the current repo-level
	// understanding. Pull it once; all clusters use the same bar.
	currentUnderstanding := ks.GetRepositoryUnderstanding(
		repoID,
		knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
	)
	var currentRevFP string
	if currentUnderstanding != nil {
		currentRevFP = currentUnderstanding.RevisionFP
	}

	// With no current understanding fingerprint we cannot assert freshness;
	// fall back to raw-symbol generation for all pages.
	if currentRevFP == "" {
		return pages
	}

	// Fetch all artifacts for the repo in one call, then filter and sort.
	allArtifacts := ks.GetKnowledgeArtifacts(repoID)

	// Build a lookup: scopePath → sorted fresh artifacts.
	// scopePath for module-scoped artifacts is the module path (directory).
	// Repository-scoped artifacts are filed under the empty string as a fallback.
	type candidateArtifact struct {
		art   *knowledge.Artifact
		depth int // 3=deep 2=medium 1=summary 0=other
	}
	depthRank := func(d knowledge.Depth) int {
		switch d {
		case knowledge.DepthDeep:
			return 3
		case knowledge.DepthMedium:
			return 2
		case knowledge.DepthSummary:
			return 1
		default:
			return 0
		}
	}

	byScope := make(map[string][]candidateArtifact)
	var staleSkipped int
	for _, a := range allArtifacts {
		if a.Status != knowledge.StatusReady || a.Stale {
			continue
		}
		if a.UnderstandingRevisionFP != currentRevFP {
			staleSkipped++
			continue
		}
		scope := knowledge.ScopeRepository
		scopePath := ""
		if a.Scope != nil {
			scope = a.Scope.ScopeType
			scopePath = a.Scope.ScopePath
		}
		if scope != knowledge.ScopeModule && scope != knowledge.ScopeRepository {
			// File- and symbol-scoped artifacts are too narrow for architecture pages.
			continue
		}
		byScope[scopePath] = append(byScope[scopePath], candidateArtifact{
			art:   a,
			depth: depthRank(a.Depth),
		})
	}

	// Sort each bucket: deepest first, then newest GeneratedAt desc.
	for key := range byScope {
		sort.Slice(byScope[key], func(i, j int) bool {
			di, dj := byScope[key][i].depth, byScope[key][j].depth
			if di != dj {
				return di > dj
			}
			return byScope[key][i].art.GeneratedAt.After(byScope[key][j].art.GeneratedAt)
		})
	}

	_ = staleSkipped // used in logArtifactResolution below

	// Attach to each architecture page by matching cluster MemberPackages against
	// module-scoped artifact paths, with a repo-level fallback.
	for i, p := range pages {
		if p.TemplateID != "architecture" || p.PackageInfo == nil {
			continue
		}
		pkg := p.PackageInfo

		// Collect all candidate artifacts: first module-scoped matches, then
		// repo-scoped fallback if no module match was found.
		var candidates []candidateArtifact

		// Module scope: artifact's scopePath matches any member package (or is a
		// prefix of one, since a cluster may span multiple sub-packages).
		memberSet := make(map[string]struct{}, len(pkg.MemberPackages)+1)
		// The cluster label itself (pkg.Package) is always a member.
		memberSet[pkg.Package] = struct{}{}
		for _, mp := range pkg.MemberPackages {
			memberSet[mp] = struct{}{}
		}

		for scopePath, bucket := range byScope {
			if scopePath == "" {
				continue // repo-level; handled below as fallback
			}
			for member := range memberSet {
				if member == scopePath || strings.HasPrefix(member, scopePath+"/") || strings.HasPrefix(scopePath, member+"/") {
					candidates = append(candidates, bucket...)
					break
				}
			}
		}

		// Repo-level fallback only when no module-scoped artifact matched.
		if len(candidates) == 0 {
			candidates = append(candidates, byScope[""]...)
		}

		// Cap and convert.
		if len(candidates) > maxArtifactsPerCluster {
			candidates = candidates[:maxArtifactsPerCluster]
		}

		summaries := make([]lworch.KnowledgeArtifactSummary, 0, len(candidates))
		for _, c := range candidates {
			// CR6: populate ID and ScopeType so the fingerprint helper can
			// include per-artifact identity in the page fingerprint. Without
			// these fields, re-running understanding at the same CommitSHA
			// would not invalidate the fingerprint even when artifact content changed.
			scopeType := ""
			if c.art.Scope != nil {
				scopeType = string(c.art.Scope.ScopeType)
			}
			s := lworch.KnowledgeArtifactSummary{
				ID:          c.art.ID,
				Type:        string(c.art.Type),
				Audience:    string(c.art.Audience),
				Depth:       string(c.art.Depth),
				ScopePath:   scopePathOf(c.art),
				ScopeType:   scopeType,
				RevisionFp:  c.art.UnderstandingRevisionFP,
				GeneratedAt: c.art.GeneratedAt,
			}
			for _, sec := range c.art.Sections {
				ks2 := lworch.KnowledgeSection{
					Title:   sec.Title,
					Content: sec.Content,
					Summary: sec.Summary,
				}
				for _, ev := range sec.Evidence {
					ks2.Evidence = append(ks2.Evidence, lworch.KnowledgeEvidence{
						FilePath:  ev.FilePath,
						LineStart: ev.LineStart,
						LineEnd:   ev.LineEnd,
						Rationale: ev.Rationale,
					})
				}
				s.Sections = append(s.Sections, ks2)
			}
			summaries = append(summaries, s)
		}

		if len(summaries) > 0 {
			updated := *pkg
			updated.KnowledgeArtifacts = summaries
			pages[i].PackageInfo = &updated
		}
	}

	return pages
}

// scopePathOf returns the canonical scope path for an artifact, empty when the
// artifact is repository-scoped.
func scopePathOf(a *knowledge.Artifact) string {
	if a.Scope == nil {
		return ""
	}
	return a.Scope.ScopePath
}

// logArtifactResolution emits a single structured slog event summarising how
// many clusters received fresh knowledge artifacts. This is the breadcrumb
// needed to debug "why didn't my artifact get used".
func logArtifactResolution(repoID string, pages []lworch.PlannedPage) {
	clusters := 0
	withArtifacts := 0
	totalArtifacts := 0
	staleSkipped := 0 // placeholder — computed in attachKnowledgeArtifacts

	for _, p := range pages {
		if p.TemplateID != "architecture" || p.PackageInfo == nil {
			continue
		}
		clusters++
		n := len(p.PackageInfo.KnowledgeArtifacts)
		if n > 0 {
			withArtifacts++
			totalArtifacts += n
		}
	}

	slog.Info("livingwiki/coldstart: knowledge artifact resolution",
		"repo_id", repoID,
		"clusters", clusters,
		"clusters_with_artifacts", withArtifacts,
		"artifacts_used", totalArtifacts,
		"stale_skipped", staleSkipped,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Smart-resume helpers (Phase 1)
// ─────────────────────────────────────────────────────────────────────────────

// Bucket constants for the 3-way smart-resume split (CR4).
const (
	bucketRegenerate     = "regenerate"
	bucketSkipFully      = "skipFully"
	bucketSkipNeedsFixup = "skipNeedsFixup"
)

// buildRelatedPageIDsByLabel returns a map from cluster label (or page ID
// suffix) to page ID for every planned page. This is built from the FULL
// manifest BEFORE smart-resume splits it into buckets, so pages in the
// regenerate bucket can resolve correct cross-page links to pages in the
// skip buckets (CR3).
//
// Key: the human-visible label used in cross-page links.
// For architecture pages: PackageInfo.Package (the cluster label).
// For non-architecture pages: the page ID itself.
func buildRelatedPageIDsByLabel(pages []lworch.PlannedPage) map[string]string {
	m := make(map[string]string, len(pages))
	for _, p := range pages {
		if p.TemplateID == "architecture" && p.PackageInfo != nil {
			m[p.PackageInfo.Package] = p.ID
		} else {
			m[p.ID] = p.ID
		}
	}
	return m
}

// livingWikiModelIdentity resolves the LLM model identity string for the
// cold-start run. Format: "<provider>/<model>" per LD-7 / C1.
// Returns "unresolved/unresolved" on any resolve failure so fingerprints
// always include a sentinel rather than an empty string.
func livingWikiModelIdentity(ctx context.Context, resolver resolution.Resolver, repoID string) string {
	if resolver == nil {
		return "unresolved/unresolved"
	}
	snap, err := resolver.Resolve(ctx, repoID, resolution.OpLivingWikiColdStart)
	if err != nil || snap.Provider == "" || snap.Model == "" {
		return "unresolved/unresolved"
	}
	return snap.Provider + "/" + snap.Model
}

// repoSourceRevFor returns the canonical revision string for a repository used
// as the repoSourceRev input to the fingerprint algorithm. Prefers CommitSHA
// when present; falls back to LastIndexedAt nanoseconds as a monotonic proxy.
// Returns "" when the graphStore is nil or the repository is unknown (the
// fingerprint will still be computed, but rev-dependent changes won't invalidate it).
func repoSourceRevFor(gs graphstore.GraphStore, repoID string) string {
	if gs == nil {
		return ""
	}
	repo := gs.GetRepository(repoID)
	if repo == nil {
		return ""
	}
	if repo.CommitSHA != "" {
		return repo.CommitSHA
	}
	return fmt.Sprintf("%d", repo.LastIndexedAt.UnixNano())
}

// buildSinkWriters is the cold-start–local wrapper around sinks.BuildSinkWriters
// that takes a credential snapshot before building. It returns (nil, nil) when
// the broker or repoSettingsStore is nil. Errors from BuildSinkWriters are
// returned directly so callers can classify missing-creds vs not-implemented.
func buildSinkWriters(
	ctx context.Context,
	repoID string,
	broker credentials.Broker,
	repoSettingsStore livingwiki.RepoSettingsStore,
) ([]sinks.NamedSinkWriter, error) {
	if broker == nil || repoSettingsStore == nil {
		return nil, nil
	}
	repoSettings, err := repoSettingsStore.GetRepoSettings(ctx, defaultTenantID, repoID)
	if err != nil || repoSettings == nil || len(repoSettings.Sinks) == 0 {
		return nil, nil
	}
	snap, err := credentials.Take(ctx, broker)
	if err != nil {
		return nil, nil
	}
	// repoName not needed for smart-resume (only for hierarchy-create in dispatch).
	return sinks.BuildSinkWriters(ctx, repoSettings, snap, "")
}

// classifyPage implements the 3-way smart-resume decision (CR4):
//
//   - regenerate      — absent from any configured sink, OR fingerprint mismatch,
//     OR status != "ready" in the status store.
//   - skipFully       — present on every sink, fp matches, status="ready",
//     fixup_status is "none" or "done".
//   - skipNeedsFixup  — present everywhere, fp matches, status="ready",
//     fixup_status is "pending" or "failed".
//
// alreadyPublished is the cross-sink intersection from listAlreadyPublishedAcrossSinks.
// currentFp is the freshly-computed fingerprint for this page.
// persistedFps maps pageID → sinkKey → PagePublishStatusRow (nil map ⇒ no rows).
// writers is the currently-configured sink list; when empty (no sinks) every
// page regenerates so the generation phase runs (the dispatch phase then
// produces no sink writes but the AST is persisted).
func classifyPage(
	pageID string,
	alreadyPublished map[string]struct{},
	currentFp string,
	persistedFps map[string]map[string]livingwiki.PagePublishStatusRow,
	writers []sinks.NamedSinkWriter,
) string {
	// When there are no writers, regenerate every page so the AST is at least
	// freshly persisted (useful for testing without sink credentials).
	if len(writers) == 0 {
		return bucketRegenerate
	}

	// Must be published to every configured sink.
	if _, ok := alreadyPublished[pageID]; !ok {
		return bucketRegenerate
	}

	// Check the status store for every sink.
	sinkRows := persistedFps[pageID] // nil if no rows
	for _, w := range writers {
		sinkKey := string(w.Writer.Kind()) + "/" + w.Name
		row, ok := sinkRows[sinkKey]
		if !ok || row.Status != "ready" {
			return bucketRegenerate
		}
		if row.ContentFingerprint != currentFp {
			return bucketRegenerate
		}
	}

	// Fingerprints match and all sinks show "ready". Check fixup status.
	for _, w := range writers {
		sinkKey := string(w.Writer.Kind()) + "/" + w.Name
		row := sinkRows[sinkKey]
		if row.FixupStatus == livingwiki.FixupStatusPending ||
			row.FixupStatus == livingwiki.FixupStatusFailed {
			return bucketSkipNeedsFixup
		}
	}
	return bucketSkipFully
}

// streamDispatchPage dispatches a single ready page to every configured sink
// and writes the resulting status to the publishStatusStore.
//
// Cancellation model (CR2):
//   - Sink write calls use dispatchCtx so they are cancelled when the parent
//     job is killed or the time budget is exceeded.
//   - Status-store writes use a fresh context.WithoutCancel-bounded context
//     (5 s) so they survive dispatchCtx cancellation and the next run sees
//     accurate state rather than stale "generating" rows.
//
// writerBuildErr is the error returned when the sink writers were constructed;
// when non-nil it is recorded as a "failed" status for every writer slot
// without attempting any write.
func streamDispatchPage(
	dispatchCtx context.Context,
	parentCtx context.Context,
	page ast.Page,
	fingerprint string,
	writers []sinks.NamedSinkWriter,
	rateLimiter markdown.SinkRateLimiter,
	mc *lwmetrics.Collector,
	statusStore livingwiki.PagePublishStatusStore,
	repoID string,
	writerBuildErr error,
) {
	if len(writers) == 0 {
		return
	}

	// Helper: write a status row without being cancelled by dispatchCtx.
	writeStatus := func(args livingwiki.SetReadyArgs) {
		statusCtx, cancel := context.WithTimeout(
			context.WithoutCancel(parentCtx), 5*time.Second)
		defer cancel()
		if statusStore != nil {
			_ = statusStore.SetReady(statusCtx, args)
		}
	}
	writeNonReady := func(args livingwiki.SetNonReadyArgs) {
		statusCtx, cancel := context.WithTimeout(
			context.WithoutCancel(parentCtx), 5*time.Second)
		defer cancel()
		if statusStore != nil {
			_ = statusStore.SetNonReady(statusCtx, args)
		}
	}

	// When the writer build failed, record it for every sink slot.
	if writerBuildErr != nil {
		for _, w := range writers {
			writeNonReady(livingwiki.SetNonReadyArgs{
				RepoID:          repoID,
				PageID:          page.ID,
				SinkKind:        string(w.Writer.Kind()),
				IntegrationName: w.Name,
				Status:          "failed",
				ErrorMsg:        writerBuildErr.Error(),
			})
		}
		return
	}

	// Dispatch the single page to every sink sequentially (same rate-limit contract
	// as DispatchPagesNamed, but for one page at a time so we can capture per-page
	// fingerprints immediately).
	for _, nsw := range writers {
		if dispatchCtx.Err() != nil {
			break
		}

		sw := nsw.Writer
		sinkKind := string(sw.Kind())
		integrationName := nsw.Name

		if rateLimiter != nil {
			if rlErr := rateLimiter.Allow(dispatchCtx, sw.Kind()); rlErr != nil {
				writeNonReady(livingwiki.SetNonReadyArgs{
					RepoID:          repoID,
					PageID:          page.ID,
					SinkKind:        sinkKind,
					IntegrationName: integrationName,
					Status:          "failed",
					ErrorMsg:        "rate limit exceeded: " + rlErr.Error(),
				})
				continue
			}
		}

		wStart := time.Now()
		writeErr := sw.WritePage(dispatchCtx, page)
		writeDuration := time.Since(wStart).Seconds()
		if mc != nil {
			mc.RecordSinkWrite(sinkKind, writeDuration)
		}
		if rateLimiter != nil {
			rateLimiter.Record(sw.Kind())
		}

		if writeErr != nil {
			slog.Warn("livingwiki/dispatch: page write failed",
				"sink", sinkKind, "repo_id", repoID, "page_id", page.ID,
				"error", writeErr)
			writeNonReady(livingwiki.SetNonReadyArgs{
				RepoID:          repoID,
				PageID:          page.ID,
				SinkKind:        sinkKind,
				IntegrationName: integrationName,
				Status:          "failed",
				ErrorMsg:        writeErr.Error(),
			})
			continue
		}

		// Success — record the fingerprint so next run can skip this page.
		writeStatus(livingwiki.SetReadyArgs{
			RepoID:          repoID,
			PageID:          page.ID,
			SinkKind:        sinkKind,
			IntegrationName: integrationName,
			Fingerprint:     fingerprint,
			// HasStubs is always false here: the orchestrator's persistence
			// loop fires OnPageReady only after a fully-resolved page passes
			// the quality gate. Stub-link fixup (Phase 3) updates this later.
			HasStubs:    false,
			FixupStatus: livingwiki.FixupStatusNone,
		})
	}
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
