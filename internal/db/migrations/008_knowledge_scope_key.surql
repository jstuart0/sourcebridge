-- Migration 008: Scoped knowledge artifact identity
-- Adds canonical scope fields and a unique key across repo/type/audience/depth/scope.

DEFINE FIELD IF NOT EXISTS scope_type ON ca_knowledge_artifact TYPE string DEFAULT 'repository';
DEFINE FIELD IF NOT EXISTS scope_key ON ca_knowledge_artifact TYPE string DEFAULT 'repository:';
DEFINE FIELD IF NOT EXISTS scope_path ON ca_knowledge_artifact TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS scope_symbol_name ON ca_knowledge_artifact TYPE string DEFAULT '';

UPDATE ca_knowledge_artifact SET
  scope_type = 'repository',
  scope_key = 'repository:',
  scope_path = '',
  scope_symbol_name = ''
WHERE scope_key = NONE OR scope_key = NULL OR scope_key = '';

DEFINE INDEX IF NOT EXISTS idx_knowledge_artifact_key
  ON ca_knowledge_artifact FIELDS repo_id, type, audience, depth, scope_key UNIQUE;

UPSERT schema_version:v8 SET version = 8;
