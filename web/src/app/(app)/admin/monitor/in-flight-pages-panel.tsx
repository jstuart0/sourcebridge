"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";

// Flat fallback threshold (ms) used when fewer than 3 pages have completed.
// Matches the plan's 300s rationale: upper bound for a page within the 5-min
// run TimeBudget; 120s would produce spurious warn-dots on legitimate slow
// architecture pages.
const WARN_THRESHOLD_FLAT_MS = 300_000;

const POLL_INTERVAL_MS = 2000;

interface InFlightPage {
  page_id: string;
  template_id: string;
  attempt: number;
  started_at: string;
  elapsed_ms: number;
}

interface InFlightResponse {
  job_id: string;
  as_of: string;
  median_completed_ms: number;
  median_completed_ms_known: boolean;
  pages: InFlightPage[];
}

/** Formats elapsed milliseconds as "Xs" or "Xm Ys". */
export function formatElapsedInFlight(ms: number): string {
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${s}s`;
}

/**
 * Returns true when a page's elapsed time exceeds the warn threshold.
 * If the run's median is known (at least 3 completions), warn at 3× median.
 * Otherwise, fall back to the flat 300s threshold.
 */
function isWarnElapsed(
  elapsedMs: number,
  medianMs: number,
  medianKnown: boolean
): boolean {
  if (medianKnown && medianMs > 0) {
    return elapsedMs > 3 * medianMs;
  }
  return elapsedMs > WARN_THRESHOLD_FLAT_MS;
}

/**
 * InFlightPagesPanel polls /api/v1/admin/llm/jobs/{jobId}/livingwiki/in-flight
 * every 2s and renders the currently in-flight pages as a compact table.
 *
 * Mount only when the parent job row is expanded AND the job is a running
 * living_wiki job. The component self-cleans its poll on unmount.
 */
export function InFlightPagesPanel({ jobId }: { jobId: string }) {
  const [data, setData] = useState<InFlightResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const pollRef = useRef<number | null>(null);

  const fetchInFlight = useCallback(async () => {
    try {
      const res = await authFetch(
        `/api/v1/admin/llm/jobs/${encodeURIComponent(jobId)}/livingwiki/in-flight`
      );
      if (res.status === 503) {
        // Orchestrator unavailable — feature not deployed yet.
        setError("in-flight data unavailable");
        return;
      }
      if (!res.ok) {
        throw new Error(`in-flight endpoint returned ${res.status}`);
      }
      const body = (await res.json()) as InFlightResponse;
      // Ensure pages is always a non-null array.
      if (!Array.isArray(body.pages)) {
        body.pages = [];
      }
      setData(body);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load in-flight data");
    }
  }, [jobId]);

  useEffect(() => {
    void fetchInFlight();
    pollRef.current = window.setInterval(() => {
      void fetchInFlight();
    }, POLL_INTERVAL_MS);
    return () => {
      if (pollRef.current !== null) {
        window.clearInterval(pollRef.current);
      }
    };
  }, [fetchInFlight]);

  if (error) {
    return (
      <div className="mt-3 rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-subtle)] px-3 py-2">
        <p className="text-xs text-[var(--text-tertiary)]">{error}</p>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="mt-3 rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-subtle)] px-3 py-2">
        <p className="text-xs text-[var(--text-tertiary)]">Loading in-flight pages…</p>
      </div>
    );
  }

  const pages = data.pages ?? [];

  return (
    <div className="mt-3 rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-subtle)] p-3">
      <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--text-tertiary)]">
        In-flight pages{pages.length > 0 ? ` (${pages.length})` : ""}
      </p>

      {pages.length === 0 ? (
        <p className="text-xs text-[var(--text-secondary)]">
          No pages currently in-flight for this job.
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-[var(--border-default)] text-left text-[10px] uppercase tracking-wide text-[var(--text-tertiary)]">
                <th className="pb-1 pr-3">Page</th>
                <th className="pb-1 pr-3">Template</th>
                <th className="pb-1 pr-3">Attempt</th>
                <th className="pb-1 pr-3">Started</th>
                <th className="pb-1">Elapsed</th>
              </tr>
            </thead>
            <tbody>
              {pages.map((page) => {
                const warn = isWarnElapsed(
                  page.elapsed_ms,
                  data.median_completed_ms,
                  data.median_completed_ms_known
                );
                return (
                  <tr
                    key={page.page_id}
                    className="border-b border-[var(--border-subtle)] last:border-0"
                  >
                    <td className="py-1.5 pr-3 font-mono text-[var(--text-primary)]">
                      {page.page_id}
                    </td>
                    <td className="py-1.5 pr-3 text-[var(--text-secondary)]">
                      {page.template_id}
                    </td>
                    <td className="py-1.5 pr-3 text-[var(--text-secondary)]">
                      {page.attempt}
                    </td>
                    <td className="py-1.5 pr-3 text-[var(--text-secondary)]">
                      {new Date(page.started_at).toLocaleTimeString([], {
                        hour: "2-digit",
                        minute: "2-digit",
                        second: "2-digit",
                      })}
                    </td>
                    <td className="py-1.5">
                      <span className="flex items-center gap-1.5">
                        <span className={cn(warn ? "text-amber-600 dark:text-amber-400" : "text-[var(--text-secondary)]")}>
                          {formatElapsedInFlight(page.elapsed_ms)}
                        </span>
                        {warn && (
                          <span
                            className="inline-block h-2 w-2 rounded-full bg-amber-400"
                            title={
                              data.median_completed_ms_known
                                ? `Elapsed exceeds 3× median for this run (median: ${formatElapsedInFlight(data.median_completed_ms)})`
                                : `Elapsed exceeds ${formatElapsedInFlight(WARN_THRESHOLD_FLAT_MS)} (flat threshold)`
                            }
                            aria-label="Slow page warning"
                          />
                        )}
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
