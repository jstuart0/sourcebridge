// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Shared helpers used across the split command modules.
 *
 * This module exists because register.ts used to be a 1300-line
 * monolith with feature-specific command handlers tangled together.
 * Pulling the reusable bits out here keeps each feature module
 * (auth, knowledge, explain, requirements, review, repository) short
 * and focused — each only imports what it actually needs.
 */

import * as vscode from "vscode";
import {
  SourceBridgeClient,
  KnowledgeArtifact,
  Repository,
  Requirement,
  RequirementLink,
  ScopeType,
  SymbolNode,
} from "../graphql/client";
import { DiscussionTreeProvider } from "../views/discussionTree";
import { KnowledgeTreeProvider } from "../views/knowledgeTree";
import { RequirementsTreeProvider } from "../views/requirementsTree";
import {
  createKnowledgePanel,
  updateKnowledgePanel,
} from "../panels/knowledgePanel";
import { createRequirementPanel, ArtifactHint } from "../panels/requirementPanel";
import { createDiscussionPanel, DiscussionData } from "../panels/discussionPanel";
import { getCapabilities } from "../context/capabilities";
import {
  createRequirementScope,
  ScopeContext,
  toGraphQLScopeType,
} from "../context/scope";
import {
  getCurrentWorkspaceFolder,
  resolveRepository,
  toRelativePosixPath,
} from "../context/repositories";
import { openWorkspaceLocation, parseFileReference } from "../panels/utils";
import * as log from "../logging";

export interface CommandDependencies {
  discussionTree?: DiscussionTreeProvider;
  knowledgeTree?: KnowledgeTreeProvider;
  requirementsTree?: RequirementsTreeProvider;
}

export interface ResolvedRepoCtx {
  repository: Repository;
  workspaceFolder: vscode.WorkspaceFolder;
}

export interface ResolvedEditorCtx extends ResolvedRepoCtx {
  editor: vscode.TextEditor;
}

/**
 * Translate any error from the GraphQL client into user-friendly copy.
 *
 * Understands both the legacy free-form messages and the new
 * {@link TransportError} shape with a `kind` discriminator. New code
 * paths prefer the kind-based branches.
 */
export function classifyError(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  log.error("command", `classifyError: ${msg}`, err);

  if (err && typeof err === "object" && (err as { kind?: string }).kind) {
    const kind = (err as { kind?: string }).kind;
    if (kind === "unauthenticated") return "Not signed in. Run SourceBridge: Sign In.";
    if (kind === "forbidden") return "Action forbidden for this account.";
    if (kind === "not-found") return "The requested item no longer exists.";
    if (kind === "timeout") return `Request timed out. The server may be slow — try again.`;
    if (kind === "offline" || kind === "network") {
      return `SourceBridge server returned an error: ${msg}`;
    }
    if (kind === "http") return `SourceBridge server returned an error: ${msg}`;
    if (kind === "graphql") {
      if (msg.includes("AI features are unavailable") || msg.includes("source unavailable")) {
        return "AI features are currently unavailable on the server. Check that the LLM backend is configured and running.";
      }
      return `SourceBridge server returned an error: ${msg}`;
    }
  }

  if (msg.includes("AI features are unavailable") || msg.includes("source unavailable")) {
    return "AI features are currently unavailable on the server. Check that the LLM backend is configured and running.";
  }
  if (msg.includes("GraphQL request failed") || msg.includes("HTTP 5")) {
    return `SourceBridge server returned an error: ${msg}`;
  }
  return msg;
}

export async function ensureServer(client: SourceBridgeClient): Promise<boolean> {
  const running = await client.isServerRunning();
  if (!running) {
    log.warn("command", "Server not running — command aborted");
    vscode.window.showErrorMessage(
      "SourceBridge server not running. Start it with `sourcebridge serve`.",
    );
    return false;
  }
  return true;
}

