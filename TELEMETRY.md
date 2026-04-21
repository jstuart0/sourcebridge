# Telemetry

SourceBridge collects **anonymous, aggregate usage data** to help us understand
how the product is used and prioritize improvements. No personally identifiable
information is ever collected.

## What is collected

| Field | Example | Purpose |
|-------|---------|---------|
| Installation ID | `a1b2c3d4-...` | Random UUID generated on first run. Not linked to any person. |
| Version | `0.5.0` | Which version is deployed |
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

## First-run notice

On first startup, SourceBridge logs a message indicating that telemetry is
enabled and how to disable it. This message appears once per startup.

## Data handling

Telemetry data is sent to `https://telemetry.sourcebridge.ai/v1/ping` via
HTTPS. The endpoint is operated by SourceBridge. Data is used in aggregate
only and is not sold or shared with third parties.

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
