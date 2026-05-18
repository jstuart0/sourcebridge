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

**2026-05-18 multi-arch Docker images: linux/arm64 added to build-images.yml (F1, O-M4)** — 1 commit, `012ff08d`.
Adds QEMU cross-platform support to `.github/workflows/build-images.yml` so all three component images (`sourcebridge-{api,worker,web}`) are published with `linux/amd64,linux/arm64` manifest lists. Closes the install-blocker for Apple Silicon Mac users (previously `docker compose pull` failed with "no matching manifest for linux/arm64/v8"). O-M4 from the 2026-05-09 audit-remediation wave 3 campaign is now closed. `oss-release.yml`'s separate `build-docker` job (combined all-in-one image) is still amd64-only; tracked as a follow-up.

**2026-05-18 post-setup onboarding UX: `/repositories` empty-state CTA + `/admin/llm` env-seeded callout (CA-540, CA-542)** — 1 commit, `74ddde33`.
Two follow-up UX fixes deferred from the 2026-05-17 first-install rough-edges campaign. CA-542 (F1): `/repositories` empty state now surfaces a primary CTA "Configure your AI provider to get started" linking to `/admin/llm` when no active LLM profile with a provider is configured. Admin-only (`isAdminRole(user?.role)` client-side gate); non-admins see the existing empty state. Flicker-free: `llmStatus` initializes `{ configured: true, checked: false }` so the existing empty state renders during the probe window. CA-540 (F2): `/admin/llm` shows an informational callout when the active profile was auto-configured at startup from environment variables; includes model name and masked API key fingerprint; callout is positioned below the existing State 1/2/3 banners. Backend adds `created_via` column on `ca_llm_profile` (migration 060) with three values: `"env_bootstrap"` / `"legacy_migration"` / `""`. `ProfileResponse.CreatedVia` exposed with `json:"created_via,omitempty"` (wire-additive).

Load-bearing constraints for future-Claude:

- **`ca_llm_profile.created_via` has exactly three values: `"env_bootstrap"`, `"legacy_migration"`, `""`.** Do not collapse to two. `"env_bootstrap"` is set ONLY when `freshInstall && (envBoot.APIKey != "" || envBoot.Provider != "")` inside `runMigrationBatch`. `"legacy_migration"` is set when `!freshInstall` (profile migrated from `ca_llm_config:default`). Empty string is the schema default for admin-UI-created profiles and unknown provenance. UI gates strictly on `=== "env_bootstrap"`; neither `"legacy_migration"` nor `""` renders the callout.
- **The variable that carries the value in `MigrateToProfiles` / `runMigrationBatch` is named `createdViaLabel`, NOT `source`.** The local variable `source` already exists at `internal/db/llm_config_migration.go:153,175` with a different meaning (API-key provenance label returned by `chooseAPIKeyForMigratedProfile`). Renaming `createdViaLabel` to `source` would cause a shadow collision and a semantic confusion. Don't rename it.
- **`createdViaLabel` is computed per-iteration INSIDE the `MigrateToProfiles` retry loop (R6).** If computed before the loop, a second retry that takes the self-heal path (legacy row gained values mid-retry) would persist a stale label from the first attempt. The per-iteration computation ensures the label tracks the per-attempt `freshInstall` value.
- **Migration startup order at `cli/serve.go` is load-bearing.** SQL migration runner (line ~202) MUST run before `EnsureSchema` (line ~566) and before `MigrateToProfiles` (line ~580). Migration 060 defines the `created_via` column; both downstream writes depend on the column existing. Do not reorder these three call sites.
- **`EnsureSchema` includes a belt-and-suspenders `DEFINE FIELD IF NOT EXISTS created_via` line.** This is intentional even though migration 060 provides the same definition — it protects fresh test scaffolding and any path where the SQL migration runner is skipped. Do not remove it.
- **F2 callout on `/admin/llm` is positioned BELOW the State 1/2/3 banner cluster, not above.** Actionable warnings lead the attention hierarchy; informational callouts follow. Moving it above the State banners reverses this and causes the "you may still need to act" warning to be visually buried.
- **F2 callout copy is "Active profile auto-configured at startup from environment variables."** "environment variables" is intentionally plural and generic — covers both `SOURCEBRIDGE_LLM_API_KEY` and `SOURCEBRIDGE_LLM_PROVIDER` paths (Ollama vs cloud). Do not revert to naming a specific variable.
- **F1 `llmStatus` initializes `{ configured: true, checked: false }`.** Changing the initial `configured` value to `false` introduces a skeleton → blank → CTA double-flicker (ruby H1). The optimistic default means the existing empty state renders during the in-flight probe window; the CTA substitutes only after the probe completes and confirms no LLM is configured.
- **F1 CTA is only accessible to admins.** The probe is `isAdminRole(user?.role)`-gated client-side. Non-admins can't act on the CTA (they can't configure LLM profiles) so surfacing it to them is misleading. Do not expose it to non-admins.
- **`profileToResponse` in `cli/serve.go` MUST map every field on `db.Profile` to `ProfileResponse`.** Missing wire-up silently omits fields from the API response. `TestProfileToResponse_CreatedViaRoundTrips` in `cli/profile_to_response_test.go` is the regression gate (4 cases). Any new `Profile` field requires a new test case there.
- **`ProfileResponse.CreatedVia` uses `json:"created_via,omitempty"`.** This is wire-additive: older API replicas omit the field entirely; older clients ignore unknown fields. Do not remove `omitempty` without a coordinated client update that handles the explicit `""` value.

Plan: `thoughts/shared/plans/finished/2026-05-18-deliver-post-setup-onboarding-ux.md`
Plane tickets: CA-540, CA-542

**2026-05-17 first-install rough edges: CSP dev `unsafe-eval`, `/setup` redirect, AI key env-var wiring (CA-537, CA-538, CA-539)** — 1 commit, `17a386c9`.
Three first-install friction points removed in a single commit. CA-537: `web/next.config.ts` `script-src` now appends `'unsafe-eval'` when `isDev` (`NODE_ENV === "development"`). Webpack HMR uses `eval()`; without this token Chrome blocks the entire dev page with a blank screen. Production CSP is unchanged. CA-338 precedent for conditional CSP tokens in the same file. CA-538: `GET /setup` 307-redirects to `/login` via `next.config.ts redirects()`. The setup form lives at `/login` (adapts based on `GET /auth/info` returning `setup_done: false`); the path was returning 404. `permanent: false` preserves a future dedicated `/setup` route. CA-539: `Makefile` `dev-go` and `dev-worker` targets bridge `ANTHROPIC_API_KEY` → `SOURCEBRIDGE_LLM_API_KEY` / `SOURCEBRIDGE_WORKER_LLM_API_KEY` via `${VAR:-fallback}` shell semantics (canonical always wins). Cloud-provider rows in `docs/installation.md` and `README.md` corrected to canonical `SOURCEBRIDGE_WORKER_LLM_API_KEY` across all 4 providers; only Anthropic carries the Makefile-bridge parenthetical.

Load-bearing constraints for future-Claude:

- **`web/next.config.ts:52` (approximately) adds `'unsafe-eval'` to `script-src` ONLY when `isDev` (`NODE_ENV === "development"`).** Production CSP must not include `'unsafe-eval'`. This is the same gate already used for dev-only WebSocket origins at `web/next.config.ts:42`. The CA-337 residual (`'unsafe-inline'`) is orthogonal and untouched — its removal requires a nonce refactor (see CA-337 comment block in the file).
- **`web/src/__tests__/csp.test.ts` asserts `expect(scriptSrc).not.toContain("'unsafe-eval'")` when `NODE_ENV=test`.** This is the regression detector for the `isDev` gate, mirroring the CA-338 `connect-src` precedent. Don't remove or weaken this assertion without replacing it with an equivalent guard.
- **`web/src/__tests__/csp.test.ts` asserts the `redirects()` array contains a `{source: "/setup", destination: "/login", permanent: false}` entry.** Removing the redirect requires removing this assertion in the same commit, or the test suite will fail.
- **The Makefile `dev-go` and `dev-worker` bridges are dev-convenience only.** `docker-compose.yml`, `docker-compose.hub.yml`, Helm, and kustomize do NOT bridge — production operators must set the canonical vars (`SOURCEBRIDGE_LLM_API_KEY`, `SOURCEBRIDGE_WORKER_LLM_API_KEY`) directly. The bridge is `${VAR:-fallback}` shell semantics: canonical always wins; fallback fires only when the canonical var is unset.
- **Cloud-provider tables in `docs/installation.md` and `README.md` use canonical SourceBridge env var names (`SOURCEBRIDGE_WORKER_LLM_API_KEY`), NOT upstream vendor names.** Only the Anthropic row carries the Makefile-bridge parenthetical. Do not add upstream vendor var names (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, etc.) back to those table cells — that's the doc bug CA-539 fixed.
- **The setup form lives at `/login`, not `/setup`.** The `/login` page adapts based on `GET /auth/info` returning `setup_done: false`. `/setup` redirects there via `next.config.ts redirects()`. Do not create a dedicated `/setup/page.tsx` without removing the redirect entry in the same PR, or they will conflict.

Plan: `thoughts/shared/plans/finished/2026-05-17-deliver-first-install-rough-edges.md`
Plane tickets: CA-537, CA-538, CA-539

**2026-05-17 audit remediation 5th sweep / r2 verification (CA-523..CA-536)** — 7 commits, `91738bf4..4ad027dc`.
14 findings (0 Critical / 0 High / 5 Medium / 9 Low) from the 5th full-codebase audit — the post-remediation verification run against v0.15.1-rc.3. First audit in the campaign series with no Highs; xander returned 0 findings. Commits close: Phase 1 (CI, CA-536): 16 GHA actions bumped to Node 24-compatible majors + golangci-lint v1→v2 (`91738bf4`). Phase 2 (a11y recovery, CA-523/524): 6 admin/comprehension inputs and the search input had `outline-none` removed in the 4th-audit CA-495 pass but no ring substitute added; explicit `focus:outline focus:outline-2 focus:outline-[var(--accent-primary)] focus:outline-offset-2` added at all sites (`7b0e691a`). Phase 3 (design tokens, CA-525/533): `--color-error`, `--color-accent`, `--color-primary`, `--color-info` defined as aliases in both theme blocks of `tokens.css`; profile-name-pill focus ring corrected to ring-2 + `--accent-focus` (`eb0bb6cf`). Phase 4 (a11y polish, CA-526/531/532): symbols-tab + chat inputs get aria-labels; TraceabilityMatrix confidence dot gains `role="img"` + `aria-label`; 6 decorative status dots get `aria-hidden="true"` (`6e8fb9f2`). Phase 5 (tests, CA-534/535): ThemeToggle SSR placeholder path covered via module factory; login page tests cover both `setup_done` modes; 350 → 358 web tests (`31fe561b`). Phase 6 (infra/docs, CA-527..530): README kustomize example bumped to v0.15.1-rc.3 + `oss-release.yml` `sed` step auto-bumps README on tagged release; 2 CLAUDE.md test-name drift refs fixed; 3 stale TODO comments in `concurrency.py` resolved (`27620612`). Codex r2 reconcile (`4ad027dc`): golangci v2 config migration (`.golangci.yml` v1 keys → v2 format; `gosimple` dropped — merged into staticcheck); `git pull --rebase` in `oss-release.yml` moved before edits to avoid dirty-index failure.

Load-bearing constraints for future-Claude:

- **`--color-error`, `--color-accent`, `--color-primary`, `--color-info` are defined in `web/src/styles/tokens.css` in both theme blocks as aliases** (`--color-error: var(--danger-text)`, etc.). ~55 call sites depend on these aliases. Do not redefine as direct oklch/hex values — they must remain aliases for automatic theme correctness. Pattern established by CA-495's `--bg-subtle` precedent; token definitions live in `tokens.css`, never `globals.css` or `recipes.css`.
- **GHA workflow actions are pinned to Node 24-compatible majors in all 8 workflow files.** `bufbuild/buf-setup-action@v1.50.0` is the sole exception — no Node 24 release exists as of 2026-05-17; `TODO(CA-536-followup)` comment marks it. When that action ships a Node 24 release, bump it. Don't downgrade any of the 16 bumped actions — that re-introduces the June 2, 2026 force-break risk.
- **`.golangci.yml` is v2-format** (`version: "2"`, `linters.settings`, `linters.exclusions.paths/rules`). The lint-action v7+/v9 enforces this. Don't revert to v1 keys (`linters-settings`, `issues.exclude-dirs`). `gosimple` is intentionally absent from the enable list — merged into `staticcheck` in v2; its checks continue to run via staticcheck.
- **`oss-release.yml` `update-helm-chart` step ordering**: `git fetch origin main` + `git pull --rebase origin main` MUST precede `yq`/`sed` edits and `git add`. Inserting the rebase after staging breaks with "cannot pull with rebase: Your index contains uncommitted changes". Idempotency is preserved by `git diff --cached --quiet && exit 0` after staging.
- **`oss-release.yml` README auto-bump via `sed`**: two substitutions — `?ref=v[0-9].*` → `?ref=${TAG}` and `newTag: v[0-9].*` → `newTag: ${TAG}`. README.md is explicitly listed in `git add`. If new README patterns reference release versions, extend the sed expression in lockstep.
- **ThemeToggle SSR placeholder test uses a `vi.mock("react", ...)` module factory with a `suppressUseEffect` flag** at `web/src/components/ThemeToggle.test.tsx`. Vitest ESM cannot redefine named exports via `vi.spyOn(React, "useState")`. The `afterEach` flag-reset is load-bearing — without it, suppression leaks to subsequent tests. Don't simplify to `vi.spyOn`.
- **Login page tests cover both `setup_done` modes in one file** (`web/src/app/login/page.test.tsx`). The setup flow is inline in `login/page.tsx` via the `needsSetup` state flag — there is no separate `setup-admin/page.tsx`. Don't split into two files.
- **TraceabilityMatrix confidence dot requires `role="img"` + `aria-label`** — the dot is the primary confidence/verified signal; no adjacent text exists. `title` is retained for sighted hover but AT doesn't announce `title` on `<div>`. CA-531 closed the title-only regression; don't revert.
- **Wiring-sites discipline**: the 4th-audit CA-495 pass fixed line 672's `<select>` but missed 5 sibling `<input>` elements — they shipped Done while failing WCAG 2.4.7. Future campaigns extending a pattern MUST re-grep the documented wiring sites after the commit, not just verify the named site.

