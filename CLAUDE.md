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

**2026-05-09 store ctx threading + decomposition (CA-183 + CA-182)** — 12 commits, `71f6542` + `150c094` + `8a3ccd0` + `eefa122` + `fa084b4` + `2eaad0d` + `1244396` + `037bf2d` + `0105f3d` + `7081107` + `c67be11` + `73bd8f3`.
Closes CA-183 (CRITICAL — `context.Background()` discarded in every store method, breaking request cancellation and tracing) and CA-182 (HIGH — `internal/db/store.go` monolith at ~4,500 LOC). Five phases shipped under a green-CI discipline (every intermediate commit passes `go build`, `go vet`, `go test -short -race`, and the full integration suite).

Phase 1 (`71f6542` + `150c094`) — signature threading. Every method on `*SurrealStore` (and the 6 store interfaces it satisfies: `GraphStore`, `KnowledgeStore`, `JobStore`, `comprehension.SettingsStore`, `livingwiki.RepoSettingsStore`, `SummaryNodeStore`) gains `ctx context.Context` as its first parameter. The unexported `diagramDocumentPersistence` interface (`internal/api/rest/diagram_document.go`) and the 9 local subset interfaces (`architecture.DiagramStore`, orchestrator `PackageDepsProvider`, QA package interfaces — `RepoLocator`, `GraphExpander`, `ArtifactLookup`, `RequirementLookup`, `SymbolLookup`, `FileReader`, `UnderstandingReader`, `templates.SymbolGraph`, `search.Booster`, `graph.KnowledgeFreshnessProvider`) are all updated in lockstep. Phase 1 covers ~165 caller files / ~2,450 LOC; all `internal/db/` method bodies still call the package-local `ctx()` helper so the package builds.

Phase 2 (`8a3ccd0` + `eefa122`) — `ctx()` helper deletion. The `func ctx() context.Context { return context.Background() }` helper is deleted; all ~185 call sites in `internal/db/` are replaced with the threaded parameter.

Phase 3 (`fa084b4`) — file decomposition. `internal/db/store.go` is deleted and its contents split into 6 per-domain files: `repository_store.go`, `requirement_store.go`, `cluster_store.go`, `analytics_store.go`, `index_result.go`, `helpers.go` (all `package db`). Pure file moves — no logic changes.

Phase 4 (`2eaad0d`) — CLAUDE.md update for phases 1–4.

Phase 5 (`1244396` + `037bf2d` + `0105f3d` + `7081107` + `c67be11`) — MCP handler ctx threading (codex r2 BLOCK). All 29 MCP tool dispatch handlers previously registered via `noCtxHandler` (which silently dropped the live request context) converted to `withCtxHandler`. Handler method signatures updated to accept `ctx context.Context`; every store/knowledge/cluster call inside those handlers now receives the threaded ctx. Three deliberate `context.Background()` detachments preserved: `indexingSvc.Import`, `indexingSvc.Reindex` (background ops that must outlive the request), `changeDispatcher.Submit` (router background work survives agent disconnect). Two `DeleteClusters` ctx-drops in `internal/db/index_result.go` and `internal/db/repository_store.go` fixed. `buildDiffReviewStructural` in `mcp_review.go` upgraded to accept and thread ctx. `internal/db/index_result.go` file-level comment corrected: `MergeIndexResult` is fail-closed, not a multi-step writer. Regression test `TestFormerlyNoCtxHandlerTools_CtxThreadedToStore` in `mcp_dispatch_test.go` verifies that a formerly-`noCtxHandler` tool (`get_index_status`) now propagates the request ctx sentinel value to `store.GetRepository`.

Load-bearing constraints for future-Claude:

- **`func ctx() context.Context { return context.Background() }` is GONE.** Don't add it back. Every method on `*SurrealStore` (and the 6+ store interfaces) takes `ctx context.Context` as first parameter; threading that through is the contract. Bridging with `context.Background()` at a call site is an explicit rollback of CA-183.
- **`internal/db/store.go` is DELETED.** Methods live in `repository_store.go`, `requirement_store.go`, `cluster_store.go`, `analytics_store.go`, `index_result.go`, `helpers.go` — all `package db`. Don't recreate `store.go`.
- **Multi-step writers expose partial-write windows on cancellation**: `StoreIndexResult`, `ReplaceIndexResult`, `MergeIndexResult`, `RecomputePackageDependencies` issue 100+ sequential `surrealdb.Query` calls without a transaction wrapper (`internal/db/tx.go` `RunInTx` is documented no-op). Request cancellation now propagates to SurrealDB. Callers MUST NOT rely on these being all-or-nothing. Follow-up: `CA-TBD-store-multi-step-write-atomicity`.
- **`var _ diagramDocumentPersistence = (*db.SurrealStore)(nil)`** at `internal/api/rest/diagram_document.go:29` is the compile-time gate that `*SurrealStore` (now spread across 6 files) still satisfies the unexported `diagramDocumentPersistence` interface. The 3 satisfying methods live in `internal/db/diagram_document_store.go`. Don't move them without updating this assertion.
- **`impactReportRow` lives with the impact-report methods** in `analytics_store.go`, NOT in `helpers.go`. Decomposition rule: cross-domain row types (`surrealRepo`, `surrealFile`, `surrealSymbol`, `surrealModule`, `surrealRequirement`, `surrealLink`) live in `helpers.go`; single-domain row types live with their domain.
- **TenantFilteredStore `hasAccess` gating preserved** on the same 8 federation methods as wave-3 P8 (CA-203). The ctx threading didn't introduce new methods; the gating set is unchanged.
- **Embedded-interface override audit findings**: `callRecorder` (`internal/graph/filtered_test.go:42-79`, 8 overrides), `countingGraphStore` (`internal/api/rest/mcp_change_impact_test.go:495-502`), `truncatingGraphStore` (`internal/api/rest/mcp_requirement_tools_test.go:1137-1148`) all updated to take ctx as first parameter. Future test override patterns must follow.
- **Phase 1 scope was ~165 caller files / ~2,450 LOC**: every package holding a `graphstore.GraphStore` value or implementing one of the 6+ store interfaces or any of the 9 local subset interfaces listed above.
- **`noCtxHandler` adapter is STILL DEFINED** (as a backward-compat shim used by `safeDispatch` in tests) but MUST NOT be used for new tool registrations. All 29 former `noCtxHandler` registrations in `coreTools()` and all per-phase `register*Tools()` functions now use `withCtxHandler`. Adding a new tool with `noCtxHandler` is a silent ctx-drop; the compiler won't catch it. `TestFormerlyNoCtxHandlerTools_CtxThreadedToStore` catches regressions at the dispatch layer.
- **Three deliberate `context.Background()` detachments in MCP handlers must not be changed to the threaded ctx**: `indexingSvc.Import` and `indexingSvc.Reindex` (background indexing jobs that must survive request cancellation), and `changeDispatcher.Submit` (router background work that must complete even if the agent disconnects). These are commented in the source.
- **`buildDiffReviewStructural` takes `ctx context.Context` as first param** (added in Phase 5). Any future caller must thread the request ctx. The function calls `resolveDiffTouchedSymbols`, `GetLinksForSymbol`, `GetRequirementsByIDs`, and `GetSymbol` — all ctx-aware.

