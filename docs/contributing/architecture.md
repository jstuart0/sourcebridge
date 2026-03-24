# Architecture Overview

## System Design

SourceBridge.ai is a polyglot monorepo with three primary runtime components:

```
┌─────────────┐     ┌──────────────────┐     ┌─────────────────┐
│  Clients    │     │  Go API Gateway  │     │ Python Workers  │
│ - CLI       │────▶│  - GraphQL API   │────▶│ - Linking       │
│ - Web UI    │     │  - REST Auth     │     │ - Reasoning     │
│ - VS Code   │     │  - JWT Auth      │     │ - Parsing       │
└─────────────┘     └──────────────────┘     └─────────────────┘
                           │
                    ┌──────┴──────┐
                    │ Graph Store │
                    │ (In-Memory) │
                    └─────────────┘
```

## Go API Gateway

The Go binary (`cmd/sourcebridge`) serves as the API gateway and CLI entry point.

**Key packages:**
- `internal/api/graphql/` — gqlgen-based GraphQL API
- `internal/api/rest/` — chi-based REST endpoints for auth
- `internal/auth/` — JWT authentication and middleware
- `internal/graph/` — Thread-safe in-memory graph store
- `internal/indexer/` — Tree-sitter code indexer
- `internal/requirements/` — Requirement file parsing
- `cli/` — CLI commands (index, import, trace, review, ask)

## Python Workers

Python handles AI-powered features via subprocess invocation.

**Key modules:**
- `workers/linking/` — Multi-strategy requirement linking engine
- `workers/reasoning/` — LLM-powered summarizer, reviewer, discussion, explainer
- `workers/reasoning/cache.py` — Summary caching with LRU eviction and circuit breaker

## Graph Store

The in-memory graph store (`internal/graph/store.go`) manages all entity relationships:

- **Repositories** — Top-level container for indexed codebases
- **Files** — Source files within repositories
- **Symbols** — Functions, methods, classes, interfaces extracted by tree-sitter
- **Modules** — Logical groupings of files
- **Requirements** — Imported requirement specifications
- **Links** — Bidirectional requirement-to-symbol connections with confidence scores

Thread safety is ensured via `sync.RWMutex`.

## Web Application

Next.js 15 with React 19, communicating with the Go API via GraphQL (urql client).

**Key components:**
- Code viewer with CodeMirror 6 and requirement overlays
- Traceability matrix visualization
- Dependency graph using @xyflow/react
- Coverage charts using recharts
- Command palette using cmdk

## VS Code Extension

Thin client connecting to the local API server via GraphQL.

**Providers:**
- CodeLens — Requirement IDs above functions
- Hover — Symbol summaries and linked requirements
- Gutter Decorations — Confidence-colored line highlights
