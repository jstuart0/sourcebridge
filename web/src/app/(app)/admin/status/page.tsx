"use client";

import { useCallback, useEffect, useState } from "react";
import { useQuery } from "urql";

import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { StatCard } from "@/components/ui/stat-card";
import { StatusBadge } from "@/components/admin/StatusBadge";
import { authFetch } from "@/lib/auth-fetch";
import { HEALTH_QUERY } from "@/lib/graphql/queries";

interface AdminStatus {
  version: string;
  commit: string;
  uptime: string;
  database: string;
  worker: string;
  env: string;
}

export default function AdminStatusPage() {
  const [status, setStatus] = useState<AdminStatus | null>(null);
  const [testResult, setTestResult] = useState<string | null>(null);
  const [healthResult] = useQuery({ query: HEALTH_QUERY });

  const refetchStatus = useCallback(async () => {
    const res = await authFetch("/api/v1/admin/status");
    if (res.ok) setStatus(await res.json());
  }, []);

  useEffect(() => {
    refetchStatus();
  }, [refetchStatus]);

  async function testEndpoint(path: string) {
    setTestResult(null);
    const res = await authFetch(path, { method: "POST" });
    const data = await res.json();
    setTestResult(JSON.stringify(data, null, 2));
  }

  const codeBlockClass =
    "rounded-[var(--radius-md)] bg-black/20 p-3 font-mono text-sm whitespace-pre-wrap text-[var(--text-primary)]";

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Admin"
        title="System status"
        description="Service health, version, and connectivity checks."
      />

      <div className="space-y-6">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 sm:gap-4 xl:grid-cols-4">
          {status && (
            <>
              <StatCard
                label="Version"
                value={status.version}
                detail={`Commit ${status.commit?.slice(0, 8) || "—"}`}
              />
              <StatCard label="Uptime" value={status.uptime} />
              <Panel>
                <div className="text-sm text-[var(--text-secondary)]">Database</div>
                <StatusBadge status={status.database} />
              </Panel>
              <Panel>
                <div className="text-sm text-[var(--text-secondary)]">Worker</div>
                <StatusBadge status={status.worker} />
              </Panel>
            </>
          )}
        </div>

        <div className="flex flex-wrap gap-3">
          <Button onClick={() => testEndpoint("/api/v1/admin/test-worker")}>Test Worker</Button>
          <Button onClick={() => testEndpoint("/api/v1/admin/test-llm")}>Test LLM</Button>
          <Button
            variant="secondary"
            onClick={() => {
              refetchStatus();
              setTestResult(null);
            }}
          >
            Refresh
          </Button>
        </div>

        {testResult && (
          <Panel>
            <pre className={codeBlockClass}>{testResult}</pre>
          </Panel>
        )}

        {healthResult.data?.health && (
          <Panel>
            <h3 className="mb-2 text-base font-semibold text-[var(--text-primary)]">GraphQL Health</h3>
            <p className="text-sm text-[var(--text-primary)]">
              Status: {healthResult.data.health.status}
            </p>
            {healthResult.data.health.services?.map((svc: { name: string; status: string }) => (
              <div
                key={svc.name}
                className="flex justify-between py-1 text-sm text-[var(--text-primary)]"
              >
                <span>{svc.name}</span>
                <StatusBadge status={svc.status} />
              </div>
            ))}
          </Panel>
        )}
      </div>
    </PageFrame>
  );
}
