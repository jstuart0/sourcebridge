import * as vscode from "vscode";
import { SourceBridgeClient, Repository, ScopeType, SymbolNode } from "../graphql/client";
import { toRelativePosixPath } from "./repositories";

export interface ScopeContext {
  repositoryId: string;
  repositoryName: string;
  workspaceFolder: vscode.WorkspaceFolder;
  scopeType: ScopeType;
  scopePath?: string;
  filePath?: string;
  symbolName?: string;
}

export function toGraphQLScopeType(scopeType: ScopeType): string {
  switch (scopeType) {
    case "module":
      return "MODULE";
    case "file":
      return "FILE";
    case "symbol":
      return "SYMBOL";
    default:
      return "REPOSITORY";
  }
}

export function fromGraphQLScopeType(scopeType: string): ScopeType {
  switch (scopeType) {
    case "MODULE":
      return "module";
    case "FILE":
      return "file";
    case "SYMBOL":
      return "symbol";
    default:
      return "repository";
  }
}

export function getScopeLabel(scope: Pick<ScopeContext, "scopeType" | "scopePath" | "repositoryName">): string {
  if (scope.scopeType === "module" && scope.scopePath) {
    return `${scope.scopePath}/`;
  }
  if (scope.scopeType === "file" && scope.scopePath) {
    return scope.scopePath.split("/").at(-1) || scope.scopePath;
  }
  if (scope.scopeType === "symbol" && scope.scopePath) {
    return scope.scopePath.split("#").at(-1) || scope.scopePath;
  }
  return scope.repositoryName;
}

export function getScopeBreadcrumbs(scope: ScopeContext): Array<{ label: string; scopeType: ScopeType; scopePath?: string }> {
  const breadcrumbs: Array<{ label: string; scopeType: ScopeType; scopePath?: string }> = [
    { label: scope.repositoryName, scopeType: "repository" },
  ];

  if (scope.scopeType === "module" && scope.scopePath) {
    const parts = scope.scopePath.split("/").filter(Boolean);
    let current = "";
    for (const part of parts) {
      current = current ? `${current}/${part}` : part;
      breadcrumbs.push({ label: `${part}/`, scopeType: "module", scopePath: current });
    }
  }

  if (scope.scopeType === "file" && scope.filePath) {
    const modulePath = parentModulePath(scope.filePath);
    if (modulePath) {
      breadcrumbs.push({ label: `${modulePath}/`, scopeType: "module", scopePath: modulePath });
    }
    breadcrumbs.push({
      label: scope.filePath.split("/").at(-1) || scope.filePath,
      scopeType: "file",
      scopePath: scope.filePath,
    });
  }

  if (scope.scopeType === "symbol" && scope.filePath && scope.scopePath) {
    const modulePath = parentModulePath(scope.filePath);
    if (modulePath) {
      breadcrumbs.push({ label: `${modulePath}/`, scopeType: "module", scopePath: modulePath });
    }
    breadcrumbs.push({
      label: scope.filePath.split("/").at(-1) || scope.filePath,
      scopeType: "file",
      scopePath: scope.filePath,
    });
    breadcrumbs.push({
      label: scope.symbolName || scope.scopePath.split("#").at(-1) || "Symbol",
      scopeType: "symbol",
      scopePath: scope.scopePath,
    });
  }

  return breadcrumbs;
}

export function createRepositoryScope(
  repository: Repository,
  workspaceFolder: vscode.WorkspaceFolder
): ScopeContext {
  return {
    repositoryId: repository.id,
    repositoryName: repository.name,
    workspaceFolder,
    scopeType: "repository",
  };
}

export function createFileScope(
  repository: Repository,
  workspaceFolder: vscode.WorkspaceFolder,
  filePath: string
): ScopeContext {
  return {
    repositoryId: repository.id,
    repositoryName: repository.name,
    workspaceFolder,
    scopeType: "file",
    scopePath: filePath,
    filePath,
  };
}

export function createModuleScope(
  repository: Repository,
  workspaceFolder: vscode.WorkspaceFolder,
  modulePath: string
): ScopeContext {
  return {
    repositoryId: repository.id,
    repositoryName: repository.name,
    workspaceFolder,
    scopeType: "module",
    scopePath: modulePath,
  };
}

export function createSymbolScope(
  repository: Repository,
  workspaceFolder: vscode.WorkspaceFolder,
  filePath: string,
  symbolName: string
): ScopeContext {
  return {
    repositoryId: repository.id,
    repositoryName: repository.name,
    workspaceFolder,
    scopeType: "symbol",
    scopePath: `${filePath}#${symbolName}`,
    filePath,
    symbolName,
  };
}

export async function inferFileScope(
  repository: Repository,
  workspaceFolder: vscode.WorkspaceFolder,
  editor: vscode.TextEditor
): Promise<ScopeContext> {
  const filePath = toRelativePosixPath(editor.document.uri.fsPath, workspaceFolder);
  return createFileScope(repository, workspaceFolder, filePath);
}

export async function inferSymbolScope(
  client: SourceBridgeClient,
  repository: Repository,
  workspaceFolder: vscode.WorkspaceFolder,
  editor: vscode.TextEditor
): Promise<ScopeContext> {
  const filePath = toRelativePosixPath(editor.document.uri.fsPath, workspaceFolder);
  const symbols = await client.getSymbolsForFile(repository.id, filePath);
  const activeLine = editor.selection.active.line + 1;
  const symbol = pickSymbolAtLine(symbols, activeLine);
  if (!symbol) {
    return createFileScope(repository, workspaceFolder, filePath);
  }
  return createSymbolScope(repository, workspaceFolder, filePath, symbol.name);
}

function pickSymbolAtLine(symbols: SymbolNode[], line: number): SymbolNode | undefined {
  return symbols
    .filter((sym) => line >= sym.startLine && line <= sym.endLine)
    .sort((a, b) => (a.endLine - a.startLine) - (b.endLine - b.startLine))[0];
}

export function parentModulePath(filePath: string): string | undefined {
  if (!filePath.includes("/")) {
    return undefined;
  }
  return filePath.slice(0, filePath.lastIndexOf("/"));
}
