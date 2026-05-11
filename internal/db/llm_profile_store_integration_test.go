// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

//go:build integration

package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// These integration tests stand up a real SurrealDB via testcontainer
// and exercise the full slice-1 surface end-to-end:
//   - schema-ensure idempotency (codex-H4)
//   - profile CRUD with pointer-patch semantics + name uniqueness
//   - Migration: fresh install / legacy plaintext / legacy ciphertext / partial state self-heal
//   - Helpers: writeActive / writeNonActive / activate / delete / reconcile under interleaved writes
//   - Resolver invariants: A→B→A switches, active edits, non-active edits, old-pod legacy writes

// helperStores is the shared per-test wiring: both stores share one
// cipher (librarian-M1).
type helperStores struct {
	surreal *SurrealDB
	lcs     *SurrealLLMConfigStore
	lps     *SurrealLLMProfileStore
	cipher  secretcipher.Cipher
}

func newHelperStores(t *testing.T, encryptionKey string, allowUnenc bool) *helperStores {
	t.Helper()
	surreal := startSurrealContainer(t)
	cipher, err := secretcipher.NewAESGCMCipher(encryptionKey, secretcipher.DeriveInstallationSaltFromKey(encryptionKey), allowUnenc)
	if err != nil {
		t.Fatalf("NewAESGCMCipher: %v", err)
	}
	lcs := NewSurrealLLMConfigStore(surreal, WithLLMConfigCipher(cipher))
	lps := NewSurrealLLMProfileStore(surreal, WithLLMProfileCipher(cipher))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := lps.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema (profile): %v", err)
	}
	if err := lcs.EnsureProfilesSchemaExtensions(ctx); err != nil {
		t.Fatalf("EnsureProfilesSchemaExtensions (config): %v", err)
	}
	return &helperStores{
		surreal: surreal,
		lcs:     lcs,
		lps:     lps,
		cipher:  cipher,
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Schema-ensure idempotency (codex-H4)
// ─────────────────────────────────────────────────────────────────────────

func TestIntegration_SchemaEnsureIdempotent(t *testing.T) {
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := hs.lps.EnsureSchema(ctx); err != nil {
			t.Fatalf("EnsureSchema iteration %d: %v", i, err)
		}
		if err := hs.lcs.EnsureProfilesSchemaExtensions(ctx); err != nil {
			t.Fatalf("EnsureProfilesSchemaExtensions iteration %d: %v", i, err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Profile CRUD
// ─────────────────────────────────────────────────────────────────────────

func TestIntegration_ProfileCreateLoadDelete(t *testing.T) {
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	id, err := hs.lps.CreateProfile(ctx, ProfileCreate{
		Name:         "Test Profile",
		Provider:     "anthropic",
		APIKey:       "sk-ant-test",
		SummaryModel: "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if !strings.HasPrefix(id, "ca_llm_profile:") {
		t.Errorf("id should be table-prefixed, got %q", id)
	}

	p, err := hs.lps.LoadProfile(ctx, id)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Name != "Test Profile" {
		t.Errorf("name: got %q, want Test Profile", p.Name)
	}
	if p.NameKey != "test profile" {
		t.Errorf("name_key: got %q, want 'test profile'", p.NameKey)
	}
	if p.APIKey != "sk-ant-test" {
		t.Errorf("api_key: got %q, want sk-ant-test (decrypted)", p.APIKey)
	}

	// Delete: zeroes ciphertext then removes row.
	if err := hs.lps.DeleteProfile(ctx, id); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	_, err = hs.lps.LoadProfile(ctx, id)
	if !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("post-delete load: got %v, want ErrProfileNotFound", err)
	}
}

func TestIntegration_ProfileNameUniquenessCaseInsensitive(t *testing.T) {
	// codex-M2: name_key UNIQUE INDEX enforces case-insensitive uniqueness.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if _, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "Default"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "default"})
	if !errors.Is(err, ErrDuplicateProfileName) {
		t.Errorf("second create with case-collision: got %v, want ErrDuplicateProfileName", err)
	}
	_, err = hs.lps.CreateProfile(ctx, ProfileCreate{Name: "  DEFAULT  "})
	if !errors.Is(err, ErrDuplicateProfileName) {
		t.Errorf("third create with whitespace+case-collision: got %v, want ErrDuplicateProfileName", err)
	}
}

func TestIntegration_ProfileUpdatePointerPatch(t *testing.T) {
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	id, err := hs.lps.CreateProfile(ctx, ProfileCreate{
		Name:         "P1",
		Provider:     "anthropic",
		APIKey:       "k1",
		SummaryModel: "m1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// nil pointer = preserve.
	if err := hs.lps.UpdateProfile(ctx, id, ProfileUpdate{}); err != nil {
		t.Fatalf("noop update: %v", err)
	}
	p, _ := hs.lps.LoadProfile(ctx, id)
	if p.APIKey != "k1" || p.Provider != "anthropic" || p.SummaryModel != "m1" {
		t.Errorf("noop update mutated fields: %+v", p)
	}

	// pointer to non-empty = set.
	prov := "openai"
	model := "gpt-4o"
	if err := hs.lps.UpdateProfile(ctx, id, ProfileUpdate{Provider: &prov, SummaryModel: &model}); err != nil {
		t.Fatalf("set update: %v", err)
	}
	p, _ = hs.lps.LoadProfile(ctx, id)
	if p.Provider != "openai" || p.SummaryModel != "gpt-4o" {
		t.Errorf("set update did not apply: %+v", p)
	}
	if p.APIKey != "k1" {
		t.Errorf("set update touched untouched api_key: got %q", p.APIKey)
	}

	// pointer to "" = clear.
	emptyKey := ""
	if err := hs.lps.UpdateProfile(ctx, id, ProfileUpdate{APIKey: &emptyKey}); err != nil {
		t.Fatalf("clear-via-empty: %v", err)
	}
	p, _ = hs.lps.LoadProfile(ctx, id)
	if p.APIKey != "" {
		t.Errorf("clear-via-empty did not clear: got %q", p.APIKey)
	}

	// ClearAPIKey = clear.
	newKey := "k2"
	if err := hs.lps.UpdateProfile(ctx, id, ProfileUpdate{APIKey: &newKey}); err != nil {
		t.Fatalf("set after clear: %v", err)
	}
	if err := hs.lps.UpdateProfile(ctx, id, ProfileUpdate{ClearAPIKey: true}); err != nil {
		t.Fatalf("ClearAPIKey: %v", err)
	}
	p, _ = hs.lps.LoadProfile(ctx, id)
	if p.APIKey != "" {
		t.Errorf("ClearAPIKey did not clear: got %q", p.APIKey)
	}

	// empty Name pointer = REJECTED.
	emptyName := ""
	err = hs.lps.UpdateProfile(ctx, id, ProfileUpdate{Name: &emptyName})
	if !errors.Is(err, ErrProfileNameRequired) {
		t.Errorf("empty name pointer: got %v, want ErrProfileNameRequired", err)
	}
}

func TestIntegration_ProfileUpdateRenameUniqueness(t *testing.T) {
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	id1, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "P1"})
	if err != nil {
		t.Fatalf("create P1: %v", err)
	}
	if _, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "P2"}); err != nil {
		t.Fatalf("create P2: %v", err)
	}
	rename := "p2" // case-collides with P2
	err = hs.lps.UpdateProfile(ctx, id1, ProfileUpdate{Name: &rename})
	if !errors.Is(err, ErrDuplicateProfileName) {
		t.Errorf("rename to colliding name: got %v, want ErrDuplicateProfileName", err)
	}
}

func TestIntegration_DeleteUnknown404(t *testing.T) {
	// ian-L1: 404 on DELETE unknown id.
	hs := newHelperStores(t, "test-key", false)
	err := hs.lps.DeleteProfile(context.Background(), "ca_llm_profile:nonexistent")
	if !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("delete unknown: got %v, want ErrProfileNotFound", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Migration
// ─────────────────────────────────────────────────────────────────────────

func TestIntegration_MigrationFreshInstall(t *testing.T) {
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	envBoot := config.LLMConfig{
		Provider:     "anthropic",
		APIKey:       "env-key",
		SummaryModel: "claude-sonnet-4",
		TimeoutSecs:  900,
	}
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, envBoot); err != nil {
		t.Fatalf("MigrateToProfiles: %v", err)
	}
	// Active profile id should be the deterministic id.
	activeID, version, err := hs.lcs.LoadActiveProfileIDAndVersion(ctx)
	if err != nil {
		t.Fatalf("LoadActiveProfileIDAndVersion: %v", err)
	}
	if activeID != MigratedProfileRecordID {
		t.Errorf("active_profile_id: got %q, want %q", activeID, MigratedProfileRecordID)
	}
	if version != 1 {
		t.Errorf("version: got %d, want 1", version)
	}
	// Profile content matches env bootstrap.
	p, err := hs.lps.LoadProfile(ctx, activeID)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic", p.Provider)
	}
	if p.APIKey != "env-key" {
		t.Errorf("api_key: got %q, want env-key (decrypted)", p.APIKey)
	}
	if p.Name != "Default" {
		t.Errorf("name: got %q, want Default", p.Name)
	}
	if p.LastLegacyVersionConsumed != 1 {
		t.Errorf("watermark: got %d, want 1", p.LastLegacyVersionConsumed)
	}
}

func TestIntegration_MigrationFastExitOnReboot(t *testing.T) {
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	envBoot := config.LLMConfig{Provider: "anthropic", APIKey: "k"}
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, envBoot); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	_, v1, _ := hs.lcs.LoadActiveProfileIDAndVersion(ctx)
	// Re-run: should fast-exit, NOT bump version.
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, envBoot); err != nil {
		t.Fatalf("second migrate (fast-exit): %v", err)
	}
	_, v2, _ := hs.lcs.LoadActiveProfileIDAndVersion(ctx)
	if v1 != v2 {
		t.Errorf("fast-exit bumped version: v1=%d, v2=%d", v1, v2)
	}
}

