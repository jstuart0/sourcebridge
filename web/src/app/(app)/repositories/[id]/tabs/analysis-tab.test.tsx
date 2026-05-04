/**
 * Regression test: AnalysisTab state persists when the panel is hidden
 * (tab switch via `hidden` attribute) rather than unmounted.
 *
 * Pre-Phase-4: state lived in the parent and survived tab switches.
 * Phase-4 regression (codex H1): conditional render unmounted the tab on
 * switch, resetting discussQuestion, analysisResult, reviewFile, etc.
 * Fix: wrapper uses `hidden` attribute; component is never unmounted.
 *
 * This test verifies the fix by:
 *   1. Rendering AnalysisTab inside a wrapper that can toggle `hidden`.
 *   2. Typing into the "Discuss Code" input.
 *   3. Hiding the wrapper (simulating switching to another tab).
 *   4. Showing the wrapper again (switching back).
 *   5. Asserting the input value is unchanged.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { useState } from "react";

// ── mock urql before importing the component ──────────────────────────────────
vi.mock("urql", () => ({
  useMutation: vi.fn(() => [{ fetching: false, error: undefined }, vi.fn()]),
  gql: (strs: TemplateStringsArray, ...vals: unknown[]) =>
    strs.reduce((acc, s, i) => acc + s + (vals[i] ?? ""), ""),
}));

// ── mock telemetry (no-op) ────────────────────────────────────────────────────
vi.mock("@/lib/telemetry", () => ({ trackEvent: vi.fn() }));

// ── mock askStream (not exercised in this test) ───────────────────────────────
vi.mock("@/lib/askStream", () => ({ askStream: vi.fn() }));

// ── import after mocks ────────────────────────────────────────────────────────
import { AnalysisTab } from "./analysis-tab";

afterEach(cleanup);
afterEach(() => vi.clearAllMocks());

// ─────────────────────────────────────────────────────────────────────────────
// Wrapper that mimics the page.tsx hidden-panel pattern
// ─────────────────────────────────────────────────────────────────────────────

function HiddenTabWrapper({ children }: { children: React.ReactNode }) {
  const [hidden, setHidden] = useState(false);
  return (
    <div>
      <button onClick={() => setHidden((h) => !h)}>
        {hidden ? "show" : "hide"}
      </button>
      {/* `hidden` attribute hides from view without unmounting — mirrors page.tsx */}
      <div hidden={hidden}>{children}</div>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

describe("AnalysisTab — state preservation across tab switches", () => {
  const baseProps = {
    repoId: "repo-123",
    symbols: [],
    symbolQuery: "",
    setSymbolQuery: vi.fn(),
    selectedSymbolId: null,
    setSelectedSymbolId: vi.fn(),
    isAiLoading: vi.fn(() => false),
    runAiOp: vi.fn(),
  };

  it("retains discuss question when panel is hidden then shown (no unmount)", () => {
    render(
      <HiddenTabWrapper>
        <AnalysisTab {...baseProps} />
      </HiddenTabWrapper>,
    );

    const input = screen.getByPlaceholderText("Ask a question about this code...");
    fireEvent.change(input, { target: { value: "What does this function do?" } });
    expect(input).toHaveValue("What does this function do?");

    // Simulate switching to another tab: hide the panel (but do NOT unmount)
    fireEvent.click(screen.getByText("hide"));

    // The input is now hidden from view but still in the DOM
    expect(input).toHaveValue("What does this function do?");

    // Simulate switching back
    fireEvent.click(screen.getByText("show"));

    // Value must be preserved — no remount reset
    expect(input).toHaveValue("What does this function do?");
  });

  it("retains review file path across hide/show cycles", () => {
    render(
      <HiddenTabWrapper>
        <AnalysisTab {...baseProps} />
      </HiddenTabWrapper>,
    );

    const reviewInput = screen.getByPlaceholderText(
      "File path (e.g. internal/api/rest/router.go)",
    );
    fireEvent.change(reviewInput, { target: { value: "internal/core/auth.go" } });
    expect(reviewInput).toHaveValue("internal/core/auth.go");

    fireEvent.click(screen.getByText("hide"));
    fireEvent.click(screen.getByText("show"));

    expect(reviewInput).toHaveValue("internal/core/auth.go");
  });
});
