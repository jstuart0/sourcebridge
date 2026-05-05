"use client";

import React, { useState, useEffect, useMemo, useCallback, useRef } from "react";
import Link from "next/link";
import { useParams, usePathname, useRouter, useSearchParams } from "next/navigation";
import { useQuery, useMutation } from "urql";
import {
  REPOSITORY_QUERY,
  SYMBOLS_QUERY,
  LIVING_WIKI_GLOBAL_SETTINGS_QUERY,
  BUILD_REPOSITORY_UNDERSTANDING_MUTATION,
} from "@/lib/graphql/queries";
import { useFeatures } from "@/lib/features";
import {
  understandingStageHasReadableContent,
  understandingStageIsRunning,
} from "@/lib/understanding";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { RepoJobsPopover } from "@/components/llm/repo-jobs-popover";
import type { LLMJobView } from "@/lib/llm/job-types";
import { Breadcrumb } from "@/components/ui/breadcrumb";
import { cn } from "@/lib/utils";
import { LazyScoreBreakdown } from "@/components/understanding-score";
import { UpstreamStalenessPill } from "@/components/repository/UpstreamStalenessPill";
import { RepositoryDetailSkeleton } from "./repository-detail-skeleton";
import { KnowledgeTab } from "./tabs/knowledge-tab";
import { SymbolsTab } from "./tabs/symbols-tab";
import { SettingsTab } from "./tabs/settings-tab";
import { FilesTab } from "./tabs/files-tab";
import { RequirementsTab } from "./tabs/requirements-tab";
import { SpecsTab } from "./tabs/specs-tab";
import { AnalysisTab } from "./tabs/analysis-tab";
import { ImpactTab } from "./tabs/impact-tab";
import { ArchitectureTab } from "./tabs/architecture-tab";
import { RelatedTab } from "./tabs/related-tab";
import { SubsystemsTab } from "./tabs/subsystems-tab";
import { normalizeActivityResponse } from "@/lib/llm/activity";
import { authFetch } from "@/lib/auth-fetch";
import { trackEvent } from "@/lib/telemetry";
import { notifyJobEvent } from "@/lib/notifications";

type Tab = "files" | "symbols" | "requirements" | "specs" | "analysis" | "impact" | "architecture" | "related" | "knowledge" | "subsystems" | "settings";

interface FileNode {
  id: string;
  path: string;
  language: string;
  lineCount: number;
  aiScore?: number;
  aiSignals?: string[];
}

interface SymbolNode {
  id: string;
  name: string;
  qualifiedName: string;
  kind: string;
  language: string;
  filePath: string;
  startLine: number;
  endLine: number;
  signature: string | null;
}

interface RepositoryUnderstanding {
  id: string;
  repositoryId: string;
  corpusId?: string | null;
  revisionFp: string;
  strategy?: string | null;
  stage: string;
  treeStatus: string;
  cachedNodes: number;
  totalNodes: number;
  modelUsed?: string | null;
  refreshAvailable: boolean;
  progress: number;
  progressPhase?: string | null;
  progressMessage?: string | null;
  firstPassSections?: Array<{
    title: string;
    summary: string;
  }>;
  createdAt: string;
  updatedAt: string;
  errorCode?: string | null;
  errorMessage?: string | null;
  scope: {
    scopeType: string;
    scopePath: string;
    modulePath?: string | null;
    filePath?: string | null;
    symbolName?: string | null;
  };
}

type RepositoryGenerationMode = "CLASSIC" | "UNDERSTANDING_FIRST";

// Repo-page activity-feed job. Identical to the canonical LLMJobView; kept
// as a named alias so it reads naturally in the surrounding code.
type RepoJobView = LLMJobView;

interface RepoJobActivityResponse {
  active: RepoJobView[];
  recent: RepoJobView[];
  stats: {
    in_flight: number;
    queue_depth: number;
    max_concurrency: number;
  };
}