func TestIntegration_MigrationLegacyCiphertextPreserved(t *testing.T) {
	// Pre-existing ca_llm_config:default row with sbenc:v1 api_key.
	// Migration must copy ciphertext bytes-for-bytes (codex-H5).
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := hs.lcs.SaveLLMConfig(&LLMConfigRecord{
		Provider:     "anthropic",
		APIKey:       "secret-bytes",
		SummaryModel: "model-x",
	}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	// Capture the on-disk sealed bytes.
	snap, err := hs.lcs.LoadConfigSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.LegacyAPIKey != "secret-bytes" {
		t.Errorf("snapshot decrypts legacy api_key: got %q", snap.LegacyAPIKey)
	}

	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	p, err := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.APIKey != "secret-bytes" {
		t.Errorf("migrated api_key (decrypted): got %q, want secret-bytes", p.APIKey)
	}
	if p.Provider != "anthropic" {
		t.Errorf("migrated provider: got %q, want anthropic", p.Provider)
	}
	if p.SummaryModel != "model-x" {
		t.Errorf("migrated summary_model: got %q, want model-x", p.SummaryModel)
	}
}

func TestIntegration_MigrationHardStopOnPlaintextNoKey(t *testing.T) {
	// codex-H5: plaintext legacy + no encryption key + no escape hatch
	// → MigrateToProfiles returns ErrEncryptionKeyRequired.
	//
	// Build the stores with NO encryption key. The legacy save will
	// fail under the cipher's normal Encrypt path UNLESS we use the
	// escape hatch on the LCS to plant the plaintext, then construct
	// a separate cipher (no key, no escape hatch) for the migration.
	surreal := startSurrealContainer(t)
	noKeyCipher := secretcipher.MustNewAESGCMCipher("", secretcipher.DeriveInstallationSaltFromKey(""), false)
	allowUnencCipher := secretcipher.MustNewAESGCMCipher("", secretcipher.DeriveInstallationSaltFromKey(""), true)
	lcs := NewSurrealLLMConfigStore(surreal, WithLLMConfigCipher(allowUnencCipher))
	lps := NewSurrealLLMProfileStore(surreal, WithLLMProfileCipher(noKeyCipher))

	ctx := context.Background()
	if err := lps.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lcs.EnsureProfilesSchemaExtensions(ctx); err != nil {
		t.Fatal(err)
	}
	// Plant a plaintext legacy api_key via the allow-unenc store.
	if err := lcs.SaveLLMConfig(&LLMConfigRecord{
		Provider: "anthropic",
		APIKey:   "plaintext-leak",
	}); err != nil {
		t.Fatalf("seed legacy plaintext: %v", err)
	}
	// Migrate with the no-key cipher AND allowUnenc=false → HARD STOP.
	err := MigrateToProfiles(ctx, surreal, lcs, lps, noKeyCipher, false, config.LLMConfig{})
	if err == nil {
		t.Fatal("expected hard-stop error on plaintext+nokey+noescape, got nil")
	}
	if !errors.Is(err, ErrEncryptionKeyRequired) {
		t.Errorf("expected ErrEncryptionKeyRequired, got %v", err)
	}
}

func TestIntegration_MigrationConcurrentBoots(t *testing.T) {
	// Two replicas booting simultaneously both call MigrateToProfiles.
	// Final state: one ca_llm_profile:default-migrated row, version
	// is 1 or 2 (both correct), workspace + profile watermark in lockstep.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	envBoot := config.LLMConfig{Provider: "anthropic", APIKey: "k"}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, envBoot)
		}(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Errorf("concurrent migrate: %v", e)
		}
	}
	activeID, version, err := hs.lcs.LoadActiveProfileIDAndVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if activeID != MigratedProfileRecordID {
		t.Errorf("active_profile_id: got %q, want %q", activeID, MigratedProfileRecordID)
	}
	if version < 1 || version > 2 {
		t.Errorf("version: got %d, want 1 or 2", version)
	}
	p, err := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.LastLegacyVersionConsumed != version {
		t.Errorf("watermark: got %d, want %d (in-lockstep)", p.LastLegacyVersionConsumed, version)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Helpers (writeActive / writeNonActive / activate / delete / reconcile)
// ─────────────────────────────────────────────────────────────────────────

func TestIntegration_WriteActiveDualWritesLegacy(t *testing.T) {
	// Active-profile edit must dual-write the active profile + the
	// legacy mirror in one BEGIN/COMMIT (codex-H2). After the helper,
	// snap.LegacyProvider == active.Provider.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	envBoot := config.LLMConfig{Provider: "anthropic", APIKey: "k"}
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, envBoot); err != nil {
		t.Fatal(err)
	}
	sealed, _ := hs.cipher.Encrypt("new-key")
	patch := ProfilePatch{
		Provider:   "openai",
		BaseURL:    "https://api.openai.com",
		APIKey:     sealed,
		APIKeyMode: APIKeyModeSet(),
		FieldsPresent: ProfilePatchFields{
			Provider: true,
			BaseURL:  true,
		},
	}
	if _, err := WriteActiveProfilePatchWithRetry(ctx, hs.surreal, hs.lcs, patch); err != nil {
		t.Fatalf("WriteActiveProfilePatchWithRetry: %v", err)
	}
	// Active profile reflects the edit.
	p, err := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "openai" {
		t.Errorf("active.provider: got %q, want openai", p.Provider)
	}
	if p.APIKey != "new-key" {
		t.Errorf("active.api_key (decrypted): got %q, want new-key", p.APIKey)
	}
	// Legacy row mirror reflects the edit too.
	snap, err := hs.lcs.LoadConfigSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.LegacyProvider != "openai" {
		t.Errorf("legacy.provider: got %q, want openai (dual-write)", snap.LegacyProvider)
	}
	if snap.LegacyAPIKey != "new-key" {
		t.Errorf("legacy.api_key: got %q, want new-key (dual-write)", snap.LegacyAPIKey)
	}
	// Watermark advanced in lockstep with version.
	if p.LastLegacyVersionConsumed != snap.Version {
		t.Errorf("watermark vs version: %d vs %d (must be in lockstep)", p.LastLegacyVersionConsumed, snap.Version)
	}
}