export async function resolveWorkspaceRepository(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  workspaceFolder?: vscode.WorkspaceFolder,
): Promise<ResolvedRepoCtx | undefined> {
  const folder = workspaceFolder || getCurrentWorkspaceFolder();
  if (!folder) {
    vscode.window.showErrorMessage("No workspace folder open.");
    return undefined;
  }
  const repository = await resolveRepository(client, folder, context);
  if (!repository) return undefined;
  return { repository, workspaceFolder: folder };
}

export async function resolveEditorRepository(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
): Promise<ResolvedEditorCtx | undefined> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) {
    vscode.window.showWarningMessage("No active editor");
    return undefined;
  }
  const workspaceFolder = vscode.workspace.getWorkspaceFolder(editor.document.uri);
  if (!workspaceFolder) {
    vscode.window.showErrorMessage("The active file is not inside a workspace folder.");
    return undefined;
  }
  const repository = await resolveRepository(client, workspaceFolder, context);
  if (!repository) return undefined;
  return { editor, repository, workspaceFolder };
}

export function getActiveEditorContext():
  | { editor: vscode.TextEditor; workspaceFolder: vscode.WorkspaceFolder }
  | undefined {
  const editor = vscode.window.activeTextEditor;
  if (!editor) {
    vscode.window.showWarningMessage("No active editor");
    return undefined;
  }
  const workspaceFolder = vscode.workspace.getWorkspaceFolder(editor.document.uri);
  if (!workspaceFolder) {
    vscode.window.showErrorMessage("The active file is not inside a workspace folder.");
    return undefined;
  }
  return { editor, workspaceFolder };
}

export async function ensureScopedKnowledge(client: SourceBridgeClient): Promise<boolean> {
  const capabilities = await getCapabilities(client);
  if (!capabilities.scopedKnowledge) {
    vscode.window.showWarningMessage("Scoped knowledge is not available on this server yet.");
    return false;
  }
  return true;
}

export async function ensureScopedExplain(client: SourceBridgeClient): Promise<boolean> {
  const capabilities = await getCapabilities(client);
  if (!capabilities.scopedExplain) {
    vscode.window.showWarningMessage("Scoped explain is not available on this server yet.");
    return false;
  }
  return true;
}

export async function ensureImpactReports(client: SourceBridgeClient): Promise<boolean> {
  const capabilities = await getCapabilities(client);
  if (!capabilities.impactReports) {
    vscode.window.showWarningMessage("Impact reports are not available on this server yet.");
    return false;
  }
  return true;
}

export async function openDiscussionReference(
  workspaceFolder: vscode.WorkspaceFolder,
  reference: string,
): Promise<void> {
  const { filePath, line } = parseFileReference(reference);
  await openWorkspaceLocation(workspaceFolder, filePath, line);
}

export async function getCurrentSymbol(
  client: SourceBridgeClient,
  repository: Repository,
  workspaceFolder: vscode.WorkspaceFolder,
  editor: vscode.TextEditor,
): Promise<SymbolNode | undefined> {
  const filePath = toRelativePosixPath(editor.document.uri.fsPath, workspaceFolder);
  const symbols = await client.getSymbolsForFile(repository.id, filePath);
  const activeLine = editor.selection.active.line + 1;
  return symbols
    .filter((s) => activeLine >= s.startLine && activeLine <= s.endLine)
    .sort((a, b) => a.endLine - a.startLine - (b.endLine - b.startLine))[0];
}

export async function resolveArtifactHint(
  client: SourceBridgeClient,
  repositoryId: string,
  requirementId: string,
): Promise<ArtifactHint> {
  try {
    const artifacts = await client.getKnowledgeArtifacts(repositoryId, "REQUIREMENT", requirementId);
    if (artifacts.length > 0) {
      const a = artifacts[0];
      if (a.status === "GENERATING") return { state: "generating" };
      if (a.status === "READY") return { state: "ready", stale: a.stale };
    }
  } catch (err) {
    log.debug("command", `resolveArtifactHint failed: ${err instanceof Error ? err.message : err}`);
  }
  return { state: "none" };
}

