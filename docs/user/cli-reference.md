# CLI Reference

## Global Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--config` | Config file path | `~/.sourcebridge/config.yaml` |
| `--api-url` | API server URL | `http://localhost:8080` |
| `--verbose` | Enable verbose output | `false` |

## Commands

### `sourcebridge serve`

Start the API server.

```bash
sourcebridge serve [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--port` | Server port | `8080` |
| `--host` | Server host | `0.0.0.0` |

### `sourcebridge index <path>`

Index a repository. Parses source files using tree-sitter and builds the code graph.

```bash
sourcebridge index /path/to/repo [flags]
```

**Supported languages:** Go, Python, TypeScript, JavaScript, Java, Rust, C, C++, C#

### `sourcebridge import <file>`

Import requirements from a markdown or CSV file.

```bash
sourcebridge import /path/to/requirements.md [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--format` | File format (`markdown`, `csv`) | auto-detect |
| `--repo` | Target repository ID | first indexed repo |

### `sourcebridge trace <requirement-id>`

Trace a requirement to linked code symbols.

```bash
sourcebridge trace REQ-001
```

Output includes symbol name, file path, line range, confidence level, and link source.

### `sourcebridge review <path>`

Run a structured code review.

```bash
sourcebridge review /path/to/repo --template security
```

| Flag | Description | Default |
|------|-------------|---------|
| `--template` | Review template | `security` |

**Templates:** `security`, `solid`, `performance`, `reliability`, `maintainability`

### `sourcebridge ask <question>`

Ask a question about the indexed codebase.

```bash
sourcebridge ask "What does processPayment do?"
```

The answer includes references to relevant code symbols and files.
