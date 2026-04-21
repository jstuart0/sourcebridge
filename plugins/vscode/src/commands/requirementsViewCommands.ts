// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Sidebar-centric commands for the requirements tree: focus, show
 * linked, show detail, filter / clear / group.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import {
  CommandDependencies,
  chooseRequirement,
  ensureServer,
  getActiveEditorContext,
  getCurrentSymbol,
  openRequirementDetail,
  resolveEditorRepository,
  resolveWorkspaceRepository,
} from "./common";
import * as log from "../logging";

export function registerRequirementsViewCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  deps: CommandDependencies,
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.showRequirements", async () => {
      if (!(await ensureServer(client))) return;
      await vscode.commands.executeCommand("sourcebridge.requirements.focus");
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.showLinkedRequirements",
      async (symbolId?: string) => {
        log.info("command", `showLinkedRequirements (symbolId=${symbolId || "from cursor"})`);
        const active = getActiveEditorContext();
        if (!active) return;
        if (!(await ensureServer(client))) return;
        const editorRepo = await resolveEditorRepository(context, client);
        if (!editorRepo) return;

        const effectiveSymbolId =
          symbolId ||
          (
            await getCurrentSymbol(
              client,
              editorRepo.repository,
              editorRepo.workspaceFolder,
              editorRepo.editor,
            )
          )?.id;
        if (!effectiveSymbolId) {
          vscode.window.showWarningMessage("No symbol selected at the current cursor position.");
          return;
        }

        const links = await client.getCodeToRequirements(effectiveSymbolId);
        const choice = await chooseRequirement(client, links);
        if (!choice) {
          vscode.window.showInformationMessage("No linked requirements found.");
          return;
        }

        await openRequirementDetail(
          context,
          client,
          choice.requirement,
          await client.getRequirementLinks(choice.requirement.id, 50),
          editorRepo,
          deps,
        );
      },
    ),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.showRequirementDetail",
      async (requirementId: string) => {
        if (!requirementId) return;
        if (!(await ensureServer(client))) return;
        const repoCtx = await resolveWorkspaceRepository(context, client);
        if (!repoCtx) return;
        const requirement = await client.getRequirement(requirementId);
        if (!requirement) {
          vscode.window.showErrorMessage("Requirement not found.");
          return;
        }
        const links = await client.getRequirementLinks(requirementId, 50);
        await openRequirementDetail(context, client, requirement, links, repoCtx, deps);
      },
    ),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.filterRequirements", async () => {
      const current = await vscode.window.showInputBox({
        prompt: "Filter requirements (title, external ID, description, tags)",
        placeHolder: "Leave blank to clear filter",
        ignoreFocusOut: true,
      });
      if (current === undefined) return;
      deps.requirementsTree?.setFilter(current);
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.clearRequirementsFilter", async () => {
      deps.requirementsTree?.clearFilter();
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.toggleRequirementsGrouping", async () => {
      deps.requirementsTree?.toggleGrouping();
    }),
  );
}