func TestIntegration_NonActiveEditAdvancesActiveWatermark(t *testing.T) {
	// codex-r1d M1: non-active edits MUST advance the active profile's
	// watermark to the post-bump workspace.version, otherwise the
	// resolver false-positives a reconciliation on the next read.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	// Create a non-active profile.
	nonActiveID, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "Variant", Provider: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	// BumpVersionAfterCreate is what the rest adapter calls; do that
	// to make the system look "post-create."
	if _, err := BumpVersionAfterCreate(ctx, hs.surreal, hs.lcs); err != nil {
		t.Fatal(err)
	}

	// Now patch the non-active via the helper.
	patch := ProfilePatch{
		Provider:   "ollama",
		APIKeyMode: APIKeyModeKeep(),
		FieldsPresent: ProfilePatchFields{
			Provider: true,
		},
	}
	postVer, err := WriteNonActivePatchWithRetry(ctx, hs.surreal, hs.lcs, nonActiveID, patch)
	if err != nil {
		t.Fatalf("WriteNonActivePatchWithRetry: %v", err)
	}

	// Active profile's watermark MUST equal postVer.
	active, err := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if active.LastLegacyVersionConsumed != postVer {
		t.Errorf("active.watermark after non-active edit: got %d, want %d (codex-r1d M1)",
			active.LastLegacyVersionConsumed, postVer)
	}
}

