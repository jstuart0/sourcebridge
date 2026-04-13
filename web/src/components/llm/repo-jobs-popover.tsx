"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";

import { Button } from "@/components/ui/button";
import { normalizeActivityResponse } from "@/lib/llm/activity";
import { TOKEN_KEY } from "@/lib/token-key";
import { cn } from "@/lib/utils";

/**
 * RepoJobsPopover — compact "AI jobs" popover embedded in the repository
 * detail page header. Shows only jobs for the current repo by filtering
 * the /admin/llm/activity endpoint with ?repo_id=<repoId>.
 *
 * The popover follows the same Phase 2d UX principles as the full
 * Monitor page but with a much smaller footprint: one button in the
 * header, click to expand a panel with the last N jobs for this repo,
 * plus a "Full monitor →" link into /admin/llm.
 */

interface JobView {
  id: string;
  subsystem: string;
  job_type: string;
  status: "pending" | "generating" | "ready" | "failed" | "cancelled";
  progress: number;
  progress_message?: string;
  error_title?: string;
  error_hint?: string;
  error_code?: string;
  attached_requests?: number;
  reused_summaries?: number;
  leaf_cache_hits?: number;
  file_cache_hits?: number;
  package_cache_hits?: number;
  root_cache_hits?: number;
  queue_position?: number;
  queue_depth?: number;
  estimated_wait_ms?: number;
  elapsed_ms: number;
  updated_at: string;
}

interface ActivityResponse {
  active: JobView[];
  recent: JobView[];
}

const POLL_INTERVAL_MS = 3000;

function formatElapsed(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rem = seconds % 60;
  return rem > 0 ? `${minutes}m ${rem}s` : `${minutes}m`;
}

function statusColor(status: JobView["status"]): string {
  switch (status) {
    case "generating":
      return "bg-blue-500 animate-pulse";
    case "pending":
      return "bg-amber-500";
    case "ready":
      return "bg-green-500";
    case "failed":
      return "bg-red-500";
    case "cancelled":
      return "bg-gray-400";
  }
}

