"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery } from "urql";
import {
  ARCHITECTURE_DIAGRAM_QUERY,
  GENERATE_ARCHITECTURE_DIAGRAM_MUTATION,
  KNOWLEDGE_ARTIFACTS_QUERY,
  REFRESH_KNOWLEDGE_ARTIFACT_MUTATION,
} from "@/lib/graphql/queries";
import { Panel } from "@/components/ui/panel";
import { Button } from "@/components/ui/button";

interface DiagramEdge {
  targetPath: string;
  callCount: number;
}

interface DiagramModuleNode {
  path: string;
  symbolCount: number;
  fileCount: number;
  requirementLinkCount: number;
  inboundEdgeCount: number;
  outboundEdges: DiagramEdge[];
}

interface ArchitectureDiagramProps {
  repositoryId: string;
  onModuleClick?: (modulePath: string) => void;
}

type DiagramViewMode = "DETERMINISTIC" | "AI";
type AIDiagramFocus = "SYSTEM" | "EXECUTION";

interface AIDiagramSection {
  content: string;
  summary?: string | null;
  metadata?: string | null;
}

interface AIDiagramArtifact {
  id: string;
  type: string;
  status: string;
  progress?: number | null;
  progressMessage?: string | null;
  stale?: boolean | null;
  errorMessage?: string | null;
  refreshAvailable?: boolean | null;
  understandingId?: string | null;
  updatedAt: string;
  sections?: AIDiagramSection[] | null;
}

interface AIDiagramComponent {
  id: string;
  label: string;
  kind?: string;
  modulePaths?: string[];
}

function parseArchitectureMetadata(metadata?: string | null): {
  validationStatus?: string;
  repairSummary?: string;
  inferredEdges?: string[];
  contradictoryEdges?: string[];
  graphAlignmentStatus?: string;
  generationStrategy?: string;
  components?: AIDiagramComponent[];
  executionMermaidSource?: string;
  executionSummary?: string;
  systemSummary?: string;
} {
  if (!metadata) return {};
  try {
    const parsed = JSON.parse(metadata);
    return {
      validationStatus: parsed.validation_status,
      repairSummary: parsed.repair_summary,
      inferredEdges: Array.isArray(parsed.inferred_edges) ? parsed.inferred_edges : [],
      contradictoryEdges: Array.isArray(parsed.contradictory_edges) ? parsed.contradictory_edges : [],
      graphAlignmentStatus: parsed.graph_alignment_status,
      generationStrategy: parsed.generation_strategy,
      components: Array.isArray(parsed.components) ? parsed.components : [],
      executionMermaidSource: parsed.execution_mermaid_source,
      executionSummary: parsed.execution_summary,
      systemSummary: parsed.system_summary,
    };
  } catch {
    return {};
  }
}

