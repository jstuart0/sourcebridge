import * as vscode from "vscode";
import {
  SourceBridgeClient,
  KnowledgeArtifact,
  ScopeChild,
} from "../graphql/client";
import {
  ScopeContext,
  createRepositoryScope,
  fromGraphQLScopeType,
  getScopeLabel,
  toGraphQLScopeType,
} from "../context/scope";
import { getCurrentWorkspaceFolder, resolveRepository } from "../context/repositories";
import { getCapabilities } from "../context/capabilities";
import * as log from "../logging";

class PlaceholderItem extends vscode.TreeItem {
  constructor(label: string, description?: string) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.description = description;
    this.contextValue = "placeholder";
  }
}

class ScopeItem extends vscode.TreeItem {
  constructor(public readonly scope: ScopeContext, hasArtifact = false) {
    super(getScopeLabel(scope), vscode.TreeItemCollapsibleState.Collapsed);
    this.contextValue = "knowledgeScope";
    this.description = hasArtifact ? "cached" : undefined;
  }
}

class ActionItem extends vscode.TreeItem {
  constructor(label: string, scope: ScopeContext, artifact?: KnowledgeArtifact) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.contextValue = "knowledgeAction";
    this.command = {
      command: "sourcebridge.openKnowledgeScope",
      title: "Open Knowledge Scope",
      arguments: artifact ? [scope, artifact] : [scope],
    };
  }
}

class ArtifactItem extends vscode.TreeItem {
  // Leaf node, not expandable. A previous version used
  // Collapsed + children = sections, but VS Code treats single click
  // on a collapsible row as "expand", so the `command` here never
  // fired. The full section list already lives in the field guide
  // panel; showing it twice in the tree was redundant anyway.
  //
  // Failed / generating artifacts still render so the user can see
  // something is in-flight or busted, but we mark them with an
  // appropriate icon + description and disable the open-panel command
  // on failed entries. Clicking a FAILED row opens the regenerate
  // flow instead.
  constructor(public readonly scope: ScopeContext, public readonly artifact: KnowledgeArtifact) {
    super(artifactLabel(artifact), vscode.TreeItemCollapsibleState.None);
    const status = (artifact.status || "").toUpperCase();
    this.description = status.toLowerCase() + (artifact.stale ? " · stale" : "");
    this.tooltip = `${artifactLabel(artifact)} · ${artifact.audience}/${artifact.depth} · ${status}`;
    if (status === "FAILED") {
      this.contextValue = "knowledgeArtifactFailed";
      this.iconPath = new vscode.ThemeIcon(
        "error",
        new vscode.ThemeColor("errorForeground"),
      );
      // Clicking a failed row opens the scope so the user can hit
      // "Regenerate" — we don't hand them a dead panel.
      this.command = {
        command: "sourcebridge.openKnowledgeScope",
        title: "Open Failed Artifact",
        arguments: [scope],
      };
    } else if (status === "GENERATING") {
      this.contextValue = "knowledgeArtifactGenerating";
      this.iconPath = new vscode.ThemeIcon("sync~spin");
      this.command = {
        command: "sourcebridge.openKnowledgeScope",
        title: "Open Knowledge Artifact",
        arguments: [scope, artifact],
      };
    } else {
      this.contextValue = "knowledgeArtifact";
      this.iconPath = new vscode.ThemeIcon(
        artifact.stale ? "warning" : "book",
        artifact.stale ? new vscode.ThemeColor("editorWarning.foreground") : undefined,
      );
      this.command = {
        command: "sourcebridge.openKnowledgeScope",
        title: "Open Knowledge Artifact",
        arguments: [scope, artifact],
      };
    }
  }
}

class SectionItem extends vscode.TreeItem {
  constructor(title: string, confidence: string) {
    super(title, vscode.TreeItemCollapsibleState.None);
    this.description = confidence.toLowerCase();
    this.contextValue = "knowledgeSection";
  }
}

type KnowledgeTreeItem = PlaceholderItem | ScopeItem | ArtifactItem | SectionItem | ActionItem;

function artifactLabel(artifact: KnowledgeArtifact): string {
  switch (artifact.type) {
    case "cliff_notes":
      return "Field Guide";
    case "learning_path":
      return "Learning Path";
    case "code_tour":
      return "Code Tour";
    default:
      return artifact.type;
  }
}

export class KnowledgeTreeProvider implements vscode.TreeDataProvider<KnowledgeTreeItem> {
  private _onDidChangeTreeData = new vscode.EventEmitter<KnowledgeTreeItem | undefined>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;
  private audience = "DEVELOPER";
  private depth = "MEDIUM";

