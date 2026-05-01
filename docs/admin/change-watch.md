# Change-watch + connector ingress runbook

Operator runbook for the in-process change-watch feedback loop and the
public connector HTTP ingress (plan
`thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md`, Phase 1).

This is the **Phase 1 stub** — it covers what's shipped through 1.E and
what flag-flip activates. Phase 2 ships the `/admin/connectors` UI and
extends this runbook with the GitHub webhook + App connector. Phase 4
adds the `Fast / Balanced / Strict` mode picker.

## What change-watch does

Change-watch is the closed pipeline that detects code changes and
re-derives the symbol tier surgically so subsequent MCP reads return
fresh answers with honest freshness metadata. Three connectors share
one router:

1. **Passive fsnotify watcher** (in-process) — detects out-of-band
   edits made by an IDE / CLI / `git pull`. Default-on once
   `change_watch.enabled=true`.
2. **`record_change` MCP tool** (in-process) — opt-in agent
   enrichment. An agent that just made edits MAY call this for
   tighter delta scoping and intent attribution. **Never required**
   for correctness; the passive watcher is sufficient on its own.
3. **HTTP ingress** at `POST /v1/connectors/{id}/events` — for
   external connectors (Phase 2 ships GitHub webhook + App). Behind
   `connector_api.enabled=true`.

Every connector funnels through `internal/changewatch.Router.Submit`,
which enforces the schema, dedups across connectors (so fsnotify and
`record_change` observing the same edit collapse to one routed event),
rate-limits per `(repo, source.kind)`, runs a per-repo aggregate
breaker, validates the claimed branch against `git.HeadRef`, calls
`Indexer.IndexFiles` under a 100ms T0 budget, applies the merge,
re-runs the existing impact pipeline, and updates the per-repo
freshness state.

## What flag-flip activates

After Phase 1.E burn-in, the operator flips two flags:

```toml
[change_watch]
enabled = true   # turns on the passive fsnotify watcher + the router

[connector_api]
enabled = true   # turns on POST /v1/connectors/{id}/events
                 # (this stays off if you don't have external connectors yet)
```

Or via env:

```bash
SOURCEBRIDGE_CHANGE_WATCH_ENABLED=true
SOURCEBRIDGE_CONNECTOR_API_ENABLED=true   # optional — enables HTTP ingress
```

**`change_watch.enabled` is the umbrella flag.** When it's off,
nothing ships:
- The fsnotify watcher does not start
- The router rejects every event with `change_watch_disabled`
- The `record_change` MCP tool is hidden from `tools/list`
- The HTTP ingress route returns `503 change_watch_disabled` if
  `connector_api.enabled` is true, or 404 if it's also off

**`connector_api.enabled` is the public-ingress flag.** When off, the
route is never registered, so external probes see a 404 with no
fingerprint of the SourceBridge install.

## What you'll see when it's on

1. **Freshness envelope on every MCP response.** Every `tools/call`
   response carries `_meta.freshness` with `state` (`fresh` /
   `stale` / `suspect` / `invalidated`), `tier`, `branch`,
   `last_verified_at`, `partial_refresh`, and (when populated)
   `reason` describing the most recent change attribution.
   The envelope ships **even when change-watch is off** —
   default-fresh — so MCP clients can rely on the contract being
   uniform.

2. **`record_change` tool in `tools/list`.** Agents that read
   tools/list will see the new tool with description leading
   "Optional. ... never required for correctness." The tool is
   hidden when the dispatcher isn't wired so agents don't discover a
   no-op tool.

3. **Structured logs** under the `changewatch:` slog source. You
   want to watch:
   - `INFO changewatch: ChangeEvent submitted` — every accepted event
   - `WARN changewatch: branch mismatch — event rejected` — Risk #4
     (a connector claimed branch X while the working tree is on Y).
     Both branches are in the log.
   - `WARN changewatch: IndexFiles failed` — usually
     `budget_exceeded=true` (the 100ms T0 budget — freshness drops to
     `partial_refresh=true` until the next event)
   - `ERROR changewatch: containment violation` — guardrail #3
     tripped; this is a **contract bug**, not an operational
     condition. File an issue.
   - `INFO mcp record_change handled` — every `record_change` MCP
     call. Useful for tracking agent adoption.

