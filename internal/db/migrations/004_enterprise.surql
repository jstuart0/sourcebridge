-- Migration 004: Enterprise tables
-- These tables support multi-tenancy, team management, billing,
-- audit logging, SSO config, notifications, and org settings.
-- All tables use the ca_ent_ prefix to distinguish from OSS tables.

-- Tenant / Organization
DEFINE TABLE IF NOT EXISTS ca_ent_tenant SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS name ON ca_ent_tenant TYPE string;
DEFINE FIELD IF NOT EXISTS slug ON ca_ent_tenant TYPE string;
DEFINE FIELD IF NOT EXISTS plan ON ca_ent_tenant TYPE string DEFAULT 'free';
DEFINE FIELD IF NOT EXISTS created_at ON ca_ent_tenant TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_tenant TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_tenant_slug ON ca_ent_tenant FIELDS slug UNIQUE;

-- Team membership
DEFINE TABLE IF NOT EXISTS ca_ent_member SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_member TYPE string;
DEFINE FIELD IF NOT EXISTS user_id ON ca_ent_member TYPE string;
DEFINE FIELD IF NOT EXISTS email ON ca_ent_member TYPE string;
DEFINE FIELD IF NOT EXISTS name ON ca_ent_member TYPE option<string>;
DEFINE FIELD IF NOT EXISTS role ON ca_ent_member TYPE string DEFAULT 'member';
DEFINE FIELD IF NOT EXISTS status ON ca_ent_member TYPE string DEFAULT 'active';
DEFINE FIELD IF NOT EXISTS joined_at ON ca_ent_member TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_member_tenant ON ca_ent_member FIELDS tenant_id;
DEFINE INDEX IF NOT EXISTS idx_member_email ON ca_ent_member FIELDS tenant_id, email UNIQUE;

-- Invitations
DEFINE TABLE IF NOT EXISTS ca_ent_invitation SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_invitation TYPE string;
DEFINE FIELD IF NOT EXISTS email ON ca_ent_invitation TYPE string;
DEFINE FIELD IF NOT EXISTS role ON ca_ent_invitation TYPE string DEFAULT 'member';
DEFINE FIELD IF NOT EXISTS token_hash ON ca_ent_invitation TYPE string;
DEFINE FIELD IF NOT EXISTS status ON ca_ent_invitation TYPE string DEFAULT 'pending';
DEFINE FIELD IF NOT EXISTS invited_by ON ca_ent_invitation TYPE string;
DEFINE FIELD IF NOT EXISTS created_at ON ca_ent_invitation TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS expires_at ON ca_ent_invitation TYPE datetime;
DEFINE INDEX IF NOT EXISTS idx_invitation_tenant ON ca_ent_invitation FIELDS tenant_id;
DEFINE INDEX IF NOT EXISTS idx_invitation_token ON ca_ent_invitation FIELDS token_hash UNIQUE;

-- Billing / Subscription
DEFINE TABLE IF NOT EXISTS ca_ent_subscription SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_subscription TYPE string;
DEFINE FIELD IF NOT EXISTS plan ON ca_ent_subscription TYPE string DEFAULT 'free';
DEFINE FIELD IF NOT EXISTS status ON ca_ent_subscription TYPE string DEFAULT 'active';
DEFINE FIELD IF NOT EXISTS stripe_subscription_id ON ca_ent_subscription TYPE option<string>;
DEFINE FIELD IF NOT EXISTS stripe_customer_id ON ca_ent_subscription TYPE option<string>;
DEFINE FIELD IF NOT EXISTS current_period_start ON ca_ent_subscription TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS current_period_end ON ca_ent_subscription TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_ent_subscription TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_subscription TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_sub_tenant ON ca_ent_subscription FIELDS tenant_id UNIQUE;
DEFINE INDEX IF NOT EXISTS idx_sub_stripe ON ca_ent_subscription FIELDS stripe_subscription_id UNIQUE;

-- Usage tracking
DEFINE TABLE IF NOT EXISTS ca_ent_usage SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_usage TYPE string;
DEFINE FIELD IF NOT EXISTS period ON ca_ent_usage TYPE string;
DEFINE FIELD IF NOT EXISTS token_count ON ca_ent_usage TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS api_calls ON ca_ent_usage TYPE int DEFAULT 0;
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_usage TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_usage_key ON ca_ent_usage FIELDS tenant_id, period UNIQUE;

