"use client";

/**
 * WikiSettingsPanel — per-repo Living Wiki configuration panel.
 *
 * Six visual states (per the v3 plan + ruby v1+v2 reviews):
 *
 *  State 0 — Global living-wiki disabled (or kill-switch active).
 *             Muted-border info row with link to /settings/living-wiki.
 *
 *  State 1 — Global enabled, repo not yet configured.
 *             Stage A activation gate: audience + sinks, minimal form,
 *             "Enable Living Wiki" CTA. Everything else defaults silently.
 *
 *  State 2 — enabled=true but sinks=[] (corrupt / partial state).
 *             Orange warning banner + Stage A form pre-populated with existing
 *             mode, CTA becomes "Save configuration".
 *
 *  State 3 — Cold-start job running.
 *             Progress bar driven by job record from activity feed.
 *             Form fields inaccessible while job runs.
 *
 *  State 4 — Enabled, last run succeeded.
 *             Read-only summary row + collapsible generated pages list +
 *             credential health pills + "Regenerate now" + Edit (inline) +
 *             Disable.
 *
 *  State 5 — Enabled, last run failed.
 *             Same as State 4 plus failure banner with category-specific CTA.
 */

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type RefObject,
} from "react";
import Link from "next/link";
import { useMutation, useQuery } from "urql";
import { authFetch } from "@/lib/auth-fetch";
import { cn } from "@/lib/utils";
import { Panel } from "@/components/ui/panel";
import { Button } from "@/components/ui/button";
import {
  LIVING_WIKI_GLOBAL_SETTINGS_QUERY,
  ENABLE_LIVING_WIKI_FOR_REPO_MUTATION,
  DISABLE_LIVING_WIKI_FOR_REPO_MUTATION,
  UPDATE_REPOSITORY_LIVING_WIKI_SETTINGS_MUTATION,
  RETRY_LIVING_WIKI_JOB_MUTATION,
} from "@/lib/graphql/queries";

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

export type RepoWikiSinkKind =
  | "GIT_REPO"
  | "CONFLUENCE"
  | "NOTION"
  | "GITHUB_WIKI"
  | "GITLAB_WIKI";
export type RepoWikiMode = "PR_REVIEW" | "DIRECT_PUBLISH";
export type RepoWikiAudience = "ENGINEER" | "PRODUCT" | "OPERATOR";
export type RepoWikiEditPolicy = "PROPOSE_PR" | "DIRECT_PUBLISH";
export type RepoStaleWhenStrategy = "DIRECT" | "TRANSITIVE";

export interface RepoWikiSink {
  kind: RepoWikiSinkKind;
  integrationName: string;
  audience: RepoWikiAudience;
  editPolicy: RepoWikiEditPolicy;
}

export interface LivingWikiJobResult {
  jobId: string;
  startedAt: string;
  completedAt?: string | null;
  pagesPlanned: number;
  pagesGenerated: number;
  pagesExcluded: number;
  excludedPageIds: string[];
  generatedPageTitles: string[];
  exclusionReasons: string[];
  status: string;
  failureCategory?: string | null;
  errorMessage?: string | null;
}

export interface RepositoryLivingWikiSettings {
  enabled: boolean;
  mode: RepoWikiMode;
  sinks: RepoWikiSink[];
  excludePaths: string[];
  staleWhenStrategy: RepoStaleWhenStrategy;
  maxPagesPerJob: number;
  lastRunAt?: string | null;
  updatedAt?: string | null;
  updatedBy?: string | null;
  lastJobResult?: LivingWikiJobResult | null;
}

interface GlobalSettings {
  enabled?: boolean | null;
  killSwitchActive?: boolean | null;
  confluenceToken?: string | null;
  notionToken?: string | null;
  githubToken?: string | null;
  gitlabToken?: string | null;
}

interface JobActivity {
  id: string;
  job_type: string;
  status: "pending" | "generating" | "ready" | "failed" | "cancelled";
  progress: number;
  progress_message?: string;
}

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

const SINK_LABELS: Record<RepoWikiSinkKind, string> = {
  GIT_REPO: "This repository (git)",
  CONFLUENCE: "Confluence",
  NOTION: "Notion",
  GITHUB_WIKI: "GitHub Wiki",
  GITLAB_WIKI: "GitLab Wiki",
};

const AUDIENCE_LABELS: Record<RepoWikiAudience, string> = {
  ENGINEER: "Engineer",
  PRODUCT: "Product",
  OPERATOR: "Operator",
};

const AUDIENCE_DESCRIPTIONS: Record<RepoWikiAudience, string> = {
  ENGINEER: "Technical architecture and API reference content.",
  PRODUCT: "Feature-level narrative and capability summaries.",
  OPERATOR: "Deployment, configuration, and runbook content.",
};

const POLL_MS = 3000;

// ─────────────────────────────────────────────────────────────────────────────
// Small presentational helpers
// ─────────────────────────────────────────────────────────────────────────────

function FieldLabel({
  label,
  help,
  htmlFor,
  required,
}: {
  label: string;
  help?: string;
  htmlFor?: string;
  required?: boolean;
}) {
  const id = htmlFor ? `${htmlFor}-desc` : undefined;
  return (
    <div className="mb-1.5">
      <label
        htmlFor={htmlFor}
        className="text-sm font-medium text-[var(--text-primary)]"
      >
        {label}
        {required && (
          <span className="ml-0.5 text-[var(--color-error,#ef4444)]">*</span>
        )}
      </label>
      {help && (
        <p
          id={id}
          className="mt-0.5 text-xs leading-relaxed text-[var(--text-tertiary)]"
        >
          {help}
        </p>
      )}
    </div>
  );
}

function Pill({
  label,
  variant,
}: {
  label: string;
  variant: "enabled" | "disabled" | "error" | "partial";
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium",
        variant === "enabled" &&
          "border-green-500/30 bg-green-500/10 text-green-400",
        variant === "disabled" &&
          "border-[var(--border-default)] bg-[var(--bg-surface)] text-[var(--text-tertiary)]",
        variant === "error" &&
          "border-[var(--color-error,#ef4444)]/30 bg-[var(--color-error,#ef4444)]/10 text-[var(--color-error,#ef4444)]",
        variant === "partial" &&
          "border-amber-500/30 bg-amber-500/10 text-amber-400"
      )}
    >
      {label}
    </span>
  );
}

function AlertBanner({
  variant,
  message,
  children,
}: {
  variant: "error" | "warning" | "info";
  message: string;
  children?: React.ReactNode;
}) {
  const colors = {
    error:
      "border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.08)] text-[var(--color-error,#ef4444)]",
    warning:
      "border-amber-500 bg-[rgba(245,158,11,0.08)] text-amber-400",
    info: "border-[var(--border-default)] bg-[var(--bg-surface)] text-[var(--text-secondary)]",
  };
  const icon = {
    error: "✕",
    warning: "⚠",
    info: "ℹ",
  };
  return (
    <div
      role="alert"
      aria-live="polite"
      className={cn(
        "flex items-start gap-3 rounded-[var(--radius-md)] border px-4 py-3 text-sm",
        colors[variant]
      )}
    >
      <span className="mt-0.5 shrink-0 font-bold" aria-hidden="true">
        {icon[variant]}
      </span>
      <div className="min-w-0 flex-1">
        <p>{message}</p>
        {children}
      </div>
    </div>
  );
}

function InlineSpinner() {
  return (
    <span
      aria-hidden="true"
      className="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
    />
  );
}

/** Skeleton block that mimics a form row height */
function SkeletonRow({ wide }: { wide?: boolean }) {
  return (
    <div
      className={cn(
        "h-10 animate-pulse rounded-[var(--control-radius)] bg-[var(--bg-surface)]",
        wide ? "w-full" : "w-48"
      )}
    />
  );
}

