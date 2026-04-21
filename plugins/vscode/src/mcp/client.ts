// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Minimal MCP streamable-HTTP client.
 *
 * Implements just enough of MCP 2025-11-25 to drive the VS Code chat
 * panel:
 *   - `initialize` → caches the returned `Mcp-Session-Id`.
 *   - `tools/call` with `Accept: text/event-stream` so slow tools
 *     (`explain_code`, `get_cliff_notes`) can stream progress back.
 *   - Progress notifications parsed from SSE frames are forwarded to
 *     the caller via a callback (token-stream style) so the UI can
 *     show "Thinking… 3s" with live updates.
 *
 * Authentication reuses the SourceBridge bearer token stored in VS
 * Code secret storage (same flow as the GraphQL client), so users
 * don't have to sign in twice.
 */

import * as vscode from "vscode";
import * as log from "../logging";

const MCP_PATH = "/api/v1/mcp/http";
const PROTOCOL_VERSION = "2025-11-25";
// Slow MCP tools (explain_code, get_cliff_notes) routinely stream
// tokens for 30–90s on a local 32b model. `fetch()` inherits no
// timeout; callers pass their own AbortSignal through `opts.signal`
// for cancellation. We do set a generous timeout here so a truly
// hung request still fails instead of holding the UI open forever.
const TOOL_CALL_TIMEOUT_MS = 300_000;

export interface McpInitializeResult {
  protocolVersion: string;
  serverInfo?: { name?: string; version?: string };
  capabilities?: Record<string, unknown>;
}

export interface McpProgress {
  progressToken: string | number;
  progress: number;
  total?: number;
  message?: string;
  // Non-standard extension: when the server produces content
  // progressively (e.g. explain_code streaming LLM tokens), the
  // chunk of visible output arrives in this field. Clients that
  // support content streaming append each delta to the in-flight
  // answer; clients that don't just ignore the extra field.
  delta?: string;
}

export interface McpToolContent {
  type: string;
  text?: string;
}

export interface McpToolResult {
  content: McpToolContent[];
  isError: boolean;
}

/**
 * Options for a streaming tool call. The `onProgress` callback receives
 * each `notifications/progress` event until the final response arrives.
 */
export interface McpToolCallOptions {
  onProgress?: (note: McpProgress) => void;
  signal?: AbortSignal;
}

/**
 * Errors thrown by the MCP client carry a `kind` discriminator so the
 * chat panel can render actionable copy (sign-in vs offline vs tool
 * error).
 */
export type McpClientErrorKind =
  | "unauthenticated"
  | "unavailable"  // MCP not mounted / not enabled on the server
  | "network"
  | "server"
  | "tool"
  | "protocol";

export class McpClientError extends Error {
  constructor(
    public readonly kind: McpClientErrorKind,
    message: string,
    public readonly cause?: unknown,
  ) {
    super(message);
    this.name = "McpClientError";
  }
}

export class McpClient {
  private sessionId?: string;
  private initialized = false;
  private nextRequestId = 1;
  // Sticky flag set after a 404 initialize. Every subsequent call
  // short-circuits with an "unavailable" error instead of hammering
  // a route we know is not mounted. Servers that ship MCP behind a
  // feature flag (e.g. some enterprise builds) rely on this so the
  // chat panel goes straight to the GraphQL path without flashing
  // "falling back" on every question.
  private unavailable = false;

  constructor(
    private readonly apiUrl: string,
    private readonly getToken: () => Promise<string | undefined>,
  ) {}

  /** Has the server signalled that MCP isn't mounted here? */
  isUnavailable(): boolean {
    return this.unavailable;
  }

  /**
   * Re-initialize so a server restart / token change forces a fresh
   * handshake on the next call.
   */
  reset(): void {
    this.sessionId = undefined;
    this.initialized = false;
    this.unavailable = false;
  }

  private endpoint(): string {
    return `${this.apiUrl.replace(/\/$/, "")}${MCP_PATH}`;
  }