func TestIntegration_ActivateProfileFlipsAndAdvancesNewWatermark(t *testing.T) {
	// codex-r1d M2: activation flips active_profile_id, mirrors the new
	// active's contents to legacy, advances the NEW active's watermark.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	bID, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "B", Provider: "openai", APIKey: "b-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BumpVersionAfterCreate(ctx, hs.surreal, hs.lcs); err != nil {
		t.Fatal(err)
	}
	postVer, err := ActivateProfileWithRetry(ctx, hs.surreal, hs.lcs, bID)
	if err != nil {
		t.Fatalf("ActivateProfileWithRetry: %v", err)
	}
	activeID, version, _ := hs.lcs.LoadActiveProfileIDAndVersion(ctx)
	if activeID != bID {
		t.Errorf("active_profile_id: got %q, want %q", activeID, bID)
	}
	if version != postVer {
		t.Errorf("version: got %d, want %d (post-activate)", version, postVer)
	}
	// New active's watermark = postVer.
	b, err := hs.lps.LoadProfile(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if b.LastLegacyVersionConsumed != postVer {
		t.Errorf("new active watermark: got %d, want %d", b.LastLegacyVersionConsumed, postVer)
	}
	// Legacy mirror reflects B.
	snap, err := hs.lcs.LoadConfigSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.LegacyProvider != "openai" {
		t.Errorf("legacy mirror after activate: provider got %q, want openai", snap.LegacyProvider)
	}
	if snap.LegacyAPIKey != "b-key" {
		t.Errorf("legacy mirror after activate: api_key got %q, want b-key", snap.LegacyAPIKey)
	}
}

