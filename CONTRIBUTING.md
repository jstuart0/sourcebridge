# Contributing to SourceBridge.ai

Thank you for your interest in contributing to SourceBridge.ai! This guide will help you get started.

## Code of Conduct

This project follows the [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/) code of conduct.

## CLA

First-time contributors must sign the [Contributor License Agreement](CLA.md) before their PR can be merged.

## Development Setup

### Prerequisites

- Go 1.24+
- Python 3.12+ with [uv](https://docs.astral.sh/uv/)
- Node.js 22+
- Git

### Building

```bash
# Clone the repository
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge

# Build Go binaries
go build -o bin/sourcebridge ./cmd/sourcebridge

# Install Python dependencies
cd workers && uv sync --dev && cd ..

# Install web dependencies
cd web && npm install && cd ..

# Install VS Code extension dependencies
cd plugins/vscode && npm install && cd ..
```

### Running Tests

```bash
# All tests
make test

# Go tests only
go test ./...

# Python tests only
cd workers && uv run pytest

# Web tests only
cd web && npm test

# VS Code extension tests
cd plugins/vscode && npm test
```

### Running Locally

```bash
# Start the API server
bin/sourcebridge serve

# In another terminal, start the web UI
cd web && npm run dev
```

## Submitting Changes

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Run tests (`make test`)
5. Commit with a descriptive message
6. Push to your fork
7. Open a Pull Request

### PR Guidelines

- Keep PRs focused on a single change
- Include tests for new functionality
- Update documentation if needed
- Follow existing code style and patterns

## Project Structure

```
sourcebridge/
├── cmd/sourcebridge/     # Go binary entry point
├── cli/               # CLI commands
├── internal/          # Go internal packages
│   ├── api/           # GraphQL + REST API
│   ├── auth/          # JWT authentication
│   ├── config/        # Configuration
│   ├── graph/         # In-memory graph store
│   ├── indexer/       # Tree-sitter code indexer
│   └── requirements/  # Requirement parsing
├── workers/           # Python AI workers
│   ├── linking/       # Requirement linking engine
│   ├── reasoning/     # LLM-powered code reasoning
│   └── tests/         # Python tests
├── web/               # Next.js web application
├── plugins/vscode/    # VS Code extension
├── proto/             # Protocol buffer definitions
└── tests/             # Integration and smoke tests
```

## License

By contributing, you agree that your contributions will be licensed under the AGPL-3.0 license.
