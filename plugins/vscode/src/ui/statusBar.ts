// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Always-visible status bar item. Six states, each with its own text,
 * icon, tooltip, and primary click action. Clicking the item opens a
 * quick-pick of context-appropriate actions (retry, sign-in, switch
 * repo, open logs).
 *
 * Before this module the extension's only connection signal was a
 * one-shot warning toast at activation — users never knew whether
 * the server had come back online, and there was no click-to-reconnect
 * affordance.
 */

import * as vscode from "vscode";
import { ConnectionState, ConnectionSupervisor } from "../graphql/supervisor";

const ITEM_PRIORITY = 100;
const COMMAND_ID = "sourcebridge.statusBar.click";

export class SourceBridgeStatusBar implements vscode.Disposable {
  private readonly item: vscode.StatusBarItem;
  private readonly disposables: vscode.Disposable[] = [];
  private countdownTimer: NodeJS.Timeout | undefined;

  constructor(
    private readonly supervisor: ConnectionSupervisor,
    context: vscode.ExtensionContext,
    private readonly actions: StatusBarActions,
  ) {
    this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, ITEM_PRIORITY);
    this.item.command = COMMAND_ID;
    this.item.accessibilityInformation = { label: "SourceBridge status" };

    this.disposables.push(
      vscode.commands.registerCommand(COMMAND_ID, () => this.handleClick()),
      supervisor.onDidChange(() => this.render()),
    );

    context.subscriptions.push(this.item);
    this.render();
    this.item.show();
  }

  private render(): void {
    const state = this.supervisor.current;
    const visual = STATE_VISUALS[state];
    this.item.text = visual.text(this.supervisor);
    this.item.tooltip = visual.tooltip;
    this.item.backgroundColor = visual.background
      ? new vscode.ThemeColor(visual.background)
      : undefined;
    // Maintain the `sourcebridge.connected` context key so when-clauses
    // elsewhere can gate features.
    void vscode.commands.executeCommand(
      "setContext",
      "sourcebridge.connected",
      state === "connected",
    );
    this.updateCountdown(state);
  }

  /**
   * While offline, refresh the status bar once per second so the
   * `retrying in 0:42` countdown ticks visibly. Stops the timer the
   * moment the state leaves `offline`.
   */
  private updateCountdown(state: ConnectionState): void {
    if (state === "offline") {
      if (this.countdownTimer) return;
      this.countdownTimer = setInterval(() => {
        if (this.supervisor.current !== "offline") {
          this.stopCountdown();
          return;
        }
        const visual = STATE_VISUALS.offline;
        this.item.text = visual.text(this.supervisor);
      }, 1_000);
    } else {
      this.stopCountdown();
    }
  }

  private stopCountdown(): void {
    if (this.countdownTimer) {
      clearInterval(this.countdownTimer);
      this.countdownTimer = undefined;
    }
  }

  private async handleClick(): Promise<void> {
    const state = this.supervisor.current;
    const picks = this.buildPicks(state);
    if (!picks.length) return;
    const choice = await vscode.window.showQuickPick(picks, {
      placeHolder: `SourceBridge: ${STATE_VISUALS[state].shortLabel}`,
    });
    if (!choice) return;
    await choice.run();
  }

  private buildPicks(state: ConnectionState): PickItem[] {
    const items: PickItem[] = [];
    if (state === "offline") {
      items.push({
        label: "$(sync) Retry now",
        run: () => this.supervisor.retryNow(),
      });
    }
    if (state === "auth-required") {
      items.push({
        label: "$(key) Sign in",
        run: () => this.actions.signIn(),
      });
    }
    if (state === "connected") {
      items.push({
        label: "$(arrow-swap) Switch repository…",
        run: () => this.actions.switchRepository(),
      });
      items.push({
        label: "$(sign-out) Sign out",
        run: () => this.actions.signOut(),
      });
    }
    if (state === "no-workspace") {
      items.push({
        label: "$(folder) Open a folder",
        run: async () => {
          await vscode.commands.executeCommand("vscode.openFolder");
        },
      });
    }
    items.push({
      label: "$(gear) Configure server…",
      run: () => this.actions.configure(),
    });
    items.push({
      label: "$(output) Show logs",
      run: () => this.actions.showLogs(),
    });
    return items;
  }

  dispose(): void {
    this.stopCountdown();
    this.item.dispose();
    for (const d of this.disposables) d.dispose();
  }
}

export interface StatusBarActions {
  signIn(): Promise<void> | void;
  signOut(): Promise<void> | void;
  switchRepository(): Promise<void> | void;
  configure(): Promise<void> | void;
  showLogs(): Promise<void> | void;
}

interface StateVisual {
  /** Long tooltip text. */
  tooltip: string;
  /** Short label shown when the status bar is clicked (quick-pick header). */
  shortLabel: string;
  /** Optional themeable background (e.g. warning/error tint). */
  background?: string;
  /** Text builder — receives the supervisor so offline can tick its countdown. */
  text(sup: ConnectionSupervisor): string;
}

const STATE_VISUALS: Record<ConnectionState, StateVisual> = {
  connecting: {
    tooltip: "SourceBridge: connecting to the server…",
    shortLabel: "Connecting",
    text: () => "$(sync~spin) SourceBridge: connecting…",
  },
  connected: {
    tooltip: "SourceBridge is connected. Click for actions.",
    shortLabel: "Connected",
    text: () => "$(check) SourceBridge: connected",
  },
  offline: {
    tooltip:
      "SourceBridge server unreachable. Click to retry now or open the logs.",
    shortLabel: "Offline",
    background: "statusBarItem.warningBackground",
    text: (sup) => {
      const ms = sup.msUntilRetry;
      if (ms <= 0) return "$(error) SourceBridge: offline";
      const secs = Math.ceil(ms / 1000);
      const mins = Math.floor(secs / 60);
      const rem = secs % 60;
      const label = mins > 0 ? `${mins}m ${rem}s` : `${rem}s`;
      return `$(error) SourceBridge: offline · retry in ${label}`;
    },
  },
  "auth-required": {
    tooltip: "SourceBridge needs you to sign in before queries will succeed.",
    shortLabel: "Sign in required",
    background: "statusBarItem.warningBackground",
    text: () => "$(key) SourceBridge: sign in required",
  },
  "no-workspace": {
    tooltip: "SourceBridge needs an open workspace folder to resolve a repository.",
    shortLabel: "Open a folder",
    text: () => "$(folder) SourceBridge: open a folder",
  },
  "no-repo": {
    tooltip: "No indexed repository matches this workspace. Click to pick one.",
    shortLabel: "Pick a repository",
    text: () => "$(circle-slash) SourceBridge: pick a repository",
  },
};

interface PickItem extends vscode.QuickPickItem {
  run(): Promise<void> | void;
}
