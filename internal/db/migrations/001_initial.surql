-- Migration 001: Initial schema
-- Creates all core tables and relationships for SourceBridge

-- Schema version tracking
DEFINE TABLE IF NOT EXISTS schema_version SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS version ON schema_version TYPE int;
DEFINE FIELD IF NOT EXISTS applied_at ON schema_version TYPE datetime DEFAULT time::now();

-- Organizations (tenants)
DEFINE TABLE IF NOT EXISTS organization SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS name ON organization TYPE string;
DEFINE FIELD IF NOT EXISTS slug ON organization TYPE string;
DEFINE FIELD IF NOT EXISTS plan ON organization TYPE string DEFAULT 'free';
DEFINE FIELD IF NOT EXISTS settings ON organization TYPE object DEFAULT {};
DEFINE FIELD IF NOT EXISTS created_at ON organization TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_org_slug ON organization FIELDS slug UNIQUE;

-- Users
DEFINE TABLE IF NOT EXISTS user SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS email ON user TYPE string;
DEFINE FIELD IF NOT EXISTS name ON user TYPE string;
DEFINE FIELD IF NOT EXISTS password_hash ON user TYPE option<string>;
DEFINE FIELD IF NOT EXISTS oidc_provider ON user TYPE option<string>;
DEFINE FIELD IF NOT EXISTS oidc_subject ON user TYPE option<string>;
DEFINE FIELD IF NOT EXISTS created_at ON user TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_user_email ON user FIELDS email UNIQUE;

-- Organization membership
DEFINE TABLE IF NOT EXISTS member_of SCHEMAFULL TYPE RELATION FROM user TO organization;
DEFINE FIELD IF NOT EXISTS role ON member_of TYPE string;
DEFINE FIELD IF NOT EXISTS joined_at ON member_of TYPE datetime DEFAULT time::now();

-- Repositories
DEFINE TABLE IF NOT EXISTS repository SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS org ON repository TYPE record<organization>;
DEFINE FIELD IF NOT EXISTS name ON repository TYPE string;
DEFINE FIELD IF NOT EXISTS url ON repository TYPE string;
DEFINE FIELD IF NOT EXISTS provider ON repository TYPE string;
DEFINE FIELD IF NOT EXISTS provider_id ON repository TYPE option<string>;
DEFINE FIELD IF NOT EXISTS default_branch ON repository TYPE string DEFAULT 'main';
DEFINE FIELD IF NOT EXISTS last_indexed_at ON repository TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS index_status ON repository TYPE string DEFAULT 'pending';
DEFINE FIELD IF NOT EXISTS index_error ON repository TYPE option<string>;
DEFINE FIELD IF NOT EXISTS disconnected_at ON repository TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS settings ON repository TYPE object DEFAULT {};
DEFINE FIELD IF NOT EXISTS created_at ON repository TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_repo_org ON repository FIELDS org;

-- Repository Snapshots
DEFINE TABLE IF NOT EXISTS snapshot SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo ON snapshot TYPE record<repository>;
DEFINE FIELD IF NOT EXISTS commit_sha ON snapshot TYPE string;
DEFINE FIELD IF NOT EXISTS branch ON snapshot TYPE string;
DEFINE FIELD IF NOT EXISTS parent_sha ON snapshot TYPE option<string>;
DEFINE FIELD IF NOT EXISTS status ON snapshot TYPE string DEFAULT 'indexing';
DEFINE FIELD IF NOT EXISTS is_default_branch ON snapshot TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS file_count ON snapshot TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS function_count ON snapshot TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS indexed_at ON snapshot TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS expires_at ON snapshot TYPE option<datetime>;
DEFINE INDEX IF NOT EXISTS idx_snapshot_repo ON snapshot FIELDS repo;
DEFINE INDEX IF NOT EXISTS idx_snapshot_repo_sha ON snapshot FIELDS repo, commit_sha UNIQUE;

-- Files
DEFINE TABLE IF NOT EXISTS file SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo ON file TYPE record<repository>;
DEFINE FIELD IF NOT EXISTS snapshot ON file TYPE record<snapshot>;
DEFINE FIELD IF NOT EXISTS path ON file TYPE string;
DEFINE FIELD IF NOT EXISTS language ON file TYPE option<string>;
DEFINE FIELD IF NOT EXISTS size_bytes ON file TYPE int;
DEFINE FIELD IF NOT EXISTS content_hash ON file TYPE string;
DEFINE FIELD IF NOT EXISTS commit_sha ON file TYPE string;
DEFINE FIELD IF NOT EXISTS summary ON file TYPE option<string>;
DEFINE FIELD IF NOT EXISTS summary_model ON file TYPE option<string>;
DEFINE FIELD IF NOT EXISTS summary_updated_at ON file TYPE option<datetime>;
DEFINE INDEX IF NOT EXISTS idx_file_snapshot_path ON file FIELDS snapshot, path UNIQUE;
DEFINE INDEX IF NOT EXISTS idx_file_repo_path ON file FIELDS repo, path;

