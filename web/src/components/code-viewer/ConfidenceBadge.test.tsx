import { describe, it, expect, afterEach } from "vitest";
import { render, cleanup } from "@testing-library/react";
import { ConfidenceBadge } from "./ConfidenceBadge";

afterEach(cleanup);

describe("ConfidenceBadge", () => {
  it("renders with high level", () => {
    const { getByTestId } = render(<ConfidenceBadge level="high" />);
    const badge = getByTestId("confidence-badge");
    expect(badge).toHaveAttribute("data-level", "high");
    expect(badge).toHaveTextContent("High");
  });

  it("renders verified level", () => {
    const { getByTestId } = render(<ConfidenceBadge level="verified" />);
    const badge = getByTestId("confidence-badge");
    expect(badge).toHaveAttribute("data-level", "verified");
    expect(badge).toHaveTextContent("Verified");
  });

  it("renders medium level", () => {
    const { getByTestId } = render(<ConfidenceBadge level="medium" />);
    expect(getByTestId("confidence-badge")).toHaveTextContent("Medium");
  });

  it("renders low level", () => {
    const { getByTestId } = render(<ConfidenceBadge level="low" />);
    expect(getByTestId("confidence-badge")).toHaveTextContent("Low");
  });

  it("displays optional score", () => {
    const { getByTestId } = render(<ConfidenceBadge level="high" score={0.87} />);
    expect(getByTestId("confidence-badge")).toHaveTextContent("87%");
  });

  it("does not display score when not provided", () => {
    const { getByTestId } = render(<ConfidenceBadge level="high" />);
    expect(getByTestId("confidence-badge")).not.toHaveTextContent("%");
  });

  it("applies custom className", () => {
    const { getByTestId } = render(<ConfidenceBadge level="high" className="custom-class" />);
    expect(getByTestId("confidence-badge")).toHaveClass("custom-class");
  });
});
