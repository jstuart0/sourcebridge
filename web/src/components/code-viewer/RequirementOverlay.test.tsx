import { describe, it, expect, afterEach } from "vitest";
import { render, cleanup } from "@testing-library/react";
import { RequirementOverlay } from "./RequirementOverlay";

afterEach(cleanup);

describe("RequirementOverlay", () => {
  const defaultProps = {
    requirementId: "REQ-001",
    category: "business",
    startLine: 10,
    endLine: 20,
    confidence: 0.85,
  };

  it("renders with requirement data", () => {
    const { getByTestId } = render(<RequirementOverlay {...defaultProps} />);
    const overlay = getByTestId("requirement-overlay");
    expect(overlay).toHaveAttribute("data-requirement-id", "REQ-001");
    expect(overlay).toHaveAttribute("data-category", "business");
  });

  it("displays line range", () => {
    const { getByTestId } = render(<RequirementOverlay {...defaultProps} />);
    expect(getByTestId("requirement-overlay")).toHaveTextContent("L10-20");
  });

  it("displays confidence percentage", () => {
    const { getByTestId } = render(<RequirementOverlay {...defaultProps} />);
    expect(getByTestId("requirement-overlay")).toHaveTextContent("85%");
  });

  it("renders security category", () => {
    const { getByTestId } = render(<RequirementOverlay {...defaultProps} category="security" />);
    expect(getByTestId("requirement-overlay")).toHaveAttribute("data-category", "security");
  });

  it("renders data category", () => {
    const { getByTestId } = render(<RequirementOverlay {...defaultProps} category="data" />);
    expect(getByTestId("requirement-overlay")).toHaveAttribute("data-category", "data");
  });

  it("renders compliance category", () => {
    const { getByTestId } = render(<RequirementOverlay {...defaultProps} category="compliance" />);
    expect(getByTestId("requirement-overlay")).toHaveAttribute("data-category", "compliance");
  });

  it("renders performance category", () => {
    const { getByTestId } = render(<RequirementOverlay {...defaultProps} category="performance" />);
    expect(getByTestId("requirement-overlay")).toHaveAttribute("data-category", "performance");
  });

  it("falls back to business style for unknown category", () => {
    const { getByTestId } = render(<RequirementOverlay {...defaultProps} category="unknown" />);
    expect(getByTestId("requirement-overlay")).toBeInTheDocument();
  });
});
