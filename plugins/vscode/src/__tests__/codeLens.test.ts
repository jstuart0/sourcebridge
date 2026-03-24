import * as vscode from "vscode";
import { RequirementCodeLensProvider } from "../providers/codeLens";
import { SourceBridgeClient } from "../graphql/client";

const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

describe("RequirementCodeLensProvider", () => {
  let provider: RequirementCodeLensProvider;

  beforeEach(() => {
    mockFetch.mockReset();
    const client = new SourceBridgeClient();
    provider = new RequirementCodeLensProvider(client);
  });

  function mockDocument(filePath: string): vscode.TextDocument {
    return {
      uri: vscode.Uri.file(filePath),
      languageId: "go",
      getText: () => "func main() {}",
      lineCount: 10,
    } as any;
  }

  it("returns CodeLens items for functions with linked requirements", async () => {
    // Mock REPOSITORIES query
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          repositories: [{ id: "repo-1", path: "/workspace" }],
        },
      }),
    });

    // Mock SYMBOLS_FOR_FILE query
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          symbols: {
            nodes: [
              { id: "sym-1", name: "main", kind: "FUNCTION", startLine: 1, endLine: 5 },
              { id: "sym-2", name: "handler", kind: "FUNCTION", startLine: 7, endLine: 10 },
            ],
          },
        },
      }),
    });

    // Mock CODE_TO_REQUIREMENTS for sym-1
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          codeToRequirements: [{ requirementId: "REQ-001", confidence: "HIGH" }],
        },
      }),
    });

    // Mock CODE_TO_REQUIREMENTS for sym-2
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          codeToRequirements: [{ requirementId: "REQ-002", confidence: "MEDIUM" }],
        },
      }),
    });

    const doc = mockDocument("/workspace/main.go");
    const lenses = await provider.provideCodeLenses(doc);

    expect(lenses.length).toBeGreaterThanOrEqual(1);
    expect(lenses[0]).toBeInstanceOf(vscode.CodeLens);
    expect(lenses[0].command?.title).toContain("REQ-001");
  });

  it("returns empty array when no repo matches", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: { repositories: [] },
      }),
    });

    const doc = mockDocument("/other/file.go");
    const lenses = await provider.provideCodeLenses(doc);
    expect(lenses).toEqual([]);
  });

  it("returns empty array when server is down", async () => {
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    const doc = mockDocument("/workspace/main.go");
    const lenses = await provider.provideCodeLenses(doc);
    expect(lenses).toEqual([]);
  });
});
