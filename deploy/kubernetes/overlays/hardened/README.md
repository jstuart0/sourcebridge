# readOnlyRootFilesystem Hardening Overlay

This overlay sets `readOnlyRootFilesystem: true` on the API, worker, and web
containers, and adds the `emptyDir` volumes required to satisfy each
component's writable-path needs. It is **entirely opt-in** — the base manifest
is not modified and `kubectl apply -k deploy/kubernetes/base/` continues to
behave exactly as before.

## Writable paths added per component

| Component | Mount | Purpose |
|-----------|-------|---------|
| API | `/tmp` | Temporary files |
| Worker | `/tmp` | uv-cache + HuggingFace cache (redirected from home via Dockerfile env) |
| Worker | `/var/cache/sourcebridge` | Workers runtime cache directory |
| Web | `/tmp` | Node.js temporary files |
| Web | `/app/.next/cache` | Next.js standalone ISR/RSC cache |

All writable volumes are `emptyDir` (ephemeral, per-pod). No persistent storage
is added by this overlay.

## Apply

```bash
kubectl apply -k deploy/kubernetes/overlays/hardened/
```

## Rollback

```bash
kubectl apply -k deploy/kubernetes/base/
```

The base manifests set `readOnlyRootFilesystem: false`; re-applying base restores
the original posture and removes the emptyDir volumes from the pod spec.

## Combining with the NetworkPolicy hardening overlay

Both overlays can be applied independently or together:

```bash
kubectl apply -k deploy/kubernetes/overlays/network-policy-hardened/
kubectl apply -k deploy/kubernetes/overlays/hardened/
```

Apply and rollback each independently as needed.

## Helm operators

As of CA-322, the Helm chart renders the matching writable `emptyDir`
mounts automatically when you flip the security flag. To enable
hardening on a Helm install:

```bash
helm upgrade sourcebridge deploy/helm/sourcebridge \
  --set securityContext.readOnlyRootFilesystem=true
```

The chart provisions:

- `api`: `/tmp` (size limit `api.tmpSizeLimit`, default `1Gi`)
- `worker`: `/tmp` (size limit `worker.tmpSizeLimit`, default `2Gi`) +
  `/var/cache/sourcebridge` (size limit `worker.cacheSizeLimit`, default `4Gi`)
- `web`: `/tmp` (size limit `web.tmpSizeLimit`, default `1Gi`) +
  `/app/.next/cache` (size limit `web.nextCacheSizeLimit`, default `1Gi`)

These match the kustomize overlay's mounts byte-for-byte. Operators with
additional write paths can extend via `api.extraVolumes` +
`api.extraVolumeMounts` (and the worker/web equivalents); they are
appended when the flag is on and rendered alone when the flag is off.

Verify the rendered output before upgrading:

```bash
helm template sourcebridge deploy/helm/sourcebridge \
  --set securityContext.readOnlyRootFilesystem=true \
  | grep -A2 "readOnlyRootFilesystem\|mountPath\|emptyDir"
```
