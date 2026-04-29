# SourceBridge security model

This page documents the deliberately-unauthenticated surface and the
trust assumptions behind `sourcebridge login` and `sourcebridge setup claude`.

---

## Endpoints reachable without authentication

The following endpoints are reachable without a session or bearer token,
by design:

| Endpoint | Method | What it returns | Why pre-auth |
|---|---|---|---|
| `/healthz`, `/readyz` | GET | Liveness / readiness | Required for orchestrator health probes |
| `/metrics` | GET | Prometheus metrics | Required for monitoring scrape. **Deployment note:** if Prometheus metrics contain sensitive operational data, place SourceBridge behind an auth proxy (e.g. Cloudflare Access, oauth2-proxy) to restrict this endpoint. |
| `/auth/info` | GET | Which auth methods (local / OIDC) the server has configured | Required to render the web login screen |
| `/auth/desktop/info` | GET | Same as `/auth/info`, plus setup state, for the CLI and IDE plugins | Required to drive `sourcebridge login` method auto-detection and to render the correct first-touch UI |
| `/auth/setup` | POST | Initial admin-password setup (10 req/min per IP) | Required to complete first-run setup before any user exists |
| `/auth/login` | POST | Web session login (10 req/min per IP) | Required to authenticate |
| `/auth/desktop/local-login` | POST | Desktop / CLI local-password auth (10 req/min per IP) | Required to complete CLI login without OIDC |
| `/auth/desktop/oidc/start` | POST | Initiates an OIDC desktop auth session (10 req/min per IP) | Required to start the CLI's browser-based OIDC flow |
| `/auth/desktop/oidc/poll` | GET | Polls for a completed OIDC desktop auth session (10 req/min per IP) | Required to retrieve the token after the user completes OIDC in the browser |
| `/auth/logout` | POST | Session invalidation | Required for logout (no auth needed to invalidate a session) |
| `/auth/oidc/login` | GET | OIDC redirect to the identity provider | Required for the browser-based OIDC flow |
| `/auth/oidc/callback` | GET | OIDC callback — completes the browser-based web login | Required for the browser-based OIDC flow |
| `/api/v1/mcp/http` (HEAD only) | HEAD | 204 if MCP is enabled, 404 if not | Used by the web UI to detect MCP availability before the user has logged in (see trade-off below) |

All other `/api/v1/*` and GraphQL endpoints require a valid session JWT or
`ca_...` bearer token.

---

## Trade-off — unauthenticated fingerprinting

The endpoints above let an unauthenticated observer determine that a host
is running SourceBridge and which auth methods are configured. This is the
deliberate trade-off for letting the web UI render the correct first-touch
flow (login screen vs MCP-disabled banner) without requiring the user to
be logged in first.

We do **not** disclose:
- Server version (no version header on these responses)
- Tenant data, user lists, or repository lists
- Configuration values beyond the boolean auth flags
- MCP session IDs or session counts

The `HEAD /api/v1/mcp/http` probe specifically reveals only "MCP is
enabled" — no config details, no session state. Xander's adversarial
audit (finding H4) rated this as a known and accepted trade-off; see
`thoughts/shared/reviews/2026-04-28-cloud-install-security-review-xander.md`
and the comment on the handler in `internal/api/rest/router.go`.

If your deployment requires hiding these surfaces entirely, place
SourceBridge behind an auth proxy (e.g. Cloudflare Access, oauth2-proxy)
that pre-authenticates every request — these endpoints will be auth-walled
at the proxy layer.

---

## Trust assumptions in `sourcebridge login`

`sourcebridge login --server <URL>` trusts the server at `<URL>` to
return a legitimate `auth_url` for the OIDC start response. A malicious
server could return a phishing URL. Mitigations:

- The CLI prints the host portion of `auth_url` **before** opening the
  browser (`"Opening browser to authenticate via <host>..."`). Verify
  the host matches your identity provider.
- `--no-open` prints the full URL for manual inspection before you
  copy it into a browser.
- For zero-trust deployments, mint API tokens manually at
  `<your-server>/settings/tokens` and use `SOURCEBRIDGE_API_TOKEN`
  instead of `sourcebridge login`.

---

## Rate limits on auth endpoints

All credential-submission and desktop-auth endpoints share a 10 req/min
per-IP rate limit. The global limit on all other endpoints is 100 req/min
per IP. This matches the budget of comparable developer tools (GitHub CLI,
npm, AWS CLI).

Shared NAT environments (office, school) are subject to per-IP budgets
across all users at that IP. If real false-positives are observed in
production, move the read-only `/auth/desktop/info` probe to a separate
higher-budget group; this is a one-line change in `internal/api/rest/router.go`.
