// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * CRUD commands for requirements, split out from the legacy
 * `register.ts` monolith.
 *
 * Exposes six commands:
 *
 *   • `sourcebridge.createRequirement` — top-level "new requirement"
 *     flow, not tied to a symbol. Launches a chain of input boxes for
 *     title → description → priority.
 *   • `sourcebridge.createRequirementFromSymbol` — pre-fills the create
 *     form with the symbol name / file hint and, on success, creates a
 *     manual link between the new requirement and the symbol.
 *   • `sourcebridge.linkRequirementToSymbol` — picks an existing
 *     requirement via QuickPick and links it to the given symbol.
 *   • `sourcebridge.editRequirement` — edits title / description /
 *     priority on an existing requirement.
 *   • `sourcebridge.deleteRequirement` — moves the requirement to the
 *     recycle bin after confirmation.
 *   • `sourcebridge.unlinkRequirementLink` — unlinks a single
 *     requirement-link from a symbol.
 *
 * All commands share the same request → classify → toast error funnel
 * and refresh the requirements tree + decorator on success so the UI
 * reflects changes without a reload.
 */

import * as vscode from "vscode";
import {
  SourceBridgeClient,
  Requirement,
  RequirementLink,
} from "../graphql/client";
import { REQUIREMENTS } from "../graphql/queries";
import {
  getCurrentWorkspaceFolder,
  resolveRepository,
} from "../context/repositories";
import { RequirementsTreeProvider } from "../views/requirementsTree";
import { RequirementDecorator } from "../providers/decorator";
import * as log from "../logging";

interface CrudDeps {
  requirementsTree?: RequirementsTreeProvider;
  decorator?: RequirementDecorator;
}

/**
 * Translate transport / GraphQL errors into user-readable copy. Kept
 * local to this module so split-out commands don't have to depend on
 * the register.ts internals. Mirrors the helper there.
 */
function classifyError(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  if (err && typeof err === "object" && (err as { kind?: string }).kind) {
    const kind = (err as { kind?: string }).kind;
    if (kind === "unauthenticated") return "Not signed in. Run SourceBridge: Sign In.";
    if (kind === "forbidden") return "Action forbidden for this account.";
    if (kind === "not-found") return "The requested item no longer exists.";
    if (kind === "timeout") return "Request timed out. The server may be slow — try again.";
    if (kind === "offline" || kind === "network") return `SourceBridge server returned an error: ${msg}`;
    if (kind === "http") return `SourceBridge server returned an error: ${msg}`;
    if (kind === "graphql") return `SourceBridge server returned an error: ${msg}`;
  }
  return msg;
}

async function promptPriority(
  initial?: string | null,
): Promise<string | undefined | null> {
  const items: Array<vscode.QuickPickItem & { value: string | null }> = [
    { label: "(no priority)", value: null },
    { label: "low", value: "low" },
    { label: "medium", value: "medium" },
    { label: "high", value: "high" },
    { label: "critical", value: "critical" },
  ];
  const picked = await vscode.window.showQuickPick(items, {
    placeHolder: `Priority${initial ? ` (currently: ${initial})` : ""}`,
    ignoreFocusOut: true,
  });
  if (!picked) return undefined;
  return picked.value;
}

async function fetchAllRequirements(
  client: SourceBridgeClient,
  repoID: string,
): Promise<Requirement[]> {
  const data = await client.query<{
    requirements: { nodes: Requirement[]; totalCount: number };
  }>(REQUIREMENTS, { repositoryId: repoID, limit: 500, offset: 0 });
  return data.requirements.nodes;
}

/**
 * Resolve workspace → repository, toasting a helpful error if we can't.
 */
async function resolveRepoOrToast(
  client: SourceBridgeClient,
  context: vscode.ExtensionContext,
): Promise<
  | { repo: import("../graphql/client").Repository; workspaceFolder: vscode.WorkspaceFolder }
  | undefined
> {
  const workspaceFolder = getCurrentWorkspaceFolder();
  if (!workspaceFolder) {
    vscode.window.showErrorMessage("No workspace folder open.");
    return undefined;
  }
  const repo = await resolveRepository(client, workspaceFolder, context);
  if (!repo) return undefined;
  return { repo, workspaceFolder };
}

/**
 * Shared create flow. When a `symbolHint` is supplied we pre-fill the
 * title and default the external ID.
 */
