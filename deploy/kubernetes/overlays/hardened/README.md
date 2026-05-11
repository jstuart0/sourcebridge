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

## Helm equivalent

The Helm chart's `securityContext.readOnlyRootFilesystem` defaults `false`.
Operators can flip to `true` via `--set securityContext.readOnlyRootFilesystem=true`
but MUST also supply matching `emptyDir` mounts via `extraVolumes` and
`extraVolumeMounts` values, or the pods will crash on first write to a
temporary path. The Kubernetes overlays above are the recommended path for
kustomize-managed clusters.