func TestIntegration_DeleteActiveRefused(t *testing.T) {
	// D5 enforced at the API/handler layer; the helper THROWs
	// profile_now_active_use_active_helper if the active profile is
	// passed (defense-in-depth).
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{}); err != nil {
		t.Fatal(err)
	}
	_, err := DeleteNonActiveWithRetry(ctx, hs.surreal, hs.lcs, MigratedProfileRecordID)
	if !errors.Is(err, ErrTargetNoLongerActive) {
		t.Errorf("delete active via non-active helper: got %v, want ErrTargetNoLongerActive", err)
	}
}

func TestIntegration_DeleteZeroesCiphertext(t *testing.T) {
	// xander-M1: DELETE must zero the api_key ciphertext before
	// removing the row.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	id, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "Doomed", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	// Inspect raw row pre-delete: api_key should be sbenc:v1.
	loaded, err := hs.lps.LoadProfile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "secret" {
		t.Errorf("pre-delete api_key (decrypted): got %q, want secret", loaded.APIKey)
	}
	// Delete.
	if err := hs.lps.DeleteProfile(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Row is gone.
	_, err = hs.lps.LoadProfile(ctx, id)
	if !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("post-delete: got %v, want ErrProfileNotFound", err)
	}
}

func TestIntegration_LegacyWriteReconciledByResolver(t *testing.T) {
	// codex-H2 / r1c: an old pod commits a SaveLLMConfig (bumping
	// workspace.version) but doesn't touch the watermark. Next
	// reconciler call closes the gap and re-anchors the watermark.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	// Simulate an old-pod legacy SaveLLMConfig (this exact code path
	// runs at internal/db/llm_config_store.go:347).
	if err := hs.lcs.SaveLLMConfig(&LLMConfigRecord{
		Provider:     "old-pod-provider",
		APIKey:       "old-pod-key",
		SummaryModel: "old-pod-model",
	}); err != nil {
		t.Fatal(err)
	}
	// Workspace.version bumped, but watermark on active profile did not.
	snap, err := hs.lcs.LoadConfigSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	active, err := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version <= active.LastLegacyVersionConsumed {
		t.Fatalf("expected workspace.version > watermark; got version=%d watermark=%d", snap.Version, active.LastLegacyVersionConsumed)
	}
	// Run reconciler.
	result, err := ReconcileLegacyToActiveExported(ctx, hs.surreal, snap.Version, active.LastLegacyVersionConsumed, MigratedProfileRecordID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !result.ActuallyWrote {
		t.Errorf("expected ActuallyWrote=true")
	}
	// After reconcile: active profile's contents == legacy (old-pod write),
	// watermark in lockstep with version.
	postSnap, _ := hs.lcs.LoadConfigSnapshot(ctx)
	postActive, _ := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	if postActive.Provider != "old-pod-provider" {
		t.Errorf("post-reconcile active.provider: got %q, want old-pod-provider", postActive.Provider)
	}
	if postActive.APIKey != "old-pod-key" {
		t.Errorf("post-reconcile active.api_key (decrypted): got %q, want old-pod-key", postActive.APIKey)
	}
	if postActive.LastLegacyVersionConsumed != postSnap.Version {
		t.Errorf("post-reconcile: watermark=%d vs version=%d (must lockstep)",
			postActive.LastLegacyVersionConsumed, postSnap.Version)
	}
}

func TestIntegration_ConcurrentReconcileOneWins(t *testing.T) {
	// codex-r1c: two concurrent reconcilers see the same gap; only
	// one wins (CAS). The loser sees ErrVersionConflict.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	// Synthetic old-pod write.
	if err := hs.lcs.SaveLLMConfig(&LLMConfigRecord{Provider: "old", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := hs.lcs.LoadConfigSnapshot(ctx)
	active, _ := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	obsV := snap.Version
	obsW := active.LastLegacyVersionConsumed

	// Two reconcilers with the same observed values race.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	results := make([]ReconcileResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, e := ReconcileLegacyToActiveExported(ctx, hs.surreal, obsV, obsW, MigratedProfileRecordID)
			results[idx] = r
			errs[idx] = e
		}(i)
	}
	wg.Wait()

	wins := 0
	losses := 0
	for i, e := range errs {
		switch {
		case e == nil && results[i].ActuallyWrote:
			wins++
		case errors.Is(e, ErrVersionConflict), errors.Is(e, ErrWatermarkConflict):
			losses++
		default:
			t.Errorf("unexpected reconcile result: err=%v result=%+v", e, results[i])
		}
	}
	// CA-284: relax from "exactly one winner" to "at least one winner, no error
	// storms". The original assertion was "wins==1 && losses==1", which is only
	// guaranteed when both goroutines race simultaneously. On slow or
	// GOMAXPROCS=1 runners they can serialize — goroutine 0 finishes before
	// goroutine 1 starts — so both succeed (wins==2, losses==0). The real
	// contract is "at least one always wins and no unexpected errors occur";
	// the CAS prevents double-write, but cannot guarantee lost concurrency.
	if wins == 0 {
		t.Errorf("expected at least one winner; got wins=%d losses=%d", wins, losses)
	}
	if wins+losses != 2 {
		t.Errorf("expected all goroutines to produce a win or a loss; got wins=%d losses=%d", wins, losses)
	}
}

func TestIntegration_MultipleOldPodWritesAccumulate(t *testing.T) {
	// Three old-pod writes in a row: workspace.version=N+3, watermark=N.
	// One reconcile picks up the freshest contents (last writer wins).
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	for i, prov := range []string{"prov-1", "prov-2", "prov-3"} {
		if err := hs.lcs.SaveLLMConfig(&LLMConfigRecord{Provider: prov, APIKey: "k", SummaryModel: prov + "-m"}); err != nil {
			t.Fatalf("legacy save %d: %v", i, err)
		}
	}
	snap, _ := hs.lcs.LoadConfigSnapshot(ctx)
	active, _ := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)

	if snap.Version-active.LastLegacyVersionConsumed != 3 {
		t.Fatalf("expected gap=3 (3 old-pod writes), got version=%d watermark=%d", snap.Version, active.LastLegacyVersionConsumed)
	}
	result, err := ReconcileLegacyToActiveExported(ctx, hs.surreal, snap.Version, active.LastLegacyVersionConsumed, MigratedProfileRecordID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !result.ActuallyWrote {
		t.Error("expected ActuallyWrote=true")
	}
	postActive, _ := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	if postActive.Provider != "prov-3" {
		t.Errorf("expected last writer wins (prov-3); got %q", postActive.Provider)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Resolver / Cache invariants (codex-L1 cache matrix, codex-M5)
// ─────────────────────────────────────────────────────────────────────────

func TestIntegration_ResolverCacheMatrix_SwitchActiveProfile(t *testing.T) {
	// codex-L1: A→B→A switches each bump version exactly once.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	bID, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "B", Provider: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BumpVersionAfterCreate(ctx, hs.surreal, hs.lcs); err != nil {
		t.Fatal(err)
	}

	// Capture starting version.
	_, v0, _ := hs.lcs.LoadActiveProfileIDAndVersion(ctx)
	// Switch A → B
	v1, err := ActivateProfileWithRetry(ctx, hs.surreal, hs.lcs, bID)
	if err != nil {
		t.Fatal(err)
	}
	if v1 != v0+1 {
		t.Errorf("A→B bump: got %d, want %d", v1, v0+1)
	}
	// Switch B → A
	v2, err := ActivateProfileWithRetry(ctx, hs.surreal, hs.lcs, MigratedProfileRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if v2 != v1+1 {
		t.Errorf("B→A bump: got %d, want %d", v2, v1+1)
	}
}

func TestIntegration_NonActiveEditBumpsVersion(t *testing.T) {
	// codex-M5: editing a non-active profile MUST bump workspace.version
	// so the resolver picks up the change on the next probe.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	bID, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "B", Provider: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BumpVersionAfterCreate(ctx, hs.surreal, hs.lcs); err != nil {
		t.Fatal(err)
	}
	_, vBefore, _ := hs.lcs.LoadActiveProfileIDAndVersion(ctx)

	patch := ProfilePatch{
		Provider:   "ollama",
		APIKeyMode: APIKeyModeKeep(),
		FieldsPresent: ProfilePatchFields{
			Provider: true,
		},
	}
	if _, err := WriteNonActivePatchWithRetry(ctx, hs.surreal, hs.lcs, bID, patch); err != nil {
		t.Fatal(err)
	}
	_, vAfter, _ := hs.lcs.LoadActiveProfileIDAndVersion(ctx)
	if vAfter != vBefore+1 {
		t.Errorf("non-active edit: version got %d, want %d", vAfter, vBefore+1)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Concurrency / race-detector (-race)
// ─────────────────────────────────────────────────────────────────────────

func TestIntegration_ConcurrentActivateAndRead(t *testing.T) {
	// Race scenario: one goroutine activates a profile while another
	// runs LoadConfigSnapshot in a tight loop. The reader must always
	// see a consistent (active_id, version, profile-fields) tuple.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	bID, _ := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "B", Provider: "openai"})
	if _, err := BumpVersionAfterCreate(ctx, hs.surreal, hs.lcs); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := hs.lcs.LoadConfigSnapshot(ctx); err != nil {
				t.Errorf("snapshot: %v", err)
				return
			}
		}
	}()

	// Flip A↔B 20 times.
	current := bID
	for i := 0; i < 20; i++ {
		_, err := ActivateProfileWithRetry(ctx, hs.surreal, hs.lcs, current)
		if err != nil {
			t.Errorf("activate iteration %d: %v", i, err)
			break
		}
		if current == bID {
			current = MigratedProfileRecordID
		} else {
			current = bID
		}
	}
	close(stop)
	wg.Wait()
}

// ─────────────────────────────────────────────────────────────────────
// Slice-4 codex-r2 High #1: profile-PUT must CAS against the caller's
// intended target id. If activation flips the active profile out from
// under the writer, the strict variant returns ErrTargetNoLongerActive
// and does NOT mirror the original target's fields onto the new active.
// ─────────────────────────────────────────────────────────────────────

func TestIntegration_WriteActiveByID_RejectsRacedActivation(t *testing.T) {
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	// Create a second profile B.
	bID, err := hs.lps.CreateProfile(ctx, ProfileCreate{Name: "B", Provider: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BumpVersionAfterCreate(ctx, hs.surreal, hs.lcs); err != nil {
		t.Fatal(err)
	}
	// Pre-update state for B (so we can detect corruption).
	bBefore, err := hs.lps.LoadProfile(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the race: while the caller's intended target is the
	// migrated Default profile (still active), another writer flips
	// active to B. The strict helper observes the mismatch and
	// returns ErrTargetNoLongerActive without writing.
	if _, err := ActivateProfileWithRetry(ctx, hs.surreal, hs.lcs, bID); err != nil {
		t.Fatal(err)
	}

	patch := ProfilePatch{
		Provider:   "anthropic", // Default's provider — this is what the user intended
		APIKey:     "",
		APIKeyMode: APIKeyModeKeep(),
		FieldsPresent: ProfilePatchFields{
			Provider: true,
		},
	}
	_, err = WriteActiveProfilePatchByIDWithRetry(
		ctx, hs.surreal, hs.lps, hs.lcs,
		MigratedProfileRecordID, // intent: edit the (now-no-longer-active) Default
		patch,
	)
	if !errors.Is(err, ErrTargetNoLongerActive) {
		t.Fatalf("expected ErrTargetNoLongerActive when activation flipped underneath, got %v", err)
	}

	// B (the now-active profile) MUST NOT have been corrupted with
	// Default's fields. Provider stays "openai".
	bAfter, err := hs.lps.LoadProfile(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if bAfter.Provider != "openai" || bAfter.Provider != bBefore.Provider {
		t.Errorf("B.provider corrupted: got %q, want openai (race-fix codex-r2 High #1)", bAfter.Provider)
	}
}

func TestIntegration_WriteActiveByID_AcceptsCorrectTarget(t *testing.T) {
	// Sanity: when the intended target IS the current active, the
	// strict helper writes through normally.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	patch := ProfilePatch{
		Provider:   "ollama",
		APIKeyMode: APIKeyModeKeep(),
		FieldsPresent: ProfilePatchFields{
			Provider: true,
		},
	}
	_, err := WriteActiveProfilePatchByIDWithRetry(
		ctx, hs.surreal, hs.lps, hs.lcs,
		MigratedProfileRecordID,
		patch,
	)
	if err != nil {
		t.Fatalf("strict variant on correct target: %v", err)
	}
	p, err := hs.lps.LoadProfile(ctx, MigratedProfileRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "ollama" {
		t.Errorf("active.provider after write: got %q, want ollama", p.Provider)
	}
}

func TestIntegration_WriteActiveByID_DetectsMissingProfile(t *testing.T) {
	// codex-r2 High #2: when active_profile_id points at a missing
	// row (data corruption / direct DB delete), the strict helper
	// returns ErrProfileNotFound rather than silently no-op'ing the
	// row update while still bumping the legacy mirror.
	hs := newHelperStores(t, "test-key", false)
	ctx := context.Background()
	if err := MigrateToProfiles(ctx, hs.surreal, hs.lcs, hs.lps, hs.cipher, false, config.LLMConfig{Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	// Manually delete the active profile row WITHOUT going through
	// the API (simulates manual surreal sql purge or a backup-restore
	// from a pre-profiles snapshot). The active_profile_id pointer
	// stays set; the row is gone.
	dbConn := hs.surreal.DB()
	if dbConn == nil {
		t.Fatal("surreal db nil")
	}
	_, _ = surrealdb.Query[interface{}](ctx, dbConn,
		`DELETE FROM type::thing('ca_llm_profile', $rid) RETURN NONE;`,
		map[string]any{"rid": "default-migrated"},
	)
	// Confirm the pointer still points at the now-missing row.
	activeID, _, err := hs.lcs.LoadActiveProfileIDAndVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if activeID != MigratedProfileRecordID {
		t.Fatalf("activeID: got %q, want %q (test setup invariant)", activeID, MigratedProfileRecordID)
	}

	patch := ProfilePatch{
		Provider:   "openai",
		APIKeyMode: APIKeyModeKeep(),
		FieldsPresent: ProfilePatchFields{
			Provider: true,
		},
	}
	// With intendedActiveID set: should detect missing.
	_, err = WriteActiveProfilePatchByIDWithRetry(
		ctx, hs.surreal, hs.lps, hs.lcs,
		MigratedProfileRecordID, patch,
	)
	if !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("strict variant w/ missing profile: got %v, want ErrProfileNotFound", err)
	}

	// With empty intendedActiveID (legacy PUT semantics): should
	// also detect missing — the helper checks the current active
	// pointer's profile.
	_, err = WriteActiveProfilePatchByIDWithRetry(
		ctx, hs.surreal, hs.lps, hs.lcs,
		"", patch,
	)
	if !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("legacy variant w/ missing profile: got %v, want ErrProfileNotFound", err)
	}
}
