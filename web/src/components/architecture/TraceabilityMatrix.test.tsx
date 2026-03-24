import { describe, it, expect, vi, afterEach } from "vitest";
import { render, fireEvent, cleanup } from "@testing-library/react";
import { TraceabilityMatrix } from "./TraceabilityMatrix";

afterEach(cleanup);

const reqs = [
  { id: "r1", externalId: "REQ-001", title: "User auth" },
  { id: "r2", externalId: "REQ-002", title: "Data validation" },
];

const symbols = [
  { id: "s1", name: "Login", filePath: "auth.go", kind: "FUNCTION" },
  { id: "s2", name: "Validate", filePath: "data.go", kind: "FUNCTION" },
];

const links = [
  { requirementId: "r1", symbolId: "s1", confidence: "HIGH", verified: false },
  { requirementId: "r2", symbolId: "s2", confidence: "VERIFIED", verified: true },
];

describe("TraceabilityMatrix", () => {
  it("renders matrix with requirements and symbols", () => {
    const { getByTestId, getByText } = render(
      <TraceabilityMatrix requirements={reqs} symbols={symbols} links={links} coverage={0.75} />
    );
    expect(getByTestId("traceability-matrix")).toBeInTheDocument();
    expect(getByText("REQ-001")).toBeInTheDocument();
    expect(getByText("REQ-002")).toBeInTheDocument();
    expect(getByText("Login")).toBeInTheDocument();
    expect(getByText("Validate")).toBeInTheDocument();
  });

  it("displays coverage percentage", () => {
    const { getByTestId } = render(
      <TraceabilityMatrix requirements={reqs} symbols={symbols} links={links} coverage={0.75} />
    );
    const matrix = getByTestId("traceability-matrix");
    expect(matrix.textContent).toContain("Coverage: 75%");
  });

  it("renders link indicators in cells", () => {
    const { getByTestId } = render(
      <TraceabilityMatrix requirements={reqs} symbols={symbols} links={links} coverage={1} />
    );
    expect(getByTestId("matrix-cell-r1-s1")).toBeInTheDocument();
    expect(getByTestId("matrix-cell-r2-s2")).toBeInTheDocument();
  });

  it("calls onCellClick when cell clicked", () => {
    const onClick = vi.fn();
    const { getByTestId } = render(
      <TraceabilityMatrix
        requirements={reqs}
        symbols={symbols}
        links={links}
        coverage={1}
        onCellClick={onClick}
      />
    );
    fireEvent.click(getByTestId("matrix-cell-r1-s1"));
    expect(onClick).toHaveBeenCalledWith("r1", "s1");
  });

  it("handles empty data", () => {
    const { getByTestId } = render(
      <TraceabilityMatrix requirements={[]} symbols={[]} links={[]} coverage={0} />
    );
    const matrix = getByTestId("traceability-matrix");
    expect(matrix).toBeInTheDocument();
    expect(matrix.textContent).toContain("Coverage: 0%");
  });
});