Plan: `thoughts/shared/plans/active/2026-05-17-deliver-r2-audit-remediation.md`
Plane tickets: CA-523, CA-524, CA-525, CA-526, CA-527, CA-528, CA-529, CA-530, CA-531, CA-532, CA-533, CA-534, CA-535, CA-536

**2026-05-17 audit remediation 4th sweep (CA-493..CA-518)** — 8 commits, `3c39c68c..1331b6a9`.
26 tickets from the 2026-05-17 fourth-sweep audit closed across 6 phases. All four Highs are parity gaps from the May-16 campaign: CA-493 (slice safety on `GetSymbolCrossRepoRefs`) and CA-497 (TenantFilteredStore canary completeness + accordion ARIA) in Phase 1; CA-494 (NetworkPolicy egress AND-combinator gap in Helm + kustomize) in Phase 2; CA-495/496/498 (admin/comprehension a11y: `--bg-subtle` token, `outline-none` focus rings, `scope="col"`) in Phase 3. Remaining phases: CA-499..512 UX polish carry-overs (Phase 4); CA-504/506/507/508 test contracts (Phase 5); CA-502/505/513/516/517/518 cohesion + code-health + doc drift (Phase 6). CA-506 is **Deferred** — observer wiring blocked by `TestResolverStructureCanary` 4-field pin; test renamed to `TestEnrichAllRequirements_BatchSize200_AcceptedWithoutError`; clamp verified by code inspection. Codex r2 reconcile (`1331b6a9`) addressed M2 (sweep pruning fix), L1 (canary positive path inlined), and L3 (import try/finally).

Phase 1 (`3c39c68c`) — CA-493 + CA-497. `internal/graph/filtered.go:GetSymbolCrossRepoRefs` replaced `refs[:0:len(refs)]` in-place filter with `make([]*CrossRepoRef, 0, len(refs))`. `StoreAPIContract` added to `TestTenantFilteredStoreCanary_AllIDKeyedMethodsGated` (was gated at line 742 but absent from canary). Canary total updated to **25 gated methods** (8 federation + 16 ID-keyed + 1 cross-repo refs). `TestTenantFilteredStoreGetSymbolCrossRepoRefs_DoesNotMutateInnerStore` and `TestFilteredVerifyLinkPermitted_CallsInnerStore` added. Knowledge-tab accordion ARIA assertions extended to all 5 accordions (`ask` + `explore` panels added to join existing `guide`/`execution`/`workflow`).

Phase 2 (`cedb19f4`) — CA-494 + CA-503 + CA-514 + CA-515. Helm NetworkPolicy egress peers (5 sites) converted from `podSelector`-only to single-list-item `podSelector` + `namespaceSelector` AND-combinator shape — parity with May-16 ingress fix. Kustomize overlay `allow-worker-egress` SurrealDB peer similarly fixed. Redis kustomize manifest gains `imagePullPolicy: IfNotPresent`. Dev compose worker healthcheck switched to `CMD-SHELL` invoking `sourcebridge-health-probe.sh`.

Phase 3 (`57a008fe`) — CA-495 + CA-496 + CA-498. `--bg-subtle: var(--bg-surface-2)` token defined in both theme blocks in `web/src/styles/tokens.css`. `outline-none` replaced with explicit `focus:outline focus:outline-2 focus:outline-[var(--accent-primary)] focus:outline-offset-2` at 9 sites across admin/comprehension and login pages. `scope="col"` added to 19 `<th>` elements across `admin/comprehension/page.tsx` (6) and `admin/comprehension/models/page.tsx` (13).

Phase 4 (`0a2bb6de` + `a6de4780`) — CA-499..501 + CA-509..512. Login password `<input>` elements migrated to design-system `<Input>` component. Requirement chat `aria-live="polite"` region added. Requirement detail page loading skeleton. ThemeToggle `aria-label` now describes current state ("Switch to light mode" / "Switch to dark mode"). `text-red-500` → `text-[var(--danger-text)]` in requirement chat error path.

Phase 5 (`66ed1336`) — CA-504 + CA-506 + CA-507 + CA-508. New `cli/main_test.go` with `TestMain` resetting `usage.ResetCountersForTest()` (CA-400 protocol). CA-506 deferred (see above). `TestDisableWorkers_JobStaysPending` / `TestDisableWorkers_FalseJobDrains` use synchronous `GetByID` — no sleep. `TestEnrichAllRequirements_ForceIncludesEnriched` upgraded to real `orchestrator.New(...)` + three-clause assertion.

Phase 6 (`dd66b002`) — CA-505 + CA-502 + CA-513 + CA-516 + CA-517 + CA-518. `internal/auth/local.go` gains unexported `localAdminEmail` constant + exported `LocalAdminEmail()` accessor. `internal/api/rest/login_rate_limit_test.go` gains `TestLocalAuthUsernameMatchesAuthPackageEmail` cohesion test. `secondsString` body replaced with `strconv.Itoa` + floor. CLAUDE.md Redis `readOnlyRootFilesystem: false` doc drift fixed (CA-517). `loginBucket` gains `lastSeen time.Time` + lazy sweep with `len(attempts)==0 AND time.Since(lastSeen) > window` guard. `cli` package referenced `auth.LocalAdminEmail()` raw literals updated.

Codex r2 reconcile (`1331b6a9`) — M2: `sweep()` prunes aged-out attempts inside the bucket lock before checking emptiness, then deletes only if empty AND `time.Since(lastSeen) > window`. L1: canary positive-path test inlined. L3: `handleImportReqs` try/finally added so `isImporting` resets on thrown errors.

Load-bearing constraints for future-Claude:

- **`internal/graph/filtered.go:GetSymbolCrossRepoRefs` uses `make([]*CrossRepoRef, 0, len(refs))`, NOT in-place filter.** Extends the May-16 D-029 constraint to the cross-repo refs method. `refs[:0:len(refs)]` is the forbidden pattern — it mutates MemStore backing slices and enables cross-tenant data corruption.
- **TenantFilteredStore canary covers 25 gated methods (8 federation + 16 ID-keyed + 1 cross-repo refs).** `StoreAPIContract` is the 16th ID-keyed method (was correctly gated at production line 742 but absent from canary). Adding any new ID-keyed Get/Update/Promote/Dismiss/Verify method MUST come with a canary entry or a load-bearing justification comment. `TestTenantFilteredStoreCanary_AllIDKeyedMethodsGated` is the enforcement contract.
- **NetworkPolicy egress uses the AND-combinator single-list-item shape on every peer** — both Helm and kustomize. This extends the May-16 CA-346/347/355 ingress fix to egress. Any new egress peer that uses `podSelector`-only silently widens to all namespaces. Verify new egress additions with `yq '.spec.egress[].to | length' <file>` — value must equal 1.
- **`cli/main_test.go` has `TestMain` resetting `usage.ResetCountersForTest()` before `m.Run()`.** The `cli` package now joins `internal/api/graphql` and `internal/qa` on the CA-400 reset protocol. Any new test package that calls `usage.QueriesCounter.Increment()` or `usage.ArtifactsCounter.Increment()` MUST add the same `TestMain`.
- **`internal/auth.LocalAdminEmail()` is an exported accessor function, NOT an exported constant.** Returns the unexported `localAdminEmail` ("admin@localhost"). Multi-user paths MUST use the actual submitted username, NOT this accessor. The `internal/api/rest` package's `localAuthUsername` constant is the OSS single-user rate-limit key; `TestLocalAuthUsernameMatchesAuthPackageEmail` enforces their equality. Do NOT export the constant itself — that invites the cross-package import that CLAUDE.md's existing guardrail forbids.
- **`loginBucket.sweep()` prunes aged-out attempts inside the bucket lock before checking emptiness, then deletes the bucket only if `len(attempts) == 0 AND time.Since(lastSeen) > window`.** The `lastSeen` guard is load-bearing against the LoadOrStore/Delete race: it ensures a bucket that received recent traffic is never silently dropped even after its window ages out. Triggered opportunistically every 256th `Allow` call.
- **`--bg-subtle: var(--bg-surface-2)` is defined in `web/src/styles/tokens.css` in both theme blocks.** 34 components depend on this alias. Do not redefine it as an oklch value — it must remain an alias to `--bg-surface-2` for automatic theme correctness. Do not move it to `globals.css` or `recipes.css` — token definitions live in `tokens.css`.
- **`handleImportReqs` in `requirements-tab.tsx` uses try/finally to reset `isImporting`.** The finally block is load-bearing — without it, a thrown network/client error leaves the textarea and button permanently disabled. The codex r2 L3 fix (`1331b6a9`) adds this pattern; do not revert to await-then-reset.

Plan: `thoughts/shared/plans/active/2026-05-17-deliver-audit-remediation.md`
Plane tickets: CA-493, CA-494, CA-495, CA-496, CA-497, CA-498, CA-499, CA-500, CA-501, CA-502, CA-503, CA-504, CA-505, CA-507, CA-508, CA-509, CA-510, CA-511, CA-512, CA-513, CA-514, CA-515, CA-516, CA-517, CA-518 (deferred: CA-506)

**2026-05-16 audit remediation: 27 findings (CA-463..CA-487 + CA-489)** — 12 commits, `87724f18..d49eed59`.
Three security-shaped gaps in just-shipped surfaces closed first, then 24 test/UX/infra/code-health findings. D-H1/X-H1 (same root cause): the X-H2 DNS rebind guard (`c2b816da`, 2026-05-15) was wired into `AsyncOpenAI` but not the Ollama-native paths (`_complete_ollama_native`, `_stream_ollama_native`), the concurrency probe (`OpenAICompatProbeBackend.call`), or either embedding provider — exactly the surface attackers targeting IMDS on cloud installs running Ollama profiles or any embedding-backed RAG flow would hit. A-H2: `TenantFilteredStore` ID-keyed lookups were pass-throughs without tenant validation, enabling cross-tenant content disclosure on multi-tenant deployments. O-H1: `sourcebridge-health-probe.sh` in `deploy/docker/Dockerfile.worker` was missing the `-rpc-header=x-sb-worker-secret:${SECRET}` flag added by CA-202; every kustomize + Helm worker deployment with a non-default gRPC secret ran permanently-unhealthy.

Phase 1 (`87724f18` + `6c1ad776`) — DNS rebind guard completeness. New `workers/common/llm/ip_check.py` is the canonical IP-classification module; `is_private_or_internal_ip`, `is_cloud_metadata_ip`, `CGNAT_NETWORK` exported as public API. `config.py` and `rebind_guard.py` both import from it (circular-import comment deleted). IPv4-mapped IPv6 unwrap (`_unwrap_ipv4_mapped`) and `_IMDS_V6_NETWORKS` (`fd00:ec2::/32`) added — blocks IMDS bypass on dual-stack hosts + AWS IMDSv2 IPv6. `socket.getaddrinfo` in `rebind_guard.py:handle_async_request` offloaded via `loop.run_in_executor` (not synchronous — would block the asyncio event loop). Ollama-native paths, concurrency probe, and both embedding providers get per-call `RebindGuardedTransport` instantiation (not shared with SDK client pool).

Phase 2 (`45f7e17a` + `1bb56360`) — `TenantFilteredStore` ID-keyed gating. 15 new methods gated via `hasAccess()` post-retrieval (entities) or pre-retrieval parent-gate + post-filter (collection methods). `GetSymbolCrossRepoRefs` gates both `SourceRepoID` AND `TargetRepoID`. Slice safety: `make([]string, 0, len(ids))` — no in-place filter (would mutate MemStore backing slices). `PlatformStats` GraphQL is NOT admin-gated (non-admin dashboard would break; stats sandbox is the security control). `TestTenantFilteredStoreCanary_AllIDKeyedMethodsGated` enumerates gated (24) + intentionally-ungated (6) methods with load-bearing per-method comments.