Plan: `thoughts/shared/plans/active-2026-05-09-deliver-store-ctx-decomp.md`

**2026-05-09 audit-remediation wave 3: P8 security hardening — SSRF denylist, gRPC reflection gate, SSE tenant filter, TenantFilteredStore gating (CA-202, CA-312, CA-203, CA-205)** — 5 commits, `56380d2..2552036`.

Phase CA-202 (`56380d2`) — gRPC reflection gate tightened to dual-key: both
`SOURCEBRIDGE_WORKER_DEBUG=true` AND `SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED=true`
must be set. Setting only `WORKER_DEBUG=true` no longer enables reflection.

Phase CA-312 (`dd437e7`) — SSRF protection for git clone/pull: `ValidateGitURLForClone`
in `internal/indexing/pathutil/pathutil.go` blocks URLs resolving to private/loopback/
link-local/CGNAT/ULA/unspecified (0.0.0.0/::)/multicast addresses. Scheme allowlist:
https://, ssh://, and SCP-form only; http:// is always rejected. `gitCloneCmd` and
`gitPullCmd` both pass `-c http.followRedirects=false`. `Config.Indexing.AllowPrivateGitHosts`
is a dangerous opt-in for self-hosted internal networks (default false).

Phase CA-203 (`7245203`) — `TenantFilteredStore` methods now gate on tenant access; 8
methods return nil/empty on cross-tenant denial (opaque nil) instead of bypassing the
allowed-repo set. `GetRepoLink(linkID)` was added to the `GraphStore` interface.

Phase CA-205 (`9c3b1ab`) — per-tenant SSE filter: `handleSSE` drops events whose
`repo_id`/`repository_id` is not in the tenant's allowed set. Events with no repo
identifier are dropped defensively on non-default tenants. `events.Bus.Subscribe`
returns `*Subscription`; callers must call `Unsubscribe()` on cleanup (X-L2 leak fix).
Subscription leak in the SSE handler was fixed with `defer s.eventBus.Unsubscribe(sub)`.

Reconcile pass (this commit) — 5 punch-list items from xander, ian, codex r2:

- **codex r2 H1**: `isPrivateOrInternalIP` now also rejects `IsUnspecified()` (0.0.0.0,
  ::), `IsMulticast()`, and `IsInterfaceLocalMulticast()`. Tests T15-T16 added.
- **xander H1**: `gitPullCmd` now passes `-c http.followRedirects=false` (was on clone
  path only; pull path was unguarded against redirect-chain bypass).
- **ian H1**: `TestSSERepoIDFromEvent_FallbackToRepositoryID` + 7 `TestHandleSSE_*` tests
  added in `internal/api/rest/sse_test.go`. Multi-tenant tests use direct handler
  invocation with injected context (TenantMiddleware is enterprise-only).
- **codex r2 L1**: MCP fallback path in `callIndexRepository` now calls
  `pathutil.ValidateGitURLForClone` before `CreateRepository` on remote URLs.
  `allowPrivateGitHosts` field added to `mcpHandler`, wired in router.go.
- **codex r2 M2**: `CLAUDE.md`, `docs/going-to-production.md`, and
  `docs/admin/configuration.md` updated; stale `test_worker_debug_config.py`
  docstring corrected.

Load-bearing constraints for future-Claude:

- **`WORKER_DEBUG=true` alone does NOT enable gRPC reflection.** Both
  `SOURCEBRIDGE_WORKER_DEBUG=true` AND `SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED=true`
  must be set. `test_worker_debug_config.py` now documents this explicitly.
- **`SOURCEBRIDGE_INDEXING_ALLOW_PRIVATE_GIT_HOSTS`** is a dangerous opt-in (default
  false); only enable on single-operator self-hosted instances. The config default is
  pinned by `TestIndexingDefaultsAllowPrivateGitHostsFalse`.
- **`GraphStore` interface grew `GetRepoLink(linkID)`** — all three implementers
  (SurrealStore, MemStore, TenantFilteredStore) provide it. Adding a method to
  `GraphStore` requires updating all three or the build breaks.
- **`events.Bus.Subscribe` returns `*Subscription`** (was void before CA-205).
  Callers must call `s.eventBus.Unsubscribe(sub)` on cleanup. The SSE handler uses
  `defer s.eventBus.Unsubscribe(sub)`. Any future subscriber that doesn't unsubscribe
  will leak a reference to the closed-over channel after the handler returns.
- **8 `TenantFilteredStore` methods now gated by `hasAccess`** — opaque-nil return on
  cross-tenant denial. Future additions to `GraphStore` that return data must follow
  the same gating pattern in `TenantFilteredStore`.
- **`csrfProtectionWithName` (CSRF) and `ValidateGitURLForClone` (CA-312) both have
  safe-by-default operator opt-in flags** following the `Config.Security.*Enabled` /
  `Config.Indexing.*Enabled` convention.
- **`WithEventBus(bus)` server option** added to allow test injection of a pre-built
  `*events.Bus`. Do not use in production (the default is created in `NewServer`).
- **MCP `callIndexRepository` fallback path validates URLs** before calling
  `CreateRepository`. The `allowPrivateGitHosts` field on `mcpHandler` mirrors
  `Config.Indexing.AllowPrivateGitHosts` and is wired in `router.go` alongside
  `indexingSvc`.

Plan: `thoughts/shared/plans/active-2026-05-08-deliver-p8-security-highs.md`

**2026-05-09 audit-remediation wave 3: P1 deferred — CSRF admin route group + Bearer-bypass tightening with frontend X-CSRF-Token injection (CA-198 + CA-201)** — 4 commits, `19dcd6b..(reconcile)`.

Closes the two security findings deferred from wave-1 P1: CA-198 (CRITICAL — no CSRF
middleware on admin route group) and CA-201 (HIGH — Bearer-bypass too permissive,
skipped CSRF whenever an Authorization header was present, even when a session cookie
also accompanied the request). Wave-1 codex r1 had surfaced two Criticals on the
original combined plan: the frontend hadn't been designed to inject `X-CSRF-Token`
on every browser write (so backend-only fix would 403 every browser mutation) and a
cookie-name confusion bug (the original snippet checked the CSRF cookie name, not the
session cookie name, in the bypass logic). This campaign solves both.

Phase 1 (frontend, ruby, `19dcd6b`) — new `web/src/lib/csrf-token-store.ts` module with
synchronous cookie read + on-demand `GET /api/v1/csrf-token` refresh (single-flight via
in-module `Promise<string|undefined> | null` cleared in `finally`; bare `fetch` never
`authFetch` to prevent retry recursion; SSR-safe; 5s AbortController timeout). Header
injection wired into `authFetch`, URQL `fetch:` wrapper, `askStream`, `telemetry`
(`sendBeacon` → `fetch keepalive`), and `TopBar.tsx` logout. ReadableStream guard skips
retry on stream bodies. Single retry per request, no recursion. Comprehensive grep
audit embedded as a comment block at the top of `csrf-token-store.ts` classifies every
browser fetch path.

