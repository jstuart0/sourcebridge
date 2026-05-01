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
| `--version` | Print the binary version and exit | — |

The `--version` flag prints `sourcebridge version <X>` to stdout. Local
builds report `dev`; release builds carry the tag (e.g. `v0.7.3`). The
one-line installer (`scripts/install.sh`) uses `--version` to detect
upgrades vs. fresh installs.

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
| `--token <ca_...>` | string | — | API token to use for this invocation. Validated against the server and saved to `~/.sourcebridge/token` (0600) unless `--no-save` is set. Takes precedence over `SOURCEBRIDGE_API_TOKEN`. **Security note:** values passed via `--token` are visible to other processes that can read `/proc/<pid>/cmdline` (Linux) or the equivalent on macOS/Windows. Prefer `SOURCEBRIDGE_API_TOKEN` (env var) or `sourcebridge login` (interactive) for shared or multi-user systems. |
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
3. `~/.sourcebridge/token` (written by `sourcebridge login`)
4. `~/.config/sourcebridge/token`

### Server URL resolution order

1. `--server` flag
2. `SOURCEBRIDGE_URL` environment variable
3. `~/.sourcebridge/server` (written by `sourcebridge login`)
4. `server.public_base_url` in `config.toml`

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

## `sourcebridge login`

Authenticate with a SourceBridge server and persist credentials to disk.
Saves the API token to `~/.sourcebridge/token` (mode 0600) and the server URL
to `~/.sourcebridge/server` (mode 0644). Subsequent commands pick these up
automatically — no `--server` or `--token` flags needed.

This is the recommended first step for cloud / hosted SourceBridge installs.
It supports two auth flows, selected automatically or via `--method`:

- **OIDC** — opens a browser, completes a standard OAuth 2.0 login, and polls
  for the resulting token. Use `--no-open` to print the URL instead of launching
  a browser (useful in headless / CI environments).
- **local** — prompts for the single-admin password directly in the terminal
  (characters are hidden). For self-hosted installs that are not connected to an
  identity provider.

```bash
sourcebridge login [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server <url>` | string | `SOURCEBRIDGE_URL` env or `~/.sourcebridge/server` | SourceBridge server URL. Required on first login. |
| `--method <method>` | string | `auto` | Auth flow: `auto`, `oidc`, or `local`. `auto` prefers OIDC when both are available. |
| `--no-open` | bool | `false` | Print the OIDC auth URL instead of opening a browser. |
| `--password-stdin` | bool | `false` | (`--method local` only) Read the password from stdin (one line). Recommended for CI. |
| `--password-file <path>` | string | — | (`--method local` only) Read the password from a file. Warns to stderr if mode is more permissive than 0600. |

`SOURCEBRIDGE_PASSWORD` is also accepted as a last-resort env var for
`--method local`. Supplying more than one of these at the same time is refused.
`--password <value>` is absent by design (leaks into shell history and `ps`).

### Server URL resolution

`sourcebridge login` checks, in order:

1. `--server` flag
2. `SOURCEBRIDGE_URL` environment variable
3. `~/.sourcebridge/server` (previously saved by a prior `login`)

### Examples

**Cloud install, first login** — opens a browser for OIDC authentication:

```bash
sourcebridge login --server https://sourcebridge.example.com
```

After this completes, `~/.sourcebridge/token` and `~/.sourcebridge/server` are
written. All subsequent `sourcebridge setup claude` runs will pick them up
without additional flags.

**Headless / CI** — print the URL and complete the flow in a separate browser:

```bash
sourcebridge login --server https://sourcebridge.example.com --no-open
# Copy the printed URL into a browser, then wait for the terminal to confirm.
```

**Self-hosted with local password** — bypass OIDC and enter the admin password:

```bash
sourcebridge login --server https://internal.example.com --method local
```

**Re-login** — running `login` again replaces the existing token automatically
(no `--force` flag needed):

```bash
sourcebridge login --server https://sourcebridge.example.com
# Prints: Replaced existing ~/.sourcebridge/token.
```

### After login

Run `sourcebridge setup claude` with no additional flags:

```bash
sourcebridge setup claude --repo-id <id>
```

The server URL and token are read from `~/.sourcebridge/` automatically.

> **Future direction:** The repo Settings page also offers an inline wizard for
> minting a token and generating setup commands manually. That wizard currently
> requires pasting a literal token into your terminal once. A planned follow-up
> will redesign it to make `sourcebridge login` the default path — so the literal
> token never crosses the shell at all.

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

Parent command group for integration setup subcommands. Contains `setup claude`
and `setup admin`.

---

## `sourcebridge setup admin`

Initialize the admin password on a fresh self-hosted SourceBridge server. Posts
to `POST /auth/setup`, validates the password client-side (minimum 8 characters),
and saves the returned session token to `~/.sourcebridge/token` (mode 0600) so
the next CLI command works without a separate `sourcebridge login`.

This is the recommended first step after deploying a new server, and the natural
CI/scripted-install path.

