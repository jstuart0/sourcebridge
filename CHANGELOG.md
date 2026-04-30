# Changelog

All notable changes to SourceBridge are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
