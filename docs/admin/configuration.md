# Configuration

## Server Configuration

SourceBridge.ai reads configuration from environment variables and config files.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `SOURCEBRIDGE_PORT` | API server port | `8080` |
| `SOURCEBRIDGE_HOST` | API server host | `0.0.0.0` |
| `SOURCEBRIDGE_JWT_SECRET` | JWT signing secret | (required for auth) |
| `SOURCEBRIDGE_LOG_LEVEL` | Log level (debug, info, warn, error) | `info` |
| `SOURCEBRIDGE_TEST_MODE` | Enable test mode (no auth required) | `false` |
| `SOURCEBRIDGE_INDEXING_ALLOW_PRIVATE_GIT_HOSTS` | Allow git clone/pull to private IPs (SSRF risk — for self-hosted Forgejo/Gitea operators only; never enable on multi-tenant deployments) | `false` |
| `SOURCEBRIDGE_WORKER_GRPC_REFLECTION_ENABLED` | Enable gRPC reflection on the worker for grpcurl debugging. Requires `SOURCEBRIDGE_WORKER_DEBUG=true` as well. Never enable in production. | `false` |

### Config File

Default location: `~/.sourcebridge/config.yaml`

```yaml
server:
  port: 8080
  host: 0.0.0.0

auth:
  jwt_secret: your-secret-here
  token_expiry: 24h

logging:
  level: info
  format: json
```

## LLM Configuration

Code reasoning features (review, discussion, explanation) require an LLM provider.

### Supported LLM Providers

Configure via `SOURCEBRIDGE_WORKER_LLM_PROVIDER` or `[worker] llm_provider` in
`config.toml`. The worker validates the value at startup and refuses to start
with an unknown provider, printing an actionable error that names the supported
set.

| Provider | Config value |
|----------|-------------|
| Anthropic | `anthropic` |
| OpenAI | `openai` |
| Ollama (local) | `ollama` |
| vLLM | `vllm` |
| llama.cpp | `llama-cpp` |
| SGLang | `sglang` |
| Google Gemini | `gemini` |
| OpenRouter | `openrouter` |
| LM Studio | `lmstudio` |

### Supported Embedding Providers

Embeddings are configured independently from the LLM provider. Use
`SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER` or `[worker.embedding] provider` in
`config.toml`. As with LLM providers, an unknown value is rejected at startup.

| Provider | Config value | Notes |
|----------|-------------|-------|
| Ollama | `ollama` | Default. Requires a model like `nomic-embed-text`. |
| OpenAI | `openai` | OpenAI hosted embeddings (`text-embedding-3-*`). |
| OpenAI-compatible | `openai-compatible` | Any self-hosted endpoint with the OpenAI embeddings API shape. |

**Note:** `anthropic` is not a valid embedding provider — Anthropic does not
offer an embeddings API. Setting it produces an error at startup that names
this explicitly.

Embedding env vars use the `SOURCEBRIDGE_WORKER_EMBEDDING_` prefix:

```bash
SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=ollama
SOURCEBRIDGE_WORKER_EMBEDDING_MODEL=nomic-embed-text
SOURCEBRIDGE_WORKER_EMBEDDING_BASE_URL=http://localhost:11434
SOURCEBRIDGE_WORKER_EMBEDDING_DIMENSION=768
```

See `config.toml.example` for the full `[worker.embedding]` section with
comments.

### Test Mode

When `SOURCEBRIDGE_TEST_MODE=true`, the system uses a `FakeLLMProvider` that returns deterministic responses. This is used for testing and CI.

## Docker Compose Configuration

The `docker-compose.yml` file configures all services:

```yaml
services:
  api:
    image: ghcr.io/sourcebridge/sourcebridge:latest
    ports:
      - "8080:8080"
    environment:
      - SOURCEBRIDGE_JWT_SECRET=change-me
      - SOURCEBRIDGE_LOG_LEVEL=info

  web:
    image: ghcr.io/sourcebridge/sourcebridge-web:latest
    ports:
      - "3300:3000"
    environment:
      - NEXT_PUBLIC_API_URL=http://api:8080

  worker:
    image: ghcr.io/sourcebridge/sourcebridge-worker:latest
    environment:
      - OPENAI_API_KEY=${OPENAI_API_KEY}
```

## VS Code Extension Configuration

In VS Code settings:

| Setting | Description | Default |
|---------|-------------|---------|
| `sourcebridge.apiUrl` | API server URL | `http://localhost:8080` |
| `sourcebridge.token` | JWT token for auth | (empty) |
