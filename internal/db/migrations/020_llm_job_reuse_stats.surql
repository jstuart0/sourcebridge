-- Migration 020: structured cache-reuse stats on llm jobs

DEFINE FIELD IF NOT EXISTS reused_summaries ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS leaf_cache_hits ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS file_cache_hits ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS package_cache_hits ON ca_llm_job TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS root_cache_hits ON ca_llm_job TYPE int DEFAULT 0;
