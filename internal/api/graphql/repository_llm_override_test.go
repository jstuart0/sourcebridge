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

// ─────────────────────────────────────────────────────────────────────────
// Slice 3 of the LLM provider profiles plan: profileId / clearProfile
// patch semantics + PROFILE_NO_LONGER_EXISTS error code + profileName
// enrichment via LLMProfileLookup.
// ─────────────────────────────────────────────────────────────────────────

// fakeProfileLookup is a graphql.LLMProfileLookup test double. The
// `present` set drives the (name, exists, err) result tuple per id.
type fakeProfileLookup struct {
	present map[string]string // id → name
	err     error             // when non-nil, every call returns this err
	calls   int
}

func (f *fakeProfileLookup) LookupProfileName(_ context.Context, profileID string) (string, bool, error) {
	f.calls++
	if f.err != nil {
		return "", false, f.err
	}
	name, ok := f.present[profileID]
	if !ok {
		return "", false, nil
	}
	return name, true, nil
}

// TestSetRepositoryLLMOverride_ProfileMode_HappyPath: setting profileId
// to a valid id stores the row in profile mode (profile_id set, every
// inline field cleared). The response carries profileName populated
// from the lookup.
func TestSetRepositoryLLMOverride_ProfileMode_HappyPath(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pl := &fakeProfileLookup{present: map[string]string{
		"ca_llm_profile:default-migrated": "Default",
	}}
	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    pl,
	}}

	out, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		ProfileID: llmStrPtr("ca_llm_profile:default-migrated"),
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}
	if out.ProfileID == nil || *out.ProfileID != "ca_llm_profile:default-migrated" {
		t.Errorf("ProfileID: got %v, want ca_llm_profile:default-migrated", out.ProfileID)
	}
	if out.ProfileName == nil || *out.ProfileName != "Default" {
		t.Errorf("ProfileName: got %v, want Default (enriched via lookup)", out.ProfileName)
	}
	if out.Provider != nil || out.APIKeySet {
		t.Errorf("inline fields must be cleared in profile mode; got provider=%v apiKeySet=%v", out.Provider, out.APIKeySet)
	}

	// Verify the saved row carries only profile_id, no inline fields.
	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved == nil || saved.LLMOverride == nil {
		t.Fatalf("expected saved override; got %+v", saved)
	}
	if saved.LLMOverride.ProfileID != "ca_llm_profile:default-migrated" {
		t.Errorf("saved ProfileID: got %q, want ca_llm_profile:default-migrated", saved.LLMOverride.ProfileID)
	}
	if saved.LLMOverride.Provider != "" || saved.LLMOverride.APIKey != "" || saved.LLMOverride.SummaryModel != "" {
		t.Errorf("saved override has stray inline fields: %+v", saved.LLMOverride)
	}
}

// TestSetRepositoryLLMOverride_ProfileMode_DeletedProfileReturns409Style:
// the lookup returns exists=false → mutation aborts with a GraphQL
// error carrying extensions.code = "PROFILE_NO_LONGER_EXISTS" and the
// row is NOT mutated.
func TestSetRepositoryLLMOverride_ProfileMode_DeletedProfileBlocksSave(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	// Pre-seed an inline override so we can confirm it's preserved.
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			Provider: "ollama",
			APIKey:   "preserved-key",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pl := &fakeProfileLookup{present: map[string]string{}} // empty
	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    pl,
	}}

	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		ProfileID: llmStrPtr("ca_llm_profile:gone"),
	})
	if err == nil {
		t.Fatal("expected error for deleted profile reference")
	}
	var gqlErr *gqlerror.Error
	if !errors.As(err, &gqlErr) {
		t.Fatalf("expected gqlerror.Error, got %T: %v", err, err)
	}
	if code, _ := gqlErr.Extensions["code"].(string); code != "PROFILE_NO_LONGER_EXISTS" {
		t.Errorf("extension code: got %q, want PROFILE_NO_LONGER_EXISTS", code)
	}
	if id, _ := gqlErr.Extensions["profileId"].(string); id != "ca_llm_profile:gone" {
		t.Errorf("extension profileId: got %q, want ca_llm_profile:gone", id)
	}

	// Critical: pre-existing override must be UNCHANGED — the failed
	// mutation cannot leave the user with a degraded state.
	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved == nil || saved.LLMOverride == nil {
		t.Fatal("pre-existing override was destroyed by failed mutation")
	}
	if saved.LLMOverride.Provider != "ollama" || saved.LLMOverride.APIKey != "preserved-key" {
		t.Errorf("pre-existing override mutated: %+v", saved.LLMOverride)
	}
}

