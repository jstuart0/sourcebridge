// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * RequirementDecorator paints subtle background colour + gutter markers
 * on lines that are linked to a requirement.
 *
 * Reliability rewrite (0.2.0):
 *
 *  * We used to fire on every keystroke (150 ms debounce). A 50-symbol
 *    file emitted 51 GraphQL requests within the debounce window —
 *    `getSymbolsForFile` plus an N-symbol fan-out to
 *    `getCodeToRequirements`. With two editors open we'd saturate the
 *    server on a continuous typing session.
 *  * Now we subscribe only to editor-focus and save events. Symbol
 *    resolution and link lookup are cached by `DocCache` (version-keyed
 *    by `document.version`), and the client-side `symbolLinkCache`
 *    inside `SourceBridgeClient` dedupes the per-symbol fan-out.
 *  * Background tint has been dropped — it collided with debugger /
 *    bracket / error highlighting. We keep an overview-ruler mark and
 *    a subtle gutter icon so users still see where annotations live
 *    without the editor looking like a highlighter explosion.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import {
  getCurrentWorkspaceFolder,
  resolveRepository,
  toRelativePosixPath,
} from "../context/repositories";
import { DocCache } from "../state/cache";
import * as log from "../logging";

export interface DecorationRange {
  startLine: number;
  endLine: number;
  confidence: string;
}

const CONFIDENCE_ORDER = ["VERIFIED", "HIGH", "MEDIUM", "LOW"] as const;

/** Overview-ruler colours per confidence tier. */
const CONFIDENCE_RULER: Record<string, string> = {
  VERIFIED: "rgba(34, 197, 94, 0.8)", // green
  HIGH: "rgba(59, 130, 246, 0.8)", // blue
  MEDIUM: "rgba(234, 179, 8, 0.8)", // yellow
  LOW: "rgba(148, 163, 184, 0.4)", // gray
};

export class RequirementDecorator implements vscode.Disposable {
  private readonly decorationTypes = new Map<string, vscode.TextEditorDecorationType>();
  private readonly disposables: vscode.Disposable[] = [];
  /**
   * Cache of decoration ranges per (repoID:relPath) at a given doc
   * version. Rebuilt only on save, active-editor change, or explicit
   * refresh — never on keystroke.
   */
  private readonly cache = new DocCache<DecorationRange[]>({ ttlMs: 5 * 60_000, max: 100 });

  constructor(private client: SourceBridgeClient) {
    for (const conf of CONFIDENCE_ORDER) {
      this.decorationTypes.set(
        conf,
        vscode.window.createTextEditorDecorationType({
          overviewRulerColor: CONFIDENCE_RULER[conf],
          overviewRulerLane: vscode.OverviewRulerLane?.Right,
          // Gutter shows a subtle colored bar on annotated lines.
          gutterIconSize: "contain",
        }),
      );
    }

    this.disposables.push(
      vscode.window.onDidChangeActiveTextEditor(() => void this.renderActive()),
      // Save — not change — so we don't fire on every keystroke.
      vscode.workspace.onDidSaveTextDocument((doc) => {
        this.cache.invalidate(doc.uri.fsPath);
        void this.renderActive();
      }),
      vscode.workspace.onDidCloseTextDocument((doc) => {
        this.cache.invalidate(doc.uri.fsPath);
      }),
    );

    void this.renderActive();
  }

  /** Rebuild decorations for the currently active editor. */
  async renderActive(): Promise<void> {
    const editor = vscode.window.activeTextEditor;
    if (!editor) return;
    await this.render(editor);
  }

  /** Public refresh entrypoint (called from commands / reconnect). */
  async refresh(): Promise<void> {
    this.cache.clear();
    this.client.invalidateSymbolLinks();
    await this.renderActive();
  }

  private async render(editor: vscode.TextEditor): Promise<void> {
    const document = editor.document;
    const ranges = await this.getDecorationRanges(document);
    const grouped: Record<string, vscode.DecorationOptions[]> = {};
    for (const conf of this.decorationTypes.keys()) grouped[conf] = [];
    for (const r of ranges) {
      const conf = this.decorationTypes.has(r.confidence) ? r.confidence : "LOW";
      grouped[conf].push({
        range: new vscode.Range(
          new vscode.Position(Math.max(0, r.startLine - 1), 0),
          new vscode.Position(Math.max(0, r.endLine - 1), Number.MAX_SAFE_INTEGER),
        ),
      });
    }
    for (const [conf, type] of this.decorationTypes) {
      editor.setDecorations(type, grouped[conf] ?? []);
    }
  }

  async getDecorationRanges(document: vscode.TextDocument): Promise<DecorationRange[]> {
    try {
      const workspaceFolder =
        vscode.workspace.getWorkspaceFolder(document.uri) || getCurrentWorkspaceFolder();
      if (!workspaceFolder) return [];
      const repo = await resolveRepository(this.client, workspaceFolder);
      if (!repo) return [];

      const relativePath = toRelativePosixPath(document.uri.fsPath, workspaceFolder);
      const key = `${repo.id}:${relativePath}`;

      return await this.cache.getOrFetch(key, document.version, async () => {
        log.debug("decorator", `Fetching decoration ranges for ${relativePath}`);
        const symbols = await this.client.getSymbolsForFile(repo.id, relativePath);
        const ranges: DecorationRange[] = [];
        for (const sym of symbols) {
          try {
            const links = await this.client.getCodeToRequirements(sym.id);
            if (!links.length) continue;
            ranges.push({
              startLine: sym.startLine,
              endLine: sym.endLine,
              confidence: pickMaxConfidence(links.map((l) => l.confidence)),
            });
          } catch (err) {
            log.warn(
              "decorator",
              `Failed to get requirements for symbol ${sym.name}: ${errorMessage(err)}`,
            );
          }
        }
        log.debug("decorator", `${ranges.length} decoration ranges for ${relativePath}`);
        return ranges;
      });
    } catch (err) {
      log.error("decorator", `getDecorationRanges failed for ${document.uri.fsPath}`, err);
      return [];
    }
  }

  /** Exposed for unit tests. */
  static computeRangesFromMockData(
    symbols: Array<{ startLine: number; endLine: number; id: string }>,
    links: Map<string, Array<{ confidence: string }>>,
  ): DecorationRange[] {
    const ranges: DecorationRange[] = [];
    for (const sym of symbols) {
      const symLinks = links.get(sym.id) ?? [];
      if (!symLinks.length) continue;
      ranges.push({
        startLine: sym.startLine,
        endLine: sym.endLine,
        confidence: pickMaxConfidence(symLinks.map((l) => l.confidence)),
      });
    }
    return ranges;
  }

  dispose(): void {
    for (const type of this.decorationTypes.values()) type.dispose();
    for (const d of this.disposables) d.dispose();
    this.cache.clear();
  }
}

/** Confidence picker: returns the highest-confidence tier in the array. */
function pickMaxConfidence(confidences: string[]): string {
  let bestIdx = -1;
  for (const c of confidences) {
    const idx = CONFIDENCE_ORDER.indexOf(c as (typeof CONFIDENCE_ORDER)[number]);
    if (idx !== -1 && (bestIdx === -1 || idx < bestIdx)) bestIdx = idx;
  }
  return bestIdx >= 0 ? CONFIDENCE_ORDER[bestIdx] : "LOW";
}

function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}
