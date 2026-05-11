"use client";

import { posthog } from "@/lib/posthog";
import { CSRF_HEADER, getCSRFToken } from "@/lib/csrf-token-store";

export interface TelemetryEvent {
  event: string;
  repositoryId?: string;
  metadata?: Record<string, unknown>;
}

export function trackEvent(payload: TelemetryEvent) {
  if (typeof window === "undefined") return;

  // Honour Do Not Track before any capture() call — mirrors identifyUser.
  if (
    navigator.doNotTrack === "1" ||
    (window as Window & { doNotTrack?: string }).doNotTrack === "1"
  ) {
    return;
  }

  // Send to PostHog (if initialized)
  posthog.capture(payload.event, {
    repository_id: payload.repositoryId,
    ...payload.metadata,
  });

  // Send to the Go backend for server-side logging.
  //
  // Previously used navigator.sendBeacon which cannot set custom headers —
  // once Phase 2 gates /api/v1/telemetry on CSRF, every sendBeacon POST
  // would 403. Replaced with fetch + keepalive: true which preserves the
  // unload-time send semantics while allowing X-CSRF-Token injection.
  //
  // If getCSRFToken() returns undefined, we send with an empty header rather
  // than blocking — telemetry is best-effort and should never interrupt the
  // user. The backend will reject it once Phase 2 is enabled, but the client
  // shouldn't stall waiting for a token refresh on unload.
  const body = JSON.stringify(payload);
  void fetch("/api/v1/telemetry", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      [CSRF_HEADER]: getCSRFToken() ?? "",
    },
    body,
    keepalive: true,
  }).catch(() => undefined);
}
