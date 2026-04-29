// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// fakeStore implements LLMConfigStore for tests. It tracks how many times
// LoadLLMConfig was called so the version-keyed cache behavior can be
// asserted directly.
type fakeStore struct {
	rec        *WorkspaceRecord
	version    uint64
	loadCalls  atomic.Int64
	verCalls   atomic.Int64
	loadErr    error
	versionErr error
}

func (f *fakeStore) LoadLLMConfig() (*WorkspaceRecord, error) {
	f.loadCalls.Add(1)
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.rec == nil {
		return nil, nil
	}
	cp := *f.rec
	cp.Version = f.version
	return &cp, nil
}

func (f *fakeStore) LoadLLMConfigVersion() (uint64, error) {
	f.verCalls.Add(1)
	if f.versionErr != nil {
		return 0, f.versionErr
	}
	return f.version, nil
}

type fakeRepoStore struct {
	overrides map[string]*RepoOverride
	err       error
}

func (f *fakeRepoStore) LoadLivingWikiLLMOverride(_ context.Context, repoID string) (*RepoOverride, error) {
	if f.err != nil {
		return nil, f.err
	}
	if ov, ok := f.overrides[repoID]; ok {
		return ov, nil
	}
	return nil, nil
}

func TestResolve_BuiltinOnly(t *testing.T) {
	r := New(nil, nil, config.LLMConfig{}, nil)
	snap, err := r.Resolve(context.Background(), "", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic", snap.Provider)
	}
	if snap.Sources[FieldProvider] != SourceBuiltin {
		t.Errorf("provider source: got %q, want builtin", snap.Sources[FieldProvider])
	}
	if snap.APIKey != "" {
		t.Errorf("api key: got %q, want empty", snap.APIKey)
	}
	if snap.Sources[FieldAPIKey] != SourceBuiltin {
		t.Errorf("api_key source: got %q, want builtin", snap.Sources[FieldAPIKey])
	}
}

