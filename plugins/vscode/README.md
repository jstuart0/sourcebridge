# SourceBridge for VS Code

SourceBridge brings a codebase **field guide** into the editor: specs-to-code
traceability, AI explanations grounded in real source, and one-click access to
rendered cliff notes and code tours.

This extension is the VS Code client for the open-source
[SourceBridge](https://github.com/sourcebridge-ai/sourcebridge) server.

## Features (0.3.0)

- **Code-action lightbulbs.** `Cmd+.` on any symbol opens Link-to-requirement,
  Create-from-symbol, Show-linked-requirements, and Ask-about-this-symbol
  actions. No more digging through the command palette.
- **Requirements sidebar v2.** Grouped by priority with inline edit / delete
  actions, substring filter, click-to-open detail panel, and a
  "Create Requirement" empty state row when your repo is fresh.
- **Full requirement CRUD.** Create, edit, link, unlink, and soft-delete
  requirements entirely from inside the editor — no need to context-switch
  to the web UI. All deletes land in the 30-day recycle bin.
- **Ask anywhere.** `Cmd+I` on any highlight starts a grounded, streaming
  MCP conversation with your codebase. Answers arrive token-by-token with
  a live "Thinking · 3s" progress indicator. `Cmd+K N` generates a field
  guide for the active file. `Cmd+Shift+;` pops a scoped command palette
  with only the actions that apply to your current focus.
- **Change Risk sidebar.** Dedicated view groups the latest impact report
  into Changed Files / Affected Requirements / Stale Field Guides — click
  any row to jump to the file or requirement.
- **CodeLens + hover + gutter decorations** over functions / methods / classes
  showing the linked requirement title and confidence.
- **Field guide / learning path / code tour / architecture diagram** generation
  with a "lens" picker (audience × depth) and progressive streaming of
  sections as the worker produces them.
- **Repository-aware** — automatically matches your workspace folder to an
  indexed repo. Multi-root workspaces supported.
- **Always-on status bar.** Connection state in six clear modes with a
  context-appropriate click-through (retry / sign in / switch repo / logs).
- **Auto-reconnect.** 30 s heartbeat when connected; backs off
  (5 s → 15 s → 45 s → 2 min → 5 min) while offline. Transient server
  blips no longer require a window reload.
- **Opt-in telemetry.** Off by default. Enable via
  `sourcebridge.telemetry.enabled` to share anonymous command counts +
  durations. Never sends code or file paths.

## Requirements

- VS Code 1.85.0 or later.
- A reachable SourceBridge server. Either:
  - **Local**: run `sourcebridge serve` on your machine (default URL
    `http://localhost:8080`).
  - **Remote**: point the extension at your team's deployed URL via
    **SourceBridge: Configure Server**.

## Getting started

1. Open a folder your SourceBridge server has indexed.
2. Run **SourceBridge: Configure Server** from the command palette
   (`Cmd/Ctrl+Shift+P`) if the default `http://localhost:8080` isn't right.
3. Run **SourceBridge: Sign In** and choose local-password or OIDC.
4. Open any source file — CodeLenses appear above linked functions.

## Settings

| Setting | Default | Description |
| --- | --- | --- |
| `sourcebridge.apiUrl` | `http://localhost:8080` | URL of the SourceBridge.ai API server. |
| `sourcebridge.debug` | `false` | Verbose debug logging in the SourceBridge output channel. |

The legacy `sourcebridge.token` setting (0.1.x) has been removed — tokens are
always stored in VS Code's secret storage now. If you have a value in your
settings.json, the extension migrates it on first run and then drops the
setting. No action needed.

## Commands

All commands are discoverable from the palette, prefixed with **SourceBridge:**.
Highlights:

- **SourceBridge: Configure Server** — set the API URL.
- **SourceBridge: Sign In / Sign Out** — local password or OIDC flow.
- **SourceBridge: Switch Repository** — pick which indexed repo this
  workspace maps to.
- **SourceBridge: Discuss This Code** — ask a question about the selection.
- **SourceBridge: Show Requirements** — list requirements for the current
  repo.
- **SourceBridge: Show Linked Requirements** — requirements tied to the
  symbol at cursor.
- **SourceBridge: Generate Field Guide** — generate cliff notes for the
  repository, file, or symbol.
- **SourceBridge: Show Change Risk** — latest impact report.
- **SourceBridge: Show Logs** — open the output channel for
  troubleshooting.

## Troubleshooting

- **`offline · retry in …`** on the status bar: the extension can't reach
  the server. Check that it's running (`sourcebridge serve`) and that
  `sourcebridge.apiUrl` matches. Click the status bar → **Retry now** to
  force a probe.
- **Commands reveal as disabled / no lenses appear**: check **SourceBridge:
  Show Logs**. Common causes are a bad `apiUrl`, an unindexed repo, or a
  stale auth token — run **Sign In** again.
- **Slow LLM operations time out**: the server applies a 10 s network
  timeout per request. LLM-heavy calls (`Explain …`, `Generate Field Guide`)
  go through SourceBridge's streaming endpoints and should not hit that
  limit in normal use.

## License

[AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0.en.html). Contributions are
welcome at the [upstream repository](https://github.com/sourcebridge-ai/sourcebridge).
