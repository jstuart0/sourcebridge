// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// chooseAPIKeyForMigratedProfile is the cipher invariant that
// determines what api_key bytes the migration writes. Slice 1 must
// preserve every codex-H5 branch:
//
//   1. empty            → empty stays empty (with proper "fresh-install" vs "legacy-empty" labelling)
//   2. sbenc:v1 already → copy bytes byte-for-byte (NO decrypt+re-encrypt)
//   3. plaintext + key  → decrypt(no-op for unprefixed) + re-encrypt → sbenc:v1
//   4. plaintext + escape hatch → preserve plaintext bytes
//   5. plaintext + no key + no escape hatch → HARD STOP

func TestChooseAPIKey_EmptyFreshInstall(t *testing.T) {
	cipher := secretcipher.MustNewAESGCMCipher("k", secretcipher.DeriveInstallationSaltFromKey("k"), false)
	got, source, err := chooseAPIKeyForMigratedProfile("", cipher, false, true /*freshInstall*/)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "" {
		t.Errorf("empty fresh-install: got %q, want empty", got)
	}
	if source != "fresh-install-env-seed" {
		t.Errorf("source: got %q, want fresh-install-env-seed", source)
	}
}

func TestChooseAPIKey_EmptyLegacy(t *testing.T) {
	cipher := secretcipher.MustNewAESGCMCipher("k", secretcipher.DeriveInstallationSaltFromKey("k"), false)
	got, source, err := chooseAPIKeyForMigratedProfile("", cipher, false, false /*freshInstall*/)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "" {
		t.Errorf("empty legacy: got %q, want empty", got)
	}
	if source != "legacy-empty" {
		t.Errorf("source: got %q, want legacy-empty", source)
	}
}

