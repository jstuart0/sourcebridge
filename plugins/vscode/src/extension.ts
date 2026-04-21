// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Extension entry point.
 *
 * Activation order (0.2.0):
 *   1. Open the output channel, load config.
 *   2. Migrate the legacy `sourcebridge.token` setting into secret
 *      storage (one-time, logs a warning and then clears the value).
 *   3. Construct the SourceBridgeClient + ConnectionSupervisor.
 *   4. Register providers (CodeLens, hover, decorator) and tree views.
 *   5. Install the status bar item.
 *   6. Register commands.
 *   7. Start the supervisor — it fires the first health probe, all
 *      subsequent heartbeats, and the reconnect event that refreshes
 *      providers.
 *
 * The supervisor is the single source of truth for "are we connected".
 * Providers subscribe to its `onReconnect` event so a transient
 * outage drops lenses/decorations until the server is back; no
 * "restart VS Code" workarounds.
 */

import * as vscode from "vscode";
import { SourceBridgeClient } from "./graphql/client";
import { RequirementCodeLensProvider } from "./providers/codeLens";
import { RequirementCodeActionProvider } from "./providers/codeActions";
import { RequirementHoverProvider } from "./providers/hover";
import { RequirementDecorator } from "./providers/decorator";
import { RequirementsTreeProvider } from "./views/requirementsTree";
import { DiscussionTreeProvider } from "./views/discussionTree";
import { KnowledgeTreeProvider } from "./views/knowledgeTree";
import { ImpactTreeProvider } from "./views/impactTree";
import { registerCommands } from "./commands/register";
import { registerRequirementCrudCommands } from "./commands/requirementCrud";
import { registerAskCommands } from "./commands/askCommands";
import { registerScopedPaletteCommand } from "./commands/scopedPalette";
import { createMcpClient } from "./mcp/client";
import { ConnectionSupervisor } from "./graphql/supervisor";
import { SourceBridgeStatusBar } from "./ui/statusBar";
import { initLogger, info, debug, warn, showChannel } from "./logging";
import { Telemetry } from "./telemetry";

