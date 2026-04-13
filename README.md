<p align="center">
  <img src="web/public/logo.png" alt="SourceBridge.ai" width="128">
</p>

<h1 align="center">SourceBridge.ai</h1>

<p align="center"><strong>Most tools help you search code. SourceBridge helps you understand systems.</strong></p>

<p align="center">
  <em>Point it at any codebase. Get cliff notes, code tours, learning paths, architecture diagrams, and requirement traceability — so your team actually understands how the system works.</em>
</p>

<p align="center">

[![CI](https://github.com/sourcebridge/sourcebridge/actions/workflows/ci.yml/badge.svg)](https://github.com/sourcebridge/sourcebridge/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev/)
[![Release](https://img.shields.io/github/v/release/sourcebridge/sourcebridge)](https://github.com/sourcebridge/sourcebridge/releases)

</p>

---

## Quick Start

The fastest way to try SourceBridge. Requires [Docker](https://docker.com).

```bash
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge
./demo.sh
```

That's it. The demo script starts all services, imports a sample codebase, and opens the dashboard at **http://localhost:3000**.

**Have an LLM?** The demo auto-detects [Ollama](https://ollama.com) on your machine — or pass a cloud API key for the best results:

```bash
SOURCEBRIDGE_LLM_API_KEY=sk-ant-... ./demo.sh
```

---

## What You'll See

### Dashboard Overview

Your workspace at a glance — repositories indexed, symbols discovered, understanding scores, and field guide activity.

<p align="center">
  <img src="docs/screenshots/SourceBridge-Overview.png" alt="SourceBridge Dashboard" width="800">
</p>

### Cliff Notes

A structured summary of the entire system: what it does, how it's built, its dependencies, domain model, core flows, and risk areas. Each insight links to the specific files and lines that support it.

<p align="center">
  <img src="docs/screenshots/SourceBridge-CliffNotes.png" alt="Cliff Notes" width="800">
</p>

### Search Across Code and Requirements

Find symbols, files, and requirements from a single indexed view of the system.

<p align="center">
  <img src="docs/screenshots/SourceBridge-Search.png" alt="Search" width="800">
</p>

### Generation Monitor

Live view of every AI job SourceBridge is running — what's working, what's queued, what failed.

<p align="center">
  <img src="docs/screenshots/SourceBridge-Generation.png" alt="Generation Monitor" width="800">
</p>

> **What's different?** SourceBridge doesn't just search your code — it builds a graph-backed understanding of your system: who calls what, what depends on what, and how requirements connect to implementation. Every insight is evidence-grounded, linking back to the actual code that supports it.

---

## Did it work?

- **It worked?** [Star the repo](https://github.com/sourcebridge/sourcebridge) — it helps others find the project
- **Something broke?** [Open an issue](https://github.com/sourcebridge/sourcebridge/issues) — we want to fix it
- **Have feedback?** [Start a discussion](https://github.com/sourcebridge/sourcebridge/discussions) — tell us about your first-run experience

---

## Key Features

- **Field Guides** — Cliff notes, learning paths, code tours, workflow stories, and system explanations at repository, file, and symbol levels
- **Code Indexing** — Tree-sitter based parsing for Go, Python, TypeScript, JavaScript, Java, Rust, and C++
- **Requirement Tracing** — Import requirements from Markdown or CSV, auto-link to code, generate traceability matrices
- **AI Code Review** — Structured reviews for security, SOLID, performance, reliability, and maintainability
- **Architecture Diagrams** — Auto-generated Mermaid diagrams from code structure
- **Impact Analysis** — Simulate changes and see affected requirements and code paths
- **Code Discussion** — Conversational exploration with full codebase context
- **MCP Server** — Model Context Protocol support for AI agent integration
- **Multi-Provider LLM** — Cloud (Anthropic, OpenAI, Gemini, OpenRouter) or fully local (Ollama, vLLM, llama.cpp, SGLang, LM Studio)
- **Self-Hostable** — Your code never leaves your infrastructure. Run air-gapped with local models.

---

## Run on Your Own Repo

After the demo, try SourceBridge on your own code:

1. Open http://localhost:3000
2. Click **Add Repository**
3. Enter a local path or paste a Git URL
4. SourceBridge indexes the repo and starts generating field guides

For private Git repos, provide a personal access token when adding the repository.

> **New here?** Read [Start Here](docs/start-here.md) for a walkthrough of what happened and what to do next.

---

## LLM Providers

SourceBridge works with cloud-hosted or local inference providers.

### Cloud Providers

| Provider | Config Value | Models |
|---|---|---|
| Anthropic | `anthropic` | Claude Sonnet 4, Claude Haiku (recommended) |
| OpenAI | `openai` | GPT-4o, GPT-4o-mini |
| Google Gemini | `gemini` | Gemini 2.5 Pro, Flash |
| OpenRouter | `openrouter` | Any model on OpenRouter |

### Local Inference

| Provider | Config Value | Notes |
|---|---|---|
| Ollama | `ollama` | Easiest local setup — pull a model and go |
| vLLM | `vllm` | High-throughput serving with PagedAttention |
| llama.cpp | `llamacpp` | CPU/GPU inference, GGUF models |
| SGLang | `sglang` | Optimized serving with RadixAttention |
| LM Studio | `lmstudio` | Desktop app with OpenAI-compatible API |

---

## Other Install Options

### Docker Compose (manual)

```bash
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge
cp .env.example .env   # configure your LLM provider
docker compose up -d
```

### Homebrew

```bash
brew install sourcebridge/tap/sourcebridge
sourcebridge serve
```

### One-Command Setup (from source)

```bash
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge
./setup.sh
```

### Helm / Kubernetes

For production deployments:

```bash
helm install sourcebridge deploy/helm/sourcebridge/ \
  --set llm.provider=anthropic \
  --set llm.apiKey=$ANTHROPIC_API_KEY
```

See [Helm Guide](docs/self-hosted/helm-guide.md) for configuration options including air-gapped setups.

---

## Configuration

SourceBridge reads configuration from a TOML config file and environment variables. Environment variables use the `SOURCEBRIDGE_` prefix and override file values.

See [`config.toml.example`](config.toml.example) for the complete annotated example.

### Key Environment Variables

| Variable | Description | Default |
|---|---|---|
| `SOURCEBRIDGE_LLM_PROVIDER` | LLM provider name | `ollama` |
| `SOURCEBRIDGE_LLM_BASE_URL` | LLM API endpoint | (provider default) |
| `SOURCEBRIDGE_LLM_MODEL` | Model name | (provider default) |
| `SOURCEBRIDGE_LLM_API_KEY` | API key for cloud providers | -- |
| `SOURCEBRIDGE_SERVER_HTTP_PORT` | API server port | `8080` |
| `SOURCEBRIDGE_STORAGE_SURREAL_MODE` | `embedded` or `external` | `embedded` |
| `SOURCEBRIDGE_SECURITY_JWT_SECRET` | JWT signing secret | (required) |

---

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
               │  (optional)   │    └──────┬───────────┘
               └───────────────┘           │
                                    ┌──────▼───────────┐
                                    │   LLM Provider   │
                                    │  Cloud or Local   │
                                    └──────────────────┘
```

**Go API Server** (`internal/`, `cmd/`) — HTTP and GraphQL API, authentication, code indexing.

**Python gRPC Worker** (`workers/`) — AI reasoning, knowledge generation, requirements linking.

**Next.js Web UI** (`web/`) — React 19, Tailwind CSS, CodeMirror, dependency graphs, Mermaid diagrams.

**SurrealDB** — Document and graph database. Embedded for single-node or external for production.

---

## CLI Reference

| Command | Description |
|---|---|
| `sourcebridge serve` | Start the API server |
| `sourcebridge index <path>` | Index a repository |
| `sourcebridge import <file>` | Import requirements from Markdown or CSV |
| `sourcebridge trace <req-id>` | Trace a requirement to linked code |
| `sourcebridge review <path>` | Run an AI-powered code review |
| `sourcebridge ask <question>` | Ask a question about the codebase |

See [CLI Reference](docs/user/cli-reference.md) for full documentation.

---

## Deployment

- [Docker Compose](docs/admin/deployment.md) — Best for evaluation and small teams
- [Helm Guide](docs/self-hosted/helm-guide.md) — Production Kubernetes deployments
- [Air-Gapped Installations](docs/self-hosted/air-gapped.md) — Deploy without internet access
- [Upgrade Guide](docs/self-hosted/upgrade.md) — Version upgrades and migrations
- [Backup and Restore](docs/admin/backup-restore.md) — Data protection procedures

---

## Documentation

- [Start Here](docs/start-here.md) — What just happened and what to do next
- [Getting Started](docs/user/getting-started.md) — Full setup walkthrough
- [CLI Reference](docs/user/cli-reference.md) — Command-line interface
- [Web UI Guide](docs/user/web-ui-guide.md) — Dashboard features
- [Configuration](docs/admin/configuration.md) — All settings and options
- [Troubleshooting](docs/troubleshooting.md) — Common issues and fixes

---

## Development

### Prerequisites

- Go 1.25+
- Python 3.12+ with [uv](https://docs.astral.sh/uv/)
- Node.js 22+
- Git

### Building from Source

```bash
make build          # Build everything (Go + web)
make build-worker   # Install Python worker deps
make test           # Run all tests
make lint           # Run all linters
```

### Running Locally

```bash
make dev            # Start API server
make dev-web        # Start web UI (separate terminal)
```

---

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, coding standards, and the pull request process.

First-time contributors must agree to the [Contributor License Agreement](CLA.md) before their PR can be merged.

---

## License

SourceBridge is licensed under the [GNU Affero General Public License v3.0](LICENSE).
