import * as vscode from "vscode";
import { SourceBridgeClient, Repository } from "../graphql/client";

const KEY_PREFIX = "sourcebridge.selectedRepo.";

export function toRelativePosixPath(
  absolutePath: string,
  workspaceFolder: vscode.WorkspaceFolder
): string {
  const root = workspaceFolder.uri.fsPath;
  let relative = absolutePath;
  if (absolutePath.startsWith(root)) {
    relative = absolutePath.slice(root.length);
  }
  relative = relative.replace(/\\/g, "/");
  if (relative.startsWith("/")) {
    relative = relative.slice(1);
  }
  return relative;
}

export function getCurrentWorkspaceFolder(
  editor = vscode.window.activeTextEditor
): vscode.WorkspaceFolder | undefined {
  if (editor) {
    const folder = vscode.workspace.getWorkspaceFolder(editor.document.uri);
    if (folder) {
      return folder;
    }
  }
  return vscode.workspace.workspaceFolders?.[0];
}

function workspaceStateKey(workspaceFolder: vscode.WorkspaceFolder): string {
  return `${KEY_PREFIX}${workspaceFolder.uri.fsPath}`;
}

export function getSelectedRepositoryId(
  context: vscode.ExtensionContext | undefined,
  workspaceFolder: vscode.WorkspaceFolder
): string | undefined {
  return context?.workspaceState?.get<string>(workspaceStateKey(workspaceFolder));
}

export async function setSelectedRepositoryId(
  context: vscode.ExtensionContext | undefined,
  workspaceFolder: vscode.WorkspaceFolder,
  repositoryId: string
): Promise<void> {
  await context?.workspaceState?.update(workspaceStateKey(workspaceFolder), repositoryId);
}

export async function resolveRepository(
  client: SourceBridgeClient,
  workspaceFolder: vscode.WorkspaceFolder,
  context?: vscode.ExtensionContext
): Promise<Repository | undefined> {
  const repos = await client.getRepositories();
  if (repos.length === 0) {
    vscode.window.showErrorMessage(
      "No repositories indexed on the SourceBridge server. Add a repository first."
    );
    return undefined;
  }

  const rememberedId = getSelectedRepositoryId(context, workspaceFolder);
  if (rememberedId) {
    const remembered = repos.find((r) => r.id === rememberedId);
    if (remembered) {
      return remembered;
    }
  }

  const folderName = workspaceFolder.name.toLowerCase();
  const directMatch = repos.find((r) => (r.name || "").toLowerCase() === folderName);
  if (directMatch) {
    await setSelectedRepositoryId(context, workspaceFolder, directMatch.id);
    return directMatch;
  }

  const pathMatch = repos.find(
    (r) =>
      workspaceFolder.uri.fsPath === r.path ||
      workspaceFolder.uri.fsPath.startsWith(`${r.path}/`)
  );
  if (pathMatch) {
    await setSelectedRepositoryId(context, workspaceFolder, pathMatch.id);
    return pathMatch;
  }

  if (repos.length === 1) {
    await setSelectedRepositoryId(context, workspaceFolder, repos[0].id);
    return repos[0];
  }

  const pick = await vscode.window.showQuickPick(
    repos.map((r) => ({
      label: r.name,
      description: `${r.fileCount || 0} files, ${r.functionCount || 0} functions`,
      detail: r.path,
      repo: r,
    })),
    { placeHolder: "Select the repository to use" }
  );
  if (!pick) {
    return undefined;
  }
  await setSelectedRepositoryId(context, workspaceFolder, pick.repo.id);
  return pick.repo;
}

export async function switchRepository(
  client: SourceBridgeClient,
  context: vscode.ExtensionContext,
  workspaceFolder: vscode.WorkspaceFolder
): Promise<Repository | undefined> {
  const repos = await client.getRepositories();
  if (repos.length === 0) {
    vscode.window.showErrorMessage(
      "No repositories indexed on the SourceBridge server. Add a repository first."
    );
    return undefined;
  }

  const currentId = getSelectedRepositoryId(context, workspaceFolder);
  const pick = await vscode.window.showQuickPick(
    repos.map((repo) => ({
      label: repo.name,
      description: `${repo.fileCount || 0} files, ${repo.functionCount || 0} functions`,
      detail: repo.path,
      picked: repo.id === currentId,
      repo,
    })),
    { placeHolder: `Select repository for ${workspaceFolder.name}` }
  );
  if (!pick) {
    return undefined;
  }

  await setSelectedRepositoryId(context, workspaceFolder, pick.repo.id);
  return pick.repo;
}
