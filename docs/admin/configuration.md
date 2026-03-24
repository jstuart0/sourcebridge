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

### Supported Providers

| Provider | Environment Variable |
|----------|---------------------|
| OpenAI | `OPENAI_API_KEY` |
| Anthropic | `ANTHROPIC_API_KEY` |
| Local (Ollama) | `OLLAMA_URL` |

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
      - "3000:3000"
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