  private async authHeaders(): Promise<Record<string, string>> {
    const token = await this.getToken();
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };
    if (token) headers["Authorization"] = `Bearer ${token}`;
    if (this.sessionId) headers["Mcp-Session-Id"] = this.sessionId;
    return headers;
  }

  private async ensureInitialized(): Promise<void> {
    if (this.unavailable) {
      throw new McpClientError("unavailable", "MCP is not mounted on this server");
    }
    if (this.initialized) return;

    const body = {
      jsonrpc: "2.0",
      id: this.nextRequestId++,
      method: "initialize",
      params: {
        protocolVersion: PROTOCOL_VERSION,
        capabilities: {},
        clientInfo: { name: "sourcebridge-vscode", version: "0.3.0" },
      },
    };

    log.debug("mcp", `initialize → ${this.endpoint()}`);
    let response: Response;
    try {
      response = await fetch(this.endpoint(), {
        method: "POST",
        headers: await this.authHeaders(),
        body: JSON.stringify(body),
      });
    } catch (err) {
      throw new McpClientError("network", `MCP initialize failed: ${(err as Error).message}`, err);
    }

    if (response.status === 401 || response.status === 403) {
      throw new McpClientError("unauthenticated", "Not signed in. Run SourceBridge: Sign In.");
    }
    if (response.status === 404 || response.status === 405) {
      this.unavailable = true;
      throw new McpClientError(
        "unavailable",
        `MCP is not mounted on this server (HTTP ${response.status})`,
      );
    }
    if (!response.ok) {
      const text = await safeText(response);
      throw new McpClientError("server", `MCP initialize HTTP ${response.status}: ${text}`);
    }

    const sid = response.headers.get("Mcp-Session-Id");
    if (!sid) {
      throw new McpClientError("protocol", "MCP initialize response missing Mcp-Session-Id header.");
    }
    this.sessionId = sid;
    this.initialized = true;
    log.debug("mcp", `initialized, session=${sid}`);

    // Per spec, also send `notifications/initialized` so the server
    // knows we're ready for tool calls.
    try {
      await fetch(this.endpoint(), {
        method: "POST",
        headers: await this.authHeaders(),
        body: JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }),
      });
    } catch (err) {
      log.warn("mcp", `notifications/initialized failed (non-fatal): ${(err as Error).message}`);
    }
  }

  /**
   * Invoke an MCP tool. When the server advertises a progress channel
   * (slow tools), the response is `text/event-stream`; we parse frames
   * until the terminal response arrives. Otherwise we take the simple
   * JSON-RPC response.
   */
  async callTool(
    name: string,
    args: Record<string, unknown>,
    opts: McpToolCallOptions = {},
  ): Promise<McpToolResult> {
    await this.ensureInitialized();

    const id = this.nextRequestId++;
    const progressToken = `vscode-${id}`;
    const body = {
      jsonrpc: "2.0",
      id,
      method: "tools/call",
      params: {
        name,
        arguments: args,
        _meta: opts.onProgress ? { progressToken } : undefined,
      },
    };

    const headers = await this.authHeaders();
    headers["Accept"] = opts.onProgress
      ? "application/json, text/event-stream"
      : "application/json";

    log.debug("mcp", `tools/call ${name}`);
    // Compose the caller's abort signal (if any) with our own timeout
    // controller. The user can still cancel explicitly; we just also
    // bail if the whole round trip exceeds the ceiling.
    const timeoutCtrl = new AbortController();
    const timeoutHandle = setTimeout(() => timeoutCtrl.abort(new Error("mcp timeout")), TOOL_CALL_TIMEOUT_MS);
    const combinedSignal = opts.signal
      ? combineSignals(opts.signal, timeoutCtrl.signal)
      : timeoutCtrl.signal;
    let response: Response;
    try {
      response = await fetch(this.endpoint(), {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal: combinedSignal,
      });
    } catch (err) {
      if ((err as Error).name === "AbortError") {
        throw new McpClientError("network", "cancelled");
      }
      throw new McpClientError("network", `MCP tools/call failed: ${(err as Error).message}`, err);
    } finally {
      clearTimeout(timeoutHandle);
    }

    if (response.status === 401 || response.status === 403) {
      throw new McpClientError("unauthenticated", "Not signed in. Run SourceBridge: Sign In.");
    }
    if (!response.ok) {
      const text = await safeText(response);
      throw new McpClientError("server", `MCP tools/call HTTP ${response.status}: ${text}`);
    }

    const ct = response.headers.get("Content-Type") || "";
    if (ct.includes("text/event-stream")) {
      return this.readEventStream(response, id, progressToken, opts);
    }

    const rpc = (await response.json()) as JsonRpcEnvelope;
    return extractToolResult(rpc);
  }

  /**
   * Shutdown on VSCode dispose. The server treats DELETE as
   * "terminate this session" so its state doesn't linger.
   */
  async close(): Promise<void> {
    if (!this.sessionId) return;
    try {
      await fetch(this.endpoint(), {
        method: "DELETE",
        headers: await this.authHeaders(),
      });
    } catch (err) {
      log.debug("mcp", `close failed (non-fatal): ${(err as Error).message}`);
    } finally {
      this.reset();
    }
  }

  private async readEventStream(
    response: Response,
    requestId: number,
    progressToken: string,
    opts: McpToolCallOptions,
  ): Promise<McpToolResult> {
    const body = response.body;
    if (!body) {
      throw new McpClientError("protocol", "event-stream response has no body");
    }
    const reader = (body as unknown as { getReader(): ReadableStreamDefaultReader<Uint8Array> }).getReader();
    const decoder = new TextDecoder("utf-8");
    let buffer = "";

    try {
      while (true) {
        const chunk = await reader.read();
        if (chunk.done) break;
        buffer += decoder.decode(chunk.value, { stream: true });

        // SSE frames are delimited by a blank line. We may get several
        // per read, or a partial frame that needs more data.
        while (true) {
          const split = buffer.indexOf("\n\n");
          if (split === -1) break;
          const frame = buffer.slice(0, split);
          buffer = buffer.slice(split + 2);

          const parsed = parseSseFrame(frame);
          if (!parsed) continue;

          // Only `message` events carry JSON-RPC envelopes; anything
          // else (comments, keepalives) we ignore.
          if (parsed.event && parsed.event !== "message") continue;

          let envelope: JsonRpcEnvelope;
          try {
            envelope = JSON.parse(parsed.data) as JsonRpcEnvelope;
          } catch (err) {
            log.warn("mcp", `malformed SSE JSON: ${(err as Error).message}`);
            continue;
          }

          // Progress notifications arrive as JSON-RPC notifications.
          if (envelope.method === "notifications/progress" && opts.onProgress) {
            const params = (envelope.params || {}) as Record<string, unknown>;
            if (
              params.progressToken === progressToken ||
              params.progressToken === undefined
            ) {
              opts.onProgress({
                progressToken: progressToken,
                progress: Number(params.progress ?? 0),
                total: params.total === undefined ? undefined : Number(params.total),
                message: typeof params.message === "string" ? params.message : undefined,
                delta: typeof params.delta === "string" ? params.delta : undefined,
              });
            }
            continue;
          }

          // Terminal response for our request.
          if (envelope.id === requestId) {
            return extractToolResult(envelope);
          }
        }
      }
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* ignore */
      }
    }

    throw new McpClientError("protocol", "event-stream ended before final response");
  }
}

