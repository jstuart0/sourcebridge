-- Migration 054: Quality-gate tier on model capabilities
--
-- CA-150: Adds `quality_gate_tier` to the model registry so operators can
-- explicitly tag a model as frontier/mid/local. The tier drives Living Wiki
-- quality-gate threshold selection (Phase 4). Empty string means "unknown" —
-- the pattern classifier in modeltier.ClassifyByPattern acts as a fallback.
--
-- ASSERT constraint mirrors the three named constants in
-- internal/llm/modeltier/tier.go (TierFrontier, TierMid, TierLocal) plus
-- the empty string TierUnknown. This is a Go-only API-server field; the
-- Python worker does not read it.
--
-- All statements are idempotent (DEFINE … IF NOT EXISTS).

DEFINE FIELD IF NOT EXISTS quality_gate_tier ON ca_model_capabilities TYPE string
  DEFAULT ''
  ASSERT $value IN ['', 'frontier', 'mid', 'local'];

UPSERT schema_version:v54 SET version = 54;
