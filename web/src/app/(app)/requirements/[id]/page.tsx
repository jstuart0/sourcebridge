"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useState } from "react";
import { useMutation, useQuery } from "urql";
import {
  CREATE_MANUAL_LINK_MUTATION,
  ENRICH_REQUIREMENT_MUTATION,
  REQUIREMENT_QUERY,
  REPOSITORIES_LIGHT_QUERY as REPOSITORIES_QUERY,
  SYMBOLS_QUERY,
  VERIFY_LINK_MUTATION,
} from "@/lib/graphql/queries";
import { ConfidenceBadge, type ConfidenceLevel } from "@/components/code-viewer/ConfidenceBadge";
import { SourceRefLink } from "@/components/source/SourceRefLink";
import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { cn } from "@/lib/utils";

interface ReqLink {
  id: string;
  symbolId: string;
  confidence: string;
  rationale: string | null;
  verified: boolean;
  symbol?: { id: string; name: string; filePath: string; kind: string; startLine?: number; endLine?: number } | null;
}

interface Req {
  id: string;
  externalId: string | null;
  title: string;
  description: string;
  source: string;
  priority: string | null;
  tags: string[];
  links: ReqLink[];
  createdAt: string;
  updatedAt: string | null;
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

export default function RequirementDetailPage() {
  const params = useParams();
  const reqId = params.id as string;

  const [reqResult, reexecute] = useQuery({ query: REQUIREMENT_QUERY, variables: { id: reqId } });
  const [reposResult] = useQuery({ query: REPOSITORIES_QUERY });
  const repoId = reposResult.data?.repositories?.[0]?.id || "";

  const [showLinkForm, setShowLinkForm] = useState(false);
  const [symbolSearch, setSymbolSearch] = useState("");
  const [selectedSymbol, setSelectedSymbol] = useState<string | null>(null);
  const [linkRationale, setLinkRationale] = useState("");
  const [enriching, setEnriching] = useState(false);

  const [symbolsResult] = useQuery({
    query: SYMBOLS_QUERY,
    variables: { repositoryId: repoId, query: symbolSearch || undefined, limit: 20 },
    pause: !showLinkForm || !repoId,
  });

  const [, verifyLink] = useMutation(VERIFY_LINK_MUTATION);
  const [, createLink] = useMutation(CREATE_MANUAL_LINK_MUTATION);
  const [, enrichReq] = useMutation(ENRICH_REQUIREMENT_MUTATION);

  const req: Req | null = reqResult.data?.requirement || null;
  const symbols = symbolsResult.data?.symbols?.nodes || [];

  if (!req && !reqResult.fetching) {
    return (
      <PageFrame>
        <Panel>
          <p className="text-sm text-[var(--text-secondary)]">Requirement not found.</p>
        </Panel>
      </PageFrame>
    );
  }

  async function handleVerify(linkId: string, verified: boolean) {
    await verifyLink({ linkId, verified });
    reexecute({ requestPolicy: "network-only" });
  }

  async function handleCreateLink() {
    if (!selectedSymbol || !repoId) return;
    await createLink({
      input: {
        repositoryId: repoId,
        requirementId: reqId,
        symbolId: selectedSymbol,
        rationale: linkRationale || null,
      },
    });
    setShowLinkForm(false);
    setSelectedSymbol(null);
    setLinkRationale("");
    setSymbolSearch("");
    reexecute({ requestPolicy: "network-only" });
  }

  async function handleEnrich() {
    setEnriching(true);
    await enrichReq({ requirementId: reqId });
    reexecute({ requestPolicy: "network-only" });
    setEnriching(false);
  }

  return (
    <PageFrame>
      <div className="text-sm text-[var(--text-secondary)]">
        <Link href="/requirements" className="hover:text-[var(--accent-primary)]">
          Requirements
        </Link>
        <span className="mx-2">/</span>
        <span className="font-medium text-[var(--text-primary)]">{req?.externalId || "…"}</span>
      </div>

      {req ? (
        <>
          <PageHeader
            eyebrow="Requirement Detail"
            title={req.title}
            description={req.description}
            actions={
              <Button onClick={handleEnrich} disabled={enriching}>
                {enriching ? "Enriching…" : "Enrich with AI"}
              </Button>
            }
          />

          <Panel variant="surface" className="space-y-4">
            <div className="flex flex-wrap gap-2">
              {req.externalId ? (
                <span className="rounded-full border border-[var(--border-default)] px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] text-[var(--text-tertiary)]">
                  {req.externalId}
                </span>
              ) : null}
              {req.priority ? (
                <span className="rounded-full border border-[var(--border-default)] px-3 py-1 text-xs text-[var(--text-secondary)]">
                  {req.priority}
                </span>
              ) : null}
              {req.source ? (
                <span className="rounded-full border border-[var(--border-default)] px-3 py-1 text-xs text-[var(--text-secondary)]">
                  Source: {req.source}
                </span>
              ) : null}
              {req.tags.map((tag) => (
                <span
                  key={tag}
                  className="rounded-full bg-[var(--bg-active)] px-3 py-1 text-xs text-[var(--text-secondary)]"
                >
                  {tag}
                </span>
              ))}
            </div>
          </Panel>

          <Panel variant="elevated" className="space-y-5">
            <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
              <div className="space-y-1">
                <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                  Linked Code
                </p>
                <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                  {req.links.length} links
                </h2>
              </div>
              <Button variant="secondary" onClick={() => setShowLinkForm((value) => !value)}>
                {showLinkForm ? "Cancel" : "Add Manual Link"}
              </Button>
            </div>

            {showLinkForm ? (
              <div className="space-y-4 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
                <input
                  type="text"
                  value={symbolSearch}
                  onChange={(e) => setSymbolSearch(e.target.value)}
                  placeholder="Search symbols…"
                  className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 text-sm text-[var(--text-primary)]"
                />
                <div className="max-h-56 overflow-y-auto rounded-[var(--control-radius)] border border-[var(--border-default)]">
                  {symbols.map((sym: { id: string; name: string; kind: string; filePath: string }) => (
                    <button
                      key={sym.id}
                      type="button"
                      onClick={() => setSelectedSymbol(sym.id)}
                      className={cn(
                        "block w-full border-b border-[var(--border-subtle)] px-3 py-3 text-left text-sm transition-colors last:border-b-0 hover:bg-[var(--bg-hover)]",
                        selectedSymbol === sym.id ? "bg-[var(--nav-item-bg-active)]" : "bg-transparent"
                      )}
                    >
                      <span className="font-mono text-[var(--text-primary)]">{sym.name}</span>
                      <span className="ml-2 text-[var(--text-secondary)]">
                        {sym.kind} · {sym.filePath}
                      </span>
                    </button>
                  ))}
                </div>
                <input
                  type="text"
                  value={linkRationale}
                  onChange={(e) => setLinkRationale(e.target.value)}
                  placeholder="Rationale (optional)"
                  className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 text-sm text-[var(--text-primary)]"
                />
                <Button disabled={!selectedSymbol} onClick={handleCreateLink}>
                  Create Link
                </Button>
              </div>
            ) : null}

            {req.links.length === 0 ? (
              <p className="text-sm text-[var(--text-secondary)]">
                No code linked to this requirement yet.
              </p>
            ) : (
              <div className="divide-y divide-[var(--border-subtle)]">
                {req.links.map((link) => (
                  <div
                    key={link.id}
                    className="flex flex-col gap-4 py-4 md:flex-row md:items-start md:justify-between"
                  >
                    <div>
                      <p className="font-mono text-sm text-[var(--text-primary)]">
                        {link.symbol?.name || link.symbolId}
                      </p>
                      {link.symbol?.filePath ? (
                        <div className="mt-1 text-xs text-[var(--text-tertiary)]">
                          {repoId ? (
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
                          ) : (
                            link.symbol.filePath
                          )}
                        </div>
                      ) : null}
                      {link.rationale ? (
                        <p className="mt-2 text-sm text-[var(--text-secondary)]">{link.rationale}</p>
                      ) : null}
                    </div>
                    <div className="flex items-center gap-3">
                      <ConfidenceBadge level={confidenceLevel(link.confidence)} />
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => handleVerify(link.id, !link.verified)}
                      >
                        {link.verified ? "Verified" : "Verify"}
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Panel>
        </>
      ) : null}
    </PageFrame>
  );
}
