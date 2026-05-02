-- Migration 056: Funnel & workflow-adoption telemetry table
--
-- CA-61: Persists first-run funnel and feature-adoption events to a local
-- SurrealDB table so operators can answer "do users get past first-run?"
-- and "which features do they actually use?" without an external service.
--
-- All data stays on the operator's own install (no external egress from
-- this table). See TELEMETRY.md for the full per-field disclosure and
-- the retention statement.
--
-- All statements are idempotent.

DEFINE TABLE ca_funnel_event SCHEMAFULL;

-- Core event fields
DEFINE FIELD event        ON ca_funnel_event TYPE string;
DEFINE FIELD source       ON ca_funnel_event TYPE string;
DEFINE FIELD occurred_at  ON ca_funnel_event TYPE datetime;

-- Identity context — optional because server-side bus events carry no auth context.
DEFINE FIELD user_id      ON ca_funnel_event TYPE option<string>;
DEFINE FIELD tenant_id    ON ca_funnel_event TYPE option<string>;

-- Scope context — empty string when not applicable.
DEFINE FIELD repo_id         ON ca_funnel_event TYPE string DEFAULT '';
DEFINE FIELD artifact_id     ON ca_funnel_event TYPE string DEFAULT '';
DEFINE FIELD requirement_id  ON ca_funnel_event TYPE string DEFAULT '';
DEFINE FIELD symbol_id       ON ca_funnel_event TYPE string DEFAULT '';
DEFINE FIELD artifact_type   ON ca_funnel_event TYPE string DEFAULT '';

-- Freeform payload — stripped of PII at the write layer before storage.
DEFINE FIELD metadata     ON ca_funnel_event TYPE option<object>;

-- Indexes for the two query patterns: "funnel for a user" and "activity on a repo".
DEFINE INDEX idx_funnel_event_user   ON ca_funnel_event FIELDS user_id;
DEFINE INDEX idx_funnel_event_repo   ON ca_funnel_event FIELDS repo_id;
DEFINE INDEX idx_funnel_event_time   ON ca_funnel_event FIELDS occurred_at;

UPSERT schema_version:v56 SET version = 56;
