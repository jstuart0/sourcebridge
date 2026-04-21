import * as vscode from "vscode";

export function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

export function createNonce(): string {
  return Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
}

export async function openWorkspaceLocation(
  workspaceFolder: vscode.WorkspaceFolder,
  filePath: string,
  line?: number
): Promise<void> {
  const target = vscode.Uri.file(`${workspaceFolder.uri.fsPath}/${filePath}`);
  const document = await vscode.workspace.openTextDocument(target);
  const editor = await vscode.window.showTextDocument(document, vscode.ViewColumn.One);
  if (line && line > 0) {
    const position = new vscode.Position(line - 1, 0);
    editor.selection = new vscode.Selection(position, position);
    editor.revealRange(new vscode.Range(position, position));
  }
}

export function parseFileReference(ref: string): { filePath: string; line?: number } {
  const match = ref.match(/^(.+?)(?::(\d+))?$/);
  if (!match) {
    return { filePath: ref };
  }
  return {
    filePath: match[1],
    line: match[2] ? Number(match[2]) : undefined,
  };
}
