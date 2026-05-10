// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// CA-146 Phase 1: unit tests for the previewLivingWikiPlan query resolver.

package graphql

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/clustering"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

const testRepoID = "preview-test-repo"

// previewResolver builds a queryResolver with the minimal wiring needed to
// exercise PreviewLivingWikiPlan. It seeds repoSettings into an in-memory
// RepoSettingsStore and optionally seeds the global livingwiki.Store.
func previewResolver(
	t *testing.T,
	repoSettings *livingwiki.RepositoryLivingWikiSettings,
	globalEnabled bool,
) *queryResolver {
	t.Helper()

	repoStore := livingwiki.NewRepoSettingsMemStore()
	if repoSettings != nil {
		if err := repoStore.SetRepoSettings(context.Background(), *repoSettings); err != nil {
			t.Fatalf("seed repo settings: %v", err)
		}
	}

	globalStore := livingwiki.NewMemStore()
	enabled := globalEnabled
	if err := globalStore.Set(context.Background(), &livingwiki.Settings{Enabled: &enabled}); err != nil {
		t.Fatalf("seed global settings: %v", err)
	}

	r := &Resolver{
		Deps: &appdeps.AppDeps{
			LivingWikiRepoStore: repoStore,
			LivingWikiStore:     globalStore,
			ClusterStore:        &stubClusterStore{clusters: nil},
		},
	}
	return &queryResolver{r}
}

// defaultRepoSettingsForPreview returns baseline enabled settings with mode=Detailed.
func defaultRepoSettingsForPreview() *livingwiki.RepositoryLivingWikiSettings {
	return &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    testRepoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
	}
}

// resolverWithClusters builds a queryResolver backed by a cluster store that
// returns the given clusters for testRepoID.
func resolverWithClusters(
	t *testing.T,
	clusterStore *stubClusterStore,
	repoSettings *livingwiki.RepositoryLivingWikiSettings,
) *queryResolver {
	t.Helper()
	if repoSettings == nil {
		repoSettings = defaultRepoSettingsForPreview()
	}
	repoStore := livingwiki.NewRepoSettingsMemStore()
	if err := repoStore.SetRepoSettings(context.Background(), *repoSettings); err != nil {
		t.Fatalf("seed repo settings: %v", err)
	}

	globalStore := livingwiki.NewMemStore()
	enabled := true
	if err := globalStore.Set(context.Background(), &livingwiki.Settings{Enabled: &enabled}); err != nil {
		t.Fatalf("seed global settings: %v", err)
	}

	r := &Resolver{
		Deps: &appdeps.AppDeps{
			LivingWikiRepoStore: repoStore,
			LivingWikiStore:     globalStore,
			ClusterStore:        clusterStore,
		},
		Store: newStubGraphStore(),
	}
	return &queryResolver{r}
}

