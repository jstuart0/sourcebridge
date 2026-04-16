-- Migration 029: durable structured diagram documents

DEFINE TABLE IF NOT EXISTS ca_diagram_document SCHEMALESS;

DEFINE FIELD IF NOT EXISTS repo_id ON ca_diagram_document TYPE string;
DEFINE FIELD IF NOT EXISTS source_kind ON ca_diagram_document TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS document ON ca_diagram_document TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS created_at ON ca_diagram_document TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_diagram_document TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_diagram_document_repo ON TABLE ca_diagram_document COLUMNS repo_id;
DEFINE INDEX IF NOT EXISTS uniq_diagram_document_repo_source ON TABLE ca_diagram_document COLUMNS repo_id, source_kind UNIQUE;