  constructor(
    private client: SourceBridgeClient,
    private context?: vscode.ExtensionContext
  ) {}

  refresh(): void {
    this._onDidChangeTreeData.fire(undefined);
  }

  setLens(audience: string, depth: string): void {
    this.audience = audience;
    this.depth = depth;
    this.refresh();
  }

  getLens(): { audience: string; depth: string } {
    return { audience: this.audience, depth: this.depth };
  }

  getTreeItem(element: KnowledgeTreeItem): vscode.TreeItem {
    return element;
  }

  async getChildren(element?: KnowledgeTreeItem): Promise<KnowledgeTreeItem[]> {
    try {
      if (!element) {
        const workspaceFolder = getCurrentWorkspaceFolder();
        if (!workspaceFolder) {
          return [new PlaceholderItem("No workspace folder", "Open a repository workspace")];
        }
        const repository = await resolveRepository(this.client, workspaceFolder, this.context);
        if (!repository) {
          return [new PlaceholderItem("No repository selected")];
        }
        const scope = createRepositoryScope(repository, workspaceFolder);
        const hasArtifact =
          (
            await this.client.getKnowledgeArtifacts(
              repository.id,
              toGraphQLScopeType(scope.scopeType),
              scope.scopePath
            )
          ).length > 0;
        return [new ScopeItem(scope, hasArtifact)];
      }

      if (element instanceof ScopeItem) {
        return this.getScopeChildren(element.scope);
      }

      // ArtifactItem is intentionally a leaf (see class comment).
      // Sections live in the panel, not the tree.
      return [];
    } catch (err) {
      log.error("knowledgeTree", "getChildren failed", err);
      return [new PlaceholderItem("Field Guide unavailable", "Check server connectivity")];
    }
  }

  private async getScopeChildren(scope: ScopeContext): Promise<KnowledgeTreeItem[]> {
    const items: KnowledgeTreeItem[] = [];
    const graphScopeType = toGraphQLScopeType(scope.scopeType);
    const artifacts = await this.client.getKnowledgeArtifacts(
      scope.repositoryId,
      graphScopeType,
      scope.scopePath
    );
    const ready = artifacts.filter((a) => (a.status || "").toUpperCase() === "READY");
    if (ready.length > 0) {
      items.push(new ActionItem("Open current guide", scope, this.findBestArtifact(ready)));
    } else {
      items.push(new ActionItem("Generate Field Guide", scope));
    }
    for (const artifact of artifacts) {
      items.push(new ArtifactItem(scope, artifact));
    }

    const capabilities = await getCapabilities(this.client);
    if (!capabilities.scopedKnowledge) {
      if (items.length === 0) {
        items.push(new PlaceholderItem("No field guide yet", "Use commands to generate one"));
      }
      return items;
    }

    const scopeChildren = await this.client.getKnowledgeScopeChildren(
      scope.repositoryId,
      graphScopeType,
      scope.scopePath || "",
      this.audience,
      this.depth
    );
    for (const child of scopeChildren) {
      items.push(new ScopeItem(this.childScope(scope, child), child.hasArtifact));
    }

    if (items.length === 0) {
      items.push(new PlaceholderItem("No cached field guide", "Generate a field guide for this scope"));
    }
    return items;
  }

  private childScope(parent: ScopeContext, child: ScopeChild): ScopeContext {
    const scopeType = fromGraphQLScopeType(child.scopeType);
    const scope: ScopeContext = {
      repositoryId: parent.repositoryId,
      repositoryName: parent.repositoryName,
      workspaceFolder: parent.workspaceFolder,
      scopeType,
      scopePath: child.scopePath,
    };
    if (scopeType === "file") {
      scope.filePath = child.scopePath;
    }
    if (scopeType === "symbol") {
      const [filePath, symbolName] = child.scopePath.split("#");
      scope.filePath = filePath;
      scope.symbolName = symbolName;
    }
    return scope;
  }

  private findBestArtifact(artifacts: KnowledgeArtifact[]): KnowledgeArtifact {
    return (
      artifacts.find(
        (artifact) =>
          artifact.type === "cliff_notes" &&
          artifact.audience === this.audience &&
          artifact.depth === this.depth
      ) ||
      artifacts.find((artifact) => artifact.type === "cliff_notes") ||
      artifacts[0]
    );
  }
}

export function isKnowledgeScopeContext(value: unknown): value is ScopeContext {
  return !!value && typeof value === "object" && "repositoryId" in value && "scopeType" in value;
}

export function isKnowledgeArtifact(value: unknown): value is KnowledgeArtifact {
  return !!value && typeof value === "object" && "type" in value && "sections" in value;
}
