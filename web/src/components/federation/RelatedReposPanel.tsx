"use client";

import { useState } from "react";
import { useQuery, useMutation } from "urql";
import {
  REPO_LINKS_QUERY,
  CROSS_REPO_REFS_QUERY,
  API_CONTRACTS_QUERY,
  LINK_REPOS_MUTATION,
  UNLINK_REPOS_MUTATION,
  DETECT_CONTRACTS_MUTATION,
  REPOSITORIES_LIGHT_QUERY,
} from "@/lib/graphql/queries";
import { Panel } from "@/components/ui/panel";
import { Button } from "@/components/ui/button";

interface RepoLink {
  id: string;
  sourceRepoId: string;
  targetRepoId: string;
  linkType: string;
  createdAt: string;
}

interface CrossRepoRef {
  id: string;
  sourceSymbolId: string;
  targetSymbolId: string;
  sourceRepoId: string;
  targetRepoId: string;
  refType: string;
  confidence: number;
  contractFile?: string;
  consumerFile?: string;
  evidence?: string;
  createdAt: string;
}

interface APIContract {
  id: string;
  repoId: string;
  filePath: string;
  contractType: string;
  endpointCount: number;
  version?: string;
  detectedAt: string;
}

interface RepoMeta {
  id: string;
  name: string;
}

function ConfidenceBar({ confidence }: { confidence: number }) {
  const pct = Math.round(confidence * 100);
  const color = pct >= 80 ? "bg-emerald-500" : pct >= 50 ? "bg-amber-500" : "bg-gray-500";
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-16 rounded-full bg-[var(--bg-hover)]">
        <div className={`h-1.5 rounded-full ${color}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="text-xs text-[var(--text-tertiary)]">{pct}%</span>
    </div>
  );
}

function ContractTypeBadge({ type }: { type: string }) {
  const colors: Record<string, string> = {
    openapi: "border-blue-400/40 bg-blue-500/10 text-blue-400",
    protobuf: "border-green-400/40 bg-green-500/10 text-green-400",
    graphql: "border-pink-400/40 bg-pink-500/10 text-pink-400",
  };
  return (
    <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-semibold ${colors[type] || "border-gray-400/40 bg-gray-500/10 text-gray-400"}`}>
      {type}
    </span>
  );
}

