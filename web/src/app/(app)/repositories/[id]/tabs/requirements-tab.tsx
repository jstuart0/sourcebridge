"use client";

import { useState, useEffect } from "react";
import Link from "next/link";
import { useClient, useQuery, useMutation } from "urql";
import {
  REQUIREMENTS_QUERY,
  AUTO_LINK_MUTATION,
  ENRICH_ALL_REQUIREMENTS_MUTATION,
  IMPORT_REQUIREMENTS_MUTATION,
} from "@/lib/graphql/queries";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { CreateRequirementDialog } from "@/components/requirements/CreateRequirementDialog";
import { trackEvent } from "@/lib/telemetry";
import { useCurrentUser, isAdminRole } from "@/lib/current-user";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ReqNode {
  id: string;
  externalId: string;
  title: string;
  source: string;
  priority: string;
  tags?: string[];
}

interface RequirementsTabProps {
  repoId: string;
  repoName: string;
  /** True when this tab is the currently visible tab. Gates queries and effects. */
  active?: boolean;
  /** Per-op AI loading gate: true when "requirements:auto-link" is in flight */
  isAiLoading: (key: string) => boolean;
  runAiOp: (key: string, fn: () => Promise<void>) => Promise<void>;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function RequirementsTab({
  repoId,
  repoName,
  active = true,
  isAiLoading,
  runAiOp,
}: RequirementsTabProps) {
  const [createRequirementOpen, setCreateRequirementOpen] = useState(false);
  const [linkResult, setLinkResult] = useState<
    { type: "success" | "error" | "info"; message: string } | null
  >(null);
  const [importContent, setImportContent] = useState("");

  const currentUser = useCurrentUser();
  const isAdmin = isAdminRole(currentUser?.role);

  const [reqsResult, reexecuteRequirements] = useQuery({
    query: REQUIREMENTS_QUERY,
    variables: { repositoryId: repoId, limit: 50 },
    pause: !active,
  });

  const urqlClient = useClient();
  const initialReqs: ReqNode[] = reqsResult.data?.requirements?.nodes || [];
  const reqsTotalCount: number = reqsResult.data?.requirements?.totalCount ?? 0;
  const [extraReqs, setExtraReqs] = useState<ReqNode[]>([]);
  const [loadingMoreReqs, setLoadingMoreReqs] = useState(false);

  // Paginated load gated on `active` — runs only when the tab is visible.
  // Component stays mounted across tab switches so state is preserved.
  useEffect(() => {
    if (!active) return;
    if (initialReqs.length < 50 || initialReqs.length >= reqsTotalCount) {
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
  }, [active, initialReqs.length, reqsTotalCount, repoId, urqlClient]);

  const reqs: ReqNode[] = [...initialReqs, ...extraReqs];

  const [, autoLink] = useMutation(AUTO_LINK_MUTATION);
  const [, enrichAll] = useMutation(ENRICH_ALL_REQUIREMENTS_MUTATION);
  const [, importReqs] = useMutation(IMPORT_REQUIREMENTS_MUTATION);

  async function handleAutoLink() {
    await runAiOp("requirements:auto-link", async () => {
      setLinkResult(null);
      const res = await autoLink({ repositoryId: repoId });
      if (res.data?.autoLinkRequirements) {
        const { linksCreated, requirementsProcessed } = res.data.autoLinkRequirements;
        setLinkResult({
          type: "success",
          message: `Processed ${requirementsProcessed} requirements, created ${linksCreated} links.`,
        });
      } else if (res.error) {
        setLinkResult({ type: "error", message: `Auto-link failed: ${res.error.message}` });
      }
    });
  }

  async function handleEnrichAll() {
    await runAiOp("requirements:enrich-all", async () => {
      setLinkResult(null);
      trackEvent({ event: "requirements_enrich_all", repositoryId: repoId });
      const res = await enrichAll({ repositoryId: repoId, batchSize: 10 });
      if (res.data?.enrichAllRequirements) {
        const { requirementsQueued, jobId } = res.data.enrichAllRequirements;
        if (jobId === "none" || requirementsQueued === 0) {
          setLinkResult({
            type: "info",
            message: "All requirements already enriched — nothing to do.",
          });
        } else {
          setLinkResult({
            type: "success",
            message: `Enriching ${requirementsQueued} requirements in the background (job ${jobId}). Progress is visible on the admin Monitor page.`,
          });
        }
      } else if (res.error) {
        setLinkResult({ type: "error", message: `Enrich all failed: ${res.error.message}` });
      }
    });
  }

  async function handleImportReqs() {
    if (!importContent.trim()) return;
    trackEvent({ event: "requirements_imported", repositoryId: repoId });
    const res = await importReqs({ input: { repositoryId: repoId, content: importContent, format: "MARKDOWN" } });
    setImportContent("");
    if (res.error) {
      setLinkResult({ type: "error", message: `Import failed: ${res.error.message}` });
    } else {
      setLinkResult({ type: "success", message: "Requirements imported successfully." });
    }
  }

  const autoLinkBusy = isAiLoading("requirements:auto-link");
  const enrichAllBusy = isAiLoading("requirements:enrich-all");

  // Enrich all is visible to admins only. Non-admin users don't see the button
  // to avoid confusing 403s (the backend gate returns a clear error if called).
  // Mirrors the resolver's unenriched filter: no tags AND priority empty/unset.
  const hasUnenriched = reqs.some(
    (req) =>
      (!req.tags || req.tags.length === 0) &&
      (!req.priority || req.priority === "unset")
  );

  const listContainerClass = "max-h-[60vh] overflow-y-auto";
  const listRowClass =
    "border-b border-[var(--border-subtle)] px-0 py-2.5 text-sm last:border-b-0";

  return (
    <div>
      <div className="mb-4 flex flex-wrap gap-3">
        <Button onClick={() => setCreateRequirementOpen(true)}>
          + New requirement
        </Button>
        <Button variant="secondary" onClick={handleAutoLink} disabled={autoLinkBusy}>
          {autoLinkBusy ? "Linking..." : "Auto-Link Specs to Code"}
        </Button>
        {isAdmin && (
          <Button
            variant="secondary"
            onClick={handleEnrichAll}
            disabled={enrichAllBusy || !hasUnenriched}
            title={
              !hasUnenriched
                ? "All visible requirements already have tags and priority"
                : "Enrich unenriched requirements with AI-suggested tags and priority (admin only)"
            }
          >
            {enrichAllBusy ? "Enriching..." : "Enrich All"}
          </Button>
        )}
      </div>
      {linkResult ? (
        <div
          role="status"
          aria-live="polite"
          aria-atomic="true"
          className={`mb-4 rounded-[var(--control-radius)] border px-3 py-2 text-sm ${
            linkResult.type === "error"
              ? "border-[var(--danger-border)] bg-[var(--danger-bg)] text-[var(--danger-text)]"
              : linkResult.type === "info"
              ? "border-[var(--border-default)] bg-[var(--bg-surface)] text-[var(--text-secondary)]"
              : "border-emerald-500/30 bg-emerald-500/10 text-emerald-500"
          }`}
        >
          {linkResult.message}
        </div>
      ) : null}
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
              This is optional. SourceBridge.ai can still explain the codebase, generate cliff notes, and review files without it.
              Importing specs later unlocks intent-to-code links, coverage visibility, and richer change impact analysis.
            </p>
          </div>
        ) : (
          <div className={listContainerClass}>
            {reqs.map((req) => (
              <Link
                key={req.id}
                href={`/requirements/${req.id}?repoId=${repoId}&repoName=${encodeURIComponent(repoName)}`}
                className={`${listRowClass} block cursor-pointer rounded-[var(--control-radius)] px-3 transition-colors hover:bg-[var(--bg-hover)]`}
              >
                <div className="flex items-center justify-between gap-4">
                  <span className="font-medium text-[var(--text-primary)]">{req.externalId}</span>
                  <div className="flex items-center gap-2">
                    <span className="text-[var(--text-secondary)]">
                      {req.priority || req.source || "—"}
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
          reexecuteRequirements({ requestPolicy: "network-only" });
        }}
      />
    </div>
  );
}
