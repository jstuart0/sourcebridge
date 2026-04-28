# Using SourceBridge from an AI client (MCP)

SourceBridge exposes its indexed repositories as an **MCP server** over HTTP.
Any MCP client — Codex, Claude Code, Cursor, Claude Desktop, the `mcp-remote`
proxy — can connect and call tools like `search_symbols`, `explain_code`,
`get_requirements`, `get_impact_report`, and `get_cliff_notes` against any
repo indexed in SourceBridge.

This doc covers:

- enabling the MCP server on a deployed instance
- minting a bearer token
- wiring up Codex, Claude Code, and Cursor
- the known multi-replica caveat
- debugging checklist when clients can't connect

---

## 1. Enable MCP on the server

MCP is **off by default**. Turn it on by setting `SOURCEBRIDGE_MCP_ENABLED=true`
on the API deployment.

**docker-compose / local:**

```bash
SOURCEBRIDGE_MCP_ENABLED=true ./sourcebridge serve
```

**Kubernetes:** edit the ConfigMap shipped in `deploy/kubernetes/configmap.yaml`:

```yaml
SOURCEBRIDGE_MCP_ENABLED: "true"
SOURCEBRIDGE_MCP_SESSION_TTL: "3600"      # seconds
SOURCEBRIDGE_MCP_KEEPALIVE: "30"          # SSE keepalive ping interval
SOURCEBRIDGE_MCP_MAX_SESSIONS: "100"
# Optional allowlist — comma-separated repo UUIDs. Empty = all repos.
# SOURCEBRIDGE_MCP_REPOS: "7c9d4387-5f3f-4acf-ac29-4b89d3f2922f"
```

Apply and roll the API:

```bash
kubectl -n sourcebridge apply -f deploy/kubernetes/configmap.yaml
kubectl -n sourcebridge rollout restart deploy/sourcebridge-api
```

Confirm the server is live:

```bash
$ curl -i https://your-host/api/v1/mcp/http -X POST
HTTP/1.1 401 Unauthorized   # 401 (not 404) means MCP is running
```

### Multi-replica deployments

Streamable-HTTP MCP (what Codex, Claude Code, and Cursor use) is **HA-safe
as long as Redis is configured**. Session state lives in the shared cache
so any replica can serve any request.

```yaml
# configmap additions
SOURCEBRIDGE_STORAGE_REDIS_MODE: "external"
SOURCEBRIDGE_STORAGE_REDIS_URL: "redis://redis:6379/0"
```

The bundled docker-compose and Helm chart ship Redis on by default. If
you disable Redis (`redis_mode: memory`) and scale the API beyond 1
replica, streamable-HTTP clients will see intermittent
`"Invalid or expired session"` errors because sessions stay pod-local.

The legacy SSE transport (`GET /api/v1/mcp/sse`) owns a TCP connection
and always requires sticky routing or `replicas=1`, even with Redis —
the event-delivery channel is process-bound.

---

## 2. Mint a bearer token

The MCP server accepts either a JWT session cookie or a `ca_`-prefixed API
token. For programmatic clients, use the API token.

```bash
# 1. Log in to get a short-lived JWT (or reuse an existing web session).
JWT=$(curl -s -X POST https://your-host/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"password":"YOUR_ADMIN_PASSWORD"}' | jq -r .token)

# 2. Mint a long-lived API token.
curl -s -X POST https://your-host/api/v1/tokens \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"codex-mcp"}' | jq .
```

Response:

```json
{
  "id": "...",
  "name": "codex-mcp",
  "prefix": "ca_535353cf",
  "token": "ca_535353cf...full-token...only-returned-once...",
  "created_at": "..."
}
```

**Save the `token` value immediately — the full token is only returned at
creation time.** The server stores only a SHA-256 hash.

---

## 3. Client configuration

### Codex CLI

Edit `~/.codex/config.toml`:

```toml
[mcp_servers.sourcebridge]
url = "https://your-host/api/v1/mcp/http"
bearer_token_env_var = "SOURCEBRIDGE_TOKEN"
```

Export the token in the shell Codex will inherit (`~/.zshrc`, `~/.bashrc`,
or `launchctl setenv` on macOS for GUI launches):

```bash
export SOURCEBRIDGE_TOKEN="ca_535353cf..."
```

Verify:

```bash
$ codex mcp list
Name          Url                                      Bearer Token Env Var  Status   Auth
sourcebridge  https://your-host/api/v1/mcp/http        SOURCEBRIDGE_TOKEN    enabled  Bearer token
```

**Sandbox note.** Codex's `read-only` and `workspace-write` sandboxes silently
cancel MCP tool calls that perform network I/O — you'll see
`user cancelled MCP tool call` in the session log even though the server
answered successfully. Run Codex with `--dangerously-bypass-approvals-and-sandbox`
or `--sandbox danger-full-access` to let MCP traffic through. This is a Codex
policy, not a SourceBridge bug.

### Claude Code

