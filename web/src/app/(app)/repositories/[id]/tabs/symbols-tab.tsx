"use client";

import { useState, useEffect, useMemo } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useQuery, useMutation } from "urql";
import {
  KNOWLEDGE_ARTIFACTS_QUERY,
  KNOWLEDGE_SCOPE_CHILDREN_QUERY,
  GENERATE_CLIFF_NOTES_MUTATION,
  REFRESH_KNOWLEDGE_ARTIFACT_MUTATION,
  DISCUSS_CODE_MUTATION,
} from "@/lib/graphql/queries";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Panel } from "@/components/ui/panel";
import { SourceRefLink } from "@/components/source/SourceRefLink";
import { SourceViewerPane } from "@/components/source/SourceViewerPane";
import { EnterpriseSourcePanel } from "@/components/source/EnterpriseSourcePanel";
import { SymbolTree } from "@/components/source/SymbolTree";
import { SymbolList } from "@/components/source/SymbolList";
import { kindBadgeClass, kindLabel, SYMBOL_KINDS } from "@/components/source/symbol-kind";
import { cn } from "@/lib/utils";
import { sourceTargetFromSearchParams, type SourceTarget } from "@/lib/source-target";

// ---------------------------------------------------------------------------
// Types (mirrored from page.tsx — shared types will be extracted to a shared
// module when the full split is complete)
// ---------------------------------------------------------------------------

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

interface CliffNotesSectionMetadata {
  section_key?: string | null;
  refinement_tier?: string | null;
  refined_with_evidence?: boolean | null;
  evidence_revision_fp?: string | null;
  renderer_version?: string | null;
  understanding_id?: string | null;
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
  sections: KnowledgeSection[];
}

interface ScopeChild {
  scopeType: string;
  label: string;
  scopePath: string;
  hasArtifact: boolean;
  summary: string | null;
}

interface SymbolChatMessage {
  role: "user" | "assistant";
  text: string;
}

type SymbolDetailTab = "source" | "cliff-notes" | "chat";

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface SymbolsTabProps {
  repoId: string;
  /** True when this tab is the currently visible tab. Gates polling effects. */
  active?: boolean;
  symbols: SymbolNode[];
  symbolsTotalCount: number | null;
  symbolQuery: string;
  setSymbolQuery: (q: string) => void;
  selectedSymbol: string | null;
  setSelectedSymbol: (id: string | null) => void;
  symbolKindFilter: string | null;
  setSymbolKindFilter: (k: string | null) => void;
  symbolScopedAnalysisEnabled: boolean;
  knowledgeLoading: boolean;
  startLoading: (op: string) => void;
  finishLoading: (op: string) => void;
  isLoading: (op: string) => boolean;
  repoGenerationModeDefault: string;
}

// ---------------------------------------------------------------------------
// SymbolsTab component
// ---------------------------------------------------------------------------