-- Audit log
DEFINE TABLE IF NOT EXISTS ca_ent_audit SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_audit TYPE string;
DEFINE FIELD IF NOT EXISTS actor ON ca_ent_audit TYPE string;
DEFINE FIELD IF NOT EXISTS action ON ca_ent_audit TYPE string;
DEFINE FIELD IF NOT EXISTS resource_type ON ca_ent_audit TYPE string;
DEFINE FIELD IF NOT EXISTS resource_id ON ca_ent_audit TYPE string;
DEFINE FIELD IF NOT EXISTS before_state ON ca_ent_audit TYPE option<string>;
DEFINE FIELD IF NOT EXISTS after_state ON ca_ent_audit TYPE option<string>;
DEFINE FIELD IF NOT EXISTS prev_hash ON ca_ent_audit TYPE string;
DEFINE FIELD IF NOT EXISTS hash ON ca_ent_audit TYPE string;
DEFINE FIELD IF NOT EXISTS created_at ON ca_ent_audit TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_audit_tenant ON ca_ent_audit FIELDS tenant_id;
DEFINE INDEX IF NOT EXISTS idx_audit_actor ON ca_ent_audit FIELDS actor;

-- SSO configuration (per tenant)
DEFINE TABLE IF NOT EXISTS ca_ent_sso_config SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_sso_config TYPE string;
DEFINE FIELD IF NOT EXISTS provider ON ca_ent_sso_config TYPE string DEFAULT 'none';
DEFINE FIELD IF NOT EXISTS enabled ON ca_ent_sso_config TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS status ON ca_ent_sso_config TYPE string DEFAULT 'inactive';
DEFINE FIELD IF NOT EXISTS issuer_url ON ca_ent_sso_config TYPE option<string>;
DEFINE FIELD IF NOT EXISTS client_id ON ca_ent_sso_config TYPE option<string>;
DEFINE FIELD IF NOT EXISTS metadata_url ON ca_ent_sso_config TYPE option<string>;
DEFINE FIELD IF NOT EXISTS entity_id ON ca_ent_sso_config TYPE option<string>;
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_sso_config TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_sso_tenant ON ca_ent_sso_config FIELDS tenant_id UNIQUE;

-- Notification configuration (per tenant)
DEFINE TABLE IF NOT EXISTS ca_ent_notification_config SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_notification_config TYPE string;
DEFINE FIELD IF NOT EXISTS smtp_host ON ca_ent_notification_config TYPE option<string>;
DEFINE FIELD IF NOT EXISTS smtp_port ON ca_ent_notification_config TYPE int DEFAULT 587;
DEFINE FIELD IF NOT EXISTS from_address ON ca_ent_notification_config TYPE option<string>;
DEFINE FIELD IF NOT EXISTS use_tls ON ca_ent_notification_config TYPE bool DEFAULT true;
DEFINE FIELD IF NOT EXISTS configured ON ca_ent_notification_config TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_notification_config TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_notif_tenant ON ca_ent_notification_config FIELDS tenant_id UNIQUE;

-- Organization settings (per tenant)
DEFINE TABLE IF NOT EXISTS ca_ent_org_settings SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_org_settings TYPE string;
DEFINE FIELD IF NOT EXISTS org_name ON ca_ent_org_settings TYPE string DEFAULT 'My Organization';
DEFINE FIELD IF NOT EXISTS data_retention_days ON ca_ent_org_settings TYPE int DEFAULT 90;
DEFINE FIELD IF NOT EXISTS default_role ON ca_ent_org_settings TYPE string DEFAULT 'member';
DEFINE FIELD IF NOT EXISTS updated_at ON ca_ent_org_settings TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_orgsettings_tenant ON ca_ent_org_settings FIELDS tenant_id UNIQUE;

-- Tenant-to-repository mapping
DEFINE TABLE IF NOT EXISTS ca_ent_tenant_repo SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_tenant_repo TYPE string;
DEFINE FIELD IF NOT EXISTS repo_id ON ca_ent_tenant_repo TYPE string;
DEFINE FIELD IF NOT EXISTS assigned_at ON ca_ent_tenant_repo TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_tenant_repo ON ca_ent_tenant_repo FIELDS tenant_id, repo_id UNIQUE;

-- GitHub App installations
DEFINE TABLE IF NOT EXISTS ca_ent_github_installation SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS installation_id ON ca_ent_github_installation TYPE int;
DEFINE FIELD IF NOT EXISTS account_login ON ca_ent_github_installation TYPE string;
DEFINE FIELD IF NOT EXISTS tenant_id ON ca_ent_github_installation TYPE option<string>;
DEFINE FIELD IF NOT EXISTS repos ON ca_ent_github_installation TYPE option<array>;
DEFINE FIELD IF NOT EXISTS created_at ON ca_ent_github_installation TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_gh_install ON ca_ent_github_installation FIELDS installation_id UNIQUE;

UPSERT schema_version:v4 SET version = 4;