-- Functions
DEFINE TABLE IF NOT EXISTS function SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS file ON function TYPE record<file>;
DEFINE FIELD IF NOT EXISTS snapshot ON function TYPE record<snapshot>;
DEFINE FIELD IF NOT EXISTS name ON function TYPE string;
DEFINE FIELD IF NOT EXISTS signature ON function TYPE option<string>;
DEFINE FIELD IF NOT EXISTS start_line ON function TYPE int;
DEFINE FIELD IF NOT EXISTS end_line ON function TYPE int;
DEFINE FIELD IF NOT EXISTS start_byte ON function TYPE int;
DEFINE FIELD IF NOT EXISTS end_byte ON function TYPE int;
DEFINE FIELD IF NOT EXISTS kind ON function TYPE string;
DEFINE FIELD IF NOT EXISTS visibility ON function TYPE option<string>;
DEFINE FIELD IF NOT EXISTS content_hash ON function TYPE string;
DEFINE FIELD IF NOT EXISTS summary ON function TYPE option<string>;
DEFINE FIELD IF NOT EXISTS summary_model ON function TYPE option<string>;
DEFINE FIELD IF NOT EXISTS summary_updated_at ON function TYPE option<datetime>;
DEFINE INDEX IF NOT EXISTS idx_func_file ON function FIELDS file;
DEFINE INDEX IF NOT EXISTS idx_func_snapshot ON function FIELDS snapshot;

-- Classes
DEFINE TABLE IF NOT EXISTS class SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS file ON class TYPE record<file>;
DEFINE FIELD IF NOT EXISTS snapshot ON class TYPE record<snapshot>;
DEFINE FIELD IF NOT EXISTS name ON class TYPE string;
DEFINE FIELD IF NOT EXISTS start_line ON class TYPE int;
DEFINE FIELD IF NOT EXISTS end_line ON class TYPE int;
DEFINE FIELD IF NOT EXISTS kind ON class TYPE string;
DEFINE FIELD IF NOT EXISTS content_hash ON class TYPE string;
DEFINE FIELD IF NOT EXISTS summary ON class TYPE option<string>;
DEFINE FIELD IF NOT EXISTS summary_model ON class TYPE option<string>;
DEFINE INDEX IF NOT EXISTS idx_class_file ON class FIELDS file;
DEFINE INDEX IF NOT EXISTS idx_class_snapshot ON class FIELDS snapshot;

-- Modules
DEFINE TABLE IF NOT EXISTS module SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo ON module TYPE record<repository>;
DEFINE FIELD IF NOT EXISTS snapshot ON module TYPE record<snapshot>;
DEFINE FIELD IF NOT EXISTS path ON module TYPE string;
DEFINE FIELD IF NOT EXISTS name ON module TYPE string;
DEFINE FIELD IF NOT EXISTS summary ON module TYPE option<string>;
DEFINE INDEX IF NOT EXISTS idx_module_repo ON module FIELDS repo;
DEFINE INDEX IF NOT EXISTS idx_module_snapshot ON module FIELDS snapshot;

-- Tests
DEFINE TABLE IF NOT EXISTS test SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS file ON test TYPE record<file>;
DEFINE FIELD IF NOT EXISTS name ON test TYPE string;
DEFINE FIELD IF NOT EXISTS kind ON test TYPE string;
DEFINE FIELD IF NOT EXISTS start_line ON test TYPE int;
DEFINE FIELD IF NOT EXISTS end_line ON test TYPE int;
DEFINE INDEX IF NOT EXISTS idx_test_file ON test FIELDS file;

-- Code relationships
DEFINE TABLE IF NOT EXISTS calls TYPE RELATION FROM function TO function;
DEFINE FIELD IF NOT EXISTS call_site_line ON calls TYPE int;

DEFINE TABLE IF NOT EXISTS imports TYPE RELATION FROM file TO file;
DEFINE FIELD IF NOT EXISTS import_path ON imports TYPE string;

DEFINE TABLE IF NOT EXISTS contains TYPE RELATION FROM file TO function | class;
DEFINE TABLE IF NOT EXISTS belongs_to TYPE RELATION FROM function TO class;
DEFINE TABLE IF NOT EXISTS part_of TYPE RELATION FROM file TO module;

-- LLM usage tracking
DEFINE TABLE IF NOT EXISTS llm_usage SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS org ON llm_usage TYPE option<record<organization>>;
DEFINE FIELD IF NOT EXISTS repo ON llm_usage TYPE option<record<repository>>;
DEFINE FIELD IF NOT EXISTS user ON llm_usage TYPE option<record<user>>;
DEFINE FIELD IF NOT EXISTS provider ON llm_usage TYPE string;
DEFINE FIELD IF NOT EXISTS model ON llm_usage TYPE string;
DEFINE FIELD IF NOT EXISTS operation ON llm_usage TYPE string;
DEFINE FIELD IF NOT EXISTS input_tokens ON llm_usage TYPE int;
DEFINE FIELD IF NOT EXISTS output_tokens ON llm_usage TYPE int;
DEFINE FIELD IF NOT EXISTS estimated_cost_usd ON llm_usage TYPE float;
DEFINE FIELD IF NOT EXISTS created_at ON llm_usage TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_llm_usage_org_created ON llm_usage FIELDS org, created_at;

-- Record migration (idempotent via fixed record ID)
UPSERT schema_version:v1 SET version = 1;
