// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
)

// fakeLLMConfigStore is an in-memory LLMConfigStore for handler tests.
type fakeLLMConfigStore struct {
	mu      sync.Mutex
	rec     *LLMConfigRecord
	saveErr error
}

func (f *fakeLLMConfigStore) LoadLLMConfig() (*LLMConfigRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rec == nil {
		return nil, nil
	}
	cp := *f.rec
	return &cp, nil
}

func (f *fakeLLMConfigStore) SaveLLMConfig(rec *LLMConfigRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	cp := *rec
	f.rec = &cp
	return nil
}

func newLLMConfigTestServer(_ *testing.T, env config.LLMConfig, store *fakeLLMConfigStore) *Server {
	cfg := &config.Config{}
	cfg.LLM = env
	return &Server{
		cfg:            cfg,
		llmConfigStore: store,
	}
}

func TestHandleGetLLMConfig_PrefersWorkspaceOverEnv(t *testing.T) {
	env := config.LLMConfig{
		Provider:     "anthropic",
		APIKey:       "env-key",
		SummaryModel: "claude-sonnet-4",
	}
	store := &fakeLLMConfigStore{
		rec: &LLMConfigRecord{
			Provider:     "openai",
			APIKey:       "ws-key",
			SummaryModel: "gpt-4o",
		},
	}
	s := newLLMConfigTestServer(t, env, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-config", nil)
	w := httptest.NewRecorder()
	s.handleGetLLMConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp llmConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Provider != "openai" {
		t.Errorf("provider: got %q, want openai (workspace wins)", resp.Provider)
	}
	if !resp.APIKeySet {
		t.Errorf("api_key_set: want true (workspace key)")
	}
	if resp.SummaryModel != "gpt-4o" {
		t.Errorf("summary_model: got %q, want gpt-4o", resp.SummaryModel)
	}
}

func TestHandleGetLLMConfig_FallsThroughToEnv(t *testing.T) {
	env := config.LLMConfig{
		Provider:     "anthropic",
		APIKey:       "env-key",
		SummaryModel: "claude-sonnet-4",
	}
	// No store record → env values should appear.
	store := &fakeLLMConfigStore{}
	s := newLLMConfigTestServer(t, env, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/llm-config", nil)
	w := httptest.NewRecorder()
	s.handleGetLLMConfig(w, req)

	var resp llmConfigResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic (env)", resp.Provider)
	}
	if !resp.APIKeySet {
		t.Error("api_key_set: want true (env)")
	}
}

func TestHandleUpdateLLMConfig_PartialMergeAgainstDB(t *testing.T) {
	store := &fakeLLMConfigStore{
		rec: &LLMConfigRecord{
			Provider:       "openai",
			APIKey:         "ws-key",
			SummaryModel:   "gpt-4o",
			KnowledgeModel: "gpt-4o",
			AdvancedMode:   true,
		},
	}
	s := newLLMConfigTestServer(t, config.LLMConfig{Provider: "anthropic"}, store)

	// Request that only updates Provider; other fields must be
	// preserved from the existing DB record.
	body := `{"provider":"ollama"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-config", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.handleUpdateLLMConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	saved, _ := store.LoadLLMConfig()
	if saved.Provider != "ollama" {
		t.Errorf("provider: got %q, want ollama", saved.Provider)
	}
	if saved.APIKey != "ws-key" {
		t.Errorf("api_key dropped: got %q, want ws-key (must be preserved)", saved.APIKey)
	}
	if saved.SummaryModel != "gpt-4o" {
		t.Errorf("summary_model dropped: got %q, want gpt-4o", saved.SummaryModel)
	}
	if saved.KnowledgeModel != "gpt-4o" {
		t.Errorf("knowledge_model dropped: got %q, want gpt-4o", saved.KnowledgeModel)
	}
	if !saved.AdvancedMode {
		t.Error("advanced_mode dropped: want true")
	}
}

func TestHandleUpdateLLMConfig_RejectsInvalidProvider(t *testing.T) {
	s := newLLMConfigTestServer(t, config.LLMConfig{}, &fakeLLMConfigStore{})
	body := `{"provider":"bogus"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-config", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.handleUpdateLLMConfig(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleUpdateLLMConfig_NoStoreReturns503(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	body := `{"provider":"openai"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-config", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.handleUpdateLLMConfig(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when no store, got %d: %s", w.Code, w.Body.String())
	}
}

// fakeResolverStore mimics resolution.LLMConfigStore for the resolver
// integration test below.
type fakeResolverStore struct {
	mu      sync.Mutex
	rec     *resolution.WorkspaceRecord
	version uint64
}

func (f *fakeResolverStore) LoadLLMConfig() (*resolution.WorkspaceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rec == nil {
		return nil, nil
	}
	cp := *f.rec
	cp.Version = f.version
	return &cp, nil
}

func (f *fakeResolverStore) LoadLLMConfigVersion() (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.version, nil
}

func TestHandleUpdateLLMConfig_InvalidatesResolverCache(t *testing.T) {
	// When an admin save lands, the resolver's local cache must be
	// invalidated so the very next Resolve on this replica picks up the
	// new values without waiting for the version stamp.
	resolverStore := &fakeResolverStore{
		rec: &resolution.WorkspaceRecord{
			Provider: "openai",
			APIKey:   "v1",
		},
		version: 1,
	}
	resolver := resolution.New(resolverStore, nil, config.LLMConfig{}, nil)

	// Warm the resolver's cache.
	if _, err := resolver.Resolve(context.Background(), "", resolution.OpDiscussion); err != nil {
		t.Fatalf("warm: %v", err)
	}

	store := &fakeLLMConfigStore{
		rec: &LLMConfigRecord{Provider: "openai", APIKey: "v1"},
	}
	s := newLLMConfigTestServer(t, config.LLMConfig{}, store)
	s.llmResolver = resolver

	body := `{"api_key":"v2"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/llm-config", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	s.handleUpdateLLMConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Bump the resolver-store's version + record (simulating the
	// real-world: SaveLLMConfig in the underlying SurrealDB store
	// would have done both atomically; the test split is just the
	// fakery).
	resolverStore.mu.Lock()
	resolverStore.rec = &resolution.WorkspaceRecord{Provider: "openai", APIKey: "v2"}
	resolverStore.version = 2
	resolverStore.mu.Unlock()

	snap, err := resolver.Resolve(context.Background(), "", resolution.OpDiscussion)
	if err != nil {
		t.Fatalf("post-save resolve: %v", err)
	}
	if snap.APIKey != "v2" {
		t.Errorf("post-save api_key: got %q, want v2", snap.APIKey)
	}
}
