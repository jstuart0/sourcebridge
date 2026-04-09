# Cliff Notes Generation — UX & Reliability Enhancement Plan

**Date:** 2026-04-09
**Author:** Jay Stuart (drafted with Claude)
**Status:** SUPERSEDED by [2026-04-09-comprehension-engine-and-llm-orchestration.md](./2026-04-09-comprehension-engine-and-llm-orchestration.md)
**Scope:** Go API (`internal/`, `cmd/`) · Python worker (`workers/`) · Web UI (`web/`) · Proto (`proto/`)

> **Note:** This plan was an early, cliff-notes-focused draft. After a scoping discussion we decided to broaden it into a corpus-agnostic comprehension engine with pluggable strategies (hierarchical summarization, RAPTOR, GraphRAG, long-context) and model-capability-aware configuration. See the superseding plan linked above. The findings and root-cause analysis below are still valid and are referenced from the new plan.

## Overview

Cliff Notes (and sibling knowledge artifacts — learning path, code tour, workflow story) are currently failing in ways that look like silent success to end users. The deployed instance on the `thor` cluster exhibits three visible symptoms:

1. **Progress bar barely moves** during generation, then jumps to done — users can't tell if anything is happening.
2. **Repository-level ("site as a whole") cliff notes** worked initially but now produce stub content that the UI displays as successful.
3. **"Generation failed" messages** for other scopes intermittently, with no actionable detail surfaced to users.

Additionally, the system has no concurrency control — every mutation spawns an unbounded goroutine that blocks on a single-threaded LLM (Ollama), causing cascading timeouts under load, and there is no operator-facing way to see what the generation subsystem is actually doing.

This plan addresses the root causes, ships an explicit **Generation Monitor** page, replaces the 3-step progress bar with true streaming progress, and introduces a bounded work queue with retries. It is structured in four phases so the team can ship incrementally and stop the silent-failure bleeding within a day.

## Current State Analysis

### Root causes confirmed from live thor logs (2026-04-09)

**Pod logs inspected:**
- `sourcebridge-api-664c554746-48nt9` (Go API)
- `sourcebridge-worker-784d8db9dc-mqb2f` (Python worker)

**Finding 1 — LLM returns empty content for large snapshots.** Worker is configured with `llm_provider: "ollama"`. For the `MACU Helpdesk` repo-level generation, Ollama's effective context window is exceeded and the provider silently returns an empty string. Repeating pattern in worker logs:

```
cliff_notes_parse_fallback  error="Expecting value: line 1 column 1 (char 0)"
cliff_notes_quality_metrics  sections_with_content=0  stub_sections=7
                             avg_content_length=38  inferred_sections=7
```

The pipeline then coerces the empty response into 7 "Insufficient data" stub sections and marks the artifact **READY**, so the UI shows a success state with unusable content.

**Finding 2 — Worker timeout is being hit.** API log:
```
refresh cliff notes failed  artifact_id=930da74e-...
error="rpc error: code = DeadlineExceeded desc = context deadline exceeded"
```
The 600s hard timeout in `internal/worker/client.go:36` (`TimeoutKnowledge`) is being hit on some refreshes when Ollama is saturated.

**Finding 3 — Unbounded concurrent generations for the same artifact.** API log shows three `generate_cliff_notes` calls for the same `REPOSITORY` scope within three seconds (16:11:21, 16:11:23, 16:11:24). There is no queue, no semaphore, and no in-flight deduplication.

**Finding 4 — Workflow story `NoneType` crash.** Worker log:
```
generate_workflow_story_failed  error="'NoneType' object is not subscriptable"
```
Reproducible on every retry for the same repo. Bug lives in `workers/knowledge/workflow_story.py` helpers around `_build_workflow_fallbacks`.

### Code-level findings

#### Backend (Go API)

**Mutation entry:** `internal/api/graphql/schema.resolvers.go:1305-1495` — `GenerateCliffNotes()`
- Spawns a **background goroutine** at `:1400` to do the actual work; returns immediately with `GENERATING` status.
- Emits exactly **three progress values**:
  - `0.1` at `:1402` (snapshot assembled)
  - `0.8` at `:1425` (LLM completed)
  - `1.0` implicitly at `:1479` (status → `READY`)
- On failure at `:1413-1423`: logs `cliff_notes_generation_failed`, sets status to `FAILED`, **does not persist the error message**.
- **No retry, no backoff, no in-flight dedupe.**

**Worker client:** `internal/worker/client.go:36` — `TimeoutKnowledge = 600 * time.Second` is applied uniformly regardless of scope.

**Knowledge store:** `internal/db/knowledge_store.go` — table `ca_knowledge_artifact` has columns for `status`, `progress`, `stale`, but **no `error_message` / `error_code` columns**.

