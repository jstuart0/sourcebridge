import { describe, expect, it } from "vitest";
import {
  diagramDocumentToFlowModel,
  diagramEdgesToFlowEdges,
  type DiagramDocument,
} from "./diagram-utils";

describe("diagram-utils", () => {
  it("lays out deterministic diagrams in graph order", () => {
    const doc: DiagramDocument = {
      id: "det-1",
      repository_id: "repo-1",
      source_kind: "deterministic",
      view_type: "detailed",
      title: "Architecture Diagram",
      nodes: [
        { id: "ui", label: "UI", kind: "interface", provenance: "graph_backed" },
        { id: "api", label: "API", kind: "service", provenance: "graph_backed" },
        { id: "db", label: "DB", kind: "storage", provenance: "graph_backed" },
      ],
      edges: [
        { id: "e1", from_node_id: "ui", to_node_id: "api", kind: "request", provenance: "graph_backed" },
        { id: "e2", from_node_id: "api", to_node_id: "db", kind: "read", provenance: "graph_backed" },
      ],
      groups: [],
      layout_hints: { direction: "LR" },
    };

    const { nodes } = diagramDocumentToFlowModel(doc);
    const positions = Object.fromEntries(nodes.map((node) => [node.id, node.position.x]));
    expect(positions.ui).toBeLessThan(positions.api);
    expect(positions.api).toBeLessThan(positions.db);
  });

  it("preserves manual positions for user-edited diagrams", () => {
    const doc: DiagramDocument = {
      id: "user-1",
      repository_id: "repo-1",
      source_kind: "user_edited",
      view_type: "system",
      title: "Edited Diagram",
      nodes: [
        {
          id: "svc",
          label: "Service",
          kind: "service",
          provenance: "user_added",
          position_x: 420,
          position_y: 180,
        },
      ],
      edges: [],
      groups: [],
    };

    const { nodes } = diagramDocumentToFlowModel(doc);
    expect(nodes[0]?.position).toEqual({ x: 420, y: 180 });
  });

  it("converts edge metadata into flow edges", () => {
    const [edge] = diagramEdgesToFlowEdges([
      {
        id: "e1",
        from_node_id: "worker",
        to_node_id: "queue",
        kind: "dispatch",
        provenance: "understanding_backed",
        call_count: 3,
      },
    ]);

    expect(edge.animated).toBe(true);
    expect(edge.label).toBe("3 calls");
    expect(edge.data?.kind).toBe("dispatch");
    expect(edge.markerEnd).toEqual(expect.objectContaining({ type: "arrowclosed" }));
  });
});
