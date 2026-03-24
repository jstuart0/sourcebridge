-- Migration 004: Knowledge Engine artifact model
-- Introduces ca_knowledge_artifact, ca_knowledge_section, and ca_knowledge_evidence
-- tables for persisting generated knowledge artifacts with evidence traceability.

DEFINE TABLE IF NOT EXISTS ca_knowledge_artifact SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_knowledge_artifact TYPE string;
DEFINE FIELD IF NOT EXISTS type ON ca_knowledge_artifact TYPE string;
DEFINE FIELD IF NOT EXISTS audience ON ca_knowledge_artifact TYPE string;
DEFINE FIELD IF NOT EXISTS depth ON ca_knowledge_artifact TYPE string;
DEFINE FIELD IF NOT EXISTS status ON ca_knowledge_artifact TYPE string DEFAULT 'pending';
DEFINE FIELD IF NOT EXISTS source_revision_commit ON ca_knowledge_artifact TYPE option<string>;
DEFINE FIELD IF NOT EXISTS source_revision_branch ON ca_knowledge_artifact TYPE option<string>;
DEFINE FIELD IF NOT EXISTS source_revision_content_fp ON ca_knowledge_artifact TYPE option<string>;
DEFINE FIELD IF NOT EXISTS source_revision_docs_fp ON ca_knowledge_artifact TYPE option<string>;
DEFINE FIELD IF NOT EXISTS stale ON ca_knowledge_artifact TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS generated_at ON ca_knowledge_artifact TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_knowledge_artifact TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_knowledge_artifact TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_knowledge_artifact_repo ON ca_knowledge_artifact FIELDS repo_id;
DEFINE INDEX IF NOT EXISTS idx_knowledge_artifact_type ON ca_knowledge_artifact FIELDS repo_id, type;

DEFINE TABLE IF NOT EXISTS ca_knowledge_section SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS artifact_id ON ca_knowledge_section TYPE string;
DEFINE FIELD IF NOT EXISTS title ON ca_knowledge_section TYPE string;
DEFINE FIELD IF NOT EXISTS content ON ca_knowledge_section TYPE string;
DEFINE FIELD IF NOT EXISTS summary ON ca_knowledge_section TYPE option<string>;
DEFINE FIELD IF NOT EXISTS confidence ON ca_knowledge_section TYPE string DEFAULT 'medium';
DEFINE FIELD IF NOT EXISTS inferred ON ca_knowledge_section TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS order_index ON ca_knowledge_section TYPE int DEFAULT 0;
DEFINE INDEX IF NOT EXISTS idx_knowledge_section_artifact ON ca_knowledge_section FIELDS artifact_id;

DEFINE TABLE IF NOT EXISTS ca_knowledge_evidence SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS section_id ON ca_knowledge_evidence TYPE string;
DEFINE FIELD IF NOT EXISTS source_type ON ca_knowledge_evidence TYPE string;
DEFINE FIELD IF NOT EXISTS source_id ON ca_knowledge_evidence TYPE string;
DEFINE FIELD IF NOT EXISTS file_path ON ca_knowledge_evidence TYPE option<string>;
DEFINE FIELD IF NOT EXISTS line_start ON ca_knowledge_evidence TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS line_end ON ca_knowledge_evidence TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS rationale ON ca_knowledge_evidence TYPE option<string>;
DEFINE FIELD IF NOT EXISTS metadata ON ca_knowledge_evidence TYPE option<string>;
DEFINE INDEX IF NOT EXISTS idx_knowledge_evidence_section ON ca_knowledge_evidence FIELDS section_id;

UPSERT schema_version:v4 SET version = 4;
