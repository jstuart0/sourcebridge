// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Repository-level commands: pick another repo for the workspace
 * folder, and open the legacy impact report webview (superseded by
 * the Change Risk tree view but kept for backwards compatibility).
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { createImpactPanel } from "../panels/impactPanel";
import { getCurrentWorkspaceFolder, switchRepository } from "../context/repositories";
import { openWorkspaceLocation } from "../panels/utils";
import {
  CommandDependencies,
  ensureImpactReports,
  ensureServer,
  resolveWorkspaceRepository,
} from "./common";

export function registerRepositoryCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  deps: CommandDependencies,
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.switchRepository", async () => {
      if (!(await ensureServer(client))) return;
      const workspaceFolder = getCurrentWorkspaceFolder();
      if (!workspaceFolder) {
        vscode.window.showErrorMessage("No workspace folder open.");
        return;
      }
      const repository = await switchRepository(client, context, workspaceFolder);
      if (!repository) return;
      vscode.window.showInformationMessage(
        `Using repository ${repository.name} for ${workspaceFolder.name}`,
      );
      deps.requirementsTree?.refresh();
      deps.knowledgeTree?.refresh();
      client.clearCaches();
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.showImpactReport", async () => {
      if (!(await ensureServer(client)) || !(await ensureImpactReports(client))) return;
      const repoCtx = await resolveWorkspaceRepository(context, client);
      if (!repoCtx) return;
      const report = await client.getLatestImpactReport(repoCtx.repository.id);
      if (!report) {
        vscode.window.showInformationMessage("No impact report available for this repository.");
        return;
      }
      createImpactPanel(report, async (filePath) => {
        await openWorkspaceLocation(repoCtx.workspaceFolder, filePath);
      });
    }),
  );
}
