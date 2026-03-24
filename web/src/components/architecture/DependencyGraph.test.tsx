import { describe, it, expect, vi, afterEach } from "vitest";
import { render, fireEvent, cleanup } from "@testing-library/react";

vi.mock("@xyflow/react", () => ({
  // eslint-disable-next-line @typescript-eslint/no-unused-vars, @typescript-eslint/no-explicit-any
  ReactFlow: ({ nodes, edges, onNodeClick, ...props }: any) => (
    <div
      data-testid="react-flow"
      data-node-count={nodes?.length}
      data-edge-count={edges?.length}
    >
      {nodes?.map((n: { id: string; data?: { label?: string }; className?: string; style?: React.CSSProperties }) => (
        <div
          key={n.id}
          data-testid={`node-${n.id}`}
          onClick={() => onNodeClick?.(null, n)}
          className={n.className}
          style={n.style}
        >
          {n.data?.label}
        </div>
      ))}
    </div>
  ),
  Background: () => <div data-testid="rf-background" />,
  Controls: () => <div data-testid="rf-controls" />,
}));

import { DependencyGraph, type GraphNode, type GraphEdge } from "./DependencyGraph";

afterEach(cleanup);

const sampleNodes: GraphNode[] = [
  { id: "m1", label: "auth-module", type: "module" },
  { id: "f1", label: "auth.ts", type: "file" },
  { id: "s1", label: "login()", type: "symbol" },
  { id: "r1", label: "REQ-001", type: "requirement" },
];

const sampleEdges: GraphEdge[] = [
  { source: "m1", target: "f1", label: "contains" },
  { source: "f1", target: "s1" },
  { source: "s1", target: "r1", label: "satisfies" },
];

describe("DependencyGraph", () => {
  it("renders with data-testid dependency-graph", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    expect(getByTestId("dependency-graph")).toBeInTheDocument();
  });

  it("passes correct node count to ReactFlow", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    expect(getByTestId("react-flow")).toHaveAttribute("data-node-count", "4");
  });

  it("passes correct edge count to ReactFlow", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    expect(getByTestId("react-flow")).toHaveAttribute("data-edge-count", "3");
  });

  it("renders node labels", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    expect(getByTestId("node-m1")).toHaveTextContent("auth-module");
    expect(getByTestId("node-f1")).toHaveTextContent("auth.ts");
    expect(getByTestId("node-s1")).toHaveTextContent("login()");
    expect(getByTestId("node-r1")).toHaveTextContent("REQ-001");
  });

  it("applies module type border color #3b82f6", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    const node = getByTestId("node-m1");
    expect(node.className).toContain("border-[#3b82f6]");
  });

  it("applies file type border color #22c55e", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    const node = getByTestId("node-f1");
    expect(node.className).toContain("border-[#22c55e]");
  });

  it("applies symbol type border color #a855f7", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    const node = getByTestId("node-s1");
    expect(node.className).toContain("border-[#a855f7]");
  });

  it("applies requirement type border color #eab308", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    const node = getByTestId("node-r1");
    expect(node.className).toContain("border-[#eab308]");
  });

  it("calls onNodeClick with node id when node is clicked", () => {
    const onClick = vi.fn();
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} onNodeClick={onClick} />
    );
    fireEvent.click(getByTestId("node-s1"));
    expect(onClick).toHaveBeenCalledTimes(1);
    expect(onClick).toHaveBeenCalledWith("s1");
  });

  it("does not crash when onNodeClick is not provided", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={sampleNodes} edges={sampleEdges} />
    );
    fireEvent.click(getByTestId("node-m1"));
    expect(getByTestId("dependency-graph")).toBeInTheDocument();
  });

  it("renders without crashing with empty nodes and edges", () => {
    const { getByTestId } = render(
      <DependencyGraph nodes={[]} edges={[]} />
    );
    expect(getByTestId("dependency-graph")).toBeInTheDocument();
    expect(getByTestId("react-flow")).toHaveAttribute("data-node-count", "0");
    expect(getByTestId("react-flow")).toHaveAttribute("data-edge-count", "0");
  });
});
