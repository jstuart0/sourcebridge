"use client";

import { useState } from "react";
import { useQuery, useMutation } from "urql";
import {
  DISCOVERED_REQUIREMENTS_QUERY,
  TRIGGER_SPEC_EXTRACTION_MUTATION,
  PROMOTE_DISCOVERED_REQUIREMENT_MUTATION,
  DISMISS_DISCOVERED_REQUIREMENT_MUTATION,
  DISMISS_ALL_DISCOVERED_REQUIREMENTS_MUTATION,
} from "@/lib/graphql/queries";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { trackEvent } from "@/lib/telemetry";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface DiscoveredSpec {
  id: string;
  text: string;
  source: string;
  sourceFile: string;
  sourceLine: number;
  confidence: string;
  language: string;
  keywords: string[];
  llmRefined: boolean;
  status: string;
}

interface SpecsTabProps {
  repoId: string;
  /** True when this tab is the currently visible tab. Gates the initial query. */
  active?: boolean;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function SpecsTab({ repoId, active = true }: SpecsTabProps) {
  const [specExtracting, setSpecExtracting] = useState(false);
  const [specExtractionResult, setSpecExtractionResult] = useState<string | null>(null);
  const [specExtractionStatus, setSpecExtractionStatus] = useState<"error" | "success" | null>(null);
  const [specConfidenceFilter, setSpecConfidenceFilter] = useState<string | null>(null);

  const [discoveredReqsResult, reexecuteDiscoveredReqs] = useQuery({
    query: DISCOVERED_REQUIREMENTS_QUERY,
    variables: { repositoryId: repoId, limit: 100 },
    pause: !active,
  });

  const [, triggerSpecExtraction] = useMutation(TRIGGER_SPEC_EXTRACTION_MUTATION);
  const [, promoteDiscoveredReq] = useMutation(PROMOTE_DISCOVERED_REQUIREMENT_MUTATION);
  const [, dismissDiscoveredReq] = useMutation(DISMISS_DISCOVERED_REQUIREMENT_MUTATION);
  const [, dismissAllDiscoveredReqs] = useMutation(DISMISS_ALL_DISCOVERED_REQUIREMENTS_MUTATION);

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

  const listContainerClass = "max-h-[60vh] overflow-y-auto";
  const listRowClass =
    "border-b border-[var(--border-subtle)] px-0 py-2.5 text-sm last:border-b-0";

  return (
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
              .filter((spec: DiscoveredSpec) => !specConfidenceFilter || spec.confidence === specConfidenceFilter)
              .map((spec: DiscoveredSpec) => (
              <div
                key={spec.id}
                className={`${listRowClass} rounded-[var(--control-radius)] px-3`}
              >
                <div className="flex items-start justify-between gap-4">
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium text-[var(--text-primary)]">{spec.text}</p>
                    <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--text-secondary)]">
                      <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${
                        spec.confidence === "high" ? "bg-[var(--confidence-high)]/10 text-[var(--confidence-high)]" :
                        spec.confidence === "medium" ? "bg-[var(--confidence-medium)]/10 text-[var(--confidence-medium)]" :
                        "bg-[var(--confidence-low)]/10 text-[var(--confidence-low)]"
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
  );
}
