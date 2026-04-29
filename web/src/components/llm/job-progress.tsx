"use client";

import { cn } from "@/lib/utils";
import type { LLMJobView } from "@/lib/llm/job-types";

/**
 * Single source of truth for rendering an LLM job's progress in the UI.
 *
 * Replaces three near-identical implementations that lived in:
 *   - /admin/monitor (ActiveJobCard's progress block)
 *   - /repositories/[id] (renderKnowledgeProgress + understanding panel)
 *   - /components/llm/repo-jobs-popover (the per-repo popover)
 *
 * Past failure mode this prevents: each call site read different fields,
 * formatted percentages slightly differently, and disagreed about which
 * label to show during queued/generating/finalising — so the same job
 * could show 80% in the popover and "starting up" on the repo page.
 *
 * The component intentionally does not poll or fetch. Callers own the
 * data lifecycle and pass a single LLMJobView; everything below is pure.
 */

export type JobProgressVariant = "card" | "compact" | "panel";

interface JobProgressProps {
  job: LLMJobView;
  /**
   * Visual density. `card` is the admin-monitor / repo-page understanding
   * panel size, `compact` is the popover size, `panel` is the repo-page
   * cliff-notes generation panel.
   */
  variant?: JobProgressVariant;
  /**
   * When the job is `pending`, callers may want a custom label
   * (e.g. "Waiting for build slot" instead of the generic "Queued").
   */
  pendingLabel?: string;
  /**
   * Optional override for the headline status word. Defaults to the
   * standard mapping ("Queued" / "Generating" / "Finalizing" …).
   */
  statusOverride?: string;
  /**
   * When true, skip the heartbeat / elapsed status line. Useful in dense
   * layouts where the surrounding card already shows that information.
   */
  hideStatusLine?: boolean;
  className?: string;
}

export function JobProgress({
  job,
  variant = "card",
  pendingLabel,
  statusOverride,
  hideStatusLine = false,
  className,
}: JobProgressProps) {
  const pct = jobProgressPercent(job);
  const headline = statusOverride ?? jobHeadlineLabel(job, pendingLabel);
  const statusLine = hideStatusLine ? null : jobProgressStatusLine(job);

  const barClass = variant === "compact"
    ? "mt-1 h-1 overflow-hidden rounded-full bg-[var(--bg-subtle)]"
    : variant === "panel"
      ? "h-1.5 overflow-hidden rounded-full bg-[var(--bg-hover)]"
      : "h-1.5 overflow-hidden rounded-full bg-[var(--bg-subtle)]";
  const fillClass = "h-full rounded-full bg-[color:var(--color-accent,#3b82f6)] transition-all";
  const percentClass = variant === "compact"
    ? "text-[11px] text-[var(--text-secondary)]"
    : "text-xs text-[var(--text-secondary)]";

  return (
    <div className={cn("space-y-1", className)}>
      <div className={cn("flex items-center justify-between", percentClass)}>
        <span className="truncate">{headline}</span>
        <span>{pct}%</span>
      </div>
      {statusLine ? (
        <div className="text-[11px] text-[var(--text-tertiary)] truncate">{statusLine}</div>
      ) : null}
      <div className={barClass}>
        <div className={fillClass} style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}

/** Headline word — "Queued" / "Generating" / "Finalizing" / "Working…". */
export function jobHeadlineLabel(job: LLMJobView, pendingLabel?: string): string {
  if (job.status === "pending") return pendingLabel ?? "Queued";
  if (job.status === "generating") {
    if (job.progress >= 0.95) return "Finalizing";
    return "Generating";
  }
  if (job.status === "ready") return "Completed";
  if (job.status === "failed") return job.error_title || "Failed";
  if (job.status === "cancelled") return "Cancelled";
  return job.status;
}

/**
 * Status-line text under the headline — heartbeat / elapsed / queue
 * info. Returns null if the job is in a terminal state and there's no
 * useful detail to render.
 */
export function jobProgressStatusLine(job: LLMJobView): string | null {
  if (job.progress_message?.trim()) return job.progress_message.trim();
  if (job.status === "pending" && job.queue_position) {
    const eta = formatQueueEta(job.estimated_wait_ms);
    const slot = `Queue #${job.queue_position}`;
    if (job.queue_depth) {
      return eta ? `${slot} of ${job.queue_depth} · ~${eta}` : `${slot} of ${job.queue_depth}`;
    }
    return eta ? `${slot} · ~${eta}` : slot;
  }
  if (job.status === "generating") {
    const heartbeat = formatHeartbeatAge(job.updated_at);
    const elapsed = formatElapsedMs(job.elapsed_ms);
    if (heartbeat && elapsed) return `alive ${heartbeat} · elapsed ${elapsed}`;
    if (heartbeat) return `alive ${heartbeat}`;
    if (elapsed) return `elapsed ${elapsed}`;
  }
  if (job.status === "failed") return job.error_hint || job.error_title || null;
  return null;
}

/** Reused / cache-hit summary line used by the popover and monitor. */
export function jobReuseSummary(job: LLMJobView): string | null {
  const reused = job.reused_summaries ?? 0;
  const cached = job.cached_nodes_loaded ?? 0;
  const parts = [
    cached > 0 ? `${cached} cached loaded` : null,
    job.resume_stage ? `resume ${job.resume_stage}` : null,
    job.leaf_cache_hits ? `${job.leaf_cache_hits} leaf` : null,
    job.file_cache_hits ? `${job.file_cache_hits} file` : null,
    job.package_cache_hits ? `${job.package_cache_hits} package` : null,
    job.root_cache_hits ? `${job.root_cache_hits} root` : null,
  ].filter(Boolean) as string[];
  if (reused <= 0 && parts.length === 0) return null;
  if (reused > 0) {
    return parts.length > 0 ? `${reused} reused · ${parts.join(" · ")}` : `${reused} reused`;
  }
  return parts.join(" · ");
}

/** Always renders at least 5% so users see the bar even at p≈0. */
export function jobProgressPercent(job: LLMJobView): number {
  return Math.max(5, Math.round(job.progress * 100));
}

export function formatQueueEta(ms?: number | null): string | null {
  if (!ms || ms <= 0) return null;
  const seconds = Math.ceil(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  return `${Math.ceil(seconds / 60)}m`;
}

export function formatHeartbeatAge(iso?: string | null): string | null {
  if (!iso) return null;
  const ts = new Date(iso).getTime();
  if (!ts || Number.isNaN(ts)) return null;
  const diff = Math.max(0, Date.now() - ts);
  const seconds = Math.floor(diff / 1000);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ago`;
}

export function formatElapsedMs(ms?: number | null): string | null {
  if (!ms || ms <= 0) return null;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rem = seconds % 60;
  if (minutes < 60) return rem > 0 ? `${minutes}m ${rem}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remMin = minutes % 60;
  return remMin > 0 ? `${hours}h ${remMin}m` : `${hours}h`;
}