```bash
claude mcp add --transport http sourcebridge \
  https://your-host/api/v1/mcp/http \
  --header "Authorization: Bearer ca_535353cf..."
```

Verify health:

```bash
$ claude mcp list | grep sourcebridge
sourcebridge: https://your-host/api/v1/mcp/http (HTTP) - ✓ Connected
```

Tools will appear as `mcp__sourcebridge__search_symbols`, etc.

**Tool timeout — slow LLM.** Claude Code's default tool timeout is around
60 seconds. The `explain_code` and `get_cliff_notes` tools route through
the SourceBridge worker + LLM, which on a local Ollama setup can run
60–120 s. The server automatically switches a slow tool call to an SSE
streamed response (per MCP spec §6.2.1) when the client sends
`Accept: text/event-stream` — Claude Code and Codex both do. While the
tool runs, the server emits `notifications/progress` every 15 s, which
resets the client's tool timeout. A server-side hard cap
(`toolCallDeadline`, 5 min) prevents a wedged worker from pinning a
connection forever.

### Cursor / Claude Desktop / mcp-remote

Both support HTTP-transport MCP servers. Example `mcp.json`:

```json
{
  "mcpServers": {
    "sourcebridge": {
      "url": "https://your-host/api/v1/mcp/http",
      "headers": {
        "Authorization": "Bearer ca_535353cf..."
      }
    }
  }
}
```

If your client only supports stdio, wrap it with `mcp-remote`:

```json
{
  "mcpServers": {
    "sourcebridge": {
      "command": "npx",
      "args": ["mcp-remote", "https://your-host/api/v1/mcp/http",
               "--header", "Authorization: Bearer ca_535353cf..."]
    }
  }
}
```

---

## 4. Exposed tools

| Tool | What it does | Requires worker? |
|------|--------------|-----------------|
| `search_symbols` | Find functions / classes / types in an indexed repo | No |
| `explain_code` | LLM-generated explanation of a file or snippet | Yes |
| `get_requirements` | List tracked requirements, optionally with symbol links | No |
| `get_impact_report` | Latest change impact report for a repo | No |
| `get_cliff_notes` | AI-generated summaries at repo / module / file / symbol scope | Yes (generation), No (read) |
| `get_subsystems` | List the repo's clusters with representative symbols and cross-cluster call counts | No |
| `get_subsystem_by_id` | Full member list for a known cluster ID | No |
| `get_subsystem` | The cluster a given symbol belongs to plus 5 peer symbols | No |

`search_symbols`, `get_requirements`, `get_impact_report`, and the three
`get_subsystem*` tools work even if the worker is down. `explain_code`
returns an error if the worker is unreachable.

The subsystem tools are gated by a `subsystem_clustering` capability on
the server. Clustering runs as an async job after each index; while a
cluster job is in flight, `get_subsystems` returns the previous result
with `status: "stale"`.

### Quickstart for Claude Code

Instead of wiring `.mcp.json` by hand, run `sourcebridge setup claude`
on a machine with the CLI installed and an indexed repository. It
writes `.mcp.json` using the HTTP-transport schema Claude Code understands,
generates a `.claude/CLAUDE.md` with per-subsystem sections, and creates a
sidecar so the web UI can show "last auto-refresh" timestamps.

**Local server (no auth required):**

```bash
sourcebridge setup claude --repo-id <id>
```

**Cloud or authenticated server:**

```bash
sourcebridge setup claude --server https://my.sourcebridge.example --repo-id <id>
```

Then export your API token so Claude Code can authenticate:

```bash
export SOURCEBRIDGE_API_TOKEN=ca_...    # add to ~/.zshrc or ~/.bashrc
```

Restart Claude Code after exporting the variable. The `.mcp.json` written
by `setup claude` uses `${SOURCEBRIDGE_API_TOKEN}` as a placeholder — Claude
Code expands it at startup. The file contains no literal secret and is safe
to commit to version control.

See the [CLI reference](cli-reference.md#sourcebridge-setup-claude) for flags.

---

## 5. Debugging checklist

Clients failing to connect? Walk through these in order.

```bash
HOST=https://your-host
TOKEN=ca_...

# 1. Is the API up at all?
curl -i $HOST/healthz                     # expect 200

# 2. Is MCP enabled? (404 = disabled, 401 = enabled but missing auth)
curl -i -X POST $HOST/api/v1/mcp/http     # expect 401

# 3. Does the token authenticate?
curl -i -X POST -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"debug","version":"1"},"capabilities":{}}}' \
     $HOST/api/v1/mcp/http
# expect 200 with an Mcp-Session-Id response header

# 4. Watch server logs for the session.
kubectl -n sourcebridge logs -l app.kubernetes.io/name=sourcebridge-api -f \
  | grep mcp
```

If step 3 returns 200 but your client still hangs, the cause is almost always:

- **Client-side sandbox** blocking network I/O from MCP tools (Codex `read-only`).
- **Multi-replica split-brain** on the API deployment (scale to 1 replica).
- **Tool timeout** on slow `explain_code` calls (see the Claude Code note above).
