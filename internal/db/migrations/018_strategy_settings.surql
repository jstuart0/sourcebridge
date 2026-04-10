-- Migration 018: Strategy settings table
-- Stores comprehension settings with scope-based inheritance.
-- Hierarchy (most specific wins): workspace -> corpus_type -> artifact_type -> user

DEFINE TABLE IF NOT EXISTS ca_strategy_settings SCHEMAFULL;

DEFINE FIELD IF NOT EXISTS scope_type ON ca_strategy_settings TYPE string;
DEFINE FIELD IF NOT EXISTS scope_key ON ca_strategy_settings TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS strategy_preference_chain ON ca_strategy_settings TYPE string DEFAULT '["hierarchical","single_shot"]';
DEFINE FIELD IF NOT EXISTS model_id ON ca_strategy_settings TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS max_concurrency ON ca_strategy_settings TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS max_prompt_tokens ON ca_strategy_settings TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS leaf_budget_tokens ON ca_strategy_settings TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS refine_pass_enabled ON ca_strategy_settings TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS long_context_max_tokens ON ca_strategy_settings TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS graphrag_entity_types ON ca_strategy_settings TYPE string DEFAULT '[]';
DEFINE FIELD IF NOT EXISTS cache_enabled ON ca_strategy_settings TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS allow_unsafe_combinations ON ca_strategy_settings TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS updated_at ON ca_strategy_settings TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_by ON ca_strategy_settings TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS created_at ON ca_strategy_settings TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_ca_strategy_settings_scope ON ca_strategy_settings FIELDS scope_type, scope_key UNIQUE;

UPSERT schema_version:v18 SET version = 18;
