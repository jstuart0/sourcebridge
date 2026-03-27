"use client";

import { useParams, useSearchParams } from "next/navigation";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useClient, useMutation, useQuery } from "urql";
import {
  CREATE_MANUAL_LINK_MUTATION,
  DISCUSS_CODE_MUTATION,
  ENRICH_REQUIREMENT_MUTATION,
  GENERATE_CLIFF_NOTES_MUTATION,
  REQUIREMENT_KNOWLEDGE_QUERY,
  REQUIREMENT_LINKS_QUERY,
  REQUIREMENT_QUERY,
  REPOSITORIES_LIGHT_QUERY as REPOSITORIES_QUERY,
  SYMBOLS_QUERY,
  VERIFY_LINK_MUTATION,
} from "@/lib/graphql/queries";
import { ConfidenceBadge, type ConfidenceLevel } from "@/components/code-viewer/ConfidenceBadge";
import { SourceRefLink } from "@/components/source/SourceRefLink";
import { Breadcrumb } from "@/components/ui/breadcrumb";
import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { cn } from "@/lib/utils";
import { trackEvent } from "@/lib/telemetry";

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

interface KnowledgeSection {
  id: string;
  title: string;
  content: string;
  summary: string | null;
  confidence: string;
  inferred: boolean;
  orderIndex: number;
  evidence: { id: string; sourceType: string; sourceId: string; filePath?: string; lineStart?: number; lineEnd?: number; rationale?: string }[];
}

interface KnowledgeArtifact {
  id: string;
  status: string;
  progress: number;
  stale: boolean;
  generatedAt: string | null;
  sections: KnowledgeSection[];
}

