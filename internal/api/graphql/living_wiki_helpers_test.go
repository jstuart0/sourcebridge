// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors
//
// Unit tests for the GQL-3 helpers extracted from EnableLivingWikiForRepo
// (Phase 1 Slice 3):
//   - validateEnableLivingWikiInput
//   - persistSettingsAndNotice
//   - persistLivingWikiSettings
//   - enqueueWikiJob (orchestrator-nil notice path; live dispatch avoided)

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/appdeps"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// ─────────────────────────────────────────────────────────────────────────────
// validateEnableLivingWikiInput
// ─────────────────────────────────────────────────────────────────────────────

func TestValidateEnableLivingWikiInput_NoSinks_ReturnsNoticeError(t *testing.T) {
	t.Parallel()
	input := EnableLivingWikiForRepoInput{
		RepositoryID: "repo-v1",
		Mode:         RepoWikiModePrReview,
		Sinks:        nil,
	}
	_, err := validateEnableLivingWikiInput(input)
	if err == nil {
		t.Fatal("expected error for empty sinks")
	}
	ne, ok := err.(*noticeOnlyError)
	if !ok {
		t.Fatalf("expected noticeOnlyError, got %T: %v", err, err)
	}
	if !strings.Contains(ne.notice, "sink") {
		t.Errorf("notice %q should mention 'sink'", ne.notice)
	}
}

func TestValidateEnableLivingWikiInput_SinkMissingIntegrationName_ReturnsNotice(t *testing.T) {
	t.Parallel()
	// confluence sink requires an integration name.
	kind := RepoWikiSinkKindConfluence
	input := EnableLivingWikiForRepoInput{
		RepositoryID: "repo-v2",
		Mode:         RepoWikiModePrReview,
		Sinks: []*RepoWikiSinkInput{
			{Kind: kind, IntegrationName: ""},
		},
	}
	_, err := validateEnableLivingWikiInput(input)
	if err == nil {
		t.Fatal("expected error for confluence sink with empty IntegrationName")
	}
	ne, ok := err.(*noticeOnlyError)
	if !ok {
		t.Fatalf("expected noticeOnlyError, got %T: %v", err, err)
	}
	if !strings.Contains(ne.notice, "integration name") {
		t.Errorf("notice %q should mention 'integration name'", ne.notice)
	}
}

func TestValidateEnableLivingWikiInput_ValidGitRepoSink_ReturnsNoError(t *testing.T) {
	t.Parallel()
	kind := RepoWikiSinkKindGitRepo
	aud := RepoWikiAudienceEngineer
	input := EnableLivingWikiForRepoInput{
		RepositoryID: "repo-v3",
		Mode:         RepoWikiModePrReview,
		Sinks: []*RepoWikiSinkInput{
			{Kind: kind, IntegrationName: "", Audience: aud},
		},
	}
	sinks, err := validateEnableLivingWikiInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sinks) != 1 {
		t.Errorf("expected 1 sink, got %d", len(sinks))
	}
}

func TestValidateEnableLivingWikiInput_PageCountOverrideZero_ReturnsError(t *testing.T) {
	t.Parallel()
	kind := RepoWikiSinkKindGitRepo
	aud := RepoWikiAudienceEngineer
	v := 0
	input := EnableLivingWikiForRepoInput{
		RepositoryID:      "repo-v4",
		Mode:              RepoWikiModePrReview,
		Sinks:             []*RepoWikiSinkInput{{Kind: kind, Audience: aud}},
		PageCountOverride: &v,
	}
	_, err := validateEnableLivingWikiInput(input)
	if err == nil {
		t.Fatal("expected error for pageCountOverride=0")
	}
	if gqlErrCode(err) != "LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE" {
		t.Errorf("expected LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE, got code=%q err=%v", gqlErrCode(err), err)
	}
}

