// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package orchestrator — staleness.go implements the A1.P7 stale-detection
// and banner-attachment pipeline.
//
// # Flow
//
//  1. [Orchestrator.EvaluateStaleness] iterates all known page manifests and
//     calls [manifest.EvaluateStaleConditions] for each, accumulating
//     [StalePageSignal] values for pages whose stale_when conditions fired.
//
//  2. [Orchestrator.AttachStaleBanners] takes those signals and, for each
//     affected page, prepends a [ast.BlockKindStaleBanner] block to the page's
//     AST. It writes to the proposed_ast when a PR is open; otherwise to the
//     canonical_ast.
//
//  3. [Orchestrator.RefreshPage] triggers ad-hoc single-page regen, bypassing
//     the per-push LLM budget. This is the back-end for the "Refresh from source"
//     button in the SourceBridge UI.
//
// The banner block carries a [ast.StaleBannerContent] typed attribute so the
// next regen can detect and cleanly remove it (or replace it with a new one
// if the page is still stale after regen).
package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/livingwiki/ast"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/manifest"
	"github.com/sourcebridge/sourcebridge/internal/llm/modeltier"
)

// StalePageSignal records one page that has become stale due to a code diff.
// It maps directly to the output of [manifest.EvaluateStaleConditions] but
// also carries contextual information the banner builder needs.
type StalePageSignal struct {
	// PageID is the page that has become stale.
	PageID string

	// TriggeringCommit is the source commit SHA that caused staleness.
	TriggeringCommit string

	// TriggeringSymbols is the set of symbol names that triggered the condition.
	TriggeringSymbols []string

	// ConditionKind is "signature_change_in" or "new_caller_added_to".
	ConditionKind string

	// NextRegenWindow is a human-readable description of the next scheduled
	// regen window, if known. Empty when not available.
	NextRegenWindow string
}

// StaleBannerConfig carries the configuration the banner builder needs to
// construct actionable banners.
type StaleBannerConfig struct {
	// UIBaseURL is the root URL of the SourceBridge UI, used to build the
	// "Refresh from source" link. Example: "https://app.sourcebridge.ai".
	// When empty, the link is omitted from the banner.
	UIBaseURL string
}

// refreshURL builds the SourceBridge UI link for ad-hoc regen of a page.
func refreshURL(uiBase, repoID, pageID string) string {
	if uiBase == "" {
		return ""
	}
	return fmt.Sprintf("%s/repos/%s/pages/%s/refresh", uiBase, repoID, pageID)
}

// EvaluateStaleness checks all pages in manifests against changed pairs and
// returns one [StalePageSignal] per condition that fired. Pages that are
// already in the regen set (i.e. being fully regenerated this run) do not
// need a stale banner; the caller should filter them out before calling this.
//
// changed is the diff result from the current push. headSHA is the source
// commit that triggered the push; it is stored in each signal's TriggeringCommit.
// nextRegen is an optional human-readable description of the next scheduled
// regen window.
func (o *Orchestrator) EvaluateStaleness(
	_ context.Context,
	manifests []manifest.DependencyManifest,
	changed []manifest.ChangedPair,
	headSHA string,
	nextRegen string,
) ([]StalePageSignal, error) {
	if len(manifests) == 0 || len(changed) == 0 {
		return nil, nil
	}

	var signals []StalePageSignal

	for _, m := range manifests {
		staleSignals := manifest.EvaluateStaleConditions(m, changed)
		for _, s := range staleSignals {
			signals = append(signals, StalePageSignal{
				PageID:            s.PageID,
				TriggeringCommit:  headSHA,
				TriggeringSymbols: s.TriggeringSymbols,
				ConditionKind:     s.Kind,
				NextRegenWindow:   nextRegen,
			})
		}
	}

	return signals, nil
}

// AttachStaleBanners prepends a [ast.BlockKindStaleBanner] block to each page
// named in signals. Writes to the proposed_ast when prID is non-empty (a PR is
// open); otherwise writes to the canonical_ast.
//
// If a page already has a stale banner at index 0, the existing banner is
// replaced so repeated calls do not accumulate duplicate banners.
//
// The banner block is marked [ast.OwnerGenerated] so the next full regen can
// overwrite it cleanly.
func (o *Orchestrator) AttachStaleBanners(
	ctx context.Context,
	repoID string,
	prID string,
	signals []StalePageSignal,
	bannerCfg StaleBannerConfig,
) error {
	if len(signals) == 0 {
		return nil
	}

	for _, sig := range signals {
		if err := o.attachBannerToPage(ctx, repoID, prID, sig, bannerCfg); err != nil {
			// Non-fatal: log and continue so other pages get their banners.
			// Callers that need strict error handling can wrap the Orchestrator.
			_ = err
		}
	}
	return nil
}