/**
 * Build the full set of webview handlers for the requirement detail
 * panel. Includes CRUD delegation (edit/delete/unlink) and the
 * field-guide / ask-question routes.
 */
export function buildRequirementHandlers(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  requirement: Requirement,
  scope: ResolvedRepoCtx,
  deps: CommandDependencies,
) {
  return {
    onOpenSymbol: async (link: RequirementLink) => {
      if (link.symbol?.filePath) {
        await openWorkspaceLocation(scope.workspaceFolder, link.symbol.filePath, link.symbol.startLine);
      }
    },
    onVerify: async (linkId: string, verified: boolean) => {
      await client.verifyLink(linkId, verified);
      deps.requirementsTree?.refresh();
      const refreshedLinks = await client.getRequirementLinks(requirement.id, 50);
      await openRequirementDetail(context, client, requirement, refreshedLinks, scope, deps);
    },
    onEdit: async () => {
      await vscode.commands.executeCommand("sourcebridge.editRequirement", requirement);
      const fresh = await client.getRequirement(requirement.id);
      if (fresh) {
        const links = await client.getRequirementLinks(requirement.id, 50);
        await openRequirementDetail(context, client, fresh, links, scope, deps);
      }
    },
    onDelete: async () => {
      await vscode.commands.executeCommand("sourcebridge.deleteRequirement", requirement);
    },
    onUnlink: async (linkId: string) => {
      await vscode.commands.executeCommand("sourcebridge.unlinkRequirementLink", linkId);
      const refreshed = await client.getRequirementLinks(requirement.id, 50);
      await openRequirementDetail(context, client, requirement, refreshed, scope, deps);
    },
    onGenerateFieldGuide: async () => {
      try {
        const reqScope = createRequirementScope(
          scope.repository,
          scope.workspaceFolder,
          requirement.id,
          requirement.externalId || requirement.title,
        );
        await openKnowledgeScopePanel(context, client, reqScope, deps);
      } catch (err) {
        vscode.window.showErrorMessage(`Failed to generate field guide: ${classifyError(err)}`);
      }
    },
    onAskQuestion: async () => {
      const displayName = requirement.externalId || requirement.title;
      const question = await vscode.window.showInputBox({
        prompt: `Ask about requirement: ${displayName}`,
        placeHolder: "How is this requirement implemented?",
      });
      if (!question) return;
      try {
        let artifactId: string | undefined;
        try {
          const artifacts = await client.getKnowledgeArtifacts(
            scope.repository.id,
            "REQUIREMENT",
            requirement.id,
          );
          if (artifacts.length > 0 && artifacts[0].status === "READY") {
            artifactId = artifacts[0].id;
          }
        } catch (err) {
          log.debug("command", `no cached artifact for requirement grounding: ${err instanceof Error ? err.message : err}`);
        }
        await vscode.window.withProgress(
          { location: vscode.ProgressLocation.Notification, title: "Thinking..." },
          async () => {
            const result = await client.discussCode(
              scope.repository.id,
              question,
              undefined,
              undefined,
              undefined,
              requirement.id,
              artifactId,
            );
            const discussionData: DiscussionData = {
              question,
              answer: result.answer,
              references: result.references,
              sourceLabel: `Requirement: ${displayName}`,
              sourceNote: "Indexed repository context.",
            };
            deps.discussionTree?.addDiscussion(question, result.answer);
            createDiscussionPanel(discussionData, async (reference) => {
              await openDiscussionReference(scope.workspaceFolder, reference);
            });
          },
        );
      } catch (err) {
        vscode.window.showErrorMessage(`Failed to get an answer: ${classifyError(err)}`);
      }
    },
  };
}

export async function openRequirementDetail(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  requirement: Requirement,
  links: RequirementLink[],
  scope: ResolvedRepoCtx,
  deps: CommandDependencies,
): Promise<void> {
  const handlers = buildRequirementHandlers(context, client, requirement, scope, deps);
  const hint = await resolveArtifactHint(client, scope.repository.id, requirement.id);
  createRequirementPanel(requirement, links, handlers, hint);
  if (links.length === 50) {
    const extraLinks = await client.getRequirementLinks(requirement.id, undefined, 50);
    if (extraLinks.length > 0) {
      createRequirementPanel(requirement, [...links, ...extraLinks], handlers, hint);
    }
  }
}