Phase 2 (backend, jackson, `d684d27`) — `csrfProtectionWithName(csrfCookieName,
sessionCookieName string, fullCoverage bool)` is the new three-parameter signature.
The third parameter is the runtime gate: with `fullCoverage=false` the middleware is
**bit-exact backwards-compat with today's code** (Bearer alone bypasses); with
`fullCoverage=true` the bypass requires no session cookie present AND the second
authenticated route group + auth-helper routes (`/auth/logout`,
`/auth/change-password`) get gated. The flag `Config.Security.CSRFFullCoverageEnabled`
defaults `false` so a single PR can ship both phases harmlessly; the operator flips
the flag to enable the new behaviors. All three behaviors flip atomically. New tests
delete `TestCSRFMiddlewareSkipsBearerAuth` (one-arg signature is gone) and add
behavior tests for cookie-name distinct-by-name (using real
`s.jwtMgr.SessionCookieName()` / `CSRFCookieName()` accessors), bearer+session
mismatched-token, flag-off-preserves-today-behavior, flag-off-second-group-no-op-with-
Phase1-header.

Reconcile pass (`bfc5622` — backend + frontend) — three diff reviewers (ian, codex r2,
xander) all flagged the same Critical: backend `csrfReject` was emitting
`{"error":"CSRF token missing"}` (title case + space) but the frontend's `isCsrfError`
detection looked for `csrf_token_missing` (lowercase + underscore), so the
refresh-and-retry path was dead code in production. Backend now emits machine-readable
lowercase-underscore codes, sets `Content-Type: application/json`, exposes the new
flag in `/api/v1/admin/config`, and emits a `security_csrf_full_coverage_state`
startup log line in `cli/serve.go`. Frontend tests now use real `Response` constructors
with the canonical body shape, the URQL test seam is `_csrfAwareFetch` exported for
direct coverage, conditional `if (fetchMock.mock.calls.length > 0)` assertions are
unconditional `expect(fetchMock).toHaveBeenCalled()`. New tests cover the
xander-CSRF-M1 Authorization-header-on-retry contract and the xander-CSRF-M2
missing-cookie + 403 + refresh + retry end-to-end shape.

Documentation polish (this entry's commit) — `docs/admin/llm-config.md` flip runbook
gets explicit `kubectl rollout status` / `helm upgrade --wait` instructions; the
`kill -HUP` example is tightened to call out that HUP is only correct if the wrapper
interprets it as a full restart (the flag is startup-wired). The `csrf_token_missing`
vs `csrf_token_mismatch` reason descriptions are corrected to reflect that the
former fires on missing cookie, the latter on missing-or-mismatched header.

Load-bearing constraints for future-Claude:

- **`csrfProtectionWithName` is THREE-parameter and never two.** The signature
  `(csrfCookieName, sessionCookieName string, fullCoverage bool)` is the entire
  defense against the cookie-name confusion bug that bit the wave-1 P1 plan.
  Collapsing to two parameters would re-introduce that risk; never do it. The
  third parameter is the wiring-time decision of which behavior the middleware
  has, NOT a closure read of `s.cfg` — that's intentional so unit tests can pin
  behavior with a single call without a `*Server` fixture.
- **`Config.Security.CSRFFullCoverageEnabled` defaults `false`.** Pinned by
  `TestSecurityDefaultsCSRFFullCoverageEnabled` at `internal/config/config_test.go`.
  Changing the default to `true` would activate the Bearer-bypass tightening + admin
  route gate on deploy without operator opt-in, breaking every browser write at
  process restart for installations that haven't yet rolled out the Phase-1
  frontend bundle. Don't flip the default — operators flip per the runbook.
- **The flag is wired at router construction time, NOT read per-request.** Flipping
  the env var or config requires a full API process restart / Kubernetes Deployment
  rollout. The runbook in `docs/admin/llm-config.md` describes the rollout-wait
  ritual; future code that reads the flag dynamically per request would invalidate
  the runbook. Keep the wiring at construction time, or fully replace the runbook.
- **`/auth/login` and `/auth/setup` are intentionally CSRF-exempt** — no session
  exists at call time, and the routes are outside the protected groups. Don't
  add CSRF to login; it would break the bootstrap flow. `/auth/logout` is
  CSRF-gated (when the flag is on) but stays public — no auth middleware — so a
  user with an expired session can still clear their browser state.
- **`/auth/change-password` IS gated when the flag is on** — it sits inside an
  existing rate-limited + auth group, with `r.Use(csrfProtectionWithName(...))`
  added conditionally. This protects against a CSRF-driven password change.
- **CSRF cookie name probe in `web/src/lib/csrf-token-store.ts:CSRF_COOKIE_NAMES`
  must stay in sync with backend `JWTManager.CSRFCookieName()` derivation at
  `internal/auth/jwt.go:48-60`.** The backend derives `sourcebridge_csrf` for
  the OSS edition and `sourcebridge_<edition>_csrf` for non-OSS. The frontend
  array currently probes `["sourcebridge_csrf", "sourcebridge_enterprise_csrf"]`.
  If a third edition lands, this list MUST extend OR the fast cookie-read path
  silently falls back to GET-on-403 for that edition. There's a comment in the
  module documenting this sync contract — preserve it.
- **Backend response body for CSRF rejection is `{"error":"csrf_token_missing"}`
  / `{"error":"csrf_token_mismatch"}` — lowercase + underscore.** Title-case +
  space breaks the frontend retry detection. Pinned by
  `TestCSRFMiddlewareRejectsMissingHeader` and `TestCSRFMiddlewareRejectsMismatch`
  body assertions in `csrf_test.go`. Don't change the strings without updating
  the frontend `isCsrfError()` checks in lockstep.
- **`csrfReject` sets `Content-Type: application/json`** — using `http.Error()`
  would set `text/plain` and violate the JSON contract; the frontend
  `response.json()` parse may work in browsers anyway, but strict HTTP clients
  / proxies could fail. Don't revert.
- **The drop counter is `atomic.Int64` and NEVER registered as a Prometheus
  metric.** It's an internal log-rate gate only (10/sec via in-package
  `time.Ticker`). Exposing it via `/metrics` (which is publicly scrapable per
  `router.go:803`) creates a covert oracle for the CSRF gate state — xander
  CSRF-5. If a future maintainer wants CSRF rejection metrics, aggregate by
  `reason` only (no per-IP / per-path / per-session-id labels).
- **`refreshCSRFToken()` MUST use bare `fetch`, never `authFetch`.** Calling
  `authFetch` from inside the refresh would create infinite recursion when the
  refresh itself returns 403. The bare-`fetch` rule is enforced by code review,
  not type system; preserve it.
- **`{"error":"csrf_token_missing"}` and `{"error":"csrf_token_mismatch"}` log
  reasons are NOT one-to-one with HTTP request shapes.** Codex r2b L1 noted the
  asymmetry: a request with NO cookie emits `csrf_token_missing`; a request
  with a cookie but no/wrong header emits `csrf_token_mismatch`. Both are
  recoverable via the frontend's transparent retry. The runbook documents this;
  if you're debugging a specific 403, check the `bearer_with_session_cookie`
  field in the structured log to know whether the request was browser-shaped.
