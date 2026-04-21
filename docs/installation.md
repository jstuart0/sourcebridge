# Installation

This guide covers every way to install and run SourceBridge.ai: Docker Compose for
quick evaluation, Helm for Kubernetes deployments, and from-source for contributors.

---

## Prerequisites

| Requirement | Docker Compose | Kubernetes / Helm | From Source |
|---|:-:|:-:|:-:|
| Docker + Docker Compose v2 | Required | -- | -- |
| kubectl | -- | Required | -- |
| Helm 3.x | -- | Required | -- |
| Go 1.25+ | -- | -- | Required |
| Python 3.12+ | -- | -- | Required (worker) |
| uv | -- | -- | Required (worker) |
| Node.js 22+ | -- | -- | Required (web UI) |
| LLM provider (Anthropic, OpenAI, or Ollama) | Recommended | Recommended | Recommended |

An LLM provider is not strictly required to start the server, but all AI-powered
features (code review, discussion, explanation, field guide generation) depend on one.

---

## 1. Docker Compose (Recommended)

Docker Compose is the fastest path from zero to a running SourceBridge instance. It
starts four containers: the Go API server, the Python worker, SurrealDB, and the
Next.js web UI.

### Quick start

```bash
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge
./setup.sh            # guided setup -- generates .env, starts containers
```

Or, if you prefer to skip the interactive script:

```bash
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge

# Create a minimal .env
cat > .env <<'EOF'
SOURCEBRIDGE_GRPC_SECRET=$(openssl rand -hex 16)
SOURCEBRIDGE_JWT_SECRET=$(openssl rand -hex 32)
SOURCEBRIDGE_LLM_PROVIDER=anthropic
SOURCEBRIDGE_LLM_MODEL=claude-sonnet-4-20250514
SOURCEBRIDGE_LLM_API_KEY=sk-ant-...
EOF

docker compose up -d
```

Open **http://localhost:3000** once the containers are healthy.

### Verifying the install

```bash
# Container status
docker compose ps

# API health check
curl http://localhost:8080/healthz

# Follow logs
docker compose logs -f
```

### Configuration (.env reference)

The Docker Compose file reads its configuration from a `.env` file in the project
root. All variables are optional and have sensible defaults.

| Variable | Description | Default |
|---|---|---|
| `SOURCEBRIDGE_GRPC_SECRET` | Shared secret for API-to-worker gRPC auth | `dev-shared-secret` |
| `SOURCEBRIDGE_JWT_SECRET` | JWT signing key | `dev-jwt-secret-change-me` |
| `SOURCEBRIDGE_LLM_PROVIDER` | LLM backend: `anthropic`, `openai`, `ollama`, `vllm` | `ollama` |
| `SOURCEBRIDGE_LLM_MODEL` | Model name | `qwen3:32b` |
| `SOURCEBRIDGE_LLM_BASE_URL` | Base URL for the LLM API | (provider default) |
| `SOURCEBRIDGE_LLM_API_KEY` | API key for cloud LLM providers | -- |
| `SOURCEBRIDGE_EMBEDDING_PROVIDER` | Embedding backend: `voyage`, `openai`, `ollama` | -- |
| `VOYAGE_API_KEY` | API key for Voyage AI embeddings | -- |

### Customizing the LLM provider

Edit `.env` to switch providers. Examples:

**Anthropic (cloud):**
```
SOURCEBRIDGE_LLM_PROVIDER=anthropic
SOURCEBRIDGE_LLM_MODEL=claude-sonnet-4-20250514
SOURCEBRIDGE_LLM_API_KEY=sk-ant-your-key-here
```

**OpenAI (cloud):**
```
SOURCEBRIDGE_LLM_PROVIDER=openai
SOURCEBRIDGE_LLM_MODEL=gpt-4o
SOURCEBRIDGE_LLM_API_KEY=sk-your-key-here
```

**Ollama (local, free):**
```
SOURCEBRIDGE_LLM_PROVIDER=ollama
SOURCEBRIDGE_LLM_MODEL=qwen3:32b
SOURCEBRIDGE_LLM_BASE_URL=http://host.docker.internal:11434/v1
SOURCEBRIDGE_LLM_API_KEY=not-needed
```