Phase 3 (`034e9b72` + `58b95bae`) — cross-deployment drift. `sourcebridge-health-probe.sh` (Dockerfile.worker) reads `SOURCEBRIDGE_WORKER_GRPC_AUTH_SECRET` and conditionally appends `-rpc-header` in both TLS and plaintext branches. `docker-compose.hub.yml` switched to `CMD-SHELL` healthcheck invoking the script. Helm `Chart.yaml` `appVersion` set to `0.14.0`; `values.yaml` `*.image.tag: ""` defaults to `.Chart.AppVersion` via `| default .Chart.AppVersion` in templates. `oss-release.yml` gains a `yq` step to bump `version` + `appVersion` from `github.ref_name` on tagged releases (SemVer validation guard). Redis kustomize manifest gets `imagePullPolicy: IfNotPresent`; AND-combinator parity applied to all egress peers in `allow-set.yaml`.

Phase 4 (`759d349b`) — enrich-mutation tests + UX banner. `TestEnrichAllRequirements_BatchSizeClampsAt100`, `TestEnrichRequirement_MutationFields`, `TestEnrichRequirement_ForceFlag`, `TestEnrichAllRequirements_Orchestrator` rewrite with a concrete `orchestrator.New(...)` (no hand-rolled fake). `ENRICH_REQUIREMENT_MUTATION` in `queries.ts` accepts `$force: Boolean`. Banner state in `requirements-tab.tsx` is typed `{ type: "success" | "error" | "info"; message: string } | null`; `role="status" aria-live="polite" aria-atomic="true"`; error branch uses `--danger-*` tokens; `handleImportReqs` now sets the typed object.

Phase 5 (`4c2b7333`) — remaining test gaps. `internal/api/graphql` package gets `TestMain` resetting `usage.ResetCountersForTest()` (CA-400 protocol). `ConcurrencyRegistry.close()` test uses `await registry.close()`. Makefile CSP soak deadline target updated.

Phase 6 (`682a3c6c` + `4a071b6e`) — UX a11y. Requirements-tab panel in-flight text uses `text-[var(--text-secondary)]` + `animate-pulse`. Artifact-detail tables and admin monitor tables get `scope="col"` on `<th>`. Chat message renders token count from `usage.inputTokens + usage.outputTokens`. `<summary>` focus ring verified via global `:focus-visible` rule at `web/src/styles/recipes.css:25-28`; no per-element override needed.

Phase 7 (`275777a3`) — code-health. `localAuthUsername = "admin@localhost"` promoted to package-level constant in `internal/api/rest/login_rate_limit.go`. Dead unreachable `secondsString` branch removed.

Reconcile (`d49eed59`) — codex r2 fixes: `PlatformStats` non-admin gate reverted (dashboard breakage), DNS bound comment added, `yq` CI install fixed, `filtered.go` file-level comment added.

Load-bearing constraints for future-Claude:

- **`workers/common/llm/ip_check.py` is the canonical IP-classification module.** `is_private_or_internal_ip`, `is_cloud_metadata_ip`, `CGNAT_NETWORK` are public exports. Do NOT re-introduce duplicate copies in `config.py` or `rebind_guard.py`. The `_unwrap_ipv4_mapped` helper and `_IMDS_V6_NETWORKS` (`fd00:ec2::/32`) are load-bearing — they block IMDS bypass on dual-stack hosts and AWS IMDSv2 IPv6.
- **Ollama-native paths and concurrency probe use per-call `RebindGuardedTransport(allow_private=self._allow_private_base_url)` instantiation.** The SDK client keeps `self._rebind_transport` because its lifecycle matches the provider instance. Do NOT share the transport with a separate `async with httpx.AsyncClient`: httpx `__aexit__` calls `aclose()` on the transport, tearing down the connection pool the SDK client still holds.
- **Embedding providers use per-call `RebindGuardedTransport`.** No long-lived `self._client` on `workers/common/embedding/ollama.py` or `openai_compat.py`. `close()` is a no-op (no pool to release). Per-call overhead is acceptable vs DNS-rebind security gain.
- **`socket.getaddrinfo` in `rebind_guard.py:handle_async_request` is offloaded via `loop.run_in_executor(None, socket.getaddrinfo, ...)`**. Do NOT regress to a synchronous call — blocks the asyncio event loop. DNS executor is the default `ThreadPoolExecutor` (cpu_count+4, max 32). Under heavy slow-DNS load + high LLM concurrency, executor may saturate; operators should keep `SOURCEBRIDGE_LLM_MAX_CONCURRENT_CALLS` below the executor bound.
- **`TenantFilteredStore` gated methods: 25 total (8 federation + 16 ID-keyed + 1 cross-repo refs).** Intentionally-ungated: `StoreEmbedding`, `StoreReviewResult`, `GetReviewResults`, `GetEmbedding`, `GetFileSymbols`, `GetLinksForFile` — each has a load-bearing comment in `filtered.go` explaining why. Re-gate before any new public-API consumer. `TestTenantFilteredStoreCanary_AllIDKeyedMethodsGated` is the enforcement contract.
- **`TenantFilteredStore.Stats()` returns empty map (`graphstore.Stats{}`).** `PlatformStats` GraphQL is NOT admin-gated — non-admin users access it via the dashboard. The stats sandbox (empty map) is the security control. Do NOT add an admin-only gate to `PlatformStats`; that breaks the web dashboard.
- **Call-graph `!found` policy: drop, don't retain.** Deleted cross-tenant symbol IDs must not appear in caller/callee/test lists. The `make([]string, 0, len(ids))` slice pattern is load-bearing — do NOT use in-place filter `ids[:0:len(ids)]`; that mutates MemStore backing slices.
- **Helm `Chart.AppVersion` is the authoritative version source.** `values.yaml` `*.image.tag: ""` defaults to `.Chart.AppVersion`. `oss-release.yml` bumps `Chart.yaml` `version` + `appVersion` from `github.ref_name` on tagged releases (SemVer guard). Operators with explicit `image.tag` overrides keep them.
- **`sourcebridge-health-probe.sh` (baked into `deploy/docker/Dockerfile.worker`) reads `SOURCEBRIDGE_WORKER_GRPC_AUTH_SECRET` and conditionally appends `-rpc-header=x-sb-worker-secret:${SECRET}`.** Every kustomize + Helm + hub-compose worker healthcheck depends on this. Hub-compose uses `CMD-SHELL` to invoke the script. **Worker image must be rebuilt after upgrading** — existing images from before O-H1 will run permanently-unhealthy on installs with a non-default gRPC secret.
- **NetworkPolicy AND-combinator parity**: every ingress AND egress peer in `allow-set.yaml` uses the single-list-item shape (`podSelector` + `namespaceSelector: kubernetes.io/metadata.name: sourcebridge` under the same `- ` item). Two-list-item shape means OR and silently widens the policy.
- **Banner state in `requirements-tab.tsx` is typed `{ type: "success" | "error" | "info"; message: string } | null`** with `role="status" aria-live="polite" aria-atomic="true"`. Error branch uses `--danger-*` tokens. Info uses `--border-default` + `--bg-subtle` + `--text-secondary`. Success stays emerald (no `--success-*` token family). All four handlers (`handleAutoLink`, `handleEnrichAll`, `handleImportReqs`, "nothing to do" path) set the typed object.
- **`internal/api/graphql` package has `TestMain` resetting `usage.ResetCountersForTest()` before `m.Run()`** (CA-400 protocol). Any new test package referencing `usage.QueriesCounter` or `usage.ArtifactsCounter` MUST add the same `TestMain`.
- **`localAuthUsername = "admin@localhost"` is a package-level constant in `internal/api/rest/login_rate_limit.go`.** Multi-user paths MUST use the actual submitted username — do not import this constant outside the OSS single-user path.
- **`<summary>` focus ring is provided by the global `:focus-visible` rule at `web/src/styles/recipes.css:25-28`.** Do not add per-element `summary:focus-visible` overrides unless a Preflight reset is observed removing the global rule.

Plan: `thoughts/shared/plans/active/2026-05-16-deliver-audit-remediation.md`
Plane tickets: CA-463, CA-464, CA-465, CA-466, CA-467, CA-468, CA-469, CA-470, CA-471, CA-472, CA-473, CA-474, CA-475, CA-476, CA-477, CA-478, CA-479, CA-480, CA-481, CA-482, CA-483, CA-484, CA-485, CA-486, CA-487, CA-489 (deferred: CA-488, CA-490, CA-491, CA-492)

**2026-05-15 Tier 1 security Mediums: CSP tightening + per-username login rate limit + OIDC/local PII scrub (CA-337, CA-338, CA-339, CA-207, CA-340)** — 2 commits.

CA-337 DEFERRED. Next.js 15 streaming RSC injects `self.__next_f.push(...)` inline scripts into every SSR page; `unsafe-inline` in `script-src` is mandatory until a nonce-based refactor lands. Residual documented in `web/next.config.ts` and CHANGELOG.

CA-338 shipped: bare `wss:` / `ws:` scheme tokens removed from `connect-src` in `web/next.config.ts`; replaced with `'self'` (production) + `ws://localhost:* wss://localhost:*` (dev builds only via `process.env.NODE_ENV === "development"`). `trimEnd()` guards the case where no dev origins are injected from leaving a trailing space.

CA-339 + CA-207 shipped: `internal/api/rest/login_rate_limit.go` — `loginRateLimiter` with `sync.Map` of `*loginBucket` (sliding-window timestamp slice). Shared instance on `rest.Server.loginLimiter`; wired into both `handleLogin` (`auth.go`) and `handleDesktopLocalLogin` (`desktop_auth.go`). Config: `auth.login_rate_limit_per_user` (default 5) + `auth.login_rate_limit_window_secs` (default 300). Both viper-defaulted + struct-defaulted in `internal/config/config.go:AuthConfig`. `TestServerStructureCanary` allowlisted `loginLimiter` with comment.

CA-340 shipped: `slog.Info("OIDC login successful", "email", ...)` at `internal/auth/oidc.go:193` demoted to `slog.Debug` (email is PII). `slog.Info("loaded persisted admin user", "email", ...)` at `internal/auth/local.go:74` demoted to `slog.Debug`; new `slog.Info("local_auth_setup_loaded", "user_id", ...)` emitted without email.

Load-bearing constraints for future-Claude:

- **CA-337 residual: `unsafe-inline` in `script-src` is NOT removable without a nonce refactor.** Next.js 15 RSC streaming always injects inline scripts (`self.__next_f.push([...])`). Attempting to remove `unsafe-inline` without nonce-prop wiring will silently break hydration on all pages. The deferred path: (1) generate a per-request nonce in `web/src/middleware.ts`, (2) pass it to the Next.js `<script>` elements via the `nonce` prop, (3) include `'nonce-<value>'` in `script-src`. Until that's done, the residual comment in `web/next.config.ts` is load-bearing documentation.
- **CA-338: `connect-src` `'self'` covers same-origin WebSockets.** The CSP spec includes WebSocket connections in `connect-src`; `'self'` allows `wss://same-host` in production. Any future feature that needs cross-origin WebSocket connections (e.g., connecting to a remote Ollama) must explicitly add the origin to `connect-src`, not revert to bare `wss:`.
- **`loginRateLimiter.Allow()` is called BEFORE the bcrypt comparison** in both login handlers. This is the constant-time safety property — the 429 response time is independent of whether the account has a valid password. Do not re-order to "skip the rate check for obviously wrong passwords first." That would create a timing oracle distinguishing lockout-on-valid-account from lockout-on-invalid-account.
- **`loginLimiter` is a shared instance across `/auth/login` and `/auth/desktop/local-login`.** Both paths consume from the same per-username budget. If you add a third login endpoint (e.g., `/auth/api-key-login`), wire `s.loginLimiter.Allow(...)` there too or it becomes an unguarded side door.
- **`localAuthUsername` (the `internal/api/rest` package-level constant) is the OSS-local-auth key.** For multi-user/enterprise extensions that add username-based login, replace this constant with the actual submitted username so each account gets its own bucket. The current constant is correct for OSS single-user local-auth only.
- **CA-340: `slog.Debug` with `email` is intentional** at `internal/auth/oidc.go` and `internal/auth/local.go`. Operators who need email correlation for troubleshooting can enable debug logging. Don't elevate back to INFO — that re-introduces PII in default production log streams. The `sub` / `user_id` fields at INFO level are the stable opaque identifiers.

**2026-05-14 telemetry metrics expansion (CA-400)** — 3 commits across 3 repos: `7824873` + `81cef10` (collector), `86330d1` (agent), `v32` nginx image (marketing site).
Adds engagement metrics — repos indexed, queries asked, artifacts generated — to the telemetry pipeline end-to-end. Three coordinated changes: (1) collector `sourcebridge-telemetry/src/worker.ts` adds 9 new `/v1/stats` aggregations; (2) agent adds `internal/usage/` package with `RollingDayCounter` primitive and wires `QueriesCounter` / `ArtifactsCounter` into the telemetry ping; (3) marketing site `sourcebridge-website/index.html` switches its stats bar to the new cohort-filtered keys.

