// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Single-entry-point transport for anything the extension fetches over
 * the network. Previously every call site built its own `fetch()` with
 * ad-hoc headers and no timeout; that pattern caused the extension to
 * hang when the server was slow and to fan out uncancellable requests
 * on every keystroke. Every read path — GraphQL queries, auth REST
 * endpoints, health probes — migrates to this helper.
 *
 * Guarantees provided:
 *
 *  - **Timeout.** Every request has a default 10 s hard deadline.
 *    Callers can raise or lower this with the `timeoutMs` option.
 *  - **Cancellation.** Callers pass a `vscode.CancellationToken`; the
 *    helper converts it to an `AbortSignal` and cancels the in-flight
 *    request when VS Code fires the token.
 *  - **Retry with jitter.** 5xx responses and transport errors trigger
 *    up to 3 attempts with exponential backoff (100 ms × 2^n plus a
 *    small random jitter). 4xx responses fail immediately — they're
 *    almost always config bugs (bad token, bad URL) and retrying just
 *    thrashes.
 *  - **Uniform error surface.** Every failure comes back as
 *    {@link TransportError} with a stable `kind` so callers can render
 *    specific error messages (`"offline"`, `"unauthenticated"`, …).
 *
 * This module is deliberately free of VS Code API imports other than
 * the token type, so unit tests can exercise it under plain Jest.
 */

import type { CancellationToken } from "vscode";

/** Stable classifier for user-facing error copy. */
export type TransportErrorKind =
  | "timeout"
  | "cancelled"
  | "offline"
  | "unauthenticated"
  | "forbidden"
  | "not-found"
  | "graphql"
  | "http"
  | "network";

export class TransportError extends Error {
  readonly kind: TransportErrorKind;
  readonly status?: number;
  readonly details?: unknown;
  constructor(kind: TransportErrorKind, message: string, status?: number, details?: unknown) {
    super(message);
    this.name = "TransportError";
    this.kind = kind;
    this.status = status;
    this.details = details;
  }
}

export interface TransportOptions {
  /** Hard request deadline in ms. Default 10 000. */
  timeoutMs?: number;
  /** VS Code cancellation token; cancels the in-flight request. */
  token?: CancellationToken;
  /** Extra headers to merge in (caller-owned). */
  headers?: Record<string, string>;
  /** Max retry attempts for transient failures (5xx / network). Default 3. */
  maxRetries?: number;
  /** Retry base delay in ms. Default 100. */
  retryBaseMs?: number;
}

interface RequestArgs {
  url: string;
  method: "GET" | "POST" | "DELETE";
  body?: string;
  contentType?: string;
  opts?: TransportOptions;
}

const DEFAULT_TIMEOUT_MS = 10_000;
const DEFAULT_MAX_RETRIES = 3;
const DEFAULT_RETRY_BASE_MS = 100;

/** Fetch JSON over HTTP with our hardened rules. Returns the raw body. */
export async function requestJSON<T>(args: RequestArgs): Promise<T> {
  const text = await requestText(args);
  if (!text) {
    // Empty body is a programming error for JSON endpoints.
    throw new TransportError("http", "empty response body");
  }
  try {
    return JSON.parse(text) as T;
  } catch (err) {
    throw new TransportError("http", `invalid JSON body: ${(err as Error).message}`);
  }
}