async function generateArtifactForScope(
  client: SourceBridgeClient,
  scope: ScopeContext,
  type: string,
  audience?: string,
  depth?: string,
): Promise<KnowledgeArtifact> {
  switch (type) {
    case "learning_path":
      return client.generateLearningPath(scope.repositoryId, audience, depth);
    case "code_tour":
      return client.generateCodeTour(scope.repositoryId, audience, depth);
    default:
      return client.generateCliffNotes(
        scope.repositoryId,
        audience,
        depth,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath,
      );
  }
}

async function findArtifactForLens(
  client: SourceBridgeClient,
  scope: ScopeContext,
  type: string,
  audience: string,
  depth: string,
): Promise<KnowledgeArtifact | undefined> {
  const artifacts = await client.getKnowledgeArtifacts(
    scope.repositoryId,
    toGraphQLScopeType(scope.scopeType),
    scope.scopePath,
  );
  return artifacts.find(
    (a) => a.type === type && a.audience === audience && a.depth === depth,
  );
}

export async function chooseRequirement(
  client: SourceBridgeClient,
  links: RequirementLink[],
): Promise<{ requirement: Requirement; links: RequirementLink[] } | undefined> {
  type RequirementPick = vscode.QuickPickItem & {
    requirement: Requirement;
    links: RequirementLink[];
  };
  const grouped = new Map<string, RequirementLink[]>();
  for (const link of links) {
    const list = grouped.get(link.requirementId) || [];
    list.push(link);
    grouped.set(link.requirementId, list);
  }
  const entries = [...grouped.entries()];
  if (entries.length === 0) return undefined;
  if (entries.length === 1) {
    const [requirementId, reqLinks] = entries[0];
    const requirement = await client.getRequirement(requirementId);
    if (!requirement) return undefined;
    return { requirement, links: reqLinks };
  }

  const items = (
    await Promise.all(
      entries.map(async ([requirementId, reqLinks]) => {
        const requirement = await client.getRequirement(requirementId);
        if (!requirement) return undefined;
        const item: RequirementPick = {
          label: requirement.externalId || requirement.title,
          description: requirement.title,
          detail: requirement.description,
          requirement,
          links: reqLinks,
        };
        return item;
      }),
    )
  ).filter((i): i is RequirementPick => !!i);

  const picked = await vscode.window.showQuickPick(items, {
    placeHolder: "Select a requirement",
  });
  if (!picked) return undefined;
  return { requirement: picked.requirement, links: picked.links };
}

