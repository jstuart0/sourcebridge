"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { StatCard } from "@/components/ui/stat-card";
import { normalizeActivityResponse } from "@/lib/llm/activity";
import { disableJobAlerts, enableJobAlerts, jobAlertsEnabled, notifyJobEvent } from "@/lib/notifications";
import { TOKEN_KEY } from "@/lib/token-key";
import { cn } from "@/lib/utils";

/**
 * Generation Monitor page (/admin/llm).
 *
 * Live view of every LLM job the orchestrator is running. Driven by the
 * Phase 2c REST endpoints:
 *   - /api/v1/admin/llm/activity   (bundled snapshot — polled every 2s)
 *   - /api/v1/admin/llm/stream     (SSE live feed)
 *
 * UX principles from the plan:
 *  1. Three zones only: green/yellow/red health banner at the top,
 *     "Now running" cards in the middle, "Recent history" table at the
 *     bottom. No tabs, no filters visible by default.
 *  2. Empty states teach instead of leaving the operator staring at a
 *     blank table.
 *  3. Every error row shows a human-readable title + remediation hint
 *     that the backend pre-computed in admin_llm_monitor.go.
 *  4. Glance test: an operator should know the system's state in under
 *     10 seconds of looking at this page.
 */

type Status = "pending" | "generating" | "ready" | "failed" | "cancelled";

interface JobView {
  id: string;
  subsystem: string;
  job_type: string;
  target_key: string;
  strategy?: string;
  model?: string;
  priority?: "interactive" | "maintenance" | "prewarm";
  generation_mode?: "classic" | "understanding_first";
  status: Status;
  progress: number;
  progress_phase?: string;
  progress_message?: string;
  error_code?: string;
  error_message?: string;
  error_title?: string;
  error_hint?: string;
  retry_count: number;
  max_attempts: number;
  attached_requests: number;
  input_tokens: number;
  output_tokens: number;
  snapshot_bytes: number;
  reused_summaries?: number;
  leaf_cache_hits?: number;
  file_cache_hits?: number;
  package_cache_hits?: number;
  root_cache_hits?: number;
  cached_nodes_loaded?: number;
  total_nodes?: number;
  resume_stage?: string;
  skipped_leaf_units?: number;
  skipped_file_units?: number;
  skipped_package_units?: number;
  skipped_root_units?: number;
  artifact_id?: string;
  repo_id?: string;
  queue_position?: number;
  queue_depth?: number;
  estimated_wait_ms?: number;
  elapsed_ms: number;
  created_at: string;
  started_at?: string;
  updated_at: string;
  completed_at?: string;
}

interface JobLogView {
  id: string;
  job_id: string;
  level: "debug" | "info" | "warn" | "error";
  phase?: string;
  event: string;
  message: string;
  payload_json?: string;
  sequence: number;
  created_at: string;
}

interface HealthPayload {
  status: "healthy" | "degraded" | "unhealthy";
  summary: string;
  worker_connected: boolean;
  active_count: number;
  recent_failed: number;
  recent_succeeded: number;
}

interface MetricsSnapshot {
  by_subsystem?: Record<
    string,
    {
      total: number;
      succeeded: number;
      failed: number;
      cancelled: number;
      p50_latency_ms: number;
      p95_latency_ms: number;
      success_rate: number;
    }
  >;
  overall?: {
    total: number;
    succeeded: number;
    failed: number;
    p50_latency_ms: number;
    p95_latency_ms: number;
    success_rate: number;
  };
}

interface ActivityResponse {
  health: HealthPayload;
  active: JobView[];
  recent: JobView[];
  metrics: MetricsSnapshot;
  control: {
    intake_paused: boolean;
  };
  stats: {
    in_flight: number;
    queue_depth: number;
    gate_waiting?: number;
    total_waiting?: number;
    max_concurrency: number;
    recent_reused_summaries?: number;
    active_classic?: number;
    active_understanding_first?: number;
    recent_classic?: number;
    recent_understanding_first?: number;
    pending_interactive?: number;
    pending_maintenance?: number;
    pending_prewarm?: number;
  };
}