async function runCreateFlow(
  client: SourceBridgeClient,
  repoID: string,
  symbolHint?: { name: string; filePath?: string; docComment?: string | null },
): Promise<Requirement | undefined> {
  const title = await vscode.window.showInputBox({
    prompt: "Requirement title",
    placeHolder: symbolHint
      ? `e.g., "${symbolHint.name} should…"`
      : "e.g., Users can reset their password",
    value: symbolHint ? `${symbolHint.name}: ` : undefined,
    ignoreFocusOut: true,
    validateInput: (v) => (v.trim().length === 0 ? "Title is required" : undefined),
  });
  if (!title) return undefined;

  const description = await vscode.window.showInputBox({
    prompt: "Description (optional)",
    placeHolder: symbolHint?.docComment?.trim().slice(0, 200) || "Leave empty to skip",
    value: symbolHint?.docComment?.trim() || "",
    ignoreFocusOut: true,
  });
  if (description === undefined) return undefined;

  const priority = await promptPriority();
  if (priority === undefined) return undefined;

  try {
    const requirement = await client.createRequirement({
      repositoryId: repoID,
      title: title.trim(),
      description: description.trim() || null,
      priority,
    });
    vscode.window.showInformationMessage(
      `Created ${requirement.externalId || requirement.title}`,
    );
    return requirement;
  } catch (err) {
    vscode.window.showErrorMessage(`Create failed: ${classifyError(err)}`);
    return undefined;
  }
}

