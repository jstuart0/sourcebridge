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
