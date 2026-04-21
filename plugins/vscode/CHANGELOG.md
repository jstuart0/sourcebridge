# Changelog

All notable changes to this extension are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this extension uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] — 2026-04-20

### MCP streaming chat (Phase 3)

- **New MCP streamable-HTTP client** (`src/mcp/client.ts`). Wraps the
  server's `/api/v1/mcp/http` endpoint and supports SSE progress
  notifications per MCP spec 2025-11-25. Reuses the existing
  SourceBridge bearer token — no second sign-in.
- **Persistent chat panel** (`src/panels/chatPanel.ts`). Multi-turn
  conversation with streaming answers, scope badge, ARIA-labelled
  input, Enter-to-send with Shift+Enter for newline, and a top-right
  clear-history control.
- **Ask commands use MCP by default.** `Cmd+I`, "Ask about symbol",
  and "Ask about selection" all stream through `explain_code`. If
  MCP is unreachable we transparently fall back to the legacy
  `discussCode` GraphQL mutation so older servers keep working.

### Change Risk sidebar (Phase 3)

- **New impact tree view** at `views/impactTree.ts`. Groups changed
  files, affected requirements, and stale field guides into a
  collapsible sidebar with icons per file status (added / removed /
  renamed / modified). Tree rows click through to open the file or
  the requirement detail panel. Refresh button in the view title.

### register.ts split (Phase 2 cleanup)

- **Command registration is no longer a 1300-line monolith.** Split
  into `authCommands.ts`, `reviewCommands.ts`, `explainCommands.ts`,
  `knowledgeCommands.ts`, `requirementsViewCommands.ts`,
  `repositoryCommands.ts`, with all shared helpers in `common.ts`.
  `register.ts` is now a 44-line aggregator.

### Symbol ↔ Requirement Navigation (Phase 2)

- **Code-action lightbulbs.** On any linked symbol: "Show linked requirements",
  "Link to existing requirement…", "Create requirement from this symbol…",
  "Ask SourceBridge about …". On an unlinked symbol or arbitrary range: the
  "Ask…" entries stay so you're never more than a Cmd+. away from context.
- **First-class requirement CRUD.** Six new commands plus inline sidebar
  actions: create (top-level), create-from-symbol, link, edit, move-to-trash,
  and unlink. All delete operations soft-delete into the recycle bin with a
  30-day restore window — no destructive-by-default paths.
- **Requirements sidebar v2.** Grouped by priority, click-to-open detail,
  inline edit / delete actions, substring filter, and an onboarding
  "Create Requirement" empty state row when the list is blank.
- **Enhanced detail panel.** Edit / Delete / Unlink buttons beside the
  existing Verify / Ask / Field-guide actions. ARIA labels for screen
  readers added to every action.

### Ask Anywhere (Phase 3 partial)

- **Cmd+I ask-about-selection keybinding.** Ask SourceBridge about whatever
  is highlighted in the editor without leaving the keyboard; falls back to
  the whole file when nothing is selected.
- **Cmd+K N field-guide keybinding.** One-keystroke cliff notes for the
  active file.
- **Cmd+Shift+; scoped palette.** A curated picker that only shows commands
  relevant to the current focus — no more scrolling past 40 command-palette
  entries to find the one you want.
- **Right-click menu reorg.** "Ask" entries come first (the most frequent
  action), then explain, review, knowledge. Matches the mental-model order
  users actually reach for.

### Opt-in Telemetry (Phase 4 partial)

- **`sourcebridge.telemetry.enabled`** setting — off by default. When on,
  sends event names + durations to the configured server's
  `/v1/telemetry/vscode` endpoint. Never sends code, file paths, or
  requirement bodies. Failures are silently swallowed so telemetry can't
  disrupt your workflow.

### Under the hood

- Commands are now split across four modules (`register.ts`,
  `requirementCrud.ts`, `askCommands.ts`, `scopedPalette.ts`). The
  register.ts monolith split is still in flight for 0.4.0.
- Added the `createManualLink`, `createRequirement`, and
  `updateRequirementFields` GraphQL mutations to the client, mirroring
  the backend shipping in SourceBridge 0.8.
- 9 new unit tests covering CRUD command registration, the v2 tree
  grouping/filter logic, and ask-command edge cases. Full suite: 81
  passing.

## [0.2.0] — 2026-04-21

### Reliability

- **No more per-keystroke network storms.** The decorator provider used to
  fire `onDidChangeTextDocument` every 150 ms, issuing a `getSymbolsForFile`
  call plus a per-symbol `getCodeToRequirements` fan-out for every render.
  Decorations now refresh only on save / active-editor change, and results
  are cached per `(repo, path, document.version)` so scroll and refocus
  don't re-fetch.
- **CodeLens honours cancellation.** `provideCodeLenses` previously ignored
  the `vscode.CancellationToken` VS Code hands it. Every await boundary now
  checks the token, and in-flight GraphQL requests are aborted through an
  `AbortSignal` adapter.
- **Client-side link caching.** The new `DocCache` holds requirement links
  per symbol id with a 2-minute TTL and an LRU cap. Subsequent CodeLens /
  hover / decoration passes for the same file hit cache instead of the wire.
- **Hardened GraphQL transport.** Every request now goes through a single
  `graphqlRequest` / `requestJSON` helper with a 10 s default timeout,
  exponential-backoff retries on 5xx + network errors, and uniform
  `TransportError` classification (`timeout`, `unauthenticated`, `offline`,
  etc.) so error messages can be specific.

### Visibility

- **Always-on status bar item.** Reflects connection state in six states —
  `connecting`, `connected`, `offline · retry in Ns`, `sign in required`,
  `open a folder`, `pick a repository`. Clicking opens a context-appropriate
  quick pick (Retry now / Sign in / Switch repository / Configure / Show
  logs).
- **Reconnect supervisor.** 30 s heartbeat when connected; exponential
  backoff (5 s → 15 s → 45 s → 2 min → 5 min cap) while offline. A
  `sourcebridge.connected` context key is set so future `when` clauses can
  gate feature availability cleanly.

### Reach

- **Narrower activation.** Was `onLanguage:{go,python,ts,js,java,rust,cpp,c,csharp}`,
  which eagerly woke the extension on basically every source file. Now
  activates on the first SourceBridge sidebar view opening, the first
  SourceBridge command being invoked, or a `.sourcebridge` marker file in
  the workspace. Cold VS Code startup no longer pays the extension's cost
  for users who never touch it.

### Configuration

- Removed the deprecated `sourcebridge.token` setting. Tokens are, and have
  been, stored in VS Code's secret storage; the setting is cleaned up on
  first run.
- Added categories `AI` and `Programming Languages` plus marketplace
  keywords so the extension is findable without knowing its name.

### Known limitations

- Command registration still lives in a single `src/commands/register.ts`
  file. A split into per-feature modules is scheduled for 0.3.0 alongside
  the bidirectional requirement CRUD work.
- No README / CHANGELOG is shown on the marketplace yet because the
  extension isn't published; those files now ship with the VSIX.
- Accessibility audit, screen-reader pass, and light/high-contrast theme
  verification land in 1.0.

## [0.1.0] — 2026-03-27

Initial preview. Registered 21 commands, a three-view sidebar container,
GraphQL client, CodeLens / hover / decoration providers, and webview panels
for discussion / review / knowledge / requirement / impact content. See
previous commit history for details.
