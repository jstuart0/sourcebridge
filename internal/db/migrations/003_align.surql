-- Migration 003: Align ca_* tables as the authoritative OSS schema
-- This formalizes the working tables used by the application.
-- Fields marked "NEW" are introduced in this migration; all others
-- already exist in live data written by the Go store layer.

DEFINE TABLE IF NOT EXISTS ca_repository SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS name ON ca_repository TYPE string;
DEFINE FIELD IF NOT EXISTS path ON ca_repository TYPE string;
DEFINE FIELD IF NOT EXISTS status ON ca_repository TYPE string DEFAULT 'ready';
DEFINE FIELD IF NOT EXISTS file_count ON ca_repository TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS function_count ON ca_repository TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS class_count ON ca_repository TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS last_indexed_at ON ca_repository TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS commit_sha ON ca_repository TYPE option<string>;
DEFINE FIELD IF NOT EXISTS branch ON ca_repository TYPE option<string>;
DEFINE FIELD IF NOT EXISTS clone_path ON ca_repository TYPE option<string>;
DEFINE FIELD IF NOT EXISTS remote_url ON ca_repository TYPE option<string>;
DEFINE FIELD IF NOT EXISTS index_error ON ca_repository TYPE option<string>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_repository TYPE datetime DEFAULT time::now();

DEFINE TABLE IF NOT EXISTS ca_file SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_file TYPE string;
DEFINE FIELD IF NOT EXISTS path ON ca_file TYPE string;
DEFINE FIELD IF NOT EXISTS language ON ca_file TYPE option<string>;
DEFINE FIELD IF NOT EXISTS line_count ON ca_file TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS content_hash ON ca_file TYPE option<string>;

DEFINE TABLE IF NOT EXISTS ca_symbol SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_symbol TYPE string;
DEFINE FIELD IF NOT EXISTS file_id ON ca_symbol TYPE string;
DEFINE FIELD IF NOT EXISTS name ON ca_symbol TYPE string;
DEFINE FIELD IF NOT EXISTS qualified_name ON ca_symbol TYPE string;
DEFINE FIELD IF NOT EXISTS kind ON ca_symbol TYPE string;
DEFINE FIELD IF NOT EXISTS language ON ca_symbol TYPE string;
DEFINE FIELD IF NOT EXISTS file_path ON ca_symbol TYPE string;
DEFINE FIELD IF NOT EXISTS start_line ON ca_symbol TYPE int;
DEFINE FIELD IF NOT EXISTS end_line ON ca_symbol TYPE int;
DEFINE FIELD IF NOT EXISTS signature ON ca_symbol TYPE option<string>;
DEFINE FIELD IF NOT EXISTS doc_comment ON ca_symbol TYPE option<string>;
DEFINE FIELD IF NOT EXISTS is_test ON ca_symbol TYPE bool DEFAULT false;

DEFINE TABLE IF NOT EXISTS ca_module SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_module TYPE string;
DEFINE FIELD IF NOT EXISTS name ON ca_module TYPE string;
DEFINE FIELD IF NOT EXISTS path ON ca_module TYPE string;
DEFINE FIELD IF NOT EXISTS file_count ON ca_module TYPE int DEFAULT 0;

DEFINE TABLE IF NOT EXISTS ca_import SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS file_id ON ca_import TYPE string;
DEFINE FIELD IF NOT EXISTS path ON ca_import TYPE string;
DEFINE FIELD IF NOT EXISTS line ON ca_import TYPE int DEFAULT 0;

-- Call graph relations (NEW)
DEFINE TABLE IF NOT EXISTS ca_calls SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS caller_id ON ca_calls TYPE string;
DEFINE FIELD IF NOT EXISTS callee_id ON ca_calls TYPE string;
DEFINE FIELD IF NOT EXISTS call_site_line ON ca_calls TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_calls TYPE string;

-- Requirement and link tables (already in use, now formalized)
DEFINE TABLE IF NOT EXISTS ca_requirement SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS external_id ON ca_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS title ON ca_requirement TYPE string;
DEFINE FIELD IF NOT EXISTS description ON ca_requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS source ON ca_requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS priority ON ca_requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS tags ON ca_requirement TYPE option<array>;
DEFINE FIELD IF NOT EXISTS acceptance_criteria ON ca_requirement TYPE option<array>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_requirement TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_requirement TYPE datetime DEFAULT time::now();