export function ArchitectureDiagram({
  repositoryId,
  onModuleClick,
}: ArchitectureDiagramProps) {
  const [viewMode, setViewMode] = useState<DiagramViewMode>("DETERMINISTIC");
  const [aiFocus, setAIFocus] = useState<AIDiagramFocus>("SYSTEM");
  const [level, setLevel] = useState<"MODULE" | "FILE">("MODULE");
  const [moduleFilter, setModuleFilter] = useState<string | null>(null);
  const [moduleDepth, setModuleDepth] = useState(1);
  const [selectedAIComponentId, setSelectedAIComponentId] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [renderError, setRenderError] = useState<string | null>(null);
  const [structuredMermaid, setStructuredMermaid] = useState<string | null>(null);
  const [aiRefreshStartedAt, setAIRefreshStartedAt] = useState<number | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  const [deterministicResult, reexecuteDeterministicQuery] = useQuery({
    query: ARCHITECTURE_DIAGRAM_QUERY,
    variables: {
      repoId: repositoryId,
      level,
      moduleFilter,
      moduleDepth,
    },
  });

  const [artifactsResult, reexecuteArtifactsQuery] = useQuery({
    query: KNOWLEDGE_ARTIFACTS_QUERY,
    variables: {
      repositoryId,
    },
  });

  const [, generateArchitectureDiagram] = useMutation(GENERATE_ARCHITECTURE_DIAGRAM_MUTATION);
  const [, refreshArtifact] = useMutation(REFRESH_KNOWLEDGE_ARTIFACT_MUTATION);

  const deterministicDiagram = deterministicResult.data?.architectureDiagram;
  const aiArtifact = useMemo(() => {
    const artifacts: AIDiagramArtifact[] = artifactsResult.data?.knowledgeArtifacts ?? [];
    return artifacts
      .filter((artifact) => artifact.type === "ARCHITECTURE_DIAGRAM")
      .sort((a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime())[0] ?? null;
  }, [artifactsResult.data]);
  const aiSection = aiArtifact?.sections?.[0] ?? null;
  const aiMetadata = parseArchitectureMetadata(aiSection?.metadata);
  const aiComponents = useMemo(() => aiMetadata.components ?? [], [aiMetadata.components]);
  const selectedAIComponent = aiComponents.find((component) => component.id === selectedAIComponentId) ?? null;
  const aiArtifactInFlight = aiArtifact?.status === "PENDING" || aiArtifact?.status === "GENERATING";
  const aiKickoffPending =
    aiRefreshStartedAt !== null &&
    (!aiArtifact ||
      (!aiArtifactInFlight && new Date(aiArtifact.updatedAt).getTime() < aiRefreshStartedAt));

  const aiSystemMermaidSource = aiSection?.content ?? "";
  const aiExecutionMermaidSource = aiMetadata.executionMermaidSource ?? "";
  const currentMermaidSource =
    viewMode === "AI"
      ? (aiFocus === "EXECUTION" ? aiExecutionMermaidSource : aiSystemMermaidSource)
      : structuredMermaid || deterministicDiagram?.mermaidSource || "";
  const currentAICaption =
    aiFocus === "EXECUTION"
      ? aiMetadata.executionSummary || "This view follows the request-to-worker execution path and the supporting systems around it."
      : aiMetadata.systemSummary || aiSection?.summary || "This view highlights the main system context for the repository.";

  const handleNodeClick = useCallback(
    (nodePath: string) => {
      if (level === "MODULE") {
        setModuleFilter(nodePath);
        setLevel("FILE");
      } else {
        onModuleClick?.(nodePath);
      }
    },
    [level, onModuleClick],
  );

  const handleOpenComponentModule = useCallback((modulePath: string) => {
    setViewMode("DETERMINISTIC");
    setLevel("FILE");
    setModuleFilter(modulePath);
  }, []);

  useEffect(() => {
    if (viewMode !== "DETERMINISTIC") {
      return;
    }
    let cancelled = false;

    (async () => {
      try {
        const params = new URLSearchParams({ depth: String(moduleDepth) });
        const response = await fetch(`/api/v1/diagrams/${repositoryId}/export/mermaid?${params.toString()}`);
        if (!response.ok) {
          throw new Error("Failed to load structured Mermaid");
        }
        const mermaid = await response.text();
        if (!cancelled) {
          setStructuredMermaid(mermaid);
        }
      } catch {
        if (!cancelled) {
          setStructuredMermaid(null);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [moduleDepth, moduleFilter, repositoryId, viewMode]);

  useEffect(() => {
    if (!currentMermaidSource || !containerRef.current) return;
    let cancelled = false;

    (async () => {
      try {
        setRenderError(null);
        const mermaid = (await import("mermaid")).default;
        mermaid.initialize({
          startOnLoad: false,
          theme: "dark",
          flowchart: { curve: "basis", padding: 16 },
          securityLevel: "loose",
        });

        if (cancelled) return;
        const id = `arch-${repositoryId.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8)}-${Date.now()}`;
        const { svg } = await mermaid.render(id, currentMermaidSource);
        if (!cancelled && containerRef.current) {
          containerRef.current.innerHTML = svg;
          if (viewMode === "DETERMINISTIC") {
            containerRef.current.querySelectorAll<HTMLElement>(".node").forEach((node) => {
              node.style.cursor = "pointer";
              node.addEventListener("click", () => {
                const nodeId = node.id;
                const path = nodeId.replace(/^flowchart-/, "").replace(/-\d+$/, "").replace(/_/g, "/");
                handleNodeClick(path);
              });
            });
          } else if (viewMode === "AI") {
            containerRef.current.querySelectorAll<HTMLElement>(".node").forEach((node) => {
              const nodeId = node.id.replace(/^flowchart-/, "").replace(/-\d+$/, "");
              const component = aiComponents.find((entry) => entry.id === nodeId);
              if (!component) {
                return;
              }
              node.style.cursor = "pointer";
              node.addEventListener("click", () => {
                setSelectedAIComponentId(component.id);
              });
            });
          }
        }
      } catch (error) {
        if (!cancelled && containerRef.current) {
          containerRef.current.innerHTML = "";
          setRenderError(error instanceof Error ? error.message : "Failed to render Mermaid diagram.");
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [aiComponents, currentMermaidSource, repositoryId, viewMode, handleNodeClick]);

  const handleCopyMermaid = useCallback(() => {
    if (!currentMermaidSource) return;
    navigator.clipboard.writeText(currentMermaidSource);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }, [currentMermaidSource]);

  const handleBackToModules = useCallback(() => {
    setLevel("MODULE");
    setModuleFilter(null);
  }, []);

  useEffect(() => {
    setSelectedAIComponentId(null);
  }, [aiArtifact?.id, aiFocus]);

  useEffect(() => {
    if (aiRefreshStartedAt === null) {
      return;
    }
    if (aiArtifactInFlight) {
      setAIRefreshStartedAt(null);
      return;
    }
    if (aiArtifact && new Date(aiArtifact.updatedAt).getTime() >= aiRefreshStartedAt) {
      setAIRefreshStartedAt(null);
    }
  }, [aiArtifact, aiArtifactInFlight, aiRefreshStartedAt]);

  useEffect(() => {
    if (!aiKickoffPending && !aiArtifactInFlight) {
      return;
    }
    const interval = window.setInterval(() => {
      reexecuteArtifactsQuery({ requestPolicy: "network-only" });
    }, 2000);
    return () => window.clearInterval(interval);
  }, [aiArtifactInFlight, aiKickoffPending, reexecuteArtifactsQuery]);

  const handleGenerateAIDiagram = useCallback(async () => {
    const startedAt = Date.now();
    setAIRefreshStartedAt(startedAt);
    if (aiArtifact?.id) {
      await refreshArtifact({ id: aiArtifact.id });
    } else {
      await generateArchitectureDiagram({
        input: {
          repositoryId,
          audience: "DEVELOPER",
          depth: "MEDIUM",
        },
      });
    }
    reexecuteArtifactsQuery({ requestPolicy: "network-only" });
    reexecuteDeterministicQuery({ requestPolicy: "network-only" });
  }, [aiArtifact?.id, generateArchitectureDiagram, refreshArtifact, reexecuteArtifactsQuery, reexecuteDeterministicQuery, repositoryId]);

  if (deterministicResult.fetching && !deterministicDiagram) {
    return (
      <Panel>
        <div className="flex h-64 items-center justify-center text-sm text-[var(--text-secondary)]">
          Generating architecture diagram...
        </div>
      </Panel>
    );
  }

  if (!deterministicDiagram || deterministicResult.error) {
    return (
      <Panel>
        <div className="flex h-64 items-center justify-center text-sm text-[var(--text-secondary)]">
          {deterministicResult.error
            ? "Failed to generate deterministic diagram."
            : "No symbol graph available. Index this repository first."}
        </div>
      </Panel>
    );
  }

  const aiBusy = aiKickoffPending || aiArtifactInFlight;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="inline-flex rounded-full border border-[var(--border-default)] bg-[var(--bg-surface)] p-1">
          <button
            type="button"
            onClick={() => setViewMode("DETERMINISTIC")}
            className={`rounded-full px-3 py-1.5 text-xs font-medium ${viewMode === "DETERMINISTIC" ? "bg-[var(--accent-primary)] text-[var(--accent-contrast)]" : "text-[var(--text-secondary)]"}`}
          >
            Graph
          </button>
          <button
            type="button"
            onClick={() => setViewMode("AI")}
            className={`rounded-full px-3 py-1.5 text-xs font-medium ${viewMode === "AI" ? "bg-[var(--accent-primary)] text-[var(--accent-contrast)]" : "text-[var(--text-secondary)]"}`}
          >
            AI Diagram
          </button>
        </div>
        {viewMode === "DETERMINISTIC" && level === "FILE" && (
          <Button variant="ghost" size="sm" onClick={handleBackToModules}>
            Back to Modules
          </Button>
        )}
        {viewMode === "DETERMINISTIC" && level === "FILE" && moduleFilter && (
          <span className="text-xs font-medium text-[var(--text-primary)]">{moduleFilter}</span>
        )}
        <div className="flex-1" />
        {viewMode === "DETERMINISTIC" && deterministicDiagram.truncated && (
          <span className="text-xs text-[var(--text-tertiary)]">
            Showing {deterministicDiagram.shownModules} of {deterministicDiagram.totalModules}
          </span>
        )}
        {viewMode === "DETERMINISTIC" && (
          <label className="flex items-center gap-2 text-xs text-[var(--text-secondary)]">
            Depth
            <select
              value={moduleDepth}
              onChange={(e) => setModuleDepth(Number(e.target.value))}
              className="rounded border border-[var(--border-default)] bg-[var(--bg-surface)] px-2 py-1 text-xs"
            >
              {[1, 2, 3].map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
          </label>
        )}
        {viewMode === "AI" && (
          <Button variant="secondary" size="sm" onClick={handleGenerateAIDiagram}>
            {aiBusy ? "Generating…" : aiArtifact?.id ? "Regenerate AI Diagram" : "Generate AI Diagram"}
          </Button>
        )}
        <Button variant="secondary" size="sm" onClick={handleCopyMermaid} disabled={!currentMermaidSource}>
          {copied ? "Copied!" : "Copy Mermaid"}
        </Button>
      </div>

      {viewMode === "AI" && (
        <Panel>
          {!aiArtifact ? (
            <div className="text-sm text-[var(--text-secondary)]">
              Generate the AI diagram to compare the grounded deterministic graph with an understanding-first Mermaid view.
            </div>
          ) : aiBusy ? (
            <div className="space-y-2 text-sm text-[var(--text-secondary)]">
              <div>
                {aiKickoffPending
                  ? "Starting AI architecture diagram regeneration..."
                  : aiArtifact.progressMessage || "Generating AI architecture diagram..."}
              </div>
              <div>{aiKickoffPending ? "Queued…" : `${Math.round((aiArtifact.progress || 0) * 100)}%`}</div>
            </div>
          ) : aiArtifact.status === "FAILED" ? (
            <div className="space-y-2 text-sm text-[var(--text-secondary)]">
              <div>AI diagram generation failed.</div>
              {aiArtifact.errorMessage && <div>{aiArtifact.errorMessage}</div>}
            </div>
          ) : (
            <div className="space-y-3 text-sm text-[var(--text-secondary)]">
              <div className="flex flex-wrap items-center gap-2">
                <div className="inline-flex rounded-full border border-[var(--border-default)] bg-[var(--bg-surface)] p-1">
                  <button
                    type="button"
                    onClick={() => setAIFocus("SYSTEM")}
                    className={`rounded-full px-3 py-1.5 text-xs font-medium ${aiFocus === "SYSTEM" ? "bg-[var(--accent-primary)] text-[var(--accent-contrast)]" : "text-[var(--text-secondary)]"}`}
                  >
                    System View
                  </button>
                  <button
                    type="button"
                    onClick={() => setAIFocus("EXECUTION")}
                    className={`rounded-full px-3 py-1.5 text-xs font-medium ${aiFocus === "EXECUTION" ? "bg-[var(--accent-primary)] text-[var(--accent-contrast)]" : "text-[var(--text-secondary)]"}`}
                    disabled={!aiExecutionMermaidSource}
                    title={!aiExecutionMermaidSource ? "Regenerate the AI diagram to generate the execution view." : undefined}
                  >
                    Execution View
                  </button>
                </div>
                <span className="rounded-full border border-[var(--border-default)] px-2.5 py-1 text-xs text-[var(--text-primary)]">
                  {aiMetadata.generationStrategy === "fallback"
                    ? "Deterministic fallback"
                    : aiMetadata.generationStrategy === "repaired"
                      ? "Repaired AI output"
                      : "Model-generated"}
                </span>
                {aiMetadata.validationStatus && (
                  <span className="rounded-full border border-[var(--border-default)] px-2.5 py-1 text-xs text-[var(--text-primary)]">
                    Validation: {aiMetadata.validationStatus}
                  </span>
                )}
                {aiMetadata.graphAlignmentStatus ? (
                  <span className="rounded-full border border-[var(--border-default)] px-2.5 py-1 text-xs text-[var(--text-primary)]">
                    {aiMetadata.graphAlignmentStatus === "contradictory"
                      ? "Graph contradiction"
                      : aiMetadata.graphAlignmentStatus === "inferred"
                        ? "Contains inferred structure"
                        : "Graph-aligned"}
                  </span>
                ) : null}
                {aiMetadata.inferredEdges && aiMetadata.inferredEdges.length > 0 && (
                  <span className="rounded-full border border-[var(--border-default)] px-2.5 py-1 text-xs text-[var(--text-primary)]">
                    Inferred edges: {aiMetadata.inferredEdges.length}
                  </span>
                )}
                {aiMetadata.contradictoryEdges && aiMetadata.contradictoryEdges.length > 0 && (
                  <span className="rounded-full border border-[var(--color-error,#ef4444)] px-2.5 py-1 text-xs text-[var(--color-error,#ef4444)]">
                    Contradictory edges: {aiMetadata.contradictoryEdges.length}
                  </span>
                )}
              </div>
              <div className="text-[var(--text-primary)]">{currentAICaption}</div>
              {aiMetadata.repairSummary && <div>Repair: {aiMetadata.repairSummary}</div>}
              {aiMetadata.contradictoryEdges && aiMetadata.contradictoryEdges.length > 0 ? (
                <div className="text-[var(--color-error,#ef4444)]">
                  Structural warning: some AI edges contradict the deterministic system view.
                </div>
              ) : null}
              {aiArtifact.understandingId && <div>Backed by repository understanding.</div>}
              {aiComponents.length > 0 ? (
                <div className="text-xs text-[var(--text-tertiary)]">
                  Click an AI diagram box to inspect the repo areas it is grounded in.
                </div>
              ) : null}
            </div>
          )}
        </Panel>
      )}

      <Panel className="overflow-auto">
        {renderError ? (
          <div className="flex min-h-[180px] items-center justify-center text-sm text-[var(--text-secondary)]">
            {renderError}
          </div>
        ) : currentMermaidSource ? (
          <div
            ref={containerRef}
            className="min-h-[400px] w-full [&_svg]:mx-auto [&_svg]:max-h-[70vh]"
            data-testid="architecture-diagram"
          />
        ) : (
          <div className="flex h-64 items-center justify-center text-sm text-[var(--text-secondary)]">
            {viewMode === "AI" ? "No AI diagram generated yet." : "No diagram available."}
          </div>
        )}
      </Panel>

      {viewMode === "AI" && selectedAIComponent ? (
        <Panel>
          <div className="space-y-3 text-sm text-[var(--text-secondary)]">
            <div className="flex flex-wrap items-center gap-2">
              <h4 className="text-sm font-medium text-[var(--text-primary)]">{selectedAIComponent.label}</h4>
              {selectedAIComponent.kind ? (
                <span className="rounded-full border border-[var(--border-default)] px-2.5 py-1 text-xs text-[var(--text-primary)]">
                  {selectedAIComponent.kind}
                </span>
              ) : null}
            </div>
            {selectedAIComponent.modulePaths && selectedAIComponent.modulePaths.length > 0 ? (
              <>
                <div>Grounded in these repo areas:</div>
                <div className="flex flex-wrap gap-2">
                  {selectedAIComponent.modulePaths.map((modulePath) => (
                    <button
                      key={modulePath}
                      type="button"
                      onClick={() => handleOpenComponentModule(modulePath)}
                      className="rounded-full border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 py-1 text-xs font-mono text-[var(--text-primary)] hover:bg-[var(--bg-hover)]"
                    >
                      {modulePath}
                    </button>
                  ))}
                </div>
              </>
            ) : (
              <div>This component is conceptual and does not map to a local module path.</div>
            )}
          </div>
        </Panel>
      ) : null}

      {viewMode === "DETERMINISTIC" && deterministicDiagram.modules.length > 0 && (
        <Panel>
          <h4 className="mb-3 text-sm font-medium text-[var(--text-primary)]">
            {level === "MODULE" ? "Modules" : "Files"} ({deterministicDiagram.modules.length})
          </h4>
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr className="border-b border-[var(--border-subtle)] text-[var(--text-secondary)]">
                  <th className="pb-2 pr-4">Path</th>
                  <th className="pb-2 pr-4 text-right">Symbols</th>
                  <th className="pb-2 pr-4 text-right">Files</th>
                  <th className="pb-2 pr-4 text-right">Req. Links</th>
                  <th className="pb-2 pr-4 text-right">Inbound</th>
                  <th className="pb-2 text-right">Outbound</th>
                </tr>
              </thead>
              <tbody>
                {deterministicDiagram.modules.map((mod: DiagramModuleNode) => (
                  <tr
                    key={mod.path}
                    className="cursor-pointer border-b border-[var(--border-subtle)] hover:bg-[var(--bg-hover)]"
                    onClick={() => handleNodeClick(mod.path)}
                  >
                    <td className="py-1.5 pr-4 font-mono">{mod.path}</td>
                    <td className="py-1.5 pr-4 text-right">{mod.symbolCount}</td>
                    <td className="py-1.5 pr-4 text-right">{mod.fileCount}</td>
                    <td className="py-1.5 pr-4 text-right">{mod.requirementLinkCount}</td>
                    <td className="py-1.5 pr-4 text-right">{mod.inboundEdgeCount}</td>
                    <td className="py-1.5 text-right">{mod.outboundEdges.length}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      )}
    </div>
  );
}
