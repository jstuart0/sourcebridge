"use client";

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { AlertTriangle } from "lucide-react";
import { useServiceHealth } from "@/lib/service-health";

/**
 * ServiceHealthBanner renders a sticky warning strip at the very top of every
 * authenticated page when one or more backend services are unreachable.
 *
 * Design decisions:
 *   - Yellow / amber tones (same as AlertBanner "warning" variant elsewhere).
 *   - No dismiss button — it auto-disappears when health recovers.
 *   - Shows which specific subsystem is down so the user has context.
 *   - Renders nothing at all while the first health check is in flight, so
 *     there is no flash of the banner on initial page load.
 */
export function ServiceHealthBanner() {
  const health = useServiceHealth();

  // Unknown state (first fetch in progress) or all healthy → render nothing.
  if (!health || health.overall) {
    return null;
  }

  // Build the secondary line listing the specific failing subsystem(s).
  const failingSubsystems: string[] = [];
  if (!health.surreal) failingSubsystems.push("SurrealDB unreachable");
  if (!health.worker) failingSubsystems.push("AI worker unreachable");
  const detail = failingSubsystems.join(" · ");

  return (
    <div
      role="alert"
      aria-live="assertive"
      className="sticky top-0 z-50 flex items-center gap-3 border-b border-amber-500/40 bg-amber-500/10 px-4 py-2.5 text-amber-400 backdrop-blur"
    >
      <AlertTriangle className="h-4 w-4 shrink-0" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <span className="text-sm font-medium">
          Backend services are degraded. Some actions may fail. Retry in a moment.
        </span>
        {detail ? (
          <span className="ml-2 text-xs text-amber-400/70">{detail}</span>
        ) : null}
      </div>
    </div>
  );
}
