# Telemetry

SourceBridge collects **anonymous, aggregate usage data** to help us understand
how the product is used and prioritize improvements. No personally identifiable
information is ever collected.

## What is collected

| Field | Example | Purpose |
|-------|---------|---------|
| Installation ID | `a1b2c3d4-...` | Random UUID generated on first run. Not linked to any person. |
| Version | `v0.9.0-rc.3-dev.216+g956607e` | Which version is deployed. Computed by `scripts/version.sh` and baked in via Go ldflags at build time; see [docs/admin/build-info.md](docs/admin/build-info.md). |
| Edition | `oss` | OSS or enterprise |
| Platform | `linux/amd64` | OS and architecture |
| Repo count | `12` | How many repositories are indexed (count only, no names) |
| Feature flags | `["reports"]` | Which features are active |
| `trash_moves_total` | `42` | Cumulative count of `moveToTrash` invocations since process start (recycle bin feature) |
| `trash_restores_total` | `9` | Cumulative count of successful `restoreFromTrash` invocations |
| `trash_conflicts_total` | `3` | Cumulative count of restore attempts that hit a natural-key conflict |
| `trash_permanent_deletes_total` | `5` | Cumulative count of user-initiated `permanentlyDelete` invocations |
| `trash_purges_total` | `120` | Cumulative count of rows purged by the retention worker |
| `trash_size_gauge` | `17` | Most recent sampled count of items currently in the trash |
| `qa_asks_total_14d` | `342` | Rolling 14-day count of server-side QA (`/api/v1/ask`, `ask` mutation, MCP `ask_question`) invocations on this install. Zero when server-side QA is disabled. |
| `queries_30d` | `342` | Rolling 30-day count of QA invocations (every `Orchestrator.Ask`) on this install. **Process-local; resets to zero when the agent process restarts; reported as the in-process sum at the moment of the ping.** Zero on fresh processes; grows over the next 30 days. |
| `artifacts_generated_30d` | `17` | Rolling 30-day count of knowledge artifacts (cliff notes, architecture diagram, learning path, code tour, workflow story) that transitioned from GENERATING to READY via user-requested generation. **Excludes** field-guide seed artifacts and cliff-note section deepening (initialization/refresh, not new generation). **Process-local; resets to zero on agent process restart.** |
| `qa_server_side` feature flag | `["qa_server_side"]` | Present in the `features` array when `SOURCEBRIDGE_QA_SERVER_SIDE_ENABLED=true`. Lets the public dashboard track orchestrator adoption. |
| `clustering_enabled` | `true` | True when the `subsystem_clustering` capability is active on this installation. |
| `cluster_count` | `12` | Number of clusters for the largest indexed repository. Zero when clustering has not run or no repos are indexed. |
| `clustering_modularity_q` | `0.42` | Newman–Girvan modularity Q of the most recent clustering run, rounded to 2 decimal places. Zero when clustering has not run. |
| `agent_setup_used` | `false` | Reserved for Sprint 3 (`sourcebridge setup claude`). Always `false` in v1. |

## What is NOT collected

- Repository names, URLs, or contents
- User names, emails, or credentials
- IP addresses (the telemetry server does not log them)
- Source code or analysis results
- File paths or file contents
- Any data from your repositories

## How to opt out

Any of the following will disable telemetry:

```bash
# Environment variable
export SOURCEBRIDGE_TELEMETRY=off

# Or use the community-standard DO_NOT_TRACK
export DO_NOT_TRACK=1
```

Or in `config.toml`:

```toml
[telemetry]
enabled = false
```

## Mark an install as a test install

If you run SourceBridge as part of CI, a homelab, a dev environment, or
any other context where pings shouldn't count toward the public usage
numbers, set the platform override:

```bash
export SOURCEBRIDGE_TELEMETRY_PLATFORM=test
```

The collector auto-flags `platform == "test"` and excludes the install
from public dashboards. The flag is sticky once set on the collector
side — even if a later ping reports a real platform, the install stays
flagged.

This is the cleanest way to keep dev/test deployments out of public
counts without disabling telemetry entirely (you'll still see your own
usage data when the dashboard is queried with `?include_test=1`).

## First-run notice

On first startup, SourceBridge logs a message indicating that telemetry is
enabled and how to disable it. This message appears once per startup.

## Data handling

Telemetry data is sent to `https://telemetry.sourcebridge.ai/v1/ping` via
HTTPS. The endpoint is operated by SourceBridge. Data is used in aggregate
only and is not sold or shared with third parties.

## Third-party analytics (PostHog)

The web frontend optionally sends browser-side analytics to PostHog.
This is **separate** from the server-side telemetry above and is only
active when `NEXT_PUBLIC_POSTHOG_KEY` is set at build time.

**Host**: `NEXT_PUBLIC_POSTHOG_HOST` (default: `https://us.i.posthog.com`)

**What is sent**:

| Event | Fields |
|-------|--------|
| Page views (auto) | URL path (no query params with PII) |
| Capture events | Event name, timestamp |
| User identity | Opaque user ID (JWT subject UUID) |

**What is NOT sent**: email address, name, tenant ID, or any other PII. The
`identify()` call was audited (CA-211 + CA-320) to send only the JWT subject
(an opaque UUID assigned at account creation).

**Opt out**:

- Set the **Do Not Track** browser setting (`navigator.doNotTrack = "1"`).
  The analytics client checks DNT before every `identify()` call, and
  PostHog's `respect_dnt: true` option suppresses autocapture as well.
- Leave `NEXT_PUBLIC_POSTHOG_KEY` unset (or empty) at build time. When
  the key is absent, the PostHog client never initialises and no data
  is sent.

## Source code

The client-side telemetry sender remains in the OSS repository at
[`internal/telemetry/telemetry.go`](internal/telemetry/telemetry.go).

The hosted telemetry collector and dashboard are maintained separately from the
main OSS repository (Cloudflare Worker + D1). Public dashboard:
<https://telemetry.sourcebridge.ai/dashboard>.

## Test vs real installs

The collector flags installations as "test" and hides them from the public
dashboard and badge by default. A ping is auto-flagged when any of:

- `platform == "test"`
- `version` starts with `http://`, `https://`, or `localhost`

The flag is sticky once set. SourceBridge maintainers can also flag specific
installation IDs via an authenticated admin endpoint on the collector. Dev
builds (`version == "dev"`) are intentionally **not** auto-flagged because they
often come from real contributors — maintainers flag those individually when
they know the ID is their own.

Toggle **Include test installs** on the dashboard (or append `?include_test=1`
to any stats URL) to see the unfiltered totals.
