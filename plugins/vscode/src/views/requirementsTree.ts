// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Requirements tree view (v2).
 *
 * Renders the repository's requirements in the sidebar with:
 *   - Grouping by priority (optional; falls back to flat when no
 *     priorities are set so the view isn't awkwardly empty).
 *   - Click-to-open — selecting a requirement opens the detail panel
 *     via `sourcebridge.showRequirementDetail`.
 *   - Inline QuickFix actions: edit + delete (context menu in
 *     package.json "view/item/context").
 *   - Search/filter — `sourcebridge.filterRequirements` sets a
 *     substring filter that matches title, externalId, description.
 *   - Top-level "Create Requirement" entry surfaced as the first row
 *     when the list is empty, doubling as the onboarding cue.
 */

import * as vscode from "vscode";
import { SourceBridgeClient, Requirement } from "../graphql/client";
import { REQUIREMENTS } from "../graphql/queries";
import { getCurrentWorkspaceFolder, resolveRepository } from "../context/repositories";
import * as log from "../logging";

interface RequirementsResponse {
  requirements: { nodes: Requirement[]; totalCount: number };
}

type TreeNode = RequirementItem | GroupItem | EmptyStateItem;

export class RequirementItem extends vscode.TreeItem {
  constructor(public readonly requirement: Requirement) {
    const label = requirement.externalId || requirement.title;
    super(label, vscode.TreeItemCollapsibleState.None);
    this.description = requirement.externalId ? requirement.title : undefined;
    this.tooltip = new vscode.MarkdownString(
      `**${label}**\n\n${requirement.title}\n\n` +
        (requirement.description ? `${requirement.description}\n\n` : "") +
        (requirement.priority ? `Priority: ${requirement.priority}` : ""),
    );
    this.contextValue = "requirement";
    this.command = {
      command: "sourcebridge.showRequirementDetail",
      title: "Open Requirement",
      arguments: [requirement.id],
    };
    this.iconPath = new vscode.ThemeIcon(iconForPriority(requirement.priority));
  }
}

class GroupItem extends vscode.TreeItem {
  constructor(
    public readonly key: string,
    label: string,
    public readonly children: RequirementItem[],
  ) {
    super(label, vscode.TreeItemCollapsibleState.Expanded);
    this.contextValue = "requirements-group";
    this.description = `${children.length}`;
    this.iconPath = new vscode.ThemeIcon("list-unordered");
  }
}

class EmptyStateItem extends vscode.TreeItem {
  constructor(label: string, commandId?: string) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.contextValue = "empty-state";
    this.iconPath = new vscode.ThemeIcon("add");
    if (commandId) {
      this.command = {
        command: commandId,
        title: label,
      };
    }
  }
}

function iconForPriority(priority: string | null | undefined): string {
  switch ((priority || "").toLowerCase()) {
    case "critical":
      return "error";
    case "high":
      return "warning";
    case "medium":
      return "primitive-square";
    case "low":
      return "circle-outline";
    default:
      return "checklist";
  }
}

export class RequirementsTreeProvider implements vscode.TreeDataProvider<TreeNode> {
  private _onDidChangeTreeData = new vscode.EventEmitter<TreeNode | undefined>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private filterText = "";
  private groupByPriority = true;

  constructor(
    private client: SourceBridgeClient,
    private context?: vscode.ExtensionContext,
  ) {}

  refresh(): void {
    this._onDidChangeTreeData.fire(undefined);
  }

  setFilter(text: string): void {
    this.filterText = text.trim().toLowerCase();
    this.refresh();
  }

  clearFilter(): void {
    this.setFilter("");
  }

  toggleGrouping(): void {
    this.groupByPriority = !this.groupByPriority;
    this.refresh();
  }

  getTreeItem(element: TreeNode): vscode.TreeItem {
    return element;
  }

  async getChildren(element?: TreeNode): Promise<TreeNode[]> {
    if (element instanceof GroupItem) {
      return element.children;
    }
    if (element) {
      return [];
    }

    try {
      const workspaceFolder = getCurrentWorkspaceFolder();
      if (!workspaceFolder) {
        log.debug("requirementsTree", "No workspace folder");
        return [];
      }
      const repo = await resolveRepository(this.client, workspaceFolder, this.context);
      if (!repo) {
        log.debug("requirementsTree", "No repository resolved");
        return [];
      }

      log.debug("requirementsTree", `Fetching requirements for repo ${repo.name}`);
      const reqsData = await this.client.query<RequirementsResponse>(REQUIREMENTS, {
        repositoryId: repo.id,
        limit: 500,
        offset: 0,
      });
      log.debug(
        "requirementsTree",
        `Got ${reqsData.requirements.nodes.length} of ${reqsData.requirements.totalCount} requirements`,
      );

      const matches = this.filterText
        ? reqsData.requirements.nodes.filter((r) => matchesFilter(r, this.filterText))
        : reqsData.requirements.nodes;

      if (matches.length === 0) {
        if (this.filterText) {
          return [new EmptyStateItem(`No matches for "${this.filterText}"`)];
        }
        return [new EmptyStateItem("Create Requirement", "sourcebridge.createRequirement")];
      }

      if (!this.groupByPriority) {
        return matches.map((r) => new RequirementItem(r));
      }

      return groupByPriority(matches);
    } catch (err) {
      log.error("requirementsTree", "Failed to load requirements", err);
      return [];
    }
  }
}

function matchesFilter(requirement: Requirement, needle: string): boolean {
  const hay = [
    requirement.externalId || "",
    requirement.title || "",
    requirement.description || "",
    requirement.priority || "",
    ...(requirement.tags || []),
  ]
    .join(" ")
    .toLowerCase();
  return hay.includes(needle);
}

function groupByPriority(requirements: Requirement[]): TreeNode[] {
  const order: Array<{ key: string; label: string }> = [
    { key: "critical", label: "Critical" },
    { key: "high", label: "High" },
    { key: "medium", label: "Medium" },
    { key: "low", label: "Low" },
    { key: "", label: "Unprioritized" },
  ];
  const buckets = new Map<string, RequirementItem[]>();
  for (const { key } of order) {
    buckets.set(key, []);
  }
  for (const r of requirements) {
    const key = (r.priority || "").toLowerCase();
    const bucket = buckets.get(key) || buckets.get("");
    bucket!.push(new RequirementItem(r));
  }
  const groups: TreeNode[] = [];
  for (const { key, label } of order) {
    const children = buckets.get(key) || [];
    if (children.length === 0) continue;
    groups.push(new GroupItem(key, label, children));
  }
  // Degenerate case: everything lives in a single "Unprioritized"
  // bucket — flatten rather than show a pointless group.
  if (groups.length === 1 && groups[0] instanceof GroupItem && groups[0].key === "") {
    return groups[0].children;
  }
  return groups;
}
