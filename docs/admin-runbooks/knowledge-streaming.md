# Knowledge streaming RPCs runbook

Covers the cancellation, heartbeat, and timeout contracts for the
seven knowledge gRPCs delivered as part of CA-122 (commits
`01779d3`...`0b92350` on the `feature/CA-122-streaming-rpc` branch
and onward). Use this page if you see knowledge generation jobs (deep
cliff notes, learning paths, code tours, architecture diagrams,
workflow stories, system explanations, build-repository-understanding,
or enterprise reports) failing with `DEADLINE_EXCEEDED` or stuck
mid-progress on the admin LLM activity feed.

## TL;DR

- Every knowledge RPC is now **server-streaming**. The worker emits
  phase markers (`SNAPSHOT`, `LEAF_SUMMARIES`, `FILE_SUMMARIES`,
  `PACKAGE_SUMMARIES`, `ROOT_SYNTHESIS`, `RENDER`, `FINALIZING`) and
  per-phase progress events; the API bridges them into the
  `ca_knowledge_artifact.progress` row in real time.
- The orchestrator's 10-minute stale-job reaper now keys off **real**
  progress events, not a synthetic 5-second ticker. A worker that is
  genuinely making progress keeps `UpdatedAt` fresh; a wedged worker
  gets reaped at the 10-min mark and its gRPC stream is actively
  cancelled.
- A **safety-net timeout** (default 4 hours) hard-caps any single
  RPC that has established a streaming heartbeat. Tunable per
  deployment via the admin REST endpoint below.
- Single-call RPCs (learning path, architecture diagram, code tour,
  workflow story, explain system) emit a phase-only heartbeat every
  **30 seconds** so a long single LLM round-trip does not get reaped
  for inactivity. Tunable via `SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS`.
- Cliff-notes and report progress writes follow a **drop-oldest** policy
  under backpressure: the bounded channel preserves the most recent
  phase / counter rather than the first one queued.

## Operator knobs

### Safety-net timeout (REST, per-workspace)

```bash
# Get
curl -fsS \
  -H "Authorization: Bearer $SOURCEBRIDGE_ADMIN_JWT" \
  https://sourcebridge.example/api/v1/admin/knowledge/rpc-safety-net-timeout

# Set (10 minutes minimum, 24 hours maximum)
curl -fsS -X POST \
  -H "Authorization: Bearer $SOURCEBRIDGE_ADMIN_JWT" \
  -H "Content-Type: application/json" \
  -d '{"timeout_seconds": 10800}' \
  https://sourcebridge.example/api/v1/admin/knowledge/rpc-safety-net-timeout
```

Persists to `ca_knowledge_settings.rpc_safety_net_timeout_secs`.
Replicated across all replicas via Surreal LIVE QUERY.

When to lower from the 4h default:
- You have hard SLAs on knowledge generation completion (e.g. 1h)
  and would rather fail-fast than burn another N hours.
- Tests / staging environments where you want quick feedback.

When to raise from the 4h default:
- You routinely deep-render Fortune-500-sized monorepos that take
  >4h end-to-end and you accept the cost.
- The cap is a safety net, not a budget — the reaper still cancels
  truly wedged streams at the 10-minute heartbeat-stale mark.

### Single-call heartbeat cadence (env, worker-side)

```bash
# Default 30s; min 1s; floats accepted
SOURCEBRIDGE_KNOWLEDGE_STREAM_HEARTBEAT_SECS=30
```

Set on the worker pod. Lower (e.g. 10s) if you suspect the
orchestrator's reaper window has been tightened below 10 minutes
and single-call RPCs are getting reaped before they finish their
single LLM round-trip. Raise (e.g. 60s) only on resource-
constrained workers where the heartbeat task itself is measurable
overhead.

## Diagnosing failures

### `DEADLINE_EXCEEDED` on the admin LLM activity feed

1. Check the artifact row's `progress_phase`: did it ever leave
   `snapshot` or `queued`? If yes, the worker was doing real work; see
   §"Worker stuck without heartbeat" below.
2. Check the worker pod's logs for `cliff_notes_strategy_cleanup_timeout`
   or `report_engine_cleanup_timeout`. Those mean the strategy task
   ignored cancellation for >5 seconds (usually a hung HTTP call to
   the LLM provider).
3. Check the orchestrator logs for `reaper_cancelled_active_run`. That
   confirms the reaper actively cancelled the stream rather than just
   marking the job failed and leaving the worker running.

### Worker stuck without heartbeat

Symptoms: artifact `progress` is monotonically increasing, then stops
for 10+ minutes; `UpdatedAt` does not refresh; reaper marks the job
failed; the gRPC stream is cancelled.

Likely causes:
- The LLM provider's HTTP call hit a network timeout that did not
  surface back to the worker (rare; usually shows up as a
  `httpx.ReadTimeout`).
- The hierarchical pipeline is mid-stage and the strategy's
  `progress_snapshot()` is reporting the same counters tick after
  tick. The heartbeat helper still emits an empty progress event,
  but if the worker process is genuinely frozen (e.g. paused in
  pdb, OOM-throttled) the event never makes it onto the wire.

Mitigation:
- Restart the worker pod. The artifact row's terminal-state guard
  (CA-122 r2 M1) will reject any late progress writes from the dying
  worker, so re-running the generation will not get confused by
  stale progress.

### `report_stream_cancelled` on enterprise reports

Reports do not honor request-context cancellation today. The
enterprise `SetReportGenerator` callback runs on `context.Background()`
because the callback signature predates request-context plumbing.
Cancellation only fires via the orchestrator reaper's 10-minute
stale window or the 4h safety-net timeout. This is a known follow-up
documented in `internal/api/rest/enterprise_routes.go`.

## Migration notes (post-CA-122)

If you upgrade across CA-122 with knowledge artifacts already in
flight, those artifacts will be re-classified by the new reaper. A
job whose `UpdatedAt` is older than 10 minutes will be cancelled
immediately on the upgraded API's first reaper pass. If you have
genuinely-running multi-hour jobs at upgrade time, set the safety-net
timeout high (e.g. 21600 / 6h) before the rollout and reset it after.

## Related documentation

- `CHANGELOG.md` "[Unreleased]" section, "Deep cliff-notes
  DEADLINE_EXCEEDED reaper races" entry.
- The implementation plan and codex review chain at
  `thoughts/shared/plans/2026-04-29-deep-cliffnotes-deadline-exceeded.*.md`.
- The investigation that triggered the work:
  `thoughts/shared/investigations/2026-04-29-deep-cliffnotes-deadline-exceeded.md`.
