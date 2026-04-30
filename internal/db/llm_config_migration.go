// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/surrealdb/surrealdb.go"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// MigratedProfileRecordID is the deterministic record id used by
// MigrateToProfiles for the seeded "Default" profile (bob-H1: a fixed
// id makes UPSERT idempotent across concurrent boots; no UNIQUE-name
// retry chatter).
const MigratedProfileRecordID = "ca_llm_profile:default-migrated"
const migratedProfileShortID = "default-migrated"

// migrationMaxRetries caps the number of times MigrateToProfiles will
// retry on ErrLegacyChanged (old pod committed between step-2 read and
// the BEGIN/COMMIT batch). Beyond this, the boot fails fast — the
// operator must investigate a high-contention legacy row before this
// version can come up. (codex-r1d-NEW-H.)
const migrationMaxRetries = 3

// MigrateToProfiles is the boot-time, idempotent migration that seeds a
// "Default" profile from the legacy `ca_llm_config:default` row (or from
// cfg.LLM env-bootstrap on a fresh install) and publishes
// `active_profile_id = ca_llm_profile:default-migrated` atomically with
// a workspace.version bump. Runs from cli/serve.go AFTER all three
// schema-ensure steps and BEFORE the resolver / REST handlers mount.
//
// The migration is unconditional (codex-H1): every boot of the new
// code creates a Default profile if none exists. The page UI therefore
// never has to render an empty-profiles state. After the first
// successful run, subsequent boots fast-exit at step 1.
//
// allowUnenc maps to the OSS escape hatch
// (SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true). If the legacy api_key
// is unprefixed plaintext AND the cipher has no key configured AND
// allowUnenc is false, the migration HARD-STOPS with
// ErrEncryptionKeyRequired (codex-H5).
//
// envBoot is captured by VALUE so any subsequent mutation of cfg.LLM
// after this call cannot affect the migration's seed values.
func MigrateToProfiles(
	ctx context.Context,
	surrealDB *SurrealDB,
	lcs *SurrealLLMConfigStore,
	lps *SurrealLLMProfileStore,
	cipher secretcipher.Cipher,
	allowUnenc bool,
	envBoot config.LLMConfig,
) error {
	if surrealDB == nil || lcs == nil || lps == nil {
		return fmt.Errorf("llm profile migration: stores not configured")
	}
	if cipher == nil {
		return fmt.Errorf("llm profile migration: cipher not configured")
	}

	for attempt := 0; attempt < migrationMaxRetries; attempt++ {
		// Step 1: fast-exit if the migration has already completed AND the
		// profile still exists. This is the common case on every reboot.
		// On the retry path (attempt > 0), this also fast-exits when a
		// concurrent new-code-boot winner has already published the
		// deterministic pointer + profile pair (codex-r1e M2).
		activeID, _, err := lcs.LoadActiveProfileIDAndVersion(ctx)
		if err != nil {
			return fmt.Errorf("llm profile migration: load active profile id/version: %w", err)
		}
		if activeID != "" {
			_, loadErr := lps.LoadProfile(ctx, activeID)
			if loadErr == nil {
				if attempt == 0 {
					slog.Debug("llm profile migration: fast-exit (active profile present)",
						"active_profile_id", activeID)
				} else {
					slog.Info("llm profile migration: concurrent winner published profile; fast-exiting",
						"active_profile_id", activeID)
				}
				return nil
			}
			if !errors.Is(loadErr, ErrProfileNotFound) {
				return fmt.Errorf("llm profile migration: load active profile %s: %w", activeID, loadErr)
			}
			// active_profile_id points at a missing profile.
			if activeID != MigratedProfileRecordID {
				// This is admin-repair-required. We do not auto-heal a
				// non-deterministic pointer because we can't safely
				// reconstruct that row's intended contents.
				slog.Error("llm profile migration: active_profile_id points at a non-recoverable missing profile; admin repair required",
					"active_profile_id", activeID)
				return nil
			}
			// Self-heal path (codex-r1c-NEW): pointer is the deterministic
			// id but its row is missing. Fall through to the BEGIN/COMMIT
			// below, which is idempotent on the deterministic id.
			slog.Warn("llm profile migration: self-healing partial state (pointer set but ca_llm_profile:default-migrated missing); re-running migration batch",
				"active_profile_id", activeID)
		}

		// Step 2: read legacy fields (may be empty on fresh install).
		// Capture observedLegacyVersion IMMEDIATELY so any env-bootstrap
		// substitution below doesn't shadow a legitimate legacy version
		// (codex-r1e M3).
		legacy, hasLegacy, err := lcs.LoadLegacyFieldsRaw(ctx)
		if err != nil {
			return fmt.Errorf("llm profile migration: load legacy fields: %w", err)
		}
		observedLegacyVersion := uint64(0)
		if hasLegacy {
			observedLegacyVersion = legacy.Version
		}

		// Step 3: fresh-install / never-configured path. Seed Default
		// from cfg.LLM env-bootstrap. Empty values are fine; the UI
		// pre-fills whatever env supplied (or all-empty if no env).
		freshInstall := !hasLegacy || legacy.Provider == ""
		if freshInstall {
			legacy = envBootstrapToLegacy(envBoot, legacy.Version)
			// On true fresh install (no legacy row), observedLegacyVersion
			// is 0 — that's correct for the CAS guard, since the existing
			// row will be NONE and the batch's IF check tolerates it.
		}

		// Step 4-5: choose the api_key form for the migrated profile.
		// codex-H5 three-branch handling:
		//   a) empty                 → empty stays empty
		//   b) sbenc:v1 ciphertext   → copy bytes as-is (NO decrypt+re-encrypt)
		//   c) unprefixed plaintext  → decrypt (returns same bytes) + re-encrypt
		//                              under cipher.Encrypt; HARD STOP if no key
		//                              and no escape hatch
		apiKeyForProfile, source, encErr := chooseAPIKeyForMigratedProfile(legacy.APIKey, cipher, allowUnenc, freshInstall)
		if encErr != nil {
			return fmt.Errorf("llm profile migration: cannot establish encryption-at-rest invariant for legacy api_key: %w", encErr)
		}

		// Step 6-7: single CAS-guarded BEGIN/COMMIT (codex-r1c + r1d).
		// Atomic across: workspace version bump, active_profile_id
		// pointer publication, ca_llm_profile:default-migrated UPSERT,
		// watermark assignment.
		err = runMigrationBatch(ctx, surrealDB, observedLegacyVersion, hasLegacy, legacy, apiKeyForProfile)
		if errors.Is(err, ErrLegacyChanged) {
			slog.Warn("llm profile migration: legacy row changed mid-migration; retrying from step 1",
				"observed_legacy_version", observedLegacyVersion,
				"attempt", attempt+1)
			continue
		}
		if err != nil {
			return fmt.Errorf("llm profile migration: batch failed: %w", err)
		}

		slog.Info("llm profile migration: complete",
			"profile_id", MigratedProfileRecordID,
			"source", source,
			"fresh_install", freshInstall,
			"observed_legacy_version", observedLegacyVersion)
		return nil
	}

	return fmt.Errorf("llm profile migration: exhausted %d retries on legacy row contention; investigate concurrent legacy writers", migrationMaxRetries)
}

