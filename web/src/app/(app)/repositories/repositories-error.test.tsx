/**
 * CA-363: repositories list page distinguishes a network / server error
 * from an empty-repositories result.
 *
 * CA-542 / F1: empty-state CTA when no active LLM profile is configured.
 *
 * Strategy: vi.mock("urql") + vi.mock("next/navigation") to isolate the
 * page from infrastructure. The test controls useQuery return values to
 * exercise the error branch (result.error != null) and the empty branch
 * (result.data.repositories == []) independently. authFetch is mocked to
 * control the LLM-profile probe result for CA-542 tests.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { render, screen, cleanup, waitFor } from "@testing-library/react";
import type { UseQueryState } from "urql";

// ── mock urql before importing the component ──────────────────────────────
vi.mock("urql", () => ({
  useQuery: vi.fn(),
  useMutation: vi.fn(() => [{ fetching: false }, vi.fn()]),
  gql: (strs: TemplateStringsArray, ...vals: unknown[]) =>
    strs.reduce((acc, s, i) => acc + s + (vals[i] ?? ""), ""),
}));

// ── mock next/navigation ──────────────────────────────────────────────────
const mockPush = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mockPush }),
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

// ── mock authFetch (CA-542 LLM-probe) ─────────────────────────────────────
const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

// ── mock useCurrentUser + isAdminRole (CA-542) ────────────────────────────
// Default: user is admin (OSS single-user dominant case).
vi.mock("@/lib/current-user", () => ({
  useCurrentUser: vi.fn(() => ({ role: "admin" })),
  isAdminRole: vi.fn((role: string | undefined) => !role || role === "admin" || role === "owner"),
}));

// ── import after mocks ────────────────────────────────────────────────────
import { useQuery } from "urql";
import { useCurrentUser } from "@/lib/current-user";
import RepositoriesPage from "./page";

afterEach(cleanup);
afterEach(() => vi.clearAllMocks());
beforeEach(() => {
  // Default: LLM probe returns "configured" so existing empty state renders.
  mockAuthFetch.mockResolvedValue({
    ok: true,
    json: async () => ({
      profiles: [{ is_active: true, provider: "anthropic" }],
      active_profile_missing: false,
    }),
  });
});

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

// ─────────────────────────────────────────────────────────────────────────
// CA-542 / F1 — empty-state CTA when no active LLM profile is configured
// ─────────────────────────────────────────────────────────────────────────

// Helper: set up repos query for empty state (no repos, no error, not fetching).
function setupEmptyReposQuery() {
  let callCount = 0;
  vi.mocked(useQuery).mockImplementation(() => {
    const idx = callCount++;
    if (idx === 0) {
      return [{ fetching: false, error: undefined, data: { repositories: [] }, stale: false } as UseQueryState, vi.fn()];
    }
    return [{ fetching: false, error: undefined, data: null, stale: false } as UseQueryState, vi.fn()];
  });
}

describe("RepositoriesPage — CA-542 / F1 LLM-probe empty-state CTA", () => {
  it("admin + empty repos + no active LLM provider → renders Configure AI provider CTA", async () => {
    setupEmptyReposQuery();
    // Probe returns: active profile exists but provider is empty (not configured).
    mockAuthFetch.mockResolvedValue({
      ok: true,
      json: async () => ({
        profiles: [{ is_active: true, provider: "" }],
        active_profile_missing: false,
      }),
    });

    render(<RepositoriesPage />);

    await waitFor(() => {
      expect(screen.getByText("Configure your AI provider to get started")).toBeInTheDocument();
    });
    expect(screen.getByText(/SourceBridge uses an LLM/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /configure ai provider/i })).toBeInTheDocument();
    // Existing "No repositories indexed yet" should NOT appear.
    expect(screen.queryByText(/no repositories indexed yet/i)).toBeNull();
  });

  it("admin + empty repos + active_profile_missing=true → renders Configure AI provider CTA", async () => {
    setupEmptyReposQuery();
    mockAuthFetch.mockResolvedValue({
      ok: true,
      json: async () => ({
        profiles: [],
        active_profile_missing: true,
      }),
    });

    render(<RepositoriesPage />);

    await waitFor(() => {
      expect(screen.getByText("Configure your AI provider to get started")).toBeInTheDocument();
    });
  });

  it("admin + empty repos + active profile with provider → existing empty state (not CTA)", async () => {
    setupEmptyReposQuery();
    // beforeEach default already returns a configured profile; make explicit.
    mockAuthFetch.mockResolvedValue({
      ok: true,
      json: async () => ({
        profiles: [{ is_active: true, provider: "anthropic" }],
        active_profile_missing: false,
      }),
    });

    render(<RepositoriesPage />);

    await waitFor(() => {
      expect(screen.getByText(/no repositories indexed yet/i)).toBeInTheDocument();
    });
    expect(screen.queryByText("Configure your AI provider to get started")).toBeNull();
  });

  it("non-admin + empty repos → existing empty state, no fetch issued (Decision 1)", async () => {
    setupEmptyReposQuery();
    vi.mocked(useCurrentUser).mockReturnValue({ role: "viewer" } as ReturnType<typeof useCurrentUser>);

    render(<RepositoriesPage />);

    // Non-admin: existing empty state renders immediately (no probe needed).
    expect(screen.getByText(/no repositories indexed yet/i)).toBeInTheDocument();
    expect(screen.queryByText("Configure your AI provider to get started")).toBeNull();
    // authFetch must NOT have been called (non-admin skips probe).
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  it("admin + fetch in flight (probe pending) → existing empty state, NOT the new CTA (ruby H1 / Decision 4)", () => {
    setupEmptyReposQuery();
    // Simulate a never-resolving fetch so probe stays pending.
    mockAuthFetch.mockReturnValue(new Promise(() => {}));

    render(<RepositoriesPage />);

    // While probe is unresolved (checked=false), existing empty state renders.
    expect(screen.getByText(/no repositories indexed yet/i)).toBeInTheDocument();
    expect(screen.queryByText("Configure your AI provider to get started")).toBeNull();
  });

  it("admin + probe returns 403 → existing empty state (fallback to safe default)", async () => {
    setupEmptyReposQuery();
    mockAuthFetch.mockResolvedValue({
      ok: false,
      status: 403,
      json: async () => ({}),
    });

    render(<RepositoriesPage />);

    // 403 → configured=true → existing empty state.
    await waitFor(() => {
      expect(screen.getByText(/no repositories indexed yet/i)).toBeInTheDocument();
    });
    expect(screen.queryByText("Configure your AI provider to get started")).toBeNull();
  });
});
