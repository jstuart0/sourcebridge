// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// CA-146 Phase 1: previewLivingWikiPlan query resolver.
//
// Returns a deterministic preview of the pages a Living Wiki cold-start
// would generate, keyed by (repositoryId, mode, pageCountOverride). The
// caller can inspect the page list, deselect non-required pages, and echo
// back planSignature when invoking enableLivingWikiForRepo.

package graphql

import (
	"context"
	"fmt"
	"os"

	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// PreviewLivingWikiPlan resolves Query.previewLivingWikiPlan.
//
// It synchronously resolves the taxonomy for the given mode, applies the
// effective page cap, and returns a LivingWikiPlan with a deterministic
// planSignature. The caller must echo the signature back on
// enableLivingWikiForRepo when selectedPageIds is supplied; on mismatch
// the mutation rejects with LIVING_WIKI_PLAN_STALE.
//
// mode derivation:
//   - nil         → derive from current effective repo settings.
//   - OVERVIEW    → "lw_overview"
//   - DETAILED    → "lw_detailed"
//   - ALL_ENABLED → rejected with PREVIEW_MODE_NOT_SUPPORTED (Decision 8)
//
// pageCountOverride range: 1..500 (same validation as enableLivingWikiForRepo).
//
// Kill-switch / globally-disabled: returns a LivingWikiPlan with
// totalPages=0, pages=[], notice= rather than a typed error — the UI needs
// the notice string to render its "settings saved but paused" banner.
func (r *queryResolver) PreviewLivingWikiPlan(
	ctx context.Context,
	repositoryID string,
	mode *LivingWikiBuildMode,
	pageCountOverride *int,
) (*LivingWikiPlan, error) {
	// ── 1. Kill-switch + global-disabled gate ────────────────────────────────
	//
	// Check before any heavy work. Returns a notice-bearing plan (not an error)
	// so the UI can render the degraded banner rather than a generic error toast.
	killSwitch := os.Getenv("SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH") == "true"
	globalEnabled := r.isLivingWikiGloballyEnabled()

	if killSwitch || !globalEnabled {
		var notice string
		if killSwitch {
			notice = "Living wiki is paused via SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH. Settings are saved but no jobs will run until the kill-switch is unset."
		} else {
			notice = "Living Wiki is not enabled globally. Enable it in the global settings panel, then jobs will start automatically."
		}
		return &LivingWikiPlan{
			PlanSignature: "",
			Mode:          "",
			ModeTooltip:   "",
			Summary:       "",
			TotalPages:    0,
			PreCap:        0,
			CapSource:     "none",
			CapValue:      0,
			Pages:         []*LivingWikiPlanPage{},
			Notice:        &notice,
		}, nil
	}

	// ── 2. Validate pageCountOverride ────────────────────────────────────────
	//
	// Same 1..500 range check as EnableLivingWikiForRepo:2541-2555. Validated
	// before settings load so the error is cheap.
	if pageCountOverride != nil {
		v := *pageCountOverride
		if v < 1 || v > 500 {
			return nil, &gqlerror.Error{
				Message: fmt.Sprintf("pageCountOverride must be between 1 and 500 (got %d)", v),
				Extensions: map[string]any{
					"code": "LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE",
				},
			}
		}
	}

	// ── 3. Reject ALL_ENABLED mode ───────────────────────────────────────────
	//
	// The plan preview is for single explicit-mode builds. ALL_ENABLED is
	// handled by two separate cold-start enqueues upstream and is out of scope
	// (codex r1 M3 / Decision 8). Reject before touching settings to keep the
	// error fast and self-describing.
	if mode != nil && *mode == LivingWikiBuildModeAllEnabled {
		return nil, &gqlerror.Error{
			Message: "previewLivingWikiPlan does not support ALL_ENABLED mode",
			Extensions: map[string]any{
				"code": "PREVIEW_MODE_NOT_SUPPORTED",
			},
		}
	}

	// ── 4. Resolve effective mode ────────────────────────────────────────────
	//
	// When mode is nil we derive it from the current repo settings, exactly as
	// EnableLivingWikiForRepo does (deriveLivingWikiJobMode on persisted row).
	var modeStr string
	switch {
	case mode != nil && *mode == LivingWikiBuildModeOverview:
		modeStr = GenerationModeLWOverview
	case mode != nil && *mode == LivingWikiBuildModeDetailed:
		modeStr = GenerationModeLWDetailed
	default:
		// nil → derive from effective settings. Load existing repo settings; if
		// none exist yet, fall back to lw_detailed (the established default).
		if r.LivingWikiRepoStore != nil {
			existing, err := r.LivingWikiRepoStore.GetRepoSettings(ctx, defaultTenantID, repositoryID)
			if err != nil {
				return nil, fmt.Errorf("load repo settings: %w", err)
			}
			if existing != nil {
				modeStr = deriveLivingWikiJobMode(*existing)
			}
		}
		if modeStr == "" {
			modeStr = GenerationModeLWDetailed
		}
	}

	// ── 5. Resolve frozen LLM caller ─────────────────────────────────────────
	//
	// Preview MUST use the same snapshot strategy as cold-start so the page
	// list a user reviews reflects the model that will actually be used at
	// build time. On resolver error we continue with a nil frozenCaller —
	// resolveTaxonomyForMode degrades gracefully without one.
	var frozenCaller *llmcall.Caller
	if r.LLMResolver != nil && r.LLMCaller != nil {
		snap, resolveErr := r.LLMResolver.Resolve(ctx, repositoryID, resolution.OpLivingWikiColdStart)
		if resolveErr == nil {
			frozenCaller = llmcall.New(r.LLMCaller.Inner(), resolution.NewFrozenResolver(snap), nil)
		}
	}

	// ── 6. Resolve taxonomy ───────────────────────────────────────────────────
	graphStore := r.getStore(ctx)
	pages, err := resolveTaxonomyForMode(ctx, modeStr, repositoryID, graphStore, frozenCaller, r.ClusterStore)
	if err != nil {
		return nil, fmt.Errorf("living-wiki: taxonomy resolution failed: %w", err)
	}

	// ── 7. Resolve MaxPagesPerJob ─────────────────────────────────────────────
	//
	// Load repo settings to obtain MaxPagesPerJob for the cap formula.
	// If no settings row exists, maxPagesPerJob = 0 (no cap).
	var maxPagesPerJob int
	if r.LivingWikiRepoStore != nil {
		if existing, sErr := r.LivingWikiRepoStore.GetRepoSettings(ctx, defaultTenantID, repositoryID); sErr == nil && existing != nil {
			maxPagesPerJob = existing.MaxPagesPerJob
		}
	}

	// ── 8. Apply page-count cap ───────────────────────────────────────────────
	//
	// excludedOnlyRetry=false — preview is never a retry-excluded path.
	cappedPages, capSource, capValue, effectiveCap, preCap := applyPageCap(
		pages, maxPagesPerJob, pageCountOverride, false,
	)

	// ── 9. Compute planSignature ──────────────────────────────────────────────
	//
	// Signature is over (sorted page IDs, modeStr, effectiveCap) — the same
	// formula used by EnableLivingWikiForRepo's validator so symmetry is
	// mechanical (both call computePlanSignature from living_wiki_plan_helpers.go).
	pageIDs := make([]string, len(cappedPages))
	for i, p := range cappedPages {
		pageIDs[i] = p.ID
	}
	planSig := computePlanSignature(pageIDs, modeStr, effectiveCap)

	// ── 10. Build response pages ──────────────────────────────────────────────
	gqlPages := make([]*LivingWikiPlanPage, 0, len(cappedPages))
	for _, p := range cappedPages {
		page := plannedPageToGQL(p)
		gqlPages = append(gqlPages, page)
	}

	// ── 11. Build summary (for slog / debug; UI overrides display text) ───────
	var clusterPages, topLevelDirPages, repoWidePages int
	for _, p := range cappedPages {
		switch classifyPageType(p) {
		case LivingWikiPageTypeRepoWide:
			repoWidePages++
		case LivingWikiPageTypeArchitecture:
			clusterPages++
		default:
			topLevelDirPages++
		}
	}
	summary := buildPlanningSummary(modeStr, len(cappedPages), clusterPages, topLevelDirPages, repoWidePages, capSource, capValue, preCap)

	return &LivingWikiPlan{
		PlanSignature: planSig,
		Mode:          modeStr,
		ModeTooltip:   modeTooltip(modeStr),
		Summary:       summary,
		TotalPages:    len(cappedPages),
		PreCap:        preCap,
		CapSource:     capSource,
		CapValue:      capValue,
		Pages:         gqlPages,
	}, nil
}

// plannedPageToGQL converts a lworch.PlannedPage to its GraphQL representation.
// classifyPageType prefers the Kind field and falls back to TemplateID for
// legacy plans (see living_wiki_plan_helpers.go).
func plannedPageToGQL(p lworch.PlannedPage) *LivingWikiPlanPage {
	pageType := classifyPageType(p)
	required := p.Kind == lworch.PageKindRepoWide ||
		(p.Kind == lworch.PageKindUnknown && repoWideTemplateIDs[p.TemplateID])

	gqlPage := &LivingWikiPlanPage{
		ID:         p.ID,
		TemplateID: p.TemplateID,
		Title:      previewPageTitle(p),
		PageType:   pageType,
		Audience:   string(p.Audience),
		Required:   required,
	}

	// subsystem = cluster label or package path for non-repo-wide pages.
	if p.PackageInfo != nil && p.PackageInfo.Package != "" {
		pkg := p.PackageInfo.Package
		gqlPage.Subsystem = &pkg
	}

	return gqlPage
}

// previewPageTitle returns a human-readable preview title for a planned page.
// Titles are template-rendered at generation time; this function provides a
// deterministic preview label from the available pre-generation data.
//
// For architecture/top-level-dir pages the cluster or package label is used.
// For repo-wide pages a fixed display name is returned.
func previewPageTitle(p lworch.PlannedPage) string {
	if p.PackageInfo != nil && p.PackageInfo.Package != "" {
		return p.PackageInfo.Package
	}
	// Repo-wide pages: derive from TemplateID.
	switch p.TemplateID {
	case "api_reference":
		return "API Reference"
	case "system_overview":
		return "System Overview"
	case "glossary":
		return "Glossary"
	}
	// Fallback: use page ID as a last resort.
	return p.ID
}