/** Fetch raw text with our hardened rules. */
export async function requestText(args: RequestArgs): Promise<string> {
  const { url, method, body, contentType, opts = {} } = args;
  const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const maxRetries = Math.max(0, opts.maxRetries ?? DEFAULT_MAX_RETRIES);
  const retryBase = Math.max(1, opts.retryBaseMs ?? DEFAULT_RETRY_BASE_MS);

  let lastErr: unknown;
  for (let attempt = 0; attempt <= maxRetries; attempt++) {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(new Error("timeout")), timeoutMs);
    const tokenDisposable = opts.token?.onCancellationRequested(() => ctrl.abort(new Error("cancelled")));
    try {
      const headers: Record<string, string> = { ...(opts.headers ?? {}) };
      if (body !== undefined && contentType) headers["Content-Type"] = contentType;
      const res = await fetch(url, {
        method,
        headers,
        body,
        signal: ctrl.signal,
      });
      if (!res.ok) {
        const status = res.status;
        const text = await safeReadText(res);
        // 4xx: do not retry — almost always a config bug.
        if (status === 401) throw new TransportError("unauthenticated", "unauthenticated", status, text);
        if (status === 403) throw new TransportError("forbidden", "forbidden", status, text);
        if (status === 404) throw new TransportError("not-found", "not found", status, text);
        if (status >= 400 && status < 500) throw new TransportError("http", `HTTP ${status}: ${text}`, status, text);
        // 5xx / others: retry.
        lastErr = new TransportError("http", `HTTP ${status}: ${text}`, status, text);
        if (attempt < maxRetries) {
          await jitteredDelay(retryBase, attempt);
          continue;
        }
        throw lastErr;
      }
      return await safeReadText(res);
    } catch (err) {
      // Cancellation + timeout are terminal, not retryable.
      if (opts.token?.isCancellationRequested) {
        throw new TransportError("cancelled", "request cancelled");
      }
      if (isAbort(err)) {
        // The abort reason was set to "timeout" if we fired the timer.
        const reason = (ctrl.signal as { reason?: unknown }).reason;
        if (reason instanceof Error && reason.message === "timeout") {
          throw new TransportError("timeout", `request timed out after ${timeoutMs} ms`);
        }
        throw new TransportError("cancelled", "request cancelled");
      }
      // Network error. Retry if attempts remain.
      if (err instanceof TransportError) throw err;
      lastErr = err;
      if (attempt < maxRetries) {
        await jitteredDelay(retryBase, attempt);
        continue;
      }
      const msg = (err as Error | undefined)?.message ?? String(err);
      throw new TransportError("network", `network error: ${msg}`, undefined, err);
    } finally {
      clearTimeout(timer);
      tokenDisposable?.dispose();
    }
  }
  // Unreachable — the loop either returns or throws.
  throw lastErr ?? new TransportError("network", "exhausted retries");
}

async function safeReadText(res: Response): Promise<string> {
  try {
    // Real fetch Responses always have text(); legacy test mocks may only
    // have json(). Support both so tests written against the old mock
    // shape keep passing without rewrites.
    if (typeof (res as { text?: unknown }).text === "function") {
      return (await res.text()) ?? "";
    }
    const anyRes = res as { json?: () => Promise<unknown> };
    if (typeof anyRes.json === "function") {
      return JSON.stringify(await anyRes.json());
    }
    return "";
  } catch {
    return "";
  }
}

function isAbort(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const anyErr = err as { name?: string };
  return anyErr.name === "AbortError";
}

async function jitteredDelay(baseMs: number, attempt: number): Promise<void> {
  const exp = baseMs * Math.pow(2, attempt);
  const jitter = Math.random() * baseMs;
  await new Promise((resolve) => setTimeout(resolve, exp + jitter));
}

/* ----- GraphQL-specific convenience wrapper ----------------------- */

/**
 * Execute a GraphQL operation. Handles response-level error surfacing
 * so callers don't have to remember to check `body.errors`.
 */
export async function graphqlRequest<T>(
  url: string,
  query: string,
  variables: Record<string, unknown> | undefined,
  opts: TransportOptions,
): Promise<T> {
  const body = JSON.stringify({ query, variables });
  const response = await requestJSON<{
    data?: T;
    errors?: Array<{ message: string; extensions?: Record<string, unknown> }>;
  }>({
    url,
    method: "POST",
    body,
    contentType: "application/json",
    opts,
  });
  if (response.errors?.length) {
    const msg = response.errors.map((e) => e.message).join("; ");
    throw new TransportError("graphql", msg, undefined, response.errors);
  }
  if (!response.data) {
    throw new TransportError("graphql", "no data returned");
  }
  return response.data;
}