interface JsonRpcEnvelope {
  jsonrpc?: string;
  id?: number | string;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

function extractToolResult(rpc: JsonRpcEnvelope): McpToolResult {
  if (rpc.error) {
    throw new McpClientError("tool", rpc.error.message || "MCP tool error", rpc.error);
  }
  const result = rpc.result as McpToolResult | undefined;
  if (!result || !Array.isArray(result.content)) {
    throw new McpClientError("protocol", "MCP response missing tool result content");
  }
  return result;
}

interface ParsedSseFrame {
  event?: string;
  data: string;
}

export function parseSseFrame(raw: string): ParsedSseFrame | undefined {
  let event: string | undefined;
  const dataLines: string[] = [];
  for (const line of raw.split("\n")) {
    if (!line || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    if (colon === -1) continue;
    const field = line.slice(0, colon);
    const value = line.slice(colon + 1).replace(/^\s/, "");
    if (field === "event") event = value;
    if (field === "data") dataLines.push(value);
  }
  if (dataLines.length === 0) return undefined;
  return { event, data: dataLines.join("\n") };
}

/**
 * Merge two AbortSignals into one so fetch() honors whichever fires
 * first. Runtime supports AbortSignal.any only in Node 20+; we do
 * the plumbing manually to stay compatible with VS Code's Node 18
 * extension host.
 */
function combineSignals(a: AbortSignal, b: AbortSignal): AbortSignal {
  const ctrl = new AbortController();
  const forward = (source: AbortSignal) => {
    if (source.aborted) {
      ctrl.abort((source as { reason?: unknown }).reason);
      return;
    }
    source.addEventListener("abort", () => ctrl.abort((source as { reason?: unknown }).reason), { once: true });
  };
  forward(a);
  forward(b);
  return ctrl.signal;
}

async function safeText(response: Response): Promise<string> {
  try {
    return await response.text();
  } catch {
    return "";
  }
}

/** Convenience used by extension.ts to build a client tied to the current config. */
export function createMcpClient(context: vscode.ExtensionContext): McpClient {
  const apiUrl = vscode.workspace.getConfiguration("sourcebridge").get<string>("apiUrl", "http://localhost:8080");
  return new McpClient(apiUrl, async () => context.secrets.get("sourcebridge.token"));
}
