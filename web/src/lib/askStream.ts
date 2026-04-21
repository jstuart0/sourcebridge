// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Client for POST /api/v1/discuss/stream.
 *
 * The endpoint serves an SSE body with three event types:
 *   - `token` — { delta: string }  — append to the visible answer
 *   - `done`  — { answer, references, usage, elapsed_ms } — final
 *   - `error` — { error: string } — user-visible failure
 *
 * This helper parses incoming chunks into complete SSE events and
 * fires the callbacks as they arrive. Designed for a React page that
 * wants to update state as each token lands; the caller owns the
 * AbortController so they can cancel on unmount or a fresh submit.
 */

export interface AskStreamInput {
  repositoryId: string;
  question: string;
  filePath?: string;
  code?: string;
  language?: string;
}

export interface AskStreamDone {
  answer: string;
  references: string[];
  elapsedMs: number;
}

export interface AskStreamHandlers {
  onToken?: (delta: string) => void;
  onDone?: (result: AskStreamDone) => void;
  onError?: (message: string) => void;
  signal?: AbortSignal;
}

/**
 * Fetch the streaming discuss endpoint and pump events into the
 * handlers. Returns when the server sends `done` or `error`.
 *
 * Network / HTTP errors are reported through `onError`; the caller
 * doesn't need to wrap this in try/catch (though it's safe to).
 */
export async function askStream(
  input: AskStreamInput,
  handlers: AskStreamHandlers,
): Promise<void> {
  const res = await fetch("/api/v1/discuss/stream", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
    },
    credentials: "same-origin",
    body: JSON.stringify({
      repository_id: input.repositoryId,
      question: input.question,
      file_path: input.filePath,
      code: input.code,
      language: input.language,
    }),
    signal: handlers.signal,
  });

  if (!res.ok || !res.body) {
    const msg = `HTTP ${res.status}: ${await res.text().catch(() => "stream failed")}`;
    handlers.onError?.(msg);
    return;
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      // SSE events end at `\n\n`. Pump out complete events; leave a
      // trailing partial event in the buffer for the next read.
      while (true) {
        const end = buffer.indexOf("\n\n");
        if (end === -1) break;
        const raw = buffer.slice(0, end);
        buffer = buffer.slice(end + 2);
        const frame = parseSseFrame(raw);
        if (!frame) continue;

        if (frame.event === "token") {
          const delta = parseJsonField(frame.data, "delta");
          if (typeof delta === "string") handlers.onToken?.(delta);
        } else if (frame.event === "done") {
          const parsed = safeJson(frame.data) as
            | { answer?: string; references?: string[]; elapsed_ms?: number }
            | null;
          handlers.onDone?.({
            answer: parsed?.answer ?? "",
            references: parsed?.references ?? [],
            elapsedMs: parsed?.elapsed_ms ?? 0,
          });
          return;
        } else if (frame.event === "error") {
          const msg = parseJsonField(frame.data, "error");
          handlers.onError?.(typeof msg === "string" ? msg : "stream error");
          return;
        }
      }
    }
  } catch (err) {
    if ((err as Error)?.name === "AbortError") return;
    handlers.onError?.((err as Error).message);
  } finally {
    try {
      await reader.cancel();
    } catch {
      /* ignore */
    }
  }
}

interface SseFrame {
  event?: string;
  data: string;
}

function parseSseFrame(raw: string): SseFrame | undefined {
  let event: string | undefined;
  const data: string[] = [];
  for (const line of raw.split("\n")) {
    if (!line || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    if (colon < 0) continue;
    const field = line.slice(0, colon);
    const value = line.slice(colon + 1).replace(/^\s/, "");
    if (field === "event") event = value;
    if (field === "data") data.push(value);
  }
  if (data.length === 0) return undefined;
  return { event, data: data.join("\n") };
}

function safeJson(raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

function parseJsonField(raw: string, field: string): unknown {
  const obj = safeJson(raw);
  if (obj && typeof obj === "object") return (obj as Record<string, unknown>)[field];
  return undefined;
}