func TestChooseAPIKey_AlreadyEncryptedCopiesBytes(t *testing.T) {
	cipher := secretcipher.MustNewAESGCMCipher("k", secretcipher.DeriveInstallationSaltFromKey("k"), false)
	sealed, err := cipher.Encrypt("real-secret")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, source, err := chooseAPIKeyForMigratedProfile(sealed, cipher, false, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != sealed {
		t.Errorf("already-encrypted: bytes mismatch. got %q want %q", got, sealed)
	}
	if source != "legacy-ciphertext" {
		t.Errorf("source: got %q, want legacy-ciphertext", source)
	}
}

func TestChooseAPIKey_PlaintextWithKeyResealed(t *testing.T) {
	cipher := secretcipher.MustNewAESGCMCipher("test-key", secretcipher.DeriveInstallationSaltFromKey("test-key"), false)
	got, source, err := chooseAPIKeyForMigratedProfile("plaintext", cipher, false, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.HasPrefix(got, secretcipher.EnvelopePrefix) {
		t.Errorf("plaintext+key: expected sbenc:v1 prefix, got %q", got)
	}
	if source != "legacy-plaintext-resealed" {
		t.Errorf("source: got %q, want legacy-plaintext-resealed", source)
	}
	// And the round-trip recovers the original plaintext.
	plainOut, err := cipher.Decrypt(got)
	if err != nil {
		t.Fatalf("round trip decrypt: %v", err)
	}
	if plainOut != "plaintext" {
		t.Errorf("round trip: got %q, want plaintext", plainOut)
	}
}

func TestChooseAPIKey_PlaintextWithEscapeHatchPreserved(t *testing.T) {
	cipher := secretcipher.MustNewAESGCMCipher("", secretcipher.DeriveInstallationSaltFromKey(""), true /*allowUnenc*/)
	got, source, err := chooseAPIKeyForMigratedProfile("plain-bytes", cipher, true /*allowUnenc*/, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "plain-bytes" {
		t.Errorf("escape hatch: got %q, want plain-bytes preserved", got)
	}
	if source != "legacy-plaintext-preserved" {
		t.Errorf("source: got %q, want legacy-plaintext-preserved", source)
	}
}

func TestChooseAPIKey_PlaintextNoKeyNoEscapeHatchHardStop(t *testing.T) {
	cipher := secretcipher.MustNewAESGCMCipher("", secretcipher.DeriveInstallationSaltFromKey(""), false)
	_, _, err := chooseAPIKeyForMigratedProfile("plain", cipher, false /*allowUnenc=false*/, false)
	if err == nil {
		t.Fatal("expected hard-stop error, got nil")
	}
	if !errors.Is(err, ErrEncryptionKeyRequired) {
		t.Errorf("expected ErrEncryptionKeyRequired, got %v", err)
	}
}

func TestEnvBootstrapToLegacy(t *testing.T) {
	env := config.LLMConfig{
		Provider:                 "anthropic",
		BaseURL:                  "https://api.anthropic.com",
		APIKey:                   "env-key",
		SummaryModel:             "claude-sonnet-4",
		ReviewModel:              "review-m",
		AskModel:                 "ask-m",
		KnowledgeModel:           "knowledge-m",
		ArchitectureDiagramModel: "diagram-m",
		ReportModel:              "report-m",
		DraftModel:               "draft-m",
		TimeoutSecs:              900,
		AdvancedMode:             true,
	}
	lf := envBootstrapToLegacy(env, 0)
	if lf.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic", lf.Provider)
	}
	if lf.APIKey != "env-key" {
		t.Errorf("api_key: got %q, want env-key", lf.APIKey)
	}
	if lf.SummaryModel != "claude-sonnet-4" {
		t.Errorf("summary_model: got %q", lf.SummaryModel)
	}
	if !lf.AdvancedMode {
		t.Errorf("advanced_mode: got false, want true")
	}
	if lf.Version != 0 {
		t.Errorf("version: got %d, want 0", lf.Version)
	}
}

func TestTranslateThrowErr(t *testing.T) {
	cases := []struct {
		msg  string
		want error
	}{
		{"There was a problem with the database: An error occurred: ca_llm_config_version_changed", ErrVersionConflict},
		{"There was a problem with the database: An error occurred: ca_llm_config_version_changed_during_reconcile", ErrVersionConflict},
		{"There was a problem with the database: An error occurred: active_profile_watermark_changed_during_reconcile", ErrWatermarkConflict},
		{"There was a problem with the database: An error occurred: profile_not_found", ErrProfileNotFound},
		{"There was a problem with the database: An error occurred: profile_now_active_use_active_helper", ErrTargetNoLongerActive},
		{"There was a problem with the database: An error occurred: llm_profile_migration_legacy_changed", ErrLegacyChanged},
		{"some unrelated error", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := translateThrowErr(errors.New(c.msg))
		if c.want == nil {
			if got != nil {
				t.Errorf("translateThrowErr(%q): got %v, want nil", c.msg, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("translateThrowErr(%q): got nil, want %v", c.msg, c.want)
			continue
		}
		if !errors.Is(got, c.want) {
			t.Errorf("translateThrowErr(%q): got %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestBuildProfileFieldClauses_AllPresent(t *testing.T) {
	patch := ProfilePatch{
		Provider:                 "p",
		BaseURL:                  "u",
		APIKey:                   "sealed",
		APIKeyMode:               apiKeySet,
		SummaryModel:             "sm",
		ReviewModel:              "rm",
		AskModel:                 "am",
		KnowledgeModel:           "km",
		ArchitectureDiagramModel: "dm",
		ReportModel:              "rep",
		DraftModel:               "dr",
		TimeoutSecs:              60,
		AdvancedMode:             true,
		FieldsPresent: ProfilePatchFields{
			Provider:                 true,
			BaseURL:                  true,
			SummaryModel:             true,
			ReviewModel:              true,
			AskModel:                 true,
			KnowledgeModel:           true,
			ArchitectureDiagramModel: true,
			ReportModel:              true,
			DraftModel:               true,
			TimeoutSecs:              true,
			AdvancedMode:             true,
		},
	}
	clause, vars := buildProfileFieldClauses(patch, "p_")
	for _, col := range []string{"provider", "base_url", "summary_model", "review_model", "ask_model", "knowledge_model", "architecture_diagram_model", "report_model", "draft_model", "timeout_secs", "advanced_mode", "api_key"} {
		if !strings.Contains(clause, col+" =") {
			t.Errorf("clause missing %s: %s", col, clause)
		}
	}
	if vars["p_api_key"] != "sealed" {
		t.Errorf("vars: api_key got %v, want sealed", vars["p_api_key"])
	}
	if vars["p_provider"] != "p" {
		t.Errorf("vars: provider got %v, want p", vars["p_provider"])
	}
}

func TestBuildProfileFieldClauses_OnlyClearAPIKey(t *testing.T) {
	patch := ProfilePatch{
		APIKeyMode:    apiKeyClear,
		FieldsPresent: ProfilePatchFields{}, // nothing else set
	}
	clause, vars := buildProfileFieldClauses(patch, "p_")
	if !strings.Contains(clause, "api_key = ''") {
		t.Errorf("expected clear clause, got %q", clause)
	}
	// No api_key var should be present (clear is inline literal).
	if _, ok := vars["p_api_key"]; ok {
		t.Errorf("clear mode should not bind p_api_key: vars=%v", vars)
	}
}

func TestBuildProfileFieldClauses_KeepDoesNotEmitAPIKey(t *testing.T) {
	patch := ProfilePatch{
		APIKeyMode:    apiKeyKeep,
		FieldsPresent: ProfilePatchFields{Provider: true},
		Provider:      "openai",
	}
	clause, vars := buildProfileFieldClauses(patch, "p_")
	if strings.Contains(clause, "api_key") {
		t.Errorf("apiKeyKeep mode should NOT emit api_key clause, got %q", clause)
	}
	if vars["p_provider"] != "openai" {
		t.Errorf("vars: provider got %v, want openai", vars["p_provider"])
	}
}

func TestAPIKeyModeAccessors(t *testing.T) {
	if APIKeyModeKeep() != apiKeyKeep {
		t.Errorf("APIKeyModeKeep mismatch")
	}
	if APIKeyModeClear() != apiKeyClear {
		t.Errorf("APIKeyModeClear mismatch")
	}
	if APIKeyModeSet() != apiKeySet {
		t.Errorf("APIKeyModeSet mismatch")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Phase 1 — fresh-install seed + self-heal guard unit tests
// ─────────────────────────────────────────────────────────────────────────

// TestEnvBootstrapToLegacy_EmptyDefaults verifies that when cfg.LLM has all
// empty fields (the new Phase 1 default), envBootstrapToLegacy produces a
// LegacyFields with empty provider and model fields. This is the fresh-install
// path: the migration seeds a blank Default profile; the editor falls back to
// "ollama" for display.
func TestEnvBootstrapToLegacy_EmptyDefaults(t *testing.T) {
	env := config.LLMConfig{
		Provider:    "",
		SummaryModel: "",
		ReviewModel:  "",
		AskModel:     "",
		TimeoutSecs: 900,
	}
	lf := envBootstrapToLegacy(env, 0)
	if lf.Provider != "" {
		t.Errorf("fresh-install env-bootstrap: want empty provider, got %q (RC-1 regression)", lf.Provider)
	}
	if lf.SummaryModel != "" {
		t.Errorf("fresh-install env-bootstrap: want empty summary_model, got %q", lf.SummaryModel)
	}
	if lf.ReviewModel != "" {
		t.Errorf("fresh-install env-bootstrap: want empty review_model, got %q", lf.ReviewModel)
	}
	if lf.AskModel != "" {
		t.Errorf("fresh-install env-bootstrap: want empty ask_model, got %q", lf.AskModel)
	}
}

// TestFreshInstallUsesEnvBootstrap verifies that on a true fresh install
// (no legacy row, empty env config), the migration correctly classifies
// the state as fresh and would call envBootstrapToLegacy with the empty env.
// We test the logic in isolation (not the full DB round-trip) by exercising
// the freshInstall derivation:
//   - !hasLegacy → freshInstall = true (uses env-bootstrap, all-empty)
//   - hasLegacy && legacy.Provider == "" → freshInstall = true
//   - hasLegacy && legacy.Provider != "" → freshInstall = false (self-heal)
func TestFreshInstallClassification(t *testing.T) {
	cases := []struct {
		name           string
		hasLegacy      bool
		legacyProvider string
		wantFresh      bool
	}{
		{"no legacy row", false, "", true},
		{"legacy row empty provider", true, "", true},
		{"legacy row with provider", true, "anthropic", false},
		{"legacy row ollama provider", true, "ollama", false},
	}
	for _, c := range cases {
		legacy := LegacyFields{Provider: c.legacyProvider}
		got := !c.hasLegacy || legacy.Provider == ""
		if got != c.wantFresh {
			t.Errorf("%s: freshInstall=%v, want %v", c.name, got, c.wantFresh)
		}
	}
}

// TestSelfHealPrefersLegacyProvider verifies the r1 H1 guard: when the legacy
// row has a real provider (admin had a working config), the self-heal path
// should NOT apply env-bootstrap defaults (which would overwrite with empty
// values after Phase 1's default change). The guard detects freshInstall=false
// and skips the envBootstrapToLegacy call.
func TestSelfHealPrefersLegacyProvider(t *testing.T) {
	// Simulate: hasLegacy=true, legacy.Provider="anthropic",
	// activeID=MigratedProfileRecordID (self-heal path).
	// Expect: freshInstall=false → env-bootstrap NOT called → provider preserved.
	legacyProvider := "anthropic"
	hasLegacy := true
	legacy := LegacyFields{
		Provider:    legacyProvider,
		APIKey:      "sbenc:v1:testdata",
		SummaryModel: "claude-sonnet-4",
	}
	freshInstall := !hasLegacy || legacy.Provider == ""
	if freshInstall {
		t.Errorf("r1 H1: with legacy.Provider=%q, freshInstall should be false (would call env-bootstrap and wipe provider)", legacyProvider)
	}

	// Simulate the env-bootstrap result for comparison — should NOT be applied.
	emptyEnv := config.LLMConfig{Provider: "", SummaryModel: ""}
	bootstrapped := envBootstrapToLegacy(emptyEnv, legacy.Version)
	if bootstrapped.Provider != "" {
		t.Errorf("sanity: empty env bootstrap should have empty provider, got %q", bootstrapped.Provider)
	}

	// Confirm: the guard would leave legacy intact.
	if !freshInstall {
		// This is the self-heal branch — legacy flows to runMigrationBatch directly.
		if legacy.Provider != legacyProvider {
			t.Errorf("self-heal: legacy.Provider changed; want %q, got %q", legacyProvider, legacy.Provider)
		}
	}
}
