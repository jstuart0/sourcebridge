-- 050_lw_page_publish_status.surql
--
-- Introduces the lw_page_publish_status table that tracks per-page, per-sink
-- dispatch state for the Living Wiki cold-start runner. Each row represents
-- one (repo_id, page_id, sink_kind, integration_name) tuple and carries the
-- publish status, error message, content fingerprint, and stub-fixup state.
--
-- Design decisions (see plan 2026-04-29-livingwiki-incremental-publish-redesign.md):
--
--   CR9 — Record IDs use type::thing() composite-key convention matching the
--          precedent in lw_repo_settings (migration 036). The primary-key index
--          named idx_lw_page_publish_status_pk enforces the uniqueness
--          constraint that the UPSERT relies on.
--
--   CR4 — has_stubs, stub_target_page_ids, and fixup_status land in this
--          migration (not a follow-up) so Phase 3's fix-up pass can read them
--          without a schema change. fixup_status values: "none" (no stubs),
--          "pending" (stubs present, fix-up not yet run), "done" (stubs cleared),
--          "failed" (fix-up attempt failed; will retry next run).
--
--   LD-7 — content_fingerprint defaults to '' (empty string, not NULL) so
--           LoadFingerprints comparisons never encounter nil-string ambiguity.
--
--   CR8  — The __cleanup_state__:<prefix> synthetic page_id convention also
--          uses this table. Those rows have synthetic page IDs and are excluded
--          from normal page-status queries by their prefix.
--
-- Plan: thoughts/shared/plans/2026-04-29-livingwiki-incremental-publish-redesign.md

DEFINE TABLE IF NOT EXISTS lw_page_publish_status SCHEMAFULL;

DEFINE FIELD IF NOT EXISTS repo_id              ON TABLE lw_page_publish_status TYPE string;
DEFINE FIELD IF NOT EXISTS page_id              ON TABLE lw_page_publish_status TYPE string;
DEFINE FIELD IF NOT EXISTS sink_kind            ON TABLE lw_page_publish_status TYPE string;
DEFINE FIELD IF NOT EXISTS integration_name     ON TABLE lw_page_publish_status TYPE string;
DEFINE FIELD IF NOT EXISTS status               ON TABLE lw_page_publish_status TYPE string DEFAULT 'pending';
DEFINE FIELD IF NOT EXISTS error_msg            ON TABLE lw_page_publish_status TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS content_fingerprint  ON TABLE lw_page_publish_status TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS has_stubs            ON TABLE lw_page_publish_status TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS stub_target_page_ids ON TABLE lw_page_publish_status TYPE array DEFAULT [];
DEFINE FIELD IF NOT EXISTS fixup_status         ON TABLE lw_page_publish_status TYPE string DEFAULT 'none';
DEFINE FIELD IF NOT EXISTS updated_at           ON TABLE lw_page_publish_status TYPE datetime DEFAULT time::now();

-- Primary key index: the UPSERT in SetReady/SetNonReady relies on this for
-- stable record addressing via type::thing('lw_page_publish_status', [...]).
DEFINE INDEX IF NOT EXISTS idx_lw_page_publish_status_pk
    ON TABLE lw_page_publish_status COLUMNS repo_id, page_id, sink_kind, integration_name UNIQUE;

-- Secondary index for efficient repo-scoped queries (LoadFingerprints,
-- ListByRepo, run-start generating-write).
DEFINE INDEX IF NOT EXISTS idx_lw_page_publish_status_repo
    ON TABLE lw_page_publish_status COLUMNS repo_id;

UPSERT schema_version:v50 SET version = 50;
