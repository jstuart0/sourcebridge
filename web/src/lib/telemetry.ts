"use client";

export interface TelemetryEvent {
  event: string;
  repositoryId?: string;
  metadata?: Record<string, unknown>;
}

export function trackEvent(payload: TelemetryEvent) {
  if (typeof window === "undefined") return;

  const body = JSON.stringify(payload);
  if (typeof navigator !== "undefined" && typeof navigator.sendBeacon === "function") {
    const blob = new Blob([body], { type: "application/json" });
    navigator.sendBeacon("/api/v1/telemetry", blob);
    return;
  }

  void fetch("/api/v1/telemetry", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
    keepalive: true,
  }).catch(() => undefined);
}