func TestResolve_EnvBootstrap(t *testing.T) {
	env := config.LLMConfig{
		Provider:     "openai",
		APIKey:       "env-key",
		SummaryModel: "gpt-4o",
		TimeoutSecs:  120,
	}
	r := New(nil, nil, env, nil)
	snap, err := r.Resolve(context.Background(), "", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "openai" {
		t.Errorf("provider: got %q, want openai", snap.Provider)
	}
	if snap.Sources[FieldProvider] != SourceEnvFallback {
		t.Errorf("provider source: got %q, want env_fallback", snap.Sources[FieldProvider])
	}
	if snap.APIKey != "env-key" {
		t.Errorf("api key: got %q, want env-key", snap.APIKey)
	}
	if snap.Sources[FieldAPIKey] != SourceEnvFallback {
		t.Errorf("api_key source: got %q, want env_fallback", snap.Sources[FieldAPIKey])
	}
	if snap.Model != "gpt-4o" {
		t.Errorf("model: got %q, want gpt-4o", snap.Model)
	}
	if snap.TimeoutSecs != 120 {
		t.Errorf("timeout: got %d, want 120", snap.TimeoutSecs)
	}
}

func TestResolve_WorkspaceOverridesEnv(t *testing.T) {
	env := config.LLMConfig{
		Provider: "anthropic",
		APIKey:   "env-key",
	}
	store := &fakeStore{
		rec: &WorkspaceRecord{
			Provider:     "openai",
			APIKey:       "ws-key",
			SummaryModel: "gpt-4o",
		},
		version: 1,
	}
	r := New(store, nil, env, nil)
	snap, err := r.Resolve(context.Background(), "", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "openai" {
		t.Errorf("provider: got %q, want openai (workspace wins)", snap.Provider)
	}
	if snap.APIKey != "ws-key" {
		t.Errorf("api key: got %q, want ws-key (workspace wins)", snap.APIKey)
	}
	if snap.Sources[FieldProvider] != SourceWorkspace {
		t.Errorf("provider source: got %q, want workspace", snap.Sources[FieldProvider])
	}
	if snap.Sources[FieldAPIKey] != SourceWorkspace {
		t.Errorf("api_key source: got %q, want workspace", snap.Sources[FieldAPIKey])
	}
}

func TestResolve_VersionCacheHit(t *testing.T) {
	store := &fakeStore{
		rec: &WorkspaceRecord{
			Provider: "openai",
			APIKey:   "ws-key",
		},
		version: 7,
	}
	r := New(store, nil, config.LLMConfig{}, nil)

	// First Resolve fetches the full record once.
	if _, err := r.Resolve(context.Background(), "", OpDiscussion); err != nil {
		t.Fatalf("Resolve#1: %v", err)
	}
	if got := store.loadCalls.Load(); got != 1 {
		t.Errorf("loadCalls after #1: got %d, want 1", got)
	}

	// Second Resolve at the same version: only version probe, no full load.
	if _, err := r.Resolve(context.Background(), "", OpDiscussion); err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	if got := store.loadCalls.Load(); got != 1 {
		t.Errorf("loadCalls after #2: got %d, want 1 (cache should have hit)", got)
	}
	if got := store.verCalls.Load(); got != 2 {
		t.Errorf("verCalls after #2: got %d, want 2", got)
	}
}

func TestResolve_VersionBumpInvalidatesCache(t *testing.T) {
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "openai", APIKey: "ws-key"},
		version: 1,
	}
	r := New(store, nil, config.LLMConfig{}, nil)

	if _, err := r.Resolve(context.Background(), "", OpDiscussion); err != nil {
		t.Fatalf("Resolve#1: %v", err)
	}

	// Save bumps the version.
	store.rec = &WorkspaceRecord{Provider: "anthropic", APIKey: "new-key"}
	store.version = 2

	snap, err := r.Resolve(context.Background(), "", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	if snap.Provider != "anthropic" {
		t.Errorf("provider after bump: got %q, want anthropic", snap.Provider)
	}
	if snap.APIKey != "new-key" {
		t.Errorf("api key after bump: got %q, want new-key", snap.APIKey)
	}
	if got := store.loadCalls.Load(); got != 2 {
		t.Errorf("loadCalls: got %d, want 2 (one per version)", got)
	}
}

func TestResolve_DBOutageServesCachedSnapshot(t *testing.T) {
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "openai", APIKey: "ws-key"},
		version: 1,
	}
	r := New(store, nil, config.LLMConfig{}, nil)

	// Warm the cache.
	if _, err := r.Resolve(context.Background(), "", OpDiscussion); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	// Simulate DB outage on the version probe.
	store.versionErr = errors.New("connection refused")

	snap, err := r.Resolve(context.Background(), "", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve under outage: %v", err)
	}
	if !snap.Stale {
		t.Error("expected snap.Stale=true under DB outage")
	}
	if snap.Provider != "openai" {
		t.Errorf("provider under outage: got %q, want openai (from cache)", snap.Provider)
	}
	if snap.APIKey != "ws-key" {
		t.Errorf("api key under outage: got %q, want ws-key (from cache)", snap.APIKey)
	}
	if !snap.StaleFields[FieldAPIKey] {
		t.Error("expected api_key marked stale")
	}
}