- **Phase 1 + Phase 2 ship in one PR; the flag flip is a separate operator
  action.** The earlier "≥24h between PRs" plan was social, not technical;
  the flag-default-false approach replaces it with a hard gate. Future plans
  involving this kind of "deploy frontend before backend gate" coordination
  should use the same flag-default-false pattern; the social-gap pattern is
  unenforceable in the Argo Image Updater pipeline.

Plan: `thoughts/shared/plans/active-2026-05-08-deliver-csrf-admin-frontend-injection.md`
Codex r1: `.codex-r1-plan.md` (verdict iterate); r1b: `.codex-r1b-plan.md` (verdict iterate, mechanical signature alignment); r2: `.codex-r2-diff.md` (verdict iterate, error-string mismatch + restart docs); r2b: `.codex-r2b-diff.md` (verdict iterate, two non-blocking polish items)
Reviews: bob (`.bob.md`), xander (inline), tessa (inline), librarian (inline NEW verdict)
Validation: valerie FIXES REQUIRED initially (2 punch-list items: TestSecurityDefaultsCSRFFullCoverageEnabled + this CLAUDE.md entry); both addressed in this commit

**2026-05-08 audit-remediation wave 2: P5 + P9 + P2 (CA-239..CA-250, CA-304, CA-200)** — 3 commits, `2309b60..b4d7a08`.
Continues the master remediation plan. Closes 14 of the remaining 32 Critical+High audit
findings: 12 HIGH UX (P5), 1 CRITICAL data-loss (P9), 1 HIGH security cipher upgrade (P2).
Master plan: `thoughts/shared/plans/active-2026-05-08-deliver-audit-remediation-master-plan.md`.

P5 (UX terminology + a11y, `2309b60`) — twelve HIGH ruby-audit findings shipped together.
Status badges → human labels + tooltips; queue jargon → plain English; user-facing
surface standardized on "Cliff Notes" everywhere (the audit recommended choosing one
noun consistently — "Cliff Notes" was retained as the user-facing brand;
three-button toolbar → single contextual
action; duplicate "Build understanding" button removed; div onClick patterns now expose
proper button semantics (role, tabIndex, aria-expanded, onKeyDown, focus-visible);
execution-trace select gets sr-only label; monitor page gets a skeleton during first
poll; Test Connection JSON dump → success/failure callout; admin/knowledge auto-refreshes
while generating.

P9 (ReplaceIndexResult data correctness, `a37abb0`) — closed a CRITICAL silent data loss
where re-indexing dropped every test-linkage edge. Two stacked bugs (DELETE missed
ca_tests + relation re-insert loop only handled RelationCalls) combined to make
GetTestsForSymbolPersisted return empty for every symbol after the second indexing pass.

P2 (encryption-at-rest cipher hardening, `b4d7a08`) — replaced sbenc:v1's unsalted
single-pass SHA-256 KDF with sbenc:v2's Argon2id (memory-hard, GPU-resistant) +
per-installation salt. v1 envelopes are transparently decrypted on read via the
legacy v1 KDF (SHA-256 of passphrase), with a one-time-per-process WARN log for
operator visibility; new writes always use v2. Post-wave-2 operational change:
homelab deploy on 2026-05-08 surfaced installations with v1-encrypted data, so
the original fail-closed rejection was rolled back to transparent read-fallback.
Migration to v2 happens lazily as v1 rows get re-saved through normal application
flow. Salt derived deterministically from the encryption key via HMAC — zero
operator burden, but documented as weaker than independent random salt with a
follow-up tracked.

Load-bearing constraints for future-Claude (wave 2):

- **`secretcipher.NewAESGCMCipher` signature is `(passphrase, salt, allowUnencrypted)
  → (*AESGCMCipher, error)`.** All 4 stores (git, llm config, llm profile,
  livingwiki-repo-settings) update the bootstrap path to derive salt via
  `DeriveInstallationSaltFromKey(key)` when no explicit salt option is provided.
  Tests use `MustNewAESGCMCipher` (panic-on-error variant). Production wiring at
  `cli/serve.go:402-415` derives `installSalt` once and shares it across both
  gitCipher and llmCipher — keep this single-salt-per-installation invariant.
- **`DeriveInstallationSaltFromKey` is HMAC-SHA256 with domain-separation tag
  `"sourcebridge-installation-salt-v1"`.** Never change the tag without a follow-up
  migration: every existing v2-encrypted row depends on this exact derivation.
  Future independent-random-salt rollout MUST add a v3 envelope, not modify v1
  derivation.
- **`Validate()` JWT length gate fires only on operator-configured short secrets.**
  Auto-generated path produces 64-hex-char (32 raw bytes) which clears trivially.
  Tests that construct Config{} directly without going through Load() must seed
  a 64-hex placeholder (existing pattern in config_test.go and tests/integration).
- **CA-241 user-facing surface standardized on "Cliff Notes".** The
  internal artifact-type enum (`CLIFF_NOTES`), the Go struct names, and the gRPC
  proto field names are unchanged. Don't rename them — they're stable contracts
  with the worker, MCP, and persisted artifacts.
- **CA-243 panel-level "Build understanding" button is REMOVED.** The page-header
  button (`repositories/[id]/page.tsx`) is now the canonical trigger. Adding the
  panel button back would re-introduce the redundant-mutation double-click bug.
  The empty-state prompt directs users to the header.
- **CA-242 button-consolidation rule.** When a knowledge artifact panel renders
  actions: in-flight → Cancel only; stale or failed → Regenerate (primary); else
  → Refresh (secondary). One button per state. Don't re-introduce simultaneous
  Generate + Refresh + Cancel triples.
- **CA-200 v1 envelope read behavior is transparent, not fail-closed.** Post-wave-2
  operational change (2026-05-08 homelab deploy surfaced installations with v1-encrypted
  data): `Decrypt` transparently handles sbenc:v1 envelopes via the legacy SHA-256 KDF,
  emitting a one-time-per-process WARN log (rate-gated via `loadedV1Once`) for operator
  visibility without breaking reads. `ErrV1EnvelopeRejected` is retained for compile
  compatibility but is no longer returned; it is marked Deprecated. New writes always
  use v2. Migration to v2 happens lazily as v1 rows get re-saved through normal
  application flow. Don't add forced-migration logic without threat-modeling the race
  against concurrent v2 encrypts.
- **CA-304 fix preserves `RelationTests` AND `RelationCalls` in
  ReplaceIndexResult.** Future relation types (e.g., `RelationImports`,
  `RelationOverrides`) added to the indexer MUST be added to BOTH the
  StoreIndexResult AND ReplaceIndexResult relation loops. The shape is
  symmetric; deviating is what produced CA-304 in the first place.
- **CA-304 DELETE block ordering is load-bearing.** Drop ca_tests AND ca_calls
  before ca_symbol; ca_module + ca_file last. The ca_import deletion uses a
  subquery against ca_file — that subquery must run before the ca_file DELETE.
  SurrealDB does not enforce referential integrity on these tables, so order
  matters only for query correctness, but the comment in store.go explains
  this so a future maintainer doesn't reorder.

Plan: `thoughts/shared/plans/active-2026-05-08-deliver-audit-remediation-master-plan.md`
Audit synthesis: `thoughts/shared/audits/finished-2026-05-08-audit-full-codebase.md`

**2026-05-08 audit-remediation wave 1: P4 + P3-partial + P1-subset + P7 (CA-256..260, CA-279, CA-280, CA-311, CA-204, CA-206, NEW-H1, CA-227, CA-228, CA-317, CA-229)** — 5 commits, `db1614c..4a9ac10`.
First wave of the master remediation plan from the 2026-05-08 full codebase audit. Covers
14 of the 47 Critical+High findings end-to-end, plus one new HIGH finding surfaced during
plan review (NEW-H1: Notion-poll webhook unauth). Master plan: `thoughts/shared/plans/active-2026-05-08-deliver-audit-remediation-master-plan.md`.

P4 (UI state-derivation crash hardening, `db1614c`) — five mechanical optional-chain
fixes that close active CA-180-pattern crashes from partial response shapes. Knowledge tab,
admin monitor, admin LLM. Type-only changes; no API surface.

P3 partial (test gaps, `119964e`) — CA-279 fixes `mockKnowledgeStore.ClaimArtifact` to
mirror MemStore claim semantics (was returning `(nil, false, nil)` always — caused
mock/prod divergence). CA-280 adds a snapshot test asserting `"markdown backticks"` is
present in `CLIFF_NOTES_RENDER_TEMPLATE` and `CLIFF_NOTES_SECTION_REPAIR_TEMPLATE` so
future cleanups can't silently regress local-tier confidence (the
`_enforce_deep_confidence_floor` upgrade function depends on backtick-wrapped identifiers).
CA-281, CA-282, CA-283 deferred — handler-coverage tests need test-server harness work.

