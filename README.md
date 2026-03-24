# SourceBridge.ai

**Understand any codebase. Fast.**

SourceBridge.ai is a codebase field guide and context layer for unfamiliar systems. It maps files, symbols, change risk, explanations, reviews, and specs-to-code links so teams can understand how a codebase actually works.

[![CI](https://github.com/sourcebridge/sourcebridge/actions/workflows/ci.yml/badge.svg)](https://github.com/sourcebridge/sourcebridge/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)

## Features

- **Code Indexing** — Parse and index codebases across 6 languages (Go, Python, TypeScript, Java, Rust, C++) using tree-sitter
- **Field Guide** — Repository, file, and symbol-level guided notes with evidence-backed context
- **Requirement Tracing** — Bidirectional linking between specs/requirements and code with confidence scoring
- **Code Review** — Structured review templates (security, SOLID, performance, reliability, maintainability)
- **Code Discussion** — Ask questions about code with context-aware AI responses
- **Traceability Matrix** — Visual matrix of requirement-to-code coverage
- **VS Code Extension** — CodeLens, hover cards, gutter decorations, and sidebar panels
- **Web Dashboard** — Repository workspaces, understanding signals, change risk, and guided exploration

## Quick Start

### Using Docker Compose

```bash
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge
docker compose up -d
```

Open http://localhost:3000 for the web UI.

### Using Homebrew

```bash
brew install sourcebridge/tap/sourcebridge
sourcebridge serve
```

### From Source

```bash
# Build the Go binary
go build -o bin/sourcebridge ./cmd/sourcebridge

# Start the server
bin/sourcebridge serve

# Index a repository
bin/sourcebridge index /path/to/repo

# Import requirements
bin/sourcebridge import /path/to/requirements.md

# Trace a requirement to code
bin/sourcebridge trace REQ-001

# Run a security review
bin/sourcebridge review /path/to/repo --template security

# Ask about code
bin/sourcebridge ask "What does processPayment do?"
```

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   CLI / Web UI  │───▶│  Go API Gateway  │───▶│  Python Workers │
│                 │    │  (GraphQL+REST)  │    │  (AI Reasoning) │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                              │
                       ┌──────┴──────┐
                       │  Graph Store │
                       │  (In-Memory) │
                       └─────────────┘
```

- **Go API Gateway** — chi router, gqlgen GraphQL, JWT auth
- **Python Workers** — Requirement parsing, linking engine, LLM-powered reasoning
- **Graph Store** — Thread-safe in-memory store for repositories, files, symbols, requirements, and links
- **Web App** — Next.js 15, React 19, CodeMirror 6, @xyflow/react, recharts
- **VS Code Extension** — CodeLens, hover, gutter decorations, sidebar views

## Documentation

- [Getting Started](docs/user/getting-started.md)
- [CLI Reference](docs/user/cli-reference.md)
- [Web UI Guide](docs/user/web-ui-guide.md)
- [Configuration](docs/admin/configuration.md)
- [Troubleshooting](docs/admin/troubleshooting.md)
- [Contributing](CONTRIBUTING.md)

## Development

```bash
# Run all tests
make test

# Run linting
make lint

# Build all binaries
make build
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup details.

## License

SourceBridge.ai Community is licensed under the [GNU Affero General Public License v3.0](LICENSE).
