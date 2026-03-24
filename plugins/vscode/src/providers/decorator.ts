import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { getCurrentWorkspaceFolder, resolveRepository, toRelativePosixPath } from "../context/repositories";

export interface DecorationRange {
  startLine: number;
  endLine: number;
  confidence: string;
}

const CONFIDENCE_COLORS: Record<string, string> = {
  VERIFIED: "rgba(34, 197, 94, 0.08)",
  HIGH: "rgba(59, 130, 246, 0.08)",
  MEDIUM: "rgba(234, 179, 8, 0.08)",
  LOW: "rgba(148, 163, 184, 0.05)",
};

const CONFIDENCE_GUTTER: Record<string, string> = {
  VERIFIED: "rgba(34, 197, 94, 0.6)",
  HIGH: "rgba(59, 130, 246, 0.6)",
  MEDIUM: "rgba(234, 179, 8, 0.6)",
  LOW: "rgba(148, 163, 184, 0.3)",
};

export class RequirementDecorator implements vscode.Disposable {
  private decorationTypes: Map<string, vscode.TextEditorDecorationType> = new Map();
  private disposables: vscode.Disposable[] = [];
  private refreshTimer: NodeJS.Timeout | undefined;

  constructor(private client: SourceBridgeClient) {
    for (const [conf, bg] of Object.entries(CONFIDENCE_COLORS)) {
      this.decorationTypes.set(
        conf,
        vscode.window.createTextEditorDecorationType({
          backgroundColor: bg,
          gutterIconPath: undefined,
          overviewRulerColor: CONFIDENCE_GUTTER[conf],
          overviewRulerLane: vscode.OverviewRulerLane?.Right,
          isWholeLine: true,
        })
      );
    }

    this.disposables.push(
      vscode.window.onDidChangeActiveTextEditor(() => this.updateDecorations()),
      vscode.workspace.onDidChangeTextDocument(() => this.updateDecorations())
    );

    this.updateDecorations();
  }

  async updateDecorations(): Promise<void> {
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer);
    }
    this.refreshTimer = setTimeout(() => {
      void this.renderDecorations();
    }, 150);
  }

  private async renderDecorations(): Promise<void> {
    const editor = vscode.window.activeTextEditor;
    if (!editor) return;

    const ranges = await this.getDecorationRanges(editor.document);
    const grouped: Record<string, vscode.DecorationOptions[]> = {};

    for (const [conf] of this.decorationTypes) {
      grouped[conf] = [];
    }

    for (const r of ranges) {
      const conf = r.confidence;
      if (!grouped[conf]) grouped[conf] = [];
      grouped[conf].push({
        range: new vscode.Range(
          new vscode.Position(r.startLine - 1, 0),
          new vscode.Position(r.endLine - 1, Number.MAX_SAFE_INTEGER)
        ),
      });
    }

    for (const [conf, type] of this.decorationTypes) {
      editor.setDecorations(type, grouped[conf] || []);
    }
  }

  async getDecorationRanges(document: vscode.TextDocument): Promise<DecorationRange[]> {
    const ranges: DecorationRange[] = [];

    try {
      const workspaceFolder =
        vscode.workspace.getWorkspaceFolder(document.uri) || getCurrentWorkspaceFolder();
      if (!workspaceFolder) return ranges;
      const repo = await resolveRepository(this.client, workspaceFolder);
      if (!repo) return ranges;

      const relativePath = toRelativePosixPath(document.uri.fsPath, workspaceFolder);
      const symbols = await this.client.getSymbolsForFile(repo.id, relativePath);

      for (const sym of symbols) {
        try {
          const links = await this.client.getCodeToRequirements(sym.id);

          if (links.length > 0) {
            const maxConfidence = links.reduce((max, l) => {
              const order = ["VERIFIED", "HIGH", "MEDIUM", "LOW"];
              return order.indexOf(l.confidence) < order.indexOf(max) ? l.confidence : max;
            }, "LOW");

            ranges.push({
              startLine: sym.startLine,
              endLine: sym.endLine,
              confidence: maxConfidence,
            });
          }
        } catch {
          // Skip
        }
      }
    } catch {
      // Server not available
    }

    return ranges;
  }

  static computeRangesFromMockData(
    symbols: Array<{ startLine: number; endLine: number; id: string }>,
    links: Map<string, Array<{ confidence: string }>>
  ): DecorationRange[] {
    const ranges: DecorationRange[] = [];
    for (const sym of symbols) {
      const symLinks = links.get(sym.id) || [];
      if (symLinks.length > 0) {
        const maxConfidence = symLinks.reduce((max, l) => {
          const order = ["VERIFIED", "HIGH", "MEDIUM", "LOW"];
          return order.indexOf(l.confidence) < order.indexOf(max) ? l.confidence : max;
        }, "LOW");
        ranges.push({
          startLine: sym.startLine,
          endLine: sym.endLine,
          confidence: maxConfidence,
        });
      }
    }
    return ranges;
  }

  dispose(): void {
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer);
    }
    for (const type of this.decorationTypes.values()) {
      type.dispose();
    }
    this.disposables.forEach((d) => d.dispose());
  }
}
