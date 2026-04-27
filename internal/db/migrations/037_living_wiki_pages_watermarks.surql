-- Migration 037: Living-wiki page store + watermark store tables
--
-- lw_pages:      per-repo proposed and canonical page AST records.
-- lw_watermarks: per-repo SHA watermarks for the incremental generation model.
--
-- These tables are consumed by the assembly factory (R2) which wires them
-- into the orchestrator's PageStore and WatermarkStore ports.

-- ─────────────────────────────────────────────────────────────────────────────
-- Page store
-- ─────────────────────────────────────────────────────────────────────────────

DEFINE TABLE IF NOT EXISTS lw_pages SCHEMAFULL;

-- repo_id + page_id form the stable lookup key.
DEFINE FIELD IF NOT EXISTS repo_id  ON TABLE lw_pages TYPE string;
DEFINE FIELD IF NOT EXISTS page_id  ON TABLE lw_pages TYPE string;

-- kind distinguishes canonical ("canonical") from proposed ("proposed").
-- For proposed pages, pr_id is required; for canonical pages it is empty.
DEFINE FIELD IF NOT EXISTS kind    ON TABLE lw_pages TYPE string DEFAULT 'canonical';
DEFINE FIELD IF NOT EXISTS pr_id   ON TABLE lw_pages TYPE string DEFAULT '';

-- ast_json is the JSON-serialised ast.Page value.
DEFINE FIELD IF NOT EXISTS ast_json ON TABLE lw_pages TYPE string DEFAULT '';

DEFINE FIELD IF NOT EXISTS created_at ON TABLE lw_pages TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON TABLE lw_pages TYPE datetime DEFAULT time::now();

-- Uniqueness is over (repo_id, pr_id, page_id, kind). For canonical rows
-- pr_id is empty, so this collapses to (repo_id, page_id, 'canonical').
-- For proposed rows pr_id is the PR ID, so each PR's proposed set is
-- independent. SurrealDB's DEFINE INDEX does not accept WHERE clauses,
-- so the disambiguation is encoded in the column tuple itself.
DEFINE INDEX IF NOT EXISTS idx_lw_pages_pk
    ON TABLE lw_pages COLUMNS repo_id, pr_id, page_id, kind UNIQUE;

-- Fast lookup of all proposed pages in a PR.
DEFINE INDEX IF NOT EXISTS idx_lw_pages_pr
    ON TABLE lw_pages COLUMNS repo_id, pr_id;

-- ─────────────────────────────────────────────────────────────────────────────
-- Watermark store
-- ─────────────────────────────────────────────────────────────────────────────

DEFINE TABLE IF NOT EXISTS lw_watermarks SCHEMAFULL;

DEFINE FIELD IF NOT EXISTS repo_id              ON TABLE lw_watermarks TYPE string;
DEFINE FIELD IF NOT EXISTS source_processed_sha ON TABLE lw_watermarks TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS wiki_published_sha   ON TABLE lw_watermarks TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS updated_at           ON TABLE lw_watermarks TYPE datetime DEFAULT time::now();

-- One row per repo.
DEFINE INDEX IF NOT EXISTS idx_lw_watermarks_pk
    ON TABLE lw_watermarks COLUMNS repo_id UNIQUE;

-- ─────────────────────────────────────────────────────────────────────────────
-- Schema version marker
-- ─────────────────────────────────────────────────────────────────────────────

UPSERT schema_version:v37 SET version = 37;
