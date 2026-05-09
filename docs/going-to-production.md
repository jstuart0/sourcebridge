# Going to Production

You ran the demo. It worked. Now you want to run SourceBridge for real.

This guide covers everything between `docker compose up` and a production deployment. Each section is self-contained — jump to what you need.

---

**Quick links:**

| Section | What it covers |
|---|---|
| [1. Security](#1-security) | Secrets, credentials, TLS |
| [2. Storage](#2-storage) | Persistent database, Redis, repo cache |
| [3. LLM Provider](#3-llm-provider) | Cloud vs. local, model selection, API keys |
| [4. Networking](#4-networking) | CORS, reverse proxy, public URL |
| [5. SSO / OIDC](#5-sso--oidc) | Connect to your identity provider |
| [6. Backups](#6-backups) | Database and artifact protection |
| [7. Monitoring](#7-monitoring) | Health checks, readiness, logs |
| [8. Resource Sizing](#8-resource-sizing) | CPU, memory, storage requirements |
| [9. Kubernetes / Helm](#9-kubernetes--helm) | Moving from Compose to Kubernetes |
| [Troubleshooting](#troubleshooting) | Common errors and how to fix them |
| [FAQ](#faq) | Frequently asked questions |

---

## 1. Security

The demo ships with intentionally weak secrets. Before exposing SourceBridge to users, change all of them.

### JWT Secret

The JWT secret signs all user session tokens. **CA-311 (2026-05-08): the
old `dev-secret-change-in-production` literal fallback was removed. The
server now refuses to start when the resolved JWT secret is shorter than
32 bytes**. The default `docker-compose` files ship with a publicly-known
64-hex placeholder so the quickstart still boots; for any non-throwaway
deployment, override it.

Three ways to provide the secret, in order of preference:

```bash
# 1. (preferred) File path — matches Vault / Postgres _FILE convention
#    Files are higher-trust than env vars (no leak via /proc, docker
#    inspect, or shell history).
echo -n "$(openssl rand -hex 32)" > /run/secrets/sourcebridge-jwt
SOURCEBRIDGE_SECURITY_JWT_SECRET_FILE=/run/secrets/sourcebridge-jwt

# 2. Literal env var
SOURCEBRIDGE_SECURITY_JWT_SECRET=$(openssl rand -hex 32)

# 3. config.toml (file path)
[security]
jwt_secret_file = "/run/secrets/sourcebridge-jwt"
```

When BOTH are set, the file wins and a WARN is logged.

When **neither** is set, the API auto-generates a 64-hex-char in-memory
secret per process and emits a one-time INFO log. **Sessions invalidated
on every restart in this mode** — production multi-replica deployments
must provide the secret externally (file or env), otherwise replicas have
different secrets and auth fails non-deterministically.

The Helm chart's existing `randAlphaNum 32` Secret remains the
recommended path for Kubernetes deployments; it produces a 32-character
ASCII secret that satisfies the new length gate and is shared by all
replicas via Kubernetes Secret mounting.

If you change this after users have logged in, all existing sessions
will be invalidated. Users will need to log in again.

### gRPC Auth Secret

The API server and Python worker authenticate to each other using a shared gRPC secret. The demo default is `dev-shared-secret`.

```bash
# Generate a new secret
openssl rand -base64 32
```

Set it on **both** services:

```bash
# API server
SOURCEBRIDGE_SECURITY_GRPC_AUTH_SECRET=your-grpc-secret

# Worker (note the different prefix)
SOURCEBRIDGE_WORKER_GRPC_AUTH_SECRET=your-grpc-secret
```

These must match exactly. If they don't, the worker will reject all requests from the API server and AI features will be unavailable.

### SurrealDB Credentials

The demo uses `root` / `root`. Change both:

```bash
SOURCEBRIDGE_STORAGE_SURREAL_USER=sourcebridge
SOURCEBRIDGE_STORAGE_SURREAL_PASS=your-database-password
```

Set the same credentials on the SurrealDB instance itself. If using Docker Compose, update the `command` in your compose file:

```yaml
surrealdb:
  command: start --user sourcebridge --pass your-database-password --log info file:/data/database.db
```

### Encryption Key

Used for encrypting sensitive data at rest:

```bash
SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY=$(openssl rand -base64 32)
```

### TLS / HTTPS

SourceBridge does not terminate TLS itself. Place a reverse proxy in front of it (Traefik, nginx, Caddy, cloud load balancer). The Go server listens on plain HTTP.

This is important because SourceBridge sets `Secure: true` on its CSRF cookie. If you access the API over plain HTTP without a TLS-terminating proxy, cookie-based authentication will silently fail — the browser won't send the cookie on non-HTTPS requests.

### Webhook Secrets (Optional)

If you receive webhooks from GitHub or GitLab:

```bash
SOURCEBRIDGE_SECURITY_GITHUB_WEBHOOK_SECRET=your-github-secret
SOURCEBRIDGE_SECURITY_GITLAB_WEBHOOK_SECRET=your-gitlab-secret
```

### Git URL SSRF Protection (CA-312)

By default, SourceBridge rejects git clone/pull URLs that resolve to private,
loopback, link-local, CGNAT, unspecified (0.0.0.0/::), or multicast addresses.
This blocks SSRF attacks on multi-tenant or internet-exposed deployments.

**Self-hosted Forgejo/Gitea operators** running on an internal network will see
`ErrPrivateIPNotAllowed` when they register an internal repository URL. Opt in
by setting:

```bash
SOURCEBRIDGE_INDEXING_ALLOW_PRIVATE_GIT_HOSTS=true
```

**Do not enable this on multi-tenant or publicly reachable instances.** It is
safe only for single-operator self-hosted environments where the operator fully
controls DNS and network routing.

### gRPC Reflection (Development Only)

The worker exposes gRPC reflection for grpcurl-based debugging. It is disabled
by default and requires **both** flags to be set:

```bash
SOURCEBRIDGE_WORKER_DEBUG=true
SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED=true
```

Setting only `SOURCEBRIDGE_WORKER_DEBUG=true` does NOT enable reflection (CA-202).
Leave both flags unset (the default) in production.

---

## 2. Storage

The demo uses in-memory storage. Everything is lost on restart. Production requires external SurrealDB.

### Switch to External SurrealDB

```bash
SOURCEBRIDGE_STORAGE_SURREAL_MODE=external
SOURCEBRIDGE_STORAGE_SURREAL_URL=ws://your-surrealdb-host:8000/rpc
SOURCEBRIDGE_STORAGE_SURREAL_NAMESPACE=sourcebridge
SOURCEBRIDGE_STORAGE_SURREAL_DATABASE=sourcebridge
SOURCEBRIDGE_STORAGE_SURREAL_USER=sourcebridge
SOURCEBRIDGE_STORAGE_SURREAL_PASS=your-database-password
```

When you switch to `external` mode, the API server automatically runs database migrations on startup. There are 27 migration files that create the schema for repositories, knowledge artifacts, auth tokens, jobs, and more.

The migration files must be accessible to the server. In Docker, they're baked into the image at `/migrations`. If running from source, they're found relative to the binary.

### What changes in external mode

| Capability | Embedded (demo) | External (production) |
|---|---|---|
| Data persistence | Lost on restart | Survives restarts |
| Database migrations | Not run | Run automatically on startup |
| Auth tokens | In-memory | Persisted to SurrealDB |
| LLM config | Env vars only | Env vars + DB-stored config |
| Git credentials | In-memory | Persisted to SurrealDB |
| Knowledge artifacts | In-memory | Persisted to SurrealDB |
| Job queue | In-memory | SurrealDB-backed |

### Redis (Optional)

Redis provides a shared cache layer. By default, SourceBridge uses an in-memory cache which works for single-instance deployments.

Switch to external Redis if you run multiple API server replicas:

```bash
SOURCEBRIDGE_STORAGE_REDIS_MODE=external
SOURCEBRIDGE_STORAGE_REDIS_URL=redis://your-redis-host:6379
```

### Repository Cache Path

Cloned repositories are cached locally. Set a persistent path:

```bash
SOURCEBRIDGE_STORAGE_REPO_CACHE_PATH=/data/repo-cache
```

In Docker Compose, mount this as a volume so cloned repos survive container restarts.

---

## 3. LLM Provider

SourceBridge needs an LLM to generate field guides, code tours, and reviews. The demo may have auto-detected Ollama or used a demo API key. For production, choose deliberately.

### Cloud Providers

Best quality, simplest setup, pay-per-use:

```bash
# Anthropic (recommended for best quality)
SOURCEBRIDGE_LLM_PROVIDER=anthropic
SOURCEBRIDGE_LLM_API_KEY=sk-ant-...

# OpenAI
SOURCEBRIDGE_LLM_PROVIDER=openai
SOURCEBRIDGE_LLM_API_KEY=sk-...

# Google Gemini
SOURCEBRIDGE_LLM_PROVIDER=gemini
SOURCEBRIDGE_LLM_API_KEY=your-google-api-key

# OpenRouter (access to many models)
SOURCEBRIDGE_LLM_PROVIDER=openrouter
SOURCEBRIDGE_LLM_API_KEY=your-openrouter-key
```

### Local / Self-Hosted Providers

Your code stays on your network. No external API calls:

```bash
# Ollama (easiest)
SOURCEBRIDGE_LLM_PROVIDER=ollama
SOURCEBRIDGE_LLM_BASE_URL=http://your-ollama-host:11434/v1
SOURCEBRIDGE_LLM_MODEL=qwen3:32b

# vLLM
SOURCEBRIDGE_LLM_PROVIDER=vllm
SOURCEBRIDGE_LLM_BASE_URL=http://your-vllm-host:8000/v1
SOURCEBRIDGE_LLM_MODEL=your-model

# llama.cpp
SOURCEBRIDGE_LLM_PROVIDER=llama-cpp
SOURCEBRIDGE_LLM_BASE_URL=http://your-llamacpp-host:8080/v1

# SGLang
SOURCEBRIDGE_LLM_PROVIDER=sglang
SOURCEBRIDGE_LLM_BASE_URL=http://your-sglang-host:30000/v1

# LM Studio
SOURCEBRIDGE_LLM_PROVIDER=lmstudio
SOURCEBRIDGE_LLM_BASE_URL=http://your-lmstudio-host:1234/v1
```

Local providers require `base_url` — the server will not start without it.

### Embedding Provider

Embeddings power semantic search and requirement linking. Configured separately on the worker:

```bash
# Ollama (local, free)
SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=ollama
SOURCEBRIDGE_WORKER_EMBEDDING_BASE_URL=http://your-ollama-host:11434
SOURCEBRIDGE_WORKER_EMBEDDING_MODEL=nomic-embed-text

# OpenAI-compatible
SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=openai-compatible
SOURCEBRIDGE_WORKER_EMBEDDING_BASE_URL=http://your-endpoint
SOURCEBRIDGE_WORKER_EMBEDDING_API_KEY=your-key
```

### Model Selection

For cloud providers, the model is set on the API server (which sends it to the worker with each request):

```bash
# Default model for all operations
SOURCEBRIDGE_LLM_SUMMARY_MODEL=claude-sonnet-4-20250514
```

For advanced setups, you can use different models for different operations:

```toml
# config.toml
[llm]
advanced_mode = true
summary_model = "claude-sonnet-4-20250514"    # analysis, summaries
review_model = "claude-sonnet-4-20250514"     # code reviews
discussion_model = "claude-sonnet-4-20250514" # code discussions
knowledge_model = "claude-sonnet-4-20250514"  # field guides
report_model = "claude-sonnet-4-20250514"     # reports
```

### API Key Validation

SourceBridge does **not** validate your API key at startup. You won't know it's wrong until the first LLM operation runs. To test your configuration:

1. Log in to the web UI
2. Go to **Admin** > **Test LLM Connection**
3. Or use the API: `POST /api/v1/admin/test-llm`

---

## 4. Networking

### Public Base URL

Tell SourceBridge its public-facing URL. This is used for OIDC redirects and telemetry identification:

```bash
SOURCEBRIDGE_SERVER_PUBLIC_BASE_URL=https://sourcebridge.yourcompany.com
```

### CORS Origins

The demo allows `http://localhost:3300`. In production, set this to your actual web UI origin:

```bash
SOURCEBRIDGE_SERVER_CORS_ORIGINS=https://sourcebridge.yourcompany.com
```

Multiple origins can be comma-separated.

### Trusted Proxies

If SourceBridge is behind a reverse proxy or load balancer, configure trusted proxies so rate limiting uses the correct client IP:

```toml
[server]
trusted_proxies = ["10.0.0.0/8", "172.16.0.0/12"]
```

### Web UI API URL

The Next.js web UI needs to know where the API server is. This is baked into the Docker image at build time via `NEXT_PUBLIC_API_URL`.

- In **Docker Compose**: The web container proxies to `http://sourcebridge:8080` via internal Docker networking
- Behind a **reverse proxy**: Both the web UI and API should be on the same domain (different paths or ports) to avoid CORS issues
- The simplest production setup: reverse proxy at `https://sourcebridge.yourcompany.com` that routes `/api/*` and `/auth/*` to the API server, and everything else to the web UI

### Ports

| Service | Default Port | Env Var |
|---|---|---|
| API server | 8080 | `SOURCEBRIDGE_SERVER_HTTP_PORT` |
| Web UI | 3000 | `PORT` (Next.js) |
| Worker (gRPC) | 50051 | `SOURCEBRIDGE_WORKER_GRPC_PORT` |
| SurrealDB | 8000 | `SOURCEBRIDGE_SURREALDB_PORT` |

---

## 5. SSO / OIDC

SourceBridge supports any OIDC-compatible identity provider (Authentik, Keycloak, Okta, Auth0, Azure AD, Google Workspace).

### Configuration

```bash
SOURCEBRIDGE_SECURITY_OIDC_ISSUER_URL=https://your-idp.com/application/o/sourcebridge/
SOURCEBRIDGE_SECURITY_OIDC_CLIENT_ID=your-client-id
SOURCEBRIDGE_SECURITY_OIDC_CLIENT_SECRET=your-client-secret
SOURCEBRIDGE_SECURITY_OIDC_REDIRECT_URL=https://sourcebridge.yourcompany.com/auth/oidc/callback
SOURCEBRIDGE_SECURITY_OIDC_SCOPES=openid,profile,email
```

OIDC is activated when both `client_id` and `issuer_url` are set. The local password login continues to work alongside OIDC.

### What your IdP needs

1. Create an OIDC/OAuth2 application for SourceBridge
2. Set the redirect URI to `https://your-sourcebridge-url/auth/oidc/callback`
3. Enable the `openid`, `profile`, and `email` scopes
4. Note the client ID, client secret, and issuer URL

### Claims

SourceBridge reads these claims from the ID token:

| Claim | Used for | Fallback |
|---|---|---|
| `email` | User identity | `sub` + `@oidc` |
| `name` | Display name | — |
| `sub` | Unique user ID | Required |
| `org_id` | Organization mapping | — |
| `role` | Role assignment | — |

---

## 6. Backups

SourceBridge stores all persistent data in SurrealDB. Protect it.

### SurrealDB Export

SurrealDB supports native export:

```bash
# Export the full database
surreal export --conn http://your-surrealdb:8000 \
  --user sourcebridge --pass your-password \
  --ns sourcebridge --db sourcebridge \
  > backup-$(date +%Y%m%d).surql

# Import a backup
surreal import --conn http://your-surrealdb:8000 \
  --user sourcebridge --pass your-password \
  --ns sourcebridge --db sourcebridge \
  backup-20260414.surql
```

### Automated Backups

Add a cron job or Kubernetes CronJob:

```bash
# Daily backup at 2am, keep 7 days
0 2 * * * surreal export --conn http://surrealdb:8000 --user sourcebridge --pass $DB_PASS --ns sourcebridge --db sourcebridge > /backups/sourcebridge-$(date +\%Y\%m\%d).surql && find /backups -name "sourcebridge-*.surql" -mtime +7 -delete
```

### Application-Level Export

SourceBridge can export individual artifacts and data:

| Endpoint | Format | What it exports |
|---|---|---|
| `GET /api/v1/export/knowledge/{id}?format=json` | JSON, Markdown, HTML | A single knowledge artifact with sections and evidence |
| `GET /api/v1/export/traceability/{repoId}?format=csv` | CSV, JSON | Requirement-to-symbol traceability links |
| `GET /api/v1/export/requirements/{repoId}` | CSV | Imported requirements |
| `GET /api/v1/export/symbols/{repoId}` | CSV | Code symbols |

### Volume Backups

If SurrealDB uses file-based storage (the default Docker setup), you can also back up the data volume directly:

```bash
# Docker Compose
docker compose stop surrealdb
docker run --rm -v sourcebridge_surrealdb-data:/data -v $(pwd):/backup alpine tar czf /backup/surrealdb-data.tar.gz /data
docker compose start surrealdb
```

---

## 7. Monitoring

### Health Endpoints

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /healthz` | No | Basic liveness — returns `"ok"` |
| `GET /readyz` | No | Detailed readiness — returns per-component status |

`/readyz` response example:

```json
{
  "status": "healthy",
  "components": {
    "api": { "status": "healthy" },
    "database": { "status": "healthy" },
    "worker": { "status": "healthy" }
  }
}
```

Status values: `healthy`, `degraded` (worker unavailable but DB is fine), `unavailable` (database down — returns HTTP 503).

The worker being unavailable does **not** return 503 — the API continues to serve indexed data, search, and file browsing. Only AI generation features are affected.

### What to Monitor

- `/healthz` for liveness probes (Kubernetes, load balancer health checks)
- `/readyz` for readiness probes and alerting
- `worker` component status in `/readyz` — if persistently `unavailable`, check gRPC connectivity
- Docker/Kubernetes container restart counts — SurrealDB crashes indicate storage issues
- Worker logs for `hierarchical_node_fallback` events — indicates LLM failures

### Rate Limits

Built-in rate limits protect the server:

| Scope | Limit | What happens |
|---|---|---|
| Global per-IP | 100 req/min | HTTP 429 |
| Auth endpoints | 10 req/min | HTTP 429 |
| AI operations | 5 concurrent | HTTP 429 with `"AI operations at capacity"` |
| MCP sessions | 100 total | HTTP 429 with `"too many MCP sessions"` |

---

## 8. Resource Sizing

### Minimum Requirements

| Component | CPU | Memory | Storage |
|---|---|---|---|
| API server | 250m | 256Mi | — |
| Web UI | 100m | 128Mi | — |
| Worker | 500m | 512Mi | — |
| SurrealDB | 500m | 512Mi | 20Gi |
| **Total minimum** | **~2 cores** | **~4 GB** | **20 GB** |

### Recommended for Production

| Component | CPU | Memory | Storage |
|---|---|---|---|
| API server | 1000m | 1Gi | — |
| Web UI | 500m | 512Mi | — |
| Worker | 2000m | 2Gi | — |
| SurrealDB | 2000m | 4Gi | 50Gi |
| Redis (optional) | 500m | 512Mi | 1Gi |
| **Total recommended** | **~6 cores** | **~8 GB** | **50 GB** |

### Scaling Considerations

- **Repo count**: SurrealDB storage grows ~50-100MB per indexed repository depending on size
- **Concurrent users**: A single API server handles ~50 concurrent users comfortably
- **AI generation**: The worker processes one knowledge artifact at a time per concurrency slot (default 3 slots). Large repos take 5-30 minutes per artifact depending on model speed
- **Write timeout**: The API server has a 360-second write timeout for long AI operations. Don't set your reverse proxy timeout lower than this.

---

## 9. Kubernetes / Helm

When Docker Compose isn't enough — multiple replicas, auto-scaling, or integration with existing cluster infrastructure.

### Install with Helm

```bash
helm install sourcebridge deploy/helm/sourcebridge/ \
  --namespace sourcebridge \
  --create-namespace \
  --set llm.provider=anthropic \
  --set secrets.llmApiKey=$ANTHROPIC_API_KEY \
  --set ingress.enabled=true \
  --set ingress.host=sourcebridge.yourcompany.com
```

The Helm chart handles:
- Secret generation (JWT, gRPC, SurrealDB passwords)
- Pod security context (non-root, read-only filesystem)
- PVC provisioning for SurrealDB and Redis
- Health-check-based readiness and liveness probes
- Ingress configuration

### Key Differences from Compose

| Concern | Docker Compose | Helm |
|---|---|---|
| Secrets | Env vars / `.env` file | Kubernetes Secrets (auto-generated or external) |
| Storage | Docker volumes | PersistentVolumeClaims |
| TLS | Manual reverse proxy | Ingress + cert-manager |
| Security | Runs as root | Non-root (UID 1000), no privilege escalation |
| Scaling | Single instance | Replica count per service |
| Upgrades | `docker compose pull && up` | `helm upgrade` with rollback |

See the full [Helm Guide](self-hosted/helm-guide.md) and [Air-Gapped Installation](self-hosted/air-gapped.md) docs.

---

## Troubleshooting

### Server won't start: "invalid configuration"

**Symptom:** The API server exits immediately with `invalid configuration: ...`

**Common causes:**

| Error message | Fix |
|---|---|
| `invalid LLM provider: <X>` | Must be one of: `anthropic`, `openai`, `ollama`, `vllm`, `llama-cpp`, `sglang`, `lmstudio`, `gemini`, `openrouter` |
| `llm.base_url is required when provider is ollama` | Local providers (`ollama`, `vllm`, `llama-cpp`, `sglang`, `lmstudio`) require `SOURCEBRIDGE_LLM_BASE_URL` |
| `invalid SurrealDB mode: <X>` | Must be `embedded` or `external` |
| `invalid HTTP port: <N>` | Port must be between 1 and 65535 |

### Server won't start: "failed to connect to database"

**Symptom:** `failed to connect to database: surrealdb connect: ...`

The API server cannot reach SurrealDB. Check:

1. Is SurrealDB running? `curl http://your-surrealdb:8000/health`
2. Is the URL correct? It must be a WebSocket URL: `ws://host:8000/rpc` (not `http://`)
3. Are the credentials correct? A wrong username or password produces `surrealdb signin: ...`
4. Is the namespace/database correct? Wrong names produce `surrealdb use ns/db: ...`

### Server won't start: "failed to run migrations"

**Symptom:** `failed to run migrations: reading migrations dir: ...`

The migration `.surql` files can't be found. The server looks in this order:
1. `SOURCEBRIDGE_MIGRATIONS_DIR` env var
2. `/migrations` (Docker container path)
3. Relative to the binary
4. `internal/db/migrations` (source tree)

In Docker, the files are at `/migrations` inside the image. If you're running a custom image, make sure the migration files are copied in.

### Worker unavailable — AI features don't work

**Symptom:** `/readyz` shows `worker: unavailable`. Knowledge generation, reviews, and code discussion return errors.

Check in order:

1. **Is the worker running?** `docker compose ps worker` or check pod status
2. **Can the API server reach the worker?** The worker address defaults to `worker:50051` in compose. Check `SOURCEBRIDGE_WORKER_ADDRESS`
3. **Do the gRPC secrets match?** `SOURCEBRIDGE_SECURITY_GRPC_AUTH_SECRET` on the API server must exactly match `SOURCEBRIDGE_WORKER_GRPC_AUTH_SECRET` on the worker
4. **Is the worker crashing on startup?** Check worker logs. Common causes:
   - `Unknown LLM provider: <X>` — invalid `SOURCEBRIDGE_WORKER_LLM_PROVIDER`
   - `Embedding provider '<X>' not yet implemented` — invalid `SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER` (must be `ollama` or `openai-compatible`)
   - Python dependency errors — rebuild the worker image

### LLM calls fail but the worker is running

**Symptom:** Worker is healthy, but knowledge generation fails with errors in the worker logs.

| Worker log message | Cause | Fix |
|---|---|---|
| `Error code: 401 - authentication_error` | Invalid API key | Check `SOURCEBRIDGE_LLM_API_KEY` / `SOURCEBRIDGE_WORKER_LLM_API_KEY` |
| `Error code: 400 - credit balance is too low` | Anthropic account needs credits | Add credits at console.anthropic.com |
| `Error code: 529 - Overloaded` | Provider temporarily at capacity | Retry later, or switch to a less loaded model |
| `LLM returned empty content ... stop_reason=length` | Model hit max_tokens limit (common with thinking models) | Use a non-thinking model variant, or a larger model |
| `Connection refused` / `ECONNREFUSED` | LLM endpoint unreachable | Check `SOURCEBRIDGE_LLM_BASE_URL` and that the LLM server is running |
| `DEADLINE_EXCEEDED` | Generation took longer than the gRPC timeout | Normal for large repos with slow models. The worker will retry. |

### CSRF / cookie authentication issues

**Symptom:** Login works via API but the web UI shows authentication errors or infinite redirects.

The CSRF cookie is set with `Secure: true`, which means it's only sent over HTTPS. If you're accessing the web UI over plain HTTP:

- In development: this is expected — use `http://localhost` which browsers treat as secure
- In production: ensure TLS termination is working at your reverse proxy
- Check that your reverse proxy forwards the `Host` header correctly

### Web UI shows "Failed to proxy" errors

**Symptom:** The web UI loads but API calls fail with 500 errors. Web container logs show `Failed to proxy http://...`

The Next.js server can't reach the API server. The proxy URL is baked into the Docker image at build time via `NEXT_PUBLIC_API_URL`.

- In Docker Compose: ensure the web service can resolve the `sourcebridge` hostname
- Behind a reverse proxy: both web UI and API should be on the same origin, or configure the web UI to use the correct internal API URL

### SurrealDB keeps restarting

**Symptom:** SurrealDB container restarts in a loop. Logs show `Permission denied` or `Failed to create RocksDB directory`.

This is a known issue with SurrealDB and Docker volumes on some platforms:

```yaml
# Fix: run SurrealDB as root in compose
surrealdb:
  user: root
```

Or use a bind mount instead of a Docker volume:

```yaml
volumes:
  - ./data/surrealdb:/data
```

### Data lost after restart

**Symptom:** Repositories, knowledge artifacts, and settings disappear after restarting the server.

You're running in **embedded mode** (the default). All data is in-memory. Switch to external mode:

```bash
SOURCEBRIDGE_STORAGE_SURREAL_MODE=external
SOURCEBRIDGE_STORAGE_SURREAL_URL=ws://your-surrealdb:8000/rpc
```

See [Section 2: Storage](#2-storage).

---

## FAQ

### Can I run SourceBridge without an LLM?

Yes. Code indexing, file browsing, symbol search, and the deterministic architecture diagram all work without an LLM. Only AI-generated features (cliff notes, code tours, learning paths, reviews, discussions) require one.

### Can I change the LLM provider after initial setup?

Yes. Change the environment variables and restart the services. In external mode, LLM config set through the Admin UI is persisted to the database and survives restarts. Environment variables take precedence over database-stored config.

### How long does indexing take?

Indexing is tree-sitter based (no LLM involved) and is fast:
- Small repos (< 100 files): seconds
- Medium repos (100-1,000 files): 10-30 seconds
- Large repos (1,000+ files): 1-3 minutes

AI artifact generation (cliff notes, tours, etc.) is separate and depends on model speed. A typical repo takes 5-30 minutes for a full field guide with a cloud provider.

### Does my code leave my machine?

Only if you use a cloud LLM provider. The code snippets included in LLM prompts are sent to the provider's API. If you use a local provider (Ollama, vLLM, etc.), nothing leaves your network.

Repository indexing, symbol graph construction, and the deterministic architecture diagram are all local.

### Can I use different models for different operations?

Yes. Enable advanced mode and set per-operation models:

```toml
[llm]
advanced_mode = true
summary_model = "claude-haiku-4-5-20251001"    # cheaper, used for summaries
review_model = "claude-sonnet-4-20250514"      # better, used for code reviews
```

This lets you optimize cost by routing cheap operations to cheaper models.

### How do I add a private Git repository?

In the web UI, click **Add Repository** and enter the Git URL. For private repos, you'll be prompted for a personal access token. The token is stored in the database (external mode) or in-memory (embedded mode).

You can also set a default PAT via config:

```bash
SOURCEBRIDGE_GIT_DEFAULT_PAT=ghp_your-token
```

### Can I run multiple instances of the API server?

Yes, if you:
1. Use **external** SurrealDB (shared state)
2. Use **external** Redis (shared cache)
3. Set the same JWT secret and gRPC secret on all instances

The worker should remain a single instance — it manages its own concurrency slots.

### What happens if the worker crashes mid-generation?

The job queue (in external mode) tracks job state. When the worker comes back, in-progress jobs are retried (up to 3 attempts by default). Partially generated artifacts remain in `generating` status until completed or failed.

### How do I completely reset SourceBridge?

```bash
# Docker Compose
docker compose down -v   # removes containers AND volumes
docker compose up -d     # fresh start

# Just the database
surreal export ...       # backup first!
# Then delete and recreate the SurrealDB volume
```

### Is there an API I can script against?

Yes. SourceBridge exposes a full GraphQL API at `/api/v1/graphql` and REST endpoints for auth, admin, and export. Authenticate with a JWT token from `/auth/login` or create an API token in the web UI under Settings.

### Can I disable telemetry?

Yes:

```bash
SOURCEBRIDGE_TELEMETRY=off
# or
DO_NOT_TRACK=1
```

Telemetry is anonymous and opt-out. See [TELEMETRY.md](../TELEMETRY.md) for details.
