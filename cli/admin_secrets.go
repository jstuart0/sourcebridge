// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
)

// migrateLLMSecretsCmd re-encrypts ca_llm_config.api_key under the v1
// envelope. Idempotent — rows that already start with `sbenc:v1:` are
// left alone. Run once after rolling out slice 3 of the workspace-LLM-
// source-of-truth plan, then never again unless you rotate the
// encryption key (rotation procedure: deploy new key, re-save via
// /admin/llm OR run this command with the old key still configured to
// decrypt, set the new key, re-run to re-encrypt — the migration spec
// is captured in the followups doc).
//
// Read-only when no rows need migrating; one UPDATE per legacy row.
// Concurrent saves are not coordinated — if a user happens to save via
// /admin/llm during the migration, last-writer-wins on the row level
// (the SurrealDB UPSERT is atomic). The version stamp bumps on each
// save so resolvers will refetch.
var migrateLLMSecretsCmd = &cobra.Command{
	Use:   "migrate-llm-secrets",
	Short: "Re-encrypt the saved LLM api_key under the sbenc:v1 envelope",
	Long: `Re-encrypts the api_key column of the ca_llm_config workspace settings
record under the v1 envelope ("sbenc:v1:" + base64(nonce || ciphertext)).

Read the existing value transparently (encrypted or legacy plaintext),
then re-save under the configured SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY.

Idempotent: rows already encrypted under the v1 envelope are skipped.
The version stamp on the row is bumped on save so resolver instances
on other replicas pick up the change on their next probe.

Requires SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY to be set unless
SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true (OSS dev only).`,
	RunE: runMigrateLLMSecrets,
}

func runMigrateLLMSecrets(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Storage.SurrealMode != "external" {
		return fmt.Errorf("migrate-llm-secrets requires external SurrealDB (storage.surreal_mode=external); embedded mode has nothing to migrate")
	}

	surrealDB := db.NewSurrealDB(cfg.Storage)
	if err := surrealDB.Connect(context.Background()); err != nil {
		return fmt.Errorf("connect to SurrealDB: %w", err)
	}
	defer surrealDB.Close()

	store := db.NewSurrealLLMConfigStore(surrealDB,
		db.WithLLMConfigEncryptionKey(cfg.Security.EncryptionKey),
	)

	rec, err := store.LoadLLMConfig()
	if err != nil {
		return fmt.Errorf("load existing record: %w", err)
	}
	if rec == nil {
		fmt.Println("migrate-llm-secrets: no ca_llm_config row found; nothing to migrate")
		return nil
	}

	if rec.APIKey == "" {
		fmt.Println("migrate-llm-secrets: ca_llm_config.api_key is empty; nothing to encrypt")
		return nil
	}

	// Inspect the on-disk form. SurrealLLMConfigStore.LoadLLMConfig
	// already decrypted the value, but it doesn't tell us whether the
	// stored form was legacy plaintext or already-v1-encrypted.
	// EncryptedAPIKey returns the encrypted form for *any* plaintext
	// input, so we use it to produce the new on-disk value and let
	// SaveLLMConfig handle the actual write.
	if cfg.Security.EncryptionKey == "" {
		return fmt.Errorf("migrate-llm-secrets: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY must be set; refusing to migrate to a no-op encryption")
	}

	// Save will encrypt under the configured key.
	if err := store.SaveLLMConfig(rec); err != nil {
		return fmt.Errorf("re-save record: %w", err)
	}

	slog.Info("llm secrets migration complete",
		"provider", rec.Provider,
		"version_after_save", "version-bumped (resolvers on other replicas will refetch)")
	fmt.Println("migrate-llm-secrets: ca_llm_config.api_key re-encrypted under sbenc:v1 envelope")
	return nil
}

// migrateGitSecretsCmd re-encrypts ca_git_config.default_token under the
// v1 envelope. Idempotent — rows that already start with `sbenc:v1:` are
// left alone. Run once after rolling out R3 slice 2, then never again
// unless the encryption key rotates.
//
// Read-only when there is no plaintext row to migrate; one UPDATE per
// legacy row. Concurrent saves are not coordinated; the SurrealDB UPSERT
// is atomic and the version stamp bumps so resolvers on other replicas
// will refetch on their next probe.
var migrateGitSecretsCmd = &cobra.Command{
	Use:   "migrate-git-secrets",
	Short: "Re-encrypt the saved git default_token under the sbenc:v1 envelope",
	Long: `Re-encrypts the default_token column of the ca_git_config workspace
record under the v1 envelope ("sbenc:v1:" + base64(nonce || ciphertext)).

Read the existing value transparently (encrypted or legacy plaintext),
then re-save under the configured SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY.

Idempotent: rows already encrypted under the v1 envelope are skipped.
The version stamp on the row is bumped on save so resolver instances
on other replicas pick up the change on their next probe.

Requires SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY to be set unless
SOURCEBRIDGE_ALLOW_UNENCRYPTED_GIT_TOKEN=true (OSS dev only).`,
	RunE: runMigrateGitSecrets,
}

func runMigrateGitSecrets(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Storage.SurrealMode != "external" {
		return fmt.Errorf("migrate-git-secrets requires external SurrealDB (storage.surreal_mode=external); embedded mode has nothing to migrate")
	}

	surrealDB := db.NewSurrealDB(cfg.Storage)
	if err := surrealDB.Connect(context.Background()); err != nil {
		return fmt.Errorf("connect to SurrealDB: %w", err)
	}
	defer surrealDB.Close()

	// Build the cipher under the configured key (or refuse if neither
	// the key nor the OSS escape hatch is on).
	cipher := secretcipher.NewAESGCMCipher(cfg.Security.EncryptionKey, false)
	if !cipher.HasKey() {
		return fmt.Errorf("migrate-git-secrets: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY must be set; refusing to migrate to a no-op encryption")
	}

	store := db.NewSurrealGitConfigStore(surrealDB, db.WithGitConfigCipher(cipher))
	if err := store.MigrateGitSecrets(context.Background()); err != nil {
		return fmt.Errorf("migrate-git-secrets: %w", err)
	}

	slog.Info("git secrets migration complete (idempotent — already-encrypted rows skipped, plaintext rows re-saved with version bumped)")
	fmt.Println("migrate-git-secrets: ca_git_config.default_token now under sbenc:v1 envelope (or empty)")
	return nil
}

func init() {
	rootCmd.AddCommand(migrateLLMSecretsCmd)
	rootCmd.AddCommand(migrateGitSecretsCmd)
}
