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
  REINDEX_REPOSITORY_MUTATION,
  BUILD_REPOSITORY_UNDERSTANDING_MUTATION,
  UPDATE_REPOSITORY_KNOWLEDGE_SETTINGS_MUTATION,
  REMOVE_REPOSITORY_MUTATION,
  ANALYZE_SYMBOL_MUTATION,
  DISCUSS_CODE_MUTATION,
  REVIEW_CODE_MUTATION,
  AUTO_LINK_MUTATION,
  IMPORT_REQUIREMENTS_MUTATION,
  KNOWLEDGE_ARTIFACTS_QUERY,
  KNOWLEDGE_SCOPE_CHILDREN_QUERY,
  GENERATE_CLIFF_NOTES_MUTATION,
  REFRESH_KNOWLEDGE_ARTIFACT_MUTATION,
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
import { useServerCapabilities } from "@/lib/use-server-capabilities";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { RepoJobsPopover } from "@/components/llm/repo-jobs-popover";
import type { LLMJobView } from "@/lib/llm/job-types";
import { FileTree } from "@/components/source/FileTree";
import { EnterpriseSourcePanel } from "@/components/source/EnterpriseSourcePanel";
import { SourceRefLink } from "@/components/source/SourceRefLink";
import { SourceViewerPane } from "@/components/source/SourceViewerPane";
import {
  sourceTargetFromSearchParams,
  type SourceTarget,
} from "@/lib/source-target";
import { Breadcrumb } from "@/components/ui/breadcrumb";
import { cn, getServerOrigin } from "@/lib/utils";
import { LazyScoreBreakdown } from "@/components/understanding-score";
import { ImpactReportPanel } from "@/components/impact-report";
import { ChangeSimulationPanel } from "@/components/change-simulation";
import { ArchitectureDiagram } from "@/components/architecture/ArchitectureDiagram";
import { RelatedReposPanel } from "@/components/federation/RelatedReposPanel";
import { CreateRequirementDialog } from "@/components/requirements/CreateRequirementDialog";
import { UpstreamStalenessPill } from "@/components/repository/UpstreamStalenessPill";
import { RepositoryDetailSkeleton } from "./repository-detail-skeleton";
import { WikiSettingsPanel } from "./wiki-settings-panel";
import { ClaudeCodeWizard } from "./_components/claude-code-wizard";
import { KnowledgeTab } from "./tabs/knowledge-tab";
import { ClusterTable } from "@/components/subsystems/ClusterTable";
import { ImproveLabelsButton } from "@/components/subsystems/ImproveLabelsButton";
import { SymbolTree } from "@/components/source/SymbolTree";
import { SymbolList } from "@/components/source/SymbolList";
import { kindBadgeClass, kindLabel, SYMBOL_KINDS } from "@/components/source/symbol-kind";
import { normalizeActivityResponse } from "@/lib/llm/activity";
import { authFetch } from "@/lib/auth-fetch";
import { trackEvent } from "@/lib/telemetry";
import { notifyJobEvent } from "@/lib/notifications";

type Tab = "files" | "symbols" | "requirements" | "specs" | "analysis" | "impact" | "architecture" | "related" | "knowledge" | "subsystems" | "settings";
type SymbolDetailTab = "source" | "cliff-notes" | "chat";

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

interface KnowledgeEvidence {
  id: string;
  sectionId: string;
  sourceType: string;
  sourceId: string;
  filePath: string | null;
  lineStart: number | null;
  lineEnd: number | null;
  rationale: string | null;
}

interface KnowledgeSection {
  id: string;
  artifactId: string;
  title: string;
  content: string;
  summary: string | null;
  metadata?: string | null;
  sectionKey?: string | null;
  refinementStatus?: string | null;
  confidence: string;
  inferred: boolean;
  orderIndex: number;
  evidence: KnowledgeEvidence[];
}

interface ArtifactDependency {
  dependencyType: string;
  targetId: string;
  targetRevisionFp: string | null;
}

interface KnowledgeRefinementUnit {
  id: string;
  artifactId: string;
  sectionKey: string;
  sectionTitle: string;
  refinementType: string;
  status: string;
  attemptCount: number;
  understandingId?: string | null;
  evidenceRevisionFp?: string | null;
  rendererVersion?: string | null;
  lastError?: string | null;
  metadata?: string | null;
  createdAt: string;
  updatedAt: string;
}