const POLL_INTERVAL_MS = 2000;

function formatElapsed(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rem = seconds % 60;
  return rem > 0 ? `${minutes}m ${rem}s` : `${minutes}m`;
}

function formatRelative(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso).getTime();
  if (Number.isNaN(d)) return "";
  const diff = Date.now() - d;
  if (diff < 0) return "just now";
  if (diff < 60_000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return `${Math.floor(diff / 86_400_000)}d ago`;
}

function formatTime(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function statusDot(status: Status): string {
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
    default:
      return "bg-gray-400";
  }
}

function formatQueueEta(ms?: number): string | null {
  if (!ms || ms <= 0) return null;
  const seconds = Math.ceil(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  return `${Math.ceil(seconds / 60)}m`;
}

function jobReuseSummary(job: Pick<JobView, "reused_summaries" | "leaf_cache_hits" | "file_cache_hits" | "package_cache_hits" | "root_cache_hits" | "cached_nodes_loaded" | "resume_stage">): string | null {
  const reused = job.reused_summaries ?? 0;
  const cached = job.cached_nodes_loaded ?? 0;
  const parts = [
    cached > 0 ? `${cached} cached loaded` : null,
    job.resume_stage ? `resume ${job.resume_stage}` : null,
    job.leaf_cache_hits ? `${job.leaf_cache_hits} leaf` : null,
    job.file_cache_hits ? `${job.file_cache_hits} file` : null,
    job.package_cache_hits ? `${job.package_cache_hits} package` : null,
    job.root_cache_hits ? `${job.root_cache_hits} root` : null,
  ].filter(Boolean);
  if (reused <= 0 && parts.length === 0) return null;
  if (reused > 0) {
    return parts.length > 0 ? `${reused} reused · ${parts.join(" · ")}` : `${reused} reused`;
  }
  return parts.join(" · ");
}

function formatGenerationMode(mode?: JobView["generation_mode"]): string | null {
  if (!mode) return null;
  return mode === "classic" ? "Classic" : "Understanding First";
}

function formatPriority(priority?: JobView["priority"]): string | null {
  if (!priority) return null;
  switch (priority) {
    case "maintenance":
      return "Maintenance";
    case "prewarm":
      return "Prewarm";
    default:
      return "Interactive";
  }
}

function healthStyle(status: HealthPayload["status"]) {
  switch (status) {
    case "healthy":
      return {
        border: "border-[color:var(--color-success,#22c55e)]",
        bg: "bg-[color:rgba(34,197,94,0.1)]",
        text: "text-[color:var(--color-success,#22c55e)]",
        icon: "●",
      };
    case "degraded":
      return {
        border: "border-[color:var(--color-warning,#eab308)]",
        bg: "bg-[color:rgba(234,179,8,0.1)]",
        text: "text-[color:var(--color-warning,#eab308)]",
        icon: "▲",
      };
    case "unhealthy":
      return {
        border: "border-[color:var(--color-error,#ef4444)]",
        bg: "bg-[color:rgba(239,68,68,0.1)]",
        text: "text-[color:var(--color-error,#ef4444)]",
        icon: "■",
      };
  }
}

function logLevelStyle(level: JobLogView["level"]): string {
  switch (level) {
    case "error":
      return "border-[color:var(--color-error,#ef4444)] text-[color:var(--color-error,#ef4444)]";
    case "warn":
      return "border-amber-500 text-amber-600";
    case "debug":
      return "border-[var(--border-default)] text-[var(--text-tertiary)]";
    case "info":
    default:
      return "border-[var(--border-default)] text-[var(--text-secondary)]";
  }
}

export default function MonitorPage() {
  const [data, setData] = useState<ActivityResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<JobView | null>(null);
  const [alertsEnabled, setAlertsEnabled] = useState(false);
  const pollRef = useRef<number | null>(null);
  const seenTerminalRef = useRef<Record<string, string>>({});

  const fetchActivity = useCallback(async () => {
    try {
      const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
      const res = await fetch("/api/v1/admin/llm/activity", {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      });
      if (!res.ok) {
        throw new Error(`activity endpoint returned ${res.status}`);
      }
      const body = normalizeActivityResponse((await res.json()) as ActivityResponse);
      setData(body);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load activity");
    }
  }, []);

  const cancelJob = useCallback(async (jobId: string) => {
    const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
    const res = await fetch(`/api/v1/admin/llm/jobs/${encodeURIComponent(jobId)}/cancel`, {
      method: "POST",
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!res.ok) throw new Error(`cancel returned ${res.status}`);
    await fetchActivity();
  }, [fetchActivity]);

  const setIntakePaused = useCallback(async (paused: boolean) => {
    const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
    const res = await fetch("/api/v1/admin/llm/control", {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify({ intake_paused: paused }),
    });
    if (!res.ok) throw new Error(`queue control returned ${res.status}`);
    await fetchActivity();
  }, [fetchActivity]);

  const drainQueue = useCallback(async () => {
    const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
    const res = await fetch("/api/v1/admin/llm/drain", {
      method: "POST",
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!res.ok) throw new Error(`drain returned ${res.status}`);
    const body = await res.json() as { cancelled_pending?: number };
    notifyJobEvent("Queue drained", `Cancelled ${body.cancelled_pending ?? 0} pending job(s).`);
    await fetchActivity();
  }, [fetchActivity]);

  const toggleAlerts = useCallback(async () => {
    if (alertsEnabled) {
      disableJobAlerts();
      setAlertsEnabled(false);
      return;
    }
    const permission = await enableJobAlerts();
    if (permission === "granted") {
      setAlertsEnabled(true);
      notifyJobEvent("Desktop alerts enabled", "You will now get queue completion and failure alerts in this browser.");
      return;
    }
    notifyJobEvent("Desktop alerts unavailable", permission === "unsupported" ? "This browser does not support desktop notifications." : "Notification permission was not granted.");
  }, [alertsEnabled]);

  // Poll the activity endpoint every 2 seconds while the tab is visible.
  // Switch to a 10-second interval when hidden so we don't burn bandwidth
  // in the background.
  useEffect(() => {
    setAlertsEnabled(jobAlertsEnabled());
  }, []);

  useEffect(() => {
    fetchActivity();
    const schedule = () => {
      if (pollRef.current) {
        window.clearInterval(pollRef.current);
      }
      const interval = document.visibilityState === "visible" ? POLL_INTERVAL_MS : 10_000;
      pollRef.current = window.setInterval(() => {
        void fetchActivity();
      }, interval);
    };
    schedule();
    const onVisibilityChange = () => schedule();
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      if (pollRef.current) window.clearInterval(pollRef.current);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [fetchActivity]);

  useEffect(() => {
    if (!data?.recent?.length) return;
    const now = Date.now()
    for (const job of data.recent) {
      if (job.status !== "ready" && job.status !== "failed" && job.status !== "cancelled") continue;
      const marker = `${job.status}:${job.updated_at}`;
      if (seenTerminalRef.current[job.id] === marker) continue;
      seenTerminalRef.current[job.id] = marker;
      const updatedMs = new Date(job.updated_at).getTime();
      if (!updatedMs || now-updatedMs > 20_000) continue;
      if (job.status === "ready") {
        notifyJobEvent("AI job completed", `${job.job_type} finished successfully.`);
      } else if (job.status === "failed") {
        notifyJobEvent("AI job failed", job.error_title || `${job.job_type} failed.`);
      } else {
        notifyJobEvent("AI job cancelled", `${job.job_type} was cancelled.`);
      }
    }
  }, [data?.recent]);

  const stats = data?.stats;
  const overall = data?.metrics?.overall;

  const saturation = useMemo(() => {
    if (!stats) return null;
    const pct = stats.max_concurrency > 0 ? (stats.in_flight / stats.max_concurrency) * 100 : 0;
    return Math.round(pct);
  }, [stats]);

  return (
    <PageFrame>
      <PageHeader
        title="Generation Monitor"
        description="Live view of every AI job SourceBridge is running — what's working, what's queued, what failed."
      />

      {/* Zone 1 — Health banner */}
      <HealthBanner health={data?.health} error={error} />

      {/* Stats strip */}
      <div className="flex flex-wrap gap-4">
        <StatCard
          label="In flight"
          value={stats ? `${stats.in_flight} / ${stats.max_concurrency}` : "—"}
          detail={saturation !== null ? `${saturation}% of capacity` : undefined}
        />
        <StatCard
          label="Outer queue"
          value={stats?.queue_depth ?? "—"}
          detail="Jobs not yet admitted to execution"
        />
        <StatCard
          label="Waiting on slot"
          value={stats?.gate_waiting ?? "—"}
          detail="Jobs admitted but blocked on a knowledge slot"
        />
        <StatCard
          label="Succeeded (last hour)"
          value={overall?.succeeded ?? "—"}
          detail={overall?.p50_latency_ms ? `p50 ${formatElapsed(overall.p50_latency_ms)}` : undefined}
        />
        <StatCard
          label="Failed (last hour)"
          value={overall?.failed ?? "—"}
          detail={overall?.p95_latency_ms ? `p95 ${formatElapsed(overall.p95_latency_ms)}` : undefined}
        />
        <StatCard
          label="Reused summaries"
          value={stats?.recent_reused_summaries ?? "—"}
          detail="Cache hits reused across recent jobs"
        />
        <StatCard
          label="Understanding-first"
          value={stats ? `${stats.active_understanding_first ?? 0} active` : "—"}
          detail={stats ? `${stats.recent_understanding_first ?? 0} recent` : undefined}
        />
        <StatCard
          label="Classic"
          value={stats ? `${stats.active_classic ?? 0} active` : "—"}
          detail={stats ? `${stats.recent_classic ?? 0} recent` : undefined}
        />
        <StatCard
          label="Background backlog"
          value={stats ? `${(stats.pending_maintenance ?? 0) + (stats.pending_prewarm ?? 0)}` : "—"}
          detail={stats ? `${stats.pending_maintenance ?? 0} maint · ${stats.pending_prewarm ?? 0} prewarm` : undefined}
        />
      </div>

      {/* Zone 2 — Now running */}
      <Panel>
        <header className="mb-4 flex items-center justify-between">
          <div>
            <h2 className="text-lg font-semibold text-[var(--text-primary)]">Now running</h2>
            <p className="text-sm text-[var(--text-secondary)]">
              Jobs currently executing or waiting in the queue.
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" onClick={() => void toggleAlerts()}>
              {alertsEnabled ? "Desktop alerts on" : "Enable desktop alerts"}
            </Button>
            <Button
              variant="secondary"
              onClick={() => void setIntakePaused(!data?.control?.intake_paused)}
            >
              {data?.control?.intake_paused ? "Resume intake" : "Pause intake"}
            </Button>
            <Button variant="secondary" onClick={() => void drainQueue()}>
              Drain pending
            </Button>
            <Button variant="secondary" onClick={() => void fetchActivity()}>
              Refresh
            </Button>
          </div>
        </header>
        {data?.control?.intake_paused ? (
          <div className="mb-4 rounded-[var(--radius-md)] border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-[var(--text-primary)]">
            Queue intake is paused. Running jobs continue; new jobs will be rejected until intake is resumed.
          </div>
        ) : null}
        <ActiveJobsSection jobs={data?.active ?? []} onSelect={setSelected} onCancel={cancelJob} />
      </Panel>

      {/* Zone 3 — Recent history */}
      <Panel>
        <header className="mb-4">
          <h2 className="text-lg font-semibold text-[var(--text-primary)]">Recent history</h2>
          <p className="text-sm text-[var(--text-secondary)]">
            Jobs that finished in the last hour. Click a row for details.
          </p>
        </header>
        <RecentHistoryTable jobs={data?.recent ?? []} onSelect={setSelected} />
      </Panel>

      {selected ? <JobDetailDrawer job={selected} onClose={() => setSelected(null)} /> : null}
    </PageFrame>
  );
}

function HealthBanner({ health, error }: { health?: HealthPayload; error: string | null }) {
  if (error) {
    const style = healthStyle("unhealthy");
    return (
      <div
        className={cn(
          "flex items-center gap-3 rounded-[var(--radius-md)] border px-4 py-3",
          style.border,
          style.bg
        )}
      >
        <span className={cn("text-2xl", style.text)}>{style.icon}</span>
        <div className="flex-1">
          <p className={cn("text-sm font-semibold", style.text)}>Monitor unavailable</p>
          <p className="text-xs text-[var(--text-secondary)]">{error}</p>
        </div>
      </div>
    );
  }
  if (!health) {
    return (
      <div className="rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-subtle)] px-4 py-3">
        <p className="text-sm text-[var(--text-secondary)]">Loading health status…</p>
      </div>
    );
  }
  const style = healthStyle(health.status);
  return (
    <div
      className={cn(
        "flex items-center gap-3 rounded-[var(--radius-md)] border px-4 py-3",
        style.border,
        style.bg
      )}
    >
      <span className={cn("text-2xl", style.text)}>{style.icon}</span>
      <div className="flex-1">
        <p className={cn("text-sm font-semibold uppercase tracking-wide", style.text)}>
          {health.status}
        </p>
        <p className="text-sm text-[var(--text-primary)]">{health.summary}</p>
      </div>
    </div>
  );
}

function ActiveJobsSection({
  jobs,
  onSelect,
  onCancel,
}: {
  jobs: JobView[];
  onSelect: (job: JobView) => void;
  onCancel: (jobId: string) => Promise<void>;
}) {
  if (jobs.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 rounded-[var(--radius-md)] border border-dashed border-[var(--border-default)] py-10 text-center">
        <p className="text-sm font-medium text-[var(--text-primary)]">No jobs right now.</p>
        <p className="max-w-md text-xs text-[var(--text-secondary)]">
          Generate cliff notes for a repository and you&apos;ll see it here — with a live
          progress bar, elapsed time, and a cancel button.
        </p>
        <Link href="/repositories">
          <Button variant="secondary">Go to repositories →</Button>
        </Link>
      </div>
    );
  }
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {jobs.map((job) => (
        <ActiveJobCard key={job.id} job={job} onSelect={onSelect} onCancel={onCancel} />
      ))}
    </div>
  );
}

function ActiveJobCard({
  job,
  onSelect,
  onCancel,
}: {
  job: JobView;
  onSelect: (job: JobView) => void;
  onCancel: (jobId: string) => Promise<void>;
}) {
  const progressPct = Math.max(5, Math.round(job.progress * 100));
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={() => onSelect(job)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSelect(job);
        }
      }}
      className="flex flex-col gap-2 rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4 text-left transition-colors hover:border-[var(--border-strong)]"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold text-[var(--text-primary)]">
            {job.job_type}
          </p>
          <p className="truncate text-xs text-[var(--text-secondary)]">{job.subsystem}</p>
        </div>
        <span className={cn("mt-1 inline-block h-2.5 w-2.5 rounded-full", statusDot(job.status))} />
      </div>

      <div className="space-y-1">
        <div className="flex justify-between text-xs text-[var(--text-secondary)]">
          <span>{job.progress_message || job.progress_phase || "Working…"}</span>
          <span>{progressPct}%</span>
        </div>
        {(job.status === "pending" || job.progress_phase === "queued") && job.queue_position ? (
          <div className="text-[11px] text-[var(--text-tertiary)]">
            {job.progress_phase === "queued" && job.status !== "pending" ? "Slot wait" : "Queue"} #{job.queue_position}
            {job.queue_depth ? ` of ${job.queue_depth}` : ""}
            {formatQueueEta(job.estimated_wait_ms) ? ` · ~${formatQueueEta(job.estimated_wait_ms)}` : ""}
          </div>
        ) : null}
        <div className="h-1.5 overflow-hidden rounded-full bg-[var(--bg-subtle)]">
          <div
            className="h-full rounded-full bg-[color:var(--color-accent,#3b82f6)] transition-all"
            style={{ width: `${progressPct}%` }}
          />
        </div>
      </div>

      <div className="flex items-center justify-between gap-2 text-xs text-[var(--text-tertiary)]">
        <span>{formatElapsed(job.elapsed_ms)}</span>
        <span className="flex items-center gap-2">
          {formatGenerationMode(job.generation_mode) ? <span>{formatGenerationMode(job.generation_mode)}</span> : null}
          {formatPriority(job.priority) ? <span>{formatPriority(job.priority)}</span> : null}
          {job.retry_count > 0 ? <span>retry {job.retry_count}</span> : null}
          {job.attached_requests > 1 ? <span>shared {job.attached_requests}</span> : null}
        </span>
      </div>
      {jobReuseSummary(job) ? (
        <div className="text-[11px] text-[var(--text-tertiary)]">{jobReuseSummary(job)}</div>
      ) : null}
      <div className="pt-1">
        <Button
          variant="secondary"
          className="w-full"
          onClick={(e) => {
            e.stopPropagation();
            void onCancel(job.id);
          }}
        >
          Cancel
        </Button>
      </div>
    </div>
  );
}

