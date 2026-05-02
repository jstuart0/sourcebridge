# Graceful API Server Drain (CA-142)

This document describes SourceBridge's graceful shutdown sequence, the
`SOURCEBRIDGE_SHUTDOWN_GRACE_SECONDS` tuning knob, and the operator steps
for a clean rolling update or forced kill.

## What "drain" means

When the API server receives SIGTERM (or a Kubernetes preStop hook fires), it
enters a multi-step shutdown sequence designed to let long-running Living Wiki
cold-start jobs finish rather than being abruptly killed mid-generation.

**8-step sequence in `cli/serve.go`:**

1. `BeginDrain("sigterm")` — flips readiness to failing (K8s stops routing new
   traffic) and pauses the LLM orchestrator's job intake so no new jobs start.
   Crucially, this also sets `o.draining = true` on the orchestrator so the
   stale-job reaper skips `StatusGenerating` jobs for the entire drain window.
2. 5 s settle sleep — lets the K8s endpoints controller propagate the readiness
   failure to upstream load-balancers before connections actually stop.
3. `AwaitDrain(graceCtx)` — blocks until both the in-flight job count and the
   on-demand request counter reach zero, logging `drain_progress` every 30 s.
4. `orch.CancelAndWait(30s)` — cancels worker goroutines and waits for them to
   exit.
5. `httpServer.Shutdown(30s)` — drains active HTTP connections.
6. `server.FinishShutdown(30s)` — shuts down the event bus and Living Wiki
   dispatcher.

## Grace period

| Environment variable | Default | Effect |
|---|---|---|
| `SOURCEBRIDGE_SHUTDOWN_GRACE_SECONDS` | `3600` (1 h) | How long `AwaitDrain` waits for in-flight jobs before timing out. |

A Living Wiki cold-start on a large codebase can legitimately take 30–60
minutes. Set this to at least as long as your longest expected generation run.
The Kubernetes `terminationGracePeriodSeconds` on the API pod should be this
value plus ~5 minutes of slack (the default Helm value is 3900 s).

**To override for a faster forced restart:**

```bash
# Apply a temporary lower grace before a manual rollout
kubectl -n sourcebridge set env deployment/sourcebridge-api \
  SOURCEBRIDGE_SHUTDOWN_GRACE_SECONDS=30
```

## Reaper guard

During drain, `reapStaleJobs` skips any job whose status is
`StatusGenerating`. This prevents the classic failure mode: a 31-minute
Living Wiki cold-start gets reaped at the 30-minute threshold, drops
`InFlightCount` to zero, `AwaitDrain` returns early, and SIGTERM kills the
process before generation completes.

Pending jobs (never picked up by a worker) are still reaped during drain —
they will never be resumed once workers stop, so holding them only delays
cleanup.

## Force-kill escape hatch

If a job is genuinely wedged (non-responsive, not making progress), you have
two options:

**Option A — cancel the specific job:**

```bash
curl -sX POST https://your-server/api/v1/admin/llm/server-drain \
  -H "Authorization: Bearer $TOKEN"
# Then cancel the stuck job via the Monitor page or the REST API
```

**Option B — override the grace period and redeploy:**

```bash
kubectl -n sourcebridge set env deployment/sourcebridge-api \
  SOURCEBRIDGE_SHUTDOWN_GRACE_SECONDS=60
kubectl -n sourcebridge rollout restart deployment/sourcebridge-api
```

This will reap in-flight jobs at the next reaper tick (15 s cadence) once the
new pod's drain window expires.

## Structured log events

All drain lifecycle events are emitted as structured JSON logs. Use these for
observability dashboards and alerting:

| `event` field | When emitted |
|---|---|
| `server_drain_handler_armed` | `NewServer()` — drain primitives wired |
| `received_sigterm` | SIGTERM received |
| `begindrain_via_sigterm` | First `BeginDrain` call from SIGTERM path |
| `begindrain_via_prestop` | First `BeginDrain` call from K8s preStop hook |
| `begindrain_duplicate` | Subsequent `BeginDrain` call ignored (idempotent) |
| `drain_await_begin` | `AwaitDrain` starts, logs current counts |
| `drain_progress` | Every 30 s while waiting, logs in-flight + on-demand counts + elapsed |
| `drain_workers_wait_complete` | `CancelAndWait` returned |
| `http_shutdown_complete` | `httpServer.Shutdown` returned |
| `reaper_skipped_during_drain` | Reaper skipped a generating job (guard fired) |

**Example Grafana/Loki query to detect slow drains:**

```logql
{app="sourcebridge-api"} | json | event = "drain_progress" | in_flight > 0
```

## Image-updater re-enable sequence

When Argo CD Image Updater is paused for a manual rollout (e.g. to upgrade the
LLM provider config), re-enable it after the new pod is healthy:

1. Verify the new pod shows `event=server_drain_handler_armed` in logs.
2. Verify `/readyz` returns 200.
3. Remove the `argocd-image-updater.argoproj.io/ignore-tags` annotation or
   re-enable the `ImageUpdateAutomation` resource.

## Worker startup probe

The Python worker Helm template includes a `startupProbe` that allows up to
90 s for cold startup (Python imports + ML client warmup) before the tighter
readiness probe takes over. This prevents premature liveness failures on first
deploy:

```yaml
startupProbe:
  exec:
    command: ["/usr/local/bin/sourcebridge-health-probe.sh"]
  initialDelaySeconds: 5
  periodSeconds: 3
  failureThreshold: 30   # 5 + 3×30 = 95 s max
  timeoutSeconds: 3
```

If your worker image takes longer to initialize (e.g. large model downloads),
increase `failureThreshold` accordingly.
