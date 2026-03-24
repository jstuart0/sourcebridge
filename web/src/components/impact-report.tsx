"use client";

import { Panel } from "@/components/ui/panel";

interface FileDiff {
  path: string;
  oldPath?: string;
  status: string;
  additions: number;
  deletions: number;
}

interface SymbolChange {
  symbolId?: string;
  name: string;
  filePath: string;
  changeType: string;
  oldSignature?: string;
  newSignature?: string;
}

interface AffectedRequirement {
  requirementId: string;
  externalId: string;
  title: string;
  affectedLinks: number;
  totalLinks: number;
}

export interface ImpactReportData {
  id: string;
  repositoryId?: string;
  oldCommitSha?: string;
  newCommitSha?: string;
  filesChanged: FileDiff[];
  symbolsAdded: SymbolChange[];
  symbolsModified: SymbolChange[];
  symbolsRemoved: SymbolChange[];
  affectedLinks: { linkId: string; impact: string }[];
  affectedRequirements: AffectedRequirement[];
  staleArtifacts: string[];
  computedAt: string;
}

function statusIcon(status: string) {
  switch (status.toUpperCase()) {
    case "ADDED": return <span className="text-emerald-500">+</span>;
    case "DELETED": return <span className="text-rose-500">-</span>;
    case "MODIFIED": return <span className="text-amber-500">~</span>;
    case "RENAMED": return <span className="text-blue-400">R</span>;
    default: return <span className="text-[var(--text-secondary)]">?</span>;
  }
}

function StatPill({ label, value, color }: { label: string; value: number; color: string }) {
  if (value === 0) return null;
  return (
    <span className={`inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs font-semibold tabular-nums ${color}`}>
      {value} {label}
    </span>
  );
}