**Admin status:** `internal/api/rest/admin_knowledge.go:56-113` — returns aggregate counts (total, ready, stale, generating, failed, pending) and per-repo lists, but no live activity feed, no per-artifact error detail, no timing/throughput metrics.

#### Python worker

**Servicer:** `workers/knowledge/servicer.py:81-161` — `GenerateCliffNotes` gRPC method. Uses a single unary RPC; no streaming.

**Pipeline:** `workers/knowledge/cliff_notes.py:208-324` — `generate_cliff_notes()`
- At `:106-123` (helper), snapshots over 300KB use retrieval; otherwise they get progressive condensation. **No hard ceiling.**
- At `:228-230`, a single blocking `await provider.complete(...)` call with `max_tokens=8192`.
- At `:232-246`, empty LLM responses fall through `_parse_sections("")` into a silent fabricated stub, which is the root of Finding 1.

**LLM adapter:** `workers/common/llm/openai_compat.py:98` — `content=choice.message.content or ""` coerces `None` to empty string without raising, which is what enables the silent fallback upstream.

**Concurrency:** worker uses `grpc.aio` (`workers/__main__.py:74`) so it can accept multiple concurrent RPCs, but the LLM provider is a single connection pool and Ollama serializes the underlying calls.

**Workflow story:** `workers/knowledge/workflow_story.py:369-498` — `_load_json` always returns a dict, so the `NoneType` must come from a downstream helper (likely in `_build_workflow_fallbacks` at `:140` or `_gather_execution_evidence` / `_gather_scope_evidence` callers) where a `.get()` returning `None` is later subscripted.

#### Frontend

**Repo detail page:** `web/src/app/(app)/repositories/[id]/page.tsx`
- Progress bar at `:1840-1848`:
  ```tsx
  <progress max={100} value={Math.max(currentCliffNotes.progress * 100, 5)} />
  ```
