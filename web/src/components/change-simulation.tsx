"use client";

import { useState } from "react";
import { useMutation } from "urql";
import { SIMULATE_CHANGE_MUTATION } from "@/lib/graphql/queries";
import { Panel } from "@/components/ui/panel";
import { Button } from "@/components/ui/button";
import { ImpactReportPanel, type ImpactReportData } from "@/components/impact-report";

interface ResolvedSymbol {
  symbolId: string;
  name: string;
  qualifiedName: string;
  kind: string;
  filePath: string;
  similarity: number;
  isAnchor: boolean;
}

interface SimulatedResult {
  id: string;
  simulated: boolean;
  description: string;
  resolvedSymbols: ResolvedSymbol[];
  report: ImpactReportData;
  computedAt: string;
}

function SimulatedBadge() {
  return (
    <span className="inline-flex items-center gap-1 rounded-full border border-violet-400/40 bg-violet-500/10 px-2 py-0.5 text-xs font-semibold text-violet-400">
      Simulated
    </span>
  );
}

function SimilarityBar({ similarity }: { similarity: number }) {
  const pct = Math.round(similarity * 100);
  const color = pct >= 70 ? "bg-emerald-500" : pct >= 40 ? "bg-amber-500" : "bg-gray-500";
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-16 rounded-full bg-[var(--bg-hover)]">
        <div className={`h-1.5 rounded-full ${color}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="text-xs text-[var(--text-tertiary)]">{pct}%</span>
    </div>
  );
}

export function ChangeSimulationPanel({ repositoryId }: { repositoryId: string }) {
  const [description, setDescription] = useState("");
  const [anchorFile, setAnchorFile] = useState("");
  const [anchorSymbol, setAnchorSymbol] = useState("");
  const [result, setResult] = useState<SimulatedResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [{ fetching }, simulateChange] = useMutation(SIMULATE_CHANGE_MUTATION);

  async function handleSimulate() {
    if (!description.trim() || fetching) return;
    setError(null);
    setResult(null);

    const res = await simulateChange({
      input: {
        repositoryId,
        description: description.trim(),
        ...(anchorFile.trim() ? { anchorFile: anchorFile.trim() } : {}),
        ...(anchorSymbol.trim() ? { anchorSymbol: anchorSymbol.trim() } : {}),
      },
    });

    if (res.error) {
      setError(res.error.message);
    } else if (res.data?.simulateChange) {
      setResult(res.data.simulateChange);
    }
  }

  const inputClass =
    "w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)]";

  return (
    <div className="space-y-4">
      <Panel>
        <h4 className="mb-1 text-sm font-semibold text-[var(--text-primary)]">
          Simulate a Change
        </h4>
        <p className="mb-3 text-xs text-[var(--text-secondary)]">
          Describe a hypothetical change to see its projected impact on the codebase.
        </p>

        <textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder='Describe the change... e.g., "Add a required email field to the User struct"'
          rows={3}
          maxLength={2000}
          className={`${inputClass} min-h-[5rem] resize-y`}
        />

        <div className="mt-3 grid grid-cols-2 gap-3">
          <div>
            <label className="mb-1 block text-xs text-[var(--text-secondary)]">Anchor file (optional)</label>
            <input
              type="text"
              value={anchorFile}
              onChange={(e) => setAnchorFile(e.target.value)}
              placeholder="e.g., internal/graph/impact.go"
              className={inputClass}
            />
          </div>
          <div>
            <label className="mb-1 block text-xs text-[var(--text-secondary)]">Anchor symbol (optional)</label>
            <input
              type="text"
              value={anchorSymbol}
              onChange={(e) => setAnchorSymbol(e.target.value)}
              placeholder="e.g., ComputeImpact"
              className={inputClass}
            />
          </div>
        </div>

        <div className="mt-3 flex items-center gap-3">
          <Button onClick={handleSimulate} disabled={fetching || !description.trim()}>
            {fetching ? "Simulating..." : "Simulate"}
          </Button>
          {fetching && (
            <span className="text-xs text-[var(--text-secondary)]">
              Analyzing impact...
            </span>
          )}
        </div>

        {error && (
          <div className="mt-3 rounded-[var(--control-radius)] border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-500">
            {error}
          </div>
        )}
      </Panel>

      {result && (
        <Panel>
          <div className="flex items-center gap-2">
            <h4 className="text-sm font-semibold text-[var(--text-primary)]">
              Resolved Symbols
            </h4>
            <SimulatedBadge />
          </div>
          <p className="mt-1 mb-3 text-xs text-[var(--text-secondary)]">
            These symbols were identified as primarily affected by: &ldquo;{result.description}&rdquo;
          </p>
          <div className="max-h-48 overflow-y-auto">
            {result.resolvedSymbols.map((sym) => (
              <div
                key={sym.symbolId}
                className="flex items-center justify-between border-b border-[var(--border-subtle)] py-1.5 last:border-0"
              >
                <div className="flex items-center gap-2">
                  {sym.isAnchor && (
                    <span className="rounded bg-blue-500/10 px-1.5 py-0.5 text-xs font-medium text-blue-400">
                      anchor
                    </span>
                  )}
                  <span className="font-mono text-xs text-[var(--text-primary)]">{sym.name}</span>
                  <span className="text-xs text-[var(--text-tertiary)]">{sym.filePath}</span>
                </div>
                <SimilarityBar similarity={sym.similarity} />
              </div>
            ))}
          </div>
        </Panel>
      )}

      {result && (
        <div className="relative">
          <div className="absolute top-3 right-3 z-10">
            <SimulatedBadge />
          </div>
          <ImpactReportPanel report={result.report} repositoryId={repositoryId} />
        </div>
      )}
    </div>
  );
}
