-- Migration 013: Multi-repo federation (OSS)

-- Repo-to-repo link (bidirectional "related" relationship)
DEFINE TABLE IF NOT EXISTS ca_repo_link SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS source_repo_id ON ca_repo_link TYPE string;
DEFINE FIELD IF NOT EXISTS target_repo_id ON ca_repo_link TYPE string;
DEFINE FIELD IF NOT EXISTS link_type ON ca_repo_link TYPE string DEFAULT 'manual';
DEFINE FIELD IF NOT EXISTS created_by ON ca_repo_link TYPE option<string>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_repo_link TYPE datetime DEFAULT time::now();

-- Prevent duplicate links (canonical ordering: source < target)
DEFINE INDEX IF NOT EXISTS idx_repo_link_pair ON ca_repo_link FIELDS source_repo_id, target_repo_id UNIQUE;
DEFINE INDEX IF NOT EXISTS idx_repo_link_source ON ca_repo_link FIELDS source_repo_id;
DEFINE INDEX IF NOT EXISTS idx_repo_link_target ON ca_repo_link FIELDS target_repo_id;

-- Cross-repo symbol reference (detected by contract worker)
DEFINE TABLE IF NOT EXISTS ca_cross_repo_ref SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS source_symbol_id ON ca_cross_repo_ref TYPE string;
DEFINE FIELD IF NOT EXISTS target_symbol_id ON ca_cross_repo_ref TYPE string;
DEFINE FIELD IF NOT EXISTS source_repo_id ON ca_cross_repo_ref TYPE string;
DEFINE FIELD IF NOT EXISTS target_repo_id ON ca_cross_repo_ref TYPE string;
DEFINE FIELD IF NOT EXISTS ref_type ON ca_cross_repo_ref TYPE string;
DEFINE FIELD IF NOT EXISTS confidence ON ca_cross_repo_ref TYPE float DEFAULT 0.0;
DEFINE FIELD IF NOT EXISTS contract_file ON ca_cross_repo_ref TYPE option<string>;
DEFINE FIELD IF NOT EXISTS consumer_file ON ca_cross_repo_ref TYPE option<string>;
DEFINE FIELD IF NOT EXISTS evidence ON ca_cross_repo_ref TYPE option<string>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_cross_repo_ref TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_cross_repo_ref TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_xref_source_repo ON ca_cross_repo_ref FIELDS source_repo_id;
DEFINE INDEX IF NOT EXISTS idx_xref_target_repo ON ca_cross_repo_ref FIELDS target_repo_id;
DEFINE INDEX IF NOT EXISTS idx_xref_source_symbol ON ca_cross_repo_ref FIELDS source_symbol_id;
DEFINE INDEX IF NOT EXISTS idx_xref_target_symbol ON ca_cross_repo_ref FIELDS target_symbol_id;
DEFINE INDEX IF NOT EXISTS idx_xref_pair ON ca_cross_repo_ref FIELDS source_symbol_id, target_symbol_id UNIQUE;

-- API contract registry (detected contract surfaces)
DEFINE TABLE IF NOT EXISTS ca_api_contract SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_api_contract TYPE string;
DEFINE FIELD IF NOT EXISTS file_path ON ca_api_contract TYPE string;
DEFINE FIELD IF NOT EXISTS contract_type ON ca_api_contract TYPE string;
DEFINE FIELD IF NOT EXISTS endpoints ON ca_api_contract TYPE option<string>;
DEFINE FIELD IF NOT EXISTS version ON ca_api_contract TYPE option<string>;
DEFINE FIELD IF NOT EXISTS content_hash ON ca_api_contract TYPE string;
DEFINE FIELD IF NOT EXISTS detected_at ON ca_api_contract TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_api_contract_repo ON ca_api_contract FIELDS repo_id;
DEFINE INDEX IF NOT EXISTS idx_api_contract_file ON ca_api_contract FIELDS repo_id, file_path UNIQUE;

-- === Enterprise: Workspace Management ===

DEFINE TABLE IF NOT EXISTS ca_ent_workspace SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_workspace TYPE string;
DEFINE FIELD IF NOT EXISTS name ON ca_ent_workspace TYPE string;
DEFINE FIELD IF NOT EXISTS description ON ca_ent_workspace TYPE option<string>;
DEFINE FIELD IF NOT EXISTS created_by ON ca_ent_workspace TYPE string;
DEFINE FIELD IF NOT EXISTS created_at ON ca_ent_workspace TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_workspace TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_workspace_tenant ON ca_ent_workspace FIELDS tenant_id;
DEFINE INDEX IF NOT EXISTS idx_workspace_name ON ca_ent_workspace FIELDS tenant_id, name UNIQUE;

DEFINE TABLE IF NOT EXISTS ca_ent_workspace_repo SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS workspace_id ON ca_ent_workspace_repo TYPE string;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_ent_workspace_repo TYPE string;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_workspace_repo TYPE string;
DEFINE FIELD IF NOT EXISTS added_by ON ca_ent_workspace_repo TYPE string;
DEFINE FIELD IF NOT EXISTS added_at ON ca_ent_workspace_repo TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_ws_repo_workspace ON ca_ent_workspace_repo FIELDS workspace_id;
DEFINE INDEX IF NOT EXISTS idx_ws_repo_pair ON ca_ent_workspace_repo FIELDS workspace_id, repo_id UNIQUE;
DEFINE INDEX IF NOT EXISTS idx_ws_repo_tenant ON ca_ent_workspace_repo FIELDS tenant_id;

DEFINE TABLE IF NOT EXISTS ca_ent_workspace_member SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS workspace_id ON ca_ent_workspace_member TYPE string;
DEFINE FIELD IF NOT EXISTS user_id ON ca_ent_workspace_member TYPE string;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_workspace_member TYPE string;
DEFINE FIELD IF NOT EXISTS workspace_role ON ca_ent_workspace_member TYPE string DEFAULT 'viewer';
DEFINE FIELD IF NOT EXISTS assigned_at ON ca_ent_workspace_member TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_ws_member_workspace ON ca_ent_workspace_member FIELDS workspace_id;
DEFINE INDEX IF NOT EXISTS idx_ws_member_pair ON ca_ent_workspace_member FIELDS workspace_id, user_id UNIQUE;

UPSERT schema_version:v13 SET version = 13;