P1 subset (auth/JWT/pprof/webhook hardening, `b55b623` + `9b17a60`) — campaign re-scoped
mid-flight after codex r1 review identified two Criticals on the original CSRF plan: web
frontend uses Bearer + cookie via `authFetch`/URQL, so tightening CSRF to require token
on Bearer+cookie requests breaks every browser write unless the frontend gets X-CSRF-Token
injection. CA-198 + CA-201 split to a dedicated CSRF-frontend campaign (TBD slug
`2026-05-08-deliver-csrf-admin-frontend-injection`). The four independent fixes shipped
this wave:

- **CA-311** — JWT secret default literal removed; `Validate()` enforces ≥32-byte length
  gate. `ResolveJWTSecret()` mirrors `ResolveEncryptionKey` (file > literal env > unset
  with auto-generated in-memory fallback). New env var
  `SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE`. Helm chart's `randAlphaNum 32` Secret stays
  unchanged and passes the new gate. Docker Compose dev defaults updated to a 64-hex
  publicly-known placeholder so `docker compose up` still boots; `WarnInsecureDefaults`
  flags it. **Breaking** — communicated in CHANGELOG.
- **CA-204** — pprof endpoints now require admin role when `SOURCEBRIDGE_PPROF_ENABLED=true`.
- **CA-206** — Confluence webhook refuses to dispatch when secret unconfigured (returns
  503 with scrubbed body `{"error":"route_unavailable"}`); secret resolved per-request
  via `ConfluenceSecretResolver` so admin-UI changes take effect without restart.
- **NEW-H1** — Notion-poll webhook requires admin bearer auth (was completely
  unauthenticated). Route refuses to register when `NotionPollAuthMiddleware` is nil.

P7 (infra gaps, `4a9ac10`) — CA-227 fail-fast LLM URL placeholders (RFC 2606 .invalid
TLD); CA-228 `storageClassName: ""` in base manifests so cluster default class is used
out of box; CA-317 `WORKER_DEBUG=true` un-hardcoded in compose hub; CA-229 dev compose
SurrealDB creds env-overridable.

Load-bearing constraints for future-Claude:

- **`Validate()` JWT length gate is post-resolution, not pre-resolution.** It runs after
  `Load()` decides between resolved-secret and auto-generated fallback. The auto-generated
  path produces 64 ASCII chars (32 raw bytes hex-encoded) which clears the gate trivially.
  This means a future regression that disables the auto-generate fallback would surface
  as a `Validate()` rejection — not a silent default — which is the desired posture.
- **`ResolveJWTSecret()` and `ResolveEncryptionKey()` share an idiom.** Both: file env
  var first (`_FILE`), literal env var second, source string trichotomy, fallback to
  literal-env on file-missing/empty-file. Future `*_FILE` patterns should mirror this
  exactly — diverging fragments the operator mental model.
- **`KnownInsecureSentinels` is generalized from `InsecureSentinel`.** Adding new shipped
  defaults (e.g., a future template literal) requires adding them to the map, not just
  adding a new const. `InsecureCredentials()` skips empty values so callers don't need
  to nil-guard.
- **`ConfluenceSecretResolver` is new on `LivingWikiWebhookDeps` but the eager
  `ConfluenceWebhookSecret` field stays.** Tests typically use the eager field; production
  wires the resolver. Don't remove the eager field — that breaks every existing test.
- **`NotionPollAuthMiddleware` REQUIRED for the route to register.** A future code path
  that constructs `LivingWikiWebhookDeps` without setting this field will silently 404
  the Notion-poll endpoint. The startup `slog.Warn` in `RegisterLivingWikiRoutes` is the
  signal; don't suppress it.
- **`mockKnowledgeStore.ClaimArtifact` now has real claim semantics.** Tests that
  previously relied on `(nil, false, nil)` will see different behavior — search for
  test sites that branch on `claimed` and verify they still pass with the realistic
  shape.
- **`storageClassName: ""` in base manifests requires a cluster with a default
  StorageClass.** Operators on clusters without a default class must override via
  overlay (the top-of-file comment in `api.yaml` and `surrealdb.yaml` documents the
  patch pattern). For SurrealDB, production operators MUST override with a class that
  has `reclaimPolicy: Retain` — the empty default is for kind / microk8s only.
- **CA-198 + CA-201 explicitly NOT shipped this wave.** A future CSRF-on-admin campaign
  must include a frontend slice that injects `X-CSRF-Token` in `authFetch` and the URQL
  fetch wrapper before the backend gate goes live, OR every browser write breaks with
  403. Plan path: `thoughts/shared/plans/active-2026-05-08-deliver-csrf-admin-frontend-injection.md` (TBD).

Plan: `thoughts/shared/plans/active-2026-05-08-deliver-auth-csrf-jwt-pprof-hardening.md`
Master plan: `thoughts/shared/plans/active-2026-05-08-deliver-audit-remediation-master-plan.md`
Audit synthesis: `thoughts/shared/audits/finished-2026-05-08-audit-full-codebase.md`
Codex r1 (plan): `thoughts/shared/plans/active-2026-05-08-deliver-auth-csrf-jwt-pprof-hardening.codex-r1-plan.md`