func TestValidateEnableLivingWikiInput_PageCountOverride501_ReturnsError(t *testing.T) {
	t.Parallel()
	kind := RepoWikiSinkKindGitRepo
	aud := RepoWikiAudienceEngineer
	v := 501
	input := EnableLivingWikiForRepoInput{
		RepositoryID:      "repo-v5",
		Mode:              RepoWikiModePrReview,
		Sinks:             []*RepoWikiSinkInput{{Kind: kind, Audience: aud}},
		PageCountOverride: &v,
	}
	_, err := validateEnableLivingWikiInput(input)
	if err == nil {
		t.Fatal("expected error for pageCountOverride=501")
	}
	if gqlErrCode(err) != "LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE" {
		t.Errorf("expected LIVING_WIKI_INVALID_PAGE_COUNT_OVERRIDE, got code=%q err=%v", gqlErrCode(err), err)
	}
}

func TestValidateEnableLivingWikiInput_RetryExcludedAndSelectedPageIds_Conflict(t *testing.T) {
	t.Parallel()
	kind := RepoWikiSinkKindGitRepo
	aud := RepoWikiAudienceEngineer
	tr := true
	selected := []string{"page-1"}
	input := EnableLivingWikiForRepoInput{
		RepositoryID:      "repo-v6",
		Mode:              RepoWikiModePrReview,
		Sinks:             []*RepoWikiSinkInput{{Kind: kind, Audience: aud}},
		RetryExcludedOnly: &tr,
		SelectedPageIds:   selected,
	}
	_, err := validateEnableLivingWikiInput(input)
	if err == nil {
		t.Fatal("expected error for retryExcludedOnly+selectedPageIds conflict")
	}
	if gqlErrCode(err) != "LIVING_WIKI_INPUT_CONFLICT" {
		t.Errorf("expected LIVING_WIKI_INPUT_CONFLICT, got code=%q", gqlErrCode(err))
	}
}

