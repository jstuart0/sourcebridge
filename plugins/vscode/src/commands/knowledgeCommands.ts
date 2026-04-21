// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Knowledge (field guide) commands: cliff notes at repo / file /
 * symbol scope, learning path + code tour generation, the knowledge
 * scope opener that the tree view delegates into, and the lens /
 * refresh / focus helpers.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import {
  isKnowledgeArtifact,
  isKnowledgeScopeContext,
} from "../views/knowledgeTree";
import {
  createFileScope,
  createRepositoryScope,
  inferSymbolScope,
  toGraphQLScopeType,
} from "../context/scope";
import { toRelativePosixPath } from "../context/repositories";
import {
  CommandDependencies,
  ensureScopedKnowledge,
  ensureServer,
  getActiveEditorContext,
  openKnowledgeScopePanel,
  resolveEditorRepository,
  resolveWorkspaceRepository,
} from "./common";

export function registerKnowledgeCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  deps: CommandDependencies,
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateCliffNotes", async () => {
      if (!(await ensureServer(client))) return;
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) return;
      const scope = createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder);
      const artifact = await client.generateCliffNotes(repoCtx.repository.id);
      await openKnowledgeScopePanel(context, client, scope, deps, artifact);
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateCliffNotesForFile", async () => {
      const active = getActiveEditorContext();
      if (!active) return;
      if (!(await ensureServer(client)) || !(await ensureScopedKnowledge(client))) return;
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) return;
      const scope = createFileScope(
        editorRepo.repository,
        editorRepo.workspaceFolder,
        toRelativePosixPath(editorRepo.editor.document.uri.fsPath, editorRepo.workspaceFolder),
      );
      const artifact = await client.generateCliffNotes(
        scope.repositoryId,
        undefined,
        undefined,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath,
      );
      await openKnowledgeScopePanel(context, client, scope, deps, artifact);
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateCliffNotesForSymbol", async () => {
      const active = getActiveEditorContext();
      if (!active) return;
      if (!(await ensureServer(client)) || !(await ensureScopedKnowledge(client))) return;
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) return;
      const scope = await inferSymbolScope(
        client,
        editorRepo.repository,
        editorRepo.workspaceFolder,
        editorRepo.editor,
      );
      const artifact = await client.generateCliffNotes(
        scope.repositoryId,
        undefined,
        undefined,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath,
      );
      await openKnowledgeScopePanel(context, client, scope, deps, artifact);
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateLearningPath", async () => {
      if (!(await ensureServer(client))) return;
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) return;
      const artifact = await client.generateLearningPath(repoCtx.repository.id);
      await openKnowledgeScopePanel(
        context,
        client,
        createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder),
        deps,
        artifact,
      );
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.generateCodeTour", async () => {
      if (!(await ensureServer(client))) return;
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) return;
      const artifact = await client.generateCodeTour(repoCtx.repository.id);
      await openKnowledgeScopePanel(
        context,
        client,
        createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder),
        deps,
        artifact,
      );
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.openKnowledgeScope",
      async (arg1?: unknown, arg2?: unknown) => {
        if (!(await ensureServer(client))) return;
        const scope = isKnowledgeScopeContext(arg1) ? arg1 : undefined;
        const artifact = isKnowledgeArtifact(arg2)
          ? arg2
          : isKnowledgeArtifact(arg1)
            ? arg1
            : undefined;
        if (!scope) {
          const repoCtx = await resolveWorkspaceRepository(context, client);
          if (!repoCtx) return;
          await openKnowledgeScopePanel(
            context,
            client,
            createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder),
            deps,
            artifact,
          );
          return;
        }
        await openKnowledgeScopePanel(context, client, scope, deps, artifact);
      },
    ),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.showKnowledge", async () => {
      if (!(await ensureServer(client))) return;
      await vscode.commands.executeCommand("sourcebridge.knowledge.focus");
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.refreshKnowledge", async () => {
      deps.knowledgeTree?.refresh();
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.setKnowledgeLens", async () => {
      const audience = await vscode.window.showQuickPick(["DEVELOPER", "BEGINNER"], {
        placeHolder: "Knowledge audience",
      });
      if (!audience) return;
      const depth = await vscode.window.showQuickPick(["SUMMARY", "MEDIUM", "DEEP"], {
        placeHolder: "Knowledge depth",
      });
      if (!depth) return;
      deps.knowledgeTree?.setLens(audience, depth);
    }),
  );
}
