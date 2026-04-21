import * as vscode from "vscode";
import { registerCommands } from "../commands/register";
import { SourceBridgeClient } from "../graphql/client";

const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

describe("Command Registration", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    context = {
      subscriptions: [],
    } as any;

    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("registers sourcebridge.discussCode command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.discussCode");
  });

  it("registers sourcebridge.runReview command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.runReview");
  });

  it("registers sourcebridge.showRequirements command", async () => {
    const commands = await vscode.commands.getCommands();
    expect(commands).toContain("sourcebridge.showRequirements");
  });

  it("all commands are registered in subscriptions", () => {
    // 17 register.ts commands + 4 new tree-filter/detail/grouping
    // commands added for the v2 sidebar.
    expect(context.subscriptions.length).toBe(25);
  });

  it("discussCode shows warning when no editor is open", async () => {
    (vscode.window as any).activeTextEditor = undefined;
    await vscode.commands.executeCommand("sourcebridge.discussCode");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("No active editor");
  });
});

function createMockEditor(
  text = "const x = 1;",
  languageId = "typescript",
  fsPath = "/workspace/src/index.ts"
) {
  return {
    document: {
      uri: vscode.Uri.file(fsPath),
      getText: jest.fn().mockReturnValue(text),
      languageId,
      fileName: fsPath,
    },
    selection: { isEmpty: true },
  };
}

describe("discussCode server connectivity", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    (vscode.window.showErrorMessage as jest.Mock).mockClear();
    (vscode.window.showWarningMessage as jest.Mock).mockClear();

    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("shows error when server is not running", async () => {
    (vscode.window as any).activeTextEditor = createMockEditor();

    // isServerRunning calls fetch to /readyz — make it fail
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    await vscode.commands.executeCommand("sourcebridge.discussCode");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "SourceBridge server not running. Start it with `sourcebridge serve`."
    );
  });

  it("shows error when server returns non-ok health", async () => {
    (vscode.window as any).activeTextEditor = createMockEditor();

    // isServerRunning calls /readyz — server responds with 503
    mockFetch.mockResolvedValueOnce({ ok: false, status: 503 });

    await vscode.commands.executeCommand("sourcebridge.discussCode");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "SourceBridge server not running. Start it with `sourcebridge serve`."
    );
  });
});

describe("runReview server connectivity", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    (vscode.window.showErrorMessage as jest.Mock).mockClear();
    (vscode.window.showWarningMessage as jest.Mock).mockClear();

    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("shows error when server is not running", async () => {
    (vscode.window as any).activeTextEditor = createMockEditor();

    // isServerRunning calls fetch to /readyz — make it fail
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    await vscode.commands.executeCommand("sourcebridge.runReview");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "SourceBridge server not running. Start it with `sourcebridge serve`."
    );
  });

  it("shows warning when no editor is open", async () => {
    (vscode.window as any).activeTextEditor = undefined;

    await vscode.commands.executeCommand("sourcebridge.runReview");

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("No active editor");
  });

  it("shows error when server returns non-ok health", async () => {
    (vscode.window as any).activeTextEditor = createMockEditor();

    mockFetch.mockResolvedValueOnce({ ok: false, status: 500 });

    await vscode.commands.executeCommand("sourcebridge.runReview");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "SourceBridge server not running. Start it with `sourcebridge serve`."
    );
  });
});

