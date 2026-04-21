import * as vscode from "vscode";
import { SourceBridgeClient, Repository } from "../graphql/client";
import * as log from "../logging";

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

/**
 * In-flight dedupe keyed by workspace path. Multiple tree providers
 * (requirements / impact / field-guide / status bar) all resolve the
 * workspace → repo at activation and race each other. Without this,
 * every view opens its own QuickPick — so dismissing or answering one
 * leaves you face-to-face with the next, making it look like the
 * picker is "coming back" on each selection.
 *
 * By caching the resolution promise per workspace, concurrent callers
 * share a single prompt-and-persist round trip. The cache entry is
 * cleared on completion so a later explicit re-resolve still works.
 */
const inFlightByWorkspace = new Map<string, Promise<Repository | undefined>>();

export async function resolveRepository(
  client: SourceBridgeClient,
  workspaceFolder: vscode.WorkspaceFolder,
  context?: vscode.ExtensionContext
): Promise<Repository | undefined> {
  const cacheKey = workspaceFolder.uri.fsPath;
  const existing = inFlightByWorkspace.get(cacheKey);
  if (existing) {
    log.debug("repos", `Reusing in-flight resolve for "${workspaceFolder.name}"`);
    return existing;
  }
  const promise = doResolveRepository(client, workspaceFolder, context).finally(() => {
    inFlightByWorkspace.delete(cacheKey);
  });
  inFlightByWorkspace.set(cacheKey, promise);
  return promise;
}

async function doResolveRepository(
  client: SourceBridgeClient,
  workspaceFolder: vscode.WorkspaceFolder,
  context?: vscode.ExtensionContext
): Promise<Repository | undefined> {
  log.debug("repos", `Resolving repository for workspace "${workspaceFolder.name}" (${workspaceFolder.uri.fsPath})`);
  const repos = await client.getRepositories();
  if (repos.length === 0) {
    log.warn("repos", "No repositories indexed on the server");
    vscode.window.showErrorMessage(
      "No repositories indexed on the SourceBridge server. Add a repository first."
    );
    return undefined;
  }
  log.debug("repos", `Server has ${repos.length} repositories: ${repos.map((r) => `${r.name}(${r.path})`).join(", ")}`);

  const rememberedId = getSelectedRepositoryId(context, workspaceFolder);
  if (rememberedId) {
    const remembered = repos.find((r) => r.id === rememberedId);
    if (remembered) {
      log.debug("repos", `Resolved via remembered ID: ${remembered.name}`);
      return remembered;
    }
    log.debug("repos", `Remembered ID ${rememberedId} no longer exists on server`);
  }

  const folderName = workspaceFolder.name.toLowerCase();
  const directMatch = repos.find((r) => (r.name || "").toLowerCase() === folderName);
  if (directMatch) {
    log.info("repos", `Resolved via name match: "${directMatch.name}"`);
    await setSelectedRepositoryId(context, workspaceFolder, directMatch.id);
    return directMatch;
  }

  const pathMatch = repos.find(
    (r) =>
      workspaceFolder.uri.fsPath === r.path ||
      workspaceFolder.uri.fsPath.startsWith(`${r.path}/`)
  );
  if (pathMatch) {
    log.info("repos", `Resolved via path match: "${pathMatch.name}" (${pathMatch.path})`);
    await setSelectedRepositoryId(context, workspaceFolder, pathMatch.id);
    return pathMatch;
  }

  if (repos.length === 1) {
    log.info("repos", `Auto-selected only repository: "${repos[0].name}"`);
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