On Linux, replace `host.docker.internal` with `172.17.0.1` (the default Docker
bridge gateway) or use `--add-host=host.docker.internal:host-gateway` in your
Compose override.

**vLLM (local, GPU-accelerated):**
```
SOURCEBRIDGE_LLM_PROVIDER=vllm
SOURCEBRIDGE_LLM_MODEL=Qwen/Qwen2.5-32B-Instruct
SOURCEBRIDGE_LLM_BASE_URL=http://host.docker.internal:8000/v1
SOURCEBRIDGE_LLM_API_KEY=not-needed
```

After editing, restart the worker:

```bash
docker compose restart worker
```

### Persistent storage

SurrealDB data is stored in a named Docker volume (`surrealdb-data`). Data survives
`docker compose down` but is removed by `docker compose down -v`.

### Redis (optional — required for HA MCP)

The bundled `docker compose` stack starts a Redis container by default.
SourceBridge uses it for its shared cache, which backs the **MCP session
store**: sessions created on one replica stay valid when subsequent requests
hit another replica.

If you're not running MCP or only running a single API replica, you can
remove the `redis` service from `docker-compose.yml` and set
`SOURCEBRIDGE_STORAGE_REDIS_MODE=memory`. SourceBridge will keep the cache
in-process.

To customize the Redis URL, set `SOURCEBRIDGE_STORAGE_REDIS_URL` (e.g.
`redis://host:6379/0`). With `redis_mode=external` and no reachable Redis,
the server logs a warning at startup and falls back to in-memory — it
never refuses to boot.

See [docs/user/mcp-clients.md](user/mcp-clients.md) for full MCP client
setup and the multi-replica reliability notes.

To back up the volume:

```bash
docker run --rm -v sourcebridge_surrealdb-data:/data -v $(pwd):/backup \
  alpine tar czf /backup/surrealdb-backup.tar.gz -C /data .
```

### Updating

```bash
git pull
docker compose up -d --build
```

To pull new pre-built images instead of building locally:

```bash
docker compose pull
docker compose up -d
```

### Stopping

```bash
docker compose down        # stop containers, keep data
docker compose down -v     # stop containers AND remove data volumes
```

---

## 2. Kubernetes / Helm

SourceBridge ships a Helm chart for production Kubernetes deployments.

### Prerequisites

- Kubernetes 1.24+
- Helm 3.x
- `kubectl` configured for your cluster
- A default StorageClass (or specify one explicitly)

### Basic install

```bash
# From a local clone
helm install sourcebridge deploy/helm/sourcebridge/ \
  --namespace sourcebridge --create-namespace

# Or from the Helm repository (when published)
helm repo add sourcebridge https://charts.sourcebridge.dev
helm repo update
helm install sourcebridge sourcebridge/sourcebridge \
  --namespace sourcebridge --create-namespace
```

Verify:

```bash
kubectl -n sourcebridge get pods
kubectl -n sourcebridge get svc
```

### Configuration via values.yaml

Create a `my-values.yaml` and pass it with `--values`:

```bash
helm install sourcebridge deploy/helm/sourcebridge/ \
  --namespace sourcebridge --create-namespace \
  --values my-values.yaml
```

The full values reference is in `deploy/helm/sourcebridge/values.yaml`. The most
important sections are documented below.

### Common configurations

#### With Traefik ingress

```yaml
global:
  domain: sourcebridge.example.com

ingress:
  enabled: true
  className: traefik
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-production
```

#### With nginx ingress

```yaml
global:
  domain: sourcebridge.example.com

ingress:
  enabled: true
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
    cert-manager.io/cluster-issuer: letsencrypt-production
```

#### With TLS

```yaml
global:
  domain: sourcebridge.example.com
  tls:
    enabled: true
    secretName: sourcebridge-tls    # pre-existing TLS secret

ingress:
  enabled: true
  className: traefik
```

Or let cert-manager provision the certificate:

```yaml
global:
  domain: sourcebridge.example.com
  tls:
    enabled: true

ingress:
  enabled: true
  className: traefik
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-production
  tls:
    - hosts:
        - sourcebridge.example.com
      secretName: sourcebridge-tls
```

#### With external SurrealDB

Disable the built-in SurrealDB and point to your own instance:

