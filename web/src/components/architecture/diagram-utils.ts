"use client";

import dagre from "@dagrejs/dagre";
import { MarkerType, type Edge, type Node } from "@xyflow/react";

export interface DiagramNode {
  id: string;
  label: string;
  kind: string;
  description?: string;
  group_id?: string;
  source_refs?: string[];
  provenance: string;
  symbol_count?: number;
  file_count?: number;
  position_x?: number | null;
  position_y?: number | null;
}

export interface DiagramEdge {
  id: string;
  from_node_id: string;
  to_node_id: string;
  label?: string;
  kind: string;
  provenance: string;
  confidence?: string;
  source_refs?: string[];
  call_count?: number;
}

export interface DiagramGroup {
  id: string;
  label: string;
  kind: string;
}

export interface DiagramDocument {
  id: string;
  repository_id: string;
  artifact_id?: string;
  source_kind: string;
  view_type: string;
  title: string;
  summary?: string;
  nodes: DiagramNode[];
  edges: DiagramEdge[];
  groups: DiagramGroup[];
  layout_hints?: { direction?: string };
  raw_mermaid_source?: string;
  created_at?: string;
  updated_at?: string;
}

export interface FlowNodeData extends Record<string, unknown> {
  label: string;
  kind: string;
  description?: string;
  provenance?: string;
  symbolCount?: number;
  fileCount?: number;
  sourceRefs?: string[];
  groupId?: string;
}

export interface FlowEdgeData extends Record<string, unknown> {
  kind?: string;
  provenance?: string;
  confidence?: string;
  sourceRefs?: string[];
}

export type FlowNode = Node<FlowNodeData>;
export type FlowEdge = Edge<FlowEdgeData>;

export const kindColors: Record<string, { bg: string; border: string; text: string }> = {
  actor: { bg: "#dbeafe", border: "#3b82f6", text: "#1e40af" },
  interface: { bg: "#e0e7ff", border: "#6366f1", text: "#3730a3" },
  service: { bg: "#dcfce7", border: "#22c55e", text: "#166534" },
  worker: { bg: "#fef3c7", border: "#f59e0b", text: "#92400e" },
  storage: { bg: "#fce7f3", border: "#ec4899", text: "#9d174d" },
  cache: { bg: "#ffe4e6", border: "#f43f5e", text: "#9f1239" },
  queue: { bg: "#fed7aa", border: "#f97316", text: "#9a3412" },
  external: { bg: "#f1f5f9", border: "#94a3b8", text: "#475569" },
  component: { bg: "#f3f4f6", border: "#6b7280", text: "#374151" },
};

export const edgeKindStyles: Record<string, { stroke: string; animated: boolean; strokeDasharray?: string }> = {
  request: { stroke: "#3b82f6", animated: false },
  dispatch: { stroke: "#f59e0b", animated: true },
  read: { stroke: "#22c55e", animated: false },
  write: { stroke: "#ec4899", animated: false },
  call: { stroke: "#6b7280", animated: false },
  event: { stroke: "#8b5cf6", animated: true, strokeDasharray: "5 5" },
  depends: { stroke: "#94a3b8", animated: false, strokeDasharray: "5 5" },
  other: { stroke: "#9ca3af", animated: false },
};

export const provenanceBadge: Record<string, { label: string; color: string }> = {
  graph_backed: { label: "Graph", color: "#22c55e" },
  understanding_backed: { label: "AI", color: "#6366f1" },
  imported: { label: "Imported", color: "#f59e0b" },
  user_added: { label: "Manual", color: "#3b82f6" },
  inferred_by_normalizer: { label: "Inferred", color: "#94a3b8" },
  inferred_by_ai: { label: "AI Inferred", color: "#8b5cf6" },
};

const NODE_WIDTH = 220;
const NODE_HEIGHT = 78;

export function diagramNodeToFlowNode(
  node: DiagramNode,
  position: { x: number; y: number },
): FlowNode {
  const colors = kindColors[node.kind] || kindColors.component;
  return {
    id: node.id,
    position,
    data: {
      label: node.label,
      kind: node.kind,
      description: node.description,
      provenance: node.provenance,
      symbolCount: node.symbol_count,
      fileCount: node.file_count,
      sourceRefs: node.source_refs,
      groupId: node.group_id,
    },
    style: {
      backgroundColor: colors.bg,
      borderColor: colors.border,
      color: colors.text,
      borderWidth: "2px",
      borderStyle: "solid",
      borderRadius: "10px",
      padding: "12px 16px",
      fontSize: "13px",
      fontWeight: 500,
      width: NODE_WIDTH,
      minHeight: 60,
    },
  };
}

export function diagramEdgesToFlowEdges(edges: DiagramEdge[]): FlowEdge[] {
  return edges.map((edge) => {
    const style = edgeKindStyles[edge.kind] || edgeKindStyles.other;
    return {
      id: edge.id,
      source: edge.from_node_id,
      target: edge.to_node_id,
      label: edge.label || (edge.call_count && edge.call_count > 1 ? `${edge.call_count} calls` : undefined),
      animated: style.animated,
      style: {
        stroke: style.stroke,
        strokeWidth: 2,
        strokeDasharray: style.strokeDasharray,
      },
      markerEnd: { type: MarkerType.ArrowClosed, color: style.stroke },
      data: {
        kind: edge.kind,
        provenance: edge.provenance,
        confidence: edge.confidence,
        sourceRefs: edge.source_refs,
      },
    };
  });
}

export interface FlowModelOptions {
  preserveManualPositions?: boolean;
}

export function diagramDocumentToFlowModel(
  document: DiagramDocument,
  options: FlowModelOptions = {},
): { nodes: FlowNode[]; edges: FlowEdge[] } {
  const edges = diagramEdgesToFlowEdges(document.edges);
  const shouldPreservePositions = options.preserveManualPositions ?? document.source_kind === "user_edited";

  if (shouldPreservePositions && document.nodes.every((node) => node.position_x != null && node.position_y != null)) {
    return {
      nodes: document.nodes.map((node) =>
        diagramNodeToFlowNode(node, {
          x: node.position_x ?? 0,
          y: node.position_y ?? 0,
        }),
      ),
      edges,
    };
  }

  return {
    nodes: layoutDiagramNodes(document),
    edges,
  };
}

function layoutDiagramNodes(document: DiagramDocument): FlowNode[] {
  const graph = new dagre.graphlib.Graph({ multigraph: false, compound: false });
  graph.setDefaultEdgeLabel(() => ({}));
  graph.setGraph({
    rankdir: document.layout_hints?.direction === "TB" ? "TB" : "LR",
    ranksep: 110,
    nodesep: 40,
    marginx: 16,
    marginy: 16,
  });

  for (const node of document.nodes) {
    graph.setNode(node.id, {
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
      label: node.label,
    });
  }

  for (const edge of document.edges) {
    if (edge.from_node_id === edge.to_node_id) {
      continue;
    }
    if (!graph.hasNode(edge.from_node_id) || !graph.hasNode(edge.to_node_id)) {
      continue;
    }
    graph.setEdge(edge.from_node_id, edge.to_node_id);
  }

  dagre.layout(graph);

  return document.nodes.map((node) => {
    const laidOut = graph.node(node.id);
    const position = laidOut
      ? { x: laidOut.x - NODE_WIDTH / 2, y: laidOut.y - NODE_HEIGHT / 2 }
      : { x: 0, y: 0 };
    return diagramNodeToFlowNode(node, position);
  });
}
