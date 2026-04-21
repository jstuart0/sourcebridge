// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * "Ask SourceBridge" commands surfaced by the CodeAction lightbulb, the
 * right-click menu, and the Cmd+I keybinding.
 *
 *   • `sourcebridge.askAboutSymbol` — scoped to the focused symbol.
 *     Pulls the symbol's source text + docComment so the worker has
 *     rich context without the user having to copy-paste.
 *   • `sourcebridge.askAboutSelection` — uses whatever text is
 *     highlighted (or the full file when no selection exists).
 *
 * Both commands now open the persistent MCP-backed chat panel and
 * stream answers via `explain_code`. If the MCP endpoint is
 * unreachable we fall back to the legacy `discussCode` GraphQL
 * mutation so users on older servers still get an answer.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { DiscussionTreeProvider } from "../views/discussionTree";
import { McpClient, McpClientError } from "../mcp/client";
import {
  ChatExchange,
  ChatScope,
  openChatPanel,
} from "../panels/chatPanel";
import {
  getCurrentWorkspaceFolder,
  resolveRepository,
  toRelativePosixPath,
} from "../context/repositories";
import { openWorkspaceLocation, parseFileReference } from "../panels/utils";
import * as log from "../logging";

interface AskDeps {
  mcpClient: McpClient;
  discussionTree?: DiscussionTreeProvider;
}

interface ChatHandlerDeps extends AskDeps {
  client: SourceBridgeClient;
}

function classifyError(err: unknown): string {
  if (err instanceof McpClientError) {
    if (err.kind === "unauthenticated") return "Not signed in. Run SourceBridge: Sign In.";
    if (err.kind === "network") return "SourceBridge server unreachable.";
    return err.message;
  }
  const msg = err instanceof Error ? err.message : String(err);
  if (err && typeof err === "object" && (err as { kind?: string }).kind) {
    const kind = (err as { kind?: string }).kind;
    if (kind === "unauthenticated") return "Not signed in. Run SourceBridge: Sign In.";
    if (kind === "timeout") return "Request timed out. The server may be slow — try again.";
  }
  return msg;
}

async function resolveActiveContext(
  client: SourceBridgeClient,
  context: vscode.ExtensionContext,
): Promise<
  | {
      editor: vscode.TextEditor;
      repoID: string;
      workspaceFolder: vscode.WorkspaceFolder;
      relativePath: string;
    }
  | undefined
> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) {
    vscode.window.showWarningMessage("No active editor.");
    return undefined;
  }
  const workspaceFolder =
    vscode.workspace.getWorkspaceFolder(editor.document.uri) || getCurrentWorkspaceFolder();
  if (!workspaceFolder) {
    vscode.window.showErrorMessage("The active file is not inside a workspace folder.");
    return undefined;
  }
  const repo = await resolveRepository(client, workspaceFolder, context);
  if (!repo) return undefined;
  return {
    editor,
    repoID: repo.id,
    workspaceFolder,
    relativePath: toRelativePosixPath(editor.document.uri.fsPath, workspaceFolder),
  };
}

