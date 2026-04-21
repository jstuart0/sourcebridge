"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { useClient, useMutation, useQuery } from "urql";
import {
  AUTO_LINK_MUTATION,
  REPOSITORIES_LIGHT_QUERY as REPOSITORIES,
  REQUIREMENTS_QUERY as REQUIREMENTS,
  REQUIREMENT_TO_CODE_QUERY as REQUIREMENT_TO_CODE,
} from "@/lib/graphql/queries";
import { ConfidenceBadge, ConfidenceLevel } from "@/components/code-viewer/ConfidenceBadge";
import { CreateRequirementDialog } from "@/components/requirements/CreateRequirementDialog";
import { SourceRefLink } from "@/components/source/SourceRefLink";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { cn } from "@/lib/utils";

interface Requirement {
  id: string;
  externalId: string;
  title: string;
  source: string;
  priority: string;
}

interface ReqLink {
  id: string;
  symbolId: string;
  confidence: string;
  rationale: string | null;
  verified: boolean;
  symbol?: {
    id: string;
    name: string;
    qualifiedName: string;
    kind: string;
    filePath: string;
    startLine?: number;
    endLine?: number;
  } | null;
}

function confidenceLevel(conf: string): ConfidenceLevel {
  switch (conf) {
    case "VERIFIED":
      return "verified";
    case "HIGH":
      return "high";
    case "MEDIUM":
      return "medium";
    default:
      return "low";
  }
}

interface RepoOption {
  id: string;
  name: string;
  requirementCount: number;
}