// runMigrationBatch executes the BEGIN/COMMIT batch that seeds the
// Default profile and publishes the active_profile_id pointer. The CAS
// guard is on `existing.version == observed_legacy_version` — if an
// old pod committed a SaveLLMConfig between step 2's read and this
// batch, the THROW signals the caller to retry (codex-r1d-NEW).
func runMigrationBatch(
	ctx context.Context,
	surrealDB *SurrealDB,
	observedLegacyVersion uint64,
	hasLegacy bool,
	legacy LegacyFields,
	apiKeyForProfile string,
) error {
	db := surrealDB.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// The CAS guard tolerates a missing ca_llm_config:default row
	// (fresh install). hasLegacy=false → existing.version is NONE; the
	// IF check uses `$existing.version == NONE OR $existing.version == $observed_legacy_version`
	// to allow the first migration on an empty DB to proceed without
	// a phantom CAS failure.
	sql := `
		BEGIN;
		LET $existing = (SELECT version FROM ca_llm_config:default)[0];
		IF $existing != NONE AND $existing.version != $observed_legacy_version {
			THROW "llm_profile_migration_legacy_changed";
		};
		LET $cur_version = IF $existing != NONE THEN $existing.version ELSE 0 END;
		LET $new_version = $cur_version + 1;
		UPSERT type::thing('ca_llm_profile', $migrated_short_id) SET
			name                         = "Default",
			name_key                     = "default",
			provider                     = $legacy_provider,
			base_url                     = $legacy_base_url,
			api_key                      = $api_key_for_profile,
			summary_model                = $legacy_summary_model,
			review_model                 = $legacy_review_model,
			ask_model                    = $legacy_ask_model,
			knowledge_model              = $legacy_knowledge_model,
			architecture_diagram_model   = $legacy_architecture_diagram_model,
			report_model                 = $legacy_report_model,
			draft_model                  = $legacy_draft_model,
			timeout_secs                 = $legacy_timeout_secs,
			advanced_mode                = $legacy_advanced_mode,
			created_at                   = (IF created_at != NONE THEN created_at ELSE type::datetime($now) END),
			updated_at                   = type::datetime($now),
			last_legacy_version_consumed = $new_version;
		UPSERT ca_llm_config:default SET
			active_profile_id          = $migrated_full_id,
			version                    = $new_version,
			updated_at                 = type::datetime($now),
			provider                   = $legacy_provider,
			base_url                   = $legacy_base_url,
			api_key                    = $api_key_for_profile,
			summary_model              = $legacy_summary_model,
			review_model               = $legacy_review_model,
			ask_model                  = $legacy_ask_model,
			knowledge_model            = $legacy_knowledge_model,
			architecture_diagram_model = $legacy_architecture_diagram_model,
			report_model               = $legacy_report_model,
			draft_model                = $legacy_draft_model,
			timeout_secs               = $legacy_timeout_secs,
			advanced_mode              = $legacy_advanced_mode;
		COMMIT;
	`
	vars := map[string]any{
		"observed_legacy_version":           observedLegacyVersion,
		"migrated_short_id":                 migratedProfileShortID,
		"migrated_full_id":                  MigratedProfileRecordID,
		"now":                               now,
		"legacy_provider":                   legacy.Provider,
		"legacy_base_url":                   legacy.BaseURL,
		"api_key_for_profile":               apiKeyForProfile,
		"legacy_summary_model":              legacy.SummaryModel,
		"legacy_review_model":               legacy.ReviewModel,
		"legacy_ask_model":                  legacy.AskModel,
		"legacy_knowledge_model":            legacy.KnowledgeModel,
		"legacy_architecture_diagram_model": legacy.ArchitectureDiagramModel,
		"legacy_report_model":               legacy.ReportModel,
		"legacy_draft_model":                legacy.DraftModel,
		"legacy_timeout_secs":               legacy.TimeoutSecs,
		"legacy_advanced_mode":              legacy.AdvancedMode,
	}
	raw, err := surrealdb.Query[interface{}](ctx, db, sql, vars)
	if err != nil {
		if typed := translateThrowErr(err); typed != nil {
			return typed
		}
		return err
	}
	if raw == nil {
		return nil
	}
	for _, qr := range *raw {
		if qr.Error != nil {
			errStr := fmt.Sprintf("%v", qr.Error)
			if typed := translateThrowErr(errors.New(errStr)); typed != nil {
				return typed
			}
			return fmt.Errorf("migration batch query error: %s", errStr)
		}
	}
	return nil
}

