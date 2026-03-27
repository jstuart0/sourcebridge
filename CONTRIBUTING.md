# Contributing to SourceBridge

Thank you for your interest in contributing to SourceBridge. Whether you are fixing a bug, adding a feature, improving documentation, or reporting an issue -- all contributions are valued.

## Getting Started

### Prerequisites

| Tool | Version | Purpose |
|---|---|---|
| [Go](https://go.dev/dl/) | 1.25+ | API server |
| [Python](https://www.python.org/) + [uv](https://docs.astral.sh/uv/) | 3.12+ | gRPC worker |
| [Node.js](https://nodejs.org/) | 22+ | Web UI |
| [Git](https://git-scm.com/) | 2.x+ | Version control |
| [Docker](https://docs.docker.com/get-docker/) | (optional) | Container builds |

### Fork and Clone

```bash
# Fork the repository on GitHub, then:
git clone https://github.com/<your-username>/sourcebridge.git
cd sourcebridge
git remote add upstream https://github.com/sourcebridge/sourcebridge.git
```

### Initial Setup

```bash
# Build the Go API server
make build-go

# Install Python worker dependencies
cd workers && uv sync --dev && cd ..

# Install web UI dependencies
cd web && npm ci && cd ..

# Verify everything works
make ci
```

## Development Workflow

### Building

```bash
make build           # Build Go binary + web app
make build-go        # Build Go binary only
make build-web       # Build web UI only
make build-worker    # Install Python worker dependencies
```

### Running Locally

```bash
# Start the API server (builds first if needed)
make dev

# In a separate terminal, start the web dev server with hot reload
make dev-web
```

The API server runs at `http://localhost:8080` and the web UI at `http://localhost:3000`.

### Testing

```bash
make test            # Run all tests (Go + web + worker)
make test-go         # Go tests only (with -race)
make test-web        # Web UI tests only
make test-worker     # Python worker tests only
```

### Linting

```bash
make lint            # Run all linters
make lint-go         # golangci-lint
make lint-web        # eslint
make lint-worker     # ruff
```

### Pre-Push Check

Before pushing, run the full CI pipeline locally:

```bash
make ci              # Runs lint + test
```

### Protobuf Generation

If you modify any `.proto` files under `proto/`, regenerate the generated code:

```bash
make proto           # Requires buf and grpc_tools
```

## Project Structure

```
sourcebridge/
├── cmd/sourcebridge/        # Go binary entry point
├── cli/                     # CLI command implementations
├── internal/                # Go internal packages
│   ├── api/                 # GraphQL (gqlgen) + REST API handlers
│   ├── auth/                # JWT authentication, OIDC SSO
│   ├── config/              # Configuration loading (TOML + env)
│   ├── db/                  # SurrealDB persistence layer
│   ├── graph/               # In-memory graph store
│   ├── indexer/             # Tree-sitter code indexer (7 languages)
│   ├── knowledge/           # Knowledge extraction and retrieval
│   ├── requirements/        # Requirement parsing (Markdown, CSV)
│   ├── search/              # Code search
│   └── worker/              # gRPC client for Python worker
├── workers/                 # Python gRPC worker
│   ├── reasoning/           # LLM-powered code reasoning
│   ├── linking/             # Requirement-to-code linking engine
│   ├── requirements/        # Requirement analysis
│   ├── knowledge/           # Knowledge extraction
│   ├── contracts/           # Contract generation
│   └── tests/               # Python tests
├── web/                     # Next.js web application
├── proto/                   # Protocol Buffer definitions
├── deploy/                  # Deployment configurations
│   ├── docker/              # Dockerfiles
│   ├── helm/                # Helm chart
│   └── kubernetes/          # Raw K8s manifests
├── docs/                    # Documentation
│   ├── user/                # End-user guides
│   ├── admin/               # Operator/admin guides
│   ├── self-hosted/         # Self-hosted deployment guides
│   └── contributing/        # Architecture and design docs
├── tests/                   # Integration, e2e, and smoke tests
├── config.toml.example      # Annotated configuration example
└── Makefile                 # Build, test, lint, and dev commands
```

## Pull Request Process

### Branch Naming

Use descriptive branch names with a type prefix:

- `feat/add-rust-indexer` -- New feature
- `fix/graphql-pagination` -- Bug fix
- `docs/helm-guide-update` -- Documentation
- `refactor/worker-client` -- Code restructuring
- `test/review-integration` -- Test additions
- `chore/update-deps` -- Dependency updates

### Making a Pull Request

1. Create a branch from `main`:
   ```bash
   git fetch upstream
   git checkout -b feat/my-feature upstream/main
   ```

2. Make your changes in focused, logical commits.

3. Run the full CI check locally:
   ```bash
   make ci
   ```

4. Push to your fork and open a PR against `sourcebridge/sourcebridge:main`.

5. Fill in the PR description:
   - **What** the change does
   - **Why** it is needed
   - **How** to test it
   - Screenshots or output samples for UI or CLI changes

### Review Process

- All PRs require at least one maintainer review.
- CI must pass (lint + tests) before merge.
- Keep PRs focused on a single concern. Large features should be broken into a series of smaller PRs.
- Address review feedback by pushing new commits (do not force-push during review).

## Code Standards

### Go

- Linter: [golangci-lint](https://golangci-lint.run/) -- must pass with zero warnings.
- Follow standard Go conventions: `gofmt`, exported types documented, errors wrapped with context.
- Tests use the standard `testing` package. Table-driven tests are preferred.
- Internal packages use the `internal/` convention to enforce encapsulation.

### Python

- Linter and formatter: [ruff](https://docs.astral.sh/ruff/).
- Package management: [uv](https://docs.astral.sh/uv/).
- Tests use [pytest](https://docs.pytest.org/) and live under `workers/tests/`.
- Type hints are expected on public functions.

### TypeScript / JavaScript

- Linter: [ESLint](https://eslint.org/) with the project configuration.
- The web app uses Next.js with React 19 and Tailwind CSS.
- Components should be functional with hooks. Avoid class components.

### General

- Write tests for new functionality. Bug fixes should include a regression test.
- Update documentation when changing user-facing behavior.
- Do not introduce new dependencies without discussion in the PR.

## Architecture Decisions

For significant changes to the system architecture:

1. Open a GitHub Issue or Discussion describing the problem and proposed approach.
2. Reference the [Architecture Overview](docs/contributing/architecture.md) for context on existing design decisions.
3. Wait for maintainer feedback before starting implementation. This prevents wasted effort on approaches that conflict with the project direction.

Small, incremental improvements do not require an architecture discussion -- use your judgment.

## Contributor License Agreement

First-time contributors must agree to the [Contributor License Agreement](CLA.md) before their pull request can be merged. By submitting a PR, you indicate agreement with the CLA terms.

The CLA ensures that contributions can be distributed under the project license while you retain ownership of your work.

## Code of Conduct

This project follows the [Contributor Covenant v2.1](https://www.contributor-covenant.org/version/2/1/code_of_conduct/). In short:

- Be respectful and constructive in all interactions.
- Focus on the technical merits of contributions.
- Harassment, discrimination, and personal attacks are not tolerated.
- Report concerns to the project maintainers.

## License

By contributing to SourceBridge, you agree that your contributions will be licensed under the [GNU Affero General Public License v3.0](LICENSE).