**2026-05-08 `build_repository_understanding` failure leaves stage stuck (CA-180)** — 1 commit, `60bb4c2`.
When a `build_repository_understanding` job failed for any reason (LLM unreachable, retry
exhaustion, reaper kill, or startup zombie reconciliation), the `ca_repository_understanding.stage`
field was not transitioned to `FAILED`. It stayed stuck at whatever in-progress stage was active
(`BUILDING_TREE` or `DEEPENING`), so the repo screen kept showing "Generating…" indefinitely while
the jobs screen correctly showed the job as failed. Two stacked bugs closed together.

Backend (root cause): new `MarkRepositoryUnderstandingFailed(understandingID, errorCode, errorMessage string) error`
method added to the `KnowledgeStore` interface with implementations in SurrealStore
(`internal/db/knowledge_store.go`), MemStore (`internal/knowledge/memstore.go`), and the test
mock (`internal/api/rest/mcp_test.go`). New `OnJobFailed func(*llm.Job)` callback added to
`orchestrator.Config` and wired from all three failure paths in the orchestrator:
`finalizeFailed` (retry exhaustion), the reaper stale-job loop (alongside the existing
`OnStaleJob`), and `reconcileZombieJobs` (startup reconciliation — CA-175 origin; previously had
no callback at all). Callback is registered in `internal/api/rest/router.go` gated on
`job.JobType == "build_repository_understanding"`. The SQL update gates on
`WHERE stage INSIDE ['building_tree', 'deepening']` for idempotency.

Frontend (mask fix): `understandingProgressJobView` at `knowledge-tab.tsx:460-477` previously
synthesized a hardcoded `status: "generating"` view for any non-pending/non-generating liveJob.
Now returns `liveJob` whenever non-null; only synthesizes when no live job exists. The `JobProgress`
component already renders `failed` and `cancelled` correctly — no component change needed.

Known cosmetic gap (pre-existing, not a blocker): `error_code` on the written understanding row
sometimes lands as `""` instead of the expected code — a timing window where `GetByID` reads the
job record before `SetError` fully commits. The router-side fallback (`"" → "JOB_FAILED"`) exists
but did not always fire as expected in manual repro. `errorMessage` carries the diagnostic
information. The follow-up ticket **CA-TBD-knowledge-artifact-reconciler-coverage** tracks the
broader gap: the reconciler-callback pattern is unimplemented for living-wiki and knowledge-artifact
jobs (they have the same stuck-on-failure symptom on process restart via that path).

Load-bearing constraints for future-Claude:

- **`OnJobFailed` must fire from all three failure paths.** The reconciler path (`reconcileZombieJobs`,
  CA-175 origin) had no failure callback before CA-180. If a future change adds a fourth job-failure
  path (e.g., manual cancel-as-fail, force-stop), it must also invoke `OnJobFailed` or understanding
  rows will get stuck again.
- **`OnJobFailed` fires inside the `if job != nil` guard in `finalizeFailed`** (after
  `o.publish(EventFailed)`). If `store.GetByID` returns nil (transient store degradation), the
  callback intentionally does not fire — startup reconciliation catches the orphan on the next
  process restart. Do not move the call outside the guard; it would nil-deref on `job.ArtifactID`.
- **The reaper calls both `OnStaleJob` and `OnJobFailed`.** Do not deduplicate. They handle
  different concerns: `OnStaleJob` drives artifact/living-wiki side-effects already wired at
  `router.go:464`; `OnJobFailed` drives understanding-stage propagation. Collapsing them would
  break one or the other path.
- **Receiver is `s.knowledgeStore`, not `s.store`.** `s.store` is `graphstore.GraphStore`
  (`router.go:280`); `s.knowledgeStore` is `knowledge.KnowledgeStore` (`router.go:281`). The
  existing `OnStaleJob` callback at `:465` already establishes this convention. Don't route
  understanding-store writes through the graphstore.
- **Idempotency gate (`WHERE stage INSIDE ['building_tree', 'deepening']`) is load-bearing.**
  Re-firing `MarkRepositoryUnderstandingFailed` on an already-`FAILED` row is a no-op. This is
  what makes the 3-path callback safe under concurrent failure events. Do not relax or remove it.
- **`KnowledgeStore` has THREE implementations**: SurrealStore (`internal/db/knowledge_store.go`),
  MemStore (`internal/knowledge/memstore.go`), and the test mock
  (`internal/api/rest/mcp_test.go:163`). Adding a method to the interface requires updating all
  three or the build breaks. Verified with `grep -rn "MarkRepositoryUnderstandingNeedsRefresh"`.
- **`understandingProgressJobView` must return `liveJob` whenever non-null.** The previous
  `pending|generating`-only filter is what created the hardcoded-"generating" fallback bug. Do not
  reintroduce a status-specific filter on the early-return path.
- **Follow-up scope (out of CA-180):** `ca_knowledge_artifact` and living-wiki jobs have the same
  reconciler-no-callback gap (on process restart, those job types also lack an `OnJobFailed`-
  equivalent on the `reconcileZombieJobs` path). Tracked as
  **CA-TBD-knowledge-artifact-reconciler-coverage**.

Plan: `thoughts/shared/plans/finished-2026-05-08-deliver-understanding-stage-stuck-on-failure.md`

**2026-05-07 SurrealDB v2.6.5 upgrade + option-field NULL remediation (CA-179)** — 1 commit, `6951279`.
Upgrades the SurrealDB integration testcontainer and production pin from v2.2.1 to v2.6.5
across 4 implementation phases. The upgrade surfaces that v2.6.5 enforces strict rejection
of JSON null on `option<…>` schema columns in contexts that earlier versions tolerated.
Phase 1 remediates `SetModelCapabilities` in `internal/db/comprehension_settings_store.go` —
three pointer fields (`cost_per_1k_input *float64`, `cost_per_1k_output *float64`,
`last_probed_at *time.Time`) are now guarded with the conditional-vars closure idiom (matching
`UpdateRequirementFields` at `internal/db/requirement_store.go:546`); nil pointers are never serialised as
JSON null. Phase 1 also discovers that `option<datetime>` columns require `models.CustomDateTime{Time: t}`
binding, not RFC3339 strings. Phase 2 remediates `UpdateRequirementFields` directly (the
conditional-vars idiom was already there; this phase extended it and deleted the old
`UpdateRequirement(id, priority, tags)` method that always SET both columns regardless of
nil). Phase 3 wires the `EnrichRequirement` GraphQL mutation to call `UpdateRequirementFields`
instead of the now-deleted `UpdateRequirement`. Phase 4 updates the testcontainer pin to
v2.6.5 and verifies the full integration suite is green.

Load-bearing constraints for future-Claude:

- **`internal/db/comprehension_settings_store.go:SetModelCapabilities`** uses the
  conditional-vars closure idiom for three `option<…>` fields: `cost_per_1k_input`
  (`*float64` → `option<float>`), `cost_per_1k_output` (`*float64` → `option<float>`),
  and `last_probed_at` (`*time.Time` → `option<datetime>`). `last_probed_at` is guarded
  preemptively — no caller writes it today, but `ModelCapabilities.LastProbedAt` exists on
  the public Go struct, so a future writer would otherwise trip v2.6.5 NULL rejection. Don't
  revert to static SET clauses for any of these three fields.
