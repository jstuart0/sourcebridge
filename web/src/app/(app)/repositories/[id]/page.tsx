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
  EXECUTION_ENTRY_POINTS_QUERY,
  EXECUTION_PATH_QUERY,
  GENERATE_CLIFF_NOTES_MUTATION,
  GENERATE_LEARNING_PATH_MUTATION,
  GENERATE_CODE_TOUR_MUTATION,
  GENERATE_WORKFLOW_STORY_MUTATION,
  EXPLAIN_SYSTEM_MUTATION,
  REFRESH_KNOWLEDGE_ARTIFACT_MUTATION,
  LATEST_IMPACT_REPORT_QUERY,
  DISCOVERED_REQUIREMENTS_QUERY,
  TRIGGER_SPEC_EXTRACTION_MUTATION,
  PROMOTE_DISCOVERED_REQUIREMENT_MUTATION,
  DISMISS_DISCOVERED_REQUIREMENT_MUTATION,
  DISMISS_ALL_DISCOVERED_REQUIREMENTS_MUTATION,
} from "@/lib/graphql/queries";
import { useFeatures } from "@/lib/features";
import { useServerCapabilities } from "@/lib/use-server-capabilities";
import { Button } from "@/components/ui/button";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { RepoJobsPopover } from "@/components/llm/repo-jobs-popover";
import { FileTree } from "@/components/source/FileTree";
import { EnterpriseSourcePanel } from "@/components/source/EnterpriseSourcePanel";
import { SourceRefLink } from "@/components/source/SourceRefLink";
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
import { WikiSettingsPanel } from "./wiki-settings-panel";
import { ClusterTable } from "@/components/subsystems/ClusterTable";
import { ImproveLabelsButton } from "@/components/subsystems/ImproveLabelsButton";
import { SymbolTree } from "@/components/source/SymbolTree";
import { SymbolList } from "@/components/source/SymbolList";
import { kindBadgeClass, kindLabel, SYMBOL_KINDS } from "@/components/source/symbol-kind";
import { normalizeActivityResponse } from "@/lib/llm/activity";
import { authFetch } from "@/lib/auth-fetch";
import { trackEvent } from "@/lib/telemetry";
import { disableJobAlerts, enableJobAlerts, jobAlertsEnabled, notifyJobEvent } from "@/lib/notifications";

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

function knowledgeProgressLabel(artifact: KnowledgeArtifact): string {
  if (artifact.progressMessage?.trim()) return artifact.progressMessage.trim();
  if (artifact.progressPhase === "queued") return "Waiting for a generation slot";
  if (artifact.progressPhase === "snapshot") return "Preparing the repository snapshot";
  if (artifact.progressPhase === "llm") return "LLM completed, persisting the result";
  if (artifact.progressPhase === "ready") return "Finishing up";
  if (artifact.status === "PENDING") return "Waiting for a generation slot";
  return "Generating artifact";
}

function knowledgeJobProgressLabel(job: RepoJobView): string {
  if (job.progress_message?.trim()) return job.progress_message.trim();
  if (job.progress_phase === "queued") return "Waiting for a generation slot";
  if (job.progress_phase === "snapshot") return "Preparing the repository snapshot";
  if (job.progress_phase === "llm") return "LLM completed, persisting the result";
  if (job.progress_phase === "backoff") return "Waiting for model backend to recover";
  if (job.status === "pending") return "Waiting for a generation slot";
  return "Generating artifact";
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
    case "BUILDING_TREE":
      return "Building tree";
    case "FIRST_PASS_READY":
      return "First pass ready";
    case "NEEDS_REFRESH":
      return "Refresh available";
    case "DEEPENING":
      return "Deepening";
    case "READY":
      return "Ready";
    case "FAILED":
      return "Failed";
    default:
      return "Not built";
  }
}

function understandingTreeLabel(understanding: RepositoryUnderstanding | null | undefined): string {
  switch (understanding?.treeStatus) {
    case "COMPLETE":
      return "Tree complete";
    case "PARTIAL":
      return "Tree partial";
    case "MISSING":
      return "Tree missing";
    default:
      return "Tree unknown";
  }
}

function shouldCollapseRepositoryUnderstanding(understanding: RepositoryUnderstanding | null | undefined): boolean {
  if (!understanding) return false;
  if (understanding.errorMessage) return false;
  if (understanding.refreshAvailable) return false;
  return understanding.stage === "READY" && understanding.treeStatus === "COMPLETE";
}