type RequirementTab = "links" | "field-guide" | "chat";

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
  const searchParams = useSearchParams();
  const reqId = params.id as string;

  // Repo context — passed when navigating from a repository's requirements tab
  const fromRepoId = searchParams.get("repoId");
  const fromRepoName = searchParams.get("repoName");

  const [reqResult, reexecute] = useQuery({ query: REQUIREMENT_QUERY, variables: { id: reqId } });
  const [reposResult] = useQuery({ query: REPOSITORIES_QUERY });
  const repoId = fromRepoId || reposResult.data?.repositories?.[0]?.id || "";

  const [activeTab, setActiveTab] = useState<RequirementTab>("links");
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
  const [, generateCliffNotes] = useMutation(GENERATE_CLIFF_NOTES_MUTATION);
  const [, discussCode] = useMutation(DISCUSS_CODE_MUTATION);

  // Lazy-load remaining links after the initial 50 are rendered.
  const client = useClient();
  const [extraLinks, setExtraLinks] = useState<ReqLink[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  const initialLinks: ReqLink[] = useMemo(
    () => reqResult.data?.requirement?.links || [],
    [reqResult.data?.requirement?.links],
  );

  useEffect(() => {
    if (initialLinks.length < 50) return;
    let cancelled = false;
    setLoadingMore(true);

    (async () => {
      const allExtra: ReqLink[] = [];
      let offset = 50;
      const batchSize = 200;

      while (!cancelled) {
        const result = await client
          .query(REQUIREMENT_LINKS_QUERY, { requirementId: reqId, limit: batchSize, offset })
          .toPromise();
        const batch: ReqLink[] = result.data?.requirementLinks || [];
        if (batch.length === 0) break;
        allExtra.push(...batch);
        offset += batch.length;
        if (batch.length < batchSize) break;
      }

      if (!cancelled) {
        setExtraLinks(allExtra);
        setLoadingMore(false);
      }
    })();

    return () => { cancelled = true; };
  }, [initialLinks.length, reqId, client]);

  const req: Req | null = reqResult.data?.requirement || null;
  const allLinks = useMemo(() => [...initialLinks, ...extraLinks], [initialLinks, extraLinks]);
  const symbols = symbolsResult.data?.symbols?.nodes || [];

  // --- Field Guide state ---
  const [knowledgeResult, reexecuteKnowledge] = useQuery({
    query: REQUIREMENT_KNOWLEDGE_QUERY,
    variables: { repositoryId: repoId, requirementId: reqId },
    pause: !repoId,
  });
  const artifacts: KnowledgeArtifact[] = knowledgeResult.data?.knowledgeArtifacts || [];
  const artifact = artifacts[0] || null;
  const [generating, setGenerating] = useState(false);

  // Poll while generating
  useEffect(() => {
    if (!artifact || (artifact.status !== "GENERATING" && artifact.status !== "PENDING")) return;
    const timer = setTimeout(() => {
      reexecuteKnowledge({ requestPolicy: "network-only" });
    }, 3000);
    return () => clearTimeout(timer);
  }, [artifact, reexecuteKnowledge]);

  // Timeout detection
  const lastProgressRef = useRef<number>(0);
  const lastProgressTimeRef = useRef<number>(Date.now());
  const [timedOut, setTimedOut] = useState(false);
  useEffect(() => {
    if (artifact?.status === "GENERATING") {
      const progress = artifact.progress || 0;
      if (progress !== lastProgressRef.current) {
        lastProgressRef.current = progress;
        lastProgressTimeRef.current = Date.now();
        setTimedOut(false);
      } else if (Date.now() - lastProgressTimeRef.current > 5 * 60 * 1000) {
        setTimedOut(true);
        trackEvent({ event: "requirement_field_guide_timed_out", repositoryId: repoId, metadata: { requirementId: reqId } });
      }
    } else {
      setTimedOut(false);
    }
  }, [artifact, repoId, reqId]);

  // Track FAILED status
  const prevStatusRef = useRef<string | null>(null);
  useEffect(() => {
    if (artifact?.status === "FAILED" && prevStatusRef.current !== "FAILED") {
      trackEvent({ event: "requirement_field_guide_failed", repositoryId: repoId, metadata: { requirementId: reqId } });
    }
    prevStatusRef.current = artifact?.status || null;
  }, [artifact?.status, repoId, reqId]);

  // --- Chat state ---
  const [chatMessages, setChatMessages] = useState<{ role: string; text: string }[]>([]);
  const [chatQuestion, setChatQuestion] = useState("");
  const [chatLoading, setChatLoading] = useState(false);
  const [chatError, setChatError] = useState<string | null>(null);

  // --- Handlers ---

  async function handleVerify(linkId: string, verified: boolean) {
    await verifyLink({ linkId, verified });
    setExtraLinks([]);
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
    setExtraLinks([]);
    reexecute({ requestPolicy: "network-only" });
  }

  async function handleEnrich() {
    setEnriching(true);
    await enrichReq({ requirementId: reqId });
    setExtraLinks([]);
    reexecute({ requestPolicy: "network-only" });
    setEnriching(false);
  }

  async function handleGenerateFieldGuide() {
    if (!repoId) return;
    setGenerating(true);
    trackEvent({ event: "requirement_field_guide_generated", repositoryId: repoId, metadata: { requirementId: reqId, linkedSymbolCount: allLinks.length } });
    await generateCliffNotes({
      input: {
        repositoryId: repoId,
        scopeType: "REQUIREMENT",
        scopePath: reqId,
        audience: "DEVELOPER",
        depth: "MEDIUM",
      },
    });
    setGenerating(false);
    reexecuteKnowledge({ requestPolicy: "network-only" });
  }

  async function handleRegenerateFieldGuide() {
    trackEvent({ event: "requirement_field_guide_regenerated", repositoryId: repoId, metadata: { requirementId: reqId, artifactId: artifact?.id } });
    await handleGenerateFieldGuide();
  }

  const handleChatSubmit = useCallback(async () => {
    if (!chatQuestion.trim() || !repoId) return;
    const question = chatQuestion.trim();
    setChatQuestion("");
    setChatLoading(true);
    setChatError(null);
    setChatMessages((prev) => [...prev, { role: "user", text: question }]);
    trackEvent({ event: "requirement_chat_used", repositoryId: repoId, metadata: { requirementId: reqId, hasArtifact: !!artifact } });

    const result = await discussCode({
      input: {
        repositoryId: repoId,
        question,
        artifactId: artifact?.id || undefined,
        requirementId: reqId,
        conversationHistory: chatMessages.map((m) =>
          `${m.role === "user" ? "User" : "Assistant"}: ${m.text}`
        ),
      },
    });

    setChatLoading(false);
    if (result.error) {
      setChatError("Unable to get a response. Please try again.");
    } else if (!result.data?.discussCode?.answer) {
      setChatError("No answer generated. Try rephrasing your question.");
    } else {
      setChatMessages((prev) => [...prev, { role: "assistant", text: result.data.discussCode.answer }]);
    }
  }, [chatQuestion, repoId, reqId, artifact, chatMessages, discussCode]);

  function handleTabSwitch(tab: RequirementTab) {
    setActiveTab(tab);
    trackEvent({ event: "requirement_tab_switched", repositoryId: repoId, metadata: { requirementId: reqId, tab } });
  }

  if (!req && !reqResult.fetching) {
    return (
      <PageFrame>
        <Panel>
          <p className="text-sm text-[var(--text-secondary)]">Requirement not found.</p>
        </Panel>
      </PageFrame>
    );
  }

  const hasLinks = allLinks.length > 0;
  const isGenerating = artifact?.status === "GENERATING" || artifact?.status === "PENDING";
  const isReady = artifact?.status === "READY";
  const isFailed = artifact?.status === "FAILED";

  return (
    <PageFrame>
      <Breadcrumb items={[
        ...(fromRepoId ? [
          { label: "Repositories", href: "/repositories" },
          { label: fromRepoName || "Repository", href: `/repositories/${fromRepoId}?tab=requirements` },
        ] : []),
        { label: "Requirements", href: fromRepoId ? `/repositories/${fromRepoId}?tab=requirements` : "/requirements" },
        { label: req?.externalId || "..." },
      ]} />

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

          {/* Tab switcher */}
          <div className="flex items-center gap-6 border-b border-[var(--border-default)]">
            {(["links", "field-guide", "chat"] as RequirementTab[]).map((tab) => (
              <button
                key={tab}
                type="button"
                onClick={() => handleTabSwitch(tab)}
                className={cn(
                  "border-b-2 pb-2 pt-1 text-sm font-medium transition-colors",
                  activeTab === tab
                    ? "border-[var(--accent-primary)] text-[var(--text-primary)]"
                    : "border-transparent text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
                )}
              >
                {tab === "links" ? `Links (${allLinks.length}${loadingMore ? "+" : ""})` : tab === "field-guide" ? "Field Guide" : "Chat"}
              </button>
            ))}
            <span className="ml-auto inline-flex rounded-full bg-[var(--bg-hover)] px-2.5 py-1 text-xs font-medium text-[var(--text-tertiary)]">
              Indexed repository view
            </span>
          </div>

          {/* Links tab */}
          {activeTab === "links" ? (
            <Panel variant="elevated" className="space-y-5">
              <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                <div className="space-y-1">
                  <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                    Linked Code
                  </p>
                  <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
                    {allLinks.length} links{loadingMore ? " (loading more...)" : ""}
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

              {allLinks.length === 0 ? (
                <p className="text-sm text-[var(--text-secondary)]">
                  No code linked to this requirement yet.
                </p>
              ) : (
                <div className="max-h-[60vh] overflow-y-auto divide-y divide-[var(--border-subtle)]">
                  {allLinks.map((link) => (
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
          ) : null}

          {/* Field Guide tab */}
          {activeTab === "field-guide" ? (
            <Panel variant="elevated" className="space-y-5">
              {!hasLinks ? (
                <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-5">
                  <p className="text-sm font-medium text-[var(--text-primary)]">No linked code yet</p>
                  <p className="mt-2 text-sm text-[var(--text-secondary)]">
                    Link code to this requirement first (via Auto-Link or manual linking),
                    then generate a Field Guide to see how it&apos;s implemented.
                  </p>
                </div>
              ) : isFailed ? (
                <div className="space-y-3">
                  <p className="text-sm text-[var(--text-secondary)]">
                    Generation failed. This can happen with very large or complex requirements.
                  </p>
                  <Button onClick={handleGenerateFieldGuide}>Try Again</Button>
                </div>
              ) : timedOut ? (
                <div className="space-y-3">
                  <p className="text-sm text-[var(--text-secondary)]">
                    Generation appears to have stalled. You can try again.
                  </p>
                  <Button onClick={handleGenerateFieldGuide}>Try Again</Button>
                </div>
              ) : isGenerating ? (
                <div className="space-y-3">
                  <p className="text-sm text-[var(--text-secondary)]">
                    Generating Field Guide{artifact?.progress ? ` (${Math.round(artifact.progress * 100)}%)` : ""}…
                  </p>
                  <div className="h-2 w-full overflow-hidden rounded-full bg-[var(--bg-hover)]">
                    <div
                      className="h-full rounded-full bg-[var(--accent-primary)] transition-all"
                      style={{ width: `${(artifact?.progress || 0.05) * 100}%` }}
                    />
                  </div>
                </div>
              ) : isReady && artifact ? (
                <div className="space-y-5">
                  <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                    <div className="flex items-center gap-3">
                      <h2 className="text-lg font-semibold text-[var(--text-primary)]">Field Guide</h2>
                      {artifact.stale ? (
                        <span className="rounded-full bg-amber-500/10 px-2.5 py-0.5 text-xs font-medium text-amber-600">
                          Stale
                        </span>
                      ) : null}
                      {artifact.generatedAt ? (
                        <span className="text-xs text-[var(--text-tertiary)]">
                          Generated {new Date(artifact.generatedAt).toLocaleDateString()}
                        </span>
                      ) : null}
                    </div>
                    <div className="flex gap-2">
                      <Button variant="secondary" size="sm" onClick={handleRegenerateFieldGuide}>
                        Regenerate
                      </Button>
                    </div>
                  </div>
                  <div className="space-y-6">
                    {artifact.sections
                      .slice()
                      .sort((a, b) => a.orderIndex - b.orderIndex)
                      .map((section) => (
                        <div key={section.id} className="space-y-2">
                          <h3 className="text-sm font-semibold text-[var(--text-primary)]">{section.title}</h3>
                          <div className="prose prose-sm max-w-none text-[var(--text-secondary)]">
                            <div className="whitespace-pre-wrap text-sm">{section.content}</div>
                          </div>
                          {section.evidence.length > 0 ? (
                            <div className="mt-2 flex flex-wrap gap-2">
                              {section.evidence.slice(0, 5).map((ev) =>
                                ev.filePath ? (
                                  <span key={ev.id} className="rounded bg-[var(--bg-hover)] px-2 py-0.5 font-mono text-xs text-[var(--text-tertiary)]">
                                    {repoId ? (
                                      <SourceRefLink
                                        repositoryId={repoId}
                                        target={{ tab: "files", filePath: ev.filePath, line: ev.lineStart }}
                                        className="text-xs"
                                      >
                                        {ev.filePath}{ev.lineStart ? `:${ev.lineStart}` : ""}
                                      </SourceRefLink>
                                    ) : (
                                      `${ev.filePath}${ev.lineStart ? `:${ev.lineStart}` : ""}`
                                    )}
                                  </span>
                                ) : null
                              )}
                            </div>
                          ) : null}
                        </div>
                      ))}
                  </div>
                </div>
              ) : (
                <div className="space-y-3">
                  <p className="text-sm text-[var(--text-secondary)]">
                    Generate a Field Guide to see a cross-cutting summary of how this requirement is implemented.
                  </p>
                  <Button onClick={handleGenerateFieldGuide} disabled={generating}>
                    {generating ? "Starting…" : "Generate Field Guide"}
                  </Button>
                </div>
              )}
            </Panel>
          ) : null}

          {/* Chat tab */}
          {activeTab === "chat" ? (
            <Panel variant="elevated" className="space-y-5">
              {!hasLinks ? (
                <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-5">
                  <p className="text-sm font-medium text-[var(--text-primary)]">No linked code yet</p>
                  <p className="mt-2 text-sm text-[var(--text-secondary)]">
                    Link code to this requirement first, then generate a Field Guide to enable follow-up questions.
                  </p>
                </div>
              ) : !isReady ? (
                <p className="text-sm text-[var(--text-secondary)]">
                  Generate a Field Guide first to enable follow-up questions.
                </p>
              ) : (
                <>
                  <p className="text-xs text-[var(--text-tertiary)]">
                    Follow-up on cached requirement analysis
                  </p>
                  <div className="max-h-[50vh] space-y-4 overflow-y-auto">
                    {chatMessages.map((msg, i) => (
                      <div
                        key={i}
                        className={cn(
                          "rounded-lg px-4 py-3 text-sm",
                          msg.role === "user"
                            ? "ml-8 bg-[var(--accent-primary)]/10 text-[var(--text-primary)]"
                            : "mr-8 bg-[var(--bg-surface)] text-[var(--text-secondary)]"
                        )}
                      >
                        <div className="whitespace-pre-wrap">{msg.text}</div>
                      </div>
                    ))}
                    {chatLoading ? (
                      <div className="mr-8 rounded-lg bg-[var(--bg-surface)] px-4 py-3 text-sm text-[var(--text-tertiary)]">
                        Thinking…
                      </div>
                    ) : null}
                  </div>
                  {chatError ? (
                    <p className="text-sm text-red-500">{chatError}</p>
                  ) : null}
                  <div className="flex gap-2">
                    <input
                      type="text"
                      value={chatQuestion}
                      onChange={(e) => setChatQuestion(e.target.value)}
                      onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); handleChatSubmit(); } }}
                      placeholder="Ask about this requirement's implementation…"
                      className="h-11 flex-1 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 text-sm text-[var(--text-primary)]"
                      disabled={chatLoading}
                    />
                    <Button onClick={handleChatSubmit} disabled={chatLoading || !chatQuestion.trim()}>
                      Send
                    </Button>
                  </div>
                </>
              )}
            </Panel>
          ) : null}
        </>
      ) : null}
    </PageFrame>
  );
}
