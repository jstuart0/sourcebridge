// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeConfigStore + fakeProfileStore + fakeReconciler implement the
// adapter's narrow interfaces against in-memory state. Used by every
// unit test below.
type fakeConfigStore struct {
	snap         *ConfigSnapshot
	loadCalls    atomic.Int64
	versionCalls atomic.Int64
	loadErr      error
	versionErr   error
}

func (f *fakeConfigStore) LoadConfigSnapshot(_ context.Context) (*ConfigSnapshot, error) {
	f.loadCalls.Add(1)
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.snap == nil {
		return nil, nil
	}
	cp := *f.snap
	return &cp, nil
}

func (f *fakeConfigStore) LoadLLMConfigVersion() (uint64, error) {
	f.versionCalls.Add(1)
	if f.versionErr != nil {
		return 0, f.versionErr
	}
	if f.snap == nil {
		return 0, nil
	}
	return f.snap.Version, nil
}

type fakeProfileStore struct {
	profiles  map[string]*WorkspaceRecord
	loadCalls atomic.Int64
	loadErr   error
}

func (f *fakeProfileStore) LoadProfileForResolution(_ context.Context, profileID string) (*WorkspaceRecord, error) {
	f.loadCalls.Add(1)
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if p, ok := f.profiles[profileID]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, ErrProfileNotFound
}

func (f *fakeProfileStore) LoadAllProfileIDs(_ context.Context) ([]string, error) {
	ids := make([]string, 0, len(f.profiles))
	for id := range f.profiles {
		ids = append(ids, id)
	}
	return ids, nil
}

type fakeReconciler struct {
	calls     atomic.Int64
	result    ReconcileResult
	err       error
	lastObsV  uint64
	lastObsW  uint64
	lastID    string
}

func (f *fakeReconciler) ReconcileLegacyToActive(_ context.Context, observedVersion, observedWatermark uint64, activeID string) (ReconcileResult, error) {
	f.calls.Add(1)
	f.lastObsV = observedVersion
	f.lastObsW = observedWatermark
	f.lastID = activeID
	if f.err != nil {
		return ReconcileResult{}, f.err
	}
	return f.result, nil
}

func TestAdapter_NoActiveProfileFreshInstall(t *testing.T) {
	cs := &fakeConfigStore{snap: &ConfigSnapshot{}}
	ps := &fakeProfileStore{}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, nil, nil)

	rec, err := a.LoadLLMConfig()
	if err != nil {
		t.Fatalf("LoadLLMConfig: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil record on fresh install with empty active id, got %+v", rec)
	}
	if a.ActiveProfileMissing() {
		t.Errorf("ActiveProfileMissing should be false")
	}
}

func TestAdapter_DualReadFallbackPreMigration(t *testing.T) {
	// Active profile id empty + dual-read enabled + populated legacy
	// fields → returns legacy overlay (D8 transitional / codex-H3).
	cs := &fakeConfigStore{snap: &ConfigSnapshot{
		ActiveProfileID:    "",
		Version:            1,
		LegacyProvider:     "anthropic",
		LegacyAPIKey:       "leg-key",
		LegacySummaryModel: "leg-model",
	}}
	ps := &fakeProfileStore{}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, nil, nil)

	rec, err := a.LoadLLMConfig()
	if err != nil {
		t.Fatalf("LoadLLMConfig: %v", err)
	}
	if rec == nil {
		t.Fatalf("expected legacy overlay record, got nil")
	}
	if rec.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic", rec.Provider)
	}
	if rec.APIKey != "leg-key" {
		t.Errorf("api_key: got %q, want leg-key", rec.APIKey)
	}
	if rec.ProfileID != "" {
		t.Errorf("ProfileID should be empty for legacy overlay, got %q", rec.ProfileID)
	}
	if rec.Version != 1 {
		t.Errorf("Version: got %d, want 1", rec.Version)
	}
}

func TestAdapter_DualReadFallbackDisabled(t *testing.T) {
	// dexter-M1: when dualReadFallbackEnabled is false, the fallback
	// path returns nil (empty workspace overlay), even with populated
	// legacy fields.
	cs := &fakeConfigStore{snap: &ConfigSnapshot{
		LegacyProvider: "anthropic",
	}}
	ps := &fakeProfileStore{}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, nil, nil)
	a.SetDualReadFallback(false)

	rec, err := a.LoadLLMConfig()
	if err != nil {
		t.Fatalf("LoadLLMConfig: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil with fallback disabled, got %+v", rec)
	}
}

func TestAdapter_ActiveProfileMissingSentinel(t *testing.T) {
	// active_profile_id points at a deleted row. The adapter MUST NOT
	// silently fall back to legacy (codex-H3); instead it logs error,
	// latches activeProfileMissing=true, and returns nil.
	cs := &fakeConfigStore{snap: &ConfigSnapshot{
		ActiveProfileID: "ca_llm_profile:vanished",
		Version:         5,
		LegacyProvider:  "anthropic", // populated, but MUST NOT be used
	}}
	ps := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{}, // empty
	}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, nil, nil)

	rec, err := a.LoadLLMConfig()
	if err != nil {
		t.Fatalf("LoadLLMConfig: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil overlay on active-missing, got %+v (legacy resurrection bug)", rec)
	}
	if !a.ActiveProfileMissing() {
		t.Errorf("ActiveProfileMissing should latch to true")
	}
}

