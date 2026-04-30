// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// Slice 3 of the LLM provider profiles plan: per-repo override gains a
// "saved-profile" mode. The resolver fetches the referenced profile via
// ProfileLookupStore and overlays its values with source label
// SourceRepoOverrideProfile. These tests cover the three-mode
// discrimination + the failure modes (deleted profile, missing
// ProfileLookupStore wiring, generic store error).

// TestRepoOverrideProfile_HappyPath: workspace=A, override.profileId=B
// → snapshot fields all sourced from B with SourceRepoOverrideProfile
// labels.
func TestRepoOverrideProfile_HappyPath(t *testing.T) {
	env := config.LLMConfig{Provider: "anthropic"}
	store := &fakeStore{
		rec: &WorkspaceRecord{
			Provider:     "openai",         // workspace = profile A
			BaseURL:      "https://api.openai.com/v1",
			APIKey:       "ws-key",
			SummaryModel: "gpt-4o-A",
			ProfileID:    "ca_llm_profile:A",
		},
		version: 5,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-X": {ProfileID: "ca_llm_profile:B"},
		},
	}
	profileLookup := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			"ca_llm_profile:B": {
				Provider:     "ollama",
				BaseURL:      "http://localhost:11434",
				APIKey:       "B-key",
				SummaryModel: "qwen2.5:32b",
				TimeoutSecs:  300,
				ProfileID:    "ca_llm_profile:B",
			},
		},
	}
	r := NewWithProfileLookup(store, repoStore, profileLookup, env, nil)

	snap, err := r.Resolve(context.Background(), "repo-X", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "ollama" {
		t.Errorf("provider: got %q, want ollama (from profile B)", snap.Provider)
	}
	if snap.APIKey != "B-key" {
		t.Errorf("api_key: got %q, want B-key (from profile B)", snap.APIKey)
	}
	if snap.Model != "qwen2.5:32b" {
		t.Errorf("model: got %q, want qwen2.5:32b (from profile B summary_model)", snap.Model)
	}
	if snap.BaseURL != "http://localhost:11434" {
		t.Errorf("base_url: got %q, want http://localhost:11434 (from profile B)", snap.BaseURL)
	}
	if snap.TimeoutSecs != 300 {
		t.Errorf("timeout_secs: got %d, want 300 (from profile B)", snap.TimeoutSecs)
	}

	if got := snap.Sources[FieldProvider]; got != SourceRepoOverrideProfile {
		t.Errorf("sources.provider: got %q, want repo_override_profile", got)
	}
	if got := snap.Sources[FieldAPIKey]; got != SourceRepoOverrideProfile {
		t.Errorf("sources.api_key: got %q, want repo_override_profile", got)
	}
	if got := snap.Sources[FieldModel]; got != SourceRepoOverrideProfile {
		t.Errorf("sources.model: got %q, want repo_override_profile", got)
	}
	if got := snap.Sources[FieldBaseURL]; got != SourceRepoOverrideProfile {
		t.Errorf("sources.base_url: got %q, want repo_override_profile", got)
	}
	if got := snap.Sources[FieldTimeoutSecs]; got != SourceRepoOverrideProfile {
		t.Errorf("sources.timeout_secs: got %q, want repo_override_profile", got)
	}
}

// TestRepoOverrideProfile_AdvancedModeAreaSelection: profile B in
// advanced mode with per-area models. The resolver picks the right
// per-area model per op (mirrors workspace overlay's behavior; the
// profile schema mirrors the workspace schema, so workspaceModelForOp
// is the right helper).
func TestRepoOverrideProfile_AdvancedModeAreaSelection(t *testing.T) {
	env := config.LLMConfig{}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "anthropic", SummaryModel: "ws-default"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-X": {ProfileID: "ca_llm_profile:adv"},
		},
	}
	profileLookup := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			"ca_llm_profile:adv": {
				Provider:                 "openai",
				AdvancedMode:             true,
				SummaryModel:             "summary-m",
				ReviewModel:              "review-m",
				AskModel:                 "ask-m",
				KnowledgeModel:           "knowledge-m",
				ArchitectureDiagramModel: "diagram-m",
				ReportModel:              "report-m",
			},
		},
	}
	r := NewWithProfileLookup(store, repoStore, profileLookup, env, nil)

	cases := []struct {
		op       string
		wantName string
	}{
		{OpReview, "review-m"},
		{OpDiscussion, "ask-m"},
		{OpKnowledge, "knowledge-m"},
		{OpArchitectureDiagram, "diagram-m"},
		{OpReportGenerate, "report-m"},
		{OpAnalysis, "summary-m"},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			snap, err := r.Resolve(context.Background(), "repo-X", tc.op)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if snap.Model != tc.wantName {
				t.Errorf("model for %s: got %q, want %q", tc.op, snap.Model, tc.wantName)
			}
			if snap.Sources[FieldModel] != SourceRepoOverrideProfile {
				t.Errorf("sources.model: got %q, want repo_override_profile", snap.Sources[FieldModel])
			}
		})
	}
}

