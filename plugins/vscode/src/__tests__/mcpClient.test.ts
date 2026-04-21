import { McpClient, McpClientError, parseSseFrame } from "../mcp/client";

const mockFetch = jest.fn();
(global as any).fetch = mockFetch;

function jsonResponse(body: unknown, init: Partial<{ status: number; headers: Record<string, string> }> = {}) {
  const headers = new Map(Object.entries({ "Content-Type": "application/json", ...(init.headers || {}) }));
  return Promise.resolve({
    ok: (init.status ?? 200) < 400,
    status: init.status ?? 200,
    headers: {
      get: (name: string) => headers.get(name) || headers.get(name.toLowerCase()) || null,
    },
    json: async () => body,
    text: async () => JSON.stringify(body),
    body: null,
  });
}

function sseResponse(frames: string[]) {
  const encoder = new TextEncoder();
  const chunks = frames.map((f) => encoder.encode(f + "\n\n"));
  let i = 0;
  const reader = {
    read: async () => {
      if (i >= chunks.length) return { done: true, value: undefined };
      const value = chunks[i++];
      return { done: false, value };
    },
    cancel: async () => {},
  };
  return Promise.resolve({
    ok: true,
    status: 200,
    headers: {
      get: (name: string) =>
        name.toLowerCase() === "content-type"
          ? "text/event-stream"
          : name === "Mcp-Session-Id"
            ? "sess_123"
            : null,
    },
    body: { getReader: () => reader },
  });
}

describe("MCP client", () => {
  beforeEach(() => {
    mockFetch.mockReset();
  });

  it("initializes with session header and invokes a tool", async () => {
    mockFetch
      .mockImplementationOnce(() =>
        jsonResponse(
          { jsonrpc: "2.0", id: 1, result: { protocolVersion: "2025-11-25" } },
          { headers: { "Mcp-Session-Id": "sess_abc" } },
        ),
      )
      .mockImplementationOnce(() => jsonResponse({})) // notifications/initialized
      .mockImplementationOnce(() =>
        jsonResponse({
          jsonrpc: "2.0",
          id: 2,
          result: { content: [{ type: "text", text: "hello" }], isError: false },
        }),
      );

    const client = new McpClient("http://localhost:8080", async () => "tok_1");
    const result = await client.callTool("explain_code", { repository_id: "r1" });
    expect(result.content[0].text).toBe("hello");

    // Second fetch is notifications/initialized; third is tool call.
    expect(mockFetch).toHaveBeenCalledTimes(3);
    const thirdCall = mockFetch.mock.calls[2];
    expect(thirdCall[1].headers["Mcp-Session-Id"]).toBe("sess_abc");
    expect(thirdCall[1].headers["Authorization"]).toBe("Bearer tok_1");
  });

  it("surfaces 401 as unauthenticated error", async () => {
    mockFetch.mockImplementationOnce(() =>
      jsonResponse({ error: "unauthorized" }, { status: 401 }),
    );
    const client = new McpClient("http://localhost:8080", async () => undefined);
    await expect(client.callTool("explain_code", {})).rejects.toMatchObject({
      kind: "unauthenticated",
    });
  });

  it("parses SSE progress frames then returns final result", async () => {
    mockFetch
      .mockImplementationOnce(() =>
        jsonResponse(
          { jsonrpc: "2.0", id: 1, result: { protocolVersion: "2025-11-25" } },
          { headers: { "Mcp-Session-Id": "sess_sse" } },
        ),
      )
      .mockImplementationOnce(() => jsonResponse({})) // notifications/initialized
      .mockImplementationOnce(() =>
        sseResponse([
          `event: message\ndata: ${JSON.stringify({
            jsonrpc: "2.0",
            method: "notifications/progress",
            params: { progressToken: "vscode-2", progress: 0.3, message: "Planning" },
          })}`,
          `event: message\ndata: ${JSON.stringify({
            jsonrpc: "2.0",
            id: 2,
            result: { content: [{ type: "text", text: "done" }], isError: false },
          })}`,
        ]),
      );

    const progress: Array<{ message?: string; progress: number }> = [];
    const client = new McpClient("http://localhost:8080", async () => "tok");
    const result = await client.callTool(
      "explain_code",
      { repository_id: "r1" },
      { onProgress: (n) => progress.push({ message: n.message, progress: n.progress }) },
    );
    expect(result.content[0].text).toBe("done");
    expect(progress).toHaveLength(1);
    expect(progress[0].message).toBe("Planning");
  });

  it("throws McpClientError on JSON-RPC errors", async () => {
    mockFetch
      .mockImplementationOnce(() =>
        jsonResponse(
          { jsonrpc: "2.0", id: 1, result: { protocolVersion: "2025-11-25" } },
          { headers: { "Mcp-Session-Id": "sess_err" } },
        ),
      )
      .mockImplementationOnce(() => jsonResponse({}))
      .mockImplementationOnce(() =>
        jsonResponse({
          jsonrpc: "2.0",
          id: 2,
          error: { code: -32000, message: "worker unavailable" },
        }),
      );
    const client = new McpClient("http://localhost:8080", async () => "tok");
    await expect(client.callTool("explain_code", {})).rejects.toBeInstanceOf(McpClientError);
  });
});

describe("parseSseFrame", () => {
  it("extracts event and data fields", () => {
    const frame = "event: message\ndata: {\"foo\": 1}";
    expect(parseSseFrame(frame)).toEqual({ event: "message", data: '{"foo": 1}' });
  });

  it("concatenates multi-line data", () => {
    const frame = "data: line1\ndata: line2";
    expect(parseSseFrame(frame)).toEqual({ event: undefined, data: "line1\nline2" });
  });

  it("ignores comments and empty lines", () => {
    const frame = ": keepalive\n\ndata: hi";
    expect(parseSseFrame(frame)).toEqual({ event: undefined, data: "hi" });
  });

  it("returns undefined when no data", () => {
    expect(parseSseFrame("event: message")).toBeUndefined();
  });
});
