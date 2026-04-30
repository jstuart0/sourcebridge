-- 053_knowledge_settings.surql
--
-- CA-122: knowledge-generation safety-net timeout — admin-tunable.
--
-- This is a singleton-row table holding the operator-configured cap
-- on per-call knowledge RPC timeouts (the OUTER safety-net, not the
-- per-phase reaper). The streaming refactor replaces the previous
-- 1h hard wall-clock deadline with this configurable cap (default
-- 4h, range 30 min - 24 h).
--
-- Read path: cli/serve.go's knowledgeTimeoutProvider closure does a
-- live read through a 5s in-memory cache. On Surreal failure: use
-- last-known-good cached value if any; else hardcoded 4h fallback.
-- Defensive read-side clamping in the provider (codex r1 M3) catches
-- a malformed row that escaped the write-side validator.
--
-- Write path: REST PUT /api/v1/admin/knowledge/timeout. Out-of-range
-- values rejected with HTTP 400 (codex r1 L1: reject on write, clamp
-- on read).
--
-- All statements idempotent (DEFINE … IF NOT EXISTS).
--
-- Plan: thoughts/shared/plans/2026-04-29-deep-cliffnotes-deadline-exceeded.md (Phase 5)

DEFINE TABLE IF NOT EXISTS ca_knowledge_settings SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repository_timeout_secs ON ca_knowledge_settings TYPE int
  ASSERT $value >= 1800 AND $value <= 86400;
DEFINE FIELD IF NOT EXISTS updated_at ON ca_knowledge_settings TYPE datetime
  VALUE time::now();
DEFINE FIELD IF NOT EXISTS updated_by ON ca_knowledge_settings TYPE string
  DEFAULT "";
