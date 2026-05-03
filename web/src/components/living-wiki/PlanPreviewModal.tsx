// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

"use client";

/**
 * PlanPreviewModal — shows the planned page set before a Living Wiki build,
 * letting users deselect non-required pages before committing.
 *
 * Required pages (REPO_WIDE) render as static rows with a lock badge — no
 * checkbox. They are always included and cannot be deselected.
 *
 * Uses Radix Dialog for focus trap + ESC-to-close + inert backdrop.
 * Uses Radix Tooltip for the mode pill info-icon and the disabled-button
 * "Loading plan..." affordance.
 */

import * as Dialog from "@radix-ui/react-dialog";
import * as Tooltip from "@radix-ui/react-tooltip";
import { useEffect, useRef, useState } from "react";
import { useQuery } from "urql";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { PREVIEW_LIVING_WIKI_PLAN_QUERY } from "@/lib/graphql/queries";

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

export type PlanModalIntent =
  | { kind: "enable"; mode: "OVERVIEW" | "DETAILED" }
  | { kind: "regenerate"; mode: "OVERVIEW" | "DETAILED" }
  | { kind: "retry"; mode: "OVERVIEW" | "DETAILED" };

export interface PlanPreviewModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  repositoryId: string;
  intent: PlanModalIntent;
  /** Human label for the primary action button, e.g. "Build", "Regenerate", "Retry" */
  intentLabel: string;
  /** Per-run page count override from the settings panel (null = use repo setting) */
  pageCountOverride: number | null;
  /** Called when the user confirms. Never throws — errors handled upstream. */
  onConfirm: (selection: {
    selectedPageIds: string[] | null;
    planSignature: string | null;
  }) => Promise<void>;
}

interface LivingWikiPlanPage {
  id: string;
  templateId: string;
  title: string;
  pageType: "REPO_WIDE" | "ARCHITECTURE" | "TOP_LEVEL_DIR";
  subsystem: string | null;
  audience: string;
  required: boolean;
}

interface LivingWikiPlan {
  planSignature: string;
  mode: string;
  modeTooltip: string;
  summary: string;
  totalPages: number;
  preCap: number;
  capSource: string;
  capValue: number;
  notice: string | null;
  pages: LivingWikiPlanPage[];
}

// ─────────────────────────────────────────────────────────────────────────────
// Small presentational helpers
// ─────────────────────────────────────────────────────────────────────────────

function InfoIcon({ className }: { className?: string }) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 16 16"
      fill="currentColor"
      aria-hidden="true"
      className={cn("h-3.5 w-3.5", className)}
    >
      <path
        fillRule="evenodd"
        d="M15 8A7 7 0 1 1 1 8a7 7 0 0 1 14 0Zm-6-1.5a1 1 0 1 0-2 0v4a1 1 0 1 0 2 0v-4ZM8 5a1 1 0 1 0 0-2 1 1 0 0 0 0 2Z"
        clipRule="evenodd"
      />
    </svg>
  );
}

function LockIcon({ className }: { className?: string }) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 16 16"
      fill="currentColor"
      aria-hidden="true"
      className={cn("h-3 w-3", className)}
    >
      <path
        fillRule="evenodd"
        d="M8 1a3.5 3.5 0 0 0-3.5 3.5V6H4a2 2 0 0 0-2 2v5a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-.5V4.5A3.5 3.5 0 0 0 8 1Zm2 5V4.5a2 2 0 1 0-4 0V6h4Z"
        clipRule="evenodd"
      />
    </svg>
  );
}

/** Skeleton shimmer row (for loading state). */
function SkeletonRow({ wide }: { wide?: boolean }) {
  return (
    <div
      className={cn(
        "h-4 rounded animate-pulse bg-[var(--bg-hover)]",
        wide ? "w-32" : "w-full",
      )}
      aria-hidden="true"
    />
  );
}

