import * as vscode from "vscode";
import { RequirementCodeLensProvider } from "../providers/codeLens";
import { SourceBridgeClient } from "../graphql/client";

const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

function neverCancelledToken(): vscode.CancellationToken {
  return {
    isCancellationRequested: false,
    onCancellationRequested: () => ({ dispose: () => { /* noop */ } }),
  } as vscode.CancellationToken;
}

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

    // Mock CODE_TO_REQUIREMENTS for sym-1. Include `requirement` so the
    // lens builder can source the title, matching what real responses look
    // like.
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          codeToRequirements: [
            {
              requirementId: "REQ-001",
              confidence: "HIGH",
              requirement: { id: "REQ-001", externalId: "REQ-001", title: "REQ-001 Login flow" },
            },
          ],
        },
      }),
    });

    // Mock CODE_TO_REQUIREMENTS for sym-2
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          codeToRequirements: [
            {
              requirementId: "REQ-002",
              confidence: "MEDIUM",
              requirement: { id: "REQ-002", externalId: "REQ-002", title: "REQ-002 Session" },
            },
          ],
        },
      }),
    });

    const doc = mockDocument("/workspace/main.go");
    const lenses = await provider.provideCodeLenses(doc, neverCancelledToken());

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
    const lenses = await provider.provideCodeLenses(doc, neverCancelledToken());
    expect(lenses).toEqual([]);
  });

  it("returns empty array when server is down", async () => {
    mockFetch.mockRejectedValueOnce(new Error("Connection refused"));

    const doc = mockDocument("/workspace/main.go");
    const lenses = await provider.provideCodeLenses(doc, neverCancelledToken());
    expect(lenses).toEqual([]);
  });
});
