// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// Phase 4b tests: mode-flag toggles, build-mode routing, and GQL mapping.

package graphql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// modeFlagsResolver builds a mutationResolver backed by an in-memory store
// pre-seeded with the given mode flags.
func modeFlagsResolver(repoID string, overview, detailed bool) (*mutationResolver, *livingwiki.RepoSettingsMemStore) {
	store := livingwiki.NewRepoSettingsMemStore()
	_ = store.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		LivingWikiOverviewEnabled: overview,
		LivingWikiDetailedEnabled: detailed,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})
	return &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: store}}, store
}

// ─────────────────────────────────────────────────────────────────────────────
// SetLivingWikiModeFlags
// ─────────────────────────────────────────────────────────────────────────────

func TestSetLivingWikiModeFlagsMutationPersists(t *testing.T) {
	r, store := modeFlagsResolver("repo-1", true, false)

	got, err := r.SetLivingWikiModeFlags(context.Background(), "repo-1", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil settings")
	}
	if got.LivingWikiOverviewEnabled != false {
		t.Errorf("overviewEnabled: got %v, want false", got.LivingWikiOverviewEnabled)
	}
	if got.LivingWikiDetailedEnabled != true {
		t.Errorf("detailedEnabled: got %v, want true", got.LivingWikiDetailedEnabled)
	}

	// Verify persisted to store.
	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-1")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if saved.LivingWikiOverviewEnabled != false || saved.LivingWikiDetailedEnabled != true {
		t.Errorf("persisted flags wrong: overview=%v detailed=%v", saved.LivingWikiOverviewEnabled, saved.LivingWikiDetailedEnabled)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RetryLivingWikiJob: mode routing
// ─────────────────────────────────────────────────────────────────────────────

func TestRetryLivingWikiJobBothModesOffReturnsError(t *testing.T) {
	r, _ := modeFlagsResolver("repo-2", false, false)

	_, err := r.RetryLivingWikiJob(context.Background(), "repo-2", nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error when both modes are off")
	}
}

func TestRetryLivingWikiJobAllEnabledModeWithBothOff(t *testing.T) {
	r, _ := modeFlagsResolver("repo-2b", false, false)
	m := LivingWikiBuildModeAllEnabled
	_, err := r.RetryLivingWikiJob(context.Background(), "repo-2b", nil, &m, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error when ALL_ENABLED but both modes are off")
	}
}

func TestRetryLivingWikiJobOverviewModeOverride(t *testing.T) {
	// Both modes off, but explicit OVERVIEW override provided.
	r, _ := modeFlagsResolver("repo-3", false, false)
	mode := LivingWikiBuildModeOverview
	// No orchestrator configured, so it will gate at the orchestrator nil check.
	// We just verify no "both modes disabled" error is returned.
	_, err := r.RetryLivingWikiJob(context.Background(), "repo-3", nil, &mode, nil, nil, nil)
	// Error may be nil (gated by no-orchestrator notice path) or a non-mode error.
	// The key assertion: the error is NOT "LIVING_WIKI_BOTH_MODES_DISABLED".
	if err != nil {
		if err.Error() == "Both Overview and Detailed modes are disabled. Enable at least one mode first." {
			t.Errorf("OVERVIEW mode override should bypass both-modes-off check, got: %v", err)
		}
	}
}

func TestRetryLivingWikiJobDetailedModeOverride(t *testing.T) {
	r, _ := modeFlagsResolver("repo-4", false, false)
	mode := LivingWikiBuildModeDetailed
	_, err := r.RetryLivingWikiJob(context.Background(), "repo-4", nil, &mode, nil, nil, nil)
	if err != nil {
		if err.Error() == "Both Overview and Detailed modes are disabled. Enable at least one mode first." {
			t.Errorf("DETAILED mode override should bypass both-modes-off check, got: %v", err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TriggerLivingWikiColdStartAllEnabled
// ─────────────────────────────────────────────────────────────────────────────

func TestTriggerAllEnabledBothOn(t *testing.T) {
	r, _ := modeFlagsResolver("repo-5", true, true)
	// No orchestrator: jobs are gated by notice path, not error.
	results, err := r.TriggerLivingWikiColdStartAllEnabled(context.Background(), "repo-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With no sinks beyond git_repo but no orchestrator, EnableLivingWikiForRepo
	// succeeds with a notice. We expect 2 results (one per mode).
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestTriggerAllEnabledOverviewOnly(t *testing.T) {
	r, _ := modeFlagsResolver("repo-6", true, false)
	results, err := r.TriggerLivingWikiColdStartAllEnabled(context.Background(), "repo-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (overview only), got %d", len(results))
	}
}

func TestTriggerAllEnabledBothOff(t *testing.T) {
	r, _ := modeFlagsResolver("repo-7", false, false)
	results, err := r.TriggerLivingWikiColdStartAllEnabled(context.Background(), "repo-7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results when both modes off, got %d", len(results))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// defaultRepoSettings and mapRepoLivingWikiSettings
// ─────────────────────────────────────────────────────────────────────────────

func TestRepoSettingsDefaultsOverviewTrue(t *testing.T) {
	s := defaultRepoSettings("repo-8")
	if !s.LivingWikiOverviewEnabled {
		t.Error("new repo should default to overview=true (LD-13)")
	}
	if s.LivingWikiDetailedEnabled {
		t.Error("new repo should default to detailed=false (LD-13)")
	}
}

func TestMapRepoLivingWikiSettingsExposesModeFlagsInGQL(t *testing.T) {
	domain := &livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    "repo-9",
		Enabled:                   true,
		LivingWikiOverviewEnabled: true,
		LivingWikiDetailedEnabled: false,
	}
	gql := mapRepoLivingWikiSettings(domain)
	if gql == nil {
		t.Fatal("expected non-nil GQL settings")
	}
	if !gql.LivingWikiOverviewEnabled {
		t.Error("GQL overviewEnabled should be true")
	}
	if gql.LivingWikiDetailedEnabled {
		t.Error("GQL detailedEnabled should be false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-138: deriveLivingWikiJobMode + override-flag plumbing fix.
//
// These tests verify the fix for the latent override-flag plumbing bug
// surfaced by codex r1 against the prior CA-138 plan. The bug:
// EnableLivingWikiForRepo built a fresh settings record from scratch,
// ignoring the input's LivingWikiOverviewEnabled / LivingWikiDetailedEnabled
// override pointers AND wiping any prior persisted mode flags. Effect:
// triggerLivingWikiColdStart(mode: OVERVIEW) etc. did not actually drive
// distinct cold-start jobs.
//
// The CA-138 fix: load existing settings, persist canonical row WITHOUT
// applying the override pointers, then derive jobMode from a job-local
// merged copy. The override pointers are TRANSIENT — they affect job
// mode only, never the persisted row. setLivingWikiModeFlags remains
// the source of truth for persisted mode flags.
// ─────────────────────────────────────────────────────────────────────────────

func TestDeriveLivingWikiJobMode_OverviewOnly_LWOverview(t *testing.T) {
	got := deriveLivingWikiJobMode(livingwiki.RepositoryLivingWikiSettings{
		LivingWikiOverviewEnabled: true,
		LivingWikiDetailedEnabled: false,
	})
	if got != GenerationModeLWOverview {
		t.Errorf("overview-only: got %q, want %q", got, GenerationModeLWOverview)
	}
}

func TestDeriveLivingWikiJobMode_DetailedOnly_LWDetailed(t *testing.T) {
	got := deriveLivingWikiJobMode(livingwiki.RepositoryLivingWikiSettings{
		LivingWikiOverviewEnabled: false,
		LivingWikiDetailedEnabled: true,
	})
	if got != GenerationModeLWDetailed {
		t.Errorf("detailed-only: got %q, want %q", got, GenerationModeLWDetailed)
	}
}

func TestDeriveLivingWikiJobMode_BothEnabled_LWDetailed(t *testing.T) {
	// "Both enabled" is the upstream-handled case (TriggerLivingWikiColdStartAllEnabled
	// calls EnableLivingWikiForRepo twice with distinct overrides). When the
	// EFFECTIVE settings reach the helper with both flags on, we use lw_detailed
	// as the fallback — but that's never the in-flight case for a single job.
	got := deriveLivingWikiJobMode(livingwiki.RepositoryLivingWikiSettings{
		LivingWikiOverviewEnabled: true,
		LivingWikiDetailedEnabled: true,
	})
	if got != GenerationModeLWDetailed {
		t.Errorf("both-enabled: got %q, want %q", got, GenerationModeLWDetailed)
	}
}

func TestDeriveLivingWikiJobMode_BothDisabled_LWDetailed(t *testing.T) {
	got := deriveLivingWikiJobMode(livingwiki.RepositoryLivingWikiSettings{})
	if got != GenerationModeLWDetailed {
		t.Errorf("both-disabled: got %q, want %q", got, GenerationModeLWDetailed)
	}
}

// modeFlagsResolverWithSinks returns a mutationResolver pre-seeded with
// the given mode flags AND a configured git_repo sink so EnableLivingWikiForRepo
// can proceed past sink validation.
func modeFlagsResolverWithSinks(repoID string, overview, detailed bool) (*mutationResolver, *livingwiki.RepoSettingsMemStore) {
	store := livingwiki.NewRepoSettingsMemStore()
	_ = store.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    repoID,
		Enabled:                   true,
		LivingWikiOverviewEnabled: overview,
		LivingWikiDetailedEnabled: detailed,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})
	return &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: store}}, store
}

// TestEnableLivingWikiForRepo_OverviewOverride_DoesNotPersistFlags is the
// regression guard for the codex r1 High 2 finding. With the transient-
// overrides pattern, calling EnableLivingWikiForRepo with an OVERVIEW
// override on a repo whose persisted flags are both-on must:
//   - persist the row WITHOUT mutating the existing mode flags
//   - return successfully
//
// The job mode itself is verified by the deriveLivingWikiJobMode tests above.
func TestEnableLivingWikiForRepo_OverviewOverride_DoesNotPersistFlags(t *testing.T) {
	r, store := modeFlagsResolverWithSinks("repo-override-1", true, true)

	t_ := true
	f_ := false
	input := EnableLivingWikiForRepoInput{
		RepositoryID:              "repo-override-1",
		Mode:                      RepoWikiModePrReview,
		Sinks:                     []*RepoWikiSinkInput{{Kind: RepoWikiSinkKindGitRepo}},
		LivingWikiOverviewEnabled: &t_,
		LivingWikiDetailedEnabled: &f_,
	}
	if _, err := r.EnableLivingWikiForRepo(context.Background(), input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-override-1")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if !saved.LivingWikiOverviewEnabled {
		t.Error("transient overview-override must NOT clear the persisted overviewEnabled flag")
	}
	if !saved.LivingWikiDetailedEnabled {
		t.Error("transient overview-override must NOT clear the persisted detailedEnabled flag")
	}
}

// TestEnableLivingWikiForRepo_DetailedOverride_DoesNotPersistFlags mirrors
// the above for DETAILED override.
func TestEnableLivingWikiForRepo_DetailedOverride_DoesNotPersistFlags(t *testing.T) {
	r, store := modeFlagsResolverWithSinks("repo-override-2", true, true)

	t_ := true
	f_ := false
	input := EnableLivingWikiForRepoInput{
		RepositoryID:              "repo-override-2",
		Mode:                      RepoWikiModePrReview,
		Sinks:                     []*RepoWikiSinkInput{{Kind: RepoWikiSinkKindGitRepo}},
		LivingWikiOverviewEnabled: &f_,
		LivingWikiDetailedEnabled: &t_,
	}
	if _, err := r.EnableLivingWikiForRepo(context.Background(), input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-override-2")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if !saved.LivingWikiOverviewEnabled {
		t.Error("transient detailed-override must NOT clear the persisted overviewEnabled flag")
	}
	if !saved.LivingWikiDetailedEnabled {
		t.Error("transient detailed-override must NOT clear the persisted detailedEnabled flag")
	}
}

// TestEnableLivingWikiForRepo_NoOverride_PreservesPersistedFlags is a
// regression guard against the secondary form of the bug: the prior
// from-scratch struct build wiped the persisted mode flags on every UI
// save. With the load-and-merge pattern, a UI save with no override
// pointers must preserve whatever setLivingWikiModeFlags had previously
// persisted.
func TestEnableLivingWikiForRepo_NoOverride_PreservesPersistedFlags(t *testing.T) {
	// Seed: overview=true, detailed=false (from a hypothetical setLivingWikiModeFlags call).
	r, store := modeFlagsResolverWithSinks("repo-override-3", true, false)

	// UI-save-shaped call — no override pointers.
	input := EnableLivingWikiForRepoInput{
		RepositoryID: "repo-override-3",
		Mode:         RepoWikiModePrReview,
		Sinks:        []*RepoWikiSinkInput{{Kind: RepoWikiSinkKindGitRepo}},
	}
	if _, err := r.EnableLivingWikiForRepo(context.Background(), input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-override-3")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if !saved.LivingWikiOverviewEnabled {
		t.Error("UI save with no overrides must preserve persisted overviewEnabled (was true, now false)")
	}
	if saved.LivingWikiDetailedEnabled {
		t.Errorf("UI save with no overrides must preserve persisted detailedEnabled (was false, now %v)", saved.LivingWikiDetailedEnabled)
	}
}

// TestEnableLivingWikiForRepo_ReEnable_ClearsDisabledAt verifies the
// codex r1 Medium 1 finding: the fix MUST clear DisabledAt on re-enable
// to match UpdateRepositoryLivingWikiSettings semantics.
func TestEnableLivingWikiForRepo_ReEnable_ClearsDisabledAt(t *testing.T) {
	store := livingwiki.NewRepoSettingsMemStore()
	pastTime := time.Now().Add(-1 * time.Hour)
	_ = store.SetRepoSettings(context.Background(), livingwiki.RepositoryLivingWikiSettings{
		TenantID:   "default",
		RepoID:     "repo-redisable",
		Enabled:    false,
		DisabledAt: &pastTime,
		Sinks:      []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	})
	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: store}}

	input := EnableLivingWikiForRepoInput{
		RepositoryID: "repo-redisable",
		Mode:         RepoWikiModePrReview,
		Sinks:        []*RepoWikiSinkInput{{Kind: RepoWikiSinkKindGitRepo}},
	}
	if _, err := r.EnableLivingWikiForRepo(context.Background(), input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-redisable")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if !saved.Enabled {
		t.Error("re-enable should set Enabled=true")
	}
	if saved.DisabledAt != nil {
		t.Errorf("re-enable must clear DisabledAt; still set to %v", saved.DisabledAt)
	}
}

// TestEnableLivingWikiForRepo_NewRepo_BuildsDefaults verifies the
// load-and-merge pattern doesn't fail on a repo that has no prior
// settings row (existing == nil case). All seed defaults must apply,
// INCLUDING the mode flags (overview=true, detailed=false per LD-13)
// — without those, the UI would show "No build modes enabled" right
// after the user clicks Enable for a brand-new repo. (Codex r2
// Medium finding.)
func TestEnableLivingWikiForRepo_NewRepo_BuildsDefaults(t *testing.T) {
	store := livingwiki.NewRepoSettingsMemStore()
	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: store}}

	input := EnableLivingWikiForRepoInput{
		RepositoryID: "repo-new",
		Mode:         RepoWikiModePrReview,
		Sinks:        []*RepoWikiSinkInput{{Kind: RepoWikiSinkKindGitRepo}},
	}
	if _, err := r.EnableLivingWikiForRepo(context.Background(), input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-new")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if !saved.Enabled {
		t.Error("expected Enabled=true on new repo")
	}
	if saved.StaleWhenStrategy != livingwiki.StaleStrategyDirect {
		t.Errorf("expected StaleStrategy=DIRECT default, got %q", saved.StaleWhenStrategy)
	}
	if saved.MaxPagesPerJob != 500 {
		t.Errorf("expected MaxPagesPerJob=500 default (CA-146: raised from 50), got %d", saved.MaxPagesPerJob)
	}
	// Codex r2 Medium: first-time enable must seed mode-flag defaults.
	if !saved.LivingWikiOverviewEnabled {
		t.Error("expected LivingWikiOverviewEnabled=true on new repo (LD-13 default)")
	}
	if saved.LivingWikiDetailedEnabled {
		t.Error("expected LivingWikiDetailedEnabled=false on new repo (LD-13 default)")
	}
}

// TestTriggerLivingWikiColdStartAllEnabled_PreservesPersistedFlags
// verifies the trigger-all path's persistence invariant. The two
// delegated EnableLivingWikiForRepo calls both pass transient overrides
// (Overview-only then Detailed-only) but neither must touch the
// persisted row's mode flags — the row stays "both on" throughout.
// (Codex r2 Low 1 — full trigger-all call-site contract.)
func TestTriggerLivingWikiColdStartAllEnabled_PreservesPersistedFlags(t *testing.T) {
	r, store := modeFlagsResolverWithSinks("repo-trigger-all", true, true)

	if _, err := r.TriggerLivingWikiColdStartAllEnabled(context.Background(), "repo-trigger-all"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-trigger-all")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if !saved.LivingWikiOverviewEnabled {
		t.Error("triggerAll must NOT clear persisted overviewEnabled")
	}
	if !saved.LivingWikiDetailedEnabled {
		t.Error("triggerAll must NOT clear persisted detailedEnabled")
	}
}

// TestRetryLivingWikiJob_OverviewOverride_PreservesPersistedFlags is
// the explicit retry-path persistence assertion. Calling retry with an
// OVERVIEW mode override sets a transient pointer in the input — the
// persisted row's flags must NOT change as a result. (Codex r2 Low 1
// — full retry call-site contract.)
func TestRetryLivingWikiJob_OverviewOverride_PreservesPersistedFlags(t *testing.T) {
	// Seed: overview=false, detailed=true. Retry with mode=OVERVIEW
	// passes transient overrides; persisted flags must survive.
	r, store := modeFlagsResolverWithSinks("repo-retry-1", false, true)

	mode := LivingWikiBuildModeOverview
	if _, err := r.RetryLivingWikiJob(context.Background(), "repo-retry-1", nil, &mode, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-retry-1")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if saved.LivingWikiOverviewEnabled {
		t.Error("retry(OVERVIEW) must NOT mutate persisted overviewEnabled (was false, now true)")
	}
	if !saved.LivingWikiDetailedEnabled {
		t.Error("retry(OVERVIEW) must NOT mutate persisted detailedEnabled (was true, now false)")
	}
}

// TestRetryLivingWikiJob_DetailedOverride_PreservesPersistedFlags
// mirrors the above for a DETAILED override.
func TestRetryLivingWikiJob_DetailedOverride_PreservesPersistedFlags(t *testing.T) {
	r, store := modeFlagsResolverWithSinks("repo-retry-2", true, false)

	mode := LivingWikiBuildModeDetailed
	if _, err := r.RetryLivingWikiJob(context.Background(), "repo-retry-2", nil, &mode, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, _ := store.GetRepoSettings(context.Background(), "default", "repo-retry-2")
	if saved == nil {
		t.Fatal("expected settings persisted")
	}
	if !saved.LivingWikiOverviewEnabled {
		t.Error("retry(DETAILED) must NOT mutate persisted overviewEnabled (was true, now false)")
	}
	if saved.LivingWikiDetailedEnabled {
		t.Error("retry(DETAILED) must NOT mutate persisted detailedEnabled (was false, now true)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CA-146: pageCountOverride validation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRetryLivingWikiJob_PageCountOverride_Zero_ReturnsError(t *testing.T) {
	r, _ := modeFlagsResolverWithSinks("pco-zero", true, false)
	v := 0
	_, err := r.RetryLivingWikiJob(context.Background(), "pco-zero", nil, nil, &v, nil, nil)
	if err == nil {
		t.Fatal("expected error for pageCountOverride=0")
	}
	if !isLivingWikiInvalidPageCountOverrideError(err) {
		t.Errorf("expected LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE error, got: %v", err)
	}
}

func TestRetryLivingWikiJob_PageCountOverride_Negative_ReturnsError(t *testing.T) {
	r, _ := modeFlagsResolverWithSinks("pco-neg", true, false)
	v := -5
	_, err := r.RetryLivingWikiJob(context.Background(), "pco-neg", nil, nil, &v, nil, nil)
	if err == nil {
		t.Fatal("expected error for pageCountOverride=-5")
	}
	if !isLivingWikiInvalidPageCountOverrideError(err) {
		t.Errorf("expected LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE error, got: %v", err)
	}
}

func TestRetryLivingWikiJob_PageCountOverride_TooLarge_ReturnsError(t *testing.T) {
	r, _ := modeFlagsResolverWithSinks("pco-big", true, false)
	v := 501
	_, err := r.RetryLivingWikiJob(context.Background(), "pco-big", nil, nil, &v, nil, nil)
	if err == nil {
		t.Fatal("expected error for pageCountOverride=501")
	}
	if !isLivingWikiInvalidPageCountOverrideError(err) {
		t.Errorf("expected LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE error, got: %v", err)
	}
}

func TestRetryLivingWikiJob_PageCountOverride_Nil_NoError(t *testing.T) {
	// nil override = no-override path; should not fail on validation.
	r, _ := modeFlagsResolverWithSinks("pco-nil", true, false)
	_, err := r.RetryLivingWikiJob(context.Background(), "pco-nil", nil, nil, nil, nil, nil)
	// Error may be non-nil due to orchestrator not configured, but NOT an
	// INVALID_PAGE_COUNT_OVERRIDE error.
	if err != nil && isLivingWikiInvalidPageCountOverrideError(err) {
		t.Errorf("nil override should not trigger validation error, got: %v", err)
	}
}

func TestRetryLivingWikiJob_PageCountOverride_Valid_NoError(t *testing.T) {
	// Valid override (1..500) — should not fail on validation.
	r, _ := modeFlagsResolverWithSinks("pco-valid", true, false)
	v := 42
	_, err := r.RetryLivingWikiJob(context.Background(), "pco-valid", nil, nil, &v, nil, nil)
	if err != nil && isLivingWikiInvalidPageCountOverrideError(err) {
		t.Errorf("valid override (42) should not trigger validation error, got: %v", err)
	}
}

// isLivingWikiInvalidPageCountOverrideError checks whether the error is a
// GraphQL error with the LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE extension code.
func isLivingWikiInvalidPageCountOverrideError(err error) bool {
	if err == nil {
		return false
	}
	// gqlerror.Error has a plain Extensions field (not a method).
	if e, ok := err.(*gqlerror.Error); ok {
		if code, _ := e.Extensions["code"].(string); code == "LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE" {
			return true
		}
	}
	// Also check plain string match in case of wrapping.
	return strings.Contains(err.Error(), "LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE")
}
