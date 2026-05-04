"use client";

import { useState, useEffect, useMemo } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useQuery, useMutation, type UseQueryExecute } from "urql";
import {
  KNOWLEDGE_ARTIFACTS_QUERY,
  KNOWLEDGE_SCOPE_CHILDREN_QUERY,
  EXECUTION_ENTRY_POINTS_QUERY,
  EXECUTION_PATH_QUERY,
  GENERATE_CLIFF_NOTES_MUTATION,
  GENERATE_LEARNING_PATH_MUTATION,
  GENERATE_CODE_TOUR_MUTATION,
  GENERATE_WORKFLOW_STORY_MUTATION,
  EXPLAIN_SYSTEM_MUTATION,
  REFRESH_KNOWLEDGE_ARTIFACT_MUTATION,
} from "@/lib/graphql/queries";
import { useFeatures } from "@/lib/features";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import {
  JobProgress,
  formatElapsedMs as sharedFormatElapsedMs,
  formatHeartbeatAge as sharedFormatHeartbeatAge,
  formatQueueEta,
} from "@/components/llm/job-progress";
import type { LLMJobView } from "@/lib/llm/job-types";
import { SourceRefLink } from "@/components/source/SourceRefLink";
import { cn } from "@/lib/utils";
import { trackEvent } from "@/lib/telemetry";
import { disableJobAlerts, enableJobAlerts, jobAlertsEnabled, notifyJobEvent } from "@/lib/notifications";
import { authFetch } from "@/lib/auth-fetch";

// ---------------------------------------------------------------------------
// Types (mirrored from page.tsx — single source of truth when extraction is
// complete; for now kept here so the tab compiles standalone)
// ---------------------------------------------------------------------------

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

export interface KnowledgeArtifact {
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
  firstPassSections?: Array<{ title: string; summary: string }>;
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

interface ScopeChild {
  scopeType: string;
  label: string;
  scopePath: string;
  hasArtifact: boolean;
  summary: string | null;
}

interface ExecutionEntryPoint {
  kind: string;
  label: string;
  value: string;
  filePath: string | null;
  lineStart: number | null;
  lineEnd: number | null;
  symbolId: string | null;
  summary: string | null;
}

interface ExecutionPathStep {
  orderIndex: number;
  kind: string;
  label: string;
  explanation: string;
  confidence: string;
  observed: boolean;
  reason: string | null;
  filePath: string | null;
  lineStart: number | null;
  lineEnd: number | null;
  symbolId: string | null;
  symbolName: string | null;
}

interface ExecutionPathResult {
  entryKind: string;
  entryLabel: string;
  message: string | null;
  trustQualified: boolean;
  observedStepCount: number;
  inferredStepCount: number;
  steps: ExecutionPathStep[];
}

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

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface KnowledgeTabProps {
  repoId: string;
  repo: {
    id: string;
    name: string;
    generationModeDefault?: string | null;
  } | null | undefined;
  // Loading ops from parent (Slice 4 pattern)
  loadingOps: Set<string>;
  startLoading: (op: string) => void;
  finishLoading: (op: string) => void;
  isLoading: (op: string) => boolean;
  knowledgeLoading: boolean;
  // Understanding — owned by parent (also shown in page header)
  currentUnderstanding: RepositoryUnderstanding | null;
  understandingBuilding: boolean;
  understandingJob: RepoJobView | null;
  understandingDedupeNote: boolean;
  setUnderstandingDedupeNote: (v: boolean) => void;
  handleBuildRepositoryUnderstanding: () => Promise<void>;
  // Job activity — owned by parent (popover + notifications share it)
  repoJobs: RepoJobActivityResponse | null;
  repoJobsError: string | null;
  repoActiveJobs: RepoJobView[];
  repoRecentJobs: RepoJobView[];
  cancellingJobIds: Record<string, boolean>;
  setCancellingJobIds: React.Dispatch<React.SetStateAction<Record<string, boolean>>>;
  fetchRepoJobs: () => Promise<void>;
  reexecuteRepo: UseQueryExecute;
}

// ---------------------------------------------------------------------------
// Pure helpers (knowledge-only — no cross-tab consumers)
// ---------------------------------------------------------------------------

function knowledgeErrorHint(errorCode: string | null | undefined): string {
  switch (errorCode) {
    case "LLM_EMPTY":
      return "The model returned no content. This usually means the prompt was too large for the current model or the provider is unstable.";
    case "SNAPSHOT_TOO_LARGE":
      return "This scope likely exceeded the current model budget. Try a smaller scope or a strategy that chunks the corpus.";
    case "DEADLINE_EXCEEDED":
      return "The worker timed out before the generation completed. The provider may be overloaded.";
    case "WORKER_UNAVAILABLE":
      return "The worker could not be reached. Check the worker process or deployment health.";
    case "PROVIDER_COMPUTE":
      return "The model backend returned a compute failure. The queue now backs off automatically, but you may need to retry once the model server recovers.";
    case "DEGRADED_COMPUTE":
      return "The generation was blocked because the model backend degraded during summarization. The system refused to save low-quality fallback output as a completed artifact.";
    case "CANCELLED":
      return "The generation was cancelled before completion.";
    default:
      return "The artifact generation failed. Check the latest error details before retrying.";
  }
}

function renderKnowledgeFailure(artifact: KnowledgeArtifact) {
  return (
    <div className="mb-5 rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] p-3">
      <p className="text-sm font-medium text-[var(--text-primary)]">
        {artifact.errorCode || "GENERATION_FAILED"}
      </p>
      <p className="mt-1 text-sm text-[var(--text-secondary)]">
        {knowledgeErrorHint(artifact.errorCode)}
      </p>
      {artifact.errorMessage ? (
        <p className="mt-2 whitespace-pre-wrap text-xs text-[var(--text-tertiary)]">
          {artifact.errorMessage}
        </p>
      ) : null}
    </div>
  );
}

function knowledgeQueueLabel(artifact: KnowledgeArtifact): string {
  if (artifact.status === "PENDING") return "Queued";
  if (artifact.status === "GENERATING") return "Generating";
  if (artifact.status === "FAILED") return "Failed";
  if (artifact.status === "STALE") return "Stale";
  return artifact.status;
}

function repoJobReuseLabel(job: RepoJobView | null | undefined): string | null {
  const reused = job?.reused_summaries ?? 0;
  const cached = job?.cached_nodes_loaded ?? 0;
  const parts = [
    cached > 0 ? `${cached} cached loaded` : null,
    job?.resume_stage ? `resume ${job.resume_stage}` : null,
    job?.leaf_cache_hits ? `${job.leaf_cache_hits} leaf` : null,
    job?.file_cache_hits ? `${job.file_cache_hits} file` : null,
    job?.package_cache_hits ? `${job.package_cache_hits} package` : null,
    job?.root_cache_hits ? `${job.root_cache_hits} root` : null,
  ].filter(Boolean);
  if (reused <= 0 && parts.length === 0) return null;
  if (reused > 0) {
    return parts.length > 0 ? `${reused} reused · ${parts.join(" · ")}` : `${reused} reused`;
  }
  return parts.join(" · ");
}

function understandingStageLabel(understanding: RepositoryUnderstanding | null | undefined): string {
  switch (understanding?.stage) {
    case "BUILDING_TREE": return "Building tree";
    case "FIRST_PASS_READY": return "First pass ready";
    case "NEEDS_REFRESH": return "Refresh available";
    case "DEEPENING": return "Deepening";
    case "READY": return "Ready";
    case "FAILED": return "Failed";
    default: return "Not built";
  }
}

function understandingTreeLabel(understanding: RepositoryUnderstanding | null | undefined): string {
  switch (understanding?.treeStatus) {
    case "COMPLETE": return "Tree complete";
    case "PARTIAL": return "Tree partial";
    case "MISSING": return "Tree missing";
    default: return "Tree unknown";
  }
}

function understandingLeadSummary(understanding: RepositoryUnderstanding | null | undefined): string | null {
  const sections = understanding?.firstPassSections ?? [];
  if (!sections.length) return null;
  const preferredTitles = ["Architecture Overview", "System Purpose", "Core Components", "Core System Flows"];
  for (const title of preferredTitles) {
    const match = sections.find((s) => s.title === title && s.summary.trim());
    if (match) return match.summary.trim();
  }
  return sections.find((s) => s.summary.trim())?.summary.trim() || null;
}

function understandingHighlightSections(understanding: RepositoryUnderstanding | null | undefined): Array<{ title: string; summary: string }> {
  const sections = (understanding?.firstPassSections ?? []).filter(
    (s) => s.title.trim() && s.summary.trim(),
  );
  const preferredTitles = ["System Purpose", "Architecture Overview", "Core Components", "Core System Flows", "External Dependencies", "Domain Model"];
  const ordered: Array<{ title: string; summary: string }> = [];
  for (const title of preferredTitles) {
    const match = sections.find((s) => s.title === title);
    if (match && !ordered.some((s) => s.title === match.title)) ordered.push(match);
  }
  for (const s of sections) {
    if (!ordered.some((c) => c.title === s.title)) ordered.push(s);
  }
  return ordered;
}

function sectionRefinementLabel(section: KnowledgeSection): string | null {
  const status = (section.refinementStatus || "").trim().toLowerCase();
  if (status === "deep") return "Deepened";
  if (status === "light") return "Refined";
  if (status === "first_pass") return "First pass";
  return null;
}

function sectionRefinementClass(section: KnowledgeSection): string {
  const status = (section.refinementStatus || "").trim().toLowerCase();
  if (status === "deep") return "rounded-full border border-[var(--accent-primary)]/30 bg-[var(--accent-primary)]/10 px-2 py-0.5 text-xs font-medium text-[var(--accent-primary)]";
  if (status === "light") return "rounded-full border border-[var(--border-default)] bg-[var(--bg-surface)] px-2 py-0.5 text-xs font-medium text-[var(--text-primary)]";
  return "rounded-full border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-2 py-0.5 text-xs font-medium text-[var(--text-tertiary)]";
}

function artifactRefinementSummary(artifact: KnowledgeArtifact | null | undefined): string | null {
  if (!artifact?.sections?.length) return null;
  let refined = 0;
  let deepened = 0;
  for (const section of artifact.sections) {
    const status = (section.refinementStatus || "").trim().toLowerCase();
    if (status === "deep") { deepened++; continue; }
    if (status === "light") refined++;
  }
  const parts: string[] = [];
  if (refined > 0) parts.push(`${refined} refined`);
  if (deepened > 0) parts.push(`${deepened} deepened`);
  return parts.length > 0 ? parts.join(" · ") : null;
}

function artifactDeepeningSummary(artifact: KnowledgeArtifact | null | undefined): string | null {
  const units = (artifact?.refinementUnits ?? []).filter((u) => u.refinementType === "cliff_notes_deep");
  if (!units.length) return null;
  let queued = 0, running = 0, failed = 0, completed = 0;
  for (const u of units) {
    const s = u.status.trim().toLowerCase();
    if (s === "queued") queued++;
    else if (s === "running") running++;
    else if (s === "failed") failed++;
    else if (s === "completed") completed++;
  }
  const parts: string[] = [];
  if (running > 0) parts.push(`${running} deepening`);
  if (queued > 0) parts.push(`${queued} queued`);
  if (failed > 0) parts.push(`${failed} failed`);
  if (completed > 0) parts.push(`${completed} deepened`);
  return parts.length ? parts.join(" · ") : null;
}

function parseCliffNotesSectionMetadata(section: KnowledgeSection): CliffNotesSectionMetadata | null {
  if (!section.metadata?.trim()) return null;
  try {
    const parsed = JSON.parse(section.metadata) as CliffNotesSectionMetadata;
    if (!parsed || typeof parsed !== "object") return null;
    return parsed;
  } catch { return null; }
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
        <span key={part} className="rounded-full border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-2 py-0.5">{part}</span>
      ))}
    </div>
  );
}