interface KnowledgeArtifact {
  id: string;
  repositoryId: string;
  type: string;
  audience: string;
  depth: string;
  scope: {
    scopeType: string;
    scopePath: string;
    modulePath: string | null;
    filePath: string | null;
    symbolName: string | null;
  };
  status: string;
  progress: number;
  progressPhase: string | null;
  progressMessage: string | null;
  stale: boolean;
  errorCode: string | null;
  errorMessage: string | null;
  understandingId?: string | null;
  understandingRevisionFp?: string | null;
  generationMode?: string | null;
  rendererVersion?: string | null;
  dependencies?: ArtifactDependency[];
  refinementUnits?: KnowledgeRefinementUnit[];
  refreshAvailable?: boolean;
  generatedAt: string | null;
  createdAt: string;
  updatedAt: string;
  sourceRevision?: {
    commitSha?: string | null;
    branch?: string | null;
    contentFingerprint?: string | null;
  };
  sections: KnowledgeSection[];
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

interface CliffNotesSectionMetadata {
  section_key?: string | null;
  refinement_tier?: string | null;
  refined_with_evidence?: boolean | null;
  evidence_revision_fp?: string | null;
  renderer_version?: string | null;
  understanding_id?: string | null;
}

type RepositoryGenerationMode = "CLASSIC" | "UNDERSTANDING_FIRST";



function parseCliffNotesSectionMetadata(section: KnowledgeSection): CliffNotesSectionMetadata | null {
  if (!section.metadata?.trim()) return null;
  try {
    const parsed = JSON.parse(section.metadata) as CliffNotesSectionMetadata;
    if (!parsed || typeof parsed !== "object") return null;
    return parsed;
  } catch {
    return null;
  }
}

function shortFingerprint(value: string | null | undefined): string | null {
  const trimmed = value?.trim();
  if (!trimmed) return null;
  return trimmed.length > 12 ? trimmed.slice(0, 12) : trimmed;
}

function renderCliffNotesSectionProvenance(section: KnowledgeSection) {
  const metadata = parseCliffNotesSectionMetadata(section);
  if (!metadata) return null;
  const parts: string[] = [];
  if (metadata.refined_with_evidence) parts.push("Evidence-backed");
  if (metadata.refinement_tier?.trim()) parts.push(`Tier ${metadata.refinement_tier.trim()}`);
  if (metadata.renderer_version?.trim()) parts.push(`Renderer ${metadata.renderer_version.trim()}`);
  const understanding = shortFingerprint(metadata.understanding_id);
  if (understanding) parts.push(`Understanding ${understanding}`);
  const evidenceRevision = shortFingerprint(metadata.evidence_revision_fp);
  if (evidenceRevision) parts.push(`Evidence rev ${evidenceRevision}`);
  if (!parts.length) return null;
  return (
    <div className="mt-3 flex flex-wrap gap-2 text-xs text-[var(--text-tertiary)]">
      {parts.map((part) => (
        <span
          key={part}
          className="rounded-full border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-2 py-0.5"
        >
          {part}
        </span>
      ))}
    </div>
  );
}



interface ScopeChild {
  scopeType: string;
  label: string;
  scopePath: string;
  hasArtifact: boolean;
  summary: string | null;
}


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

interface SymbolChatMessage {
  role: "user" | "assistant";
  text: string;
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
  const [symbolView, setSymbolView] = useState<"list" | "tree">("list");
  const [symbolKindFilter, setSymbolKindFilter] = useState<string | null>(null);
  const [symbolDetailTab, setSymbolDetailTab] = useState<SymbolDetailTab>("source");
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
  const [symbolChatQuestion, setSymbolChatQuestion] = useState("");
  const [symbolChatByScope, setSymbolChatByScope] = useState<Record<string, SymbolChatMessage[]>>({});
  const [symbolExpandedSection, setSymbolExpandedSection] = useState<string | null>(null);
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

  const [removeRepoConfirmOpen, setRemoveRepoConfirmOpen] = useState(false);

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

  const [, reindex] = useMutation(REINDEX_REPOSITORY_MUTATION);
  const [, buildRepositoryUnderstanding] = useMutation(BUILD_REPOSITORY_UNDERSTANDING_MUTATION);
  const [, updateRepositoryKnowledgeSettings] = useMutation(UPDATE_REPOSITORY_KNOWLEDGE_SETTINGS_MUTATION);
  const [, removeRepo] = useMutation(REMOVE_REPOSITORY_MUTATION);
  const [, analyzeSymbol] = useMutation(ANALYZE_SYMBOL_MUTATION);
  const [, discussCode] = useMutation(DISCUSS_CODE_MUTATION);
  const [, reviewCode] = useMutation(REVIEW_CODE_MUTATION);
  const [, autoLink] = useMutation(AUTO_LINK_MUTATION);
  const [, importReqs] = useMutation(IMPORT_REQUIREMENTS_MUTATION);
  const [, generateCliffNotes] = useMutation(GENERATE_CLIFF_NOTES_MUTATION);
  const [, refreshArtifact] = useMutation(REFRESH_KNOWLEDGE_ARTIFACT_MUTATION);
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
  const [copiedSetupCmd, setCopiedSetupCmd] = useState(false);
  const [useExistingToken, setUseExistingToken] = useState(false);
  const serverCaps = useServerCapabilities();
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

