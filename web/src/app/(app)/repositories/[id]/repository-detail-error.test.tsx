/**
 * CA-363: repository detail page distinguishes a network / server error
 * (repoResult.error != null) from a genuine 404 (no error but no repo data).
 *
 * Strategy: vi.mock("urql") + vi.mock("next/navigation") + stub all tab
 * sub-components to avoid deep render-tree complexity.
 */

import { describe, it, expect, afterEach, vi } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import type { UseQueryState } from "urql";

// ── mock urql ─────────────────────────────────────────────────────────────
vi.mock("urql", () => ({
  useQuery: vi.fn(),
  useMutation: vi.fn(() => [{ fetching: false }, vi.fn()]),
  gql: (strs: TemplateStringsArray, ...vals: unknown[]) =>
    strs.reduce((acc, s, i) => acc + s + (vals[i] ?? ""), ""),
}));

// ── mock next/navigation ──────────────────────────────────────────────────
vi.mock("next/navigation", () => ({
  useParams: () => ({ id: "repo-abc" }),
  usePathname: () => "/repositories/repo-abc",
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

// ── mock next/link ────────────────────────────────────────────────────────
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
}));

// ── stub heavy sub-components ─────────────────────────────────────────────
vi.mock("./tabs/knowledge-tab", () => ({ KnowledgeTab: () => <div data-testid="knowledge-tab" /> }));
vi.mock("./tabs/symbols-tab", () => ({ SymbolsTab: () => null }));
vi.mock("./tabs/settings-tab", () => ({ SettingsTab: () => null }));
vi.mock("./tabs/files-tab", () => ({ FilesTab: () => null }));
vi.mock("./tabs/requirements-tab", () => ({ RequirementsTab: () => null }));
vi.mock("./tabs/specs-tab", () => ({ SpecsTab: () => null }));
vi.mock("./tabs/analysis-tab", () => ({ AnalysisTab: () => null }));
vi.mock("./tabs/impact-tab", () => ({ ImpactTab: () => null }));
vi.mock("./tabs/architecture-tab", () => ({ ArchitectureTab: () => null }));
vi.mock("./tabs/related-tab", () => ({ RelatedTab: () => null }));
vi.mock("./tabs/subsystems-tab", () => ({ SubsystemsTab: () => null }));
vi.mock("./repository-detail-skeleton", () => ({
  RepositoryDetailSkeleton: () => <div data-testid="skeleton" />,
}));

// ── mock misc dependencies ────────────────────────────────────────────────
vi.mock("@/lib/features", () => ({ useFeatures: () => ({ cliffNotes: false, learningPaths: false, codeTours: false }) }));
vi.mock("@/lib/telemetry", () => ({ trackEvent: vi.fn() }));
vi.mock("@/lib/notifications", () => ({
  notifyJobEvent: vi.fn(),
  jobAlertsEnabled: () => false,
  enableJobAlerts: vi.fn(),
  disableJobAlerts: vi.fn(),
}));
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: vi.fn().mockResolvedValue({
    ok: true,
    json: () => Promise.resolve({ active: [], recent: [], stats: { in_flight: 0, queue_depth: 0, max_concurrency: 1 } }),
  }),
}));
vi.mock("@/lib/llm/activity", () => ({ normalizeActivityResponse: vi.fn(() => ({ active: [], recent: [], stats: {} })) }));
vi.mock("@/components/understanding-score", () => ({
  LazyScoreBreakdown: () => null,
}));
vi.mock("@/components/repository/UpstreamStalenessPill", () => ({
  UpstreamStalenessPill: () => null,
}));
vi.mock("@/components/llm/repo-jobs-popover", () => ({
  RepoJobsPopover: () => null,
}));
vi.mock("@/lib/understanding", () => ({
  understandingStageHasReadableContent: () => false,
  understandingStageIsRunning: () => false,
}));

// ── import after mocks ────────────────────────────────────────────────────
import { useQuery } from "urql";
import RepositoryDetailPage from "./page";

afterEach(cleanup);
afterEach(() => vi.clearAllMocks());

// ─────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────

function setupQueryMocks(repoResult: Partial<UseQueryState>, otherResult: Partial<UseQueryState> = { data: null }) {
  let callCount = 0;
  vi.mocked(useQuery).mockImplementation(() => {
    const idx = callCount++;
    const base = { fetching: false, error: undefined, data: null, stale: false };
    const result = idx === 0 ? { ...base, ...repoResult } : { ...base, ...otherResult };
    return [result as UseQueryState, vi.fn()];
  });
}

// ─────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────

describe("RepositoryDetailPage — CA-363 error vs 404 distinction", () => {
  it("renders the error alert when the repo query returns an error with no data", () => {
    setupQueryMocks({
      data: undefined,
      error: new Error("500 Internal Server Error") as unknown as UseQueryState["error"],
      fetching: false,
    });

    render(<RepositoryDetailPage />);

    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByText("Couldn't load repository")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument();
  });

  it("does NOT render the error alert for a genuine 404 (no error, no data)", () => {
    setupQueryMocks({
      data: { repository: null },
      error: undefined,
      fetching: false,
    });

    render(<RepositoryDetailPage />);

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText("Couldn't load repository")).not.toBeInTheDocument();
  });

  it("renders 'Repository not found' for the 404 case", () => {
    setupQueryMocks({
      data: { repository: null },
      error: undefined,
      fetching: false,
    });

    render(<RepositoryDetailPage />);

    expect(screen.getByText("Repository not found")).toBeInTheDocument();
  });

  it("Retry button calls reexecuteRepo with network-only policy", () => {
    const reexecuteRepo = vi.fn();
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
          reexecuteRepo,
        ];
      }
      return [{ fetching: false, error: undefined, data: null, stale: false } as UseQueryState, vi.fn()];
    });

    render(<RepositoryDetailPage />);
    screen.getByRole("button", { name: /retry/i }).click();

    expect(reexecuteRepo).toHaveBeenCalledWith({ requestPolicy: "network-only" });
  });

  it("renders the skeleton when fetching is true", () => {
    setupQueryMocks({ data: undefined, fetching: true, error: undefined });

    render(<RepositoryDetailPage />);

    expect(screen.getByTestId("skeleton")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