func TestValidateEnableLivingWikiInput_SelectedWithoutSignature_ReturnsError(t *testing.T) {
	t.Parallel()
	kind := RepoWikiSinkKindGitRepo
	aud := RepoWikiAudienceEngineer
	selected := []string{}
	input := EnableLivingWikiForRepoInput{
		RepositoryID:    "repo-v7",
		Mode:            RepoWikiModePrReview,
		Sinks:           []*RepoWikiSinkInput{{Kind: kind, Audience: aud}},
		SelectedPageIds: selected,
		PlanSignature:   nil,
	}
	_, err := validateEnableLivingWikiInput(input)
	if err == nil {
		t.Fatal("expected PLAN_SIGNATURE_REQUIRED error")
	}
	if gqlErrCode(err) != "LIVING_WIKI_PLAN_SIGNATURE_REQUIRED" {
		t.Errorf("expected LIVING_WIKI_PLAN_SIGNATURE_REQUIRED, got code=%q", gqlErrCode(err))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// persistSettingsAndNotice — happy path + store error
// ─────────────────────────────────────────────────────────────────────────────

func TestPersistSettingsAndNotice_PersistsAndReturnsNotice(t *testing.T) {
	t.Parallel()
	store := livingwiki.NewRepoSettingsMemStore()
	settings := livingwiki.RepositoryLivingWikiSettings{
		TenantID: "default",
		RepoID:   "repo-p1",
		Enabled:  true,
		Sinks:    []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	}
	gql := mapRepoLivingWikiSettings(&settings)
	notice := "system paused"

	res, err := persistSettingsAndNotice(context.Background(), store, settings, gql, notice)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Notice == nil || *res.Notice != notice {
		t.Errorf("expected notice=%q, got %v", notice, res.Notice)
	}
	if res.Settings == nil {
		t.Error("expected non-nil settings in result")
	}

	// Verify row was actually persisted.
	stored, storeErr := store.GetRepoSettings(context.Background(), "default", "repo-p1")
	if storeErr != nil {
		t.Fatalf("store read error: %v", storeErr)
	}
	if stored == nil {
		t.Fatal("settings row not persisted")
	}
}

func TestPersistSettingsAndNotice_KillSwitch_ReturnsNotice(t *testing.T) {
	t.Parallel()
	store := livingwiki.NewRepoSettingsMemStore()
	settings := livingwiki.RepositoryLivingWikiSettings{
		TenantID: "default",
		RepoID:   "repo-p2",
		Enabled:  true,
	}
	gql := mapRepoLivingWikiSettings(&settings)
	notice := "Living wiki is paused via SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH."

	res, err := persistSettingsAndNotice(context.Background(), store, settings, gql, notice)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Notice == nil || *res.Notice != notice {
		t.Errorf("expected kill-switch notice, got %v", res.Notice)
	}
}

func TestPersistSettingsAndNotice_NilOrchestratorNotice(t *testing.T) {
	t.Parallel()
	store := livingwiki.NewRepoSettingsMemStore()
	settings := livingwiki.RepositoryLivingWikiSettings{
		TenantID: "default",
		RepoID:   "repo-p3",
		Enabled:  true,
	}
	gql := mapRepoLivingWikiSettings(&settings)
	notice := "Living Wiki orchestrator is not available."

	res, err := persistSettingsAndNotice(context.Background(), store, settings, gql, notice)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Notice == nil || *res.Notice != notice {
		t.Errorf("expected orchestrator-nil notice, got %v", res.Notice)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// persistLivingWikiSettings — optimistic-concurrency retry paths (T-H2)
// ─────────────────────────────────────────────────────────────────────────────

// conflictOnceStore wraps a RepoSettingsMemStore and injects a single
// ErrLWikiSettingsVersionConflict on the first SetRepoSettingsIfVersion call.
// After that, it delegates to the real MemStore so the retry succeeds.
type conflictOnceStore struct {
	*livingwiki.RepoSettingsMemStore
	calls int
}

func (s *conflictOnceStore) SetRepoSettingsIfVersion(ctx context.Context, settings livingwiki.RepositoryLivingWikiSettings, expectedVersion int) error {
	s.calls++
	if s.calls == 1 {
		return livingwiki.ErrLWikiSettingsVersionConflict
	}
	return s.RepoSettingsMemStore.SetRepoSettingsIfVersion(ctx, settings, expectedVersion)
}

// alwaysConflictStore returns ErrLWikiSettingsVersionConflict on every
// SetRepoSettingsIfVersion call, simulating persistent concurrent writers.
type alwaysConflictStore struct {
	*livingwiki.RepoSettingsMemStore
}

func (s *alwaysConflictStore) SetRepoSettingsIfVersion(_ context.Context, _ livingwiki.RepositoryLivingWikiSettings, _ int) error {
	return livingwiki.ErrLWikiSettingsVersionConflict
}

// TestPersistLivingWikiSettings_ConflictRetrySucceeds verifies the CA-158
// first-conflict → re-read → re-merge → retry path: when the store returns
// ErrLWikiSettingsVersionConflict on the first write attempt, persistLivingWikiSettings
// re-reads the fresh row, merges intent fields, and retries. The second attempt
// succeeds and the function returns nil.
func TestPersistLivingWikiSettings_ConflictRetrySucceeds(t *testing.T) {
	t.Parallel()

	// Seed a row so the re-read on conflict finds something to merge onto.
	inner := livingwiki.NewRepoSettingsMemStore()
	seed := livingwiki.RepositoryLivingWikiSettings{
		TenantID: "default",
		RepoID:   "repo-retry-1",
		Enabled:  false,
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	}
	if err := inner.SetRepoSettings(context.Background(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seeded, _ := inner.GetRepoSettings(context.Background(), "default", "repo-retry-1")
	if seeded == nil {
		t.Fatal("seed read returned nil")
	}

	store := &conflictOnceStore{RepoSettingsMemStore: inner}

	// Attempt to enable the repo.  The first write will conflict; the retry
	// should succeed after re-reading the fresh row and merging.
	settings := *seeded
	settings.Enabled = true

	if err := persistLivingWikiSettings(context.Background(), store, settings); err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if store.calls != 2 {
		t.Errorf("expected exactly 2 SetRepoSettingsIfVersion calls (initial + retry), got %d", store.calls)
	}

	// Verify the row was actually updated.
	got, err := inner.GetRepoSettings(context.Background(), "default", "repo-retry-1")
	if err != nil {
		t.Fatalf("post-persist read: %v", err)
	}
	if got == nil {
		t.Fatal("row not found after persist")
	}
	if !got.Enabled {
		t.Error("expected Enabled=true after successful retry persist")
	}
}

// TestPersistLivingWikiSettings_BothConflicts_ReturnsError verifies the second
// conflict → surface-error path: when both write attempts return
// ErrLWikiSettingsVersionConflict, persistLivingWikiSettings returns an error
// wrapping the sentinel rather than looping indefinitely.
func TestPersistLivingWikiSettings_BothConflicts_ReturnsError(t *testing.T) {
	t.Parallel()

	// Seed a row so the re-read succeeds (otherwise we'd hit the "row deleted"
	// fallback branch instead of the retry branch).
	inner := livingwiki.NewRepoSettingsMemStore()
	seed := livingwiki.RepositoryLivingWikiSettings{
		TenantID: "default",
		RepoID:   "repo-retry-2",
		Enabled:  false,
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
	}
	if err := inner.SetRepoSettings(context.Background(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seeded, _ := inner.GetRepoSettings(context.Background(), "default", "repo-retry-2")
	if seeded == nil {
		t.Fatal("seed read returned nil")
	}

	store := &alwaysConflictStore{RepoSettingsMemStore: inner}

	settings := *seeded
	settings.Enabled = true

	err := persistLivingWikiSettings(context.Background(), store, settings)
	if err == nil {
		t.Fatal("expected error when both write attempts conflict, got nil")
	}
	// The error must wrap or include something meaningful — it should NOT be
	// ErrLWikiSettingsVersionConflict unwrapped directly (the function wraps it
	// with context). Verify the message contains the retry context phrase.
	if !strings.Contains(err.Error(), "after conflict retry") {
		t.Errorf("error %q should mention 'after conflict retry'", err.Error())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// enqueueWikiJob — nil-orchestrator path (no real side effects)
// ─────────────────────────────────────────────────────────────────────────────

func TestEnqueueWikiJob_NilOrchestrator_ReturnsDispatchFailureNotice(t *testing.T) {
	t.Parallel()
	// With a nil Orchestrator, buildColdStartRunner returns a stub closure that
	// immediately marks the job complete. However, the enqueue call itself goes
	// to r.Deps.Orchestrator.Enqueue which is nil — so enqueueWikiJob returns a
	// "Settings saved but job dispatch failed" notice, not a hard error.
	// Verify that nil Orchestrator doesn't panic and yields a notice result.
	r := &Resolver{
		Deps: &appdeps.AppDeps{
			LivingWikiRepoStore: livingwiki.NewRepoSettingsMemStore(),
			// Orchestrator: nil — dispatch-failure notice path
		},
		Store: newStubGraphStore(),
	}
	settings := livingwiki.RepositoryLivingWikiSettings{
		TenantID:                  "default",
		RepoID:                    "repo-eq1",
		Enabled:                   true,
		LivingWikiDetailedEnabled: true,
		Sinks:                     []livingwiki.RepoWikiSink{{Kind: livingwiki.RepoWikiSinkGitRepo}},
		MaxPagesPerJob:            500,
	}
	effectiveSettings := settings
	gql := mapRepoLivingWikiSettings(&settings)
	input := EnableLivingWikiForRepoInput{RepositoryID: "repo-eq1"}
	jobMode := deriveLivingWikiJobMode(effectiveSettings)

	res, err := enqueueWikiJob(context.Background(), r, settings, effectiveSettings, jobMode, input, gql)
	if err != nil {
		t.Fatalf("expected no error (dispatch failure becomes notice), got: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if res.Notice == nil {
		t.Error("expected a dispatch-failure notice, got nil")
	}
	if !strings.Contains(*res.Notice, "dispatch failed") {
		t.Errorf("notice %q should mention 'dispatch failed'", *res.Notice)
	}
}
