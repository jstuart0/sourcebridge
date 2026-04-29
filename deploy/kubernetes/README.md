# SourceBridge Kubernetes Deployment

The canonical OSS manifests live in **[`base/`](./base/)**. Everything you need to
deploy SourceBridge — namespace, configmap, secrets template, surrealdb,
api, worker, web, ingress — is there. Start there.

## What lives at this level (not in `base/`)

- `kustomization.yaml` — thin wrapper that resolves to `./base` plus a
  few opt-in resources. Existing tooling that points at
  `deploy/kubernetes/` (e.g. older Argo paths) keeps working.
- `mtls-issuers.yaml`, `mtls-worker-cert.yaml`, `mtls-api-cert.yaml` —
  cert-manager Issuer + Certificate resources for the API↔worker
  gRPC channel. Default disabled in the configmap; flip
  `SOURCEBRIDGE_WORKER_TLS_ENABLED=true` to opt in. Requires
  cert-manager installed cluster-wide.
- `redis.yaml` — opt-in Redis StatefulSet (some deployments use it
  for caching; not part of the canonical stack). Apply explicitly:
  `kubectl apply -f redis.yaml`.
- `llama-server-speculative.yaml`, `vllm-speculative.yaml` —
  speculative-decoding profiles for specific GPU/model setups.
  Apply explicitly when you want them.
- `BACKUP-RESTORE.md` — operator runbook for backup/restore.

## What used to live here (R3 slice 5 cleanup)

The top level used to have eight stale duplicates of the `base/`
manifests (`api.yaml`, `configmap.yaml`, `ingress.yaml`,
`namespace.yaml`, `secrets.yaml`, `surrealdb.yaml`, `web.yaml`,
`worker.yaml`). They had drifted from `base/`: outdated image org name
(`ghcr.io/sourcebridge/...` vs the canonical
`ghcr.io/sourcebridge-ai/...`), older default model strings, and a
homelab-specific `nfs` storageClass. Builds out of this directory now
resolve to `base/` directly via the kustomization above, so the
duplicates were deleted.

If you previously customized one of those files in-place (rather than
in an overlay), the canonical advice is now: copy the relevant block
from `base/<file>.yaml` into your own overlay and patch it there. See
[`base/README.md`](./base/README.md) for an overlay walkthrough.
