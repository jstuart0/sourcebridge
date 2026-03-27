"use client";

import Link from "next/link";
import { useQuery } from "urql";
import {
  LLM_USAGE_QUERY,
  PLATFORM_STATS_QUERY,
  REPOSITORIES_LIGHT_QUERY as REPOSITORIES,
  REPOSITORIES_QUERY as REPOSITORIES_WITH_SCORES,
  TRACEABILITY_MATRIX_QUERY as TRACEABILITY_MATRIX,
} from "@/lib/graphql/queries";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { StatCard } from "@/components/ui/stat-card";

interface RepoNode {
  id: string;
  name: string;
  status: string;
  fileCount: number;
  functionCount: number;
  classCount: number;
}

interface RepoWithScore extends RepoNode {
  understandingScore?: {
    overall: number;
  } | null;
}

interface TMRequirement {
  id: string;
  externalId: string;
  title: string;
}

interface TMLink {
  id: string;
  requirementId: string;
  symbolId: string;
  confidence: string;
  verified: boolean;
}

interface TMSymbol {
  id: string;
  name: string;
  filePath: string;
  kind: string;
}

interface LLMUsageEntry {
  id: string;
  operation: string;
  model: string;
  inputTokens: number;
  outputTokens: number;
  createdAt: string;
}

