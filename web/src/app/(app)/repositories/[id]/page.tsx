"use client";

import { useState, useEffect, useMemo, useCallback, useRef } from "react";
import Link from "next/link";
import { useParams, usePathname, useRouter, useSearchParams } from "next/navigation";
import { useClient, useQuery, useMutation } from "urql";
import {
  REPOSITORY_QUERY,
  SYMBOLS_QUERY,
  REQUIREMENTS_QUERY,
  LIVING_WIKI_GLOBAL_SETTINGS_QUERY,
  BUILD_REPOSITORY_UNDERSTANDING_MUTATION,
  ANALYZE_SYMBOL_MUTATION,
  DISCUSS_CODE_MUTATION,
  REVIEW_CODE_MUTATION,
  AUTO_LINK_MUTATION,
  IMPORT_REQUIREMENTS_MUTATION,
  LATEST_IMPACT_REPORT_QUERY,
  DISCOVERED_REQUIREMENTS_QUERY,
  TRIGGER_SPEC_EXTRACTION_MUTATION,
  PROMOTE_DISCOVERED_REQUIREMENT_MUTATION,
  DISMISS_DISCOVERED_REQUIREMENT_MUTATION,
  DISMISS_ALL_DISCOVERED_REQUIREMENTS_MUTATION,
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
import { FileTree } from "@/components/source/FileTree";
import { EnterpriseSourcePanel } from "@/components/source/EnterpriseSourcePanel";
import { SourceViewerPane } from "@/components/source/SourceViewerPane";
import {
  sourceTargetFromSearchParams,
  type SourceTarget,
} from "@/lib/source-target";
import { Breadcrumb } from "@/components/ui/breadcrumb";
import { cn } from "@/lib/utils";
import { LazyScoreBreakdown } from "@/components/understanding-score";
import { ImpactReportPanel } from "@/components/impact-report";
import { ChangeSimulationPanel } from "@/components/change-simulation";
import { ArchitectureDiagram } from "@/components/architecture/ArchitectureDiagram";
import { RelatedReposPanel } from "@/components/federation/RelatedReposPanel";
import { CreateRequirementDialog } from "@/components/requirements/CreateRequirementDialog";
import { UpstreamStalenessPill } from "@/components/repository/UpstreamStalenessPill";
import { RepositoryDetailSkeleton } from "./repository-detail-skeleton";
import { KnowledgeTab } from "./tabs/knowledge-tab";
import { SymbolsTab } from "./tabs/symbols-tab";
import { SettingsTab } from "./tabs/settings-tab";
import { ClusterTable } from "@/components/subsystems/ClusterTable";
import { ImproveLabelsButton } from "@/components/subsystems/ImproveLabelsButton";
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

interface ReqNode {
  id: string;
  externalId: string;
  title: string;
  source: string;
  priority: string;
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
  const [symbolQuery, setSymbolQuery] = useState("");
  const [selectedSymbol, setSelectedSymbol] = useState<string | null>(null);
  const [symbolKindFilter, setSymbolKindFilter] = useState<string | null>(null);
  const [analysisResult, setAnalysisResult] = useState<{ summary: string; purpose: string; concerns: string[]; suggestions: string[] } | null>(null);
  const [discussQuestion, setDiscussQuestion] = useState("");
  const [discussResult, setDiscussResult] = useState<{ answer: string } | null>(null);
  const [reviewFile, setReviewFile] = useState("");
  const [reviewTemplate, setReviewTemplate] = useState("security");
  const [reviewResult, setReviewResult] = useState<{ findings: { category: string; severity: string; message: string; suggestion: string | null }[]; score: number } | null>(null);
  const [importContent, setImportContent] = useState("");
  const [aiLoading, setAiLoading] = useState(false);
  const [linkResult, setLinkResult] = useState<string | null>(null);
  const [createRequirementOpen, setCreateRequirementOpen] = useState(false);
  const [specExtracting, setSpecExtracting] = useState(false);
  const [specExtractionResult, setSpecExtractionResult] = useState<string | null>(null);
  const [specExtractionStatus, setSpecExtractionStatus] = useState<"error" | "success" | null>(null);
  const [specConfidenceFilter, setSpecConfidenceFilter] = useState<string | null>(null);
  const [repoJobs, setRepoJobs] = useState<RepoJobActivityResponse | null>(null);
  const [repoJobsError, setRepoJobsError] = useState<string | null>(null);
  const [cancellingJobIds, setCancellingJobIds] = useState<Record<string, boolean>>({});
  const repoJobsPollRef = useRef<number | null>(null);
  const [understandingDedupeNote, setUnderstandingDedupeNote] = useState(false);
  const seenRepoTerminalRef = useRef<Record<string, string>>({});
  const locallyCancelledJobsRef = useRef<Record<string, number>>({});

  const [repoResult, reexecuteRepo] = useQuery({ query: REPOSITORY_QUERY, variables: { id: repoId } });
  const [globalWikiResult] = useQuery({ query: LIVING_WIKI_GLOBAL_SETTINGS_QUERY });
  const [symbolsResult] = useQuery({
    query: SYMBOLS_QUERY,
    variables: { repositoryId: repoId, query: symbolQuery || undefined, kind: symbolKindFilter || undefined, limit: 200 },
    pause: tab !== "symbols" && tab !== "analysis",
  });
  const [reqsResult, reexecuteRequirements] = useQuery({
    query: REQUIREMENTS_QUERY,
    variables: { repositoryId: repoId, limit: 50 },
    pause: tab !== "requirements",
  });

  const [discoveredReqsResult, reexecuteDiscoveredReqs] = useQuery({
    query: DISCOVERED_REQUIREMENTS_QUERY,
    variables: { repositoryId: repoId, limit: 100 },
    pause: tab !== "specs",
  });

  const [impactResult] = useQuery({
    query: LATEST_IMPACT_REPORT_QUERY,
    variables: { repositoryId: repoId },
    pause: tab !== "impact",
  });

  const fetchRepoJobs = useCallback(async () => {
    try {
      const res = await authFetch(`/api/v1/admin/llm/activity?repo_id=${encodeURIComponent(repoId)}&limit=40`);
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
  const [, analyzeSymbol] = useMutation(ANALYZE_SYMBOL_MUTATION);
  const [, discussCode] = useMutation(DISCUSS_CODE_MUTATION);
  const [, reviewCode] = useMutation(REVIEW_CODE_MUTATION);
  const [, autoLink] = useMutation(AUTO_LINK_MUTATION);
  const [, importReqs] = useMutation(IMPORT_REQUIREMENTS_MUTATION);
  const [, triggerSpecExtraction] = useMutation(TRIGGER_SPEC_EXTRACTION_MUTATION);
  const [, promoteDiscoveredReq] = useMutation(PROMOTE_DISCOVERED_REQUIREMENT_MUTATION);
  const [, dismissDiscoveredReq] = useMutation(DISMISS_DISCOVERED_REQUIREMENT_MUTATION);
  const [, dismissAllDiscoveredReqs] = useMutation(DISMISS_ALL_DISCOVERED_REQUIREMENTS_MUTATION);

  const repo = repoResult.data?.repository;
  const files: FileNode[] = repo?.files?.nodes || [];
  const symbols: SymbolNode[] = symbolsResult.data?.symbols?.nodes || [];
  // Requirements: load first 50 fast, lazy-load the rest
  const urqlClient = useClient();
  const initialReqs: ReqNode[] = reqsResult.data?.requirements?.nodes || [];
  const reqsTotalCount: number = reqsResult.data?.requirements?.totalCount ?? 0;
  const [extraReqs, setExtraReqs] = useState<ReqNode[]>([]);
  const [loadingMoreReqs, setLoadingMoreReqs] = useState(false);

  useEffect(() => {
    if (tab !== "requirements" || initialReqs.length < 50 || initialReqs.length >= reqsTotalCount) {
      return;
    }
    let cancelled = false;
    setLoadingMoreReqs(true);

    (async () => {
      const allExtra: ReqNode[] = [];
      let offset = 50;
      const batchSize = 200;

      while (!cancelled) {
        const result = await urqlClient
          .query(REQUIREMENTS_QUERY, { repositoryId: repoId, limit: batchSize, offset })
          .toPromise();
        const batch: ReqNode[] = result.data?.requirements?.nodes || [];
        if (batch.length === 0) break;
        allExtra.push(...batch);
        offset += batch.length;
        if (batch.length < batchSize) break;
      }

      if (!cancelled) {
        setExtraReqs(allExtra);
        setLoadingMoreReqs(false);
      }
    })();

    return () => { cancelled = true; };
  }, [tab, initialReqs.length, reqsTotalCount, repoId, urqlClient]);

  const reqs: ReqNode[] = [...initialReqs, ...extraReqs];

  const features = useFeatures();
  const symbolScopedAnalysisEnabled = features.symbolScopedAnalysis;
  const [subsystemsRefreshKey, setSubsystemsRefreshKey] = useState(0);
  const [loadingOps, setLoadingOps] = useState<Set<string>>(new Set());
  const isLoading = (op: string) => loadingOps.has(op);
  const startLoading = (op: string) =>
    setLoadingOps((prev) => new Set(prev).add(op));
  const finishLoading = (op: string) =>
    setLoadingOps((prev) => { const n = new Set(prev); n.delete(op); return n; });
  // Derived: true when any knowledge operation is in flight.
  // Kept for any code that needs a global gate; individual buttons should prefer isLoading(op).
  const knowledgeLoading = loadingOps.size > 0;
  const sourceTarget = useMemo(
    () => sourceTargetFromSearchParams(new URLSearchParams(searchParams.toString())),
    [searchParams]
  );
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

  async function handleAnalyze(symId: string) {
    trackEvent({ event: "analyze_symbol_used", repositoryId: repoId, metadata: { symbolId: symId } });
    setAiLoading(true);
    setAnalysisResult(null);
    try {
      const res = await analyzeSymbol({ repositoryId: repoId, symbolId: symId });
      if (res.data?.analyzeSymbol) setAnalysisResult(res.data.analyzeSymbol);
    } finally {
      setAiLoading(false);
    }
  }

  async function handleDiscuss() {
    if (!discussQuestion.trim()) return;
    trackEvent({ event: "discuss_code_used", repositoryId: repoId, metadata: { questionLength: discussQuestion.trim().length } });
    setAiLoading(true);
    setDiscussResult({ answer: "" });
    try {
      // Use the SSE streaming endpoint so the user sees tokens as
      // they're generated. On error we fall back to the legacy
      // GraphQL mutation so older servers (where /discuss/stream
      // isn't mounted) still work.
      const { askStream } = await import("@/lib/askStream");
      let accumulated = "";
      let streamErrored = false;
      await askStream(
        { repositoryId: repoId, question: discussQuestion.trim() },
        {
          onToken: (delta) => {
            accumulated += delta;
            setDiscussResult({ answer: accumulated });
          },
          onDone: (result) => {
            // Server's final answer is authoritative — prefer it
            // when non-empty (it may have been post-processed) and
            // otherwise keep whatever we streamed.
            setDiscussResult({ answer: result.answer || accumulated });
          },
          onError: () => {
            streamErrored = true;
          },
        },
      );
      if (streamErrored) {
        const res = await discussCode({ input: { repositoryId: repoId, question: discussQuestion } });
        if (res.data?.discussCode) setDiscussResult(res.data.discussCode);
      }
    } finally {
      setAiLoading(false);
    }
  }

  async function handleReview() {
    if (!reviewFile.trim()) return;
    trackEvent({ event: "review_code_used", repositoryId: repoId, metadata: { template: reviewTemplate, filePath: reviewFile } });
    setAiLoading(true);
    setReviewResult(null);
    try {
      const res = await reviewCode({ input: { repositoryId: repoId, filePath: reviewFile, template: reviewTemplate } });
      if (res.data?.reviewCode) setReviewResult(res.data.reviewCode);
    } finally {
      setAiLoading(false);
    }
  }

  async function handleAutoLink() {
    setAiLoading(true);
    setLinkResult(null);
    try {
      const res = await autoLink({ repositoryId: repoId });
      if (res.data?.autoLinkRequirements) {
        const { linksCreated, requirementsProcessed } = res.data.autoLinkRequirements;
        setLinkResult(`Processed ${requirementsProcessed} requirements, created ${linksCreated} links.`);
      } else if (res.error) {
        setLinkResult(`Auto-link failed: ${res.error.message}`);
      }
    } finally {
      setAiLoading(false);
    }
  }

  async function handleImportReqs() {
    if (!importContent.trim()) return;
    trackEvent({ event: "requirements_imported", repositoryId: repoId });
    await importReqs({ input: { repositoryId: repoId, content: importContent, format: "MARKDOWN" } });
    setImportContent("");
  }

  async function handleExtractSpecs() {
    trackEvent({ event: "spec_extraction_triggered", repositoryId: repoId });
    setSpecExtracting(true);
    setSpecExtractionResult(null);
    setSpecExtractionStatus(null);
    try {
      const res = await triggerSpecExtraction({ input: { repositoryId: repoId } });
      if (res.data?.triggerSpecExtraction) {
        const r = res.data.triggerSpecExtraction;
        setSpecExtractionResult(`Discovered ${r.discovered} specs from ${r.totalCandidates} candidates`);
        setSpecExtractionStatus("success");
      } else if (res.error) {
        setSpecExtractionResult(`Extraction failed: ${res.error.message}`);
        setSpecExtractionStatus("error");
      }
      reexecuteDiscoveredReqs({ requestPolicy: "network-only" });
    } finally {
      setSpecExtracting(false);
    }
  }

  async function handlePromoteSpec(id: string) {
    await promoteDiscoveredReq({ id });
    reexecuteDiscoveredReqs({ requestPolicy: "network-only" });
  }

  async function handleDismissSpec(id: string) {
    await dismissDiscoveredReq({ id });
    reexecuteDiscoveredReqs({ requestPolicy: "network-only" });
  }

  async function handleDismissAllSpecs() {
    await dismissAllDiscoveredReqs({ repositoryId: repoId });
    reexecuteDiscoveredReqs({ requestPolicy: "network-only" });
  }

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

  const inputClass =
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]";
  const inputCompactClass =
    "rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)]";
  const listContainerClass = "max-h-[60vh] overflow-y-auto";
  const listRowClass =
    "border-b border-[var(--border-subtle)] px-0 py-2.5 text-sm last:border-b-0";

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

  function openSource(target: SourceTarget) {
    // Use updateSearchParams (not buildRepositorySourceHref +
    // router.replace) so we preserve every other param on the URL.
    // Reconstructing the href from scratch drops things like
    // `engine`, `scope`, and `scopePath`, which other tabs depend
    // on to stay consistent when the user navigates back out of
    // the file view.
    updateSearchParams((next) => {
      next.set("tab", target.tab ?? "files");
      next.set("file", target.filePath);
      if (typeof target.line === "number" && target.line > 0) {
        next.set("line", String(target.line));
      } else {
        next.delete("line");
      }
      if (typeof target.endLine === "number" && target.endLine > 0) {
        next.set("endLine", String(target.endLine));
      } else {
        next.delete("endLine");
      }
    });
  }


  const selectedFilePath = sourceTarget?.filePath;

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

      <div className="-mx-3 flex gap-2 overflow-x-auto scrollbar-none border-b border-[var(--border-subtle)] px-3 pb-4 sm:mx-0 sm:flex-wrap sm:overflow-visible sm:px-0">
        {tabs.map((t) => (
          <button
            key={t.key}
            onClick={() => setActiveTab(t.key)}
            className={cn(
              "shrink-0 rounded-[var(--control-radius)] border px-3 py-2 text-sm transition-colors",
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

      {/* Files Tab */}
      {tab === "files" && (
        <div className="grid gap-6 lg:grid-cols-[minmax(18rem,24rem)_minmax(0,1fr)]">
          <Panel className="min-h-[32rem]">
            <div className="mb-4 flex items-center justify-between gap-4">
              <div>
                <h3 className="text-lg font-semibold text-[var(--text-primary)]">
                  Files ({files.length})
                </h3>
                <p className="mt-1 text-sm text-[var(--text-secondary)]">
                  Browse directories and open source in the shared viewer.
                </p>
              </div>
            </div>
            {files.length === 0 ? (
              <p className="text-sm text-[var(--text-secondary)]">No files indexed yet.</p>
            ) : (
              <div className="max-h-[42rem] overflow-y-auto">
                <FileTree
                  files={files}
                  selectedPath={selectedFilePath}
                  onSelect={(file) => openSource({ filePath: file.path, tab: "files" })}
                />
              </div>
            )}
          </Panel>
          <div className="space-y-4">
            <SourceViewerPane repositoryId={repoId} target={sourceTarget} />
            <EnterpriseSourcePanel repositoryId={repoId} target={sourceTarget} />
          </div>
        </div>
      )}

      {/* Symbols Tab */}
      {tab === "symbols" && (
        <SymbolsTab
          repoId={repoId}
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
      )}

      {/* Requirements Tab */}
      {tab === "requirements" && (
        <div>
          <div className="mb-4 flex flex-wrap gap-3">
            <Button onClick={() => setCreateRequirementOpen(true)}>
              + New requirement
            </Button>
            <Button variant="secondary" onClick={handleAutoLink} disabled={aiLoading}>
              {aiLoading ? "Linking..." : "Auto-Link Specs to Code"}
            </Button>
          </div>
          {linkResult && (
            <div className={`mb-4 rounded-[var(--control-radius)] border px-3 py-2 text-sm ${linkResult.startsWith("Auto-link failed") ? "border-red-500/30 bg-red-500/10 text-red-500" : "border-emerald-500/30 bg-emerald-500/10 text-emerald-500"}`}>
              {linkResult}
            </div>
          )}
          <div className="mb-4">
            <textarea
              value={importContent}
              onChange={(e) => setImportContent(e.target.value)}
              placeholder="Paste specs or requirements in Markdown format to connect intent to code..."
              rows={3}
              className="min-h-[7rem] w-full resize-y rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-3 text-sm text-[var(--text-primary)]"
            />
            <Button className="mt-3" onClick={handleImportReqs} disabled={!importContent.trim()}>
              Import Specs
            </Button>
          </div>
          <Panel>
            <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">
              Specs & Requirements ({reqs.length}{loadingMoreReqs ? "+" : ""} of {reqsTotalCount || "..."})
            </h3>
            {reqs.length === 0 ? (
              <div className="space-y-2 text-sm text-[var(--text-secondary)]">
                <p>No specs or requirements imported yet.</p>
                <p>
                  This is optional. SourceBridge.ai can still explain the codebase, generate field guides, and review files without it.
                  Importing specs later unlocks intent-to-code links, coverage visibility, and richer change impact analysis.
                </p>
              </div>
            ) : (
              <div className={listContainerClass}>
                {reqs.map((req) => (
                  <Link
                    key={req.id}
                    href={`/requirements/${req.id}?repoId=${repoId}&repoName=${encodeURIComponent(repo?.name || "")}`}
                    className={`${listRowClass} block cursor-pointer rounded-[var(--control-radius)] px-3 transition-colors hover:bg-[var(--bg-hover)]`}
                  >
                    <div className="flex items-center justify-between gap-4">
                      <span className="font-medium text-[var(--text-primary)]">{req.externalId}</span>
                      <div className="flex items-center gap-2">
                        <span className="text-[var(--text-secondary)]">
                          {req.priority || req.source || "\u2014"}
                        </span>
                        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" className="text-[var(--text-tertiary)]">
                          <path d="M6 4l4 4-4 4" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
                        </svg>
                      </div>
                    </div>
                    <div className="mt-1 text-[var(--text-secondary)]">{req.title}</div>
                  </Link>
                ))}
              </div>
            )}
          </Panel>
          <CreateRequirementDialog
            open={createRequirementOpen}
            repositoryId={repoId}
            onClose={() => setCreateRequirementOpen(false)}
            onCreated={() => {
              // Refresh the list so the new row appears immediately.
              reexecuteRequirements({ requestPolicy: "network-only" });
            }}
          />
        </div>
      )}

      {/* Discovered Specs Tab */}
      {tab === "specs" && (
        <div>
          <div className="mb-4 flex items-center gap-4">
            <Button onClick={handleExtractSpecs} disabled={specExtracting}>
              {specExtracting ? "Extracting..." : "Extract Specs from Code"}
            </Button>
            {(discoveredReqsResult.data?.discoveredRequirements?.totalCount ?? 0) > 0 && (
              <Button variant="secondary" onClick={handleDismissAllSpecs}>
                Dismiss All
              </Button>
            )}
            <select
              value={specConfidenceFilter || ""}
              onChange={(e) => setSpecConfidenceFilter(e.target.value || null)}
              className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-2 py-2 text-sm text-[var(--text-primary)]"
            >
              <option value="">All Confidence</option>
              <option value="high">High</option>
              <option value="medium">Medium</option>
              <option value="low">Low</option>
            </select>
          </div>
          {specExtractionResult && (
            <div className={`mb-4 rounded-[var(--control-radius)] border px-3 py-2 text-sm ${
              specExtractionStatus === "error"
                ? "border-[var(--danger-border,#dc2626)] bg-[var(--danger-bg,rgba(239,68,68,0.1))] text-[var(--danger-text,#ef4444)]"
                : "border-[var(--success-border,rgba(34,197,94,0.3))] bg-[var(--success-bg,rgba(34,197,94,0.1))] text-[var(--success-text,#22c55e)]"
            }`}>
              {specExtractionResult}
            </div>
          )}
          <Panel>
            <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">
              Discovered Specs ({discoveredReqsResult.data?.discoveredRequirements?.totalCount ?? 0})
            </h3>
            {discoveredReqsResult.fetching ? (
              <p className="text-sm text-[var(--text-secondary)]">Loading...</p>
            ) : (discoveredReqsResult.data?.discoveredRequirements?.nodes?.length ?? 0) === 0 ? (
              <div className="space-y-2 text-sm text-[var(--text-secondary)]">
                <p>No discovered specs yet.</p>
                <p>
                  Click &ldquo;Extract Specs from Code&rdquo; to scan test files, API schemas, and doc comments
                  for implicit specifications that can be promoted to tracked requirements.
                </p>
              </div>
            ) : (
              <div className={listContainerClass}>
                {(discoveredReqsResult.data?.discoveredRequirements?.nodes ?? [])
                  .filter((spec: { confidence: string }) => !specConfidenceFilter || spec.confidence === specConfidenceFilter)
                  .map((spec: { id: string; text: string; source: string; sourceFile: string; sourceLine: number; confidence: string; language: string; keywords: string[]; llmRefined: boolean; status: string }) => (
                  <div
                    key={spec.id}
                    className={`${listRowClass} rounded-[var(--control-radius)] px-3`}
                  >
                    <div className="flex items-start justify-between gap-4">
                      <div className="min-w-0 flex-1">
                        <p className="text-sm font-medium text-[var(--text-primary)]">{spec.text}</p>
                        <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--text-secondary)]">
                          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${
                            spec.confidence === "high" ? "bg-emerald-500/10 text-emerald-500" :
                            spec.confidence === "medium" ? "bg-amber-500/10 text-amber-500" :
                            "bg-gray-500/10 text-gray-400"
                          }`}>
                            {spec.confidence}
                          </span>
                          <span className="rounded-full bg-[var(--bg-hover)] px-2 py-0.5">{spec.source}</span>
                          <span>{spec.sourceFile}{spec.sourceLine > 0 ? `:${spec.sourceLine}` : ""}</span>
                          {spec.llmRefined && <span className="rounded-full bg-blue-500/10 px-2 py-0.5 text-blue-400">AI-refined</span>}
                        </div>
                        {spec.keywords.length > 0 && (
                          <div className="mt-1 flex flex-wrap gap-1">
                            {spec.keywords.map((kw: string) => (
                              <span key={kw} className="rounded bg-[var(--bg-hover)] px-1.5 py-0.5 text-xs text-[var(--text-tertiary)]">{kw}</span>
                            ))}
                          </div>
                        )}
                      </div>
                      {spec.status === "discovered" && (
                        <div className="flex shrink-0 gap-2">
                          <Button variant="secondary" size="sm" onClick={() => handlePromoteSpec(spec.id)}>
                            Promote
                          </Button>
                          <Button variant="ghost" size="sm" onClick={() => handleDismissSpec(spec.id)}>
                            Dismiss
                          </Button>
                        </div>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Panel>
        </div>
      )}

      {/* Analysis Tab */}
      {tab === "analysis" && (
        <div className="grid gap-6 lg:grid-cols-2">
          <div>
            <Panel>
              <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">
                Select Symbol to Analyze
              </h3>
              <input
                type="text"
                value={symbolQuery}
                onChange={(e) => setSymbolQuery(e.target.value)}
                placeholder="Search symbols..."
                className={`${inputClass} mb-3`}
              />
              <div className="max-h-[40vh] overflow-y-auto">
                {symbols.map((sym) => (
                  <div
                    key={sym.id}
                    onClick={() => setSelectedSymbol(sym.id)}
                    className={cn(
                      `${listRowClass} cursor-pointer rounded-[var(--control-radius)] px-3`,
                      selectedSymbol === sym.id ? "bg-[var(--bg-active)]" : "bg-transparent"
                    )}
                  >
                    <span className="font-mono text-[var(--text-primary)]">{sym.name}</span>
                    <span className="ml-2 text-[var(--text-secondary)]">{sym.kind}</span>
                  </div>
                ))}
              </div>
              {selectedSymbol && (
                <Button className="mt-3" onClick={() => handleAnalyze(selectedSymbol)} disabled={aiLoading}>
                  {aiLoading ? "Analyzing..." : "Analyze Symbol"}
                </Button>
              )}
            </Panel>

            <Panel className="mt-4">
              <h3 className="mb-3 text-lg font-semibold text-[var(--text-primary)]">Discuss Code</h3>
              <input
                type="text"
                value={discussQuestion}
                onChange={(e) => setDiscussQuestion(e.target.value)}
                placeholder="Ask a question about this code..."
                className={`${inputClass} mb-3`}
              />
              <Button onClick={handleDiscuss} disabled={aiLoading || !discussQuestion.trim()}>
                {aiLoading ? "Thinking..." : "Ask"}
              </Button>
            </Panel>

            <Panel className="mt-4">
              <h3 className="mb-3 text-lg font-semibold text-[var(--text-primary)]">Review Code</h3>
              <input
                type="text"
                value={reviewFile}
                onChange={(e) => setReviewFile(e.target.value)}
                placeholder="File path (e.g. internal/api/rest/router.go)"
                className={`${inputClass} mb-3`}
              />
              <div className="flex flex-wrap gap-2">
                <select
                  value={reviewTemplate}
                  onChange={(e) => setReviewTemplate(e.target.value)}
                  className={inputCompactClass}
                >
                  <option value="security">Security</option>
                  <option value="performance">Performance</option>
                  <option value="reliability">Reliability</option>
                  <option value="maintainability">Maintainability</option>
                  <option value="solid">SOLID</option>
                  <option value="ai_detection">AI Detection</option>
                </select>
                <Button onClick={handleReview} disabled={aiLoading || !reviewFile.trim()}>
                  {aiLoading ? "Reviewing..." : "Review"}
                </Button>
              </div>
            </Panel>
          </div>

          <div>
            <Panel>
              <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">Results</h3>
              {analysisResult ? (
                <div className="text-sm">
                  <h4 className="mb-2 font-medium">Analysis</h4>
                  <p><strong>Summary:</strong> {analysisResult.summary}</p>
                  <p className="mt-2"><strong>Purpose:</strong> {analysisResult.purpose}</p>
                  {analysisResult.concerns.length > 0 && (
                    <div className="mt-2">
                      <strong>Concerns:</strong>
                      <ul className="my-1 pl-5">
                        {analysisResult.concerns.map((c, i) => <li key={i}>{c}</li>)}
                      </ul>
                    </div>
                  )}
                  {analysisResult.suggestions.length > 0 && (
                    <div className="mt-2">
                      <strong>Suggestions:</strong>
                      <ul className="my-1 pl-5">
                        {analysisResult.suggestions.map((s, i) => <li key={i}>{s}</li>)}
                      </ul>
                    </div>
                  )}
                </div>
              ) : discussResult ? (
                <div className="text-sm">
                  <h4 className="mb-2 font-medium">Discussion</h4>
                  <p className="whitespace-pre-wrap">{discussResult.answer}</p>
                </div>
              ) : reviewResult ? (
                <div className="text-sm">
                  <h4 className="mb-2 font-medium">
                    Review (Score: {Math.round(reviewResult.score * 100)}%)
                  </h4>
                  {reviewResult.findings.map((f, i) => (
                    <div key={i} className="border-b border-[var(--border-subtle)] py-2.5 last:border-b-0">
                      <span className="font-medium">[{f.severity}] {f.category}</span>
                      <p className="mt-1">{f.message}</p>
                      {f.suggestion && <p className="mt-1 text-[var(--text-secondary)]">Suggestion: {f.suggestion}</p>}
                    </div>
                  ))}
                </div>
              ) : aiLoading ? (
                <p className="text-sm text-[var(--text-secondary)]">Processing…</p>
              ) : (
                <p className="text-sm text-[var(--text-secondary)]">
                  Select a symbol and run an analysis, ask a question, or review a file.
                </p>
              )}
            </Panel>
          </div>
        </div>
      )}

      {/* Impact Tab */}
      {tab === "impact" && (
        <div className="space-y-6">
          <ChangeSimulationPanel repositoryId={repoId} />
          <ImpactReportPanel report={impactResult.data?.latestImpactReport} repositoryId={repoId} />
        </div>
      )}

      {/* Architecture Tab */}
      {tab === "architecture" && (
        <ArchitectureDiagram
          repositoryId={repoId}
          onModuleClick={(_path) => {
            setActiveTab("files");
          }}
        />
      )}

      {/* Related Tab */}
      {tab === "related" && (
        <RelatedReposPanel repositoryId={repoId} />
      )}

      {/* Knowledge Tab */}
      {tab === "knowledge" && (
        <KnowledgeTab
          repoId={repoId}
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
      )}


      {/* Subsystems Tab */}
      {tab === "subsystems" && features.subsystemClustering && repoId && (
        <Panel className="space-y-4">
          <div className="flex items-center justify-between gap-4">
            <div>
              <h3 className="text-lg font-semibold text-[var(--text-primary)]">Subsystems</h3>
              <p className="mt-0.5 text-sm text-[var(--text-secondary)]">
                Subsystems are groups of related symbols based on how they call each other. Use them to navigate the codebase, understand boundaries, and onboard faster.
              </p>
            </div>
            <ImproveLabelsButton
              repoId={repoId}
              onComplete={() => setSubsystemsRefreshKey((k) => k + 1)}
            />
          </div>
          <ClusterTable repoId={repoId} refreshKey={subsystemsRefreshKey} />
        </Panel>
      )}

      {/* Settings Tab */}
      {tab === "settings" && (
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
      )}
    </PageFrame>
  );
}
