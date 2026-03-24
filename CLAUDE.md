# CLAUDE.md

This file provides guidance to Claude Code when working in the SourceBridge.ai project.

## Project Overview

**SourceBridge.ai** is an open-source codebase field guide and context layer for unfamiliar systems. It maps files, symbols, change risk, explanations, reviews, and specs-to-code links so teams can understand how a codebase actually works.

## Project Structure

```
sourcebridge/
├── internal/          # Go API server (HTTP/GraphQL)
├── workers/           # Python gRPC worker (reasoning, linking, requirements, knowledge)
├── web/               # React/Next.js web UI
├── plugins/vscode/    # VS Code extension
├── proto/             # Protobuf definitions
├── gen/               # Generated protobuf code (Go + Python)
├── cli/               # CLI commands (cobra)
├── cmd/sourcebridge/  # Main entrypoint
├── deploy/            # Docker, Helm charts
├── docs/              # Documentation
└── tests/             # Integration and smoke tests
```

## Build & Run

```bash
# Go API server
go build ./cmd/sourcebridge
./sourcebridge serve

# Web UI (Next.js)
cd web && npm install && npm run dev

# Python workers
cd workers && pip install -e . && python -m workers

# Docker Compose (all-in-one)
docker compose up
```

## Key Commands

```bash
make build        # Build all binaries
make test         # Run Go tests
make lint         # Run linters
make proto        # Regenerate protobuf (requires buf)
```

## Architecture

- **Go API** (`internal/`): HTTP + GraphQL server, SurrealDB persistence, gRPC client to workers
- **Python Workers** (`workers/`): LLM-powered reasoning, knowledge generation, requirement linking
- **Web UI** (`web/`): Next.js app with urql GraphQL client
- **VS Code Extension** (`plugins/vscode/`): CodeLens, hover, sidebar panels

## Safety Constraints

- Never commit credentials or `.env` files
- SurrealDB `ca_` table prefixes are intentional — do not rename for data compatibility
- Generated protobuf files (`gen/`) must be regenerated with `buf generate` from `proto/`, never edited by hand or sed