function SectionHeader({ title }: { title: string }) {
  return (
    <h3 className="text-lg font-semibold tracking-[-0.02em] text-[var(--text-primary)]">
      {title}
    </h3>
  );
}

function formatRelative(isoOrNull?: string | null): string {
  if (!isoOrNull) return "never";
  const diff = Date.now() - new Date(isoOrNull).getTime();
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} minutes ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} hours ago`;
  return `${Math.floor(diff / 86_400_000)} days ago`;
}

// ─────────────────────────────────────────────────────────────────────────────
// Audience row (used inside sink rows in Stage A + Stage B)
// ─────────────────────────────────────────────────────────────────────────────

function AudienceSelector({
  value,
  onChange,
  idPrefix,
  disabled,
}: {
  value: RepoWikiAudience;
  onChange: (v: RepoWikiAudience) => void;
  idPrefix: string;
  disabled?: boolean;
}) {
  const audiences: RepoWikiAudience[] = ["ENGINEER", "PRODUCT", "OPERATOR"];
  return (
    <div className="mt-2">
      <p className="mb-1 text-xs font-medium text-[var(--text-secondary)]">
        Audience for this sink
      </p>
      <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Audience">
        {audiences.map((a) => {
          const inputId = `${idPrefix}-audience-${a}`;
          const descId = `${idPrefix}-audience-${a}-desc`;
          return (
            <label key={a} className="flex cursor-pointer flex-col gap-0.5">
              <span className="flex items-center gap-1.5">
                <input
                  id={inputId}
                  type="radio"
                  name={`${idPrefix}-audience`}
                  value={a}
                  checked={value === a}
                  onChange={() => onChange(a)}
                  disabled={disabled}
                  aria-describedby={descId}
                  className="accent-[var(--accent-primary)]"
                />
                <span className="text-xs font-medium text-[var(--text-primary)]">
                  {AUDIENCE_LABELS[a]}
                </span>
              </span>
              <span
                id={descId}
                className="ml-5 text-[11px] leading-tight text-[var(--text-tertiary)]"
              >
                {AUDIENCE_DESCRIPTIONS[a]}
              </span>
            </label>
          );
        })}
      </div>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Sink row for Stage A (checkbox + expansion on check)
// ─────────────────────────────────────────────────────────────────────────────

interface SinkRowState {
  checked: boolean;
  integrationName: string;
  audience: RepoWikiAudience;
}

function SinkRow({
  kind,
  state,
  onChange,
  disabled,
  credsMissing,
  multiSinkTooltip,
}: {
  kind: RepoWikiSinkKind;
  state: SinkRowState;
  onChange: (s: SinkRowState) => void;
  disabled?: boolean;
  credsMissing?: boolean;
  multiSinkTooltip?: boolean;
}) {
  const checkboxId = `sink-${kind}-check`;
  const nameId = `sink-${kind}-name`;
  const isDisabledRow = credsMissing || disabled;
  const tooltip = credsMissing
    ? `${SINK_LABELS[kind]} credentials required — configure them in Settings → Living Wiki`
    : multiSinkTooltip
      ? "Multiple sinks coming soon — only one sink can be configured at this time"
      : undefined;

  return (
    <div
      className={cn(
        "rounded-[var(--radius-sm)] border border-[var(--border-subtle)] p-3 transition-colors",
        state.checked && !isDisabledRow && "border-[var(--accent-primary)]/30 bg-[var(--bg-surface)]",
        isDisabledRow && "opacity-50"
      )}
      title={tooltip}
    >
      <label className="flex cursor-pointer items-center gap-3">
        <input
          id={checkboxId}
          type="checkbox"
          checked={state.checked}
          disabled={isDisabledRow}
          onChange={(e) =>
            onChange({ ...state, checked: e.target.checked })
          }
          aria-describedby={tooltip ? `${checkboxId}-tip` : undefined}
          className="accent-[var(--accent-primary)]"
        />
        <span className="text-sm font-medium text-[var(--text-primary)]">
          {SINK_LABELS[kind]}
        </span>
        {credsMissing && (
          <span className="text-xs text-[var(--text-tertiary)]">
            (credentials not configured)
          </span>
        )}
        {multiSinkTooltip && !credsMissing && (
          <span className="text-xs text-[var(--text-tertiary)]">
            (coming soon)
          </span>
        )}
      </label>
      {tooltip && (
        <p id={`${checkboxId}-tip`} className="sr-only">
          {tooltip}
        </p>
      )}

      {state.checked && !isDisabledRow && (
        <div className="mt-3 space-y-3 border-t border-[var(--border-subtle)] pt-3">
          {kind !== "GIT_REPO" && (
            <div>
              <FieldLabel
                label="Integration name"
                htmlFor={nameId}
                help="A stable, human-readable label for this sink. Used to group pages across runs. Example: confluence-eng-docs"
                required
              />
              <input
                id={nameId}
                type="text"
                value={state.integrationName}
                onChange={(e) =>
                  onChange({ ...state, integrationName: e.target.value })
                }
                placeholder={`${SINK_LABELS[kind].toLowerCase().replace(/\s+/g, "-")}-docs`}
                disabled={disabled}
                aria-describedby={`${nameId}-desc`}
                className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 font-mono text-sm text-[var(--text-primary)] placeholder:font-sans placeholder:text-[var(--text-tertiary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)] disabled:opacity-60"
              />
            </div>
          )}
          <AudienceSelector
            value={state.audience}
            onChange={(a) => onChange({ ...state, audience: a })}
            idPrefix={`sink-${kind}`}
            disabled={disabled}
          />
        </div>
      )}
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Stage A form (activation gate)
// ─────────────────────────────────────────────────────────────────────────────

interface StageAFormProps {
  repoId: string;
  repoName: string;
  globalSettings: GlobalSettings;
  initial?: {
    mode: RepoWikiMode;
    sinks: RepoWikiSink[];
  };
  isCorrupt?: boolean;
  onSuccess: (result: {
    settings: RepositoryLivingWikiSettings;
    jobId: string | null;
    notice: string | null;
  }) => void;
}

function StageAForm({
  repoId,
  repoName,
  globalSettings,
  initial,
  isCorrupt,
  onSuccess,
}: StageAFormProps) {
  const [mode, setMode] = useState<RepoWikiMode>(initial?.mode ?? "PR_REVIEW");
  const enableButtonRef = useRef<HTMLButtonElement>(null);

  const hasConfluenceCreds = !!(
    globalSettings.confluenceToken &&
    globalSettings.confluenceToken !== ""
  );
  const hasNotionCreds = !!(
    globalSettings.notionToken && globalSettings.notionToken !== ""
  );
  const hasGitHubCreds = !!(
    globalSettings.githubToken && globalSettings.githubToken !== ""
  );
  const hasGitLabCreds = !!(
    globalSettings.gitlabToken && globalSettings.gitlabToken !== ""
  );

  const initialSinkChecked = (kind: RepoWikiSinkKind) =>
    initial?.sinks.some((s) => s.kind === kind) ?? kind === "GIT_REPO";
  const initialSinkIntegration = (kind: RepoWikiSinkKind) =>
    initial?.sinks.find((s) => s.kind === kind)?.integrationName ?? "";
  const initialSinkAudience = (kind: RepoWikiSinkKind) =>
    initial?.sinks.find((s) => s.kind === kind)?.audience ?? "ENGINEER";

  const [gitRepoSink, setGitRepoSink] = useState<SinkRowState>({
    checked: initialSinkChecked("GIT_REPO"),
    integrationName: "git-repo",
    audience: initialSinkAudience("GIT_REPO"),
  });
  const [confluenceSink, setConfluenceSink] = useState<SinkRowState>({
    checked: initialSinkChecked("CONFLUENCE"),
    integrationName: initialSinkIntegration("CONFLUENCE"),
    audience: initialSinkAudience("CONFLUENCE"),
  });
  const [notionSink, setNotionSink] = useState<SinkRowState>({
    checked: initialSinkChecked("NOTION"),
    integrationName: initialSinkIntegration("NOTION"),
    audience: initialSinkAudience("NOTION"),
  });
  const [githubWikiSink, setGithubWikiSink] = useState<SinkRowState>({
    checked: initialSinkChecked("GITHUB_WIKI"),
    integrationName: initialSinkIntegration("GITHUB_WIKI"),
    audience: initialSinkAudience("GITHUB_WIKI"),
  });
  const [gitlabWikiSink, setGitlabWikiSink] = useState<SinkRowState>({
    checked: initialSinkChecked("GITLAB_WIKI"),
    integrationName: initialSinkIntegration("GITLAB_WIKI"),
    audience: initialSinkAudience("GITLAB_WIKI"),
  });

  const checkedSinks = [
    gitRepoSink.checked ? "GIT_REPO" : null,
    confluenceSink.checked ? "CONFLUENCE" : null,
    notionSink.checked ? "NOTION" : null,
    githubWikiSink.checked ? "GITHUB_WIKI" : null,
    gitlabWikiSink.checked ? "GITLAB_WIKI" : null,
  ].filter(Boolean).length;

  const sinkState: Record<RepoWikiSinkKind, SinkRowState> = {
    GIT_REPO: gitRepoSink,
    CONFLUENCE: confluenceSink,
    NOTION: notionSink,
    GITHUB_WIKI: githubWikiSink,
    GITLAB_WIKI: gitlabWikiSink,
  };

  const atLeastOneSink = Object.values(sinkState).some((s) => s.checked);

  const [, enableMutation] = useMutation(ENABLE_LIVING_WIKI_FOR_REPO_MUTATION);
  const [enabling, setEnabling] = useState(false);
  const [enableError, setEnableError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const handleEnable = async () => {
    setEnabling(true);
    setEnableError(null);
    setNotice(null);

    const sinks = Object.entries(sinkState)
      .filter(([, s]) => s.checked)
      .map(([kind, s]) => ({
        kind: kind as RepoWikiSinkKind,
        integrationName:
          kind === "GIT_REPO"
            ? `${repoName.toLowerCase().replace(/[^a-z0-9]+/g, "-")}-git`
            : s.integrationName,
        audience: s.audience,
      }));

    const result = await enableMutation({
      input: { repositoryId: repoId, mode, sinks },
    });

    setEnabling(false);

    if (result.error) {
      setEnableError(result.error.message);
      return;
    }

    const data = result.data?.enableLivingWikiForRepo;
    if (!data) {
      setEnableError("Unexpected empty response from server.");
      return;
    }

    if (data.notice && !data.jobId) {
      setNotice(data.notice);
      return;
    }

    onSuccess({
      settings: data.settings,
      jobId: data.jobId ?? null,
      notice: data.notice ?? null,
    });
  };

  return (
    <div className="space-y-6">
      {isCorrupt && (
        <AlertBanner
          variant="warning"
          message="Living Wiki is enabled but no sinks are configured. Please re-configure below."
        />
      )}

      {notice && (
        <div
          role="status"
          className="rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-3 text-sm text-[var(--text-secondary)]"
        >
          {notice}
        </div>
      )}

      {/* Mode selector */}
      <div>
        <FieldLabel
          label="Publish mode"
          help="How generated wiki pages reach your sink."
        />
        <div className="mt-2 flex flex-wrap gap-2">
          {(
            [
              {
                key: "PR_REVIEW" as RepoWikiMode,
                label: "PR Review",
                desc: "Generated wiki pages are proposed as a pull request. Your team reviews and merges.",
              },
              {
                key: "DIRECT_PUBLISH" as RepoWikiMode,
                label: "Direct Publish",
                desc: "Generated wiki pages are written directly to the configured sink without a review step.",
              },
            ] as const
          ).map((opt) => (
            <button
              key={opt.key}
              type="button"
              onClick={() => setMode(opt.key)}
              disabled={enabling}
              aria-pressed={mode === opt.key}
              className={cn(
                "rounded-full border px-3 py-1.5 text-xs font-medium transition-colors",
                mode === opt.key
                  ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                  : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
              )}
            >
              {opt.label}
            </button>
          ))}
        </div>
        <p className="mt-2 text-xs text-[var(--text-tertiary)]">
          {mode === "PR_REVIEW"
            ? "Generated wiki pages are proposed as a pull request. Your team reviews and merges."
            : "Generated wiki pages are written directly to the configured sink without a review step."}
        </p>
      </div>

      {/* Sink list */}
      <div>
        <FieldLabel
          label="Destination sinks"
          help="Where SourceBridge will publish generated wiki pages."
          required
        />
        <div className="mt-2 space-y-2">
          <SinkRow
            kind="GIT_REPO"
            state={gitRepoSink}
            onChange={(s) => {
              if (!s.checked && !Object.entries(sinkState).some(([k, v]) => k !== "GIT_REPO" && v.checked)) {
                return;
              }
              setGitRepoSink(s);
            }}
            disabled={enabling}
            multiSinkTooltip={!gitRepoSink.checked && checkedSinks >= 1}
          />
          <SinkRow
            kind="CONFLUENCE"
            state={confluenceSink}
            onChange={setConfluenceSink}
            disabled={enabling}
            credsMissing={!hasConfluenceCreds}
            multiSinkTooltip={!confluenceSink.checked && checkedSinks >= 1 && hasConfluenceCreds}
          />
          <SinkRow
            kind="NOTION"
            state={notionSink}
            onChange={setNotionSink}
            disabled={enabling}
            credsMissing={!hasNotionCreds}
            multiSinkTooltip={!notionSink.checked && checkedSinks >= 1 && hasNotionCreds}
          />
          {hasGitHubCreds && (
            <SinkRow
              kind="GITHUB_WIKI"
              state={githubWikiSink}
              onChange={setGithubWikiSink}
              disabled={enabling}
              multiSinkTooltip={!githubWikiSink.checked && checkedSinks >= 1}
            />
          )}
          {hasGitLabCreds && (
            <SinkRow
              kind="GITLAB_WIKI"
              state={gitlabWikiSink}
              onChange={setGitlabWikiSink}
              disabled={enabling}
              multiSinkTooltip={!gitlabWikiSink.checked && checkedSinks >= 1}
            />
          )}
        </div>
      </div>

      {/* Page estimate notice */}
      <p className="text-xs text-[var(--text-tertiary)]">
        Based on this repository&apos;s structure, SourceBridge will generate
        wiki pages for each indexed package and module. The exact count depends
        on your symbol graph depth.
      </p>

      {enableError && (
        <AlertBanner variant="error" message={enableError} />
      )}

      <div className="flex items-center gap-3">
        <button
          ref={enableButtonRef}
          type="button"
          onClick={() => void handleEnable()}
          disabled={enabling || !atLeastOneSink}
          className="inline-flex h-11 items-center justify-center gap-2 rounded-[var(--control-radius)] border border-transparent bg-[var(--accent-primary)] px-4 py-2.5 text-sm font-medium text-[var(--accent-contrast)] transition-colors hover:bg-[var(--accent-primary-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--bg-base)] disabled:pointer-events-none disabled:opacity-60"
        >
          {enabling ? (
            <>
              <InlineSpinner />
              Enabling&hellip;
            </>
          ) : isCorrupt ? (
            "Save configuration"
          ) : (
            "Enable Living Wiki"
          )}
        </button>
      </div>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Cold-start progress (State 3)
// ─────────────────────────────────────────────────────────────────────────────

function ColdStartProgress({
  repoId,
  repoName,
  jobId,
  onComplete,
  progressRef,
}: {
  repoId: string;
  repoName: string;
  jobId: string;
  onComplete: () => void;
  progressRef?: RefObject<HTMLDivElement | null>;
}) {
  const [progress, setProgress] = useState(0);
  const [message, setMessage] = useState("Preparing to generate wiki pages…");
  const [pagesGenerated, setPagesGenerated] = useState(0);
  const [pagesPlanned, setPagesPlanned] = useState(0);
  const [warnings, setWarnings] = useState(0);
  const pollRef = useRef<number | null>(null);

  const poll = useCallback(async () => {
    try {
      const res = await authFetch(
        `/api/v1/admin/llm/activity?repo_id=${encodeURIComponent(repoId)}&limit=20`
      );
      if (!res.ok) return;
      const body = (await res.json()) as {
        active?: JobActivity[];
        recent?: JobActivity[];
      };
      const all = [...(body.active ?? []), ...(body.recent ?? [])];
      const job = all.find(
        (j) =>
          j.id === jobId || j.job_type === "living_wiki_cold_start"
      );
      if (!job) return;

      setProgress(job.progress ?? 0);
      if (job.progress_message) setMessage(job.progress_message);

      const msgMatch = job.progress_message?.match(/(\d+)\/(\d+)/);
      if (msgMatch) {
        setPagesGenerated(parseInt(msgMatch[1], 10));
        setPagesPlanned(parseInt(msgMatch[2], 10));
      }

      const warnMatch = job.progress_message?.match(/\((\d+) warning/);
      if (warnMatch) setWarnings(parseInt(warnMatch[1], 10));

      if (job.status === "ready" || job.status === "failed" || job.status === "cancelled") {
        if (pollRef.current) {
          window.clearInterval(pollRef.current);
          pollRef.current = null;
        }
        onComplete();
      }
    } catch {
      // Non-fatal — next poll will retry.
    }
  }, [repoId, jobId, onComplete]);

  useEffect(() => {
    void poll();
    pollRef.current = window.setInterval(() => {
      void poll();
    }, POLL_MS);
    return () => {
      if (pollRef.current) {
        window.clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [poll]);

  const pct = Math.max(5, Math.round(progress * 100));
  const progressLabel =
    pagesPlanned > 0
      ? `Generated ${pagesGenerated}/${pagesPlanned} pages${warnings > 0 ? ` (${warnings} warning${warnings > 1 ? "s" : ""})` : ""}`
      : message;

  return (
    <div ref={progressRef} className="space-y-3" tabIndex={-1}>
      <p className="text-sm font-medium text-[var(--text-primary)]">
        Generating wiki for {repoName}&hellip;
      </p>
      <div
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label="Wiki generation progress"
        className="h-2 overflow-hidden rounded-full bg-[var(--bg-hover)]"
      >
        <div
          className="h-full rounded-full bg-[var(--accent-primary)] transition-all duration-500"
          style={{ width: `${pct}%` }}
        />
      </div>
      <p
        aria-live="polite"
        aria-atomic="true"
        className="text-xs text-[var(--text-secondary)]"
      >
        {progressLabel}
      </p>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Failure banners for States 3/4/5
// ─────────────────────────────────────────────────────────────────────────────

/**
 * Translate a (failureCategory, errorMessage) pair into plain-language guidance.
 * The backend produces precise but cryptic messages
 * ("orchestrator: time budget exceeded", "Job timed out — no progress for the
 * stale-job threshold. Retry to resume.") and asking the user to interpret
 * those is unkind. The user just wants to know what happened, what it means
 * for their work, and what to do next.
 *
 * Returns:
 *   title  – one-line headline that sets the right tone
 *   detail – one to three sentences explaining the situation, including the
 *            raw error if it might help a developer file a bug
 *   action – what the user should do (rendered next to the retry button)
 */
function humanizeFailure(result: LivingWikiJobResult): {
  title: string;
  detail: string;
  action: string;
} {
  const raw = result.errorMessage ?? "";
  const category = result.failureCategory ?? "";

  // Time-budget exhaustion — the orchestrator hit its wall-clock cap.
  if (raw.includes("time budget exceeded")) {
    return {
      title: "Generation ran out of time",
      detail: `This run finished ${result.pagesGenerated} of ${result.pagesPlanned} pages before hitting the 60-minute time budget. Pages already in your wiki are kept; smart resume will skip them on the next attempt and only generate what's missing.`,
      action: "Retry to continue from where this left off.",
    };
  }

  // Stale-reaper killed the job (no progress in the threshold window).
  if (raw.toLowerCase().includes("no progress") || raw.toLowerCase().includes("timed out")) {
    return {
      title: "Generation went silent",
      detail: `Generation stopped reporting progress for too long and the system gave up to free its slot. ${result.pagesGenerated} pages were saved before that happened. Smart resume will skip them on retry.`,
      action: "Retry to resume.",
    };
  }

  // Authentication failure — credentials are wrong or expired.
  if (category === "auth") {
    return {
      title: "Authentication failed",
      detail:
        raw ||
        "A configured sink rejected our credentials. Check the Living Wiki settings page to confirm the token and email are still valid.",
      action: "Fix credentials in Settings → Living Wiki, then retry.",
    };
  }

  // Worker / network problem — the LLM gRPC call couldn't reach the worker.
  if (
    raw.includes("connection refused") ||
    raw.includes("rpc error") ||
    raw.includes("Unavailable")
  ) {
    return {
      title: "Lost connection to the AI worker",
      detail: `The API couldn't reach the worker that runs LLM calls. Often this means the worker pod restarted while a job was in flight. ${result.pagesGenerated} pages had already been saved; smart resume will skip them on retry.`,
      action: "Wait a few seconds for the worker to come back up, then retry.",
    };
  }

  // Page-level template error surfaced via the orchestrator.
  if (raw.startsWith("page \"") || raw.includes("template generate")) {
    return {
      title: "A page failed to generate",
      detail: `One page failed and the run stopped before completing. Detail: ${raw}.`,
      action: "Retry to try again — smart resume keeps the pages that already succeeded.",
    };
  }

  // Partial-content (some pages excluded by quality gates) is handled below
  // with its own banner, so this branch only fires for genuinely unknown
  // failures. Surface the raw message rather than hiding it — it's the only
  // breadcrumb the user (or we) have to file a bug.
  return {
    title: "Wiki generation failed",
    detail: raw || "An unexpected error stopped generation.",
    action: "Retry — if it keeps failing, capture this message and let us know.",
  };
}

