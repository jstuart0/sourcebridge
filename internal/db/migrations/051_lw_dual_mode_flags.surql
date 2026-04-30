-- 051_lw_dual_mode_flags.surql
--
-- Adds the dual-mode (Overview / Detailed) flag columns to lw_repo_settings.
--
-- CR10 / CR12 (Phase 1 deliverable):
--   The schema lands in Phase 1 so Phase 2's index renderer and Phase 4a's
--   runner can read the flags without a separate migration. The GraphQL
--   exposure and UI toggles land in Phase 4b.
--
-- CR12 Part A: migration 051 moves from Phase 4b into Phase 1.
--
-- Default policy (J1 — Jay's locked decision):
--   - New rows (no prior value): DEFAULT true for overview, DEFAULT false for
--     detailed. New repos get Overview-only by default.
--   - Existing rows (pre-051, neither column present): the UPDATE below sets
--     BOTH flags to true, preserving today's per-folder (Detailed) pages AND
--     giving existing users Overview on the next run. Explicit data-loss-
--     avoidance choice; reversible from the UI.
--
-- Plan: thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md

DEFINE FIELD IF NOT EXISTS living_wiki_overview_enabled
    ON TABLE lw_repo_settings TYPE bool DEFAULT true;
DEFINE FIELD IF NOT EXISTS living_wiki_detailed_enabled
    ON TABLE lw_repo_settings TYPE bool DEFAULT false;

-- Backfill existing rows: both modes ON so users keep their Detailed pages
-- and additionally get Overview on the next run (J1).
UPDATE lw_repo_settings SET
    living_wiki_overview_enabled = true,
    living_wiki_detailed_enabled = true
WHERE living_wiki_overview_enabled IS NONE
   OR living_wiki_overview_enabled IS NULL
   OR living_wiki_detailed_enabled IS NONE
   OR living_wiki_detailed_enabled IS NULL;

UPSERT schema_version:v51 SET version = 51;
