/**
 * CA-363: repositories list page distinguishes a network / server error
 * from an empty-repositories result.
 *
 * Strategy: vi.mock("urql") + vi.mock("next/navigation") to isolate the
 * page from infrastructure. The test controls useQuery return values to
 * exercise the error branch (result.error != null) and the empty branch
 * (result.data.repositories == []) independently.
 */

import { describe, it, expect, afterEach, vi } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import type { UseQueryState } from "urql";

// ── mock urql before importing the component ──────────────────────────────
vi.mock("urql", () => ({
  useQuery: vi.fn(),
  useMutation: vi.fn(() => [{ fetching: false }, vi.fn()]),
  gql: (strs: TemplateStringsArray, ...vals: unknown[]) =>
    strs.reduce((acc, s, i) => acc + s + (vals[i] ?? ""), ""),
}));

// ── mock next/navigation ──────────────────────────────────────────────────
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
}));

// ── mock next/link ────────────────────────────────────────────────────────
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}));

// ── mock sse (no-op stream) ───────────────────────────────────────────────
vi.mock("@/lib/sse", () => ({ useEventStream: vi.fn() }));

// ── mock telemetry ────────────────────────────────────────────────────────
vi.mock("@/lib/telemetry", () => ({ trackEvent: vi.fn() }));

// ── mock understanding-score (heavy component) ────────────────────────────
vi.mock("@/components/understanding-score", () => ({
  LazyScoreBadge: () => null,
}));

// ── import after mocks ────────────────────────────────────────────────────
import { useQuery } from "urql";
import RepositoriesPage from "./page";

afterEach(cleanup);
afterEach(() => vi.clearAllMocks());

// ── helpers ───────────────────────────────────────────────────────────────

// A useQuery mock that cycles through calls:
// call 0 → REPOSITORIES result, call 1 → LIVING_WIKI result.
function setupQueryMocks(
  reposResult: Partial<UseQueryState>,
  lwResult: Partial<UseQueryState> = { data: null, fetching: false, error: undefined },
) {
  let callCount = 0;
  vi.mocked(useQuery).mockImplementation(() => {
    const idx = callCount++;
    const result = idx === 0 ? reposResult : lwResult;
    return [{ fetching: false, error: undefined, data: null, stale: false, ...result } as UseQueryState, vi.fn()];
  });
}

// ─────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────

describe("RepositoriesPage — CA-363 error vs empty distinction", () => {
  it("renders the error alert when the query returns an error", () => {
    setupQueryMocks({
      data: undefined,
      error: new Error("network error") as unknown as UseQueryState["error"],
      fetching: false,
    });

    render(<RepositoriesPage />);

    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByText("Couldn't load repositories")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument();
  });

  it("does NOT render the error alert when the query returns an empty array", () => {
    setupQueryMocks({
      data: { repositories: [] },
      error: undefined,
      fetching: false,
    });

    render(<RepositoriesPage />);

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText("Couldn't load repositories")).not.toBeInTheDocument();
  });

  it("renders the empty state CTA when repos is empty with no error", () => {
    setupQueryMocks({
      data: { repositories: [] },
      error: undefined,
      fetching: false,
    });

    render(<RepositoriesPage />);

    // The EmptyState renders the "no repositories" copy
    expect(screen.getByText(/no repositories indexed yet/i)).toBeInTheDocument();
  });

  it("Retry button calls reexecute with network-only policy", () => {
    const reexecute = vi.fn();
    let callCount = 0;
    vi.mocked(useQuery).mockImplementation(() => {
      const idx = callCount++;
      if (idx === 0) {
        return [
          {
            fetching: false,
            error: new Error("fail") as unknown as UseQueryState["error"],
            data: undefined,
            stale: false,
          } as UseQueryState,
          reexecute,
        ];
      }
      return [{ fetching: false, error: undefined, data: null, stale: false } as UseQueryState, vi.fn()];
    });

    render(<RepositoriesPage />);
    screen.getByRole("button", { name: /retry/i }).click();

    expect(reexecute).toHaveBeenCalledWith({ requestPolicy: "network-only" });
  });
});
