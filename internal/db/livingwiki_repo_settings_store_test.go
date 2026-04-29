// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"testing"
	"time"
)

// TestToSettings_LegacyModelKeyPromotedToSummaryModel pins the back-compat
// behavior: a pre-R2 row with only the legacy `model` JSON key (no
// `summary_model`) decodes with LLMOverride.SummaryModel populated.
//
// This guards against silent data loss during a rolling deploy or
// rollback: pre-R2 replicas write `model` only; R2 replicas write both
// `model` and `summary_model` (see SetRepoSettings); both shapes must
// read correctly during the transition window.
func TestToSettings_LegacyModelKeyPromotedToSummaryModel(t *testing.T) {
	row := &surrealLivingWikiRepoSettings{
		TenantID:          "default",
		RepoID:            "repo-A",
		Mode:              "PR_REVIEW",
		Sinks:             []surrealRepoWikiSink{},
		ExcludePaths:      []string{},
		StaleWhenStrategy: "DIRECT",
		MaxPagesPerJob:    50,
		LLMOverride: &surrealLivingWikiLLMOverride{
			Provider:    "ollama",
			BaseURL:     "http://localhost:11434",
			LegacyModel: "qwen2.5:32b", // pre-R2 single-model field
			// SummaryModel and other per-area fields are absent (zero).
			// AdvancedMode default false.
		},
	}

	// No api-key cipher in this row, so decryptAPIKey is a no-op.
	noopDecrypt := func(s string) (string, error) { return s, nil }

	settings, err := row.toSettings(noopDecrypt)
	if err != nil {
		t.Fatalf("toSettings: %v", err)
	}
	if settings.LLMOverride == nil {
		t.Fatal("LLMOverride: got nil, want a populated override (legacy model key should have been promoted)")
	}
	if settings.LLMOverride.SummaryModel != "qwen2.5:32b" {
		t.Errorf("SummaryModel: got %q, want qwen2.5:32b (legacy `model` key must be promoted to SummaryModel when SummaryModel is empty)", settings.LLMOverride.SummaryModel)
	}
	if settings.LLMOverride.Provider != "ollama" {
		t.Errorf("Provider: got %q, want ollama", settings.LLMOverride.Provider)
	}
	if settings.LLMOverride.AdvancedMode {
		t.Error("AdvancedMode: got true, want false (default)")
	}
}

// TestToSettings_NewSummaryModelTakesPrecedenceOverLegacy: when both
// `model` and `summary_model` are present (the dual-write case during
// the transition window), the new key wins. This pins the dual-write
// is forward-safe: a row written by an R2 replica reads correctly even
// if the values in `model` and `summary_model` ever diverge.
func TestToSettings_NewSummaryModelTakesPrecedenceOverLegacy(t *testing.T) {
	row := &surrealLivingWikiRepoSettings{
		TenantID:          "default",
		RepoID:            "repo-A",
		Mode:              "PR_REVIEW",
		Sinks:             []surrealRepoWikiSink{},
		ExcludePaths:      []string{},
		StaleWhenStrategy: "DIRECT",
		MaxPagesPerJob:    50,
		LLMOverride: &surrealLivingWikiLLMOverride{
			Provider:     "ollama",
			LegacyModel:  "qwen2.5:32b",
			SummaryModel: "qwen3:32b", // new value, must win
		},
	}

	noopDecrypt := func(s string) (string, error) { return s, nil }

	settings, err := row.toSettings(noopDecrypt)
	if err != nil {
		t.Fatalf("toSettings: %v", err)
	}
	if settings.LLMOverride == nil || settings.LLMOverride.SummaryModel != "qwen3:32b" {
		t.Errorf("SummaryModel: got %q, want qwen3:32b (new `summary_model` key must win over legacy `model`)",
			settings.LLMOverride.SummaryModel)
	}
}

// TestToSettings_PerAreaModelsRoundTrip: round-trip every per-area model
// field. R2 stores them as additional keys nested under the
// `living_wiki_llm_override` option<object> column; the DTO decodes
// them straight onto the domain struct.
func TestToSettings_PerAreaModelsRoundTrip(t *testing.T) {
	row := &surrealLivingWikiRepoSettings{
		TenantID:          "default",
		RepoID:            "repo-A",
		Mode:              "PR_REVIEW",
		Sinks:             []surrealRepoWikiSink{},
		ExcludePaths:      []string{},
		StaleWhenStrategy: "DIRECT",
		MaxPagesPerJob:    50,
		LLMOverride: &surrealLivingWikiLLMOverride{
			Provider:                 "openai",
			BaseURL:                  "https://api.openai.com/v1",
			AdvancedMode:             true,
			SummaryModel:             "gpt-4o-mini",
			ReviewModel:              "gpt-4o",
			AskModel:                 "gpt-4.1",
			KnowledgeModel:           "gpt-4o",
			ArchitectureDiagramModel: "gpt-4o",
			ReportModel:              "gpt-4o",
			DraftModel:               "gpt-4o-mini",
			UpdatedBy:                "tester",
			UpdatedAt:                &surrealTime{Time: time.Now().UTC().Truncate(time.Second)},
		},
	}

	noopDecrypt := func(s string) (string, error) { return s, nil }

	settings, err := row.toSettings(noopDecrypt)
	if err != nil {
		t.Fatalf("toSettings: %v", err)
	}
	ov := settings.LLMOverride
	if ov == nil {
		t.Fatal("LLMOverride is nil")
	}
	checks := []struct {
		field, got, want string
	}{
		{"Provider", ov.Provider, "openai"},
		{"BaseURL", ov.BaseURL, "https://api.openai.com/v1"},
		{"SummaryModel", ov.SummaryModel, "gpt-4o-mini"},
		{"ReviewModel", ov.ReviewModel, "gpt-4o"},
		{"AskModel", ov.AskModel, "gpt-4.1"},
		{"KnowledgeModel", ov.KnowledgeModel, "gpt-4o"},
		{"ArchitectureDiagramModel", ov.ArchitectureDiagramModel, "gpt-4o"},
		{"ReportModel", ov.ReportModel, "gpt-4o"},
		{"DraftModel", ov.DraftModel, "gpt-4o-mini"},
		{"UpdatedBy", ov.UpdatedBy, "tester"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.field, c.got, c.want)
		}
	}
	if !ov.AdvancedMode {
		t.Error("AdvancedMode: got false, want true")
	}
}

// TestToSettings_EmptyOverrideTreatedAsNil: a row with all-empty
// override fields decodes as LLMOverride=nil. Distinct from "no row at
// all" but indistinguishable in resolver semantics.
func TestToSettings_EmptyOverrideTreatedAsNil(t *testing.T) {
	row := &surrealLivingWikiRepoSettings{
		TenantID:          "default",
		RepoID:            "repo-A",
		Mode:              "PR_REVIEW",
		Sinks:             []surrealRepoWikiSink{},
		ExcludePaths:      []string{},
		StaleWhenStrategy: "DIRECT",
		MaxPagesPerJob:    50,
		LLMOverride:       &surrealLivingWikiLLMOverride{}, // all empty
	}

	noopDecrypt := func(s string) (string, error) { return s, nil }

	settings, err := row.toSettings(noopDecrypt)
	if err != nil {
		t.Fatalf("toSettings: %v", err)
	}
	if settings.LLMOverride != nil {
		t.Errorf("LLMOverride: got non-nil, want nil for an all-empty override (IsEmpty path)")
	}
}