func TestAdapter_ActivePresentNoReconcile(t *testing.T) {
	cs := &fakeConfigStore{snap: &ConfigSnapshot{
		ActiveProfileID: "ca_llm_profile:abc",
		Version:         3,
	}}
	ps := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			"ca_llm_profile:abc": {
				Provider:                  "openai",
				APIKey:                    "active-key",
				LastLegacyVersionConsumed: 3, // == workspace.version, no reconcile needed
			},
		},
	}
	rec := &fakeReconciler{}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, rec, nil)

	got, err := a.LoadLLMConfig()
	if err != nil {
		t.Fatalf("LoadLLMConfig: %v", err)
	}
	if got == nil {
		t.Fatal("expected active profile record, got nil")
	}
	if got.Provider != "openai" {
		t.Errorf("provider: got %q, want openai", got.Provider)
	}
	if got.ProfileID != "ca_llm_profile:abc" {
		t.Errorf("ProfileID: got %q, want ca_llm_profile:abc", got.ProfileID)
	}
	if rec.calls.Load() != 0 {
		t.Errorf("reconciler called %d times; expected 0 (no gap)", rec.calls.Load())
	}
}

func TestAdapter_LegacyWriteTriggersReconcile(t *testing.T) {
	// Workspace version > profile.LastLegacyVersionConsumed: an old pod
	// has written via SaveLLMConfig since the last new-code update.
	// Adapter serves the legacy overlay AND fires the reconciler
	// write-through (codex-H2 / r1c).
	cs := &fakeConfigStore{snap: &ConfigSnapshot{
		ActiveProfileID:    "ca_llm_profile:abc",
		Version:            5, // bumped by old pod
		LegacyProvider:     "old-pod-provider",
		LegacyAPIKey:       "old-pod-key",
		LegacySummaryModel: "old-pod-model",
	}}
	ps := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			"ca_llm_profile:abc": {
				Provider:                  "stale-active",
				APIKey:                    "stale-key",
				LastLegacyVersionConsumed: 4, // 1 less than workspace.version
			},
		},
	}
	rec := &fakeReconciler{
		result: ReconcileResult{ActuallyWrote: true, NewWatermark: 6},
	}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, rec, nil)

	got, err := a.LoadLLMConfig()
	if err != nil {
		t.Fatalf("LoadLLMConfig: %v", err)
	}
	if got == nil {
		t.Fatal("expected legacy overlay record, got nil")
	}
	// THIS resolve serves the legacy overlay (it's at least as fresh).
	if got.Provider != "old-pod-provider" {
		t.Errorf("provider: got %q, want old-pod-provider (legacy overlay)", got.Provider)
	}
	if got.APIKey != "old-pod-key" {
		t.Errorf("api_key: got %q, want old-pod-key", got.APIKey)
	}
	// Reconciler was called with the right snapshot.
	if rec.calls.Load() != 1 {
		t.Errorf("reconciler called %d times; expected 1", rec.calls.Load())
	}
	if rec.lastObsV != 5 {
		t.Errorf("reconciler observedVersion: got %d, want 5", rec.lastObsV)
	}
	if rec.lastObsW != 4 {
		t.Errorf("reconciler observedWatermark: got %d, want 4", rec.lastObsW)
	}
	if rec.lastID != "ca_llm_profile:abc" {
		t.Errorf("reconciler activeID: got %q, want ca_llm_profile:abc", rec.lastID)
	}
}

func TestAdapter_ReconcileVersionConflictIsSwallowed(t *testing.T) {
	// Reconciler returns ErrVersionConflict (another writer raced us).
	// Adapter serves the legacy overlay and logs at debug — does NOT
	// surface the error.
	cs := &fakeConfigStore{snap: &ConfigSnapshot{
		ActiveProfileID: "ca_llm_profile:abc",
		Version:         5,
		LegacyProvider:  "x",
	}}
	ps := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			"ca_llm_profile:abc": {LastLegacyVersionConsumed: 4},
		},
	}
	rec := &fakeReconciler{err: ErrVersionConflict}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, rec, nil)

	got, err := a.LoadLLMConfig()
	if err != nil {
		t.Fatalf("LoadLLMConfig should swallow ErrVersionConflict, got %v", err)
	}
	if got == nil {
		t.Fatal("expected legacy overlay even on reconcile conflict")
	}
	if got.Provider != "x" {
		t.Errorf("legacy overlay not served; got %+v", got)
	}
}