function understandingProgressJobView(
  liveJob: RepoJobView | null | undefined,
  understanding: RepositoryUnderstanding | null | undefined,
): LLMJobView {
  if (liveJob && (liveJob.status === "pending" || liveJob.status === "generating")) return liveJob;
  const updated = understanding?.updatedAt ?? new Date().toISOString();
  const elapsed = updated ? Math.max(0, Date.now() - new Date(updated).getTime()) : 0;
  return {
    id: understanding?.id ?? "understanding",
    subsystem: "knowledge",
    job_type: "build_repository_understanding",
    status: "generating",
    progress: understanding?.progress ?? 0,
    progress_phase: understanding?.progressPhase ?? undefined,
    progress_message: understanding?.progressMessage ?? undefined,
    elapsed_ms: elapsed,
    updated_at: updated,
  };
}

function artifactAsJobView(artifact: KnowledgeArtifact): LLMJobView {
  const status: LLMJobView["status"] =
    artifact.status === "PENDING" ? "pending"
    : artifact.status === "GENERATING" ? "generating"
    : artifact.status === "FAILED" ? "failed"
    : "ready";
  const updated = artifact.updatedAt;
  const elapsed = updated ? Math.max(0, Date.now() - new Date(updated).getTime()) : 0;
  return {
    id: artifact.id,
    subsystem: "knowledge",
    job_type: artifact.type.toLowerCase(),
    status,
    progress: artifact.progress,
    progress_phase: artifact.progressPhase ?? undefined,
    progress_message: artifact.progressMessage ?? undefined,
    elapsed_ms: elapsed,
    updated_at: updated,
  };
}

function renderKnowledgeProgress(artifact: KnowledgeArtifact, waitingLabel: string, job?: RepoJobView | null) {
  const liveJob = job && (job.status === "pending" || job.status === "generating") ? job : null;
  const sourceJob = liveJob ?? artifactAsJobView(artifact);
  return (
    <div className="mb-5">
      <JobProgress job={sourceJob} variant="panel" pendingLabel={waitingLabel} />
    </div>
  );
}

const formatHeartbeatAge = sharedFormatHeartbeatAge;
const formatElapsedMs = sharedFormatElapsedMs;

function repoJobStatusLabel(job: RepoJobView | null | undefined): string | null {
  if (!job) return null;
  if (job.status === "pending" && job.queue_position) {
    const eta = formatQueueEta(job.estimated_wait_ms);
    return eta ? `Queued #${job.queue_position} · ~${eta}` : `Queued #${job.queue_position}`;
  }
  if (job.status === "generating") {
    const heartbeat = formatHeartbeatAge(job.updated_at);
    const elapsed = formatElapsedMs(job.elapsed_ms);
    if (job.progress_phase === "deepening") {
      if (heartbeat && elapsed) return `Improving in background · alive ${heartbeat} · elapsed ${elapsed}`;
      if (heartbeat) return `Improving in background · alive ${heartbeat}`;
      if (elapsed) return `Improving in background · elapsed ${elapsed}`;
      return "Improving in background";
    }
    if (job.progress >= 0.6 && job.progress < 0.96) {
      if (heartbeat && elapsed) return `Building summary tree · alive ${heartbeat} · elapsed ${elapsed}`;
      if (heartbeat) return `Building summary tree · alive ${heartbeat}`;
      if (elapsed) return `Building summary tree · elapsed ${elapsed}`;
      return "Building summary tree";
    }
    if (heartbeat && elapsed) return `Generating now · alive ${heartbeat} · elapsed ${elapsed}`;
    if (heartbeat) return `Generating now · alive ${heartbeat}`;
    if (elapsed) return `Generating now · elapsed ${elapsed}`;
    return "Generating now";
  }
  if (job.status === "failed") return job.error_title || "Last run failed";
  if (job.status === "cancelled") return "Cancelled";
  if (job.status === "ready") return "Completed";
  return null;
}

function artifactRetryLabel(
  artifact: { status: string } | null | undefined,
  job: RepoJobView | null | undefined,
  baseLabel: string,
): string {
  if (artifact?.status === "FAILED" || job?.status === "cancelled" || job?.status === "failed") return `Retry ${baseLabel}`;
  return `Refresh ${baseLabel}`;
}

function artifactHasActiveJob(
  artifact: KnowledgeArtifact | null | undefined,
  job: RepoJobView | null | undefined,
): boolean {
  if (!artifact) return false;
  if (job) return job.status === "pending" || job.status === "generating";
  return artifact.status === "PENDING" || artifact.status === "GENERATING";
}

// ---------------------------------------------------------------------------
// KnowledgeTab component
// ---------------------------------------------------------------------------