function understandingLeadSummary(understanding: RepositoryUnderstanding | null | undefined): string | null {
  const sections = understanding?.firstPassSections ?? [];
  if (!sections.length) return null;
  const preferredTitles = [
    "Architecture Overview",
    "System Purpose",
    "Core Components",
    "Core System Flows",
  ];
  for (const title of preferredTitles) {
    const match = sections.find((section) => section.title === title && section.summary.trim());
    if (match) return match.summary.trim();
  }
  const fallback = sections.find((section) => section.summary.trim());
  return fallback?.summary.trim() || null;
}

function understandingHighlightSections(understanding: RepositoryUnderstanding | null | undefined): Array<{ title: string; summary: string }> {
  const sections = (understanding?.firstPassSections ?? []).filter(
    (section) => section.title.trim() && section.summary.trim(),
  );
  const preferredTitles = [
    "System Purpose",
    "Architecture Overview",
    "Core Components",
    "Core System Flows",
    "External Dependencies",
    "Domain Model",
  ];
  const ordered: Array<{ title: string; summary: string }> = [];
  for (const title of preferredTitles) {
    const match = sections.find((section) => section.title === title);
    if (match && !ordered.some((section) => section.title === match.title)) {
      ordered.push(match);
    }
  }
  for (const section of sections) {
    if (!ordered.some((candidate) => candidate.title === section.title)) {
      ordered.push(section);
    }
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
  if (status === "deep") {
    return "rounded-full border border-[var(--accent-primary)]/30 bg-[var(--accent-primary)]/10 px-2 py-0.5 text-xs font-medium text-[var(--accent-primary)]";
  }
  if (status === "light") {
    return "rounded-full border border-[var(--border-default)] bg-[var(--bg-surface)] px-2 py-0.5 text-xs font-medium text-[var(--text-primary)]";
  }
  return "rounded-full border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-2 py-0.5 text-xs font-medium text-[var(--text-tertiary)]";
}

function artifactRefinementSummary(artifact: KnowledgeArtifact | null | undefined): string | null {
  if (!artifact?.sections?.length) return null;
  let refined = 0;
  let deepened = 0;
  for (const section of artifact.sections) {
    const status = (section.refinementStatus || "").trim().toLowerCase();
    if (status === "deep") {
      deepened++;
      continue;
    }
    if (status === "light") {
      refined++;
    }
  }
  const parts: string[] = [];
  if (refined > 0) parts.push(`${refined} refined`);
  if (deepened > 0) parts.push(`${deepened} deepened`);
  return parts.length > 0 ? parts.join(" · ") : null;
}

function artifactDeepeningSummary(artifact: KnowledgeArtifact | null | undefined): string | null {
  const units = (artifact?.refinementUnits ?? []).filter((unit) => unit.refinementType === "cliff_notes_deep");
  if (!units.length) return null;
  let queued = 0;
  let running = 0;
  let failed = 0;
  let completed = 0;
  for (const unit of units) {
    const status = unit.status.trim().toLowerCase();
    if (status === "queued") queued++;
    else if (status === "running") running++;
    else if (status === "failed") failed++;
    else if (status === "completed") completed++;
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

function renderKnowledgeProgress(artifact: KnowledgeArtifact, waitingLabel: string, job?: RepoJobView | null) {
  const liveJob = job && (job.status === "pending" || job.status === "generating") ? job : null;
  const heartbeat = liveJob ? formatHeartbeatAge(liveJob.updated_at) : null;
  const isCliffNotes = artifact.type === "CLIFF_NOTES";
  const label = liveJob
    ? (liveJob.status === "pending"
      ? waitingLabel
      : liveJob.progress >= 0.9
        ? "Finalizing"
        : "Generating")
    : artifact.status === "PENDING"
      ? waitingLabel
      : knowledgeQueueLabel(artifact);
  const progress = liveJob ? liveJob.progress : artifact.progress;
  const baseProgressLabel = liveJob ? knowledgeJobProgressLabel(liveJob) : knowledgeProgressLabel(artifact);
  const activeFallbackLabel = isCliffNotes && liveJob?.status === "generating" && progress >= 0.6 && progress < 0.96
    ? "Building the repository summary tree. Slow leaves may fall back and continue."
    : null;
  const displayLabel = activeFallbackLabel || baseProgressLabel;
  const progressLabel = heartbeat && liveJob?.status === "generating"
    ? `${displayLabel} · Last heartbeat ${heartbeat}`
    : displayLabel;
  return (
    <div className="mb-5 space-y-1">
      <div className="flex items-center justify-between text-xs text-[var(--text-secondary)]">
        <span>{label}</span>
        <span>{Math.round(progress * 100)}%</span>
      </div>
      <div className="text-xs text-[var(--text-tertiary)]">{progressLabel}</div>
      <progress
        className="h-1.5 w-full overflow-hidden rounded-full [&::-webkit-progress-bar]:bg-[var(--bg-hover)] [&::-webkit-progress-value]:bg-[var(--accent-primary)] [&::-moz-progress-bar]:bg-[var(--accent-primary)]"
        max={100}
        value={Math.max(progress * 100, 5)}
      />
    </div>
  );
}

function formatQueueEta(ms?: number): string | null {
  if (!ms || ms <= 0) return null;
  const seconds = Math.ceil(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.ceil(seconds / 60);
  return `${minutes}m`;
}

function formatHeartbeatAge(iso?: string): string | null {
  if (!iso) return null;
  const ts = new Date(iso).getTime();
  if (!ts || Number.isNaN(ts)) return null;
  const diff = Math.max(0, Date.now() - ts);
  const seconds = Math.floor(diff / 1000);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ago`;
}

function formatElapsedMs(ms?: number): string | null {
  if (!ms || ms <= 0) return null;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rem = seconds % 60;
  if (minutes < 60) return rem > 0 ? `${minutes}m ${rem}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remMin = minutes % 60;
  return remMin > 0 ? `${hours}h ${remMin}m` : `${hours}h`;
}

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
  if (artifact?.status === "FAILED" || job?.status === "cancelled" || job?.status === "failed") {
    return `Retry ${baseLabel}`;
  }
  return `Refresh ${baseLabel}`;
}

function artifactHasActiveJob(
  artifact: KnowledgeArtifact | null | undefined,
  job: RepoJobView | null | undefined,
): boolean {
  if (!artifact) return false;
  if (job) {
    return job.status === "pending" || job.status === "generating";
  }
  return artifact.status === "PENDING" || artifact.status === "GENERATING";
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

interface RepoJobView {
  id: string;
  subsystem: string;
  job_type: string;
  status: "pending" | "generating" | "ready" | "failed" | "cancelled";
  progress: number;
  progress_phase?: string;
  progress_message?: string;
  error_code?: string;
  error_message?: string;
  error_title?: string;
  error_hint?: string;
  retry_count: number;
  max_attempts: number;
  attached_requests: number;
  reused_summaries?: number;
  leaf_cache_hits?: number;
  file_cache_hits?: number;
  package_cache_hits?: number;
  root_cache_hits?: number;
  cached_nodes_loaded?: number;
  total_nodes?: number;
  resume_stage?: string;
  skipped_leaf_units?: number;
  skipped_file_units?: number;
  skipped_package_units?: number;
  skipped_root_units?: number;
  artifact_id?: string;
  repo_id?: string;
  queue_position?: number;
  queue_depth?: number;
  estimated_wait_ms?: number;
  elapsed_ms: number;
  updated_at: string;
}

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
    : "files";
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
  const [specExtracting, setSpecExtracting] = useState(false);
  const [specExtractionResult, setSpecExtractionResult] = useState<string | null>(null);
  const [specConfidenceFilter, setSpecConfidenceFilter] = useState<string | null>(null);
  const [repoJobs, setRepoJobs] = useState<RepoJobActivityResponse | null>(null);
  const [repoJobsError, setRepoJobsError] = useState<string | null>(null);
  const [cancellingJobIds, setCancellingJobIds] = useState<Record<string, boolean>>({});
  const repoJobsPollRef = useRef<number | null>(null);
  const [alertsEnabled, setAlertsEnabled] = useState(false);
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

  const knowledgeScopeType = (searchParams.get("scope") || "repository").toUpperCase();
  const knowledgeScopePath = searchParams.get("path") || "";
  const knowledgeAudience = (searchParams.get("audience") || "developer").toUpperCase();
  const knowledgeDepth = (searchParams.get("depth") || "medium").toUpperCase();
  const knowledgeGenerationMode = (searchParams.get("mode") || "understanding_first").toUpperCase();

  const [knowledgeResult, reexecuteKnowledge] = useQuery({
    query: KNOWLEDGE_ARTIFACTS_QUERY,
    variables: {
      repositoryId: repoId,
      scopeType: knowledgeScopeType,
      scopePath: knowledgeScopeType === "REPOSITORY" ? undefined : knowledgeScopePath,
    },
    pause: tab !== "knowledge",
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
    pause: tab !== "knowledge",
  });
  const [executionRequested, setExecutionRequested] = useState(false);
  const [executionCompact, setExecutionCompact] = useState(false);
  const [selectedExecutionEntry, setSelectedExecutionEntry] = useState("");
  const [executionEntriesResult] = useQuery({
    query: EXECUTION_ENTRY_POINTS_QUERY,
    variables: { repositoryId: repoId },
    pause: tab !== "knowledge",
  });
  const executionInput = useMemo(() => {
    if (tab !== "knowledge") return null;
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
  }, [tab, knowledgeScopeType, knowledgeScopePath, repoId, selectedExecutionEntry]);
  const [executionResult, reexecuteExecution] = useQuery({
    query: EXECUTION_PATH_QUERY,
    variables: executionInput ? { input: executionInput } : undefined,
    pause: !executionRequested || !executionInput,
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

  // Poll for knowledge artifacts when any are in GENERATING state
  const hasGenerating = knowledgeResult.data?.knowledgeArtifacts?.some(
    (a: KnowledgeArtifact) => a.status === "GENERATING" || a.status === "PENDING"
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
    setAlertsEnabled(jobAlertsEnabled());
  }, []);

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
  const [, generateLearningPath] = useMutation(GENERATE_LEARNING_PATH_MUTATION);
  const [, generateCodeTour] = useMutation(GENERATE_CODE_TOUR_MUTATION);
  const [, generateWorkflowStory] = useMutation(GENERATE_WORKFLOW_STORY_MUTATION);
  const [, explainSystem] = useMutation(EXPLAIN_SYSTEM_MUTATION);
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
  const knowledgeArtifacts: KnowledgeArtifact[] = knowledgeResult.data?.knowledgeArtifacts || [];
  const scopeChildren: ScopeChild[] = scopeChildrenResult.data?.knowledgeScopeChildren || [];
  const executionEntries: ExecutionEntryPoint[] = useMemo(
    () => executionEntriesResult.data?.executionEntryPoints || [],
    [executionEntriesResult.data?.executionEntryPoints]
  );
  const executionPath: ExecutionPathResult | null = executionResult.data?.executionPath || null;

  const features = useFeatures();
  const symbolScopedAnalysisEnabled = features.symbolScopedAnalysis;
  const [subsystemsRefreshKey, setSubsystemsRefreshKey] = useState(0);
  const [knowledgeLoading, setKnowledgeLoading] = useState(false);
  const [copiedSetupCmd, setCopiedSetupCmd] = useState(false);
  const serverCaps = useServerCapabilities();
  const [understandingCollapsed, setUnderstandingCollapsed] = useState(false);
  const [understandingShowAllSections, setUnderstandingShowAllSections] = useState(false);
  const [explainQuestion, setExplainQuestion] = useState("");
  const [explainResult, setExplainResult] = useState<{ explanation: string } | null>(null);
  const [tourStopIndex, setTourStopIndex] = useState(0);
  const [expandedSection, setExpandedSection] = useState<string | null>(null);
  const [expandedWorkflowSection, setExpandedWorkflowSection] = useState<string | null>(null);
  const [openCategory, setOpenCategory] = useState<"guide" | "ask" | "execution" | "workflow" | "explore" | null>("guide");
  const sourceTarget = useMemo(
    () => sourceTargetFromSearchParams(new URLSearchParams(searchParams.toString())),
    [searchParams]
  );
  // Engine-mode-aware picker: must filter by generationMode so the selected
  // engine toggle (Classic vs Understanding First) is respected. Without this
  // guard, .find() returns whichever artifact the server happened to list
  // first, which can mask a Classic-mode READY artifact behind an
  // Understanding-First FAILED one (or vice versa) in the UI.
  const matchesEngine = (a: KnowledgeArtifact): boolean =>
    (a.generationMode || "").toUpperCase() === knowledgeGenerationMode;
  const currentCliffNotes = knowledgeArtifacts.find(
    (a) => a.type === "CLIFF_NOTES" && a.audience === knowledgeAudience && a.depth === knowledgeDepth && matchesEngine(a)
  );
  const currentLearningPath = knowledgeArtifacts.find(
    (a) => a.type === "LEARNING_PATH" && a.audience === knowledgeAudience && a.depth === knowledgeDepth && matchesEngine(a)
  );
  const currentCodeTour = knowledgeArtifacts.find(
    (a) => a.type === "CODE_TOUR" && a.audience === knowledgeAudience && a.depth === knowledgeDepth && matchesEngine(a)
  );
  const currentWorkflowStory = knowledgeArtifacts.find(
    (a) => a.type === "WORKFLOW_STORY" && a.audience === knowledgeAudience && a.depth === knowledgeDepth && matchesEngine(a)
  );
  const currentUnderstanding: RepositoryUnderstanding | null = repoResult.data?.repository?.repositoryUnderstanding || null;
  const shouldAutoCollapseUnderstanding = shouldCollapseRepositoryUnderstanding(currentUnderstanding);
  const understandingSummary = understandingLeadSummary(currentUnderstanding);
  const understandingSections = understandingHighlightSections(currentUnderstanding);
  const understandingFeaturedSections = understandingSections.slice(0, 4);
  const understandingAdditionalSections = understandingSections.slice(4);
  const understandingBusy = Boolean(
    currentUnderstanding &&
      (currentUnderstanding.stage === "BUILDING_TREE" ||
        currentUnderstanding.stage === "FIRST_PASS_READY" ||
        currentUnderstanding.stage === "DEEPENING"),
  );

  useEffect(() => {
    setUnderstandingCollapsed(shouldAutoCollapseUnderstanding);
  }, [currentUnderstanding?.id, shouldAutoCollapseUnderstanding]);

  useEffect(() => {
    setUnderstandingShowAllSections(false);
  }, [currentUnderstanding?.id]);
  const repoGenerationModeDefault: RepositoryGenerationMode = (repo?.generationModeDefault || "UNDERSTANDING_FIRST") as RepositoryGenerationMode;
  const repoActiveJobs = useMemo(() => repoJobs?.active ?? [], [repoJobs?.active]);
  const repoRecentJobs = useMemo(() => repoJobs?.recent ?? [], [repoJobs?.recent]);
  const artifactJobMap = useMemo(() => {
    const map = new Map<string, RepoJobView>();
    for (const job of [...repoActiveJobs, ...repoRecentJobs]) {
      if (job.artifact_id && !map.has(job.artifact_id)) {
        map.set(job.artifact_id, job);
      }
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
  }, [
    currentCliffNotes,
    currentLearningPath,
    currentCodeTour,
    currentWorkflowStory,
    isCliffNotesGenerating,
    isLearningPathGenerating,
    isCodeTourGenerating,
    isWorkflowStoryGenerating,
  ]);

  // Reset tour stop index when code tour changes (e.g. after refresh with different stop count)
  const codeTourId = currentCodeTour?.id;
  useEffect(() => { setTourStopIndex(0); }, [codeTourId]);

  useEffect(() => {
    if (!repo?.id) return;
    trackEvent({
      event: tab === "knowledge" ? "field_guide_opened" : "repository_workspace_opened",
      repositoryId: repo.id,
      metadata: { tab },
    });
  }, [repo?.id, tab]);

  useEffect(() => {
    if (knowledgeScopeType !== "REPOSITORY") return;
    if (!selectedExecutionEntry && executionEntries.length > 0) {
      setSelectedExecutionEntry(executionEntries[0].value);
    }
  }, [knowledgeScopeType, executionEntries, selectedExecutionEntry]);

  useEffect(() => {
    setExecutionRequested(false);
  }, [knowledgeScopeType, knowledgeScopePath]);

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
    try {
      const res = await triggerSpecExtraction({ input: { repositoryId: repoId } });
      if (res.data?.triggerSpecExtraction) {
        const r = res.data.triggerSpecExtraction;
        setSpecExtractionResult(`Discovered ${r.discovered} specs from ${r.totalCandidates} candidates`);
      } else if (res.error) {
        setSpecExtractionResult(`Extraction failed: ${res.error.message}`);
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

  async function handleCancelRepoJob(jobId: string) {
    if (cancellingJobIds[jobId]) return;
    setCancellingJobIds((current) => ({ ...current, [jobId]: true }));
    try {
      const res = await authFetch(`/api/v1/admin/llm/jobs/${encodeURIComponent(jobId)}/cancel`, {
        method: "POST",
      });
      if (!res.ok) {
        throw new Error(`cancel returned ${res.status}`);
      }
      locallyCancelledJobsRef.current[jobId] = Date.now();
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

  async function handleGenerateCliffNotesFor(scopeType = knowledgeScopeType, scopePath = knowledgeScopePath) {
    trackEvent({
      event: "field_guide_generated",
      repositoryId: repoId,
      metadata: { scopeType, scopePath: scopePath || null, audience: knowledgeAudience, depth: knowledgeDepth },
    });
    setKnowledgeLoading(true);
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
      setKnowledgeLoading(false);
    }
  }

  async function handleGenerateCliffNotes() {
    await handleGenerateCliffNotesFor();
  }

  async function handleGenerateScopedCliffNotes() {
    if (!symbolScopeType) return;
    setKnowledgeLoading(true);
    try {
      await generateCliffNotes({
        input: {
          repositoryId: repoId,
          audience: "DEVELOPER",
          depth: "MEDIUM",
          generationMode: knowledgeGenerationMode,
          scopeType: symbolScopeType,
          scopePath: symbolScopePath,
        },
      });
      reexecuteSymbolKnowledge({ requestPolicy: "network-only" });
      reexecuteSymbolChildren({ requestPolicy: "network-only" });
    } finally {
      setKnowledgeLoading(false);
    }
  }

  async function handleRefreshScopedArtifact() {
    if (!currentScopedCliffNotes) return;
    setKnowledgeLoading(true);
    try {
      await refreshArtifact({ id: currentScopedCliffNotes.id });
      reexecuteSymbolKnowledge({ requestPolicy: "network-only" });
      reexecuteSymbolChildren({ requestPolicy: "network-only" });
    } finally {
      setKnowledgeLoading(false);
    }
  }

  async function handleScopedFollowUp() {
    if (!currentScopedCliffNotes || !symbolChatQuestion.trim()) return;
    setKnowledgeLoading(true);
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
      setKnowledgeLoading(false);
    }
  }

  async function handleGenerateLearningPath() {
    setKnowledgeLoading(true);
    try {
      await generateLearningPath({ input: { repositoryId: repoId, audience: knowledgeAudience, depth: knowledgeDepth, generationMode: knowledgeGenerationMode } });
      reexecuteKnowledge({ requestPolicy: "network-only" });
    } finally {
      setKnowledgeLoading(false);
    }
  }

  async function handleBuildRepositoryUnderstanding() {
    setKnowledgeLoading(true);
    try {
      await buildRepositoryUnderstanding({
        input: {
          repositoryId: repoId,
          scopeType: knowledgeScopeType,
          scopePath: knowledgeScopeType === "REPOSITORY" ? undefined : knowledgeScopePath,
        },
      });
      reexecuteRepo({ requestPolicy: "network-only" });
      void fetchRepoJobs();
    } finally {
      setKnowledgeLoading(false);
    }
  }

  async function handleSaveRepositoryGenerationMode(nextMode: RepositoryGenerationMode) {
    setKnowledgeLoading(true);
    try {
      await updateRepositoryKnowledgeSettings({
        input: {
          repositoryId: repoId,
          generationModeDefault: nextMode,
        },
      });
      reexecuteRepo({ requestPolicy: "network-only" });
    } finally {
      setKnowledgeLoading(false);
    }
  }

  async function handleGenerateCodeTour() {
    setKnowledgeLoading(true);
    try {
      await generateCodeTour({ input: { repositoryId: repoId, audience: knowledgeAudience, depth: knowledgeDepth, generationMode: knowledgeGenerationMode } });
      reexecuteKnowledge({ requestPolicy: "network-only" });
    } finally {
      setKnowledgeLoading(false);
    }
  }

  function workflowStoryAnchorLabel() {
    if (knowledgeScopeType === "REPOSITORY") {
      const entry = executionEntries.find((candidate) => candidate.value === selectedExecutionEntry);
      return entry?.label || `${repo?.name || "Repository"} workspace journey`;
    }
    if (knowledgeScopeType === "FILE") {
      return `How someone uses ${scopeTitle()}`;
    }
    if (knowledgeScopeType === "SYMBOL") {
      return `How ${scopeTitle()} participates in a workflow`;
    }
    return `How someone uses ${scopeTitle()}`;
  }

  async function handleGenerateWorkflowStory() {
    trackEvent({
      event: "workflow_story_generated",
      repositoryId: repoId,
      metadata: { scopeType: knowledgeScopeType, scopePath: knowledgeScopePath || null },
    });
    setKnowledgeLoading(true);
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
      setKnowledgeLoading(false);
    }
  }

  async function handleRefreshArtifact(artifactId: string) {
    setKnowledgeLoading(true);
    try {
      await refreshArtifact({ id: artifactId });
      reexecuteKnowledge({ requestPolicy: "network-only" });
      reexecuteScopeChildren({ requestPolicy: "network-only" });
    } finally {
      setKnowledgeLoading(false);
    }
  }

  async function handleExplainSystem() {
    if (!explainQuestion.trim() || knowledgeLoading) return;
    trackEvent({
      event: "explain_scope_used",
      repositoryId: repoId,
      metadata: { scopeType: knowledgeScopeType, scopePath: knowledgeScopePath || null },
    });
    setKnowledgeLoading(true);
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
      setKnowledgeLoading(false);
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

  function setKnowledgeScope(nextScopeType: string, nextScopePath = "") {
    updateSearchParams((next) => {
      next.set("tab", "knowledge");
      next.set("scope", nextScopeType.toLowerCase());
      if (nextScopePath) {
        next.set("path", nextScopePath);
      } else {
        next.delete("path");
      }
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

  function renderScopedCliffNotesSection(section: KnowledgeSection) {
    return (
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
            <span className={confidenceClass(section.confidence)}>{section.confidence}</span>
            {section.inferred ? <span className="text-xs text-[var(--text-tertiary)]">inferred</span> : null}
          </div>
        </div>
        {expandedSection === section.id ? (
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

  if (!repo && !repoResult.fetching) {
    return (
      <PageFrame>
        <Panel>
          <p className="text-sm text-[var(--text-secondary)]">Repository not found.</p>
        </Panel>
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
              disabled={knowledgeLoading || understandingBusy}
            >
              {knowledgeLoading
                ? "Starting..."
                : understandingBusy
                  ? "Understanding running"
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
            <div className={`mb-4 rounded-[var(--control-radius)] border px-3 py-2 text-sm ${specExtractionResult.startsWith("Extraction failed") ? "border-red-500/30 bg-red-500/10 text-red-500" : "border-emerald-500/30 bg-emerald-500/10 text-emerald-500"}`}>
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
        <div className="space-y-6">
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
                              : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
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
                              : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
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
                              : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
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
                    <Button variant="secondary" size="sm" onClick={handleBuildRepositoryUnderstanding} disabled={knowledgeLoading}>
                      {knowledgeLoading ? "Starting..." : currentUnderstanding ? "Refresh understanding" : "Build understanding"}
                    </Button>
                    {currentUnderstanding ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setUnderstandingCollapsed((current) => !current)}
                      >
                        {understandingCollapsed ? "Show details" : "Hide details"}
                      </Button>
                    ) : null}
                    {currentUnderstanding?.updatedAt ? (
                      <span className="text-xs text-[var(--text-tertiary)]">
                        Updated {formatGeneratedAt(currentUnderstanding.updatedAt)}
                      </span>
                    ) : null}
                  </div>
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
                        {" · "}
                        {currentUnderstanding.strategy || "hierarchical"}
                        {" · "}
                        {currentUnderstanding.modelUsed || "Unknown model"}
                        {understandingSections.length ? (
                          <>
                            {" · "}
                            {understandingSections.length} first-pass sections
                          </>
                        ) : null}
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
                          <p className="mt-1 text-sm font-medium text-[var(--text-primary)]">
                            {currentUnderstanding.strategy || "hierarchical"}
                          </p>
                        </div>
                        <div className="rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3">
                          <p className="text-[11px] uppercase tracking-[0.14em] text-[var(--text-tertiary)]">Model</p>
                          <p className="mt-1 truncate text-sm font-medium text-[var(--text-primary)]">
                            {currentUnderstanding.modelUsed || "Unknown"}
                          </p>
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
                            <p className="text-xs font-semibold uppercase tracking-[0.14em] text-[var(--text-tertiary)]">
                              Understanding Highlights
                            </p>
                            <span className="text-xs text-[var(--text-tertiary)]">
                              {understandingSections.length} sections
                            </span>
                          </div>
                          <div className="grid gap-3 lg:grid-cols-2">
                            {understandingFeaturedSections.map((section) => (
                              <div
                                key={section.title}
                                className="rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] p-3"
                              >
                                <p className="text-sm font-medium text-[var(--text-primary)]">{section.title}</p>
                                <p className="mt-1 text-sm text-[var(--text-secondary)]">{section.summary}</p>
                              </div>
                            ))}
                          </div>
                          {understandingAdditionalSections.length ? (
                            <div className="space-y-3">
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={() => setUnderstandingShowAllSections((current) => !current)}
                              >
                                {understandingShowAllSections
                                  ? `Hide ${understandingAdditionalSections.length} additional sections`
                                  : `Show ${understandingAdditionalSections.length} additional sections`}
                              </Button>
                              {understandingShowAllSections ? (
                                <div className="grid gap-3 lg:grid-cols-2">
                                  {understandingAdditionalSections.map((section) => (
                                    <div
                                      key={section.title}
                                      className="rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] p-3"
                                    >
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

                {/* Category 1: Field Guide */}
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
                                {isCliffNotesGenerating ? (
                                  <span className={artifactStatusClass}>{knowledgeQueueLabel(currentCliffNotes)}</span>
                                ) : null}
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
                                <p className="mt-1 text-xs text-[var(--text-tertiary)]">
                                  {artifactRefinementSummary(currentCliffNotes)}
                                </p>
                              ) : null}
                              {artifactDeepeningSummary(currentCliffNotes) ? (
                                <p className="mt-1 text-xs text-[var(--text-tertiary)]">
                                  {artifactDeepeningSummary(currentCliffNotes)}
                                </p>
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
                          {isCliffNotesGenerating ? (
                            renderKnowledgeProgress(currentCliffNotes, "Queued for generation", currentCliffNotesJob)
                          ) : null}
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
                                      {job.attached_requests > 1 ? ` · shared by ${job.attached_requests}` : ""}
                                    </span>
                                    <span>{new Date(job.updated_at).toLocaleTimeString()}</span>
                                  </div>
                                ))}
                              </div>
                            </div>
                          ) : null}
                          {currentCliffNotes.refinementUnits?.some((unit) => unit.refinementType === "cliff_notes_deep" && unit.status.toLowerCase() === "failed") ? (
                            <div className="mb-4 rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
                              <p className="text-sm font-medium text-[var(--text-primary)]">Some background deepening attempts failed.</p>
                              <div className="mt-2 space-y-1 text-xs text-[var(--text-secondary)]">
                                {currentCliffNotes.refinementUnits
                                  .filter((unit) => unit.refinementType === "cliff_notes_deep" && unit.status.toLowerCase() === "failed")
                                  .map((unit) => (
                                    <p key={unit.id}>
                                      {unit.sectionTitle}
                                      {unit.attemptCount > 0 ? ` · attempt ${unit.attemptCount}` : ""}
                                      {unit.lastError ? ` · ${unit.lastError}` : ""}
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
                        <Button variant="secondary" size="sm" onClick={() => setExecutionCompact((value) => !value)}>
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
                              <option key={entry.value} value={entry.value}>
                                {entry.label}
                              </option>
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
                                  <p className="text-xs uppercase tracking-[0.14em] text-[var(--text-tertiary)]">
                                    Step {step.orderIndex + 1} · {step.kind}
                                  </p>
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
                              {!executionCompact && step.reason ? (
                                <p className="mt-2 text-xs text-[var(--text-tertiary)]">{step.reason}</p>
                              ) : null}
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

                      {currentWorkflowStory && isWorkflowStoryGenerating ? (
                        renderKnowledgeProgress(currentWorkflowStory, "Queued for workflow generation", currentWorkflowStoryJob)
                      ) : null}

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
                              {currentLearningPath?.status === "PENDING"
                                ? "Queued..."
                                : isLearningPathGenerating
                                  ? "Generating..."
                                  : currentLearningPath
                                    ? artifactRetryLabel(currentLearningPath, currentLearningPathJob, "learning path")
                                    : "Generate learning path"}
                            </Button>
                          )}
                          {features.codeTours && (
                            <Button variant="secondary" size="sm" onClick={currentCodeTour ? () => handleRefreshArtifact(currentCodeTour.id) : handleGenerateCodeTour} disabled={knowledgeLoading || isCodeTourGenerating}>
                              {currentCodeTour?.status === "PENDING"
                                ? "Queued..."
                                : isCodeTourGenerating
                                  ? "Generating..."
                                  : currentCodeTour
                                    ? artifactRetryLabel(currentCodeTour, currentCodeTourJob, "code tour")
                                    : "Generate code tour"}
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
                            {currentLearningPath.status === "FAILED" ? (
                              <div className="mt-3">{renderKnowledgeFailure(currentLearningPath)}</div>
                            ) : null}
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
                            {isCodeTourGenerating ? (
                              <div className="mt-3">{renderKnowledgeProgress(currentCodeTour, "Queued for code tour generation", currentCodeTourJob)}</div>
                            ) : null}
                            {currentCodeTour.status === "FAILED" ? (
                              <div className="mt-3">{renderKnowledgeFailure(currentCodeTour)}</div>
                            ) : null}
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
                                      : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)]"
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
                /* Cloud / auth-required — three-step flow */
                <div className="mt-3 space-y-3">
                  {serverCaps.error && (
                    <p className="text-xs text-[var(--text-tertiary,var(--text-secondary))]">
                      Couldn&apos;t detect this server&apos;s auth configuration automatically — showing the hosted-instance flow. If you&apos;re on a local install, use{" "}
                      <code className="rounded bg-[var(--bg-subtle)] px-1 py-0.5 text-xs">sourcebridge setup claude --repo-id {repoId}</code> instead.
                    </p>
                  )}
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
                          {typeof window !== "undefined"
                            ? `sourcebridge setup claude --server ${window.location.protocol}//${window.location.host} --token <paste-here> --repo-id ${repoId}`
                            : `sourcebridge setup claude --server <your-server-url> --token <paste-here> --repo-id ${repoId}`}
                        </code>
                        <button
                          type="button"
                          onClick={() => {
                            const host = typeof window !== "undefined"
                              ? `${window.location.protocol}//${window.location.host}`
                              : "<your-server-url>";
                            void navigator.clipboard.writeText(
                              `sourcebridge setup claude --server ${host} --token <paste-here> --repo-id ${repoId}`
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
                </div>
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
              onClick={() => {
                if (confirm(`Remove "${repo?.name}"? This cannot be undone.`)) {
                  removeRepo({ id: repoId }).then(() => {
                    window.location.href = "/repositories";
                  });
                }
              }}
              className="bg-rose-600 text-white hover:bg-rose-700"
            >
              Remove Repository
            </Button>
          </div>
        </Panel>
      )}
    </PageFrame>
  );
}
