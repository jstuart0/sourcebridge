"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { TOKEN_KEY } from "@/lib/token-key";
import { isTokenExpired, forceLogout } from "@/lib/auth-utils";

export interface ServerEvent {
  type: string;
  timestamp: string;
  data: Record<string, unknown>;
}

const EVENT_TYPES = [
  "repo.index.started",
  "repo.index.completed",
  "repo.index.failed",
  "requirement.imported",
  "requirement.linked",
  "link.verified",
  "link.rejected",
  "review.completed",
];

/**
 * Subscribe to all server-sent events on /api/v1/events.
 * Reconnects automatically on connection loss.
 */
export function useEventStream(onEvent: (event: ServerEvent) => void) {
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
    if (!token) return;

    // Don't even connect if the token is already expired
    if (isTokenExpired(token)) {
      forceLogout();
      return;
    }

    // EventSource doesn't support custom headers, so we pass token as query param
    const source = new EventSource(`/api/v1/events?token=${encodeURIComponent(token)}`);
    let errorCount = 0;

    for (const eventType of EVENT_TYPES) {
      source.addEventListener(eventType, (e) => {
        errorCount = 0; // reset on successful event
        try {
          const parsed = JSON.parse(e.data) as ServerEvent;
          onEventRef.current(parsed);
        } catch {
          // ignore malformed events
        }
      });
    }

    source.onerror = () => {
      errorCount++;
      // EventSource auto-reconnects, but if we get repeated errors
      // (3+ in a row with no successful events), the token is likely
      // expired — check and force logout if so.
      if (errorCount >= 3) {
        const currentToken = localStorage.getItem(TOKEN_KEY);
        if (!currentToken || isTokenExpired(currentToken)) {
          source.close();
          forceLogout();
        }
      }
    };

    return () => source.close();
  }, []);
}

/** Legacy hook for basic SSE streaming (used by older components) */
export function useSSE(url: string) {
  const [data, setData] = useState<string>("");
  const [isStreaming, setIsStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const sourceRef = useRef<EventSource | null>(null);

  const start = useCallback(() => {
    setData("");
    setError(null);
    setIsStreaming(true);

    const source = new EventSource(url);
    sourceRef.current = source;

    source.onmessage = (event) => {
      setData((prev) => prev + event.data);
    };

    source.onerror = () => {
      setError("Stream connection lost");
      setIsStreaming(false);
      source.close();
    };

    source.addEventListener("done", () => {
      setIsStreaming(false);
      source.close();
    });
  }, [url]);

  const stop = useCallback(() => {
    sourceRef.current?.close();
    setIsStreaming(false);
  }, []);

  return { data, isStreaming, error, start, stop };
}