// TestRepoOverrideProfile_InlineModePreserved: when ProfileID is empty,
// the resolver runs the legacy inline-fields path (today's behavior)
// with SourceRepoOverride labels — NOT SourceRepoOverrideProfile.
func TestRepoOverrideProfile_InlineModePreserved(t *testing.T) {
	env := config.LLMConfig{}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "anthropic", APIKey: "ws"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-X": {
				ProfileID:    "", // inline mode
				Provider:     "ollama",
				APIKey:       "inline-key",
				SummaryModel: "inline-model",
			},
		},
	}
	profileLookup := &fakeProfileStore{} // empty, never consulted
	r := NewWithProfileLookup(store, repoStore, profileLookup, env, nil)

	snap, err := r.Resolve(context.Background(), "repo-X", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "ollama" {
		t.Errorf("provider: got %q, want ollama (inline)", snap.Provider)
	}
	if snap.Sources[FieldProvider] != SourceRepoOverride {
		t.Errorf("sources.provider: got %q, want repo_override (inline mode)", snap.Sources[FieldProvider])
	}
	if snap.Sources[FieldProvider] == SourceRepoOverrideProfile {
		t.Errorf("sources.provider must NOT be repo_override_profile in inline mode")
	}
	// Confirm the profile lookup was never called.
	if got := profileLookup.loadCalls.Load(); got != 0 {
		t.Errorf("profile lookup invoked %d times in inline mode (expected 0)", got)
	}
}

// TestRepoOverrideProfile_DeletedProfileFallsBackToWorkspace: ProfileID
// references a profile that has since been deleted. The resolver logs a
// Warn, treats the override as a no-op, and the workspace overlay
// applies. Critically, it does NOT silently leak a partial state (no
// half-applied profile fields).
func TestRepoOverrideProfile_DeletedProfileFallsBackToWorkspace(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	env := config.LLMConfig{}
	store := &fakeStore{
		rec: &WorkspaceRecord{
			Provider:     "anthropic",
			APIKey:       "ws-key",
			SummaryModel: "ws-model",
		},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-X": {ProfileID: "ca_llm_profile:gone"},
		},
	}
	profileLookup := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			// "gone" is NOT in the map → returns ErrProfileNotFound
		},
	}
	r := NewWithProfileLookup(store, repoStore, profileLookup, env, logger)

	snap, err := r.Resolve(context.Background(), "repo-X", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic (workspace fallback)", snap.Provider)
	}
	if snap.APIKey != "ws-key" {
		t.Errorf("api_key: got %q, want ws-key (workspace fallback)", snap.APIKey)
	}
	if snap.Sources[FieldProvider] != SourceWorkspace {
		t.Errorf("sources.provider: got %q, want workspace (override discarded)", snap.Sources[FieldProvider])
	}
	if !strings.Contains(logBuf.String(), "deleted profile") {
		t.Errorf("expected Warn log mentioning deleted profile, got: %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "ca_llm_profile:gone") {
		t.Errorf("expected log to include the missing profile id, got: %q", logBuf.String())
	}
}

// TestRepoOverrideProfile_StoreErrorFallsBackToWorkspace: a generic
// store error (not ErrProfileNotFound) on profile lookup also degrades
// to workspace fallback — same fail-closed semantics as the deleted
// profile case, but with a different log message so operators can
// distinguish "your profile was deleted" from "DB hiccup".
func TestRepoOverrideProfile_StoreErrorFallsBackToWorkspace(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	env := config.LLMConfig{}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "anthropic"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-X": {ProfileID: "ca_llm_profile:any"},
		},
	}
	profileLookup := &fakeProfileStore{
		loadErr: errors.New("simulated db outage"),
	}
	r := NewWithProfileLookup(store, repoStore, profileLookup, env, logger)

	snap, err := r.Resolve(context.Background(), "repo-X", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic (workspace fallback)", snap.Provider)
	}
	if !strings.Contains(logBuf.String(), "profile fetch failed") {
		t.Errorf("expected Warn log mentioning fetch failed (not 'deleted'), got: %q", logBuf.String())
	}
	if strings.Contains(logBuf.String(), "deleted profile") {
		t.Errorf("DB outage must NOT be reported as a deleted profile; got: %q", logBuf.String())
	}
}

