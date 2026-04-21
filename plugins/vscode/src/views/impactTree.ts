// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Change-Risk sidebar tree.
 *
 * Groups the latest impact report into three nodes:
 *   - "Changed Files (N)" — expands to each file with status + line
 *     delta, clicking opens the file.
 *   - "Affected Requirements (N)" — expands to each requirement,
 *     clicking opens the detail panel.
 *   - "Stale Field Guides (N)" — expands to scope paths whose cached
 *     knowledge artifact is stale.
 *
 * The empty state prompts the user to run or refresh the report. A
 * top-level refresh button (view/title) triggers a refetch.
 */

import * as vscode from "vscode";
import { SourceBridgeClient, ImpactReport } from "../graphql/client";
import { getCurrentWorkspaceFolder, resolveRepository } from "../context/repositories";
import * as log from "../logging";

type Node = GroupNode | ChangedFileNode | AffectedRequirementNode | StaleArtifactNode | EmptyNode;

class GroupNode extends vscode.TreeItem {
  constructor(
    public readonly kind: "changed" | "affected" | "stale",
    label: string,
    public readonly children: (ChangedFileNode | AffectedRequirementNode | StaleArtifactNode)[],
  ) {
    super(label, vscode.TreeItemCollapsibleState.Expanded);
    this.description = `${children.length}`;
    this.contextValue = `impact-group-${kind}`;
    this.iconPath = new vscode.ThemeIcon(
      kind === "changed" ? "git-commit" : kind === "affected" ? "checklist" : "history",
    );
  }
}

class ChangedFileNode extends vscode.TreeItem {
  constructor(public readonly file: ImpactReport["filesChanged"][number]) {
    super(file.path, vscode.TreeItemCollapsibleState.None);
    this.description = `${file.status} · +${file.additions} / -${file.deletions}`;
    this.contextValue = "impact-file";
    this.iconPath = new vscode.ThemeIcon(iconForStatus(file.status));
    this.tooltip = new vscode.MarkdownString(
      `**${file.path}**\n\nStatus: ${file.status}\n\nAdditions: ${file.additions} · Deletions: ${file.deletions}`,
    );
    this.command = {
      command: "sourcebridge.impact.openFile",
      title: "Open File",
      arguments: [file.path],
    };
  }
}

class AffectedRequirementNode extends vscode.TreeItem {
  constructor(public readonly requirement: ImpactReport["affectedRequirements"][number]) {
    super(requirement.externalId || requirement.requirementId, vscode.TreeItemCollapsibleState.None);
    this.description = `${requirement.title} — ${requirement.affectedLinks}/${requirement.totalLinks} links`;
    this.contextValue = "impact-requirement";
    this.iconPath = new vscode.ThemeIcon("link");
    this.tooltip = new vscode.MarkdownString(
      `**${requirement.externalId || requirement.requirementId}**\n\n${requirement.title}\n\n` +
        `${requirement.affectedLinks} of ${requirement.totalLinks} links touch changed code.`,
    );
    this.command = {
      command: "sourcebridge.showRequirementDetail",
      title: "Open Requirement",
      arguments: [requirement.requirementId],
    };
  }
}

class StaleArtifactNode extends vscode.TreeItem {
  constructor(public readonly scopePath: string) {
    super(scopePath, vscode.TreeItemCollapsibleState.None);
    this.iconPath = new vscode.ThemeIcon("warning");
    this.contextValue = "impact-stale";
  }
}

class EmptyNode extends vscode.TreeItem {
  constructor(label: string) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.iconPath = new vscode.ThemeIcon("info");
  }
}

function iconForStatus(status: string): string {
  switch (status.toLowerCase()) {
    case "added":
    case "a":
      return "diff-added";
    case "removed":
    case "deleted":
    case "d":
      return "diff-removed";
    case "renamed":
    case "r":
      return "diff-renamed";
    default:
      return "diff-modified";
  }
}

export class ImpactTreeProvider implements vscode.TreeDataProvider<Node> {
  private _onDidChangeTreeData = new vscode.EventEmitter<Node | undefined>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;
  private report: ImpactReport | null = null;
  private loaded = false;

  constructor(
    private readonly client: SourceBridgeClient,
    private readonly context?: vscode.ExtensionContext,
  ) {}

  refresh(): void {
    this.loaded = false;
    this._onDidChangeTreeData.fire(undefined);
  }

  getTreeItem(element: Node): vscode.TreeItem {
    return element;
  }

  async getChildren(element?: Node): Promise<Node[]> {
    if (element instanceof GroupNode) {
      return element.children;
    }
    if (element) return [];

    if (!this.loaded) {
      await this.load();
    }

    if (!this.report) {
      return [new EmptyNode("No impact report yet. Push a commit to compute one.")];
    }

    const groups: Node[] = [];
    if (this.report.filesChanged.length > 0) {
      groups.push(
        new GroupNode(
          "changed",
          "Changed Files",
          this.report.filesChanged.map((f) => new ChangedFileNode(f)),
        ),
      );
    }
    if (this.report.affectedRequirements.length > 0) {
      groups.push(
        new GroupNode(
          "affected",
          "Affected Requirements",
          this.report.affectedRequirements.map((r) => new AffectedRequirementNode(r)),
        ),
      );
    }
    if (this.report.staleArtifacts && this.report.staleArtifacts.length > 0) {
      groups.push(
        new GroupNode(
          "stale",
          "Stale Field Guides",
          this.report.staleArtifacts.map((s) => new StaleArtifactNode(s)),
        ),
      );
    }

    if (groups.length === 0) {
      return [new EmptyNode("No impact for this commit. Everything is in sync.")];
    }

    return groups;
  }

  private async load(): Promise<void> {
    this.loaded = true;
    try {
      const workspaceFolder = getCurrentWorkspaceFolder();
      if (!workspaceFolder) return;
      const repo = await resolveRepository(this.client, workspaceFolder, this.context);
      if (!repo) return;
      this.report = await this.client.getLatestImpactReport(repo.id);
    } catch (err) {
      log.warn("impactTree", `load failed: ${(err as Error).message}`);
      this.report = null;
    }
  }
}