function RecentHistoryTable({
  jobs,
  onSelect,
}: {
  jobs: JobView[];
  onSelect: (job: JobView) => void;
}) {
  if (jobs.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 rounded-[var(--radius-md)] border border-dashed border-[var(--border-default)] py-8 text-center">
        <p className="text-sm text-[var(--text-secondary)]">
          No jobs have finished in the last hour.
        </p>
      </div>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-[var(--border-default)] text-left text-xs uppercase tracking-wide text-[var(--text-tertiary)]">
            <th className="py-2 pr-3">Status</th>
            <th className="py-2 pr-3">Job</th>
            <th className="py-2 pr-3">Subsystem</th>
            <th className="py-2 pr-3">Duration</th>
            <th className="py-2 pr-3">Finished</th>
            <th className="py-2 pr-3">Error</th>
          </tr>
        </thead>
        <tbody>
          {jobs.map((job) => (
            <tr
              key={job.id}
              className="cursor-pointer border-b border-[var(--border-subtle)] transition-colors hover:bg-[var(--bg-subtle)]"
              onClick={() => onSelect(job)}
            >
              <td className="py-3 pr-3">
                <span className="flex items-center gap-2">
                  <span
                    className={cn(
                      "inline-block h-2 w-2 rounded-full",
                      statusDot(job.status)
                    )}
                  />
                  <span className="text-xs uppercase tracking-wide text-[var(--text-secondary)]">
                    {job.status}
                  </span>
                </span>
              </td>
              <td className="py-3 pr-3 text-[var(--text-primary)]">{job.job_type}</td>
              <td className="py-3 pr-3 text-[var(--text-secondary)]">{job.subsystem}</td>
              <td className="py-3 pr-3 text-[var(--text-secondary)]">
                {formatElapsed(job.elapsed_ms)}
              </td>
              <td className="py-3 pr-3 text-[var(--text-secondary)]">
                {formatRelative(job.updated_at)}
              </td>
              <td className="py-3 pr-3 text-[var(--text-secondary)]">
                {job.status === "failed" && job.error_title ? (
                  <span className="text-[color:var(--color-error,#ef4444)]">
                    {job.error_title}
                  </span>
                ) : job.status === "failed" ? (
                  job.error_code || "—"
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function JobDetailDrawer({ job, onClose }: { job: JobView; onClose: () => void }) {
  // Close on Escape for keyboard accessibility.
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 flex justify-end" aria-modal="true" role="dialog">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/40"
        onClick={onClose}
        aria-hidden="true"
      />
      {/* Drawer */}
      <div className="relative z-10 flex h-full w-full max-w-md flex-col gap-4 overflow-y-auto border-l border-[var(--border-default)] bg-[var(--bg-base)] p-6 shadow-xl">
        <header className="flex items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <p className="text-xs uppercase tracking-wide text-[var(--text-tertiary)]">
              {job.subsystem}
            </p>
            <h3 className="text-lg font-semibold text-[var(--text-primary)]">{job.job_type}</h3>
          </div>
          <Button variant="secondary" onClick={onClose}>
            Close
          </Button>
        </header>

        <section className="space-y-1">
          <p className="text-xs uppercase tracking-wide text-[var(--text-tertiary)]">How it went</p>
          <div className="flex items-center gap-2">
            <span className={cn("inline-block h-2.5 w-2.5 rounded-full", statusDot(job.status))} />
            <span className="text-sm font-medium uppercase tracking-wide text-[var(--text-primary)]">
              {job.status}
            </span>
            <span className="text-xs text-[var(--text-secondary)]">
              · {formatElapsed(job.elapsed_ms)}
            </span>
          </div>
          {job.progress_message ? (
            <p className="text-xs text-[var(--text-secondary)]">{job.progress_message}</p>
          ) : null}
          {job.retry_count > 0 ? (
            <p className="text-xs text-[var(--text-secondary)]">
              Retried {job.retry_count} time(s){" "}
              {job.max_attempts > 0 ? `(max ${job.max_attempts})` : ""}
            </p>
          ) : null}
        </section>

        {job.status === "failed" && (job.error_title || job.error_message) ? (
          <section className="space-y-2 rounded-[var(--radius-md)] border border-[color:var(--color-error,#ef4444)] bg-[color:rgba(239,68,68,0.08)] p-3">
            <p className="text-xs uppercase tracking-wide text-[color:var(--color-error,#ef4444)]">
              What went wrong
            </p>
            {job.error_title ? (
              <p className="text-sm font-semibold text-[var(--text-primary)]">{job.error_title}</p>
            ) : null}
            {job.error_hint ? (
              <p className="text-xs text-[var(--text-secondary)]">{job.error_hint}</p>
            ) : null}
            {job.error_code ? (
              <p className="text-[10px] font-mono text-[var(--text-tertiary)]">
                code: {job.error_code}
              </p>
            ) : null}
            {job.error_message ? (
              <details className="text-xs text-[var(--text-secondary)]">
                <summary className="cursor-pointer">Raw error</summary>
                <pre className="mt-1 whitespace-pre-wrap break-words">{job.error_message}</pre>
              </details>
            ) : null}
          </section>
        ) : null}

        <section className="space-y-1">
          <p className="text-xs uppercase tracking-wide text-[var(--text-tertiary)]">Where it ran</p>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs text-[var(--text-secondary)]">
            {job.strategy ? (
              <>
                <dt>Strategy</dt>
                <dd className="text-[var(--text-primary)]">{job.strategy}</dd>
              </>
            ) : null}
            {job.model ? (
              <>
                <dt>Model</dt>
                <dd className="text-[var(--text-primary)]">{job.model}</dd>
              </>
            ) : null}
            {formatGenerationMode(job.generation_mode) ? (
              <>
                <dt>Mode</dt>
                <dd className="text-[var(--text-primary)]">{formatGenerationMode(job.generation_mode)}</dd>
              </>
            ) : null}
            {formatPriority(job.priority) ? (
              <>
                <dt>Priority</dt>
                <dd className="text-[var(--text-primary)]">{formatPriority(job.priority)}</dd>
              </>
            ) : null}
            <dt>Target</dt>
            <dd className="break-all font-mono text-[10px] text-[var(--text-primary)]">
              {job.target_key}
            </dd>
          </dl>
        </section>

        <section className="space-y-1">
          <p className="text-xs uppercase tracking-wide text-[var(--text-tertiary)]">
            What it produced
          </p>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs text-[var(--text-secondary)]">
            <dt>Input tokens</dt>
            <dd className="text-[var(--text-primary)]">{job.input_tokens.toLocaleString()}</dd>
            <dt>Output tokens</dt>
            <dd className="text-[var(--text-primary)]">{job.output_tokens.toLocaleString()}</dd>
            {job.reused_summaries ? (
              <>
                <dt>Reused summaries</dt>
                <dd className="text-[var(--text-primary)]">
                  {job.reused_summaries.toLocaleString()}
                  {jobReuseSummary(job) ? ` · ${jobReuseSummary(job)?.replace(/^\d+ reused · /, "")}` : ""}
                </dd>
              </>
            ) : null}
            {job.snapshot_bytes > 0 ? (
              <>
                <dt>Snapshot</dt>
                <dd className="text-[var(--text-primary)]">
                  {(job.snapshot_bytes / 1024).toFixed(1)} KB
                </dd>
              </>
            ) : null}
          </dl>
        </section>

        <JobLogsPanel job={job} />

        {job.repo_id ? (
          <Link href={`/repositories/${job.repo_id}`} className="block">
            <Button variant="secondary" className="w-full">
              Open repository →
            </Button>
          </Link>
        ) : null}
      </div>
    </div>
  );
}

function JobLogsPanel({ job }: { job: JobView }) {
  const [logs, setLogs] = useState<JobLogView[]>([]);
  const [error, setError] = useState<string | null>(null);

  const fetchLogs = useCallback(async () => {
    try {
      const token = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
      const res = await fetch(`/api/v1/admin/llm/jobs/${encodeURIComponent(job.id)}/logs?limit=200`, {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      });
      if (!res.ok) throw new Error(`logs endpoint returned ${res.status}`);
      const body = (await res.json()) as { logs?: JobLogView[] };
      setLogs(Array.isArray(body.logs) ? body.logs : []);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load logs");
    }
  }, [job.id]);

  useEffect(() => {
    void fetchLogs();
    const interval = window.setInterval(() => {
      void fetchLogs();
    }, job.status === "generating" || job.status === "pending" ? 2000 : 5000);
    return () => window.clearInterval(interval);
  }, [fetchLogs, job.status]);

  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between">
        <p className="text-xs uppercase tracking-wide text-[var(--text-tertiary)]">Execution log</p>
        <span className="text-[11px] text-[var(--text-tertiary)]">{logs.length} entries</span>
      </div>
      <div className="max-h-80 overflow-y-auto rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-subtle)]">
        {error ? (
          <p className="px-3 py-2 text-xs text-[color:var(--color-error,#ef4444)]">{error}</p>
        ) : logs.length === 0 ? (
          <p className="px-3 py-2 text-xs text-[var(--text-secondary)]">No job logs yet.</p>
        ) : (
          <div className="space-y-2 p-3">
            {logs.map((entry) => (
              <div key={entry.id || `${entry.sequence}`} className="rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--bg-base)] p-2">
                <div className="flex items-center justify-between gap-2 text-[11px]">
                  <span className={cn("rounded-full border px-1.5 py-0.5 font-medium uppercase tracking-wide", logLevelStyle(entry.level))}>
                    {entry.level}
                  </span>
                  <span className="text-[var(--text-tertiary)]">{formatTime(entry.created_at)}</span>
                </div>
                <div className="mt-1 text-xs font-medium text-[var(--text-primary)]">{entry.message}</div>
                <div className="mt-1 flex flex-wrap gap-2 text-[11px] text-[var(--text-tertiary)]">
                  {entry.phase ? <span>{entry.phase}</span> : null}
                  <span className="font-mono">{entry.event}</span>
                </div>
                {entry.payload_json ? (
                  <details className="mt-2 text-[11px] text-[var(--text-secondary)]">
                    <summary className="cursor-pointer">Payload</summary>
                    <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-words rounded-[var(--radius-sm)] bg-[var(--bg-subtle)] p-2 font-mono text-[10px]">
                      {(() => {
                        try {
                          return JSON.stringify(JSON.parse(entry.payload_json), null, 2);
                        } catch {
                          return entry.payload_json;
                        }
                      })()}
                    </pre>
                  </details>
                ) : null}
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}
