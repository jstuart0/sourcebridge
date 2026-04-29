# SourceBridge Kubernetes Base Manifests

This is the canonical OSS deployment base. Production overlays (e.g.
the homelab cluster's `manifests/sourcebridge` overlay) reference this
tree via Kustomize:

```yaml
resources:
  - github.com/sourcebridge-ai/sourcebridge//deploy/kubernetes/base?ref=main
```

## What lives here

- `namespace.yaml`, `configmap.yaml`, `secrets.yaml` — top-level config
- `surrealdb.yaml` — SurrealDB statefulset + service
- `api.yaml`, `worker.yaml`, `web.yaml` — workload Deployments + Services
- `ingress.yaml` — Traefik IngressRoute
- `cronjobs/` — scheduled jobs (e.g. retention worker)
- `kustomization.yaml` — the base assembly

## What does NOT live here (intentional)

- **Cert-manager Issuers + Certificates for API↔worker mTLS**.
  These live one directory up at `deploy/kubernetes/mtls-issuers.yaml`,
  `deploy/kubernetes/mtls-worker-cert.yaml`, and
  `deploy/kubernetes/mtls-api-cert.yaml`. They are NOT included in
  `base/` because:

  1. Cert-manager is not part of the SourceBridge OSS baseline. Most
     OSS deployments don't have it installed; importing the mTLS
     resources unconditionally would fail Argo sync on those clusters.
  2. mTLS is opt-in per the slice 4 design (default
     `SOURCEBRIDGE_WORKER_TLS_ENABLED=false` in `configmap.yaml`).
     A cluster running with the env flag off doesn't need the
     resources.

  Production overlays (e.g. the homelab) that *do* run cert-manager
  should reference the mTLS manifests directly from their own
  kustomization.yaml:

  ```yaml
  resources:
    - github.com/sourcebridge-ai/sourcebridge//deploy/kubernetes/base?ref=main
    - github.com/sourcebridge-ai/sourcebridge//deploy/kubernetes/mtls-issuers.yaml?ref=main
    - github.com/sourcebridge-ai/sourcebridge//deploy/kubernetes/mtls-worker-cert.yaml?ref=main
    - github.com/sourcebridge-ai/sourcebridge//deploy/kubernetes/mtls-api-cert.yaml?ref=main
  ```

  …and patch the configmap to flip `SOURCEBRIDGE_WORKER_TLS_ENABLED` to
  `"true"`.

  See `thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth-r2.md`
  section 5.4 for the full rationale.