/** Audience badge — small pill with audience text. */
function AudienceBadge({ audience }: { audience: string }) {
  const label =
    audience === "ENGINEER"
      ? "Engineer"
      : audience === "PRODUCT"
        ? "Product"
        : audience === "OPERATOR"
          ? "Operator"
          : audience;
  return (
    <span className="inline-flex shrink-0 items-center rounded-full border border-[var(--border-subtle)] px-2 py-0.5 text-[11px] font-medium text-[var(--text-tertiary)]">
      {label}
    </span>
  );
}

/** Group header with title + subtitle. */
function GroupHeader({
  title,
  subtitle,
}: {
  title: string;
  subtitle: string;
}) {
  return (
    <div className="mb-2 mt-5 first:mt-0">
      <p className="text-xs font-semibold uppercase tracking-[0.12em] text-[var(--text-tertiary)]">
        {title}
      </p>
      <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">{subtitle}</p>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Mode pill
// ─────────────────────────────────────────────────────────────────────────────

const MODE_TOOLTIP: Record<string, string> = {
  lw_detailed:
    "Detailed mode — one architecture doc per subsystem cluster (shown below), plus the 3 repo-wide pages.",
  lw_overview:
    "Overview mode — subsystem summaries only, no per-package drill-downs. Fewer pages, broader audience.",
  DETAILED:
    "Detailed mode — one architecture doc per subsystem cluster (shown below), plus the 3 repo-wide pages.",
  OVERVIEW:
    "Overview mode — subsystem summaries only, no per-package drill-downs. Fewer pages, broader audience.",
};

function ModePill({
  mode,
  tooltipContent,
}: {
  mode: string;
  tooltipContent: string;
}) {
  const label =
    mode === "lw_detailed" || mode === "DETAILED" ? "Detailed mode" : "Overview mode";
  return (
    <Tooltip.Provider delayDuration={200}>
      <Tooltip.Root>
        <Tooltip.Trigger asChild>
          <span className="inline-flex cursor-default items-center gap-1 rounded-full border border-[var(--border-default)] bg-[var(--bg-surface-2)] px-2.5 py-1 text-xs font-medium text-[var(--text-secondary)]">
            {label}
            <InfoIcon className="text-[var(--text-tertiary)]" />
          </span>
        </Tooltip.Trigger>
        <Tooltip.Portal>
          <Tooltip.Content
            side="bottom"
            align="start"
            sideOffset={6}
            className="z-[200] max-w-xs rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-elevated)] px-3 py-2 text-xs leading-relaxed text-[var(--text-secondary)] shadow-lg"
          >
            {tooltipContent}
            <Tooltip.Arrow className="fill-[var(--bg-elevated)]" />
          </Tooltip.Content>
        </Tooltip.Portal>
      </Tooltip.Root>
    </Tooltip.Provider>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Main component
// ─────────────────────────────────────────────────────────────────────────────

export function PlanPreviewModal({
  open,
  onOpenChange,
  repositoryId,
  intent,
  intentLabel,
  pageCountOverride,
  onConfirm,
}: PlanPreviewModalProps) {
  // ── query ──────────────────────────────────────────────────────────────────
  const [queryKey, setQueryKey] = useState(0);
  const [debouncedPageCountOverride, setDebouncedPageCountOverride] = useState(
    pageCountOverride,
  );

  // Debounce pageCountOverride changes (300ms) — pattern from search/page.tsx
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedPageCountOverride(pageCountOverride);
    }, 300);
    return () => clearTimeout(timer);
  }, [pageCountOverride]);

  // Re-fetch on every open (no client-side cache) by incrementing queryKey
  const prevOpenRef = useRef(false);
  useEffect(() => {
    if (open && !prevOpenRef.current) {
      setQueryKey((k) => k + 1);
      // Reset selection when modal reopens
      setUncheckedIds(new Set());
    }
    prevOpenRef.current = open;
  }, [open]);

  const [{ data, fetching, error }] = useQuery<{ previewLivingWikiPlan: LivingWikiPlan }>({
    query: PREVIEW_LIVING_WIKI_PLAN_QUERY,
    variables: {
      repositoryId,
      mode: intent.mode,
      pageCountOverride: debouncedPageCountOverride ?? undefined,
      // queryKey is embedded as a variable so urql re-fetches when it changes
      _key: queryKey,
    },
    pause: !open || !repositoryId,
    requestPolicy: "network-only",
  });

  const plan = data?.previewLivingWikiPlan ?? null;

  // ── selection state ────────────────────────────────────────────────────────
  // Track which non-required page IDs are unchecked. Absent = checked.
  const [uncheckedIds, setUncheckedIds] = useState<Set<string>>(new Set());

  const togglePage = (id: string, checked: boolean) => {
    setUncheckedIds((prev) => {
      const next = new Set(prev);
      if (checked) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  // ── derived counts ─────────────────────────────────────────────────────────
  const requiredPages = plan?.pages.filter((p) => p.required) ?? [];
  const nonRequiredPages = plan?.pages.filter((p) => !p.required) ?? [];
  const checkedNonRequired = nonRequiredPages.filter(
    (p) => !uncheckedIds.has(p.id),
  );
  const totalSelected = requiredPages.length + checkedNonRequired.length;
  const totalPages = plan?.pages.length ?? 0;
  const requiredCount = requiredPages.length;

  // ── confirm handler ────────────────────────────────────────────────────────
  const [confirming, setConfirming] = useState(false);

  const handleConfirm = async () => {
    if (confirming) return;
    setConfirming(true);
    try {
      if (!plan || uncheckedIds.size === 0) {
        // All non-required checked (or no plan) — "no filter" path
        await onConfirm({ selectedPageIds: null, planSignature: null });
      } else {
        // At least one non-required page was deselected
        const selectedNonRequired = nonRequiredPages
          .filter((p) => !uncheckedIds.has(p.id))
          .map((p) => p.id);
        await onConfirm({
          selectedPageIds: selectedNonRequired,
          planSignature: plan.planSignature,
        });
      }
    } finally {
      setConfirming(false);
    }
  };

  const handleBuildAnyway = async () => {
    if (confirming) return;
    setConfirming(true);
    try {
      await onConfirm({ selectedPageIds: null, planSignature: null });
    } finally {
      setConfirming(false);
    }
  };

  // ── tooltip content for mode pill ─────────────────────────────────────────
  const modeTooltipContent =
    plan?.modeTooltip ||
    MODE_TOOLTIP[plan?.mode ?? intent.mode] ||
    MODE_TOOLTIP[intent.mode] ||
    "";

  const primaryDisabled = fetching || confirming;

  // ─────────────────────────────────────────────────────────────────────────
  // Render
  // ─────────────────────────────────────────────────────────────────────────
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        {/* Backdrop */}
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/40 backdrop-blur-[2px] data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0" />

        {/* Modal */}
        <Dialog.Content
          data-testid="plan-preview-modal"
          className={cn(
            "fixed left-1/2 top-1/2 z-50 flex max-h-[85vh] w-full max-w-xl -translate-x-1/2 -translate-y-1/2 flex-col",
            "rounded-[var(--radius-md)] border border-[var(--border-default)] bg-[var(--bg-surface)] shadow-xl",
            "data-[state=open]:animate-in data-[state=closed]:animate-out",
            "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
            "data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95",
            "data-[state=closed]:slide-out-to-left-1/2 data-[state=closed]:slide-out-to-top-[48%]",
            "data-[state=open]:slide-in-from-left-1/2 data-[state=open]:slide-in-from-top-[48%]",
          )}
        >
          {/* Header */}
          <div className="shrink-0 px-6 pb-3 pt-5">
            <Dialog.Title className="text-base font-semibold tracking-[-0.01em] text-[var(--text-primary)]">
              {intentLabel} Living Wiki
            </Dialog.Title>
            <div className="mt-2 flex items-center gap-2">
              <ModePill
                mode={plan?.mode ?? intent.mode}
                tooltipContent={
                  modeTooltipContent ||
                  (intent.mode === "DETAILED"
                    ? MODE_TOOLTIP.DETAILED
                    : MODE_TOOLTIP.OVERVIEW)
                }
              />
            </div>
          </div>

          {/* Divider */}
          <div className="shrink-0 border-t border-[var(--border-subtle)]" />

          {/* Accessible description (visually hidden — body is the real content) */}
          <Dialog.Description className="sr-only">
            Review and select the pages to include in this Living Wiki build.
          </Dialog.Description>

          {/* Body */}
          <div className="min-h-0 flex-1 overflow-y-auto px-6 py-4">
              {fetching && (
                <div className="space-y-3">
                  <p className="text-xs text-[var(--text-tertiary)]">
                    Resolving page plan…
                  </p>
                  <SkeletonRow wide />
                  <SkeletonRow />
                  <SkeletonRow />
                  <SkeletonRow />
                  <div className="mt-4">
                    <SkeletonRow wide />
                  </div>
                  <SkeletonRow />
                  <SkeletonRow />
                </div>
              )}

              {!fetching && error && (
                <div className="space-y-3">
                  <p className="text-sm text-[var(--text-secondary)]">
                    Could not load the page plan. You can wait and try again, or build now using the repository&apos;s current settings.
                  </p>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={handleBuildAnyway}
                    disabled={confirming}
                    className="border-[var(--danger-border)] text-[var(--danger-text)] hover:bg-[var(--danger-bg)]"
                  >
                    Build anyway (plan unavailable)
                  </Button>
                </div>
              )}

              {!fetching && !error && plan && (
                <PageList
                  plan={plan}
                  uncheckedIds={uncheckedIds}
                  onToggle={togglePage}
                />
              )}

              {/* Phase 4 placeholder for stale-plan banner */}
              <div data-testid="plan-preview-stale-banner" />
            </div>

          {/* Divider */}
          <div className="shrink-0 border-t border-[var(--border-subtle)]" />

          {/* Footer */}
          <div className="shrink-0 flex items-center justify-between gap-3 px-6 py-4">
            {/* Counter */}
            <p className="text-xs text-[var(--text-tertiary)]">
              {plan && !fetching && !error
                ? `${totalSelected} of ${totalPages} selected (${requiredCount} required)`
                : null}
            </p>

            {/* Actions */}
            <div className="flex items-center gap-2">
              <Dialog.Close asChild>
                <Button variant="secondary" size="sm" disabled={confirming}>
                  Cancel
                </Button>
              </Dialog.Close>

              <Tooltip.Provider delayDuration={0}>
                <Tooltip.Root open={fetching ? undefined : false}>
                  <Tooltip.Trigger asChild>
                    {/* span wrapper: disabled buttons don't fire pointer events */}
                    <span>
                      <Button
                        variant="primary"
                        size="sm"
                        disabled={primaryDisabled}
                        onClick={handleConfirm}
                      >
                        {intentLabel}
                      </Button>
                    </span>
                  </Tooltip.Trigger>
                  <Tooltip.Portal>
                    <Tooltip.Content
                      side="top"
                      sideOffset={6}
                      className="z-[200] rounded-[var(--radius-sm)] border border-[var(--border-default)] bg-[var(--bg-elevated)] px-2.5 py-1.5 text-xs text-[var(--text-secondary)] shadow-lg"
                    >
                      Loading plan…
                      <Tooltip.Arrow className="fill-[var(--bg-elevated)]" />
                    </Tooltip.Content>
                  </Tooltip.Portal>
                </Tooltip.Root>
              </Tooltip.Provider>
            </div>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// PageList — grouped page rows
// ─────────────────────────────────────────────────────────────────────────────

function PageList({
  plan,
  uncheckedIds,
  onToggle,
}: {
  plan: LivingWikiPlan;
  uncheckedIds: Set<string>;
  onToggle: (id: string, checked: boolean) => void;
}) {
  const repoWidePages = plan.pages.filter((p) => p.pageType === "REPO_WIDE");
  const architecturePages = plan.pages.filter(
    (p) => p.pageType === "ARCHITECTURE",
  );
  const topLevelDirPages = plan.pages.filter(
    (p) => p.pageType === "TOP_LEVEL_DIR",
  );

  return (
    <div>
      {repoWidePages.length > 0 && (
        <section aria-label="Repository pages">
          <GroupHeader
            title="Repository pages"
            subtitle="Always generated — define the wiki's navigation structure"
          />
          <ul className="space-y-1" role="list">
            {repoWidePages.map((page) => (
              <RequiredPageRow key={page.id} page={page} />
            ))}
          </ul>
        </section>
      )}

      {architecturePages.length > 0 && (
        <section aria-label="Subsystem pages">
          <GroupHeader
            title="Subsystem pages"
            subtitle="One page per detected code cluster"
          />
          <ul className="space-y-1" role="list">
            {architecturePages.map((page) => (
              <SelectablePageRow
                key={page.id}
                page={page}
                checked={!uncheckedIds.has(page.id)}
                onToggle={onToggle}
              />
            ))}
          </ul>
        </section>
      )}

      {topLevelDirPages.length > 0 && (
        <section aria-label="Package pages">
          <GroupHeader
            title="Package pages"
            subtitle="Top-level package or directory summaries"
          />
          <ul className="space-y-1" role="list">
            {topLevelDirPages.map((page) => (
              <SelectablePageRow
                key={page.id}
                page={page}
                checked={!uncheckedIds.has(page.id)}
                onToggle={onToggle}
              />
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Row components
// ─────────────────────────────────────────────────────────────────────────────

function RequiredPageRow({ page }: { page: LivingWikiPlanPage }) {
  return (
    <li className="flex items-center gap-3 rounded-[var(--radius-sm)] px-2 py-1.5">
      {/* Spacer to align with checkboxes */}
      <span className="h-4 w-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-2 flex-wrap">
          <span className="truncate text-sm text-[var(--text-primary)]">
            {page.title}
          </span>
          <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-[var(--border-subtle)] px-2 py-0.5 text-[11px] font-medium text-[var(--text-tertiary)]">
            <LockIcon />
            Always included
          </span>
        </span>
      </span>
    </li>
  );
}

function SelectablePageRow({
  page,
  checked,
  onToggle,
}: {
  page: LivingWikiPlanPage;
  checked: boolean;
  onToggle: (id: string, checked: boolean) => void;
}) {
  const checkboxId = `page-row-${page.id}`;
  return (
    <li className="flex items-start gap-3 rounded-[var(--radius-sm)] px-2 py-1.5 hover:bg-[var(--bg-hover)]">
      <input
        id={checkboxId}
        type="checkbox"
        checked={checked}
        onChange={(e) => onToggle(page.id, e.target.checked)}
        className="mt-0.5 h-4 w-4 shrink-0 cursor-pointer accent-[var(--accent-primary)]"
        aria-label={page.title}
      />
      <label
        htmlFor={checkboxId}
        className="flex min-w-0 flex-1 cursor-pointer flex-col gap-1 sm:flex-row sm:items-center sm:gap-2"
      >
        <span className="min-w-0 flex-1 truncate text-sm text-[var(--text-primary)]">
          {page.title}
        </span>
        <span className="flex shrink-0 flex-wrap items-center gap-1.5">
          {page.subsystem && (
            <span className="hidden sm:inline text-xs text-[var(--text-tertiary)]">
              {page.subsystem}
            </span>
          )}
          <AudienceBadge audience={page.audience} />
        </span>
      </label>
    </li>
  );
}