```yaml
surrealdb:
  enabled: false

api:
  env:
    SOURCEBRIDGE_STORAGE_SURREAL_MODE: "external"
    SOURCEBRIDGE_STORAGE_SURREAL_URL: "ws://my-surrealdb:8000/rpc"
    SOURCEBRIDGE_STORAGE_SURREAL_NAMESPACE: "sourcebridge"
    SOURCEBRIDGE_STORAGE_SURREAL_DATABASE: "main"
    SOURCEBRIDGE_STORAGE_SURREAL_USER: "root"
    SOURCEBRIDGE_STORAGE_SURREAL_PASS: "your-password"
```

#### With Anthropic API

```yaml
secrets:
  create: true
  llmApiKey: "sk-ant-your-key-here"

worker:
  env:
    SOURCEBRIDGE_WORKER_LLM_PROVIDER: "anthropic"
    SOURCEBRIDGE_WORKER_LLM_MODEL: "claude-sonnet-4-20250514"
    SOURCEBRIDGE_WORKER_LLM_BASE_URL: ""
```

#### With OpenAI API

```yaml
secrets:
  create: true
  llmApiKey: "sk-your-key-here"

worker:
  env:
    SOURCEBRIDGE_WORKER_LLM_PROVIDER: "openai"
    SOURCEBRIDGE_WORKER_LLM_MODEL: "gpt-4o"
    SOURCEBRIDGE_WORKER_LLM_BASE_URL: ""
```

#### With local Ollama

If Ollama runs on the cluster nodes or as a separate service:

```yaml
worker:
  env:
    SOURCEBRIDGE_WORKER_LLM_PROVIDER: "ollama"
    SOURCEBRIDGE_WORKER_LLM_MODEL: "qwen3:32b"
    SOURCEBRIDGE_WORKER_LLM_BASE_URL: "http://ollama.ollama.svc.cluster.local:11434/v1"
    SOURCEBRIDGE_WORKER_LLM_API_KEY: "not-needed"
```

If Ollama runs on the host (outside the cluster), use the node IP or a NodePort
service to make it reachable from worker pods.

### Upgrading

```bash
helm upgrade sourcebridge deploy/helm/sourcebridge/ \
  --namespace sourcebridge --values my-values.yaml
```

### Uninstalling

```bash
helm uninstall sourcebridge --namespace sourcebridge

# Remove persistent volume claims if you want to delete data
kubectl -n sourcebridge delete pvc --all
```

### Helm values reference

| Key | Description | Default |
|---|---|---|
| `global.domain` | Public domain for ingress | `sourcebridge.example.com` |
| `global.tls.enabled` | Enable TLS on ingress | `false` |
| `global.tls.secretName` | Pre-existing TLS secret name | `""` |
| `api.image.repository` | API server image | `sourcebridge/api` |
| `api.image.tag` | API server image tag | `latest` |
| `api.replicas` | API server replicas | `1` |
| `api.port` | API server port | `8080` |
| `api.resources` | API server resource requests/limits | 250m-1 CPU, 512Mi-1Gi |
| `web.image.repository` | Web UI image | `sourcebridge/web` |
| `web.image.tag` | Web UI image tag | `latest` |
| `web.replicas` | Web UI replicas | `1` |
| `web.port` | Web UI port | `3000` |
| `worker.image.repository` | Worker image | `sourcebridge/worker` |
| `worker.image.tag` | Worker image tag | `latest` |
| `worker.replicas` | Worker replicas | `1` |
| `worker.env` | Worker environment variables (LLM config) | (see values.yaml) |
| `surrealdb.enabled` | Deploy SurrealDB as part of the release | `true` |
| `surrealdb.image.tag` | SurrealDB image tag | `latest` |
| `surrealdb.persistence.enabled` | Enable persistent storage for SurrealDB | `true` |
| `surrealdb.persistence.size` | PVC size for SurrealDB | `1Gi` |
| `surrealdb.persistence.storageClass` | StorageClass (empty = default) | `""` |
| `redis.enabled` | Deploy Redis as part of the release | `true` |
| `redis.persistence.enabled` | Enable persistent storage for Redis | `true` |
| `redis.persistence.size` | PVC size for Redis | `1Gi` |
| `ingress.enabled` | Create an Ingress resource | `false` |
| `ingress.className` | Ingress class name | `traefik` |
| `ingress.annotations` | Extra Ingress annotations | `{}` |
| `ingress.tls` | Custom TLS configuration for Ingress | `[]` |
| `secrets.create` | Create a Kubernetes Secret from values | `false` |
| `secrets.llmApiKey` | LLM provider API key | `""` |
| `secrets.surrealdbUser` | SurrealDB admin username | `root` |
| `secrets.surrealdbPassword` | SurrealDB admin password (auto-generated if empty) | `""` |