export function registerAskCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  deps: AskDeps,
): void {
  // --------------------------------------------------------------------
  // askAboutSymbol
  // --------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.askAboutSymbol",
      async (symbolId?: string, repoID?: string) => {
        log.info("command", `askAboutSymbol (symbol=${symbolId || "cursor"})`);
        const ctx = await resolveActiveContext(client, context);
        if (!ctx) return;
        const effectiveRepoID = repoID || ctx.repoID;

        // Discover the symbol name at the cursor — purely for the
        // scope label shown in the chat panel. A best-effort miss
        // falls back to the file scope.
        let symbolName: string | undefined;
        try {
          const symbols = await client.getSymbolsForFile(effectiveRepoID, ctx.relativePath);
          const line = ctx.editor.selection.active.line + 1;
          const match = symbols
            .filter((s) => line >= s.startLine && line <= s.endLine)
            .sort((a, b) => a.endLine - a.startLine - (b.endLine - b.startLine))[0];
          symbolName = match?.name;
        } catch {
          /* fall back to file label */
        }

        const question = await vscode.window.showInputBox({
          prompt: symbolName
            ? `Ask SourceBridge about ${symbolName}`
            : "Ask SourceBridge about this symbol",
          placeHolder: "e.g., What invariants does this function rely on?",
          ignoreFocusOut: true,
        });
        if (!question) return;

        const scope: ChatScope = {
          label: symbolName ? `Symbol: ${symbolName}` : `File: ${ctx.relativePath}`,
          repositoryId: effectiveRepoID,
          filePath: ctx.relativePath,
          code: ctx.editor.document.getText(),
          language: ctx.editor.document.languageId,
        };
        log.info("askCommands", `openChatPanel scope=${scope.label}`);
        try {
          openChatPanel(
            scope,
            buildChatHandlers({ ...deps, client }, ctx.workspaceFolder),
            question,
          );
        } catch (err) {
          log.error("askCommands", "openChatPanel threw", err);
          vscode.window.showErrorMessage(
            `Could not open chat panel: ${(err as Error).message}`,
          );
        }
      },
    ),
  );

  // --------------------------------------------------------------------
  // askAboutSelection (also bound to Cmd+I)
  // --------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.askAboutSelection",
      async (maybeRepoID?: string) => {
        log.info("command", "askAboutSelection");
        const ctx = await resolveActiveContext(client, context);
        if (!ctx) return;
        const effectiveRepoID = maybeRepoID || ctx.repoID;

        const selection = ctx.editor.selection;
        const hasSelection = !selection.isEmpty;
        const question = await vscode.window.showInputBox({
          prompt: "Ask SourceBridge",
          placeHolder: hasSelection
            ? "About the highlighted code"
            : "About the current file — e.g., walk me through this",
          ignoreFocusOut: true,
        });
        if (!question) return;

        const code = hasSelection
          ? ctx.editor.document.getText(selection)
          : ctx.editor.document.getText();
        const lines = hasSelection ? selection.end.line - selection.start.line + 1 : undefined;
        const scope: ChatScope = {
          label: hasSelection
            ? `Selection (${lines} lines in ${ctx.relativePath})`
            : `File: ${ctx.relativePath}`,
          repositoryId: effectiveRepoID,
          filePath: ctx.relativePath,
          code,
          language: ctx.editor.document.languageId,
        };
        log.info("askCommands", `openChatPanel scope=${scope.label}`);
        try {
          openChatPanel(
            scope,
            buildChatHandlers({ ...deps, client }, ctx.workspaceFolder),
            question,
          );
        } catch (err) {
          log.error("askCommands", "openChatPanel threw", err);
          vscode.window.showErrorMessage(
            `Could not open chat panel: ${(err as Error).message}`,
          );
        }
      },
    ),
  );
}

/**
 * Route the ask. MCP is the preferred transport because it streams
 * progress, but if the server doesn't mount it (some enterprise
 * builds, older OSS releases) we quietly use discussCode — not
 * "falling back", just using the right transport for this server.
 * The chat UI only shows "Thinking…" either way; the source label
 * is elided unless something actually errors.
 */
