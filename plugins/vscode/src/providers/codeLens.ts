// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * RequirementCodeLensProvider surfaces a one-line CodeLens above every
 * function / method that has at least one linked requirement.
 *
 * Reliability rewrite (0.2.0):
 *
 *  * We used to ignore the `CancellationToken` VS Code hands us, so
 *    every scroll/focus/edit spun up another full symbol+links fan-out
 *    that had no way to be aborted.
 *  * The new implementation short-circuits on the token at every await
 *    boundary, caches symbol+link results by (repo:path:doc-version)
 *    in a DocCache, and serves subsequent calls from cache.
 *  * Lens titles now show the requirement **title** (not the bare
 *    external ID). Multi-link symbols collapse to
 *    `<top-title> · +N more` with a 48-char title cap.
 */

import * as vscode from "vscode";
import { SourceBridgeClient, RequirementLink, SymbolNode } from "../graphql/client";
import {
  getCurrentWorkspaceFolder,
  resolveRepository,
  toRelativePosixPath,
} from "../context/repositories";
import { DocCache } from "../state/cache";
import * as log from "../logging";

const MAX_TITLE_LEN = 48;
const CONFIDENCE_DOTS: Record<string, string> = {
  VERIFIED: "$(pass-filled)",
  HIGH: "$(primitive-dot)",
  MEDIUM: "$(circle)",
  LOW: "$(circle-outline)",
};

interface LensBundle {
  symbols: SymbolNode[];
  // links per symbol id
  linksBySymbol: Map<string, RequirementLink[]>;
}

export class RequirementCodeLensProvider implements vscode.CodeLensProvider {
  private readonly _onDidChangeCodeLenses = new vscode.EventEmitter<void>();
  readonly onDidChangeCodeLenses = this._onDidChangeCodeLenses.event;

  private readonly cache = new DocCache<LensBundle>({ ttlMs: 5 * 60_000, max: 50 });

  constructor(private client: SourceBridgeClient) {}

  /** Public refresh (called from reconnect / repo switch). */
  refresh(): void {
    this.cache.clear();
    this._onDidChangeCodeLenses.fire();
  }

  async provideCodeLenses(
    document: vscode.TextDocument,
    token: vscode.CancellationToken,
  ): Promise<vscode.CodeLens[]> {
    if (token.isCancellationRequested) return [];

    try {
      const workspaceFolder =
        vscode.workspace.getWorkspaceFolder(document.uri) || getCurrentWorkspaceFolder();
      if (!workspaceFolder) return [];
      const repo = await resolveRepository(this.client, workspaceFolder);
      if (!repo || token.isCancellationRequested) return [];

      const relativePath = toRelativePosixPath(document.uri.fsPath, workspaceFolder);
      const cacheKey = `${repo.id}:${relativePath}`;
      const bundle = await this.cache.getOrFetch(cacheKey, document.version, () =>
        this.loadBundle(repo.id, relativePath, token),
      );
      if (token.isCancellationRequested) return [];

      const lenses: vscode.CodeLens[] = [];
      for (const sym of bundle.symbols) {
        if (sym.kind !== "FUNCTION" && sym.kind !== "METHOD") continue;
        const links = bundle.linksBySymbol.get(sym.id) ?? [];
        if (!links.length) continue;
        lenses.push(this.buildLens(sym, links));
      }
      log.debug("codeLens", `Returning ${lenses.length} lenses for ${relativePath}`);
      return lenses;
    } catch (err) {
      // Cancellation isn't an error for our purposes.
      if (isCancellation(err)) return [];
      log.error("codeLens", `provideCodeLenses failed for ${document.uri.fsPath}`, err);
      return [];
    }
  }

  private async loadBundle(
    repoId: string,
    relativePath: string,
    token: vscode.CancellationToken,
  ): Promise<LensBundle> {
    log.debug("codeLens", `Fetching symbols for ${relativePath} in repo ${repoId}`);
    const symbols = await this.client.getSymbolsForFile(repoId, relativePath);
    if (token.isCancellationRequested) return { symbols, linksBySymbol: new Map() };
    log.debug("codeLens", `Got ${symbols.length} symbols for ${relativePath}`);

    // Fan-out is still N calls, but the client dedupes via its own
    // symbolLinkCache, and we're no longer firing on keystroke — so
    // the cost is capped at "once per save."
    const funcIds = symbols.filter((s) => s.kind === "FUNCTION" || s.kind === "METHOD").map((s) => s.id);
    const linksBySymbol = new Map<string, RequirementLink[]>();
    for (const id of funcIds) {
      if (token.isCancellationRequested) break;
      try {
        const links = await this.client.getCodeToRequirements(id, { token });
        if (links.length) linksBySymbol.set(id, links);
      } catch (err) {
        if (isCancellation(err)) break;
        log.warn("codeLens", `links fetch failed for ${id}: ${(err as Error).message}`);
      }
    }
    return { symbols, linksBySymbol };
  }

  private buildLens(sym: SymbolNode, links: RequirementLink[]): vscode.CodeLens {
    const range = new vscode.Range(
      new vscode.Position(Math.max(0, sym.startLine - 1), 0),
      new vscode.Position(Math.max(0, sym.startLine - 1), 0),
    );
    const top = links[0];
    const topTitle = truncate(top.requirement?.title ?? top.requirement?.externalId ?? "requirement", MAX_TITLE_LEN);
    const dot = CONFIDENCE_DOTS[top.confidence] ?? CONFIDENCE_DOTS.LOW;
    const suffix = links.length > 1 ? ` · +${links.length - 1} more` : "";
    const title = `${dot} ${topTitle}${suffix}`;

    // Tooltip duplicates the title with the confidence spelled out;
    // screen readers pick this up.
    const tierText = toSentenceCase(top.confidence);
    const tooltip = `${topTitle} · Confidence: ${tierText}${suffix}`;

    return new vscode.CodeLens(range, {
      title,
      tooltip,
      command: "sourcebridge.showLinkedRequirements",
      arguments: [sym.id],
    });
  }
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s;
  return s.slice(0, max - 1) + "…";
}

function toSentenceCase(s: string): string {
  if (!s) return "";
  const lower = s.toLowerCase();
  return lower.charAt(0).toUpperCase() + lower.slice(1);
}

function isCancellation(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const anyErr = err as { kind?: string; name?: string };
  return anyErr.kind === "cancelled" || anyErr.name === "AbortError";
}
