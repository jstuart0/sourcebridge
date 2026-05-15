-- Migration 059: Add version column to lw_repo_settings for optimistic
-- concurrency control (CA-158).
--
-- The column is TYPE int DEFAULT 0. Existing rows are backfilled to 0 so
-- they compare cleanly with the "first write after upgrade" path, which
-- calls SetRepoSettingsIfVersion with expectedVersion==0 (unconditional).
-- Subsequent writes increment the counter via `version = version + 1`.

DEFINE FIELD IF NOT EXISTS version ON TABLE lw_repo_settings TYPE int DEFAULT 0;

-- Backfill pre-059 rows to version = 0 so they are readable as "no
-- version yet" by the optimistic-concurrency helpers.
UPDATE lw_repo_settings SET version = 0 WHERE version IS NONE;

UPSERT schema_version:v59 SET version = 59;
