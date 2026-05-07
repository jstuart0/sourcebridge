-- 058_llm_job_process_id.surql
--
-- Adds process_id to ca_llm_job so the orchestrator can identify zombie
-- jobs left behind by a prior process after a supervised restart (CA-175).
--
-- Legacy rows (created before this migration) have process_id = NONE.
-- The startup reconciliation in Orchestrator.reconcileZombieJobs treats NONE
-- as "unknown provenance" and reconciles it the same way as a mismatched
-- process_id — gated on the heartbeat-freshness threshold so that live rows
-- from a rolling-restart peer are not prematurely failed.
--
-- No ASSERT constraint: option<string> allows any string or NONE.
-- No backfill: existing rows keep NONE, which is the correct pre-migration value.

DEFINE FIELD IF NOT EXISTS process_id ON ca_llm_job TYPE option<string>;
UPSERT schema_version:v58 SET version = 58;
