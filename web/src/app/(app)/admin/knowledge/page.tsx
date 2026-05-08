"use client";

import { useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { StatCard } from "@/components/ui/stat-card";
import { StatusBadge } from "@/components/admin/StatusBadge";
import { authFetch } from "@/lib/auth-fetch";

interface KnowledgeAdminStatus {
  configured: boolean;
  stats?: {
    total: number;
    ready: number;
    stale: number;
    generating: number;
    failed: number;
    pending: number;
    by_type: Record<string, number>;
  };
  repositories?: Array<{
    repo_id: string;
    repo_name: string;
    artifacts: Array<{
      id: string;
      type: string;
      status: string;
      stale: boolean;
      audience: string;
      depth: string;
      generated_at?: string;
      commit_sha?: string;
    }>;
  }>;
}

export default function AdminKnowledgePage() {
  const [data, setData] = useState<KnowledgeAdminStatus | null>(null);
  const [loading, setLoading] = useState(true);
  // CA-250: track last-refreshed timestamp + auto-refresh while any
  // artifact is generating/pending. Mirrors the polling pattern used
  // in the knowledge-tab (knowledge-tab.tsx ~line 773-784).
  const [lastRefreshedAt, setLastRefreshedAt] = useState<number | null>(null);

  const refetch = useCallback(async () => {
    setLoading(true);
    const res = await authFetch("/api/v1/admin/knowledge");
    if (res.ok) {
      setData(await res.json());
      setLastRefreshedAt(Date.now());
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    refetch();
  }, [refetch]);

  // CA-250: auto-refresh every 5s while any artifact is in a non-terminal
  // state. Stops polling when everything is ready/failed/stale to avoid
  // wasted requests.
  const hasInflightWork =
    (data?.stats?.generating ?? 0) > 0 || (data?.stats?.pending ?? 0) > 0;
  useEffect(() => {
    if (!hasInflightWork) return undefined;
    const id = window.setInterval(() => {
      void refetch();
    }, 5000);
    return () => window.clearInterval(id);
  }, [hasInflightWork, refetch]);

  // Tick re-render every 1s while inflight so the "last refreshed Xs
  // ago" label updates even if the data didn't change.
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!hasInflightWork || lastRefreshedAt === null) return undefined;
    const id = window.setInterval(() => setTick((t) => t + 1), 1000);
    return () => window.clearInterval(id);
  }, [hasInflightWork, lastRefreshedAt]);

  const lastRefreshedAgo = lastRefreshedAt === null ? null : Math.max(0, Math.floor((Date.now() - lastRefreshedAt) / 1000));

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Admin"
        title="Knowledge engine"
        description="Generated artifact status across repositories."
      />

      <div>
        <div className="mb-4 flex items-center justify-between gap-3">
          <h3 className="text-base font-semibold text-[var(--text-primary)]">
            {loading ? "Loading…" : "Overview"}
          </h3>
          <div className="flex items-center gap-3">
            {lastRefreshedAgo !== null && (
              <span className="text-xs text-[var(--text-tertiary)]" title={hasInflightWork ? "Auto-refreshing every 5s while generation is in progress" : "Auto-refresh paused (no work in flight)"}>
                Last refreshed {lastRefreshedAgo}s ago
                {hasInflightWork ? " · auto" : ""}
              </span>
            )}
            <Button size="sm" variant="secondary" onClick={refetch}>
              Refresh
            </Button>
          </div>
        </div>

        {data && !data.configured && (
          <Panel>
            <p className="text-sm text-[var(--text-secondary)]">Knowledge store not configured.</p>
          </Panel>
        )}

        {data?.stats && (
          <div className="mb-6 grid grid-cols-2 gap-3 sm:gap-4 md:grid-cols-3 xl:grid-cols-5">
            <StatCard label="Total Artifacts" value={data.stats.total} />
            <StatCard label="Ready" value={data.stats.ready} />
            <StatCard label="Stale" value={data.stats.stale} />
            <StatCard label="Failed" value={data.stats.failed} />
            <StatCard label="Generating" value={data.stats.generating} />
          </div>
        )}

        {data?.stats?.by_type && Object.keys(data.stats.by_type).length > 0 && (
          <Panel className="mb-6">
            <h4 className="mb-2 text-sm font-medium text-[var(--text-primary)]">By Type</h4>
            {Object.entries(data.stats.by_type).map(([type, count]) => (
              <div
                key={type}
                className="flex justify-between py-1 text-sm text-[var(--text-primary)]"
              >
                <span>{type.replace(/_/g, " ")}</span>
                <span className="font-medium">{count}</span>
              </div>
            ))}
          </Panel>
        )}

        {data?.repositories && data.repositories.length > 0 && (
          <Panel>
            <h4 className="mb-3 text-sm font-medium text-[var(--text-primary)]">
              Per-Repository Status
            </h4>
            {data.repositories.map((repo) => (
              <div key={repo.repo_id} className="mb-4 last:mb-0">
                <div className="mb-1 text-sm font-medium text-[var(--text-primary)]">
                  {repo.repo_name}
                </div>
                {repo.artifacts.map((a) => (
                  <div
                    key={a.id}
                    className="flex items-center justify-between border-b border-[var(--border-default)] px-2 py-1.5 text-xs last:border-b-0"
                  >
                    <span>
                      {a.type.replace(/_/g, " ")} ({a.audience}/{a.depth})
                    </span>
                    <div className="flex items-center gap-2">
                      {a.stale && (
                        <span className="rounded-full border border-[var(--border-default)] bg-[var(--bg-hover)] px-1.5 py-0.5 text-[var(--text-secondary)]">
                          stale
                        </span>
                      )}
                      <StatusBadge
                        status={
                          a.status === "ready"
                            ? "healthy"
                            : a.status === "failed"
                            ? "error"
                            : a.status
                        }
                      />
                      {a.generated_at && (
                        <span className="text-[var(--text-tertiary)]">
                          {new Date(a.generated_at).toLocaleDateString()}
                        </span>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            ))}
          </Panel>
        )}
      </div>
    </PageFrame>
  );
}
