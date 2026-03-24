"use client";

import React, { useMemo } from "react";
import { ReactFlow, Background, Controls, type Node, type Edge } from "@xyflow/react";
import "@xyflow/react/dist/style.css";

export interface GraphNode {
  id: string;
  label: string;
  type: "module" | "file" | "symbol" | "requirement";
}

export interface GraphEdge {
  source: string;
  target: string;
  label?: string;
}

export interface DependencyGraphProps {
  nodes: GraphNode[];
  edges: GraphEdge[];
  onNodeClick?: (nodeId: string) => void;
}

const typeClasses: Record<string, string> = {
  module: "border-[#3b82f6]",
  file: "border-[#22c55e]",
  symbol: "border-[#a855f7]",
  requirement: "border-[#eab308]",
};

export function DependencyGraph({ nodes, edges, onNodeClick }: DependencyGraphProps) {
  const flowNodes: Node[] = useMemo(() => {
    const cols = 4;
    return nodes.map((n, i) => ({
      id: n.id,
      position: { x: (i % cols) * 220, y: Math.floor(i / cols) * 100 },
      data: { label: n.label },
      className: `min-w-[120px] rounded-lg border-2 bg-[var(--bg-elevated,#1e293b)] px-3 py-2 text-xs text-[var(--text-primary,#e2e8f0)] ${typeClasses[n.type] || "border-[#64748b]"}`,
      type: "default",
    }));
  }, [nodes]);

  const flowEdges: Edge[] = useMemo(
    () =>
      edges.map((e, i) => ({
        id: `e-${i}`,
        source: e.source,
        target: e.target,
        label: e.label,
        animated: true,
        style: { stroke: "var(--border-strong, #475569)" },
        labelStyle: { fill: "var(--text-secondary, #94a3b8)", fontSize: 10 },
      })),
    [edges]
  );

  return (
    <div data-testid="dependency-graph" className="h-[min(500px,80vh)] w-full">
      <ReactFlow
        nodes={flowNodes}
        edges={flowEdges}
        onNodeClick={(_, node) => onNodeClick?.(node.id)}
        fitView
        proOptions={{ hideAttribution: true }}
      >
        <Background color="var(--border-default, #334155)" gap={20} />
        <Controls className="!border-[var(--border-default,#334155)] !bg-[var(--bg-surface,#1e293b)]" />
      </ReactFlow>
    </div>
  );
}
