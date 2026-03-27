"use client";

import { useState } from "react";
import { useMutation } from "urql";
import { DISCUSS_CODE_MUTATION } from "@/lib/graphql/queries";
import { Panel } from "@/components/ui/panel";
import { Button } from "@/components/ui/button";
import { SourceRefLink } from "@/components/source/SourceRefLink";
import { cn } from "@/lib/utils";

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

function statusPill(status: string) {
  switch (status.toUpperCase()) {
    case "ADDED":
      return <span className="shrink-0 rounded bg-emerald-500/10 px-1.5 py-0.5 text-xs font-medium text-emerald-500">New</span>;
    case "DELETED":
      return <span className="shrink-0 rounded bg-rose-500/10 px-1.5 py-0.5 text-xs font-medium text-rose-500">Deleted</span>;
    case "MODIFIED":
      return <span className="shrink-0 rounded bg-amber-500/10 px-1.5 py-0.5 text-xs font-medium text-amber-500">Modified</span>;
    case "RENAMED":
      return <span className="shrink-0 rounded bg-blue-400/10 px-1.5 py-0.5 text-xs font-medium text-blue-400">Renamed</span>;
    default:
      return <span className="shrink-0 rounded bg-[var(--bg-hover)] px-1.5 py-0.5 text-xs font-medium text-[var(--text-secondary)]">{status}</span>;
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

function DeltaBar({ additions, deletions }: { additions: number; deletions: number }) {
  const total = additions + deletions;
  if (total === 0) return null;
  const maxBlocks = 5;
  const addBlocks = total > 0 ? Math.max(1, Math.round((additions / total) * maxBlocks)) : 0;
  const delBlocks = total > 0 ? maxBlocks - addBlocks : 0;
  return (
    <span className="inline-flex items-center gap-0.5">
      <span className="tabular-nums text-xs text-[var(--text-tertiary)]">{additions + deletions}</span>
      <span className="ml-1 inline-flex gap-px">
        {Array.from({ length: addBlocks }).map((_, i) => (
          <span key={`a${i}`} className="inline-block h-2.5 w-1.5 rounded-sm bg-emerald-500" />
        ))}
        {Array.from({ length: delBlocks }).map((_, i) => (
          <span key={`d${i}`} className="inline-block h-2.5 w-1.5 rounded-sm bg-rose-500" />
        ))}
      </span>
    </span>
  );
}

function SymbolRow({
  symbol,
  type,
  repositoryId,
  expanded,
  onToggle,
  explanation,
  loading,
  onExplain,
}: {
  symbol: SymbolChange;
  type: "added" | "modified" | "removed";
  repositoryId: string;
  expanded: boolean;
  onToggle: () => void;
  explanation: string | null;
  loading: boolean;
  onExplain: () => void;
}) {
  const pill = type === "added"
    ? <span className="shrink-0 rounded bg-emerald-500/10 px-1.5 py-0.5 text-xs font-medium text-emerald-500">Added</span>
    : type === "modified"
      ? <span className="shrink-0 rounded bg-amber-500/10 px-1.5 py-0.5 text-xs font-medium text-amber-500">Modified</span>
      : <span className="shrink-0 rounded bg-rose-500/10 px-1.5 py-0.5 text-xs font-medium text-rose-500">Removed</span>;

  return (
    <div className="border-b border-[var(--border-subtle)] last:border-b-0">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-2 py-2 text-left text-xs transition-colors hover:bg-[var(--bg-hover)]"
      >
        <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={cn("shrink-0 text-[var(--text-tertiary)] transition-transform duration-150", expanded && "rotate-90")}><path d="m9 18 6-6-6-6"/></svg>
        {pill}
        {type !== "removed" ? (
          <SourceRefLink
            repositoryId={repositoryId}
            target={{ tab: "files", filePath: symbol.filePath }}
            className="min-w-0 truncate font-mono text-xs"
          >
            {symbol.name}
          </SourceRefLink>
        ) : (
          <span className="min-w-0 truncate font-mono text-[var(--text-primary)]">{symbol.name}</span>
        )}
        <span className="ml-auto shrink-0 font-mono text-xs text-[var(--text-tertiary)]">
          {symbol.filePath.split("/").at(-1)}
        </span>
      </button>
      {expanded && (
        <div className="pb-3 pl-6 pr-2">
          {symbol.oldSignature && symbol.newSignature && symbol.oldSignature !== symbol.newSignature && (
            <div className="mb-2 space-y-1">
              <div className="flex items-start gap-2 font-mono text-xs">
                <span className="shrink-0 text-rose-500">-</span>
                <span className="text-[var(--text-secondary)] line-through">{symbol.oldSignature}</span>
              </div>
              <div className="flex items-start gap-2 font-mono text-xs">
                <span className="shrink-0 text-emerald-500">+</span>
                <span className="text-[var(--text-primary)]">{symbol.newSignature}</span>
              </div>
            </div>
          )}
          {symbol.newSignature && !symbol.oldSignature && (
            <div className="mb-2 font-mono text-xs text-[var(--text-secondary)]">{symbol.newSignature}</div>
          )}
          {explanation ? (
            <div className="mt-2 whitespace-pre-wrap text-xs leading-6 text-[var(--text-secondary)]">{explanation}</div>
          ) : (
            <Button
              variant="secondary"
              size="sm"
              onClick={(e) => { e.stopPropagation(); onExplain(); }}
              disabled={loading}
            >
              {loading ? "Generating..." : "Explain this change"}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

export function ImpactReportPanel({ report, repositoryId }: { report: ImpactReportData | null | undefined; repositoryId?: string }) {
  const repoId = repositoryId || report?.repositoryId || "";
  const [expandedItem, setExpandedItem] = useState<string | null>(null);
  const [explanations, setExplanations] = useState<Record<string, string>>({});
  const [loadingExplain, setLoadingExplain] = useState<string | null>(null);
  const [, discussCode] = useMutation(DISCUSS_CODE_MUTATION);

  // Chat state
  const [chatQuestion, setChatQuestion] = useState("");
  const [chatAnswer, setChatAnswer] = useState<string | null>(null);
  const [chatLoading, setChatLoading] = useState(false);

  async function handleExplainSymbol(key: string, symbol: SymbolChange, type: string) {
    setLoadingExplain(key);
    try {
      const question = type === "added"
        ? `A new symbol "${symbol.name}" was added in ${symbol.filePath}. What does it do and why might it have been added?`
        : type === "removed"
          ? `The symbol "${symbol.name}" was removed from ${symbol.filePath}. What did it do and what might have replaced it?`
          : `The symbol "${symbol.name}" in ${symbol.filePath} was modified${symbol.oldSignature && symbol.newSignature ? ` (signature changed from "${symbol.oldSignature}" to "${symbol.newSignature}")` : ""}. What changed and why might it matter?`;

      const result = await discussCode({
        input: {
          repositoryId: repoId,
          question,
          ...(symbol.symbolId ? { symbolId: symbol.symbolId } : { filePath: symbol.filePath }),
        },
      });
      if (result.data?.discussCode?.answer) {
        setExplanations((prev) => ({ ...prev, [key]: result.data.discussCode.answer }));
      }
    } catch {
      // silently fail
    }
    setLoadingExplain(null);
  }

  async function handleChat() {
    if (!chatQuestion.trim() || chatLoading) return;
    setChatLoading(true);
    setChatAnswer(null);
    try {
      // Build context about the changes
      const changesContext = [
        report?.filesChanged.length ? `Files changed: ${report.filesChanged.map((f) => `${f.path} (${f.status}, +${f.additions}/-${f.deletions})`).join(", ")}` : "",
        report?.symbolsAdded.length ? `Symbols added: ${report.symbolsAdded.map((s) => s.name).join(", ")}` : "",
        report?.symbolsModified.length ? `Symbols modified: ${report.symbolsModified.map((s) => s.name).join(", ")}` : "",
        report?.symbolsRemoved.length ? `Symbols removed: ${report.symbolsRemoved.map((s) => s.name).join(", ")}` : "",
      ].filter(Boolean).join("\n");

      const result = await discussCode({
        input: {
          repositoryId: repoId,
          question: chatQuestion,
          code: changesContext,
        },
      });
      if (result.data?.discussCode?.answer) {
        setChatAnswer(result.data.discussCode.answer);
      }
    } catch {
      setChatAnswer("Unable to get an answer. Please try again.");
    }
    setChatLoading(false);
  }

  if (!report) {
    return (
      <Panel>
        <h3 className="text-lg font-semibold text-[var(--text-primary)]">Change Impact</h3>
        <p className="mt-2 text-sm text-[var(--text-secondary)]">
          Shows what was affected by recent commits: which files changed, which symbols were added, modified, or removed, and which requirements may need attention.
        </p>
        <p className="mt-2 text-sm text-[var(--text-secondary)]">
          No impact report available yet. Reindex the repository to generate one.
        </p>
      </Panel>
    );
  }

  const totalSymbolChanges = report.symbolsAdded.length + report.symbolsModified.length + report.symbolsRemoved.length;
  const commitRange = report.oldCommitSha && report.newCommitSha
    ? `${report.oldCommitSha.slice(0, 7)}..${report.newCommitSha.slice(0, 7)}`
    : null;

  const inputClass =
    "h-10 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)]";

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

      {/* Ask About These Changes */}
      <Panel>
        <h4 className="mb-1 text-sm font-semibold text-[var(--text-primary)]">Ask About These Changes</h4>
        <p className="mb-3 text-xs text-[var(--text-secondary)]">
          Ask questions about what changed, why it might matter, or what to watch out for.
        </p>
        <div className="flex gap-2">
          <input
            type="text"
            value={chatQuestion}
            onChange={(e) => setChatQuestion(e.target.value)}
            placeholder="What changed and why? What should I review first?"
            onKeyDown={(e) => { if (e.key === "Enter") handleChat(); }}
            className={`${inputClass} flex-1`}
          />
          <Button onClick={handleChat} disabled={chatLoading || !chatQuestion.trim()}>
            {chatLoading ? "Thinking..." : "Ask"}
          </Button>
        </div>
        {chatAnswer && (
          <div className="mt-3 whitespace-pre-wrap rounded-[var(--radius-sm)] bg-[var(--bg-surface)] p-3 text-sm leading-7 text-[var(--text-secondary)]">
            {chatAnswer}
          </div>
        )}
      </Panel>

      {/* Files Changed */}
      {report.filesChanged.length > 0 && (
        <Panel>
          <h4 className="mb-2 text-sm font-semibold text-[var(--text-primary)]">
            Files Changed ({report.filesChanged.length})
          </h4>
          <div className="max-h-72 overflow-y-auto">
            {report.filesChanged.map((f, i) => (
              <div key={i} className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] py-2 last:border-b-0">
                <div className="flex min-w-0 items-center gap-2">
                  {statusPill(f.status)}
                  <SourceRefLink
                    repositoryId={repoId}
                    target={{ tab: "files", filePath: f.path }}
                    className="min-w-0 truncate font-mono text-xs"
                  >
                    {f.path}
                  </SourceRefLink>
                  {f.oldPath && f.oldPath !== f.path && (
                    <span className="text-xs text-[var(--text-tertiary)]">from {f.oldPath}</span>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-3">
                  {f.status.toUpperCase() !== "DELETED" && (f.additions > 0 || f.deletions > 0) && (
                    <span className="flex gap-1.5 tabular-nums text-xs">
                      {f.additions > 0 && <span className="text-emerald-500">+{f.additions}</span>}
                      {f.deletions > 0 && <span className="text-rose-500">-{f.deletions}</span>}
                    </span>
                  )}
                  <DeltaBar additions={f.additions} deletions={f.deletions} />
                </div>
              </div>
            ))}
          </div>
        </Panel>
      )}

      {/* Symbol Changes */}
      {totalSymbolChanges > 0 && (
        <Panel>
          <h4 className="mb-1 text-sm font-semibold text-[var(--text-primary)]">
            Symbol Changes ({totalSymbolChanges})
          </h4>
          <p className="mb-2 text-xs text-[var(--text-secondary)]">
            Expand a symbol to see signature changes and generate an explanation.
          </p>
          <div className="max-h-[28rem] overflow-y-auto">
            {report.symbolsAdded.map((s, i) => {
              const key = `a-${i}`;
              return (
                <SymbolRow
                  key={key}
                  symbol={s}
                  type="added"
                  repositoryId={repoId}
                  expanded={expandedItem === key}
                  onToggle={() => setExpandedItem(expandedItem === key ? null : key)}
                  explanation={explanations[key] || null}
                  loading={loadingExplain === key}
                  onExplain={() => handleExplainSymbol(key, s, "added")}
                />
              );
            })}
            {report.symbolsModified.map((s, i) => {
              const key = `m-${i}`;
              return (
                <SymbolRow
                  key={key}
                  symbol={s}
                  type="modified"
                  repositoryId={repoId}
                  expanded={expandedItem === key}
                  onToggle={() => setExpandedItem(expandedItem === key ? null : key)}
                  explanation={explanations[key] || null}
                  loading={loadingExplain === key}
                  onExplain={() => handleExplainSymbol(key, s, "modified")}
                />
              );
            })}
            {report.symbolsRemoved.map((s, i) => {
              const key = `r-${i}`;
              return (
                <SymbolRow
                  key={key}
                  symbol={s}
                  type="removed"
                  repositoryId={repoId}
                  expanded={expandedItem === key}
                  onToggle={() => setExpandedItem(expandedItem === key ? null : key)}
                  explanation={explanations[key] || null}
                  loading={loadingExplain === key}
                  onExplain={() => handleExplainSymbol(key, s, "removed")}
                />
              );
            })}
          </div>
        </Panel>
      )}

      {/* Affected Requirements */}
      {report.affectedRequirements.length > 0 && (
        <Panel>
          <h4 className="mb-2 text-sm font-semibold text-[var(--text-primary)]">
            Affected Requirements ({report.affectedRequirements.length})
          </h4>
          <div className="max-h-48 overflow-y-auto">
            {report.affectedRequirements.map((req, i) => (
              <div key={i} className="flex items-center justify-between border-b border-[var(--border-subtle)] py-2 last:border-b-0">
                <div className="flex min-w-0 items-center gap-2">
                  <a
                    href={`/requirements/${req.requirementId}`}
                    className="font-mono text-xs text-[var(--accent-primary)] underline decoration-[color:var(--accent-quiet)] underline-offset-4 hover:text-[var(--accent-primary-strong)]"
                  >
                    {req.externalId}
                  </a>
                  <span className="min-w-0 truncate text-xs text-[var(--text-primary)]">{req.title}</span>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <span className="text-xs tabular-nums text-[var(--text-secondary)]">
                    {req.affectedLinks}/{req.totalLinks} links
                  </span>
                  {req.affectedLinks === req.totalLinks && (
                    <span className="rounded bg-rose-500/10 px-1.5 py-0.5 text-xs font-medium text-rose-500">All links affected</span>
                  )}
                </div>
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