4. **Per-repo aggregate breaker** opens at 60 events/min sustained
   for 5 consecutive minutes (default; tune via
   `SOURCEBRIDGE_CHANGE_WATCH_REPO_BREAKER_PER_MIN`). When open,
   every event for that repo returns `breaker_tripped` until the
   rate falls back under threshold. Watch for this if a CI loop or
   a misbehaving agent is hammering one repo.

## Disabling

To turn change-watch off cleanly:

```bash
SOURCEBRIDGE_CHANGE_WATCH_ENABLED=false
# restart the API deployment
```

After the restart:
- Watcher stops (no new events)
- Router rejects everything still in flight
- Freshness envelope reverts to default-fresh on every MCP response
- `record_change` disappears from `tools/list`
- HTTP ingress returns `503 change_watch_disabled` (route still
  registered while `connector_api.enabled=true`)

There is **no in-flight state to drain.** The router holds a
short-lived dedup window and per-repo prev-IndexResult caches; both
clear on restart with no operator intervention.

## Tuning knobs

| Env var | Default | What it controls |
|---|---|---|
| `SOURCEBRIDGE_CHANGE_WATCH_ENABLED` | `false` | Umbrella — turns on the watcher + router |
| `SOURCEBRIDGE_CHANGE_WATCH_DEBOUNCE_MS` | `2000` | Per-repo fsnotify debounce window. Phase 4 makes this per-repo via the responsiveness mode picker; Phase 1 wires the Balanced default. |
| `SOURCEBRIDGE_CHANGE_WATCH_RATE_LIMIT_PER_MIN` | `30` | Per-(repo, source.kind) throttle |
| `SOURCEBRIDGE_CHANGE_WATCH_REPO_BREAKER_PER_MIN` | `60` | Per-repo aggregate breaker threshold (5min sustained → trip) |
| `SOURCEBRIDGE_CHANGE_WATCH_T0_BUDGET_MS` | `100` | Hard ceiling for synchronous IndexFiles refresh on read |
| `SOURCEBRIDGE_CONNECTOR_API_ENABLED` | `false` | Public HTTP ingress |
| `SOURCEBRIDGE_LINKING_INVALIDATE_GRACE_HOURS` | `24` | Grace window before dependent links transition to `invalidated` (Phase 2 wires the actual transition logic) |

Tune these only when you see specific symptoms. Defaults are sized for
typical agent + IDE traffic on a single-repo install.

## Observability checklist for the first week post-flip

After flipping `SOURCEBRIDGE_CHANGE_WATCH_ENABLED=true`:

1. **Hour 1**: confirm the watcher is reporting events. Tail logs for
   `changewatch: ChangeEvent submitted`. If you see nothing in 5
   minutes of normal activity, the watcher isn't picking up edits —
   most common cause is symlink resolution. The watcher
   `EvalSymlinks` on the input path; if the operator-supplied repo
   path differs from the resolved path the watcher won't classify
   events. Compare `repo.path` to the resolved on-disk path.

2. **Day 1**: confirm freshness envelope is reaching agents. Have an
   agent run `search_symbols` and inspect `_meta.freshness` —
   `state` should be `fresh` initially with `last_verified_at` from
   the last index. After an edit, `last_verified_at` should advance
   within ~3s.

3. **Day 1**: confirm no containment violations. Grep logs for
   `containment violation` — should be zero. If non-zero, file an
   issue immediately; it indicates a contract bug in the watcher /
   router / IndexFiles chain.

4. **Day 3**: review per-repo event volumes. Look for outlier repos
   that hit the breaker — usually a misbehaving CI hook or an
   indexer that didn't realize change-watch was on.

