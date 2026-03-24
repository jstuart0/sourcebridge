import { describe, it, expect, afterEach } from "vitest";
import { render, cleanup } from "@testing-library/react";
import { CoverageChart } from "./CoverageChart";

afterEach(cleanup);

const mockData = [
  { name: "Business", category: "business", covered: 8, total: 10 },
  { name: "Security", category: "security", covered: 3, total: 5 },
  { name: "Data", category: "data", covered: 0, total: 4 },
];

describe("CoverageChart", () => {
  it("renders chart container", () => {
    const { getByTestId } = render(<CoverageChart data={mockData} />);
    expect(getByTestId("coverage-chart")).toBeInTheDocument();
  });

  it("renders title", () => {
    const { getByText } = render(<CoverageChart data={mockData} title="Test Coverage" />);
    expect(getByText("Test Coverage")).toBeInTheDocument();
  });

  it("renders default title", () => {
    const { getByText } = render(<CoverageChart data={mockData} />);
    expect(getByText("Requirement Coverage")).toBeInTheDocument();
  });

  it("handles empty data", () => {
    const { getByTestId } = render(<CoverageChart data={[]} />);
    expect(getByTestId("coverage-chart")).toBeInTheDocument();
  });
});
