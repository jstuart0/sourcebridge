import * as vscode from "vscode";

class DiscussionItem extends vscode.TreeItem {
  constructor(label: string, description?: string) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.description = description;
  }
}

export class DiscussionTreeProvider implements vscode.TreeDataProvider<DiscussionItem> {
  private _onDidChangeTreeData = new vscode.EventEmitter<DiscussionItem | undefined>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;
  private discussions: Array<{ question: string; answer: string }> = [];

  refresh(): void {
    this._onDidChangeTreeData.fire(undefined);
  }

  addDiscussion(question: string, answer: string): void {
    this.discussions.unshift({ question, answer });
    this.refresh();
  }

  getTreeItem(element: DiscussionItem): vscode.TreeItem {
    return element;
  }

  async getChildren(): Promise<DiscussionItem[]> {
    if (this.discussions.length === 0) {
      return [new DiscussionItem("No discussions yet", "Use 'Discuss This Code' to start")];
    }

    return this.discussions.map(
      (d, i) => new DiscussionItem(`Q${i + 1}: ${d.question.slice(0, 50)}`, d.answer.slice(0, 80))
    );
  }
}
