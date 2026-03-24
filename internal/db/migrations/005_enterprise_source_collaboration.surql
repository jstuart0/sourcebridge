-- Migration 005: Enterprise source collaboration
-- Shared source-view contexts and enterprise-only source annotations.

DEFINE TABLE IF NOT EXISTS ca_ent_source_context SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_source_context TYPE string;
DEFINE FIELD IF NOT EXISTS repository_id ON ca_ent_source_context TYPE string;
DEFINE FIELD IF NOT EXISTS file_path ON ca_ent_source_context TYPE string;
DEFINE FIELD IF NOT EXISTS line_start ON ca_ent_source_context TYPE int;
DEFINE FIELD IF NOT EXISTS line_end ON ca_ent_source_context TYPE int;
DEFINE FIELD IF NOT EXISTS label ON ca_ent_source_context TYPE string;
DEFINE FIELD IF NOT EXISTS focus_reason ON ca_ent_source_context TYPE option<string>;
DEFINE FIELD IF NOT EXISTS created_by ON ca_ent_source_context TYPE string;
DEFINE FIELD IF NOT EXISTS created_at ON ca_ent_source_context TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_source_context TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_source_context_tenant ON ca_ent_source_context FIELDS tenant_id;
DEFINE INDEX IF NOT EXISTS idx_source_context_repo ON ca_ent_source_context FIELDS tenant_id, repository_id;

DEFINE TABLE IF NOT EXISTS ca_ent_source_annotation SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_source_annotation TYPE string;
DEFINE FIELD IF NOT EXISTS repository_id ON ca_ent_source_annotation TYPE string;
DEFINE FIELD IF NOT EXISTS file_path ON ca_ent_source_annotation TYPE string;
DEFINE FIELD IF NOT EXISTS line_start ON ca_ent_source_annotation TYPE int;
DEFINE FIELD IF NOT EXISTS line_end ON ca_ent_source_annotation TYPE int;
DEFINE FIELD IF NOT EXISTS kind ON ca_ent_source_annotation TYPE string;
DEFINE FIELD IF NOT EXISTS body ON ca_ent_source_annotation TYPE string;
DEFINE FIELD IF NOT EXISTS status ON ca_ent_source_annotation TYPE string DEFAULT 'open';
DEFINE FIELD IF NOT EXISTS created_by ON ca_ent_source_annotation TYPE string;
DEFINE FIELD IF NOT EXISTS created_at ON ca_ent_source_annotation TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_source_annotation TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_source_annotation_tenant ON ca_ent_source_annotation FIELDS tenant_id;
DEFINE INDEX IF NOT EXISTS idx_source_annotation_file ON ca_ent_source_annotation FIELDS tenant_id, repository_id, file_path;

UPSERT schema_version:v5 SET version = 5;
