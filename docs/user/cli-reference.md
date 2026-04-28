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

### `sourcebridge setup claude`

Wire an indexed repository into Claude Code. Writes `.claude/CLAUDE.md`
with per-subsystem sections, registers SourceBridge's MCP server in
`.mcp.json`, creates a `.claude/sourcebridge.json` sidecar, and patches
`.gitignore`. Idempotent — safe to re-run.

```bash
sourcebridge setup claude --repo-id <id> [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--repo-id` | Repository ID (auto-detected from cwd if omitted) | — |
| `--server` | SourceBridge server URL | `config.toml` or `SOURCEBRIDGE_URL` |
| `--no-skills` | Skip CLAUDE.md generation | `false` |
| `--no-mcp` | Skip `.mcp.json` registration | `false` |
| `--enable-hooks` | Reserved (no-op in v1) | `false` |
| `--dry-run` | Print the file diff without writing | `false` |
| `--ci` | Exit non-zero if any section is user-modified | auto from `CI=true` |
| `--force` | Overwrite user-modified sections | `false` |
| `--commit-config` | Don't gitignore `.claude/sourcebridge.json` | `false` |

After a successful `sourcebridge index`, the post-index hint prints the
exact `setup claude` command with the resolved repo ID. See the
[Getting Started](getting-started.md) guide for the full first-run flow.

### `sourcebridge ask <question>`

Ask a question about the indexed codebase.

```bash
sourcebridge ask "What does processPayment do?"
```

The answer includes references to relevant code symbols and files.
