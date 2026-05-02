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
import {
  buildInfo,
  fetchRuntimeBuildInfo,
  formatBuildInfoMarkdown,
  shortCommit,
  type RuntimeBuildInfo,
} from "@/lib/version";

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
  const [runtime, setRuntime] = useState<RuntimeBuildInfo | null>(null);
  const [copyOk, setCopyOk] = useState(false);

  const refetchStatus = useCallback(async () => {
    const res = await authFetch("/api/v1/admin/status");
    if (res.ok) setStatus(await res.json());
  }, []);

  const refetchRuntime = useCallback(async () => {
    setRuntime(await fetchRuntimeBuildInfo());
  }, []);

  useEffect(() => {
    refetchStatus();
    refetchRuntime();
  }, [refetchStatus, refetchRuntime]);

  async function testEndpoint(path: string) {
    setTestResult(null);
    const res = await authFetch(path, { method: "POST" });
    const data = await res.json();
    setTestResult(JSON.stringify(data, null, 2));
  }

  async function copyBuildInfo() {
    const md = formatBuildInfoMarkdown(runtime);
    try {
      await navigator.clipboard.writeText(md);
      setCopyOk(true);
      setTimeout(() => setCopyOk(false), 2000);
    } catch {
      setCopyOk(false);
    }
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
                detail={`Commit ${shortCommit(status.commit)}`}
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

        <Panel>
          <div className="mb-3 flex items-center justify-between gap-3">
            <h3 className="text-base font-semibold text-[var(--text-primary)]">Build info</h3>
            <Button variant="secondary" onClick={copyBuildInfo}>
              {copyOk ? "Copied" : "Copy build info"}
            </Button>
          </div>
          <p className="mb-3 text-sm text-[var(--text-secondary)]">
            Paste this block into a support ticket so engineers know exactly which build you&apos;re running.
            The web bundle and the API server may report different versions during a rolling deploy.
          </p>
          <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-[max-content_1fr]">
            <BuildField
              label="Web bundle"
              value={`${buildInfo.version} (commit ${shortCommit(buildInfo.commit)}, built ${buildInfo.buildDate})`}
            />
            {runtime ? (
              <>
                <BuildField
                  label="API server"
                  value={`${runtime.version} (commit ${shortCommit(runtime.commit)}, built ${runtime.buildDate})`}
                />
                <BuildField label="Go runtime" value={runtime.goVersion} />
                <BuildField
                  label="Edition"
                  value={
                    runtime.buildEdition && runtime.buildEdition !== runtime.edition
                      ? `${runtime.edition} (build flavor: ${runtime.buildEdition})`
                      : runtime.edition
                  }
                />
                <BuildField
                  label="Worker"
                  value={runtime.workerVersion || "(unavailable)"}
                />
              </>
            ) : (
              <BuildField
                label="API server"
                value="(unavailable — could not reach /api/v1/version)"
              />
            )}
          </dl>
        </Panel>

        <div className="flex flex-wrap gap-3">
          <Button onClick={() => testEndpoint("/api/v1/admin/test-worker")}>Test Worker</Button>
          <Button onClick={() => testEndpoint("/api/v1/admin/test-llm")}>Test LLM</Button>
          <Button
            variant="secondary"
            onClick={() => {
              refetchStatus();
              refetchRuntime();
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

function BuildField({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="text-[var(--text-secondary)]">{label}</dt>
      <dd className="font-mono text-[var(--text-primary)]">{value}</dd>
    </>
  );
}