- **`option<datetime>` fields require `models.CustomDateTime{Time: t}` binding, NOT RFC3339
  strings.** SurrealDB's CBOR codec accepts CBOR tag 12 (datetime) for `option<datetime>`
  columns but rejects RFC3339Nano string format. If a future writer adds a new
  `option<datetime>` SET clause and binds a Go `time.Time` directly (or its
  `.Format(time.RFC3339Nano)`), the write will fail at runtime against v2.6.5. Always wrap
  as `models.CustomDateTime{Time: t}` (the `models` package is the SurrealDB Go SDK's models
  package — import path confirmed in `internal/db/comprehension_settings_store.go`). Other
  `option<datetime>` writers that work today (e.g., `livingwiki_repo_settings_store.go:439-454`)
  use `time::now()` literals server-side, which sidesteps the encoding question entirely.
- **The conditional-vars `setFragment` interpolates into BOTH the UPDATE and CREATE arms** of
  the LET/IF/ELSE statement in `SetModelCapabilities`. Mutating `sets []string` or
  `setFragment` after assignment affects both arms. A future maintainer adding a field "only
  in the UPDATE branch" needs to build a second fragment or accept both arms get the clause
  (which is correct UPSERT behavior anyway). Don't split the LET/IF/ELSE into separate CREATE
  and UPDATE statements without strong justification — adds a round-trip and a race window.
- **Legacy `UpdateRequirement(id, priority, tags)` is DELETED.** Use
  `UpdateRequirementFields(id, RequirementUpdate{Priority: &p, Tags: &t})` instead. Deleted
  from four sites: SurrealStore impl (`internal/db/requirement_store.go`), in-memory store
  (`internal/graph/store.go`), interface (`internal/graph/iface.go`), and tenant-filter
  passthrough (`internal/graph/filtered.go`). Don't reintroduce as a convenience wrapper —
  that's how the v2.6.5 NULL bug got into the codebase.
- **`EnrichRequirement` GraphQL mutation no longer bumps `updated_at` when the LLM returns no
  suggestions.** Before Phase 3, the mutation always issued an UPDATE (touching `updated_at`).
  After Phase 3, `UpdateRequirementFields` short-circuits at `internal/db/requirement_store.go:593` when
  `len(sets) == 1` (only `updated_at` would change). This is more correct semantically but is
  a behavioral shift — downstream consumers keying off `updated_at` to detect "recently
  touched" should use a different signal.
- **Audit `option<…>` writers schema-outward, not Go-pointer-inward.** For every `option<…>`
  schema column in `internal/db/migrations/`, find its Go writer and verify it uses
  CONTENT-payload, conditional-vars, or guarantees non-nil binding. Going Go-pointer-inward
  (look for `*T` fields → check schema) misses the case where the Go field is a value type
  but the schema column is `option<…>` — that pattern silently trips v2.6.5 with no
  JSON-null safety net.

v3.x migration is deferred (separate future ticket). Known break points: `SEARCH ANALYZER` → `FULLTEXT ANALYZER` in migration 033, ~30 `type::thing(…)` call sites need migration to `type::record`, audit for bare `$param =` assignments needing `LET`.

Plan: `thoughts/shared/plans/finished-2026-05-07-deliver-surrealdb-2.6.5-upgrade.md`
Investigation: this session's research brief (sarah, in-conversation)
Plane ticket: CA-179

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

**2026-05-07 knowledge-job slot-stall remediation (CA-174 + CA-175)** — 2 commits, `2dd1149` + `1ed4cfd`.
Two-phase fix for a compounding issue that caused Living Wiki knowledge-artifact
generation to permanently stall the orchestrator slot queue across process
restarts. CA-174 fixes a SurrealDB schema rejection on `option<string>` fields
that produced ERROR-level log noise on every understanding pass. CA-175 fixes
the zombie-job problem: when an orchestrator process exits mid-job and restarts,
the in-flight job held the slot indefinitely because nothing reclaimed it.

Phase 1 (CA-174 — schema): `ReplaceClusters` was using `SET field = $c.field`
with an explicit JSON null when `LLMLabel` was absent. SurrealDB v2.2.1 rejects
null on `option<string>` fields (`Found NULL for field llm_label`). Fix: switch
to `CREATE ... CONTENT $c.content` where `clusterRow` is split into
`clusterContent` (schema fields, `omitempty` on `LLMLabel`) + a `clusterID`
wrapper. `SaveClusters` similarly conditionally includes `llm_label` in vars only
when non-nil. No behavior change for operators; ERROR log noise is eliminated.

Phase 2 (CA-175 — zombie reconciliation): each orchestrator now generates a
per-process UUID at `New()` (`uuid.New().String()`) and stamps it on every job at
`Create()` time. On startup, `reconcileZombieJobs()` runs synchronously before
workers start — it marks any active job whose `updated_at` is older than 90s as
failed with error code `PROCESS_RESTART_RECONCILIATION`, freeing the slot for the
next enqueue. Default `SOURCEBRIDGE_KNOWLEDGE_MAX_CONCURRENCY=1` no longer means
a single supervisor restart blocks all knowledge-artifact generation for 10 minutes.

Load-bearing constraints for future-Claude:

- **`internal/db/cluster_store.go` cluster write path**: `ReplaceClusters` uses
  `CREATE ... CONTENT $c.content` (not `SET field = $c.field`), and `clusterRow`
  is split into `clusterContent` (schema fields, `omitempty` on `LLMLabel`) +
  `clusterID` wrapper (cid + content). Don't revert to `SET` — the SET path with
  an explicit JSON null is what SurrealDB v2.2.1 rejects on `option<string>`
  fields. `SaveClusters` similarly conditionally includes `llm_label` in vars
  only when non-nil.
- **Integration testcontainer pinned to `surrealdb/surrealdb:v2.6.5`** at
  `internal/db/testutil_integration_test.go:31` (matches production — bumped from
  v2.2.1 in CA-179). Version tolerance for `option<…>` NULL rejection is
  **non-monotonic** across SurrealDB versions: v2.2.1 was lenient, v2.3.5 had
  different tolerance that masked the bug during that window, v2.6.5 enforces strict
  rejection. On any future bump, run the full integration suite against the new
  version before landing — not just the targeted writer tests. The CA-174 + CA-179
  fixes (CONTENT-payload + conditional-vars idioms) defend against any version that
  falls anywhere on the tolerance spectrum.
- **`Job.ProcessID` is the per-process UUID stamp**, generated once per
  orchestrator at `New()` and stamped on every job at `Create()` time only — NOT
  at `SetStatus(generating)`. The rolling-restart edge case (P1 creates, P2
  claims) is intentionally handled by the 90s heartbeat-freshness gate, not by
  re-stamping. Don't add a `SetStatusWithProcessID` interface method — explicitly
  rejected in plan review.
- **`reconcileZombieJobs()` runs synchronously in `Orchestrator.New()` before
  workers start**. `reconcileStaleThreshold = 90 * time.Second` is 18× the 5s
  `knowledgeQueueHeartbeatInterval` — don't tighten to <60s without accounting
  for inter-replica clock skew in HA deployments. Don't loosen to >150s without
  impact-checking the user-visible bench-stall window.
