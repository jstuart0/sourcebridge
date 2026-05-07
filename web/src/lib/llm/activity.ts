"use client";

/**
 * LLMGateEntry mirrors the monitorGateEntry JSON shape from the Go REST handler.
 * One entry per (provider, base_url_normalized, kind) gate in the worker registry.
 *
 * Consumers must treat zero values as "unknown/uncapped" for max_concurrent
 * (matches the (known=true, calls=0) unbounded encoding from GetProviderCapabilities).
 */
export interface LLMGateEntry {
  provider: string;
  base_url_normalized: string;
  kind: "llm" | "embedding";
  in_flight: number;
  queued: number;
  max_concurrent: number;
  retries_since_start: number;
  recent_429_count: number;
  tokens_per_second: number;
  rpm?: number;
}

interface ActivityEnvelope<TJob> {
  active?: TJob[];
  recent?: TJob[];
  active_jobs?: TJob[];
  recent_jobs?: TJob[];
}

export function normalizeActivityResponse<TJob, TBody extends ActivityEnvelope<TJob>>(
  body: TBody
): TBody & { active: TJob[]; recent: TJob[] } {
  const active = Array.isArray(body.active)
    ? body.active
    : Array.isArray(body.active_jobs)
      ? body.active_jobs
      : [];
  const recent = Array.isArray(body.recent)
    ? body.recent
    : Array.isArray(body.recent_jobs)
      ? body.recent_jobs
      : [];
  return {
    ...body,
    active,
    recent,
  };
}
