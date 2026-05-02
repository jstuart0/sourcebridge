// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// CA-146 Phase 0: unit tests for the shared plan-helper functions extracted
// into living_wiki_plan_helpers.go.

package graphql

import (
	"testing"

	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
)

// ─────────────────────────────────────────────────────────────────────────────
// applyPageCap tests
// ─────────────────────────────────────────────────────────────────────────────

func makePlannedPages(templateIDs ...string) []lworch.PlannedPage {
	pages := make([]lworch.PlannedPage, len(templateIDs))
	for i, tid := range templateIDs {
		pages[i] = lworch.PlannedPage{ID: "page-" + tid + "-" + string(rune('a'+i)), TemplateID: tid}
	}
	return pages
}

func TestApplyPageCap_FitsWithinCap(t *testing.T) {
	t.Parallel()
	// 3 repo-wide + 2 architecture = 5 pages; cap = 10 → no truncation.
	pages := makePlannedPages("api_reference", "system_overview", "glossary", "architecture", "architecture")
	out, capSource, capValue, _, preCap := applyPageCap(pages, 10, nil, false)
	if len(out) != 5 {
		t.Errorf("expected 5 pages, got %d", len(out))
	}
	if capSource != "none" {
		t.Errorf("expected capSource=none when fits within cap, got %q", capSource)
	}
	if capValue != 0 {
		t.Errorf("expected capValue=0 when capSource=none, got %d", capValue)
	}
	if preCap != 5 {
		t.Errorf("expected preCap=5, got %d", preCap)
	}
}

func TestApplyPageCap_TruncatesPastCap_RepoSetting(t *testing.T) {
	t.Parallel()
	// 3 repo-wide + 4 architecture = 7 pages; cap = 5 → truncate to 3 repo-wide + 2 arch.
	pages := makePlannedPages("api_reference", "system_overview", "glossary",
		"architecture", "architecture", "architecture", "architecture")
	out, capSource, capValue, _, preCap := applyPageCap(pages, 5, nil, false)
	if len(out) != 5 {
		t.Errorf("expected 5 pages after cap, got %d", len(out))
	}
	if capSource != "repo_setting" {
		t.Errorf("expected capSource=repo_setting, got %q", capSource)
	}
	if capValue != 5 {
		t.Errorf("expected capValue=5, got %d", capValue)
	}
	if preCap != 7 {
		t.Errorf("expected preCap=7, got %d", preCap)
	}
	// Repo-wide pages must all be present.
	repoWideCount := 0
	for _, p := range out {
		if repoWideTemplateIDs[p.TemplateID] {
			repoWideCount++
		}
	}
	if repoWideCount != 3 {
		t.Errorf("expected 3 repo-wide pages in output, got %d", repoWideCount)
	}
}

func TestApplyPageCap_TruncatesPastCap_PerRunOverride(t *testing.T) {
	t.Parallel()
	// maxPagesPerJob = 100 (loose), but override = 4 → override wins.
	pages := makePlannedPages("api_reference", "system_overview", "glossary",
		"architecture", "architecture", "top_level_dir")
	override := 4
	out, capSource, capValue, _, _ := applyPageCap(pages, 100, &override, false)
	if len(out) != 4 {
		t.Errorf("expected 4 pages after per-run override cap, got %d", len(out))
	}
	if capSource != "per_run_override" {
		t.Errorf("expected capSource=per_run_override, got %q", capSource)
	}
	if capValue != 4 {
		t.Errorf("expected capValue=4, got %d", capValue)
	}
}

func TestApplyPageCap_RetryExcludedOnlyBypassesCap(t *testing.T) {
	t.Parallel()
	// maxPagesPerJob = 1 (very tight), but excludedOnlyRetry bypasses cap entirely.
	pages := makePlannedPages("architecture", "architecture", "architecture")
	out, capSource, capValue, _, _ := applyPageCap(pages, 1, nil, true)
	if len(out) != 3 {
		t.Errorf("expected all 3 pages returned when excludedOnlyRetry=true, got %d", len(out))
	}
	if capSource != "none" {
		t.Errorf("expected capSource=none on excludedOnlyRetry path, got %q", capSource)
	}
	if capValue != 0 {
		t.Errorf("expected capValue=0 on excludedOnlyRetry path, got %d", capValue)
	}
}

func TestApplyPageCap_CapLessThanRepoWide_ClampsAllowToZero(t *testing.T) {
	t.Parallel()
	// Cap = 2, but there are 3 repo-wide pages: allowForRest clamps to 0.
	// Output must still contain all 3 repo-wide pages (repo-wide always retained).
	pages := makePlannedPages("api_reference", "system_overview", "glossary",
		"architecture", "architecture")
	out, capSource, capValue, _, _ := applyPageCap(pages, 2, nil, false)
	// allowForRest = 2 - 3 = -1 → clamped to 0. arch pages truncated entirely.
	// Output = 3 repo-wide (cap is not enforced against repo-wide pages themselves).
	if len(out) != 3 {
		t.Errorf("expected 3 pages (all repo-wide, no arch), got %d", len(out))
	}
	if capSource != "repo_setting" {
		t.Errorf("expected capSource=repo_setting, got %q", capSource)
	}
	if capValue != 2 {
		t.Errorf("expected capValue=2, got %d", capValue)
	}
}

