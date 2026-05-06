# CLAUDE.md

This file provides guidance to AI assistants working in this repository.

## Project Overview

SourceBridge is a requirement-aware code comprehension platform — a "field guide" for unfamiliar codebases.

## Architecture

- **Go API Server** (`internal/`, `cmd/`) — HTTP/GraphQL API, auth, indexing
- **Python Worker** (`workers/`) — gRPC service for AI reasoning, linking, knowledge generation
- **Web UI** (`web/`) — Next.js 15 / React 19 dashboard
- **Proto** (`proto/`) — gRPC service definitions

**Capability tiers and quality gates**: Living Wiki generation applies tiered
quality-gate thresholds so that local/open-weight models (Ollama, smaller
open-weights) are not uniformly rejected by gates calibrated for frontier
models. The three tiers are `frontier`, `mid`, and `local`; an unregistered
model falls back to pattern-matching on provider/model name. Tiers are set
per-model in the Admin → Comprehension → Model Registry (`/admin/comprehension/models`).
See [`docs/admin/llm-config.md`](docs/admin/llm-config.md#capability-tiers-and-quality-gates)
for the full operator runbook and threshold table reference.

## Recent refactors

**2026-05-05 web runtime API proxy fix** — 3 commits, `1fee78b..873bc53`.
Replaces `next.config.ts rewrites()` with a Next.js middleware at
`web/src/middleware.ts` that proxies `/api/*`, `/auth/*`, `/healthz`,
`/readyz`, and `/metrics` to the upstream API at request time.

Load-bearing constraints for future-Claude:

- The middleware reads `SOURCEBRIDGE_WEB_DEV_PROXY || 'http://localhost:8080'`
  at request time. **Do not switch this back to `NEXT_PUBLIC_*`** — webpack's
  DefinePlugin inlines `NEXT_PUBLIC_*` vars at build time (including in
  server-side middleware bundles), which is exactly the bug that was fixed.
- The `next.config.ts` `env: { NEXT_PUBLIC_API_URL: ... }` block is
  **mandatory and must not be deleted**. Its sole consumer is the `"use client"`
  page at `web/src/app/(app)/settings/appearance/page.tsx`, which needs
  DefinePlugin inlining in the client bundle.
- Future Next.js native handlers under `web/src/app/api/<name>/route.ts` will
  be silently proxied to the Go API unless their path is added to the exclusion
  guard in `middleware.ts`. `/api/health` is the currently-excluded path.

Plan: `thoughts/shared/plans/active-2026-05-05-deliver-web-runtime-api-proxy.md`

**2026-05-04 system audit refactor** (CA-155) — 74 commits, `a176b6f..89c85f3`.
Full-codebase audit covering security hardening, GraphQL deduplication, subsystem
registration, MCP tool refactor, web UI consolidation, and infrastructure
hardening. No public surface removed.

- Plan: [`thoughts/shared/plans/2026-05-04-system-audit-refactor.md`](thoughts/shared/plans/2026-05-04-system-audit-refactor.md)
- Audit synthesis: [`thoughts/shared/audits/2026-05-04-system-audit-refactor.md`](thoughts/shared/audits/2026-05-04-system-audit-refactor.md)
- Phase reports (codex r2):
  - [`thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase0.md`](thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase0.md)
  - [`thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase1.md`](thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase1.md)
  - [`thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase2.md`](thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase2.md)
  - [`thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase3.md`](thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase3.md)
  - [`thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase4.md`](thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase4.md)
  - [`thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase5.md`](thoughts/shared/plans/2026-05-04-system-audit-refactor.codex-r2-phase5.md)

## Subsystem registration

`internal/appdeps/appdeps.go` defines `AppDeps` — the canonical registry of
shared application-layer dependencies. Both `rest.Server` and `graphql.Resolver`
hold a `*AppDeps` pointer constructed once in `NewServer`.

To add a new subsystem dependency:
1. Add a field to `AppDeps` in `internal/appdeps/appdeps.go`.
2. Add the matching field on `graphql.Resolver` (exported) and/or `rest.Server`
   (unexported), as appropriate.
3. Add one line to the relevant sync helper (`syncResolverDepsFromAppDeps` or
   `syncServerDepsFromAppDeps`).

`ClusteringHook` is intentionally absent from `AppDeps` — it is a closure
constructed at wiring time and does not belong in the long-lived registry.

## Legacy CodeAware naming

This project was originally called **CodeAware** and still contains `CODEAWARE_*`
environment variables, `codeaware` Kubernetes resource names, `ca_*` database
table names, and other legacy references that are deliberately preserved to avoid
breaking deployed infrastructure.

Consult [`docs/codeaware-legacy-census.md`](docs/codeaware-legacy-census.md)
before renaming anything. The census classifies each reference as KEEP (deployed
infra dependency), DEFER (DB table / k8s resource), or RENAME (safe internal
name). The `CODEAWARE_*` env vars in `internal/` are **KEEP** and must not be
removed — they are runtime fallbacks alongside the `SOURCEBRIDGE_*` equivalents.

## Building

```bash
make build          # Build Go binary + web frontend
make build-worker   # Install Python worker deps
make test           # Run all tests
make lint           # Run all linters
make proto          # Regenerate protobuf stubs
```

## Running

```bash
# Quick start with Docker
docker compose up

# Local development (run each in its own terminal)
make dev            # Start API server
make dev-web        # Start web frontend
make dev-worker     # Start Python AI worker (required for agentic + embeddings + review)
```

## Configuration

Configuration via `config.toml` or environment variables with `SOURCEBRIDGE_` prefix.
See `config.toml.example` for all options.

**LLM provider / API key / models**: managed in the admin UI at
`/admin/llm`, which is the source of truth across every replica. The
configmap is bootstrap-only. See [`docs/admin/llm-config.md`](docs/admin/llm-config.md)
for the resolution order, the per-call structured-log verification
ritual, encryption-at-rest details, and the operator runbook for
removing API keys from the configmap.

**Encryption at rest**: the API resolves `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE`
(preferred) or `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` (literal env) at startup; on
hub-compose installs an init container auto-bootstraps the key into the
`sourcebridge-secrets` volume — `docker compose up -d` works zero-touch.
`docker compose down -v` deletes the volume and the key with it; see
[`docs/admin-runbooks/encryption-key-setup.md`](docs/admin-runbooks/encryption-key-setup.md)
for the resolution order, rotation procedure, and the `down -v` data-loss warning.

## Telemetry

Anonymous install telemetry is sent from `internal/telemetry/telemetry.go` to
`https://telemetry.sourcebridge.ai/v1/ping`. Opt-out rules and the collected-fields
table live in [`TELEMETRY.md`](TELEMETRY.md).

- Public dashboard: <https://telemetry.sourcebridge.ai/dashboard>
- Collector/worker source: `/Users/jaystuart/dev/sourcebridge-telemetry/` (separate repo)
- Dashboard and badge hide installations flagged `is_test`; toggle on the
  dashboard or append `?include_test=1` to any stats URL to include them.
- Jay's own dev/test installs should be marked with the admin endpoint
  (`POST /v1/admin/mark-test`, bearer token in Vaultwarden as
  **SourceBridge Telemetry → ADMIN_TOKEN**) so the public numbers reflect
  real users. Obvious patterns (`platform=test`, ingress-URL versions) are
  auto-flagged at ingest; `version=dev` is not, to avoid hiding contributors.

When adding new telemetry fields:
1. Update the ping struct and send site in `internal/telemetry/telemetry.go`.
2. Mirror the field in the collector repo (`schema.sql`, worker handlers, and a
   new `migrations/NNN_*.sql` for existing databases).
3. Add the field to `TELEMETRY.md`'s collected-fields table so the opt-in
   disclosure stays accurate.

## Legacy Name: CodeAware

This project was originally called **CodeAware**. It has since been renamed to **SourceBridge**, but remnants of the old name exist throughout the codebase — environment variables with the `CODEAWARE_` prefix, Go module paths, internal references, config keys, Kubernetes resource names, and database records.

When you encounter a `CODEAWARE_` or `codeaware` reference:
- There is likely a `SOURCEBRIDGE_` / `sourcebridge` counterpart already in place
- If you can safely replace the old reference without breaking anything, do so
- **Do not** rename things that would break runtime behavior — e.g. database table names, persisted config keys, Kubernetes service names that other services resolve by name, or environment variables that deployed infrastructure depends on
- When in doubt, leave it and note it for a future cleanup pass

## Safety

- Never commit credentials or `.env` files
- Always specify namespace explicitly with kubectl

## Plane Project

- Workspace: agile-solutions-group
- Project ID: d3fa4bd8-1177-4364-88a7-aae69698b75d
- Project Name: CodeAware
- Identifier: CA
