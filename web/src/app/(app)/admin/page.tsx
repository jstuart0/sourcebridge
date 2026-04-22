"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import {
  Activity,
  BookOpen,
  Brain,
  Cpu,
  FolderGit2,
  GitBranch,
  Gauge,
  LockKeyhole,
  Sparkles,
} from "lucide-react";

import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { StatCard } from "@/components/ui/stat-card";
import { StatusBadge } from "@/components/admin/StatusBadge";
import { authFetch } from "@/lib/auth-fetch";

interface AdminStatus {
  version: string;
  commit: string;
  uptime: string;
  database: string;
  worker: string;
}

type SectionCard = {
  href: string;
  title: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
  enterpriseOnly?: boolean;
};

const SECTION_CARDS: SectionCard[] = [
  {
    href: "/admin/status",
    title: "System Status",
    description: "Version, uptime, database & worker health checks.",
    icon: Activity,
  },
  {
    href: "/admin/llm",
    title: "LLM Configuration",
    description: "Provider, models, per-operation overrides, and timeouts.",
    icon: Cpu,
  },
  {
    href: "/admin/monitor",
    title: "Generation Monitor",
    description: "Live view of every AI job the orchestrator is running.",
    icon: Gauge,
  },
  {
    href: "/admin/comprehension",
    title: "Comprehension",
    description: "How knowledge artifacts are generated and retained.",
    icon: Brain,
  },
  {
    href: "/admin/knowledge",
    title: "Knowledge Engine",
    description: "Generated artifacts per repository: ready, stale, failed.",
    icon: BookOpen,
  },
  {
    href: "/admin/git",
    title: "Git Credentials",
    description: "Default token and SSH key path for private repositories.",
    icon: GitBranch,
  },
  {
    href: "/admin/repos",
    title: "Repositories",
    description: "Indexing status and manual reindex.",
    icon: FolderGit2,
  },
  {
    href: "/admin/auth",
    title: "Authentication",
    description: "Auth mode, CSRF, and OIDC configuration summary.",
    icon: LockKeyhole,
  },
  {
    href: "/admin/enterprise",
    title: "Enterprise",
    description: "Billing, SSO, audit log, team, and org settings.",
    icon: Sparkles,
    enterpriseOnly: true,
  },
];

export default function AdminDashboardPage() {
  const isEnterprise = process.env.NEXT_PUBLIC_EDITION === "enterprise";
  const [status, setStatus] = useState<AdminStatus | null>(null);

  const refetch = useCallback(async () => {
    const res = await authFetch("/api/v1/admin/status");
    if (res.ok) setStatus(await res.json());
  }, []);

  useEffect(() => {
    refetch();
  }, [refetch]);

  const cards = SECTION_CARDS.filter((c) => !c.enterpriseOnly || isEnterprise);

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Operations"
        title="Admin"
        description="Monitor service health, configure providers, and manage repository-level operational settings."
      />

      {status && (
        <div className="mb-6 grid grid-cols-1 gap-3 sm:grid-cols-2 sm:gap-4 xl:grid-cols-4">
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
        </div>
      )}

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {cards.map((card) => {
          const Icon = card.icon;
          return (
            <Link
              key={card.href}
              href={card.href}
              className="group flex flex-col gap-2 rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg)] p-5 shadow-[var(--panel-shadow-soft)] transition-colors hover:border-[var(--accent-primary)]/40 hover:bg-[var(--bg-hover)]"
            >
              <div className="flex items-center gap-2 text-[var(--text-primary)]">
                <Icon className="h-5 w-5 text-[var(--text-secondary)] transition-colors group-hover:text-[var(--accent-primary)]" />
                <span className="text-base font-semibold">{card.title}</span>
              </div>
              <p className="text-sm leading-relaxed text-[var(--text-secondary)]">
                {card.description}
              </p>
            </Link>
          );
        })}
      </div>
    </PageFrame>
  );
}
