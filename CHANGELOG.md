# Changelog

All notable changes to SourceBridge are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

> **Note**: This section is maintained manually as a preview of what
> release-please will roll into the next rc bump. Release-please regenerates
> the cut release notes from conventional-commit messages on each push to
> main, so this section will be replaced when the next release PR opens.

### ⚠ BREAKING

* **security:** At-rest encryption envelope upgraded from `sbenc:v1` to `sbenc:v2` (CA-200). The previous v1 envelope used unsalted single-pass SHA-256 as the AES-GCM KDF; v2 uses Argon2id (memory-hard, GPU-resistant) with a per-installation salt. v1-encrypted rows now fail closed on decrypt with `ErrV1EnvelopeRejected` — operators MUST re-save every encrypted secret (LLM API keys, git tokens, living-wiki webhook secrets) via the Admin UI to migrate. Pre-release telemetry shows 0 prod installs in the wild; the aggressive migration is acceptable. The salt is derived deterministically from the encryption key via HMAC (zero operator burden — no new env var); a follow-up will switch to an independent random salt persisted in `SOURCEBRIDGE_SECURITY_ENCRYPTION_SALT_FILE`. Hot-path latency unchanged (Argon2id derivation runs once at cipher construction, not per encrypt/decrypt) ([b4d7a08](https://github.com/sourcebridge-ai/sourcebridge/commit/b4d7a08)).
* **security:** JWT secret default literal `dev-secret-change-in-production` removed; `Validate()` now enforces a ≥32-byte length gate (CA-311). Operators not using Helm's auto-generated `jwt-secret` Secret must configure `SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE` (preferred — file path; matches `_FILE` convention; precedence over env) or `SOURCEBRIDGE_SECURITY_JWT_SECRET` (literal env). When neither is set, the API auto-generates a 64-hex-char in-memory secret and emits a one-time INFO log; **sessions invalidated on every restart in this mode** — production multi-replica deployments must provide the secret externally. Helm chart's existing `randAlphaNum 32` Secret remains stable and passes the new gate. Docker Compose dev defaults updated to a 64-hex publicly-known placeholder so `docker compose up` continues to boot — `WarnInsecureDefaults` still flags it as insecure ([b55b623](https://github.com/sourcebridge-ai/sourcebridge/commit/b55b623)).

### Security

* **api:** pprof endpoints (`/debug/pprof/*`) now require admin role when `SOURCEBRIDGE_PPROF_ENABLED=true` (CA-204). Goroutine, heap, and profile dumps could leak in-flight tokens and environment values; the previous unauthenticated mount made enabling pprof a hard production no-go. The endpoints still 404 when the env var is unset ([9b17a60](https://github.com/sourcebridge-ai/sourcebridge/commit/9b17a60)).
* **livingwiki:** Confluence webhook (`POST /webhooks/confluence`) refuses to dispatch when `ConfluenceWebhookSecret` is empty (CA-206). Returns 503 with scrubbed body `{"error":"route_unavailable"}` (operator-actionable details only in server log). Secret is now resolved per-request, so admin-UI changes take effect without restart. Existing tests updated to provide a configured secret + valid signature; new LOAD-BEARING tests anchor the unconfigured-secret-503 contract and the post-boot-resolver behavior ([9b17a60](https://github.com/sourcebridge-ai/sourcebridge/commit/9b17a60)).
* **livingwiki:** Notion-poll webhook (`POST /webhooks/notion-poll`) requires admin bearer auth (NEW-H1, follow-up of audit). Previously unauthenticated — any external caller could trigger arbitrary repo reconciliation. The route only registers when `LivingWikiWebhookDeps.NotionPollAuthMiddleware` is provided; production wiring composes `authMiddleware` + `RequireRole(admin)` to match the operator-controlled CronJob context. Bearer was chosen over shared-secret to avoid a new deployment footgun ([9b17a60](https://github.com/sourcebridge-ai/sourcebridge/commit/9b17a60)).

### Added

* **worker:** Universal LLM concurrency gate with retry, tok/s observability, and admin gate-snapshot endpoint ([3093791](https://github.com/sourcebridge-ai/sourcebridge/commit/30937917ae76fe5f9a25a102499e26bbbb1ab1af)). Every LLM and embedding call passes through a per-`(provider, base_url)` host or per-kind semaphore, jitter-aware tenacity retry loop, optional RPM limiter, and tok/s ring buffer. Eliminates the 5×3=15-attempt storm from stacked hand-rolled retries. `GetProviderCapabilities.max_concurrent_calls` is now sourced from the gate's effective cap for the resolved context, so Go and Python agree on capacity by construction. Adds a live "LLM Gate Activity" section to the admin monitor page. Kill switch: `SOURCEBRIDGE_LLM_CONCURRENCY_WRAPPER_ENABLED=false`.
* **comprehension:** Section-level repair pass for `learning_path` and `workflow_story` deep render — architectural parity with cliff_notes ([810dc1a..4546ced](https://github.com/sourcebridge-ai/sourcebridge/compare/810dc1a...4546ced)). When a step or section fails the confidence floor, the repair re-renders just that unit with a repair-targeted prompt; single-attempt policy with three-clause acceptance check (mirrors cliff_notes). Distinguished telemetry: operation label trichotomy (`*_repaired` when repair fired and accepted; `*_repair_attempted` when fired but all rejected; otherwise unchanged). Distinguished error logs (`*_repair_skipped_budget_exceeded` vs `*_repair_skipped`). Net runtime is approximately zero change — after the prompt + few-shot fix below, sections rarely fall through the gate; benefit is robustness on edge cases.

### Changed

* **db:** Bumped SurrealDB to v2.6.5 (from v2.2.1). Remediated `option<…>` NULL writes in `SetModelCapabilities` (`cost_per_1k_input`, `cost_per_1k_output`, and preemptive `last_probed_at`). Migrated `EnrichRequirement` GraphQL mutation from the deleted `UpdateRequirement` Go method to `UpdateRequirementFields`; side effect: `EnrichRequirement` no longer bumps `updated_at` when the LLM returns no enrichment suggestions.

### Fixed

* **db:** Re-indexing an existing repository no longer silently drops test-linkage edges (CA-304 — CRITICAL silent data loss). Two stacked bugs combined to lose every `RelationTests` row on every re-index: the DELETE block in `ReplaceIndexResult` missed `ca_tests`, leaving orphan rows pointing at deleted symbols; and the relation re-insert loop only handled `RelationCalls`, silently dropping every `RelationTests`. After the fix, `GetTestsForSymbolPersisted` returns the correct set after re-indexing. New integration test pins the contract ([a37abb0](https://github.com/sourcebridge-ai/sourcebridge/commit/a37abb0)).
* **web:** UX terminology consistency + accessibility hardening across the daily-driver surfaces (CA-239..CA-250 — 12 HIGH ruby-audit findings). Status badges now render plain-English labels with explanatory tooltips; queue strip jargon ("Queue 0 / 1 slots") replaced with "1 running · 3 queued (cap 4)" / "Idle"; user-facing surface standardized on "Cliff Notes" everywhere (the audit recommended choosing one noun consistently — "Cliff Notes" was retained as the user-facing brand); the three-button toolbar collapsed to a single contextual action; the duplicate "Build understanding" button removed in favor of the canonical page-header trigger; the disabled empty-state button now exposes its reason via title + aria-describedby; section + step rows expose proper button semantics (role, tabIndex, aria-expanded, onKeyDown); execution-trace select gets a sr-only label; monitor page renders a skeleton during the first poll; "Test Connection" interprets the JSON response into a success/failure callout with raw payload behind a details disclosure; admin/knowledge auto-refreshes (5s) while artifacts are generating ([2309b60](https://github.com/sourcebridge-ai/sourcebridge/commit/2309b60)).
* **infra:** Bundled k8s base manifests + Docker Compose now apply-and-boot cleanly without operator intervention beyond the documented overlay points (CA-227, CA-228, CA-317, CA-229). LLM URL ConfigMap placeholders changed from `CHANGE-ME-set-via-overlay` to `http://no-llm-configured-set-via-overlay.invalid:0` (RFC 2606 .invalid never resolves; an unset overlay surfaces as immediate connect failure rather than a DNS-resolution loop). API PVC and SurrealDB volumeClaimTemplate `storageClassName` placeholders changed to `""` (empty = cluster default StorageClass; production operators still override via overlay for SurrealDB's required `Retain` reclaimPolicy). `SOURCEBRIDGE_WORKER_DEBUG=true` was hardcoded in Docker Compose; now `${SOURCEBRIDGE_WORKER_DEBUG:-false}` in hub deploy (was burning debug-level logs in every public install) and `${SOURCEBRIDGE_WORKER_DEBUG:-true}` in dev. Dev compose `SURREAL_USER` / `SURREAL_PASS` env-overridable (default still `root/root` for dev ergonomics) ([4a9ac10](https://github.com/sourcebridge-ai/sourcebridge/commit/4a9ac10)).
* **web:** UI state-derivation crashes hardened against partial response shapes (CA-256..CA-260). Five mechanical optional-chain fixes that close active CA-180-pattern crashes: knowledge-tab `repoJobs?.stats.queue_depth` double-chained; monitor `saturation` useMemo double-guards `stats.max_concurrency` and `stats.in_flight` independently with `?? 0`; `RepoJobActivityResponse.stats` marked optional; `artifactAsJobView` chained-ternary replaced with explicit switch + non-prod warn on unknown statuses; `active_profile_missing` consumer sites use `?? false` so older API replicas don't flicker the repair banner ([db1614c](https://github.com/sourcebridge-ai/sourcebridge/commit/db1614c)).
* **livingwiki:** Restore deep-render confidence for local LLMs (qwen3.6 family and analogous models). Under the higher `deep_parallelism=4` default, KV-cache pressure caused models to emit NDJSON instead of a JSON array — the parser fell back and stub-filled 12 of 16 sections as `confidence="low"`, bypassing quality gates entirely. Two-part fix: NDJSON recovery in `parse_json_sections` (accepts `≥2 dict fragments`; pinned by false-positive trap test), plus a provider-aware default for `deep_parallelism` (`2` for local providers, `4` for cloud — preserves cloud throughput). New env vars `SOURCEBRIDGE_CLIFF_NOTES_DEEP_PARALLELISM` and `SOURCEBRIDGE_CLIFF_NOTES_DEEP_REPAIR_PARALLELISM` for operator override. See [`docs/admin/llm-config.md`](docs/admin/llm-config.md#deep-render-parallelism-cliff-notes) for the tuning runbook ([952f88e..4f3f079](https://github.com/sourcebridge-ai/sourcebridge/compare/952f88e...4f3f079)).
* **livingwiki:** Collapse `GraphStoreMetrics` N+1 caller lookups using `GetCallEdges` + one `GetSymbolsByIDs` batch ([56246ea](https://github.com/sourcebridge-ai/sourcebridge/commit/56246ea040994e56fb05583a131c061bd95250d6)). Page generation previously stalled indefinitely on any non-trivial repo because each metric call issued one SurrealDB websocket round-trip per caller (O(symbols × callers) sequential RPCs). Caught via pprof goroutine dump during deploy validation. Also adds a dev-only `/debug/pprof/*` endpoint behind `SOURCEBRIDGE_PPROF_ENABLED=true`.
* **confluence:** Adopt existing pages on 400-duplicate-title; repo-scoped Architecture section title; return create-response ID directly ([222eb3a](https://github.com/sourcebridge-ai/sourcebridge/commit/222eb3a0dd5280491e49b5270b3b05e9933e14dc)). Confluence returns 400 (not 409) when a target title already exists in the space without our property — typical for orphan pages from earlier OSS runs. `EnsurePage` and `UpsertPage` now look the page up by title alone, stamp our property, and update its body. The Architecture section page title is now `Architecture · <repoName>` to avoid cross-repo collisions in shared spaces. The post-create title-search lookup that races Atlassian's eventual-consistency search index is gone — `createPage` returns the new page's ID directly from the create response.
* **db:** SurrealDB cluster-replace transactions no longer fail with "Found NULL for field llm_label" when clusters are written before labeling completes. `ReplaceClusters` now uses `CREATE ... CONTENT $c.content` with `omitempty` on the `LLMLabel` field; `SaveClusters` conditionally includes the field only when non-nil. Eliminates ERROR-level log noise on every Living Wiki understanding pass that produces clusters ([2dd1149](https://github.com/sourcebridge-ai/sourcebridge/commit/2dd1149)).
* **orchestrator:** Knowledge jobs no longer permanently jam the orchestrator slot queue across process restarts. Each orchestrator generates a per-process UUID at startup, stamps it on jobs at creation, and synchronously reconciles any active jobs from a prior process (whose `updated_at` is older than 90s) before serving requests. Zombie jobs are marked failed with error code `PROCESS_RESTART_RECONCILIATION`; subsequent enqueues for the same artifact key proceed normally. Default `SOURCEBRIDGE_KNOWLEDGE_MAX_CONCURRENCY=1` no longer means a single supervisor restart blocks all knowledge-artifact generation for 10 minutes ([1ed4cfd](https://github.com/sourcebridge-ai/sourcebridge/commit/1ed4cfd)).
* **comprehension:** Living Wiki deep-render confidence on local LLMs now consistently scores high. An earlier benchmark against the April 14H/0M/2L baseline returned 7H/0M/9L because the deep-render prompt asked the model to "name specific functions/types/methods" without specifying backtick markdown format, while `_enforce_deep_confidence_floor` only counts backtick-wrapped identifiers. Fix adds explicit backtick-format requirement to both `CLIFF_NOTES_RENDER_TEMPLATE` and `CLIFF_NOTES_SECTION_REPAIR_TEMPLATE`, plus backtick-wrapped identifiers in all four `GROUP_FEWSHOT_EXAMPLES`. Result: qwen3.6:35b-a3b-q4_K_M scores 16H/0M/0L, exceeding the April baseline ([f907f16](https://github.com/sourcebridge-ai/sourcebridge/commit/f907f16)).
* **livingwiki:** Build-deep-understanding failures now correctly surface in the repo UI. When a `build_repository_understanding` job failed (LLM unreachable, retry exhaustion, reaper kill, or process-restart reconciliation), the persisted `ca_repository_understanding.stage` was not transitioned to `FAILED`, so the repo screen showed "Generating…" indefinitely while the jobs screen correctly showed "failed". Now: a new `OnJobFailed` orchestrator callback fires from all three failure paths (finalizer, reaper, reconciler) and writes `stage='failed'` via a new `MarkRepositoryUnderstandingFailed` store method (idempotent, gated on `stage INSIDE ['building_tree','deepening']`). Frontend hardening: `understandingProgressJobView` now propagates the live job's actual status instead of hardcoding "generating" for non-running states. Closes the gap on the startup-reconciliation failure path, which previously had no failure callback at all (knowledge-artifact and living-wiki jobs have the same gap on a separate ticket).

## [0.10.0-rc.3](https://github.com/sourcebridge-ai/sourcebridge/compare/v0.9.0-rc.3...v0.10.0-rc.3) (2026-05-06)


### Added

* **a11y:** WAI-ARIA tabs, toast roles, and account menu keyboard navigation ([453cc16](https://github.com/sourcebridge-ai/sourcebridge/commit/453cc162b2f1dc5e40055097441b2b3fa737c7d6))
* **audit-refactor:** phase 4 slice 8 — Input and Textarea primitives (DS-1) ([c18c3bb](https://github.com/sourcebridge-ai/sourcebridge/commit/c18c3bb779593c8e7f20c96a6397d67aabd427ba))
* **audit-refactor:** phase 4 slice 9 — design tokens for danger + confidence (DS-2 + DS-4 + H-4) ([e7aecac](https://github.com/sourcebridge-ai/sourcebridge/commit/e7aecacdcde7e168cc7719f8a42a322c6955b84d))
* **audit-refactor:** phase 5 slice 1 — INFRA-1 hub-compose sentinel + boot warning ticker ([0ea8a00](https://github.com/sourcebridge-ai/sourcebridge/commit/0ea8a00113e8fe6a71de24c1242667822649f30e))
* **audit-refactor:** phase 5 slice 2 — INFRA-2/12/13 probes + healthchecks + Next.js /api/health ([da7149e](https://github.com/sourcebridge-ai/sourcebridge/commit/da7149edafe50148669723a39e5f2715a6cdceec))
* **audit-refactor:** phase 5 slice 3 — INFRA-3/10 + SEC-10 USER directives + ServiceAccounts + mTLS-by-default in production overlay ([8bc15b1](https://github.com/sourcebridge-ai/sourcebridge/commit/8bc15b185cd27883d00c53c1f366492a1e91c54f))
* **audit-refactor:** phase 5 slice 4 — INFRA-4/14 ingress + ServiceMonitor + small fixes ([7772a8e](https://github.com/sourcebridge-ai/sourcebridge/commit/7772a8e7a4430f4005dc6f5c649c682695639dbe))
* **audit-refactor:** phase 5 slice 5 — INFRA-5 SHA-pin GitHub Actions + dependabot config ([7819c22](https://github.com/sourcebridge-ai/sourcebridge/commit/7819c228f128ef313f577d617ede2cd6983baac5))
* **audit-refactor:** phase 5 slice 6 — INFRA-6/7/8/9/11 storage + image pins + workflow rename ([2d3bdb3](https://github.com/sourcebridge-ai/sourcebridge/commit/2d3bdb33be9e34bc23712ee5ee35e59c78da151f))
* **audit-refactor:** phase 5 slice 7 — SEC-8/9/11/12 git-creds + tenant-warn + gated reflection + CSP audit ([10d38b3](https://github.com/sourcebridge-ai/sourcebridge/commit/10d38b3f3668872bd65c36c93ba7d7af8d9a52de))
* **ca-107:** phase 1 — SymbolDetail struct + SymbolLookup.SymbolDetails interface ([8945564](https://github.com/sourcebridge-ai/sourcebridge/commit/89455644e0af730f581e105aefe05f01ee6a749c))
* **ca-107:** phase 2 — qaSymbolLookup.SymbolDetails real impl + SymbolsInFile enrichment ([1a5d973](https://github.com/sourcebridge-ai/sourcebridge/commit/1a5d973178a2d1938eb5fcd66b7c7e763f75274a))
* **ca-107:** phase 3 — Stage 3d range-pin + sliceLines + buildProtoContextSymbols wiring ([6b2a6d2](https://github.com/sourcebridge-ai/sourcebridge/commit/6b2a6d2f7d3a29c9d3004bbe97cf42abfd1af46b))
* **ca-126:** pazaryna wave 3 — lazy capability probe ([#22](https://github.com/sourcebridge-ai/sourcebridge/issues/22)) ([9d15856](https://github.com/sourcebridge-ai/sourcebridge/commit/9d158565bfd730e3c793fd6ace3cffa42f22edfa))
* **ca-127:** pazaryna wave 4 — non-interactive auth + sourcebridge setup admin ([#21](https://github.com/sourcebridge-ai/sourcebridge/issues/21)) ([876c798](https://github.com/sourcebridge-ai/sourcebridge/commit/876c798a77698c39977c83ce186757de87fd1b1c))
* **ca-130:** dual-publish images to GHCR + Docker Hub ([bcd7a8a](https://github.com/sourcebridge-ai/sourcebridge/commit/bcd7a8aeee10a0f576579399d8b1f8185dab0c03))
* **ca-130:** dual-publish images to GHCR + Docker Hub; OSS-clean build script ([#24](https://github.com/sourcebridge-ai/sourcebridge/issues/24)) ([ed91499](https://github.com/sourcebridge-ai/sourcebridge/commit/ed914993852a0d5ae293d90eb25ede84a0bf7427))
* **ca-136:** phase 1 — unify version source via internal/version + script helper ([2524d17](https://github.com/sourcebridge-ai/sourcebridge/commit/2524d1770dbed97243fb86389e3c66ee1ae30d3c))
* **ca-136:** phase 2 — public /api/v1/version endpoint ([3c9fc97](https://github.com/sourcebridge-ai/sourcebridge/commit/3c9fc976dac51a6014b88e486c6bab39e6ffe8c5))
* **ca-136:** phase 3 — Python worker __version__ + gRPC VersionService ([6f74a81](https://github.com/sourcebridge-ai/sourcebridge/commit/6f74a81e6e8bd806a54c3d5bbd08345279f281d9))
* **ca-136:** phase 4 — web build-info display (sidebar footer + admin card) ([f3b8e49](https://github.com/sourcebridge-ai/sourcebridge/commit/f3b8e499f8a9e04efe7130b5b0c897013f48e851))
* **ca-136:** phase 5 — Dockerfile build-args + OCI labels + docker-compose wiring ([3b96a2b](https://github.com/sourcebridge-ai/sourcebridge/commit/3b96a2bc87fef97847135f615731df28ac9c48e6))
* **ca-137:** MCP server reports git-derived version via internal/version ([47adbad](https://github.com/sourcebridge-ai/sourcebridge/commit/47adbad6ff9b10a27918239fa8d58923eab571d9))
* **ca-138:** GraphQL VersionInfo extension + Living Wiki override-flag plumbing fix + gqlgen drift untangle (Path B) ([aeb92d8](https://github.com/sourcebridge-ai/sourcebridge/commit/aeb92d8af4028c8df8ddcb9340843ba169a3871b))
* **ca-139:** cosign keyless OIDC signing for OSS images + verification docs ([c10694c](https://github.com/sourcebridge-ai/sourcebridge/commit/c10694cc4488c92b5e6af86bf0caf4fb0e8916b6))
* **ca-141:** heartbeat-stale threshold + faster reaper tick for living_wiki jobs ([1b61436](https://github.com/sourcebridge-ai/sourcebridge/commit/1b61436b77c5555ca96b72cff164ae2ed210832a))
* **ca-142:** graceful drain + manifest updates for in-flight job protection ([35bfa80](https://github.com/sourcebridge-ai/sourcebridge/commit/35bfa803aa96dd4642716655f6162ffd5e06802f))
* **ca-144:** per-page in-flight visibility for Living Wiki cold-starts ([5023ced](https://github.com/sourcebridge-ai/sourcebridge/commit/5023cedd94a97c9a4ac4a90a81e74506eb5ee145))
* **ca-146:** per-run page count override + wire up MaxPagesPerJob (was no-op) ([313d074](https://github.com/sourcebridge-ai/sourcebridge/commit/313d07430f40621e430ddc27a3a58ca4789a6573))
* **ca-146:** phase 1 — previewLivingWikiPlan query resolver + tests ([2f30a56](https://github.com/sourcebridge-ai/sourcebridge/commit/2f30a563b87d9c0601c6915c8397ace32c5f4f0d))
* **ca-146:** phase 2 — selectedPageIds filter + resolver-body signature validation ([3ee8795](https://github.com/sourcebridge-ai/sourcebridge/commit/3ee87956f48623cd5d27f1a505d3094579c89341))
* **ca-146:** phase 3 — PlanPreviewModal + wire 4 cold-start CTAs through preview ([28c73b9](https://github.com/sourcebridge-ai/sourcebridge/commit/28c73b9c3e2ec4fd6ff4fa426011644c13152d8c))
* **ca-146:** phase 4 — stale-plan banner + integration tests + docs ([c4837ff](https://github.com/sourcebridge-ai/sourcebridge/commit/c4837ff6b6a31be15509a9cde48f331ce60db802))
* **ca-147:** release-please for auto-version-bump-on-merge (CA-140 path A) ([e02fd92](https://github.com/sourcebridge-ai/sourcebridge/commit/e02fd925f8f3e56997d4eff741d458398f0bdbf4))
* **ca-150:** phase 1 — modeltier package + Profile.Tier field ([c0a7bbb](https://github.com/sourcebridge-ai/sourcebridge/commit/c0a7bbbb7300f42d82693046dee3ac7d6e2cd989))
* **ca-150:** phase 2 — materialize profiles from base + tier overrides; pull ProfileTier forward ([c8617ae](https://github.com/sourcebridge-ai/sourcebridge/commit/c8617ae1f5cf300652f12fa2af5e15c14a3130f0))
* **ca-150:** phase 3a — persist quality_gate_tier on ModelCapabilities (Surreal + REST validation) ([e7566a2](https://github.com/sourcebridge-ai/sourcebridge/commit/e7566a28e7e828cd60f0e008fa0b71b6d6adc107))
* **ca-150:** phase 3b — graphql + web admin ui for quality_gate_tier ([ef0db41](https://github.com/sourcebridge-ai/sourcebridge/commit/ef0db41c82d7f04f24daffba8de39b1c9a26ee70))
* **ca-150:** phase 4 — thread quality_gate_tier from snapshot through cold-start + on-demand ([1d1acf3](https://github.com/sourcebridge-ai/sourcebridge/commit/1d1acf3524182bed0e9813290b9597cb68b51ddb))
* **ca-150:** phase 5 — deterministic fixture validation for tier thresholds ([61f0ef3](https://github.com/sourcebridge-ai/sourcebridge/commit/61f0ef3d0a527ac31171921c5ceb21f6b0e04f20))
* **ca-150:** phase 6 — wordsPerCitation fix + integration tests + security hardening + docs ([41c0d5a](https://github.com/sourcebridge-ai/sourcebridge/commit/41c0d5abe50322225480da69721b42b5561597ac))
* **ca-151:** phase 1 — internal/source package + capabilityChecker DI + registry drift test ([a43fd3f](https://github.com/sourcebridge-ai/sourcebridge/commit/a43fd3f120c47ba9fe982d47d212c9e572b5b2c3))
* **ca-151:** phase 2 — get_symbol_source handler + tests ([904a3a6](https://github.com/sourcebridge-ai/sourcebridge/commit/904a3a60d4a8aaacc89f817a023d6eef8fe1946c))
* **ca-151:** phase 3 — get_symbol_context bundled handler + degraded contract + benchmark ([25ce37d](https://github.com/sourcebridge-ai/sourcebridge/commit/25ce37db24a7bbbe4c5ceba9900d79a58f042011))
* **ca-153:** phase 0 — dispatcher refactor + worker ReviewFile plumbing + drift test ([5a38b49](https://github.com/sourcebridge-ai/sourcebridge/commit/5a38b4991c49b17ad3aa1630625eeaba4d533283))
* **ca-153:** phase 1a — get_requirements_for_symbol + get_symbols_for_requirement ([d6bf9e8](https://github.com/sourcebridge-ai/sourcebridge/commit/d6bf9e8f03655e945827ec285bdfb8ec508c591d))
* **ca-153:** phase 1b — get_orphan_symbols + get_uncovered_requirements ([527a7e8](https://github.com/sourcebridge-ai/sourcebridge/commit/527a7e8d03737a10a0e463b9bc4e1cae4820d210))
* **ca-153:** phase 2b — get_field_guide ([e2fb208](https://github.com/sourcebridge-ai/sourcebridge/commit/e2fb208ae675ed982a4a654af1d2556f5a55ff92))
* **ca-153:** phase 2c — predict_change_impact (extends impact_summary) ([7458520](https://github.com/sourcebridge-ai/sourcebridge/commit/7458520461146a7ec2695a28f1e46135d1dc8384))
* **ca-153:** phase 2d — get_changed_requirements ([58cb555](https://github.com/sourcebridge-ai/sourcebridge/commit/58cb555280e1fa955eda0d5669ce8e806d1d402d))
* **ca-153:** phase 3 — get_review_for_diff + companion prompt ([9f33509](https://github.com/sourcebridge-ai/sourcebridge/commit/9f335099c68f6ef72c51ed34898d071e575d277b))
* **ca-154:** phase 1 — find_dead_code + get_untested_symbols (gap audit) ([f4a7b92](https://github.com/sourcebridge-ai/sourcebridge/commit/f4a7b92ba89051e2722eb7cfc924479e355b156f))
* **ca-154:** phase 2a — get_changed_symbols ([6f31b45](https://github.com/sourcebridge-ai/sourcebridge/commit/6f31b457bfc566cb01ffb54b39845ac9bdd0f244))
* **ca-154:** phase 2b — find_importers (package-level) ([be046ff](https://github.com/sourcebridge-ai/sourcebridge/commit/be046ff35da7c35c5704b2ebac6a799961bf8afd))
* **ca-154:** phase 3 — get_blast_radius (BFS + risk score + benchmark) ([a10e0d5](https://github.com/sourcebridge-ai/sourcebridge/commit/a10e0d58a50f5c16da209893d18913a084b15614))
* **CA-155:** WEB-1 Part A — extract KnowledgeTab component ([545b715](https://github.com/sourcebridge-ai/sourcebridge/commit/545b715319e4c21ee4efa4d261201d7eca64395c))
* **ca-163:** phase 1+2 — TierMid + TierLocal factual_grounding overrides + SystemOverview/Product/Mid symmetry fix ([d2f2999](https://github.com/sourcebridge-ai/sourcebridge/commit/d2f2999aa2b3ed3b2faa3ddaf9337f3d4e6542f8))
* **ca-164,ca-165:** phase 1+2 — vagueness TierMid overrides + citation_density TierMid demotion + existing-test updates ([f6ae3eb](https://github.com/sourcebridge-ai/sourcebridge/commit/f6ae3eb44d2f532b7e3acfba8049ab2c7f4328d7))
* **ca-60:** default repository detail to Field Guide tab + prominent empty state ([a95fb4d](https://github.com/sourcebridge-ai/sourcebridge/commit/a95fb4d7ad3e6f933a8aef4e31d2e633d5a0db24))
* **cli:** sourcebridge login — OIDC + local-password flows, persisted server URL ([1bb8148](https://github.com/sourcebridge-ai/sourcebridge/commit/1bb81488a59f7e1e247301dce699e958f388c748))
* **cli:** sourcebridge mcp-proxy stdio→streamable-HTTP bridge (slice 2) ([9c2cd6a](https://github.com/sourcebridge-ai/sourcebridge/commit/9c2cd6af737ffd78840bbc2e622fc5ce1e44f74f))
* **cli:** wire up --version flag (slice 1 of cli-mcp-proxy-and-installer) ([8107610](https://github.com/sourcebridge-ai/sourcebridge/commit/810761001211ad3c48eb6b09de180d7d6752a1b3))
* **config-source-of-truth-r3-followups:** T1.1 — hard-block LLM-backed enqueues with empty provider ([98e6ecf](https://github.com/sourcebridge-ai/sourcebridge/commit/98e6ecfbf16f0968d16ea93cdb19881b10ff0cfd))
* **config-source-of-truth-r3-followups:** T1.2 — per-bundle immutable mTLS credentials via Stage/Commit ([4b08dd8](https://github.com/sourcebridge-ai/sourcebridge/commit/4b08dd8e8d03453c286abd5f59f0e5890b21a54c))
* **config-source-of-truth-r3-followups:** T1.7 + T1.8 — Reloader annotations + worker drain refactor ([7ee2bdf](https://github.com/sourcebridge-ai/sourcebridge/commit/7ee2bdf87aee27399374fd5d3d54b6391244827a))
* **config-source-of-truth-r3:** slice 1 — SecretCipher abstraction (sbenc:v1) ([82a7667](https://github.com/sourcebridge-ai/sourcebridge/commit/82a76672a1aaa553d11ceb4f5ef8af125f7ba3ae))
* **config-source-of-truth-r3:** slice 2 — git creds source-of-truth (HEADLINE) ([b2bf3b2](https://github.com/sourcebridge-ai/sourcebridge/commit/b2bf3b246a76bba40421bcb2d856a41ab6444abb))
* **config-source-of-truth-r3:** slice 3 — llm_provider on jobs + logs ([8857642](https://github.com/sourcebridge-ai/sourcebridge/commit/8857642d30d7e395d88c2600212d1f8456d1dc57))
* **config-source-of-truth-r3:** slice 4 — Go-side mTLS hot reload ([6460c62](https://github.com/sourcebridge-ai/sourcebridge/commit/6460c6282108823376e6a1fa52093dc51c7dcf7f))
* **config-source-of-truth-r3:** slice 5 — flatten manifest tree ([4bb42d2](https://github.com/sourcebridge-ai/sourcebridge/commit/4bb42d23bd05fc635235beaa1bea81c11f7d97a2))
* **graph:** index-time package dependency aggregation ([7fafc09](https://github.com/sourcebridge-ai/sourcebridge/commit/7fafc09a9153bbe197fd2053131ae144772fd28f))
* **health:** surface service-degraded state to users ([a0af523](https://github.com/sourcebridge-ai/sourcebridge/commit/a0af523bb9f1c17bb2fcea06628ed03a6ba2967e))
* **installer:** one-line install.sh + /dev/tty fallback (slice 4) ([780b8aa](https://github.com/sourcebridge-ai/sourcebridge/commit/780b8aa9dec8db1ce739870d58a6e864340db6f5))
* **knowledge:** better progress signal for understanding builds ([fccacd4](https://github.com/sourcebridge-ai/sourcebridge/commit/fccacd4811475a53de88a417386100079b656baa))
* **livingwiki:** wire callers/callees into architecture pages + Confluence cross-page links ([1696b95](https://github.com/sourcebridge-ai/sourcebridge/commit/1696b9595e7b814683eb4b14c9e1486559dbb6a3))
* **livingwiki:** wire knowledge artifacts into architecture page generation ([40651bf](https://github.com/sourcebridge-ai/sourcebridge/commit/40651bf2837362c2f53f15ab0cb0c08536bc386b))
* **llm-onboarding:** phase 3 — N=1 UX clarity, Active pill, onboarding banner, 422 translation ([d4db325](https://github.com/sourcebridge-ai/sourcebridge/commit/d4db32515eff132bea1d29020cda003f77051ccc))
* **llm:** provider profiles — admin UI (slice 2) ([c87cf54](https://github.com/sourcebridge-ai/sourcebridge/commit/c87cf5493d82c34976a98883e7549d9de5106c2e))
* **llm:** provider profiles — per-repo override profile picker (slice 3) ([ff540b2](https://github.com/sourcebridge-ai/sourcebridge/commit/ff540b20d5a31ba3abf1bb7a6360967f5b38cd67))
* **llm:** provider profiles — schema, store, resolver, migration, dual-write (slice 1) ([a938674](https://github.com/sourcebridge-ai/sourcebridge/commit/a938674ed288bd2ad5f288056ae35172a6e92ce1))
* **llm:** provider profiles — UX polish (slice 4 polish) ([4e6b709](https://github.com/sourcebridge-ai/sourcebridge/commit/4e6b7091f283d588e6d2fdbd90d5fbcc9a40821d))
* **llm:** three-layer defense against thinking-model derailment ([f897d09](https://github.com/sourcebridge-ai/sourcebridge/commit/f897d092990d5475cef5f66cc08c756b6be95ad3))
* **mcp-feedback-1a:** add Indexer.IndexFiles signature stub for Phase 1.B ([bc5bb11](https://github.com/sourcebridge-ai/sourcebridge/commit/bc5bb110ac2a7fa21a06cb7f4d1ced647faaa8c2))
* **mcp-feedback-1a:** add IndexResult.Branch field and git.HeadRef helper ([40d8947](https://github.com/sourcebridge-ai/sourcebridge/commit/40d8947d35a6cb2a4f87233e277d6a353583fea1))
* **mcp-feedback-1a:** gate IndexRepository behind RepoIndexFullReason enum ([dca6075](https://github.com/sourcebridge-ai/sourcebridge/commit/dca607500403815b19cd2a586982f8cab972790b))
* **mcp-feedback-1b:** implement Indexer.IndexFiles + 100ms budget test ([83a57d1](https://github.com/sourcebridge-ai/sourcebridge/commit/83a57d128353788fb3b96121fec2e53b812e4c7f))
* **mcp-feedback-1c:** add ChangeEvent schema + internal/changewatch package skeleton ([f70c960](https://github.com/sourcebridge-ai/sourcebridge/commit/f70c96079f59768b7ce8c1847a605b03bf119e3c))
* **mcp-feedback-1c:** add ChangeWatch config + SOURCEBRIDGE_CHANGE_WATCH_ENABLED flag ([b9d7a09](https://github.com/sourcebridge-ai/sourcebridge/commit/b9d7a096a47dc960dcf2d944c658f454be28b297))
* **mcp-feedback-1c:** add GraphStore.MergeIndexResult for per-file delta ([7192d08](https://github.com/sourcebridge-ai/sourcebridge/commit/7192d0841a6a1508c76dbf439ad2be4aaba26a20))
* **mcp-feedback-1c:** implement changewatch fsnotify Watcher ([3bda125](https://github.com/sourcebridge-ai/sourcebridge/commit/3bda125418ab9b686b1dcaf2f4064c20ea061981))
* **mcp-feedback-1c:** implement changewatch Router with rate limiting + circuit breaker ([7a5cf9f](https://github.com/sourcebridge-ai/sourcebridge/commit/7a5cf9f46ee92c0c1d7f02abdf2ab09a8652b7c9))
* **mcp-feedback-1c:** wire _meta.freshness envelope on every MCP tool response ([e094a3f](https://github.com/sourcebridge-ai/sourcebridge/commit/e094a3f01909b734026ea02fa9a2c687451fce45))
* **mcp-feedback-1d:** add changewatch.NormalizePath path-norm helper ([57d52f1](https://github.com/sourcebridge-ai/sourcebridge/commit/57d52f16f401212f399d341b876d04452a2c5e57))
* **mcp-feedback-1d:** add ConnectorAPIConfig + LinkInvalidateGraceHours ([b4b8a05](https://github.com/sourcebridge-ai/sourcebridge/commit/b4b8a05aefe23c426abc74e0868e451373c6ae0a))
* **mcp-feedback-1d:** add HTTP ingress endpoint for change-watch connectors ([d65b8da](https://github.com/sourcebridge-ai/sourcebridge/commit/d65b8dad99b46172552638b74d23f624e0c962fe))
* **mcp-feedback-1d:** add record_change MCP tool + freshness wiring ([f2f5a63](https://github.com/sourcebridge-ai/sourcebridge/commit/f2f5a63db16c5c2a037e97fe6ad8b61c1112951b))
* **security:** bootstrap encryption key via init container + _FILE indirection ([923555d](https://github.com/sourcebridge-ai/sourcebridge/commit/923555d348b30dfed01c74ea15a8467aa7eaeb5f))
* **setup-claude:** --token flag, actionable 401, transport-vs-status errors ([162537a](https://github.com/sourcebridge-ai/sourcebridge/commit/162537a759db7b61a6fe726730c637eec9f1a05c))
* **skillcard:** migrate .mcp.json to stdio-proxy shape (slice 3) ([9fe4456](https://github.com/sourcebridge-ai/sourcebridge/commit/9fe44560e8b4df16e0e1cfd685c812baa596c609))
* **web:** cloud-aware "Use with Claude Code" card with capability probes ([85011ab](https://github.com/sourcebridge-ai/sourcebridge/commit/85011abc1a0af986e0e4f244533fdc135e210209))
* **web:** inline Claude Code token-mint wizard ([95c7d64](https://github.com/sourcebridge-ai/sourcebridge/commit/95c7d64012938b72ddf3f456701e0f07d3017f04))
* **web:** wizard collapses to one-liner-first install (slice 5) ([4454a11](https://github.com/sourcebridge-ai/sourcebridge/commit/4454a11981b0b2c6dd8c3da23019eea5ceb3186e))
* **workspace-llm-source-of-truth-r2:** slice 1 — widen per-repo LLM override to all workspace areas + fix architecture-diagram op gap ([287a4b9](https://github.com/sourcebridge-ai/sourcebridge/commit/287a4b997ed7ceb6de1c464f915a30ba7b13c962))
* **workspace-llm-source-of-truth-r2:** slice 2 — GraphQL surface for per-repo LLM override (mutations + masked field resolver) ([107f4ef](https://github.com/sourcebridge-ai/sourcebridge/commit/107f4ef47002a0e00c2d2969edb02d46b9290161))
* **workspace-llm-source-of-truth-r2:** slice 3 — UI for per-repository LLM override (collapsed advanced section in wiki-settings-panel) ([0d5b520](https://github.com/sourcebridge-ai/sourcebridge/commit/0d5b520c7f339d9c70cde1e473496391d971621c))
* **workspace-llm-source-of-truth-r2:** slice 4 — opt-in mTLS for API↔worker gRPC channel ([6893b75](https://github.com/sourcebridge-ai/sourcebridge/commit/6893b7516222f95da989bd5611d5058a8a28eb0a))
* **workspace-llm-source-of-truth-r2:** slice 5 — inline 'effective value' badges on /admin/comprehension ([b9df7b3](https://github.com/sourcebridge-ai/sourcebridge/commit/b9df7b3b854e303f56bfdf6469f833749ff503d1))
* **workspace-llm-source-of-truth:** slice 1 — runtime resolver + llmcall adapter, REST admin migrated, boot merge bug deleted ([0ee0d53](https://github.com/sourcebridge-ai/sourcebridge/commit/0ee0d539964fa1454a33e6fbc013d6e0bbe2974d))
* **workspace-llm-source-of-truth:** slice 2 — migrate every remaining bypass call site, AST lint ENFORCE ([c1c63be](https://github.com/sourcebridge-ai/sourcebridge/commit/c1c63be8b9468b6a66ac2cb5da078064bf5536b1))
* **workspace-llm-source-of-truth:** slice 3 — encrypt ca_llm_config.api_key at rest with versioned sbenc:v1 envelope ([aa6e173](https://github.com/sourcebridge-ai/sourcebridge/commit/aa6e173fcbde1b50254519fa0487bc69e64c9a7a))
* **workspace-llm-source-of-truth:** slice 5 — per-repo LivingWikiLLMOverride data layer + resolver wiring ([64042ae](https://github.com/sourcebridge-ai/sourcebridge/commit/64042ae5de56927ef935b9f2acf0c00cef28c89f))
* **workspace-llm-source-of-truth:** slice 6 — discoverability callouts on /admin/comprehension and /admin/comprehension/models ([f02cf6f](https://github.com/sourcebridge-ai/sourcebridge/commit/f02cf6f0da7213d64f830c9a376d0cfe6d043a90))


### Fixed

* address codex r2 punch list — streaming SSE, JSON-RPC validation, ordering, drain coverage, classifier strictness, backup mode ([7ecd037](https://github.com/sourcebridge-ai/sourcebridge/commit/7ecd0372392179f358ee264f2d830a79e13914a6))
* **api:** expose per-repo LLM activity/job endpoints under /repositories/{id}/ for non-admin users ([c5fd69f](https://github.com/sourcebridge-ai/sourcebridge/commit/c5fd69f3a153659155b81ca59df36af63774da98))
* **api:** include stats block in /repositories/{id}/llm-activity response ([39df8a5](https://github.com/sourcebridge-ai/sourcebridge/commit/39df8a56c3231d48c393f6df4795d39a1fce3abc))
* **audit-refactor:** phase 0 reconcile — bootstrap admin JWT carries admin role ([e813083](https://github.com/sourcebridge-ai/sourcebridge/commit/e81308395faa08dcd0398bd3b908cfa66ccbea59))
* **audit-refactor:** phase 0 reconcile r2 — drop unused CreateAPITokenInput (codex r2 M-2) ([0a359b9](https://github.com/sourcebridge-ai/sourcebridge/commit/0a359b9a4f707abc3c64f1280e8f2060b1f5eb6f))
* **audit-refactor:** phase 0 reconcile r2 — SEC-1 streamable MCP ownership (codex r2 C-1) ([3fa6d38](https://github.com/sourcebridge-ai/sourcebridge/commit/3fa6d38a036e4feff9441cb6650fbefe80925de8))
* **audit-refactor:** phase 0 reconcile r2 — SEC-2 escape hatch works for Surreal tokens (codex r2 H-1) ([a539071](https://github.com/sourcebridge-ai/sourcebridge/commit/a539071069e1e99d3186a4591ca533b2f6d51c8a))
* **audit-refactor:** phase 0 reconcile r2 — SEC-5 GraphQL Ask/DiscussCode gating (codex r2 C-2) ([7c60d3f](https://github.com/sourcebridge-ai/sourcebridge/commit/7c60d3fa43d7355cf641faa785037bcdbb3366ce))
* **audit-refactor:** phase 0 reconcile r2 — update pre-existing DELETE test for SEC-1 ([8db8daa](https://github.com/sourcebridge-ai/sourcebridge/commit/8db8daa6a8e6a8a302c762cf0b67f1ea76093810))
* **audit-refactor:** phase 0 slice 1 — SEC-7 constant-time CSRF compare ([b640a58](https://github.com/sourcebridge-ai/sourcebridge/commit/b640a5865d3aff1f636f7f51648268547cc1e83b))
* **audit-refactor:** phase 0 slice 2 — SEC-6 CSRF-on-by-default verified + log ([d71d7fa](https://github.com/sourcebridge-ai/sourcebridge/commit/d71d7fa5bef30b5b283a22acdf9ade630c933d0e))
* **audit-refactor:** phase 0 slice 3 — SEC-3 OIDC role allowlist (fail-closed) ([2850fc3](https://github.com/sourcebridge-ai/sourcebridge/commit/2850fc3e6fb9da66c73cdbf435f889cf43398158))
* **audit-refactor:** phase 0 slice 4 — SEC-2 API token role from record ([4cff161](https://github.com/sourcebridge-ai/sourcebridge/commit/4cff161c4c89ee06242afdf1e8522af48bfda331))
* **audit-refactor:** phase 0 slice 5 — SEC-4 admin role gate (user-token routes carved out) ([76b1208](https://github.com/sourcebridge-ai/sourcebridge/commit/76b12087d65347d655921e47097638f9f0edca6f))
* **audit-refactor:** phase 0 slice 6 — SEC-5 ask handler repo-access check ([ce682ed](https://github.com/sourcebridge-ai/sourcebridge/commit/ce682edbed52bac911cb8bde4d1424f3d3775e80))
* **audit-refactor:** phase 0 slice 7 — SEC-1 MCP message auth + session ownership (CRITICAL) ([1c2a656](https://github.com/sourcebridge-ai/sourcebridge/commit/1c2a6565fbb8e582c96ea0c750bf243b000c9ce6))
* **audit-refactor:** phase 1 reconcile r2 — RefreshKnowledgeArtifact validates type before mutating status (codex H1) ([0c6aac3](https://github.com/sourcebridge-ai/sourcebridge/commit/0c6aac3ab1e4e6390961487b7dc33e36b0e394e9))
* **audit-refactor:** phase 1 reconcile r2 — Surreal StoreKnowledgeSections deletes old evidence (codex H2) ([4b279c7](https://github.com/sourcebridge-ai/sourcebridge/commit/4b279c7db347daadbef401a6801d8b88f2c137d9))
* **audit-refactor:** phase 1 reconcile r2 — worker provider sentinel cleanup (codex C1) ([09d046c](https://github.com/sourcebridge-ai/sourcebridge/commit/09d046ca0399e28f7a6eedb9b968a73ea3f43a59))
* **audit-refactor:** phase 1 slice 1a — LLMUsage.Provider field + canonical helper (GQL-2 part 1) ([96eaba5](https://github.com/sourcebridge-ai/sourcebridge/commit/96eaba572dfc722e7cff0963a0c1a1923aeac34c))
* **audit-refactor:** phase 1 slice 1b — rewrite StoreLLMUsage call sites + worker _llm_usage_proto (GQL-2 part 2) ([c1524d2](https://github.com/sourcebridge-ai/sourcebridge/commit/c1524d2dea5700d9d590bf8a6e62451c076a651b))
* **audit-refactor:** phase 1 slice 2 — boolToMutationResult helper (GQL-6) ([936b595](https://github.com/sourcebridge-ai/sourcebridge/commit/936b5952c5830a1e7fce3cf0220d3b055a0f0d5c))
* **audit-refactor:** phase 1 slice 3 — extract DiscussCode context + split EnableLivingWikiForRepo (GQL-3 + GQL-4) ([224f44a](https://github.com/sourcebridge-ai/sourcebridge/commit/224f44adb5944009211d955556e240bb7eb3e96a))
* **audit-refactor:** phase 1 slice 4 — move buildColdStartRunner to coldstart package (GQL-5) ([33c65e7](https://github.com/sourcebridge-ai/sourcebridge/commit/33c65e7040802b566ba2594839cd5a149b74efcd))
* **audit-refactor:** phase 1 slice 5 (pre) — extract per-type generation pipelines + stub RefreshFromExisting (GQL-1 foundation) ([63bb43d](https://github.com/sourcebridge-ai/sourcebridge/commit/63bb43d2012c85ae6ca247811b314a8012d9879c))
* **audit-refactor:** phase 1 slice 5b — RefreshFromExisting for cliffNotesGenerationService (GQL-1 part 1/5) ([bf327f6](https://github.com/sourcebridge-ai/sourcebridge/commit/bf327f618236ed83f4deaf090d9e88814f10dfd7))
* **audit-refactor:** phase 1 slice 5c — RefreshFromExisting for architectureDiagramGenerationService (GQL-1 part 2/5) ([a89b35e](https://github.com/sourcebridge-ai/sourcebridge/commit/a89b35ef06f56e176c3c234efb1a1ba06362236c))
* **audit-refactor:** phase 1 slice 5d — RefreshFromExisting for learningPathGenerationService (GQL-1 part 3/5) ([e85380d](https://github.com/sourcebridge-ai/sourcebridge/commit/e85380d64e0744e4c8765b324d9c499998b696fd))
* **audit-refactor:** phase 1 slice 5e — RefreshFromExisting for codeTourGenerationService (GQL-1 part 4/5) ([7074652](https://github.com/sourcebridge-ai/sourcebridge/commit/7074652f00d058e1a37633068ebeb05367585baf))
* **audit-refactor:** phase 1 slice 5f — RefreshFromExisting for workflowStoryGenerationService + dispatcher cleanup (GQL-1 part 5/5) ([7c5f57a](https://github.com/sourcebridge-ai/sourcebridge/commit/7c5f57a402e0ee582eb58ac2aa6988195476ebd3))
* **audit-refactor:** phase 1 slice 7 — recordDeprecatedFieldRead ctx-aware (GQL-8) ([94d3fdf](https://github.com/sourcebridge-ai/sourcebridge/commit/94d3fdfbeb48d2a5063df86db890bac4d54482bd))
* **audit-refactor:** phase 2 reconcile r2 — complete feature-flag injection in hot paths (codex H) ([4074eec](https://github.com/sourcebridge-ai/sourcebridge/commit/4074eec02ae4c311b9244339aa95c2fd6f66200d))
* **audit-refactor:** phase 2 reconcile r2 — map FeatureAuditLog → CapAuditLog in featureToCapability (codex L) ([5ebf943](https://github.com/sourcebridge-ai/sourcebridge/commit/5ebf9435ac0c6859bb4eb76910245668cd067c59))
* **audit-refactor:** phase 2 reconcile r2 — normalize zero-value Plan to PlanOSS in resolveCapabilities (codex M2) ([27ef250](https://github.com/sourcebridge-ai/sourcebridge/commit/27ef250759351287fc9a6a8e084b6c5cf926e6c2))
* **audit-refactor:** phase 2 reconcile r2 — syncJobOp lint asserts coverage via llm.AllSubsystems (codex M1) ([84923b8](https://github.com/sourcebridge-ai/sourcebridge/commit/84923b8c644123a5a954c100f8f5a54b62d29642))
* **audit-refactor:** phase 2 slice 1 — SubsystemLivingWiki typed constant (LW-1) ([b07f419](https://github.com/sourcebridge-ai/sourcebridge/commit/b07f4198accfece98a98209774116c58249e809c))
* **audit-refactor:** phase 2 slice 3 — resolve plan at boot, not per-request (CFG-1) ([b81a2cb](https://github.com/sourcebridge-ai/sourcebridge/commit/b81a2cbc4d886080af1e9fd89205a2d97debc897))
* **audit-refactor:** phase 2 slice 4 — feature-flag injection (CFG-2 + CFG-3 + LW-2 + A-M5) ([0425f82](https://github.com/sourcebridge-ai/sourcebridge/commit/0425f82031169e84f24802e406556dbadff3deef))
* **audit-refactor:** phase 2 slice 5 — AppDeps shared dependency registry (STRUCT-1, librarian [#10](https://github.com/sourcebridge-ai/sourcebridge/issues/10)) ([59fcb73](https://github.com/sourcebridge-ai/sourcebridge/commit/59fcb7321dd462d6cb54b3b724faa1953859a8b5))
* **audit-refactor:** phase 2 slice 6 — typed EnterpriseDB interface (STRUCT-3 + STRUCT-4) ([7bb7ab3](https://github.com/sourcebridge-ai/sourcebridge/commit/7bb7ab31654047d8e4fc2498333144737f794d65))
* **audit-refactor:** phase 2 slice 7 — capabilities/entitlements unification + ModelForOp + syncjob lint (STRUCT-2 + CFG-4 + A-M2 + A-M6) ([88de4ea](https://github.com/sourcebridge-ai/sourcebridge/commit/88de4ea0e1d9575d8de0d0df6528fcaabdbb0658))
* **audit-refactor:** phase 3 reconcile r2 — IsGitURL guards local .git directories (codex M) ([135a9a0](https://github.com/sourcebridge-ai/sourcebridge/commit/135a9a056465de7f4888cccbc5840c131ab3b880))
* **audit-refactor:** phase 3 reconcile r2 — restore sanitizeRepoNameForQA behavior (codex H2) ([fbe431d](https://github.com/sourcebridge-ai/sourcebridge/commit/fbe431dee27417a3e73dadc9ce87b33b078341f8))
* **audit-refactor:** phase 3 reconcile r2 — route production MCP dispatch through ctx-aware wrapper (codex H1) ([718c5ba](https://github.com/sourcebridge-ai/sourcebridge/commit/718c5ba2d194d1bdb29ea032e32112bb74a68132))
* **audit-refactor:** phase 3 slice 1 — mcpTool struct unifies tool definition + handler (MCP-1) ([52a430b](https://github.com/sourcebridge-ai/sourcebridge/commit/52a430b4c64a87e8c716c6eb1018c13ebf17a4a4))
* **audit-refactor:** phase 3 slice 2 — withCtxHandler parity for ctx-bearing tools (MCP-2 + librarian [#11](https://github.com/sourcebridge-ai/sourcebridge/issues/11)) ([87ad1b1](https://github.com/sourcebridge-ai/sourcebridge/commit/87ad1b1f2baa7f69d2d4bfc173f91c53e30a7bf0))
* **audit-refactor:** phase 3 slice 3 — small MCP fixes (MCP-3 + MCP-4 + MCP-5 + MCP-6) ([ea95525](https://github.com/sourcebridge-ai/sourcebridge/commit/ea95525c0393158558203390fd0910c2d93a800d))
* **audit-refactor:** phase 3 slice 4 — shared provider resolver + provider-error classifier (PY-3 + PY-4 + librarian [#1](https://github.com/sourcebridge-ai/sourcebridge/issues/1) + [#2](https://github.com/sourcebridge-ai/sourcebridge/issues/2)) ([f7c6bef](https://github.com/sourcebridge-ai/sourcebridge/commit/f7c6bef2b5a1e1ca6b3db1f1f58edea1d28336c0))
* **audit-refactor:** phase 3 slice 5 — Python cleanups (PY-1 + PY-2 + PY-5 + PY-6) ([2797c57](https://github.com/sourcebridge-ai/sourcebridge/commit/2797c572fd15575069e7aa18965c1d5b5eaa53c2))
* **audit-refactor:** phase 3 slice 6 — shared ClassifyLLMError (GO-1 + librarian [#3](https://github.com/sourcebridge-ai/sourcebridge/issues/3)) ([09cb30d](https://github.com/sourcebridge-ai/sourcebridge/commit/09cb30d0957a7ff5df0612e67294b932f029be17))
* **audit-refactor:** phase 3 slice 7 — shared path/repo helpers (GO-2/3/4/5 + librarian [#5](https://github.com/sourcebridge-ai/sourcebridge/issues/5)/6/7/8) ([b50c087](https://github.com/sourcebridge-ai/sourcebridge/commit/b50c08718bc6cd29f95753b0898fd9d4a9fd56c3))
* **audit-refactor:** phase 3 slice 8 — AdminShell uses authFetch (librarian [#15](https://github.com/sourcebridge-ai/sourcebridge/issues/15)) ([861d848](https://github.com/sourcebridge-ai/sourcebridge/commit/861d848ac296b0a14fa7f833d46f6009e10a715c))
* **audit-refactor:** phase 4 reconcile r2 — tab state preservation + tablist keyboard nav (codex H1, L2) ([dc58dcc](https://github.com/sourcebridge-ai/sourcebridge/commit/dc58dccc3c21ef8d5b1b4aa9bcd095d17a4c700c))
* **audit-refactor:** phase 4 slice 1 — EmptyState consolidate-don't-delete (WEB-2) ([aa3ac98](https://github.com/sourcebridge-ai/sourcebridge/commit/aa3ac985c85ef8698574e1396ab0655aa12d9065))
* **audit-refactor:** phase 4 slice 2 — ConfirmDialog + useFocusTrap (WEB-3) ([a5429fb](https://github.com/sourcebridge-ai/sourcebridge/commit/a5429fb8363108044689bb83fe29f4ac4f6b6a2d))
* **audit-refactor:** phase 4 slice 3 — skeleton + EmptyState + utility extractions (WEB-4 + M-2 + parts of WEB-7) ([b0eec1c](https://github.com/sourcebridge-ai/sourcebridge/commit/b0eec1c6e023ce84d27362d8d105cac92fa264a7))
* **audit-refactor:** phase 4 slice 4 — per-operation loading + router.push + error toast (WEB-5 + WEB-6 + WEB-8) ([ed7793c](https://github.com/sourcebridge-ai/sourcebridge/commit/ed7793ce7ff33282dbec2ca062dee98697766424))
* **audit-refactor:** phase 5 reconcile r2 — public-health routing + git-helper hardening + surreal initContainer + speculative-LLM pins (codex C1, H1, H2, M1) ([89c85f3](https://github.com/sourcebridge-ai/sourcebridge/commit/89c85f3f17ef83def487f061f1064e2abe1788b2))
* **ca-107:** address codex r2 findings — start-beyond-EOF returns empty + authoritative filePath ([a497c1e](https://github.com/sourcebridge-ai/sourcebridge/commit/a497c1ed7b2b54d9839517494a4a716eceedfcb8))
* **ca-124:** pazaryna wave 1 — docs, walker, login message ([#19](https://github.com/sourcebridge-ai/sourcebridge/issues/19)) ([201548a](https://github.com/sourcebridge-ai/sourcebridge/commit/201548abc77477b55f4b113142c9101d110808e3))
* **ca-125:** pazaryna wave 2 — provider validation hardening ([#20](https://github.com/sourcebridge-ai/sourcebridge/issues/20)) ([f6f9951](https://github.com/sourcebridge-ai/sourcebridge/commit/f6f9951e3b15bd652700b2c2ed6fa79288873f8d))
* **ca-135:** canonicalize SurrealDB record-id strings on read ([#26](https://github.com/sourcebridge-ai/sourcebridge/issues/26)) ([49a3fa0](https://github.com/sourcebridge-ai/sourcebridge/commit/49a3fa08bf2d67e39404700de6a864d2a3c12058))
* **ca-136:** address codex r2 findings (OCI-label collision + Makefile freeze) ([a13f7a3](https://github.com/sourcebridge-ai/sourcebridge/commit/a13f7a3f240d8e61db0745bb5ca4ad1ee5042e60))
* **ca-136:** make CI lints green (buf STANDARD + ruff UP006) ([22a003a](https://github.com/sourcebridge-ai/sourcebridge/commit/22a003a203ec13fd6830f67d5c63ebdaaac3a665))
* **ca-136:** phase 7 — synchronize logCapture buffer to fix -race violation ([993c332](https://github.com/sourcebridge-ai/sourcebridge/commit/993c332dcc7f43c887106eaad716cad55912fc90))
* **ca-139:** address codex r2 doc findings (tag format + cosign version) ([1f4a2ae](https://github.com/sourcebridge-ai/sourcebridge/commit/1f4a2aeadb504934fbbf76955242771f2dd2ad81))
* **ca-141:** address valerie r1 findings — add 3 missing reaper tests + extract tick const ([d21d373](https://github.com/sourcebridge-ai/sourcebridge/commit/d21d3734a5edf0ca8918689d444f93a4b6c192e5))
* **ca-142:** address codex r2 findings — SIGTERM race + atomic admission + loopback enforcement + cancellation propagation ([ac5f0af](https://github.com/sourcebridge-ai/sourcebridge/commit/ac5f0af6fe31e19cba2c09f8c5dc23927d234fd8))
* **ca-142:** address validation findings — MarkDraining + settle timer + drain tests + structured logs + docs ([36ed5a4](https://github.com/sourcebridge-ai/sourcebridge/commit/36ed5a4db9a80494a9e4ca9c7b8f28485f66ded3))
* **ca-144:** address validation findings — add tracker panic test + per-job isolation test ([e543e6d](https://github.com/sourcebridge-ai/sourcebridge/commit/e543e6db95783441cebc78530104548ba9a2573f))
* **ca-145,ca-143:** phase 1 — move OnPageDone after persistence (single-goroutine + comment hygiene) ([1317cec](https://github.com/sourcebridge-ai/sourcebridge/commit/1317cec33b10210f9b886c622a0c3042fd9ae306))
* **ca-145,ca-143:** phase 2+3 — retry-resume tests + CHANGELOG ([c597468](https://github.com/sourcebridge-ai/sourcebridge/commit/c59746805e2932e103dd989a32a3637498fe557f))
* **ca-146:** address codex r2 + ian findings — wire stale handler, always-send-signature, notice rendering, integration test ([270b2ca](https://github.com/sourcebridge-ai/sourcebridge/commit/270b2ca39b448151015d4a39e48b6a41a541365a))
* **ca-150:** address codex r2 validation findings — tier classification + normalization + observability ([3145077](https://github.com/sourcebridge-ai/sourcebridge/commit/314507739dede5abe901e194c6c8c9f4e1028f55))
* **ca-151:** address codex r2 + valerie findings — DegradedBoth test, FileShrankSinceIndex test, negative-depth validation, ContextLines echo, lint comment ([81e68a8](https://github.com/sourcebridge-ai/sourcebridge/commit/81e68a812197a36a118390df663edd5e80886346))
* **ca-152:** factual_grounding tier-awareness + SystemOverview/Product local overrides ([47dff9a](https://github.com/sourcebridge-ai/sourcebridge/commit/47dff9a53f60f048c7354ecaf15a37665f735402))
* **ca-153:** reconcile r1 — codex P1 ×3 + P2 + capability drift + companion prompt tests + response shape assertions ([5d82d38](https://github.com/sourcebridge-ai/sourcebridge/commit/5d82d38291789ed7d2ed35196e72caacebe98d15))
* **ca-154:** reconcile r1 — codex P2 ×2 + valerie P2-P10 + gofmt ([7a29c71](https://github.com/sourcebridge-ai/sourcebridge/commit/7a29c719964bee9efb83ca5363faac15b095c067))
* **ca-163:** address codex r2 validation findings — operator field spelling + CA-168 references + warning-discard comment correction ([091362d](https://github.com/sourcebridge-ai/sourcebridge/commit/091362debb5646fb50a6c8a30facba4cbb7b1cea))
* **ca-164,ca-165:** address codex r2 + ian validation findings — fixture isolation, test rename, doc detail, stale comments ([266b911](https://github.com/sourcebridge-ai/sourcebridge/commit/266b9112dbba6a85d940953b136fb160e8b4559b))
* **ci:** repair red CI — buf SHA bump, lint-go cleanup, ruff fixes, reaper test flake ([ffead23](https://github.com/sourcebridge-ai/sourcebridge/commit/ffead238ae40a5c03820dcd8ed5d510525887d88))
* **ci:** unblock CI for the R3 followups delivery (lint + race) ([91cad7e](https://github.com/sourcebridge-ai/sourcebridge/commit/91cad7eeb1f2ce0156b526641230a9e4836579ee))
* **compose:** add surrealdb-init to docker-compose.hub.yml — first install fails with PermissionDenied ([b73502c](https://github.com/sourcebridge-ai/sourcebridge/commit/b73502cc52df92539a2ee13a7688887ad26cdc77))
* **compose:** chown encryption key to API user (UID 1000) before chmod 600 ([77087d8](https://github.com/sourcebridge-ai/sourcebridge/commit/77087d808d3304100d74b5347292cb9ed4364024))
* **config-source-of-truth-r3-followups:** address codex r2 BLOCK on diff ([daa1a96](https://github.com/sourcebridge-ai/sourcebridge/commit/daa1a96f60a3620b32abfddfa290ab9f930d12bf))
* **config-source-of-truth-r3-followups:** address codex r2b Medium on diff ([0c8f425](https://github.com/sourcebridge-ai/sourcebridge/commit/0c8f425d8da990d80d82d7650df6630cd528c2d9))
* **config-source-of-truth-r3:** address codex r2 BLOCK on diff ([527dd83](https://github.com/sourcebridge-ai/sourcebridge/commit/527dd83dbfe4df6b28019a1c0f543efa0b7f46e6))
* **config:** bind missing security.* fields to env via SetDefault ([f181a46](https://github.com/sourcebridge-ai/sourcebridge/commit/f181a468d820969ca959b65d9320217ebd52891a))
* **config:** blank out LLM defaults so fresh installs don't seed fake Anthropic profile ([8a7cc6d](https://github.com/sourcebridge-ai/sourcebridge/commit/8a7cc6d679453e0d73ba37bb2b5c18bd83fbfd88))
* **graphql:** move setLivingWikiModeFlags + trigger mutation into Mutation type ([5520a1c](https://github.com/sourcebridge-ai/sourcebridge/commit/5520a1c940919eb9da0ce2bb06d10d7934169332))
* **graphql:** regenerate gqlgen — missing RepositoryUnderstanding fields ([f59a65c](https://github.com/sourcebridge-ai/sourcebridge/commit/f59a65c126b92e93a4560ac24a9e7d0630f30fe9))
* **health:** drive refetch via setInterval — urql has no pollInterval ([2b064be](https://github.com/sourcebridge-ai/sourcebridge/commit/2b064be7cf741af324516060814d882ffdf71f44))
* **health:** hoist urql context to module scope to break render loop ([1831115](https://github.com/sourcebridge-ai/sourcebridge/commit/1831115733861f8cc9f1d161863384fae6081e85))
* **knowledge:** "Refresh understanding" actually rebuilds when forced ([6a04e2b](https://github.com/sourcebridge-ai/sourcebridge/commit/6a04e2bddae1828def210a00e9c1fa0aa4556b6b))
* **knowledge:** retry build_repository_understanding on transient errors ([763ecde](https://github.com/sourcebridge-ai/sourcebridge/commit/763ecded4ae580c6fe958f0da52300a17fb86eee))
* **livingwiki:** address codex r2 — render breaker, hard-abort accuracy, systemic LLM category ([7c29e63](https://github.com/sourcebridge-ai/sourcebridge/commit/7c29e633f00016a21e4f6d44a4ac654131eaa047))
* **livingwiki:** cold-start tolerates per-page LLM errors, aborts on systemic failure ([35b1fcb](https://github.com/sourcebridge-ai/sourcebridge/commit/35b1fcb573a40e3e92fc141921d74d0e57898d15))
* **livingwiki:** dedicated heartbeat write keeps long cold-starts alive ([61c904f](https://github.com/sourcebridge-ai/sourcebridge/commit/61c904fc55bfd3d6c761561296ab9b3d3661d9f3))
* **livingwiki:** JobResultStore.Save is now true upsert by job_id ([cee63f3](https://github.com/sourcebridge-ai/sourcebridge/commit/cee63f380bc7ad75d1b0e131817a294fbf0120b5))
* **livingwiki:** MaxAttempts=1 for cold-start; user-driven retries only ([876abf7](https://github.com/sourcebridge-ai/sourcebridge/commit/876abf7d42f44103fd80419dd12ab1a9a7872394))
* **livingwiki:** persistence-mid-loop failures only report durable pages ([a550a61](https://github.com/sourcebridge-ai/sourcebridge/commit/a550a61730fb3ced1898d8c3a461827d2ec2c332))
* **livingwiki:** structured failure-category surface (C2) + systemic-abort metric (C3) ([030b41c](https://github.com/sourcebridge-ai/sourcebridge/commit/030b41ca1d394fa1ec092dcb376639d30cf30bf7))
* **llm-onboarding:** mid-build reconcile — comment, runbook, base64 portability, missing test, type drift ([c8bb9c8](https://github.com/sourcebridge-ai/sourcebridge/commit/c8bb9c878d89b7a68c872255cba29491fb16e5d2))
* **llm-reaper:** heartbeat-aware queued-job threshold ([b2c5a7d](https://github.com/sourcebridge-ai/sourcebridge/commit/b2c5a7daa26d783484821c460bd8e69aa5c3c89a))
* **llm:** provider profiles — address codex r2 punch list (slice 4) ([6ecfdf3](https://github.com/sourcebridge-ai/sourcebridge/commit/6ecfdf3554744a92afd3796ed79a8721d8581b7f))
* **llm:** provider profiles — address codex r2b punch list (slice 4) ([d3bfbdc](https://github.com/sourcebridge-ai/sourcebridge/commit/d3bfbdc488f568c3f030779fefb933b62d455b0d))
* **llm:** provider profiles slice 4 — preemptive codex-r2 fixes (race + corruption guards) ([acbfa8f](https://github.com/sourcebridge-ai/sourcebridge/commit/acbfa8fc6d8dada6f49ec9ad15906fb7726e1434))
* **mcp-feedback-1c:** remove router deadlock + fix breaker cooldown semantics ([9535d31](https://github.com/sourcebridge-ai/sourcebridge/commit/9535d31d8dee7146083b489b36d1ebf8f3874808))
* **mcp-feedback-1c:** watcher correctly walks directories on macOS + fix IsIgnoredPath misuse ([d23c59e](https://github.com/sourcebridge-ai/sourcebridge/commit/d23c59e2dd3abc851a04be02720ea0b6169b9351))
* **mcp-feedback-1d:** xander-pass security hardening on 1.D ([8eca09d](https://github.com/sourcebridge-ai/sourcebridge/commit/8eca09d160d11b643a3b0c81a2b821e2558ad69f))
* **mcp-proxy:** address codex r2b — strict response validator + cancellable pre-semaphore notifications ([1a4c20e](https://github.com/sourcebridge-ai/sourcebridge/commit/1a4c20e3a1caaec4f4e7bb3cd60d03ca28b33aa4))
* **mcp-proxy:** address codex r2c — strict SSE frame validation by kind ([6c747fa](https://github.com/sourcebridge-ai/sourcebridge/commit/6c747fa9990198b8696949154934783bafd56ed0))
* **rest:** URL-decode chi path param in canonicalProfileID ([c583392](https://github.com/sourcebridge-ai/sourcebridge/commit/c58339235c389c44cfd4be9ecc08da3d53be6efd))
* **setup-claude:** emit HTTP-transport .mcp.json and migrate broken stdio entries ([141f544](https://github.com/sourcebridge-ai/sourcebridge/commit/141f544273396f355cb373c77077f233e48df3b8))
* **test:** drop flaky stdout-ordering assertion in PipelinedRequestsDispatchConcurrently ([c4eafa1](https://github.com/sourcebridge-ai/sourcebridge/commit/c4eafa1a1e1a3ea410ff8b875ffdd32988dd11f4))
* **tooling:** update snapshot-public-api.sh for post-Phase-3 mcpTool registration pattern ([7db53b6](https://github.com/sourcebridge-ai/sourcebridge/commit/7db53b62994559bf53120864273e231fe2bf530e))
* understanding panel live progress + building state + dedupe note ([b627953](https://github.com/sourcebridge-ai/sourcebridge/commit/b627953abd7a3e7c0c1640906c424758ebf81883))
* **understanding:** lock the running-vs-terminal contract end-to-end ([28951c0](https://github.com/sourcebridge-ai/sourcebridge/commit/28951c0288a5b3309f64ef92eb431f9163787060))
* **web:** don't use NEXT_PUBLIC_API_URL in middleware — it's inlined at build time ([9ba671b](https://github.com/sourcebridge-ai/sourcebridge/commit/9ba671b480aa89b61beff988c8505143eed73eef))
* **web:** FIRST_PASS_READY is not a busy stage — stop showing "Generating" forever ([73c8d48](https://github.com/sourcebridge-ai/sourcebridge/commit/73c8d48bedd8e68379695e2da7ed6d40c43a6951))
* **web:** runtime API proxy fixes baked-localhost bug in hub install ([1fee78b](https://github.com/sourcebridge-ai/sourcebridge/commit/1fee78bed38452289e00275af75feb67036cfe5b))
* **web:** select livingWikiOverviewEnabled/DetailedEnabled in repository queries ([956607e](https://github.com/sourcebridge-ai/sourcebridge/commit/956607e9269fd31330950c2ce8a6b3b374acaa74))
* **workers:** retry on timeouts in hierarchical + cliff-notes path ([2629cda](https://github.com/sourcebridge-ai/sourcebridge/commit/2629cda225f0598a39bd1cb07fdf98b1461814b1))
* **worker:** zero-disruption rollouts via probes + maxUnavailable=0 ([9a2bc8b](https://github.com/sourcebridge-ai/sourcebridge/commit/9a2bc8bc78aef7daa9a30221838db9237387eb74))
* **workspace-llm-source-of-truth-r2:** address codex r2 punch list ([166c4e6](https://github.com/sourcebridge-ai/sourcebridge/commit/166c4e628a0c28d0afc99961ee2a9c1e8a1ee4d7))
* **workspace-llm-source-of-truth-r2:** address codex r2c — boot-time CheckHealth probe under mTLS ([f4f42b2](https://github.com/sourcebridge-ai/sourcebridge/commit/f4f42b2b37cfbd7ec47e5f3f36d298fda08e9f20))
* zero understanding progress on terminal stage + unify job-progress UI ([35daaf8](https://github.com/sourcebridge-ai/sourcebridge/commit/35daaf8c7d33645b675a24e1b1bb4ce0b48f2270))


### Changed

* **audit-refactor:** phase 4 slice 6 — extract SymbolsTab + SettingsTab (WEB-1 Part B) ([c431c1c](https://github.com/sourcebridge-ai/sourcebridge/commit/c431c1c93d6d66ebd2bd6efb5239234342de433d))
* **audit-refactor:** phase 4 slice 7 — extract remaining 8 tabs (WEB-1 Part C) ([38a8bbc](https://github.com/sourcebridge-ai/sourcebridge/commit/38a8bbcfae6b3868cc698fa91eb36e53ed7b2b3a))
* **audit-refactor:** phase 4 slice 9b — GraphQL fragments multi-shape (DS-3) ([40c01ad](https://github.com/sourcebridge-ai/sourcebridge/commit/40c01ad2dbe5c2e86f0f2373626fede28f2d9534))
* **ca-130:** drop kubectl deploy from build-and-deploy.sh — OSS hygiene ([250e74d](https://github.com/sourcebridge-ai/sourcebridge/commit/250e74dbb89edc504fc7830409e7efc4e69150b3))
* **ca-146:** phase 0 — coldStartConfig struct + plan-helpers extraction + schema regen ([5246525](https://github.com/sourcebridge-ai/sourcebridge/commit/5246525884f8d21ea32eb80d4a5968d72bcc1459))
* **ca-146:** phase 0.5 — typed PageKind on PlannedPage + classifyPageType returns LivingWikiPageType (codex r1 C1, L1) ([d94f1f0](https://github.com/sourcebridge-ai/sourcebridge/commit/d94f1f0ce1df65900048f77592189e689eacd0d7))
* **ca-153:** phase 2a.1 — extract resolveDiffTouchedSymbols + fix runGitLog parser ([b4feb67](https://github.com/sourcebridge-ai/sourcebridge/commit/b4feb6725aee24cc2b0e192fe4d716ba8f0b1410))
* **mcp-feedback-1a:** extract applyImpactFromChange from ReindexRepository ([d379bb5](https://github.com/sourcebridge-ai/sourcebridge/commit/d379bb55339d02ec58b2262b1b383a6fbc4c2699))
* **mcp-feedback-1a:** extract git.IsIgnoredPath helper for change-watch reuse ([1424126](https://github.com/sourcebridge-ai/sourcebridge/commit/142412648159372cda2bebcdb6450d9daad182af))


### Documentation

* **audit-refactor:** final pass — CLAUDE.md + CHANGELOG + architecture docs + runbooks + wiki + ticket placeholders ([fb2ebba](https://github.com/sourcebridge-ai/sourcebridge/commit/fb2ebba33eec0f53ca7bb2eb56314796ec08b3a9))
* **audit-refactor:** phase 2 slice 2 — orchestrator nesting doc + tenant comment (LW-3 + LW-4) ([4596f55](https://github.com/sourcebridge-ai/sourcebridge/commit/4596f55a4a53195f1010a2c5ef247d8ca427e4f5))
* **ca-124..128:** document pazaryna waves 1-4 + CI green-up ([dd2bf75](https://github.com/sourcebridge-ai/sourcebridge/commit/dd2bf75bc8039ff697076fc7379bc36866283433))
* **ca-130:** note Docker Hub mirror + tag policy in README; drop thor leak ([e0a2ab0](https://github.com/sourcebridge-ai/sourcebridge/commit/e0a2ab0658f32565590c69aeef894a1b9c9079d6))
* **ca-136:** phase 8 — version scheme reference + CHANGELOG ([2612ff2](https://github.com/sourcebridge-ai/sourcebridge/commit/2612ff2b1368d46080e0e8b6e87760fd4ab19ad3))
* **ca-138/ca-147:** backfill commit SHAs in CHANGELOG ([8e976fd](https://github.com/sourcebridge-ai/sourcebridge/commit/8e976fda08a71238d2d98ea2709fec7ddc0b3b69))
* **ca-145,ca-143:** backfill commit SHAs in CHANGELOG entry ([f3cb488](https://github.com/sourcebridge-ai/sourcebridge/commit/f3cb4884735ee827cc506ff15c767247e92004d0))
* **ca-147:** document GHA "Allow PRs" repo gate in RELEASING.md ([06119b3](https://github.com/sourcebridge-ai/sourcebridge/commit/06119b38aa3cf7770432f111a216c8857fa0a077))
* **ca-150:** post-merge documentation pass ([32aaa53](https://github.com/sourcebridge-ai/sourcebridge/commit/32aaa53b225056da3e3728810fd006227335d4e4))
* **ca-151:** CHANGELOG + mcp-clients.md for get_symbol_source / get_symbol_context ([a7fe78b](https://github.com/sourcebridge-ai/sourcebridge/commit/a7fe78b1ec0cbcdc32907458f09e623cbc486795))
* **ca-153:** CHANGELOG + mcp-clients.md for moat-track tools (8 tools + companion prompt) ([b3fd636](https://github.com/sourcebridge-ai/sourcebridge/commit/b3fd636dc7a903af6a7c3579bb2c33fa05bb4411))
* **ca-154:** phase 4 — CHANGELOG + mcp-clients.md for parity-track tools ([6a3b178](https://github.com/sourcebridge-ai/sourcebridge/commit/6a3b17861eba91ad038bf2a617ff3638dc002f06))
* **ca-163:** phase 4 — CHANGELOG + llm-config TierMid/TierLocal factual_grounding section ([447b26d](https://github.com/sourcebridge-ai/sourcebridge/commit/447b26d565d75d3a0e40e8a7470d66d923dd5467))
* **ca-164,ca-165:** phase 4 — CHANGELOG + llm-config TierMid vagueness/citation_density section ([97ce6bf](https://github.com/sourcebridge-ai/sourcebridge/commit/97ce6bf177a41efa0e272db29b03eff4308a2313))
* **ca-60:** changelog entry for Field Guide default tab ([70965d5](https://github.com/sourcebridge-ai/sourcebridge/commit/70965d59b7006d37fc3a78d3ad2cfa484a9c6546))
* **campaign-2026-05-02:** close-out documentation pass ([428ed26](https://github.com/sourcebridge-ai/sourcebridge/commit/428ed264a88421d931d97b679c98ea23c3c8ef81))
* cloud-first quickstart, full setup-claude flag table, drop broken brew claim ([681467d](https://github.com/sourcebridge-ai/sourcebridge/commit/681467d7fc56c1398a5495a2cd534f3a85f4d5ee))
* **config-source-of-truth-r3-followups:** T1.5 + T1.9 — admin runbooks for LLM creds and encryption-key setup ([9b30d1f](https://github.com/sourcebridge-ai/sourcebridge/commit/9b30d1fdda94d2e6a02b916efd6c8e3e4bbebfa9))
* cross-link encryption-key bootstrap from README, installation, CLAUDE.md ([1e4273f](https://github.com/sourcebridge-ai/sourcebridge/commit/1e4273fada6c5920f4df845d4c5356002e6373cd))
* document runtime API proxy fix ([7c0e2a9](https://github.com/sourcebridge-ai/sourcebridge/commit/7c0e2a9a1b18bbb2a58fd1563d35712cebf19fcc))
* **mcp-feedback-1a:** add CHANGELOG entry under Unreleased ([1b49e67](https://github.com/sourcebridge-ai/sourcebridge/commit/1b49e6711d4234a4acf97f2529467a484ec1214a))
* **mcp-feedback-1b:** add CHANGELOG entry under Unreleased ([a6f657e](https://github.com/sourcebridge-ai/sourcebridge/commit/a6f657e926b542d893191515b109c7f80d427b01))
* **mcp-feedback-1c:** CHANGELOG entry for change-watch router + watcher + freshness envelope ([d4948a6](https://github.com/sourcebridge-ai/sourcebridge/commit/d4948a6b8de4c51a0c6bced3ef1526ff4938e30a))
* **mcp-feedback-1d:** CHANGELOG entry for connector ingress + record_change ([4e11e3a](https://github.com/sourcebridge-ai/sourcebridge/commit/4e11e3a9f752e2dc6e8b406f5735e4b7b7af711b))
* **mcp-feedback-1e:** Phase 1 closing summary in CHANGELOG ([a8125c1](https://github.com/sourcebridge-ai/sourcebridge/commit/a8125c118ca803bbb0aab4c3819cf5438fd7063e))
* **web:** document /api/* matcher convention in middleware ([873bc53](https://github.com/sourcebridge-ai/sourcebridge/commit/873bc536763dc3e81be1087288b8d6e3bf72859a))
* **workspace-llm-source-of-truth-r2:** slice 6 — admin guide refresh for per-repo override + opt-in mTLS ([87475fa](https://github.com/sourcebridge-ai/sourcebridge/commit/87475fa1d5c882016f7fbdb9cea0917efd7b5059))
* **workspace-llm-source-of-truth:** slice 7 — admin guide + project CLAUDE.md pointer ([b0eead4](https://github.com/sourcebridge-ai/sourcebridge/commit/b0eead4450f828c17232740340a2b4a728320306))

## [Unreleased]

**Orchestrator capacity detection** (2026-05-06, branch `fix/orchestrator-capacity-detection`). Three compounding LW throughput issues fixed end-to-end: capacity mismatch, empty-content retry tax, and `/no_think` unreliability at small `max_tokens` budgets.

- **Capacity clamping (Go):** the LW orchestrator's per-job concurrency is now clamped to `min(MaxConcurrency, upstream_capacity)`. Upstream capacity is reported by the Python worker via an extended `GetProviderCapabilities` RPC response (`max_concurrent_calls` + `max_concurrent_calls_known` fields). Proto change is wire-compatible (proto3 zero-defaults give fail-open behavior on old workers). Logs once per job: `livingwiki/orchestrator: clamping MaxConcurrency to upstream capacity`.
- **Breaker recalibration (C1):** soft-failure breaker window and threshold are now computed from the *clamped* effective concurrency, not the unconfigured `MaxConcurrency`. Prevents false trips when `effective=1` (serialized Ollama path).
- **Per-profile `max_concurrent_calls` field (DB + REST):** new optional `INT` column on `ca_llm_profile` (`NULL` = unknown; `0` = unbounded; `1..256` = clamp). DB-level `ASSERT` + REST-layer validation enforce the `[0, 256]` range. `SOURCEBRIDGE_LLM_PARALLEL_HINT` env var seeds the Default profile column on first boot (seed-once guard: never overwrites a non-NULL value).
- **gRPC auth interceptor (D10):** `internal/worker/client.go` now attaches `x-sb-worker-secret` metadata when `SOURCEBRIDGE_WORKER_GRPC_AUTH_SECRET` is set; Python worker verifies it. Startup ERROR log when worker is bound to a non-loopback address without auth.
- **Empty-content retry telemetry (Phase 3):** `llm_empty_content_retry` log now includes `attempt`, `strategy`, and `original_latency_ms` fields. Total attempt budget is a shared counter capped at 3.
- **Ollama thinking suppression (Phase 4 replacement):** empirical sweep on Mac Studio with qwen3.5:9b + qwen3.6:27b proved that ALL prompt-injection strategies (strategy f and predecessors) are silently ignored by Ollama's OpenAI-compat shim. `OpenAICompatProvider` now branches to the native `/api/chat` endpoint with `think: false` when `provider_name == "ollama"` and `disable_thinking` is True. Non-Ollama providers (llama.cpp, vLLM, etc.) are unaffected and continue using `chat_template_kwargs`. See `thoughts/shared/investigations/2026-05-06-ollama-think-suppression-empirical.md`.
- **Capacity provider wiring fix (ian mid-build):** `AppDeps.UpstreamCapacityProvider` was populated in `router.go` but never passed into `coldstart.Config{}` in `repository_living_wiki.resolvers.go`, making Phase 2's capacity clamp a no-op in every production cold-start. Fixed in commit `e1fe4b1`; nil-safe end-to-end (tests construct `Resolver` without `Deps`; orchestrator clamp site also nil-guards).
- **Docs (`docs/admin/llm-config.md`):** new "Backend parallelism and the `max_concurrent_calls` field" section covering recommended values by provider, Ollama tuning env vars, breaker recalibration, multi-replica caveat, and gRPC auth setup.

**Fresh-install LLM onboarding** (2026-05-05). Fixes three compounding root causes that bricked first-time setup on hub-compose installs. Users can now reach a working LLM configuration in under 60 seconds without editing YAML or hitting opaque 422 errors.

### Changed

- **`internal/config/config.go`**: `LLM.Provider`, `SummaryModel`, `ReviewModel`, `AskModel` defaults are now empty strings instead of `"anthropic"` / `"claude-sonnet-4-20250514"`. Fresh installs seed a blank Default profile; the admin UI pre-fills "ollama" as a sensible starting point. **Upgrade impact: none** — the migration's fast-exit path protects all existing installs with a seeded profile. Operators who relied on the implicit Anthropic default without setting `SOURCEBRIDGE_LLM_PROVIDER` should set it explicitly: `SOURCEBRIDGE_LLM_PROVIDER=anthropic` (in `config.toml`, `.env`, or `docker-compose.yml`).

### Fixed

- **`internal/db/llm_config_migration.go`**: Self-heal guard (r1 H1) — when the legacy row has a real provider but the deterministic profile row is missing, the migration now prefers the legacy row's values over the (now-empty) env-bootstrap defaults. Prevents a partially-corrupt admin's recovery path from being silently zeroed.

### Added

- **Encryption key bootstrap** (`docker-compose.hub.yml`, `docker-compose.yml`): New `encryption-key-init` service generates a unique per-install encryption key on first boot and persists it in the `sourcebridge-secrets` named volume. The API container reads it via `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE` (file wins over literal env per `_FILE` convention — matches Vault / Postgres). **`down -v` deletes the volume and all encrypted secrets; back up `sourcebridge-secrets` before restores.** New volume: `sourcebridge-secrets`.

- **`SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY_FILE` env var** (`internal/config/config.go`): New `SecurityConfig.ResolveEncryptionKey()` helper implements r1 H2 resolution order (`_FILE > literal env > empty`) and r1 H4 minimum-entropy guardrail (logs ERROR for keys < 32 bytes, does not crash). Called once at the top of `runServe` before any cipher construction; result is written back into `cfg.Security.EncryptionKey` so all five downstream consumers (gitCipher, llmCipher, lwStore, lwRepoStore, empty-key check) see the resolved value. **Precedence change for existing operators with both `_FILE` and the literal env set: file now wins.** This is intentional and documented in the runbook.

- **`encryption_key_set` flag on `GET /api/v1/admin/llm-profiles`** (`internal/api/rest/llm_profiles.go`): New boolean field in the list response. `true` when the API booted with a resolved encryption key. Web UI uses this to show the correct onboarding state. Existing API consumers should default to `true` on older replicas during rolling deploys.

- **`setup.sh`**: Now generates `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` in `.env` using `openssl rand -hex 32` (macOS-portable). Preserves existing value on re-run.

- **Helm chart** (`deploy/helm/sourcebridge/templates/secrets.yaml`, `api-deployment.yaml`, `values.yaml`): Auto-generates an `encryption-key` secret on first install (`randAlphaNum 32`). `helm.sh/resource-policy: keep` ensures `helm upgrade` does NOT regenerate the key. Override with `secrets.encryptionKey`.

- **Runbook** (`docs/admin/llm-config.md`): Added `##-Encryption-key-bootstrap` section covering resolution order, expected format, Docker Compose paths, Helm chart, and "Wipe-and-re-enter (destructive cleanup)" (r1 M5 — renamed from "rotation" to avoid implying key-safe re-encryption).



**System audit refactor campaign** (CA-155, 2026-05-04, `a176b6f..89c85f3`). Full-codebase audit — 70 deduplicated findings (9 Critical, 23 High, 30 Medium, 18 Low), 74 commits across 5 phases, no public surface removed.

### Security (Phase 0)

- **Fixed**: CSRF protection verified on by default (`CSRFEnabled: true` default confirmed, test added) ([CA-155](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/797d0038-6493-49dc-8307-d7c54d3f6611/))
- **Fixed**: OIDC role allowlist enforced — provider claims no longer set admin role without explicit configuration
- **Fixed**: API token role field enforced — tokens with no role now default to `viewer`; migration writes `role='admin'` for all pre-existing tokens to preserve behavior
- **Fixed**: User-scoped token routes (`/api/v1/tokens`) correctly separated from the `RequireRole(admin)` gate; non-admin users can manage their own tokens
- **Fixed**: Bootstrap admin credential handling hardened; removal-guard CI script added to prevent accidental deletion of public surfaces
- Commits: `a176b6f..8db8daa` (14 commits)

### GraphQL refactor (Phase 1)

- **Added**: `LLMUsage.Provider` field to `proto/common/v1/types.proto`; all 9+ Go call sites and the Python worker updated to populate it
- **Refactored**: `schema.resolvers.go` reduced from 4857 to ~4317 lines via per-type delegation for `RefreshKnowledgeArtifact` (Cliff Notes, Architecture Diagram, Learning Path, Code Tour, Workflow Story each get a typed service)
- **Fixed**: Gen-1 enterprise DB typing cleaned up
- Commits: `8db8daa..4b279c7` (16 commits)

### Subsystem registration and Living Wiki typing (Phase 2)

- **Added**: `internal/appdeps` package — `AppDeps` struct is now the canonical single-construction dependency registry shared by `rest.Server` and `graphql.Resolver`; new subsystems register fields here (see CLAUDE.md "Subsystem registration")
- **Added**: `capabilities`/`entitlements` mapping helper and sync-job-op lint; both packages remain (no-removal rule)
- **Refactored**: Living Wiki types fully typed; `OpGroup` typed; `EnterpriseDB` typed
- Commits: `4b279c7..5ebf943` (11 commits)

### MCP tool refactor and Python servicer dedup (Phase 3)

- **Refactored**: `mcpTool` struct introduced — definition and dispatch handler are now always registered together via `registerTool`; eliminates the prior pattern where `baseTools()` list and dispatch map could drift independently
- **Added**: `withCtxHandler` adapter for context-bearing MCP tools; mirrors `noCtxHandler`
- **Refactored**: Python worker servicer duplication eliminated; `ClassifyLLMError` extracted as canonical helper
- **Added**: `pathutil` package; `AdminShell` `authFetch` helper
- Commits: `5ebf943..135a9a0` (12 commits)

### Web UI refactor (Phase 4)

- **Refactored**: `EmptyState` components consolidated; `web/src/components/empty-states/*` retained as compatibility adapters re-exporting from `@/components/ui/empty-state`
- **Refactored**: `RepositoryDetailPage` (`page.tsx`) reduced from 3836 to ~590 lines (~85%) by extracting 11 tab components
- **Added**: Design tokens for confidence and danger states
- **Refactored**: GraphQL fragments extracted (multi-shape pattern); avoids over-fetching
- **Added**: WAI-ARIA roles for tabs, toast, and account menu; keyboard navigation
- **Renamed**: CSS class prefix `ca-*` → `sb-*` (internal names only; no public surface removed); census at `docs/codeaware-legacy-census.md`
- Commits: `135a9a0..dc58dcc` (13 commits)

### Infrastructure hardening (Phase 5)

- **Fixed**: `docker-compose.hub.yml` no longer ships working default credentials; sentinel values (`INSECURE-DEFAULT-CHANGE-ME-NOW`) trigger a repeating boot warning until replaced. Run `scripts/init-hub-secrets.sh` once before starting the stack
- **Added**: Container liveness/readiness probes for all services; `/api/health` public endpoint (Next.js); `grpc_health_probe` for worker
- **Fixed**: All container images now run as non-root (`USER` directives); per-workload `ServiceAccount` resources with `automountServiceAccountToken: false`; `SOURCEBRIDGE_WORKER_TLS_ENABLED` flag for mTLS in production overlay
- **Fixed**: `/metrics`, `/healthz`, `/readyz` removed from Ingress exposure; `ServiceMonitor` added for Prometheus scrape
- **Fixed**: All GitHub Actions pinned to 40-character commit SHAs; `.github/dependabot.yml` added for github-actions ecosystem
- **Fixed**: CSP `unsafe-eval` removed from API server security headers; gRPC reflection gated behind `WorkerConfig.Debug`; git credential helper shell-quote vulnerability fixed
- Follow-up tickets: [CA-156](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/f71f2a1d-cee6-4551-9dd4-b3cf0b58f79f/) (CSP CI enforcement), [CA-157](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/fc3a831e-17b5-492f-bd1c-c44be1cd3342/) (OSS multi-tenant design), [CA-158](https://plane.xmojo.net/agile-solutions-group/projects/d3fa4bd8-1177-4364-88a7-aae69698b75d/issues/8d2842cb-24bb-4e7c-9a91-71ffe6b5168c/) (EnableLivingWikiForRepo RMW race)
- Commits: `dc58dcc..89c85f3` (8 commits)

**Living Wiki cold-start reliability campaign** (2026-05-02 outage remediation). On
2026-05-02 a Living Wiki cold-start on a default Ollama install produced 12 pages that
were all rejected by quality gates, with no per-page progress signal, broken retry-resume,
and a pod restart that killed the job mid-flight. Seven tickets fixed the structural causes:
CA-150 (tier-aware quality gates so local models pass), CA-145 + CA-143 (progress counter
and retry-resume now agree on durable state), CA-142 (graceful drain protects in-flight
jobs from Argo Image Updater rollouts), CA-141 (heartbeat-stale reaper detects stuck jobs
in ~5 min instead of ~32 min), CA-144 (per-page in-flight visibility in admin Monitor),
CA-146 (page-count transparency and per-run override).

### Added

- **Six parity-track MCP tools: dead code, untested symbols, changed symbols,
  package importers, and multi-hop blast radius** (CA-154).

  *Gap-audit extensions (extends existing `gap_audit` capability, OSS + Enterprise):*

  - **`find_dead_code`** — cursor-paginated list of symbols with no callers in
    the call graph (dead-code candidates). Exported / public-named symbols are
    excluded by default (`exclude_entry_points: true`) via the
    `isLikelyPublicSymbol` name heuristic. Scan capped at 10,000 symbols per
    page (`scan_truncated: true` when hit). Supports `kinds[]` filter and
    `min_callers` threshold.
  - **`get_untested_symbols`** — cursor-paginated list of symbols with no test
    linkage (no persisted test edge and no adjacent-test-file heuristic match).
    Same 10,000-symbol scan cap. Supports `kinds[]` filter and
    `exclude_entry_points` toggle.

  *Diff-anchored symbol enumeration (extends existing `change_impact` capability):*

  - **`get_changed_symbols`** — given a diff scope (`commit_range` and/or
    `files`), returns every code symbol touched by the diff in two projections:
    `changed_files` (symbols grouped by file) and `changed_symbols` (flat
    deduped list). Hydration via `GetSymbolsByIDs` (not name-based lookup) per
    dexter M4. Global `max_symbols` cap with `truncated: true` flag. Does NOT
    distinguish added/modified/removed (`change_type` deferred — symbol-level
    diff fingerprints are not yet stored).

  *Package dependency queries (new `code_dependencies` capability, OSS + Enterprise):*

  - **`find_importers`** — returns the packages that import the package
    containing a given file. Resolves `file_path` to its directory and looks up
    the pre-computed `StoredPackageDependencies` record. Supports Go module-
    qualified import paths via suffix-match. Cursor-paginated (default 50,
    cap 200). Three discriminated `_meta.reason` values:
    `"package_dependencies_not_computed"` when no deps have been computed for
    the repo; `"package_has_no_recorded_importers"` when deps are present but
    no record matches the requested package; no reason when a match is found
    (even with an empty importers list). For runtime call relationships, use
    `get_callers` instead. Each importer entry is a raw import-path string as
    it appears in the importer's source.

  *Multi-hop blast radius (extends existing `change_impact` capability):*

  - **`get_blast_radius`** — BFS over the caller graph up to `depth` hops
    (default 3, max 5; inputs > 5 are clamped to 5, not rejected). Returns
    `impact_by_depth` (per-layer callers, test matches, requirements, and risk
    score), top-level `affected_requirements` and `affected_tests` (deduped
    unions across all layers), and an `overall_risk_score` (weighted by
    1/depth^0.7). Cross-repo isolation: callers not in the repo's symbol set
    are filtered at frontier expansion (per bob C2). Cap: 500 nodes; cap check
    fires at hop boundary after full frontier expansion so shallow nodes are
    never evicted (per bob H3). New `include_test_callers` parameter (default
    `false`) excludes `IsTest` symbols from `impact_by_depth` callers.

  *Internal changes shipped as part of this campaign:*

  - **`registerGapAuditExtraTools`, `registerChangedSymbolsTools`,
    `registerDependenciesTools`, `registerBlastRadiusTools`** — four new
    registration blocks wired into `newMCPHandlerWithEdition` after the CA-153
    blocks.
  - **`capability_registry`** — `change_impact` description updated to mention
    diff-anchored symbol enumeration and multi-hop blast radius; `gap_audit`
    description extended with dead-code and test-coverage gap tools;
    `code_dependencies` capability added for `find_importers`.
  - **`TestMCP_ToolsList`** — updated to expect the six new tool names.

- **Eight moat-track MCP tools: requirement traceability, gap audit, field
  guide, change-impact prediction, and AI diff review** (CA-153).

  *Requirement-linking tools (new `requirement_linking` capability, OSS + Enterprise):*

  - **`get_requirements_for_symbol`** — given a symbol, return the requirements
    linked to it. Wraps `GetLinksForSymbol` + `GetRequirementsByIDs`.
  - **`get_symbols_for_requirement`** — given a requirement, return the symbols
    linked to it. Wraps `GetLinksForRequirement` + `GetSymbolsByIDs`.
  - **`get_changed_requirements`** — given a diff scope (file-anchored or
    commit-range-anchored), return the requirements affected. Lives in
    `mcp_requirement_tools.go` alongside the linking tools.

  *Gap-audit tools (new `gap_audit` capability, OSS + Enterprise):*

  - **`get_orphan_symbols`** — symbols with no linked requirement. Cursor-paginated.
    Note: each page performs a full repo scan; the cursor slices the output, it
    does not reduce the per-page scan cost.
  - **`get_uncovered_requirements`** — requirements with no linked symbol.
    Cursor-paginated with the same full-scan per page. A hard scan cap
    (`maxUncoveredReqScan = 10000`) prevents runaway queries; the response
    includes `scan_truncated: true` if the cap was hit.

  *Field guide (new `field_guides` capability, OSS + Enterprise):*

  - **`get_field_guide`** — fetch `cliff_notes`, `learning_path`, `code_tour`,
    or `workflow_story` for a path or symbol scope. The `format` enum routes to
    one of four artifact types, all pre-seeded by `seedRepositoryFieldGuide`
    at index time. Read-only: if an artifact has not been generated yet, the
    tool returns the same "not generated yet" empty-payload response as
    `get_cliff_notes`. Generation is triggered via the SourceBridge web UI.

  *Change-impact prediction (extends existing `change_impact` capability):*

  - **`predict_change_impact`** — bundled response with symbol blast radius,
    affected requirements, and affected tests (paths + names) for a proposed
    change (file-anchored, commit-range-anchored, or symbol-anchored).
    Depth is capped at 1 for this release; `depth > 1` is rejected. The
    test-symbol set is pre-resolved once per call (O(n+k) rather than O(n×k)).
    The existing `impact_summary` tool remains functional (coexistence per D1).

  *AI diff review (extends existing `compound_workflows` capability):*

  - **`get_review_for_diff`** — extends `review_diff_against_requirements` with
    an optional AI review pipeline. `include_ai_review: false` (default) returns
    the structural review only. `include_ai_review: true` invokes the LLM
    worker's `ReviewFile` per touched file × template under a 90-second context
    deadline. The AI path requires the `workerReviewCaller` interface and
    `IsAvailable()` to return true; when the worker is nil, does not implement
    the interface, or reports unavailable, the tool degrades gracefully
    (`degraded: true`, `degraded_reason: "..."`) rather than erroring.
    The existing `review_diff_against_requirements` tool remains functional
    (coexistence per D1).
  - **Companion prompt `review_diff_with_sourcebridge`** — encodes the
    multi-step workflow for agents using `get_review_for_diff`. Includes a
    latency warning (30–90 seconds for the AI path).

  *Internal changes shipped as part of this campaign:*

  - **`mcpToolHandlerFunc` dispatcher** — the 26-case `tools/call` switch
    (`mcp.go`) is replaced by a `map[string]mcpToolHandlerFunc` with a
    `noCtxHandler` adapter for non-ctx tools and a `(*mcpHandler).registerTool`
    helper that panics on duplicate registration.
  - **`OpMCPReview` op constant** added to `KnownOps`; `deriveOperationGroup`
    routes it to `GroupReview`.
  - **`workerReviewCaller` interface** — additive sibling to `mcpWorkerCaller`;
    `mcpLLMCallerAdapter` satisfies both via a `reviewOp` field plumbed through
    `router.go`.
  - **`TestDispatchMapCoversBaseTools`** — catches entries missing from the
    dispatch map (inverse of CA-151's `TestRegistry_AllMCPToolsExistInBaseTools`).
  - **`runGitLog` parser fix (`mcp_accessors.go`)** — the `\x1e` record separator
    was moved from the end of the `--pretty=format` string to the beginning.
    Before this fix, `commit_range`-anchored compound tools returned empty
    `FilesTouched` because file-name lines from each commit landed in the next
    commit's header parser. Fixed in Phase 2a.1 (commit `b4feb67`).

- **`get_symbol_source` and `get_symbol_context` MCP tools** (CA-151).
  Two new tools close the 2x token tax that agents paid following up
  `search_symbols` with a separate `Read`. `get_symbol_source` returns a
  symbol's source bytes plus name/qualified_name/kind/signature/language/line
  range in a flat JSON shape (`context_lines` clamped `[0, 10]`; `source_note`
  warning emitted when source exceeds 500 lines). `get_symbol_context` bundles
  that with first-hop callers, callees (capped at 20 each), and the file's
  imports (capped at 50) so an agent can ground a single question in one
  round-trip. Both tools are gated by a new `symbol_source` capability (OSS +
  Enterprise) and accept a `symbol_id` fast path. When the `call_graph` or
  `file_imports` capability is disabled, `get_symbol_context` returns the
  symbol payload with the affected arrays empty and `degraded: true` plus a
  `degraded_reason` field (reasons joined with `"; "` when both are off) rather
  than erroring out. Errors flow through the existing structured envelope; a new
  `REPOSITORY_STALE` constructor surfaces the file-deleted-since-index case with
  a `refresh_repository` remediation hint. Internal cleanup: the three
  near-duplicate line-slicing helpers in REST, QA, and GraphQL are consolidated
  into a new `internal/source/` package exporting `SliceLines(content, start,
  end)` with 1-based inclusive semantics; a new
  `TestRegistry_AllMCPToolsExistInBaseTools` test catches drift between the
  capability registry and the live tool surface. **Behavior change:** the GraphQL
  `symbolContext` internal helper (`extractSymbolContext`) now returns `""` on
  non-positive `startLine` instead of clamping to line 1. Callers passing
  `startLine >= 1` (the only valid range; both call sites at
  `schema.resolvers.go:616, 1033` use the indexer-provided value which is always
  `>= 1`) are unaffected.

- **Graceful drain for in-flight Living Wiki cold-starts** (CA-142).
  SIGTERM no longer races with the reaper to kill long-running Living Wiki
  jobs. `BeginDrain` now sets `o.draining = true` on the LLM orchestrator so
  `reapStaleJobs` skips `StatusGenerating` jobs for the full drain window (up
  to `SOURCEBRIDGE_SHUTDOWN_GRACE_SECONDS`, default 3600s). A public
  `MarkDraining(bool)` method is exposed for testing. A 5 s K8s endpoint
  propagation settle delay is inserted between `BeginDrain` and `AwaitDrain`.
  Seven structured log events added (`server_drain_handler_armed`,
  `drain_await_begin`, `drain_progress`, `drain_workers_wait_complete`,
  `http_shutdown_complete`, `received_sigterm`, `reaper_skipped_during_drain`).
  Worker Helm template gains a `startupProbe` matching the kustomize base.
  See `docs/admin/api-drain.md` for operator runbook.

- **Per-run page count override for Living Wiki cold-starts** (CA-146).
  New optional `pageCountOverride: Int` field on `enableLivingWikiForRepo`
  and `retryLivingWikiJob` lets operators cap the number of pages generated
  for a single run without changing the persisted `MaxPagesPerJob` setting.
  Valid range 1–500. The UI surfaces this as a collapsible "Run options"
  section inline with the Build Overview / Build Detailed buttons. The cap
  is skipped on targeted-retry paths (`retryExcludedOnly: true`) so
  explicitly-named pages are never silently discarded. A structured planning
  log line (`livingwiki/coldstart: planned page count`) is now emitted at
  the start of every run stating the page breakdown and applied cap.

- **Auto-version-bump-on-merge via release-please** (`e02fd92`, CA-147).
  Adopted [release-please](https://github.com/googleapis/release-please) to
  automate version bumping and changelog generation. Conventional-commit PRs
  to main now drive an always-current Release PR; merging it cuts the next
  tag, which fires `oss-release.yml` (when `RELEASE_PLEASE_TOKEN` is
  provisioned) for the existing binary build / Docker image / cosign /
  Homebrew tap pipeline. Manifest-MINIMAL: tracks only the canonical release
  surfaces — git tags (root, `release-type: go`) and
  `plugins/vscode/package.json` (separate VSCode-marketplace cadence,
  tagged as `sourcebridge-vscode-vX.Y.Z`). The web bundle, Python worker,
  and example app are decorative — explicitly NOT bumped to avoid churn.
  CHANGELOG storytelling preserved as much as possible via
  `changelog-sections` config (Keep-a-Changelog mapping). `oss-release.yml`
  patched to set `generate_release_notes: false` so release-please owns
  release-note text. Both tag-triggered workflows tightened to SemVer-shaped
  globs to avoid plugin-tag collisions. RELEASING.md fully rewritten;
  CONTRIBUTING.md gains a "Commit messages" section explaining conventional
  commits. Token provisioning (GH App preferred) tracked in CA-149 — until
  that lands, every release-please-merged tag requires a manual re-push to
  fire downstream workflows. RELEASING.md documents the manual unblock.

- **Per-page in-flight visibility for Living Wiki cold-starts** (CA-144).
  Operators can now see which pages are mid-generation in the admin Monitor
  when a Living Wiki cold-start is running. New REST endpoint
  `GET /api/v1/admin/llm/jobs/{id}/livingwiki/in-flight` returns sorted
  in-flight pages with elapsed time and a stuck-page warn-dot (yellow dot
  when elapsed exceeds 3× the run's median, or 300s flat before 3
  completions). Structured `slog` events (`page generation started`,
  `page generation retrying`, `page generation finished`) carry `job_id`,
  `page_id`, `template_id`, and `duration_ms` for headless operators using
  log aggregation. The in-flight panel mounts under the expanded job row in
  the Monitor UI for all running `living_wiki` jobs, polled every 2s.

- **Plan preview modal for Living Wiki cold-starts** (CA-146). Before clicking
  Build (Overview, Detailed, Regenerate, or Retry), operators now see the exact
  page set that would be generated — grouped into Repository pages (always
  included), Subsystem pages (one per cluster, Detailed mode), and Package pages
  (top-level-dir fallback). Non-required pages can be deselected; repository
  pages are locked with an "Always included" badge. A mode pill with a tooltip
  explains the generation scope. When the plan changes between preview and Build
  (cluster re-index, `MaxPagesPerJob` change in another tab), the modal stays
  open and shows an inline warning banner with the fresh plan; existing
  deselections are preserved where IDs still exist and the Build button
  re-disables until the user re-confirms. The "Build anyway (plan unavailable)"
  fallback only appears when the preview query itself fails. Backend exposes a
  new `previewLivingWikiPlan` query and threads `selectedPageIds` +
  `planSignature` through `enableLivingWikiForRepo` and `retryLivingWikiJob`.
  See `docs/admin/living-wiki-ops.md` for the operator runbook.

- **GraphQL `VersionInfo` reaches API parity with REST `/api/v1/version`**
  (`aeb92d8`, CA-138). The `VersionInfo` type now exposes all 7 fields
  the REST endpoint reports: `version`, `commit`, `buildDate`, `goVersion`,
  `edition`, `buildEdition`, `workerVersion`. Identical cached worker-version
  lookup is shared between REST and GraphQL via a new `Resolver.WorkerVersion`
  function-shaped DI wired from `rest.NewServer`. A new parity test in
  `internal/api/rest/graphql_version_parity_test.go` asserts both surfaces
  return field-for-field identical responses for every config shape.

### Changed

- **Tool descriptions for `review_diff_against_requirements`, `impact_summary`,
  and `onboard_new_contributor` updated with legacy markers** (CA-153). Each
  tool description now appends `(legacy — use get_review_for_diff)`,
  `(legacy — use predict_change_impact)`, and `(legacy — use get_field_guide)`
  respectively, so agent discovery surfaces the moat-track successors. All
  three legacy tools remain fully functional; this is a coexistence pattern
  (D1), not a deprecation.

- **`runGitLog` parser fix: `\x1e` record separator moved to start of
  `--pretty=format` string** (CA-153, `mcp_accessors.go`, commit `b4feb67`).
  Before this fix, commit-range-anchored compound tools (`review_diff_against_requirements`
  and its successors) returned empty `FilesTouched` because the trailing
  separator caused file-name lines from each commit to land in the next commit's
  header parser. Fixed: separator now leads each record so each commit's files
  parse under the correct header. Callers that depended on the prior empty-files
  behavior should not exist — the prior behavior was a bug.

- **Repository detail page now opens on the Field Guide tab by default** (CA-60).
  The fallback tab for `/repositories/[id]` changed from `files` to `knowledge`
  (Field Guide). Direct links using `?tab=knowledge` continue to work unchanged.
  When no Field Guide artifacts exist yet, the tab renders a prominent
  "Generate Field Guide" CTA in place of the empty state. All other tabs are
  unaffected.

- **`MaxPagesPerJob` default raised from 50 to 500, and is now wired as a real
  cap** (CA-146). Previously, `MaxPagesPerJob` was stored in the database and
  surfaced in the Settings UI but was never applied to cold-start jobs —
  effectively a placebo. It is now wired into `buildColdStartRunner` and
  applied after taxonomy resolution on every cold-start. The default is raised
  from 50 to 500 so that existing repos with the never-touched default do not
  experience silent truncation after upgrading. Repos whose `MaxPagesPerJob`
  was exactly 50 (the prior default) are automatically migrated to 500 by
  migration `055_lw_max_pages_default_500.surql`. Operators who deliberately
  set a value other than 50 retain their custom value. The planning log line
  now records `cap_source=repo_setting` when this cap fires.

### Fixed

- **Living Wiki no longer rejects all pages on mid-tier models** (Gemini Flash family,
  gpt-4o-mini, o3-mini, ≥70B open-weights via OpenRouter). The `factual_grounding`
  quality gate is now a warning at TierMid for `architecture`, `api_reference`,
  `adr`, `glossary`, and additionally a warning at TierLocal for `architecture`
  and `adr` (extending CA-152's coverage). Closes a structural failure where
  every paragraph with a behavioral assertion failed the gate without the
  `(path:N-N)` citation format that mid/local-tier models do not reliably emit.
  Affected pages now ship; warnings are not yet surfaced in the PR description
  (tracked as CA-168). See CA-163 and the investigation at
  `thoughts/shared/investigations/2026-05-05-living-wiki-broken-on-openrouter.md`.

- Living Wiki ships substantially more pages on mid-tier models — `vagueness`
  is now a warning at TierMid for `architecture/engineers`, `api_reference/engineers`,
  `adr/engineers`, `system_overview/engineers`, and `system_overview/product`,
  mirroring the CA-152 TierLocal pattern. `citation_density` is now a warning
  at TierMid for `architecture/engineers` (the threshold of 1 citation per 300
  words from CA-150 is preserved unchanged — only the level changes). Closes
  the post-CA-163-deploy gap on Gemini Flash where 11/12 pages were excluded
  with vague-quantifier and stub-paragraph citation violations after
  `factual_grounding` had been demoted in CA-163. Confirmed by post-CA-163-deploy
  job `50583123-70fe-49bb-a2d5-b62eaa09f8d4` on Gemini Flash showing 11/12 pages
  excluded with vague-quantifier and stub-paragraph violations after factual_grounding
  had been demoted in CA-163. Successful pages with vagueness or citation_density
  warnings still ship without the warning text surfaced in the PR description
  (tracked as a follow-up — see CA-168 follow-up scope). CA-164 + CA-165 — see
  `thoughts/shared/investigations/2026-05-05-living-wiki-broken-on-openrouter.md`
  and `thoughts/shared/plans/2026-05-05-deliver-tiermid-vagueness-and-citation-density.md`.

- **discuss / Q&A**: When `discussCode` is anchored on a symbol, the LLM prompt now
  includes the symbol's actual implementation source sliced from the file by line range,
  instead of relying on metadata plus a whole-file dump. Token cost on long files is
  meaningfully reduced and answer quality on symbol-scoped discuss threads improves.
  (CA-107)

- **`OnPageDone` now fires after persistence** (`1317cec`+`c597468`, CA-145, CA-143). Progress
  counter and smart-resume now agree on which pages are durably stored.
  Previously, `OnPageDone` fired before the post-Wait persistence loop, so
  the in-memory completion set could exceed the persisted set; on
  interruption and retry, smart-resume saw a smaller persisted set than
  progress had reported, leading to apparent regressions and
  double-generation of pages the user saw counted. Phase 1 (`1317cec`)
  moved the callsite; Phase 2 adds three orchestrator tests
  (`TestRetryResume_ProgressMatchesPersistedSet_PR`,
  `TestRetryResume_ProgressMatchesPersistedSet_DirectPublish`,
  `TestRetryResume_NoProgressOnHardError`) and one cold-start runner test
  (`TestRetryResume_SmartResumeMatchesProgress`) locking the contract.

- **Stale-job reaper detects stuck Living Wiki jobs in ~5 min instead of ~32 min**
  (`1b61436`+`d21d373`, CA-141). The reaper tick is dropped from 60 s to 15 s, and a
  heartbeat-stale threshold (5 min since last heartbeat) is applied specifically to
  `living_wiki` jobs, which already emit a heartbeat every 30 s. Previously, the
  reaper used a flat 30-minute wall-clock threshold and a 60-second tick, so a wedged
  cold-start was not reaped until `30 min + ≤60 s ≈ 32 min` after it stalled. Healthy
  long-running jobs are unaffected: they heartbeat continuously, so the 5-minute
  heartbeat-stale window never fires on a running job. Other subsystems that do not
  emit heartbeats retain the existing 30-minute threshold. Composed with CA-142's drain
  guard: the faster reaper does not race with graceful shutdown because CA-142's
  `o.draining` check fires first.

- **Quality gates no longer reject all local-LLM Living Wiki output**
  (`c0a7bbb`–`41c0d5a`, reconcile `3145077`, CA-150). Living Wiki generation
  now calibrates quality-gate thresholds against the LLM's capability tier
  (`frontier` / `mid` / `local`). Local models served via Ollama or other
  open-weight providers previously hit frontier citation-density and vagueness
  gates they structurally cannot satisfy, producing 100% exclusion. The tier
  is set per-model in Admin → Comprehension → Model Registry
  (`/admin/comprehension/models`) or falls back to pattern-matching on the
  provider/model name. Pattern-match thresholds: open-weights ≥70B →
  `TierMid`; <70B → `TierLocal` (the default OSS install, `qwen3:32b`, is
  32B and therefore `TierLocal`; the reconcile commit `3145077` lowered the
  mid/local boundary from 30B to 70B to ensure the default install cannot
  re-hit the outage). A transient registry error falls back to `TierLocal`
  rather than reproducing the outage. Also fixes the `wordsPerCitation`
  message-clarity bug: zero-citation pages no longer report a nonsensical
  "1 per ~N words" ratio in the violation message. See
  [`docs/admin/llm-config.md`](docs/admin/llm-config.md#capability-tiers-and-quality-gates)
  for the operator runbook.

- **Living Wiki mode-override flag plumbing in `EnableLivingWikiForRepo`**
  (`aeb92d8`, CA-138). The resolver previously built a fresh
  `RepositoryLivingWikiSettings` from scratch on every call, which (a)
  ignored the input's `LivingWikiOverviewEnabled` / `LivingWikiDetailedEnabled`
  override pointers AND (b) wiped any persisted mode flags from prior
  `setLivingWikiModeFlags` calls. Effect:
  `triggerLivingWikiColdStart(mode: OVERVIEW)` and
  `triggerLivingWikiColdStartAllEnabled` did not actually drive distinct
  cold-start jobs by mode, and every UI save reset the persisted mode flags
  to zero. The fix:
  - Load existing settings; persist a merged record that preserves all
    pre-existing fields the input doesn't explicitly overwrite.
  - Treat `LivingWikiOverviewEnabled` / `LivingWikiDetailedEnabled` as
    TRANSIENT — they drive per-job mode derivation only, never the
    persisted row. Persistence for mode flags remains owned by
    `setLivingWikiModeFlags`.
  - Clear `DisabledAt` on re-enable (matches `UpdateRepositoryLivingWikiSettings`
    semantics).
  - Extracted the job-mode derivation into `deriveLivingWikiJobMode` for
    direct unit-test coverage (no orchestrator stub needed).
  Nine new tests in `living_wiki_mode_flags_test.go` cover the contract
  (`Test*Override*` and `TestDeriveLivingWikiJobMode_*`).

- **gqlgen drift untangle** (`aeb92d8`, CA-138). `gqlgen.yml` now
  preserves the two override pointers via `extraFields` with explicit
  `overrideTags: 'json:"-"'`. Resolver-ownership inversion: four
  resolvers that gqlgen kept duplicating into `schema.resolvers.go`
  (`GenerateLivingWikiPageOnDemand`, `SetRepositoryLLMOverride`,
  `ClearRepositoryLLMOverride`, `LlmOverride`) now live exclusively in
  `schema.resolvers.go`; `living_wiki_on_demand.go` deleted, the helpers
  in `repository_llm_override.resolvers.go` retained. `gqlgen generate`
  is now idempotent (verified via consecutive runs producing zero diff).

- **MCP server reports git-derived version** (`47adbad`, CA-137). The
  SourceBridge MCP server (`internal/api/rest/mcp.go`) previously
  hardcoded `mcpServerVersion = "1.0.0"` in its `initialize` response's
  `serverInfo.version` and the `experimental.sourcebridge.version`
  capability. It now reads `internal/version.Version` — the same symbol
  `/api/v1/version`, `/api/v1/admin/status`, GraphQL `Query.version`,
  and the telemetry sender all use. Every visible version surface on a
  given binary now reports the same string. The MCP **protocol** version
  (`mcpProtocolVersion = "2025-11-25"`) is unchanged.

- **Sigstore cosign keyless OIDC signing for OSS images** (`c10694c`,
  CA-139). All three OSS images (`sourcebridge-api`, `sourcebridge-web`,
  `sourcebridge-worker`) plus the combined release image are signed with
  cosign keyless OIDC on every push to `main`/`dev` and on tagged
  releases. Signatures live next to images in GHCR (and Docker Hub when
  configured). No keys to manage — uses the GHA OIDC token bound to the
  workflow identity, with Fulcio-issued ephemeral certs and Rekor
  transparency-log entries. Pull-request builds are deliberately not
  signed. See `docs/admin/build-info.md` for the verification regex
  (strict / permissive recipes), what it enforces, and how to verify by
  digest when tags drift.

Theme: **Pazaryna — first-run reliability.** Five fixes and features that close
the gap between what a new self-hoster reads and what actually works: a
scriptable admin-bootstrap command, non-interactive login, lazy agentic-feature
activation (so worker startup order no longer matters), fail-fast provider
validation in the Python worker, and walker hardening in `sourcebridge review`.
CI is now green again after a proto-naming lint break and a flaky wall-clock
test.

### Added

- **`sourcebridge setup admin` — scriptable admin bootstrap** (`876c798`,
  CA-127). New subcommand under `sourcebridge setup` that posts to
  `POST /auth/setup`, validates the password client-side (≥8 chars), saves
  the returned session token to `~/.sourcebridge/token` (0600), and prints
  copy-paste-ready next steps with the actual server URL. Supports all three
  non-interactive password vectors (see Changed below). Returns a 409-aware
  error pointing at `sourcebridge login` when the server is already
  initialized. Use `--no-save` to skip writing the session token (useful when
  bootstrapping a server you don't intend to use from this machine).

- **Non-interactive password input for `sourcebridge login` and `sourcebridge
  setup admin`** (`876c798`, CA-127). Three vectors, in strict precedence
  order — exactly one may be set at a time; supplying more than one is
  refused with a "pick exactly one" error:
  - `--password-stdin` — reads one line from stdin. Recommended for CI
    (`echo "$ADMIN_PW" | sourcebridge ...`). Never appears in shell history
    or `/proc/<pid>/cmdline`.
  - `--password-file <path>` — reads from a file; warns to stderr if file
    mode is more permissive than 0600.
  - `SOURCEBRIDGE_PASSWORD` — env var; last resort.
  `--password <value>` is intentionally absent (would leak into shell history
  and `ps`).

- **`out` added to the canonical ignored-directory set** (`201548a`, CA-124).
  `git.DefaultIgnorePatterns` now includes `out` (Next.js static export, Java
  IDEs, several bundlers). The indexer, change-watch, and `sourcebridge review`
  walker all benefit.

- **Visible app version + auto-versioning across releases & dev builds**
  (CA-136). Every running SourceBridge instance now displays a version
  string derived from git, baked into the Go binary via `-ldflags` and
  the web bundle via `NEXT_PUBLIC_*`, and exposed at
  `GET /api/v1/version` (public, unauthenticated), `sourcebridge --version`,
  the sidebar footer, the **Admin → System status → Build info** card,
  and the OCI image labels (`org.opencontainers.image.version` and
  friends). Tagged releases produce bare-tag versions
  (`v1.2.3`); commits past the last tag produce `<tag>-dev.N+g<sha>`;
  pull-request builds produce `<tag>-pr<N>+g<sha>`; local builds
  produce `<tag>-local+g<sha>[.dirty]`. The grammar is implemented by
  `scripts/version.sh` (9-case shell test in `tests/scripts/`) and
  consumed identically by the Makefile, the three Dockerfiles,
  `docker-compose.yml`, `build-images.yml`, and `oss-release.yml`.
  The Python worker also exposes a gRPC `VersionService.GetVersion`
  used by the API server to report the worker version. See the new
  [docs/admin/build-info.md](docs/admin/build-info.md) for full
  reference.

### Changed

- **`sourcebridge review` walker uses the canonical ignore list** (`201548a`,
  CA-124). The walk no longer maintains its own four-entry skip list
  (`node_modules`, `.git`, `vendor`, `__pycache__`). It now calls
  `git.IsIgnoredDir` — the same helper the indexer and change-watch pipeline
  use — so `.next/`, `dist/`, `build/`, `out/`, `target/`, and any future
  additions to `DefaultIgnorePatterns` are automatically skipped. The internal
  `findReviewableFiles` helper is now unit-testable and covered by
  `cli/review_walker_test.go`.

- **Agentic features activate lazily instead of at API server boot** (`9d15856`,
  CA-126). Previously the API server probed the worker once during startup
  with six 5-second retries; if all attempts failed the agentic synthesizer
  was never wired and agentic features stayed disabled for the pod's entire
  lifetime — meaning starting the worker after the API server did nothing.
  Now `LazyAgentSynth` defers the probe to the first agentic-eligible request.
  The probe has a 2-second timeout and coalesces concurrent first-request bursts
  via single-flight. A success is cached (and re-validated when the workspace
  LLM config version changes); a failure is retried after a 60-second cooldown.
  A best-effort boot probe still runs in the background: on success it hot-warms
  the cache; on failure it prints a one-line stderr warning (`warning: AI worker
  not reachable at <addr>; ... Start the worker with: make dev-worker`) without
  installing the cooldown, so the first real agentic request after the worker
  comes up activates immediately.

- **Worker config validates providers at startup, not at first call** (`f6f9951`,
  CA-125). `WorkerConfig` now runs pydantic `field_validator`s on
  `llm_provider`, `embedding_provider`, and `llm_report_provider` at
  config-load time. Unknown values produce an actionable error naming the
  supported set rather than a mid-init `NotImplementedError` or bare
  `ValueError`. Anthropic is explicitly noted as unsupported for embeddings in
  the error message. The factory functions retain their own guards as defense
  in depth (the pydantic validator is bypassed by `model_copy(update=...)`,
  the path per-request overrides take). Supported sets: LLM — `anthropic`,
  `openai`, `ollama`, `vllm`, `llama-cpp`, `sglang`, `gemini`, `openrouter`,
  `lmstudio`; embedding — `ollama`, `openai`, `openai-compatible`.

- **Login error message points at `/login`, not `/setup`** (`201548a`, CA-124).
  The "setup not done" error from `sourcebridge login` now directs users to
  `/login` (where Next.js handles initial setup automatically) and includes a
  curl one-liner for headless setups via `POST /auth/setup`.

- **`make dev-worker` is now the canonical worker entry point** (`201548a`,
  CA-124). `Makefile` gained a `dev-worker` target; the README and CLAUDE.md
  were updated to use it everywhere. The previous instruction
  (`cd workers && uv run python -m workers`) broke because absolute imports
  fail when the CWD is `workers/`.

### Fixed

- **Hub install: web container proxy now resolves the upstream API URL at
  request time** (`1fee78b`, `9ba671b`, `873bc53`). The published
  `sourcebridge-web` image previously baked `next.config.ts` rewrites
  pointing to `http://localhost:8080` at build time. Inside the running
  container that address is unreachable — the API is at `sourcebridge:8080`
  on the Docker network — so every fresh `docker-compose.hub.yml` install
  got HTTP 500 on `/auth/info`, and the login page silently fell back to
  password-entry mode for an account that was never created. The fix
  replaces `next.config.ts rewrites()` with a Next.js middleware
  (`web/src/middleware.ts`) that reads `SOURCEBRIDGE_WEB_DEV_PROXY` per
  request. The middleware also strips upstream API security headers
  (prevents CSP/X-Frame-Options collisions with the web UI's own headers)
  and `X-Forwarded-*` injection vectors (closes a rate-limit bypass via
  spoofed client IP). SSE streams propagate browser disconnect via
  `signal: request.signal`.

- **`docker-compose.hub.yml` SurrealDB init no longer fails with
  `PermissionDenied`** (`b73502c`). The `surrealdb-init` service was
  missing from the hub compose file; first installs silently skipped DB
  initialization and the API container crashed on boot.

- **`sourcebridge login` error no longer sends users to a 404** (`201548a`,
  CA-124). The "setup not complete" error previously named `/setup`, which
  returns 404. It now names `/login` and provides a curl fallback.

- **Flaky `-race` violation in
  `internal/api/graphql/knowledge_refresh_test.go`** (`993c332`, CA-136).
  `captureSlog` previously created an unsynchronized `*bytes.Buffer` and
  pointed `slog.SetDefault` at it. `enqueueStaleArtifactRefresh` in live
  mode fires `slog.Warn` from a background goroutine the test never
  joins — the handler raced the test's buffer reads. The fix wraps the
  buffer in a `sync.Mutex`-protected writer; the function under test is
  unchanged. Closes the deploy-gate intermittency seen in CI run
  25242941735.

### Internal

- **Proto naming — `AnswerQuestionStream` gets distinct request/response types**
  (`75b5108`, CA-128). `buf` STANDARD lint requires distinct types per RPC.
  `AnswerQuestionStreamRequest` is introduced (field-for-field copy of
  `AnswerQuestionRequest` today, separately evolvable). `AnswerDelta` is
  renamed `AnswerQuestionStreamResponse`. Wire-compatible: proto3 messages
  with the same field tags are indistinguishable on the wire. Generated Go
  and Python stubs regenerated; all consumers updated.

- **67 Go lint errors resolved across the tree** (`75b5108`, CA-128). Mix:
  49 unused symbols, 6 ineffassign, 6 gosimple, 3 staticcheck, 2 errcheck.
  Dead code removed where callers are gone; `//nolint:unused` applied where
  retention is intentional (backward-compat shims, option-pattern parity,
  canonical reference lists).

- **Flaky `TestIndexFiles_DeltaBudgetUnder100ms` replaced with a ratio gate**
  (`75b5108`, CA-128). The old test asserted a 100 ms absolute wall-clock
  ceiling on the single-file delta — marginal on shared CI runners where
  scheduler jitter can push a 14 ms operation past 100 ms. The replacement
  uses a ratio gate (delta/baseline ≤ 0.25) which is hardware-independent,
  plus a 500 ms sanity ceiling for catastrophic regressions where both paths
  blow up.

---

Theme: **Living wiki runtime activation.** The living-wiki feature shipped
as compiled-but-inert code in the prior release; this workstream makes it
actually run. A user can now enable the feature for a specific repo, choose
which sinks to publish to, and watch a Confluence (or Notion, or git-repo)
page appear — driven by a scheduler that wakes up periodically, elects a
leader, respects per-sink rate limits, and persists per-job results the UI
can display. Eight workstreams (R1–R9) cover the full path from data model
to UI panel to sink dispatch, with three testing tiers and a runbook in
`RELEASING.md`. Four deploy-validation bugs were found and fixed before the
feature reached production.

Also: **CI + deploy infrastructure.** A GitHub Actions workflow now builds
and publishes per-SHA, per-branch, and per-tag images to GHCR on every push.
A generic kustomize base at `deploy/kubernetes/base/` gives OSS deployers a
ready-made starting point without copy-pasting manifests.

### Added

- **Per-repo living-wiki settings** (`c841f61`). Data model (`lw_repo_settings`,
  `lw_job_results`), migration 036, and three GraphQL mutations:
  `updateRepositoryLivingWikiSettings`, `enableLivingWikiForRepo`,
  `disableLivingWikiForRepo`, plus an admin-only `repositoriesUsingSink`
  query. Each repo independently selects which sinks to use, which
  audiences to publish for, whether to PR-review or direct-publish, and
  per-sink edit policies. Soft-disable preserves config so re-enable
  restores prior state.

- **Boot wiring and webhook dispatcher** (`88eb809`).
  `internal/livingwiki/assembly/AssembleDispatcher` constructs every
  orchestrator port and the dispatcher in one place. The webhook dispatcher
  starts at boot when `Enabled` is true; disabled-feature paths return 503
  (not 404) with a JSON body so webhook senders can distinguish. Explicit
  shutdown sequence: HTTP drain → `dispatcher.Stop` with 30 s timeout →
  store close. Migration 037 adds `lw_pages` and `lw_watermarks`.
  `SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH` env var bypasses the feature
  without redeploying.

- **Credential broker with per-job snapshots** (`792ca64`).
  `internal/livingwiki/credentials/Broker` interface and per-job `Snapshot`.
  HTTP clients no longer take credentials in their constructors; they receive
  a Snapshot per call, enforcing the at-most-one-rotation-per-job invariant.
  Credential rotations are recorded on `governance.AuditLog`.

- **Cold-start jobs via the existing LLM activity feed** (`fba7ad7`). Cold-
  start jobs route through `/api/v1/admin/llm/activity` alongside knowledge-
  engine jobs — no second polling loop. Three-category failure classification:
  transient (Retry), auth (Fix credentials deep-link), partial-content (Retry
  excluded pages only). `retryLivingWikiJob` GraphQL mutation scopes retries
  to previously-excluded pages. Per-page progress events are visible in the UI.

- **Per-repo wiki settings panel in the web UI** (`46a96a6`). Six visual states
  (globally-disabled / kill-switch / activation gate / corrupt / cold-start in
  progress / enabled-idle / refinement panel) with progressive disclosure.
  Stage A hides advanced fields behind sensible defaults; Stage B exposes them
  after first generation. Failure banners carry category-specific CTAs.
  Mobile-responsive State 4 summary row. Discoverability callout on the repo
  overview when the wiki is unconfigured.

- **Periodic scheduler with leader election, jitter, and metrics** (`4b472ef`).
  Per-repo FNV-32a jitter is deterministic across restarts; leader election
  reuses the `trash_sweep` lease pattern. Per-tenant concurrency cap and per-
  sink rate limiters (Confluence / Notion / GitHub / GitLab). Five Prometheus
  metric series: `livingwiki_jobs_total`, `livingwiki_pages_generated_total`,
  `livingwiki_validation_failures_total`, `livingwiki_job_duration_seconds`,
  `livingwiki_sink_write_duration_seconds`. Persistent `LivingWikiJobResult`
  store with a per-repo last-result GraphQL field.

- **Sink dispatch wiring** (`a6a532b` R9). `internal/livingwiki/sinks/`
  package with a `SinkWriter` interface and concrete implementations for
  Confluence, Notion, and git-repo. The cold-start runner iterates
  `RepositoryLivingWikiSettings.Sinks`, builds the right writer per kind via
  the assembly factory and credentials snapshot, and pushes generated pages
  through each. Per-sink results (`SinkWriteResults`) persist on
  `LivingWikiJobResult` so the UI can show "Confluence: 22 pages, 0 failed."

- **Three testing tiers** (`acd28dd`). Unit-integration test suite (`make
  test-livingwiki-integration`, build tag `livingwiki_integration`); a real-
  Confluence weekly CronJob smoke test (`cmd/livingwiki-smoke` +
  `deploy/kubernetes/base/cronjobs/livingwiki-smoke.yaml.example`); manual
  release validation runbook in `RELEASING.md`.

- **GitHub Actions image build workflow + kustomize base** (`8e0b128`).
  Per-SHA, per-branch, and per-tag images published to GHCR on every push.
  Generic kustomize base at `deploy/kubernetes/base/` for OSS Kubernetes
  deployments. `dev` branch added to the push trigger.

- **MCP-edits feedback-loop scaffolding (Phase 1.A — refactors only).** Five
  small public-API changes that downstream Phase 1.B–1.E slices will compile
  against. No behavior change for existing call sites; the umbrella
  `SOURCEBRIDGE_CHANGE_WATCH_ENABLED` flag flips with the watcher in 1.C.
  - `git.IsIgnoredPath(repoPath, relPath string) bool` — extracted from
    `ScanRepository`'s inline filtering so the upcoming change-watch pipeline
    and the indexer share one source of truth for ignored paths.
  - `git.HeadRef(repoPath string) (string, error)` — thin wrapper over
    `GetGitMetadata` that returns just the branch name and surfaces
    `git.ErrNotAGitRepo` when there's no `.git` entry, instead of the
    silent `(nil, nil)` the existing helper returns.
  - `indexer.IndexResult.Branch string \`json:"branch,omitempty"\`` — new
    optional field; existing `IndexRepository`/`IndexRepositoryIncremental`
    callers leave it empty (and the JSON tag's `omitempty` keeps the
    serialized shape unchanged for every current consumer).
  - `indexer.Indexer.IndexFiles(ctx, repoPath, files, branch, prev)` —
    signature stub; body returns `indexer.ErrIndexFilesNotImplemented`. The
    Phase 1.B implementation will land the per-file refresh path the
    change-watch router calls; locking the signature here lets downstream
    slices compile.
  - `indexer.RepoIndexFullReason` enum + precondition guard on
    `IndexRepository`. Every existing caller now passes a typed reason
    (`ReasonInitialOnboard` or `ReasonOperatorRebuild`); the change-watch
    router has no reason value to pass and therefore cannot reach the
    whole-tree path.
  Internally: the post-`ComputeImpact` block in the `ReindexRepository`
  GraphQL mutation has been extracted into a reusable
  `applyImpactFromChange` helper with full behavior preservation. Phase 1.B
  ships next.

- **MCP-edits feedback-loop — IndexFiles per-file delta (Phase 1.B).**
  Replaces the `ErrIndexFilesNotImplemented` stub from 1.A with the real
  per-file refresh entry point. The change-watch router (Phase 1.C) and the
  T0 sync-refresh path (also 1.C) will both call `IndexFiles` exclusively;
  the function parses only the listed files, merges into a copy of
  `previousResult`, and recomputes per-`IndexResult` aggregates over the
  merged file set. Existing `IndexRepository` and
  `IndexRepositoryIncremental` callers are unchanged.
  - New sentinel errors: `indexer.ErrBranchMismatch` (load-bearing for
    Risk #4 — a CI push to `main` while an agent works on `feature/x`
    must not silently corrupt the agent's branch-scoped freshness state),
    `indexer.ErrEmptyFiles`, `indexer.ErrPreviousResultRequired`. The
    branch-mismatch wrapped message includes both claimed and head
    branches so the router's `rejected_branch_mismatch` log entry has
    the diagnostic data the plan specifies.
  - Performance: `git.HeadRef` now reads `.git/HEAD` directly (microsecond
    scale) and falls back to `git rev-parse` only for uncommon cases.
    The previous implementation shelled out unconditionally (~30-90 ms
    per call on macOS / shared CI runners), which would have consumed
    most of the 100 ms IndexFiles T0 budget on a single subprocess fork.
    Worktrees (`.git` as a file with a gitdir pointer) and detached
    HEADs are handled in the fast path. The defense-in-depth contract is
    preserved: every IndexFiles call still validates the claimed branch
    against the working-tree HEAD.
  - Phase 1 done-definition tests landed: #6 (IndexFiles 100 ms budget on
    a >= 500-file fixture — observed ~12 ms wall-clock with ~8x headroom),
    #12 in-process half (branch threading + branch-mismatch rejection;
    the router-level half lands in 1.C with a `t.Skip` placeholder
    `TestIndexFiles_RouterBranchMismatch_DeferredTo1C` carrying the
    contract forward), and #11's deferred end-to-end half (the
    `ReindexRepository` GraphQL mutation drives the indexer end-to-end
    against an on-disk fixture and reaches `applyImpactFromChange` with
    a real `ImpactReport` that produces the same observable post-state
    the helper-level test asserts for the synthetic-input case).
  - New shared scaffolding: `internal/indexer/testfixtures.LargeGoRepo`
    materializes a synthetic Go repository (default 500 files across 10
    package buckets, configurable) at `t.TempDir`, git-init'd on the
    configured branch with one initial commit. `WriteFile` / `Commit` /
    `Branch` helpers let tests simulate out-of-band edits, commit them,
    or switch branches. Used by both #6 and #11's e2e half so the e2e
    test does not have to build a one-off fixture harness.
  - The `SOURCEBRIDGE_CHANGE_WATCH_ENABLED` umbrella flag remains off;
    nothing in 1.B is reachable from production paths until the watcher
    + router land in 1.C and the flag flips at the end of 1.E burn-in.

- **MCP-edits feedback-loop — change-watch router + watcher + freshness
  envelope (Phase 1.C).** Lands the in-process feedback loop: a passive
  `internal/changewatch.Watcher` driven by `fsnotify`, the
  `internal/changewatch.Router` that all connectors funnel through, and
  the `_meta.freshness` envelope on every MCP tool response. The
  umbrella `SOURCEBRIDGE_CHANGE_WATCH_ENABLED` flag remains off — Phase
  1.E flips it after burn-in. Production code paths see no change yet.
  - **`internal/changewatch.Router`**: single Submit entry point all
    connectors share. Schema validation → flag gate → repo resolve →
    per-repo aggregate breaker (60/min × 5min across all source kinds
    combined) → per-(repo, source.kind) token bucket → dedup window
    (event_id idempotency + content-hash collapse so fsnotify and
    record_change observing the same edit produce one routed event)
    → branch validation against `git.HeadRef` (rejects mismatch) →
    `Indexer.IndexFiles` under a 100 ms T0 budget → containment
    assertion (no merged file outside the declared affected set) →
    `GraphStore.MergeIndexResult` → impact report + existing
    `applyImpactFromChange` policy → freshness envelope update.
    Goroutine-safe; the clock reference is stored behind
    `sync/atomic.Pointer` so callers reading time from inside a
    critical section don't re-enter the mutex.
  - **`internal/changewatch.Watcher`**: in-process passive connector.
    `fsnotify.Watcher` watching every non-ignored directory under each
    indexed repo, debouncing raw kernel events into per-repo batches,
    stamping a `ChangeEvent` per batch, and submitting to the router.
    macOS support: the watcher resolves symlinks on the input path
    (`filepath.EvalSymlinks`) so the stored repoPath matches the path
    fsnotify reports in event names — without this the
    `/var → /private/var` symlink on macOS made the prefix-match in
    classify silently miss every event for `t.TempDir()`-based repos.
  - **`git.IsIgnoredDir`**: directory-side filter sharing the
    component-name and hidden-prefix rules with `IsIgnoredPath` but
    skipping the unknown-language rule. The watcher uses this during
    its initial walk so plain package directories like `pkg0` aren't
    pruned by the file-shaped rule.
  - **`_meta.freshness` envelope on every MCP tool response** (added
    in this slice — see prior commit `7903efe`). Surfaces `state`,
    `tier`, `branch`, `indexed_commit`, `last_verified_at`, `reason`,
    `partial_refresh` to every consumer. Default-fresh when no
    provider is wired so disabling change-watch never breaks the wire
    contract.
  - **`GraphStore.MergeIndexResult`** (added in commit `056c9b6`):
    per-file delta merge that drops dependent records on the affected
    files and re-inserts them while preserving carry-forward
    Symbol IDs. Production `Store` implements it; the SurrealDB-backed
    persistence path returns `ErrMergeNotSupported` (1.C surfaces this
    through the freshness envelope as `state: "suspect"` rather than
    failing loudly; full SurrealDB merge ships in Phase 4).
  - **Per-repo aggregate circuit breaker semantics**: opens when 5
    consecutive minutes are at or above the configured threshold;
    cooldown lasts the full minute after the last saturated minute
    (head + 2 min), so the very next event is rejected rather than
    landing on the cooldown boundary.
  - Phase 1 done-definition tests landed:
    - **#1 + #3** — `TestIntegration_ExternalEditFlowsToFreshness`:
      real fsnotify watcher attached to a real on-disk git working
      tree (`testfixtures.LargeGoRepo`), real `os.WriteFile` performed
      outside any SourceBridge code, real branch validation against
      the working tree (`HeadRefBranchValidator`), real Router
      pipeline, real `FreshnessForExport` read by the same MCP
      envelope adapter consumers go through. ~300 ms wall-clock with
      a tightened 200 ms debounce.
    - **#2 + #5** — `TestIntegration_RecordChange_FlowsToFreshness`:
      router-level in-process record_change path. The public MCP tool
      surface ships in 1.D; this test pins the router-level contract
      so 1.D's wire-up only needs to exercise the HTTP/MCP plumbing on
      top.
    - **#8** — `TestRouter_RejectsEmptyDelta` +
      `TestRouter_RejectsInvalidPaths`: router rejects empty `files[]`
      and path-traversal attempts before any dispatch.
    - **#9** — `TestRouter_MultiTenantContainment`: events for tenant
      A's repo do not mutate tenant B's freshness envelope or trigger
      any IndexFiles call against B's path.
    - **#10 router half** — `TestIndexRepository_RouterHasNoFullReindexCallPath`:
      AST-walks `internal/changewatch/*.go` (production sources only)
      and fails on any selector expression naming `IndexRepository` or
      `IndexRepositoryIncremental`. Replaces 1.A's `t.Skip` placeholder.
    - **#12 router half** — `TestRouter_RejectsBranchMismatch` +
      `TestRouter_AcceptsRefsHeadsBranchEquivalent`. The indexer-side
      `TestIndexFiles_RouterBranchMismatchHookedTo1C` pins the
      cross-package reference so a future rename of the canonical test
      surfaces at the indexer boundary too. Replaces 1.B's `t.Skip`
      placeholder.
    - **#13** — `TestRouter_PerRepoBreakerTrips`: drives 30 fsnotify +
      30 record_change events per minute for 5 minutes (each kind under
      its 100/min throttle, but 60/min combined per-repo) and verifies
      the breaker trips on minute 6.
    - **#14** — `TestRouter_DedupByContentHashAcrossSourceKinds` +
      `TestRouter_DedupByEventIDIdempotency`: same content_hash from two
      source kinds collapses to one routed event; same event_id replayed
      collapses too.
    - **#15 dir-side** — `TestIsIgnoredDir`: 16 cases pinning the new
      `git.IsIgnoredDir` helper.

- **MCP-edits feedback-loop — connector ingress + `record_change` MCP
  tool (Phase 1.D).** Lands the public HTTP ingress and the in-process
  MCP-tool connector. Together with the fsnotify watcher (1.C) these
  are the three Phase 1 connectors that share the canonical
  `ChangeEvent` schema and funnel through the same `changewatch.Router`
  dispatch path. Both surfaces stay default-off through Phase 1.E
  burn-in.
  - **HTTP ingress endpoint** at `POST /v1/connectors/{id}/events`,
    behind `SOURCEBRIDGE_CONNECTOR_API_ENABLED` (default false). Auth:
    bearer/JWT through the same middleware as other authenticated
    routes (HMAC-SHA256 specific to GitHub webhooks lands in Phase 2).
    Trust stamping locks `received_via=http_ingress`,
    `verification_method=bearer`; connectors cannot claim `in_process`
    via the body. Source.kind is locked to HTTP-ingress-family kinds —
    a remote caller posting `mcp_record_change` or `fsnotify_local`
    is silently rewritten to `http_ingress` (those kinds are reserved
    for in-process connectors with different downstream trust
    assumptions; spoofing them remotely is a privilege-escalation
    shape). `DisallowUnknownFields` on JSON decoding so 0.x callers
    fail loudly when they hit the 1.0 schema rather than silently
    degrading. Outcome → HTTP-status mapping documented as part of the
    Connector API contract: 202 (indexing/deduped), 429 (rate_limited),
    503 (breaker_tripped / change_watch_disabled), 400 (schema /
    no-delta / invalid-paths), 409 (branch_mismatch / unknown_repo),
    500 (unmapped outcomes — alarms loudly so a missing case lights up
    in tests).
  - **`record_change` MCP tool**, the in-process MCP-tool connector
    named in plan v5 §Phase 1 > 4. Inputs: `repository_id`, `files[]`,
    `branch`, optional `intent`, optional `requirement_ids[]`. Output:
    `{accepted, change_id, routed_to, reason, branch, file_count}`.
    Adoption posture per plan v4 decision #4 — easy, never required:
    tool description leads with "Optional"; tool is hidden from
    `tools/list` when the change-watch dispatcher is nil so agents
    don't discover a no-op tool; defense-in-depth path returns
    structured `CAPABILITY_DISABLED` if a hand-crafted `tools/call`
    reaches the handler with the dispatcher unwired.
  - **Trust contract**: `Source.Actor` is derived from the MCP
    session's authenticated identity (`session.claims.UserID@OrgID`),
    NOT from tool args — agents cannot lie about being human.
    `Trust.ReceivedVia="in_process"` stamped at the connector boundary
    so an in-process call cannot claim http transport (the inverse
    spoofing direction).
  - **Path-normalization contract** (plan v5 HIGH fix L3) is enforced
    at three points: `changewatch.NormalizePath` at both ingress
    surfaces (clean MCP / 400 errors), and re-validated by the router
    via `ChangeEvent.Validate` (defense in depth). Contract: repo-
    relative; Unix forward-slash separators only (rejects backslash —
    we don't silently `filepath.ToSlash` so caller-side bugs don't
    slip through); no leading `./` or `/`; no trailing `/`; no `..`
    components; no `//` or `/./` tricks. Case-handling: caller's
    casing is preserved verbatim (Linux is case-sensitive at FS level;
    macOS/Windows are case-insensitive at FS but git treats every
    path case-sensitively regardless — the helper matches git's
    worldview).
  - **Multi-tenant boundary**: `record_change` calls `checkRepoAccess`
    against the session's allowed-repo list + the enterprise
    `MCPPermissionChecker` before any router work. An agent targeting
    an inaccessible repo gets the standard "not found" error (no
    fingerprinting of which repos exist). HTTP ingress relies on the
    auth middleware identity + the router's per-repo state isolation.
  - **Defensive bounds at the tool boundary**: per-call cap of 1024
    files, 1024-byte intent, 100 requirement IDs (256 bytes each).
    Intent is sanitized — ASCII control bytes / non-printable runes
    stripped to defend against log-injection / terminal-escape via
    the structured-log path that records `intent`. Whitespace
    (space/tab/newline/CR) survives so multi-line intents stay
    readable.
  - **`Linking.InvalidateGraceHours` already configurable** (was 24h
    hardcoded; surfaced as `linking.invalidate_grace_hours` /
    `SOURCEBRIDGE_LINKING_INVALIDATE_GRACE_HOURS` in the slice 4
    config-source-of-truth work — covers nice-to-have L1 from plan v5
    without a new flag).
  - Phase 1 done-definition tests landed:
    - **#7 passive-only correctness** —
      `TestPassiveOnly_Phase1DoneDef7`: five scenarios (single-file
      write, multi-file refactor, file addition, file deletion, file
      rename) on a real on-disk fixture with the in-process
      `record_change` MCP tool *never invoked*. Each scenario asserts
      the freshness envelope advances, state transitions correctly,
      `IndexFiles` is called for the affected paths, and the impact
      applier runs. **Load-bearing for the non-goal**: if this test
      ever fails, the change in question violates "no SourceBridge
      feature shall be built that requires `record_change` for
      correctness" and must be revised. Do NOT make the test pass by
      adding `record_change` calls to it.
  - HTTP ingress per-tool tests cover dispatcher-unwired (503),
    happy path (202 with trust populated + connector_id stamped),
    URL→connector_id stamping when body omits it, default `http_ingress`
    kind, trust-overridden-from-body, bad JSON (400), unknown-fields
    rejection (`DisallowUnknownFields`), every router outcome → HTTP
    status mapping, unknown-outcome→500 alarm.
  - `record_change` per-tool tests cover hidden-vs-visible in
    tools/list, happy path with full ChangeEvent shape verification,
    status defaults, path normalization rejection, rename-without-old-
    path rejection, empty-files rejection, tenant-boundary rejection,
    every router outcome → `accepted` boolean mapping, actor always
    derived from session, defense-in-depth on nil dispatcher, plus
    the four xander-pass security tests (oversized files / intent /
    requirement_ids; control-char stripping).
  - The `SOURCEBRIDGE_CHANGE_WATCH_ENABLED` umbrella flag remains off,
    and `SOURCEBRIDGE_CONNECTOR_API_ENABLED` defaults off through
    Phase 1; nothing in 1.D is reachable from production paths until
    1.E flips the flag.

- **MCP-edits feedback-loop — Phase 1 closing (1.E burn-in).** The
  no-new-feature-code phase that gates the `SOURCEBRIDGE_CHANGE_WATCH_ENABLED`
  flag-flip behind a final integration sweep, an operator runbook,
  and a verified done-definition. Phase 1's full commit chain
  (1.A → 1.B → 1.C → 1.D → 1.E) is ready for review.
  - **Cross-package integration sweep** at
    `tests/integration/phase1_changewatch_test.go`. Exercises the full
    closed loop in the order a real edit travels: fsnotify Watcher
    detects external edit → Router validates schema + branch +
    delta-only guardrails → IndexFiles under T0 budget →
    MergeIndexResult → ImpactApplier → freshness envelope updated.
    Confirms the package-boundary wiring (watcher → router →
    FreshnessForExport, the same path the MCP envelope adapter reads)
    beyond what individual sub-phase tests cover.
  - **Operator runbook stub** at `docs/admin/change-watch.md`. Covers
    what change-watch does, what flag-flip activates, what disabling
    does, tuning knobs, observability checklist for the first week,
    rollback procedure, Phase 1 readiness summary, and explicit list
    of what flag-flip does NOT activate (Phase 2-5 deferrals).
  - **All 15 Phase 1 done-definition tests** verified green:
    - #1 + #3 — `TestIntegration_ExternalEditFlowsToFreshness` (1.C)
    - #2 + #5 — `TestIntegration_RecordChange_FlowsToFreshness` (1.C
      router-level) + `TestRecordChange_HappyPath` (1.D tool-level)
    - #6 — `TestIndexFiles_DeltaBudgetUnder100ms` (1.B)
    - #7 — `TestPassiveOnly_Phase1DoneDef7` (1.D, 5 scenarios) —
      load-bearing for the `record_change`-never-required non-goal
    - #8 — `TestRouter_RejectsEmptyDelta` +
      `TestRouter_RejectsInvalidPaths` (1.C)
    - #9 — `TestRouter_MultiTenantContainment` (1.C)
    - #10 — `TestRepoIndexFullReason_GuardRefusesUnspecified` (1.A)
      + `TestRouter_OnlyCallsIndexFiles` (1.C) +
      `TestIndexRepository_RouterHasNoFullReindexCallPath` (1.C)
    - #11 — `applyImpactFromChange` extraction regression (helper
      level 1.A; e2e 1.B)
    - #12 — `TestIndexFiles_BranchMismatchRejected` /
      `TestIndexFiles_BranchMatchAccepted` (1.B in-process half) +
      `TestRouter_RejectsBranchMismatch` /
      `TestRouter_AcceptsRefsHeadsBranchEquivalent` (1.C router half)
    - #13 — `TestRouter_PerRepoBreakerTrips` (1.C)
    - #14 — `TestRouter_DedupByContentHashAcrossSourceKinds` +
      `TestRouter_DedupByEventIDIdempotency` (1.C)
    - #15 — `git.IsIgnoredPath` parity (1.A) + `TestIsIgnoredDir`
      directory-side parity (1.C)
  - **What flag-flip activates after Phase 1.E**: passive fsnotify
    detection, per-repo router with delta-only guardrails, per-(repo,
    source.kind) rate limit + per-repo aggregate breaker, branch
    mismatch rejection, multi-tenant containment, freshness envelope
    on every MCP response, the `record_change` MCP tool (opt-in,
    never required), HTTP ingress at
    `POST /v1/connectors/{id}/events`, path-normalization contract.
  - **What flag-flip does NOT activate** (deferred to later phases):
    `Fast / Balanced / Strict` mode picker (Phase 4),
    `mark-suspect`/`auto-resolve` link state machine (Phase 2), web
    UI freshness chips and change feed (Phase 5), compound-tool
    `LINK_INVALIDATED` refusal (Phase 2), GitHub webhook + App
    connector (Phase 2), `/admin/connectors` admin UI (Phase 2),
    schema promotion `0.x → 1.0` (Phase 2 after the schema-stability
    checkpoint), T2/T3 surgical re-derivation (Phase 3), persistent
    `ca_change_event` table (Phase 5).

### Changed

- **Credential model** (`792ca64`). HTTP clients for all living-wiki sinks
  receive a per-call `Snapshot` rather than credentials baked in at
  construction time, so credential rotation takes effect on the next job
  without restarting any long-lived client.

- **Boot path** (`88eb809`). The living-wiki dispatcher is now part of the
  standard server startup sequence with a defined shutdown contract, rather
  than launched ad hoc.

### Fixed

- **Migration 037 conditional unique indexes** (`adbf4ba`). SurrealDB does not
  support `WHERE` clauses on `DEFINE INDEX`. The two conditional indexes were
  collapsed into a single `(repo_id, pr_id, page_id, kind)` UNIQUE tuple that
  SurrealDB accepts without a predicate.

- **`testConfluenceConnection` endpoint mismatch** (`4fbc197`). The test-
  connection call was hitting `https://api.atlassian.com/me`, an OAuth 2.0
  endpoint that rejects API-token basic auth. Switched to
  `https://<site>.atlassian.net/wiki/rest/api/user/current`, which accepts
  basic auth. A new `confluenceSite` field was added to global settings, the
  UI, and migration 038 to carry the site subdomain.

- **`lw_settings` stale-row schema failures** (`8104c06`, `cb246ec`). The
  original `lw_settings` row predated migrations 036 and 038 that added
  `tenant_id` and `confluence_site`. SurrealDB `DEFAULT` only fires on row
  creation, so the existing row had `NONE` for both fields, failing schema
  validation on every subsequent UPSERT. Migration 039 backfills both fields
  on the existing row, and the UPSERT path now always sets them explicitly.

- **Deep cliff-notes "DEADLINE_EXCEEDED" reaper races** (CA-122,
  `01779d3`...`0b92350`). The seven knowledge gRPCs (cliff notes,
  architecture diagram, learning path, code tour, workflow story,
  explain system, build repository understanding, plus enterprise
  reports) were unary calls with no liveness signal. When the
  hierarchical pipeline took longer than the orchestrator's 10-minute
  stale-job reaper window, the reaper marked the job failed mid-run
  and the gRPC stream got a confusing `DEADLINE_EXCEEDED`. Each RPC
  is now server-streaming, with phase markers (`SNAPSHOT`,
  `LEAF_SUMMARIES`, `FILE_SUMMARIES`, `PACKAGE_SUMMARIES`,
  `ROOT_SYNTHESIS`, `RENDER`, `FINALIZING`) and per-phase progress
  events bridged into the existing `ca_knowledge_artifact` row via a
  bounded-channel writer goroutine. The single-call RPCs (no internal
  step counters) emit a phase-only heartbeat every 30s
  (`SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS`-tunable) so the
  orchestrator's `UpdatedAt` stays fresh through a multi-minute
  single LLM call. The reaper now actively cancels active gRPC
  streams instead of just marking the job failed; first-claimant-
  wins finalization prevents the reaper and the run goroutine from
  racing terminal writes; artifact progress writers no-op once the
  artifact is `ready` or `failed` so a late stream-driver flush
  cannot resurrect a terminal row. The synthetic 5-second progress
  ticker is fully deleted (`startProgressTicker` /
  `runProgressTicker` / `progressPhaseLabel` are gone). One known
  gap, documented in
  `internal/api/rest/enterprise_routes.go`: the enterprise
  `SetReportGenerator` callback still runs on `context.Background()`,
  so HTTP client disconnects do not cancel an in-flight report; the
  signature change is tracked as a follow-up. New operator config:
  `sourcebridge.knowledge.rpc_safety_net_timeout` (default 4h) is
  the hard cap once the streaming heartbeat is established, set via
  `POST /api/v1/admin/knowledge/rpc-safety-net-timeout`. New stream
  bucket maps in `internal/api/graphql/knowledge_stream_driver.go`
  give file/symbol/module cliff-notes scopes a collapsed
  `SNAPSHOT -> RENDER -> FINALIZING` progress contract that does not
  regress when the worker's hierarchical strategy uses internal
  phase counters.

---

Theme: **Living wiki + auto-extracted doc templates.** SourceBridge can now
generate and maintain a coherent, cited wiki directly from your codebase —
opening a PR within 90 seconds of enabling the feature, appending additive
commits on subsequent pushes without force-pushing or overwriting reviewer
edits, and publishing to nine sink targets from a single canonical page
model. Every citation in every report, QA answer, and compliance artifact
now speaks the same `(path:start-end)` contract, so the VS Code plugin can
jump to the exact line a generated page is talking about.

Also: **Claude Code Quickstart.** One command (`sourcebridge setup claude`)
turns any indexed repo into Claude Code-aware — generating a per-subsystem
`.claude/CLAUDE.md` with concrete graph-derived "Watch out:" guidance,
registering the MCP server, and exposing three new MCP tools that let
agents query subsystem boundaries before they refactor. Subsystems are
detected automatically by label-propagation clustering over the call graph
and surfaced in a new web UI tab with inline-editable, LLM-improvable
labels. Living Wiki taxonomy now consumes the same clusters as its primary
"areas" signal, so a single source of truth drives both the wiki and the
agent integration.

### Added

- **Subsystem clustering on the call graph** (`a6a532b`). New
  `internal/clustering/` package runs label propagation (Raghavan / Albert
  / Kumara 2007) over the symbol call graph as an async job after each
  index — never blocks indexing, recomputes only when the SHA-256 of
  sorted edge tuples changes. Migration 040 adds `cluster` and
  `cluster_member` tables. Atomic `ReplaceClusters` wraps delete+insert
  in a SurrealDB transaction so readers never see an empty mid-update
  window. `DeleteClusters` is wired into `RemoveRepository` and
  `ReplaceIndexResult` for orphan-free invalidation. Modularity Q, size
  distribution (min/max/p50/p95), and partial-convergence flag logged
  per run; surfaced via `GET /api/v1/admin/clustering/stats` for ops
  visibility. Four new telemetry fields (`clustering_enabled`,
  `cluster_count`, `clustering_modularity_q`, `agent_setup_used`)
  documented in `TELEMETRY.md`.

- **Three new MCP tools for subsystem awareness** (`a6a532b`).
  `get_subsystems(repo_id)` returns the cluster list with representative
  symbols and cross-cluster call counts. `get_subsystem_by_id(cluster_id)`
  returns the full member list for a known cluster ID.
  `get_subsystem(symbol_id)` returns the cluster a given symbol belongs to
  plus 5 peers. Response shape uses `representative_symbols` plus a
  `selection_method` metadata field so the underlying ranking strategy
  (today: highest in-degree) can change without breaking agent contracts.
  Gated by a new `subsystem_clustering` capability (OSS + Enterprise).

- **Subsystems tab in the web UI** (`a6a532b`). New tab on the repo
  detail page renders a sortable table with cluster label, member count,
  top 3 representative symbols as code chips, and a "Calls into"
  cross-cluster adjacency hint. Cluster labels are inline-editable —
  saving fires a single-cluster LLM rename job; an "Improve labels"
  button kicks off a batch rename for the whole repo with a 10-minute
  polling timeout and humanized error states. Sortable headers use
  `<button>` inside `<th>` with `aria-sort` and ≥44px touch targets;
  long symbol names truncate to a `max-w-[200px]` chip with full-name
  tooltip. Capability-gated on `subsystem_clustering`.

- **Living Wiki taxonomy uses clusters as the primary "areas" signal**
  (`a6a532b`). `TaxonomyResolver.Resolve()` accepts clusters as a
  call-time parameter symmetric with `pkgGraph`. When clusters exist,
  generated wiki pages follow architectural cluster boundaries instead
  of mechanical package boundaries — a Confluence page titled "Auth &
  Sessions" describing 14 symbols across `auth`, `middleware`, and
  `session` packages, instead of three disjoint per-package pages.
  Falls back to package-path heuristics when clusters are absent or
  stale. The cold-start runner now fetches clusters and translates them
  to `ClusterSummary` in production, not just in tests.

- **`sourcebridge setup claude` command + per-subsystem skill cards**
  (`a6a532b`). One command writes `.claude/CLAUDE.md` (with per-subsystem
  `## Subsystem:` sections), registers the SourceBridge MCP server in
  `.mcp.json` via idempotent merge that preserves foreign keys, creates a
  `.claude/sourcebridge.json` sidecar (repo ID, server URL, last index
  timestamp, generated-files manifest), and patches `.gitignore`.
  Generated CLAUDE.md is reference-card-shaped: each subsystem section
  names its packages, member count, representative symbols, and concrete
  "Watch out:" lines derived from the call graph — `cross-package-
  callers` (a symbol with callers in ≥3 top-level packages) and
  `hot-path` (the highest in-degree symbol in a cluster). Marker-based
  idempotency (`<!-- sourcebridge:start/end -->`) with section-hash
  user-edit detection means re-runs preserve handcrafted edits unless
  `--force`. CI mode (`--ci` or `CI=true`) exits non-zero when
  stale-but-skipped sections exist. A repo-specific "Try this first"
  prompt seeded with the two largest clusters demos the integration on
  the first interaction. Differentiated CLI errors for server-unreachable
  vs repo-not-found vs repo-not-indexed. `--dry-run` produces a per-file
  diff (`[CREATE]`/`[MODIFY]`/`[SKIP — user-modified]`) with reasons
  inline.

- **`internal/skillcard/` package** (`a6a532b`). Pure generation logic
  with native input types (no wiki imports). Files: `generator.go`,
  `warnings.go` (heuristics), `render.go` (style guide enforced in a
  comment at top: no filler, no document-shaped headings, only graph-
  derived facts), `writer.go` (marker-based idempotent CLAUDE.md merge
  with orphan-marker safety and `--force` recovery), `mcpjson.go`
  (`.mcp.json` merge with conflict-on-different-command abort + invalid-
  JSON backup), `sidecar.go` (v0→v1 migration), `gitignore.go`
  (idempotent patch), `diff.go` (dry-run output).

- **Cluster API extended with packages and warnings** (`a6a532b`). The
  `GET /api/v1/repositories/{id}/clusters` endpoint computes per-cluster
  packages and call-graph warnings server-side using
  `skillcard.DeriveWarnings`, so the setup CLI can render insight-rich
  CLAUDE.md without re-fetching the full call graph itself. Each warning
  carries a `symbol`, `kind`, and human-readable `detail`.

- **Discoverability surfaces for the Claude Code integration**
  (`a6a532b`). Three closure points so users find the command:
  (1) post-`sourcebridge index` CLI hint prints the exact
  `setup claude` invocation with the resolved repo ID and a thousands-
  separator symbol count, (2) "Use with Claude Code" card on the repo
  Settings tab in the web UI (capability-gated on `agent_setup`) with a
  one-click copyable command, (3) "Using with Claude Code" section in
  the README linking to Anthropic's CLAUDE.md memory docs.

- **Citation contract unified across all report paths** (`f48ac47`). New
  `internal/citations` package — `Citation` struct, `Format()`, `Parse()` —
  replaces ad-hoc formatters in QA, MCP accessors, and the VS Code plugin.
  Every surface now emits `(path:start-end)` or `sym_<id>`; the plugin
  handles both `path:line` and `path:start-end` ranges via
  `citationToFileLocation`.

- **Quality framework** (`4411950`). Eight validators (`vagueness`,
  `empty_headline`, `code_example_present`, `citation_density`,
  `reading_level`, `architectural_relevance`, `factual_grounding`,
  `block_count`) with per-template profiles per audience
  (engineer / product / operator). Gates vs. warnings are configured
  per-template — ADRs don't need the same citation density as API ref pages.
  Gate failure triggers one retry with the rejection reason injected into the
  prompt; second failure excludes the page and surfaces the reason in the PR
  description.

- **Page dependency model + canonical Page AST** (`f4f40e1`). Every
  generated page carries a typed `DependencyManifest` (paths, symbols,
  upstream/downstream packages, `dependency_scope`, `stale_when` conditions)
  so the system knows exactly which pages to regenerate when a diff lands.
  Pages are internally a tree of typed blocks with stable IDs — sticky to
  logical position, not derived from content — and four-state ownership
  (`generated`, `human-edited`, `human-edited-on-pr-branch`, `human-only`).
  Per-sink overlay storage keeps sink-divergent edits out of the canonical
  AST without losing them.

- **Edit governance** (`826d8b2`). Three per-sink edit policies:
  `local_to_sink` (edit stays in that sink's overlay; canonical unchanged),
  `promote_to_canonical` (edit syncs back and propagates to all sinks), and
  `require_review_before_promote` (edit opens a sync-PR). Default policy per
  sink kind: `promote_to_canonical` for git-repo sinks (PR review already
  happened), `require_review_before_promote` for GitHub/GitLab built-in
  wikis, `local_to_sink` for Confluence/Notion. Full audit trail on every
  promotion and sync-PR disposition.

- **Glossary, Activity Log, and ADR templates** (`64dbc51`). Three
  auto-extracted doc templates:
  - *Glossary* — zero-LLM, one entry per exported symbol from the graph,
    deterministic, updates on reindex.
  - *Activity log* — commit-graph bucketed by author and week; optional
    LLM weekly-digest pass behind a config flag.
  - *Decision records* — detects `decision:`/`adr:` commit prefixes and
    `BREAKING CHANGE:` bodies; single LLM pass in ADR format (Context /
    Decision / Consequences).

- **Cold-start to PR in ≤ 90s** (`ad2096e`). `living_wiki` report type with
  `git_repo` sink: generator produces a `proposed_ast`, `markdown_writer`
  renders it, SourceBridge opens a `wiki: initial generation (sourcebridge)`
  PR. On merge, `proposed_ast` promotes to `canonical_ast`. On rejection,
  `proposed_ast` is discarded and cold-start retries on next push. Direct-
  publish mode (skip the review gate) is an opt-in per-repo setting.

- **Two-watermark incremental updates with additive commits** (`da2a7b6`).
  Two markers per repo: `source_processed_sha` (last commit the generator
  ran for) and `wiki_published_sha` (last merged-wiki baseline). Incremental
  runs diff against the published baseline, not the unmerged PR head.
  Subsequent pushes while a PR is open append a new commit
  (`wiki: incremental update (<sha>)`) to the existing PR branch — no
  force-push, no orphaned inline comments, no overwritten reviewer tweaks.
  Reviewer commits to the PR branch mark affected blocks
  `human-edited-on-pr-branch` in `proposed_ast`; subsequent bot commits
  leave those blocks alone.

- **GitHub/GitLab wiki and static-site sinks** (`3f7f252`). `github_wiki`
  and `gitlab_wiki` sinks push AST → markdown to the repo's built-in wiki
  (no PR gate; configurable 24h delay queue). Static-site sinks:
  `backstage_techdocs`, `mkdocs`, `docusaurus`, `vitepress` — all use the
  same `markdown_writer` path as `git_repo`, different output paths. Stale-
  detection banners rendered as native callouts per sink: top-of-page
  blockquote (markdown), `info` macro (Confluence), `callout` block
  (Notion).

- **Confluence and Notion API sinks** (`8f5d64c`). `confluence_writer`
  emits AST → Confluence storage XHTML with block IDs preserved as
  `ac:macro` parameters; pages reconciled by `external_id` in Confluence
  metadata. `notion_writer` emits AST → Notion block API with block IDs as
  `external_id` properties. Both perform block-level reconciliation on each
  write — only changed generated blocks are updated; human-edited blocks are
  left alone.

- **Post-merge sink reconciliation + page migrations** (`ad892d4`). After a
  wiki PR merges, the orchestrator reconciles all sinks: sink-overlay blocks
  compose `canonical + overlay[sink]` on render; `human-edited` blocks
  promoted from the PR branch are frozen in canonical. Explicit
  `BlockMigration` ops (moved / split / merged / renamed) surface in PR
  descriptions so reviewers can see what restructuring happened without
  diffing the whole file.

- **Real infrastructure adapters wired** (`822e1de`, `5622cd2`, `84baa65`):
  - `GraphMetricsProvider` backed by the existing `graph.GraphStore` — page
    IDs of the form `<repoID>.arch.<pkg>` map to package paths; replaces the
    `ConstGraphMetrics` test stub in production.
  - `DiffProvider` and `ExtendedRepoWriter` via `os/exec` git CLI calls,
    matching the pattern in `internal/git/local.go`. SHA-not-found signals
    from stderr trigger the force-push recovery path.
  - HTTP clients for GitHub, GitLab, Confluence, and Notion — all stdlib
    `net/http`, no SDK dependencies. Rate-limit headers honored on GitHub
    (`X-RateLimit-Remaining/Reset`) and GitLab; bounded exponential backoff
    on 5xx and 429. GitHub branch commits use the five-step Git Data API
    dance; GitLab uses the repository-commits `actions` array for atomic
    multi-file commits.
  - Webhook event dispatcher with per-repo goroutine serialization
    (different-repo events run concurrently, same-repo events never overlap)
    and fixed-capacity LRU idempotency by delivery ID.
  - `POST /webhooks/confluence` validates `X-Confluence-Signature`
    (HMAC-SHA256) and maps `page_updated` events to the dispatcher.
  - `POST /webhooks/notion-poll` accepts poll-trigger events from the Notion
    integration.

- **UI-configurable living-wiki settings** (`0cf3108`). Full settings page
  at `/settings/living-wiki` with progressive disclosure: general settings
  (enabled toggle, worker count, event timeout) visible by default;
  integration sections (GitHub, GitLab, Confluence, Notion) and webhook
  secrets collapsed until needed. Seven secret fields stored with field-level
  encryption; the API returns `"********"` for any set secret — clients can
  replace or clear but never read back plaintext. Test-connection buttons for
  each integration. Precedence: UI value > env var > default, so existing
  env-var deployments keep working without migration.

### Fixed

- **CLI integration tests** (`16fdba6`). `TestCLIReview*` and `TestCLIAsk*`
  were invoking `uv run python` without `SOURCEBRIDGE_TEST_MODE=1` in the
  subprocess environment, so each test spawned a real Anthropic API call and
  exited 1 with an auth error. The `FakeLLMProvider` in
  `workers/common/llm/fake.py` was already designed for this case but the
  env var was never wired into `cmd.Env`. New `testEnv(extras...)` helper
  adds `SOURCEBRIDGE_TEST_MODE=1` to the inherited environment; new
  `requireUV(t)` calls `t.Skip()` explicitly when `uv` is not on PATH so
  contributors without a Python environment don't see silent failures. All
  six tests now pass.
- **Non-deterministic API reference symbol order** (`822e1de`). The API
  reference template iterated over a Go map (`byPkg`), producing different
  symbol orderings across runs and phantom diffs in
  `samples/wiki-example/`. Now iterates over the already-sorted package
  slice and emits symbols sorted by name within each package.

### Changed

- **Telemetry platform override** (`8dfb80b`). New
  `SOURCEBRIDGE_TELEMETRY_PLATFORM` env var lets dev and CI installs
  override the auto-detected platform string in telemetry pings. Set it to
  `test` and the collector's auto-flag rule excludes the install from public
  counts — replaces the per-install "remember to call mark-test after every
  fresh install" workflow with a one-liner at deploy time. `resolvePlatform()`
  falls back to `runtime.GOOS/GOARCH` when the var is unset; default
  behavior is unchanged.

---

Theme: **MCP as a first-class client surface.** SourceBridge's capabilities
are now exposed through a complete Model Context Protocol server — not the
minimum-viable handful, but the full retrieval/analysis/lifecycle surface a
serious coding agent needs. Three phases, 19 tools, a capability registry
that's the single source of truth across MCP + GraphQL + REST, structured
errors, cursor pagination, compound workflows, and real intra-agentic-loop
progress events streaming through SSE.

### Added

- **MCP Phase 1a — accessor tools.** `get_callers`, `get_callees`,
  `get_file_imports`, `get_architecture_diagram`, `get_recent_changes`.
  Each takes `{repository_id, file_path, symbol_name, line_start?}` and
  returns the structured graph projection the UI uses, now directly
  addressable by an agent.
- **MCP Phase 1b — intent-shaped tools.** `get_tests_for_symbol` merges
  persisted `RelationTests` edges, filename-adjacency heuristics, and
  text-reference scans into a single result set where each hit is tagged
  with `match_sources: []` so the agent can see which signals agreed.
  `get_entry_points` classifies entries in both `basic` and
  `framework_aware` modes (Grails controllers, FastAPI routers, Go
  `http.Handler`, Next.js `app/api/.../route.ts`). `get_recent_changes`
  symbol-scopes `git log -L`.
- **MCP Phase 2 — workflow tools.** `review_diff_against_requirements`
  takes a commit range or explicit file list, resolves touched symbols,
  and cross-references linked requirements to flag public symbols with
  no coverage. `impact_summary` composes callers + tests + linked
  requirements in one call. `onboard_new_contributor` returns a ranked
  reading list of entry points with cliff notes and recent-activity
  authorship. Server-side composition so clients aren't forced to
  orchestrate 4–6 round-trips.
- **MCP Phase 2.2 — prompts surface.** `prompts/list` + `prompts/get`
  expose three curated workflows as MCP prompts for clients that prefer
  them over tool composition.
- **MCP Phase 2.3 — cursor pagination.** Shared `encodeCursor` /
  `decodeCursor` / `paginateSlice[T]` helpers return opaque base64 JSON
  cursors. Every list-returning tool now carries `{total, next_cursor}`.
- **MCP Phase 2.5 — real agent-loop progress events.** The agentic
  QA loop emits structured `planning` / `tool_call` / `tool_result` /
  `synthesizing` / `done(reason)` events through a new `qa.ProgressEmitter`;
  a `contentEmitterProgressAdapter` bridges them to the MCP SSE path so
  streaming clients see live `[agent] → search_evidence` /
  `[agent] ← search_evidence (231ms)` markers instead of a 30s blank wait.
- **MCP Phase 2.6 — structured error envelope.** `{ isError: true, content,
  _meta.sourcebridge: { code, remediation } }` — vanilla MCP clients still
  get a readable text body; capability-aware clients get a machine-actionable
  code (`SYMBOL_NOT_FOUND`, `REPOSITORY_NOT_FOUND`, `INVALID_CURSOR`, …) and
  a concrete next step. Back-compat safe.
- **MCP Phase 3 — capability registry.** `internal/capabilities/registry.go`
  is the single source of truth for what's available in which edition.
  Drives the MCP `tools/list` filter, the `initialize` response
  (`experimental.sourcebridge.features`), GraphQL `__schema` gating, and
  REST config responses. Test suite enforces no duplicate names, every
  capability has at least one edition, and no tool is gated by two
  capabilities.
- **MCP Phase 3.2 — indexing lifecycle tools.** `index_repository`,
  `get_index_status`, `refresh_repository`. Remote git URLs now flow
  through the extracted `internal/indexing.Service` end-to-end — clone,
  parse, persist, without the GraphQL resolver as the critical path.
- **MCP Phase 3.4 — `get_cross_repo_impact`** (enterprise). Hidden on OSS
  editions via the capability registry, visible and functional on
  enterprise.
- **RelationTests edge persistence.** The indexer now emits
  `test_for` edges during the resolve pass instead of recomputing them
  at query time. `Store.GetTestsForSymbolPersisted` exposes the cached
  view; `get_tests_for_symbol` uses it as one of three merged sources.
- **Shared `internal/indexing.Service`.** Import and reindex logic lifted
  out of the GraphQL resolver so MCP, CLI, and future surfaces share one
  path. Exposes `IsGitURL`, `NormalizeGitURL`, `GitCloneCmd`,
  `sanitizeRepoName`, `deriveRepoName` as supporting helpers.
- **Compliance collector wiring.** GitHub platform collector now composes
  into the compliance orchestrator via a routes adapter; the
  `/api/v1/compliance` surface is mounted under the enterprise router
  group and wrapped in JWT + tenant middleware.

### Changed

- **GraphQL + REST edition checks migrated to the capability registry.**
  Direct `cfg.Edition == "enterprise"` comparisons in
  `internal/api/graphql/resolver.go` and `internal/api/rest/llm_config.go`
  are now `capabilities.IsAvailable("per_op_models",
  capabilities.NormalizeEdition(...))`. Reduces the risk of gate drift as
  new capabilities land.
- **`ask_question` now streams.** The `slowToolNames` allowlist in
  `internal/api/rest/mcp_progress.go` gained `ask_question` so the
  streamable-HTTP path triggers and the adapter has a channel to push
  agent-loop events onto.

### Removed

- Nothing shipping. 20 commits of additive surface.

## [0.9.0-rc.2] — 2026-04-23

Second prerelease. Two material changes on top of rc.1: a reliability
fix for rolling deploys and a quality win on ownership questions.
Paired benchmark vs the Phase-3 agentic baseline now shows
**+5.83% overall** (65.83% → 71.67% useful-rate) and a **+8%**
gain on ownership, up from the **-8% regression** rc.1 shipped with.

### Fixed

- **Split-brain agentic deployment on rolling rollouts.** Under a
  rolling deploy, the API pod could probe the worker's capability
  endpoint before the worker Pod reached Ready, fail, and stay on
  the single-shot path for its entire lifetime. The sibling pod,
  probed seconds later, would wire agentic normally — so 50% of
  traffic silently routed to single-shot until a manual restart.
  The startup probe now retries up to 6× with 5s backoff (30s
  window), so both pods converge on the same capability state
  and benchmark samples land on the intended code path.
- **Ownership-class fabrication from advisory file candidates.**
  The smart classifier's `file_candidates` hint field was being
  treated as a citation anchor by the synthesis turn on ownership
  questions, which rewarded "plausible-sounding but unverified
  path" citations and scored as fabrication. Fix: render
  `file_candidates` only for classes that benefit from a seed
  entry file (architecture, execution_flow, cross_cutting) — the
  classes where the model would otherwise have to search from
  scratch. Ownership and behavior questions now see only symbol
  and topic hints, which work as search queries rather than
  citation anchors.

### Changed

- **Quality: ownership 76% → 84% (+8%)**, cross_cutting 56% → 60%
  (+4%), overall 65.83% → 71.67% (+5.83%) on the 120-question
  benchmark (Opus-4.7 judge). Architecture stays at 84%.
  Execution_flow 80% → 76% (-4%, one question, within noise).
- Prompt-cache read ratio of 99.6% on the benchmark run; ~70%
  input-token cost savings vs pre-cache.

Full benchmark report at
`benchmarks/qa/reports/2026-04-23_surgical_v2_vs_agentic/report.md`.

## [0.9.0-rc.1] — 2026-04-23

Theme: **ask smarter, not harder.** A new agentic retrieval loop, a
server-side deep-QA orchestrator, and a hybrid search backbone plugged into
the deep pipeline. Measured quality gains on a 120-question parity
benchmark with LLM-as-judge.

### Added

- **Agentic retrieval loop.** Phases 0–4.5 ship a tool-dispatching agent
  synthesizer that swaps passive retrieval for an explicit plan → call
  tools → cite answer loop. Tools include `search_evidence`,
  `find_tests`, and a query decomposition pre-pass. The agent
  capability probe runs unconditionally at startup and is wired into
  the REST server. Paired-benchmark result vs Phase-3 baseline:
  **+10.00% overall quality**, with another **+3.33%** added by the
  Phase-5 quality push.
- **Anthropic prompt caching on the agentic loop** (quality-push Phase 1)
  — repeated tool-call framing is cached across turns, cutting token
  cost without changing answer fidelity.
- **Smart classifier + seed-context routing** (quality-push Phase 2) —
  the classifier picks a retrieval strategy per question class instead
  of running the full pipeline for every query.
- **`find_tests` agent tool** (quality-push Phase 3) — lets the agent
  pull in the test file that exercises a symbol when the question is
  about behavior, not structure.
- **Query decomposition pre-pass** (quality-push Phase 4) — gated to
  architecture-class questions where sub-question routing actually
  helped the judges; skipped on everything else to avoid latency
  churn.
- **Server-side deep-QA pipeline.** A new `internal/qa` orchestrator
  runs the deep ask flow on the API side with readiness gating and a
  CTA fallback when the pipeline can't complete. Exposed as a
  GraphQL `ask` mutation, a `POST /api/v1/ask` REST endpoint, and an
  MCP `ask_deep` tool. CLI auto-picks the server path when
  `/healthz` advertises QA. The old `cli_ask.py` deep mode now prints
  a deprecation warning.
- **Deep pipeline uses hybrid search.** The deep-QA path now calls
  the hybrid `search.Service` (Phases 1–6 from the prior release) as
  its retriever instead of the legacy grep path — requirements,
  files, symbols, and signals all flow through one ranked backbone.
- **QA parity benchmark.** 120 curated questions across architecture,
  execution flow, domain concepts, and requirements grounding, with
  an LLM-as-judge runner (`benchmarks/qa/`). Baseline vs candidate
  arms, per-question judgments, per-arm environment capture, and a
  rollup report. Seed script + per-question repo-path mapping let
  the candidate run inside a k8s worker pod or against a remote
  instance.
- **Fallback-compat CI lane** — a dedicated workflow that exercises
  the pipeline with the agentic loop disabled so the fallback path
  can't regress silently.
- **Ops docs** — `docs/admin/server-side-qa-rollout.md` with staged
  canary instructions and rollout decisions finalized (Q5.6 / Q6.1 /
  Q7.1); `docs/admin/telemetry-collector-qa-fields.md` for the
  collector-side field additions that QA adoption needs.

### Fixed

- **`find_tests` schema**: Anthropic's API rejects `anyOf` at the
  `input_schema` root. The tool definition now expresses the variant
  shape without the root-level union so cloud and local models
  accept the same schema.
- **Smart-classifier fabrication**: dropped `file_candidates` from
  the classifier's seed prompt, which was inviting the model to
  invent plausible-but-non-existent file paths.
- **Agentic deadlines** iteratively tuned — **60 s / 30 s** first,
  then **90 s / 45 s** wall/per-turn after the initial setting
  truncated legitimate long answers. Decomposition sub-loop budget
  bumped **30 s → 60 s**.
- **Agentic `search_evidence` init order** — the tool was being
  registered before the search service was ready on startup; moved
  service init before QA wiring so the tool is usable on the first
  request.
- **Citation fallback widened** to scan every tool-result turn, not
  just the final turn, so an answer stitched together from earlier
  tool calls still carries the evidence citations forward.

### Changed

- **Decomposition gate narrowed to architecture-only** (post-Phase 5).
  The quality-push evaluation showed decomposition helped
  architecture questions but hurt execution-flow and concrete
  questions; the gate now reflects that.
- **Default prod posture** recorded in
  `thoughts/shared/plans/` as the surgical config — Phase-5
  decomposition + architecture gate + agentic loop + hybrid search
  is the baseline unless overridden.
- **Q5.1–Q5.6 deep-QA migration series** — `discussCode` context
  ported into the orchestrator, GraphQL `ask` adapter added,
  synthesis routed through the LLM job orchestrator, telemetry
  fields reserved for QA adoption, CLI and REPL re-pointed at the
  server path.

### Infrastructure

- `.claude/scheduled_tasks.lock` added to `.gitignore`.
- Judge docs pointed at the canonical
  `automation/anthropic-api-credentials` secret path used by other
  benchmarks.
- QA benchmark reports live in `benchmarks/qa/reports/` alongside
  the runner output so rollouts can diff arms over time.

---

## [0.8.0-rc.1] — 2026-04-21

Release candidate for 0.8.0. Theme: **token streaming end-to-end**, first-class
requirement CRUD, and the VS Code extension relocated into this repository
so the full stack lives in one place.

### Added

- **Streaming discussion answers** via a new `AnswerQuestionStream` gRPC
  alongside the existing unary `AnswerQuestion`. The worker yields LLM
  content deltas as they're generated; the API relays them through
  **two delivery surfaces**:
  - **MCP**: `explain_code` progress notifications carry a `delta` field
    that the VS Code plugin appends to the running answer in real time.
  - **REST SSE**: new `POST /api/v1/discuss/stream` endpoint emits
    `event: token` / `event: done` / `event: error` frames. The web UI's
    Discuss page consumes them through `src/lib/askStream.ts`.
  No more 30–90 s of "Thinking…" on a local model — users see tokens as
  they land.
- **Requirement CRUD** mutations on GraphQL: `createRequirement` (with
  auto-generated `REQ-<uuid>` external IDs and uniqueness enforcement) and
  `updateRequirementFields` (partial patch semantics — explicit nulls
  clear fields, omitted fields preserve them). Matching web flows:
  CreateRequirementDialog, inline EditRequirementCard, RemoveRequirementDialog.
  `acceptanceCriteria` round-trips through the full stack.
- **VS Code extension (0.3.0)** now lives in `plugins/vscode/`. Packaged via
  `make package-vscode` / `make install-vscode`. Features: code-action
  lightbulbs, `Cmd+I` streaming chat, `Cmd+K N` field guides, `Cmd+Shift+;`
  scoped palette, Change Risk sidebar tree, inline requirement CRUD,
  opt-in telemetry, ARIA labels throughout. Status-bar connection indicator
  with auto-reconnect.
- **Namespace-local Redis** support for MCP session storage (HA across
  replicas). Enterprise deployment now ships its own Redis manifest so
  MCP sessions don't collide with OSS.

### Fixed

- **Qwen thinking-disable on Ollama**. The previous implementation only
  sent the llama.cpp-specific `chat_template_kwargs={"enable_thinking":
  False}`, which Ollama ignores — Qwen 3.5 MoE burned its entire
  `max_tokens` budget inside an unemitted thinking block and returned
  empty content with `stop_reason=length`. Now also sends the `/no_think`
  directive in the user message for Qwen-family models; both backends work
  without runtime detection.
- **Orchestrator stale-inflight claim release**. An API pod that failed a
  job in-memory during a worker-pod startup race kept the dead job's ID
  in its inflight registry, so every identical request deduped to that
  failed job forever until the pod restarted. `Enqueue` now detects
  terminal states on claim collisions and retries with a fresh job.
- **arm64 release binaries** now build on a native `ubuntu-24.04-arm`
  runner. Cross-compiling from amd64 with `CGO_ENABLED=1` was failing on
  tree-sitter's arm64 assembly.
- **Release packaging** skips the `*.dockerbuild` provenance artifact so
  the release job doesn't retry 5× on a flaky artifact download.
- **SemVer prerelease tags** (hyphenated suffixes like `-rc.1`, `-beta.2`,
  `-alpha`) are now auto-flagged as prerelease on GitHub.

### Changed

- **Trash retention worker** matured into Phase 1 complete — telemetry,
  lint clean, SurrealDB round-trip fixed (dropped the unsupported COMMIT
  wrap; fixed the SCHEMAFULL NONE-field backfill trap in migration 030).
- **Knowledge artifact regeneration** is now delta-driven: a shadow
  pipeline computes which scopes' evidence actually changed on reindex
  and only those are flagged stale. Replaces the scorched-earth
  full-rebuild behavior.
- **Selective knowledge-artifact invalidation** on reindex — unchanged
  scopes stay READY.
- **GraphQL extension timeouts** raised to 180 s for LLM operations
  (`DiscussCode`, `ReviewCode`, `ExplainSystem`, `GenerateCliffNotes`,
  `GenerateLearningPath`, `GenerateCodeTour`). Previous 10 s ceiling was
  cutting off the server mid-response.

### Infrastructure

- Enterprise Dockerfiles live in-tree so Tekton builds them alongside
  OSS images without a parallel repo.
- Web, worker, and Go CI pipelines now all pass cleanly on the same push.

---

## [0.7.0] — 2026-04-19

**Runtime reconfiguration and API cleanup.**

### Added

- **Runtime orchestrator reconfiguration** — change `MaxConcurrency` on a
  live instance without a restart. The Admin Monitor page surfaces a
  provider-aware recommended value based on the model size + hosting
  mode, and `handleUseRecommended` wires the chosen value into the orchestrator.
- **Provider-aware concurrency recommendations** — `MaxConcurrency`
  suggestion per provider × model class (small local, large local MoE,
  cloud API), calibrated against the bench harness.
- **Enterprise report RPC shim** — reserves the wire surface for the
  commercial report-generation feature without the OSS build carrying
  any of the enterprise logic.
- **Knowledge proto dual-field enums** — additive, deprecation-friendly
  replacements for the legacy string-encoded enum fields. GraphQL
  deprecations flag the old names.
- **Article addendum infrastructure** for benchmark write-ups, including
  a top-5 sweep across `learning_path` / `code_tour` / `workflow_story`
  and a parallel other-artifacts harness for slow local models.

### Fixed

- **Worker CI and release pipeline** — mypy debt cleared, Go data races
  fixed, Python lint clean, unused code removed.
- **Hallucination scorer** — no more trailing-slash false positives on
  path citations.
- **Workflow story generation** — raised `max_tokens` to match DEEP
  artifacts elsewhere so long walks don't truncate.
- **Path filter** accepts known-directory citations (previously rejected
  directory-only paths as hallucinations).
- **Shared knowledge parsing helpers** promoted to a reusable module so
  each generator doesn't reinvent the same regex dance.

### Changed

- **Frontend auth fetch paths consolidated** — all API calls now go
  through one helper with consistent header handling and error
  classification.
- **Error handling and shutdown paths tightened** — graceful drains,
  fewer leaked goroutines, cleaner logs.

---

## [0.6.0] — 2026-04-16

**Telemetry, Docker Hub, and community infrastructure.**

### Added

- **Anonymous install telemetry** from the Go API to
  `https://telemetry.sourcebridge.ai/v1/ping`. Opt-out respected; provider
  kind and version reported. Minimal dashboard for the maintainers to
  understand deployment spread.
- **Docker Hub distribution** — `docker compose up -d` with the official
  `sourcebridge/sourcebridge-api` image is now the recommended quickstart.
- **Community files** — `CODE_OF_CONDUCT.md` (Contributor Covenant),
  `SECURITY.md`, CI lint fixes, standardized issue templates.
- **Fast / Deep repo QA modes** — the CLI and REPL `ask` command now
  exposes two grounding profiles so casual queries don't pay for full
  repo context unnecessarily.

### Fixed

- CLI `ask` grounding quality — better evidence selection, fewer
  hallucinations, clearer error surfaces.
- Local compose and CLI AI paths — several configuration mismatches
  between `dev` and `compose` environments.
- Telemetry version reporting previously stamped "dev" even on tagged
  builds.

### Changed

- Removed the hosted telemetry service from the OSS repo (it lives in
  its own collector repo now, so this repo has no server code it
  shouldn't).
- Benchmark and demo seed data excluded from the OSS distribution to
  keep the repo lean.

---

## [0.5.0] — 2026-04-14

**First-run demo experience.**

### Added

- **`./demo.sh`** — one command that starts SourceBridge, indexes a
  44-file sample `acme-api` TypeScript project, and generates cliff
  notes, code tours, and architecture diagrams. Drops new users directly
  into a fully-populated workspace without a long cold index.
- **Going-to-production guide** in `docs/` with backup / restore,
  capacity planning, and hardening checklists.
- **Screenshots in README** — overview, cliff notes, search, generation
  queue — so people can see what SourceBridge does before installing.

### Fixed

- **OSS worker logging** — job lifecycle events now emit correctly with
  the expected structure.
- **Worker Surreal fallback** — handles the "DB not reachable at startup"
  case without panic.
- **Viewport layout** — page-level scroll removed when the shell grid is
  active; the sidebar and main column now scroll independently. No more
  double scrollbars.

---

## [0.4.2] — 2026-04-13

Minor follow-up to 0.4.1 with a handful of persistence fixes.

### Added

- **Saved generation-mode overrides** per scope, so a repo set to DEEP
  for cliff notes doesn't forget across restarts.
- **Repeatable generation-mode benchmark harness** for regression
  testing model swaps.

### Fixed

- Generation-mode persistence race on rapid scope switches.
- Benchmark hardening: flake-free sampling, consistent seeding, fair
  provider comparisons.

---

## [0.4.1] — 2026-04-13

**Knowledge generation reliability + queue visibility.**

### Added

- **Prioritized refinement and generation-mode controls** — the queue
  now favors interactive work over maintenance sweeps; repo-level
  reindex no longer starves user-triggered cliff notes.
- **Monitor rollup for reused summaries** — aggregate cache-reuse stats
  surface on the Admin Monitor page so operators see how much work is
  served from the summary cache vs. regenerated.
- **Cache-reuse stats as first-class job fields** (leafHits, fileHits,
  packageHits, rootHits). Previously buried in message strings; now
  queryable via GraphQL and visible on every job card.
- **Knowledge timeouts driven by app config** — operators can tune the
  per-scope ceilings without recompiling.

### Fixed

- **Summary-node cache writes** — race between writers caused the cache
  to silently drop hot entries under load.
- **Queued knowledge jobs heartbeat** behind slot gates so the reaper
  doesn't mark legitimately-waiting jobs as stale.
- **Noisy repo segmentation** — the monitor's health signals got
  confused when a single repo had hundreds of sibling scopes. Segmented
  so each repo gets its own bucket.

### Changed

- **Understanding-first artifact rendering** — the field guide view
  now leads with the repository-understanding score and derived
  recommendations rather than a flat list of generated artifacts.
- **Vector-based logo** across all assets (dashboard, README, docs).

---

## [0.4.0-pre-report-pipeline] — 2026-04-10

Preview checkpoint for the reports feature-flag work. Not a normal release.

### Added

- Reports feature plan committed to `thoughts/` — professional
  multi-repo report generation, audience targeting, evidence system,
  appendices, level-of-effort estimation, PDF rendering. No runtime
  behavior yet; this tag marks the start of the implementation arc.

---

## [0.3.1] — 2026-04-10

### Changed

- **Reports feature moved to enterprise-only.** OSS ships without the
  report-generation path; the enterprise build re-injects it via the
  `MCPToolExtender` / enterprise-routes hook.

---

## [0.3.0] — 2026-04-10

**Comprehension engine polish and confidence honesty.**

### Added

- **Deep-mode cliff notes** use repo-level analysis when generating a
  scoped (file or symbol) cliff note — scoped output now has access to
  the full repo understanding, not just local evidence.
- **Deep-mode workflow stories** also inherit cliff-notes analysis, so
  walk-throughs cite the same evidence the summary cited.
- **Bulk repository import** — paste a list of URLs to import many
  repos in one go.

### Fixed

- **Test coverage 100% bug** — the understanding-score calc clamped
  coverage to 100% even when fewer tests existed than symbols.
- **Confidence rules for cliff notes** — summaries ARE direct evidence
  (they were being treated as derivative, inflating the confidence
  badge on citation-light repos).
- **Progress-bar advancement** during generation for every artifact
  type (several types had been stuck at 0% until completion).
- **Workflow story richness** — higher base confidence, fuller content
  blocks, fewer null-field crashes (`entry_points` null-safety, full
  tracebacks in error logs).
- **Render prompt rewrite** — richer output, fewer in-flight flickers
  when a job is mid-generation and another poll arrives.
- **Refresh buttons** for code tour / learning path actually call
  `refreshArtifact` (they were previously no-ops on the UI side).
- **Stale job reaper** also marks linked artifacts as failed, so a
  stuck job doesn't leave a zombie artifact that looks READY in the UI.
- **Null-safety** across artifact dict lookups (`.get(key, []) or []`
  pattern applied consistently).

### Changed

- **Understanding-score horizontal layout** — fits better on narrow
  repo-detail cards and reads left-to-right.

---

## [0.2.0] — 2026-04-10

**Comprehension Engine + production hardening.**

This release absorbed two months of comprehension-engine work, the
multi-phase summary-tree rollout, and the initial production-grade
hardening pass (53 commits over the 0.1.0-alpha baseline).

### Added

- **Hierarchical summary tree** — leaf / file / package / root
  comprehension layers with per-level max-token budgets and evidence
  propagation.
- **Scoped field-guide generation** — cliff notes / learning paths /
  code tours / workflow stories at any scope (repo / file / symbol /
  requirement).
- **Generation mode picker** — Fast vs. Medium vs. Deep, per scope,
  with live token and latency estimates.
- **Admin Monitor page** with the LLM job queue, live generation
  progress, reuse stats, and a breaker for runaway providers.
- **Semantic search** against the repository graph, grounded in the
  indexed symbol vectors.

### Fixed

- Initial production-grade reliability pass: retries, context-cancel
  plumbing, bounded goroutines, graceful shutdown, breaker on
  consecutive compute failures.
- Null-safety, type-narrowing, and traceback surfacing across the
  worker's generation codepaths.

---

## [0.1.0-alpha] — 2026-04-03

**First public release.**

Initial alpha: repository indexing via tree-sitter, a gRPC worker with
Ollama / OpenAI / Anthropic LLM providers, a GraphQL API, a Next.js
web UI, and the bones of the cliff-notes generation pipeline. Enough
to demo; rough at the edges, with production hardening explicitly
deferred to 0.2.0.

[0.8.0-rc.1]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.8.0-rc.1
[0.7.0]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.7.0
[0.6.0]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.6.0
[0.5.0]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.5.0
[0.4.2]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.4.2
[0.4.1]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.4.1
[0.4.0-pre-report-pipeline]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.4.0-pre-report-pipeline
[0.3.1]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.3.1
[0.3.0]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.3.0
[0.2.0]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.2.0
[0.1.0-alpha]: https://github.com/sourcebridge-ai/sourcebridge/releases/tag/v0.1.0-alpha