func TestAdapter_LoadProfileForResolutionImplementsLookup(t *testing.T) {
	// codex-M5 / slice 3: the adapter implements ProfileLookupStore
	// alongside LLMConfigStore. Slice 1 wires both interfaces.
	cs := &fakeConfigStore{}
	ps := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			"ca_llm_profile:abc": {Provider: "p"},
			"ca_llm_profile:def": {Provider: "q"},
		},
	}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, nil, nil)

	got, err := a.LoadProfileForResolution(context.Background(), "ca_llm_profile:abc")
	if err != nil {
		t.Fatalf("LoadProfileForResolution: %v", err)
	}
	if got.Provider != "p" {
		t.Errorf("provider: got %q, want p", got.Provider)
	}

	ids, err := a.LoadAllProfileIDs(context.Background())
	if err != nil {
		t.Fatalf("LoadAllProfileIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("LoadAllProfileIDs: got %d ids, want 2", len(ids))
	}

	_, err = a.LoadProfileForResolution(context.Background(), "ca_llm_profile:missing")
	if !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("missing id: got %v, want ErrProfileNotFound", err)
	}
}

func TestAdapter_ProfileVersionInheritsFromWorkspace(t *testing.T) {
	// When the profile store doesn't populate WorkspaceRecord.Version
	// (it's the workspace cell, not a profile field), the adapter
	// fills it from the snapshot so the resolver's cache key works.
	cs := &fakeConfigStore{snap: &ConfigSnapshot{
		ActiveProfileID: "ca_llm_profile:abc",
		Version:         42,
	}}
	ps := &fakeProfileStore{
		profiles: map[string]*WorkspaceRecord{
			"ca_llm_profile:abc": {
				Provider:                  "x",
				LastLegacyVersionConsumed: 42, // up to date
				Version:                   0,  // not set by the profile store
			},
		},
	}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, nil, nil)

	got, err := a.LoadLLMConfig()
	if err != nil {
		t.Fatalf("LoadLLMConfig: %v", err)
	}
	if got.Version != 42 {
		t.Errorf("Version: got %d, want 42 (filled from snapshot)", got.Version)
	}
}

func TestAdapter_ActiveMissingResetsOnSuccessfulResolve(t *testing.T) {
	// Latched activeProfileMissing should reset to false the next
	// time a Resolve succeeds.
	cs := &fakeConfigStore{snap: &ConfigSnapshot{
		ActiveProfileID: "ca_llm_profile:gone",
		Version:         1,
	}}
	ps := &fakeProfileStore{profiles: map[string]*WorkspaceRecord{}}
	a := NewProfileAwareLLMResolverAdapter(cs, ps, nil, nil)

	// First resolve: profile missing → latched true.
	if _, err := a.LoadLLMConfig(); err != nil {
		t.Fatal(err)
	}
	if !a.ActiveProfileMissing() {
		t.Error("expected latch true after missing-profile resolve")
	}

	// Now profile becomes available; next resolve unlatches.
	cs.snap.ActiveProfileID = "ca_llm_profile:abc"
	cs.snap.Version = 2
	ps.profiles["ca_llm_profile:abc"] = &WorkspaceRecord{
		Provider:                  "x",
		LastLegacyVersionConsumed: 2,
	}
	if _, err := a.LoadLLMConfig(); err != nil {
		t.Fatal(err)
	}
	if a.ActiveProfileMissing() {
		t.Error("expected latch false after successful resolve")
	}
}

func TestLegacyOverlayFromSnapPreservesAllFields(t *testing.T) {
	snap := &ConfigSnapshot{
		ActiveProfileID:                "ca_llm_profile:x",
		Version:                        9,
		LegacyProvider:                 "p",
		LegacyBaseURL:                  "u",
		LegacyAPIKey:                   "k",
		LegacySummaryModel:             "sm",
		LegacyReviewModel:              "rm",
		LegacyAskModel:                 "am",
		LegacyKnowledgeModel:           "km",
		LegacyArchitectureDiagramModel: "dm",
		LegacyReportModel:              "rep",
		LegacyDraftModel:               "dr",
		LegacyTimeoutSecs:              60,
		LegacyAdvancedMode:             true,
	}
	rec := legacyOverlayFromSnap(snap)
	if rec.Provider != "p" || rec.BaseURL != "u" || rec.APIKey != "k" {
		t.Errorf("core fields mismatch: %+v", rec)
	}
	if rec.SummaryModel != "sm" || rec.ReviewModel != "rm" || rec.AskModel != "am" ||
		rec.KnowledgeModel != "km" || rec.ArchitectureDiagramModel != "dm" ||
		rec.ReportModel != "rep" || rec.DraftModel != "dr" {
		t.Errorf("model fields mismatch: %+v", rec)
	}
	if rec.TimeoutSecs != 60 || !rec.AdvancedMode {
		t.Errorf("scalar fields mismatch: %+v", rec)
	}
	if rec.Version != 9 {
		t.Errorf("Version: got %d, want 9", rec.Version)
	}
	if rec.ProfileID != "" {
		t.Errorf("ProfileID should be empty for legacy overlay, got %q", rec.ProfileID)
	}
	if rec.LastLegacyVersionConsumed != 0 {
		t.Errorf("LastLegacyVersionConsumed should be 0, got %d", rec.LastLegacyVersionConsumed)
	}
}