export function SymbolsTab({
  repoId,
  active = true,
  symbols,
  symbolsTotalCount,
  symbolQuery,
  setSymbolQuery,
  selectedSymbol,
  setSelectedSymbol,
  symbolKindFilter,
  setSymbolKindFilter,
  symbolScopedAnalysisEnabled,
  knowledgeLoading,
  startLoading,
  finishLoading,
  isLoading: _isLoading,
  repoGenerationModeDefault,
}: SymbolsTabProps) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  // Local state owned by this tab
  const [symbolView, setSymbolView] = useState<"list" | "tree">("list");
  const [symbolDetailTab, setSymbolDetailTab] = useState<SymbolDetailTab>("source");
  const [symbolChatQuestion, setSymbolChatQuestion] = useState("");
  const [symbolChatByScope, setSymbolChatByScope] = useState<Record<string, SymbolChatMessage[]>>({});
  const [expandedSection, setExpandedSection] = useState<string | null>(null);

  // URL-derived state
  const sourceTarget: SourceTarget | null = useMemo(
    () => sourceTargetFromSearchParams(new URLSearchParams(searchParams.toString())),
    [searchParams],
  );
  const selectedFilePath = sourceTarget?.filePath ?? null;

  // Mutations
  const [, generateCliffNotes] = useMutation(GENERATE_CLIFF_NOTES_MUTATION);
  const [, refreshArtifact] = useMutation(REFRESH_KNOWLEDGE_ARTIFACT_MUTATION);
  const [, discussCode] = useMutation(DISCUSS_CODE_MUTATION);

  // Derived symbol state
  const selectedSymbolNode: SymbolNode | null =
    selectedSymbol && symbols.length > 0
      ? (symbols.find((sym) => sym.id === selectedSymbol) ?? null)
      : null;
  const symbolScopeType = selectedSymbolNode ? "SYMBOL" : selectedFilePath ? "FILE" : null;
  const symbolScopePath = selectedSymbolNode
    ? `${selectedSymbolNode.filePath}#${selectedSymbolNode.name}`
    : selectedFilePath || "";
  const selectedSymbolFilePath = selectedSymbolNode?.filePath || selectedFilePath || null;

  // Scoped knowledge queries
  const [symbolKnowledgeResult, reexecuteSymbolKnowledge] = useQuery({
    query: KNOWLEDGE_ARTIFACTS_QUERY,
    variables: symbolScopeType
      ? { repositoryId: repoId, scopeType: symbolScopeType, scopePath: symbolScopePath }
      : undefined,
    pause: !symbolScopedAnalysisEnabled || !symbolScopeType,
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
    pause: !symbolScopedAnalysisEnabled || !selectedSymbolFilePath,
  });

  // Derived artifact state
  const symbolKnowledgeArtifacts: KnowledgeArtifact[] =
    symbolKnowledgeResult.data?.knowledgeArtifacts || [];
  const hasGeneratingScopedArtifact = symbolKnowledgeResult.data?.knowledgeArtifacts?.some(
    (a: KnowledgeArtifact) => a.status === "GENERATING" || a.status === "PENDING",
  );
  const currentScopedCliffNotes = symbolKnowledgeArtifacts.find(
    (a) => a.type === "CLIFF_NOTES" && a.audience === "DEVELOPER" && a.depth === "MEDIUM",
  );
  const scopedArtifactNeedsImpactRefresh =
    currentScopedCliffNotes?.scope.scopeType === "SYMBOL" &&
    currentScopedCliffNotes.status === "READY" &&
    !currentScopedCliffNotes.sections.some((section) => section.title === "Impact Analysis");
  const symbolHasReadyArtifactPaths = new Set<string>(
    (symbolChildrenResult.data?.knowledgeScopeChildren || [])
      .filter((child: ScopeChild) => child.hasArtifact)
      .map((child: ScopeChild) => String(child.scopePath)),
  );

  // Chat scoping
  const symbolChatScopeKey = symbolScopeType ? `${symbolScopeType}:${symbolScopePath}` : "none";
  const symbolChatMessages = symbolChatByScope[symbolChatScopeKey] || [];

  // Reset detail tab and chat input when scope changes
  useEffect(() => {
    setSymbolDetailTab("source");
    setSymbolChatQuestion("");
  }, [symbolScopeType, symbolScopePath]);

  // Poll while artifacts are generating — gated on `active` so the interval
  // pauses when the user switches to another tab. State is preserved because
  // this component stays mounted (hidden via the wrapper's `hidden` attribute).
  useEffect(() => {
    if (!active || !hasGeneratingScopedArtifact) return;
    const interval = setInterval(() => {
      reexecuteSymbolKnowledge({ requestPolicy: "network-only" });
      reexecuteSymbolChildren({ requestPolicy: "network-only" });
    }, 2000);
    return () => clearInterval(interval);
  }, [active, hasGeneratingScopedArtifact, reexecuteSymbolKnowledge, reexecuteSymbolChildren]);

  // Handlers
  function openSource(target: SourceTarget) {
    const next = new URLSearchParams(searchParams.toString());
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
    router.replace(`${pathname}?${next.toString()}`, { scroll: false });
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
        `${message.role === "user" ? "User" : "Assistant"}: ${message.text}`,
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

  // Style helpers (local to this component)
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const inputClass =
    "rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-2 text-sm text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] focus:border-[var(--accent-primary)] focus:outline-none";
  const listContainerClass = "max-h-[60vh] overflow-y-auto";
  const artifactStatusClass =
    "rounded-full border border-[var(--border-default)] bg-[var(--bg-hover)] px-2.5 py-1 text-xs text-[var(--text-secondary)]";
  const confidenceClass = (confidence: string) =>
    cn(
      "rounded-full px-1.5 py-0.5 text-xs text-white",
      confidence === "HIGH"
        ? "bg-[var(--confidence-high)]"
        : confidence === "MEDIUM"
          ? "bg-[var(--confidence-medium)]"
          : "bg-[var(--confidence-low)]",
    );

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

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(20rem,28rem)_minmax(0,1fr)]">
      <div>
        {/* Search + view toggle row */}
        <div className="mb-3 flex items-center gap-3">
          <Input
            type="text"
            value={symbolQuery}
            onChange={(e) => setSymbolQuery(e.target.value)}
            placeholder="Search symbols..."
            className="min-w-0 flex-1"
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
                    : "text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]",
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
                : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]",
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
                  : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]",
              )}
            >
              {k.label}
            </button>
          ))}
        </div>

        <Panel>
          <h3 className="mb-4 text-lg font-semibold text-[var(--text-primary)]">
            Symbols ({symbolsTotalCount ?? "..."})
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
                    {selectedSymbolNode
                      ? selectedSymbolNode.name
                      : selectedFilePath
                        ? selectedFilePath.split("/").at(-1)
                        : "Select a symbol"}
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
                          : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]",
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
                        ? "Generate or reuse a cached cliff notes for this symbol. Impact analysis is included once the symbol guide is up to date."
                        : "Generate or reuse a cached cliff notes for this file."}
                    </p>
                  </div>
                  {!currentScopedCliffNotes ? (
                    <div className="rounded-[var(--radius-sm)] border border-dashed border-[var(--border-default)] bg-[var(--bg-surface)] p-5">
                      <p className="text-sm font-medium text-[var(--text-primary)]">No scoped Cliff Notes yet.</p>
                      <p className="mt-2 text-sm text-[var(--text-secondary)]">
                        Generate an indexed cliff notes for this {selectedSymbolNode ? "symbol" : "file"} to get purpose, local context, and safe-change guidance in one place.
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
                            Start with a concrete question like &ldquo;What would I verify before changing this?&rdquo; or &ldquo;Which callers are most exposed if I edit this symbol?&rdquo;
                          </p>
                        ) : (
                          symbolChatMessages.map((message, index) => (
                            <div
                              key={`${message.role}-${index}`}
                              className={cn(
                                "rounded-[var(--radius-sm)] px-4 py-3 text-sm leading-7",
                                message.role === "user"
                                  ? "bg-[var(--bg-surface)] text-[var(--text-primary)]"
                                  : "border border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)]",
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
                        <Input
                          type="text"
                          value={symbolChatQuestion}
                          onChange={(e) => setSymbolChatQuestion(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter") {
                              void handleScopedFollowUp();
                            }
                          }}
                          placeholder={selectedSymbolNode ? `Ask about ${selectedSymbolNode.name}...` : "Ask about this file..."}
                          className="flex-1"
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
  );
}