func TestApplyPageCap_ExactFit(t *testing.T) {
	t.Parallel()
	// preCap == effectiveCap exactly → no truncation, capSource="none".
	pages := makePlannedPages("api_reference", "system_overview", "glossary", "architecture")
	out, capSource, capValue, _, _ := applyPageCap(pages, 4, nil, false)
	if len(out) != 4 {
		t.Errorf("expected 4 pages on exact fit, got %d", len(out))
	}
	if capSource != "none" {
		t.Errorf("expected capSource=none on exact fit, got %q", capSource)
	}
	if capValue != 0 {
		t.Errorf("expected capValue=0 on exact fit (capSource=none), got %d", capValue)
	}
}

func TestApplyPageCap_NoCap_ZeroMaxPages(t *testing.T) {
	t.Parallel()
	// maxPagesPerJob = 0 → no cap.
	pages := makePlannedPages("architecture", "architecture", "architecture")
	out, capSource, capValue, effectiveCap, _ := applyPageCap(pages, 0, nil, false)
	if len(out) != 3 {
		t.Errorf("expected all 3 pages, got %d", len(out))
	}
	if capSource != "none" {
		t.Errorf("expected capSource=none when maxPagesPerJob=0, got %q", capSource)
	}
	if capValue != 0 {
		t.Errorf("expected capValue=0, got %d", capValue)
	}
	if effectiveCap != 0 {
		t.Errorf("expected effectiveCap=0, got %d", effectiveCap)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// computePlanSignature tests
// ─────────────────────────────────────────────────────────────────────────────

func TestComputePlanSignature_StableAcrossCalls(t *testing.T) {
	t.Parallel()
	ids := []string{"page-arch-a", "page-glossary-b", "page-api-c"}
	sig1 := computePlanSignature(ids, "lw_detailed", 50)
	sig2 := computePlanSignature(ids, "lw_detailed", 50)
	if sig1 != sig2 {
		t.Errorf("signature not stable: %q vs %q", sig1, sig2)
	}
	if len(sig1) != 64 {
		t.Errorf("expected 64-char hex sha256, got len=%d: %q", len(sig1), sig1)
	}
}

func TestComputePlanSignature_DeterministicAcrossOrder(t *testing.T) {
	t.Parallel()
	ids1 := []string{"alpha", "beta", "gamma"}
	ids2 := []string{"gamma", "alpha", "beta"}
	sig1 := computePlanSignature(ids1, "lw_detailed", 10)
	sig2 := computePlanSignature(ids2, "lw_detailed", 10)
	if sig1 != sig2 {
		t.Errorf("signature varies by input order; want deterministic:\n  ids1=%v: %q\n  ids2=%v: %q",
			ids1, sig1, ids2, sig2)
	}
}

func TestComputePlanSignature_DiffersOnInputChange(t *testing.T) {
	t.Parallel()
	base := []string{"page-a", "page-b"}
	sig := computePlanSignature(base, "lw_detailed", 50)

	// Change page IDs.
	diffIDs := computePlanSignature([]string{"page-a", "page-c"}, "lw_detailed", 50)
	if sig == diffIDs {
		t.Error("signature did not change when page ID set changed")
	}

	// Change mode.
	diffMode := computePlanSignature(base, "lw_overview", 50)
	if sig == diffMode {
		t.Error("signature did not change when mode changed")
	}

	// Change effectiveCap.
	diffCap := computePlanSignature(base, "lw_detailed", 99)
	if sig == diffCap {
		t.Error("signature did not change when effectiveCap changed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// classifyPageType tests
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifyPageType_AllBranches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		templateID string
		want       string
	}{
		{"api_reference", pageTypeRepoWide},
		{"system_overview", pageTypeRepoWide},
		{"glossary", pageTypeRepoWide},
		{"architecture", pageTypeArchitecture},
		{"top_level_dir", pageTypeTopLevelDir},
		{"some_other_template", pageTypeTopLevelDir},
		{"", pageTypeTopLevelDir},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.templateID, func(t *testing.T) {
			t.Parallel()
			got := classifyPageType(tc.templateID)
			if got != tc.want {
				t.Errorf("classifyPageType(%q) = %q, want %q", tc.templateID, got, tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// modeTooltip tests
// ─────────────────────────────────────────────────────────────────────────────

func TestModeTooltip_BothModes(t *testing.T) {
	t.Parallel()
	if tip := modeTooltip(GenerationModeLWDetailed); tip == "" {
		t.Errorf("expected non-empty tooltip for %s", GenerationModeLWDetailed)
	}
	if tip := modeTooltip(GenerationModeLWOverview); tip == "" {
		t.Errorf("expected non-empty tooltip for %s", GenerationModeLWOverview)
	}
	if tip := modeTooltip("unknown_mode"); tip != "" {
		t.Errorf("expected empty tooltip for unknown mode, got %q", tip)
	}
	if tip := modeTooltip(""); tip != "" {
		t.Errorf("expected empty tooltip for empty mode, got %q", tip)
	}
}
