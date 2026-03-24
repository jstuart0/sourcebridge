-- Migration 009: Harden API token and desktop auth persistence

DEFINE FIELD IF NOT EXISTS prefix ON ca_api_token TYPE option<string>;
DEFINE FIELD IF NOT EXISTS user_id ON ca_api_token TYPE option<string>;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_api_token TYPE option<string>;
DEFINE FIELD IF NOT EXISTS token_kind ON ca_api_token TYPE option<string>;
DEFINE FIELD IF NOT EXISTS client_type ON ca_api_token TYPE option<string>;
DEFINE FIELD IF NOT EXISTS auth_method ON ca_api_token TYPE option<string>;
DEFINE FIELD IF NOT EXISTS device_label ON ca_api_token TYPE option<string>;
DEFINE FIELD IF NOT EXISTS metadata ON ca_api_token TYPE option<string>;
DEFINE FIELD IF NOT EXISTS revoked_at ON ca_api_token TYPE option<datetime>;
DEFINE INDEX IF NOT EXISTS idx_api_token_hash ON ca_api_token FIELDS token_hash UNIQUE;
DEFINE INDEX IF NOT EXISTS idx_api_token_user ON ca_api_token FIELDS user_id;
DEFINE INDEX IF NOT EXISTS idx_api_token_tenant ON ca_api_token FIELDS tenant_id;
DEFINE INDEX IF NOT EXISTS idx_api_token_kind ON ca_api_token FIELDS token_kind;

DEFINE TABLE IF NOT EXISTS ca_oidc_state SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS state ON ca_oidc_state TYPE string;
DEFINE FIELD IF NOT EXISTS expires_at ON ca_oidc_state TYPE datetime;
DEFINE FIELD IF NOT EXISTS consumed_at ON ca_oidc_state TYPE option<datetime>;
DEFINE INDEX IF NOT EXISTS idx_oidc_state_value ON ca_oidc_state FIELDS state UNIQUE;

DEFINE TABLE IF NOT EXISTS ca_desktop_auth_session SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS state ON ca_desktop_auth_session TYPE string;
DEFINE FIELD IF NOT EXISTS token ON ca_desktop_auth_session TYPE option<string>;
DEFINE FIELD IF NOT EXISTS expires_at ON ca_desktop_auth_session TYPE datetime;
DEFINE FIELD IF NOT EXISTS completed_at ON ca_desktop_auth_session TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS consumed_at ON ca_desktop_auth_session TYPE option<datetime>;
DEFINE INDEX IF NOT EXISTS idx_desktop_auth_state ON ca_desktop_auth_session FIELDS state UNIQUE;

-- Retain revoked and expired token/session records for audit visibility.
-- Cleanup can be added later as a separate retention policy task.

UPSERT schema_version:v9 SET version = 9;
