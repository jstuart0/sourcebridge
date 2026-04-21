-- Migration 032: Enforce DB-level uniqueness of (repo_id, external_id) on
-- ca_requirement. Closes the check-then-act race in the app-layer
-- GetRequirementByExternalID precheck.
--
-- Trashed rows are naturally disambiguated: internal/trash/surrealstore.go
-- rewrites external_id to `<original>::trashed::<uuid8>` when a requirement
-- is moved to trash, and preserves the original value in
-- ca_requirement.original_external_id for restore. A plain UNIQUE index
-- across all rows is therefore safe — only live rows share the flat
-- external_id namespace; trashed rows are guaranteed unique by the uuid8
-- suffix.
--
-- This migration runs on every API startup (the migrate path always
-- replays every file; see internal/db/surreal.go:Migrate). All
-- statements below are idempotent: REMOVE INDEX IF EXISTS, DEFINE INDEX
-- IF NOT EXISTS, UPSERT.
--
-- If a fresh install of this migration fails because of pre-existing
-- duplicate active rows, operator recovery is:
--
--   SELECT repo_id, external_id, count() AS n, array::group(id) AS ids
--   FROM ca_requirement
--   WHERE deleted_at IS NONE
--   GROUP BY repo_id, external_id
--   HAVING count() > 1;
--
-- then hand-edit the newer rows to append a disambiguator suffix.
-- The deployed thor installation as of 2026-04-21 has no such duplicates
-- (verified via the query above).

REMOVE INDEX IF EXISTS idx_ca_requirement_external ON ca_requirement;

DEFINE INDEX IF NOT EXISTS uniq_ca_requirement_external
    ON ca_requirement FIELDS repo_id, external_id UNIQUE;

UPSERT schema_version:v32 SET version = 32;
