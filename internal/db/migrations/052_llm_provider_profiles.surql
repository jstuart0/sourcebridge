-- 050_llm_provider_profiles.surql
--
-- LLM provider profiles delivery — Slice 1.
--
-- This migration is a defense-in-depth complement to the runtime
-- schema-ensure performed by:
--   - SurrealLLMProfileStore.EnsureSchema(ctx)
--   - SurrealLLMConfigStore.EnsureProfilesSchemaExtensions(ctx)
--   - SurrealLivingWikiRepoSettingsStore.EnsureProfilesSchemaExtensions(ctx)
--
-- The runtime ensure runs at every boot in cli/serve.go BEFORE the
-- profile-aware adapter is wired and BEFORE REST handlers can serve
-- writes. This SQL file exists so a fresh DB seeded purely from the
-- migration directory (e.g., test scaffolding) lands the same shape
-- without depending on runtime code execution.
--
-- All statements are idempotent (DEFINE … IF NOT EXISTS).
--
-- Plan: thoughts/shared/plans/2026-04-29-llm-provider-profiles.md (Slice 1)

-- Profile rows: one per saved LLM configuration. Each row owns its own
-- per-profile encrypted api_key under the sbenc:v1 envelope. The
-- name_key field carries lowercase(trim(name)) so the UNIQUE INDEX
-- enforces case-insensitive uniqueness across replicas (codex-M2).
DEFINE TABLE IF NOT EXISTS ca_llm_profile SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS name ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS name_key ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS provider ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS base_url ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS api_key ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS summary_model ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS review_model ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS ask_model ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS knowledge_model ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS architecture_diagram_model ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS report_model ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS draft_model ON ca_llm_profile TYPE string;
DEFINE FIELD IF NOT EXISTS timeout_secs ON ca_llm_profile TYPE int;
DEFINE FIELD IF NOT EXISTS advanced_mode ON ca_llm_profile TYPE bool;
DEFINE FIELD IF NOT EXISTS created_at ON ca_llm_profile TYPE datetime;
DEFINE FIELD IF NOT EXISTS updated_at ON ca_llm_profile TYPE datetime;
-- Reconciliation watermark for the rolling-deploy window: new-code
-- writes set this to ca_llm_config:default.version after the bump.
-- Old pods (pre-deploy) do NOT touch this field. The resolver
-- compares workspace.version > active_profile.last_legacy_version_consumed
-- to detect and reconcile old-pod legacy writes (codex-H2 / r1b).
DEFINE FIELD IF NOT EXISTS last_legacy_version_consumed ON ca_llm_profile TYPE int DEFAULT 0;
DEFINE INDEX IF NOT EXISTS ca_llm_profile_name_key_unique ON ca_llm_profile FIELDS name_key UNIQUE;

-- ca_llm_config:default extensions — workspace-level pointer + observability
-- timestamp. The legacy provider/api_key/model fields ALREADY exist
-- (defined in this file's predecessor 044_ca_llm_config_version.surql);
-- they remain in place for the rolling-deploy dual-read window. After
-- the rollout is verified on thor, a follow-up cleanup plan drops them.
-- DEFAULT '' / now is critical: legacy SaveLLMConfig writes only the
-- legacy fields, so an UPSERT against a fresh ca_llm_config:default
-- row would otherwise fail the SCHEMAFULL TYPE check on these new
-- fields.
DEFINE FIELD IF NOT EXISTS active_profile_id ON ca_llm_config TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS updated_at ON ca_llm_config TYPE datetime DEFAULT time::now();

-- Per-repo override extension: the `living_wiki_llm_override` nested
-- object gains a `profile_id` key. When non-empty the resolver uses
-- the picked profile instead of the inline fields. Slice 3 wires the
-- read/write path; the field is defined here so it's always present
-- before any reader reaches it (codex-H4).
DEFINE FIELD IF NOT EXISTS living_wiki_llm_override.profile_id ON lw_repo_settings TYPE string DEFAULT '';
