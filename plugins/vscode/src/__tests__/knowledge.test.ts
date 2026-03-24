import * as vscode from "vscode";
import { registerCommands } from "../commands/register";
import { SourceBridgeClient } from "../graphql/client";

const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

describe("Knowledge Command Registration", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("registers sourcebridge.explainSystem command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.explainSystem");
  });

  it("registers sourcebridge.generateCliffNotes command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.generateCliffNotes");
  });

  it("registers sourcebridge.generateLearningPath command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.generateLearningPath");
  });

  it("registers sourcebridge.generateCodeTour command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.generateCodeTour");
  });

  it("registers sourcebridge.showKnowledge command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.showKnowledge");
  });

  it("registers all 9 commands in subscriptions", () => {
    expect(context.subscriptions.length).toBe(21);
  });
});

describe("explainSystem command", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    (vscode.window.showErrorMessage as jest.Mock).mockClear();
    (vscode.window.showWarningMessage as jest.Mock).mockClear();
    (vscode.window.showInputBox as jest.Mock).mockClear();

    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("shows error when server is not running", async () => {
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    await vscode.commands.executeCommand("sourcebridge.explainSystem");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "SourceBridge server not running. Start it with `sourcebridge serve`."
    );
  });

  it("opens explain panel on success", async () => {
    // 1. isServerRunning — success
    mockFetch.mockResolvedValueOnce({ ok: true });

    // 2. showInputBox returns a question
    (vscode.window.showInputBox as jest.Mock).mockResolvedValueOnce(
      "How does auth work?"
    );

    // 3. getRepositories — return one repo
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          repositories: [
            {
              id: "repo-1",
              name: "test",
              path: "/workspace",
              status: "indexed",
              fileCount: 5,
              functionCount: 10,
            },
          ],
        },
      }),
    });

    // 4. explainSystem — return explanation
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          explainSystem: {
            explanation: "Auth uses JWT tokens...",
            model: "llama3",
            inputTokens: 100,
            outputTokens: 50,
          },
        },
      }),
    });

    await vscode.commands.executeCommand("sourcebridge.explainSystem");

    expect(vscode.window.createWebviewPanel).toHaveBeenCalled();
  });

  it("classifies AI unavailable error", async () => {
    // 1. isServerRunning — success
    mockFetch.mockResolvedValueOnce({ ok: true });

    // 2. showInputBox returns a question
    (vscode.window.showInputBox as jest.Mock).mockResolvedValueOnce("Explain auth");

    // 3. getRepositories — return one repo
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          repositories: [
            {
              id: "repo-1",
              name: "test",
              path: "/workspace",
              status: "indexed",
              fileCount: 5,
              functionCount: 10,
            },
          ],
        },
      }),
    });

    // 4. explainSystem — AI unavailable
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        errors: [{ message: "AI features are unavailable" }],
      }),
    });

    await vscode.commands.executeCommand("sourcebridge.explainSystem");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("AI features are currently unavailable")
    );
  });
});

describe("generateCliffNotes command", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    (vscode.window.showErrorMessage as jest.Mock).mockClear();

    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("shows error when server is not running", async () => {
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    await vscode.commands.executeCommand("sourcebridge.generateCliffNotes");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "SourceBridge server not running. Start it with `sourcebridge serve`."
    );
  });

  it("opens knowledge panel on success", async () => {
    // 1. isServerRunning
    mockFetch.mockResolvedValueOnce({ ok: true });

    // 2. getRepositories
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          repositories: [
            {
              id: "repo-1",
              name: "test",
              path: "/workspace",
              status: "indexed",
              fileCount: 5,
              functionCount: 10,
            },
          ],
        },
      }),
    });

    // 3. generateCliffNotes
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          generateCliffNotes: {
            id: "art-1",
            repositoryId: "repo-1",
            type: "cliff_notes",
            audience: "developer",
            depth: "standard",
            status: "ready",
            stale: false,
            generatedAt: new Date().toISOString(),
            sections: [
              {
                id: "s1",
                title: "Overview",
                content: "This is a Go project...",
                summary: "Project overview",
                confidence: 0.9,
                inferred: false,
                orderIndex: 0,
                evidence: [],
              },
            ],
          },
        },
      }),
    });

    await vscode.commands.executeCommand("sourcebridge.generateCliffNotes");

    expect(vscode.window.createWebviewPanel).toHaveBeenCalled();
  });
});

describe("generateCodeTour command", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    (vscode.window.showErrorMessage as jest.Mock).mockClear();

    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("shows error when server is not running", async () => {
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    await vscode.commands.executeCommand("sourcebridge.generateCodeTour");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "SourceBridge server not running. Start it with `sourcebridge serve`."
    );
  });
});
