// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// Phase 4b tests: mode-flag toggles, build-mode routing, and GQL mapping.

package graphql

import (
	"context"
	"testing"

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

	_, err := r.RetryLivingWikiJob(context.Background(), "repo-2", nil, nil)
	if err == nil {
		t.Fatal("expected error when both modes are off")
	}
}

func TestRetryLivingWikiJobAllEnabledModeWithBothOff(t *testing.T) {
	r, _ := modeFlagsResolver("repo-2b", false, false)
	m := LivingWikiBuildModeAllEnabled
	_, err := r.RetryLivingWikiJob(context.Background(), "repo-2b", nil, &m)
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
	_, err := r.RetryLivingWikiJob(context.Background(), "repo-3", nil, &mode)
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
	_, err := r.RetryLivingWikiJob(context.Background(), "repo-4", nil, &mode)
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