describe("classifyError (indirect via discussCode)", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    (vscode.window.showErrorMessage as jest.Mock).mockClear();
    (vscode.window.showInputBox as jest.Mock).mockClear();
    (vscode.window.showQuickPick as jest.Mock).mockClear();

    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("classifies AI unavailable errors", async () => {
    (vscode.window as any).activeTextEditor = createMockEditor();

    // 1. isServerRunning — success
    mockFetch.mockResolvedValueOnce({ ok: true });

    // 2. showInputBox returns a question
    (vscode.window.showInputBox as jest.Mock).mockResolvedValueOnce("What does this do?");

    // 3. getRepositories — return one repo
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: { repositories: [{ id: "repo-1", name: "test", path: "/workspace", status: "indexed", fileCount: 5, functionCount: 10 }] },
      }),
    });

    // 4. discussCode — throws AI unavailable error
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        errors: [{ message: "AI features are unavailable" }],
      }),
    });

    await vscode.commands.executeCommand("sourcebridge.discussCode");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("AI features are currently unavailable")
    );
  });

  it("classifies GraphQL request failed errors", async () => {
    (vscode.window as any).activeTextEditor = createMockEditor();

    // 1. isServerRunning — success
    mockFetch.mockResolvedValueOnce({ ok: true });

    // 2. showInputBox returns a question
    (vscode.window.showInputBox as jest.Mock).mockResolvedValueOnce("Explain this");

    // 3. getRepositories — return one repo
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: { repositories: [{ id: "repo-1", name: "test", path: "/workspace", status: "indexed", fileCount: 5, functionCount: 10 }] },
      }),
    });

    // 4. discussCode — server error. The hardened transport retries 5xx
    // responses up to 3 times with backoff before surfacing the error.
    // Mock the same 502 four times to cover initial + retries.
    const bad502 = { ok: false, status: 502, statusText: "Bad Gateway" };
    mockFetch
      .mockResolvedValueOnce(bad502)
      .mockResolvedValueOnce(bad502)
      .mockResolvedValueOnce(bad502)
      .mockResolvedValueOnce(bad502);

    await vscode.commands.executeCommand("sourcebridge.discussCode");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("SourceBridge server returned an error")
    );
  });

  it("passes through generic error messages", async () => {
    (vscode.window as any).activeTextEditor = createMockEditor();

    // 1. isServerRunning — success
    mockFetch.mockResolvedValueOnce({ ok: true });

    // 2. showInputBox returns a question
    (vscode.window.showInputBox as jest.Mock).mockResolvedValueOnce("What is this?");

    // 3. getRepositories — return one repo
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: { repositories: [{ id: "repo-1", name: "test", path: "/workspace", status: "indexed", fileCount: 5, functionCount: 10 }] },
      }),
    });

    // 4. discussCode — network error. Network failures also retry up to
    // 3 times; reject four times total.
    mockFetch
      .mockRejectedValueOnce(new Error("Unexpected network failure"))
      .mockRejectedValueOnce(new Error("Unexpected network failure"))
      .mockRejectedValueOnce(new Error("Unexpected network failure"))
      .mockRejectedValueOnce(new Error("Unexpected network failure"));

    await vscode.commands.executeCommand("sourcebridge.discussCode");

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("Unexpected network failure")
    );
  });
});

describe("toRelativePosixPath (indirect via discussCode)", () => {
  let context: vscode.ExtensionContext;

  beforeEach(() => {
    mockFetch.mockReset();
    (vscode.window.showErrorMessage as jest.Mock).mockClear();
    (vscode.window.showInputBox as jest.Mock).mockClear();

    context = { subscriptions: [] } as any;
    const client = new SourceBridgeClient();
    registerCommands(context, client);
  });

  it("strips workspace root and sends relative path to server", async () => {
    const editor = createMockEditor(
      "function hello() {}",
      "typescript",
      "/workspace/src/deep/nested/file.ts"
    );
    (vscode.window as any).activeTextEditor = editor;

    // 1. isServerRunning — success
    mockFetch.mockResolvedValueOnce({ ok: true });

    // 2. showInputBox returns a question
    (vscode.window.showInputBox as jest.Mock).mockResolvedValueOnce("What does this do?");

    // 3. getRepositories — return one repo matching workspace name
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: { repositories: [{ id: "repo-1", name: "test", path: "/workspace", status: "indexed", fileCount: 5, functionCount: 10 }] },
      }),
    });

    // 4. discussCode — capture the call and return a valid response
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          discussCode: {
            answer: "It says hello",
            references: [],
            relatedRequirements: [],
            model: "llama3",
            inputTokens: 10,
            outputTokens: 5,
          },
        },
      }),
    });

    await vscode.commands.executeCommand("sourcebridge.discussCode");

    // The 3rd fetch call (index 2) is the discussCode GraphQL mutation
    // Calls: (0) GET /readyz, (1) POST getRepositories, (2) POST discussCode
    expect(mockFetch).toHaveBeenCalledTimes(3);
    const [, options] = mockFetch.mock.calls[2];
    const body = JSON.parse(options.body);
    // Verify the filePath is relative (workspace root stripped), not absolute
    expect(body.variables.input.filePath).toBe("src/deep/nested/file.ts");
  });
});
