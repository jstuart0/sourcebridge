-- 048_git_config_encryption.surql
--
-- Adds a monotonic `version` field to the workspace git config record so
-- the multi-replica resolver can detect cross-replica saves via a cheap
-- version-cell read (mirrors the LLM config resolver's pattern from
-- migration 044). Existing rows backfill to 1 so the resolver fetches
-- once on first post-deploy resolve and then stays stable.
--
-- The `default_token` field stays a string; its on-disk shape transitions
-- from plaintext to "sbenc:v1:..." after the first save under the
-- post-R3 code path. Reads remain compatible with both forms (sbenc:v1
-- envelope, or unprefixed legacy plaintext that the load path returns
-- as-is + emits a one-time WARN). Operators run
-- `sourcebridge migrate git-secrets` to re-save legacy rows under the
-- envelope without waiting for the next admin save.
--
-- Plan: thoughts/shared/plans/2026-04-29-config-source-of-truth-r3.md slice 2.

DEFINE FIELD IF NOT EXISTS version ON ca_git_config TYPE int DEFAULT 0;

-- Backfill any existing row(s) with version 0 → 1 so the resolver does
-- a first fetch on deploy. Rows with non-zero versions are left alone
-- (idempotent re-runs of this migration must not perturb live state).
UPDATE ca_git_config SET version = 1 WHERE version = NONE OR version = 0;
