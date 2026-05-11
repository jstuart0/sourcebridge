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

The Helm chart does not currently render `readOnlyRootFilesystem=true` with
the required writable `emptyDir` mounts — flipping
`securityContext.readOnlyRootFilesystem=true` via `--set` or `values.yaml`
will crash pods on first write to a temporary path because the chart has no
`extraVolumes`/`extraVolumeMounts` support for these paths.

**Use this kustomize overlay for `readOnlyRootFilesystem` hardening.** If you
deploy via Helm, apply the patches manually post-`helm template` (add
`readOnlyRootFilesystem: true` and the `emptyDir` volumes shown above) or
wait for Helm chart support to land as a follow-up ticket.