export function KnowledgeTab({
  repoId,
  repo,
  loadingOps: _loadingOps,
  startLoading,
  finishLoading,
  isLoading,
  knowledgeLoading,
  currentUnderstanding,
  understandingBuilding,
  understandingJob,
  understandingDedupeNote,
  setUnderstandingDedupeNote: _setUnderstandingDedupeNote,
  handleBuildRepositoryUnderstanding,
  repoJobs,
  repoJobsError,
  repoActiveJobs,
  repoRecentJobs,
  cancellingJobIds,
  setCancellingJobIds,
  fetchRepoJobs,
  reexecuteRepo: _reexecuteRepo,
}: KnowledgeTabProps) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const features = useFeatures();

  // URL-derived knowledge params
  const knowledgeScopeType = (searchParams.get("scope") || "repository").toUpperCase();
  const knowledgeScopePath = searchParams.get("path") || "";
  const knowledgeAudience = (searchParams.get("audience") || "developer").toUpperCase();
  const knowledgeDepth = (searchParams.get("depth") || "medium").toUpperCase();
  const knowledgeGenerationMode = (searchParams.get("mode") || "understanding_first").toUpperCase();

  // Queries
  const [knowledgeResult, reexecuteKnowledge] = useQuery({
    query: KNOWLEDGE_ARTIFACTS_QUERY,
    variables: {
      repositoryId: repoId,
      scopeType: knowledgeScopeType,
      scopePath: knowledgeScopeType === "REPOSITORY" ? undefined : knowledgeScopePath,
    },
  });
  const [scopeChildrenResult, reexecuteScopeChildren] = useQuery({
    query: KNOWLEDGE_SCOPE_CHILDREN_QUERY,
    variables: {
      repositoryId: repoId,
      scopeType: knowledgeScopeType,
      scopePath: knowledgeScopeType === "REPOSITORY" ? "" : knowledgeScopePath,
      audience: knowledgeAudience,
      depth: knowledgeDepth,
    },
  });

  const [executionRequested, setExecutionRequested] = useState(false);
  const [executionCompact, setExecutionCompact] = useState(false);
  const [selectedExecutionEntry, setSelectedExecutionEntry] = useState("");
  const [executionEntriesResult] = useQuery({
    query: EXECUTION_ENTRY_POINTS_QUERY,
    variables: { repositoryId: repoId },
  });
  const executionInput = useMemo(() => {
    if (knowledgeScopeType === "SYMBOL" && knowledgeScopePath) {
      return { repositoryId: repoId, entryKind: "SYMBOL", entryValue: knowledgeScopePath, maxDepth: 6 };
    }
    if (knowledgeScopeType === "FILE" && knowledgeScopePath) {
      return { repositoryId: repoId, entryKind: "FILE", entryValue: knowledgeScopePath, maxDepth: 6 };
    }
    if (selectedExecutionEntry) {
      return { repositoryId: repoId, entryKind: "ROUTE", entryValue: selectedExecutionEntry, maxDepth: 6 };
    }
    return null;
  }, [knowledgeScopeType, knowledgeScopePath, repoId, selectedExecutionEntry]);
  const [executionResult, reexecuteExecution] = useQuery({
    query: EXECUTION_PATH_QUERY,
    variables: executionInput ? { input: executionInput } : undefined,
    pause: !executionRequested || !executionInput,
  });

  // Mutations
  const [, generateCliffNotes] = useMutation(GENERATE_CLIFF_NOTES_MUTATION);
  const [, generateLearningPath] = useMutation(GENERATE_LEARNING_PATH_MUTATION);
  const [, generateCodeTour] = useMutation(GENERATE_CODE_TOUR_MUTATION);
  const [, generateWorkflowStory] = useMutation(GENERATE_WORKFLOW_STORY_MUTATION);
  const [, explainSystem] = useMutation(EXPLAIN_SYSTEM_MUTATION);
  const [, refreshArtifact] = useMutation(REFRESH_KNOWLEDGE_ARTIFACT_MUTATION);

  // Local state
  const [alertsEnabled, setAlertsEnabled] = useState(false);
  const [understandingCollapsed, setUnderstandingCollapsed] = useState(false);
  const [understandingShowAllSections, setUnderstandingShowAllSections] = useState(false);
  const [explainQuestion, setExplainQuestion] = useState("");
  const [explainResult, setExplainResult] = useState<{ explanation: string } | null>(null);
  const [tourStopIndex, setTourStopIndex] = useState(0);
  const [expandedSection, setExpandedSection] = useState<string | null>(null);
  const [expandedWorkflowSection, setExpandedWorkflowSection] = useState<string | null>(null);
  const [openCategory, setOpenCategory] = useState<"guide" | "ask" | "execution" | "workflow" | "explore" | null>("guide");

  // Derived data
  const knowledgeArtifacts: KnowledgeArtifact[] = knowledgeResult.data?.knowledgeArtifacts || [];
  const scopeChildren: ScopeChild[] = scopeChildrenResult.data?.knowledgeScopeChildren || [];
  const executionEntries: ExecutionEntryPoint[] = useMemo(
    () => executionEntriesResult.data?.executionEntryPoints || [],
    [executionEntriesResult.data?.executionEntryPoints],
  );
  const executionPath: ExecutionPathResult | null = executionResult.data?.executionPath || null;

  const matchesEngine = (a: KnowledgeArtifact): boolean =>
    (a.generationMode || "").toUpperCase() === knowledgeGenerationMode;

  const currentCliffNotes = knowledgeArtifacts.find(
    (a) => a.type === "CLIFF_NOTES" && a.audience === knowledgeAudience && a.depth === knowledgeDepth && matchesEngine(a),
  );
  const currentLearningPath = knowledgeArtifacts.find(
    (a) => a.type === "LEARNING_PATH" && a.audience === knowledgeAudience && a.depth === knowledgeDepth && matchesEngine(a),
  );
  const currentCodeTour = knowledgeArtifacts.find(
    (a) => a.type === "CODE_TOUR" && a.audience === knowledgeAudience && a.depth === knowledgeDepth && matchesEngine(a),
  );
  const currentWorkflowStory = knowledgeArtifacts.find(
    (a) => a.type === "WORKFLOW_STORY" && a.audience === knowledgeAudience && a.depth === knowledgeDepth && matchesEngine(a),
  );

  const artifactJobMap = useMemo(() => {
    const map = new Map<string, RepoJobView>();
    for (const job of [...repoActiveJobs, ...repoRecentJobs]) {
      if (job.artifact_id && !map.has(job.artifact_id)) map.set(job.artifact_id, job);
    }
    return map;
  }, [repoActiveJobs, repoRecentJobs]);

  const artifactHistoryMap = useMemo(() => {
    const map = new Map<string, RepoJobView[]>();
    for (const job of [...repoActiveJobs, ...repoRecentJobs]) {
      if (!job.artifact_id) continue;
      const current = map.get(job.artifact_id) || [];
      current.push(job);
      map.set(job.artifact_id, current);
    }
    for (const [artifactId, jobs] of map.entries()) {
      jobs.sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime());
      map.set(artifactId, jobs.slice(0, 3));
    }
    return map;
  }, [repoActiveJobs, repoRecentJobs]);

  const currentCliffNotesJob = currentCliffNotes ? artifactJobMap.get(currentCliffNotes.id) : null;
  const currentLearningPathJob = currentLearningPath ? artifactJobMap.get(currentLearningPath.id) : null;
  const currentCodeTourJob = currentCodeTour ? artifactJobMap.get(currentCodeTour.id) : null;
  const currentWorkflowStoryJob = currentWorkflowStory ? artifactJobMap.get(currentWorkflowStory.id) : null;

  const isCliffNotesGenerating = artifactHasActiveJob(currentCliffNotes, currentCliffNotesJob);
  const isLearningPathGenerating = artifactHasActiveJob(currentLearningPath, currentLearningPathJob);
  const isCodeTourGenerating = artifactHasActiveJob(currentCodeTour, currentCodeTourJob);
  const isWorkflowStoryGenerating = artifactHasActiveJob(currentWorkflowStory, currentWorkflowStoryJob);

  const batchSummary = useMemo(() => {
    const targets = [
      { artifact: currentCliffNotes, generating: isCliffNotesGenerating },
      { artifact: currentLearningPath, generating: isLearningPathGenerating },
      { artifact: currentCodeTour, generating: isCodeTourGenerating },
      { artifact: currentWorkflowStory, generating: isWorkflowStoryGenerating },
    ].filter((item) => item.artifact || item.generating);
    const total = targets.length;
    const completed = targets.filter((item) => item.artifact && (item.artifact.status === "READY" || item.artifact.status === "STALE")).length;
    const running = targets.filter((item) => item.generating).length;
    const failed = targets.filter((item) => item.artifact?.status === "FAILED").length;
    return { total, completed, running, failed };
  }, [currentCliffNotes, currentLearningPath, currentCodeTour, currentWorkflowStory, isCliffNotesGenerating, isLearningPathGenerating, isCodeTourGenerating, isWorkflowStoryGenerating]);

  const understandingSummary = understandingLeadSummary(currentUnderstanding);
  const understandingSections = understandingHighlightSections(currentUnderstanding);
  const understandingFeaturedSections = understandingSections.slice(0, 4);
  const understandingAdditionalSections = understandingSections.slice(4);

  function shouldCollapseRepositoryUnderstanding(u: RepositoryUnderstanding | null | undefined): boolean {
    if (!u) return false;
    if (u.errorMessage) return false;
    if (u.refreshAvailable) return false;
    return u.stage === "READY" && u.treeStatus === "COMPLETE";
  }
  const shouldAutoCollapseUnderstanding = shouldCollapseRepositoryUnderstanding(currentUnderstanding);

  // Effects
  useEffect(() => { setAlertsEnabled(jobAlertsEnabled()); }, []);

  useEffect(() => {
    setUnderstandingCollapsed(shouldAutoCollapseUnderstanding);
  }, [currentUnderstanding?.id, shouldAutoCollapseUnderstanding]);

  useEffect(() => {
    setUnderstandingShowAllSections(false);
  }, [currentUnderstanding?.id]);

  // Poll when artifacts are generating (ruby H-1)
  const hasGenerating = knowledgeResult.data?.knowledgeArtifacts?.some(
    (a: KnowledgeArtifact) => a.status === "GENERATING" || a.status === "PENDING",
  );
  useEffect(() => {
    if (!hasGenerating) return;
    const interval = setInterval(() => {
      reexecuteKnowledge({ requestPolicy: "network-only" });
      reexecuteScopeChildren({ requestPolicy: "network-only" });
    }, 2000);
    return () => clearInterval(interval);
  }, [hasGenerating, reexecuteKnowledge, reexecuteScopeChildren]);

  useEffect(() => {
    if (knowledgeScopeType !== "REPOSITORY") return;
    if (!selectedExecutionEntry && executionEntries.length > 0) {
      setSelectedExecutionEntry(executionEntries[0].value);
    }
  }, [knowledgeScopeType, executionEntries, selectedExecutionEntry]);

  useEffect(() => {
    setExecutionRequested(false);
  }, [knowledgeScopeType, knowledgeScopePath]);

  const codeTourId = currentCodeTour?.id;
  useEffect(() => { setTourStopIndex(0); }, [codeTourId]);

  // URL helpers
  function updateSearchParams(mutator: (params: URLSearchParams) => void) {
    const next = new URLSearchParams(searchParams.toString());
    mutator(next);
    router.replace(`${pathname}?${next.toString()}`, { scroll: false });
  }

  function setKnowledgeScope(nextScopeType: string, nextScopePath = "") {
    updateSearchParams((next) => {
      next.set("tab", "knowledge");
      next.set("scope", nextScopeType.toLowerCase());
      if (nextScopePath) next.set("path", nextScopePath);
      else next.delete("path");
    });
    setExpandedSection(null);
    setExpandedWorkflowSection(null);
    setExplainResult(null);
  }

  function setKnowledgeLens(nextAudience: string, nextDepth: string) {
    updateSearchParams((next) => {
      next.set("tab", "knowledge");
      next.set("audience", nextAudience.toLowerCase());
      next.set("depth", nextDepth.toLowerCase());
    });
  }

  function setKnowledgeGenerationMode(nextMode: string) {
    updateSearchParams((next) => {
      next.set("tab", "knowledge");
      next.set("mode", nextMode.toLowerCase());
    });
  }

  function scopeTitle() {
    if (knowledgeScopeType === "MODULE") return knowledgeScopePath || repo?.name || "Module";
    if (knowledgeScopeType === "FILE") return knowledgeScopePath.split("/").at(-1) || "File";
    if (knowledgeScopeType === "SYMBOL") return knowledgeScopePath.split("#").at(-1) || "Symbol";
    return repo?.name || "Repository";
  }

  function scopeSubtitle() {
    if (knowledgeScopeType === "REPOSITORY") return "Repository field guide";
    if (knowledgeScopeType === "MODULE") return knowledgeScopePath;
    if (knowledgeScopeType === "FILE") return knowledgeScopePath;
    if (knowledgeScopeType === "SYMBOL") return knowledgeScopePath;
    return "";
  }

  function formatGeneratedAt(value: string | null) {
    if (!value) return null;
    return new Date(value).toLocaleString();
  }

  function breadcrumbItems() {
    const items = [{ label: repo?.name || "Repository", scopeType: "repository", scopePath: "" }];
    if (knowledgeScopeType === "MODULE" && knowledgeScopePath) {
      const parts = knowledgeScopePath.split("/");
      let acc = "";
      for (const part of parts) {
        acc = acc ? `${acc}/${part}` : part;
        items.push({ label: `${part}/`, scopeType: "module", scopePath: acc });
      }
    }
    if (knowledgeScopeType === "FILE" && knowledgeScopePath) {
      const dir = knowledgeScopePath.includes("/") ? knowledgeScopePath.slice(0, knowledgeScopePath.lastIndexOf("/")) : "";
      if (dir) items.push({ label: `${dir}/`, scopeType: "module", scopePath: dir });
      items.push({ label: knowledgeScopePath.split("/").at(-1) || knowledgeScopePath, scopeType: "file", scopePath: knowledgeScopePath });
    }
    if (knowledgeScopeType === "SYMBOL" && knowledgeScopePath) {
      const [filePath, symbolName] = knowledgeScopePath.split("#");
      const dir = filePath.includes("/") ? filePath.slice(0, filePath.lastIndexOf("/")) : "";
      if (dir) items.push({ label: `${dir}/`, scopeType: "module", scopePath: dir });
      items.push({ label: filePath.split("/").at(-1) || filePath, scopeType: "file", scopePath: filePath });
      items.push({ label: symbolName || "Symbol", scopeType: "symbol", scopePath: knowledgeScopePath });
    }
    return items;
  }

  function workflowStoryAnchorLabel() {
    if (knowledgeScopeType === "REPOSITORY") {
      const entry = executionEntries.find((e) => e.value === selectedExecutionEntry);
      return entry?.label || `${repo?.name || "Repository"} workspace journey`;
    }
    if (knowledgeScopeType === "FILE") return `How someone uses ${scopeTitle()}`;
    if (knowledgeScopeType === "SYMBOL") return `How ${scopeTitle()} participates in a workflow`;
    return `How someone uses ${scopeTitle()}`;
  }

  // Handlers
  async function handleGenerateCliffNotesFor(scopeType = knowledgeScopeType, scopePath = knowledgeScopePath) {
    trackEvent({
      event: "field_guide_generated",
      repositoryId: repoId,
      metadata: { scopeType, scopePath: scopePath || null, audience: knowledgeAudience, depth: knowledgeDepth },
    });
    startLoading("cliff-notes-generate");
    try {
      await generateCliffNotes({
        input: {
          repositoryId: repoId,
          audience: knowledgeAudience,
          depth: knowledgeDepth,
          generationMode: knowledgeGenerationMode,
          scopeType,
          scopePath: scopeType === "REPOSITORY" ? undefined : scopePath,
        },
      });
      reexecuteKnowledge({ requestPolicy: "network-only" });
      reexecuteScopeChildren({ requestPolicy: "network-only" });
    } finally {
      finishLoading("cliff-notes-generate");
    }
  }

  async function handleGenerateCliffNotes() {
    await handleGenerateCliffNotesFor();
  }

  async function handleGenerateLearningPath() {
    startLoading("learning-path-generate");
    try {
      await generateLearningPath({ input: { repositoryId: repoId, audience: knowledgeAudience, depth: knowledgeDepth, generationMode: knowledgeGenerationMode } });
      reexecuteKnowledge({ requestPolicy: "network-only" });
    } finally {
      finishLoading("learning-path-generate");
    }
  }

  async function handleGenerateCodeTour() {
    startLoading("code-tour-generate");
    try {
      await generateCodeTour({ input: { repositoryId: repoId, audience: knowledgeAudience, depth: knowledgeDepth, generationMode: knowledgeGenerationMode } });
      reexecuteKnowledge({ requestPolicy: "network-only" });
    } finally {
      finishLoading("code-tour-generate");
    }
  }

  async function handleGenerateWorkflowStory() {
    trackEvent({
      event: "workflow_story_generated",
      repositoryId: repoId,
      metadata: { scopeType: knowledgeScopeType, scopePath: knowledgeScopePath || null },
    });
    startLoading("workflow-story-generate");
    try {
      await generateWorkflowStory({
        input: {
          repositoryId: repoId,
          audience: knowledgeAudience,
          depth: knowledgeDepth,
          generationMode: knowledgeGenerationMode,
          scopeType: knowledgeScopeType,
          scopePath: knowledgeScopeType === "REPOSITORY" ? undefined : knowledgeScopePath,
          anchorLabel: workflowStoryAnchorLabel(),
          executionPathJson: executionPath?.trustQualified ? JSON.stringify(executionPath.steps) : undefined,
        },
      });
      reexecuteKnowledge({ requestPolicy: "network-only" });
    } finally {
      finishLoading("workflow-story-generate");
    }
  }

  async function handleRefreshArtifact(artifactId: string) {
    startLoading(`artifact-refresh:${artifactId}`);
    try {
      await refreshArtifact({ id: artifactId });
      reexecuteKnowledge({ requestPolicy: "network-only" });
      reexecuteScopeChildren({ requestPolicy: "network-only" });
    } finally {
      finishLoading(`artifact-refresh:${artifactId}`);
    }
  }

  async function handleExplainSystem() {
    if (!explainQuestion.trim() || isLoading("explain-system")) return;
    trackEvent({
      event: "explain_scope_used",
      repositoryId: repoId,
      metadata: { scopeType: knowledgeScopeType, scopePath: knowledgeScopePath || null },
    });
    startLoading("explain-system");
    setExplainResult(null);
    try {
      const res = await explainSystem({
        input: {
          repositoryId: repoId,
          audience: knowledgeAudience,
          depth: knowledgeDepth,
          generationMode: knowledgeGenerationMode,
          question: explainQuestion,
          scopeType: knowledgeScopeType,
          scopePath: knowledgeScopeType === "REPOSITORY" ? undefined : knowledgeScopePath,
        },
      });
      if (res.data?.explainSystem) setExplainResult(res.data.explainSystem);
    } finally {
      finishLoading("explain-system");
    }
  }

  async function handleTraceExecution() {
    if (!executionInput) return;
    trackEvent({
      event: "execution_path_requested",
      repositoryId: repoId,
      metadata: { entryKind: executionInput.entryKind, entryValue: executionInput.entryValue },
    });
    setExecutionRequested(true);
    await reexecuteExecution({ requestPolicy: "network-only" });
  }

  async function handleCancelRepoJob(jobId: string) {
    if (cancellingJobIds[jobId]) return;
    setCancellingJobIds((current) => ({ ...current, [jobId]: true }));
    try {
      const res = await authFetch(`/api/v1/admin/llm/jobs/${encodeURIComponent(jobId)}/cancel`, { method: "POST" });
      if (!res.ok) throw new Error(`cancel returned ${res.status}`);
      await fetchRepoJobs();
      reexecuteKnowledge({ requestPolicy: "network-only" });
    } finally {
      setCancellingJobIds((current) => {
        const next = { ...current };
        delete next[jobId];
        return next;
      });
    }
  }

  async function handleToggleAlerts() {
    if (alertsEnabled) {
      disableJobAlerts();
      setAlertsEnabled(false);
      return;
    }
    const permission = await enableJobAlerts();
    if (permission === "granted") {
      setAlertsEnabled(true);
      notifyJobEvent("Desktop alerts enabled", `Repository generation alerts are now enabled for ${repo?.name || "this repository"}.`);
      return;
    }
    notifyJobEvent("Desktop alerts unavailable", permission === "unsupported" ? "This browser does not support desktop notifications." : "Notification permission was not granted.");
  }

  // CSS class constants (local to this tab)
  const inputClass = "h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]";
  const artifactStatusClass = "rounded-full border border-[var(--border-default)] bg-[var(--bg-hover)] px-2.5 py-1 text-xs text-[var(--text-secondary)]";
  const confidenceClass = (confidence: string) =>
    cn(
      "rounded-full px-1.5 py-0.5 text-xs text-white",
      confidence === "HIGH" ? "bg-[var(--confidence-high,#22c55e)]"
        : confidence === "MEDIUM" ? "bg-[var(--confidence-medium,#f59e0b)]"
        : "bg-[var(--confidence-low,#ef4444)]",
    );

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className="space-y-6">
      {knowledgeArtifacts.length === 0 && !knowledgeResult.fetching && (
        <Panel variant="accent" className="overflow-hidden">
          <div className="flex flex-col items-center justify-center px-8 py-16 text-center">
            <div className="mb-5 flex h-14 w-14 items-center justify-center rounded-2xl bg-[var(--accent-primary)]/10 text-[var(--accent-primary)]">
              <svg xmlns="http://www.w3.org/2000/svg" width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/></svg>
            </div>
            <h3 className="text-base font-semibold text-[var(--text-primary)]">Get to know this repository</h3>
            <p className="mt-2 max-w-sm text-sm text-[var(--text-secondary)]">
              Generate a Field Guide to get oriented fast — purpose, architecture, key patterns, and entry points.
            </p>
            <div className="mt-6">
              <Button onClick={handleGenerateCliffNotes} disabled={knowledgeLoading || isCliffNotesGenerating || !features.cliffNotes}>
                {knowledgeLoading || isCliffNotesGenerating ? "Generating..." : "Generate Field Guide"}
              </Button>
            </div>
            {!features.cliffNotes ? (
              <p className="mt-3 text-xs text-[var(--text-tertiary)]">
                Field-guide generation is not enabled on this server. Contact your administrator to configure an LLM provider.
              </p>
            ) : null}
          </div>
        </Panel>
      )}
      <Panel variant="accent" className="overflow-hidden">
        <div className="border-b border-[var(--border-subtle)] px-6 py-5">
          <div className="flex flex-wrap items-center gap-2 text-xs uppercase tracking-[0.18em] text-[var(--text-tertiary)]">
            {breadcrumbItems().map((item, idx) => (
              <button
                key={`${item.scopeType}-${item.scopePath || "root"}`}
                type="button"
                onClick={() => setKnowledgeScope(item.scopeType, item.scopePath)}
                className={cn("transition-colors hover:text-[var(--text-primary)]", idx === breadcrumbItems().length - 1 && "text-[var(--text-primary)]")}
              >
                {item.label}
                {idx < breadcrumbItems().length - 1 ? <span className="mx-2 text-[var(--text-tertiary)]">/</span> : null}
              </button>
            ))}
          </div>
          <div className="mt-3 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <div className="min-w-0">
              <p className="truncate text-sm font-semibold text-[var(--text-primary)]">{scopeTitle()}</p>
              {scopeSubtitle() ? (
                <p className="mt-0.5 truncate text-xs text-[var(--text-tertiary)]">{scopeSubtitle()}</p>
              ) : null}
            </div>
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
              <div className="flex items-center gap-2">
                <span className="text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Audience</span>
                <div className="flex flex-wrap gap-1.5">
                  {["DEVELOPER", "BEGINNER"].map((aud) => (
                    <button
                      key={aud}
                      type="button"
                      onClick={() => setKnowledgeLens(aud, knowledgeDepth)}
                      className={cn(
                        "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                        knowledgeAudience === aud
                          ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                          : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]",
                      )}
                    >
                      {aud === "DEVELOPER" ? "Developer" : "Beginner"}
                    </button>
                  ))}
                </div>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Depth</span>
                <div className="flex flex-wrap gap-1.5">
                  {["SUMMARY", "MEDIUM", "DEEP"].map((dep) => (
                    <button
                      key={dep}
                      type="button"
                      onClick={() => setKnowledgeLens(knowledgeAudience, dep)}
                      className={cn(
                        "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                        knowledgeDepth === dep
                          ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                          : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]",
                      )}
                    >
                      {dep[0]}{dep.slice(1).toLowerCase()}
                    </button>
                  ))}
                </div>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Engine</span>
                <div className="flex flex-wrap gap-1.5">
                  {[
                    { key: "UNDERSTANDING_FIRST", label: "Understanding First" },
                    { key: "CLASSIC", label: "Classic" },
                  ].map((mode) => (
                    <button
                      key={mode.key}
                      type="button"
                      onClick={() => setKnowledgeGenerationMode(mode.key)}
                      className={cn(
                        "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                        knowledgeGenerationMode === mode.key
                          ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                          : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]",
                      )}
                    >
                      {mode.label}
                    </button>
                  ))}
                </div>
              </div>
            </div>
          </div>
        </div>

        <div className="border-t border-[var(--border-subtle)] px-6 py-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--text-tertiary)]">
              <span className={artifactStatusClass}>
                Queue {repoJobs?.stats.queue_depth ?? 0} / {repoJobs?.stats.max_concurrency ?? 0} slots
              </span>
              <span className={artifactStatusClass}>
                Batch {batchSummary.completed}/{batchSummary.total || 0} complete
              </span>
              {batchSummary.running > 0 ? <span className={artifactStatusClass}>{batchSummary.running} running</span> : null}
              {batchSummary.failed > 0 ? <span className={artifactStatusClass}>{batchSummary.failed} failed</span> : null}
              {repoJobsError ? <span className="text-[var(--color-error,#ef4444)]">{repoJobsError}</span> : null}
            </div>
            <Button variant="secondary" size="sm" onClick={() => void handleToggleAlerts()}>
              {alertsEnabled ? "Desktop alerts on" : "Enable desktop alerts"}
            </Button>
          </div>
        </div>

        <div className="grid gap-6 px-6 py-6 xl:grid-cols-[minmax(0,1fr)_320px]">
          <div className="space-y-2">
            {/* Understanding panel */}
            <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-5">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <p className="text-sm font-semibold text-[var(--text-primary)]">Repository Understanding</p>
                  <p className="mt-1 text-sm text-[var(--text-secondary)]">
                    {currentUnderstanding
                      ? "Shared repository understanding powers cliff notes reuse and refresh decisions."
                      : "No shared repository understanding has been persisted yet."}
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--text-tertiary)]">
                  <span className={artifactStatusClass}>{understandingStageLabel(currentUnderstanding)}</span>
                  {currentUnderstanding ? <span className={artifactStatusClass}>{understandingTreeLabel(currentUnderstanding)}</span> : null}
                  {currentUnderstanding?.refreshAvailable ? <span className={artifactStatusClass}>Refresh available</span> : null}
                </div>
              </div>
              <div className="mt-4 flex flex-wrap items-center gap-2">
                <Button variant="secondary" size="sm" onClick={handleBuildRepositoryUnderstanding} disabled={knowledgeLoading || understandingBuilding}>
                  {knowledgeLoading
                    ? "Starting..."
                    : understandingBuilding
                      ? "Building understanding..."
                      : currentUnderstanding
                        ? "Refresh understanding"
                        : "Build understanding"}
                </Button>
                {currentUnderstanding ? (
                  <Button variant="ghost" size="sm" onClick={() => setUnderstandingCollapsed((c) => !c)}>
                    {understandingCollapsed ? "Show details" : "Hide details"}
                  </Button>
                ) : null}
                {currentUnderstanding?.updatedAt && !understandingBuilding ? (
                  <span className="text-xs text-[var(--text-tertiary)]">
                    Updated {formatGeneratedAt(currentUnderstanding.updatedAt)}
                  </span>
                ) : null}
              </div>
              {understandingBuilding ? (
                <div className="mt-3">
                  <JobProgress
                    job={understandingProgressJobView(understandingJob, currentUnderstanding)}
                    variant="panel"
                    pendingLabel="Queued"
                  />
                </div>
              ) : null}
              {understandingDedupeNote ? (
                <p className="mt-2 text-xs text-[var(--text-tertiary)]">
                  Already building — your click was joined to the existing run.
                </p>
              ) : null}
              {currentUnderstanding && understandingCollapsed ? (
                <div className="mt-4 space-y-3">
                  {understandingSummary ? (
                    <div className="rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-3 py-3 text-sm text-[var(--text-secondary)]">
                      <span className="font-medium text-[var(--text-primary)]">What the system learned:</span>{" "}
                      {understandingSummary}
                    </div>
                  ) : null}
                  <div className="rounded-[var(--radius-sm)] bg-[var(--bg-surface)] px-3 py-2 text-sm text-[var(--text-secondary)]">
                    <span className="text-[var(--text-primary)]">
                      {currentUnderstanding.cachedNodes}/{currentUnderstanding.totalNodes || currentUnderstanding.cachedNodes} nodes
                    </span>
                    {" · "}{currentUnderstanding.strategy || "hierarchical"}
                    {" · "}{currentUnderstanding.modelUsed || "Unknown model"}
                    {understandingSections.length ? <>{" · "}{understandingSections.length} first-pass sections</> : null}
                  </div>
                </div>
              ) : null}
              {currentUnderstanding && !understandingCollapsed ? (
                <>
                  <div className="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
                    <div className="rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                      <p className="text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Nodes</p>
                      <p className="mt-1 text-sm font-medium text-[var(--text-primary)]">
                        {currentUnderstanding.cachedNodes}/{currentUnderstanding.totalNodes || currentUnderstanding.cachedNodes}
                      </p>
                    </div>
                    <div className="rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                      <p className="text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Strategy</p>
                      <p className="mt-1 text-sm font-medium text-[var(--text-primary)]">{currentUnderstanding.strategy || "hierarchical"}</p>
                    </div>
                    <div className="rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                      <p className="text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Model</p>
                      <p className="mt-1 truncate text-sm font-medium text-[var(--text-primary)]">{currentUnderstanding.modelUsed || "Unknown"}</p>
                    </div>
                    <div className="rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                      <p className="text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Revision</p>
                      <p className="mt-1 text-sm font-medium text-[var(--text-primary)]">
                        {currentUnderstanding.revisionFp ? currentUnderstanding.revisionFp.slice(0, 12) : "Unknown"}
                      </p>
                    </div>
                  </div>
                  {understandingFeaturedSections.length ? (
                    <div className="mt-4 space-y-3">
                      <div className="flex items-center justify-between gap-3">
                        <p className="text-xs font-semibold uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Understanding Highlights</p>
                        <span className="text-xs text-[var(--text-tertiary)]">{understandingSections.length} sections</span>
                      </div>
                      <div className="grid gap-3 lg:grid-cols-2">
                        {understandingFeaturedSections.map((section) => (
                          <div key={section.title} className="rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] p-3">
                            <p className="text-sm font-medium text-[var(--text-primary)]">{section.title}</p>
                            <p className="mt-1 text-sm text-[var(--text-secondary)]">{section.summary}</p>
                          </div>
                        ))}
                      </div>
                      {understandingAdditionalSections.length ? (
                        <div className="space-y-3">
                          <Button variant="ghost" size="sm" onClick={() => setUnderstandingShowAllSections((c) => !c)}>
                            {understandingShowAllSections
                              ? `Hide ${understandingAdditionalSections.length} additional sections`
                              : `Show ${understandingAdditionalSections.length} additional sections`}
                          </Button>
                          {understandingShowAllSections ? (
                            <div className="grid gap-3 lg:grid-cols-2">
                              {understandingAdditionalSections.map((section) => (
                                <div key={section.title} className="rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] p-3">
                                  <p className="text-sm font-medium text-[var(--text-primary)]">{section.title}</p>
                                  <p className="mt-1 text-sm text-[var(--text-secondary)]">{section.summary}</p>
                                </div>
                              ))}
                            </div>
                          ) : null}
                        </div>
                      ) : null}
                    </div>
                  ) : null}
                </>
              ) : null}
              {currentUnderstanding?.errorMessage ? (
                <p className="mt-3 text-xs text-[var(--color-error,#ef4444)]">{currentUnderstanding.errorMessage}</p>
              ) : null}
            </div>

            {/* Category 1: Field Guide / Cliff Notes */}
            <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] overflow-hidden transition-all">
              <button
                type="button"
                onClick={() => setOpenCategory(openCategory === "guide" ? null : "guide")}
                className="flex w-full items-center gap-4 px-5 py-4 text-left transition-colors hover:bg-[var(--bg-hover)]"
              >
                <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-[var(--accent-primary)]/10 text-[var(--accent-primary)]">
                  <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/></svg>
                </span>
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-semibold text-[var(--text-primary)]">Cliff Notes</p>
                  <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">
                    {currentCliffNotes
                      ? `${currentCliffNotes.sections.length} section${currentCliffNotes.sections.length !== 1 ? "s" : ""}${currentCliffNotes.generatedAt ? ` · Generated ${formatGeneratedAt(currentCliffNotes.generatedAt)}` : ""}`
                      : "Not generated yet"}
                  </p>
                </div>
                <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={cn("shrink-0 text-[var(--text-tertiary)] transition-transform duration-200", openCategory === "guide" && "rotate-180")}><path d="m6 9 6 6 6-6"/></svg>
              </button>
              {openCategory === "guide" && (
                <div className="border-t border-[var(--border-subtle)] bg-[var(--bg-surface)] px-5 py-5">
                  {!currentCliffNotes && !knowledgeResult.fetching && (
                    <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-5">
                      <p className="text-sm font-medium text-[var(--text-primary)]">No field guide for this view yet.</p>
                      <p className="mt-2 text-sm text-[var(--text-secondary)]">
                        Generate a grounded guide for {scopeTitle()} to get oriented fast. Requirements are optional and can be layered in later.
                      </p>
                      <div className="mt-4">
                        <Button onClick={handleGenerateCliffNotes} disabled={knowledgeLoading || isCliffNotesGenerating || !features.cliffNotes}>
                          {knowledgeLoading || isCliffNotesGenerating ? "Generating..." : "Generate field guide"}
                        </Button>
                      </div>
                      {!features.cliffNotes ? (
                        <p className="mt-3 text-xs text-[var(--text-tertiary)]">
                          Field-guide generation is not enabled on this server. This view stays visible so you always know where guided understanding will appear.
                        </p>
                      ) : null}
                    </div>
                  )}
                  {currentCliffNotes && (
                    <>
                      <div className="mb-4 flex items-center justify-between">
                        <div>
                          <div className="flex items-center gap-2">
                            {isCliffNotesGenerating ? <span className={artifactStatusClass}>{knowledgeQueueLabel(currentCliffNotes)}</span> : null}
                            {currentCliffNotesJob?.status === "generating" && currentCliffNotesJob.progress_phase === "deepening" ? (
                              <span className={artifactStatusClass}>Improving in background</span>
                            ) : null}
                            {currentCliffNotes.stale ? <span className={artifactStatusClass}>Stale</span> : null}
                            {currentCliffNotes.refreshAvailable ? <span className={artifactStatusClass}>Refresh available</span> : null}
                            {currentCliffNotes.status === "FAILED" ? <span className={artifactStatusClass}>Refresh failed</span> : null}
                          </div>
                          <p className="mt-2 text-xs text-[var(--text-tertiary)]">
                            {formatGeneratedAt(currentCliffNotes.generatedAt)
                              ? `Generated ${formatGeneratedAt(currentCliffNotes.generatedAt)}`
                              : "Generated after the latest successful field-guide run."}
                            {currentCliffNotes.sourceRevision?.commitSha
                              ? ` · revision ${currentCliffNotes.sourceRevision.commitSha.slice(0, 7)}`
                              : ""}
                          </p>
                          {repoJobStatusLabel(currentCliffNotesJob) ? (
                            <p className="mt-2 text-xs text-[var(--text-tertiary)]">{repoJobStatusLabel(currentCliffNotesJob)}</p>
                          ) : null}
                          {repoJobReuseLabel(currentCliffNotesJob) ? (
                            <p className="mt-1 text-xs text-[var(--text-tertiary)]">{repoJobReuseLabel(currentCliffNotesJob)}</p>
                          ) : null}
                          {currentCliffNotes.understandingRevisionFp ? (
                            <p className="mt-1 text-xs text-[var(--text-tertiary)]">
                              Understanding revision {currentCliffNotes.understandingRevisionFp.slice(0, 12)}
                            </p>
                          ) : null}
                          {artifactRefinementSummary(currentCliffNotes) ? (
                            <p className="mt-1 text-xs text-[var(--text-tertiary)]">{artifactRefinementSummary(currentCliffNotes)}</p>
                          ) : null}
                          {artifactDeepeningSummary(currentCliffNotes) ? (
                            <p className="mt-1 text-xs text-[var(--text-tertiary)]">{artifactDeepeningSummary(currentCliffNotes)}</p>
                          ) : null}
                        </div>
                        <div className="flex gap-2">
                          <Button variant="secondary" size="sm" onClick={handleGenerateCliffNotes} disabled={knowledgeLoading || isCliffNotesGenerating}>
                            {currentCliffNotes?.status === "PENDING" ? "Queued..." : isCliffNotesGenerating ? "Generating..." : "Generate this lens"}
                          </Button>
                          <Button variant="secondary" size="sm" onClick={() => handleRefreshArtifact(currentCliffNotes.id)} disabled={knowledgeLoading || isCliffNotesGenerating}>
                            {artifactRetryLabel(currentCliffNotes, currentCliffNotesJob, "field guide")}
                          </Button>
                          {currentCliffNotesJob && (currentCliffNotesJob.status === "pending" || currentCliffNotesJob.status === "generating") ? (
                            <Button variant="secondary" size="sm" onClick={() => void handleCancelRepoJob(currentCliffNotesJob.id)} disabled={knowledgeLoading || cancellingJobIds[currentCliffNotesJob.id]}>
                              {cancellingJobIds[currentCliffNotesJob.id] ? "Cancelling..." : "Cancel"}
                            </Button>
                          ) : null}
                        </div>
                      </div>
                      {isCliffNotesGenerating ? renderKnowledgeProgress(currentCliffNotes, "Queued for generation", currentCliffNotesJob) : null}
                      {currentCliffNotes.status === "FAILED" ? renderKnowledgeFailure(currentCliffNotes) : null}
                      {(artifactHistoryMap.get(currentCliffNotes.id)?.length ?? 0) > 0 ? (
                        <div className="mb-4 rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                          <p className="mb-2 text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Recent runs</p>
                          <div className="space-y-1 text-xs text-[var(--text-secondary)]">
                            {(artifactHistoryMap.get(currentCliffNotes.id) || []).map((job) => (
                              <div key={job.id} className="flex items-center justify-between gap-3">
                                <span className="truncate">
                                  {job.status === "failed" ? (job.error_title || "Failed") : job.status === "pending" ? "Queued" : job.status === "generating" ? "Generating" : job.status === "cancelled" ? "Cancelled" : "Completed"}
                                  {job.progress_message ? ` · ${job.progress_message}` : ""}
                                  {repoJobReuseLabel(job) ? ` · ${repoJobReuseLabel(job)}` : ""}
                                  {job.attached_requests && job.attached_requests > 1 ? ` · shared by ${job.attached_requests}` : ""}
                                </span>
                                <span>{new Date(job.updated_at).toLocaleTimeString()}</span>
                              </div>
                            ))}
                          </div>
                        </div>
                      ) : null}
                      {currentCliffNotes.refinementUnits?.some((u) => u.refinementType === "cliff_notes_deep" && u.status.toLowerCase() === "failed") ? (
                        <div className="mb-4 rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                          <p className="text-sm font-medium text-[var(--text-primary)]">Some background deepening attempts failed.</p>
                          <div className="mt-2 space-y-1 text-xs text-[var(--text-secondary)]">
                            {currentCliffNotes.refinementUnits
                              .filter((u) => u.refinementType === "cliff_notes_deep" && u.status.toLowerCase() === "failed")
                              .map((u) => (
                                <p key={u.id}>
                                  {u.sectionTitle}
                                  {u.attemptCount > 0 ? ` · attempt ${u.attemptCount}` : ""}
                                  {u.lastError ? ` · ${u.lastError}` : ""}
                                </p>
                              ))}
                          </div>
                        </div>
                      ) : null}
                      {currentCliffNotes.sections
                        .slice()
                        .sort((a, b) => a.orderIndex - b.orderIndex)
                        .map((section) => (
                          <div key={section.id} className="border-t border-[var(--border-subtle)] py-4 first:border-t-0 first:pt-0">
                            <div
                              onClick={() => setExpandedSection(expandedSection === section.id ? null : section.id)}
                              className="flex cursor-pointer items-start justify-between gap-4"
                            >
                              <div>
                                <h3 className="text-base font-semibold text-[var(--text-primary)]">{section.title}</h3>
                                {section.summary && expandedSection !== section.id ? (
                                  <p className="mt-1 text-sm text-[var(--text-secondary)]">{section.summary}</p>
                                ) : null}
                              </div>
                              <div className="flex items-center gap-2">
                                {sectionRefinementLabel(section) ? (
                                  <span className={sectionRefinementClass(section)}>{sectionRefinementLabel(section)}</span>
                                ) : null}
                                <span className={confidenceClass(section.confidence)}>{section.confidence}</span>
                                {section.inferred ? <span className="text-xs text-[var(--text-tertiary)]">inferred</span> : null}
                              </div>
                            </div>
                            {expandedSection === section.id && (
                              <div className="mt-3">
                                <p className="whitespace-pre-wrap text-sm leading-7 text-[var(--text-secondary)]">{section.content}</p>
                                {renderCliffNotesSectionProvenance(section)}
                                {section.evidence.length > 0 && (
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
                                              {ev.filePath}{ev.lineStart ? `:${ev.lineStart}` : ""}{ev.lineEnd && ev.lineEnd !== ev.lineStart ? `-${ev.lineEnd}` : ""}
                                            </SourceRefLink>
                                          ) : null}
                                          {ev.rationale ? <span className="ml-2">{ev.rationale}</span> : null}
                                        </div>
                                      ))}
                                    </div>
                                  </div>
                                )}
                              </div>
                            )}
                          </div>
                        ))}
                    </>
                  )}
                </div>
              )}
            </div>

            {/* Category 2: Ask About This Scope */}
            {features.systemExplain && (
              <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] overflow-hidden transition-all">
                <button
                  type="button"
                  onClick={() => setOpenCategory(openCategory === "ask" ? null : "ask")}
                  className="flex w-full items-center gap-4 px-5 py-4 text-left transition-colors hover:bg-[var(--bg-hover)]"
                >
                  <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-[var(--accent-primary)]/10 text-[var(--accent-primary)]">
                    <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-semibold text-[var(--text-primary)]">Ask About This Scope</p>
                    <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">
                      {explainResult ? "Has answer" : "Ask focused questions"}
                    </p>
                  </div>
                  <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={cn("shrink-0 text-[var(--text-tertiary)] transition-transform duration-200", openCategory === "ask" && "rotate-180")}><path d="m6 9 6 6 6-6"/></svg>
                </button>
                {openCategory === "ask" && (
                  <div className="border-t border-[var(--border-subtle)] bg-[var(--bg-surface)] px-5 py-5">
                    <p className="mb-3 text-sm text-[var(--text-secondary)]">
                      Ask focused questions about {scopeTitle()} without leaving this view.
                    </p>
                    <div className="flex gap-2">
                      <input
                        type="text"
                        value={explainQuestion}
                        onChange={(e) => setExplainQuestion(e.target.value)}
                        placeholder={`Ask about ${scopeTitle()}...`}
                        onKeyDown={(e) => { if (e.key === "Enter") handleExplainSystem(); }}
                        className={`${inputClass} flex-1`}
                      />
                      <Button onClick={handleExplainSystem} disabled={knowledgeLoading || !explainQuestion.trim()}>
                        {knowledgeLoading ? "Thinking..." : "Ask"}
                      </Button>
                    </div>
                    {explainResult && (
                      <div className="mt-4 whitespace-pre-wrap text-sm leading-7 text-[var(--text-secondary)]">
                        {explainResult.explanation}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}

            {/* Category 3: How This Works */}
            <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] overflow-hidden transition-all">
              <button
                type="button"
                onClick={() => setOpenCategory(openCategory === "execution" ? null : "execution")}
                className="flex w-full items-center gap-4 px-5 py-4 text-left transition-colors hover:bg-[var(--bg-hover)]"
              >
                <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-[var(--accent-primary)]/10 text-[var(--accent-primary)]">
                  <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="10"/><polygon points="10 8 16 12 10 16 10 8"/></svg>
                </span>
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-semibold text-[var(--text-primary)]">How This Works</p>
                  <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">
                    {executionPath
                      ? `${executionPath.steps.length} step${executionPath.steps.length !== 1 ? "s" : ""} · ${executionPath.observedStepCount} observed`
                      : "Trace execution paths"}
                  </p>
                </div>
                <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={cn("shrink-0 text-[var(--text-tertiary)] transition-transform duration-200", openCategory === "execution" && "rotate-180")}><path d="m6 9 6 6 6-6"/></svg>
              </button>
              {openCategory === "execution" && (
                <div className="border-t border-[var(--border-subtle)] bg-[var(--bg-surface)] px-5 py-5">
                  <div className="mb-4 flex items-start justify-between gap-3">
                    <p className="text-sm text-[var(--text-secondary)]">
                      Follow the likely backend flow step by step. Observed steps come from indexed relationships; inferred steps are marked clearly.
                    </p>
                    <Button variant="secondary" size="sm" onClick={() => setExecutionCompact((v) => !v)}>
                      {executionCompact ? "Guided view" : "Compact view"}
                    </Button>
                  </div>
                  {knowledgeScopeType === "REPOSITORY" ? (
                    <div className="mb-4 flex flex-col gap-3 md:flex-row">
                      <select
                        value={selectedExecutionEntry}
                        onChange={(e) => setSelectedExecutionEntry(e.target.value)}
                        className={`${inputClass} md:flex-1`}
                      >
                        {executionEntries.length === 0 ? <option value="">No backend entry points found yet</option> : null}
                        {executionEntries.map((entry) => (
                          <option key={entry.value} value={entry.value}>{entry.label}</option>
                        ))}
                      </select>
                      <Button onClick={handleTraceExecution} disabled={!executionInput || executionResult.fetching}>
                        {executionResult.fetching ? "Tracing..." : "Trace execution"}
                      </Button>
                    </div>
                  ) : (
                    <div className="mb-4 flex items-center justify-between gap-3 rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                      <div>
                        <p className="text-sm font-medium text-[var(--text-primary)]">Trace from {scopeTitle()}</p>
                        <p className="mt-1 text-sm text-[var(--text-secondary)]">
                          Use the current {knowledgeScopeType === "SYMBOL" ? "symbol" : "file"} as the anchor and follow the most likely backend path.
                        </p>
                      </div>
                      <Button onClick={handleTraceExecution} disabled={!executionInput || executionResult.fetching}>
                        {executionResult.fetching ? "Tracing..." : "Trace execution"}
                      </Button>
                    </div>
                  )}
                  {!executionRequested ? (
                    <p className="text-sm text-[var(--text-secondary)]">
                      Start from a concrete route, file, or symbol. This stays intentionally scoped so it remains readable for someone new to the codebase.
                    </p>
                  ) : executionPath && !executionPath.trustQualified ? (
                    <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                      <p className="text-sm font-medium text-[var(--text-primary)]">
                        {executionPath.message || "This path is not well enough understood yet."}
                      </p>
                      <p className="mt-2 text-sm text-[var(--text-secondary)]">
                        Use the Field Guide for this scope first, then try again from a more concrete route or symbol.
                      </p>
                    </div>
                  ) : executionPath ? (
                    <div className="space-y-3">
                      <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--text-tertiary)]">
                        <span className={artifactStatusClass}>{executionPath.observedStepCount} observed</span>
                        <span className={artifactStatusClass}>{executionPath.inferredStepCount} inferred</span>
                        <span className={artifactStatusClass}>{executionPath.entryLabel}</span>
                      </div>
                      {executionPath.steps.map((step) => (
                        <div key={`${step.orderIndex}-${step.label}`} className="rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                          <div className="flex items-start justify-between gap-3">
                            <div>
                              <p className="text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Step {step.orderIndex + 1} · {step.kind}</p>
                              <p className="mt-1 text-sm font-semibold text-[var(--text-primary)]">{step.label}</p>
                            </div>
                            <div className="flex items-center gap-2">
                              <span className={confidenceClass(step.confidence.toUpperCase())}>{step.confidence}</span>
                              {!step.observed ? <span className="text-xs text-[var(--text-tertiary)]">inferred</span> : null}
                            </div>
                          </div>
                          <p className={cn("mt-2 text-sm text-[var(--text-secondary)]", executionCompact ? "leading-6" : "leading-7")}>
                            {step.explanation}
                          </p>
                          {!executionCompact && step.reason ? <p className="mt-2 text-xs text-[var(--text-tertiary)]">{step.reason}</p> : null}
                          {step.filePath ? (
                            <div className="mt-3">
                              <SourceRefLink
                                repositoryId={repoId}
                                target={{
                                  tab: "files",
                                  filePath: step.filePath,
                                  line: step.lineStart ?? undefined,
                                  endLine: step.lineEnd ?? undefined,
                                }}
                                className="text-xs"
                              >
                                {step.filePath}{step.lineStart ? `:${step.lineStart}` : ""}
                              </SourceRefLink>
                            </div>
                          ) : null}
                        </div>
                      ))}
                    </div>
                  ) : null}
                </div>
              )}
            </div>

            {/* Category 4: Workflow Story */}
            <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] overflow-hidden transition-all">
              <button
                type="button"
                onClick={() => setOpenCategory(openCategory === "workflow" ? null : "workflow")}
                className="flex w-full items-center gap-4 px-5 py-4 text-left transition-colors hover:bg-[var(--bg-hover)]"
              >
                <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-[var(--accent-primary)]/10 text-[var(--accent-primary)]">
                  <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5L14.5 2z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/><line x1="10" y1="9" x2="8" y2="9"/></svg>
                </span>
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-semibold text-[var(--text-primary)]">Workflow Story</p>
                  <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">
                    {currentWorkflowStory && (currentWorkflowStory.status === "READY" || currentWorkflowStory.status === "STALE")
                      ? `${currentWorkflowStory.sections.length} section${currentWorkflowStory.sections.length !== 1 ? "s" : ""}`
                      : currentWorkflowStory && (currentWorkflowStory.status === "GENERATING" || currentWorkflowStory.status === "PENDING")
                        ? currentWorkflowStory.status === "PENDING" ? "Queued…" : "Generating…"
                        : "Not generated yet"}
                  </p>
                </div>
                <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={cn("shrink-0 text-[var(--text-tertiary)] transition-transform duration-200", openCategory === "workflow" && "rotate-180")}><path d="m6 9 6 6 6-6"/></svg>
              </button>
              {openCategory === "workflow" && (
                <div className="border-t border-[var(--border-subtle)] bg-[var(--bg-surface)] px-5 py-5">
                  <div className="mb-4 flex items-start justify-between gap-3">
                    <p className="text-sm text-[var(--text-secondary)]">
                      See how someone is likely to experience this workflow, what usually happens next, and where to inspect the implementation.
                    </p>
                    <div className="flex shrink-0 gap-2">
                      {!currentWorkflowStory ? (
                        <Button variant="secondary" size="sm" onClick={handleGenerateWorkflowStory} disabled={knowledgeLoading || isWorkflowStoryGenerating}>
                          {isWorkflowStoryGenerating ? "Generating..." : "Generate story"}
                        </Button>
                      ) : null}
                      {currentWorkflowStory ? (
                        <Button variant="secondary" size="sm" onClick={() => handleRefreshArtifact(currentWorkflowStory.id)} disabled={knowledgeLoading}>
                          {artifactRetryLabel(currentWorkflowStory, currentWorkflowStoryJob, "story")}
                        </Button>
                      ) : null}
                      {currentWorkflowStoryJob && (currentWorkflowStoryJob.status === "pending" || currentWorkflowStoryJob.status === "generating") ? (
                        <Button variant="secondary" size="sm" onClick={() => void handleCancelRepoJob(currentWorkflowStoryJob.id)} disabled={knowledgeLoading || cancellingJobIds[currentWorkflowStoryJob.id]}>
                          {cancellingJobIds[currentWorkflowStoryJob.id] ? "Cancelling..." : "Cancel"}
                        </Button>
                      ) : null}
                    </div>
                  </div>
                  {repoJobStatusLabel(currentWorkflowStoryJob) ? (
                    <p className="mb-4 text-xs text-[var(--text-tertiary)]">{repoJobStatusLabel(currentWorkflowStoryJob)}</p>
                  ) : null}
                  {!currentWorkflowStory && !knowledgeLoading ? (
                    <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                      <p className="text-sm font-medium text-[var(--text-primary)]">No workflow story for this view yet.</p>
                      <p className="mt-2 text-sm text-[var(--text-secondary)]">
                        Generate a grounded story that explains who is trying to do what here, what the happy path looks like, and where to inspect the code when you need to change it.
                      </p>
                    </div>
                  ) : null}
                  {currentWorkflowStory && isWorkflowStoryGenerating ? renderKnowledgeProgress(currentWorkflowStory, "Queued for workflow generation", currentWorkflowStoryJob) : null}
                  {currentWorkflowStory?.status === "FAILED" ? renderKnowledgeFailure(currentWorkflowStory) : null}
                  {currentWorkflowStory && (currentWorkflowStory.status === "READY" || currentWorkflowStory.status === "STALE") ? (
                    <div className="space-y-3">
                      {currentWorkflowStory.sections
                        .slice()
                        .sort((a, b) => a.orderIndex - b.orderIndex)
                        .map((section) => (
                          <div key={section.id} className="rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                            <button
                              type="button"
                              onClick={() => setExpandedWorkflowSection(expandedWorkflowSection === section.id ? null : section.id)}
                              className="flex w-full items-start justify-between gap-3 text-left"
                            >
                              <div>
                                <p className="text-sm font-semibold text-[var(--text-primary)]">{section.title}</p>
                                {section.summary ? <p className="mt-1 text-sm text-[var(--text-secondary)]">{section.summary}</p> : null}
                              </div>
                              <div className="flex items-center gap-2">
                                <span className={confidenceClass(section.confidence)}>{section.confidence}</span>
                                {section.inferred ? <span className="text-xs text-[var(--text-tertiary)]">inferred</span> : null}
                              </div>
                            </button>
                            {expandedWorkflowSection === section.id ? (
                              <div className="mt-3">
                                <p className="whitespace-pre-wrap text-sm leading-7 text-[var(--text-secondary)]">{section.content}</p>
                                {section.evidence.length > 0 ? (
                                  <div className="mt-4 rounded-[var(--radius-sm)] bg-[var(--bg-base)] p-3">
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
                                              {ev.filePath}{ev.lineStart ? `:${ev.lineStart}` : ""}
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
                        ))}
                    </div>
                  ) : null}
                </div>
              )}
            </div>

            {/* Category 5: More Ways To Explore */}
            {knowledgeScopeType === "REPOSITORY" && (currentLearningPath || currentCodeTour || features.learningPaths || features.codeTours) && (
              <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] overflow-hidden transition-all">
                <button
                  type="button"
                  onClick={() => setOpenCategory(openCategory === "explore" ? null : "explore")}
                  className="flex w-full items-center gap-4 px-5 py-4 text-left transition-colors hover:bg-[var(--bg-hover)]"
                >
                  <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-[var(--accent-primary)]/10 text-[var(--accent-primary)]">
                    <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-semibold text-[var(--text-primary)]">More Ways To Explore</p>
                    <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">
                      {[
                        currentLearningPath ? `Learning path (${currentLearningPath.sections.length} steps)` : null,
                        currentCodeTour ? `Code tour (${currentCodeTour.sections.length} stops)` : null,
                      ].filter(Boolean).join(" · ") || "Learning paths & code tours"}
                    </p>
                  </div>
                  <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={cn("shrink-0 text-[var(--text-tertiary)] transition-transform duration-200", openCategory === "explore" && "rotate-180")}><path d="m6 9 6 6 6-6"/></svg>
                </button>
                {openCategory === "explore" && (
                  <div className="border-t border-[var(--border-subtle)] bg-[var(--bg-surface)] px-5 py-5">
                    <div className="mb-4 flex flex-wrap gap-2">
                      {features.learningPaths && (
                        <Button variant="secondary" size="sm" onClick={currentLearningPath ? () => handleRefreshArtifact(currentLearningPath.id) : handleGenerateLearningPath} disabled={knowledgeLoading || isLearningPathGenerating}>
                          {currentLearningPath?.status === "PENDING" ? "Queued..." : isLearningPathGenerating ? "Generating..." : currentLearningPath ? artifactRetryLabel(currentLearningPath, currentLearningPathJob, "learning path") : "Generate learning path"}
                        </Button>
                      )}
                      {features.codeTours && (
                        <Button variant="secondary" size="sm" onClick={currentCodeTour ? () => handleRefreshArtifact(currentCodeTour.id) : handleGenerateCodeTour} disabled={knowledgeLoading || isCodeTourGenerating}>
                          {currentCodeTour?.status === "PENDING" ? "Queued..." : isCodeTourGenerating ? "Generating..." : currentCodeTour ? artifactRetryLabel(currentCodeTour, currentCodeTourJob, "code tour") : "Generate code tour"}
                        </Button>
                      )}
                      {currentLearningPathJob && (currentLearningPathJob.status === "pending" || currentLearningPathJob.status === "generating") ? (
                        <Button variant="secondary" size="sm" onClick={() => void handleCancelRepoJob(currentLearningPathJob.id)} disabled={knowledgeLoading || cancellingJobIds[currentLearningPathJob.id]}>
                          {cancellingJobIds[currentLearningPathJob.id] ? "Cancelling..." : "Cancel learning path"}
                        </Button>
                      ) : null}
                      {currentCodeTourJob && (currentCodeTourJob.status === "pending" || currentCodeTourJob.status === "generating") ? (
                        <Button variant="secondary" size="sm" onClick={() => void handleCancelRepoJob(currentCodeTourJob.id)} disabled={knowledgeLoading || cancellingJobIds[currentCodeTourJob.id]}>
                          {cancellingJobIds[currentCodeTourJob.id] ? "Cancelling..." : "Cancel code tour"}
                        </Button>
                      ) : null}
                    </div>
                    {currentLearningPath && (
                      <div className="mb-5">
                        <h4 className="text-sm font-semibold text-[var(--text-primary)]">Learning Path</h4>
                        {repoJobStatusLabel(currentLearningPathJob) ? (
                          <p className="mt-2 text-xs text-[var(--text-tertiary)]">{repoJobStatusLabel(currentLearningPathJob)}</p>
                        ) : null}
                        {isLearningPathGenerating ? (
                          <div className="mt-3">{renderKnowledgeProgress(currentLearningPath, "Queued for learning path generation", currentLearningPathJob)}</div>
                        ) : null}
                        {currentLearningPath.status === "FAILED" ? <div className="mt-3">{renderKnowledgeFailure(currentLearningPath)}</div> : null}
                        <div className="mt-3 space-y-3">
                          {currentLearningPath.sections.slice().sort((a, b) => a.orderIndex - b.orderIndex).map((step, idx) => (
                            <div key={step.id} className="rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                              <div
                                onClick={() => setExpandedSection(expandedSection === step.id ? null : step.id)}
                                className="flex cursor-pointer gap-4"
                              >
                                <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-[var(--accent-primary)] text-xs font-semibold text-[var(--accent-contrast)]">{idx + 1}</div>
                                <div className="min-w-0 flex-1">
                                  <p className="text-sm font-medium text-[var(--text-primary)]">{step.title}</p>
                                  {step.summary && expandedSection !== step.id ? <p className="mt-1 text-xs text-[var(--text-secondary)]">{step.summary}</p> : null}
                                </div>
                              </div>
                              {expandedSection === step.id && step.content && (
                                <div className="mt-3 pl-11">
                                  <p className="whitespace-pre-wrap text-sm leading-7 text-[var(--text-secondary)]">{step.content}</p>
                                </div>
                              )}
                            </div>
                          ))}
                        </div>
                      </div>
                    )}
                    {currentCodeTour && (
                      <div>
                        <h4 className="text-sm font-semibold text-[var(--text-primary)]">Code Tour</h4>
                        {repoJobStatusLabel(currentCodeTourJob) ? (
                          <p className="mt-2 text-xs text-[var(--text-tertiary)]">{repoJobStatusLabel(currentCodeTourJob)}</p>
                        ) : null}
                        {isCodeTourGenerating ? <div className="mt-3">{renderKnowledgeProgress(currentCodeTour, "Queued for code tour generation", currentCodeTourJob)}</div> : null}
                        {currentCodeTour.status === "FAILED" ? <div className="mt-3">{renderKnowledgeFailure(currentCodeTour)}</div> : null}
                        <div className="mt-3 flex flex-wrap gap-2">
                          {currentCodeTour.sections.slice().sort((a, b) => a.orderIndex - b.orderIndex).map((stop, idx) => (
                            <button
                              key={stop.id}
                              type="button"
                              onClick={() => setTourStopIndex(idx)}
                              className={cn(
                                "rounded-full border px-3 py-1.5 text-xs transition-colors",
                                idx === tourStopIndex
                                  ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                                  : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)]",
                              )}
                            >
                              {idx + 1}. {stop.title}
                            </button>
                          ))}
                        </div>
                        {currentCodeTour.sections[tourStopIndex] && (() => {
                          const stop = currentCodeTour.sections[tourStopIndex];
                          return (
                            <div className="mt-4 rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-4">
                              <div className="flex items-start justify-between gap-4">
                                <p className="text-sm font-medium text-[var(--text-primary)]">{stop.title}</p>
                                <span className={confidenceClass(stop.confidence)}>{stop.confidence}</span>
                              </div>
                              <p className="mt-2 whitespace-pre-wrap text-sm leading-7 text-[var(--text-secondary)]">{stop.content}</p>
                              {stop.evidence.length > 0 && (
                                <div className="mt-3 rounded-[var(--radius-sm)] bg-[var(--bg-base)] p-3">
                                  <p className="mb-2 text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">References</p>
                                  <div className="space-y-2">
                                    {stop.evidence.map((ev) => (
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
                                            {ev.filePath}{ev.lineStart ? `:${ev.lineStart}` : ""}{ev.lineEnd && ev.lineEnd !== ev.lineStart ? `-${ev.lineEnd}` : ""}
                                          </SourceRefLink>
                                        ) : null}
                                        {ev.rationale ? <span className="ml-2">{ev.rationale}</span> : null}
                                      </div>
                                    ))}
                                  </div>
                                </div>
                              )}
                            </div>
                          );
                        })()}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>

          {/* Right sidebar */}
          <div className="space-y-4">
            <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
              <p className="text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">What am I looking at?</p>
              <p className="mt-2 text-sm text-[var(--text-secondary)]">
                Move from repository to module to file to symbol. Symbols are named code elements like functions, methods, classes, and exported values.
              </p>
            </div>
            <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] p-4">
              <div className="mb-3">
                <p className="text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Explore Deeper</p>
                <p className="mt-1 text-sm text-[var(--text-secondary)]">Move through the codebase one scope at a time.</p>
              </div>
              <div className="space-y-2">
                {scopeChildren.length === 0 && (
                  <p className="text-sm text-[var(--text-secondary)]">No deeper scopes available from here.</p>
                )}
                {scopeChildren.map((child) => (
                  <button
                    key={`${child.scopeType}-${child.scopePath}`}
                    type="button"
                    onClick={() => setKnowledgeScope(child.scopeType, child.scopePath)}
                    className="w-full rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 py-3 text-left transition-colors hover:bg-[var(--bg-hover)]"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <p className="text-sm font-medium text-[var(--text-primary)]">{child.label}</p>
                        {child.summary ? <p className="mt-1 text-xs text-[var(--text-secondary)]">{child.summary}</p> : null}
                      </div>
                      <div className="flex shrink-0 gap-2">
                        <span className={artifactStatusClass}>{child.hasArtifact ? "View" : "Generate"}</span>
                        {!child.hasArtifact && (
                          <Button
                            type="button"
                            size="sm"
                            variant="secondary"
                            onClick={(e) => {
                              e.stopPropagation();
                              setKnowledgeScope(child.scopeType, child.scopePath);
                              void handleGenerateCliffNotesFor(child.scopeType, child.scopePath);
                            }}
                          >
                            Generate
                          </Button>
                        )}
                      </div>
                    </div>
                  </button>
                ))}
              </div>
            </div>

            {knowledgeArtifacts.filter((a) => a.status === "FAILED" && matchesEngine(a)).map((a) => (
              <Panel key={a.id} className="border-[var(--color-error,#ef4444)]">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm text-[var(--color-error,#ef4444)]">
                    {a.type.replace("_", " ")} failed for this lens
                  </span>
                  <Button variant="secondary" size="sm" onClick={() => handleRefreshArtifact(a.id)} disabled={knowledgeLoading}>
                    Retry
                  </Button>
                </div>
              </Panel>
            ))}
          </div>
        </div>
      </Panel>
    </div>
  );
}
