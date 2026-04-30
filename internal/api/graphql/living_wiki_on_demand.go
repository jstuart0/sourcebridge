// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// living_wiki_on_demand.go — Mutation.generateLivingWikiPageOnDemand.
//
// On-demand drill-down: generates a single Detailed-mode page for the
// requested folder or symbol and dispatches it to all configured sinks.
//
// Design decisions (Phase 4a, LD-6):
//   - Synchronous execution within the mutation: one page, known upfront, no
//     need for the cold-start runner's smart-resume, progress reporting, or
//     job-result bookkeeping machinery.
//   - Gated on LivingWikiDetailedEnabled: a folder/symbol drill-down always
//     produces a detail.* page; that mode must be opted in before we'll run it.
//   - Validation: exactly one of folder or symbol must be provided. Both is a
//     programming error in the caller (returns INVALID_PAGE_SPEC).
//   - Reuses every locked invariant: DetailPageID, dispatchGeneratedPages,
//     GenerateRequest / lworch.Generate — the same path used by the cold-start
//     runner.

package graphql

import (
	"context"
	"fmt"
	"time"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/coldstart"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/quality"
	"github.com/sourcebridge/sourcebridge/internal/reports/templates"
)

// GenerateLivingWikiPageOnDemand implements Mutation.generateLivingWikiPageOnDemand.
//
// Validation order:
//  1. LivingWikiRepoStore must be configured; the repo's settings must have
//     LivingWikiDetailedEnabled = true.
//  2. Exactly one of pageSpec.Folder or pageSpec.Symbol must be non-nil and
//     non-empty. Both or neither returns INVALID_PAGE_SPEC.
//  3. lwOrch must be configured (returns a user-facing error when nil).
func (r *mutationResolver) GenerateLivingWikiPageOnDemand(
	ctx context.Context,
	repositoryID string,
	pageSpec LivingWikiOnDemandPageSpec,
) (*LivingWikiOnDemandPageResult, error) {
	// ── 1. Gate: Detailed mode must be enabled ────────────────────────────────
	if r.LivingWikiRepoStore == nil {
		return nil, fmt.Errorf("living-wiki repo store not configured")
	}
	settings, err := r.LivingWikiRepoStore.GetRepoSettings(ctx, defaultTenantID, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("load repo settings: %w", err)
	}
	if settings == nil || !settings.LivingWikiDetailedEnabled {
		return nil, &gqlerror.Error{
			Message: "On-demand drill-down requires Detailed mode to be enabled for this repository. " +
				"Enable Detailed mode in the Living Wiki settings panel and try again.",
			Extensions: map[string]interface{}{
				"code": "LIVING_WIKI_DETAILED_DISABLED",
			},
		}
	}

	// ── 2. Validate pageSpec — exactly one of folder / symbol ─────────────────
	hasFolder := pageSpec.Folder != nil && *pageSpec.Folder != ""
	hasSymbol := pageSpec.Symbol != nil && *pageSpec.Symbol != ""
	if hasFolder && hasSymbol {
		return nil, &gqlerror.Error{
			Message: "pageSpec must contain exactly one of folder or symbol, not both.",
			Extensions: map[string]interface{}{
				"code": "INVALID_PAGE_SPEC",
			},
		}
	}
	if !hasFolder && !hasSymbol {
		return nil, &gqlerror.Error{
			Message: "pageSpec must contain exactly one of folder or symbol.",
			Extensions: map[string]interface{}{
				"code": "INVALID_PAGE_SPEC",
			},
		}
	}

	// ── 3. Derive the page ID ─────────────────────────────────────────────────
	//
	// Both folder and symbol requests produce a detail.* page covering the
	// folder/package scope. Symbol-scoped pages are architecture pages for the
	// containing package — the caller uses the symbol identifier as the scope
	// path.
	var scope string
	if hasFolder {
		scope = *pageSpec.Folder
	} else {
		scope = *pageSpec.Symbol
	}
	pageID := lworch.DetailPageID(repositoryID, scope)

	// ── 4. Require the orchestrator ───────────────────────────────────────────
	if r.LivingWikiLiveOrchestrator == nil {
		return nil, fmt.Errorf("living-wiki orchestrator not configured")
	}
	lwOrch := r.LivingWikiLiveOrchestrator

	// ── 5. Build the single PlannedPage ──────────────────────────────────────
	var sg templates.SymbolGraph
	gs := r.getStore(ctx)
	if gs != nil {
		sg = &graphStoreSymbolGraph{store: gs}
	}
	var llmTemplateCaller templates.LLMCaller
	if r.LLMCaller != nil && r.LLMCaller.IsAvailable() {
		llmTemplateCaller = &coldStartLLMCaller{
			caller: r.LLMCaller,
			repoID: repositoryID,
			op:     resolution.OpLivingWikiColdStart,
		}
	}

	baseInput := templates.GenerateInput{
		RepoID:      repositoryID,
		SymbolGraph: sg,
		LLM:         llmTemplateCaller,
		Now:         time.Now(),
	}

	page := lworch.PlannedPage{
		ID:         pageID,
		TemplateID: "architecture",
		Audience:   quality.AudienceEngineers,
		Input:      baseInput,
		PackageInfo: &lworch.ArchitecturePackageInfo{
			Package: scope,
		},
	}

	// ── 6. Generate the single page ───────────────────────────────────────────
	genReq := lworch.GenerateRequest{
		Config: lworch.Config{
			RepoID:         repositoryID,
			MaxConcurrency: 1,
			TimeBudget:     coldStartTimeBudget,
		},
		Pages: []lworch.PlannedPage{page},
	}

	result, genErr := lwOrch.Generate(ctx, genReq)
	if genErr != nil && !lworch.IsPartialGenerationError(genErr) {
		return nil, fmt.Errorf("on-demand generation failed: %w", genErr)
	}

	// ── 7. Dispatch to sinks ──────────────────────────────────────────────────
	var repoName string
	if gs != nil {
		if repo := gs.GetRepository(repositoryID); repo != nil {
			repoName = repo.Name
		}
	}

	var (
		status  = "ok"
		failCat = coldstart.FailureCategoryNone
		errMsg  string
	)

	var dispatched bool
	if len(result.Generated) > 0 {
		sinkResults := dispatchGeneratedPages(
			ctx, repositoryID, defaultTenantID,
			result.Generated, nil, /* no skippedPageIDs for on-demand */
			r.livingWikiBroker(), r.LivingWikiRepoStore,
			repoName,
			&status, &failCat, &errMsg,
			GenerationModeLWDetailed,
		)
		// Dispatch succeeded when at least one sink wrote the page.
		for _, sr := range sinkResults {
			if sr.PagesWritten > 0 {
				dispatched = true
				break
			}
		}
	}

	return &LivingWikiOnDemandPageResult{
		PageID:     pageID,
		Dispatched: dispatched,
	}, nil
}
