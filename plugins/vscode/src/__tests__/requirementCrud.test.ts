import * as vscode from "vscode";
import { registerRequirementCrudCommands } from "../commands/requirementCrud";
import { registerAskCommands } from "../commands/askCommands";
import { registerScopedPaletteCommand } from "../commands/scopedPalette";
import { SourceBridgeClient } from "../graphql/client";
import { McpClient } from "../mcp/client";

const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

describe("Phase 2/3 command registration", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    const mcpClient = new McpClient("http://localhost:8080", async () => undefined);
    registerRequirementCrudCommands(context, client);
    registerAskCommands(context, client, { mcpClient });
    registerScopedPaletteCommand(context);
  });

  it("registers all CRUD commands", async () => {
    const commands = await vscode.commands.getCommands();
    for (const id of [
      "sourcebridge.createRequirement",
      "sourcebridge.createRequirementFromSymbol",
      "sourcebridge.linkRequirementToSymbol",
      "sourcebridge.editRequirement",
      "sourcebridge.deleteRequirement",
      "sourcebridge.unlinkRequirementLink",
    ]) {
      expect(commands).toContain(id);
    }
  });

  it("registers ask commands", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.askAboutSymbol");
    expect(commands).toContain("sourcebridge.askAboutSelection");
  });

  it("registers the scoped palette command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.scopedPalette");
  });

  it("deleteRequirement aborts when confirmation is declined", async () => {
    (vscode.window.showWarningMessage as jest.Mock).mockResolvedValueOnce(undefined);
    // Should not throw and should not call moveToTrash — we pass an
    // object so no network fetch is required up front.
    await vscode.commands.executeCommand("sourcebridge.deleteRequirement", {
      id: "req_1",
      title: "t",
      description: "",
      source: "manual",
      tags: [],
    });
    // No fetch should have happened because the user dismissed.
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("askAboutSelection aborts when no editor is active", async () => {
    (vscode.window as any).activeTextEditor = undefined;
    (vscode.window.showWarningMessage as jest.Mock).mockClear();
    await vscode.commands.executeCommand("sourcebridge.askAboutSelection");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("No active editor.");
  });

});
