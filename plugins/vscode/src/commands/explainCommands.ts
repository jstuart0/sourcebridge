// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Explain commands — the synchronous "give me a one-shot explanation"
 * flow backed by the `explainSystem` GraphQL mutation. Supports three
 * scopes (repo / file / symbol).
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { createExplainPanel } from "../panels/knowledgePanel";
import {
  createRepositoryScope,
  inferFileScope,
  inferSymbolScope,
  toGraphQLScopeType,
} from "../context/scope";
import {
  classifyError,
  ensureScopedExplain,
  ensureServer,
  getActiveEditorContext,
  resolveEditorRepository,
  resolveWorkspaceRepository,
} from "./common";

export function registerExplainCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.explainSystem", async () => {
      if (!(await ensureServer(client))) return;
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) return;
      const question = await vscode.window.showInputBox({
        prompt: "Ask a question about this codebase",
        placeHolder: "e.g., How does the authentication flow work?",
      });
      if (!question) return;
      try {
        const result = await client.explainSystem(repoCtx.repository.id, question);
        createExplainPanel(
          question,
          result.explanation,
          createRepositoryScope(repoCtx.repository, repoCtx.workspaceFolder),
        );
      } catch (err) {
        vscode.window.showErrorMessage(`System explanation failed: ${classifyError(err)}`);
      }
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.explainFile", async () => {
      const active = getActiveEditorContext();
      if (!active) return;
      if (!(await ensureServer(client)) || !(await ensureScopedExplain(client))) return;
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) return;
      const scope = await inferFileScope(
        editorRepo.repository,
        editorRepo.workspaceFolder,
        editorRepo.editor,
      );
      const question = await vscode.window.showInputBox({
        prompt: "Ask a question about this file",
        placeHolder: "e.g., What responsibilities live here?",
      });
      if (!question) return;
      const result = await client.explainSystem(
        scope.repositoryId,
        question,
        undefined,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath,
      );
      createExplainPanel(question, result.explanation, scope);
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.explainSymbol", async () => {
      const active = getActiveEditorContext();
      if (!active) return;
      if (!(await ensureServer(client)) || !(await ensureScopedExplain(client))) return;
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) return;
      const scope = await inferSymbolScope(
        client,
        editorRepo.repository,
        editorRepo.workspaceFolder,
        editorRepo.editor,
      );
      const question = await vscode.window.showInputBox({
        prompt: "Ask a question about this symbol",
        placeHolder: "e.g., How is this symbol used?",
      });
      if (!question) return;
      const result = await client.explainSystem(
        scope.repositoryId,
        question,
        undefined,
        toGraphQLScopeType(scope.scopeType),
        scope.scopePath,
      );
      createExplainPanel(question, result.explanation, scope);
    }),
  );
}