function formatQueueEta(ms?: number): string | null {
  if (!ms || ms <= 0) return null;
  const seconds = Math.ceil(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  return `${Math.ceil(seconds / 60)}m`;
}

function reuseLabel(job: JobView): string | null {
  const reused = job.reused_summaries ?? 0;
  if (reused <= 0) return null;
  return `${reused} reused`;
}

export function RepoJobsPopover({ repoId }: { repoId: string }) {
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<ActivityResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const pollRef = useRef<number | null>(null);

  const fetchActivity = useCallback(async () => {
    try {
      const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
      const res = await fetch(
        `/api/v1/admin/llm/activity?repo_id=${encodeURIComponent(repoId)}&limit=10`,
        {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
        }
      );
      if (!res.ok) throw new Error(`activity endpoint returned ${res.status}`);
      const body = normalizeActivityResponse((await res.json()) as ActivityResponse);
      setData(body);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load jobs");
    }
  }, [repoId]);

  // Only poll while the popover is open.
  useEffect(() => {
    if (!open) {
      if (pollRef.current) {
        window.clearInterval(pollRef.current);
        pollRef.current = null;
      }
      return;
    }
    void fetchActivity();
    pollRef.current = window.setInterval(() => {
      void fetchActivity();
    }, POLL_INTERVAL_MS);
    return () => {
      if (pollRef.current) {
        window.clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [open, fetchActivity]);

  // Close on outside click.
  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  const active = data?.active ?? [];
  const recent = data?.recent ?? [];
  const activeCount = active.length;

  return (
    <div className="relative" ref={containerRef}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="inline-flex items-center gap-2 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm font-medium text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-hover)]"
      >
        <span
          className={cn(
            "inline-block h-2 w-2 rounded-full",
            activeCount > 0 ? "bg-blue-500 animate-pulse" : "bg-gray-400"
          )}
        />
        AI jobs
        {activeCount > 0 ? (
          <span className="rounded-full bg-[color:rgba(59,130,246,0.12)] px-1.5 py-0.5 text-xs text-[color:var(--color-accent,#3b82f6)]">
            {activeCount}
          </span>
        ) : null}
      </button>

      {open ? (
        <div className="absolute right-0 z-20 mt-2 w-80 max-h-[28rem] overflow-y-auto rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-base)] p-3 shadow-lg">
          <header className="mb-2 flex items-center justify-between">
            <div>
              <p className="text-sm font-semibold text-[var(--text-primary)]">AI jobs</p>
              <p className="text-xs text-[var(--text-secondary)]">For this repository</p>
            </div>
            <Link
              href="/admin/llm"
              className="text-xs text-[var(--text-secondary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
            >
              Full monitor →
            </Link>
          </header>

          {error ? (
            <p className="text-xs text-[color:var(--color-error,#ef4444)]">{error}</p>
          ) : null}

          <section className="mt-2 space-y-1">
            <p className="text-[11px] uppercase tracking-wide text-[var(--text-tertiary)]">
              Now running
            </p>
            {active.length === 0 ? (
              <p className="text-xs text-[var(--text-secondary)]">
                No jobs are currently running.
              </p>
            ) : (
              active.map((job) => {
                const pct = Math.max(5, Math.round(job.progress * 100));
                return (
                  <div
                    key={job.id}
                    className="rounded-[var(--radius-sm)] border border-[var(--border-subtle)] p-2"
                  >
                    <div className="flex items-center justify-between text-xs">
                      <span className="font-medium text-[var(--text-primary)]">{job.job_type}</span>
                      <span className="text-[var(--text-tertiary)]">
                        {formatElapsed(job.elapsed_ms)}
                      </span>
                    </div>
                    <div className="mt-1 flex justify-between text-[11px] text-[var(--text-secondary)]">
                      <span className="truncate">
                        {job.progress_message || "Working…"}
                      </span>
                      <span>{pct}%</span>
                    </div>
                    {job.status === "pending" && job.queue_position ? (
                      <div className="mt-1 text-[11px] text-[var(--text-tertiary)]">
                        Queue #{job.queue_position}
                        {job.queue_depth ? ` of ${job.queue_depth}` : ""}
                        {formatQueueEta(job.estimated_wait_ms) ? ` · ~${formatQueueEta(job.estimated_wait_ms)}` : ""}
                      </div>
                    ) : null}
                    {job.attached_requests && job.attached_requests > 1 ? (
                      <div className="mt-1 text-[11px] text-[var(--text-tertiary)]">
                        Shared by {job.attached_requests} requests
                      </div>
                    ) : null}
                    {reuseLabel(job) ? (
                      <div className="mt-1 text-[11px] text-[var(--text-tertiary)]">
                        {reuseLabel(job)}
                      </div>
                    ) : null}
                    <div className="mt-1 h-1 overflow-hidden rounded-full bg-[var(--bg-subtle)]">
                      <div
                        className="h-full rounded-full bg-[color:var(--color-accent,#3b82f6)] transition-all"
                        style={{ width: `${pct}%` }}
                      />
                    </div>
                  </div>
                );
              })
            )}
          </section>

          <section className="mt-3 space-y-1">
            <p className="text-[11px] uppercase tracking-wide text-[var(--text-tertiary)]">
              Recent
            </p>
            {recent.length === 0 ? (
              <p className="text-xs text-[var(--text-secondary)]">No recent jobs.</p>
            ) : (
              recent.slice(0, 5).map((job) => (
                <div
                  key={job.id}
                  className="flex items-start gap-2 text-xs text-[var(--text-secondary)]"
                >
                  <span
                    className={cn(
                      "mt-[5px] inline-block h-2 w-2 shrink-0 rounded-full",
                      statusColor(job.status)
                    )}
                  />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-[var(--text-primary)]">{job.job_type}</p>
                    {job.status === "failed" && job.error_title ? (
                      <p className="truncate text-[color:var(--color-error,#ef4444)]">
                        {job.error_title}
                      </p>
                    ) : (
                      <p className="truncate">
                        {formatElapsed(job.elapsed_ms)}
                        {reuseLabel(job) ? ` · ${reuseLabel(job)}` : ""}
                      </p>
                    )}
                  </div>
                </div>
              ))
            )}
          </section>

          <div className="mt-3 border-t border-[var(--border-subtle)] pt-2">
            <Button
              variant="secondary"
              onClick={() => void fetchActivity()}
              className="w-full text-xs"
            >
              Refresh
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
