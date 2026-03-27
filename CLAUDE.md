# CLAUDE.md

This file provides guidance to AI assistants working in this repository.

## Project Overview

SourceBridge is a requirement-aware code comprehension platform — a "field guide" for unfamiliar codebases.

## Architecture

- **Go API Server** (`internal/`, `cmd/`) — HTTP/GraphQL API, auth, indexing
- **Python Worker** (`workers/`) — gRPC service for AI reasoning, linking, knowledge generation
- **Web UI** (`web/`) — Next.js 15 / React 19 dashboard
- **Proto** (`proto/`) — gRPC service definitions

## Building

```bash
make build          # Build Go binary + web frontend
make build-worker   # Install Python worker deps
make test           # Run all tests
make lint           # Run all linters
make proto          # Regenerate protobuf stubs
```

## Running

```bash
# Quick start with Docker
docker compose up

# Local development
make dev            # Start API server
make dev-web        # Start web frontend (separate terminal)
cd workers && uv run python -m workers  # Start worker (separate terminal)
```

## Configuration

Configuration via `config.toml` or environment variables with `SOURCEBRIDGE_` prefix.
See `config.toml.example` for all options.

## Safety

- Never commit credentials or `.env` files
- Always specify namespace explicitly with kubectl
