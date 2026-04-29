// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// llmOverridePtr helpers for the pointer-shaped GraphQL input. Named
// distinctly from any package-level helpers to avoid name collisions.
func llmStrPtr(s string) *string { return &s }
func llmBoolPtr(b bool) *bool    { return &b }

// encryptionFailingStore wraps a real RepoSettingsMemStore but rejects
// any save that would persist a non-empty LLMOverride.APIKey, simulating
// the "encryption key not configured" production behavior. Mirrors how
// the SurrealDB store returns livingwiki.ErrEncryptionKeyRequired in
// that case.
type encryptionFailingStore struct {
	*livingwiki.RepoSettingsMemStore
}

func (s *encryptionFailingStore) SetRepoSettings(ctx context.Context, settings livingwiki.RepositoryLivingWikiSettings) error {
	if settings.LLMOverride != nil && settings.LLMOverride.APIKey != "" {
		return fmt.Errorf("%w: encryption key required", livingwiki.ErrEncryptionKeyRequired)
	}
	return s.RepoSettingsMemStore.SetRepoSettings(ctx, settings)
}

// TestSetRepositoryLLMOverride_FullCreate sets every field on a brand-new
// override and verifies the saved row is correctly persisted and the
// returned masked view matches.
func TestSetRepositoryLLMOverride_FullCreate(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: mem}}

	out, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		Provider:                 llmStrPtr("ollama"),
		BaseURL:                  llmStrPtr("http://localhost:11434"),
		APIKey:                   llmStrPtr("plaintext-key-01234567"),
		AdvancedMode:             llmBoolPtr(true),
		SummaryModel:             llmStrPtr("qwen2.5:32b"),
		ReviewModel:              llmStrPtr("qwen2.5:14b-review"),
		AskModel:                 llmStrPtr("qwen2.5:14b-ask"),
		KnowledgeModel:           llmStrPtr("qwen2.5:32b-knowledge"),
		ArchitectureDiagramModel: llmStrPtr("qwen2.5:32b-diagram"),
		ReportModel:              llmStrPtr("qwen3:32b-report"),
		DraftModel:               llmStrPtr("qwen2.5:1.5b-draft"),
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	if out.Provider == nil || *out.Provider != "ollama" {
		t.Errorf("Provider: got %v, want ollama", out.Provider)
	}
	if out.BaseURL == nil || *out.BaseURL != "http://localhost:11434" {
		t.Errorf("BaseURL: got %v, want http://localhost:11434", out.BaseURL)
	}
	if !out.APIKeySet {
		t.Error("APIKeySet: got false, want true (a key was provided)")
	}
	if out.APIKeyHint == nil || !strings.Contains(*out.APIKeyHint, "...") {
		t.Errorf("APIKeyHint: got %v, want a masked preview", out.APIKeyHint)
	}
	// Critical security check: the raw key must NEVER appear in the response.
	if out.APIKeyHint != nil && strings.Contains(*out.APIKeyHint, "plaintext-key-01234567") {
		t.Errorf("APIKeyHint leaked the raw key: %q", *out.APIKeyHint)
	}
	if !out.AdvancedMode {
		t.Error("AdvancedMode: got false, want true")
	}
	if out.SummaryModel == nil || *out.SummaryModel != "qwen2.5:32b" {
		t.Errorf("SummaryModel: got %v, want qwen2.5:32b", out.SummaryModel)
	}
	if out.ArchitectureDiagramModel == nil || *out.ArchitectureDiagramModel != "qwen2.5:32b-diagram" {
		t.Errorf("ArchitectureDiagramModel: got %v, want qwen2.5:32b-diagram", out.ArchitectureDiagramModel)
	}

	// Verify the row was actually written.
	saved, err := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if err != nil {
		t.Fatalf("GetRepoSettings: %v", err)
	}
	if saved == nil || saved.LLMOverride == nil {
		t.Fatal("expected a saved override")
	}
	if saved.LLMOverride.APIKey != "plaintext-key-01234567" {
		t.Errorf("saved APIKey: got %q, want plaintext-key-01234567 (mem store doesn't encrypt; verify the resolver passed it through unchanged)", saved.LLMOverride.APIKey)
	}
}

// TestSetRepositoryLLMOverride_PartialPatch_PreservesOtherFields: sending
// only `provider` must leave previously-saved api_key, models, etc.
// untouched.
func TestSetRepositoryLLMOverride_PartialPatch_PreservesOtherFields(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	// Pre-populate.
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			Provider:     "openai",
			BaseURL:      "https://api.openai.com/v1",
			APIKey:       "sk-old-key-1234567890",
			AdvancedMode: true,
			SummaryModel: "gpt-4o",
			ReviewModel:  "gpt-4o-mini",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: mem}}
	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		Provider: llmStrPtr("anthropic"), // only this field
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride == nil {
		t.Fatal("override was cleared by a partial patch")
	}
	if saved.LLMOverride.Provider != "anthropic" {
		t.Errorf("Provider: got %q, want anthropic", saved.LLMOverride.Provider)
	}
	if saved.LLMOverride.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL: got %q, want preserved openai URL", saved.LLMOverride.BaseURL)
	}
	if saved.LLMOverride.APIKey != "sk-old-key-1234567890" {
		t.Errorf("APIKey: got %q, want preserved old key", saved.LLMOverride.APIKey)
	}
	if !saved.LLMOverride.AdvancedMode {
		t.Error("AdvancedMode: got false, want preserved true")
	}
	if saved.LLMOverride.SummaryModel != "gpt-4o" {
		t.Errorf("SummaryModel: got %q, want preserved gpt-4o", saved.LLMOverride.SummaryModel)
	}
}

