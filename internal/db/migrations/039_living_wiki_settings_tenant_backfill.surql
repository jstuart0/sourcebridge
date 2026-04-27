-- Migration 039: Backfill tenant_id on the existing lw_settings row.
--
-- Migration 036 added `tenant_id` to lw_settings (DEFAULT 'default') as
-- backwards-compat for the global settings record. SurrealDB DEFAULT only
-- applies on row creation, not retroactively, so the pre-existing
-- `lw_settings:default` row from migration 035 still has tenant_id = NONE.
-- Subsequent UPSERTs that omit tenant_id fail schema validation:
--
--   Found NONE for field `tenant_id`, with record `lw_settings:default`,
--   but expected a string
--
-- Backfill any required string fields whose DEFAULT clause never fired
-- because the row predates the migration that added them.
--
-- SurrealDB validates ALL schema fields on UPDATE — not just the ones
-- being assigned — so updating tenant_id alone fails when confluence_site
-- (added in migration 038) is also still NONE on the same record.
-- Fix: assign every at-risk field in one statement so post-write validation
-- sees a fully-populated row.

UPDATE lw_settings:default SET tenant_id = 'default', confluence_site = '';

-- Schema version marker
UPSERT schema_version:v39 SET version = 39;
