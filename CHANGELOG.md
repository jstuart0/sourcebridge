# Changelog

All notable changes to SourceBridge are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