Collector additions: `total_repos_7d`, `total_repos_30d`, `active_installs_7d_with_repos`, `active_installs_30d_with_repos` (installs with `repos > 0` — the "real users" cohort), `total_repos_active_7d`, `avg_repos_per_install_7d`, `max_repos_per_install_7d`, `total_queries_30d`, `total_artifacts_generated_30d`. The last two have a 48h freshness gate (only count pings with `updated_at` within 48h) to match the agent's 24h ping cadence. `total_repos` retained as a temporary alias — follow-up ticket queued to remove it once both consumers update.

Agent additions: `internal/usage/rolling_day_counter.go` — `RollingDayCounter` (clock-injectable, goroutine-safe, per-bucket bucket expiry); package-level `QueriesCounter` and `ArtifactsCounter` (30-day windows). `internal/qa/pipeline.go:409` increments `usage.QueriesCounter` alongside the existing `qa.AsksTotal14d`. New `markArtifactReady(ctx, artifactID)` wrapper on `*Resolver` wired at 3 artifact-completion sites in `knowledge_generation_shared.go`, `knowledge_generation_cliff_notes.go`, and `knowledge_generation_architecture_diagram.go`. `cli/serve.go` merges `usage.Counters()` into `TelemetryCounts()`. `TELEMETRY.md` updated with two new rows. `Makefile` adds `check-telemetry-disclosure` grep gate.

Marketing site: `active_installs_7d` → `active_installs_7d_with_repos` (with fallback), `total_repos` → `total_repos_7d` (with fallback). Stats bar labels unchanged.

Load-bearing constraints for future-Claude:

- **`markArtifactReady` is the canonical instrumentation point** for "an artifact transitioned to ready in response to a user request." Any new `UpdateKnowledgeArtifactStatus(..., StatusReady)` call site must consciously decide whether it counts as a generation event. Two paths explicitly bypass the wrapper: seed via `SupersedeArtifact` in `internal/api/graphql/knowledge_seed.go` (import from existing store, not a new generation) and cliff-notes deepening in `internal/api/graphql/knowledge_support.go:1905` (incremental update, not a cold start). Don't add the wrapper to either bypass path — that would overcount artifact generations.
- **The 48h freshness gate on `total_queries_30d` / `total_artifacts_generated_30d` is load-bearing relative to the agent's 24h ping cadence.** The collector SUMs only pings with `updated_at` within 48h. If the agent's ping cadence is ever lengthened past 48h, this gate silently zeros both metrics without any error. The ping cadence is defined in `internal/telemetry/telemetry.go`; any change there must also update the collector's freshness threshold.
- **`usage.QueriesCounter` and `qa.AsksTotal14d` BOTH increment per QA ask; they coexist intentionally.** `qa.AsksTotal14d` is a 14-day ring in `internal/qa/telemetry.go` (existing, used for the QA rate-limit feature gate). `usage.QueriesCounter` is a 30-day window in `internal/usage/` (new, feeds telemetry ping). Don't consolidate them — they serve different purposes and have different windows. Future counters for new event types MUST use `internal/usage.RollingDayCounter`, not the QA ring.
- **`usage.ResetCountersForTest()` MUST be called in `t.Cleanup` (or via `TestMain`) in any test that exercises the package-level counters.** The counters are package-level singletons; without reset, state leaks across tests in the same package. Currently enforced in `internal/qa/pipeline_test.go` via `TestMain`. Any new test package that calls `usage.QueriesCounter.Increment()` or `usage.ArtifactsCounter.Increment()` (or the helpers that call them) must add the same `TestMain` reset guard.
- **`total_repos` alias on `/v1/stats` is temporary.** Both consumers (`sourcebridge-website/index.html:614` stats bar and `sourcebridge-telemetry/src/worker.ts:812` dashboard) must update to `total_repos_7d` in the alias-removal PR. The two changes must ship together — the alias is only safe to remove once both consumers reference `total_repos_7d` directly with their own fallback logic.
- **`TELEMETRY.md` update MUST land in the same commit as any new agent-side telemetry field addition.** The `make check-telemetry-disclosure` target enforces this for `queries_30d` and `artifacts_generated_30d` via grep. Future telemetry additions must add their own `TELEMETRY.md` row AND a corresponding grep check in the `Makefile` target before the CI gate will pass.
- **`markArtifactReady` error policy is NOT uniform across call sites.** `knowledge_generation_shared.go` and `knowledge_generation_cliff_notes.go` use log-and-continue (increment failure is non-fatal; don't block the artifact transition). `knowledge_generation_architecture_diagram.go` uses return-error (architecture diagram generation is a discrete user-triggered action where a counter failure is surfaced). New wiring sites must explicitly choose a policy — don't assume log-and-continue is always correct.
- **PostHog forward in the collector uses `ctx.waitUntil(...)`, NOT `request.ctx?.waitUntil?.(...)`.** Fixed in `sourcebridge-telemetry` commit `81cef10` (post-validation, 2026-05-15). The original code at `worker.ts:228` referenced `request.ctx` — but in the Cloudflare Workers runtime, `ctx: ExecutionContext` is the THIRD argument to the `fetch` handler, not a property of `Request`. The optional chaining (`?.`) silently short-circuited every call, so no PostHog event reached the SourceBridge.ai project from the time the forward was added until this fix. The forward path is now: `fetch(request, env, ctx)` → `handlePing(request, env, headers, ctx)` → `ctx.waitUntil(fetch(...).then(check_resp_ok).catch(...))`. The new error handler emits `console.error("posthog_capture_failed", ...)` on 4xx/5xx so future regressions surface in `wrangler tail`. The PostHog **project** API key is stored at `kubectl -n automation get secret posthog-credentials` (project-api-key + host + project-id); the **personal/admin** key is stored at `posthog-admin-credentials` (used for the PostHog management API).

Plan: `thoughts/shared/plans/finished-2026-05-14-deliver-telemetry-metrics-expansion.md`
Plane ticket: [CA-400](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/b5e6e074-2734-45ce-9377-5ce8938f8604)

**2026-05-14 audit-remediation: 21 C+H findings (CA-327..CA-399 subset)** — 12 commits, `d01268d..2e0f670`.
12 atomic commits closing 21 Critical+High findings from the 2026-05-13 full-codebase audit, plus 2 co-fixed Mediums (CA-355 NetworkPolicy SurrealDB+Redis ingress, U-M4 Workflow Story empty-state button variant). Two Criticals (CA-334 CSRF default flip, CA-363 network-error states) plus 19 Highs across 5 blocker themes: B1 CSRF/SSRF chain, B2 UX consolidation + a11y closure, B3 REST/GraphQL parallel-path drift, B4 NetworkPolicy namespace-selector gap, B5 deployment-method drift. Two test-surface Highs (CA-390 AskModelResolver path coverage, CA-391 token-name rune-boundary) and one a11y campaign (R3: 5-accordion closure + ThemeToggle + onboarding inputs) round out the scope. Reviewers: bob, xander, ian, otto, ruby, tessa initial fan; bob, xander, otto, ruby re-fan; all APPROVE post-iteration.

Phase 1 (`d01268d`) — CA-334 CSRF default flip. `Config.Security.CSRFFullCoverageEnabled` flipped to default `true`. `TestSecurityDefaultsCSRFFullCoverageEnabled` assertion updated accordingly. Startup ERROR log (`csrf_full_coverage_enabled=false`) fires when the flag is disabled. `config.WarnCSRFDisabled` and `config.WarnAllowPrivateBaseURL` extracted from `cli/serve.go` into `internal/config/` as testable helpers (Decision 11 / TES-M1).

Phase 2 (`cff9a52`) — CA-335 SSRF guard on `/llm-profiles`. `handleCreateLLMProfile` and `handleUpdateLLMProfile` in `internal/api/rest/llm_profiles.go` now call `pathutil.ValidateLLMBaseURL(req.BaseURL, s.cfg.LLM.AllowPrivateBaseURL, nil)` before persisting. Scheme allowlist always active; IP-range denylist conditional on `AllowPrivateBaseURL=false`. Rejection body: `{"error":"invalid_base_url"}` — raw URL never reflected.

Phase 3 (`6954988`) — CA-336 AllowPrivateBaseURL startup WARN. Operator-visible `slog.Warn` fires at startup when `AllowPrivateBaseURL=true`. `docs/admin/llm-config.md` updated per XAN-H2: scheme allowlist + redirect-disable listed as always-active; IP-range denylist listed as conditional; "cloud-metadata block" phrase removed (was inaccurate when the flag is `true`).

Phase 4 (`dd8e636`) — CA-346 + CA-347 + CA-355 (co-fix M). NetworkPolicy `allow-worker-ingress`, `allow-surrealdb-ingress`, and `allow-redis-ingress` converted from two-list-item (OR) to single-list-item AND-combinator shape for `podSelector` + `namespaceSelector`. Helm uses `{{ .Release.Namespace }}` label; kustomize operators in non-`sourcebridge` namespaces must add the `kubernetes.io/metadata.name` label (K8s ≥ 1.22 injects it automatically).

Phase 5 (`7f4cf74`) — CA-327 canonical `qa.JobTypeToOp` helper. New function in `internal/qa/types.go` (flat `qa` package, NOT a subpackage per BOB-M2/Decision 6). Both REST and GraphQL transports import `internal/qa` and call `JobTypeToOp` directly; the prior parallel switch-tables are deleted. `TestJobTypeToOpCanary_QADeepSynth` is the regression gate.

Phase 6 (`9e93068`) — CA-329 canonical `internal/knowledge.DiscussionContextFromArtifact`. New helper in `internal/knowledge/context.go`. Format: `"%s:\n%s"` per section, `"\n\n"` separator (GraphQL shape retained; REST shape retired). Prompt-shape snapshot canary pins future drift.

Phase 7 (`ec14bad`) — CA-390 + CA-391 test surface. `TestDeepPipeline_AskModelResolver_NilUsagePath` and `TestDeepPipeline_AskModelResolver_SynthesisFailedPath` pin both call sites of `resolveAskModel(ctx)` in `deep_pipeline.go:395-424`. CA-391 token-name boundary test uses 129-rune `strings.Repeat("界", 129)` (387 bytes) to surface byte-vs-rune confusion; validator must reject at rune count, not byte count.

Phase 8 (`b474112`) — CA-345 kustomize DATABASE rename. **BREAKING for existing kustomize installs.** `"main"` → `"sourcebridge"` in both `SOURCEBRIDGE_STORAGE_SURREAL_DATABASE` (configmap line 11) and `SOURCEBRIDGE_WORKER_SURREAL_DATABASE` (line 41). Partial application split-brains API vs worker. Existing operators with data under `"main"` migrate via 8-section runbook at `docs/admin-runbooks/kustomize-database-rename.md` (scale-to-zero → `surreal export` → verify → `surreal import` → apply → scale-back → rollback path). SurrealDB has no `db rename` statement; export+import is the only migration path.

Phase 9 (`76f033b`) — CA-348 + CA-349 worker memory + Helm Redis securityContext. Worker memory limit canonicalized at `"2Gi"` on both kustomize (`deploy/kubernetes/base/worker.yaml`) and Helm (`deploy/helm/sourcebridge/values.yaml`); requests stay `512Mi`. Helm Redis StatefulSet gets pod-level `securityContext` (`runAsNonRoot: true`, `runAsUser: 999`, `runAsGroup: 999`, `fsGroup: 999`) plus container-level hardening (`readOnlyRootFilesystem: false` — Redis writes RDB temp files to the root FS before atomic-rename into /data; `true` would crash Redis on save, `allowPrivilegeEscalation: false`, `capabilities: drop: [ALL]`). `redis:7-alpine` UID/GID is 999/999 (not bitnami's 1001).

Phase 10 (`3d3e911`) — CA-363 + CA-364 + CA-365 + CA-370 + U-M4 (co-fix M). CA-363: network-error states added to repositories list and repository detail pages — inline error callouts replace silent blank states (CRITICAL closure). CA-364/CA-365: per-artifact button consolidation extended to all knowledge-artifact panels per the CA-242 pattern (one action per state: in-flight → Cancel; stale/failed → Regenerate; else → Refresh). U-M4 (co-fix): Workflow Story empty-state generate-button variant added. CA-370: MEDIUM confidence badge switched to `--text-inverse-on-amber` (`#431407`, tailwind amber-950) on amber background; WCAG AAA in both themes; the prior amber-600+white path was 3.19:1 (fails AA per RUBY-C1).

Phase 11 (`5b8265e`) — CA-366 + CA-367 + CA-368 + CA-369 a11y closure. CA-366: profile-editor label association. CA-367: 5 knowledge-tab accordions converted to stable `accordion-(header|panel)-(guide|ask|execution|workflow|explore)` IDs with full 3-attribute reciprocal binding (`aria-controls` ↔ panel `id` ↔ `aria-labelledby` ↔ button `id`). CA-368: ThemeToggle in `web/src/components/ThemeToggle.tsx` shows target-state icon (Sun in dark, Moon in light); `mounted` SSR hydration guard prevents flash. CA-369: onboarding bare `<input>` elements migrated to design-system `<Input>` component (per RUBY-M2).

Phase 12 (`2e0f670`) — CA-328 `rest.Server` mirror-field collapse. Applies the CA-184 pattern to `rest.Server`: 21 private mirror fields removed; single `Deps *appdeps.AppDeps` field is the canonical accessor. `syncServerDepsFromAppDeps` deleted. `TestServerStructureCanary` in `internal/api/rest/structure_test.go` pins the field set.

Load-bearing constraints for future-Claude:

- **`Config.Security.CSRFFullCoverageEnabled` defaults `true` (CA-334).** Startup ERROR log fires when set to `false`. Non-browser clients sending Bearer + session cookie must set `SOURCEBRIDGE_SECURITY_CSRF_FULL_COVERAGE_ENABLED=false` until they inject `X-CSRF-Token`. Pinned by `TestSecurityDefaultsCSRFFullCoverageEnabled`. Don't revert the default — it was `false` pre-CA-334 only because the frontend injection (CA-198/201) hadn't shipped yet; both are now live.
- **`config.WarnCSRFDisabled` and `config.WarnAllowPrivateBaseURL` are the canonical startup-warning helpers** (extracted from `cli/serve.go` in Phase 1). New security-gate warnings MUST follow the same testable `config.Warn*` pattern rather than inline `slog` calls at the `cli/serve.go` call site. Tests: `TestWarnCSRFDisabled_FiresWhenFalse` (level `slog.LevelError`, attr `csrf_full_coverage_enabled=false`), `TestWarnAllowPrivateBaseURL_FiresWhenTrue` (level `slog.LevelWarn`, attr `allow_private_base_url=true`).
- **`pathutil.ValidateLLMBaseURL(req.BaseURL, s.cfg.LLM.AllowPrivateBaseURL, nil)` is wired at BOTH `/llm-profiles` create+update sites (CA-335).** Adding a new profile-write handler without wiring it re-opens the SSRF gap. Worker-side `validate_llm_base_url` at `workers/common/llm/config.py:183` is defense-in-depth; both layers are bypassed when the respective `AllowPrivateBaseURL` flag is `true` (current default — flip to `false` is a 1.0 ticket per Decision 1). CA-335 is **save-time-only**; the docstring at `pathutil.go:261-272` tracks the DNS-rebind bypass risk as a follow-up. Response body on rejection is always `{"error":"invalid_base_url"}` — raw URL never reflected (pinned by `internal/api/rest/llm_profiles_ssrf_test.go`). `handleUpdateLLMProfile` gates on `req.BaseURL != nil` before calling the validator (`ProfileUpdateRequest.BaseURL` is `*string`; nil = "don't change").
- **`AllowPrivateBaseURL=true` emits a startup WARN (CA-336).** The IP-range denylist is CONDITIONAL on this flag being `false`. The scheme allowlist (`https://`, `ssh://`, SCP-form only; `http://` rejected) and `http.followRedirects=false` are always active. Default flip to `false` is a 1.0-readiness ticket and must not be done unilaterally.
- **DATABASE is `"sourcebridge"` across ALL deployment methods (CA-345).** BOTH `SOURCEBRIDGE_STORAGE_SURREAL_DATABASE` (configmap line 11) AND `SOURCEBRIDGE_WORKER_SURREAL_DATABASE` (line 41) must be `"sourcebridge"` — partial application split-brains the API and worker. Kustomize operators with data under `"main"` MUST follow the 8-section runbook at `docs/admin-runbooks/kustomize-database-rename.md` (SurrealDB has no `db rename` statement; export+import is the only migration path). This is the single most consequential operator-facing change in the campaign. CHANGELOG entry is marked **BREAKING**.
- **NetworkPolicy `allow-worker-ingress`, `allow-surrealdb-ingress`, `allow-redis-ingress` use the single-list-item AND-combinator shape (CA-346 + CA-347 + CA-355).** A single `- podSelector: {...}\n  namespaceSelector: {...}` list item means AND. Two separate `- podSelector: {...}` and `- namespaceSelector: {...}` list items means OR — silently widens the policy to all pods in the namespace OR all pods with the label across namespaces. Any new ingress policy MUST use the single-list-item shape. Helm uses `{{ .Release.Namespace }}`; kustomize operators in non-`sourcebridge` namespaces must label the namespace (`kubernetes.io/metadata.name=<name>`; K8s ≥ 1.22 injects it automatically).
- **Helm Redis StatefulSet has pod-level `securityContext` with `runAsNonRoot: true`, `runAsUser: 999`, `runAsGroup: 999`, `fsGroup: 999` (CA-348).** `redis:7-alpine` runs as UID/GID 999/999 — not bitnami's 1001. Container-level: `readOnlyRootFilesystem: false` (Redis writes RDB temp files to the root FS before atomic-rename into /data; `true` would crash Redis on save), `allowPrivilegeEscalation: false`, `capabilities: drop: [ALL]`. Don't change the UID/GID without verifying the actual base image.
- **Worker memory limit is `"2Gi"` on both deployment methods (CA-349).** Requests stay `512Mi`. Both `deploy/kubernetes/base/worker.yaml` and `deploy/helm/sourcebridge/values.yaml` must agree; divergence causes Helm vs kustomize confusion (pinned by BOB-M3 + OTTO-M2 verification checks).
- **MEDIUM confidence badge uses `--text-inverse-on-amber` (`#431407`, tailwind amber-950) on amber background (CA-370).** Passes WCAG AAA in both light and dark themes. The prior amber-600+white path was 3.19:1 (fails AA per RUBY-C1) and must not be reintroduced.
- **5 knowledge-tab accordions use stable `accordion-(header|panel)-(guide|ask|execution|workflow|explore)` IDs with full 3-attribute reciprocal binding (CA-367).** `aria-controls` on the button → panel `id`; `aria-labelledby` on the panel → button `id`; `aria-expanded` on the button toggled by state. Tests assert all three attributes. Don't use generated or positional IDs — stable IDs are what allows deep-link anchoring and AT navigation.
- **ThemeToggle at `web/src/components/ThemeToggle.tsx` shows the target-state icon (Sun in dark mode, Moon in light mode) (CA-368).** Import from `@/components/layout/ThemeProvider` (NOT `AppearanceProvider`). The `mounted` boolean SSR hydration guard is load-bearing — without it, Next.js server render produces a hydration mismatch because the server can't know the user's color-scheme preference.
- **Onboarding `<input>` elements migrated to design-system `<Input>` component (CA-369).** Any new onboarding form field MUST use `<Input>` — bare `<input>` bypasses design-system focus-ring, label association, and error-state contracts (RUBY-M2).
- **`result.Usage.Model` and `Diagnostics.ModelUsed` are populated from `Config.AskModelResolver(ctx)` on BOTH the nil-usage path AND the synthesis-failed early-return path in `deep_pipeline.go:395-424` (CA-390).** `TestDeepPipeline_AskModelResolver_NilUsagePath` and `TestDeepPipeline_AskModelResolver_SynthesisFailedPath` pin both call sites. This reinforces the CA-324 Fix B constraint — don't fork `resolveAskModel(ctx)` into a static-config read on either path.
- **Token name validator at `admin.go:215` calls `strings.TrimSpace` BEFORE validation (CA-391).** Whitespace-only names reject as empty after trim. Trailing whitespace trims-then-stores. The 128 maximum is a **rune count, not a byte count** — `TestTokenNameValidation_RuneBoundary` uses `strings.Repeat("界", 129)` (387 bytes, 129 runes) to pin this. Using `len()` (bytes) instead of `utf8.RuneCountInString()` would silently accept 128-rune multibyte names while rejecting shorter ASCII ones.
- **`internal/qa.JobTypeToOp` is in the flat `qa` package (NOT a subpackage) (CA-327).** Canonical mapping from job type string to `Op`; both REST and GraphQL transports call it directly. `TestJobTypeToOpCanary_QADeepSynth` is the regression gate — if the `qa.deep_synth` job type is added without a corresponding `Op` entry, the canary fails. Don't create a parallel mapping in either transport layer.
- **`internal/knowledge.DiscussionContextFromArtifact` is the canonical helper (CA-329).** Format: `"%s:\n%s"` per section, `"\n\n"` separator. The GraphQL shape is retained; the prior REST-only shape is retired. Prompt-shape snapshot canary catches drift. Any new discussion-context assembly MUST go through this helper — don't inline the format string at call sites.
- **`rest.Server.Deps` is mandatory (CA-328); `syncServerDepsFromAppDeps` is GONE.** Tests constructing `Server{}` directly MUST include at least `Deps: &appdeps.AppDeps{}`. The `WithXxx` options write directly to `s.Deps.<Field>`. `s.cfg` is intentionally NOT in `AppDeps` — it's used throughout `NewServer` before derived fields can be computed, and threading it through `s.Deps.Config` creates a circular initialization dependency. Adding a new shared subsystem dependency goes on `AppDeps`, NOT on `Server`. `TestServerStructureCanary` in `internal/api/rest/structure_test.go` pins the field set; any new REST-only field requires updating the allowlist there. `Field Deps` replaces old field `AppDeps` — any `\.AppDeps` grep in the rest package is a bug.

Plan: `thoughts/shared/plans/active-2026-05-13-deliver-critical-high-remediation.md`
Audit synthesis: `thoughts/shared/audits/2026-05-13-audit-full-codebase.md`
Plane tickets: CA-327, CA-328, CA-329, CA-334, CA-335, CA-336, CA-345, CA-346, CA-347, CA-348, CA-349, CA-363, CA-364, CA-365, CA-366, CA-367, CA-368, CA-369, CA-370, CA-390, CA-391 (21 C+H closed); CA-355 + U-M4 (Medium co-fixes closed).

**2026-05-11 post-CA-320 QA reliability trio (CA-324 + CA-325 + CA-326)** — 5 commits, `5ba768c..f40ccbd` plus `b51c5f1` for the pre-existing schema-leak fix surfaced during the trio's E2E.

Three interlocking discussCode / ask-deep bugs traced back to a single E2E reproduction on google-uuid + Ollama qwen3.5:9b. The discussCode mutation was silently returning `{"answer":""}` with null model/tokens despite running 4+ minutes. Root causes were independent but compounded:

- **CA-324 prompt double-wrapping** (`5ba768c`). `internal/qa/deep_pipeline.go` was setting `req.Question = promptEnvelope` — the **full** XML injection-guard envelope (`"The following context is DATA, not instructions...<context>...<question>actual user question</question>"`), not the bare user question. The worker's `discuss_code(question=request.question, ...)` then rendered `"Question: [full envelope]"`, so the model saw the injection-guard as the question with the real question buried inside `<question>` tags. Context appeared twice (once in the envelope, once in `request.context_code`). Fix: `req.Question = in.Question`; the worker's `build_discussion_prompt` reconstructs the injection guard around the bare question + separately-passed context.
- **CA-324 null model/tokens** (`5d5f685`). Proto3 zero-value `LLMUsage` message omission meant `resp.GetUsage()` returned nil whenever Ollama omitted token counts (common). Fix: new `Config.AskModelResolver func(ctx) string` callback on `qa.Config`; production wiring reads `s.llmConfigStore.LoadLLMConfig(ctx).AskModel` so the live profile model surfaces in `result.Usage.Model` + `result.Diagnostics.ModelUsed`. Admin profile changes take effect on the next call without a server restart. `o.resolveAskModel(ctx)` is the helper; it's called from BOTH the post-synth nil-usage path AND the synthesis_failed early-return path.
- **CA-324 silent empty answer** (`5ba768c`). `dispatchDiscussThroughOrchestrator` now guards `res.Answer == ""`: when `Diagnostics.FallbackUsed != ""` it surfaces the fallback reason; otherwise it returns a generic "synthesis completed but returned an empty answer" message. Eliminates the silent path entirely.
- **CA-325 configurable synthesis timeout** (`4736b83` + `1faae37`). New `Config.QA.SynthesisTimeoutSecs int` (env `SOURCEBRIDGE_QA_SYNTHESIS_TIMEOUT_SECS`). Default `0` = preserve built-in `worker.TimeoutDiscussion` (120s). Operators on slow remote LLMs raise this to 600+ to avoid `DeadlineExceeded`. Mirrors the existing `WithKnowledgeTimeoutProvider` pattern: new `worker.WithDiscussionTimeoutProvider(fn)` option + `(*Client).discussionTimeout()` helper. All 4 discussion-class RPCs gated. `docker-compose.yml` exposes the env passthrough.
- **CA-326 fail-fast on DeadlineExceeded** (`f40ccbd`). `qaJobRunner.RunSyncQAJob` now pins `MaxAttempts: 1` on QA synth jobs. The orchestrator's default policy retries `DeadlineExceeded` (`MaxAttempts: 2`); for QA synth that doubles user wait without higher success probability when the upstream LLM provider is hung. Knowledge-generation jobs keep `MaxAttempts: 2` because cold-start model swaps may legitimately complete on attempt 2. `deep_pipeline.go` now logs a WARN with `elapsed_ms`, `model`, and an operator hint pointing at `Ollama /api/ps` + `/api/version` whenever the synth call hits `DeadlineExceeded` after burning the full configured ceiling.
- **gqlgen error scrubber** (`b51c5f1`). Pre-existing schema leak flagged by xander on the CA-320 Phase 2 mid-build review: `handler.NewDefaultServer` was passing resolver errors verbatim into the response `errors[]` array, leaking SurrealDB SDK strings, `ca_<table>` references, and gRPC transport details. New `scrubGraphQLError` presenter classifies error content: `*gqlerror.Error` passes through (resolver-intended user messages); leak markers (`surrealdb`, `thing(`, `rpc error: code`, `rocksdb`, `cbor`, any `\bca_<table>\b`) get replaced with `{"message":"internal error", "extensions":{"correlation_id":"<uuid>","code":"INTERNAL"}}` and the full error is logged server-side at WARN. Cosmetic Low folded in: `requireKnowledgeGenerationSupport` now returns the sentinel `ErrKnowledgeStoreUnavailable` directly instead of `fmt.Errorf("knowledge store not configured: %w", ...)` which previously rendered the message twice.

Load-bearing constraints for future-Claude:

- **`req.Question` is the BARE user question, never `promptEnvelope`.** The envelope is retained for `AskDebug.Prompt` only (debug surface). The worker's `build_discussion_prompt` is the canonical site for the injection-guard rendering. Don't push the envelope back into `req.Question` "for clarity" — that's the CA-324 regression.
- **`resolveAskModel(ctx)` is the only path used in `deep_pipeline.go` to populate `result.Usage.Model` / `Diagnostics.ModelUsed` when usage is nil OR synthesis failed.** Don't fork the call into static-config-only — operator profile changes wouldn't propagate.
- **`Config.QA.SynthesisTimeoutSecs` default is `0` (= use 120s built-in).** Pinned by `TestQADefaultsSynthesisTimeoutSecsZero`. Changing the default to 600 silently rewrites every install's ceiling on upgrade.
- **QA synth jobs are pinned `MaxAttempts: 1`** in `internal/api/rest/qa_deps.go:RunSyncQAJob`. Knowledge jobs are NOT — they still retry. Don't unify these. The QA-synth-retry-is-wasteful argument doesn't apply to a cold-start model swap that legitimately completes on attempt 2.
- **`discussCode` GraphQL adapter MUST guard `res.Answer == ""`** at `dispatchDiscussThroughOrchestrator`. Removing the guard re-opens the silent-empty-answer path. The DiscussionResult schema has no `fallbackUsed` field on the wire; the answer-text guard is the only way the failure reason reaches a client.
- **gqlgen `scrubGraphQLError` content matchers are over-inclusive on purpose.** A new storage layer (Cassandra, etc.) needs a new marker added to `storageLeakMarkers`. A resolver that wants a specific user-facing message MUST return a `*gqlerror.Error` (e.g. via `gqlerror.Errorf`) — that branch passes through. Don't loosen the matchers; loosen the resolver instead.

Plan: discussCode investigation at `thoughts/shared/investigations/2026-05-11-diagnose-discuss-code-empty-answer.md`. Operational note for users on the homelab Ollama: when a different model is pinned in VRAM at infinite keep_alive, every request for a different ask_model triggers a full model swap. Either unload the pinned model (`POST /api/generate` with `keep_alive: 0`) or align the ask_model with the loaded one. CA-326's WARN log includes this hint.

---

**2026-05-11 audit Medium cleanup: 42 findings across 7 phases (CA-320)** — 10 commits, `0d5f6ab..eb1df44`.
Closes 42 of 47 OPEN Medium-severity findings from the 2026-05-08 full-codebase audit. 5 findings deferred (U-M2 URL aliasing, U-M14 comprehension layout, T-M5 integration nil-DB harness, D-M2 `sqlbuild.Builder` extraction, X-M1 per-user lockout). 3 additional implementation gaps surfaced by codex r3 review and intentionally deferred as follow-up campaigns (H1 password propagation, H3 Helm readOnly volumes, M2 OIDC pin tests — see CHANGELOG Known gaps section for detail). All new operator-visible behavior is **additive or behind a flag defaulting to today's behavior**; no functionality is removed; `kubectl apply -k deploy/kubernetes/base/` is bit-identical before and after.

Phase 1 (`0d5f6ab` + `a58336f`) — security header trivials. HSTS header added unconditionally to all API responses. Token name validation (max 128 chars, printable Unicode). All 7 error sites in `git_config.go` converted to structured JSON. Both OIDC error paths (`idp-supplied` at `:33-35`, exchange failure at `:47-50`) scrubbed to `{"error":"authentication_failed","correlation_id":"<uuid>"}` — full error logged server-side; the `description` field is intentionally absent from the response. CORS wildcard-with-credentials opt-in strict guard added (`Config.Server.RejectWildcardCORSWithCredentials`, default `false`; WARN line on default-off boot).

Phase 2 (`ab19334`) — Go code-health. `coerceInt`/`coerceUint64` extracted to `internal/db/helpers.go`; replaced inline CBOR-decode switches in 8+ files. `joinComma` deleted; 2 call sites replaced with `strings.Join`. Python `_UNCAPPED` sentinel exported as `UNCAPPED_SENTINEL`. Sentinel errors `ErrWorkerUnavailable`/`ErrKnowledgeStoreUnavailable` added to `internal/api/graphql/knowledge_generation.go`. `StoreRequirement`/`StoreRequirements` signatures changed to return `error` — repo-wide caller migration (~44 sites). `graphql.DrainAdmitter` and `graphql.LLMProfileLookup` duplicate interface declarations deleted from `internal/api/graphql/resolver.go`; referencing sites (`drain.go`, `llm_profiles.go`, `repository_llm_override_test.go`) updated to `appdeps.*`.

Phase 3 (`cd72648`) — frontend UX + a11y. New `useAsyncOp` hook at `web/src/lib/useAsyncOp.ts`; migrated into `repositories/[id]/page.tsx`. Knowledge tab Cliff Notes accordion: defaults closed when Cliff Notes exist; explicit empty-state CTA when no artifact exists yet (not flat `null`). Engine selector tooltip, sidebar `aria-label` always set, `TopBar` `onBlur` close-on-tab-out, form labels on repos page input/textarea, dialog guard, `<select>` CSS class, repository detail skeleton structural fidelity, login page loading state uses `<Brand>` + `<Spinner>`.

Phase 4 (`5dc40c5`) — test quality. `TestIntegration_ConcurrentReconcileOneWins` flake fixed via assertion relaxation (`wins >= 1 && noDuplicates`). Two `time.Sleep` patterns in `llm_job_store` and `llm_provider_required` tests replaced with deterministic signaling. Backtick contract test added for `CLIFF_NOTES_RENDER_TEMPLATE` / `CLIFF_NOTES_SECTION_REPAIR_TEMPLATE` (pins CA-176 regression gate). `mcp_test.go` hand-rolled mock `KnowledgeStore` replaced with `knowledge.NewMemStore`. Slow-tests CI workflow added at `.github/workflows/slow-tests.yml` (nightly cron, non-blocking).

Phase 5 (`de15368`) — infra hardening overlays. New NetworkPolicy hardening kustomize overlay at `deploy/kubernetes/overlays/network-policy-hardened/` (default-deny + enumerated allow-set for api/web/worker/SurrealDB/Redis/DNS); base `networkpolicy.yaml` is UNCHANGED. New container hardening overlay at `deploy/kubernetes/overlays/hardened/` (flips `readOnlyRootFilesystem: true` via strategic patch). Helm `networkPolicy.enabled` value (default `false`). EmptyDir volume mounts added to base api/worker/web manifests at the correct paths. Helm `values.yaml` backup-snippet comment for secret recovery. `worker.resources.limits.memory` default raised to `4Gi`.

Phase 6 (`996069c`) — web CSP + analytics privacy. CSP headers added to Next.js via `web/next.config.ts` `headers()` function computed at `next build` from `NEXT_PUBLIC_POSTHOG_HOST`. PostHog `identify` no longer sends `email` or `tenant_id`; DNT honored on `identify` and `capture`. MCP HEAD probe gated via `Config.MCP.PublicProbeEnabled` (default `true`); when `false`, returns `404 Not Found`.

Phase 7 (`0672ba8`) + reconcile (`4cf332b` + `eb1df44`) — security depth. LLM `base_url` SSRF validator wired on both Go API side (`Config.LLM.AllowPrivateBaseURL`, default `true`) and Python worker side (`WorkerConfig.LLMAllowPrivateBaseURL`, default `true`) at all provider construction sites. Password minimum length made configurable via `Config.Auth.PasswordMinLength` (default `8`); server enforces the configured minimum; `NewLocalAuthWithOptions` is the canonical constructor; `NewLocalAuth` is the back-compat shim. `surrealLLMConfig` DTO introduced covering all three `ca_llm_config` readers.

Load-bearing constraints for future-Claude:

- **`Config.Server.RejectWildcardCORSWithCredentials` defaults `false`.** Wildcard-with-credentials is a footgun but hard-failing by default would break installs with that misconfig on upgrade. The WARN line on default-off boot gives operators an upgrade window. Wildcard match uses `strings.TrimSpace` before `HasPrefix("*")` / `Contains(".*")` — the TrimSpace is load-bearing against whitespace-padded CSV env entries.
- **`Config.MCP.PublicProbeEnabled` defaults `true`; disabled-state returns `404` (NOT `401`).** `web/src/lib/use-server-capabilities.ts:98-114` treats any status other than 404 as "MCP enabled." Returning 401 on disabled would make the frontend show MCP as enabled while actual MCP requests fail auth — confusing UX. Don't change the disabled status code to anything other than 404.
- **`Config.LLM.AllowPrivateBaseURL` (Go) and `WorkerConfig.LLMAllowPrivateBaseURL` (Python) both default `true`** to preserve local-LLM workflows (Ollama at `http://localhost:11434`). The SSRF validators are wired at all provider construction sites but dormant by default. Worker-side env: `SOURCEBRIDGE_WORKER_LLM_ALLOW_PRIVATE_BASE_URL`. Adding a new provider construction path without calling `ValidateLLMBaseURL` / `validate_llm_base_url` silently re-opens the SSRF gap; the validator call is part of the construction contract.
- **`Config.Auth.PasswordMinLength` defaults `8`.** Production wiring is via `auth.NewLocalAuthWithOptions(jwtMgr, persister, auth.LocalAuthOptions{PasswordMinLength: cfg.Auth.PasswordMinLength})` at `cli/serve.go`. `NewLocalAuth` is the shim that passes `PasswordMinLength: 8`. `LocalAuth.PasswordMinLength()` accessor exposes the configured value. `/auth/info` and `/auth/desktop/info` responses include `"password_min_length"`. Note: client-side propagation to `desktop_auth.go`, `cli/setup_admin.go`, and web form validators is NOT complete — those still client-validate at 8. Hardcoding 8 in any new consumer is a regression against the configurable contract; read from the `/auth/info` response instead.
- **CSP is BUILD-TIME from `NEXT_PUBLIC_POSTHOG_HOST`.** `NEXT_PUBLIC_*` is webpack-DefinePlugin inlined at `next build`; the runtime container env has no effect on CSP. This is consistent with the PostHog client at `web/src/lib/posthog.ts:4` which reads the same build-time env. Operators who need a non-default PostHog host MUST set `NEXT_PUBLIC_POSTHOG_HOST` when building the web image. Path B (true runtime CSP override via `/api/v1/runtime-config`) is a deferred follow-up ticket. CSP includes `wss:` + `ws:` in `connect-src` for HMR/SSE.
- **NetworkPolicy base is UNCHANGED.** `deploy/kubernetes/base/networkpolicy.yaml` contains exactly one policy (`worker-allow-api-only`) and is byte-identical to before this campaign. ALL new NetworkPolicy resources (default-deny + the full enumerated allow-set) live in `deploy/kubernetes/overlays/network-policy-hardened/` (kustomize) or render together under `helm --set networkPolicy.enabled=true`. Don't add new policies to base — that would change behavior for every `kubectl apply -k deploy/kubernetes/base/` install on upgrade.
- **`readOnlyRootFilesystem` hardening is OVERLAY-ONLY for kustomize; Helm flag defaults `false`.** The kustomize overlay `deploy/kubernetes/overlays/hardened/` flips `readOnlyRootFilesystem: true` via strategic patch. Helm `securityContext.readOnlyRootFilesystem` defaults `false`; flipping it to `true` WITHOUT the emptyDir volumes will crash pods. **Helm readOnly volumes are NOT fully wired** (known gap H3 from codex r3 review — Helm operators must use the kustomize overlay for this hardening today).
- **Writable paths** (load-bearing for the hardening overlays): worker container writes to `/tmp` AND `/var/cache/sourcebridge` (per `Dockerfile.worker:120-129`); web container writes to `/app/.next/cache` AND `/tmp` (per `Dockerfile.web:64-68`). The path `/home/sourcebridge/.cache` is NOT a real writable path — don't list it.
- **`StoreRequirement` / `StoreRequirements` return `error`.** Repo-wide migration (~44 sites) completed in Phase 2. Any new caller must handle the returned error. The in-memory `graph.Store` impl returns `nil` (cannot fail); `TenantFilteredStore` passes through the inner error; SurrealStore propagates real DB errors that were previously swallowed.
- **`surrealLLMConfig` DTO (`internal/db/llm_config_store.go`) is used by all three `ca_llm_config` readers.** `APIKey` field holds RAW encrypted bytes — decryption happens AFTER CBOR decode in the calling function, NOT inside the DTO (matches CA-179 idiom). `LoadLegacyFieldsRaw` intentionally does NOT decrypt (raw pass-through). Don't add decryption to the DTO itself.
- **`useAsyncOp` does NOT dedup concurrent same-key calls.** Inline comment in `web/src/lib/useAsyncOp.ts` documents this. `run(key, fn)` with the same key called twice launches two independent `fn()` invocations; callers that need dedup must guard with `if (state.isPending(key)) return` at the call site. The tab-prop adapter at `repositories/[id]/page.tsx` already does this. Don't "fix" the hook by adding dedup — it would silently change the contract for callers that intentionally allow concurrent runs.
- **HSTS header is set unconditionally.** Operators on plain-HTTP setups are unaffected (browsers ignore HSTS on plain HTTP). HSTS preload is intentionally NOT included — preload requires domain registration with browser vendors and is irreversible for the preload list lifetime.
- **X-M1 per-user login lockout is DEFERRED (CA-207).** Local login is password-only today; there is no username or email in the login request body to key a per-user lockout off. A per-IP approach would duplicate the existing rate limiter. Will be picked up alongside local-auth username work or in a dedicated per-IP-lockout-tuning ticket.
- **OIDC `description` field is intentionally absent from the scrubbed error response.** The IdP-supplied `error_description` query parameter was attacker-controllable and could reflect arbitrary text into the browser. The new response shape is `{"error":"authentication_failed","correlation_id":"<uuid>"}` — two fields only. Future changes that want to surface more detail must log server-side (not echo from the IdP).

Plan: `thoughts/shared/plans/active-2026-05-11-deliver-audit-medium-cleanup.md`
Audit synthesis: `thoughts/shared/audits/finished-2026-05-08-audit-full-codebase.md`
Plane ticket: [CA-320](https://plane.xmojo.net)

**2026-05-10 bulletproof QA: deep default + auto-recovery from failed/partial understanding (CA-319)** — 6 commits, `fb6b87d..HEAD`.
Two independent root causes produced the same surface symptom ("no evidence" on a freshly-indexed repo) confirmed by dick's investigation. Root cause 1: the QA orchestrator's `Ask` pipeline defaulted `Mode=""` to `ModeDeep` only when called from `discussCode`; bare `ask` mutations (GraphQL `ask`, REST `/api/v1/ask`) mapped nil-mode to empty-string, which the pipeline left as-is and routed to the fast path — no file/snippet retrieval, pure LLM completion. Root cause 2: a previous failed understanding build left the row at `stage=failed` indefinitely; `MarkAllStale` (called on every reindex sweep) skipped `failed` rows, so the repo was permanently blocked from rebuilding without manual operator intervention.

Phase 1 (`fb6b87d`) — pipeline-level deep default. `internal/qa/pipeline.go` defaults `Mode == ""` to `ModeDeep` at the top of `Ask`. Adapters (`ask_adapter.go`, `ask_handler.go`) remain pure nil→empty mappers; the single canonical default site is the pipeline. GraphQL `schema.graphqls` `AskInput.mode` docstring updated to reflect the new default. Three new tests pin the default and the adapter-pass-through contract.

Phase 1.5 (`1af4024`) — CLI inherits server-side default. `sourcebridge ask --server` previously sent `"mode":"fast"` unconditionally when `--mode` was unset (the Go flag default). Phase 1.5 changes the CLI to omit `mode` from the JSON body when `cmd.Flags().Changed("mode")` is false, so the pipeline default fires. Explicit `--mode fast` / `--mode deep` continue to work. `TestPrintAskPretty_PrefersDiagnosticsModeLabel` pins the label preference: when the server echoes back a mode (e.g. `deep`), the CLI displays it rather than its local flag value.

Phase 2 (`705f500`) — `failed → needs_refresh` transition. `MarkRepositoryUnderstandingNeedsRefresh` previously gated on `INSIDE ['first_pass_ready', 'ready']`; Phase 2 extends the gate to `INSIDE ['first_pass_ready', 'ready', 'failed']`. On the `failed → needs_refresh` transition, `error_code` and `error_message` are cleared (empty-string write, not nil — CA-179 constraint not triggered). MemStore mirror updated in lockstep. Two new integration-test variants (`_FromFailed`) pin the new stage transition and the field-clear.

Phase 3 (`ec25888`) — readiness predicate accepts partial-and-progressing corpus. `RepositoryStatus.Ready` now returns `true` for any `stage INSIDE ['first_pass_ready', 'deepening', 'ready']` with `treeStatus IN {partial, complete}` (excluding `missing`); the `failed` stage remains intentionally excluded. New `RepositoryStatus.Partial` field is `true` when the corpus is partially built (tree still progressing). `deepAsk` propagates `Partial` as `FallbackUsed = "understanding_partial"` on the wire. `TestGetRepositoryStatus_ReadinessMatrix` (18-cell table + enum-growth tripwire) pins all stage × tree-status cells.

Phase 5 (`7bf1479`) — Cliff Notes canary. `test_cliff_notes_summary_classic_high_confidence_canary` in `workers/tests/test_cliff_notes.py` pins ≥1 HIGH-confidence section on a seeded fixture corpus plus an anti-stub guard asserting actual file citations are present.

Load-bearing constraints for future-Claude:

- **Pipeline-level default is the canonical site for `Mode == ""` → `ModeDeep`.** `internal/qa/pipeline.go:404-406` is the one place. Don't add a redundant default in the adapter (`ask_adapter.go`, `ask_handler.go`) "for clarity" — that creates dual-write defaulting and the next maintainer has to chase two sites. MCP `callAskQuestion` (`internal/api/rest/mcp.go`) has its own pre-pipeline default (deep for whole-repo, fast when `params.FilePath != ""` or `params.Code != ""`); removing that MCP default and falling through to the pipeline would silently regress the file-pinned-fast policy. The benchmark runner (`benchmarks/qa/cmd/runner/main.go`) always sends explicit `mode`; the pipeline default never fires for it. Don't unify.
- **CLI `--mode` default is still `"fast"` for the explicit-pass case; omission is the new default-inheritance mechanism.** `cmd.Flags().Changed("mode")` is the gate: `false` → omit `mode` from JSON body → pipeline decides; `true` → send whatever the user passed. A future flip of the flag default to `"deep"` would be a no-op from the pipeline's perspective (it would just redundantly set what the pipeline already does) but could confuse `--help` output. Don't conflate flag default with server default.
- **`discussCode` is hardcoded to `qa.ModeDeep` at `internal/api/graphql/discuss_via_orchestrator.go:38`.** Don't "simplify" by routing it through the pipeline default. `discussCode` predates the deep-default flip; its hardcoded `ModeDeep` is an independent semantic statement ("grounded QA always"), not a dependency on the default rotation. The two paths happen to converge on `ModeDeep` today; that's coincidence.
- **`MarkRepositoryUnderstandingNeedsRefresh` is the only path that transitions `failed → needs_refresh`.** Pairs with CA-180's `MarkRepositoryUnderstandingFailed`. Don't introduce a second writer (e.g., an "auto-rescue" goroutine on startup) without explicitly auditing gate overlap.
- **`MarkRepositoryUnderstandingFailed` and `MarkRepositoryUnderstandingNeedsRefresh` gate on non-overlapping stage sets.** Failed gates on `INSIDE ['building_tree', 'deepening']`; NeedsRefresh gates on `INSIDE ['first_pass_ready', 'ready', 'failed']`. The sets are disjoint; a `needs_refresh → failed` regression is structurally impossible. Don't widen either gate to "fix" a non-existent race — both orderings (A: stale fires mid-job, B: stale fires after failure) are documented in the plan's R2 section and both are safe.
- **`error_code` and `error_message` are cleared on `failed → needs_refresh`** (Phase 2). Stale error fields on a row that's about to be rebuilt would mislead operators. Diagnostic context is in the structured log stream (written by the original failure path). The web UI does read these fields: `web/src/lib/graphql/queries.ts:164-186` queries `errorCode`/`errorMessage`, and `web/src/app/(app)/repositories/[id]/tabs/knowledge-tab.tsx:1386-1387` renders `currentUnderstanding.errorMessage`. Clearing both fields is intentional and correct — `stage=needs_refresh` is no longer a failed terminal state; the UI displays `errorMessage` for failed rows and stops displaying it for rows that are being rebuilt. If a future operator-visible prior-failure-class signal is desired, preserve `error_code` only and keep clearing `error_message`.
- **The `Ready` predicate in `internal/qa/reader_understanding.go:89` accepts partial-and-progressing corpus by design.** Tightening to `stage=ready && treeStatus=complete` re-introduces the live repro symptom for repos with partial corpus or a mid-deepening pass. `TestGetRepositoryStatus_ReadinessMatrix` (18 cells + 6-stage enum tripwire) pins all stage × tree-status combinations — if the enum grows and a new stage isn't tested, the tripwire fires. The `failed` stage is intentionally NOT accepted by the predicate; route recovery through the CTA + Phase 2 NeedsRefresh path.
- **`RepositoryStatus.Partial` is the soft signal.** `Ready=true && Partial=true` = partial-corpus run; `Ready=true && Partial=false` = complete-corpus run. The `understanding_partial` `FallbackUsed` value is the wire-level surface. Callers that key off `Ready` alone get the same code path regardless; callers that want to soften the answer read `Partial`. The Go doc-comment on the `Partial` field is load-bearing; preserve it.
- **Three `KnowledgeStore` impls; all three update on every interface change.** `internal/db/knowledge_store.go` (SurrealStore/production), `internal/knowledge/memstore.go` (MemStore/tests), `internal/api/rest/mcp_test.go::mockKnowledgeStore` (test mock). CA-180 made this a hard checklist; CA-319 inherits the discipline. The mock's `MarkRepositoryUnderstandingNeedsRefresh` is a no-op that satisfies the interface signature — that's intentional (it doesn't store transitions).
- **`option<…>` columns — Phase 2 does not introduce any new nilable writes.** The NeedsRefresh SET clause writes `error_code` and `error_message` as empty-string (not nil) — the CA-179 conditional-vars idiom is not required here. Future changes that add a nilable field write to this method must use the conditional-vars idiom or the `CREATE ... CONTENT $c.content` payload form per CA-179.
- **Phase 5 Cliff Notes canary (`test_cliff_notes_summary_classic_high_confidence_canary`) pins ≥1 HIGH-confidence section AND an anti-stub guard** (presence of actual file citations). The anti-stub guard catches the CA-173/CA-176/CA-177 pattern where stub-filled LOW sections bypassed quality gates entirely. Don't remove or weaken this canary without addressing why — it is the regression gate for that class of inference degradation.

Plan: `thoughts/shared/plans/active-2026-05-10-diagnose-qa-empty-llm-context.md` (becomes `finished-...` post-merge)
Investigation: `thoughts/shared/investigations/2026-05-10-diagnose-qa-empty-llm-context.md`

**2026-05-10 extend OnJobFailed reconciler-callback to all artifact types (CA-TBD-knowledge-artifact-reconciler-coverage)** — 1 commit, `f92ae99`.
Closes the follow-up gap deferred from CA-180: `ca_knowledge_artifact` and living-wiki jobs had the same reconciler-no-callback gap — failing via retry exhaustion (`finalizeFailed`) or startup reconciliation (`reconcileZombieJobs`) left the artifact stuck at `status=GENERATING` indefinitely, while the reaper path (`OnStaleJob`) already called `SetArtifactFailed`. Live repro: `generateArchitectureDiagram` stuck at GENERATING for 50+ minutes.

Two-part fix: (1) `SetArtifactFailed` in SurrealStore and MemStore now carries an idempotency gate (`WHERE status INSIDE ['pending', 'generating']`) so re-firing on an already-terminal artifact is a safe no-op, mirroring the CA-180 gate on `MarkRepositoryUnderstandingFailed`. (2) `OnJobFailed` in `router.go` is extended from a single `build_repository_understanding` check to a `switch job.JobType` dispatch covering all five knowledge-artifact types (`cliff_notes`, `architecture_diagram`, `learning_path`, `code_tour`, `workflow_story`) via `SetArtifactFailed`, and both living-wiki types (`living_wiki_cold_start`, `living_wiki_retry_excluded`) via `persistStaleLivingWikiResult`.

Load-bearing constraints for future-Claude:

- **`OnJobFailed` switch in `router.go` and the mirror in `buildOnJobFailedCallback` (test helper) must stay in sync.** The test helper in `internal/api/rest/on_job_failed_dispatch_test.go` mirrors the switch case-for-case; tests will catch dispatch regressions at the store-call level. If you add a new job type, add it to both the production switch and the test helper.
- **`SetArtifactFailed` is now idempotent.** The SurrealDB `WHERE deleted_at IS NONE AND status INSIDE ['pending', 'generating']` gate and the MemStore `if a.Status != StatusPending && a.Status != StatusGenerating { return nil }` gate are load-bearing. A late `OnJobFailed` callback on an artifact that already reached `ready` (from a successful concurrent retry) must not clobber it. Don't remove the gate in either implementation.
- **Living-wiki jobs have no `ArtifactID`** — they carry `TargetKey = "lw:<tenant>:<repoID>"`. The `OnJobFailed` path calls `persistStaleLivingWikiResult` directly (same as `OnStaleJob`) — do not try to route them through `SetArtifactFailed`. The nil guard on `job.ArtifactID` in the artifact-type cases is intentional; living-wiki jobs hit the `living_wiki_cold_start` / `living_wiki_retry_excluded` case instead.
- **`OnStaleJob` (reaper-only) and `OnJobFailed` (all three paths) are intentionally symmetric** for knowledge artifacts. `OnStaleJob` calls `SetArtifactFailed` for artifact jobs and `persistStaleLivingWikiResult` for living-wiki; `OnJobFailed` now does the same. The reaper fires both callbacks; `finalizeFailed` and `reconcileZombieJobs` fire only `OnJobFailed`. Don't collapse the two callbacks — they were intentionally separate (CA-180 load-bearing constraint #2).
- **Frontend `renderKnowledgeProgress` is NOT analogous to the CA-180 `understandingProgressJobView` bug.** It falls back to `artifactAsJobView(artifact)` when `liveJob` is null (job not pending/generating), and `artifactAsJobView` reads the real `artifact.status`. Once `SetArtifactFailed` propagates to the DB, the GraphQL response delivers `status: "FAILED"` and `artifactAsJobView` maps it to `status: "failed"` which `JobProgress` renders correctly. No frontend changes needed.

Plan: deferred follow-up from CA-180 (`thoughts/shared/plans/finished-2026-05-08-deliver-understanding-stage-stuck-on-failure.md` last bullet).

**2026-05-10 Resolver/AppDeps dedup + GitConfigLoader removal (CA-184 + CA-305)** — 3 commits, `5ef9a47` + `9a8a646` + this docs commit.
Closes CA-184 (HIGH — `graphql.Resolver` duplicated 26 mirror fields of `AppDeps` with a sync function) and CA-305 (HIGH — `GitConfigLoader` was a transitional adapter shadowed by production CLI wiring). Phase 1 collapsed all 26 mirror fields onto a single `Deps *appdeps.AppDeps` reference; ~480 production access sites and ~20 test files rewritten; `resolver_deps.go` and `SyncResolverDepsFromAppDeps` deleted. Phase 2 removed the `GitConfigLoader` type, the `gitConfigLoaderAdapter`, and the `GitConfig` field from `Resolver`. The `Resolver` struct now has exactly 4 fields: `Deps`, `Store`, `Plan`, `ClusteringHook`.

Load-bearing constraints for future-Claude:

- **`Resolver.Deps` is mandatory.** Tests constructing `Resolver{}` MUST include at least `Deps: &appdeps.AppDeps{...}` populated with whatever fields the test exercises. Existing degraded-mode checks like `if r.Worker == nil` rewrite to `if r.Deps.Worker == nil`. Don't introduce new tests that construct a bare `Resolver{}` expecting nil-degraded behavior.
- **`Resolver` has exactly 4 fields**: `Deps`, `Store`, `Plan`, `ClusteringHook`. Adding a new field to `Resolver` should follow the question: "could this go on `AppDeps`?" If yes, add it there and access via `r.Deps.X`. The 4 resolver-only fields stayed because of per-tenant / boot-time / closure-at-wiring-time semantics documented in `internal/appdeps/appdeps.go:13-16`.
- **`SyncResolverDepsFromAppDeps` is GONE** (was the helper that synced `AppDeps` → `Resolver` mirror fields). The structural canary `TestResolverStructureCanary` in `internal/appdeps/appdeps_test.go` pins the 4-field shape; adding a new field to `Resolver` requires updating that test deliberately.
- **`GitConfigLoader` is GONE.** Production wiring uses `rest.WithGitResolver(gitResolver)` at `cli/serve.go:816`. Don't reintroduce a fallback adapter; in-process degraded-mode should use the `cfg.Git` config read directly.

Plan: `thoughts/shared/plans/active-2026-05-10-deliver-resolver-appdeps-dedup.md`

**2026-05-09 store ctx threading + decomposition (CA-183 + CA-182)** — 13 commits, `71f6542..bde3ae2`.
Closes CA-183 (CRITICAL — `context.Background()` discarded in every store method, breaking request cancellation and tracing) and CA-182 (HIGH — `internal/db/store.go` monolith at ~4,500 LOC). Five phases shipped under a green-CI discipline (every intermediate commit passes `go build`, `go vet`, `go test -short -race`, and the full integration suite).

Phase 1 (`71f6542` + `150c094`) — signature threading. Every method on `*SurrealStore` (and the 6 store interfaces it satisfies: `GraphStore`, `KnowledgeStore`, `JobStore`, `comprehension.SettingsStore`, `livingwiki.RepoSettingsStore`, `SummaryNodeStore`) gains `ctx context.Context` as its first parameter. The unexported `diagramDocumentPersistence` interface (`internal/api/rest/diagram_document.go`) and the 9 local subset interfaces (`architecture.DiagramStore`, orchestrator `PackageDepsProvider`, QA package interfaces — `RepoLocator`, `GraphExpander`, `ArtifactLookup`, `RequirementLookup`, `SymbolLookup`, `FileReader`, `UnderstandingReader`, `templates.SymbolGraph`, `search.Booster`, `graph.KnowledgeFreshnessProvider`) are all updated in lockstep. Phase 1 covers ~165 caller files / ~2,450 LOC; all `internal/db/` method bodies still call the package-local `ctx()` helper so the package builds.

Phase 2 (`8a3ccd0` + `eefa122`) — `ctx()` helper deletion. The `func ctx() context.Context { return context.Background() }` helper is deleted; all ~185 call sites in `internal/db/` are replaced with the threaded parameter.

Phase 3 (`fa084b4`) — file decomposition. `internal/db/store.go` is deleted and its contents split into 6 per-domain files: `repository_store.go`, `requirement_store.go`, `cluster_store.go`, `analytics_store.go`, `index_result.go`, `helpers.go` (all `package db`). Pure file moves — no logic changes.

Phase 4 (`2eaad0d`) — CLAUDE.md update for phases 1–4.

Phase 5 (`1244396` + `037bf2d` + `0105f3d` + `7081107` + `c67be11`) — MCP handler ctx threading (codex r2 BLOCK). All 29 MCP tool dispatch handlers previously registered via `noCtxHandler` (which silently dropped the live request context) converted to `withCtxHandler`. Handler method signatures updated to accept `ctx context.Context`; every store/knowledge/cluster call inside those handlers now receives the threaded ctx. Three deliberate `context.Background()` detachments preserved: `indexingSvc.Import`, `indexingSvc.Reindex` (background ops that must outlive the request), `changeDispatcher.Submit` (router background work survives agent disconnect). Two `DeleteClusters` ctx-drops in `internal/db/index_result.go` and `internal/db/repository_store.go` fixed. `buildDiffReviewStructural` in `mcp_review.go` upgraded to accept and thread ctx. `internal/db/index_result.go` file-level comment corrected: `MergeIndexResult` is fail-closed, not a multi-step writer. Regression test `TestFormerlyNoCtxHandlerTools_CtxThreadedToStore` in `mcp_dispatch_test.go` verifies that a formerly-`noCtxHandler` tool (`get_index_status`) now propagates the request ctx sentinel value to `store.GetRepository`.

Load-bearing constraints for future-Claude:

- **`func ctx() context.Context { return context.Background() }` is GONE.** Don't add it back. Every method on `*SurrealStore` (and the 6+ store interfaces) takes `ctx context.Context` as first parameter; threading that through is the contract. Bridging with `context.Background()` at a call site is an explicit rollback of CA-183.
- **`internal/db/store.go` is DELETED.** Methods live in `repository_store.go`, `requirement_store.go`, `cluster_store.go`, `analytics_store.go`, `index_result.go`, `helpers.go` — all `package db`. Don't recreate `store.go`.
- **Multi-step writers are now atomic via `RunInTxBatch` (CA-TBD-store-multi-step-write-atomicity resolved)**: `StoreIndexResult`, `ReplaceIndexResult`, and `RecomputePackageDependencies` build a single SQL batch of all statements and send it to SurrealDB wrapped in `BEGIN TRANSACTION; ... COMMIT TRANSACTION;` via `SurrealDB.RunInTxBatch` in `internal/db/tx.go`. SurrealDB v2.6.5 honours multi-statement transactions in a single `Query` call — proven by `TestRunInTxBatch_MultiStatementTransaction` in `tx_integration_test.go`. Context cancellation before the batch fires leaves the DB unchanged; any error from the server (including `THROW`) rolls back the entire batch. `MergeIndexResult` is still a fail-closed stub (not a multi-step writer). `RunInTx` (fn-callback form) is still a no-op; use `RunInTxBatch` for new multi-statement writes. Separate `BEGIN TRANSACTION`/`COMMIT TRANSACTION` calls as standalone Query calls are still NOT supported (server returns `Unexpected statement type encountered`).
- **`var _ diagramDocumentPersistence = (*db.SurrealStore)(nil)`** at `internal/api/rest/diagram_document.go:29` is the compile-time gate that `*SurrealStore` (now spread across 6 files) still satisfies the unexported `diagramDocumentPersistence` interface. The 3 satisfying methods live in `internal/db/diagram_document_store.go`. Don't move them without updating this assertion.
- **`impactReportRow` lives with the impact-report methods** in `analytics_store.go`, NOT in `helpers.go`. Decomposition rule: cross-domain row types (`surrealRepo`, `surrealFile`, `surrealSymbol`, `surrealModule`, `surrealRequirement`, `surrealLink`) live in `helpers.go`; single-domain row types live with their domain.
- **TenantFilteredStore `hasAccess` gating preserved** on the same 8 federation methods as wave-3 P8 (CA-203). The ctx threading didn't introduce new methods; the gating set was 8 at this point (extended to 25 total across the 2026-05-16 and 2026-05-17 campaigns — see those entries).
- **Embedded-interface override audit findings**: `callRecorder` (`internal/graph/filtered_test.go:42-79`, 8 overrides), `countingGraphStore` (`internal/api/rest/mcp_change_impact_test.go:495-502`), `truncatingGraphStore` (`internal/api/rest/mcp_requirement_tools_test.go:1137-1148`) all updated to take ctx as first parameter. Future test override patterns must follow.
- **Phase 1 scope was ~165 caller files / ~2,450 LOC**: every package holding a `graphstore.GraphStore` value or implementing one of the 6+ store interfaces or any of the 9 local subset interfaces listed above.
- **`noCtxHandler` adapter is DELETED.** After Phase 5 finished converting all 29 registrations to `withCtxHandler`, `noCtxHandler` had zero callers — golangci-lint's `unused` check flagged it as dead code and it was removed (the `safeDispatch` test path referenced the name in comments only, not in actual calls). New tool registrations MUST go through `withCtxHandler` so the request ctx threads to every store call; the compiler enforces the signature, and `TestFormerlyNoCtxHandlerTools_CtxThreadedToStore` in `mcp_dispatch_test.go` catches regressions at the dispatch layer. Do not reintroduce a no-ctx adapter — that re-opens the silent ctx-drop hole.
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
- **25 `TenantFilteredStore` methods now gated by `hasAccess`** (8 federation + 16 ID-keyed + 1 cross-repo refs; updated from 8 post-CA-489 in the 2026-05-16 campaign, +1 ID-keyed (`StoreAPIContract`) in the 2026-05-17 campaign) — opaque-nil return on cross-tenant denial. Future additions to `GraphStore` that return data must follow the same gating pattern in `TenantFilteredStore`. `TestTenantFilteredStoreCanary_AllIDKeyedMethodsGated` is the enforcement contract.
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
2. For `rest.Server`: also add the matching lowercase field and one line to
   `syncServerDepsFromAppDeps` in `internal/api/rest/router_deps.go`.
3. For `graphql.Resolver`: no resolver-side step is required. The resolver
   reads the new field directly via `r.Deps.<Field>`. There is no
   `syncResolverDepsFromAppDeps` function — it was removed in P11.

The three resolver-only fields (`Store`, `Plan`, `ClusteringHook`) live directly
on `graphql.Resolver` because they are not shared with `rest.Server`. See
`internal/appdeps/appdeps.go:13-16` for the rationale. Do not add mirror fields
to `graphql.Resolver` for anything that already lives on `AppDeps`.

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