function buildChatHandlers(
  deps: ChatHandlerDeps,
  workspaceFolder: vscode.WorkspaceFolder,
): Parameters<typeof openChatPanel>[1] {
  return {
    onAsk: async (exchange, scope, update) => {
      const started = Date.now();
      log.info("askCommands", `onAsk starting for scope=${scope.label}`);

      // Short-circuit straight to GraphQL when we've already learned
      // this server doesn't speak MCP. No spurious "falling back"
      // flash, no extra 404 round trip per question.
      if (deps.mcpClient.isUnavailable()) {
        log.debug("askCommands", "MCP known unavailable — using discussCode");
        await runDiscussCode(deps, scope, exchange, update);
        return;
      }

      try {
        const result = await deps.mcpClient.callTool(
          "explain_code",
          {
            repository_id: scope.repositoryId,
            file_path: scope.filePath,
            code: scope.code,
            language: scope.language,
            question: exchange.question,
          },
          {
            onProgress: (p) => {
              const secs = Math.floor((Date.now() - started) / 1000);
              // Server sends `delta` when it's streaming LLM tokens
              // for this tool. Append to the running answer and keep
              // the progress label live so the user sees both
              // "Thinking · Ns" and the text appearing under it.
              if (p.delta) {
                update({
                  answer: (exchange.answer || "") + p.delta,
                  progress: `Generating · ${secs}s`,
                });
              } else {
                update({
                  progress: `${p.message || "Thinking"} · ${secs}s`,
                });
              }
            },
          },
        );
        const text = result.content
          .map((c) => c.text ?? "")
          .join("\n")
          .trim();
        // The terminal tool result carries the authoritative full
        // answer. If the server streamed deltas we've been building
        // `exchange.answer` progressively; prefer the server's final
        // version because it reflects any cleanup / JSON-unwrap the
        // tool handler did. If the server sent no text (no content
        // frames), keep the streamed answer as-is.
        const finalAnswer = text || exchange.answer || "";
        update({ answer: finalAnswer, streaming: false, progress: undefined });
        deps.discussionTree?.addDiscussion(exchange.question, finalAnswer, {
          sourceLabel: scope.label,
        });
      } catch (err) {
        const kind = err instanceof McpClientError ? err.kind : "?";
        log.warn("askCommands", `MCP call failed kind=${kind} msg=${(err as Error)?.message}`);

        // "unavailable" means the server doesn't speak MCP at all
        // (e.g. enterprise build without the route mounted). This is
        // expected, not a failure — use GraphQL silently, no "Falling
        // back" banner. For genuine MCP failures (network / server
        // errors mid-call) we still try GraphQL but keep the user
        // informed so they know the slower path is in play.
        if (err instanceof McpClientError && err.kind === "unavailable") {
          log.info("askCommands", "MCP unavailable; using discussCode");
          await runDiscussCode(deps, scope, exchange, update);
          return;
        }
        if (err instanceof McpClientError && (err.kind === "network" || err.kind === "server")) {
          update({ progress: "Falling back to GraphQL…" });
          log.info("askCommands", "MCP errored; falling back to discussCode");
          await runDiscussCode(deps, scope, exchange, update);
          return;
        }
        update({ streaming: false, progress: undefined, error: classifyError(err) });
      }
    },
    onOpenReference: async (reference) => {
      const { filePath, line } = parseFileReference(reference);
      await openWorkspaceLocation(workspaceFolder, filePath, line);
    },
  };
}

/**
 * GraphQL discussCode path. Used when MCP isn't available on the
 * server or when an MCP call fails mid-flight. Produces the same
 * result shape as the MCP happy path so the chat panel doesn't care
 * which transport answered.
 */
async function runDiscussCode(
  deps: ChatHandlerDeps,
  scope: import("../panels/chatPanel").ChatScope,
  exchange: ChatExchange,
  update: (patch: Partial<ChatExchange>) => void,
): Promise<void> {
  try {
    const result = await deps.client.discussCode(
      scope.repositoryId,
      exchange.question,
      scope.filePath,
      scope.code,
      scope.language,
    );
    const references = result.references ?? [];
    update({
      answer: result.answer,
      references,
      streaming: false,
      progress: undefined,
    });
    deps.discussionTree?.addDiscussion(exchange.question, result.answer, {
      references,
      sourceLabel: scope.label,
    });
  } catch (err) {
    update({ streaming: false, progress: undefined, error: classifyError(err) });
  }
}

// Public — exported only for tests. Preserves the historical
// "classify transport error to friendly copy" helper shape.
export const _internal = { classifyError, resolveActiveContext };

// Avoid unused-import error on ChatExchange in debug builds.
export type { ChatExchange };
