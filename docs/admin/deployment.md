# Deployment Guide

## Docker Compose (Recommended)

Create a `docker-compose.yml` in your project directory:

```yaml
services:
  api:
    image: ghcr.io/sourcebridge/sourcebridge:latest
    ports:
      - "8080:8080"
    environment:
      - SOURCEBRIDGE_DB_URL=ws://surrealdb:8000
      - SOURCEBRIDGE_DB_NS=sourcebridge
      - SOURCEBRIDGE_DB_DB=production
      - SOURCEBRIDGE_JWT_SECRET=${JWT_SECRET}
      - SOURCEBRIDGE_LOG_LEVEL=info
      - REDIS_URL=redis://redis:6379
    depends_on:
      surrealdb:
        condition: service_healthy
      redis:
        condition: service_healthy

  web:
    image: ghcr.io/sourcebridge/sourcebridge-web:latest
    ports:
      - "3000:3000"
    environment:
      - NEXT_PUBLIC_API_URL=http://api:8080

  worker:
    image: ghcr.io/sourcebridge/sourcebridge-worker:latest
    environment:
      - SOURCEBRIDGE_DB_URL=ws://surrealdb:8000
      - OPENAI_API_KEY=${OPENAI_API_KEY}
      - REDIS_URL=redis://redis:6379
    depends_on:
      - api

  surrealdb:
    image: surrealdb/surrealdb:v2.0
    command: start --user root --pass ${SURREAL_ROOT_PASS} file:/data/sourcebridge.db
    volumes:
      - surreal_data:/data
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8000/health"]
      interval: 5s
      retries: 10

  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      retries: 5

volumes:
  surreal_data:
  redis_data:
```

Start the stack:

```bash
export JWT_SECRET=$(openssl rand -hex 32)
export SURREAL_ROOT_PASS=$(openssl rand -hex 16)
export OPENAI_API_KEY=sk-...
docker compose up -d
```

## Kubernetes Deployment

Apply manifests directly with kubectl:

```bash
kubectl create namespace sourcebridge

# Create secrets
kubectl -n sourcebridge create secret generic sourcebridge-secrets \
  --from-literal=jwt-secret=$(openssl rand -hex 32) \
  --from-literal=surreal-root-pass=$(openssl rand -hex 16) \
  --from-literal=openai-api-key=sk-...

# Apply core services
kubectl -n sourcebridge apply -f manifests/surrealdb.yaml
kubectl -n sourcebridge apply -f manifests/redis.yaml
kubectl -n sourcebridge apply -f manifests/api.yaml
kubectl -n sourcebridge apply -f manifests/web.yaml
kubectl -n sourcebridge apply -f manifests/worker.yaml
```

## Resource Requirements

| Component | CPU Request | CPU Limit | Memory Request | Memory Limit | Storage |
|-----------|------------|-----------|----------------|--------------|---------|
| API       | 250m       | 1000m     | 256Mi          | 1Gi          | -       |
| Web       | 100m       | 500m      | 128Mi          | 512Mi        | -       |
| Worker    | 500m       | 2000m     | 512Mi          | 2Gi          | -       |
| SurrealDB | 500m       | 2000m     | 512Mi          | 4Gi          | 20Gi    |
| Redis     | 100m       | 500m      | 128Mi          | 512Mi        | 1Gi     |

Minimum total: 2 CPU cores, 4 GB RAM, 25 GB storage.

## Environment Variables Reference

### API Server

| Variable | Description | Default |
|----------|-------------|---------|
| `SOURCEBRIDGE_PORT` | HTTP listen port | `8080` |
| `SOURCEBRIDGE_HOST` | Bind address | `0.0.0.0` |
| `SOURCEBRIDGE_DB_URL` | SurrealDB WebSocket URL | `ws://localhost:8000` |
| `SOURCEBRIDGE_DB_NS` | SurrealDB namespace | `sourcebridge` |
| `SOURCEBRIDGE_DB_DB` | SurrealDB database name | `production` |
| `SOURCEBRIDGE_JWT_SECRET` | JWT signing key (min 32 chars) | (required) |
| `SOURCEBRIDGE_LOG_LEVEL` | Log level: debug, info, warn, error | `info` |
| `SOURCEBRIDGE_TEST_MODE` | Disable auth for development | `false` |
| `REDIS_URL` | Redis connection string | `redis://localhost:6379` |
| `STRIPE_SECRET_KEY` | Stripe API key for billing | (optional) |
| `STRIPE_WEBHOOK_SECRET` | Stripe webhook signing secret | (optional) |

### Worker

| Variable | Description | Default |
|----------|-------------|---------|
| `OPENAI_API_KEY` | OpenAI API key | (optional) |
| `ANTHROPIC_API_KEY` | Anthropic API key | (optional) |
| `OLLAMA_URL` | Ollama endpoint for local LLM | (optional) |
| `SOURCEBRIDGE_LLM_PROVIDER` | Force provider: openai, anthropic, ollama | (auto-detect) |
| `SOURCEBRIDGE_LLM_MODEL` | Model override | (provider default) |

### Web UI

| Variable | Description | Default |
|----------|-------------|---------|
| `NEXT_PUBLIC_API_URL` | API server URL visible to browser | `http://localhost:8080` |

## Health Check Endpoints

```bash
# API liveness (returns 200 when process is running)
curl http://localhost:8080/healthz

# API readiness (returns 200 when DB and Redis are connected)
curl http://localhost:8080/readyz

# GraphQL health query
curl -X POST http://localhost:8080/api/v1/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ health { status version uptime } }"}'
```

Kubernetes probe configuration:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 10
readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
  initialDelaySeconds: 10
  periodSeconds: 5
```

## Scaling Considerations

**Horizontal scaling:** The API server is stateless and can run multiple replicas behind a load balancer. Workers scale independently based on indexing and LLM workload.

```bash
# Scale API replicas
kubectl -n sourcebridge scale deployment/sourcebridge-api --replicas=3

# Scale workers for heavy indexing
kubectl -n sourcebridge scale deployment/sourcebridge-worker --replicas=5
```

**SurrealDB:** Runs as a single instance for most deployments. For high availability, use SurrealDB's TiKV-backed cluster mode.

**Redis:** Used for worker queues and summary caching. A single instance handles most workloads. Use Redis Sentinel or Cluster for HA.

## Monitoring Integration

SourceBridge.ai exposes Prometheus metrics at `/metrics` on the API server:

```yaml
# Prometheus scrape config
- job_name: sourcebridge
  static_configs:
    - targets: ['sourcebridge-api:8080']
  metrics_path: /metrics
```

Key metrics:
- `sourcebridge_api_requests_total` -- HTTP request count by method, path, status
- `sourcebridge_indexing_duration_seconds` -- Repository indexing latency histogram
- `sourcebridge_llm_tokens_total` -- LLM token usage by provider and tenant
- `sourcebridge_active_tenants` -- Number of tenants with recent activity

Grafana dashboard ID `sourcebridge-overview` is included in the `monitoring/` directory.
