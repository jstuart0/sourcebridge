// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Legacy discuss + run-review commands. New flows should use the
 * MCP-backed chat panel in askCommands.ts; these remain as the
 * "right-click > Discuss" entry points that pre-date 0.3.0.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { createDiscussionPanel, DiscussionData } from "../panels/discussionPanel";
import { createReviewPanel, ReviewData } from "../panels/reviewPanel";
import { openWorkspaceLocation } from "../panels/utils";
import { toRelativePosixPath } from "../context/repositories";
import {
  CommandDependencies,
  classifyError,
  ensureServer,
  getActiveEditorContext,
  openDiscussionReference,
  resolveEditorRepository,
} from "./common";
import * as log from "../logging";

export function registerReviewCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  deps: CommandDependencies,
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.discussCode", async () => {
      log.info("command", "discussCode invoked");
      const active = getActiveEditorContext();
      if (!active) return;
      if (!(await ensureServer(client))) return;
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) return;

      const question = await vscode.window.showInputBox({
        prompt: "Ask a question about this code",
        placeHolder: "e.g., What does this function do?",
      });
      if (!question) return;

      const selection = editorRepo.editor.selection;
      const code = editorRepo.editor.document.getText(
        selection.isEmpty ? undefined : selection,
      );
      const filePath = toRelativePosixPath(
        editorRepo.editor.document.uri.fsPath,
        editorRepo.workspaceFolder,
      );

      try {
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: "SourceBridge: discussing code...",
            cancellable: false,
          },
          async () => {
            const result = await client.discussCode(
              editorRepo.repository.id,
              question,
              filePath,
              code,
              editorRepo.editor.document.languageId,
            );
            const discussionData: DiscussionData = {
              question,
              answer: result.answer,
              references: result.references ?? [],
              sourceLabel: selection.isEmpty ? "Current editor contents" : "Current selection",
              sourceNote: editorRepo.editor.document.isDirty
                ? "Includes unsaved changes."
                : "Uses the code currently open in your editor.",
            };
            deps.discussionTree?.addDiscussion(question, result.answer);
            createDiscussionPanel(discussionData, async (reference) => {
              await openDiscussionReference(editorRepo.workspaceFolder, reference);
            });
          },
        );
      } catch (err) {
        vscode.window.showErrorMessage(`Code discussion failed: ${classifyError(err)}`);
      }
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.runReview", async () => {
      log.info("command", "runReview invoked");
      const active = getActiveEditorContext();
      if (!active) return;
      if (!(await ensureServer(client))) return;
      const editorRepo = await resolveEditorRepository(context, client);
      if (!editorRepo) return;

      const template = await vscode.window.showQuickPick(
        ["security", "solid", "performance", "reliability", "maintainability"],
        { placeHolder: "Select review template" },
      );
      if (!template) return;

      const filePath = toRelativePosixPath(
        editorRepo.editor.document.uri.fsPath,
        editorRepo.workspaceFolder,
      );
      const code = editorRepo.editor.document.getText();

      try {
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `SourceBridge: running ${template} review...`,
            cancellable: false,
          },
          async () => {
            const result = await client.reviewCode(
              editorRepo.repository.id,
              filePath,
              template,
              code,
              editorRepo.editor.document.languageId,
            );
            const reviewData: ReviewData = {
              template: result.template,
              score: result.score,
              findings: result.findings.map((f) => ({
                severity: f.severity,
                category: f.category,
                message: f.message,
                line: f.startLine,
                filePath: f.filePath || filePath,
                suggestion: f.suggestion,
              })),
              sourceLabel: "Current editor contents",
              sourceNote: editorRepo.editor.document.isDirty
                ? "Includes unsaved changes."
                : "Uses the code currently open in your editor.",
            };
            createReviewPanel(reviewData, async (finding) => {
              if (!finding.filePath) return;
              await openWorkspaceLocation(
                editorRepo.workspaceFolder,
                finding.filePath,
                finding.line,
              );
            });
          },
        );
      } catch (err) {
        vscode.window.showErrorMessage(`Code review failed: ${classifyError(err)}`);
      }
    }),
  );
}
