/**
 * Tests for ServiceHealthBanner — verifies that:
 *   - Nothing renders when overall health is true (or data not yet loaded).
 *   - Warning banner renders when overall is false.
 *   - The banner names the failing subsystem(s).
 */

import { describe, it, expect, afterEach, vi } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";

// Mock urql before importing any module that uses it.
vi.mock("urql", () => ({
  useQuery: vi.fn(),
  gql: (strs: TemplateStringsArray, ...vals: unknown[]) =>
    strs.reduce((acc, s, i) => acc + s + (vals[i] ?? ""), ""),
}));

import { useQuery } from "urql";
import { ServiceHealthBanner } from "./ServiceHealthBanner";

afterEach(cleanup);
afterEach(() => vi.clearAllMocks());

function mockHealth(data: Record<string, unknown> | null) {
  (useQuery as ReturnType<typeof vi.fn>).mockReturnValue([
    { data: data ? { serviceHealth: data } : undefined, fetching: !data, error: undefined },
  ]);
}

describe("ServiceHealthBanner", () => {
  it("renders nothing when health is unknown (first fetch in flight)", () => {
    mockHealth(null);
    const { container } = render(<ServiceHealthBanner />);
    expect(container.firstChild).toBeNull();
  });

  it("renders nothing when overall is true", () => {
    mockHealth({ overall: true, surreal: true, worker: true, message: "All systems normal", checkedAt: new Date().toISOString() });
    const { container } = render(<ServiceHealthBanner />);
    expect(container.firstChild).toBeNull();
  });

  it("renders the warning banner when overall is false", () => {
    mockHealth({ overall: false, surreal: false, worker: true, message: "SurrealDB unreachable", checkedAt: new Date().toISOString() });
    render(<ServiceHealthBanner />);
    expect(screen.getByRole("alert")).toBeDefined();
    expect(screen.getByText(/Backend services are degraded/i)).toBeDefined();
  });

  it("shows SurrealDB in the detail line when surreal is false", () => {
    mockHealth({ overall: false, surreal: false, worker: true, message: "SurrealDB unreachable", checkedAt: new Date().toISOString() });
    render(<ServiceHealthBanner />);
    expect(screen.getByText(/SurrealDB unreachable/i)).toBeDefined();
  });

  it("shows worker in the detail line when worker is false", () => {
    mockHealth({ overall: false, surreal: true, worker: false, message: "AI worker unreachable", checkedAt: new Date().toISOString() });
    render(<ServiceHealthBanner />);
    expect(screen.getByText(/AI worker unreachable/i)).toBeDefined();
  });

  it("shows both subsystems when both are down", () => {
    mockHealth({ overall: false, surreal: false, worker: false, message: "both down", checkedAt: new Date().toISOString() });
    render(<ServiceHealthBanner />);
    // Both failing subsystems appear in the detail text
    const detail = screen.getByText(/SurrealDB unreachable.*AI worker unreachable/i);
    expect(detail).toBeDefined();
  });
});