- **`Config.SkipStartupReconciliation`** is a positive opt-out flag (zero-value
  false → reconciliation runs in production). `newTestOrchestrator` defaults it
  to `true` to protect the existing test suite. Don't flip the default to true in
  production without re-validating the bench scenario.
- **Migration 058**: `DEFINE FIELD IF NOT EXISTS process_id ON ca_llm_job TYPE option<string>`.
  Idempotent. Legacy rows have `process_id = NONE`; the freshness gate protects
  them during rolling-restart upgrades.

Plan: `thoughts/shared/plans/active-2026-05-07-diagnose-knowledge-slot-stall.md`
Investigation: `thoughts/shared/investigations/2026-05-07-diagnose-knowledge-slot-stall.md`
Runbook: [`docs/admin/llm-config.md`](docs/admin/llm-config.md#knowledge-job-startup-reconciliation-ca-175)

**2026-05-07 deep-render confidence prompt fix (CA-176)** — 1 commit, `f907f16`.
Closes the residual confidence gap left after CA-173+CA-174+CA-175: the post-CA-173
bench still returned 7H/0M/9L against the April 14H/0M/2L baseline. Root cause: a
prompt/upgrade-function mismatch. `_enforce_deep_confidence_floor` in
`workers/comprehension/renderers.py` counts backtick-wrapped identifiers (matched by
`_SPECIFIC_IDENTIFIER_RE`) to determine whether a section earns high confidence — but
`CLIFF_NOTES_RENDER_TEMPLATE` and `CLIFF_NOTES_SECTION_REPAIR_TEMPLATE` only asked the
model to "name specific functions/types/methods" without specifying backtick markdown
format. April's qwen3.6 happened to use backticks by convention; today's runs
didn't for 9 of 16 sections, so those sections were scored low regardless of content
quality. Fix: explicit backtick-format requirement added to both templates, plus
backtick-wrapped identifiers injected into all four `GROUP_FEWSHOT_EXAMPLES` Good
examples to model the format via few-shot. Result: qwen3.6:35b-a3b-q4_K_M on Mac
Studio Ollama at `deep_parallelism=2` scores 16H/0M/0L, exceeding the April baseline
by 2 high sections, with CA-169's 735s wall time preserved (vs April's 3500s).

Load-bearing constraints for future-Claude:

- **Don't remove the backtick instruction from the templates.** The backtick-format
  requirement in `CLIFF_NOTES_RENDER_TEMPLATE` and `CLIFF_NOTES_SECTION_REPAIR_TEMPLATE`
  is what feeds `_enforce_deep_confidence_floor`. Removing it re-regresses local-tier
  model output to ~half its potential confidence rate — the upgrade function runs but
  finds nothing to count.
- **Don't change `_SPECIFIC_IDENTIFIER_RE`** at `renderers.py:2359` to accept plain-text
  CamelCase/snake_case without thorough false-positive testing. The backtick-only contract
  has lower false-positive risk than open identifier matching; widening it raises the
  chance that prose sentences score as high-confidence sections that aren't.
- **`GROUP_FEWSHOT_EXAMPLES` should preserve backtick formatting** in all Good examples.
  Few-shot modeling is more reliable than instruction text alone for stochastic local
  models — instruction text is guidance, examples are constraint. If a future example
  update strips the backticks "for readability," the instruction text won't fully
  compensate.

Plan: `thoughts/shared/plans/active-2026-05-07-diagnose-confidence-partial-recovery.md`
Investigation: `thoughts/shared/investigations/2026-05-07-diagnose-confidence-partial-recovery.md`

**2026-05-07 deep-render repair-pass parity for learning_path + workflow_story (CA-178)** — 3 commits, `810dc1a..4546ced`.
Closes the architectural gap where cliff_notes had a section-level repair pass but
`generate_learning_path` and `generate_workflow_story` did not. When a step (learning_path)
or section (workflow_story) fails the `meets_confidence_floor` gate in a deep render, the
repair helper re-renders that single unit with a repair-targeted prompt and a three-clause
acceptance check before shipping it as LOW. Single-attempt policy — if the repair output
also fails the floor, the original LOW unit ships unchanged. Net runtime is approximately
zero change post-CA-176/177: sections rarely fall through the gate in practice; the benefit
is robustness on edge cases that do.

Cross-links: CA-176 (backtick prompt fix), CA-177 (alignment), CA-150 (quality gates).

Load-bearing constraints for future-Claude:

- **Repair templates require the backtick-format instruction** — both
  `LEARNING_PATH_STEP_REPAIR_TEMPLATE` and `WORKFLOW_STORY_SECTION_REPAIR_TEMPLATE`
  include the explicit "wrap each identifier in markdown backticks" rule (matching
  CA-176/CA-177). Don't remove it — it's what allows repaired output to clear the same
  `_enforce_deep_confidence_floor` gate as the main render.
- **Repair helpers call `parse_json_sections` directly**, NOT `_parse_steps` or
  `parse_with_fallback`. The fallback wrapper would inject a single-element fallback
  element on parse failure that could be silently accepted by `_should_accept_repaired_*`.
  The direct call is wrapped in `try/except (json.JSONDecodeError, ValueError, TypeError):
  continue`, mirroring cliff_notes' `_parse_sections` pattern.
- **Single-attempt policy** — each LOW unit gets at most one repair call. No retry loop.
  If the repair output also fails the floor, the original LOW unit ships unchanged. The
  acceptance check `_should_accept_repaired_*` gates this with three clauses: reject if
  evidence/file_paths emptied; reject if repair stays LOW; reject if shorter+fewer-refs.
- **`response is None` skip in workflow_story** — repair must not fire on the
  synthetic-fallback path where the main render returned no response. Wiring guard:
  `if depth == "deep" and response is not None and any(s.confidence == "low" for s in sections):`.
  Don't relax this.
- **`exercises`-only-LOW skip in learning_path** — a step that is LOW solely because
  `not step.exercises` (gate passes; exercises list missing) doesn't benefit from a
  content-targeted repair. The helper skips it to avoid wasted LLM cost.
- **Operation label trichotomy** — `learning_path` / `workflow_story` (no repair fired);
  `learning_path_repaired` / `workflow_story_repaired` (fired AND ≥1 accepted);
  `learning_path_repair_attempted` / `workflow_story_repair_attempted` (fired but all
  rejected). Telemetry consumers: use `startswith("learning_path")` /
  `startswith("workflow_story")` to catch all three variants.
- **Distinguished log keys** — `*_repair_skipped_budget_exceeded` for `SnapshotTooLargeError`;
  `*_repair_skipped` for generic exceptions. Don't collapse them.
- **Repair gate is deep-only** — no repair on medium/short/summary depth. Conditional:
  `if depth == "deep":`.
- **Architectural extraction NOT done** — three repair instances (cliff_notes, learning_path,
  workflow_story) with two different evidence-shape conventions. A shared `RepairableArtifact`
  mixin/protocol was deferred per librarian's plan-review verdict; abstraction is premature
  at this count. Don't extract until ≥4 instances.

Plan: `thoughts/shared/plans/active-2026-05-07-deliver-repair-pass-learning-path-workflow-story.md`

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
