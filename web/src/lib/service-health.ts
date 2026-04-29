// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { useEffect } from "react";
import { useQuery } from "urql";
import { SERVICE_HEALTH_QUERY } from "@/lib/graphql/queries";

export interface ServiceHealthStatus {
  overall: boolean;
  surreal: boolean;
  worker: boolean;
  message: string;
  checkedAt: string;
}

// Poll interval in milliseconds. Long enough to avoid DB pressure, short
// enough that a user notices the banner within ~15 s of an outage starting.
const POLL_INTERVAL_MS = 15_000;

/**
 * Polls the serviceHealth GraphQL query every 15 seconds and returns the
 * current platform health. Returns null while the first result is in flight
 * (so callers can treat null as "unknown, stay silent").
 *
 * urql does not have a built-in `pollInterval` option (that's an Apollo
 * Client API — a previous version of this hook tried `context.pollInterval`
 * and silently shipped a one-shot fetch that never refreshed, which left the
 * banner stuck on stale "AI worker unreachable" forever after a deploy).
 * Drive the refetch ourselves with a setInterval that calls the
 * reexecuteQuery callback `useQuery` returns.
 */
export function useServiceHealth(): ServiceHealthStatus | null {
  const [result, reexecuteQuery] = useQuery({
    query: SERVICE_HEALTH_QUERY,
    // cache-and-network: first paint uses any cached result (instant), then
    // re-fetches in the background so the banner reflects real state quickly.
    requestPolicy: "cache-and-network",
  });

  useEffect(() => {
    const id = window.setInterval(() => {
      reexecuteQuery({ requestPolicy: "network-only" });
    }, POLL_INTERVAL_MS);
    return () => window.clearInterval(id);
  }, [reexecuteQuery]);

  if (!result.data?.serviceHealth) {
    return null;
  }

  return result.data.serviceHealth as ServiceHealthStatus;
}
