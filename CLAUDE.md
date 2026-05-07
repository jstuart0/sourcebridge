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

**2026-05-07 qwen3.6 confidence regression remediation (CA-173)** — 4 commits, `952f88e..4f3f079`.
Restores Living Wiki deep-render confidence from the regressed 4H/0M/12L back toward
the April baseline (14H/2L) for qwen3.6:35b-a3b-moe and analogous local models. CA-169
raised `CliffNotesRenderer.deep_parallelism` from 2 to 4; under four simultaneous
16k-output deep-group calls, qwen3.6 on Ollama runs into KV-cache pressure and emits
NDJSON instead of a JSON array. `parse_json_sections` had no NDJSON branch, so it fell
back and stub-filled 12 of 16 sections as `confidence="low"` — quality gates were never
reached. Two-part fix: NDJSON recovery in `parse_json_sections` (phase 1), and a
provider-aware default for `deep_parallelism` (`2` for local providers, `4` for cloud —
preserves CA-169's cloud throughput, restores April baseline for Ollama/vLLM operators).

Load-bearing constraints for future-Claude:

- **`parse_json_sections` NDJSON branch** (`workers/knowledge/parse_utils.py`): the
  recovery splits on `}\s*(?:\r?\n)+\s*{`, wraps fragments as a JSON array, and accepts
  the result only if `≥2 fragments AND every fragment is a dict`. Do not remove or relax
  the `≥2 dict` floor — it's the safeguard against the false-positive trap where a single
  valid JSON object whose `content` field contains `}\n{` gets split into corrupt halves.
  Pinned by `test_parse_json_sections_ndjson_does_not_split_embedded_braces`.
- **`CliffNotesRenderer.deep_parallelism` and `deep_repair_parallelism`** are no longer
  literal field defaults. `__post_init__` resolves from a precedence chain: private
  `_deep_parallelism_override` field (constructor) → `SOURCEBRIDGE_CLIFF_NOTES_DEEP_PARALLELISM`
  env var → provider-aware default (`2` local / `4` cloud). Public field type stays `int`;
  private override is `int | None` (Decision 6 — keeps semaphore consumers' `int`
  annotation green). Cloud default stays `4` (CA-169 throughput preserved); local default
  is `2` (April baseline). A structured-log line `cliff_notes_deep_parallelism_resolved`
  fires at renderer construction with fields `deep_source` / `deep_repair_source` — valid
  source values are `"constructor"`, `"env"`, `"default_local"`, `"default"`.
- **`is_local_provider(provider_name)` in `workers/common/llm/concurrency.py`** is the
  canonical predicate for local-vs-cloud classification — reads `_HOST_GATED_PROVIDERS`.
  Do not fork the source-of-truth set into other files. The two `if provider == "ollama"`
  thinking-suppression dispatches at `workers/common/llm/openai_compat.py:229,569` are
  intentionally NOT migrated to `is_local_provider` — they dispatch Ollama-native
  `/api/chat` thinking-suppression (load-bearing per the 2026-05-06 orchestrator-capacity
  entry). Migrating them would silently re-enable thinking on qwen models.
- **`ConcurrencyGatedProvider.provider_name` @property** forwards the wrapped provider's
  name. This is what makes the local-default-2 fire in production. Without it,
  `getattr(wrapper, "provider_name", "")` returns `""` and every Ollama operator gets
  cloud default `4`. Pinned by
  `test_concurrency_gated_provider_ollama_resolves_cliff_notes_deep_parallelism_2`.
- **`_LOCAL_PROBE_PROVIDERS` was deleted** from `workers/__main__.py` (phase 2). The
  call site at `__main__.py:418` now uses `is_local_provider`. Do not re-introduce a
  parallel set.

Plan: `thoughts/shared/plans/active-2026-05-07-diagnose-qwen-confidence-regression.md`
Runbook: [`docs/admin/llm-config.md`](docs/admin/llm-config.md#deep-render-parallelism-cliff-notes)

**2026-05-06 orchestrator capacity detection** — 8 commits, `e730009..e1fe4b1`.
Fixes three compounding Living Wiki throughput issues end-to-end: capacity
mismatch between orchestrator goroutine pool and upstream LLM, empty-content
retry accounting, and `/no_think` suppression unreliability on Ollama.

Load-bearing constraints for future-Claude:

- `OpenAICompatProvider` has a provider-specific branch for Ollama-native
  `/api/chat` dispatch (`think: false`) when `provider_name == "ollama"` and
  `disable_thinking` is True. **Do not remove this thinking it's redundant.**
  Ollama's OpenAI-compat shim silently ignores `chat_template_kwargs`,
  `/no_think`, and top-level `think` / `extra_body.think` — verified
  empirically against qwen3.5:9b + qwen3.6:27b (Mac Studio, Ollama 0.21.0).
  The `chat_template_kwargs` path stays for llama.cpp users (works there).
- The new `UpstreamCapacityProvider` interface on `coldstart.Config` clamps
  `MaxConcurrency` to the upstream LLM's real parallelism. Cache invalidation
  is per-profile-edit. Nil-safe end-to-end: `Resolver` constructed without
  `Deps` in tests, orchestrator clamp site also nil-guards. The 256 hard
  ceiling is enforced at three layers: SurrealDB `ASSERT`, Go-side adapter
  clamp, and Python validation.
- gRPC auth interceptor is wired both sides: Go `WithPerRPCCredentials`
  (`internal/worker/client.go`) and Python `_GrpcAuthInterceptor`
  (`workers/`). Honors `SOURCEBRIDGE_SECURITY_GRPC_AUTH_SECRET`
  (comma-separated for rotation). Worker logs a startup WARN when bound to a
  non-loopback address without a secret configured.
- **Wiring fix (commit `e1fe4b1`, ian mid-build):** `AppDeps.UpstreamCapacityProvider`
  was populated in `router.go` but never passed into `coldstart.Config{}` in
  `repository_living_wiki.resolvers.go`, making Phase 2's capacity clamp a no-op
  in every production cold-start. This fix is what makes the feature live.

Plan: `thoughts/shared/plans/active-2026-05-06-deliver-orchestrator-capacity-detection.md`
Investigation: `thoughts/shared/investigations/2026-05-06-ollama-think-suppression-empirical.md`
Runbook: [`docs/admin/llm-config.md`](docs/admin/llm-config.md#backend-parallelism-and-the-max_concurrent_calls-field)

**2026-05-06 worker LLM concurrency refactor** — 7 commits, `3274ade..6eed915`.
Universal per-`(provider, base_url)` gate registry in the Python worker:
every LLM and embedding call passes through a host or per-kind semaphore,
jitter-aware tenacity retry loop, optional RPM limiter, and tok/s ring
buffer. Eliminates the 5×3=15-attempt storm from stacked hand-rolled
retries. `GetProviderCapabilities.max_concurrent_calls` is now sourced
from the gate's effective cap for the resolved context, not bootstrap
config, so Go and Python agree on capacity by construction. Phase 7
extends `/api/v1/admin/llm/activity` with a `gate_snapshot` field and
adds a live "LLM Gate Activity" section to the admin monitor page.

Load-bearing constraints for future-Claude:

- **Don't re-enable SDK retry** (`max_retries=0` on `AsyncOpenAI` and
  `AsyncAnthropic`). The tenacity wrapper owns retry. Re-enabling SDK
  retry produces 5×3=15-attempt storms per Decision 3.
- **Don't add a `[llm.concurrency]` TOML section.** Concurrency is
  operator-tunable via env vars, not `config.toml`. Decision 7.
- **The kill switch is the rollback path**: `SOURCEBRIDGE_LLM_CONCURRENCY_WRAPPER_ENABLED=false`
  reverts to pre-refactor behavior without redeploy. Use it before
  assuming the gate is the problem.
- **Registry is constructed-once-passed-by-reference** — constructed in
  `workers/__main__.py` and `workers/common/cli_main.py` only. No
  module-level singletons. Every factory call (`create_llm_provider`,
  `create_embedding_provider`, etc.) receives `gate_registry=` as a
  required kwarg.
- **Don't delete the empty-content retry** at
  `workers/common/llm/openai_compat.py` (around lines 249–313). It
  handles `<think>`-budget exhaustion (`stop_reason=length` + empty
  visible content) — it is NOT a network retry and is explicitly distinct
  from the tenacity wrapper retry.
- **Gate is authoritative for `GetProviderCapabilities`**: the worker's
  `GetProviderCapabilities` handler reads the registry's effective cap
  for the resolved-context `(provider, base_url)` via
  `workers/reasoning/servicer.py`. Don't bypass back to
  `WorkerConfig.llm_max_concurrent_calls` except in the legacy fallback
  path (kill switch off).
- **Host gate vs. per-kind gate classification is per-provider, not
  configurable per call.** Local providers (`ollama`, `vllm`,
  `llama-cpp`, `sglang`, `lmstudio`) share one host gate across LLM and
  embedding. Cloud providers (`openai`, `anthropic`, `gemini`,
  `openrouter`) use per-kind gates. `openai-compatible` defaults host;
  flip with `SOURCEBRIDGE_LLM_PROVIDER_OPENAI_COMPATIBLE_GATING=per_kind`.
- **Don't fork the cross-language plumbing.** `/api/v1/admin/llm/activity`
  (REST) and `KnowledgeStreamProgress` (proto) are the sole channels for
  gate snapshot and per-job tok/s. Don't add a new endpoint or proto
  field; extend these.

Plan: `thoughts/shared/plans/active-2026-05-06-deliver-worker-llm-concurrency.md`
Investigation: `thoughts/shared/investigations/2026-05-06-diagnose-llm-throughput-rotten.md`
Decisions log: `thoughts/shared/plans/active-2026-05-06-deliver-worker-llm-concurrency.decisions.md`
Runbook: [`docs/admin/llm-config.md`](docs/admin/llm-config.md#operator-concurrency-tuning)

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
