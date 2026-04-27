# SourceBridge Kubernetes Deployment

## Overview

`deploy/kubernetes/base/` contains generic Kustomize manifests for deploying
SourceBridge on any Kubernetes cluster. The base is deliberately environment-
agnostic: host names, image tags, replica counts, and credentials are all
configured by overlays.

## Components

| Manifest | What it deploys |
|---|---|
| `namespace.yaml` | `sourcebridge` namespace |
| `configmap.yaml` | All non-secret environment configuration |
| `secrets.yaml` | **Template only** — see "Required Secrets" below |
| `api.yaml` | API server deployment + service (port 8080) |
| `worker.yaml` | Python gRPC worker deployment + service (port 50051) |
| `web.yaml` | Next.js web UI deployment + service (port 3000) |
| `surrealdb.yaml` | SurrealDB StatefulSet + service (port 8000) |
| `ingress.yaml` | Generic Ingress with `sourcebridge.example.com` placeholder |

## Required Secrets

The base `secrets.yaml` is a **commented template** — it is not included in
the kustomization resources. You must create the `sourcebridge-secrets` Secret
before applying.

Minimum required keys:

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

## Deploying from base directly

For a quick evaluation or single-node install:

```bash
# 1. Create the namespace and secrets first
kubectl create namespace sourcebridge
kubectl create secret generic sourcebridge-secrets \
  --namespace sourcebridge \
  --from-literal=SOURCEBRIDGE_SECURITY_JWT_SECRET="$(openssl rand -base64 32)" \
  --from-literal=SOURCEBRIDGE_STORAGE_SURREAL_USER="root" \
  --from-literal=SOURCEBRIDGE_STORAGE_SURREAL_PASS="$(openssl rand -base64 24)" \
  --from-literal=SOURCEBRIDGE_LLM_API_KEY=""

# 2. Apply the base
kubectl apply -k deploy/kubernetes/base

# 3. Patch the Ingress host to your actual hostname
kubectl -n sourcebridge patch ingress sourcebridge-ingress \
  --type=json \
  -p='[{"op":"replace","path":"/spec/rules/0/host","value":"sourcebridge.yourdomain.com"},
       {"op":"replace","path":"/spec/tls/0/hosts/0","value":"sourcebridge.yourdomain.com"}]'
```

## Using overlays (recommended)

An overlay lets you version-control your site-specific settings in a separate
directory (or repository) without forking the base.

Minimal overlay structure:

```
my-overlay/
  kustomization.yaml
  ingress-patch.yaml
```

`my-overlay/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

# Point at the OSS base (local path or a remote git URL)
resources:
  - ../../deploy/kubernetes/base  # local fork
  # or: - github.com/sourcebridge-ai/sourcebridge//deploy/kubernetes/base?ref=v0.9.0

# Rewrite image registry and/or tag
images:
  - name: ghcr.io/sourcebridge-ai/sourcebridge-api
    newTag: v0.9.0
  - name: ghcr.io/sourcebridge-ai/sourcebridge-worker
    newTag: v0.9.0
  - name: ghcr.io/sourcebridge-ai/sourcebridge-web
    newTag: v0.9.0

# Override the placeholder hostname
patches:
  - path: ingress-patch.yaml
```

`my-overlay/ingress-patch.yaml`:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: sourcebridge-ingress
  namespace: sourcebridge
spec:
  tls:
    - hosts:
        - sourcebridge.yourdomain.com
      secretName: sourcebridge-tls
  rules:
    - host: sourcebridge.yourdomain.com
      http:
        paths:
          - path: /api
            pathType: Prefix
            backend:
              service:
                name: sourcebridge-api
                port:
                  number: 8080
          - path: /auth
            pathType: Prefix
            backend:
              service:
                name: sourcebridge-api
                port:
                  number: 8080
          - path: /
            pathType: Prefix
            backend:
              service:
                name: sourcebridge-web
                port:
                  number: 3000
```

Apply the overlay:

```bash
kubectl apply -k my-overlay/
```

## LLM configuration

By default the configmap points at a local Ollama instance
(`http://localhost:11434`). Edit `configmap.yaml` (or patch it in your overlay)
to point at a remote provider:

```yaml
# OpenAI-compatible provider
SOURCEBRIDGE_LLM_PROVIDER: "openai"
SOURCEBRIDGE_LLM_BASE_URL: "https://api.openai.com/v1"
SOURCEBRIDGE_LLM_SUMMARY_MODEL: "gpt-4o-mini"
# Set SOURCEBRIDGE_LLM_API_KEY in the Secret, not the ConfigMap
```

## Ingress class

The base ingress uses `ingressClassName: traefik`. If your cluster uses nginx
or another controller, patch it:

```yaml
# in your overlay kustomization.yaml
patches:
  - target:
      kind: Ingress
      name: sourcebridge-ingress
    patch: |
      - op: replace
        path: /spec/ingressClassName
        value: nginx
```