```bash
sourcebridge setup admin [flags]
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server <url>` | string | `SOURCEBRIDGE_URL` env or `~/.sourcebridge/server` | SourceBridge server URL. |
| `--password-stdin` | bool | `false` | Read the admin password from stdin (one line). Recommended for CI. Never appears in shell history or process listings. |
| `--password-file <path>` | string | — | Read the admin password from a file (single line). Warns to stderr if file mode is more permissive than 0600. |
| `--no-save` | bool | `false` | Skip writing the returned session token to `~/.sourcebridge/token`. |

`SOURCEBRIDGE_PASSWORD` is also accepted as a last-resort env var. Supplying
more than one password vector at the same time is refused with a "pick exactly
one" error.

`--password <value>` is intentionally absent — passing a password on the
command line leaks it into shell history and `/proc/<pid>/cmdline`.

### Server URL resolution

Same chain as `sourcebridge login`:

1. `--server` flag
2. `SOURCEBRIDGE_URL` environment variable
3. `~/.sourcebridge/server` (previously saved by a prior `login`)

### Examples

**Interactive** — prompts twice with confirmation, like `passwd(1)`:

```bash
sourcebridge setup admin --server https://sourcebridge.example.com
```

**CI / scripted** — pipe the password from an environment variable:

```bash
echo "$ADMIN_PW" | sourcebridge setup admin \
    --server https://sourcebridge.example.com --password-stdin
```

**Mounted secret file**:

```bash
sourcebridge setup admin \
    --server https://sourcebridge.example.com \
    --password-file /etc/sourcebridge/admin-password
```

**Env var**:

```bash
SOURCEBRIDGE_PASSWORD="$ADMIN_PW" sourcebridge setup admin \
    --server https://sourcebridge.example.com
```

### After setup

By default the returned session token is saved and you're immediately
authenticated:

```bash
sourcebridge index <path-to-repo>
sourcebridge ask "What does this repo do?"
```

If you ran with `--no-save`, authenticate explicitly:

```bash
sourcebridge login --server https://sourcebridge.example.com --method local
```

### Error: server already initialized

If the server is already initialized, the command exits with:

```
this server is already initialized. Run `sourcebridge login --server <url> --method local` to authenticate ...
```

This is a benign error for re-run provisioning scripts — the admin account
was already created on a previous run.

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

When given a directory, the walker skips non-source directories using the same
ignore list as the indexer: `node_modules`, `.git`, `vendor`, `__pycache__`,
`.next`, `dist`, `build`, `out`, `target`, and others. Any future addition to
that shared list is picked up by `review` automatically.

---

## `sourcebridge mcp-proxy`

Bridges the stdio MCP transport to a SourceBridge server's streamable-HTTP
MCP endpoint. Intended to be invoked by Claude Code via `.mcp.json`, not by
humans directly.

`sourcebridge setup claude` writes a `.mcp.json` that names this command, so
Claude Code can connect to a remote SourceBridge install without setting a
`SOURCEBRIDGE_API_TOKEN` environment variable. The proxy reads the token from
`~/.sourcebridge/token` (or `SOURCEBRIDGE_API_TOKEN` if set) and the server
URL from `--server`, `SOURCEBRIDGE_URL`, `~/.sourcebridge/server`, or
`config.toml` in that order.

```bash
sourcebridge mcp-proxy --server https://sourcebridge.example.com
```

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server <url>` | string | resolution chain | SourceBridge server URL. |
| `--verbose` | bool | `false` | Log per-request diagnostics to stderr. **Tokens, headers, and bodies are never logged.** |
| `--max-inflight` | int | `8` | Maximum concurrent in-flight requests. |
| `--request-timeout` | duration | `10m` | Per-request HTTP timeout. Slow tool calls (deep_repo_qa) need ~5–10 minutes. |

### What the proxy supports

- **Concurrent dispatch.** Multiple requests can be in flight simultaneously
  during slow tool calls. `notifications/cancelled` aborts the matching
  in-flight request.
- **SSE progress streams.** When the server emits progress notifications via
  `text/event-stream`, the proxy parses each event per the SSE spec
  (multi-line `data` concatenation, comment stripping, blank-line boundaries)
  and forwards each JSON-RPC payload verbatim to stdout.
- **HTTP 202 acks.** Accepted-no-body responses (used for notifications)
  produce no stdout output, matching MCP semantics.
- **`MCP-Protocol-Version` forwarding.** After `initialize` captures the
  negotiated protocol version, every subsequent POST carries it.
- **Session lifecycle.** Holds `Mcp-Session-Id` from initialize; issues
  `DELETE /api/v1/mcp/http` on shutdown to free the session.

### What the proxy does not support

- Server-initiated notifications outside a tool call (long-poll). SourceBridge
  does not emit those today; if it ever does, the proxy will need an outbound
  poll loop.
- Transparent reconnection on session expiry. The client (Claude Code) owns
  the reconnection contract; the proxy surfaces session errors verbatim and
  expects a fresh `initialize`.

### Logging

By default the proxy is silent. With `--verbose`, one line per HTTP exchange
is written to stderr (method, request id, status, duration). The literal
token value never appears in any log line — this is enforced by a unit test.