// attachBannerToPage loads the target page, prepends (or replaces) the stale
// banner block, and stores the updated page.
func (o *Orchestrator) attachBannerToPage(
	ctx context.Context,
	repoID string,
	prID string,
	sig StalePageSignal,
	bannerCfg StaleBannerConfig,
) error {
	var (
		page ast.Page
		ok   bool
		err  error
	)

	if prID != "" {
		page, ok, err = o.store.GetProposed(ctx, repoID, prID, sig.PageID)
	} else {
		page, ok, err = o.store.GetCanonical(ctx, repoID, sig.PageID)
	}
	if err != nil {
		return fmt.Errorf("attachBannerToPage: loading page %q: %w", sig.PageID, err)
	}
	if !ok {
		// Page not found — nothing to attach to.
		return nil
	}

	banner := buildStaleBannerBlock(sig, bannerCfg, repoID)

	// Remove any existing stale banner at the head of the block list.
	blocks := removeStaleBannerHead(page.Blocks)

	// Prepend the new banner.
	page.Blocks = append([]ast.Block{banner}, blocks...)

	if prID != "" {
		return o.store.SetProposed(ctx, repoID, prID, page)
	}
	return o.store.SetCanonical(ctx, repoID, page)
}

// buildStaleBannerBlock constructs a [ast.Block] of [ast.BlockKindStaleBanner]
// from a [StalePageSignal].
func buildStaleBannerBlock(sig StalePageSignal, cfg StaleBannerConfig, repoID string) ast.Block {
	return ast.Block{
		ID:   staleBannerBlockID(sig.PageID),
		Kind: ast.BlockKindStaleBanner,
		Content: ast.BlockContent{
			StaleBanner: &ast.StaleBannerContent{
				TriggeringCommit:  sig.TriggeringCommit,
				TriggeringSymbols: sig.TriggeringSymbols,
				ConditionKind:     sig.ConditionKind,
				RefreshURL:        refreshURL(cfg.UIBaseURL, repoID, sig.PageID),
				NextRegenWindow:   sig.NextRegenWindow,
			},
		},
		Owner: ast.OwnerGenerated,
		LastChange: ast.BlockChange{
			SHA:       sig.TriggeringCommit,
			Timestamp: time.Now(),
			Source:    "sourcebridge",
		},
	}
}

// staleBannerBlockID returns a deterministic block ID for the stale banner of
// a page. Using a stable ID means round-trip parse → store → render preserves
// the banner's block-ID comment, and the next regen can locate and remove it
// via exact-ID match in the reconciliation algorithm.
func staleBannerBlockID(pageID string) ast.BlockID {
	return ast.GenerateBlockID(pageID, "__stale_banner__", ast.BlockKindStaleBanner, 0)
}

// removeStaleBannerHead removes the first block if it is a stale banner, so
// that subsequent calls to AttachStaleBanners do not accumulate duplicates.
func removeStaleBannerHead(blocks []ast.Block) []ast.Block {
	if len(blocks) > 0 && blocks[0].Kind == ast.BlockKindStaleBanner {
		return blocks[1:]
	}
	return blocks
}

// RefreshPage triggers ad-hoc single-page regen for pageID, bypassing the
// per-push LLM budget cap. This is the server-side implementation of the
// "Refresh from source" button in the SourceBridge UI.
//
// The caller must provide a [PlannedPage] for the target page (the UI endpoint
// constructs this from the stored manifest). The regenerated page is stored in
// proposed_ast when prID is non-empty, or canonical_ast otherwise.
//
// Any existing stale banner on the page is removed after successful regen
// because the page content is now current.
func (o *Orchestrator) RefreshPage(
	ctx context.Context,
	repoID string,
	prID string,
	planned PlannedPage,
) (ast.Page, error) {
	cfg := o.cfg
	cfg.RepoID = repoID

	// Staleness refresh: no Snapshot available at this call site; use
	// TierFrontier to preserve pre-CA-150 gate behavior for page refreshes.
	// CA-150 Phase 4 covers cold-start + on-demand only.
	outcome, err := o.generateOnePage(ctx, cfg, planned, modeltier.TierFrontier, nil)
	if err != nil {
		return ast.Page{}, fmt.Errorf("RefreshPage(%q): generate: %w", planned.ID, err)
	}
	if outcome.excluded != nil {
		return ast.Page{}, fmt.Errorf("RefreshPage(%q): excluded after quality gates", planned.ID)
	}

	// Remove any stale banner that was prepended before this regen.
	outcome.page.Blocks = removeStaleBannerHead(outcome.page.Blocks)

	if prID != "" {
		if err := o.store.SetProposed(ctx, repoID, prID, outcome.page); err != nil {
			return ast.Page{}, fmt.Errorf("RefreshPage(%q): storing proposed: %w", planned.ID, err)
		}
	} else {
		if err := o.store.SetCanonical(ctx, repoID, outcome.page); err != nil {
			return ast.Page{}, fmt.Errorf("RefreshPage(%q): storing canonical: %w", planned.ID, err)
		}
	}

	return outcome.page, nil
}