---

## 3. From Source

Building from source is the best option for contributors and for running the latest
unreleased code.

### Build steps

```bash
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge

# Build the Go API server
make build-go

# Install Python worker dependencies
cd workers && uv sync && cd ..

# Install web UI dependencies
cd web && npm ci && cd ..
```

Or use the guided setup:

```bash
./setup.sh    # choose "Local development" mode
```

### Running in development mode

Start each component in a separate terminal:

**Terminal 1 -- API server:**
```bash
make dev
```

The API server starts at http://localhost:8080 with embedded SurrealDB. It watches
for file changes if you have `air` installed.

**Terminal 2 -- Web UI:**
```bash
make dev-web
```

The Next.js dev server starts at http://localhost:3000 with hot module replacement.

**Terminal 3 -- Worker:**
```bash
cd workers
uv run python -m workers
```

The gRPC worker listens on port 50051.

### Running in production mode

For a non-containerized production deployment:

```bash
# Build optimized binaries and assets
make build-go
cd web && npm ci && npm run build && cd ..

# Create a production config
cp config.toml.example config.toml
# Edit config.toml with your settings (see Configuration Reference below)

# Run the API server
./bin/sourcebridge serve

# Run the worker (in a separate process or via systemd)
cd workers && uv run python -m workers
```

The web UI is a standalone Next.js application. After `npm run build`, serve it with:

```bash
cd web
node .next/standalone/server.js
```

---

## 4. Configuration Reference

SourceBridge reads configuration from two sources, in order of precedence:

1. **Environment variables** (highest precedence)
2. **config.toml file** (in the working directory or `~/.sourcebridge/config.toml`)

Environment variables use the `SOURCEBRIDGE_` prefix with nested keys joined by
underscores. For example, `[llm] provider` maps to `SOURCEBRIDGE_LLM_PROVIDER`.

### config.toml sections

#### [llm] -- LLM provider

Controls the large language model used for reasoning operations (review, discussion,
field guide generation, requirement tracing).

```toml
[llm]
provider      = "anthropic"               # anthropic | openai | ollama | vllm
base_url      = ""                         # leave empty for SDK default
summary_model = "claude-sonnet-4-20250514"  # model for field guide / summary
review_model  = "claude-sonnet-4-20250514"  # model for code review
ask_model     = "claude-sonnet-4-20250514"  # model for discussion / Q&A
```

#### [server] -- HTTP and gRPC ports

```toml
[server]
http_port = 8080
grpc_port = 50051
```

#### [storage] -- Database backend

```toml
[storage]
surreal_mode      = "embedded"    # embedded | external
surreal_url       = ""            # ws://host:port/rpc (external mode only)
surreal_namespace = "sourcebridge"
surreal_database  = "sourcebridge"
surreal_user      = "root"
surreal_pass      = "root"
redis_mode        = "memory"      # memory | external — see "When to use Redis" below
redis_url         = ""            # redis://host:port (external mode only)
```

##### When to use Redis

Redis is optional. Leave `redis_mode = "memory"` for single-node, single-replica
installs — SourceBridge keeps its cache in-process and everything works.

Switch to `redis_mode = "external"` when:

- You run the API with **more than one replica** and want the **MCP server**
  to stay reliable. MCP sessions are stored in this cache; with a shared
  Redis, any replica can handle any MCP request. Without it, clients see
  intermittent `"Invalid or expired session"` errors as requests round-robin
  across pods.
- You want cache state to survive pod restarts.

If `redis_mode = "external"` is set but SourceBridge can't reach the URL
at startup, it logs a warning and falls back to in-memory — startup does
not fail. Fix the Redis connection to re-enable HA-safe MCP.

Any Redis-compatible server works (Redis 6+, KeyDB, Dragonfly, Upstash over
TLS). The bundled Helm chart enables Redis by default; see `redis.enabled`
below.

