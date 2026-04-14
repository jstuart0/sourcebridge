"use client";

import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  Panel as FlowPanel,
  useNodesState,
  useEdgesState,
  addEdge,
  MarkerType,
  type Node,
  type Edge,
  type Connection,
  type NodeChange,
  type EdgeChange,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Button } from "@/components/ui/button";

// ── React Flow data types ───────────────────────────────────────────────────

interface FlowNodeData extends Record<string, unknown> {
  label: string;
  kind: string;
  description?: string;
  provenance?: string;
  symbolCount?: number;
  fileCount?: number;
}

interface FlowEdgeData extends Record<string, unknown> {
  kind?: string;
  provenance?: string;
}

type FlowNode = Node<FlowNodeData>;
type FlowEdge = Edge<FlowEdgeData>;

// ── Types matching the Go DiagramDocument model ─────────────────────────────

interface DiagramNode {
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

interface DiagramEdge {
  id: string;
  from_node_id: string;
  to_node_id: string;
  label?: string;
  kind: string;
  provenance: string;
  call_count?: number;
}

interface DiagramGroup {
  id: string;
  label: string;
  kind: string;
}

interface DiagramDocument {
  id: string;
  repository_id: string;
  source_kind: string;
  view_type: string;
  title: string;
  summary?: string;
  nodes: DiagramNode[];
  edges: DiagramEdge[];
  groups: DiagramGroup[];
  layout_hints?: { direction?: string };
  raw_mermaid_source?: string;
}

// ── Color and style mappings ────────────────────────────────────────────────

const kindColors: Record<string, { bg: string; border: string; text: string }> = {
  actor:     { bg: "#dbeafe", border: "#3b82f6", text: "#1e40af" },
  interface: { bg: "#e0e7ff", border: "#6366f1", text: "#3730a3" },
  service:   { bg: "#dcfce7", border: "#22c55e", text: "#166534" },
  worker:    { bg: "#fef3c7", border: "#f59e0b", text: "#92400e" },
  storage:   { bg: "#fce7f3", border: "#ec4899", text: "#9d174d" },
  cache:     { bg: "#ffe4e6", border: "#f43f5e", text: "#9f1239" },
  queue:     { bg: "#fed7aa", border: "#f97316", text: "#9a3412" },
  external:  { bg: "#f1f5f9", border: "#94a3b8", text: "#475569" },
  component: { bg: "#f3f4f6", border: "#6b7280", text: "#374151" },
};

const edgeKindStyles: Record<string, { stroke: string; animated: boolean; strokeDasharray?: string }> = {
  request:  { stroke: "#3b82f6", animated: false },
  dispatch: { stroke: "#f59e0b", animated: true },
  read:     { stroke: "#22c55e", animated: false },
  write:    { stroke: "#ec4899", animated: false },
  call:     { stroke: "#6b7280", animated: false },
  event:    { stroke: "#8b5cf6", animated: true, strokeDasharray: "5 5" },
  depends:  { stroke: "#94a3b8", animated: false, strokeDasharray: "5 5" },
  other:    { stroke: "#9ca3af", animated: false },
};

const provenanceBadge: Record<string, { label: string; color: string }> = {
  graph_backed:          { label: "Graph", color: "#22c55e" },
  understanding_backed:  { label: "AI", color: "#6366f1" },
  imported:              { label: "Imported", color: "#f59e0b" },
  user_added:            { label: "Manual", color: "#3b82f6" },
  inferred_by_normalizer:{ label: "Inferred", color: "#94a3b8" },
  inferred_by_ai:        { label: "AI Inferred", color: "#8b5cf6" },
};

// ── Component Props ─────────────────────────────────────────────────────────

interface DiagramEditorProps {
  repositoryId: string;
  onClose: () => void;
  onSave?: (doc: DiagramDocument) => void;
}

// ── Layout helpers ──────────────────────────────────────────────────────────

function layoutNodes(diagramNodes: DiagramNode[], groups: DiagramGroup[]): FlowNode[] {
  const groupMap = new Map<string, DiagramNode[]>();
  const ungrouped: DiagramNode[] = [];

  for (const n of diagramNodes) {
    if (n.group_id) {
      const list = groupMap.get(n.group_id) || [];
      list.push(n);
      groupMap.set(n.group_id, list);
    } else {
      ungrouped.push(n);
    }
  }

  const rfNodes: FlowNode[] = [];
  let xOffset = 0;
  const nodeWidth = 200;
  const nodeHeight = 80;
  const groupPadding = 40;
  const spacing = 30;

  // Layout grouped nodes in columns per group
  for (const group of groups) {
    const members = groupMap.get(group.id) || [];
    if (members.length === 0) continue;

    const groupWidth = nodeWidth + groupPadding * 2;
    const groupHeight = members.length * (nodeHeight + spacing) + groupPadding * 2;

    // Group parent node
    rfNodes.push({
      id: `group-${group.id}`,
      type: "group",
      position: { x: xOffset, y: 0 },
      style: {
        width: groupWidth,
        height: groupHeight,
        backgroundColor: "rgba(100,116,139,0.05)",
        borderRadius: "8px",
        border: "1px dashed rgba(100,116,139,0.3)",
      },
      data: { label: group.label, kind: "component" },
    });

    members.forEach((n, i) => {
      rfNodes.push(diagramNodeToFlowNode(n, {
        x: groupPadding,
        y: groupPadding + i * (nodeHeight + spacing),
      }, `group-${group.id}`));
    });

    xOffset += groupWidth + spacing * 2;
  }

  // Layout ungrouped nodes
  ungrouped.forEach((n, i) => {
    rfNodes.push(diagramNodeToFlowNode(n, {
      x: n.position_x ?? xOffset,
      y: n.position_y ?? i * (nodeHeight + spacing),
    }));
  });

  return rfNodes;
}

function diagramNodeToFlowNode(n: DiagramNode, pos: { x: number; y: number }, parentId?: string): FlowNode {
  const colors = kindColors[n.kind] || kindColors.component;
  const prov = provenanceBadge[n.provenance];

  return {
    id: n.id,
    position: pos,
    parentId,
    extent: parentId ? "parent" as const : undefined,
    data: {
      label: n.label,
      kind: n.kind,
      description: n.description,
      provenance: n.provenance,
      symbolCount: n.symbol_count,
      fileCount: n.file_count,
    },
    style: {
      backgroundColor: colors.bg,
      borderColor: colors.border,
      color: colors.text,
      borderWidth: "2px",
      borderStyle: "solid",
      borderRadius: "8px",
      padding: "12px 16px",
      fontSize: "13px",
      fontWeight: 500,
      width: 200,
      minHeight: 60,
    },
  };
}

function diagramEdgesToFlowEdges(edges: DiagramEdge[]): FlowEdge[] {
  return edges.map((e) => {
    const style = edgeKindStyles[e.kind] || edgeKindStyles.other;
    return {
      id: e.id,
      source: e.from_node_id,
      target: e.to_node_id,
      label: e.label || (e.call_count && e.call_count > 1 ? `${e.call_count} calls` : undefined),
      animated: style.animated,
      style: {
        stroke: style.stroke,
        strokeWidth: 2,
        strokeDasharray: style.strokeDasharray,
      },
      markerEnd: { type: MarkerType.ArrowClosed, color: style.stroke },
      data: { kind: e.kind, provenance: e.provenance },
    };
  });
}

// ── Main Editor Component ───────────────────────────────────────────────────

export function DiagramEditor({ repositoryId, onClose, onSave }: DiagramEditorProps) {
  const [document, setDocument] = useState<DiagramDocument | null>(null);
  const [nodes, setNodes, onNodesChange] = useNodesState<FlowNode>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<FlowEdge>([]);
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  const [selectedEdge, setSelectedEdge] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [importMermaid, setImportMermaid] = useState("");
  const [importMode, setImportMode] = useState<"preserve" | "improve" | "simplify">("improve");
  const [dirty, setDirty] = useState(false);

  // Fetch the structured diagram
  useEffect(() => {
    async function load() {
      try {
        const res = await fetch(`/api/v1/diagrams/${repositoryId}/structured`);
        if (!res.ok) throw new Error("Failed to fetch diagram");
        const doc: DiagramDocument = await res.json();
        setDocument(doc);
        setNodes(layoutNodes(doc.nodes, doc.groups));
        setEdges(diagramEdgesToFlowEdges(doc.edges));
      } catch (err) {
        console.error("Failed to load diagram:", err);
      } finally {
        setLoading(false);
      }
    }
    load();
  }, [repositoryId, setNodes, setEdges]);

  const onConnect = useCallback(
    (params: Connection) => {
      if (!params.source || !params.target) return;
      const newEdge: FlowEdge = {
        id: `e-${Date.now()}`,
        source: params.source,
        target: params.target,
        sourceHandle: params.sourceHandle ?? null,
        targetHandle: params.targetHandle ?? null,
        markerEnd: { type: MarkerType.ArrowClosed, color: "#6b7280" },
        style: { stroke: "#6b7280", strokeWidth: 2 },
        data: { kind: "call", provenance: "user_added" },
      };
      setEdges((eds) => [...eds, newEdge]);
      setDirty(true);
    },
    [setEdges],
  );

  const onNodeClick = useCallback((_: React.MouseEvent, node: FlowNode) => {
    setSelectedNode(node.id);
    setSelectedEdge(null);
  }, []);

  const onEdgeClick = useCallback((_: React.MouseEvent, edge: FlowEdge) => {
    setSelectedEdge(edge.id);
    setSelectedNode(null);
  }, []);

  const onPaneClick = useCallback(() => {
    setSelectedNode(null);
    setSelectedEdge(null);
  }, []);

  // Track dirty state on any change
  const handleNodesChange = useCallback((changes: NodeChange<FlowNode>[]) => {
    onNodesChange(changes);
    if (changes.some((c) => c.type === "position" && "dragging" in c && c.dragging === false)) {
      setDirty(true);
    }
  }, [onNodesChange]);

  const handleEdgesChange = useCallback((changes: EdgeChange<FlowEdge>[]) => {
    onEdgesChange(changes);
    setDirty(true);
  }, [onEdgesChange]);

  // ── Node editing ────────────────────────────────────────────────────────

  const updateNodeLabel = useCallback((nodeId: string, label: string) => {
    setNodes((nds) =>
      nds.map((n) => (n.id === nodeId ? { ...n, data: { ...n.data, label } } : n)),
    );
    setDirty(true);
  }, [setNodes]);

  const updateNodeKind = useCallback((nodeId: string, kind: string) => {
    setNodes((nds) =>
      nds.map((n) => {
        if (n.id !== nodeId) return n;
        const colors = kindColors[kind] || kindColors.component;
        return {
          ...n,
          data: { ...n.data, kind },
          style: { ...n.style, backgroundColor: colors.bg, borderColor: colors.border, color: colors.text },
        };
      }),
    );
    setDirty(true);
  }, [setNodes]);

  const deleteNode = useCallback((nodeId: string) => {
    setNodes((nds) => nds.filter((n) => n.id !== nodeId));
    setEdges((eds) => eds.filter((e) => e.source !== nodeId && e.target !== nodeId));
    setSelectedNode(null);
    setDirty(true);
  }, [setNodes, setEdges]);

  const addNode = useCallback(() => {
    const id = `new-${Date.now()}`;
    const newNode: FlowNode = {
      id,
      position: { x: 100 + Math.random() * 200, y: 100 + Math.random() * 200 },
      data: { label: "New Component", kind: "component", provenance: "user_added" },
      style: {
        ...kindColors.component,
        backgroundColor: kindColors.component.bg,
        borderColor: kindColors.component.border,
        color: kindColors.component.text,
        borderWidth: "2px",
        borderStyle: "solid",
        borderRadius: "8px",
        padding: "12px 16px",
        fontSize: "13px",
        fontWeight: 500,
        width: 200,
        minHeight: 60,
      },
    };
    setNodes((nds) => [...nds, newNode]);
    setDirty(true);
  }, [setNodes]);

  // ── Edge editing ────────────────────────────────────────────────────────

  const updateEdgeLabel = useCallback((edgeId: string, label: string) => {
    setEdges((eds) =>
      eds.map((e) => (e.id === edgeId ? { ...e, label } : e)),
    );
    setDirty(true);
  }, [setEdges]);

  const deleteEdge = useCallback((edgeId: string) => {
    setEdges((eds) => eds.filter((e) => e.id !== edgeId));
    setSelectedEdge(null);
    setDirty(true);
  }, [setEdges]);

  // ── Save ────────────────────────────────────────────────────────────────

  const handleSave = useCallback(async () => {
    if (!document) return;
    setSaving(true);

    // Convert React Flow state back to DiagramDocument
    const docNodes: DiagramNode[] = nodes
      .filter((n) => n.type !== "group")
      .map((n) => ({
        id: n.id,
        label: n.data.label || n.id,
        kind: n.data.kind || "component",
        description: n.data.description || "",
        provenance: n.data.provenance || "user_added",
        symbol_count: n.data.symbolCount,
        file_count: n.data.fileCount,
        position_x: n.position.x,
        position_y: n.position.y,
        group_id: n.parentId?.replace("group-", ""),
      }));

    const docEdges: DiagramEdge[] = edges.map((e) => ({
      id: e.id,
      from_node_id: e.source,
      to_node_id: e.target,
      label: typeof e.label === "string" ? e.label : "",
      kind: e.data?.kind || "call",
      provenance: e.data?.provenance || "user_added",
    }));

    const updatedDoc: DiagramDocument = {
      ...document,
      source_kind: "user_edited",
      nodes: docNodes,
      edges: docEdges,
    };

    try {
      const res = await fetch(`/api/v1/diagrams/${repositoryId}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(updatedDoc),
      });

      if (res.ok) {
        const result = await res.json();
        setDocument(result.document);
        setDirty(false);
        onSave?.(result.document);
      }
    } catch (err) {
      console.error("Failed to save diagram:", err);
    } finally {
      setSaving(false);
    }
  }, [document, nodes, edges, repositoryId, onSave]);

  // ── Reset ───────────────────────────────────────────────────────────────

  const handleReset = useCallback(async () => {
    if (!confirm("Reset to the generated diagram? Your edits will be lost.")) return;

    await fetch(`/api/v1/diagrams/${repositoryId}`, { method: "DELETE" });

    // Reload
    const res = await fetch(`/api/v1/diagrams/${repositoryId}/structured`);
    if (res.ok) {
      const doc: DiagramDocument = await res.json();
      setDocument(doc);
      setNodes(layoutNodes(doc.nodes, doc.groups));
      setEdges(diagramEdgesToFlowEdges(doc.edges));
      setDirty(false);
    }
  }, [repositoryId, setNodes, setEdges]);

  // ── Import Mermaid ──────────────────────────────────────────────────────

  const handleImport = useCallback(async () => {
    if (!importMermaid.trim()) return;

    const res = await fetch(`/api/v1/diagrams/${repositoryId}/import`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mermaid: importMermaid, mode: importMode }),
    });

    if (res.ok) {
      const result = await res.json();
      const doc: DiagramDocument = result.document;
      setDocument(doc);
      setNodes(layoutNodes(doc.nodes, doc.groups));
      setEdges(diagramEdgesToFlowEdges(doc.edges));
      setImportOpen(false);
      setImportMermaid("");
      setDirty(true);
    }
  }, [importMermaid, importMode, repositoryId, setNodes, setEdges]);

  // ── Selected element details ──────────────────────────────────────────

  const selectedNodeData = useMemo(() => {
    if (!selectedNode) return null;
    return nodes.find((n) => n.id === selectedNode);
  }, [selectedNode, nodes]);

  const selectedEdgeData = useMemo(() => {
    if (!selectedEdge) return null;
    return edges.find((e) => e.id === selectedEdge);
  }, [selectedEdge, edges]);

  if (loading) {
    return (
      <div className="flex items-center justify-center h-96 text-neutral-400">
        Loading diagram editor...
      </div>
    );
  }

  return (
    <div className="flex h-[calc(100vh-200px)] min-h-[500px] bg-neutral-950 rounded-lg overflow-hidden border border-neutral-800">
      {/* Main canvas */}
      <div className="flex-1 relative">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          onNodesChange={handleNodesChange}
          onEdgesChange={handleEdgesChange}
          onConnect={onConnect}
          onNodeClick={onNodeClick}
          onEdgeClick={onEdgeClick}
          onPaneClick={onPaneClick}
          fitView
          proOptions={{ hideAttribution: true }}
          style={{ backgroundColor: "#0a0a0a" }}
        >
          <Background color="#333" gap={20} />
          <Controls
            showInteractive={false}
            style={{ backgroundColor: "#1a1a1a", borderColor: "#333" }}
          />
          <MiniMap
            style={{ backgroundColor: "#1a1a1a", border: "1px solid #333" }}
            maskColor="rgba(0,0,0,0.7)"
            nodeColor={(n) => {
              const kind = String((n.data as FlowNodeData)?.kind || "component");
              return kindColors[kind]?.border || "#6b7280";
            }}
          />

          {/* Toolbar */}
          <FlowPanel position="top-left">
            <div className="flex gap-2 p-2 bg-neutral-900 rounded-lg border border-neutral-700">
              <Button size="sm" variant="ghost" onClick={addNode} title="Add node">
                + Node
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setImportOpen(true)}>
                Import Mermaid
              </Button>
              <div className="w-px bg-neutral-700" />
              <Button
                size="sm"
                variant={dirty ? "primary" : "ghost"}
                onClick={handleSave}
                disabled={saving || !dirty}
              >
                {saving ? "Saving..." : "Save"}
              </Button>
              <Button size="sm" variant="ghost" onClick={handleReset}>
                Reset
              </Button>
              <div className="w-px bg-neutral-700" />
              <Button size="sm" variant="ghost" onClick={onClose}>
                Close Editor
              </Button>
            </div>
          </FlowPanel>

          {/* Legend */}
          <FlowPanel position="bottom-left">
            <div className="p-3 bg-neutral-900/90 rounded-lg border border-neutral-700 text-xs">
              <div className="text-neutral-400 mb-2 font-medium">Node Types</div>
              <div className="grid grid-cols-3 gap-x-4 gap-y-1">
                {Object.entries(kindColors).map(([kind, colors]) => (
                  <div key={kind} className="flex items-center gap-1.5">
                    <div
                      className="w-3 h-3 rounded"
                      style={{ backgroundColor: colors.border }}
                    />
                    <span className="text-neutral-300 capitalize">{kind}</span>
                  </div>
                ))}
              </div>
            </div>
          </FlowPanel>
        </ReactFlow>
      </div>

      {/* Side panel */}
      <div className="w-72 bg-neutral-900 border-l border-neutral-800 overflow-y-auto">
        {selectedNodeData ? (
          <div className="p-4 space-y-4">
            <h3 className="text-sm font-semibold text-neutral-200">Node Properties</h3>

            <div>
              <label className="block text-xs text-neutral-400 mb-1">Label</label>
              <input
                type="text"
                value={(selectedNodeData.data as any).label || ""}
                onChange={(e) => updateNodeLabel(selectedNodeData.id, e.target.value)}
                className="w-full px-3 py-1.5 bg-neutral-800 border border-neutral-700 rounded text-sm text-neutral-200"
              />
            </div>

            <div>
              <label className="block text-xs text-neutral-400 mb-1">Kind</label>
              <select
                value={(selectedNodeData.data as any).kind || "component"}
                onChange={(e) => updateNodeKind(selectedNodeData.id, e.target.value)}
                className="w-full px-3 py-1.5 bg-neutral-800 border border-neutral-700 rounded text-sm text-neutral-200"
              >
                {Object.keys(kindColors).map((k) => (
                  <option key={k} value={k}>
                    {k.charAt(0).toUpperCase() + k.slice(1)}
                  </option>
                ))}
              </select>
            </div>

            {(selectedNodeData.data as any).provenance && (
              <div>
                <label className="block text-xs text-neutral-400 mb-1">Provenance</label>
                <span
                  className="inline-block px-2 py-0.5 rounded text-xs"
                  style={{
                    backgroundColor: provenanceBadge[(selectedNodeData.data as any).provenance]?.color + "20",
                    color: provenanceBadge[(selectedNodeData.data as any).provenance]?.color,
                  }}
                >
                  {provenanceBadge[(selectedNodeData.data as any).provenance]?.label || (selectedNodeData.data as any).provenance}
                </span>
              </div>
            )}

            {((selectedNodeData.data as any).symbolCount ?? 0) > 0 && (
              <div className="text-xs text-neutral-400">
                {(selectedNodeData.data as any).symbolCount} symbols
                {((selectedNodeData.data as any).fileCount ?? 0) > 0 &&
                  ` / ${(selectedNodeData.data as any).fileCount} files`}
              </div>
            )}

            <Button
              size="sm"
              variant="ghost"
              className="text-red-400 hover:text-red-300"
              onClick={() => deleteNode(selectedNodeData.id)}
            >
              Delete Node
            </Button>
          </div>
        ) : selectedEdgeData ? (
          <div className="p-4 space-y-4">
            <h3 className="text-sm font-semibold text-neutral-200">Edge Properties</h3>

            <div>
              <label className="block text-xs text-neutral-400 mb-1">Label</label>
              <input
                type="text"
                value={typeof selectedEdgeData.label === "string" ? selectedEdgeData.label : ""}
                onChange={(e) => updateEdgeLabel(selectedEdgeData.id, e.target.value)}
                className="w-full px-3 py-1.5 bg-neutral-800 border border-neutral-700 rounded text-sm text-neutral-200"
              />
            </div>

            <div className="text-xs text-neutral-400">
              <div>From: {selectedEdgeData.source}</div>
              <div>To: {selectedEdgeData.target}</div>
            </div>

            <Button
              size="sm"
              variant="ghost"
              className="text-red-400 hover:text-red-300"
              onClick={() => deleteEdge(selectedEdgeData.id)}
            >
              Delete Edge
            </Button>
          </div>
        ) : (
          <div className="p-4 text-sm text-neutral-400 space-y-3">
            <h3 className="font-semibold text-neutral-200">Diagram Editor</h3>
            <p>Click a <strong>node</strong> or <strong>edge</strong> to edit its properties.</p>
            <p>Drag between nodes to create new edges.</p>
            <p>Use the toolbar to add nodes, import Mermaid, save, or reset.</p>
            {document && (
              <div className="pt-2 border-t border-neutral-800 text-xs space-y-1">
                <div>Source: <span className="text-neutral-300">{document.source_kind}</span></div>
                <div>View: <span className="text-neutral-300">{document.view_type}</span></div>
                <div>Nodes: <span className="text-neutral-300">{document.nodes.length}</span></div>
                <div>Edges: <span className="text-neutral-300">{document.edges.length}</span></div>
                <div>Groups: <span className="text-neutral-300">{document.groups.length}</span></div>
              </div>
            )}
          </div>
        )}
      </div>

      {/* Import Mermaid Modal */}
      {importOpen && (
        <div className="absolute inset-0 bg-black/60 flex items-center justify-center z-50">
          <div className="bg-neutral-900 border border-neutral-700 rounded-lg p-6 w-[600px] max-h-[80vh] overflow-y-auto">
            <h3 className="text-lg font-semibold text-neutral-200 mb-4">Import Mermaid</h3>

            <textarea
              value={importMermaid}
              onChange={(e) => setImportMermaid(e.target.value)}
              placeholder="Paste Mermaid diagram here..."
              rows={12}
              className="w-full p-3 bg-neutral-800 border border-neutral-700 rounded text-sm text-neutral-200 font-mono resize-none"
            />

            <div className="mt-4">
              <label className="block text-xs text-neutral-400 mb-2">Import Mode</label>
              <div className="flex gap-3">
                {(["preserve", "improve", "simplify"] as const).map((mode) => (
                  <label key={mode} className="flex items-center gap-1.5 text-sm text-neutral-300">
                    <input
                      type="radio"
                      name="importMode"
                      value={mode}
                      checked={importMode === mode}
                      onChange={() => setImportMode(mode)}
                      className="accent-blue-500"
                    />
                    <span className="capitalize">{mode}</span>
                  </label>
                ))}
              </div>
              <div className="mt-1 text-xs text-neutral-500">
                {importMode === "preserve" && "Keep the structure as close as possible to the source."}
                {importMode === "improve" && "Normalize labels, classify nodes, infer groups, remove noise."}
                {importMode === "simplify" && "Aggressively collapse to a high-level system view."}
              </div>
            </div>

            <div className="flex justify-end gap-3 mt-6">
              <Button size="sm" variant="ghost" onClick={() => setImportOpen(false)}>
                Cancel
              </Button>
              <Button size="sm" onClick={handleImport} disabled={!importMermaid.trim()}>
                Import
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