// chooseAPIKeyForMigratedProfile decides what bytes to write into the
// new profile's api_key field based on the legacy stored value, the
// cipher's view of it, and the OSS escape hatch.
//
// Returns the chosen bytes plus a "source" label for logging:
//   - "fresh-install-env-seed"        — empty-and-skipped path
//   - "legacy-empty"                  — empty stays empty
//   - "legacy-ciphertext"             — sbenc:v1 already; copy as-is
//   - "legacy-plaintext-resealed"     — plaintext + key configured; re-encrypted
//   - "legacy-plaintext-preserved"    — plaintext + escape hatch on; preserved
//
// Returns ErrEncryptionKeyRequired when legacy is plaintext, no key is
// configured, AND the escape hatch is OFF (codex-H5 hard stop).
func chooseAPIKeyForMigratedProfile(legacyAPIKey string, cipher secretcipher.Cipher, allowUnenc bool, freshInstall bool) (string, string, error) {
	if legacyAPIKey == "" {
		if freshInstall {
			return "", "fresh-install-env-seed", nil
		}
		return "", "legacy-empty", nil
	}
	if cipher.IsEnvelopeEncrypted(legacyAPIKey) {
		// Already in sbenc:v1. Copy bytes as-is — do NOT decrypt+re-encrypt
		// (a re-encrypt under a rotated key would silently break older
		// readers, and is not the migration's job — that's `migrate-llm-secrets`).
		return legacyAPIKey, "legacy-ciphertext", nil
	}
	// Unprefixed legacy plaintext (or an env-seeded plaintext from a
	// fresh-install path). Decrypt is a no-op here (Cipher.Decrypt
	// returns unprefixed input as-is per the secretcipher contract);
	// then attempt to encrypt under the configured key.
	plaintext, decErr := cipher.Decrypt(legacyAPIKey)
	if decErr != nil {
		// Should be unreachable for unprefixed input; defensive.
		return "", "", fmt.Errorf("decrypt legacy api_key: %w", decErr)
	}
	sealed, encErr := cipher.Encrypt(plaintext)
	if encErr != nil {
		if errors.Is(encErr, secretcipher.ErrEncryptionKeyRequired) {
			if allowUnenc {
				// OSS escape hatch: preserve plaintext bytes. Reachable when
				// the cipher has no key AND its own allowUnencrypted flag
				// is OFF (defensive — in normal wiring the cipher's flag
				// matches allowUnenc, so Encrypt would have succeeded
				// below, not raised this error).
				slog.Warn("llm profile migration: SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true — migrating legacy plaintext api_key as plaintext (OSS dev only; rotate via migrate-llm-secrets)")
				return legacyAPIKey, "legacy-plaintext-preserved", nil
			}
			return "", "", ErrEncryptionKeyRequired
		}
		return "", "", fmt.Errorf("encrypt legacy api_key: %w", encErr)
	}
	// When the cipher has no key but allowUnencrypted=true, Encrypt
	// returns the plaintext bytes unchanged (no envelope prefix).
	// Distinguish by checking the envelope.
	if !cipher.IsEnvelopeEncrypted(sealed) {
		slog.Warn("llm profile migration: cipher returned unencrypted bytes (escape hatch active); legacy api_key preserved as plaintext (OSS dev only; rotate via migrate-llm-secrets)")
		return sealed, "legacy-plaintext-preserved", nil
	}
	return sealed, "legacy-plaintext-resealed", nil
}

