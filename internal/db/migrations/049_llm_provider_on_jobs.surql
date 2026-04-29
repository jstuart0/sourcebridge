-- 049_llm_provider_on_jobs.surql
--
-- Adds the `llm_provider` column to ca_llm_job and ca_llm_job_log so
-- the Monitor page and per-provider metrics can attribute work to a
-- specific provider (anthropic / openai / ollama / lmstudio / etc.).
-- R3 slice 3.
--
-- Existing rows default to empty string. Backfilling historical jobs
-- is meaningless — the provider info doesn't exist on disk anywhere
-- (the resolver populates llm_provider on enqueue, not retroactively).
-- Operators reading old rows see "" for the provider column.
--
-- LLM-backed subsystems (knowledge, reasoning, requirements, linking,
-- contracts, qa, living_wiki, and clustering with job_type=
-- relabel_clusters) MUST populate this on every enqueue. The AST lint
-- in internal/llm/orchestrator/llm_provider_lint_test.go fails CI on
-- regression. CPU-bound subsystems (graph clustering) leave it empty.
--
-- Plan: thoughts/shared/plans/2026-04-29-config-source-of-truth-r3.md slice 3.

DEFINE FIELD IF NOT EXISTS llm_provider ON ca_llm_job TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS llm_provider ON ca_llm_job_log TYPE string DEFAULT '';
