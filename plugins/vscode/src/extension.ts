import * as vscode from "vscode";
import { SourceBridgeClient } from "./graphql/client";
import { RequirementCodeLensProvider } from "./providers/codeLens";
import { RequirementHoverProvider } from "./providers/hover";
import { RequirementDecorator } from "./providers/decorator";
import { RequirementsTreeProvider } from "./views/requirementsTree";
import { DiscussionTreeProvider } from "./views/discussionTree";
import { KnowledgeTreeProvider } from "./views/knowledgeTree";
import { registerCommands } from "./commands/register";

let client: SourceBridgeClient;
let decorator: RequirementDecorator;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  const outputChannel = vscode.window.createOutputChannel("SourceBridge");
  outputChannel.appendLine("SourceBridge extension activating...");

  client = new SourceBridgeClient(context);

  const serverRunning = await client.isServerRunning();
  if (!serverRunning) {
    vscode.window.showWarningMessage(
      "SourceBridge server not running. Start it with `sourcebridge serve` to enable features."
    );
    outputChannel.appendLine("SourceBridge server not running at configured URL");
  }

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
    vscode.languages.registerCodeLensProvider(codeLensSelector, codeLensProvider)
  );

  // Hover
  const hoverProvider = new RequirementHoverProvider(client);
  context.subscriptions.push(
    vscode.languages.registerHoverProvider(codeLensSelector, hoverProvider)
  );

  // Gutter Decorations
  decorator = new RequirementDecorator(client);
  context.subscriptions.push(decorator);

  // Sidebar tree views
  const requirementsTree = new RequirementsTreeProvider(client, context);
  context.subscriptions.push(
    vscode.window.createTreeView("sourcebridge.requirements", {
      treeDataProvider: requirementsTree,
    })
  );

  const discussionTree = new DiscussionTreeProvider();
  context.subscriptions.push(
    vscode.window.createTreeView("sourcebridge.discussion", {
      treeDataProvider: discussionTree,
    })
  );

  // Knowledge tree view
  const knowledgeTree = new KnowledgeTreeProvider(client, context);
  context.subscriptions.push(
    vscode.window.createTreeView("sourcebridge.knowledge", {
      treeDataProvider: knowledgeTree,
    })
  );

  // Commands
  registerCommands(context, client, {
    discussionTree,
    knowledgeTree,
    requirementsTree,
  });

  outputChannel.appendLine("SourceBridge extension activated");
}

export function deactivate(): void {
  // Cleanup handled by subscriptions
}

export function getClient(): SourceBridgeClient {
  return client;
}