// TestSetRepositoryLLMOverride_ProfileMode_AtomicallyClearsInline: a
// patch that sends BOTH profileId AND inline fields produces a
// profile-mode row (inline fields are atomically cleared server-side).
// This is defense-in-depth: a stale UI / malformed client cannot
// produce a half-and-half row.
func TestSetRepositoryLLMOverride_ProfileMode_AtomicallyClearsInline(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pl := &fakeProfileLookup{present: map[string]string{
		"ca_llm_profile:foo": "Foo",
	}}
	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    pl,
	}}

	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		ProfileID:    llmStrPtr("ca_llm_profile:foo"),
		Provider:     llmStrPtr("ollama"),         // ignored
		APIKey:       llmStrPtr("inline-key"),     // ignored
		SummaryModel: llmStrPtr("inline-model"),   // ignored
		AdvancedMode: llmBoolPtr(true),            // ignored
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride.ProfileID != "ca_llm_profile:foo" {
		t.Errorf("ProfileID: got %q, want ca_llm_profile:foo", saved.LLMOverride.ProfileID)
	}
	if saved.LLMOverride.Provider != "" {
		t.Errorf("Provider must be cleared in profile mode; got %q", saved.LLMOverride.Provider)
	}
	if saved.LLMOverride.APIKey != "" {
		t.Errorf("APIKey must be cleared in profile mode; got %q", saved.LLMOverride.APIKey)
	}
	if saved.LLMOverride.SummaryModel != "" {
		t.Errorf("SummaryModel must be cleared in profile mode; got %q", saved.LLMOverride.SummaryModel)
	}
	if saved.LLMOverride.AdvancedMode {
		t.Errorf("AdvancedMode must be cleared in profile mode; got true")
	}
}

// TestSetRepositoryLLMOverride_ClearProfileFlag: clearProfile:true
// drops the saved profile_id (revert to inline mode); inline fields in
// the same patch are applied normally.
func TestSetRepositoryLLMOverride_ClearProfileFlag(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	// Pre-seed in profile mode.
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			ProfileID: "ca_llm_profile:default-migrated",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    &fakeProfileLookup{},
	}}

	out, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		ClearProfile: llmBoolPtr(true),
		Provider:     llmStrPtr("ollama"),
		SummaryModel: llmStrPtr("qwen2.5:32b"),
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}
	if out.ProfileID != nil {
		t.Errorf("ProfileID: got %v, want nil after clearProfile", out.ProfileID)
	}
	if out.Provider == nil || *out.Provider != "ollama" {
		t.Errorf("Provider: got %v, want ollama (inline applied)", out.Provider)
	}
	if out.SummaryModel == nil || *out.SummaryModel != "qwen2.5:32b" {
		t.Errorf("SummaryModel: got %v, want qwen2.5:32b (inline applied)", out.SummaryModel)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride.ProfileID != "" {
		t.Errorf("saved ProfileID: got %q, want empty after clearProfile", saved.LLMOverride.ProfileID)
	}
	if saved.LLMOverride.Provider != "ollama" {
		t.Errorf("saved Provider: got %q, want ollama", saved.LLMOverride.Provider)
	}
}

