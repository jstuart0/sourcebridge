-- Migration 019: Summary node table
-- Persists the hierarchical summary tree so that incremental reindexing
-- can skip unchanged subtrees on subsequent cliff notes generations.

DEFINE TABLE IF NOT EXISTS ca_summary_node SCHEMAFULL;

DEFINE FIELD IF NOT EXISTS corpus_id ON ca_summary_node TYPE string;
DEFINE FIELD IF NOT EXISTS unit_id ON ca_summary_node TYPE string;
DEFINE FIELD IF NOT EXISTS level ON ca_summary_node TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS parent_id ON ca_summary_node TYPE option<string>;
DEFINE FIELD IF NOT EXISTS child_ids ON ca_summary_node TYPE string DEFAULT '[]';
DEFINE FIELD IF NOT EXISTS summary_text ON ca_summary_node TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS headline ON ca_summary_node TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS summary_tokens ON ca_summary_node TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS source_tokens ON ca_summary_node TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS content_hash ON ca_summary_node TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS model_used ON ca_summary_node TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS strategy ON ca_summary_node TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS revision_fp ON ca_summary_node TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS metadata ON ca_summary_node TYPE string DEFAULT '{}';
DEFINE FIELD IF NOT EXISTS generated_at ON ca_summary_node TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS created_at ON ca_summary_node TYPE datetime DEFAULT time::now();

DEFINE INDEX IF NOT EXISTS idx_ca_summary_node_corpus ON ca_summary_node FIELDS corpus_id;
DEFINE INDEX IF NOT EXISTS idx_ca_summary_node_corpus_level ON ca_summary_node FIELDS corpus_id, level;
DEFINE INDEX IF NOT EXISTS idx_ca_summary_node_corpus_unit ON ca_summary_node FIELDS corpus_id, unit_id UNIQUE;

UPSERT schema_version:v19 SET version = 19;