export function RelatedReposPanel({ repositoryId }: { repositoryId: string }) {
  const [linkTargetId, setLinkTargetId] = useState("");

  // Queries
  const [linksResult, reexecuteLinks] = useQuery<{ repoLinks: RepoLink[] }>({
    query: REPO_LINKS_QUERY,
    variables: { repoId: repositoryId },
  });
  const [refsResult] = useQuery<{ crossRepoRefs: CrossRepoRef[] }>({
    query: CROSS_REPO_REFS_QUERY,
    variables: { repoId: repositoryId },
  });
  const [contractsResult, reexecuteContracts] = useQuery<{ apiContracts: APIContract[] }>({
    query: API_CONTRACTS_QUERY,
    variables: { repoId: repositoryId },
  });
  const [reposResult] = useQuery<{ repositories: RepoMeta[] }>({
    query: REPOSITORIES_LIGHT_QUERY,
  });

  // Mutations
  const [{ fetching: linking }, linkRepos] = useMutation(LINK_REPOS_MUTATION);
  const [, unlinkRepos] = useMutation(UNLINK_REPOS_MUTATION);
  const [{ fetching: detecting }, detectContracts] = useMutation(DETECT_CONTRACTS_MUTATION);

  const links = linksResult.data?.repoLinks ?? [];
  const refs = refsResult.data?.crossRepoRefs ?? [];
  const contracts = contractsResult.data?.apiContracts ?? [];
  const repos = reposResult.data?.repositories ?? [];

  const repoMap = new Map(repos.map((r) => [r.id, r.name]));
  const repoName = (id: string) => repoMap.get(id) || id.slice(0, 8);

  // Filter available repos for linking (exclude self + already linked)
  const linkedIds = new Set(links.flatMap((l) => [l.sourceRepoId, l.targetRepoId]));
  const availableRepos = repos.filter((r) => r.id !== repositoryId && !linkedIds.has(r.id));

  async function handleLink() {
    if (!linkTargetId || linking) return;
    await linkRepos({ sourceRepoId: repositoryId, targetRepoId: linkTargetId });
    setLinkTargetId("");
    reexecuteLinks({ requestPolicy: "network-only" });
  }

  async function handleUnlink(linkId: string) {
    await unlinkRepos({ linkId });
    reexecuteLinks({ requestPolicy: "network-only" });
  }

  async function handleDetectContracts() {
    await detectContracts({ repoId: repositoryId });
    reexecuteContracts({ requestPolicy: "network-only" });
  }

  return (
    <div className="space-y-6">
      {/* Linked Repositories */}
      <Panel>
        <div className="border-b border-[var(--border-subtle)] px-6 py-4">
          <h3 className="text-sm font-semibold text-[var(--text-primary)]">Linked Repositories</h3>
          <p className="mt-1 text-xs text-[var(--text-tertiary)]">
            Repositories linked for cross-repo analysis and contract matching.
          </p>
        </div>
        <div className="p-6">
          {links.length === 0 && !linksResult.fetching && (
            <p className="text-sm text-[var(--text-tertiary)]">No linked repositories yet.</p>
          )}
          {links.length > 0 && (
            <div className="space-y-2">
              {links.map((link) => {
                const otherId = link.sourceRepoId === repositoryId ? link.targetRepoId : link.sourceRepoId;
                return (
                  <div key={link.id} className="flex items-center justify-between rounded-lg border border-[var(--border-subtle)] px-4 py-3">
                    <div>
                      <span className="text-sm font-medium text-[var(--text-primary)]">{repoName(otherId)}</span>
                      <span className="ml-2 text-xs text-[var(--text-tertiary)]">{link.linkType}</span>
                    </div>
                    <button
                      onClick={() => handleUnlink(link.id)}
                      className="text-xs text-[var(--text-tertiary)] hover:text-red-400"
                    >
                      Unlink
                    </button>
                  </div>
                );
              })}
            </div>
          )}

          {availableRepos.length > 0 && (
            <div className="mt-4 flex items-center gap-2">
              <select
                value={linkTargetId}
                onChange={(e) => setLinkTargetId(e.target.value)}
                className="flex-1 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-3 py-2 text-sm text-[var(--text-primary)]"
              >
                <option value="">Select repository...</option>
                {availableRepos.map((r) => (
                  <option key={r.id} value={r.id}>{r.name}</option>
                ))}
              </select>
              <Button size="sm" onClick={handleLink} disabled={!linkTargetId || linking}>
                {linking ? "Linking..." : "Link"}
              </Button>
            </div>
          )}
        </div>
      </Panel>

      {/* API Contracts */}
      <Panel>
        <div className="border-b border-[var(--border-subtle)] px-6 py-4">
          <div className="flex items-center justify-between">
            <div>
              <h3 className="text-sm font-semibold text-[var(--text-primary)]">API Contracts</h3>
              <p className="mt-1 text-xs text-[var(--text-tertiary)]">
                Detected API specifications (OpenAPI, Protobuf, GraphQL).
              </p>
            </div>
            <Button size="sm" onClick={handleDetectContracts} disabled={detecting}>
              {detecting ? "Detecting..." : "Detect Contracts"}
            </Button>
          </div>
        </div>
        <div className="p-6">
          {contracts.length === 0 && !contractsResult.fetching && (
            <p className="text-sm text-[var(--text-tertiary)]">No API contracts detected. Click &quot;Detect Contracts&quot; to scan.</p>
          )}
          {contracts.length > 0 && (
            <div className="space-y-2">
              {contracts.map((c) => (
                <div key={c.id} className="flex items-center justify-between rounded-lg border border-[var(--border-subtle)] px-4 py-3">
                  <div className="flex items-center gap-3">
                    <ContractTypeBadge type={c.contractType} />
                    <span className="text-sm font-mono text-[var(--text-primary)]">{c.filePath}</span>
                  </div>
                  <div className="flex items-center gap-4 text-xs text-[var(--text-tertiary)]">
                    {c.endpointCount > 0 && <span>{c.endpointCount} endpoints</span>}
                    {c.version && <span>v{c.version}</span>}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </Panel>

      {/* Cross-Repo References */}
      <Panel>
        <div className="border-b border-[var(--border-subtle)] px-6 py-4">
          <h3 className="text-sm font-semibold text-[var(--text-primary)]">Cross-Repo References</h3>
          <p className="mt-1 text-xs text-[var(--text-tertiary)]">
            Symbol references between this repository and linked repositories.
          </p>
        </div>
        <div className="p-6">
          {refs.length === 0 && !refsResult.fetching && (
            <p className="text-sm text-[var(--text-tertiary)]">
              No cross-repo references found. Link repositories and detect contracts to discover references.
            </p>
          )}
          {refs.length > 0 && (
            <div className="space-y-2">
              {refs.map((ref) => {
                const direction = ref.sourceRepoId === repositoryId ? "outbound" : "inbound";
                const otherRepoId = direction === "outbound" ? ref.targetRepoId : ref.sourceRepoId;
                return (
                  <div key={ref.id} className="rounded-lg border border-[var(--border-subtle)] px-4 py-3">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <span className={`text-xs font-semibold uppercase ${direction === "outbound" ? "text-blue-400" : "text-green-400"}`}>
                          {direction}
                        </span>
                        <span className="text-xs text-[var(--text-tertiary)]">{ref.refType}</span>
                        <span className="text-sm text-[var(--text-primary)]">{repoName(otherRepoId)}</span>
                      </div>
                      <ConfidenceBar confidence={ref.confidence} />
                    </div>
                    {ref.evidence && (
                      <p className="mt-1 text-xs text-[var(--text-tertiary)]">{ref.evidence}</p>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </Panel>
    </div>
  );
}
