-- Migration 017: Model capabilities table
-- Stores per-model capability profiles used by the StrategySelector.
-- Seeded with builtin profiles; augmented by probe and manual overrides.

DEFINE TABLE IF NOT EXISTS ca_model_capabilities SCHEMAFULL;

DEFINE FIELD IF NOT EXISTS model_id ON ca_model_capabilities TYPE string;
DEFINE FIELD IF NOT EXISTS provider ON ca_model_capabilities TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS declared_context_tokens ON ca_model_capabilities TYPE int DEFAULT 4096;
DEFINE FIELD IF NOT EXISTS effective_context_tokens ON ca_model_capabilities TYPE int DEFAULT 4096;
DEFINE FIELD IF NOT EXISTS long_context_quality ON ca_model_capabilities TYPE string DEFAULT '{}';
DEFINE FIELD IF NOT EXISTS instruction_following ON ca_model_capabilities TYPE string DEFAULT 'low';
DEFINE FIELD IF NOT EXISTS json_mode ON ca_model_capabilities TYPE string DEFAULT 'none';
DEFINE FIELD IF NOT EXISTS tool_use ON ca_model_capabilities TYPE string DEFAULT 'none';
DEFINE FIELD IF NOT EXISTS extraction_grade ON ca_model_capabilities TYPE string DEFAULT 'low';
DEFINE FIELD IF NOT EXISTS creative_grade ON ca_model_capabilities TYPE string DEFAULT 'low';
DEFINE FIELD IF NOT EXISTS embedding_model ON ca_model_capabilities TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS cost_per_1k_input ON ca_model_capabilities TYPE option<float>;
DEFINE FIELD IF NOT EXISTS cost_per_1k_output ON ca_model_capabilities TYPE option<float>;
DEFINE FIELD IF NOT EXISTS last_probed_at ON ca_model_capabilities TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS source ON ca_model_capabilities TYPE string DEFAULT 'builtin';
DEFINE FIELD IF NOT EXISTS notes ON ca_model_capabilities TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS created_at ON ca_model_capabilities TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_model_capabilities TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_ca_model_capabilities_model_id ON ca_model_capabilities FIELDS model_id UNIQUE;
DEFINE INDEX IF NOT EXISTS idx_ca_model_capabilities_provider ON ca_model_capabilities FIELDS provider;

UPSERT schema_version:v17 SET version = 17;