  function renderScopedCliffNotesSection(section: KnowledgeSection) {
    return (
      <div key={section.id} className="border-t border-[var(--border-subtle)] py-4 first:border-t-0 first:pt-0">
        <div
          onClick={() => setSymbolExpandedSection(symbolExpandedSection === section.id ? null : section.id)}
          className="flex cursor-pointer items-start justify-between gap-4"
        >
          <div>
            <h3 className="text-base font-semibold text-[var(--text-primary)]">{section.title}</h3>
            {section.summary && symbolExpandedSection !== section.id ? (
              <p className="mt-1 text-sm text-[var(--text-secondary)]">{section.summary}</p>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            <span className={confidenceClass(section.confidence)}>{section.confidence}</span>
            {section.inferred ? <span className="text-xs text-[var(--text-tertiary)]">inferred</span> : null}
          </div>
        </div>
        {symbolExpandedSection === section.id ? (
          <div className="mt-3">
            <p className="whitespace-pre-wrap text-sm leading-7 text-[var(--text-secondary)]">{section.content}</p>
            {renderCliffNotesSectionProvenance(section)}
            {section.evidence.length > 0 ? (
              <div className="mt-4 rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                <p className="mb-2 text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Evidence</p>
                <div className="space-y-2">
                  {section.evidence.map((ev) => (
                    <div key={ev.id} className="text-xs text-[var(--text-secondary)]">
                      {ev.filePath ? (
                        <SourceRefLink
                          repositoryId={repoId}
                          target={{
                            tab: "files",
                            filePath: ev.filePath,
                            line: ev.lineStart ?? undefined,
                            endLine: ev.lineEnd ?? undefined,
                          }}
                          className="text-xs"
                        >
                          {ev.filePath}
                          {ev.lineStart ? `:${ev.lineStart}` : ""}
                        </SourceRefLink>
                      ) : null}
                      {ev.rationale ? <span className="ml-2">{ev.rationale}</span> : null}
                    </div>
                  ))}
                </div>
              </div>
            ) : null}
          </div>
        ) : null}
      </div>
    );
  }

  async function handleGenerateScopedCliffNotes() {
    if (!symbolScopeType) return;
    startLoading("scoped-cliff-notes-generate");
    try {
      await generateCliffNotes({
        input: {
          repositoryId: repoId,
          audience: "DEVELOPER",
          depth: "MEDIUM",
          generationMode: repoGenerationModeDefault,
          scopeType: symbolScopeType,
          scopePath: symbolScopePath,
        },
      });
      reexecuteSymbolKnowledge({ requestPolicy: "network-only" });
      reexecuteSymbolChildren({ requestPolicy: "network-only" });
    } finally {
      finishLoading("scoped-cliff-notes-generate");
    }
  }

  async function handleRefreshScopedArtifact() {
    if (!currentScopedCliffNotes) return;
    startLoading("scoped-artifact-refresh");
    try {
      await refreshArtifact({ id: currentScopedCliffNotes.id });
      reexecuteSymbolKnowledge({ requestPolicy: "network-only" });
      reexecuteSymbolChildren({ requestPolicy: "network-only" });
    } finally {
      finishLoading("scoped-artifact-refresh");
    }
  }

  async function handleScopedFollowUp() {
    if (!currentScopedCliffNotes || !symbolChatQuestion.trim()) return;
    startLoading("scoped-follow-up");
    try {
      const question = symbolChatQuestion.trim();
      const historyPayload = symbolChatMessages.map((message) =>
        `${message.role === "user" ? "User" : "Assistant"}: ${message.text}`
      );
      const res = await discussCode({
        input: {
          repositoryId: repoId,
          question,
          artifactId: currentScopedCliffNotes.id,
          symbolId: selectedSymbolNode?.id,
          conversationHistory: historyPayload,
        },
      });
      if (res.data?.discussCode?.answer) {
        setSymbolChatByScope((current) => ({
          ...current,
          [symbolChatScopeKey]: [
            ...(current[symbolChatScopeKey] || []),
            { role: "user", text: question },
            { role: "assistant", text: res.data.discussCode.answer },
          ],
        }));
        setSymbolChatQuestion("");
      }
    } finally {
      finishLoading("scoped-follow-up");
    }
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

  async function handleSaveRepositoryGenerationMode(nextMode: RepositoryGenerationMode) {
    startLoading("generation-mode-save");
    try {
      await updateRepositoryKnowledgeSettings({
        input: {
          repositoryId: repoId,
          generationModeDefault: nextMode,
        },
      });
      reexecuteRepo({ requestPolicy: "network-only" });
    } finally {
      finishLoading("generation-mode-save");
    }
  }


  const inputClass =
    "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]";
  const inputCompactClass =
    "rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)]";
  const listContainerClass = "max-h-[60vh] overflow-y-auto";
  const listRowClass =
    "border-b border-[var(--border-subtle)] px-0 py-2.5 text-sm last:border-b-0";
  const artifactStatusClass =
    "rounded-full border border-[var(--border-default)] bg-[var(--bg-hover)] px-2.5 py-1 text-xs text-[var(--text-secondary)]";
  const confidenceClass = (confidence: string) =>
    cn(
      "rounded-full px-1.5 py-0.5 text-xs text-white",
      confidence === "HIGH"
        ? "bg-[var(--confidence-high,#22c55e)]"
        : confidence === "MEDIUM"
          ? "bg-[var(--confidence-medium,#f59e0b)]"
          : "bg-[var(--confidence-low,#ef4444)]"
    );

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
  const selectedSymbolNode =
    selectedSymbol && symbols.length > 0 ? symbols.find((sym) => sym.id === selectedSymbol) ?? null : null;
  const symbolScopeType = selectedSymbolNode ? "SYMBOL" : selectedFilePath ? "FILE" : null;
  const symbolScopePath = selectedSymbolNode
    ? `${selectedSymbolNode.filePath}#${selectedSymbolNode.name}`
    : selectedFilePath || "";
  const selectedSymbolFilePath = selectedSymbolNode?.filePath || selectedFilePath || null;
  const [symbolKnowledgeResult, reexecuteSymbolKnowledge] = useQuery({
    query: KNOWLEDGE_ARTIFACTS_QUERY,
    variables: symbolScopeType
      ? { repositoryId: repoId, scopeType: symbolScopeType, scopePath: symbolScopePath }
      : undefined,
    pause: tab !== "symbols" || !symbolScopedAnalysisEnabled || !symbolScopeType,
  });
  const [symbolChildrenResult, reexecuteSymbolChildren] = useQuery({
    query: KNOWLEDGE_SCOPE_CHILDREN_QUERY,
    variables: selectedSymbolFilePath
      ? {
          repositoryId: repoId,
          scopeType: "FILE",
          scopePath: selectedSymbolFilePath,
          audience: "DEVELOPER",
          depth: "MEDIUM",
        }
      : undefined,
    pause: tab !== "symbols" || !symbolScopedAnalysisEnabled || !selectedSymbolFilePath,
  });
  const symbolKnowledgeArtifacts: KnowledgeArtifact[] = symbolKnowledgeResult.data?.knowledgeArtifacts || [];
  const hasGeneratingScopedArtifact = symbolKnowledgeResult.data?.knowledgeArtifacts?.some(
    (a: KnowledgeArtifact) => a.status === "GENERATING" || a.status === "PENDING"
  );
  const currentScopedCliffNotes = symbolKnowledgeArtifacts.find(
    (a) => a.type === "CLIFF_NOTES" && a.audience === "DEVELOPER" && a.depth === "MEDIUM"
  );
  const scopedArtifactNeedsImpactRefresh =
    currentScopedCliffNotes?.scope.scopeType === "SYMBOL" &&
    currentScopedCliffNotes.status === "READY" &&
    !currentScopedCliffNotes.sections.some((section) => section.title === "Impact Analysis");
  const symbolHasReadyArtifactPaths = new Set<string>(
    (symbolChildrenResult.data?.knowledgeScopeChildren || [])
      .filter((child: ScopeChild) => child.hasArtifact)
      .map((child: ScopeChild) => String(child.scopePath))
  );
  const symbolChatScopeKey = symbolScopeType ? `${symbolScopeType}:${symbolScopePath}` : "none";
  const symbolChatMessages = symbolChatByScope[symbolChatScopeKey] || [];

  useEffect(() => {
    setSymbolDetailTab("source");
    setSymbolChatQuestion("");
  }, [symbolScopeType, symbolScopePath]);
  useEffect(() => {
    if (!hasGeneratingScopedArtifact) return;
    const interval = setInterval(() => {
      reexecuteSymbolKnowledge({ requestPolicy: "network-only" });
      reexecuteSymbolChildren({ requestPolicy: "network-only" });
    }, 2000);
    return () => clearInterval(interval);
  }, [hasGeneratingScopedArtifact, reexecuteSymbolKnowledge, reexecuteSymbolChildren]);

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
        <div className="grid gap-6 lg:grid-cols-[minmax(20rem,28rem)_minmax(0,1fr)]">
          <div>
            {/* Search + view toggle row */}
            <div className="mb-3 flex items-center gap-3">
              <input
                type="text"
                value={symbolQuery}
                onChange={(e) => setSymbolQuery(e.target.value)}
                placeholder="Search symbols..."
                className={`${inputClass} min-w-0 flex-1`}
              />
              <div className="flex shrink-0 gap-1 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-1">
                {(["list", "tree"] as const).map((v) => (
                  <button
                    key={v}
                    type="button"
                    onClick={() => setSymbolView(v)}
                    className={cn(
                      "rounded-[var(--control-radius)] px-2.5 py-1.5 text-xs font-medium transition-colors",
                      symbolView === v
                        ? "bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                        : "text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                    )}
                  >
                    {v === "list" ? "List" : "Tree"}
                  </button>
                ))}
              </div>
            </div>

            {/* Kind filter pills */}
            <div className="mb-3 flex flex-wrap gap-1.5">
              <button
                type="button"
                onClick={() => setSymbolKindFilter(null)}
                className={cn(
                  "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                  symbolKindFilter === null
                    ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                    : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
                )}
              >
                All
              </button>
              {SYMBOL_KINDS.map((k) => (
                <button
                  key={k.value}
                  type="button"
                  onClick={() => setSymbolKindFilter(symbolKindFilter === k.value ? null : k.value)}
                  className={cn(
                    "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                    symbolKindFilter === k.value
                      ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                      : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
                  )}
                >
                  {k.label}
                </button>
              ))}
            </div>

            <Panel>
              <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">
                Symbols ({symbolsResult.data?.symbols?.totalCount ?? "..."})
              </h3>
              <div className={listContainerClass}>
                {symbolView === "tree" ? (
                  <SymbolTree
                    symbols={symbols}
                    selectedId={selectedSymbol}
                    cachedScopePaths={symbolHasReadyArtifactPaths}
                    onSelect={(sym) => {
                      setSelectedSymbol(selectedSymbol === sym.id ? null : sym.id);
                      openSource({ filePath: sym.filePath, line: sym.startLine, endLine: sym.endLine, tab: "symbols" });
                    }}
                  />
                ) : (
                  <SymbolList
                    symbols={symbols}
                    selectedId={selectedSymbol}
                    cachedScopePaths={symbolHasReadyArtifactPaths}
                    onSelect={(sym) => {
                      setSelectedSymbol(selectedSymbol === sym.id ? null : sym.id);
                      openSource({ filePath: sym.filePath, line: sym.startLine, endLine: sym.endLine, tab: "symbols" });
                    }}
                  />
                )}
              </div>
            </Panel>
          </div>
          <div className="space-y-4">
            {symbolScopedAnalysisEnabled ? (
              <Panel className="overflow-hidden">
                <div className="border-b border-[var(--border-subtle)] px-5 py-4">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div>
                      <p className="text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Symbol Detail</p>
                      <h3 className="mt-1 text-lg font-semibold text-[var(--text-primary)]">
                        {selectedSymbolNode ? selectedSymbolNode.name : selectedFilePath ? selectedFilePath.split("/").at(-1) : "Select a symbol"}
                      </h3>
                      <p className="mt-2 text-sm text-[var(--text-secondary)]">
                        <span className="inline-flex rounded-full bg-[var(--bg-hover)] px-2.5 py-1 text-xs font-medium text-[var(--text-primary)]">
                          Indexed repository view
                        </span>
                        <span className="ml-2">
                          Based on the last indexed repository state. Current editor changes are not included in this view.
                        </span>
                      </p>
                    </div>
                    <div className="flex gap-2">
                      {(["source", "cliff-notes", "chat"] as SymbolDetailTab[]).map((panelTab) => (
                        <button
                          key={panelTab}
                          type="button"
                          onClick={() => setSymbolDetailTab(panelTab)}
                          className={cn(
                            "rounded-[var(--control-radius)] border px-3 py-2 text-sm transition-colors",
                            symbolDetailTab === panelTab
                              ? "border-[var(--nav-item-border)] bg-[var(--nav-item-bg-active)] font-medium text-[var(--text-primary)]"
                              : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                          )}
                        >
                          {panelTab === "source" ? "Source" : panelTab === "cliff-notes" ? "Cliff Notes" : "Chat"}
                        </button>
                      ))}
                    </div>
                  </div>
                </div>

                <div className="px-5 py-5">
                  {!symbolScopeType ? (
                    <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-5">
                      <p className="text-sm font-medium text-[var(--text-primary)]">Select a symbol to inspect it in context.</p>
                      <p className="mt-2 text-sm text-[var(--text-secondary)]">
                        This panel keeps source, indexed Cliff Notes, and follow-up questions together so you do not need to jump between separate tools.
                      </p>
                    </div>
                  ) : symbolDetailTab === "source" ? (
                    <div className="space-y-4">
                      {selectedSymbolNode ? (
                        <div className="rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                          <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">Selected Symbol</p>
                          <h3 className="mt-2 font-mono text-base text-[var(--text-primary)]">{selectedSymbolNode.name}</h3>
                          <p className="mt-2 text-sm text-[var(--text-secondary)]">
                            <span className={kindBadgeClass(selectedSymbolNode.kind)}>{kindLabel(selectedSymbolNode.kind)}</span>
                            <span className="ml-2">{selectedSymbolNode.kind} · {selectedSymbolNode.filePath}:{selectedSymbolNode.startLine}</span>
                          </p>
                          {selectedSymbolNode.signature ? (
                            <pre className="mt-3 overflow-x-auto rounded-[var(--radius-sm)] bg-[var(--bg-base)] p-3 text-xs text-[var(--text-secondary)]">
                              {selectedSymbolNode.signature}
                            </pre>
                          ) : null}
                        </div>
                      ) : null}
                      <SourceViewerPane repositoryId={repoId} target={sourceTarget} />
                      <EnterpriseSourcePanel repositoryId={repoId} target={sourceTarget} />
                    </div>
                  ) : symbolDetailTab === "cliff-notes" ? (
                    <div className="space-y-4">
                      <div className="rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                        <p className="text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Cached analysis</p>
                        <p className="mt-2 text-sm text-[var(--text-secondary)]">
                          {selectedSymbolNode
                            ? "Generate or reuse a cached field guide for this symbol. Impact analysis is included once the symbol guide is up to date."
                            : "Generate or reuse a cached field guide for this file."}
                        </p>
                      </div>
                      {!currentScopedCliffNotes ? (
                        <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-5">
                          <p className="text-sm font-medium text-[var(--text-primary)]">No scoped Cliff Notes yet.</p>
                          <p className="mt-2 text-sm text-[var(--text-secondary)]">
                            Generate an indexed field guide for this {selectedSymbolNode ? "symbol" : "file"} to get purpose, local context, and safe-change guidance in one place.
                          </p>
                          <div className="mt-4">
                            <Button onClick={handleGenerateScopedCliffNotes} disabled={knowledgeLoading}>
                              {knowledgeLoading ? "Generating..." : "Generate Cliff Notes"}
                            </Button>
                          </div>
                        </div>
                      ) : (
                        <div className="rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-base)] p-5">
                          <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
                            <div className="flex flex-wrap items-center gap-2">
                              <span className={artifactStatusClass}>
                                {currentScopedCliffNotes.status === "READY" ? "Cached symbol analysis" : currentScopedCliffNotes.status}
                              </span>
                              {currentScopedCliffNotes.stale ? <span className={artifactStatusClass}>Stale</span> : null}
                              {scopedArtifactNeedsImpactRefresh ? <span className={artifactStatusClass}>Needs impact refresh</span> : null}
                            </div>
                            <div className="flex gap-2">
                              <Button variant="secondary" size="sm" onClick={handleGenerateScopedCliffNotes} disabled={knowledgeLoading}>
                                Regenerate
                              </Button>
                              <Button variant="secondary" size="sm" onClick={handleRefreshScopedArtifact} disabled={knowledgeLoading}>
                                Refresh
                              </Button>
                            </div>
                          </div>
                          {currentScopedCliffNotes.status === "GENERATING" || currentScopedCliffNotes.status === "PENDING" ? (
                            <div className="mb-4">
                              <progress
                                className="h-1.5 w-full overflow-hidden rounded-full [&::-webkit-progress-bar]:bg-[var(--bg-hover)] [&::-webkit-progress-value]:bg-[var(--accent-primary)] [&::-moz-progress-bar]:bg-[var(--accent-primary)]"
                                max={100}
                                value={Math.max(currentScopedCliffNotes.progress * 100, 5)}
                              />
                            </div>
                          ) : null}
                          {scopedArtifactNeedsImpactRefresh ? (
                            <div className="mb-4 rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                              <p className="text-sm font-medium text-[var(--text-primary)]">This cached symbol guide predates impact analysis.</p>
                              <p className="mt-2 text-sm text-[var(--text-secondary)]">
                                Refresh it to regenerate the indexed symbol guide with caller/callee impact and blast-radius notes.
                              </p>
                            </div>
                          ) : null}
                          {currentScopedCliffNotes.sections
                            .slice()
                            .sort((a, b) => a.orderIndex - b.orderIndex)
                            .map(renderScopedCliffNotesSection)}
                        </div>
                      )}
                    </div>
                  ) : (
                    <div className="space-y-4">
                      <div className="rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                        <p className="text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Follow-up on indexed context</p>
                        <p className="mt-2 text-sm text-[var(--text-secondary)]">
                          Ask follow-up questions about this cached symbol analysis. This uses indexed repository context, not current local editor state.
                        </p>
                      </div>
                      {!currentScopedCliffNotes ? (
                        <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-5">
                          <p className="text-sm font-medium text-[var(--text-primary)]">Generate Cliff Notes before asking follow-up questions.</p>
                          <p className="mt-2 text-sm text-[var(--text-secondary)]">
                            The chat tab is grounded in the cached symbol or file guide for this scope so the answers stay tied to indexed repository context.
                          </p>
                        </div>
                      ) : (
                        <div className="rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-base)] p-5">
                          <div className="space-y-3">
                            {symbolChatMessages.length === 0 ? (
                              <p className="text-sm text-[var(--text-secondary)]">
                                Start with a concrete question like “What would I verify before changing this?” or “Which callers are most exposed if I edit this symbol?”
                              </p>
                            ) : (
                              symbolChatMessages.map((message, index) => (
                                <div
                                  key={`${message.role}-${index}`}
                                  className={cn(
                                    "rounded-[var(--radius-sm)] px-4 py-3 text-sm leading-7",
                                    message.role === "user"
                                      ? "bg-[var(--bg-surface)] text-[var(--text-primary)]"
                                      : "border border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)]"
                                  )}
                                >
                                  <p className="mb-1 text-xs uppercase tracking-[0.12em] text-[var(--text-tertiary)]">
                                    {message.role === "user" ? "You" : "SourceBridge.ai"}
                                  </p>
                                  <p className="whitespace-pre-wrap">{message.text}</p>
                                </div>
                              ))
                            )}
                          </div>
                          <div className="mt-4 flex gap-2">
                            <input
                              type="text"
                              value={symbolChatQuestion}
                              onChange={(e) => setSymbolChatQuestion(e.target.value)}
                              onKeyDown={(e) => {
                                if (e.key === "Enter") {
                                  void handleScopedFollowUp();
                                }
                              }}
                              placeholder={selectedSymbolNode ? `Ask about ${selectedSymbolNode.name}...` : "Ask about this file..."}
                              className={`${inputClass} flex-1`}
                            />
                            <Button onClick={handleScopedFollowUp} disabled={knowledgeLoading || !symbolChatQuestion.trim()}>
                              {knowledgeLoading ? "Thinking..." : "Ask"}
                            </Button>
                          </div>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              </Panel>
            ) : (
              <>
                {selectedSymbolNode ? (
                  <Panel variant="accent">
                    <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                      Selected Symbol
                    </p>
                    <h3 className="mt-2 font-mono text-base text-[var(--text-primary)]">
                      {selectedSymbolNode.name}
                    </h3>
                    <p className="mt-2 text-sm text-[var(--text-secondary)]">
                      <span className={kindBadgeClass(selectedSymbolNode.kind)}>{kindLabel(selectedSymbolNode.kind)}</span>
                      <span className="ml-2">{selectedSymbolNode.kind} · {selectedSymbolNode.filePath}:{selectedSymbolNode.startLine}</span>
                    </p>
                    {selectedSymbolNode.signature ? (
                      <pre className="mt-3 overflow-x-auto rounded-[var(--radius-sm)] bg-[var(--bg-base)] p-3 text-xs text-[var(--text-secondary)]">
                        {selectedSymbolNode.signature}
                      </pre>
                    ) : null}
                  </Panel>
                ) : null}
                <SourceViewerPane repositoryId={repoId} target={sourceTarget} />
                <EnterpriseSourcePanel repositoryId={repoId} target={sourceTarget} />
              </>
            )}
          </div>
        </div>
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
        <Panel>
          <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">Repository Settings</h3>
          <div className="flex gap-3">
            <Button variant="secondary" onClick={() => reindex({ id: repoId })}>
              Reindex
            </Button>
          </div>
          <div className="mt-6 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
            <div className="flex items-start justify-between gap-4">
              <div>
                <h4 className="text-sm font-semibold text-[var(--text-primary)]">Knowledge Engine Default</h4>
                <p className="mt-1 text-sm text-[var(--text-secondary)]">
                  Sets the repository-level default generation engine. Request-time selections in the field guide still override this.
                </p>
              </div>
              <span className={artifactStatusClass}>{repoGenerationModeDefault === "CLASSIC" ? "Classic" : "Understanding First"}</span>
            </div>
            <div className="mt-4 flex flex-wrap gap-2">
              {[
                { key: "UNDERSTANDING_FIRST", label: "Understanding First" },
                { key: "CLASSIC", label: "Classic" },
              ].map((mode) => (
                <button
                  key={mode.key}
                  type="button"
                  onClick={() => void handleSaveRepositoryGenerationMode(mode.key as RepositoryGenerationMode)}
                  disabled={knowledgeLoading}
                  className={cn(
                    "rounded-full border px-3 py-1.5 text-xs font-medium transition-colors",
                    repoGenerationModeDefault === mode.key
                      ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                      : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
                  )}
                >
                  {mode.label}
                </button>
              ))}
            </div>
          </div>
          {/* Living Wiki panel — sits between Knowledge Engine Default and Danger Zone */}
          <div className="mt-6">
            <WikiSettingsPanel
              repoId={repoId}
              repoName={repo?.name ?? ""}
              initialSettings={repo?.livingWikiSettings ?? null}
            />
          </div>

          {/* Use with Claude Code card — capability-gated on agent_setup */}
          {features.agentSetup && repoId && (
            <div className="mt-6 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
              <h4 className="text-sm font-semibold text-[var(--text-primary)]">Use with Claude Code</h4>
              <p className="mt-1 text-sm text-[var(--text-secondary)]">
                Generate a <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">.claude/CLAUDE.md</code> skill card with per-subsystem sections so Claude Code understands how this codebase is structured before you start refactoring.
              </p>

              {serverCaps.loading ? (
                /* Loading skeleton — don't flash the wrong block */
                <div className="mt-3 space-y-2" aria-busy="true" aria-label="Detecting server configuration">
                  <div className="h-8 w-full animate-pulse rounded-[var(--control-radius)] bg-[var(--bg-subtle)]" />
                  <div className="h-4 w-2/3 animate-pulse rounded bg-[var(--bg-subtle)]" />
                </div>
              ) : !serverCaps.mcpEnabled ? (
                /* MCP disabled — admin must enable it */
                <div className="mt-3 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-subtle)] px-3 py-2.5">
                  <p className="text-sm text-[var(--text-secondary)]">
                    MCP isn&apos;t enabled on this SourceBridge instance. Ask your admin to set{" "}
                    <code className="rounded bg-[var(--bg-base)] px-1 py-0.5 text-xs">SOURCEBRIDGE_MCP_ENABLED=true</code>{" "}
                    and restart the server.
                  </p>
                </div>
              ) : serverCaps.authRequired ? (
                /* Cloud / auth-required — inline wizard (Slice 7) */
                <>
                  {serverCaps.error && (
                    <p className="mt-3 text-xs text-[var(--text-tertiary,var(--text-secondary))]">
                      Couldn&apos;t detect this server&apos;s auth configuration automatically — showing the hosted-instance flow. If you&apos;re on a local install, use{" "}
                      <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">sourcebridge setup claude --repo-id {repoId}</code> instead.
                    </p>
                  )}
                  {useExistingToken ? (
                    /* Fallback: slice-3 manual 3-step block */
                    <div className="mt-3 space-y-3">
                      {/* Step 1 */}
                      <div className="flex items-start gap-3">
                        <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-[var(--border-default)] text-xs font-medium text-[var(--text-tertiary)]">1</span>
                        <div className="min-w-0 flex-1">
                          <p className="text-sm text-[var(--text-secondary)]">
                            Mint an API token for Claude Code.
                          </p>
                          <a
                            href={`/settings/tokens?suggested_name=Claude%20Code`}
                            className="mt-1.5 inline-flex items-center rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-1.5 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                          >
                            Go to API tokens
                          </a>
                        </div>
                      </div>
                      {/* Step 2 */}
                      <div className="flex items-start gap-3">
                        <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-[var(--border-default)] text-xs font-medium text-[var(--text-tertiary)]">2</span>
                        <div className="min-w-0 flex-1">
                          <p className="text-sm text-[var(--text-secondary)]">Run this command, replacing <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">&lt;paste-here&gt;</code> with your token.</p>
                          <div className="mt-1.5 flex items-center gap-2">
                            <code className="min-w-0 flex-1 overflow-x-auto rounded bg-[var(--bg-subtle)] px-3 py-2 text-xs font-mono text-[var(--text-primary)]">
                              {`sourcebridge setup claude --server ${getServerOrigin()} --token <paste-here> --repo-id ${repoId}`}
                            </code>
                            <button
                              type="button"
                              onClick={() => {
                                void navigator.clipboard.writeText(
                                  `sourcebridge setup claude --server ${getServerOrigin()} --token <paste-here> --repo-id ${repoId}`
                                );
                                setCopiedSetupCmd(true);
                                setTimeout(() => setCopiedSetupCmd(false), 2000);
                              }}
                              className="shrink-0 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                              aria-label="Copy setup command"
                            >
                              {copiedSetupCmd ? "Copied!" : "Copy"}
                            </button>
                          </div>
                        </div>
                      </div>
                      {/* Step 3 */}
                      <div className="flex items-start gap-3">
                        <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-[var(--border-default)] text-xs font-medium text-[var(--text-tertiary)]">3</span>
                        <p className="text-sm text-[var(--text-secondary)]">
                          Add <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">export SOURCEBRIDGE_API_TOKEN=&lt;your-token&gt;</code> to your shell profile and restart Claude Code.
                        </p>
                      </div>
                      <button
                        type="button"
                        onClick={() => setUseExistingToken(false)}
                        className="text-xs text-[var(--text-tertiary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
                      >
                        Back to wizard
                      </button>
                    </div>
                  ) : (
                    <ClaudeCodeWizard
                      repoId={repoId}
                      onUseExisting={() => setUseExistingToken(true)}
                    />
                  )}
                </>
              ) : (
                /* Local / no-auth — single-step legacy flow */
                <div className="mt-3 flex items-center gap-2">
                  <code className="flex-1 rounded bg-[var(--bg-subtle)] px-3 py-2 text-xs font-mono text-[var(--text-primary)]">
                    {`sourcebridge setup claude --repo-id ${repoId}`}
                  </code>
                  <button
                    type="button"
                    onClick={() => {
                      void navigator.clipboard.writeText(`sourcebridge setup claude --repo-id ${repoId}`);
                      setCopiedSetupCmd(true);
                      setTimeout(() => setCopiedSetupCmd(false), 2000);
                    }}
                    className="shrink-0 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                    aria-label="Copy setup command"
                  >
                    {copiedSetupCmd ? "Copied!" : "Copy"}
                  </button>
                </div>
              )}

              <p className="mt-3 text-xs text-[var(--text-tertiary,var(--text-secondary))]">
                <a
                  href="https://docs.claude.com/en/docs/claude-code/memory"
                  target="_blank"
                  rel="noopener noreferrer"
                  className="underline hover:text-[var(--text-primary)]"
                >
                  Learn more about Claude Code memory
                  <span className="sr-only"> (opens in new tab)</span>
                </a>
              </p>
            </div>
          )}

          <div className="mt-8 rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] p-4">
            <h4 className="mb-2 font-semibold text-[var(--color-error,#ef4444)]">Danger Zone</h4>
            <p className="mb-3 text-sm text-[var(--text-secondary)]">
              Removing this repository will delete all indexed data, symbols, and requirement links.
            </p>
            <Button
              onClick={() => setRemoveRepoConfirmOpen(true)}
              className="bg-rose-600 text-white hover:bg-rose-700"
            >
              Remove Repository
            </Button>
            <ConfirmDialog
              open={removeRepoConfirmOpen}
              title="Remove repository"
              body={`Remove "${repo?.name}"? This cannot be undone.`}
              confirmLabel="Remove"
              cancelLabel="Cancel"
              destructive
              onConfirm={async () => {
                setRemoveRepoConfirmOpen(false);
                const res = await removeRepo({ id: repoId });
                if (res.error) return;
                router.push("/repositories");
              }}
              onCancel={() => setRemoveRepoConfirmOpen(false)}
            />
          </div>
        </Panel>
      )}
    </PageFrame>
  );
}
