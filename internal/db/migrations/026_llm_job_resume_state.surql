-- Migration 026: Persist resumable job state for active knowledge reruns

DEFINE FIELD IF NOT EXISTS cached_nodes_loaded ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS total_nodes ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS resume_stage ON ca_llm_job TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS skipped_leaf_units ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS skipped_file_units ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS skipped_package_units ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS skipped_root_units ON ca_llm_job TYPE int DEFAULT 0;

UPSERT schema_version:v26 SET version = 26;
