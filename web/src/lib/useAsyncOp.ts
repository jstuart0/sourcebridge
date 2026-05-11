"use client";

// LOAD-BEARING: useAsyncOp deliberately does NOT deduplicate concurrent
// run(key, fn) calls for the same key. Both Promises fire independently.
// A reference counter per key tracks in-flight count; isPending(key) returns
// true while any run for that key is in flight (count > 0). The exposed
// `pending` set reflects keys with count > 0 — callers see the same interface
// as a plain Set but concurrent same-key runs do not cancel each other.
// Callers that need dedup must guard at the call site:
//   `if (state.isPending(key)) return;`
// Changing this to deduplicate would silently break callers that intentionally
// allow concurrent mutations with shared key semantics (e.g., retries alongside
// in-flight requests).

import { useCallback, useRef, useState } from "react";

export type AsyncOpState<K extends string = string> = {
  /** Read-only set of currently in-flight operation keys (at least one run in flight). */
  pending: ReadonlySet<K>;
  /** True while at least one run() for this key is in flight. */
  isPending: (key: K) => boolean;
  /**
   * Adds key to pending, awaits fn(), removes key in finally (fires on both
   * success and error paths). Returns fn()'s resolved value; rethrows on error.
   */
  run: <T>(key: K, fn: () => Promise<T>) => Promise<T>;
};

export function useAsyncOp<K extends string = string>(): AsyncOpState<K> {
  const [pending, setPending] = useState<ReadonlySet<K>>(new Set<K>());
  // Reference counter: tracks how many concurrent run() calls are in flight per key.
  // Not exposed — callers only see the derived `pending` Set.
  const counts = useRef<Map<K, number>>(new Map());

  const isPending = useCallback(
    (key: K) => pending.has(key),
    [pending],
  );

  const run = useCallback(async <T>(key: K, fn: () => Promise<T>): Promise<T> => {
    const prev = counts.current.get(key) ?? 0;
    counts.current.set(key, prev + 1);
    setPending((s) => {
      const next = new Set(s);
      next.add(key);
      return next;
    });
    try {
      return await fn();
    } finally {
      const remaining = (counts.current.get(key) ?? 1) - 1;
      if (remaining <= 0) {
        counts.current.delete(key);
        setPending((s) => {
          const next = new Set(s);
          next.delete(key);
          return next;
        });
      } else {
        counts.current.set(key, remaining);
      }
    }
  }, []);

  return { pending, isPending, run };
}