// TestSetRepositoryLLMOverride_ProfileIdNilPreservesSaved: omitting
// profileId in the patch leaves the saved value untouched. This matches
// the password-field UX precedent for apiKey: pointer-nil = leave
// alone; empty pointer = leave alone (no clear); only ClearProfile=true
// clears.
func TestSetRepositoryLLMOverride_ProfileIdNilPreservesSaved(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			ProfileID: "ca_llm_profile:default-migrated",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    &fakeProfileLookup{},
	}}

	// Send a patch with profileId omitted entirely (nil pointer).
	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		// nothing — patch is a no-op
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride.ProfileID != "ca_llm_profile:default-migrated" {
		t.Errorf("ProfileID: got %q, want preserved ca_llm_profile:default-migrated", saved.LLMOverride.ProfileID)
	}
}

// TestSetRepositoryLLMOverride_ProfileIdEmptyStringIsNoop: an empty
// pointer-to-empty-string for profileId is treated as omitted (no
// change) — explicit clear requires clearProfile:true.
func TestSetRepositoryLLMOverride_ProfileIdEmptyStringIsNoop(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			ProfileID: "ca_llm_profile:default-migrated",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    &fakeProfileLookup{},
	}}

	_, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		ProfileID: llmStrPtr(""), // explicit empty: still leave-alone
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}

	saved, _ := mem.GetRepoSettings(context.Background(), defaultTenantID, "repo-A")
	if saved.LLMOverride.ProfileID != "ca_llm_profile:default-migrated" {
		t.Errorf("ProfileID: got %q, want preserved (empty-string is leave-alone)", saved.LLMOverride.ProfileID)
	}
}

// TestLlmOverride_FieldResolver_PopulatesProfileName: the field
// resolver enriches profileName via LLMProfileLookup when the override
// is in profile mode.
func TestLlmOverride_FieldResolver_PopulatesProfileName(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			ProfileID: "ca_llm_profile:default-migrated",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pl := &fakeProfileLookup{present: map[string]string{
		"ca_llm_profile:default-migrated": "Default",
	}}
	r := &repositoryLivingWikiSettingsResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    pl,
	}}

	out, err := r.LlmOverride(context.Background(), &RepositoryLivingWikiSettings{RepoID: "repo-A"})
	if err != nil {
		t.Fatalf("LlmOverride: %v", err)
	}
	if out.ProfileID == nil || *out.ProfileID != "ca_llm_profile:default-migrated" {
		t.Errorf("ProfileID: got %v", out.ProfileID)
	}
	if out.ProfileName == nil || *out.ProfileName != "Default" {
		t.Errorf("ProfileName: got %v, want Default", out.ProfileName)
	}
	if pl.calls != 1 {
		t.Errorf("LookupProfileName calls: got %d, want 1", pl.calls)
	}
}

// TestLlmOverride_FieldResolver_DeletedProfileReturnsCodeAndData:
// when the saved profile is missing, the field resolver returns BOTH
// the override (so the UI can show profileId for the resolution panel)
// AND a GraphQL error with extensions.code = "PROFILE_NO_LONGER_EXISTS".
func TestLlmOverride_FieldResolver_DeletedProfileReturnsCodeAndData(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			ProfileID: "ca_llm_profile:gone",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pl := &fakeProfileLookup{present: map[string]string{}} // missing
	r := &repositoryLivingWikiSettingsResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    pl,
	}}

	out, err := r.LlmOverride(context.Background(), &RepositoryLivingWikiSettings{RepoID: "repo-A"})
	// Expect BOTH data + error — UI needs profileId to render the panel.
	if out == nil {
		t.Fatal("expected non-nil override even on deleted-profile path")
	}
	if out.ProfileID == nil || *out.ProfileID != "ca_llm_profile:gone" {
		t.Errorf("ProfileID must be returned even on deleted-profile path; got %v", out.ProfileID)
	}
	if out.ProfileName != nil {
		t.Errorf("ProfileName must be nil for a deleted profile; got %v", out.ProfileName)
	}
	if err == nil {
		t.Fatal("expected an error with PROFILE_NO_LONGER_EXISTS extension")
	}
	var gqlErr *gqlerror.Error
	if !errors.As(err, &gqlErr) {
		t.Fatalf("expected gqlerror.Error, got %T: %v", err, err)
	}
	if code, _ := gqlErr.Extensions["code"].(string); code != "PROFILE_NO_LONGER_EXISTS" {
		t.Errorf("extension code: got %q, want PROFILE_NO_LONGER_EXISTS", code)
	}
}