// TestRepoOverrideProfile_NoLookupStoreFallsBack: the per-repo override
// row carries a ProfileID, but the resolver was never wired with a
// ProfileLookupStore (e.g., embedded mode / test wiring that uses the
// legacy New() constructor). The resolver must NOT silently apply the
// inline fields (which are empty in this row); it must fall through to
// workspace cleanly with a Warn log so the misconfiguration is visible.
func TestRepoOverrideProfile_NoLookupStoreFallsBack(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	env := config.LLMConfig{}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "anthropic", APIKey: "ws"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-X": {ProfileID: "ca_llm_profile:any"},
		},
	}
	// Use the legacy New() constructor: no profileStore wired.
	r := New(store, repoStore, env, logger)

	snap, err := r.Resolve(context.Background(), "repo-X", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic (workspace fallback when ProfileLookupStore unwired)", snap.Provider)
	}
	if !strings.Contains(logBuf.String(), "ProfileLookupStore is not wired") {
		t.Errorf("expected Warn log noting the missing ProfileLookupStore wiring, got: %q", logBuf.String())
	}
}

// TestRepoOverrideProfile_DefensiveInlineWinsOnCollision: a pathological
// row carries BOTH a non-empty ProfileID AND inline fields. The
// resolver applies inline mode (today's behavior wins on defense) so
// production rows that grew a stray ProfileID through database surgery
// don't silently swap in a different profile's credentials. The
// happy-path mutual-exclusion guarantees this never reaches production
// — but the defensive behavior is testable here.
func TestRepoOverrideProfile_DefensiveInlineWinsOnCollision(t *testing.T) {
	env := config.LLMConfig{}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "anthropic"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-X": {
				ProfileID:    "ca_llm_profile:foo",
				Provider:     "ollama",
				APIKey:       "inline-key",
				SummaryModel: "inline-m",
			},
		},
	}
	profileLookup := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			"ca_llm_profile:foo": {
				Provider: "openai",
				APIKey:   "profile-key",
			},
		},
	}
	r := NewWithProfileLookup(store, repoStore, profileLookup, env, nil)

	snap, err := r.Resolve(context.Background(), "repo-X", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Inline mode wins on the collision per the defensive
	// implementation.
	if snap.Provider != "ollama" {
		t.Errorf("provider: got %q, want ollama (inline wins on collision)", snap.Provider)
	}
	if snap.APIKey != "inline-key" {
		t.Errorf("api_key: got %q, want inline-key (inline wins on collision)", snap.APIKey)
	}
	if snap.Sources[FieldProvider] != SourceRepoOverride {
		t.Errorf("sources.provider: got %q, want repo_override (inline mode)", snap.Sources[FieldProvider])
	}
	// Profile lookup was NEVER invoked because hasInline short-circuits.
	if got := profileLookup.loadCalls.Load(); got != 0 {
		t.Errorf("profile lookup invoked %d times on collision (expected 0; inline wins)", got)
	}
}

// TestRepoOverrideProfile_EmptyOverrideIsNoOp: an override row that
// somehow ended up with all fields blank (no ProfileID, no inline) is
// a no-op — workspace settings flow through unchanged.
func TestRepoOverrideProfile_EmptyOverrideIsNoOp(t *testing.T) {
	env := config.LLMConfig{}
	store := &fakeStore{
		rec: &WorkspaceRecord{
			Provider: "anthropic",
			APIKey:   "ws-key",
		},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-X": {}, // every field zero
		},
	}
	r := NewWithProfileLookup(store, repoStore, &fakeProfileStore{}, env, nil)

	snap, err := r.Resolve(context.Background(), "repo-X", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "anthropic" || snap.APIKey != "ws-key" {
		t.Errorf("workspace not preserved with empty override: got provider=%q api_key=%q", snap.Provider, snap.APIKey)
	}
	if snap.Sources[FieldProvider] != SourceWorkspace {
		t.Errorf("sources.provider: got %q, want workspace", snap.Sources[FieldProvider])
	}
}