export function ImpactReportPanel({ report }: { report: ImpactReportData | null | undefined }) {
  if (!report) {
    return (
      <Panel>
        <p className="text-sm text-[var(--text-secondary)]">
          No impact report available. Reindex the repository to generate one.
        </p>
      </Panel>
    );
  }

  const totalSymbolChanges = report.symbolsAdded.length + report.symbolsModified.length + report.symbolsRemoved.length;
  const commitRange = report.oldCommitSha && report.newCommitSha
    ? `${report.oldCommitSha.slice(0, 7)}..${report.newCommitSha.slice(0, 7)}`
    : null;

  return (
    <div className="space-y-4">
      {/* Summary */}
      <Panel>
        <div className="flex items-center justify-between">
          <h3 className="text-lg font-semibold text-[var(--text-primary)]">
            Change Impact Report
          </h3>
          <span className="text-xs text-[var(--text-secondary)]">
            {new Date(report.computedAt).toLocaleString()}
          </span>
        </div>

        {commitRange && (
          <p className="mt-1 font-mono text-xs text-[var(--text-secondary)]">
            {commitRange}
          </p>
        )}

        <div className="mt-3 flex flex-wrap gap-2">
          <StatPill label="files changed" value={report.filesChanged.length} color="text-blue-400 border-blue-400/40" />
          <StatPill label="added" value={report.symbolsAdded.length} color="text-emerald-500 border-emerald-500/40" />
          <StatPill label="modified" value={report.symbolsModified.length} color="text-amber-500 border-amber-500/40" />
          <StatPill label="removed" value={report.symbolsRemoved.length} color="text-rose-500 border-rose-500/40" />
          <StatPill label="links affected" value={report.affectedLinks.length} color="text-purple-400 border-purple-400/40" />
          <StatPill label="requirements affected" value={report.affectedRequirements.length} color="text-orange-400 border-orange-400/40" />
        </div>
      </Panel>

      {/* Files Changed */}
      {report.filesChanged.length > 0 && (
        <Panel>
          <h4 className="mb-2 text-sm font-semibold text-[var(--text-primary)]">
            Files Changed ({report.filesChanged.length})
          </h4>
          <div className="max-h-48 overflow-y-auto text-xs">
            {report.filesChanged.map((f, i) => (
              <div key={i} className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] py-1.5 last:border-b-0">
                <span className="flex min-w-0 items-center gap-2 font-mono text-[var(--text-primary)]">
                  <span className="shrink-0">{statusIcon(f.status)}</span> <span className="truncate">{f.path}</span>
                </span>
                <span className="flex gap-2 tabular-nums">
                  {f.additions > 0 && <span className="text-emerald-500">+{f.additions}</span>}
                  {f.deletions > 0 && <span className="text-rose-500">-{f.deletions}</span>}
                </span>
              </div>
            ))}
          </div>
        </Panel>
      )}

      {/* Symbol Changes */}
      {totalSymbolChanges > 0 && (
        <Panel>
          <h4 className="mb-2 text-sm font-semibold text-[var(--text-primary)]">
            Symbol Changes ({totalSymbolChanges})
          </h4>
          <div className="max-h-48 overflow-y-auto text-xs">
            {report.symbolsAdded.map((s, i) => (
              <div key={`a-${i}`} className="flex items-center gap-2 border-b border-[var(--border-subtle)] py-1.5 last:border-b-0">
                <span className="rounded bg-emerald-500/10 px-1.5 py-0.5 text-emerald-500">added</span>
                <span className="font-mono text-[var(--text-primary)]">{s.name}</span>
                <span className="text-[var(--text-secondary)]">{s.filePath}</span>
              </div>
            ))}
            {report.symbolsModified.map((s, i) => (
              <div key={`m-${i}`} className="flex items-center gap-2 border-b border-[var(--border-subtle)] py-1.5 last:border-b-0">
                <span className="rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-500">modified</span>
                <span className="font-mono text-[var(--text-primary)]">{s.name}</span>
                <span className="text-[var(--text-secondary)]">{s.filePath}</span>
              </div>
            ))}
            {report.symbolsRemoved.map((s, i) => (
              <div key={`r-${i}`} className="flex items-center gap-2 border-b border-[var(--border-subtle)] py-1.5 last:border-b-0">
                <span className="rounded bg-rose-500/10 px-1.5 py-0.5 text-rose-500">removed</span>
                <span className="font-mono text-[var(--text-primary)]">{s.name}</span>
                <span className="text-[var(--text-secondary)]">{s.filePath}</span>
              </div>
            ))}
          </div>
        </Panel>
      )}

      {/* Affected Requirements */}
      {report.affectedRequirements.length > 0 && (
        <Panel>
          <h4 className="mb-2 text-sm font-semibold text-[var(--text-primary)]">
            Affected Requirements ({report.affectedRequirements.length})
          </h4>
          <div className="max-h-48 overflow-y-auto text-xs">
            {report.affectedRequirements.map((r, i) => (
              <div key={i} className="flex items-center justify-between border-b border-[var(--border-subtle)] py-1.5 last:border-b-0">
                <div>
                  <span className="font-mono text-[var(--text-secondary)]">{r.externalId}</span>
                  <span className="ml-2 text-[var(--text-primary)]">{r.title}</span>
                </div>
                <span className="text-[var(--text-secondary)]">
                  {r.affectedLinks}/{r.totalLinks} links
                </span>
              </div>
            ))}
          </div>
        </Panel>
      )}

      {/* Stale Artifacts */}
      {report.staleArtifacts.length > 0 && (
        <Panel>
          <h4 className="mb-2 text-sm font-semibold text-[var(--text-primary)]">
            Stale Knowledge Artifacts ({report.staleArtifacts.length})
          </h4>
          <p className="text-xs text-[var(--text-secondary)]">
            {report.staleArtifacts.length} knowledge artifact{report.staleArtifacts.length !== 1 ? "s" : ""} may need regeneration after this change.
          </p>
        </Panel>
      )}
    </div>
  );
}
