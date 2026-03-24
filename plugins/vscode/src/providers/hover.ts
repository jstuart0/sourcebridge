import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { getCurrentWorkspaceFolder, resolveRepository, toRelativePosixPath } from "../context/repositories";

export class RequirementHoverProvider implements vscode.HoverProvider {
  constructor(private client: SourceBridgeClient) {}

  async provideHover(
    document: vscode.TextDocument,
    position: vscode.Position
  ): Promise<vscode.Hover | null> {
    try {
      const workspaceFolder =
        vscode.workspace.getWorkspaceFolder(document.uri) || getCurrentWorkspaceFolder();
      if (!workspaceFolder) return null;
      const repo = await resolveRepository(this.client, workspaceFolder);
      if (!repo) return null;

      const relativePath = toRelativePosixPath(document.uri.fsPath, workspaceFolder);
      const symbols = await this.client.getSymbolsForFile(repo.id, relativePath);

      const line = position.line + 1;
      const symbol = symbols.find(
        (s) => line >= s.startLine && line <= s.endLine
      );
      if (!symbol) return null;

      const links = await this.client.getCodeToRequirements(symbol.id);

      const md = new vscode.MarkdownString();
      md.isTrusted = true;

      md.appendMarkdown(`### ${symbol.name}\n\n`);

      if (symbol.signature) {
        md.appendMarkdown(`\`\`\`\n${symbol.signature}\n\`\`\`\n\n`);
      }

      if (symbol.docComment) {
        md.appendMarkdown(`${symbol.docComment}\n\n`);
      }

      if (links.length > 0) {
        md.appendMarkdown(`---\n\n**Linked Requirements:**\n\n`);
        for (const link of links) {
          const verified = link.verified ? " ✓" : "";
          const rationale = link.rationale ? ` — ${link.rationale}` : "";
          const id = link.requirement?.externalId || link.requirementId;
          md.appendMarkdown(
            `- **${id}** (${link.confidence}${verified})${rationale}\n`
          );
        }
        const showLinksCommand = encodeURIComponent(
          JSON.stringify([symbol.id])
        );
        md.appendMarkdown(
          `\n[Show linked requirements](command:sourcebridge.showLinkedRequirements?${showLinksCommand})`
        );
      }

      return new vscode.Hover(md);
    } catch {
      return null;
    }
  }
}
