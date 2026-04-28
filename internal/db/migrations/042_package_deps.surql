-- Migration 042: Package-level dependency aggregation table
--
-- Adds the package_dep table which stores pre-computed package-level
-- caller/callee edges derived from raw import records (ca_import).
-- One record per (repo_id, package) pair; built by
-- GraphStore.RecomputePackageDependencies after each index run.
--
-- This enables the living-wiki orchestrator to populate Callers/Callees on
-- architecture pages without re-scanning every raw import at render time.

DEFINE TABLE package_dep SCHEMAFULL;
DEFINE FIELD repo_id     ON package_dep TYPE string;
DEFINE FIELD package     ON package_dep TYPE string;
DEFINE FIELD imports     ON package_dep TYPE array<string> DEFAULT [];
DEFINE FIELD imported_by ON package_dep TYPE array<string> DEFAULT [];
DEFINE FIELD updated_at  ON package_dep TYPE datetime;

DEFINE INDEX idx_package_dep_repo ON package_dep FIELDS repo_id;
DEFINE INDEX idx_package_dep_key  ON package_dep FIELDS repo_id, package UNIQUE;

UPSERT schema_version:v42 SET version = 42;
