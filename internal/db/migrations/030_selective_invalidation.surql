-- Migration 030: Selective knowledge-artifact invalidation support.
--
-- Two additions:
--   1. Indexes on ca_knowledge_evidence so the reindex resolver can quickly
--      look up artifacts whose evidence references a changed symbol or file.
--   2. Per-artifact invalidation metadata on ca_knowledge_artifact so the
--      "why is this stale?" explanation survives later reindexes.
--
-- Also adds stale_artifact_reasons to ca_impact_report as an additive,
-- rollback-safe JSON mirror of the bare stale_artifacts []string column.
-- Old binaries continue to read stale_artifacts unchanged.

DEFINE INDEX IF NOT EXISTS idx_knowledge_evidence_source
    ON ca_knowledge_evidence FIELDS source_type, source_id;

DEFINE INDEX IF NOT EXISTS idx_knowledge_evidence_file
    ON ca_knowledge_evidence FIELDS file_path;

DEFINE FIELD IF NOT EXISTS stale_reason_json
    ON ca_knowledge_artifact TYPE string DEFAULT '';

DEFINE FIELD IF NOT EXISTS stale_report_id
    ON ca_knowledge_artifact TYPE string DEFAULT '';

DEFINE FIELD IF NOT EXISTS stale_artifact_reasons
    ON ca_impact_report TYPE string DEFAULT '';

-- SurrealDB's DEFAULT is only applied at CREATE time; existing rows would
-- otherwise leave the new fields as NONE. Under SCHEMAFULL, any subsequent
-- query that touches the record re-validates EVERY field on the record, so
-- a separate UPDATE per column fails with "Found NONE for field X" while
-- X still hasn't been backfilled. Setting both new fields in a single
-- UPDATE, and using `?? ''` to coalesce NONE to the default, fixes both at
-- once. `?? ''` on an existing non-empty string is a no-op.
UPDATE ca_knowledge_artifact SET
    stale_reason_json = (stale_reason_json ?? ''),
    stale_report_id = (stale_report_id ?? '');
UPDATE ca_impact_report SET
    stale_artifact_reasons = (stale_artifact_reasons ?? '');

UPSERT schema_version:v30 SET version = 30;