func TestResolve_DBOutageNoCache_FallsThroughToEnv(t *testing.T) {
	store := &fakeStore{versionErr: errors.New("offline")}
	env := config.LLMConfig{Provider: "ollama", BaseURL: "http://x", APIKey: ""}
	r := New(store, nil, env, nil)

	snap, err := r.Resolve(context.Background(), "", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Stale {
		t.Error("expected snap.Stale=false when no cache exists")
	}
	if snap.Provider != "ollama" {
		t.Errorf("provider: got %q, want ollama (env fallback)", snap.Provider)
	}
	if snap.Sources[FieldProvider] != SourceEnvFallback {
		t.Errorf("provider source: got %q, want env_fallback", snap.Sources[FieldProvider])
	}
}

func TestResolve_PerFieldSourceMix(t *testing.T) {
	// Env supplies provider; workspace supplies api key only; model falls
	// to env; draft model falls to env.
	env := config.LLMConfig{
		Provider:     "anthropic",
		SummaryModel: "claude-sonnet-4",
		DraftModel:   "claude-haiku-4",
	}
	store := &fakeStore{
		rec:     &WorkspaceRecord{APIKey: "ws-key"},
		version: 1,
	}
	r := New(store, nil, env, nil)
	snap, err := r.Resolve(context.Background(), "", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := snap.Sources[FieldProvider]; got != SourceEnvFallback {
		t.Errorf("provider source: got %q, want env_fallback", got)
	}
	if got := snap.Sources[FieldAPIKey]; got != SourceWorkspace {
		t.Errorf("api_key source: got %q, want workspace", got)
	}
	if got := snap.Sources[FieldModel]; got != SourceEnvFallback {
		t.Errorf("model source: got %q, want env_fallback", got)
	}
	if got := snap.Sources[FieldDraftModel]; got != SourceEnvFallback {
		t.Errorf("draft_model source: got %q, want env_fallback", got)
	}
}

func TestResolve_RepoOverrideForLivingWikiOnly(t *testing.T) {
	env := config.LLMConfig{Provider: "anthropic", APIKey: "env"}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "openai", APIKey: "ws-key"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-1": {Provider: "ollama", APIKey: "repo-key", Model: "qwen2.5"},
		},
	}
	r := New(store, repoStore, env, nil)

	// Living-wiki op: override applies.
	snap, err := r.Resolve(context.Background(), "repo-1", OpLivingWikiColdStart)
	if err != nil {
		t.Fatalf("Resolve living-wiki: %v", err)
	}
	if snap.Provider != "ollama" {
		t.Errorf("living-wiki provider: got %q, want ollama (repo override wins)", snap.Provider)
	}
	if snap.APIKey != "repo-key" {
		t.Errorf("living-wiki api key: got %q, want repo-key", snap.APIKey)
	}
	if snap.Sources[FieldProvider] != SourceRepoOverride {
		t.Errorf("living-wiki provider source: got %q, want repo_override", snap.Sources[FieldProvider])
	}

	// Discussion op: override is ignored even with the same repo.
	snap2, err := r.Resolve(context.Background(), "repo-1", OpDiscussion)
	if err != nil {
		t.Fatalf("Resolve discussion: %v", err)
	}
	if snap2.Provider != "openai" {
		t.Errorf("discussion provider: got %q, want openai (workspace, override skipped)", snap2.Provider)
	}
	if snap2.Sources[FieldProvider] != SourceWorkspace {
		t.Errorf("discussion provider source: got %q, want workspace", snap2.Sources[FieldProvider])
	}
}

func TestResolve_UnknownOpReturnsError(t *testing.T) {
	r := New(nil, nil, config.LLMConfig{}, nil)
	if _, err := r.Resolve(context.Background(), "", "totally.bogus"); err == nil {
		t.Fatal("Resolve: expected error for unknown op, got nil")
	}
}

func TestInvalidateLocal_ForcesRefetch(t *testing.T) {
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "openai", APIKey: "v1"},
		version: 1,
	}
	r := New(store, nil, config.LLMConfig{}, nil)

	if _, err := r.Resolve(context.Background(), "", OpDiscussion); err != nil {
		t.Fatalf("Resolve#1: %v", err)
	}
	if got := store.loadCalls.Load(); got != 1 {
		t.Errorf("loadCalls#1: got %d, want 1", got)
	}

	r.InvalidateLocal()

	if _, err := r.Resolve(context.Background(), "", OpDiscussion); err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	if got := store.loadCalls.Load(); got != 2 {
		t.Errorf("loadCalls#2: got %d, want 2 (cache should have been invalidated)", got)
	}
}

func TestResolve_MultiReplicaSeesNewVersion(t *testing.T) {
	// Two resolvers sharing the same fake store represent two replicas.
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "openai", APIKey: "v1"},
		version: 1,
	}
	rA := New(store, nil, config.LLMConfig{}, nil)
	rB := New(store, nil, config.LLMConfig{}, nil)

	// Both warm their caches at v1.
	if _, err := rA.Resolve(context.Background(), "", OpDiscussion); err != nil {
		t.Fatalf("rA warm: %v", err)
	}
	if _, err := rB.Resolve(context.Background(), "", OpDiscussion); err != nil {
		t.Fatalf("rB warm: %v", err)
	}

	// Replica A saves; version bumps in shared store.
	store.rec = &WorkspaceRecord{Provider: "ollama", APIKey: "v2"}
	store.version = 2

	// Replica B's very next Resolve must see v2 — no time wait.
	snap, err := rB.Resolve(context.Background(), "", OpDiscussion)
	if err != nil {
		t.Fatalf("rB after save: %v", err)
	}
	if snap.Provider != "ollama" || snap.APIKey != "v2" {
		t.Errorf("rB stale after replica-A save: provider=%q api_key=%q (want ollama / v2)",
			snap.Provider, snap.APIKey)
	}
}
