-- Migration 055: Raise Living Wiki MaxPagesPerJob default from 50 to 500
--
-- CA-146: MaxPagesPerJob is now wired into buildColdStartRunner as a real cap.
-- The old default of 50 would silently truncate every repo whose planned page
-- set exceeds 50 — a regression for operators who never changed the setting.
-- Raising the default to 500 preserves observable behavior for un-set repos
-- (cluster pages + 25-cap top-level-dir fallback + 3 repo-wide is bounded
-- well under 500 for any realistic repo).
--
-- Existing rows at exactly 50 (the prior default, meaning "never touched")
-- are updated to 500. Operators who deliberately set 50 also get bumped —
-- acceptable because nothing in prior code honored the setting anyway. This
-- is called out in CHANGELOG.md.
--
-- All statements are idempotent.

UPDATE lw_repo_settings SET max_pages_per_job = 500 WHERE max_pages_per_job = 50;

DEFINE FIELD IF NOT EXISTS max_pages_per_job ON TABLE lw_repo_settings TYPE int DEFAULT 500;

UPSERT schema_version:v55 SET version = 55;
