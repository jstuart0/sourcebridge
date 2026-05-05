# CSRF and API token role enforcement runbook

This runbook covers two security controls introduced (or hardened) in Phase 0
of the 2026-05-04 system audit refactor. It is most relevant to operators
upgrading from a pre-audit install.

## CSRF protection

**Finding**: SEC-6 (audit). CSRF protection was already `true` by default in
`internal/config/config.go` (`CSRFEnabled: true`). Phase 0 Slice 2 verified the
default, added a test asserting it, and added structured logging so the CSRF
state is visible at startup.

**Operator action**: none required. CSRF is on by default and was already on for
all existing installs. If you explicitly set `SOURCEBRIDGE_CSRF_ENABLED=false`
in your environment, remove that override unless you have a specific reason to
disable it (e.g. a reverse proxy that handles CSRF separately).

**Verification**:

```bash
# Startup log line added in Phase 0 Slice 2:
docker logs sourcebridge-api 2>&1 | grep -i csrf
# Expected: level=INFO msg="CSRF protection enabled"
```

## API token role enforcement

**Finding**: SEC-2 (audit). Before the audit, API tokens had no `role` field.
All token-authenticated requests were treated as if the token holder had admin
access. Phase 0 Slice 4 introduced a `role` column on `ca_api_token` and
enforces least-privilege defaults.

### Migration behavior

The Phase 0 migration (`migrations/`) writes `role='admin'` for every existing
token row. This preserves current behavior for all pre-existing tokens — no
existing integrations break. New tokens created after the migration default to
`viewer` unless an admin explicitly sets a higher role.

### Legacy admin token override (`APITokenLegacyAdminDefault`)

A `Security.APITokenLegacyAdminDefault` config key exists for operators who
need to maintain backward-compatible admin-default behavior during a transition
period. Its default is `false` (secure). Set it to `true` only if you have
tokens that were created after the migration and must behave as admin for
compatibility reasons.

```toml
[security]
api_token_legacy_admin_default = true  # temporary override — remove when tokens are re-issued
```

Or via environment variable:

```bash
SOURCEBRIDGE_SECURITY_API_TOKEN_LEGACY_ADMIN_DEFAULT=true
```

**This override should be treated as temporary.** Re-issue affected tokens with
explicit role assignments and then remove the override.

### User-scoped token routes

`/api/v1/tokens` (user self-service CRUD for the caller's own tokens) is
intentionally outside the `RequireRole(admin)` gate. Users can manage their
own tokens without admin privileges. Admin-only token operations (listing all
users' tokens, revoking arbitrary tokens) remain gated.

## Related

- Plane ticket: [CA-155](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/797d0038-6493-49dc-8307-d7c54d3f6611/) (Phase 0 Slices 2 and 4)
- Plan: [`thoughts/shared/plans/2026-05-04-system-audit-refactor.md`](../../thoughts/shared/plans/2026-05-04-system-audit-refactor.md) Phase 0 Slices 2 and 4
- Commits: Phase 0 range `a176b6f..8db8daa`

---
*Documented by scott on 2026-05-04.*
