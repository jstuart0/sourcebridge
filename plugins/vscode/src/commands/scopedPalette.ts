// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Scoped command palette — one keybinding (Cmd+Shift+;) to surface the
 * five actions users reach for 90% of the time, filtered by the
 * currently-focused context.
 *
 * Design goals:
 *   - Never show commands that would fail (e.g. Ask About Symbol when
 *     there's no editor open).
 *   - Show the most specific scope first. Symbol > file > repository.
 *   - Keep labels short (~30 chars) so the picker reads at a glance.
 *
 * Everything in the picker delegates to an existing command ID; this
 * module doesn't own any business logic, it's purely a router.
 */

import * as vscode from "vscode";

interface PaletteAction {
  label: string;
  description?: string;
  detail?: string;
  command: string;
  args?: unknown[];
}

export function registerScopedPaletteCommand(context: vscode.ExtensionContext): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.scopedPalette", async () => {
      const actions = computeScopedActions();
      const picked = await vscode.window.showQuickPick(actions, {
        placeHolder: "SourceBridge — quick actions",
        matchOnDescription: true,
      });
      if (!picked) return;
      await vscode.commands.executeCommand(picked.command, ...(picked.args ?? []));
    }),
  );
}

function computeScopedActions(): PaletteAction[] {
  const editor = vscode.window.activeTextEditor;
  const hasSelection = editor && !editor.selection.isEmpty;
  const actions: PaletteAction[] = [];

  if (editor) {
    if (hasSelection) {
      actions.push({
        label: "$(comment-discussion) Ask about selection",
        description: "Cmd+I",
        command: "sourcebridge.askAboutSelection",
      });
    }
    actions.push({
      label: "$(symbol-method) Ask about symbol",
      description: "at cursor",
      command: "sourcebridge.askAboutSymbol",
    });
    actions.push({
      label: "$(book) Generate field guide for file",
      description: "Cmd+K N",
      command: "sourcebridge.generateCliffNotesForFile",
    });
    actions.push({
      label: "$(link) Show linked requirements",
      description: "at cursor",
      command: "sourcebridge.showLinkedRequirements",
    });
  }

  actions.push({
    label: "$(add) Create requirement",
    command: "sourcebridge.createRequirement",
  });
  actions.push({
    label: "$(checklist) Open requirements sidebar",
    command: "sourcebridge.showRequirements",
  });
  actions.push({
    label: "$(book) Open field guide",
    command: "sourcebridge.showKnowledge",
  });
  actions.push({
    label: "$(pulse) Show change risk",
    command: "sourcebridge.showImpactReport",
  });
  actions.push({
    label: "$(output) Show SourceBridge logs",
    command: "sourcebridge.showLogs",
  });

  return actions;
}