// envBootstrapToLegacy materializes a LegacyFields struct from cfg.LLM
// env-bootstrap fields. Used when no legacy row exists (fresh install)
// so the migration can seed the Default profile with whatever values
// env-vars supplied. The api_key from env is plaintext at this point;
// it goes through chooseAPIKeyForMigratedProfile which handles the
// encrypt-or-escape-hatch decision.
//
// observedVersion is forwarded so the caller's CAS guard reads the same
// value it captured from LoadLegacyFieldsRaw — typically 0 on fresh
// install, but a synthetic test could set it differently.
func envBootstrapToLegacy(env config.LLMConfig, observedVersion uint64) LegacyFields {
	return LegacyFields{
		Provider:                 env.Provider,
		BaseURL:                  env.BaseURL,
		APIKey:                   env.APIKey,
		SummaryModel:             env.SummaryModel,
		ReviewModel:              env.ReviewModel,
		AskModel:                 env.AskModel,
		KnowledgeModel:           env.KnowledgeModel,
		ArchitectureDiagramModel: env.ArchitectureDiagramModel,
		ReportModel:              env.ReportModel,
		DraftModel:               env.DraftModel,
		TimeoutSecs:              env.TimeoutSecs,
		AdvancedMode:             env.AdvancedMode,
		Version:                  observedVersion,
	}
}

// strSafe is used internally by debug log lines to avoid leaking large
// values into structured logs (kept short).
func strSafe(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}

var _ = strSafe // currently unused; reserved for future debug fields
