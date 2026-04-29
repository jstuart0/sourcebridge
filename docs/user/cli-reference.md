# CLI Reference

This page documents every top-level `sourcebridge` command. The `setup claude`
subcommand gets full treatment because it is the most common first-touch
operation. Other commands get a brief summary; follow the linked guides for
deeper coverage.

## Global flags

| Flag | Description | Default |
|------|-------------|---------|
| `--config` | Config file path | `~/.sourcebridge/config.yaml` |
| `--api-url` | API server URL | `http://localhost:8080` |
| `--verbose` | Enable verbose output | `false` |

---

## `sourcebridge setup claude`

Wire an indexed repository into Claude Code. Writes `.claude/CLAUDE.md` with
per-subsystem sections derived from the repository's clustering data, registers
SourceBridge's MCP server in `.mcp.json`, creates a `.claude/sourcebridge.json`
sidecar, and patches `.gitignore`. Idempotent — safe to re-run.

The repository must be indexed before running this command. If the server is
unreachable or the repository is not yet indexed, the command exits with a
clear error message.

```bash
sourcebridge setup claude [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--repo-id <id>` | string | auto-detect | SourceBridge repository ID. Auto-detected from the current working directory if omitted. |
| `--server <url>` | string | config / `SOURCEBRIDGE_URL` | SourceBridge server URL. Overrides `config.toml` and the `SOURCEBRIDGE_URL` environment variable. |
| `--token <ca_...>` | string | — | API token to use for this invocation. Validated against the server and saved to `~/.sourcebridge/token` (0600) unless `--no-save` is set. Takes precedence over `SOURCEBRIDGE_API_TOKEN`. |
| `--no-save` | bool | `false` | When set alongside `--token`, the token is used but not persisted to `~/.sourcebridge/token`. |
| `--force-token` | bool | `false` | Allow `--token` to overwrite a different token already saved in `~/.sourcebridge/token`. Without this flag the command refuses to clobber an existing credential. |
| `--no-skills` | bool | `false` | Skip generating `.claude/CLAUDE.md`. |
| `--no-mcp` | bool | `false` | Skip writing `.mcp.json`. |
| `--enable-hooks` | bool | `false` | Reserved — hooks are deferred to a later milestone. Currently a no-op. |
| `--dry-run` | bool | `false` | Print the file diff (CREATE / MODIFY / UNCHANGED) without writing anything. |
| `--ci` | bool | `false` | Exit non-zero if any user-modified section would be skipped. Use in CI pipelines to enforce that generated sections are not stale. |
| `--force` | bool | `false` | Overwrite user-edited sections and repair orphan markers. |
| `--commit-config` | bool | `false` | Do not add `.claude/sourcebridge.json` to `.gitignore`. By default the sidecar is gitignored. |

### Token resolution order

When the command needs an API token it checks, in order:

1. `--token` flag
2. `SOURCEBRIDGE_API_TOKEN` environment variable
3. `~/.sourcebridge/token`
4. `~/.config/sourcebridge/token`

### Examples

**Cloud install, first run** — provide server URL and token explicitly; the
token is saved to `~/.sourcebridge/token` for future runs:

```bash
sourcebridge setup claude \
  --server https://sourcebridge.example.com \
  --token ca_xxx \
  --repo-id 7c9d4387-5f3f-4acf-ac29-4b89d3f2922f
```

After this completes, add the token export to your shell profile and restart
Claude Code:

```bash
echo 'export SOURCEBRIDGE_API_TOKEN=ca_xxx' >> ~/.zshrc
source ~/.zshrc
```

**Local dev, no auth** — server URL comes from `SOURCEBRIDGE_URL` or
`config.toml`; repo ID is auto-detected from the current directory:

```bash
sourcebridge setup claude
```

**CI / scripted** — token from environment variable, dry-run to preview
changes, `--ci` to fail if user-modified sections exist:

```bash
SOURCEBRIDGE_API_TOKEN=ca_xxx \
  sourcebridge setup claude \
  --server https://sourcebridge.example.com \
  --repo-id 7c9d4387-5f3f-4acf-ac29-4b89d3f2922f \
  --dry-run \
  --ci
```

For the full first-run walkthrough see the [Getting Started](getting-started.md)
guide. For connecting Claude Code to a hosted instance see
[MCP clients](mcp-clients.md#1-quickstart--using-a-hosted-sourcebridge-from-claude-code).

---

## `sourcebridge serve`

Start the SourceBridge API server. Configuration is read from `config.toml` or
`SOURCEBRIDGE_*` environment variables. See `config.toml.example` for all
options.

```bash
sourcebridge serve [flags]
```

Refer to the [self-hosted deployment guides](../self-hosted/) for production
configuration including Helm, TLS, and multi-replica Redis setup.

---

## `sourcebridge index <path>`

Index a repository. Parses source files using tree-sitter and builds the code
graph. After indexing completes the CLI prints the `setup claude` command with
the resolved repo ID.

```bash
sourcebridge index /path/to/repo [flags]
```

**Supported languages:** Go, Python, TypeScript, JavaScript, Java, Rust, C, C++, C#

---

## `sourcebridge config`

Read and write local SourceBridge configuration. Wraps `config.toml` for
common fields without requiring a text editor.

```bash
sourcebridge config [get|set] <key> [value]
```

---

## `sourcebridge import <file>`

Import requirements from a Markdown or CSV file.

```bash
sourcebridge import /path/to/requirements.md [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--format` | File format (`markdown`, `csv`) | auto-detect |
| `--repo` | Target repository ID | first indexed repo |

---

## `sourcebridge setup`

Parent command group for integration setup subcommands. Currently contains
`setup claude` (documented above). Additional clients will be added here in
future releases.

---

## `sourcebridge ask <question>` (alias: `ask-impl`)

Ask a natural-language question about the indexed codebase.

```bash
sourcebridge ask "What does processPayment do?"
```

The answer includes references to relevant code symbols and files.

---

## `sourcebridge trace <requirement-id>` (alias: `trace-req`)

Trace a requirement to linked code symbols.

```bash
sourcebridge trace REQ-001
```

Output includes symbol name, file path, line range, confidence level, and
link source.

---

## `sourcebridge review <path>` (alias: `review-impl`)

Run a structured code review.

```bash
sourcebridge review /path/to/repo --template security
```

| Flag | Description | Default |
|------|-------------|---------|
| `--template` | Review template | `security` |

**Templates:** `security`, `solid`, `performance`, `reliability`, `maintainability`
