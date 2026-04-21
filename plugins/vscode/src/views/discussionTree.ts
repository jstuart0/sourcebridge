// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Recent-discussions sidebar view.
 *
 * Persists the last N (default 50) questions + answers to
 * globalState so they survive VS Code reloads. Each row click
 * reopens the discussion panel so users can revisit prior grounded
 * answers without re-running the query.
 */

import * as vscode from "vscode";
import { createDiscussionPanel, DiscussionData } from "../panels/discussionPanel";

const STORAGE_KEY = "sourcebridge.recentDiscussions";
const MAX_HISTORY = 50;

interface StoredDiscussion {
  question: string;
  answer: string;
  references?: string[];
  sourceLabel?: string;
  ts: number;
}

class DiscussionItem extends vscode.TreeItem {
  constructor(
    public readonly entry: StoredDiscussion,
    index: number,
  ) {
    const preview = entry.question.length > 60 ? `${entry.question.slice(0, 57)}…` : entry.question;
    super(preview, vscode.TreeItemCollapsibleState.None);
    this.description = new Date(entry.ts).toLocaleString();
    this.tooltip = new vscode.MarkdownString(
      `**${preview}**\n\n${entry.answer.slice(0, 400)}${entry.answer.length > 400 ? "…" : ""}`,
    );
    this.iconPath = new vscode.ThemeIcon("comment-discussion");
    this.contextValue = "discussion";
    this.command = {
      command: "sourcebridge.openRecentDiscussion",
      title: "Open Discussion",
      arguments: [index],
    };
  }
}

class EmptyItem extends vscode.TreeItem {
  constructor() {
    super("No discussions yet", vscode.TreeItemCollapsibleState.None);
    this.description = "Cmd+I to ask";
    this.iconPath = new vscode.ThemeIcon("comment");
  }
}

export class DiscussionTreeProvider
  implements vscode.TreeDataProvider<DiscussionItem | EmptyItem>
{
  private _onDidChangeTreeData = new vscode.EventEmitter<
    DiscussionItem | EmptyItem | undefined
  >();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;
  private discussions: StoredDiscussion[] = [];

  constructor(private readonly context?: vscode.ExtensionContext) {
    this.load();
  }

  /**
   * Install a bridge command for `arguments: [index]` so tree-item
   * clicks can reopen the stored panel without awkward wiring from
   * extension.ts. Call exactly once from activate().
   */
  static register(
    context: vscode.ExtensionContext,
    provider: DiscussionTreeProvider,
    openFileReference: (filePath: string, line?: number) => Promise<void>,
  ): void {
    context.subscriptions.push(
      vscode.commands.registerCommand(
        "sourcebridge.openRecentDiscussion",
        async (index: number) => {
          const entry = provider.discussions[index];
          if (!entry) return;
          const data: DiscussionData = {
            question: entry.question,
            answer: entry.answer,
            references: entry.references ?? [],
            sourceLabel: entry.sourceLabel || "Previous discussion",
            sourceNote: "Restored from recent discussions.",
          };
          createDiscussionPanel(data, async (reference) => {
            const [filePath, lineStr] = reference.split(":");
            const line = lineStr ? Number(lineStr) : undefined;
            await openFileReference(filePath, line);
          });
        },
      ),
    );
    context.subscriptions.push(
      vscode.commands.registerCommand("sourcebridge.clearRecentDiscussions", async () => {
        provider.clear();
      }),
    );
  }

  refresh(): void {
    this._onDidChangeTreeData.fire(undefined);
  }

  addDiscussion(
    question: string,
    answer: string,
    extras?: { references?: string[]; sourceLabel?: string },
  ): void {
    this.discussions.unshift({
      question,
      answer,
      references: extras?.references,
      sourceLabel: extras?.sourceLabel,
      ts: Date.now(),
    });
    if (this.discussions.length > MAX_HISTORY) {
      this.discussions.length = MAX_HISTORY;
    }
    this.persist();
    this.refresh();
  }

  clear(): void {
    this.discussions = [];
    this.persist();
    this.refresh();
  }

  getTreeItem(element: DiscussionItem | EmptyItem): vscode.TreeItem {
    return element;
  }

  async getChildren(): Promise<(DiscussionItem | EmptyItem)[]> {
    if (this.discussions.length === 0) {
      return [new EmptyItem()];
    }
    return this.discussions.map((d, i) => new DiscussionItem(d, i));
  }

  private load(): void {
    if (!this.context) return;
    const stored = this.context.globalState.get<StoredDiscussion[]>(STORAGE_KEY, []);
    this.discussions = Array.isArray(stored) ? stored : [];
  }

  private persist(): void {
    if (!this.context) return;
    void this.context.globalState.update(STORAGE_KEY, this.discussions);
  }
}