export function registerRequirementCrudCommands(
  context: vscode.ExtensionContext,
  client: SourceBridgeClient,
  deps: CrudDeps = {},
): void {
  // ---------------------------------------------------------------------------
  // createRequirement (top-level)
  // ---------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.createRequirement", async () => {
      log.info("command", "createRequirement invoked");
      const resolved = await resolveRepoOrToast(client, context);
      if (!resolved) return;
      const req = await runCreateFlow(client, resolved.repo.id);
      if (req) {
        deps.requirementsTree?.refresh();
      }
    }),
  );

  // ---------------------------------------------------------------------------
  // createRequirementFromSymbol
  // ---------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.createRequirementFromSymbol",
      async (
        symbolId: string,
        repoID: string,
        hint: { name: string; filePath?: string; startLine?: number; docComment?: string | null },
      ) => {
        log.info("command", `createRequirementFromSymbol (symbol=${symbolId})`);
        if (!symbolId || !repoID) {
          vscode.window.showErrorMessage("Missing symbol context.");
          return;
        }
        const req = await runCreateFlow(client, repoID, hint);
        if (!req) return;
        try {
          await client.createManualLink({
            repositoryId: repoID,
            requirementId: req.id,
            symbolId,
            rationale: `Created from ${hint.name}`,
          });
          deps.requirementsTree?.refresh();
          // Invalidate the decorator's per-symbol cache so the gutter
          // icon appears without requiring a save/reopen.
          void deps.decorator?.refresh();
        } catch (err) {
          vscode.window.showErrorMessage(
            `Requirement created but linking the symbol failed: ${classifyError(err)}`,
          );
        }
      },
    ),
  );

  // ---------------------------------------------------------------------------
  // linkRequirementToSymbol
  // ---------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.linkRequirementToSymbol",
      async (symbolId: string, repoID: string) => {
        log.info("command", `linkRequirementToSymbol (symbol=${symbolId})`);
        if (!symbolId || !repoID) {
          vscode.window.showErrorMessage("Missing symbol context.");
          return;
        }

        let requirements: Requirement[];
        try {
          requirements = await fetchAllRequirements(client, repoID);
        } catch (err) {
          vscode.window.showErrorMessage(`Failed to load requirements: ${classifyError(err)}`);
          return;
        }
        if (requirements.length === 0) {
          const choice = await vscode.window.showInformationMessage(
            "No requirements yet. Create one now?",
            "Create Requirement",
            "Cancel",
          );
          if (choice === "Create Requirement") {
            await vscode.commands.executeCommand(
              "sourcebridge.createRequirementFromSymbol",
              symbolId,
              repoID,
              { name: "" },
            );
          }
          return;
        }

        type Pick = vscode.QuickPickItem & { requirement: Requirement };
        const items: Pick[] = requirements.map((r) => ({
          label: r.externalId || r.title,
          description: r.title,
          detail: r.description,
          requirement: r,
        }));
        const picked = await vscode.window.showQuickPick(items, {
          placeHolder: "Select the requirement to link",
          matchOnDescription: true,
          matchOnDetail: true,
          ignoreFocusOut: true,
        });
        if (!picked) return;

        try {
          await client.createManualLink({
            repositoryId: repoID,
            requirementId: picked.requirement.id,
            symbolId,
            rationale: "Linked via VSCode code action",
          });
          vscode.window.showInformationMessage(
            `Linked to ${picked.requirement.externalId || picked.requirement.title}`,
          );
          deps.requirementsTree?.refresh();
          void deps.decorator?.refresh();
        } catch (err) {
          vscode.window.showErrorMessage(`Link failed: ${classifyError(err)}`);
        }
      },
    ),
  );

  // ---------------------------------------------------------------------------
  // editRequirement
  // ---------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.editRequirement",
      async (requirementOrId: Requirement | string) => {
        log.info("command", "editRequirement invoked");
        let requirement: Requirement | null;
        if (typeof requirementOrId === "string") {
          requirement = await client.getRequirement(requirementOrId);
        } else {
          requirement = requirementOrId;
        }
        if (!requirement) {
          vscode.window.showErrorMessage("Requirement not found.");
          return;
        }

        const field = await vscode.window.showQuickPick(
          [
            { label: "Title", value: "title" },
            { label: "Description", value: "description" },
            { label: "Priority", value: "priority" },
            { label: "External ID", value: "externalId" },
          ],
          { placeHolder: "Which field do you want to edit?", ignoreFocusOut: true },
        );
        if (!field) return;

        const patch: Parameters<typeof client.updateRequirementFields>[0] = {
          id: requirement.id,
        };
        if (field.value === "priority") {
          const pri = await promptPriority(requirement.priority);
          if (pri === undefined) return;
          patch.priority = pri;
        } else {
          const currentRaw = (requirement as unknown as Record<string, unknown>)[field.value];
          const current = typeof currentRaw === "string" ? currentRaw : "";
          const next = await vscode.window.showInputBox({
            prompt: `Edit ${field.label.toLowerCase()}`,
            value: current,
            ignoreFocusOut: true,
            validateInput: field.value === "title"
              ? (v) => (v.trim().length === 0 ? "Title is required" : undefined)
              : undefined,
          });
          if (next === undefined) return;
          (patch as unknown as Record<string, unknown>)[field.value] = next.trim() || null;
        }

        try {
          await client.updateRequirementFields(patch);
          vscode.window.showInformationMessage("Requirement updated.");
          deps.requirementsTree?.refresh();
        } catch (err) {
          vscode.window.showErrorMessage(`Update failed: ${classifyError(err)}`);
        }
      },
    ),
  );

  // ---------------------------------------------------------------------------
  // deleteRequirement (soft-delete / move to recycle bin)
  // ---------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.deleteRequirement",
      async (requirementOrId: Requirement | string) => {
        log.info("command", "deleteRequirement invoked");
        let requirement: Requirement | null;
        if (typeof requirementOrId === "string") {
          requirement = await client.getRequirement(requirementOrId);
        } else {
          requirement = requirementOrId;
        }
        if (!requirement) {
          vscode.window.showErrorMessage("Requirement not found.");
          return;
        }

        const confirm = await vscode.window.showWarningMessage(
          `Move "${requirement.externalId || requirement.title}" to the recycle bin? Links to symbols will also be deleted. You can restore it within 30 days.`,
          { modal: true },
          "Move to Recycle Bin",
        );
        if (confirm !== "Move to Recycle Bin") return;

        try {
          await client.moveToTrash("REQUIREMENT", requirement.id, "Deleted from VSCode");
          vscode.window.showInformationMessage(
            `Moved ${requirement.externalId || requirement.title} to the recycle bin.`,
          );
          deps.requirementsTree?.refresh();
          void deps.decorator?.refresh();
        } catch (err) {
          vscode.window.showErrorMessage(`Delete failed: ${classifyError(err)}`);
        }
      },
    ),
  );

  // ---------------------------------------------------------------------------
  // unlinkRequirementLink (remove a single symbol ↔ requirement binding)
  // ---------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "sourcebridge.unlinkRequirementLink",
      async (linkOrId: RequirementLink | string) => {
        const linkId = typeof linkOrId === "string" ? linkOrId : linkOrId.id;
        log.info("command", `unlinkRequirementLink (link=${linkId})`);
        const confirm = await vscode.window.showWarningMessage(
          "Remove this link? The requirement and symbol will both remain — only the link between them is removed.",
          { modal: true },
          "Remove Link",
        );
        if (confirm !== "Remove Link") return;
        try {
          await client.moveToTrash("REQUIREMENT_LINK", linkId, "Unlinked from VSCode");
          vscode.window.showInformationMessage("Link removed.");
          deps.requirementsTree?.refresh();
          void deps.decorator?.refresh();
        } catch (err) {
          vscode.window.showErrorMessage(`Unlink failed: ${classifyError(err)}`);
        }
      },
    ),
  );
}
