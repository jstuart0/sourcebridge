// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

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

// Module-level context object — referentially stable across renders.
// Inlining `context: { pollInterval: POLL_INTERVAL_MS }` inside the hook
// produces a fresh object every render. urql treats that as the query args
// having changed, refetches, returns a new result identity, the consumer
// re-renders, and we loop forever — React surfaces it as the minified
// "Maximum update depth exceeded" (#301) crash that took down the app shell
// when this banner first shipped.
const SERVICE_HEALTH_CONTEXT = { pollInterval: POLL_INTERVAL_MS } as const;

/**
 * Polls the serviceHealth GraphQL query every 15 seconds and returns the
 * current platform health. Returns null while the first result is in flight
 * (so callers can treat null as "unknown, stay silent").
 */
export function useServiceHealth(): ServiceHealthStatus | null {
  const [result] = useQuery({
    query: SERVICE_HEALTH_QUERY,
    // cache-and-network: first paint uses any cached result (instant), then
    // re-fetches in the background so the banner reflects real state quickly.
    requestPolicy: "cache-and-network",
    context: SERVICE_HEALTH_CONTEXT,
  });

  if (!result.data?.serviceHealth) {
    return null;
  }

  return result.data.serviceHealth as ServiceHealthStatus;
}
