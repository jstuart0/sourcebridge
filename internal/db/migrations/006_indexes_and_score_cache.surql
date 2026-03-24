-- Migration 006: Add missing indexes on ca_* tables and understanding score cache
--
-- The ca_* tables introduced in 003_align had no indexes, causing full table
-- scans on every query filtered by repo_id, file_id, requirement_id, etc.

-- ca_file: queried by repo_id in ListFiles, GetFiles, understanding score helpers
DEFINE INDEX IF NOT EXISTS idx_ca_file_repo ON ca_file FIELDS repo_id;

-- ca_symbol: queried by repo_id (symbol listing, doc coverage, test ratio)
--            and by file_id (GetFileSymbols for review coverage)
DEFINE INDEX IF NOT EXISTS idx_ca_symbol_repo ON ca_symbol FIELDS repo_id;
DEFINE INDEX IF NOT EXISTS idx_ca_symbol_file ON ca_symbol FIELDS file_id;

-- ca_link: queried by requirement_id, symbol_id, and repo_id
DEFINE INDEX IF NOT EXISTS idx_ca_link_repo ON ca_link FIELDS repo_id;
DEFINE INDEX IF NOT EXISTS idx_ca_link_requirement ON ca_link FIELDS requirement_id;
DEFINE INDEX IF NOT EXISTS idx_ca_link_symbol ON ca_link FIELDS symbol_id;

-- ca_requirement: queried by repo_id and by external_id
DEFINE INDEX IF NOT EXISTS idx_ca_requirement_repo ON ca_requirement FIELDS repo_id;
DEFINE INDEX IF NOT EXISTS idx_ca_requirement_external ON ca_requirement FIELDS repo_id, external_id;

-- ca_calls: queried by repo_id, caller_id, callee_id
DEFINE INDEX IF NOT EXISTS idx_ca_calls_repo ON ca_calls FIELDS repo_id;
DEFINE INDEX IF NOT EXISTS idx_ca_calls_caller ON ca_calls FIELDS caller_id;
DEFINE INDEX IF NOT EXISTS idx_ca_calls_callee ON ca_calls FIELDS callee_id;

-- ca_import: queried by file_id
DEFINE INDEX IF NOT EXISTS idx_ca_import_file ON ca_import FIELDS file_id;

-- ca_module: queried by repo_id
DEFINE INDEX IF NOT EXISTS idx_ca_module_repo ON ca_module FIELDS repo_id;

-- ca_llm_usage: queried by repo_id
DEFINE INDEX IF NOT EXISTS idx_ca_llm_usage_repo ON ca_llm_usage FIELDS repo_id;

-- ca_review_result: queried by repo_id and target_id
DEFINE INDEX IF NOT EXISTS idx_ca_review_result_repo ON ca_review_result FIELDS repo_id;
DEFINE INDEX IF NOT EXISTS idx_ca_review_result_target ON ca_review_result FIELDS target_id;

-- Understanding score cache on ca_repository
DEFINE FIELD IF NOT EXISTS understanding_score ON ca_repository TYPE option<float>;
DEFINE FIELD IF NOT EXISTS understanding_score_at ON ca_repository TYPE option<datetime>;

UPSERT schema_version:v6 SET version = 6;