- Polling at `:299-307` — 5-second interval, `network-only`, auto-stops on terminal status.
- On failure, shows a bare **"Refresh failed"** badge at `:1820` with no error detail (because there's no `error_message` field to display).

**Admin dashboard:** `web/src/app/(app)/admin/page.tsx` — knowledge tab at `:14`, data from `/api/v1/admin/knowledge` at `:140`. Shows counts only, no live feed, no per-job drill-down.

### Key constraints / discoveries

- The LLM provider on thor is **Ollama**, which silently truncates oversized contexts. The "it worked initially" behavior for repo-level cliff notes tracks with the repo snapshot growing over time past Ollama's configured `num_ctx`.
- The system already has `provider.stream()` in `openai_compat.py:109` — streaming progress from the worker is plumbing work, not new infrastructure.
- Admin dashboard infrastructure already exists (`/admin` route, `admin_knowledge.go` REST handler) — the Monitor page can be built as a new tab or sibling route reusing the auth/layout.
- `ca_knowledge_artifact` already has `updated_at` timestamps that can drive "elapsed" calculations without schema changes.

## Desired End State

After this plan ships:

**User experience**
- Progress bar reflects actual work: moves smoothly from ~0 to 100% as the LLM generates tokens, with a human-readable phase label ("Building prompt", "Generating sections…", "Parsing").
- Failed generations show an explicit, actionable error message in the UI ("LLM returned empty response — snapshot may exceed context window", "LLM request timed out at 600s", etc.) instead of a silent success or a bare badge.
- Users can open a **Generation Monitor** page (or popover from the repo header) and see exactly what the system is doing right now, what's queued, and what has recently failed.

**Operational behavior**
- Concurrent generation requests are bounded by a configurable worker pool (default 3). Excess requests queue as `PENDING`.
- Duplicate requests for the same artifact while one is already in flight are deduplicated, not queued.
- Transient failures (deadline exceeded, empty response) retry once with backoff; non-retryable failures (snapshot too large, invalid input) fail fast.
- Repo-level, file-level, and symbol-level generations each have an appropriate timeout.

**Observability**
- Every generation persists a full error message and error code on failure.
- Admin endpoints expose live job state, recent completions, per-type timing percentiles, and worker health.
- Operators have a documented runbook for sizing Ollama's `num_ctx` per model, plus a pre-flight check that refuses to send oversized prompts.

### Verification
- Repository-level cliff notes for `MACU Helpdesk` on thor regenerate successfully or fail with a concrete error — never silently stub.
- Refreshing cliff notes while one is already generating does not spawn a second goroutine (log assertion).
- Progress bar on `/repositories/[id]` advances at least every 2 seconds during the LLM call on a medium-depth repo.
- `/admin/generation` (or equivalent) shows a live list of in-flight jobs that updates without manual refresh.
- Firing 10 concurrent `generateCliffNotes` mutations results in at most 3 in-flight LLM calls; the rest queue as `PENDING`.
- Workflow story generation for `MACU Helpdesk` completes without `NoneType` error.

## What We're NOT Doing

- **Switching LLM providers.** Ollama stays. We fix the symptoms (context sizing, empty-response handling, dedupe) that make Ollama painful, but provider selection is out of scope.
- **Rewriting the artifact data model.** We add columns, not tables.
- **Changing the GraphQL polling strategy for the repo detail page.** Phase 3 adds streaming progress via the existing polling loop; a full push-based GraphQL subscription is a later nice-to-have.
- **Building a generic job queue.** The bounded pool in Phase 4 is scoped to knowledge generation; not a general-purpose task queue.
- **Auto-scaling worker pods.** Horizontal scaling of the Python worker is out of scope; the bounded pool targets the single-worker deployment on thor.
- **Per-user rate limiting.** Fairness between users is a future concern; Phase 4 protects the LLM, not user quotas.

## Implementation Approach

Four phases, each independently shippable:

- **Phase 1 — Stop the bleeding** (1 day): make failures honest, fix the `NoneType` crash, dedupe in-flight requests, add a snapshot size ceiling. This alone resolves the "looks broken" symptom on thor.
- **Phase 2 — Generation Monitor page** (2 days): first-class `/admin/generation` route with live activity feed, plus a repo-scoped popover. Built on polling first, SSE later.
- **Phase 3 — True streaming progress** (1-2 days): new streaming gRPC method, per-phase and per-token progress updates, no frontend polling changes required.
- **Phase 4 — Concurrency & resilience** (2-3 days): bounded worker pool, scope-aware timeouts, retry with backoff, Ollama sizing runbook.

---

## Phase 1: Stop the Bleeding

**Goal:** End the silent-failure behavior today. Every failure becomes visible and actionable; duplicate requests stop thrashing the worker.

### Changes Required

#### 1. Fail loudly when the LLM returns empty content

**File:** `workers/knowledge/cliff_notes.py`

Replace the silent fallback at `:232-246` with an explicit error when the LLM returns nothing usable:

```python
# After the provider.complete() call at :228-230
if not response.content or not response.content.strip():
    log.error(
        "cliff_notes_llm_empty_response",
        repository=repository_name,
        scope_type=effective_scope,
        scope_path=scope_path,
        model=response.model,
        input_tokens=response.input_tokens,
        finish_reason=response.stop_reason,
    )
    raise LLMEmptyResponseError(
        f"LLM returned empty content for {effective_scope} scope "
        f"(input_tokens={response.input_tokens}, model={response.model}). "
        f"Likely causes: context window exceeded, provider error, or rate limit."
    )
```

Define `LLMEmptyResponseError` in `workers/common/llm/provider.py` alongside `LLMResponse`.

Apply the same guard at the analogous positions in:
- `workers/knowledge/workflow_story.py:411-426`
- `workers/knowledge/learning_path.py`
- `workers/knowledge/code_tour.py`

Retain the JSON parse fallback (`cliff_notes_parse_fallback`) only for non-empty-but-malformed responses — that's a different failure mode and the fallback section there is defensible.

#### 2. Persist and expose error messages

**File:** `internal/db/knowledge_store.go`

Add two columns to `ca_knowledge_artifact`:
- `error_message` — text, nullable
- `error_code` — string, nullable (e.g., `LLM_EMPTY`, `DEADLINE_EXCEEDED`, `SNAPSHOT_TOO_LARGE`, `WORKER_UNAVAILABLE`, `INTERNAL`)

Add a new store method:
```go
func (s *Store) SetArtifactFailed(ctx context.Context, id string, code string, message string) error
```

**File:** `internal/api/graphql/schema.resolvers.go:1413-1423`

Replace the bare `SetArtifactStatus(..., FAILED)` call with `SetArtifactFailed(ctx, id, classifyError(err), err.Error())`. Add a `classifyError` helper that maps:
- gRPC `DeadlineExceeded` → `DEADLINE_EXCEEDED`
- gRPC `Unavailable` → `WORKER_UNAVAILABLE`
- Our new `LLMEmptyResponseError` (comes across gRPC as `Internal` with known message prefix) → `LLM_EMPTY`
- Our new `SNAPSHOT_TOO_LARGE` → `SNAPSHOT_TOO_LARGE`
- Fallback → `INTERNAL`

**File:** `internal/api/graphql/schema.graphqls`

Add to `KnowledgeArtifact` type:
```graphql
type KnowledgeArtifact {
  # ... existing fields
  errorMessage: String
  errorCode: String
}
```

**File:** `web/src/app/(app)/repositories/[id]/page.tsx:1820`

Replace the "Refresh failed" badge with a clickable element that opens a dialog/tooltip showing `errorMessage` and `errorCode`. For `LLM_EMPTY` and `SNAPSHOT_TOO_LARGE`, include a helper line: "Try reducing depth to SUMMARY, or ask an admin to increase the model's context window."

#### 3. Fix the workflow story `NoneType` bug

**File:** `workers/knowledge/workflow_story.py`

Trace the path from `generate_workflow_story(..., execution_path_json="")` → `_load_json("")` → `_build_workflow_fallbacks(execution_path={})`. `_load_json` at `:31-36` already normalizes to `{}`, so the `None` must enter via a nested `.get()` inside one of the fallback helpers. Likely candidates:
- `_build_workflow_fallbacks` at `:140` — audit every `.get()` for missing default
- `_gather_execution_evidence` at `:60` — already uses `.get("steps", [])` correctly; check callers
- `_gather_scope_evidence` at `:81` — `scope = snapshot.get("scope_context") or {}` is correct

Most likely culprit: a helper that does `snapshot["something"].get(...)` assuming `snapshot["something"]` exists, when in fact the key may be present with a `None` value rather than absent. The fix is to replace `snapshot.get("key", {})` with `snapshot.get("key") or {}` throughout.

Add a regression test in `workers/tests/test_workflow_story.py`:
```python
async def test_generate_workflow_story_with_empty_inputs(fake_provider):
    result, usage = await generate_workflow_story(
        provider=fake_provider,
        repository_name="test",
        audience="developer",
        depth="medium",
        snapshot_json="{}",
        execution_path_json="",
    )
    assert result.sections  # should not raise
```

#### 4. In-flight deduplication

**File:** `internal/api/graphql/schema.resolvers.go:1336-1400`

Before claiming the artifact at `:1377`, check if the existing artifact is already `GENERATING` with `updated_at` within the last 60 seconds:

```go
if existing != nil && existing.Status == knowledge.StatusGenerating {
    if time.Since(existing.UpdatedAt) < 60*time.Second {
        slog.Info("cliff_notes_generation_deduped",
            "artifact_id", existing.ID,
            "elapsed_ms", time.Since(existing.UpdatedAt).Milliseconds())
        return toGraphQLArtifact(existing), nil
    }
    // Stale GENERATING state (worker crashed mid-job) — fall through to re-claim
    slog.Warn("cliff_notes_stale_generating_state",
        "artifact_id", existing.ID,
        "stale_for_ms", time.Since(existing.UpdatedAt).Milliseconds())
}
```

Apply the same dedupe to the sibling mutations: `generateLearningPath`, `generateCodeTour`, `generateWorkflowStory`, and `refreshKnowledgeArtifact`.

#### 5. Pre-flight snapshot size ceiling

**File:** `workers/knowledge/cliff_notes.py` (in the snapshot prep helper around `:106-123`)

After retrieval/condensation, compute the prompt token count (approximate as `len(prompt) / 4` if no tokenizer is available) and compare against a configurable ceiling:

```python
max_prompt_tokens = int(os.environ.get("SOURCEBRIDGE_MAX_PROMPT_TOKENS", "24000"))
approx_tokens = len(prompt) // 4
if approx_tokens > max_prompt_tokens:
    raise SnapshotTooLargeError(
        f"Prompt exceeds configured ceiling: ~{approx_tokens} tokens > {max_prompt_tokens}. "
        f"Scope={scope_type} path={scope_path}. Reduce depth or increase SOURCEBRIDGE_MAX_PROMPT_TOKENS."
    )
```

Define `SnapshotTooLargeError` next to `LLMEmptyResponseError`. Make the default ceiling conservative (24k tokens leaves headroom under most Ollama `num_ctx` settings). Document the env var in `config.toml.example`.

### Success Criteria

**Automated**
- [ ] `make test` passes, including the new workflow story regression test
- [ ] Unit test for `classifyError` covers all five error codes
- [ ] Unit test for the new `SetArtifactFailed` store method

**Manual (on thor after deploy)**
- [ ] Regenerate repo-level cliff notes for `MACU Helpdesk` — either succeeds or fails with `LLM_EMPTY` / `SNAPSHOT_TOO_LARGE` error code visible in the UI
- [ ] Worker logs no longer show `cliff_notes_parse_fallback` with empty response fallback path
- [ ] Clicking "Refresh" twice in rapid succession results in only one goroutine (check API logs for `cliff_notes_generation_deduped`)
- [ ] Workflow story generation for `MACU Helpdesk` completes without `NoneType` error
- [ ] "Refresh failed" badge is clickable and shows the full error message

---

## Phase 2: Generation Monitor Page

**Goal:** Give operators a live, first-class view of what the generation subsystem is doing, plus a repo-scoped popover for end users.

### Changes Required

#### 1. New REST endpoint for live activity

**File:** `internal/api/rest/admin_knowledge.go`

Add a new handler `handleGenerationActivity` mounted at `/api/v1/admin/generation/activity`:

```go
type GenerationActivity struct {
    Active   []ArtifactActivity `json:"active"`    // status IN (pending, generating)
    Recent   []ArtifactActivity `json:"recent"`    // last 50 terminal within 1h
    Stats    ActivityStats      `json:"stats"`
    Worker   WorkerHealth       `json:"worker"`
}

type ArtifactActivity struct {
    ID             string    `json:"id"`
    RepoID         string    `json:"repo_id"`
    RepoName       string    `json:"repo_name"`
    Type           string    `json:"type"`           // CLIFF_NOTES, etc.
    ScopeType      string    `json:"scope_type"`
    ScopePath      string    `json:"scope_path"`
    Audience       string    `json:"audience"`
    Depth          string    `json:"depth"`
    Status         string    `json:"status"`
    Progress       float64   `json:"progress"`
    StartedAt      time.Time `json:"started_at"`
    UpdatedAt      time.Time `json:"updated_at"`
    ElapsedMs      int64     `json:"elapsed_ms"`
    ErrorCode      string    `json:"error_code,omitempty"`
    ErrorMessage   string    `json:"error_message,omitempty"`
    SnapshotBytes  int       `json:"snapshot_bytes,omitempty"`
    InputTokens    int       `json:"input_tokens,omitempty"`
    OutputTokens   int       `json:"output_tokens,omitempty"`
}

type ActivityStats struct {
    QueueDepth          int              `json:"queue_depth"`
    InFlight            int              `json:"in_flight"`
    MaxConcurrency      int              `json:"max_concurrency"`
    P50LatencyMs        map[string]int64 `json:"p50_latency_ms"` // by type
    P95LatencyMs        map[string]int64 `json:"p95_latency_ms"`
    SuccessRate1h       float64          `json:"success_rate_1h"`
}

type WorkerHealth struct {
    Connected     bool   `json:"connected"`
    LLMProvider   string `json:"llm_provider"`
    LLMModel      string `json:"llm_model"`
    LastHeartbeat string `json:"last_heartbeat"`
}
```

Query logic:
- **Active:** `SELECT * FROM ca_knowledge_artifact WHERE status IN ('pending', 'generating') ORDER BY updated_at DESC`
- **Recent:** `SELECT * FROM ca_knowledge_artifact WHERE status IN ('ready', 'failed') AND updated_at > now() - 1h ORDER BY updated_at DESC LIMIT 50`
- **Stats:** compute p50/p95 from recent terminal artifacts using `(updated_at - created_at)` grouped by `type`
- **Worker health:** call `r.Worker.IsConnected()` and a new `r.Worker.Info()` gRPC that returns provider/model info (Phase 2a — or stub from config if time-constrained)

Optional query params:
- `?repo_id=<uuid>` — filter to a single repo (for the popover)
- `?since=<iso8601>` — only rows updated after this timestamp (for efficient polling)

#### 2. Monitor page (new route)

**File:** `web/src/app/(app)/admin/generation/page.tsx` (new)

Layout (top-to-bottom):

**Worker health strip** — a single row showing:
- Worker connection dot (green/red)
- LLM provider + model
- In-flight / max concurrency (e.g., `2 / 3`)
- Queue depth
- Success rate over last hour
- Latency badges: `cliff_notes p50 18s · p95 47s`

**Active jobs table** — columns: Repo · Scope · Type · Progress · Elapsed · Started
- Progress column is a live bar using the artifact's `progress` field
- Elapsed ticks up client-side (don't re-fetch just for the timer)
- Row click opens a detail drawer

**Recent jobs table** — columns: Repo · Scope · Type · Status · Duration · Finished
- Status badge: READY (green) / FAILED (red) with error code
- Click a FAILED row → drawer with full error message, retry button
- Paginated or virtualized if we expect >100 rows/hour

**Detail drawer** — shows:
- Artifact metadata (all columns from `ArtifactActivity`)
- Snapshot size, input/output token counts
- Full error message if FAILED
- "Retry" button (calls `refreshKnowledgeArtifact` mutation)
- "View in repo" link to `/repositories/[id]`

**Polling:** every 2 seconds while the tab is visible, every 10 seconds when backgrounded (`document.visibilityState`). Use `If-None-Match` with an etag for efficiency if the endpoint supports it.

#### 3. Repo-scoped popover

**File:** `web/src/app/(app)/repositories/[id]/page.tsx`

Add a "Generation status" button to the repo header (near the existing refresh controls) that opens a popover showing:
- Active jobs for this repo (filtered via `?repo_id=`)
- Last 5 completed jobs for this repo
- Link to the full Monitor page

Reuses the same data source as the Monitor page — no new API work.

#### 4. Admin tab integration

**File:** `web/src/app/(app)/admin/page.tsx`

Add a new tab "Generation" alongside the existing "Knowledge" tab, routing to `/admin/generation`. Keep the existing Knowledge tab (aggregate counts) as a sibling — they serve different purposes.

### Success Criteria

**Automated**
- [ ] Integration test for `/api/v1/admin/generation/activity` endpoint returning active, recent, and stats sections
- [ ] Frontend component test for the Monitor page rendering a mock activity response

**Manual (on thor after deploy)**
- [ ] Navigate to `/admin/generation` and see at least one active or recent job
- [ ] Trigger a cliff notes regeneration and watch the row appear in the Active table within 2 seconds
- [ ] Click a failed row and see the full error message and code
- [ ] Open the repo page popover and see the same job filtered to that repo
- [ ] Worker health strip reflects actual Ollama model and concurrency

---

## Phase 3: True Streaming Progress

**Goal:** Replace the 3-value progress jumps with a smoothly advancing bar driven by real worker phases and LLM token counts.

### Changes Required

#### 1. New streaming gRPC method

**File:** `proto/sourcebridge/knowledge/v1/knowledge.proto`

Add alongside the existing unary method:

```proto
service KnowledgeService {
  // ... existing methods
  rpc GenerateCliffNotesStream(GenerateCliffNotesRequest) returns (stream CliffNotesEvent);
}

message CliffNotesEvent {
  oneof event {
    ProgressUpdate progress = 1;
    CliffNotesResult result = 2;  // terminal: final sections + usage
  }
}

message ProgressUpdate {
  string phase = 1;        // "snapshot", "retrieval", "prompt", "llm", "parse", "persist"
  float  progress = 2;     // 0.0 - 1.0
  string message = 3;      // human-readable, e.g., "Generating section 3/7"
  int32  tokens_so_far = 4;
}

message CliffNotesResult {
  repeated KnowledgeSection sections = 1;
  sourcebridge.common.v1.LLMUsage usage = 2;
}
```

Regenerate stubs via `make proto`.

#### 2. Emit phases from the Python worker

**File:** `workers/knowledge/servicer.py`

Add new method `GenerateCliffNotesStream` alongside `GenerateCliffNotes`. Refactor `workers/knowledge/cliff_notes.py:generate_cliff_notes` to take an optional `progress_callback: Callable[[str, float, str, int], Awaitable[None]] | None`.

Emit progress at these checkpoints:
- `0.05` — `snapshot` — "Snapshot received ({size} KB)"
- `0.15` — `retrieval` — "Retrieval complete" (or "Condensation complete")
- `0.25` — `prompt` — "Prompt built (~{tokens} tokens)"
- `0.30` — `llm` — "Generating sections…"
- `0.30 → 0.85` — `llm` — streamed linearly off `output_tokens / expected_tokens`
- `0.90` — `parse` — "Parsing LLM response"
- `0.95` — `persist` — "Storing sections"
- `1.00` — terminal (sent via `CliffNotesResult` branch of oneof)

For the token-level progress between 0.30 and 0.85, switch the LLM call from `provider.complete()` to `provider.stream()` (already exists at `openai_compat.py:109`), accumulate the streamed tokens, and emit a progress update every N tokens (N=50 is a reasonable default to avoid flooding the stream).

#### 3. Wire streaming through the Go API

**File:** `internal/worker/client.go`

Add a new method:
```go
func (c *Client) GenerateCliffNotesStream(
    ctx context.Context,
    req *knowledgepb.GenerateCliffNotesRequest,
    onProgress func(phase string, progress float64, message string),
) (*knowledgepb.CliffNotesResult, error)
```

It calls the streaming RPC, loops over events, invokes `onProgress` for progress events, and returns the terminal `CliffNotesResult`.

**File:** `internal/api/graphql/schema.resolvers.go:1400` (the background goroutine)

Replace the unary `r.Worker.GenerateCliffNotes()` call with the new streaming call. In the `onProgress` callback, persist the progress to the artifact:

```go
result, err := r.Worker.GenerateCliffNotesStream(ctx, req, func(phase string, progress float64, message string) {
    if err := r.Knowledge.SetProgress(ctx, artifactID, progress, phase, message); err != nil {
        slog.Warn("set_progress_failed", "error", err, "artifact_id", artifactID)
    }
})
```

**File:** `internal/db/knowledge_store.go`

Add two more columns:
- `progress_phase` — string (e.g., "llm")
- `progress_message` — string (e.g., "Generating sections…")

Extend `SetProgress` to accept the phase and message. No frontend changes required yet — the existing 5-second poll will pick them up.

**Optional hop:** lower the frontend polling interval to 2 seconds (`web/src/app/(app)/repositories/[id]/page.tsx:307`) for snappier updates now that there are meaningful values to fetch.

#### 4. Display phase + message in the UI

**File:** `web/src/app/(app)/repositories/[id]/page.tsx:1840-1848`

Extend the progress block to show the phase and message:

```tsx
<div className="space-y-1">
  <div className="flex justify-between text-xs text-muted-foreground">
    <span>{currentCliffNotes.progressMessage || "Working…"}</span>
    <span>{Math.round(currentCliffNotes.progress * 100)}%</span>
  </div>
  <progress max={100} value={Math.max(currentCliffNotes.progress * 100, 5)} />
</div>
```

Add `progressMessage` and `progressPhase` to the `KnowledgeArtifact` GraphQL type and the `KNOWLEDGE_ARTIFACTS_QUERY`.

### Success Criteria

**Automated**
- [ ] `make proto` regenerates cleanly
- [ ] Unit test for the Python `generate_cliff_notes` progress callback receiving all expected phases in order
- [ ] Go streaming client test using a fake gRPC server

**Manual (on thor after deploy)**
- [ ] Trigger a repo-level cliff notes regeneration and observe the progress bar moving at least every 2 seconds
- [ ] Phase label ("Generating sections…", "Parsing") visible and changing during generation
- [ ] Token-level progress advances monotonically from 30% to 85% during the LLM phase

---

## Phase 4: Concurrency & Resilience

**Goal:** Turn the uncontrolled fire-hose into a managed queue with retries, per-scope timeouts, and an operator runbook.

### Changes Required

#### 1. Bounded generation queue

**New file:** `internal/knowledge/generation_queue.go`

```go
type Queue struct {
    sem      chan struct{}        // size = max concurrency
    inflight sync.Map             // artifactID -> context.CancelFunc (for dedupe)
    store    *db.Store
    worker   *worker.Client
}

func NewQueue(store *db.Store, worker *worker.Client, maxConcurrency int) *Queue

// Enqueue claims the artifact (status PENDING), then blocks on a slot.
// Returns immediately with the claimed artifact; generation runs in the background.
func (q *Queue) Enqueue(ctx context.Context, job GenerationJob) (*knowledge.Artifact, error)
```

Behavior:
- On enqueue: if the artifact is already in `q.inflight`, dedupe and return the existing record.
- Otherwise: set status `PENDING`, spawn a goroutine that acquires a semaphore slot before calling the worker.
- When a slot is acquired: set status `GENERATING`, run, then release the slot on completion.
- Queue depth = count of artifacts with status `PENDING`.
- Max concurrency is configurable via `config.toml` (`knowledge.max_concurrent_generations`, default 3).

**File:** `internal/api/graphql/schema.resolvers.go:1400`

Replace the direct `go func() { ... }()` with `r.Queue.Enqueue(ctx, job)`. The resolver still returns immediately with the claimed artifact.

#### 2. Scope-aware timeouts

**File:** `internal/worker/client.go:36`

Replace the single `TimeoutKnowledge` with a function:

```go
func timeoutForScope(scopeType string) time.Duration {
    switch strings.ToLower(scopeType) {
    case "repository":
        return 600 * time.Second
    case "module":
        return 300 * time.Second
    case "file", "symbol", "requirement":
        return 120 * time.Second
    default:
        return 300 * time.Second
    }
}
```

Pass the scope through the `GenerateCliffNotesStream` client method and apply the derived timeout to the context.

#### 3. Retry with backoff

**New file:** `internal/knowledge/retry.go`

```go
type RetryPolicy struct {
    MaxAttempts int
    InitialBackoff time.Duration
    MaxBackoff time.Duration
}

func IsRetryable(err error) bool {
    // DeadlineExceeded → true (1 retry, in case worker was momentarily saturated)
    // Unavailable      → true
    // LLMEmpty         → true (1 retry, in case of transient provider hiccup)
    // SnapshotTooLarge → false
    // InvalidArgument  → false
    // Internal + known NoneType → false
}
```

Default policy: 2 attempts max, 5s initial backoff, 30s cap. Only one retry in practice because MaxAttempts=2.

The queue goroutine wraps the worker call in the retry policy and records `retry_count` on the artifact.

**File:** `internal/db/knowledge_store.go`

Add `retry_count int` column to `ca_knowledge_artifact`.

#### 4. Ollama context window runbook

**New file:** `docs/runbooks/ollama-context-sizing.md`

Document:
- How to check current `num_ctx` for a given Ollama model: `ollama show <model> --modelfile`
- How to create a Modelfile override that increases `num_ctx` (e.g., to 32768 or 65536)
- RAM requirements per `num_ctx` (rough rule: doubling context ~doubles KV-cache memory)
- Relationship between `num_ctx` and the new `SOURCEBRIDGE_MAX_PROMPT_TOKENS` env var
- Recommended settings for small/medium/large repos

Link this runbook from `README.md` in a troubleshooting section.

#### 5. Configuration

**File:** `config.toml.example`

```toml
[knowledge]
# Max concurrent generations across all artifact types (default: 3)
max_concurrent_generations = 3

# Max prompt tokens before refusing to send (default: 24000)
max_prompt_tokens = 24000

# Per-scope timeouts in seconds (optional overrides)
[knowledge.timeouts]
repository = 600
module = 300
file = 120
symbol = 120
```

### Success Criteria

**Automated**
- [ ] Queue unit test: firing 10 enqueues with max_concurrency=3 results in 3 concurrent, 7 queued
- [ ] Queue unit test: duplicate enqueue for the same artifact returns the existing record
- [ ] Retry unit test: `DeadlineExceeded` on attempt 1 triggers retry; second success succeeds
- [ ] Retry unit test: `SnapshotTooLarge` does not retry
- [ ] Integration test: scope-aware timeout picks 120s for file scope

**Manual (on thor after deploy)**
- [ ] Monitor page shows queue depth climb when firing many requests, and drain at max_concurrency rate
- [ ] Duplicate regeneration clicks result in one queued job, not multiple
- [ ] Repo-level cliff notes succeed on `MACU Helpdesk` after increasing Ollama `num_ctx` per the runbook
- [ ] Retry count increments for a transient failure case and the second attempt succeeds

---

## Performance Considerations

- **Polling cost for Monitor page:** a 2-second poll against a single SurrealDB query is acceptable at the expected operator count (single-digit concurrent viewers). If it becomes a concern, move to SSE in a follow-up.
- **Streaming gRPC overhead:** emitting progress every 50 output tokens adds trivial bytes; the bottleneck remains the LLM itself.
- **Bounded pool ceiling:** default 3 is a safe starting point for a single Ollama instance. Operators can raise this if they scale the worker or move to a batching provider.
- **Retry amplification:** capping at 2 attempts prevents retry storms; the in-flight dedupe from Phase 1 prevents client-side mashing from multiplying.
- **`error_message` column size:** SurrealDB handles large strings cheaply; no need to truncate aggressively, but cap at 8KB in the store method to be safe.

## Migration Notes

- **DB schema changes** are additive only: `error_message`, `error_code`, `progress_phase`, `progress_message`, `retry_count`. No backfill required; existing rows read `NULL`/`0` which the frontend treats as absent. No downtime.
- **gRPC:** the new `GenerateCliffNotesStream` method is additive. The existing `GenerateCliffNotes` unary stays in place during the transition and can be removed in a later cleanup once the Go client fully cuts over.
- **Frontend:** phase 1 ships the error-detail change which depends only on the GraphQL schema addition (backwards-compatible). Phases 2-4 can deploy independently.
- **Config:** new `[knowledge]` section in `config.toml` with safe defaults; operators on thor will continue working without changes until they want to tune.

## References

- Original investigation conversation: `thoughts/shared/research/` (if captured separately)
- Relevant files:
  - `internal/api/graphql/schema.resolvers.go:1305-1495` — `GenerateCliffNotes` mutation
  - `internal/worker/client.go:36` — `TimeoutKnowledge`
  - `internal/db/knowledge_store.go` — `ca_knowledge_artifact` model
  - `internal/api/rest/admin_knowledge.go:56-113` — admin knowledge endpoint
  - `workers/knowledge/servicer.py:81-161` — gRPC servicer
  - `workers/knowledge/cliff_notes.py:208-324` — generation pipeline
  - `workers/knowledge/workflow_story.py:369-498` — workflow story (NoneType bug)
  - `workers/common/llm/openai_compat.py:98` — silent empty-response coercion
  - `web/src/app/(app)/repositories/[id]/page.tsx:1820,1840-1848` — progress bar + failed badge
  - `web/src/app/(app)/admin/page.tsx:14,140` — admin dashboard
- Live log evidence from `sourcebridge-api-664c554746-48nt9` and `sourcebridge-worker-784d8db9dc-mqb2f` on thor (2026-04-09), including `cliff_notes_parse_fallback`, `refresh cliff notes failed`, and `generate_workflow_story_failed` events
