// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package resolution

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// TestRepoOverride_AppliesAcrossAllOps exercises the full override →
// workspace → env → builtin precedence chain across the entire op
// taxonomy. R2 widened the override scope: an override now applies to
// every repo-scoped LLM op, not just living-wiki ones. (The parent
// delivery only honored the override for living_wiki.* ops; this test
// is the regression-coverage for that change.)
func TestRepoOverride_AppliesAcrossAllOps(t *testing.T) {
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
				Provider:     "ollama",
				BaseURL:      "http://localhost:11434",
				APIKey:       "repo-A-key",
				SummaryModel: "qwen2.5:32b",
			},
		},
	}
	r := New(store, repoStore, env, nil)

	// Override beats workspace beats env across every op group.
	allOps := []string{
		OpLivingWikiColdStart, OpLivingWikiRegen, OpLivingWikiAssembly,
		OpDiscussion, OpKnowledge, OpReview, OpAnalysis,
		OpQAClassify, OpQADecompose, OpQASynth, OpQADeepSynth, OpQAAgentTurn,
		OpReportGenerate, OpClusteringRelabel, OpMCPExplain, OpMCPDiscussStream,
		OpDiscussStream, OpRequirementsEnrich, OpRequirementsExtract,
		OpArchitectureDiagram,
	}
	for _, op := range allOps {
		t.Run(op, func(t *testing.T) {
			snap, err := r.Resolve(context.Background(), "repo-A", op)
			if err != nil {
				t.Fatalf("Resolve %s: %v", op, err)
			}
			if snap.Provider != "ollama" {
				t.Errorf("provider for %s: got %q, want ollama (override)", op, snap.Provider)
			}
			if snap.APIKey != "repo-A-key" {
				t.Errorf("api_key for %s: got %q, want repo-A-key (override)", op, snap.APIKey)
			}
			if snap.Model != "qwen2.5:32b" {
				t.Errorf("model for %s: got %q, want qwen2.5:32b (override SummaryModel applies to all groups when AdvancedMode=false)", op, snap.Model)
			}
			if snap.Sources[FieldProvider] != SourceRepoOverride {
				t.Errorf("provider source for %s: got %q, want repo_override", op, snap.Sources[FieldProvider])
			}
			if snap.Sources[FieldAPIKey] != SourceRepoOverride {
				t.Errorf("api_key source for %s: got %q, want repo_override", op, snap.Sources[FieldAPIKey])
			}
			if snap.Sources[FieldModel] != SourceRepoOverride {
				t.Errorf("model source for %s: got %q, want repo_override", op, snap.Sources[FieldModel])
			}
		})
	}
}

// TestRepoOverride_PerAreaModelSelection_AdvancedMode: when the override
// has AdvancedMode=true, the resolver picks the right per-area model
// based on the op's group. ReviewModel for OpReview, AskModel for
// OpDiscussion, etc.
func TestRepoOverride_PerAreaModelSelection_AdvancedMode(t *testing.T) {
	env := config.LLMConfig{Provider: "anthropic"}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "openai", SummaryModel: "ws-default"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-A": {
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
	r := New(store, repoStore, env, nil)

	cases := []struct {
		op        string
		wantModel string
	}{
		{OpAnalysis, "summary-m"},
		{OpQAClassify, "summary-m"},
		{OpReview, "review-m"},
		{OpDiscussion, "ask-m"},
		{OpQASynth, "ask-m"},
		{OpKnowledge, "knowledge-m"},
		{OpLivingWikiColdStart, "knowledge-m"},
		{OpArchitectureDiagram, "diagram-m"},
		{OpReportGenerate, "report-m"},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			snap, err := r.Resolve(context.Background(), "repo-A", tc.op)
			if err != nil {
				t.Fatalf("Resolve %s: %v", tc.op, err)
			}
			if snap.Model != tc.wantModel {
				t.Errorf("model for %s: got %q, want %q", tc.op, snap.Model, tc.wantModel)
			}
			if snap.Sources[FieldModel] != SourceRepoOverride {
				t.Errorf("model source for %s: got %q, want repo_override", tc.op, snap.Sources[FieldModel])
			}
		})
	}
}