DEFINE TABLE IF NOT EXISTS ca_link SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_link TYPE string;
DEFINE FIELD IF NOT EXISTS requirement_id ON ca_link TYPE string;
DEFINE FIELD IF NOT EXISTS symbol_id ON ca_link TYPE string;
DEFINE FIELD IF NOT EXISTS confidence ON ca_link TYPE float DEFAULT 0.0;
DEFINE FIELD IF NOT EXISTS source ON ca_link TYPE option<string>;
DEFINE FIELD IF NOT EXISTS link_type ON ca_link TYPE option<string>;
DEFINE FIELD IF NOT EXISTS rationale ON ca_link TYPE option<string>;
DEFINE FIELD IF NOT EXISTS verified ON ca_link TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS verified_by ON ca_link TYPE option<string>;
DEFINE FIELD IF NOT EXISTS rejected ON ca_link TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS created_at ON ca_link TYPE datetime DEFAULT time::now();

-- LLM usage tracking (NEW)
DEFINE TABLE IF NOT EXISTS ca_llm_usage SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_llm_usage TYPE option<string>;
DEFINE FIELD IF NOT EXISTS user_id ON ca_llm_usage TYPE option<string>;
DEFINE FIELD IF NOT EXISTS provider ON ca_llm_usage TYPE string;
DEFINE FIELD IF NOT EXISTS model ON ca_llm_usage TYPE string;
DEFINE FIELD IF NOT EXISTS operation ON ca_llm_usage TYPE string;
DEFINE FIELD IF NOT EXISTS input_tokens ON ca_llm_usage TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS output_tokens ON ca_llm_usage TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS created_at ON ca_llm_usage TYPE datetime DEFAULT time::now();

-- Embeddings (NEW)
DEFINE TABLE IF NOT EXISTS ca_embedding SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS target_id ON ca_embedding TYPE string;
DEFINE FIELD IF NOT EXISTS target_type ON ca_embedding TYPE string;
DEFINE FIELD IF NOT EXISTS vector ON ca_embedding TYPE option<array>;
DEFINE FIELD IF NOT EXISTS dimension ON ca_embedding TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS model ON ca_embedding TYPE string;
DEFINE FIELD IF NOT EXISTS text_hash ON ca_embedding TYPE string;
DEFINE FIELD IF NOT EXISTS created_at ON ca_embedding TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_embedding_target ON ca_embedding FIELDS target_id;

-- Review results (NEW)
DEFINE TABLE IF NOT EXISTS ca_review_result SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_review_result TYPE string;
DEFINE FIELD IF NOT EXISTS target_id ON ca_review_result TYPE string;
DEFINE FIELD IF NOT EXISTS template ON ca_review_result TYPE string;
DEFINE FIELD IF NOT EXISTS findings ON ca_review_result TYPE option<array>;
DEFINE FIELD IF NOT EXISTS score ON ca_review_result TYPE option<float>;
DEFINE FIELD IF NOT EXISTS created_by ON ca_review_result TYPE option<string>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_review_result TYPE datetime DEFAULT time::now();

-- Runtime config (NEW) — persisted admin/runtime configuration
DEFINE TABLE IF NOT EXISTS ca_config SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS key ON ca_config TYPE string;
DEFINE FIELD IF NOT EXISTS value ON ca_config TYPE option<string>;
DEFINE FIELD IF NOT EXISTS updated_at ON ca_config TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_config_key ON ca_config FIELDS key UNIQUE;

-- API tokens (NEW) — for programmatic access
DEFINE TABLE IF NOT EXISTS ca_api_token SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS name ON ca_api_token TYPE string;
DEFINE FIELD IF NOT EXISTS token_hash ON ca_api_token TYPE string;
DEFINE FIELD IF NOT EXISTS last_used_at ON ca_api_token TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS expires_at ON ca_api_token TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_api_token TYPE datetime DEFAULT time::now();

-- Stats snapshots (NEW) — for dashboard history
DEFINE TABLE IF NOT EXISTS ca_stats_snapshot SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repositories ON ca_stats_snapshot TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS files ON ca_stats_snapshot TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS symbols ON ca_stats_snapshot TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS requirements ON ca_stats_snapshot TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS links ON ca_stats_snapshot TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS created_at ON ca_stats_snapshot TYPE datetime DEFAULT time::now();

-- Auth table (already in use via surreal_persister.go, formalized here)
DEFINE TABLE IF NOT EXISTS ca_auth SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS password_hash ON ca_auth TYPE string;
DEFINE FIELD IF NOT EXISTS created_at ON ca_auth TYPE datetime DEFAULT time::now();

UPSERT schema_version:v3 SET version = 3;
