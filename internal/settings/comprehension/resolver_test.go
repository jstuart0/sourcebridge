// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package comprehension

import (
	"testing"
)

func TestResolve_DefaultsWhenEmpty(t *testing.T) {
	store := NewMemStore()
	eff, err := Resolve(store, WorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.StrategyPreferenceChain) != 2 || eff.StrategyPreferenceChain[0] != "hierarchical" {
		t.Errorf("expected default chain, got %v", eff.StrategyPreferenceChain)
	}
	if eff.MaxConcurrency != 3 {
		t.Errorf("expected MaxConcurrency=3, got %d", eff.MaxConcurrency)
	}
}

func TestResolve_WorkspaceOverridesDefaults(t *testing.T) {
	store := NewMemStore()
	_ = store.SetSettings(&Settings{
		ScopeType:               ScopeWorkspace,
		ScopeKey:                "default",
		StrategyPreferenceChain: []string{"single_shot"},
		MaxConcurrency:          5,
	})

	eff, err := Resolve(store, WorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.StrategyPreferenceChain) != 1 || eff.StrategyPreferenceChain[0] != "single_shot" {
		t.Errorf("expected single_shot chain, got %v", eff.StrategyPreferenceChain)
	}
	if eff.MaxConcurrency != 5 {
		t.Errorf("expected MaxConcurrency=5, got %d", eff.MaxConcurrency)
	}
	// Non-overridden field inherits default
	if eff.MaxPromptTokens != 100000 {
		t.Errorf("expected default MaxPromptTokens=100000, got %d", eff.MaxPromptTokens)
	}
}

func TestResolve_ArtifactTypeOverridesWorkspace(t *testing.T) {
	store := NewMemStore()
	_ = store.SetSettings(&Settings{
		ScopeType:               ScopeWorkspace,
		ScopeKey:                "default",
		ModelID:                 "llama3:latest",
		StrategyPreferenceChain: []string{"hierarchical"},
		MaxConcurrency:          5,
	})
	_ = store.SetSettings(&Settings{
		ScopeType: ScopeArtifactType,
		ScopeKey:  "cliff_notes",
		ModelID:   "claude-sonnet-4-6",
	})

	eff, err := Resolve(store, Scope{Type: ScopeArtifactType, Key: "cliff_notes"})
	if err != nil {
		t.Fatal(err)
	}
	// Model overridden by artifact scope
	if eff.ModelID != "claude-sonnet-4-6" {
		t.Errorf("expected claude-sonnet-4-6, got %s", eff.ModelID)
	}
	// Chain inherited from workspace
	if len(eff.StrategyPreferenceChain) != 1 || eff.StrategyPreferenceChain[0] != "hierarchical" {
		t.Errorf("expected hierarchical from workspace, got %v", eff.StrategyPreferenceChain)
	}
	// Concurrency inherited from workspace
	if eff.MaxConcurrency != 5 {
		t.Errorf("expected MaxConcurrency=5, got %d", eff.MaxConcurrency)
	}
	// Track inheritance
	if eff.InheritedFrom["modelId"].Type != ScopeArtifactType {
		t.Errorf("expected modelId from artifact_type, got %v", eff.InheritedFrom["modelId"])
	}
	if eff.InheritedFrom["maxConcurrency"].Type != ScopeWorkspace {
		t.Errorf("expected maxConcurrency from workspace, got %v", eff.InheritedFrom["maxConcurrency"])
	}
}

func TestResolve_UserOverridesAll(t *testing.T) {
	store := NewMemStore()
	_ = store.SetSettings(&Settings{
		ScopeType:      ScopeWorkspace,
		ScopeKey:       "default",
		MaxConcurrency: 5,
	})
	_ = store.SetSettings(&Settings{
		ScopeType:      ScopeArtifactType,
		ScopeKey:       "cliff_notes",
		MaxConcurrency: 10,
	})
	_ = store.SetSettings(&Settings{
		ScopeType:      ScopeUser,
		ScopeKey:       "user-123",
		MaxConcurrency: 1,
	})

	eff, err := Resolve(store, Scope{Type: ScopeUser, Key: "user-123"})
	if err != nil {
		t.Fatal(err)
	}
	if eff.MaxConcurrency != 1 {
		t.Errorf("expected MaxConcurrency=1 from user, got %d", eff.MaxConcurrency)
	}
}

func TestResolve_BoolFieldInheritance(t *testing.T) {
	store := NewMemStore()
	enabled := true
	_ = store.SetSettings(&Settings{
		ScopeType:    ScopeWorkspace,
		ScopeKey:     "default",
		CacheEnabled: &enabled,
	})

	eff, err := Resolve(store, Scope{Type: ScopeArtifactType, Key: "cliff_notes"})
	if err != nil {
		t.Fatal(err)
	}
	if eff.CacheEnabled == nil || !*eff.CacheEnabled {
		t.Error("expected cache enabled from workspace")
	}

	// Artifact-level override disables cache
	disabled := false
	_ = store.SetSettings(&Settings{
		ScopeType:    ScopeArtifactType,
		ScopeKey:     "cliff_notes",
		CacheEnabled: &disabled,
	})

	eff, err = Resolve(store, Scope{Type: ScopeArtifactType, Key: "cliff_notes"})
	if err != nil {
		t.Fatal(err)
	}
	if eff.CacheEnabled == nil || *eff.CacheEnabled {
		t.Error("expected cache disabled from artifact override")
	}
}

func TestMemStore_ModelCapabilitiesCRUD(t *testing.T) {
	store := NewMemStore()

	// Get non-existent
	mc, err := store.GetModelCapabilities("none")
	if err != nil {
		t.Fatal(err)
	}
	if mc != nil {
		t.Error("expected nil for non-existent model")
	}

	// Create
	_ = store.SetModelCapabilities(&ModelCapabilities{
		ModelID:                "claude-sonnet-4-6",
		Provider:               "anthropic",
		DeclaredContextTokens:  200000,
		EffectiveContextTokens: 160000,
		InstructionFollowing:   "high",
		Source:                 "builtin",
	})

	mc, err = store.GetModelCapabilities("claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	if mc == nil || mc.Provider != "anthropic" {
		t.Error("expected anthropic provider")
	}

	// List
	all, _ := store.ListModelCapabilities()
	if len(all) != 1 {
		t.Errorf("expected 1 model, got %d", len(all))
	}

	// Delete
	_ = store.DeleteModelCapabilities("claude-sonnet-4-6")
	mc, _ = store.GetModelCapabilities("claude-sonnet-4-6")
	if mc != nil {
		t.Error("expected nil after delete")
	}
}

func TestMemStore_SettingsCRUD(t *testing.T) {
	store := NewMemStore()

	// Get non-existent
	s, err := store.GetSettings(WorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Error("expected nil for non-existent scope")
	}

	// Set
	_ = store.SetSettings(&Settings{
		ScopeType:      ScopeWorkspace,
		ScopeKey:       "default",
		MaxConcurrency: 5,
	})

	s, _ = store.GetSettings(WorkspaceScope)
	if s == nil || s.MaxConcurrency != 5 {
		t.Error("expected MaxConcurrency=5")
	}

	// List
	all, _ := store.ListSettings()
	if len(all) != 1 {
		t.Errorf("expected 1 settings, got %d", len(all))
	}

	// Delete
	_ = store.DeleteSettings(WorkspaceScope)
	s, _ = store.GetSettings(WorkspaceScope)
	if s != nil {
		t.Error("expected nil after delete")
	}
}