// TestRepoOverride_AdvancedModeOff_UsesSummaryModelForAll: with
// AdvancedMode=false, the override's SummaryModel applies to every op
// group (mirrors the workspace's simple-mode behavior).
func TestRepoOverride_AdvancedModeOff_UsesSummaryModelForAll(t *testing.T) {
	env := config.LLMConfig{Provider: "anthropic"}
	store := &fakeStore{
		rec:     &WorkspaceRecord{Provider: "openai", SummaryModel: "ws-default"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-A": {
				AdvancedMode: false,
				SummaryModel: "the-only-model",
				// Per-area fields are populated but ignored because
				// AdvancedMode is false.
				ReviewModel:    "should-be-ignored-1",
				AskModel:       "should-be-ignored-2",
				KnowledgeModel: "should-be-ignored-3",
			},
		},
	}
	r := New(store, repoStore, env, nil)

	for _, op := range []string{OpReview, OpDiscussion, OpKnowledge, OpAnalysis, OpReportGenerate, OpArchitectureDiagram} {
		t.Run(op, func(t *testing.T) {
			snap, err := r.Resolve(context.Background(), "repo-A", op)
			if err != nil {
				t.Fatalf("Resolve %s: %v", op, err)
			}
			if snap.Model != "the-only-model" {
				t.Errorf("model for %s: got %q, want the-only-model", op, snap.Model)
			}
		})
	}
}

// TestRepoOverride_DraftModelSeparateOverlay: the override's DraftModel
// is overlaid onto snap.DraftModel regardless of op group, mirroring
// applyEnvBoot and the workspace-side handling.
func TestRepoOverride_DraftModelSeparateOverlay(t *testing.T) {
	env := config.LLMConfig{}
	store := &fakeStore{
		rec:     &WorkspaceRecord{DraftModel: "ws-draft"},
		version: 1,
	}
	repoStore := &fakeRepoStore{
		overrides: map[string]*RepoOverride{
			"repo-A": {DraftModel: "repo-draft"},
		},
	}
	r := New(store, repoStore, env, nil)
	snap, err := r.Resolve(context.Background(), "repo-A", OpLivingWikiColdStart)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.DraftModel != "repo-draft" {
		t.Errorf("draft_model: got %q, want repo-draft", snap.DraftModel)
	}
	if snap.Sources[FieldDraftModel] != SourceRepoOverride {
		t.Errorf("draft_model source: got %q, want repo_override", snap.Sources[FieldDraftModel])
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
			// Override sets only SummaryModel — provider, api_key, base_url
			// must come from workspace.
			"repo-A": {SummaryModel: "qwen2.5"},
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

// TestWorkspaceOnly_ArchitectureDiagramModelHonored is the regression
// for the parent-delivery latent bug: workspace ArchitectureDiagramModel
// was saved but never selected by the resolver because OpKnowledge was
// passed for diagram generation. R2 introduces OpArchitectureDiagram and
// routes the diagram caller through it. This test pins that the
// workspace ArchitectureDiagramModel is now actually returned for the
// architecture-diagram op.
func TestWorkspaceOnly_ArchitectureDiagramModelHonored(t *testing.T) {
	env := config.LLMConfig{}
	store := &fakeStore{
		rec: &WorkspaceRecord{
			Provider:                 "anthropic",
			AdvancedMode:             true,
			SummaryModel:             "default-m",
			ArchitectureDiagramModel: "diagram-specialist-m",
		},
		version: 1,
	}
	r := New(store, nil, env, nil)
	snap, err := r.Resolve(context.Background(), "", OpArchitectureDiagram)
	if err != nil {
		t.Fatalf("Resolve OpArchitectureDiagram: %v", err)
	}
	if snap.Model != "diagram-specialist-m" {
		t.Errorf("model: got %q, want diagram-specialist-m (workspace ArchitectureDiagramModel must be selected for OpArchitectureDiagram)", snap.Model)
	}
	if snap.Sources[FieldModel] != SourceWorkspace {
		t.Errorf("model source: got %q, want workspace", snap.Sources[FieldModel])
	}
}
