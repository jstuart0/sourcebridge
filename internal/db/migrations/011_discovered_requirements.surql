-- Migration 011: Discovered requirements (spec extraction)
-- Stores implicit specifications extracted from test files, API schemas, and doc comments.

DEFINE TABLE IF NOT EXISTS ca_discovered_requirement SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_discovered_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS source ON ca_discovered_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS source_file ON ca_discovered_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS source_line ON ca_discovered_requirement TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS source_files ON ca_discovered_requirement TYPE option<array<string>>;
DEFINE FIELD IF NOT EXISTS text ON ca_discovered_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS raw_text ON ca_discovered_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS group_key ON ca_discovered_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS language ON ca_discovered_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS keywords ON ca_discovered_requirement TYPE option<array<string>>;
DEFINE FIELD IF NOT EXISTS confidence ON ca_discovered_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS status ON ca_discovered_requirement TYPE string DEFAULT 'discovered';
DEFINE FIELD IF NOT EXISTS llm_refined ON ca_discovered_requirement TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS promoted_to ON ca_discovered_requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS dismissed_by ON ca_discovered_requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS dismissed_reason ON ca_discovered_requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_discovered_requirement TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_disc_req_repo ON ca_discovered_requirement FIELDS repo_id;
DEFINE INDEX IF NOT EXISTS idx_disc_req_status ON ca_discovered_requirement FIELDS repo_id, status;
DEFINE INDEX IF NOT EXISTS idx_disc_req_confidence ON ca_discovered_requirement FIELDS repo_id, confidence;
DEFINE INDEX IF NOT EXISTS idx_disc_req_group ON ca_discovered_requirement FIELDS repo_id, group_key;

-- Record migration
UPSERT schema_version:v11 SET version = 11;
