# Getting Started with SourceBridge

SourceBridge generates field guides for codebases — cliff notes, learning paths, code tours, and architecture diagrams — so you can understand any system without starting from scratch.

**Time to first field guide: ~5 minutes.**

---

## Prerequisites

- Docker + Docker Compose v2
- (Optional) An LLM API key from Anthropic, OpenAI, Gemini, or OpenRouter — OR a local [Ollama](https://ollama.com) install. Without an LLM, the server starts but AI features are disabled.

---

## Step 1: Get the compose file

No git clone needed.

```bash
curl -O https://raw.githubusercontent.com/sourcebridge-ai/sourcebridge/main/docker-compose.hub.yml
```

## Step 2: Generate secrets

The default compose file ships with placeholder secrets. Run the init script once to replace them with strong random values:

```bash
curl -O https://raw.githubusercontent.com/sourcebridge-ai/sourcebridge/main/scripts/init-hub-secrets.sh
chmod +x init-hub-secrets.sh
./init-hub-secrets.sh
```

This creates a `.env` file (chmod 0600) with unique values for `SURREAL_PASS`, `SOURCEBRIDGE_GRPC_SECRET`, and `SOURCEBRIDGE_JWT_SECRET`. The compose file picks it up automatically.

> **`docker compose down -v` deletes the encryption key.** All API keys stored through the admin UI become unrecoverable. Back up the `sourcebridge-secrets` volume before using `-v`. See [`docs/admin/llm-config.md`](docs/admin/llm-config.md#wipe-and-re-enter).

## Step 3: Configure your LLM (optional but recommended)

**Ollama (local, free):**

```bash
# Install Ollama: https://ollama.com, then:
ollama pull qwen3:32b
# No env vars needed — Ollama is the default. Skip to Step 4.
```

**Anthropic / OpenAI / Gemini / OpenRouter:**

Add these to your `.env` file before starting the stack:

```bash
# Anthropic
SOURCEBRIDGE_LLM_PROVIDER=anthropic
SOURCEBRIDGE_LLM_API_KEY=sk-ant-...
SOURCEBRIDGE_LLM_MODEL=claude-sonnet-4-20250514

# OpenAI
SOURCEBRIDGE_LLM_PROVIDER=openai
SOURCEBRIDGE_LLM_API_KEY=sk-...
SOURCEBRIDGE_LLM_MODEL=gpt-4o

# OpenRouter
SOURCEBRIDGE_LLM_PROVIDER=openrouter
SOURCEBRIDGE_LLM_API_KEY=sk-or-...
SOURCEBRIDGE_LLM_MODEL=anthropic/claude-sonnet-4-20250514
```

Or pass them inline at start time (see Step 4 below).

> Note for from-source installs: `make dev-worker` also accepts `ANTHROPIC_API_KEY` as a bridge to `SOURCEBRIDGE_LLM_API_KEY` when the canonical var is unset.

## Step 4: Start the stack

```bash
docker compose -f docker-compose.hub.yml up -d
```

With an inline cloud key (alternative to editing `.env`):

```bash
SOURCEBRIDGE_LLM_PROVIDER=anthropic \
SOURCEBRIDGE_LLM_API_KEY=sk-ant-... \
SOURCEBRIDGE_LLM_MODEL=claude-sonnet-4-20250514 \
docker compose -f docker-compose.hub.yml up -d
```

Wait ~15 seconds for the services to become healthy:

```bash
docker compose -f docker-compose.hub.yml ps
curl http://localhost:8280/healthz
```

## Step 5: Create your admin account

Open [http://localhost:3300](http://localhost:3300). On a fresh install, the page directs you to `/login` where a first-time setup form lets you create the admin account.

> **Note:** `/setup` redirects to `/login` — direct `/setup` URLs work but the form lives at `/login`.

## Step 6: Connect your AI provider (if not done via env)

Visit [http://localhost:3300/admin/llm](http://localhost:3300/admin/llm).

If you set `SOURCEBRIDGE_LLM_API_KEY` in your `.env` before starting, the profile is already configured and an informational callout confirms it. Otherwise, add a provider and API key here.

## Step 7: Add your first repository

1. Click **Add Repository** in the web UI (or the empty-state button on the repositories page)
2. Enter a Git URL (e.g., `https://github.com/expressjs/express`) or a local path
3. For private repos, provide a personal access token

SourceBridge indexes the repo and generates field guides in the background. The admin monitor at [http://localhost:3300/admin/monitor](http://localhost:3300/admin/monitor) shows progress.

---

## What to do once it's running

See [docs/start-here.md](docs/start-here.md) for a tour of the cliff notes, code tour, and learning path views once the field guide finishes generating.

Other entry points:

- [MCP setup for AI clients](docs/user/mcp-clients.md) — Connect SourceBridge to Claude Code, Cursor, Codex, or Claude Desktop
- [VS Code extension](plugins/vscode/README.md) — Inline requirement lenses, streaming AI chat, one-keystroke field guides

---

## Other install paths

| Method | Guide |
|---|---|
| From source (developer install) | [docs/installation.md](docs/installation.md) |
| Kubernetes (kustomize) | [deploy/kubernetes/README.md](deploy/kubernetes/README.md) |
| Helm | [docs/self-hosted/helm-guide.md](docs/self-hosted/helm-guide.md) |

---

## Troubleshooting

### Blank page in Chrome when opening http://localhost:3300 (dev server only)

Fixed in v0.15.x — the CSP now includes `'unsafe-eval'` in development mode. If you're on an older version, run a production build: `npm run build && npm run start`.

### "/setup returns 404"

The setup form lives at `/login`. SourceBridge auto-redirects `/setup` → `/login` (307). If you're seeing a 404, you're on a version older than v0.15.x.

### "I set ANTHROPIC_API_KEY but the worker isn't using it"

For Docker compose: SourceBridge expects `SOURCEBRIDGE_LLM_API_KEY` + `SOURCEBRIDGE_LLM_PROVIDER` as the canonical env vars. For `make dev-worker` (from-source): both `ANTHROPIC_API_KEY` and the canonical vars are accepted (canonical wins).

### "No cliff notes are generating"

An LLM must be configured. Check [http://localhost:3300/admin/llm](http://localhost:3300/admin/llm) to confirm a profile with a provider is active.

### "The admin UI shows a profile I didn't create"

Expected — SourceBridge auto-configures an LLM profile on first boot when `SOURCEBRIDGE_LLM_API_KEY` is set. The "Active profile auto-configured at startup from environment variables" callout on `/admin/llm` confirms it.

### Something else?

See [docs/troubleshooting.md](docs/troubleshooting.md).

---

## Going to production

When you're ready to expose SourceBridge beyond localhost: [docs/going-to-production.md](docs/going-to-production.md).