export async function openKnowledgeScopePanel(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  scope: ScopeContext,
  deps: CommandDependencies,
  artifact?: KnowledgeArtifact,
): Promise<void> {
  let currentArtifact = artifact;
  if (!currentArtifact) {
    try {
      currentArtifact = (
        await client.getKnowledgeArtifacts(
          scope.repositoryId,
          toGraphQLScopeType(scope.scopeType),
          scope.scopePath,
        )
      )[0];
    } catch (err) {
      log.debug("command", `openKnowledgeScopePanel: no existing artifact: ${err instanceof Error ? err.message : err}`);
      currentArtifact = undefined;
    }
  }
  if (!currentArtifact) {
    currentArtifact = await client.generateCliffNotes(
      scope.repositoryId,
      undefined,
      undefined,
      toGraphQLScopeType(scope.scopeType),
      scope.scopePath,
    );
  }

  const loadChildScopes = async (): Promise<Array<{ label: string; scopeType: ScopeType; scopePath?: string }>> => {
    let capabilities;
    try {
      capabilities = await getCapabilities(client);
    } catch (err) {
      log.warn("command", `loadChildScopes: capabilities fetch failed: ${err instanceof Error ? err.message : err}`);
      return [];
    }
    if (!capabilities.scopedKnowledge) return [];
    const children = await client.getKnowledgeScopeChildren(
      scope.repositoryId,
      toGraphQLScopeType(scope.scopeType),
      scope.scopePath || "",
      deps.knowledgeTree?.getLens().audience,
      deps.knowledgeTree?.getLens().depth,
    );
    return children.map((c) => ({
      label: c.label,
      scopeType: c.scopeType.toLowerCase() as ScopeType,
      scopePath: c.scopePath,
    }));
  };

  const panel = createKnowledgePanel(
    currentArtifact,
    scope,
    {
      onOpenLocation: async (filePath, line) => {
        await openWorkspaceLocation(scope.workspaceFolder, filePath, line);
      },
      onRefresh: async () => {
        if (currentArtifact) {
          const refreshed = await client.getKnowledgeArtifact(currentArtifact.id);
          if (refreshed) {
            currentArtifact = refreshed;
            updateKnowledgePanel(panel, currentArtifact, scope, await loadChildScopes());
            deps.knowledgeTree?.refresh();
          }
        }
      },
      onRegenerate: async () => {
        const artifactType = currentArtifact!.type;
        currentArtifact = await generateArtifactForScope(
          client,
          scope,
          artifactType,
          deps.knowledgeTree?.getLens().audience,
          deps.knowledgeTree?.getLens().depth,
        );
        updateKnowledgePanel(panel, currentArtifact, scope, await loadChildScopes());
        deps.knowledgeTree?.refresh();
        if (currentArtifact.status === "GENERATING") {
          startKnowledgePolling(client, currentArtifact.id, panel, scope, deps, loadChildScopes);
        }
      },
      onSetLens: async (audience, depth) => {
        deps.knowledgeTree?.setLens(audience, depth);
        const artifactType = currentArtifact!.type;
        currentArtifact =
          (await findArtifactForLens(client, scope, artifactType, audience, depth)) ||
          (await generateArtifactForScope(client, scope, artifactType, audience, depth));
        updateKnowledgePanel(panel, currentArtifact, scope, await loadChildScopes());
        deps.knowledgeTree?.refresh();
        if (currentArtifact.status === "GENERATING") {
          startKnowledgePolling(client, currentArtifact.id, panel, scope, deps, loadChildScopes);
        }
      },
      onOpenChildScope: async (scopeType, scopePath) => {
        const nextScope: ScopeContext = { ...scope, scopeType, scopePath };
        if (scopeType === "file") nextScope.filePath = scopePath;
        if (scopeType === "symbol" && scopePath) {
          const [filePath, symbolName] = scopePath.split("#");
          nextScope.filePath = filePath;
          nextScope.symbolName = symbolName;
        }
        if (scopeType === "requirement" && scopePath) {
          nextScope.filePath = undefined;
          nextScope.symbolName = scopePath;
        }
        await openKnowledgeScopePanel(context, client, nextScope, deps);
      },
    },
    await loadChildScopes(),
  );

  if (currentArtifact.status === "GENERATING") {
    startKnowledgePolling(client, currentArtifact.id, panel, scope, deps, loadChildScopes);
  }
}

function startKnowledgePolling(
  client: SourceBridgeClient,
  artifactId: string,
  panel: vscode.WebviewPanel,
  scope: ScopeContext,
  deps: CommandDependencies,
  loadChildScopes: () => Promise<Array<{ label: string; scopeType: ScopeType; scopePath?: string }>>,
): void {
  let cancelled = false;
  panel.onDidDispose(() => {
    cancelled = true;
  });
  const poll = async () => {
    if (cancelled) return;
    const latest = await client.getKnowledgeArtifact(artifactId);
    if (!latest) return;
    updateKnowledgePanel(panel, latest, scope, await loadChildScopes());
    deps.knowledgeTree?.refresh();
    if (latest.status === "GENERATING") {
      setTimeout(() => void poll(), 4000);
    }
  };
  setTimeout(() => void poll(), 4000);
}
