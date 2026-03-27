-- Migration 012: Architecture diagram support indexes
-- Composite index for file_path prefix queries in architecture diagrams
DEFINE INDEX IF NOT EXISTS idx_ca_symbol_repo_filepath ON ca_symbol FIELDS repo_id, file_path;

UPSERT schema_version:v12 SET version = 12;
