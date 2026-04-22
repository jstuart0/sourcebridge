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

  const refetch = useCallback(async () => {
    setLoading(true);
    const res = await authFetch("/api/v1/admin/knowledge");
    if (res.ok) setData(await res.json());
    setLoading(false);
  }, []);

  useEffect(() => {
    refetch();
  }, [refetch]);

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Admin"
        title="Knowledge engine"
        description="Generated artifact status across repositories."
      />

      <div>
        <div className="mb-4 flex items-center justify-between">
          <h3 className="text-base font-semibold text-[var(--text-primary)]">
            {loading ? "Loading…" : "Overview"}
          </h3>
          <Button size="sm" variant="secondary" onClick={refetch}>
            Refresh
          </Button>
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
