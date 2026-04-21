-- Migration 031: soft-delete core (requirements, links, artifacts).
--
-- ADDITIVE at rest. Rollback is NOT safe once any moveToTrash has run
-- because the tombstone-key rewrite mutates user-visible columns
-- (external_id, scope_key). An older binary rolled back will see the
-- rewritten values as live IDs. Operators must run the unwind script
-- before reverting binaries.
--
-- See thoughts/shared/plans/2026-04-20-soft-delete-recycle-bin.md
-- §"Migration Notes" for the forward-only story and the unwind modes.

-- ca_requirement
DEFINE FIELD IF NOT EXISTS deleted_at           ON ca_requirement TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_by           ON ca_requirement TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_reason       ON ca_requirement TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS restored_at          ON ca_requirement TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS restored_by          ON ca_requirement TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS trash_batch_id       ON ca_requirement TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS original_external_id ON ca_requirement TYPE option<string>   DEFAULT NONE;
DEFINE INDEX IF NOT EXISTS idx_req_deleted_at   ON ca_requirement FIELDS repo_id, deleted_at;
DEFINE INDEX IF NOT EXISTS idx_req_trash_batch  ON ca_requirement FIELDS trash_batch_id;

-- ca_link (no natural-key tombstone needed — primary id is opaque).
-- NOTE: The plan document's text refers to `ca_requirement_link`; the
-- actual table name in SurrealDB is `ca_link` (migration 003). The
-- plan uses the more descriptive label; the schema uses the shorter
-- historical name.
DEFINE FIELD IF NOT EXISTS deleted_at     ON ca_link TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_by     ON ca_link TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_reason ON ca_link TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS restored_at    ON ca_link TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS restored_by    ON ca_link TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS trash_batch_id ON ca_link TYPE option<string>   DEFAULT NONE;
DEFINE INDEX IF NOT EXISTS idx_link_deleted_at  ON ca_link FIELDS repo_id, deleted_at;
DEFINE INDEX IF NOT EXISTS idx_link_trash_batch ON ca_link FIELDS trash_batch_id;

-- ca_knowledge_artifact (unique on repo_id, type, audience, depth, scope_key)
DEFINE FIELD IF NOT EXISTS deleted_at         ON ca_knowledge_artifact TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_by         ON ca_knowledge_artifact TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_reason     ON ca_knowledge_artifact TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS restored_at        ON ca_knowledge_artifact TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS restored_by        ON ca_knowledge_artifact TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS trash_batch_id     ON ca_knowledge_artifact TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS original_scope_key ON ca_knowledge_artifact TYPE option<string>   DEFAULT NONE;
DEFINE INDEX IF NOT EXISTS idx_kna_deleted_at  ON ca_knowledge_artifact FIELDS repo_id, deleted_at;
DEFINE INDEX IF NOT EXISTS idx_kna_trash_batch ON ca_knowledge_artifact FIELDS trash_batch_id;

-- Cascade members (inherit tombstone via batch_id; no key rewrite needed).
DEFINE FIELD IF NOT EXISTS deleted_at     ON ca_knowledge_section    TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS trash_batch_id ON ca_knowledge_section    TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_at     ON ca_knowledge_evidence   TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS trash_batch_id ON ca_knowledge_evidence   TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_at     ON ca_knowledge_dependency TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS trash_batch_id ON ca_knowledge_dependency TYPE option<string>   DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS deleted_at     ON ca_knowledge_refinement TYPE option<datetime> DEFAULT NONE;
DEFINE FIELD IF NOT EXISTS trash_batch_id ON ca_knowledge_refinement TYPE option<string>   DEFAULT NONE;

UPSERT schema_version:v31 SET version = 31;