#### [security] -- Authentication mode

```toml
[security]
mode            = "oss"           # oss | enterprise
jwt_secret      = ""              # required for token auth
grpc_auth_secret = ""             # shared secret for API <-> worker
```

### Environment variable mapping

| Environment Variable | config.toml Key | Description |
|---|---|---|
| `SOURCEBRIDGE_LLM_PROVIDER` | `[llm] provider` | LLM provider name |
| `SOURCEBRIDGE_LLM_BASE_URL` | `[llm] base_url` | LLM API endpoint |
| `SOURCEBRIDGE_LLM_SUMMARY_MODEL` | `[llm] summary_model` | Model for summaries |
| `SOURCEBRIDGE_LLM_REVIEW_MODEL` | `[llm] review_model` | Model for reviews |
| `SOURCEBRIDGE_LLM_ASK_MODEL` | `[llm] ask_model` | Model for Q&A |
| `SOURCEBRIDGE_SERVER_HTTP_PORT` | `[server] http_port` | API server port |
| `SOURCEBRIDGE_SERVER_GRPC_PORT` | `[server] grpc_port` | gRPC port |
| `SOURCEBRIDGE_STORAGE_SURREAL_MODE` | `[storage] surreal_mode` | Database mode |
| `SOURCEBRIDGE_STORAGE_SURREAL_URL` | `[storage] surreal_url` | SurrealDB URL |
| `SOURCEBRIDGE_STORAGE_SURREAL_NAMESPACE` | `[storage] surreal_namespace` | SurrealDB namespace |
| `SOURCEBRIDGE_STORAGE_SURREAL_DATABASE` | `[storage] surreal_database` | SurrealDB database |
| `SOURCEBRIDGE_STORAGE_SURREAL_USER` | `[storage] surreal_user` | SurrealDB username |
| `SOURCEBRIDGE_STORAGE_SURREAL_PASS` | `[storage] surreal_pass` | SurrealDB password |
| `SOURCEBRIDGE_STORAGE_REDIS_MODE` | `[storage] redis_mode` | Redis mode |
| `SOURCEBRIDGE_STORAGE_REDIS_URL` | `[storage] redis_url` | Redis URL |
| `SOURCEBRIDGE_SECURITY_MODE` | `[security] mode` | Auth mode |
| `SOURCEBRIDGE_SECURITY_JWT_SECRET` | `[security] jwt_secret` | JWT signing key |
| `SOURCEBRIDGE_SECURITY_GRPC_AUTH_SECRET` | `[security] grpc_auth_secret` | gRPC shared secret |

### LLM provider configuration

| Provider | `provider` value | `base_url` | `api_key` | Notes |
|---|---|---|---|---|
| Anthropic | `anthropic` | (leave empty) | `ANTHROPIC_API_KEY` | Claude models via cloud API |
| OpenAI | `openai` | (leave empty) | `OPENAI_API_KEY` | GPT models via cloud API |
| Ollama | `ollama` | `http://localhost:11434/v1` | `not-needed` | Local inference, no API key |
| vLLM | `vllm` | `http://localhost:8000/v1` | `not-needed` | Local GPU-accelerated inference |
| Any OpenAI-compatible | `openai` | Custom endpoint URL | As required | Works with LiteLLM, LocalAI, etc. |

### Embedding provider configuration

Embeddings are used for semantic search and knowledge retrieval in large repositories.

| Provider | Worker env vars | Notes |
|---|---|---|
| Voyage AI | `SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=voyage`, `VOYAGE_API_KEY=...` | Recommended for production |
| OpenAI | `SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=openai`, `OPENAI_API_KEY=...` | Uses text-embedding-3-small |
| Ollama | `SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=ollama`, `SOURCEBRIDGE_WORKER_EMBEDDING_BASE_URL=http://localhost:11434` | Local, uses nomic-embed-text |

---

## 5. Troubleshooting

### Health check endpoints

| Endpoint | Component | Expected response |
|---|---|---|
| `GET /healthz` | API server | `200 OK` |
| `GET /readyz` | API server | `200 OK` when fully initialized |
| `GET /metrics` | API server | Prometheus metrics |
| `GET /health` (port 8000) | SurrealDB | `200 OK` |

### Container will not start

