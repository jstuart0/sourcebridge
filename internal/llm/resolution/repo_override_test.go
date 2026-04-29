// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// TestRepoOverride_LivingWikiOpsWinOverWorkspace exercises the full
// override → workspace → env → builtin precedence chain. Slice 5
// acceptance test.
func TestRepoOverride_LivingWikiOpsWinOverWorkspace(t *testing.T) {
	env := config.LLMConfig{
		Provider:     "anthropic",
		APIKey:       "env-key",
		SummaryModel: "claude-sonnet-env",
	}
	store := &fakeStore{
		rec: &WorkspaceRecord{
			Provider:     "openai",
			APIKey:       "ws-key",
			SummaryModel: "gpt-4o-ws",
		},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-A": {
				Provider: "ollama",
				BaseURL:  "http://localhost:11434",
				APIKey:   "repo-A-key",
				Model:    "qwen2.5:32b",
			},
		},
	}
	r := New(store, repoStore, env, nil)

	// Living-wiki op: override beats workspace beats env.
	for _, op := range []string{OpLivingWikiColdStart, OpLivingWikiRegen, OpLivingWikiAssembly} {
		t.Run(op, func(t *testing.T) {
			snap, err := r.Resolve(context.Background(), "repo-A", op)
			if err != nil {
				t.Fatalf("Resolve %s: %v", op, err)
			}
			if snap.Provider != "ollama" {
				t.Errorf("provider for %s: got %q, want ollama", op, snap.Provider)
			}
			if snap.APIKey != "repo-A-key" {
				t.Errorf("api_key for %s: got %q, want repo-A-key", op, snap.APIKey)
			}
			if snap.Model != "qwen2.5:32b" {
				t.Errorf("model for %s: got %q, want qwen2.5:32b", op, snap.Model)
			}
			if snap.Sources[FieldProvider] != SourceRepoOverride {
				t.Errorf("provider source for %s: got %q, want repo_override", op, snap.Sources[FieldProvider])
			}
			if snap.Sources[FieldAPIKey] != SourceRepoOverride {
				t.Errorf("api_key source for %s: got %q, want repo_override", op, snap.Sources[FieldAPIKey])
			}
		})
	}
}

// TestRepoOverride_NonLivingWikiOpsIgnoreOverride verifies that the
// override is scoped to living-wiki ops only — even when the same repoID
// has an override row, QA / discussion / knowledge ops resolve to the
// workspace settings.
func TestRepoOverride_NonLivingWikiOpsIgnoreOverride(t *testing.T) {
	env := config.LLMConfig{Provider: "anthropic", APIKey: "env-key"}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "openai", APIKey: "ws-key"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-A": {Provider: "ollama", APIKey: "repo-key"},
		},
	}
	r := New(store, repoStore, env, nil)

	for _, op := range []string{OpDiscussion, OpKnowledge, OpQAClassify, OpReportGenerate, OpReview} {
		t.Run(op, func(t *testing.T) {
			snap, err := r.Resolve(context.Background(), "repo-A", op)
			if err != nil {
				t.Fatalf("Resolve %s: %v", op, err)
			}
			if snap.Provider != "openai" {
				t.Errorf("provider for %s: got %q, want openai (workspace, override skipped)", op, snap.Provider)
			}
			if snap.APIKey != "ws-key" {
				t.Errorf("api_key for %s: got %q, want ws-key (workspace, override skipped)", op, snap.APIKey)
			}
			if snap.Sources[FieldProvider] != SourceWorkspace {
				t.Errorf("provider source for %s: got %q, want workspace", op, snap.Sources[FieldProvider])
			}
		})
	}
}

// TestRepoOverride_PartialOverrideFallsThroughForUnsetFields: when the
// override sets only some fields, unset fields fall through to workspace
// (then env, then builtin). Per-field source labels reflect this.
func TestRepoOverride_PartialOverrideFallsThroughForUnsetFields(t *testing.T) {
	env := config.LLMConfig{Provider: "anthropic", APIKey: "env-key"}
	store := &fakeStore{
		rec: &WorkspaceRecord{
			Provider:     "openai",
			APIKey:       "ws-key",
			SummaryModel: "gpt-4o",
		},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			// Override sets only Model — provider, api_key, base_url
			// must come from workspace.
			"repo-A": {Model: "qwen2.5"},
		},
	}
	r := New(store, repoStore, env, nil)
	snap, err := r.Resolve(context.Background(), "repo-A", OpLivingWikiColdStart)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Provider != "openai" {
		t.Errorf("provider: got %q, want openai (workspace; override didn't set it)", snap.Provider)
	}
	if snap.APIKey != "ws-key" {
		t.Errorf("api_key: got %q, want ws-key (workspace; override didn't set it)", snap.APIKey)
	}
	if snap.Model != "qwen2.5" {
		t.Errorf("model: got %q, want qwen2.5 (override)", snap.Model)
	}
	if snap.Sources[FieldProvider] != SourceWorkspace {
		t.Errorf("provider source: got %q, want workspace", snap.Sources[FieldProvider])
	}
	if snap.Sources[FieldModel] != SourceRepoOverride {
		t.Errorf("model source: got %q, want repo_override", snap.Sources[FieldModel])
	}
}