export default function RequirementsPage() {
  const [reposResult] = useQuery({ query: REPOSITORIES });
  const repos: RepoOption[] = reposResult.data?.repositories || [];
  const [selectedRepoId, setSelectedRepoId] = useState<string | null>(null);
  const repoId = selectedRepoId || repos[0]?.id || "";

  const client = useClient();
  const [reqsResult, reexecuteReqs] = useQuery({
    query: REQUIREMENTS,
    variables: { repositoryId: repoId, limit: 50, offset: 0 },
    pause: !repoId,
  });

  // Lazy-load remaining requirements after the initial 50
  const [extraReqs, setExtraReqs] = useState<Requirement[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  const initialReqs: Requirement[] = reqsResult.data?.requirements?.nodes || [];
  const totalCount: number = reqsResult.data?.requirements?.totalCount ?? 0;

  useEffect(() => {
    if (initialReqs.length < 50 || initialReqs.length >= totalCount) {
      setExtraReqs([]);
      return;
    }
    let cancelled = false;
    setLoadingMore(true);

    (async () => {
      const allExtra: Requirement[] = [];
      let offset = 50;
      const batchSize = 200;

      while (!cancelled) {
        const result = await client
          .query(REQUIREMENTS, { repositoryId: repoId, limit: batchSize, offset })
          .toPromise();
        const batch: Requirement[] = result.data?.requirements?.nodes || [];
        if (batch.length === 0) break;
        allExtra.push(...batch);
        offset += batch.length;
        if (batch.length < batchSize) break;
      }

      if (!cancelled) {
        setExtraReqs(allExtra);
        setLoadingMore(false);
      }
    })();

    return () => { cancelled = true; };
  }, [initialReqs.length, totalCount, repoId, client]);

  // Reset extra reqs when repo changes
  useEffect(() => { setExtraReqs([]); }, [repoId]);

  const [, autoLink] = useMutation(AUTO_LINK_MUTATION);
  const [linking, setLinking] = useState(false);
  const [linkResult, setLinkResult] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  const [selectedReq, setSelectedReq] = useState<string | null>(null);

  const [linksResult] = useQuery({
    query: REQUIREMENT_TO_CODE,
    variables: { requirementId: selectedReq || "" },
    pause: !selectedReq,
  });

  const reqs: Requirement[] = [...initialReqs, ...extraReqs];
  const links: ReqLink[] = linksResult.data?.requirementToCode || [];
  const activeRequirement = reqs.find((req) => req.id === selectedReq) || null;

  async function handleAutoLink() {
    if (linking || !repoId) return;
    setLinking(true);
    setLinkResult(null);
    try {
      const res = await autoLink({ repositoryId: repoId });
      if (res.data?.autoLinkRequirements) {
        const { linksCreated, requirementsProcessed } = res.data.autoLinkRequirements;
        setLinkResult(`Processed ${requirementsProcessed} requirements, created ${linksCreated} links.`);
      }
      reexecuteReqs({ requestPolicy: "network-only" });
    } finally {
      setLinking(false);
    }
  }

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Requirements"
        title="Requirements and traceability"
        description="Browse imported requirements and inspect their linked code evidence."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="secondary"
              onClick={() => setCreateOpen(true)}
              disabled={!repoId}
            >
              + New requirement
            </Button>
            {reqs.length > 0 ? (
              <Button onClick={handleAutoLink} disabled={linking || !repoId}>
                {linking ? "Linking…" : "Auto-Link to Code"}
              </Button>
            ) : null}
          </div>
        }
      />

      {repoId ? (
        <CreateRequirementDialog
          open={createOpen}
          repositoryId={repoId}
          onClose={() => setCreateOpen(false)}
          onCreated={() => {
            reexecuteReqs({ requestPolicy: "network-only" });
          }}
        />
      ) : null}

      {repos.length > 1 && (
        <div className="flex items-center gap-3">
          <label className="text-sm font-medium text-[var(--text-secondary)]">Repository</label>
          <select
            value={repoId}
            onChange={(e) => {
              setSelectedRepoId(e.target.value);
              setSelectedReq(null);
              setLinkResult(null);
            }}
            className="h-9 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]"
          >
            {repos.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name} ({r.requirementCount} req{r.requirementCount !== 1 ? "s" : ""})
              </option>
            ))}
          </select>
        </div>
      )}

      {linkResult && (
        <div className="rounded-[var(--control-radius)] border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-500">
          {linkResult}
        </div>
      )}

      {reqs.length === 0 && !reqsResult.fetching ? (
        <EmptyState
          title="No requirements imported yet"
          description="Import requirements from the repository workspace or add a repository and import a Markdown or CSV source."
        />
      ) : (
        <div className="grid gap-6 lg:grid-cols-[0.9fr_1.1fr]">
          <Panel className="min-w-0 max-h-[70vh] overflow-y-auto">
            <div className="space-y-1 pb-4">
              <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                Requirements{totalCount > 0 ? ` (${reqs.length}${loadingMore ? "+" : ""} of ${totalCount})` : ""}
              </h2>
              <p className="text-sm text-[var(--text-secondary)]">
                Select a requirement to inspect linked symbols.
              </p>
            </div>
            <div className="space-y-3">
              {reqs.map((req) => (
                <button
                  key={req.id}
                  type="button"
                  onClick={() => setSelectedReq(req.id)}
                  className={cn(
                    "w-full rounded-[var(--control-radius)] border px-4 py-3 text-left transition-colors",
                    selectedReq === req.id
                      ? "border-[var(--nav-item-border)] bg-[var(--nav-item-bg-active)]"
                      : "border-[var(--border-default)] bg-[var(--bg-base)]"
                  )}
                >
                  <div className="flex items-start justify-between gap-3">
                    <Link
                      href={`/requirements/${req.id}`}
                      onClick={(e) => e.stopPropagation()}
                      className="font-medium text-[var(--text-primary)] hover:text-[var(--accent-primary)]"
                    >
                      {req.externalId}
                    </Link>
                    <span className="rounded-full border border-[var(--border-default)] px-2.5 py-1 text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">
                      {req.priority || req.source || "—"}
                    </span>
                  </div>
                  <p className="mt-2 text-sm leading-6 text-[var(--text-secondary)]">{req.title}</p>
                </button>
              ))}
            </div>
          </Panel>

          <Panel variant="elevated" className="min-w-0">
            {selectedReq && activeRequirement ? (
              <div className="space-y-5">
                <div className="space-y-1">
                  <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                    Linked Code
                  </p>
                  <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                    {activeRequirement.externalId}
                  </h2>
                  <p className="text-sm leading-7 text-[var(--text-secondary)]">
                    {activeRequirement.title}
                  </p>
                  <p className="text-sm text-[var(--text-tertiary)]">
                    {links.length} linked symbol{links.length !== 1 ? "s" : ""}
                  </p>
                </div>

                {links.length === 0 ? (
                  <p className="text-sm text-[var(--text-secondary)]">
                    No code is currently linked to this requirement.
                  </p>
                ) : (
                  <div className="divide-y divide-[var(--border-subtle)]">
                    {links.map((link) => (
                      <div
                        key={link.id}
                        className="flex flex-col gap-4 py-4 md:flex-row md:items-start md:justify-between"
                      >
                        <div className="min-w-0">
                          <p className="truncate font-mono text-sm text-[var(--text-primary)]">
                            {link.symbol?.name || link.symbolId}
                          </p>
                          {link.symbol?.filePath && repoId ? (
                            <div className="mt-1">
                              <SourceRefLink
                                repositoryId={repoId}
                                target={{
                                  tab: "files",
                                  filePath: link.symbol.filePath,
                                  line: link.symbol.startLine,
                                  endLine: link.symbol.endLine,
                                }}
                                className="text-xs"
                              >
                                {link.symbol.filePath}
                                {link.symbol.startLine ? `:${link.symbol.startLine}` : ""}
                              </SourceRefLink>
                            </div>
                          ) : null}
                          {link.rationale ? (
                            <p className="mt-2 text-sm text-[var(--text-secondary)]">
                              {link.rationale}
                            </p>
                          ) : null}
                        </div>
                        <div className="shrink-0">
                          <ConfidenceBadge level={confidenceLevel(link.confidence)} />
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ) : (
              <div className="flex min-h-[18rem] items-center justify-center text-center">
                <div className="max-w-md space-y-3">
                  <p className="text-lg font-semibold text-[var(--text-primary)]">
                    Select a requirement
                  </p>
                  <p className="text-sm leading-7 text-[var(--text-secondary)]">
                    Linked symbols, confidence, and rationale appear here once a requirement is
                    selected.
                  </p>
                </div>
              </div>
            )}
          </Panel>
        </div>
      )}
    </PageFrame>
  );
}
