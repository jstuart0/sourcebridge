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

## Required secrets

`secrets.yaml` is a **commented template** — it is not included in the
kustomization resources. Create the `sourcebridge-secrets` Secret
before applying:

```bash
kubectl create secret generic sourcebridge-secrets \
  --namespace sourcebridge \
  --from-literal=SOURCEBRIDGE_SECURITY_JWT_SECRET="$(openssl rand -base64 32)" \
  --from-literal=SOURCEBRIDGE_STORAGE_SURREAL_USER="root" \
  --from-literal=SOURCEBRIDGE_STORAGE_SURREAL_PASS="$(openssl rand -base64 24)" \
  --from-literal=SOURCEBRIDGE_LLM_API_KEY=""
```

Optional keys (leave empty to disable):

| Key | Purpose |
|---|---|
| `SOURCEBRIDGE_SECURITY_OIDC_CLIENT_ID` | OIDC SSO client ID |
| `SOURCEBRIDGE_SECURITY_OIDC_CLIENT_SECRET` | OIDC SSO client secret |
| `SOURCEBRIDGE_GIT_DEFAULT_TOKEN` | Workspace default git PAT (R3 slice 2: now a *bootstrap* — UI saves win at runtime) |
| `SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY` | Required if you save credentials via the UI |

## Deploying from base directly

```bash
# 1. Create the namespace and secrets
kubectl create namespace sourcebridge
kubectl create secret generic sourcebridge-secrets \
  --namespace sourcebridge \
  --from-literal=SOURCEBRIDGE_SECURITY_JWT_SECRET="$(openssl rand -base64 32)" \
  --from-literal=SOURCEBRIDGE_STORAGE_SURREAL_USER="root" \
  --from-literal=SOURCEBRIDGE_STORAGE_SURREAL_PASS="$(openssl rand -base64 24)"

# 2. Apply the base
kubectl apply -k deploy/kubernetes/base

# 3. Patch the Ingress host to your actual hostname
kubectl -n sourcebridge patch ingress sourcebridge-ingress \
  --type=json \
  -p='[{"op":"replace","path":"/spec/rules/0/host","value":"sourcebridge.yourdomain.com"},
       {"op":"replace","path":"/spec/tls/0/hosts/0","value":"sourcebridge.yourdomain.com"}]'
```

## Using overlays (recommended)

```yaml
# my-overlay/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - github.com/sourcebridge-ai/sourcebridge//deploy/kubernetes/base?ref=v0.9.0

images:
  - name: ghcr.io/sourcebridge-ai/sourcebridge-api
    newTag: v0.9.0
  - name: ghcr.io/sourcebridge-ai/sourcebridge-worker
    newTag: v0.9.0
  - name: ghcr.io/sourcebridge-ai/sourcebridge-web
    newTag: v0.9.0

patches:
  - path: ingress-patch.yaml
```

```bash
kubectl apply -k my-overlay/
```

## LLM configuration

The base configmap defaults to a local Ollama instance. Patch it in
your overlay (or via the admin UI on `/admin/comprehension` after
boot) to point at a remote provider:

```yaml
SOURCEBRIDGE_LLM_PROVIDER: "openai"
SOURCEBRIDGE_LLM_BASE_URL: "https://api.openai.com/v1"
SOURCEBRIDGE_LLM_SUMMARY_MODEL: "gpt-4o-mini"
# Set SOURCEBRIDGE_LLM_API_KEY in the Secret, NOT the ConfigMap.
```

R2 + R3 made the workspace UI the source of truth for LLM config and
git credentials respectively — you can ship a generic configmap and
let operators tune at runtime through `/admin/comprehension` and
`/admin/git`.

## Ingress class

The base ingress uses `ingressClassName: traefik`. For nginx (or
anything else), patch in your overlay:

```yaml
patches:
  - target:
      kind: Ingress
      name: sourcebridge-ingress
    patch: |
      - op: replace
        path: /spec/ingressClassName
        value: nginx
```
