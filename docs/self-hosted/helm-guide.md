# Helm Chart Installation Guide

## Prerequisites

- Kubernetes 1.24+
- Helm 3.x
- kubectl configured for your cluster
- A default StorageClass (or specify one explicitly)

## Quick Start

```bash
helm repo add sourcebridge https://charts.sourcebridge.dev
helm repo update

helm install sourcebridge sourcebridge/sourcebridge \
  --namespace sourcebridge --create-namespace \
  --set secrets.jwtSecret=$(openssl rand -hex 32) \
  --set secrets.surrealRootPass=$(openssl rand -hex 16) \
  --set worker.env.OPENAI_API_KEY=sk-...
```

Verify the installation:

```bash
kubectl -n sourcebridge get pods
kubectl -n sourcebridge get svc
```

## Values Reference

### global

| Key | Description | Default |
|-----|-------------|---------|
| `global.imageTag` | Override image tag for all components | `latest` |
| `global.imagePullPolicy` | Pull policy: Always, IfNotPresent, Never | `IfNotPresent` |
| `global.imagePullSecrets` | List of image pull secret names | `[]` |

### api

| Key | Description | Default |
|-----|-------------|---------|
| `api.replicas` | Number of API server pods | `2` |
| `api.image` | API container image | `ghcr.io/sourcebridge/sourcebridge` |
| `api.resources.requests.cpu` | CPU request | `250m` |
| `api.resources.requests.memory` | Memory request | `256Mi` |
| `api.resources.limits.cpu` | CPU limit | `1000m` |
| `api.resources.limits.memory` | Memory limit | `1Gi` |
| `api.env` | Additional environment variables | `{}` |
| `api.nodeSelector` | Node selector for scheduling | `{}` |

### web

| Key | Description | Default |
|-----|-------------|---------|
| `web.replicas` | Number of web UI pods | `1` |
| `web.image` | Web container image | `ghcr.io/sourcebridge/sourcebridge-web` |
| `web.resources.requests.cpu` | CPU request | `100m` |
| `web.resources.requests.memory` | Memory request | `128Mi` |
| `web.resources.limits.cpu` | CPU limit | `500m` |
| `web.resources.limits.memory` | Memory limit | `512Mi` |

### worker

| Key | Description | Default |
|-----|-------------|---------|
| `worker.replicas` | Number of worker pods | `1` |
| `worker.image` | Worker container image | `ghcr.io/sourcebridge/sourcebridge-worker` |
| `worker.resources.requests.cpu` | CPU request | `500m` |
| `worker.resources.requests.memory` | Memory request | `512Mi` |
| `worker.resources.limits.cpu` | CPU limit | `2000m` |
| `worker.resources.limits.memory` | Memory limit | `2Gi` |
| `worker.env` | Additional environment variables (LLM keys) | `{}` |

### surrealdb

| Key | Description | Default |
|-----|-------------|---------|
| `surrealdb.enabled` | Deploy SurrealDB as part of the chart | `true` |
| `surrealdb.image` | SurrealDB image | `surrealdb/surrealdb:v2.0` |
| `surrealdb.persistence.size` | PVC size | `20Gi` |
| `surrealdb.persistence.storageClass` | StorageClass name | `""` (default) |
| `surrealdb.resources.requests.cpu` | CPU request | `500m` |
| `surrealdb.resources.requests.memory` | Memory request | `512Mi` |

### redis

| Key | Description | Default |
|-----|-------------|---------|
| `redis.enabled` | Deploy Redis as part of the chart | `true` |
| `redis.image` | Redis image | `redis:7-alpine` |
| `redis.persistence.size` | PVC size | `1Gi` |
| `redis.persistence.storageClass` | StorageClass name | `""` (default) |

### ingress

| Key | Description | Default |
|-----|-------------|---------|
| `ingress.enabled` | Create Ingress resource | `false` |
| `ingress.className` | IngressClass name | `""` |
| `ingress.host` | Hostname for the ingress | `sourcebridge.example.com` |
| `ingress.tls.enabled` | Enable TLS | `false` |
| `ingress.tls.secretName` | TLS secret name | `sourcebridge-tls` |
| `ingress.annotations` | Additional ingress annotations | `{}` |