/**
 * Group exclusion-reason strings of the shape "page-id: reason" by page ID
 * so the UI can render a concise list with all the violations for each page
 * collapsed under one heading. The cold-start runner emits one entry per
 * gate violation, so a single page with multiple failed gates currently
 * appears as multiple top-level entries.
 */
function groupExclusionReasons(
  reasons: string[],
): { pageID: string; messages: string[] }[] {
  const map = new Map<string, string[]>();
  const order: string[] = [];
  for (const r of reasons) {
    const idx = r.indexOf(": ");
    const pageID = idx > 0 ? r.slice(0, idx) : "";
    const msg = idx > 0 ? r.slice(idx + 2) : r;
    if (!map.has(pageID)) {
      map.set(pageID, []);
      order.push(pageID);
    }
    map.get(pageID)!.push(msg);
  }
  return order.map((id) => ({ pageID: id, messages: map.get(id) ?? [] }));
}

function FailureBanner({
  result,
  onRetry,
  onRetryExcluded,
}: {
  result: LivingWikiJobResult;
  onRetry: () => void;
  onRetryExcluded: () => void;
}) {
  const [exclusionsOpen, setExclusionsOpen] = useState(false);
  const category = result.failureCategory;

  // Partial-content runs (status="partial" or pagesExcluded > 0) get a
  // dedicated banner because the natural follow-up is "retry excluded only"
  // rather than "retry from scratch".
  if (category === "partial_content" || result.pagesExcluded > 0) {
    const grouped = groupExclusionReasons(result.exclusionReasons);
    return (
      <AlertBanner
        variant="warning"
        message={`${result.pagesGenerated} pages generated, ${result.pagesExcluded} pages excluded.`}
      >
        <div className="mt-2 space-y-2">
          <p className="text-xs text-[var(--text-secondary)]">
            Pages get excluded when generated content fails our quality gates twice in a row. Retry — the system will only re-run the excluded ones.
          </p>
          {grouped.length > 0 && (
            <button
              type="button"
              onClick={() => setExclusionsOpen((o) => !o)}
              className="text-xs font-medium underline underline-offset-2"
            >
              {exclusionsOpen ? "Hide" : "Show"} why {result.pagesExcluded} page
              {result.pagesExcluded !== 1 ? "s were" : " was"} excluded
            </button>
          )}
          {exclusionsOpen && grouped.length > 0 && (
            <ul className="mt-1 space-y-2 text-xs">
              {grouped.map(({ pageID, messages }) => (
                <li key={pageID} className="flex flex-col gap-0.5">
                  <span className="font-mono text-[var(--text-primary)]">
                    {pageID || "(unknown page)"}
                  </span>
                  <ul className="ml-3 space-y-0.5 text-[var(--text-secondary)]">
                    {messages.map((m, j) => (
                      <li key={j} className="flex gap-1">
                        <span aria-hidden="true">–</span>
                        <span>{m}</span>
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          )}
          <div>
            <button
              type="button"
              onClick={onRetryExcluded}
              className="text-xs font-medium underline underline-offset-2"
            >
              Retry excluded pages only
            </button>
          </div>
        </div>
      </AlertBanner>
    );
  }

  // Everything else — transient, auth, raw failures, etc. — flows through
  // the humanizer so the user sees plain-language guidance instead of the
  // backend's diagnostic string. Auth banner additionally surfaces the
  // settings link the user actually needs.
  if (result.status === "failed" || (category && category !== "")) {
    const { title, detail, action } = humanizeFailure(result);
    const variant = category === "auth" ? "error" : "warning";
    return (
      <AlertBanner variant={variant} message={title}>
        <div className="mt-2 space-y-2">
          <p className="text-xs text-[var(--text-secondary)]">{detail}</p>
          <div className="flex flex-wrap items-center gap-3 text-xs font-medium">
            <button
              type="button"
              onClick={onRetry}
              className="underline underline-offset-2"
            >
              {action}
            </button>
            {category === "auth" && (
              <Link
                href="/settings/living-wiki"
                className="underline underline-offset-2"
              >
                Open Living Wiki settings
              </Link>
            )}
          </div>
        </div>
      </AlertBanner>
    );
  }

  return null;
}

// ─────────────────────────────────────────────────────────────────────────────
// Stage B form (full refinement)
// ─────────────────────────────────────────────────────────────────────────────

interface StageBFormProps {
  repoId: string;
  repoName: string;
  settings: RepositoryLivingWikiSettings;
  globalSettings: GlobalSettings;
  onSaved: (s: RepositoryLivingWikiSettings) => void;
  onCancel: () => void;
  firstFieldRef: RefObject<HTMLButtonElement | null>;
}

function StageBForm({
  repoId,
  repoName: _repoName,
  settings,
  globalSettings,
  onSaved,
  onCancel,
  firstFieldRef,
}: StageBFormProps) {
  const [mode, setMode] = useState<RepoWikiMode>(settings.mode);
  const [excludePaths, setExcludePaths] = useState(
    settings.excludePaths.join("\n")
  );
  const [staleStrategy, setStaleStrategy] = useState<RepoStaleWhenStrategy>(
    settings.staleWhenStrategy
  );
  const [maxPages, setMaxPages] = useState(settings.maxPagesPerJob);

  const hasConfluenceCreds = !!(
    globalSettings.confluenceToken && globalSettings.confluenceToken !== ""
  );
  const hasNotionCreds = !!(
    globalSettings.notionToken && globalSettings.notionToken !== ""
  );

  // Sync sink states from existing settings
  const getInitialSink = (kind: RepoWikiSinkKind): SinkRowState => {
    const existing = settings.sinks.find((s) => s.kind === kind);
    return {
      checked: !!existing,
      integrationName: existing?.integrationName ?? "",
      audience: existing?.audience ?? "ENGINEER",
    };
  };

  const [gitRepoSink, setGitRepoSink] = useState(() => getInitialSink("GIT_REPO"));
  const [confluenceSink, setConfluenceSink] = useState(() => getInitialSink("CONFLUENCE"));
  const [notionSink, setNotionSink] = useState(() => getInitialSink("NOTION"));
  const [githubWikiSink, setGithubWikiSink] = useState(() => getInitialSink("GITHUB_WIKI"));
  const [gitlabWikiSink, setGitlabWikiSink] = useState(() => getInitialSink("GITLAB_WIKI"));

  const [, updateMutation] = useMutation(UPDATE_REPOSITORY_LIVING_WIKI_SETTINGS_MUTATION);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const handleSave = async () => {
    setSaving(true);
    setSaveError(null);

    const sinkStates: Record<RepoWikiSinkKind, SinkRowState> = {
      GIT_REPO: gitRepoSink,
      CONFLUENCE: confluenceSink,
      NOTION: notionSink,
      GITHUB_WIKI: githubWikiSink,
      GITLAB_WIKI: gitlabWikiSink,
    };

    const sinks = Object.entries(sinkStates)
      .filter(([, s]) => s.checked)
      .map(([kind, s]) => ({
        kind: kind as RepoWikiSinkKind,
        integrationName: s.integrationName,
        audience: s.audience,
      }));

    const result = await updateMutation({
      input: {
        repositoryId: repoId,
        mode,
        sinks,
        excludePaths: excludePaths
          .split("\n")
          .map((p) => p.trim())
          .filter(Boolean),
        staleWhenStrategy: staleStrategy,
        maxPagesPerJob: maxPages,
      },
    });

    setSaving(false);

    if (result.error) {
      setSaveError(result.error.message);
      return;
    }

    if (result.data?.updateRepositoryLivingWikiSettings) {
      onSaved(result.data.updateRepositoryLivingWikiSettings as RepositoryLivingWikiSettings);
    }
  };

  return (
    <div className="space-y-6">
      {saveError && <AlertBanner variant="error" message={saveError} />}

      {/* Mode */}
      <div>
        <FieldLabel label="Publish mode" />
        <div className="mt-2 flex flex-wrap gap-2">
          {(["PR_REVIEW", "DIRECT_PUBLISH"] as RepoWikiMode[]).map((m) => (
            <button
              key={m}
              type="button"
              ref={m === "PR_REVIEW" ? firstFieldRef : undefined}
              onClick={() => setMode(m)}
              disabled={saving}
              aria-pressed={mode === m}
              className={cn(
                "rounded-full border px-3 py-1.5 text-xs font-medium transition-colors",
                mode === m
                  ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                  : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
              )}
            >
              {m === "PR_REVIEW" ? "PR Review" : "Direct Publish"}
            </button>
          ))}
        </div>
        <p className="mt-2 text-xs text-[var(--text-tertiary)]">
          {mode === "PR_REVIEW"
            ? "Generated wiki pages are proposed as a pull request. Your team reviews and merges."
            : "Generated wiki pages are written directly to the configured sink without a review step."}
        </p>
      </div>

      {/* Sinks */}
      <div>
        <FieldLabel label="Sinks" />
        <div className="mt-2 space-y-2">
          <SinkRow kind="GIT_REPO" state={gitRepoSink} onChange={setGitRepoSink} disabled={saving} />
          <SinkRow
            kind="CONFLUENCE"
            state={confluenceSink}
            onChange={setConfluenceSink}
            disabled={saving}
            credsMissing={!hasConfluenceCreds}
          />
          <SinkRow
            kind="NOTION"
            state={notionSink}
            onChange={setNotionSink}
            disabled={saving}
            credsMissing={!hasNotionCreds}
          />
          <SinkRow kind="GITHUB_WIKI" state={githubWikiSink} onChange={setGithubWikiSink} disabled={saving} />
          <SinkRow kind="GITLAB_WIKI" state={gitlabWikiSink} onChange={setGitlabWikiSink} disabled={saving} />
        </div>
      </div>

      {/* Exclude paths */}
      <div>
        <FieldLabel
          label="Exclude paths"
          htmlFor="exclude-paths"
          help="Glob patterns (one per line) to exclude from wiki generation. Example: vendor/**, testdata/**"
        />
        <textarea
          id="exclude-paths"
          value={excludePaths}
          onChange={(e) => setExcludePaths(e.target.value)}
          disabled={saving}
          rows={4}
          aria-describedby="exclude-paths-desc"
          placeholder="vendor/**&#10;testdata/**"
          className="mt-1 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 font-mono text-sm text-[var(--text-primary)] placeholder:font-sans placeholder:text-[var(--text-tertiary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)] disabled:opacity-60"
        />
      </div>

      {/* Stale strategy */}
      <div>
        <FieldLabel
          label="Stale detection"
          help="DIRECT: only files directly referenced by the page. TRANSITIVE: all transitive dependencies."
        />
        <div className="mt-2 flex flex-wrap gap-2">
          {(["DIRECT", "TRANSITIVE"] as RepoStaleWhenStrategy[]).map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => setStaleStrategy(s)}
              disabled={saving}
              aria-pressed={staleStrategy === s}
              className={cn(
                "rounded-full border px-3 py-1.5 text-xs font-medium transition-colors",
                staleStrategy === s
                  ? "border-[var(--accent-primary)] bg-[var(--accent-primary)] text-[var(--accent-contrast)]"
                  : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
              )}
            >
              {s === "DIRECT" ? "Direct" : "Transitive"}
            </button>
          ))}
        </div>
      </div>

      {/* Max pages per job */}
      <div>
        <FieldLabel
          label="Max pages per job"
          htmlFor="max-pages"
          help="Caps page generation per scheduler run. Prevents runaway regen on large repos. Default 50."
        />
        <input
          id="max-pages"
          type="number"
          min={1}
          max={500}
          value={maxPages}
          onChange={(e) => setMaxPages(Number(e.target.value))}
          disabled={saving}
          className="mt-1 h-11 w-32 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)] disabled:opacity-60"
        />
      </div>

      <div className="flex items-center gap-3 border-t border-[var(--border-subtle)] pt-4">
        <Button onClick={() => void handleSave()} disabled={saving}>
          {saving ? (
            <>
              <InlineSpinner />
              Saving&hellip;
            </>
          ) : (
            "Save changes"
          )}
        </Button>
        <button
          type="button"
          onClick={onCancel}
          disabled={saving}
          className="text-sm text-[var(--text-secondary)] hover:text-[var(--text-primary)] disabled:opacity-60"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Disable dialog
// ─────────────────────────────────────────────────────────────────────────────

function DisableDialog({
  repoId,
  onDisabled,
  onClose,
}: {
  repoId: string;
  onDisabled: (s: RepositoryLivingWikiSettings) => void;
  onClose: () => void;
}) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const [, disableMutation] = useMutation(DISABLE_LIVING_WIKI_FOR_REPO_MUTATION);
  const [disabling, setDisabling] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Focus cancel on mount
  useEffect(() => {
    cancelRef.current?.focus();
  }, []);

  // Trap focus + Escape
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
        return;
      }
      if (e.key !== "Tab") return;
      const dialog = dialogRef.current;
      if (!dialog) return;
      const focusable = Array.from(
        dialog.querySelectorAll<HTMLElement>(
          'button:not([disabled]), [href], input:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
        )
      );
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey) {
        if (document.activeElement === first) {
          e.preventDefault();
          last.focus();
        }
      } else {
        if (document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  const handleDisable = async () => {
    setDisabling(true);
    setError(null);
    const result = await disableMutation({ repositoryId: repoId });
    setDisabling(false);
    if (result.error) {
      setError(result.error.message);
      return;
    }
    if (result.data?.disableLivingWikiForRepo) {
      onDisabled(result.data.disableLivingWikiForRepo as RepositoryLivingWikiSettings);
    }
  };

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="disable-dialog-title"
      aria-describedby="disable-dialog-body"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        className="w-full max-w-md rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg)] p-6 shadow-xl"
      >
        <h2
          id="disable-dialog-title"
          className="text-base font-semibold text-[var(--text-primary)]"
        >
          Disable Living Wiki for this repository?
        </h2>
        <p
          id="disable-dialog-body"
          className="mt-3 text-sm leading-relaxed text-[var(--text-secondary)]"
        >
          Living Wiki will stop updating this repository&apos;s pages. Existing
          pages in Confluence will remain and can be edited manually. A banner
          will be added to each page noting that it is no longer auto-managed.
        </p>

        {error && (
          <div className="mt-4">
            <AlertBanner variant="error" message={error} />
          </div>
        )}

        <div className="mt-6 flex flex-col gap-2 sm:flex-row-reverse">
          <Button
            onClick={() => void handleDisable()}
            disabled={disabling}
            className="bg-rose-600 text-white hover:bg-rose-700"
          >
            {disabling ? (
              <>
                <InlineSpinner />
                Disabling&hellip;
              </>
            ) : (
              "Disable Living Wiki"
            )}
          </Button>
          <button
            ref={cancelRef}
            type="button"
            onClick={onClose}
            disabled={disabling}
            className="inline-flex h-11 items-center justify-center gap-2 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-2.5 text-sm font-medium text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--bg-base)] disabled:pointer-events-none disabled:opacity-60"
          >
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// State 4/5 — enabled idle or failed (summary view)
// ─────────────────────────────────────────────────────────────────────────────

interface EnabledSummaryProps {
  repoId: string;
  repoName: string;
  settings: RepositoryLivingWikiSettings;
  globalSettings: GlobalSettings;
  onSettingsUpdated: (s: RepositoryLivingWikiSettings) => void;
  onDisabled: (s: RepositoryLivingWikiSettings) => void;
  summaryRef: RefObject<HTMLDivElement | null>;
}

function EnabledSummary({
  repoId,
  repoName,
  settings,
  globalSettings,
  onSettingsUpdated,
  onDisabled,
  summaryRef,
}: EnabledSummaryProps) {
  const [editMode, setEditMode] = useState(false);
  const [showDisableDialog, setShowDisableDialog] = useState(false);
  const [pagesOpen, setPagesOpen] = useState(false);
  const [regenerating, setRegenerating] = useState(false);
  const [regenJobId, setRegenJobId] = useState<string | null>(null);
  const [regenError, setRegenError] = useState<string | null>(null);
  const firstFieldRef = useRef<HTMLButtonElement>(null);
  const editButtonRef = useRef<HTMLButtonElement>(null);

  const [, retryMutation] = useMutation(RETRY_LIVING_WIKI_JOB_MUTATION);
  const [, enableMutation] = useMutation(ENABLE_LIVING_WIKI_FOR_REPO_MUTATION);

  const jobResult = settings.lastJobResult;
  const jobStatus = jobResult?.status;
  const hasFailed =
    jobStatus === "failed" || (jobResult?.failureCategory != null && jobResult.failureCategory !== "");
  const isPartial = jobStatus === "partial" || (jobResult?.pagesExcluded ?? 0) > 0;

  const pillVariant = hasFailed ? "error" : isPartial ? "partial" : "enabled";
  const pillLabel = hasFailed ? "Error" : isPartial ? "Partial" : "Enabled";

  const handleEditClick = () => {
    setEditMode(true);
    // Focus first field after state update
    setTimeout(() => firstFieldRef.current?.focus(), 50);
  };

  const handleCancelEdit = () => {
    setEditMode(false);
    setTimeout(() => editButtonRef.current?.focus(), 50);
  };

  const handleRegenerate = async () => {
    setRegenerating(true);
    setRegenError(null);
    const result = await retryMutation({
      repositoryId: repoId,
      retryExcludedOnly: false,
    });
    if (result.error) {
      setRegenError(result.error.message);
      setRegenerating(false);
      return;
    }
    const jobId = result.data?.retryLivingWikiJob?.jobId;
    if (jobId) {
      setRegenJobId(jobId);
    } else {
      setRegenerating(false);
    }
  };

  const handleRetry = async () => {
    const result = await enableMutation({
      input: {
        repositoryId: repoId,
        mode: settings.mode,
        sinks: settings.sinks.map((s) => ({
          kind: s.kind,
          integrationName: s.integrationName,
          audience: s.audience,
        })),
      },
    });
    if (result.data?.enableLivingWikiForRepo?.jobId) {
      setRegenJobId(result.data.enableLivingWikiForRepo.jobId);
      setRegenerating(true);
    }
  };

  const handleRetryExcluded = async () => {
    const result = await retryMutation({
      repositoryId: repoId,
      retryExcludedOnly: true,
    });
    if (result.data?.retryLivingWikiJob?.jobId) {
      setRegenJobId(result.data.retryLivingWikiJob.jobId);
      setRegenerating(true);
    }
  };

  const handleRegenComplete = () => {
    setRegenerating(false);
    setRegenJobId(null);
    // Caller should re-fetch settings; for now just clear the state.
    onSettingsUpdated(settings);
  };

  const sinkSummary = settings.sinks
    .map((s) => `${SINK_LABELS[s.kind]} (${AUDIENCE_LABELS[s.audience]})`)
    .join(", ");

  return (
    <div ref={summaryRef} className="space-y-6" tabIndex={-1}>
      {editMode ? (
        <StageBForm
          repoId={repoId}
          repoName={repoName}
          settings={settings}
          globalSettings={globalSettings}
          firstFieldRef={firstFieldRef}
          onSaved={(s) => {
            setEditMode(false);
            onSettingsUpdated(s);
            setTimeout(() => editButtonRef.current?.focus(), 50);
          }}
          onCancel={handleCancelEdit}
        />
      ) : (
        <>
          {/* Header row */}
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex items-center gap-2">
              <Pill label={pillLabel} variant={pillVariant} />
              <span className="text-sm text-[var(--text-secondary)]">
                {sinkSummary || "No sinks configured"}
              </span>
            </div>
            <div className="flex items-center gap-2">
              <button
                ref={editButtonRef}
                type="button"
                onClick={handleEditClick}
                className="text-sm font-medium text-[var(--accent-primary)] hover:underline"
              >
                Edit
              </button>
            </div>
          </div>

          {/* Last run */}
          <p className="text-xs text-[var(--text-tertiary)]">
            Mode: {settings.mode === "PR_REVIEW" ? "PR Review" : "Direct Publish"}
            {" · "}
            Last run: {formatRelative(settings.lastRunAt)}
          </p>

          {/* Failure banner */}
          {hasFailed && jobResult && (
            <FailureBanner
              result={jobResult}
              onRetry={() => void handleRetry()}
              onRetryExcluded={() => void handleRetryExcluded()}
            />
          )}

          {/* Regen progress */}
          {regenerating && regenJobId && (
            <ColdStartProgress
              repoId={repoId}
              repoName={repoName}
              jobId={regenJobId}
              onComplete={handleRegenComplete}
            />
          )}

          {regenError && <AlertBanner variant="error" message={regenError} />}

          {/* Generated pages collapsible */}
          {jobResult && jobResult.generatedPageTitles.length > 0 && (
            <div className="border-t border-[var(--border-subtle)] pt-4">
              <button
                type="button"
                onClick={() => setPagesOpen((o) => !o)}
                className="flex w-full items-center justify-between text-sm font-medium text-[var(--text-primary)] hover:text-[var(--accent-primary)]"
              >
                <span>
                  Generated pages ({jobResult.generatedPageTitles.length})
                </span>
                <span className="text-xs text-[var(--text-secondary)]">
                  {pagesOpen ? "collapse" : "expand"}
                </span>
              </button>
              {pagesOpen && (
                <ul className="mt-3 space-y-1">
                  {jobResult.generatedPageTitles.map((title, i) => (
                    <li
                      key={i}
                      className="text-sm text-[var(--text-secondary)]"
                    >
                      {title}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}

          {/* Actions */}
          <div className="flex flex-wrap items-center gap-3 border-t border-[var(--border-subtle)] pt-4">
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void handleRegenerate()}
              disabled={regenerating}
            >
              {regenerating ? (
                <>
                  <InlineSpinner />
                  Regenerating&hellip;
                </>
              ) : (
                "Regenerate now"
              )}
            </Button>
            <button
              type="button"
              onClick={() => setShowDisableDialog(true)}
              className="text-sm text-[var(--text-tertiary)] hover:text-[var(--color-error,#ef4444)]"
            >
              Disable
            </button>
          </div>
        </>
      )}

      {showDisableDialog && (
        <DisableDialog
          repoId={repoId}
          onDisabled={(s) => {
            setShowDisableDialog(false);
            onDisabled(s);
          }}
          onClose={() => setShowDisableDialog(false)}
        />
      )}
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Panel skeleton
// ─────────────────────────────────────────────────────────────────────────────

function WikiPanelSkeleton() {
  return (
    <Panel className="space-y-6">
      <div className="space-y-1">
        <div className="h-6 w-32 animate-pulse rounded bg-[var(--bg-surface)]" />
        <div className="h-4 w-64 animate-pulse rounded bg-[var(--bg-surface)]" />
      </div>
      <SkeletonRow wide />
      <div className="space-y-2">
        <SkeletonRow />
        <SkeletonRow wide />
      </div>
    </Panel>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Main exported component
// ─────────────────────────────────────────────────────────────────────────────

export interface WikiSettingsPanelProps {
  repoId: string;
  repoName: string;
  /** Initial per-repo settings from REPOSITORY_QUERY. Null = not configured. */
  initialSettings: RepositoryLivingWikiSettings | null | undefined;
}

type PanelState =
  | "loading"   // global settings loading
  | "state0"    // global disabled / kill-switch
  | "state1"    // activation gate (not yet configured)
  | "state2"    // corrupt (enabled=true, sinks=[])
  | "state3"    // cold-start running
  | "state4"    // enabled idle (success)
  | "state5";   // enabled, last run failed

export function WikiSettingsPanel({
  repoId,
  repoName,
  initialSettings,
}: WikiSettingsPanelProps) {
  const [settings, setSettings] = useState<
    RepositoryLivingWikiSettings | null | undefined
  >(initialSettings);
  const [activeJobId, setActiveJobId] = useState<string | null>(null);
  const [panelState, setPanelState] = useState<PanelState>("loading");

  // Sync settings from props when initialSettings changes (e.g. parent's
  // urql query resolves after the panel mounts, or refetches with fresh
  // data). Without this, the panel commits to whatever value initialSettings
  // had on first render — which is null while the parent's Repository query
  // is still fetching, and produces the "Living Wiki disabled / Enable" CTA
  // even when the repo is enabled and a job is in flight.
  //
  // Local mutations call setSettings directly with the mutation response, and
  // urql caches the response so the next initialSettings prop value carries
  // the same data — there's no risk of clobbering a fresh local update.
  useEffect(() => {
    if (initialSettings !== undefined) {
      setSettings(initialSettings);
    }
  }, [initialSettings]);

  // Discover in-flight living-wiki jobs for this repo. activeJobId is normally
  // set by the Enable / Retry mutation success handlers, so a user who
  // refreshes the page or opens it in a new tab while a job is running would
  // otherwise see State 4/5 (last result) instead of State 3 (running). Probe
  // the LLM activity feed once on mount and whenever settings change so the
  // running job is picked up.
  useEffect(() => {
    if (!repoId) return;
    if (activeJobId) return;
    if (!settings || !settings.enabled) return;
    let cancelled = false;
    (async () => {
      try {
        const res = await authFetch(
          `/api/v1/admin/llm/activity?repo_id=${encodeURIComponent(repoId)}&limit=20`,
        );
        if (!res.ok) return;
        const body = (await res.json()) as {
          active?: { id: string; subsystem?: string; job_type?: string }[];
        };
        const job = (body.active ?? []).find(
          (j) =>
            j.subsystem === "living_wiki" &&
            (j.job_type === "living_wiki_cold_start" ||
              j.job_type === "living_wiki_retry_excluded"),
        );
        if (!cancelled && job) {
          setActiveJobId(job.id);
        }
      } catch {
        // Network blip — the user can refresh. Don't crash the panel.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [repoId, activeJobId, settings]);

  // Refs for focus management (a11y req 2, 3)
  const progressRef = useRef<HTMLDivElement>(null);
  const summaryRef = useRef<HTMLDivElement>(null);

  // Fetch global settings (for kill-switch + credential availability)
  const [{ data: globalData, fetching: globalFetching }] = useQuery({
    query: LIVING_WIKI_GLOBAL_SETTINGS_QUERY,
  });
  const global: GlobalSettings = globalData?.livingWikiSettings ?? {};

  // Derive panel state
  useEffect(() => {
    if (globalFetching) {
      setPanelState("loading");
      return;
    }

    // State 0: global disabled or kill-switch
    if (!global.enabled || global.killSwitchActive) {
      setPanelState("state0");
      return;
    }

    const s = settings;

    if (!s) {
      // State 1: globally enabled, repo not yet configured
      setPanelState("state1");
      return;
    }

    if (s.enabled && s.sinks.length === 0) {
      // State 2: corrupt
      setPanelState("state2");
      return;
    }

    if (!s.enabled) {
      // State 1: disabled, show activation gate
      setPanelState("state1");
      return;
    }

    // State 3: cold-start running
    if (activeJobId) {
      setPanelState("state3");
      return;
    }

    // State 4/5: enabled, check last job result
    const last = s.lastJobResult;
    if (
      last &&
      (last.status === "failed" ||
        (last.failureCategory != null && last.failureCategory !== ""))
    ) {
      setPanelState("state5");
      return;
    }

    setPanelState("state4");
  }, [globalFetching, global.enabled, global.killSwitchActive, settings, activeJobId]);

  // When transitioning to state3, focus the progress region
  useEffect(() => {
    if (panelState === "state3") {
      setTimeout(() => progressRef.current?.focus(), 50);
    }
    if (panelState === "state4" || panelState === "state5") {
      setTimeout(() => summaryRef.current?.focus(), 50);
    }
  }, [panelState]);

  const handleEnableSuccess = (result: {
    settings: RepositoryLivingWikiSettings;
    jobId: string | null;
    notice: string | null;
  }) => {
    setSettings(result.settings);
    if (result.jobId) {
      setActiveJobId(result.jobId);
    }
  };

  const handleJobComplete = () => {
    setActiveJobId(null);
    // Settings will be refetched by parent on next REPOSITORY_QUERY poll;
    // set locally to transition state machine.
    setSettings((prev) =>
      prev ? { ...prev, lastRunAt: new Date().toISOString() } : prev
    );
  };

  const handleSettingsUpdated = (s: RepositoryLivingWikiSettings) => {
    setSettings(s);
  };

  const handleDisabled = (s: RepositoryLivingWikiSettings) => {
    setSettings(s);
    setActiveJobId(null);
  };

  // ── Render ──

  if (panelState === "loading") {
    return <WikiPanelSkeleton />;
  }

  return (
    <>
      <Panel className="space-y-6">
        {/* Panel header */}
        <div className="flex items-center gap-3">
          <SectionHeader title="Living Wiki" />
          {panelState === "state4" && <Pill label="Enabled" variant="enabled" />}
          {panelState === "state5" && <Pill label="Error" variant="error" />}
          {(panelState === "state1" || panelState === "state2") && (
            <Pill label="Disabled" variant="disabled" />
          )}
          {panelState === "state3" && (
            <Pill label="Generating" variant="partial" />
          )}
        </div>
        <p className="text-sm leading-6 text-[var(--text-secondary)]">
          Keep your documentation in sync as code evolves. Configure sinks
          and let SourceBridge propose wiki updates automatically.
        </p>

        {/* State 0 */}
        {panelState === "state0" && (
          <div className="flex items-start gap-3 rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-3">
            <span className="mt-0.5 text-sm text-[var(--text-tertiary)]" aria-hidden="true">
              ℹ
            </span>
            <div className="min-w-0 text-sm">
              {global.killSwitchActive ? (
                <p className="text-[var(--text-secondary)]">
                  Living Wiki is paused via{" "}
                  <code className="rounded bg-[var(--bg-base)] px-1 py-0.5 font-mono text-xs">
                    SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH
                  </code>
                  . Settings are saved, but no jobs will run until the kill-switch is unset.{" "}
                  <Link
                    href="/settings/living-wiki"
                    className="font-medium text-[var(--accent-primary)] hover:underline"
                  >
                    View Living Wiki settings
                  </Link>
                </p>
              ) : (
                <p className="text-[var(--text-secondary)]">
                  Living Wiki is disabled globally.{" "}
                  <Link
                    href="/settings/living-wiki"
                    className="font-medium text-[var(--accent-primary)] hover:underline"
                  >
                    Enable it in Settings → Living Wiki
                  </Link>{" "}
                  to configure this repository.
                </p>
              )}
            </div>
          </div>
        )}

        {/* State 1: Activation gate */}
        {panelState === "state1" && (
          <StageAForm
            repoId={repoId}
            repoName={repoName}
            globalSettings={global}
            initial={
              settings
                ? { mode: settings.mode, sinks: settings.sinks }
                : undefined
            }
            onSuccess={handleEnableSuccess}
          />
        )}

        {/* State 2: Corrupt — enabled but no sinks */}
        {panelState === "state2" && (
          <StageAForm
            repoId={repoId}
            repoName={repoName}
            globalSettings={global}
            initial={
              settings
                ? { mode: settings.mode, sinks: settings.sinks }
                : undefined
            }
            isCorrupt
            onSuccess={handleEnableSuccess}
          />
        )}

        {/* State 3: Cold-start running */}
        {panelState === "state3" && activeJobId && (
          <ColdStartProgress
            repoId={repoId}
            repoName={repoName}
            jobId={activeJobId}
            onComplete={handleJobComplete}
            progressRef={progressRef}
          />
        )}

        {/* State 4/5: Enabled (idle or failed) */}
        {(panelState === "state4" || panelState === "state5") &&
          settings?.enabled && (
            <EnabledSummary
              repoId={repoId}
              repoName={repoName}
              settings={settings}
              globalSettings={global}
              onSettingsUpdated={handleSettingsUpdated}
              onDisabled={handleDisabled}
              summaryRef={summaryRef}
            />
          )}
      </Panel>
    </>
  );
}
