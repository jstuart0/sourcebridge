<p align="center">
  <img src="web/public/logo.png" alt="SourceBridge.ai" width="128">
</p>

<h1 align="center">SourceBridge.ai</h1>

<p align="center"><strong>The field guide your codebase should have had.</strong></p>

[![CI](https://github.com/sourcebridge-ai/sourcebridge/actions/workflows/ci.yml/badge.svg)](https://github.com/sourcebridge-ai/sourcebridge/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev/)
[![Release](https://img.shields.io/github/v/release/sourcebridge-ai/sourcebridge)](https://github.com/sourcebridge-ai/sourcebridge/releases)
[![Docker Pulls](https://img.shields.io/docker/pulls/sourcebridge/sourcebridge-api)](https://hub.docker.com/u/sourcebridge)

## What is SourceBridge?

SourceBridge is a requirement-aware code comprehension platform. Point it at any codebase and it generates field guides -- cliff notes, learning paths, code tours, architecture diagrams, and workflow stories -- so your team can understand how a system actually works. It also traces requirements to code, runs AI-powered reviews, and serves as an MCP server for AI agent integration.

Most tools help you search code. **SourceBridge helps you understand systems.**

<p align="center">
  <img src="docs/screenshots/SourceBridge-Overview.png" alt="Repository overview showing cliff notes, code tours, and learning paths" width="800">
</p>

<details>
<summary><strong>More screenshots</strong></summary>

### Cliff Notes
<p align="center">
  <img src="docs/screenshots/SourceBridge-CliffNotes.png" alt="AI-generated cliff notes with hierarchical code summaries" width="800">
</p>

### Admin Monitor
<p align="center">
  <img src="docs/screenshots/SourceBridge-Generation.png" alt="LLM job queue with real-time generation progress" width="800">
</p>

### Semantic Search
<p align="center">
  <img src="docs/screenshots/SourceBridge-Search.png" alt="Natural language search against the repository graph" width="800">
</p>

</details>

## Key Features

- **Code Indexing** -- Tree-sitter based parsing for Go, Python, TypeScript, JavaScript, Java, Rust, and C++
- **Field Guides** -- Cliff notes, learning paths, code tours, workflow stories, and system explanations at repository, file, and symbol levels
- **Requirement Tracing** -- Import requirements from Markdown or CSV, auto-link to code, generate traceability matrices
- **Code Review** -- AI-powered structured reviews (security, SOLID, performance, reliability, maintainability)
- **Code Discussion** -- Conversational exploration with full codebase context
- **Architecture Diagrams** -- Auto-generated Mermaid diagrams from code structure
- **Impact Analysis** -- Simulate changes and see affected requirements and code paths
- **MCP Server** -- Model Context Protocol support for AI agent integration ([setup guide](docs/user/mcp-clients.md))
- **VS Code Extension** -- First-class editor integration: inline requirement lenses, streaming AI chat (`Cmd+I`), create/edit requirements from the sidebar, one-keystroke field guides ([install guide](plugins/vscode/README.md))
- **Multi-Provider LLM** -- Works with cloud APIs (Anthropic, OpenAI, Gemini, OpenRouter) or fully local inference (Ollama, vLLM, llama.cpp, SGLang, LM Studio)
- **GraphQL API** -- Full programmatic access to all platform capabilities
- **CLI** -- Complete command-line interface for scripting and automation

## Quick Start

### Docker Hub (fastest)

No git clone needed. Just pull and run:

```bash
# Download the compose file
curl -O https://raw.githubusercontent.com/sourcebridge-ai/sourcebridge/main/docker-compose.hub.yml

# Start SourceBridge (uses Ollama by default — no API key required)
docker compose -f docker-compose.hub.yml up -d
```

Open [http://localhost:3000](http://localhost:3000) and create your admin account.

> **Using a cloud LLM?** Pass your provider config inline:
>
> ```bash
> SOURCEBRIDGE_LLM_PROVIDER=anthropic \
> SOURCEBRIDGE_LLM_API_KEY=sk-ant-... \
> SOURCEBRIDGE_LLM_MODEL=claude-sonnet-4-20250514 \
> docker compose -f docker-compose.hub.yml up -d
> ```

<details>
<summary><strong>Full configuration examples</strong></summary>

#### Anthropic (recommended for quality)

```bash
SOURCEBRIDGE_LLM_PROVIDER=anthropic \
SOURCEBRIDGE_LLM_API_KEY=sk-ant-api03-xxxxx \
SOURCEBRIDGE_LLM_MODEL=claude-sonnet-4-20250514 \
docker compose -f docker-compose.hub.yml up -d
```

#### OpenAI

```bash
SOURCEBRIDGE_LLM_PROVIDER=openai \
SOURCEBRIDGE_LLM_API_KEY=sk-xxxxx \
SOURCEBRIDGE_LLM_MODEL=gpt-4o \
docker compose -f docker-compose.hub.yml up -d
```

#### Ollama (local, free)

```bash
# Install Ollama first: https://ollama.com
ollama pull qwen3:32b

# No API key needed — Ollama is the default
docker compose -f docker-compose.hub.yml up -d
```

#### OpenRouter (access to 100+ models)

```bash
SOURCEBRIDGE_LLM_PROVIDER=openrouter \
SOURCEBRIDGE_LLM_API_KEY=sk-or-xxxxx \
SOURCEBRIDGE_LLM_MODEL=anthropic/claude-sonnet-4-20250514 \
docker compose -f docker-compose.hub.yml up -d
```

#### Production secrets

For production, pin your JWT and gRPC secrets instead of using defaults:

```bash
SOURCEBRIDGE_JWT_SECRET=$(openssl rand -hex 32) \
SOURCEBRIDGE_GRPC_SECRET=$(openssl rand -hex 32) \
SOURCEBRIDGE_LLM_PROVIDER=anthropic \
SOURCEBRIDGE_LLM_API_KEY=sk-ant-api03-xxxxx \
SOURCEBRIDGE_LLM_MODEL=claude-sonnet-4-20250514 \
docker compose -f docker-compose.hub.yml up -d
```

</details>

### Try the demo

SourceBridge includes a sample TypeScript project you can index immediately:

```bash
git clone https://github.com/sourcebridge-ai/sourcebridge.git
cd sourcebridge
./demo.sh
```

The demo starts SourceBridge, indexes a 44-file sample API, and generates cliff notes, code tours, and architecture diagrams. Open [http://localhost:3000](http://localhost:3000) to explore.

### CLI — one line, any platform

```bash
curl -fsSL https://<your-sourcebridge-server>/install.sh | sh -s -- --server https://<your-sourcebridge-server>
```

Installs to `~/.local/bin/sourcebridge` (no `sudo`), authenticates against
your server, and you're ready to run `sourcebridge setup claude` in any
indexed repository. See [Installation](docs/user/installation.md) for the
trust model, alternate paths (`brew install sourcebridge-ai/tap/sourcebridge`,
manual download, build from source), and upgrade/uninstall instructions.

### Server — Docker compose

```bash
git clone https://github.com/sourcebridge-ai/sourcebridge.git
cd sourcebridge
cp .env.example .env   # configure your LLM provider
docker compose up -d
```

### Helm / Kubernetes

For production deployments:

```bash
helm install sourcebridge deploy/helm/sourcebridge/ \
  --set llm.provider=anthropic \
  --set llm.apiKey=$ANTHROPIC_API_KEY
```

See [Helm Guide](docs/self-hosted/helm-guide.md) for full configuration options, including air-gapped and local inference setups.

## VS Code Extension

Use SourceBridge without leaving your editor. The extension is in [`plugins/vscode/`](plugins/vscode/) and talks to any SourceBridge server — local, Docker, Helm, or a shared team deployment.

<p align="center">
  <em>Inline requirement lenses · streaming AI chat · sidebar CRUD · one-keystroke field guides</em>
</p>

### Install

From a pre-built VSIX:

```bash
# Build the VSIX from source
make package-vscode

# Install it into your local VS Code
make install-vscode
```

Or from inside VS Code: `Cmd+Shift+P` → **Extensions: Install from VSIX…** → pick `plugins/vscode/sourcebridge-*.vsix`.

### Configure

1. `Cmd+Shift+P` → **SourceBridge: Sign In**
2. Enter your server URL (e.g. `http://localhost:8080` for local dev, or your team's deployed URL)
3. Pick sign-in method (browser OIDC or local password)

The status bar (bottom-left) reflects connection state: `connected · <repo>`, `offline · retry in Ns`, `sign in required`, etc. Click it for quick actions.

### Highlights

| Flow | How | What happens |
|---|---|---|
| **Ask streaming** | `Cmd+I` on any selection | Chat panel opens; tokens stream in live via MCP (fallback: non-streaming GraphQL if server doesn't mount MCP) |
| **Show linked requirements** | `Cmd+.` on a function | Lightbulb menu lists linked requirements; click to open detail panel |
| **Create requirement** | `Cmd+.` on an unlinked symbol → *Create requirement from this symbol…* | Inline flow pre-fills title from the symbol name; the new requirement is linked automatically |
| **Edit / delete from sidebar** | Hover a requirement row in the activity bar | Pencil + trash icons; delete soft-deletes (30-day recycle bin) |
| **Field guide for file** | `Cmd+K N` | Generates cliff notes for the active file and opens the panel |
| **Change Risk tree** | Activity bar → Change Risk | Shows changed files / affected requirements / stale field guides from the latest impact report |
| **Scoped palette** | `Cmd+Shift+;` | Context-filtered picker — only shows actions valid for your current focus |

Full feature list + troubleshooting in [`plugins/vscode/README.md`](plugins/vscode/README.md).

### Develop / contribute

```bash
cd plugins/vscode
npm install
npm run watch      # rebuilds on save
# Open the folder in VS Code, press F5 → "Run Extension" for a dev host
```

Tests: `make test-vscode` (or `npm test` from inside `plugins/vscode/`).

## Architecture

```
                    ┌──────────────────────────────────┐
                    │           Clients                │
                    │   Web UI / CLI / MCP / GraphQL   │
                    └──────────────┬───────────────────┘
                                   │
                    ┌──────────────▼───────────────────┐
                    │        Go API Server             │
                    │   chi router + gqlgen GraphQL    │
                    │   JWT auth, OIDC SSO, REST       │
                    │   tree-sitter code indexer        │
                    └───────┬──────────────┬───────────┘
                            │              │
               ┌────────────▼──┐    ┌──────▼──────────┐
               │   SurrealDB   │    │  Python Worker   │
               │   (embedded   │    │  gRPC service    │
               │   or external)│    │  AI reasoning,   │
               └───────────────┘    │  linking,        │
                                    │  requirements,   │
               ┌───────────────┐    │  knowledge,      │
               │  Redis Cache  │    │  contracts       │
               │  (optional,   │    └──────┬───────────┘
               │  defaults to  │           │
               │  in-memory)   │    ┌──────▼───────────┐
               └───────────────┘    │   LLM Provider   │
                                    │  Cloud or Local   │
                                    └──────────────────┘
```

**Go API Server** (`internal/`, `cmd/`) -- HTTP and GraphQL API, authentication, code indexing, and request routing. Handles tree-sitter parsing for 7 languages.

**Python gRPC Worker** (`workers/`) -- AI reasoning engine that communicates with LLM providers. Services include reasoning, linking, requirements analysis, knowledge extraction, and contract generation.

**Next.js Web UI** (`web/`) -- React 19, Tailwind CSS, CodeMirror 6 for code display, @xyflow/react for dependency graphs, recharts for metrics, Mermaid for architecture diagrams.

**SurrealDB** -- Primary data store. Runs embedded for single-node setups or connects to an external instance for production.

**Redis** -- Optional caching layer. Defaults to an in-memory cache when Redis is not configured.

## Configuration

SourceBridge reads configuration from a TOML config file and environment variables. Environment variables use the `SOURCEBRIDGE_` prefix and override file values.

See [`config.toml.example`](config.toml.example) for a complete annotated example.

### Key Environment Variables

| Variable | Description | Default |
|---|---|---|
| `SOURCEBRIDGE_LLM_PROVIDER` | LLM provider name | `ollama` |
| `SOURCEBRIDGE_LLM_BASE_URL` | LLM API endpoint | (provider default) |
| `SOURCEBRIDGE_LLM_MODEL` | Model name | (provider default) |
| `SOURCEBRIDGE_LLM_API_KEY` | API key for cloud providers | -- |
| `SOURCEBRIDGE_SERVER_HTTP_PORT` | API server port | `8080` |
| `SOURCEBRIDGE_SERVER_GRPC_PORT` | gRPC port for worker communication | `50051` |
| `SOURCEBRIDGE_STORAGE_SURREAL_MODE` | `embedded` or `external` | `embedded` |
| `SOURCEBRIDGE_STORAGE_SURREAL_URL` | SurrealDB connection URL | -- |
| `SOURCEBRIDGE_STORAGE_REDIS_MODE` | `redis` or `memory` | `memory` |
| `SOURCEBRIDGE_SECURITY_JWT_SECRET` | JWT signing secret | (required for auth) |
| `SOURCEBRIDGE_SECURITY_MODE` | Security mode (`oss` or `enterprise`) | `oss` |

## LLM Providers

SourceBridge supports both cloud-hosted and local inference providers. Configure per-operation models for cost optimization (e.g., a smaller model for summaries, a larger one for reviews).

### Cloud Providers

| Provider | Config Value | API Key Variable | Models |
|---|---|---|---|
| Anthropic | `anthropic` | `ANTHROPIC_API_KEY` | Claude Sonnet 4, Claude Haiku, etc. |
| OpenAI | `openai` | `OPENAI_API_KEY` | GPT-4o, GPT-4o-mini, etc. |
| Google Gemini | `gemini` | `GOOGLE_API_KEY` | Gemini 2.5 Pro, Flash, etc. |
| OpenRouter | `openrouter` | `OPENROUTER_API_KEY` | Any model on OpenRouter |

### Local Inference

| Provider | Config Value | Notes |
|---|---|---|
| Ollama | `ollama` | Easiest local setup. Pull a model and go. |
| vLLM | `vllm` | High-throughput serving with PagedAttention |
| llama.cpp | `llamacpp` | CPU/GPU inference, GGUF models |
| SGLang | `sglang` | Optimized serving with RadixAttention |
| LM Studio | `lmstudio` | Desktop app with OpenAI-compatible API |

All local providers expose an OpenAI-compatible API. Set `base_url` to the local endpoint.

## Using with Claude Code

After indexing a repository, generate a `.claude/CLAUDE.md` skill card that gives Claude Code a structured map of the codebase — per-subsystem sections, call-graph-derived warnings, and representative symbols — so the agent understands the architecture before refactoring:

```bash
sourcebridge setup claude --repo-id <id>
```

This writes three files into the repository:

- `.claude/CLAUDE.md` — the skill card with `## Subsystem:` sections derived from clustering data
- `.claude/sourcebridge.json` — metadata for future refreshes (gitignored by default)
- `.mcp.json` — Claude Code MCP server configuration so the agent can call SourceBridge tools directly

Re-run the command after re-indexing to refresh the skill card.

See [Claude Code memory documentation](https://docs.claude.com/en/docs/claude-code/memory) for how Claude Code reads `.claude/CLAUDE.md`.

## CLI Reference

| Command | Description |
|---|---|
| `sourcebridge serve` | Start the API server |
| `sourcebridge index <path>` | Index a repository with tree-sitter |
| `sourcebridge setup claude` | Generate a `.claude/CLAUDE.md` skill card for Claude Code |
| `sourcebridge import <file>` | Import requirements from Markdown or CSV |
| `sourcebridge trace <req-id>` | Trace a requirement to linked code |
| `sourcebridge review <path>` | Run an AI-powered code review |
| `sourcebridge ask <question>` | Ask a question about the codebase |

See [CLI Reference](docs/user/cli-reference.md) for full flag documentation.

## Development

### Prerequisites

- Go 1.25+
- Python 3.12+ with [uv](https://docs.astral.sh/uv/)
- Node.js 22+
- Git

### Building from Source

```bash
# Clone the repository
git clone https://github.com/sourcebridge-ai/sourcebridge.git
cd sourcebridge

# Build the Go API server
make build-go

# Install Python worker dependencies
make build-worker

# Build the web UI
make build-web

# Or build everything at once
make build
```

### Running Locally

```bash
# Start the API server (builds first)
make dev

# In a separate terminal, start the web UI in dev mode
make dev-web
```

### Testing

```bash
# Run all tests (Go + web + worker)
make test

# Run linting (Go + web + worker)
make lint

# Run CI checks locally (lint + test)
make ci
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full development workflow.

## Deployment

### Docker Compose

Best for evaluation and small teams. See [Docker Compose quick start](#docker-compose-recommended) above.

### Kubernetes with Helm

For production and multi-team deployments:

- [Helm Guide](docs/self-hosted/helm-guide.md) -- Installation, values reference, and examples
- [Deployment Guide](docs/admin/deployment.md) -- Architecture considerations and scaling
- [Air-Gapped Installations](docs/self-hosted/air-gapped.md) -- Deploying without internet access
- [Upgrade Guide](docs/self-hosted/upgrade.md) -- Version upgrades and migrations
- [Backup and Restore](docs/admin/backup-restore.md) -- Data protection procedures

## Documentation

- [Getting Started](docs/user/getting-started.md)
- [CLI Reference](docs/user/cli-reference.md)
- [Web UI Guide](docs/user/web-ui-guide.md)
- [Configuration](docs/admin/configuration.md)
- [Troubleshooting](docs/admin/troubleshooting.md)

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, coding standards, and the pull request process.

First-time contributors must agree to the [Contributor License Agreement](CLA.md) before their PR can be merged.

## License

SourceBridge is licensed under the [GNU Affero General Public License v3.0](LICENSE).

---

## Did it work?

If SourceBridge helped you understand a codebase, let us know:

- **It worked?** Give the repo a star — it helps others find the project
- **Something broke?** [Open an issue](https://github.com/sourcebridge-ai/sourcebridge/issues) — we want to fix it
- **Have ideas?** [Start a discussion](https://github.com/sourcebridge-ai/sourcebridge/discussions)