export default function DashboardPage() {
  // Fast initial load — no understanding score computation
  const [reposResult] = useQuery({ query: REPOSITORIES });
  // Lazy-load scores in the background (triggers expensive computation)
  const [scoresResult] = useQuery({
    query: REPOSITORIES_WITH_SCORES,
    pause: !reposResult.data?.repositories?.length,
  });
  const [matrixResult] = useQuery({
    query: TRACEABILITY_MATRIX,
    variables: { repositoryId: reposResult.data?.repositories?.[0]?.id || "" },
    pause: !reposResult.data?.repositories?.[0]?.id,
  });
  const [statsResult] = useQuery({ query: PLATFORM_STATS_QUERY });
  const [usageResult] = useQuery({ query: LLM_USAGE_QUERY, variables: { limit: 10 } });

  const repos: RepoNode[] = reposResult.data?.repositories || [];
  // Merge in scores once they arrive
  const scoreMap = new Map<string, number>();
  if (scoresResult.data?.repositories) {
    for (const r of scoresResult.data.repositories as RepoWithScore[]) {
      if (r.understandingScore?.overall != null) {
        scoreMap.set(r.id, r.understandingScore.overall);
      }
    }
  }
  const tmData = matrixResult.data?.traceabilityMatrix;
  const tmRequirements: TMRequirement[] = tmData?.requirements || [];
  const tmLinks: TMLink[] = tmData?.links || [];
  const tmSymbols: TMSymbol[] = tmData?.symbols || [];
  const coverage: number | undefined = tmData?.coverage;

  const reqMap = new Map(tmRequirements.map((r) => [r.id, r]));
  const symMap = new Map(tmSymbols.map((s) => [s.id, s]));

  const recentLinks = tmLinks.slice(0, 5).map((link) => ({
    ...link,
    requirement: reqMap.get(link.requirementId),
    symbol: symMap.get(link.symbolId),
  }));

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Overview"
        title="Understand your codebases faster"
        description="See what SourceBridge.ai has already mapped: repositories, structure, understanding signals, and recent field-guide activity."
      />

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 sm:gap-4 md:grid-cols-3">
        <StatCard label="Repositories" value={repos.length} />
        <StatCard label="Files Indexed" value={statsResult.data?.platformStats?.files ?? 0} />
        <StatCard label="Symbols Discovered" value={statsResult.data?.platformStats?.symbols ?? 0} />
      </div>

      {repos.length === 0 && !reposResult.fetching ? (
        <EmptyState
          eyebrow="First Workspace"
          title="Bring your first codebase into focus"
          description="Add a repository to build a field guide for the system: files, symbols, structure, guided explanations, and change understanding."
          actions={
            <>
              <Link href="/onboarding">
                <Button>Setup Wizard</Button>
              </Link>
              <Link href="/repositories">
                <Button variant="secondary">Add Repository</Button>
              </Link>
              <Link href="/help">
                <Button variant="ghost">Read the Guide</Button>
              </Link>
            </>
          }
        />
      ) : null}

      {statsResult.data?.platformStats && repos.length > 0 ? (
        <div className="grid grid-cols-2 gap-3 sm:gap-4 xl:grid-cols-4">
          <StatCard
            label="Understanding Score"
            value={
              scoreMap.size > 0
                ? `${Math.round(repos.reduce((sum, r) => sum + (scoreMap.get(r.id) ?? 0), 0) / repos.length)}`
                : scoresResult.fetching ? "..." : "—"
            }
          />
          <StatCard
            label="LLM Tokens Used"
            value={(
              statsResult.data.platformStats.totalInputTokens +
              statsResult.data.platformStats.totalOutputTokens
            ).toLocaleString()}
          />
          {tmRequirements.length > 0 ? (
            <>
              <StatCard label="Linked Specs" value={statsResult.data.platformStats.links} />
              <StatCard
                label="Coverage"
                value={coverage !== undefined ? `${Math.round(coverage * 100)}%` : "—"}
              />
            </>
          ) : null}
        </div>
      ) : null}

      <div className="grid gap-5 sm:gap-6 xl:grid-cols-[1.2fr_0.8fr]">
        {repos.length > 0 ? (
          <Panel variant="surface" className="space-y-4">
            <div className="space-y-1">
              <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                Workspaces
              </p>
              <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                Repository Field Guides
              </h2>
            </div>
            <div className="divide-y divide-[var(--border-subtle)]">
              {repos.slice(0, 5).map((repo) => (
                <Link
                  key={repo.id}
                  href={`/repositories/${repo.id}?tab=knowledge`}
                  className="flex flex-col gap-2 py-3 text-sm transition-colors hover:text-[var(--accent-primary)] md:flex-row md:items-center md:justify-between"
                >
                  <span className="font-medium text-[var(--text-primary)]">
                    {repo.name}
                  </span>
                  <span className="text-[var(--text-secondary)]">
                    {repo.fileCount} files · {repo.functionCount + repo.classCount} structural nodes{scoreMap.has(repo.id) ? ` · score ${Math.round(scoreMap.get(repo.id)!)}` : ""}
                  </span>
                </Link>
              ))}
            </div>
          </Panel>
        ) : null}

        {(usageResult.data?.llmUsage?.length ?? 0) > 0 ? (
          <Panel variant="elevated" className="space-y-4">
            <div className="space-y-1">
              <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                AI Activity
              </p>
              <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                Recent Operations
              </h2>
            </div>
            <div className="divide-y divide-[var(--border-subtle)]">
              {(usageResult.data?.llmUsage as LLMUsageEntry[])?.map((entry: LLMUsageEntry) => (
                <div
                  key={entry.id}
                  className="flex flex-col gap-2 py-3 text-sm md:flex-row md:items-center md:justify-between"
                >
                  <span className="font-medium text-[var(--text-primary)]">{entry.operation}</span>
                  <span className="text-[var(--text-secondary)]">
                    {entry.model} — {(entry.inputTokens + entry.outputTokens).toLocaleString()} tokens
                  </span>
                </div>
              ))}
            </div>
          </Panel>
        ) : null}

        {recentLinks.length > 0 ? (
          <Panel variant="surface" className="space-y-4">
            <div className="space-y-1">
              <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                Specs to Code
              </p>
              <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                Recent Linked Specs
              </h2>
            </div>
            <div className="divide-y divide-[var(--border-subtle)]">
              {recentLinks.map((entry) => (
                <div
                  key={entry.id}
                  className="flex flex-col gap-2 py-3 text-sm md:flex-row md:items-center md:justify-between"
                >
                  <span className="font-medium text-[var(--text-primary)]">
                    {entry.requirement?.externalId || entry.requirementId}
                  </span>
                  <span className="text-[var(--text-secondary)]">
                    {entry.symbol?.name || entry.symbolId} — {entry.confidence}
                    {entry.verified ? " (verified)" : ""}
                  </span>
                </div>
              ))}
            </div>
          </Panel>
        ) : null}
      </div>
    </PageFrame>
  );
}