5. **Day 7**: verify `record_change` adoption. Grep
   `mcp record_change handled` — agents that have updated to use the
   tool will appear; agents that haven't are still served correctly
   by the passive path. **No alerting needed** if adoption is low —
   that's the design.

## Disabling rollback procedure

If you see something wrong post-flip:

1. **Set the umbrella flag back to false.** That's the kill switch.
   ```bash
   kubectl -n <ns> set env deployment/sourcebridge-api SOURCEBRIDGE_CHANGE_WATCH_ENABLED=false
   ```
2. **Restart the API.** The watcher stops, the router stops accepting
   events, the freshness envelope reverts to default-fresh.
3. **File a bug.** Capture the `changewatch:` log lines you saw.
   The plan v5 plus the per-phase CHANGELOG entries are the
   reference.

There is no data to clean up. Change-watch state is in-memory only in
Phase 1; no migrations, no DB rows, no persistent records. (Phase 5
adds the `ca_change_event` table for the change-feed UI; that's a
forward-only addition.)

## Phase 1 readiness summary

What the flag-flip activates after Phase 1.E:

- ✓ Passive fsnotify watcher detecting all flavors of agent edits
  (single-file write, multi-file refactor, file rename, file
  deletion, file addition) — covered by Phase 1 done-def #7.
- ✓ Per-repo router with delta-only invariant guardrails (non-empty
  delta, bounded work via `IndexFiles`-only interface, containment).
- ✓ Per-`(repo, source.kind)` rate limit + per-repo aggregate
  breaker.
- ✓ Branch-mismatch rejection (Risk #4 / HIGH fix #6).
- ✓ Multi-tenant containment — events for tenant A's repo never
  surface in tenant B's freshness state.
- ✓ Freshness envelope on every MCP response (`_meta.freshness`).
- ✓ `record_change` MCP tool — opt-in, never required, hidden when
  dispatcher unwired.
- ✓ HTTP ingress at `POST /v1/connectors/{id}/events` — auth via
  bearer/JWT middleware; HMAC for GitHub ships in Phase 2.
- ✓ Path-normalization contract enforced at all three ingress
  surfaces.
- ✓ All 15 Phase 1 done-definition tests green; full Phase 1 commit
  chain passes `make test` + `make lint` for files touched.

What flag-flip does **NOT** activate (deferred to later phases):

- ✗ `Fast / Balanced / Strict` mode picker per repo — Phase 4
- ✗ `mark-suspect` / `auto-resolve` link state machine — Phase 2
- ✗ Web UI freshness chips and change feed — Phase 5
- ✗ Compound-tool `LINK_INVALIDATED` refusal — Phase 2
- ✗ GitHub webhook + App connector — Phase 2
- ✗ `/admin/connectors` admin UI — Phase 2
- ✗ Schema promotion `0.x` → `1.0` — Phase 2 after the
  schema-stability checkpoint
- ✗ T2 / T3 surgical re-derivation — Phase 3 (Phase 1 keeps the
  existing invalidation behavior at those tiers)
- ✗ Persistent `ca_change_event` table — Phase 5

## Related plans and references

- Plan: `thoughts/shared/plans/2026-04-29-mcp-edits-feedback-loop.md`
- Phase 1.A CHANGELOG entry: see Unreleased
- Phase 1.B CHANGELOG entry: see Unreleased
- Phase 1.C CHANGELOG entry: see Unreleased
- Phase 1.D CHANGELOG entry: see Unreleased
- Phase 1 closing summary: see Unreleased

The connector-API public schema documentation (`docs/admin/connector-api.md`)
ships at the start of Phase 2 with a `DRAFT — pending checkpoint`
banner; the banner is removed after the schema-stability checkpoint
passes. Until then, the schema constant in
`internal/changewatch.ChangeEventSchemaVersion` is `0.1` and operators
should treat it as internal/unstable.
