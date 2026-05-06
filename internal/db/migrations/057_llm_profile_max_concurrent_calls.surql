-- 057_llm_profile_max_concurrent_calls.surql
--
-- Adds max_concurrent_calls to ca_llm_profile.
--
-- NULL  = unknown; orchestrator does not clamp — uses configured MaxConcurrency.
-- 0     = explicitly unbounded (frontier APIs / OpenRouter).
-- 1..256 = clamp value: orchestrator limits in-flight requests to this value.
--
-- The ASSERT constraint enforces the same [0, 256] hard ceiling as the
-- Go-side clamp (D9) and the REST-layer validation.  NULL is always
-- permitted (it is the safe default for pre-existing rows).
--
-- The ASSERT expression uses a conditional so that NULL rows pass
-- through without error — SurrealDB evaluates ASSERT against the
-- actual written value, so NULL rows that omit the field pass through
-- cleanly with the existing SCHEMAFULL schema once the field is declared
-- with TYPE option<int>.
--
-- Plan: thoughts/shared/plans/active-2026-05-06-deliver-orchestrator-capacity-detection.md (Phase 5)

DEFINE FIELD IF NOT EXISTS max_concurrent_calls ON ca_llm_profile TYPE option<int>
  ASSERT $value IS NONE OR ($value >= 0 AND $value <= 256);
