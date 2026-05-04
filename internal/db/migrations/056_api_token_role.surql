-- Migration 056: Add role field to ca_api_token (SEC-2)
--
-- Before this migration, internal/auth/middleware.go hardcoded Role: "admin"
-- for every API token bearer — any authenticated user who created a token
-- silently became an admin (xander C2, CRITICAL).
--
-- Fix: store role on the token record.  New tokens default to "user"
-- (least privilege) via application code.  Every pre-existing token is
-- backfilled to "admin" to preserve the behaviour operators relied on;
-- selective migration was rejected because APIToken has no field that
-- reliably distinguishes operator-created admin tokens from bootstrap
-- tokens (codex CR-1).
--
-- The persisted table name remains ca_api_token (legacy CodeAware schema;
-- NOT renamed per CLAUDE.md "Legacy Name: CodeAware" policy).
--
-- All statements are idempotent.

-- 1. Add the role field (option<string> so existing SCHEMAFULL rows are valid
--    during the window between this DEFINE and the UPDATE below).
DEFINE FIELD IF NOT EXISTS role ON TABLE ca_api_token TYPE option<string>;

-- 2. Backfill every pre-existing row to "admin" to preserve current behaviour.
--    This is intentionally unconditional: any token that existed before this
--    migration was being treated as admin by the old hardcoded middleware, so
--    writing "admin" here changes nothing for existing operators.
UPDATE ca_api_token SET role = 'admin' WHERE role = NONE OR role = '';

UPSERT schema_version:v56 SET version = 56;
