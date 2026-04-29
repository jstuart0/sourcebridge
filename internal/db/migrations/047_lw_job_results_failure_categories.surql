-- Migration 047: per-page failure-category parallel array on lw_job_results.
--
-- Populated by the cold-start runner so the wiki-settings UI can render a
-- one-line failure-breakdown summary above the per-page exclusions list.
-- The new field is parallel to excluded_page_ids: position i of one matches
-- position i of the other.
--
-- Uses TYPE array<string> (not bare TYPE array) per the lesson learned in
-- migration 041. SCHEMAFULL bare-array fields drop or reject contents at the
-- wire level; all other primitive-string arrays on lw_job_results
-- (excluded_page_ids, generated_page_titles, exclusion_reasons) are already
-- array<string> after migration 041, and this matches.
--
-- This is a separate file from migration 046 because the migration runner
-- skips files whose version <= already-applied version. Once 046 is applied
-- in production (schema_version=46), editing 046 in place would silently
-- no-op for that install.

DEFINE FIELD IF NOT EXISTS exclusion_failure_categories
    ON TABLE lw_job_results TYPE array<string> DEFAULT [];

UPSERT schema_version:v47 SET version = 47;