### secrets

| Key | Description | Default |
|-----|-------------|---------|
| `secrets.jwtSecret` | JWT signing key (min 32 chars) | (required) |
| `secrets.surrealRootPass` | SurrealDB root password | (required) |
| `secrets.existingSecret` | Use an existing Kubernetes Secret instead | `""` |
| `secrets.stripeSecretKey` | Stripe API key | `""` |
| `secrets.stripeWebhookSecret` | Stripe webhook signing secret | `""` |

## Example: Minimal Installation

Single replica, no ingress, default storage:

```bash
helm install sourcebridge sourcebridge/sourcebridge \
  --namespace sourcebridge --create-namespace \
  --set api.replicas=1 \
  --set worker.replicas=1 \
  --set secrets.jwtSecret=$(openssl rand -hex 32) \
  --set secrets.surrealRootPass=$(openssl rand -hex 16)
```

Access via port-forward:

```bash
kubectl -n sourcebridge port-forward svc/sourcebridge-web 3000:3000
kubectl -n sourcebridge port-forward svc/sourcebridge-api 8080:8080
```

## Example: Production Installation

```bash
helm install sourcebridge sourcebridge/sourcebridge \
  --namespace sourcebridge --create-namespace \
  --values production-values.yaml
```

`production-values.yaml`:

```yaml
global:
  imageTag: "1.4.0"

api:
  replicas: 3
  resources:
    requests: { cpu: 500m, memory: 512Mi }
    limits: { cpu: 2000m, memory: 2Gi }

worker:
  replicas: 2
  env:
    OPENAI_API_KEY: sk-...

surrealdb:
  persistence:
    size: 100Gi
    storageClass: ceph-rbd

ingress:
  enabled: true
  className: traefik
  host: sourcebridge.example.com
  tls:
    enabled: true
    secretName: sourcebridge-tls
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-production

secrets:
  existingSecret: sourcebridge-credentials
```

## Example: Air-Gapped Installation

See [air-gapped.md](air-gapped.md) for image preparation. Then:

```bash
helm install sourcebridge sourcebridge/sourcebridge \
  --namespace sourcebridge --create-namespace \
  --set global.imagePullSecrets[0]=registry-creds \
  --set api.image=registry.internal/sourcebridge/sourcebridge \
  --set web.image=registry.internal/sourcebridge/sourcebridge-web \
  --set worker.image=registry.internal/sourcebridge/sourcebridge-worker \
  --set worker.env.OLLAMA_URL=http://ollama.internal:11434 \
  --set worker.env.SOURCEBRIDGE_LLM_PROVIDER=ollama \
  --set secrets.jwtSecret=$(openssl rand -hex 32) \
  --set secrets.surrealRootPass=$(openssl rand -hex 16)
```

## Upgrading

```bash
helm repo update
helm upgrade sourcebridge sourcebridge/sourcebridge \
  --namespace sourcebridge \
  --values production-values.yaml
```

Check the release notes for breaking changes before upgrading major versions. Database migrations run automatically on API server startup. See [upgrade.md](upgrade.md) for the full pre-upgrade checklist.

## Uninstalling

```bash
helm uninstall sourcebridge --namespace sourcebridge
```

This removes all Kubernetes resources but preserves PVCs. To delete data:

```bash
kubectl -n sourcebridge delete pvc --all
kubectl delete namespace sourcebridge
```

## Troubleshooting

**Pods stuck in Pending:** Check storage provisioning. `kubectl -n sourcebridge describe pod <pod>` and look for PVC binding errors.

**API pod CrashLoopBackOff:** Usually a missing secret. Verify: `kubectl -n sourcebridge get secret sourcebridge-secrets -o yaml`.

**Worker not processing jobs:** Confirm Redis is reachable: `kubectl -n sourcebridge exec deploy/sourcebridge-worker -- redis-cli -u $REDIS_URL ping`.

**SurrealDB OOMKilled:** Increase memory limits. Large repositories can cause spikes during indexing.

**Ingress 502 errors:** Verify the API readiness probe passes: `kubectl -n sourcebridge exec deploy/sourcebridge-api -- curl -s localhost:8080/readyz`.
