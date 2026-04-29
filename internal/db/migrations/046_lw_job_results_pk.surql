-- Migration 046: enforce uniqueness on lw_job_results.job_id
--
-- Background:
--   Before this migration the lw_job_results table had no uniqueness
--   constraint on job_id, so the cold-start runner's Save() (which used
--   CREATE) could append duplicate rows for the same logical run if a
--   retry path ever re-entered the closure. Slice 1 of the prior plan
--   set MaxAttempts=1 on the cold-start enqueue to prevent in-process
--   retries, but the duplicate-row hazard remained at the store layer.
--
--   This migration plus the JobResultStore.Save() rewrite (UPSERT-by-job_id
--   keyed on a deterministic record id derived from job_id) close that
--   hazard at the persistence layer regardless of the caller path.
--
-- Operator runbook for failed boots (index-uniqueness violation):
--   1. Preflight to size the issue:
--      SELECT job_id, count() AS c FROM lw_job_results
--        GROUP BY job_id HAVING c > 1;
--   2. Drop UI history (safe — lw_job_results is summary state, not
--      generated wiki content; lw_pages holds the real content):
--      DELETE FROM lw_job_results;
--   3. Restart the API pod so this migration re-runs cleanly.

DEFINE INDEX IF NOT EXISTS idx_lw_job_results_job_id
    ON TABLE lw_job_results COLUMNS job_id UNIQUE;

UPSERT schema_version:v46 SET version = 46;