export default function RepositoryDetailPage() {
  const params = useParams();
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const repoId = params.id as string;
  const urlTab = searchParams.get("tab");
  const tab: Tab = (urlTab && ["files", "symbols", "requirements", "specs", "analysis", "impact", "architecture", "related", "knowledge", "subsystems", "settings"].includes(urlTab))
    ? (urlTab as Tab)
    : "knowledge";

  // Cross-tab shared state
  const [symbolQuery, setSymbolQuery] = useState("");
  const [selectedSymbol, setSelectedSymbol] = useState<string | null>(null);
  const [symbolKindFilter, setSymbolKindFilter] = useState<string | null>(null);

  // Per-op AI loading (granular: each op gates only its own button)
  const [aiLoadingOps, setAiLoadingOps] = useState<Set<string>>(new Set());
  const runAiOp = useCallback(async (key: string, fn: () => Promise<void>) => {
    setAiLoadingOps((prev) => new Set(prev).add(key));
    try { await fn(); }
    finally {
      setAiLoadingOps((prev) => {
        const n = new Set(prev);
        n.delete(key);
        return n;
      });
    }
  }, []);
  const isAiLoading = useCallback((key: string) => aiLoadingOps.has(key), [aiLoadingOps]);

  const [repoJobs, setRepoJobs] = useState<RepoJobActivityResponse | null>(null);
  const [repoJobsError, setRepoJobsError] = useState<string | null>(null);
  const [cancellingJobIds, setCancellingJobIds] = useState<Record<string, boolean>>({});
  const repoJobsPollRef = useRef<number | null>(null);
  const [understandingDedupeNote, setUnderstandingDedupeNote] = useState(false);
  const seenRepoTerminalRef = useRef<Record<string, string>>({});
  const locallyCancelledJobsRef = useRef<Record<string, number>>({});

  const [repoResult, reexecuteRepo] = useQuery({ query: REPOSITORY_QUERY, variables: { id: repoId } });
  const [globalWikiResult] = useQuery({ query: LIVING_WIKI_GLOBAL_SETTINGS_QUERY });
  // Both SymbolsTab and AnalysisTab are now always mounted (hidden, not unmounted).
  // The query runs whenever either tab has been visited — once the component
  // mounts it stays mounted, so we never need to re-pause after first activation.
  const [symbolsResult] = useQuery({
    query: SYMBOLS_QUERY,
    variables: { repositoryId: repoId, query: symbolQuery || undefined, kind: symbolKindFilter || undefined, limit: 200 },
  });

  const fetchRepoJobs = useCallback(async () => {
    try {
      const res = await authFetch(`/api/v1/repositories/${encodeURIComponent(repoId)}/llm-activity?limit=40`);
      if (!res.ok) {
        throw new Error(`job activity returned ${res.status}`);
      }
      const body = normalizeActivityResponse((await res.json()) as RepoJobActivityResponse);
      setRepoJobs(body);
      setRepoJobsError(null);
    } catch (error) {
      setRepoJobsError(error instanceof Error ? error.message : "failed to load queue activity");
    }
  }, [repoId]);

  useEffect(() => {
    void fetchRepoJobs();
    const schedule = () => {
      if (repoJobsPollRef.current) window.clearInterval(repoJobsPollRef.current);
      const interval = document.visibilityState === "visible" ? 3000 : 10000;
      repoJobsPollRef.current = window.setInterval(() => {
        void fetchRepoJobs();
      }, interval);
    };
    schedule();
    const onVisibilityChange = () => schedule();
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      if (repoJobsPollRef.current) window.clearInterval(repoJobsPollRef.current);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [fetchRepoJobs]);

  useEffect(() => {
    if (!repoJobs?.recent?.length) return;
    const now = Date.now();
    for (const job of repoJobs.recent) {
      if (job.status !== "ready" && job.status !== "failed" && job.status !== "cancelled") continue;
      if (seenRepoTerminalRef.current[job.id] === job.status) continue;
      seenRepoTerminalRef.current[job.id] = job.status;
      const updatedMs = new Date(job.updated_at).getTime();
      if (!updatedMs || now - updatedMs > 20_000) continue;
      if (job.status === "ready") {
        notifyJobEvent("Repository generation completed", `${job.job_type} finished for ${repoResult.data?.repository?.name || "this repository"}.`);
      } else if (job.status === "failed") {
        notifyJobEvent("Repository generation failed", job.error_title || `${job.job_type} failed for ${repoResult.data?.repository?.name || "this repository"}.`);
      } else {
        delete locallyCancelledJobsRef.current[job.id];
      }
    }
  }, [repoJobs?.recent, repoResult.data?.repository?.name]);

  const [, buildRepositoryUnderstanding] = useMutation(BUILD_REPOSITORY_UNDERSTANDING_MUTATION);

  const repo = repoResult.data?.repository;
  const files: FileNode[] = repo?.files?.nodes || [];
  const symbols: SymbolNode[] = symbolsResult.data?.symbols?.nodes || [];

  const features = useFeatures();
  const symbolScopedAnalysisEnabled = features.symbolScopedAnalysis;
  const [loadingOps, setLoadingOps] = useState<Set<string>>(new Set());
  const isLoading = (op: string) => loadingOps.has(op);
  const startLoading = (op: string) =>
    setLoadingOps((prev) => new Set(prev).add(op));
  const finishLoading = (op: string) =>
    setLoadingOps((prev) => { const n = new Set(prev); n.delete(op); return n; });
  // Derived: true when any knowledge operation is in flight.
  const knowledgeLoading = loadingOps.size > 0;

  const currentUnderstanding: RepositoryUnderstanding | null = repoResult.data?.repository?.repositoryUnderstanding || null;
  // Source of truth for "is the understanding actively building?" lives in
  // `understandingStageIsRunning` (web/src/lib/understanding.ts), which
  // mirrors `RepositoryUnderstandingStage.IsRunning()` on the Go side
  // (internal/knowledge/models.go) and is exhaustiveness-checked at compile
  // time. Do NOT key this decision on `progress` — that's a heartbeat field
  // that the store zeroes on every non-running stage.
  const understandingBusy = understandingStageIsRunning(currentUnderstanding?.stage);

  const repoGenerationModeDefault: RepositoryGenerationMode = (repo?.generationModeDefault || "UNDERSTANDING_FIRST") as RepositoryGenerationMode;
  const repoActiveJobs = useMemo(() => repoJobs?.active ?? [], [repoJobs?.active]);
  const repoRecentJobs = useMemo(() => repoJobs?.recent ?? [], [repoJobs?.recent]);
  // The live job for the understanding build. We prefer an active job; fall
  // back to the most recent job so progress is visible immediately after the
  // run completes. The job_type matches the orchestrator key.
  const understandingJob = useMemo(() => {
    const active = repoActiveJobs.find((j) => j.job_type === "build_repository_understanding");
    if (active) return active;
    return repoRecentJobs.find((j) => j.job_type === "build_repository_understanding") ?? null;
  }, [repoActiveJobs, repoRecentJobs]);
  // True whenever an understanding build is in flight — either the persisted
  // stage says so (catches the window between mutation return and the first
  // job-poll cycle) or there is a live pending/generating job.
  const understandingBuilding = Boolean(
    understandingBusy ||
    (understandingJob && (understandingJob.status === "pending" || understandingJob.status === "generating"))
  );
  // Clear the dedupe note once the build finishes so it doesn't linger.
  useEffect(() => {
    if (!understandingBuilding) {
      setUnderstandingDedupeNote(false);
    }
  }, [understandingBuilding]);

  useEffect(() => {
    if (!repo?.id) return;
    trackEvent({
      event: tab === "knowledge" ? "field_guide_opened" : "repository_workspace_opened",
      repositoryId: repo.id,
      metadata: { tab },
    });
  }, [repo?.id, tab]);

  const allTabs: { key: Tab; label: string; visible: boolean }[] = [
    { key: "files", label: "Files", visible: true },
    { key: "symbols", label: "Symbols", visible: true },
    { key: "requirements", label: "Requirements", visible: true },
    { key: "specs", label: "Discovered Specs", visible: true },
    { key: "analysis", label: "Analysis", visible: true },
    { key: "impact", label: "Change Impact", visible: true },
    { key: "architecture", label: "Architecture", visible: true },
    { key: "related", label: "Related", visible: true },
    { key: "knowledge", label: "Field Guide", visible: true },
    { key: "subsystems", label: "Subsystems", visible: features.subsystemClustering },
    { key: "settings", label: "Settings", visible: true },
  ];
  const tabs = allTabs.filter((t) => t.visible);

  async function handleBuildRepositoryUnderstanding() {
    startLoading("understanding-build");
    setUnderstandingDedupeNote(false);
    const wasBuilding = understandingBuilding;
    try {
      // force: true so a click on "Refresh understanding" actually re-runs
      // the build even when the source revision is unchanged. Without this,
      // the resolver short-circuits and the user gets the cached result
      // (e.g. one generated with a different LLM model) silently.
      const result = await buildRepositoryUnderstanding({
        input: {
          repositoryId: repoId,
          scopeType: "REPOSITORY",
          scopePath: undefined,
          force: true,
        },
      });
      // When the orchestrator dedupes (an identical build is already in
      // flight or has just finished its first pass) the mutation returns the
      // existing understanding row with a running stage or FIRST_PASS_READY.
      // Show a one-line note so the user knows their click was joined to
      // existing work rather than silently ignored.
      const returnedStage = result.data?.buildRepositoryUnderstanding?.stage;
      if (
        wasBuilding &&
        (understandingStageIsRunning(returnedStage) ||
          understandingStageHasReadableContent(returnedStage))
      ) {
        setUnderstandingDedupeNote(true);
      }
      reexecuteRepo({ requestPolicy: "network-only" });
      void fetchRepoJobs();
    } finally {
      finishLoading("understanding-build");
    }
  }

  function updateSearchParams(mutator: (params: URLSearchParams) => void) {
    const next = new URLSearchParams(searchParams.toString());
    mutator(next);
    router.replace(`${pathname}?${next.toString()}`, { scroll: false });
  }

  function setActiveTab(nextTab: Tab) {
    updateSearchParams((next) => {
      next.set("tab", nextTab);
    });
  }

  function handleTablistKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    const navKeys = ["ArrowLeft", "ArrowRight", "Home", "End"];
    if (!navKeys.includes(e.key)) return;
    e.preventDefault();
    const visibleTabKeys = tabs.map((t) => t.key);
    const currentIdx = visibleTabKeys.indexOf(tab);
    let nextIdx = currentIdx;
    if (e.key === "ArrowLeft") nextIdx = (currentIdx - 1 + visibleTabKeys.length) % visibleTabKeys.length;
    else if (e.key === "ArrowRight") nextIdx = (currentIdx + 1) % visibleTabKeys.length;
    else if (e.key === "Home") nextIdx = 0;
    else if (e.key === "End") nextIdx = visibleTabKeys.length - 1;
    setActiveTab(visibleTabKeys[nextIdx]);
    requestAnimationFrame(() => {
      document.getElementById(`tab-${visibleTabKeys[nextIdx]}`)?.focus();
    });
  }

  if (repoResult.fetching) {
    return (
      <PageFrame>
        <RepositoryDetailSkeleton />
      </PageFrame>
    );
  }

  if (!repo) {
    return (
      <PageFrame>
        <EmptyState
          title="Repository not found"
          description="This repository may have been removed or the link is outdated."
          actions={
            <button
              onClick={() => router.push("/repositories")}
              className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-4 py-2 text-sm font-medium text-[var(--text-primary)] hover:bg-[var(--bg-hover)] transition-colors"
            >
              Back to repositories
            </button>
          }
        />
      </PageFrame>
    );
  }

  return (
    <PageFrame>
      <Breadcrumb items={[
        { label: "Repositories", href: "/repositories" },
        { label: repo?.name || "..." },
      ]} />

      <PageHeader
        eyebrow="Repository Workspace"
        title={
          <span className="inline-flex flex-wrap items-center gap-3">
            <span>{repo?.name || "Repository"}</span>
            {repo ? <UpstreamStalenessPill repositoryId={repo.id} /> : null}
          </span>
        }
        description={repo?.remoteUrl ? (
          <a href={repo.remoteUrl} target="_blank" rel="noopener noreferrer" className="underline decoration-[var(--border-default)] underline-offset-4 transition-colors hover:text-[var(--text-primary)] hover:decoration-[var(--text-primary)]">
            {repo.path || repo.remoteUrl}
          </a>
        ) : (repo?.path || "Explore the codebase through files, symbols, field guides, reviews, and change impact.")}
        actions={repo ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="secondary"
              size="sm"
              onClick={handleBuildRepositoryUnderstanding}
              disabled={knowledgeLoading || understandingBuilding}
            >
              {knowledgeLoading
                ? "Starting..."
                : understandingBuilding
                  ? "Building understanding..."
                  : currentUnderstanding
                    ? currentUnderstanding.refreshAvailable
                      ? "Refresh understanding"
                      : "Rebuild understanding"
                    : "Build understanding"}
            </Button>
            <RepoJobsPopover repoId={repo.id} />
          </div>
        ) : null}
      />
      {repo && (
        <Panel className="w-full" padding="sm">
          <LazyScoreBreakdown repositoryId={repo.id} />
        </Panel>
      )}

      <div
        role="tablist"
        aria-label="Repository workspace"
        className="-mx-3 flex gap-2 overflow-x-auto scrollbar-none border-b border-[var(--border-subtle)] px-3 pb-4 sm:mx-0 sm:flex-wrap sm:overflow-visible sm:px-0"
        onKeyDown={handleTablistKeyDown}
      >
        {tabs.map((t) => (
          <button
            key={t.key}
            role="tab"
            aria-selected={tab === t.key}
            aria-controls={`tabpanel-${t.key}`}
            id={`tab-${t.key}`}
            tabIndex={tab === t.key ? 0 : -1}
            onClick={() => setActiveTab(t.key)}
            className={cn(
              "min-h-[44px] shrink-0 rounded-[var(--control-radius)] border px-3 py-2 text-sm transition-colors",
              tab === t.key
                ? "border-[var(--nav-item-border)] bg-[var(--nav-item-bg-active)] font-medium text-[var(--text-primary)]"
                : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {/* Living Wiki discoverability callout — files tab only */}
      {tab === "files" &&
        globalWikiResult.data?.livingWikiSettings?.enabled &&
        !globalWikiResult.data?.livingWikiSettings?.killSwitchActive &&
        !repo?.livingWikiSettings?.enabled &&
        (repo?.fileCount ?? 0) > 0 && (
          <div className="flex items-start gap-3 rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-4 py-3">
            <span className="mt-0.5 shrink-0 text-sm text-[var(--text-tertiary)]" aria-hidden="true">
              ℹ
            </span>
            <div className="min-w-0 flex-1">
              <p className="text-sm text-[var(--text-secondary)]">
                Keep your docs in sync automatically with Living Wiki.
              </p>
            </div>
            <Link
              href={`/repositories/${repoId}?tab=settings#wiki`}
              className="shrink-0 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-1.5 text-xs font-medium text-[var(--text-primary)] hover:bg-[var(--bg-hover)]"
            >
              Set up
            </Link>
          </div>
        )}

      {/* Files Tab — always mounted; hidden attribute hides it from view */}
      <div role="tabpanel" id="tabpanel-files" aria-labelledby="tab-files" hidden={tab !== "files"}>
        <FilesTab repoId={repoId} files={files} />
      </div>

      {/* Symbols Tab — always mounted; active gates polling effects */}
      <div role="tabpanel" id="tabpanel-symbols" aria-labelledby="tab-symbols" hidden={tab !== "symbols"}>
        <SymbolsTab
          repoId={repoId}
          active={tab === "symbols"}
          symbols={symbols}
          symbolsTotalCount={symbolsResult.data?.symbols?.totalCount ?? null}
          symbolQuery={symbolQuery}
          setSymbolQuery={setSymbolQuery}
          selectedSymbol={selectedSymbol}
          setSelectedSymbol={setSelectedSymbol}
          symbolKindFilter={symbolKindFilter}
          setSymbolKindFilter={setSymbolKindFilter}
          symbolScopedAnalysisEnabled={symbolScopedAnalysisEnabled}
          knowledgeLoading={knowledgeLoading}
          startLoading={startLoading}
          finishLoading={finishLoading}
          isLoading={isLoading}
          repoGenerationModeDefault={repoGenerationModeDefault}
        />
      </div>

      {/* Requirements Tab — always mounted; active gates query and paginate effect */}
      <div role="tabpanel" id="tabpanel-requirements" aria-labelledby="tab-requirements" hidden={tab !== "requirements"}>
        <RequirementsTab
          repoId={repoId}
          active={tab === "requirements"}
          repoName={repo?.name || ""}
          isAiLoading={isAiLoading}
          runAiOp={runAiOp}
        />
      </div>

      {/* Discovered Specs Tab — always mounted; active gates query */}
      <div role="tabpanel" id="tabpanel-specs" aria-labelledby="tab-specs" hidden={tab !== "specs"}>
        <SpecsTab repoId={repoId} active={tab === "specs"} />
      </div>

      {/* Analysis Tab — always mounted; state (results, streams) persists across switches */}
      <div role="tabpanel" id="tabpanel-analysis" aria-labelledby="tab-analysis" hidden={tab !== "analysis"}>
        <AnalysisTab
          repoId={repoId}
          symbols={symbols}
          symbolQuery={symbolQuery}
          setSymbolQuery={setSymbolQuery}
          selectedSymbolId={selectedSymbol}
          setSelectedSymbolId={setSelectedSymbol}
          isAiLoading={isAiLoading}
          runAiOp={runAiOp}
        />
      </div>

      {/* Impact Tab — always mounted; active gates query */}
      <div role="tabpanel" id="tabpanel-impact" aria-labelledby="tab-impact" hidden={tab !== "impact"}>
        <ImpactTab repoId={repoId} active={tab === "impact"} />
      </div>

      {/* Architecture Tab — always mounted */}
      <div role="tabpanel" id="tabpanel-architecture" aria-labelledby="tab-architecture" hidden={tab !== "architecture"}>
        <ArchitectureTab repoId={repoId} setActiveTab={setActiveTab} />
      </div>

      {/* Related Tab — always mounted */}
      <div role="tabpanel" id="tabpanel-related" aria-labelledby="tab-related" hidden={tab !== "related"}>
        <RelatedTab repoId={repoId} />
      </div>

      {/* Knowledge Tab — always mounted; active gates all three queries */}
      <div role="tabpanel" id="tabpanel-knowledge" aria-labelledby="tab-knowledge" hidden={tab !== "knowledge"}>
        <KnowledgeTab
          repoId={repoId}
          active={tab === "knowledge"}
          repo={repo}
          loadingOps={loadingOps}
          startLoading={startLoading}
          finishLoading={finishLoading}
          isLoading={isLoading}
          knowledgeLoading={knowledgeLoading}
          currentUnderstanding={currentUnderstanding}
          understandingBuilding={understandingBuilding}
          understandingJob={understandingJob}
          understandingDedupeNote={understandingDedupeNote}
          setUnderstandingDedupeNote={setUnderstandingDedupeNote}
          handleBuildRepositoryUnderstanding={handleBuildRepositoryUnderstanding}
          repoJobs={repoJobs}
          repoJobsError={repoJobsError}
          repoActiveJobs={repoActiveJobs}
          repoRecentJobs={repoRecentJobs}
          cancellingJobIds={cancellingJobIds}
          setCancellingJobIds={setCancellingJobIds}
          fetchRepoJobs={fetchRepoJobs}
          reexecuteRepo={reexecuteRepo}
        />
      </div>

      {/* Subsystems Tab — feature-flag guarded; always mounted when feature is on */}
      <div role="tabpanel" id="tabpanel-subsystems" aria-labelledby="tab-subsystems" hidden={tab !== "subsystems"}>
        {features.subsystemClustering && repoId && <SubsystemsTab repoId={repoId} />}
      </div>

      {/* Settings Tab — always mounted; state persists across switches */}
      <div role="tabpanel" id="tabpanel-settings" aria-labelledby="tab-settings" hidden={tab !== "settings"}>
        <SettingsTab
          repoId={repoId}
          repo={repo}
          knowledgeLoading={knowledgeLoading}
          startLoading={startLoading}
          finishLoading={finishLoading}
          repoGenerationModeDefault={repoGenerationModeDefault}
          agentSetupEnabled={features.agentSetup}
          onGenerationModeChange={() => reexecuteRepo({ requestPolicy: "network-only" })}
        />
      </div>
    </PageFrame>
  );
}
