import * as vscode from "vscode";
import { RequirementHoverProvider } from "../providers/hover";
import { SourceBridgeClient } from "../graphql/client";

const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

describe("RequirementHoverProvider", () => {
  let provider: RequirementHoverProvider;

  beforeEach(() => {
    mockFetch.mockReset();
    const client = new SourceBridgeClient();
    provider = new RequirementHoverProvider(client);
  });

  function mockDocument(filePath: string): vscode.TextDocument {
    return {
      uri: vscode.Uri.file(filePath),
      languageId: "go",
      getText: () => "func handler() {}",
      lineCount: 10,
    } as any;
  }

  it("returns markdown hover for a symbol with requirements", async () => {
    // REPOSITORIES
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          repositories: [{ id: "repo-1", path: "/workspace" }],
        },
      }),
    });

    // SYMBOLS_FOR_FILE
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          symbols: {
            nodes: [
              {
                id: "sym-1",
                name: "handler",
                kind: "FUNCTION",
                startLine: 1,
                endLine: 5,
                signature: "func handler(w http.ResponseWriter, r *http.Request)",
                docComment: "Handles HTTP requests",
              },
            ],
          },
        },
      }),
    });

    // CODE_TO_REQUIREMENTS
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          codeToRequirements: [
            { requirementId: "REQ-001", confidence: "HIGH", rationale: "Comment reference", verified: true },
          ],
        },
      }),
    });

    const doc = mockDocument("/workspace/handler.go");
    const pos = new vscode.Position(2, 5); // inside the function
    const hover = await provider.provideHover(doc, pos);

    expect(hover).toBeTruthy();
    expect(hover).toBeInstanceOf(vscode.Hover);
    const contents = hover!.contents as unknown as vscode.MarkdownString;
    expect(contents.value).toContain("handler");
    expect(contents.value).toContain("REQ-001");
  });

  it("returns null when no symbol matches position", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: { repositories: [{ id: "repo-1", path: "/workspace" }] },
      }),
    });

    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: { symbols: { nodes: [{ id: "sym-1", name: "handler", kind: "FUNCTION", startLine: 10, endLine: 15, signature: "", docComment: "" }] } },
      }),
    });

    const doc = mockDocument("/workspace/handler.go");
    const pos = new vscode.Position(0, 0); // outside function range
    const hover = await provider.provideHover(doc, pos);
    expect(hover).toBeNull();
  });

  it("returns null when server is down", async () => {
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    const doc = mockDocument("/workspace/handler.go");
    const pos = new vscode.Position(2, 5);
    const hover = await provider.provideHover(doc, pos);
    expect(hover).toBeNull();
  });
});
