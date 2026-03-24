# Getting Started with SourceBridge.ai

SourceBridge.ai is a requirement-aware code comprehension platform that helps you trace requirements to code, run structured reviews, and understand codebases.

## Installation

### Option 1: Docker Compose (Recommended)

```bash
git clone https://github.com/sourcebridge/sourcebridge.git
cd sourcebridge
docker compose up -d
```

This starts the API server, web UI, and all dependencies.

### Option 2: Homebrew (macOS/Linux)

```bash
brew install sourcebridge/tap/sourcebridge
```

### Option 3: From Source

```bash
go build -o bin/sourcebridge ./cmd/sourcebridge
```

## First Steps

### 1. Start the Server

```bash
sourcebridge serve
```

The API server starts at http://localhost:8080.

### 2. Index a Repository

```bash
sourcebridge index /path/to/your/repo
```

SourceBridge.ai parses source files using tree-sitter and builds a graph of modules, files, and symbols.

### 3. Import Requirements

Create a requirements file in markdown format:

```markdown
# Requirements

## REQ-001: User Authentication
- Category: security
- Priority: high
Users must authenticate before accessing the system.

## REQ-002: Data Validation
- Category: data
- Priority: high
All input data must be validated before processing.
```

Import it:

```bash
sourcebridge import /path/to/requirements.md
```

### 4. Trace Requirements to Code

```bash
sourcebridge trace REQ-001
```

This shows which code symbols are linked to the requirement, with confidence scores.

### 5. Run a Code Review

```bash
sourcebridge review /path/to/repo --template security
```

Available templates: `security`, `solid`, `performance`, `reliability`, `maintainability`.

### 6. Ask Questions About Code

```bash
sourcebridge ask "What does the processPayment function do?"
```

## Web UI

Open http://localhost:3000 to access the web dashboard with:

- Repository browser
- Requirements list with linked code
- Traceability matrix visualization
- Coverage charts

## VS Code Extension

Install the SourceBridge.ai extension from the VS Code Marketplace for:

- Requirement IDs displayed above functions (CodeLens)
- Hover cards with requirement links
- Gutter decorations showing requirement coverage
- Sidebar panels for requirements and discussions

## Next Steps

- [CLI Reference](cli-reference.md) — Full command documentation
- [Web UI Guide](web-ui-guide.md) — Web dashboard features
- [Configuration](../admin/configuration.md) — Server and LLM configuration