let client: SourceBridgeClient;
let decorator: RequirementDecorator;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  const outputChannel = vscode.window.createOutputChannel("SourceBridge");
  initLogger(outputChannel);

  const config = vscode.workspace.getConfiguration("sourcebridge");
  const apiUrl = config.get("apiUrl", "http://localhost:8080");
  const debugMode = config.get("debug", false);

  info("activate", `SourceBridge extension 0.3.0 activating… (build ${new Date().toISOString().slice(0, 16)})`);
  info("activate", "  LLM GraphQL timeout: 180s · MCP tool timeout: 300s");
  info("activate", `  apiUrl: ${apiUrl}`);
  info("activate", `  debug: ${debugMode}`);
  info("activate", `  vscode: ${vscode.version}`);
  info("activate", `  extension: ${context.extension?.id || "unknown"}`);

  if (debugMode) {
    outputChannel.show(true);
  }

  await migrateLegacyTokenSetting(context);

  client = new SourceBridgeClient(context);

  // CodeLens
  const codeLensProvider = new RequirementCodeLensProvider(client);
  const codeLensSelector = [
    { language: "go" },
    { language: "python" },
    { language: "typescript" },
    { language: "javascript" },
    { language: "java" },
    { language: "rust" },
    { language: "cpp" },
    { language: "c" },
    { language: "csharp" },
  ];
  context.subscriptions.push(
    vscode.languages.registerCodeLensProvider(codeLensSelector, codeLensProvider),
  );

  // Hover
  const hoverProvider = new RequirementHoverProvider(client);
  context.subscriptions.push(
    vscode.languages.registerHoverProvider(codeLensSelector, hoverProvider),
  );

  // Code Actions (lightbulb). Provides symbol-aware "Link to
  // requirement…", "Create requirement from symbol…", "Ask about X…"
  // entries. Advertises both QuickFix and RefactorRewrite kinds so the
  // default keyboard binding (Cmd+.) surfaces them alongside the usual
  // refactor suggestions.
  const codeActionProvider = new RequirementCodeActionProvider(client);
  context.subscriptions.push(
    vscode.languages.registerCodeActionsProvider(
      codeLensSelector,
      codeActionProvider,
      RequirementCodeActionProvider.metadata,
    ),
  );

  // Gutter Decorations
  decorator = new RequirementDecorator(client);
  context.subscriptions.push(decorator);

  // Sidebar tree views
  const requirementsTree = new RequirementsTreeProvider(client, context);
  context.subscriptions.push(
    vscode.window.createTreeView("sourcebridge.requirements", {
      treeDataProvider: requirementsTree,
    }),
  );

  const discussionTree = new DiscussionTreeProvider(context);
  DiscussionTreeProvider.register(context, discussionTree, async (filePath, line) => {
    const workspaceFolder = vscode.workspace.workspaceFolders?.[0];
    if (!workspaceFolder) return;
    const uri = vscode.Uri.joinPath(workspaceFolder.uri, filePath);
    const doc = await vscode.workspace.openTextDocument(uri);
    const editor = await vscode.window.showTextDocument(doc);
    if (line !== undefined && line > 0) {
      const pos = new vscode.Position(line - 1, 0);
      editor.selection = new vscode.Selection(pos, pos);
      editor.revealRange(new vscode.Range(pos, pos));
    }
  });
  context.subscriptions.push(
    vscode.window.createTreeView("sourcebridge.discussion", {
      treeDataProvider: discussionTree,
    }),
  );

  const knowledgeTree = new KnowledgeTreeProvider(client, context);
  context.subscriptions.push(
    vscode.window.createTreeView("sourcebridge.knowledge", {
      treeDataProvider: knowledgeTree,
    }),
  );

  const impactTree = new ImpactTreeProvider(client, context);
  context.subscriptions.push(
    vscode.window.createTreeView("sourcebridge.impact", {
      treeDataProvider: impactTree,
    }),
  );
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.impact.refresh", () => impactTree.refresh()),
    vscode.commands.registerCommand("sourcebridge.impact.openFile", async (filePath: string) => {
      const folder = vscode.workspace.workspaceFolders?.[0];
      if (!folder || !filePath) return;
      const uri = vscode.Uri.joinPath(folder.uri, filePath);
      try {
        const doc = await vscode.workspace.openTextDocument(uri);
        await vscode.window.showTextDocument(doc);
      } catch (err) {
        vscode.window.showWarningMessage(
          `Could not open ${filePath}: ${(err as Error).message}`,
        );
      }
    }),
  );

  // Connection supervisor owns reconnect state. It fires onReconnect
  // when the server returns to `connected` so providers can flush
  // stale caches and refresh.
  const supervisor = new ConnectionSupervisor(client);
  context.subscriptions.push(supervisor);
  context.subscriptions.push(
    supervisor.onReconnect(() => {
      info("activate", "reconnect: refreshing providers + views");
      codeLensProvider.refresh();
      requirementsTree.refresh?.();
      knowledgeTree.refresh?.();
      impactTree.refresh();
      void decorator.refresh();
    }),
  );

  // Commands (still behind the monolith, split deferred to 0.3.0).
  registerCommands(context, client, {
    discussionTree,
    knowledgeTree,
    requirementsTree,
  });
  registerRequirementCrudCommands(context, client, {
    requirementsTree,
    decorator,
  });
  const mcpClient = createMcpClient(context);
  context.subscriptions.push({ dispose: () => void mcpClient.close() });
  registerAskCommands(context, client, { mcpClient, discussionTree });
  registerScopedPaletteCommand(context);

  // Register show-logs + status-bar helper commands.
  context.subscriptions.push(
    vscode.commands.registerCommand("sourcebridge.showLogs", () => {
      showChannel();
    }),
  );

  // Status bar. Construct AFTER commands so its click handler can
  // invoke `sourcebridge.signIn` et al.
  const runCommand = (id: string) => async () => {
    await vscode.commands.executeCommand(id);
  };
  const statusBar = new SourceBridgeStatusBar(supervisor, context, {
    signIn: runCommand("sourcebridge.signIn"),
    signOut: runCommand("sourcebridge.signOut"),
    switchRepository: runCommand("sourcebridge.switchRepository"),
    configure: runCommand("sourcebridge.configure"),
    showLogs: runCommand("sourcebridge.showLogs"),
  });
  context.subscriptions.push(statusBar);

  // Kick off the supervisor's first probe. Any connection state
  // changes flow through the status bar and the onReconnect callback.
  await supervisor.start();
  if (supervisor.current === "offline") {
    warn("activate", `Server NOT running at ${apiUrl} — features will be limited`);
    vscode.window.showWarningMessage(
      "SourceBridge server not running. Start it with `sourcebridge serve` to enable features.",
    );
  } else {
    info("activate", "Server connection verified");
  }

  // Opt-in telemetry. The emitter is a no-op when the setting is
  // false (default), so this call is safe to leave unguarded.
  const telemetry = new Telemetry(context);
  telemetry.event("activate");

  debug("activate", `Registered ${context.subscriptions.length} subscriptions`);
  info("activate", "SourceBridge extension activated");
}

export function deactivate(): void {
  // Cleanup handled by subscriptions
}

export function getClient(): SourceBridgeClient {
  return client;
}

/**
 * The 0.1.x `sourcebridge.token` config setting was deprecated in
 * favour of VS Code's secret storage. Remove any lingering value so
 * it doesn't clutter settings.json on upgrade. The fresh 0.2.0
 * manifest no longer declares the setting, so this runs once per
 * install and then becomes a no-op.
 */
async function migrateLegacyTokenSetting(context: vscode.ExtensionContext): Promise<void> {
  try {
    const cfg = vscode.workspace.getConfiguration("sourcebridge");
    const legacy = cfg.get<string>("token", "");
    if (!legacy) return;
    // If the user never signed in via the proper flow, preserve the
    // token by moving it into secret storage. If they have, drop it.
    const existing = await context.secrets.get("sourcebridge.token");
    if (!existing) {
      await context.secrets.store("sourcebridge.token", legacy);
      info("activate", "Migrated legacy sourcebridge.token setting into secret storage");
    } else {
      info("activate", "Dropping stale legacy sourcebridge.token setting");
    }
    await cfg.update("token", undefined, vscode.ConfigurationTarget.Global);
    try {
      await cfg.update("token", undefined, vscode.ConfigurationTarget.Workspace);
    } catch {
      // Workspace setting not present — fine to ignore.
    }
  } catch (err) {
    warn("activate", `legacy-token migration failed: ${(err as Error).message}`);
  }
}
