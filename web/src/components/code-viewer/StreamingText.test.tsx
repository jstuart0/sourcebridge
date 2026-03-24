import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, act, cleanup } from "@testing-library/react";
import { StreamingText } from "./StreamingText";

afterEach(cleanup);

describe("StreamingText", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders with streaming cursor initially", () => {
    const { getByTestId } = render(<StreamingText text="hello world" />);
    expect(getByTestId("streaming-text")).toHaveAttribute("data-complete", "false");
    expect(getByTestId("streaming-cursor")).toBeInTheDocument();
  });

  it("shows words progressively", () => {
    const { getByTestId } = render(<StreamingText text="one two three" speed={50} />);
    act(() => { vi.advanceTimersByTime(50); });
    expect(getByTestId("streaming-text")).toHaveTextContent("one");
    act(() => { vi.advanceTimersByTime(50); });
    expect(getByTestId("streaming-text")).toHaveTextContent("one two");
  });

  it("marks complete when all words displayed", () => {
    const { getByTestId, queryByTestId } = render(<StreamingText text="hello world" speed={50} />);
    act(() => { vi.advanceTimersByTime(100); });
    expect(getByTestId("streaming-text")).toHaveAttribute("data-complete", "true");
    expect(queryByTestId("streaming-cursor")).not.toBeInTheDocument();
  });

  it("calls onComplete when streaming finishes", () => {
    const onComplete = vi.fn();
    render(<StreamingText text="a b" speed={50} onComplete={onComplete} />);
    act(() => { vi.advanceTimersByTime(100); });
    expect(onComplete).toHaveBeenCalledTimes(1);
  });

  it("handles empty text", () => {
    const { getByTestId } = render(<StreamingText text="" />);
    expect(getByTestId("streaming-text")).toBeInTheDocument();
  });

  it("applies custom className", () => {
    const { getByTestId } = render(<StreamingText text="test" className="custom" />);
    expect(getByTestId("streaming-text")).toHaveClass("custom");
  });
});
