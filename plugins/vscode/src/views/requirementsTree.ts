import * as vscode from "vscode";
import { SourceBridgeClient } from "../graphql/client";
import { REQUIREMENTS } from "../graphql/queries";
import { getCurrentWorkspaceFolder, resolveRepository } from "../context/repositories";

interface Requirement {
  id: string;
  externalId: string;
  title: string;
  priority: string;
}

interface RequirementsResponse {
  requirements: { nodes: Requirement[]; totalCount: number };
}

class RequirementItem extends vscode.TreeItem {
  constructor(
    public readonly requirement: Requirement
  ) {
    super(requirement.externalId, vscode.TreeItemCollapsibleState.None);
    this.description = requirement.title;
    this.tooltip = `${requirement.externalId}: ${requirement.title}\nPriority: ${requirement.priority}`;
    this.contextValue = "requirement";
  }
}

export class RequirementsTreeProvider implements vscode.TreeDataProvider<RequirementItem> {
  private _onDidChangeTreeData = new vscode.EventEmitter<RequirementItem | undefined>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  constructor(
    private client: SourceBridgeClient,
    private context?: vscode.ExtensionContext
  ) {}

  refresh(): void {
    this._onDidChangeTreeData.fire(undefined);
  }

  getTreeItem(element: RequirementItem): vscode.TreeItem {
    return element;
  }

  async getChildren(): Promise<RequirementItem[]> {
    try {
      const workspaceFolder = getCurrentWorkspaceFolder();
      if (!workspaceFolder) return [];
      const repo = await resolveRepository(this.client, workspaceFolder, this.context);
      if (!repo) return [];

      const reqsData = await this.client.query<RequirementsResponse>(REQUIREMENTS, {
        repositoryId: repo.id,
        limit: 100,
        offset: 0,
      });

      return reqsData.requirements.nodes.map((r) => new RequirementItem(r));
    } catch {
      return [];
    }
  }
}