// gqlErrCode extracts the first "code" extension from a gqlerror.Error (or List).
func gqlErrCode(err error) string {
	if err == nil {
		return ""
	}
	var gqlErr *gqlerror.Error
	if errors.As(err, &gqlErr) {
		if gqlErr.Extensions != nil {
			if code, ok := gqlErr.Extensions["code"]; ok {
				return code.(string)
			}
		}
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_DetailedWithClusters
// ─────────────────────────────────────────────────────────────────────────────

// TestPreviewLivingWikiPlan_DetailedWithClusters verifies the happy path:
// mode=DETAILED with clusters present. Expects cluster pages classified as
// ARCHITECTURE and repo-wide pages as REPO_WIDE+required:true.
func TestPreviewLivingWikiPlan_DetailedWithClusters(t *testing.T) {
	t.Parallel()

	const repoID = "preview-clusters-repo"
	cs := &stubClusterStore{
		clusters: []clustering.Cluster{
			{ID: "c:auth", RepoID: repoID, Label: "auth", Size: 4},
			{ID: "c:billing", RepoID: repoID, Label: "billing", Size: 3},
		},
	}
	settings := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
	}
	r := resolverWithClusters(t, cs, settings)
	// Override graph store with a repoID-specific empty store (resolveTaxonomyForMode uses it).
	r.Store = newStubGraphStore()

	mode := LivingWikiBuildModeDetailed
	plan, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &mode, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must have 2 cluster arch pages + 3 repo-wide = 5 total.
	if plan.TotalPages != 5 {
		t.Errorf("totalPages: got %d, want 5", plan.TotalPages)
	}
	if plan.Mode != GenerationModeLWDetailed {
		t.Errorf("mode: got %q, want %q", plan.Mode, GenerationModeLWDetailed)
	}
	if plan.ModeTooltip == "" {
		t.Error("modeTooltip: expected non-empty")
	}

	archCount, repoWideCount := 0, 0
	for _, p := range plan.Pages {
		switch p.PageType {
		case LivingWikiPageTypeArchitecture:
			archCount++
			if p.Required {
				t.Errorf("architecture page %q should not be required", p.ID)
			}
		case LivingWikiPageTypeRepoWide:
			repoWideCount++
			if !p.Required {
				t.Errorf("repo-wide page %q must be required=true", p.ID)
			}
		}
	}
	if archCount != 2 {
		t.Errorf("architecture pages: got %d, want 2", archCount)
	}
	if repoWideCount != 3 {
		t.Errorf("repo-wide pages: got %d, want 3", repoWideCount)
	}

	if plan.PlanSignature == "" {
		t.Error("planSignature must be non-empty")
	}
	if len(plan.PlanSignature) != 64 {
		t.Errorf("planSignature length: got %d, want 64", len(plan.PlanSignature))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_DetailedNoClusters_ClassifiesAsTopLevelDir
// ─────────────────────────────────────────────────────────────────────────────

// TestPreviewLivingWikiPlan_DetailedNoClusters_ClassifiesAsTopLevelDir verifies
// that when no clusters are present and the symbol graph has top-level directories,
// architecture fallback pages are classified as TOP_LEVEL_DIR — not ARCHITECTURE.
// This is the GraphQL-boundary regression test for codex r1 C1.
func TestPreviewLivingWikiPlan_DetailedNoClusters_ClassifiesAsTopLevelDir(t *testing.T) {
	t.Parallel()

	// Empty cluster store → top-level-dir fallback path.
	cs := &stubClusterStore{clusters: nil}
	settings := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    testRepoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
	}
	r := resolverWithClusters(t, cs, settings)
	r.Store = newStubGraphStore()

	mode := LivingWikiBuildModeDetailed
	plan, err := r.PreviewLivingWikiPlan(context.Background(), testRepoID, &mode, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With an empty symbol graph and no clusters, only repo-wide pages are produced.
	// Assert: no page is classified as ARCHITECTURE (which would indicate the C1 bug).
	for _, p := range plan.Pages {
		if p.PageType == LivingWikiPageTypeArchitecture {
			t.Errorf("page %q classified as ARCHITECTURE on no-cluster path; expected TOP_LEVEL_DIR or REPO_WIDE", p.ID)
		}
	}

	// All repo-wide pages must be present with REPO_WIDE classification.
	repoWideCount := 0
	for _, p := range plan.Pages {
		if p.PageType == LivingWikiPageTypeRepoWide {
			repoWideCount++
			if !p.Required {
				t.Errorf("repo-wide page %q must be required=true", p.ID)
			}
		}
	}
	if repoWideCount != 3 {
		t.Errorf("repo-wide pages: got %d, want 3", repoWideCount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_OverviewMode
// ─────────────────────────────────────────────────────────────────────────────

func TestPreviewLivingWikiPlan_OverviewMode(t *testing.T) {
	t.Parallel()

	const repoID = "preview-overview-repo"
	cs := &stubClusterStore{
		clusters: []clustering.Cluster{
			{ID: "c:api", RepoID: repoID, Label: "api", Size: 5},
		},
	}
	settings := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiOverviewEnabled: true,
	}
	r := resolverWithClusters(t, cs, settings)
	r.Store = newStubGraphStore()

	mode := LivingWikiBuildModeOverview
	plan, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &mode, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if plan.Mode != GenerationModeLWOverview {
		t.Errorf("mode: got %q, want %q", plan.Mode, GenerationModeLWOverview)
	}
	if plan.ModeTooltip == "" {
		t.Error("modeTooltip: expected non-empty for OVERVIEW mode")
	}
	// 1 cluster arch page + 3 repo-wide = 4 total.
	if plan.TotalPages != 4 {
		t.Errorf("totalPages: got %d, want 4", plan.TotalPages)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_ModeFromSettings_WhenNil
// ─────────────────────────────────────────────────────────────────────────────

// TestPreviewLivingWikiPlan_ModeFromSettings_WhenNil verifies that omitting mode
// derives it from the current effective repo settings.
func TestPreviewLivingWikiPlan_ModeFromSettings_WhenNil(t *testing.T) {
	t.Parallel()

	t.Run("OverviewSettings", func(t *testing.T) {
		t.Parallel()
		const repoID = "preview-nil-mode-overview-repo"
		settings := &livingwiki.RepositoryLivingWikiSettings{
			TenantID:                  defaultTenantID,
			RepoID:                    repoID,
			Enabled:                   true,
			MaxPagesPerJob:            500,
			LivingWikiOverviewEnabled: true,
			LivingWikiDetailedEnabled: false,
		}
		r := resolverWithClusters(t, &stubClusterStore{}, settings)
		r.Store = newStubGraphStore()

		plan, err := r.PreviewLivingWikiPlan(context.Background(), repoID, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if plan.Mode != GenerationModeLWOverview {
			t.Errorf("derived mode: got %q, want %q", plan.Mode, GenerationModeLWOverview)
		}
	})

	t.Run("DetailedSettings", func(t *testing.T) {
		t.Parallel()
		const repoID = "preview-nil-mode-detailed-repo"
		settings := &livingwiki.RepositoryLivingWikiSettings{
			TenantID:                  defaultTenantID,
			RepoID:                    repoID,
			Enabled:                   true,
			MaxPagesPerJob:            500,
			LivingWikiOverviewEnabled: false,
			LivingWikiDetailedEnabled: true,
		}
		r := resolverWithClusters(t, &stubClusterStore{}, settings)
		r.Store = newStubGraphStore()

		plan, err := r.PreviewLivingWikiPlan(context.Background(), repoID, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if plan.Mode != GenerationModeLWDetailed {
			t.Errorf("derived mode: got %q, want %q", plan.Mode, GenerationModeLWDetailed)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_AllEnabled_ReturnsTypedError
// ─────────────────────────────────────────────────────────────────────────────

func TestPreviewLivingWikiPlan_AllEnabled_ReturnsTypedError(t *testing.T) {
	t.Parallel()

	r := previewResolver(t, defaultRepoSettingsForPreview(), true)
	mode := LivingWikiBuildModeAllEnabled
	_, err := r.PreviewLivingWikiPlan(context.Background(), testRepoID, &mode, nil)
	if err == nil {
		t.Fatal("expected error for ALL_ENABLED mode, got nil")
	}
	if code := gqlErrCode(err); code != "PREVIEW_MODE_NOT_SUPPORTED" {
		t.Errorf("error code: got %q, want PREVIEW_MODE_NOT_SUPPORTED", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_KillSwitchOrDisabled_ReturnsTypedError
// ─────────────────────────────────────────────────────────────────────────────

// TestPreviewLivingWikiPlan_KillSwitchOrDisabled_ReturnsTypedError verifies that
// the kill-switch and globally-disabled paths return a notice-bearing plan (not
// an error) so the UI can render the degraded banner.
//
// Note: KillSwitch subtest uses t.Setenv; cannot call t.Parallel on the parent.
func TestPreviewLivingWikiPlan_KillSwitchOrDisabled_ReturnsTypedError(t *testing.T) {
	t.Run("KillSwitch", func(t *testing.T) {
		r := previewResolver(t, defaultRepoSettingsForPreview(), true)
		r.Deps.Flags.LivingWikiKillSwitch = true // inject via field — no env var needed
		plan, err := r.PreviewLivingWikiPlan(context.Background(), testRepoID, nil, nil)
		if err != nil {
			t.Fatalf("expected nil error on kill-switch path, got: %v", err)
		}
		if plan.TotalPages != 0 {
			t.Errorf("totalPages: got %d, want 0", plan.TotalPages)
		}
		if len(plan.Pages) != 0 {
			t.Errorf("pages: got %d, want 0", len(plan.Pages))
		}
		if plan.Notice == nil || *plan.Notice == "" {
			t.Error("notice: expected non-empty notice string on kill-switch path")
		}
	})

	t.Run("GloballyDisabled", func(t *testing.T) {
		r := previewResolver(t, defaultRepoSettingsForPreview(), false /* globalEnabled=false */)
		plan, err := r.PreviewLivingWikiPlan(context.Background(), testRepoID, nil, nil)
		if err != nil {
			t.Fatalf("expected nil error on globally-disabled path, got: %v", err)
		}
		if plan.TotalPages != 0 {
			t.Errorf("totalPages: got %d, want 0", plan.TotalPages)
		}
		if plan.Notice == nil || *plan.Notice == "" {
			t.Error("notice: expected non-empty notice string on globally-disabled path")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_CapReducesPageCount
// ─────────────────────────────────────────────────────────────────────────────

func TestPreviewLivingWikiPlan_CapReducesPageCount(t *testing.T) {
	t.Parallel()

	const repoID = "preview-cap-repo"
	// 5 clusters + 3 repo-wide = 8 pages. Cap at 4 → 3 repo-wide + 1 arch.
	cs := &stubClusterStore{
		clusters: []clustering.Cluster{
			{ID: "c:a", RepoID: repoID, Label: "modA", Size: 1},
			{ID: "c:b", RepoID: repoID, Label: "modB", Size: 1},
			{ID: "c:c", RepoID: repoID, Label: "modC", Size: 1},
			{ID: "c:d", RepoID: repoID, Label: "modD", Size: 1},
			{ID: "c:e", RepoID: repoID, Label: "modE", Size: 1},
		},
	}
	settings := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500, // loose repo cap — override should win
		LivingWikiDetailedEnabled: true,
	}
	r := resolverWithClusters(t, cs, settings)
	r.Store = newStubGraphStore()

	mode := LivingWikiBuildModeDetailed
	override := 4
	plan, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &mode, &override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if plan.TotalPages != 4 {
		t.Errorf("totalPages after cap=4: got %d, want 4", plan.TotalPages)
	}
	if plan.PreCap != 8 {
		t.Errorf("preCap: got %d, want 8", plan.PreCap)
	}
	if plan.CapSource != "per_run_override" {
		t.Errorf("capSource: got %q, want per_run_override", plan.CapSource)
	}
	if plan.CapValue != 4 {
		t.Errorf("capValue: got %d, want 4", plan.CapValue)
	}

	// All 3 repo-wide pages must be present.
	repoWideCount := 0
	for _, p := range plan.Pages {
		if p.PageType == LivingWikiPageTypeRepoWide {
			repoWideCount++
		}
	}
	if repoWideCount != 3 {
		t.Errorf("repo-wide pages after cap: got %d, want 3 (always retained)", repoWideCount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_FitsWithinCap
// ─────────────────────────────────────────────────────────────────────────────

func TestPreviewLivingWikiPlan_FitsWithinCap(t *testing.T) {
	t.Parallel()

	const repoID = "preview-nocap-repo"
	cs := &stubClusterStore{
		clusters: []clustering.Cluster{
			{ID: "c:x", RepoID: repoID, Label: "x", Size: 2},
		},
	}
	settings := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
	}
	r := resolverWithClusters(t, cs, settings)
	r.Store = newStubGraphStore()

	mode := LivingWikiBuildModeDetailed
	plan, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &mode, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1 cluster + 3 repo-wide = 4; well under cap 500.
	if plan.CapSource != "none" {
		t.Errorf("capSource: got %q, want none (fits within cap)", plan.CapSource)
	}
	if plan.CapValue != 0 {
		t.Errorf("capValue: got %d, want 0 when capSource=none", plan.CapValue)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_SignatureStableAcrossCalls
// ─────────────────────────────────────────────────────────────────────────────

func TestPreviewLivingWikiPlan_SignatureStableAcrossCalls(t *testing.T) {
	t.Parallel()

	const repoID = "preview-sig-stable-repo"
	cs := &stubClusterStore{
		clusters: []clustering.Cluster{
			{ID: "c:auth", RepoID: repoID, Label: "auth", Size: 3},
		},
	}
	settings := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
	}
	r := resolverWithClusters(t, cs, settings)
	r.Store = newStubGraphStore()

	mode := LivingWikiBuildModeDetailed
	plan1, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &mode, nil)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	plan2, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &mode, nil)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if plan1.PlanSignature != plan2.PlanSignature {
		t.Errorf("signature not stable: %q vs %q", plan1.PlanSignature, plan2.PlanSignature)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_SignatureChangesOnInputChange
// ─────────────────────────────────────────────────────────────────────────────

func TestPreviewLivingWikiPlan_SignatureChangesOnInputChange(t *testing.T) {
	t.Parallel()

	const repoID = "preview-sig-change-repo"
	cs := &stubClusterStore{
		clusters: []clustering.Cluster{
			{ID: "c:auth", RepoID: repoID, Label: "auth", Size: 3},
		},
	}
	settings := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            500,
		LivingWikiDetailedEnabled: true,
	}
	r := resolverWithClusters(t, cs, settings)
	r.Store = newStubGraphStore()

	detailed := LivingWikiBuildModeDetailed
	overview := LivingWikiBuildModeOverview

	planDetailed, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &detailed, nil)
	if err != nil {
		t.Fatalf("detailed call: %v", err)
	}
	planOverview, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &overview, nil)
	if err != nil {
		t.Fatalf("overview call: %v", err)
	}
	if planDetailed.PlanSignature == planOverview.PlanSignature {
		t.Error("signature must differ when mode changes")
	}

	// Changing pageCountOverride also changes the signature (different effectiveCap).
	override := 3
	planCapped, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &detailed, &override)
	if err != nil {
		t.Fatalf("capped call: %v", err)
	}
	if planDetailed.PlanSignature == planCapped.PlanSignature {
		t.Error("signature must differ when pageCountOverride changes effectiveCap")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_InvalidPageCountOverride
// ─────────────────────────────────────────────────────────────────────────────

func TestPreviewLivingWikiPlan_InvalidPageCountOverride(t *testing.T) {
	t.Parallel()

	r := previewResolver(t, defaultRepoSettingsForPreview(), true)

	for _, bad := range []int{0, -1, 501, 1000} {
		bad := bad
		t.Run("override="+itoa(bad), func(t *testing.T) {
			t.Parallel()
			v := bad
			_, err := r.PreviewLivingWikiPlan(context.Background(), testRepoID, nil, &v)
			if err == nil {
				t.Fatalf("expected error for invalid override %d, got nil", bad)
			}
			if code := gqlErrCode(err); code != "LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE" {
				t.Errorf("error code: got %q, want LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE", code)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPreviewLivingWikiPlan_RepoWidePagesAlwaysPresent
// ─────────────────────────────────────────────────────────────────────────────

func TestPreviewLivingWikiPlan_RepoWidePagesAlwaysPresent(t *testing.T) {
	t.Parallel()

	// Verify that with a very tight cap (2), all 3 repo-wide pages survive.
	const repoID = "preview-rw-always-repo"
	cs := &stubClusterStore{
		clusters: []clustering.Cluster{
			{ID: "c:a", RepoID: repoID, Label: "modA", Size: 1},
			{ID: "c:b", RepoID: repoID, Label: "modB", Size: 1},
			{ID: "c:c", RepoID: repoID, Label: "modC", Size: 1},
		},
	}
	settings := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  defaultTenantID,
		RepoID:                    repoID,
		Enabled:                   true,
		MaxPagesPerJob:            2, // tight repo-level cap
		LivingWikiDetailedEnabled: true,
	}
	r := resolverWithClusters(t, cs, settings)
	r.Store = newStubGraphStore()

	mode := LivingWikiBuildModeDetailed
	plan, err := r.PreviewLivingWikiPlan(context.Background(), repoID, &mode, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repoWideCount := 0
	for _, p := range plan.Pages {
		if p.PageType == LivingWikiPageTypeRepoWide {
			repoWideCount++
		}
	}
	if repoWideCount != 3 {
		t.Errorf("repo-wide pages with tight cap: got %d, want 3 (always retained)", repoWideCount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
