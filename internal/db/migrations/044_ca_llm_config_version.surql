-- 044_ca_llm_config_version.surql
--
-- Adds a monotonic `version` field to the workspace LLM config record.
-- Bumped on every Save by the application so resolver instances on
-- other replicas can detect cross-replica saves without polling.
--
-- Existing rows backfill to 1 so the resolver's cache (which compares
-- "has the version changed since I last fetched?") fires once on the
-- first post-deploy resolve and then stays stable.
--
-- Plan: thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth.md slice 1.

DEFINE FIELD IF NOT EXISTS version ON ca_llm_config TYPE int DEFAULT 0;

-- Backfill any existing row(s) with version 0 → 1 so the resolver does
-- a first fetch on deploy. Rows with non-zero versions are left alone
-- (idempotent re-runs of this migration must not perturb live state).
UPDATE ca_llm_config SET version = 1 WHERE version = NONE OR version = 0;