**Symptom:** `docker compose ps` shows a container in `restarting` or `exited` state.

**Solutions:**

1. Check logs for the failing container:
   ```bash
   docker compose logs sourcebridge
   docker compose logs worker
   docker compose logs surrealdb
   ```

2. If SurrealDB fails, the data volume may be corrupt. Remove it and restart:
   ```bash
   docker compose down -v
   docker compose up -d
   ```

3. If the worker fails with an import error, rebuild the image:
   ```bash
   docker compose build worker
   docker compose up -d worker
   ```

### API server returns 502 or connection refused

**Symptom:** `curl http://localhost:8080/healthz` fails.

**Solutions:**

1. Verify the container is running: `docker compose ps sourcebridge`
2. Check if port 8080 is already in use: `lsof -i :8080`
3. Try a different host port by editing `docker-compose.yml`:
   ```yaml
   ports:
     - "9090:8080"
   ```

### Worker not connecting to API server

**Symptom:** AI features (review, discussion) return errors or time out.

**Solutions:**

1. Check the worker logs: `docker compose logs worker`
2. Verify the gRPC shared secret matches in both the API server and worker environment
3. Ensure SurrealDB is healthy: `curl http://localhost:8000/health`

### "Python worker required" error

**Symptom:** The CLI command `sourcebridge review` or `sourcebridge ask` shows
"Python worker required".

**Solutions:**

1. Install Python 3.12+ and uv:
   ```bash
   curl -LsSf https://astral.sh/uv/install.sh | sh
   ```
2. Install worker dependencies:
   ```bash
   cd workers && uv sync
   ```
3. Start the worker:
   ```bash
   cd workers && uv run python -m workers
   ```

### LLM features not working

**Symptom:** Review, discussion, or field guide requests return empty results or
generic errors.

**Solutions:**

1. Verify your LLM provider is configured correctly (check `.env` or `config.toml`)
2. For cloud providers, verify the API key is valid:
   ```bash
   # Anthropic
   curl https://api.anthropic.com/v1/messages \
     -H "x-api-key: $ANTHROPIC_API_KEY" \
     -H "anthropic-version: 2023-06-01" \
     -H "content-type: application/json" \
     -d '{"model":"claude-sonnet-4-20250514","max_tokens":10,"messages":[{"role":"user","content":"Hi"}]}'
   ```
3. For Ollama, verify the model is pulled and Ollama is running:
   ```bash
   ollama list
   curl http://localhost:11434/v1/models
   ```
4. Check worker logs for LLM errors: `docker compose logs worker`

### No symbols found after indexing

**Symptom:** Indexing completes but queries return no symbols.

**Solutions:**

1. Verify the repository path exists and contains source files
2. Confirm the language is supported: Go, Python, TypeScript, JavaScript, Java, Rust, C, C++, C#
3. Re-index with verbose logging:
   ```bash
   sourcebridge index /path/to/repo --verbose
   ```

### Port conflicts

**Symptom:** Container fails to start because the port is already allocated.

**Default ports used:**

| Port | Service |
|---|---|
| 3000 | Web UI |
| 8080 | API server |
| 8000 | SurrealDB |
| 50051 | Worker (gRPC) |

To resolve, either stop the conflicting process or change the port mapping in
`docker-compose.yml`.

### Viewing logs

```bash
# All containers
docker compose logs -f

# Specific container
docker compose logs -f sourcebridge
docker compose logs -f worker
docker compose logs -f web
docker compose logs -f surrealdb

# Kubernetes
kubectl -n sourcebridge logs -f deployment/sourcebridge-api
kubectl -n sourcebridge logs -f deployment/sourcebridge-worker
kubectl -n sourcebridge logs -f deployment/sourcebridge-web

# From-source (API server)
make dev    # logs go to stdout

# From-source (worker)
cd workers && uv run python -m workers    # logs go to stdout
```

### Resetting to a clean state

If something is deeply broken, a full reset is the fastest path:

```bash
# Docker Compose
docker compose down -v
docker compose up -d --build

# Helm
helm uninstall sourcebridge -n sourcebridge
kubectl -n sourcebridge delete pvc --all
helm install sourcebridge deploy/helm/sourcebridge/ -n sourcebridge

# From source (embedded SurrealDB stores data in the working directory)
rm -rf .surreal/
make dev
```