// TestSetRepositoryLLMOverride_EmptyStringClearsField: sending "" for a
// non-secret field clears it back to workspace inheritance.
func TestSetRepositoryLLMOverride_EmptyStringClearsField(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			Provider:     "openai",
			SummaryModel: "gpt-4o",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: mem}}
	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		SummaryModel: llmStrPtr(""), // explicit clear
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride == nil {
		t.Fatal("expected override to remain (only one field was cleared)")
	}
	if saved.LLMOverride.SummaryModel != "" {
		t.Errorf("SummaryModel: got %q, want empty (cleared)", saved.LLMOverride.SummaryModel)
	}
	if saved.LLMOverride.Provider != "openai" {
		t.Errorf("Provider: got %q, want preserved openai", saved.LLMOverride.Provider)
	}
}

// TestSetRepositoryLLMOverride_ClearAPIKeyFlag: clearAPIKey:true drops
// the saved cipher even when apiKey is omitted.
func TestSetRepositoryLLMOverride_ClearAPIKeyFlag(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			Provider: "openai",
			APIKey:   "sk-old-key-1234567890",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: mem}}
	out, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		ClearAPIKey: llmBoolPtr(true),
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	if out.APIKeySet {
		t.Error("APIKeySet: got true, want false (clearAPIKey was true)")
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride == nil {
		t.Fatal("expected override to remain (other fields are still set)")
	}
	if saved.LLMOverride.APIKey != "" {
		t.Errorf("APIKey: got %q, want empty after clearAPIKey:true", saved.LLMOverride.APIKey)
	}
	if saved.LLMOverride.Provider != "openai" {
		t.Errorf("Provider: got %q, want preserved openai", saved.LLMOverride.Provider)
	}
}

// TestSetRepositoryLLMOverride_OmittedAPIKeyLeavesAlone: nil apiKey
// preserves the saved cipher.
func TestSetRepositoryLLMOverride_OmittedAPIKeyLeavesAlone(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			Provider: "openai",
			APIKey:   "sk-keep-this-1234567890",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: mem}}
	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		Provider: llmStrPtr("anthropic"),
		// APIKey omitted → leave alone.
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride.APIKey != "sk-keep-this-1234567890" {
		t.Errorf("APIKey: got %q, want preserved sk-keep-this-...", saved.LLMOverride.APIKey)
	}
}

// TestSetRepositoryLLMOverride_EncryptionKeyRequiredErrorMapping: when
// the store rejects a save with livingwiki.ErrEncryptionKeyRequired, the
// resolver maps it to a GraphQL error with extension code
// "ENCRYPTION_KEY_REQUIRED" so the UI can render a precise message.
func TestSetRepositoryLLMOverride_EncryptionKeyRequiredErrorMapping(t *testing.T) {
	store := &encryptionFailingStore{RepoSettingsMemStore: livingwiki.NewRepoSettingsMemStore()}
	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: store}}

	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		APIKey: llmStrPtr("plaintext-key-1234567890"),
	})
	if err == nil {
		t.Fatal("expected an error from a save with no encryption key")
	}

	var gqlErr *gqlerror.Error
	if !errors.As(err, &gqlErr) {
		t.Fatalf("expected gqlerror.Error, got %T: %v", err, err)
	}
	code, _ := gqlErr.Extensions["code"].(string)
	if code != "ENCRYPTION_KEY_REQUIRED" {
		t.Errorf("extension code: got %q, want ENCRYPTION_KEY_REQUIRED", code)
	}
	if !strings.Contains(gqlErr.Message, "SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY") {
		t.Errorf("error message should reference the env var; got: %q", gqlErr.Message)
	}
}

// TestClearRepositoryLLMOverride_DropsRow: the clear mutation removes
// the override entirely (settings.LLMOverride becomes nil) while
// preserving the rest of the row.
func TestClearRepositoryLLMOverride_DropsRow(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			Provider: "openai",
			APIKey:   "sk-1234567890",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: mem}}
	out, err := r.ClearRepositoryLLMOverride(context.Background(), "repo-A")
	if err != nil {
		t.Fatalf("ClearRepositoryLLMOverride: %v", err)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride != nil {
		t.Error("LLMOverride: still set after Clear")
	}
	if saved.Mode != livingwiki.RepoWikiModePRReview {
		t.Errorf("Mode: got %v, want preserved PR_REVIEW", saved.Mode)
	}

	// Returned settings should reflect the clear.
	if out == nil {
		t.Fatal("Clear returned nil settings")
	}
}

// TestSetRepositoryLLMOverride_ResultingEmptyOverrideDropsRow: when a
// patch results in every field being blank, the override row is dropped
// (symmetric with explicit clearRepositoryLLMOverride).
func TestSetRepositoryLLMOverride_ResultingEmptyOverrideDropsRow(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			Provider: "openai",
			APIKey:   "sk-1234567890",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{LivingWikiRepoStore: mem}}
	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		Provider:    llmStrPtr(""),
		ClearAPIKey: llmBoolPtr(true),
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride != nil {
		t.Errorf("LLMOverride: still set after every field cleared, want nil")
	}
}
