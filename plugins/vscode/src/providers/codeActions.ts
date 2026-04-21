// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * CodeAction lightbulb entries for symbols.
 *
 * The action set is surfaced by cursor/selection context:
 *
 *   • Over a linked function/method/class → "Show linked requirements (N)"
 *     plus "Link to another requirement…" plus "Ask SourceBridge about this symbol…".
 *   • Over an unlinked symbol → "Link to existing requirement…",
 *     "Create requirement from this symbol…" and the ask shortcut.
 *   • On a range selection with no symbol containing both endpoints →
 *     "Ask SourceBridge about this selection…".
 *
 * Actions short-circuit when the connection supervisor reports
 * offline / auth-required; we don't want to pop lightbulbs that
 * immediately fail.
 */

import * as vscode from "vscode";
import { SourceBridgeClient, SymbolNode } from "../graphql/client";
import {
  getCurrentWorkspaceFolder,
  resolveRepository,
  toRelativePosixPath,
} from "../context/repositories";
import * as log from "../logging";

export class RequirementCodeActionProvider implements vscode.CodeActionProvider {
  static readonly metadata = {
    providedCodeActionKinds: [
      vscode.CodeActionKind.QuickFix,
      vscode.CodeActionKind.RefactorRewrite,
    ],
  };

  constructor(private readonly client: SourceBridgeClient) {}

  async provideCodeActions(
    document: vscode.TextDocument,
    range: vscode.Range | vscode.Selection,
    _ctx: vscode.CodeActionContext,
    token: vscode.CancellationToken,
  ): Promise<vscode.CodeAction[]> {
    if (token.isCancellationRequested) return [];
    const actions: vscode.CodeAction[] = [];

    try {
      const workspaceFolder =
        vscode.workspace.getWorkspaceFolder(document.uri) || getCurrentWorkspaceFolder();
      if (!workspaceFolder) return actions;
      const repo = await resolveRepository(this.client, workspaceFolder);
      if (!repo || token.isCancellationRequested) return actions;

      const relativePath = toRelativePosixPath(document.uri.fsPath, workspaceFolder);
      const symbol = await findSymbolAtRange(this.client, repo.id, relativePath, range, token);
      const hasSelection = !range.isEmpty;

      if (symbol) {
        // Fast-path: lookup link count via the client's per-symbol cache.
        let linkCount = 0;
        try {
          const links = await this.client.getCodeToRequirements(symbol.id, { token });
          linkCount = links.length;
        } catch {
          // ignore; best-effort only
        }

        if (linkCount > 0) {
          const show = new vscode.CodeAction(
            `Show linked requirements (${linkCount})`,
            vscode.CodeActionKind.QuickFix,
          );
          show.command = {
            title: "Show linked requirements",
            command: "sourcebridge.showLinkedRequirements",
            arguments: [symbol.id],
          };
          actions.push(show);
        }

        const linkAction = new vscode.CodeAction(
          "Link to existing requirement…",
          vscode.CodeActionKind.RefactorRewrite,
        );
        linkAction.command = {
          title: "Link requirement",
          command: "sourcebridge.linkRequirementToSymbol",
          arguments: [symbol.id, repo.id],
        };
        actions.push(linkAction);

        const createAction = new vscode.CodeAction(
          "Create requirement from this symbol…",
          vscode.CodeActionKind.RefactorRewrite,
        );
        createAction.command = {
          title: "Create requirement from symbol",
          command: "sourcebridge.createRequirementFromSymbol",
          arguments: [symbol.id, repo.id, {
            name: symbol.name,
            filePath: symbol.filePath,
            startLine: symbol.startLine,
          }],
        };
        actions.push(createAction);

        const askAction = new vscode.CodeAction(
          `Ask SourceBridge about ${symbol.name}…`,
          vscode.CodeActionKind.RefactorRewrite,
        );
        askAction.command = {
          title: "Ask SourceBridge",
          command: "sourcebridge.askAboutSymbol",
          arguments: [symbol.id, repo.id],
        };
        actions.push(askAction);
      } else if (hasSelection) {
        // Range selection outside of a resolved symbol — still allow ask.
        const askAction = new vscode.CodeAction(
          "Ask SourceBridge about this selection…",
          vscode.CodeActionKind.RefactorRewrite,
        );
        askAction.command = {
          title: "Ask SourceBridge",
          command: "sourcebridge.askAboutSelection",
          arguments: [repo.id],
        };
        actions.push(askAction);
      }
    } catch (err) {
      log.warn("codeActions", `provideCodeActions failed: ${(err as Error).message}`);
    }

    return actions;
  }
}

/**
 * Resolve the symbol whose range contains the cursor / selection
 * start. Returns undefined when nothing fits.
 */
async function findSymbolAtRange(
  client: SourceBridgeClient,
  repoID: string,
  relativePath: string,
  range: vscode.Range,
  token: vscode.CancellationToken,
): Promise<SymbolNode | undefined> {
  const line = range.start.line + 1; // symbols are 1-indexed
  try {
    const symbols = await client.getSymbolsForFile(repoID, relativePath);
    if (token.isCancellationRequested) return undefined;
    // Prefer the narrowest containing symbol — most specific wins.
    let best: SymbolNode | undefined;
    let bestSpan = Number.POSITIVE_INFINITY;
    for (const sym of symbols) {
      if (line < sym.startLine || line > sym.endLine) continue;
      const span = sym.endLine - sym.startLine;
      if (span < bestSpan) {
        best = sym;
        bestSpan = span;
      }
    }
    return best;
  } catch {
    return undefined;
  }
}
