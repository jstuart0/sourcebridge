# Collector-Side Changes for QA Telemetry Fields

Companion PR draft for the main-repo change that added
`qa_asks_total_14d` (counts) and `qa_server_side` (features) to the
public telemetry ping. This document describes exactly what to land
in `/Users/jaystuart/dev/sourcebridge-telemetry/` so the public
dashboard can chart QA adoption.

The collector repo had uncommitted working-tree changes when this
document was written, so these changes were **not** applied
automatically. Apply on top of whatever branch is active over there.

## 1. `schema.sql` — no change required

`counts` and `features` are stored as JSON TEXT blobs, so the new
keys land without a column change.

## 2. No migration required for existing rows

The JSON-blob storage means existing pings keep working; new pings
just have extra keys. No `migrations/003_*.sql` needed unless we
later decide to promote `qa_asks_total_14d` to its own indexed column
for faster queries (only worth it once the field has measurable
volume).

## 3. `src/worker.ts` — no ingest change required

The ingest handler already pass-through-persists the `counts` and
`features` JSON. No code change needed on the ingest path.

## 4. Dashboard queries (follow-up, not blocking)

When you're ready to surface QA adoption on the dashboard, add:

```sql
-- Install count with server-side QA enabled (last 24h)
SELECT COUNT(DISTINCT installation_id) AS qa_server_side_installs
FROM pings
WHERE last_seen > datetime('now', '-1 day')
  AND is_test = 0
  AND json_extract(features, '$') LIKE '%qa_server_side%';

-- Rolling 14-day QA-asks volume across installs (latest snapshot per install)
SELECT SUM(CAST(json_extract(counts, '$.qa_asks_total_14d') AS INTEGER)) AS qa_asks_14d
FROM pings
WHERE is_test = 0
  AND last_seen > datetime('now', '-1 day');
```

The first query is the "how many operators flipped the flag"
adoption metric. The second is the activity metric — together they
tell the story of a rollout.

## 5. CA-400: 30-day rolling counters for queries and artifacts

CA-400 (Phase 2) added two new keys to the `counts` blob:

- `queries_30d` — rolling 30-day count of QA invocations
  (`Orchestrator.Ask`). Process-local; resets on agent restart.
- `artifacts_generated_30d` — rolling 30-day count of knowledge
  artifacts (cliff notes, architecture diagram, learning path, code
  tour, workflow story) that completed user-requested generation.
  Excludes seed/deepening paths. Process-local; resets on agent restart.

**Important**: `pings` is a latest-per-install snapshot table. The
`counts` values on each row reflect the process-local state at the
moment of that ping. Use a freshness gate (`last_seen > -48 hours`)
when summing across installs — a 30-day window on `last_seen` would
incorrectly aggregate 0–59 days of activity per the CA-400 Decision 6
rationale.

```sql
-- Total queries (30d ring) across currently-active installs
-- "Active" = pinged within the last 48 hours (2× the 24h default cadence)
SELECT COALESCE(
    SUM(CAST(json_extract(counts, '$.queries_30d') AS INTEGER)), 0
) AS total_queries_30d
FROM pings
WHERE is_test = 0
  AND last_seen > datetime('now', '-48 hours');

-- Total artifacts generated (30d ring) across currently-active installs
SELECT COALESCE(
    SUM(CAST(json_extract(counts, '$.artifacts_generated_30d') AS INTEGER)), 0
) AS total_artifacts_generated_30d
FROM pings
WHERE is_test = 0
  AND last_seen > datetime('now', '-48 hours');
```

Installs that have not yet sent the new keys will produce NULL from
`json_extract`; `COALESCE(..., 0)` handles this gracefully so the
aggregate is never NULL.

## 5. `README.md` — document the new keys

Append to whatever section enumerates counts:

```markdown
- `qa_asks_total_14d` — rolling 14-day count of server-side QA
  invocations (`/api/v1/ask`, `ask` mutation, MCP `ask_question`).
  Zero on installs where server-side QA is disabled. Client-side
  ring buffer; stale days are not counted.

Features:
- `qa_server_side` — present when the operator enabled
  `SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED` on that install.
```

## How to apply

```bash
cd /Users/jaystuart/dev/sourcebridge-telemetry

# 1. Resolve the existing working-tree changes first — rebase, stash,
#    or commit as appropriate.
git status

# 2. When ready, create a branch for the QA fields PR:
git checkout -b qa-telemetry-fields

# 3. Apply the doc changes described in section 4 + 5 above to
#    README.md. No schema, no worker code changes required for the
#    ingest path — the JSON-blob storage already accepts the new keys.

# 4. Push + open PR:
git commit -m "Document qa_asks_total_14d + qa_server_side feature flag"
git push -u origin qa-telemetry-fields
```

Zero schema work = zero migration risk on the collector side. The
pre-existing rows keep working; new pings get extra keys the worker
silently persists.
