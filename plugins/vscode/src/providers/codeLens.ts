import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { getCurrentWorkspaceFolder, resolveRepository, toRelativePosixPath } from "../context/repositories";

export class RequirementCodeLensProvider implements vscode.CodeLensProvider {
  private _onDidChangeCodeLenses = new vscode.EventEmitter<void>();
  readonly onDidChangeCodeLenses = this._onDidChangeCodeLenses.event;

  constructor(private client: SourceBridgeClient) {}

  async provideCodeLenses(document: vscode.TextDocument): Promise<vscode.CodeLens[]> {
    const lenses: vscode.CodeLens[] = [];

    try {
      const workspaceFolder =
        vscode.workspace.getWorkspaceFolder(document.uri) || getCurrentWorkspaceFolder();
      if (!workspaceFolder) return lenses;
      const repo = await resolveRepository(this.client, workspaceFolder);
      if (!repo) return lenses;
      const relativePath = toRelativePosixPath(document.uri.fsPath, workspaceFolder);
      const symbols = await this.client.getSymbolsForFile(repo.id, relativePath);

      for (const sym of symbols) {
        if (sym.kind !== "FUNCTION" && sym.kind !== "METHOD") continue;

        try {
          const links = await this.client.getCodeToRequirements(sym.id);

          if (links.length > 0) {
            const reqIds = links.map((l) => l.requirement?.externalId || l.requirementId);
            const range = new vscode.Range(
              new vscode.Position(sym.startLine - 1, 0),
              new vscode.Position(sym.startLine - 1, 0)
            );

            lenses.push(
              new vscode.CodeLens(range, {
                title: `$(checklist) ${reqIds.join(", ")}`,
                command: "sourcebridge.showLinkedRequirements",
                arguments: [sym.id],
                tooltip: `Linked requirements: ${reqIds.join(", ")}`,
              })
            );
          }
        } catch {
          // Skip symbols with query errors
        }
      }
    } catch {
      // Server not available — return empty
    }

    return lenses;
  }

  refresh(): void {
    this._onDidChangeCodeLenses.fire();
  }
}
