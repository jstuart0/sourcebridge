# Multi-tenancy and the OSS edition

**Decision recorded: 2026-05-15 — OSS multi-tenant isolation is enterprise-only and will remain so.**
This document closes Plane ticket CA-157 (SEC-9 follow-up).

---

## Background

OSS SourceBridge builds assign `tenant=default` to all repositories. The server emits a
startup log at the INFO level noting this behaviour. The design is intentional and was
codified during the 2026-05-04 system audit refactor (Phase 5, Slice 7). The
`repoChecker` field on `rest.Server` is populated only when the binary is built with the
enterprise tag; in OSS builds it is always nil, and `lazyRepoAccessMiddleware` is a
pass-through.

## Why tenant isolation is enterprise-only

Real multi-tenant isolation requires several non-trivial components that are deliberately
out of scope for the OSS edition:

1. **Per-tenant API token scoping.** Today's API tokens grant access to the full project
   graph. Per-tenant scoping requires a token–tenant binding table, a tenant ID on every
   token mint, and a cross-tenant read check on every protected handler.

2. **Per-tenant database filtering on every read.** All SurrealDB queries touch a shared
   namespace. Enforcing isolation requires either a per-tenant namespace/database
   partition or a `WHERE tenant_id = $tid` clause threaded through every query in the
   store layer (~165 caller files as of the CA-183 ctx-threading campaign).

3. **Per-tenant rate limiting and quota enforcement.** The current rate limiter is
   per-IP and per-user. Per-tenant quotas require a separate accounting layer.

4. **Audit logging with tenant attribution.** Compliance frameworks expect every data
   access to carry a tenant identity in the audit trail.

5. **Operator UI for tenant lifecycle management.** Creating, suspending, and deleting
   tenants, managing their allowed repositories, and granting cross-tenant access grants
   are features with no analogue in the OSS admin UI today.

The OSS edition is designed for single-team or single-organisation use where every
authenticated operator is implicitly authorised for all repositories. Adding the five
components above would expand the OSS surface area substantially with no clear OSS use
case — operators who need real isolation are running the enterprise edition for the
related compliance and audit features.

## What OSS operators should expect

- Every repository you index is visible to every authenticated user of the OSS instance.
- API tokens minted via `/api/v1/tokens` grant access to the full project graph; there
  is no per-token tenant scoping in OSS.
- The boot-time log line is informational and not a vulnerability indicator — it confirms
  the expected single-tenant posture.
- `TenantFilteredStore` (in `internal/graph/filtered.go`) exists in the OSS codebase but
  is never installed on the request path in OSS builds; it is registered only by the
  enterprise `registerEnterpriseRoutes` hook.

## When to consider the enterprise edition

If your operational requirements include any of:

- Multiple independent teams using the same SourceBridge instance without cross-team
  data visibility
- Compliance frameworks that require tenant isolation (SOC 2, ISO 27001, HIPAA)
- Per-team rate limits or quota enforcement
- Audit trails attributable to specific tenants

…then evaluate the enterprise edition. The OSS codebase ships the `TenantFilteredStore`
and `RepoAccessChecker` interfaces as stable extension points; the enterprise build
populates them.

## Code references

| Symbol | File | Role |
|---|---|---|
| `repoChecker` field | `internal/api/rest/router.go:312` | nil in OSS; set by enterprise hook |
| `lazyRepoAccessMiddleware` | `internal/api/rest/router.go:398` | pass-through when `repoChecker == nil` |
| `WithRepoChecker` | `internal/api/rest/router.go:110` | option to inject the enterprise checker |
| `TenantFilteredStore` | `internal/graph/filtered.go` | enterprise-only query filter |
| `RepoAccessChecker` | `internal/api/middleware/` | interface the enterprise build satisfies |