// TestLlmOverride_FieldResolver_InlineModeNoLookup: when the override
// is in inline mode (ProfileID empty), the field resolver does NOT
// invoke LLMProfileLookup — and the response carries no profileName.
func TestLlmOverride_FieldResolver_InlineModeNoLookup(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	pre := livingwiki.RepositoryLivingWikiSettings{
		TenantID: defaultTenantID,
		RepoID:   "repo-A",
		Mode:     livingwiki.RepoWikiModePRReview,
		Sinks:    []livingwiki.RepoWikiSink{},
		LLMOverride: &livingwiki.LLMOverride{
			Provider: "ollama",
			APIKey:   "key",
		},
	}
	if err := mem.SetRepoSettings(context.Background(), pre); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pl := &fakeProfileLookup{}
	r := &repositoryLivingWikiSettingsResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    pl,
	}}

	out, err := r.LlmOverride(context.Background(), &RepositoryLivingWikiSettings{RepoID: "repo-A"})
	if err != nil {
		t.Fatalf("LlmOverride: %v", err)
	}
	if out.ProfileID != nil {
		t.Errorf("ProfileID should be nil in inline mode; got %v", out.ProfileID)
	}
	if out.ProfileName != nil {
		t.Errorf("ProfileName should be nil in inline mode; got %v", out.ProfileName)
	}
	if pl.calls != 0 {
		t.Errorf("LookupProfileName should not be invoked in inline mode; got %d calls", pl.calls)
	}
}

// TestSetRepositoryLLMOverride_ProfileMode_NilLookupDegradesGracefully:
// when LLMProfileLookup is nil (e.g., embedded mode pre-slice-3
// wiring), the mutation skips validation and persists the row. This
// matches the resolver's nil-safe behavior and lets in-memory tests
// (which often skip the lookup wiring) still exercise the profile-mode
// patch path.
func TestSetRepositoryLLMOverride_ProfileMode_NilLookupDegradesGracefully(t *testing.T) {
	mem := livingwiki.NewRepoSettingsMemStore()
	r := &mutationResolver{Resolver: &Resolver{
		LivingWikiRepoStore: mem,
		LLMProfileLookup:    nil, // not wired
	}}

	out, err := r.SetRepositoryLLMOverride(context.Background(), "repo-A", RepositoryLLMOverrideInput{
		ProfileID: llmStrPtr("ca_llm_profile:foo"),
	})
	if err != nil {
		t.Fatalf("SetRepositoryLLMOverride: %v", err)
	}
	if out.ProfileID == nil || *out.ProfileID != "ca_llm_profile:foo" {
		t.Errorf("ProfileID: got %v", out.ProfileID)
	}
	// profileName isn't populated without a lookup — that's OK; the UI
	// renders profileId and falls back to a generic label.
	if out.ProfileName != nil {
		t.Errorf("ProfileName should be nil without lookup wiring; got %v", out.ProfileName)
	}
}

// TestLLMOverride_IsEmptyHonorsProfileID: regression test for the
// IsEmpty fix. A profile-mode override (only ProfileID set) is NOT
// empty; SetRepoSettings must persist the row.
func TestLLMOverride_IsEmptyHonorsProfileID(t *testing.T) {
	ov := &livingwiki.LLMOverride{ProfileID: "ca_llm_profile:foo"}
	if ov.IsEmpty() {
		t.Errorf("override with only ProfileID set must NOT be IsEmpty (else profile-mode saves get silently dropped)")
	}

	empty := &livingwiki.LLMOverride{}
	if !empty.IsEmpty() {
		t.Errorf("override with every field zero must be IsEmpty")
	}
}
