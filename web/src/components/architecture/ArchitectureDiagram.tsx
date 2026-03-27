"use client";

import React, { useEffect, useRef, useState, useCallback } from "react";
import { useQuery } from "urql";
import { ARCHITECTURE_DIAGRAM_QUERY } from "@/lib/graphql/queries";
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

export function ArchitectureDiagram({
  repositoryId,
  onModuleClick,
}: ArchitectureDiagramProps) {
  const [level, setLevel] = useState<"MODULE" | "FILE">("MODULE");
  const [moduleFilter, setModuleFilter] = useState<string | null>(null);
  const [moduleDepth, setModuleDepth] = useState(1);
  const [copied, setCopied] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  const [result] = useQuery({
    query: ARCHITECTURE_DIAGRAM_QUERY,
    variables: {
      repoId: repositoryId,
      level,
      moduleFilter,
      moduleDepth,
    },
  });

  const diagram = result.data?.architectureDiagram;

  // Render Mermaid
  useEffect(() => {
    if (!diagram?.mermaidSource || !containerRef.current) return;
    let cancelled = false;

    (async () => {
      const mermaid = (await import("mermaid")).default;
      mermaid.initialize({
        startOnLoad: false,
        theme: "dark",
        flowchart: { curve: "basis", padding: 16 },
        securityLevel: "loose",
      });

      if (cancelled) return;
      const id = `arch-${repositoryId.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8)}-${Date.now()}`;
      const { svg } = await mermaid.render(id, diagram.mermaidSource);
      if (!cancelled && containerRef.current) {
        containerRef.current.innerHTML = svg;
        // Make nodes clickable
        containerRef.current.querySelectorAll<HTMLElement>(".node").forEach((node) => {
          node.style.cursor = "pointer";
          node.addEventListener("click", () => {
            const nodeId = node.id;
            const path = nodeId.replace(/^flowchart-/, "").replace(/-\d+$/, "").replace(/_/g, "/");
            handleNodeClick(path);
          });
        });
      }
    })();

    return () => {
      cancelled = true;
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [diagram?.mermaidSource, repositoryId]);

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

  const handleCopyMermaid = useCallback(() => {
    if (diagram?.mermaidSource) {
      navigator.clipboard.writeText(diagram.mermaidSource);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }, [diagram?.mermaidSource]);

  const handleBackToModules = useCallback(() => {
    setLevel("MODULE");
    setModuleFilter(null);
  }, []);

  if (result.fetching) {
    return (
      <Panel>
        <div className="flex h-64 items-center justify-center">
          <div className="text-sm text-[var(--text-secondary)]">
            Generating architecture diagram...
          </div>
        </div>
      </Panel>
    );
  }

  if (!diagram || result.error) {
    return (
      <Panel>
        <div className="flex h-64 items-center justify-center">
          <div className="text-sm text-[var(--text-secondary)]">
            {result.error
              ? "Failed to generate diagram."
              : "No symbol graph available. Index this repository first."}
          </div>
        </div>
      </Panel>
    );
  }

  return (
    <div className="space-y-4">
      {/* Controls bar */}
      <div className="flex items-center gap-3">
        {level === "FILE" && (
          <Button variant="ghost" size="sm" onClick={handleBackToModules}>
            Back to Modules
          </Button>
        )}
        {level === "FILE" && moduleFilter && (
          <span className="text-xs font-medium text-[var(--text-primary)]">
            {moduleFilter}
          </span>
        )}
        <div className="flex-1" />
        {diagram.truncated && (
          <span className="text-xs text-[var(--text-tertiary)]">
            Showing {diagram.shownModules} of {diagram.totalModules}
          </span>
        )}
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
        <Button variant="secondary" size="sm" onClick={handleCopyMermaid}>
          {copied ? "Copied!" : "Copy Mermaid"}
        </Button>
      </div>

      {/* Diagram container */}
      <Panel className="overflow-auto">
        <div
          ref={containerRef}
          className="min-h-[400px] w-full [&_svg]:mx-auto [&_svg]:max-h-[70vh]"
          data-testid="architecture-diagram"
        />
      </Panel>

      {/* Module metadata table */}
      {diagram.modules.length > 0 && (
        <Panel>
          <h4 className="mb-3 text-sm font-medium text-[var(--text-primary)]">
            {level === "MODULE" ? "Modules" : "Files"} ({diagram.modules.length})
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
                {diagram.modules.map((mod: DiagramModuleNode) => (
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
