/**
 * CA-364: Workflow Story button follows CA-242 rule:
 *   in-flight → Cancel only
 *   stale/failed → Regenerate (primary)
 *   fresh → Refresh secondary
 *
 * CA-365 (RUBY-H1): "More Ways To Explore" panel applies CA-242 INDEPENDENTLY
 * per artifact. Learning Path and Code Tour each get their own one-button-at-a-time
 * treatment. A Learning Path Cancel does NOT suppress Code Tour's Refresh.
 *
 * CA-370: MEDIUM confidence badge uses --text-inverse-on-amber (not text-white).
 *
 * U-M4: empty-state Workflow Story "Generate story" button is primary (no
 *   variant="secondary").
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { render, screen, cleanup, fireEvent } from "@testing-library/react";
import type { UseQueryState } from "urql";

// ── mock urql + next/navigation before importing the component ────────────
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
  usePathname: () => "/repositories/repo-xyz/knowledge",
  useSearchParams: () => new URLSearchParams(),
}));

vi.mock("urql", () => ({
  useQuery: vi.fn(),
  useMutation: vi.fn(),
  gql: (strs: TemplateStringsArray, ...vals: unknown[]) =>
    strs.reduce((acc, s, i) => acc + s + (vals[i] ?? ""), ""),
}));

vi.mock("@/lib/features", () => ({
  useFeatures: () => ({
    cliffNotes: true,
    learningPaths: true,
    codeTours: true,
    systemExplain: false,
    symbolScopedAnalysis: false,
    multiAudienceKnowledge: false,
    customKnowledgeTemplates: false,
    advancedLearningPaths: false,
    slideGeneration: false,
    podcastGeneration: false,
    knowledgeScheduling: false,
    knowledgeExport: false,
    multiTenant: false,
    sso: false,
  }),
}));

vi.mock("@/lib/telemetry", () => ({ trackEvent: vi.fn() }));
vi.mock("@/lib/notifications", () => ({
  notifyJobEvent: vi.fn(),
  jobAlertsEnabled: () => false,
  enableJobAlerts: vi.fn(),
  disableJobAlerts: vi.fn(),
}));
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve({}) }),
}));
vi.mock("@/components/source/SourceRefLink", () => ({
  SourceRefLink: ({ children }: { children: React.ReactNode }) => <span>{children}</span>,
}));

// ── import after mocks ────────────────────────────────────────────────────
import { useQuery, useMutation } from "urql";
import { KnowledgeTab, type KnowledgeTabProps } from "./knowledge-tab";
import type { KnowledgeArtifact } from "./knowledge-tab";
import type { LLMJobView } from "@/lib/llm/job-types";

// The KnowledgeTab calls useQuery 4 times, in this order:
// 1. KNOWLEDGE_ARTIFACTS_QUERY  (vars: { repositoryId, scopeType, scopePath? })
// 2. KNOWLEDGE_SCOPE_CHILDREN_QUERY (vars: { repositoryId, scopeType, scopePath, audience, depth })
// 3. EXECUTION_ENTRY_POINTS_QUERY (vars: { repositoryId })
// 4. EXECUTION_PATH_QUERY (vars may differ)
//
// We distinguish call 1 from the rest by inspecting the variables object.
// Call 1's variables have exactly 2-3 keys: repositoryId, scopeType, (optional scopePath).
// Call 2 has audience + depth. We key on "audience" being absent in call 1.

const EMPTY_RESULT: UseQueryState = {
  data: null,
  fetching: false,
  error: undefined,
  stale: false,
} as UseQueryState;

function setupKnowledgeQuery(artifacts: KnowledgeArtifact[]) {
  // urql's UseQueryArgs.variables is typed `void` for the no-variables overload,
  // which prevents a narrow destructure-style signature here. Mock with the
  // permissive Parameters<typeof useQuery>[0] and reach into variables manually.
  vi.mocked(useQuery).mockImplementation((args: Parameters<typeof useQuery>[0]) => {
    const vars = (args.variables ?? {}) as Record<string, unknown>;
    // Call 1 (KNOWLEDGE_ARTIFACTS_QUERY): has repositoryId + scopeType, NO audience/depth/entryKind
    if ("repositoryId" in vars && "scopeType" in vars && !("audience" in vars) && !("entryKind" in vars)) {
      return [
        { ...EMPTY_RESULT, data: { knowledgeArtifacts: artifacts } } as UseQueryState,
        vi.fn(),
      ];
    }
    // Call 2 (KNOWLEDGE_SCOPE_CHILDREN_QUERY): has audience + depth
    if ("audience" in vars && "depth" in vars) {
      return [{ ...EMPTY_RESULT, data: { knowledgeScopeChildren: [] } } as UseQueryState, vi.fn()];
    }
    // Call 3 (EXECUTION_ENTRY_POINTS_QUERY): has repositoryId, no scopeType
    if ("repositoryId" in vars && !("scopeType" in vars)) {
      return [{ ...EMPTY_RESULT, data: { executionEntryPoints: [] } } as UseQueryState, vi.fn()];
    }
    // Call 4 (EXECUTION_PATH_QUERY) and any others
    return [EMPTY_RESULT, vi.fn()];
  });
}

afterEach(cleanup);
afterEach(() => vi.clearAllMocks());

beforeEach(() => {
  // Restore useMutation to a safe no-op after vi.clearAllMocks()
  vi.mocked(useMutation).mockReturnValue([{ fetching: false } as ReturnType<typeof useMutation>[0], vi.fn()]);
});

// ─────────────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────────────

function makeArtifact(overrides: { id: string; type: string } & Partial<KnowledgeArtifact>): KnowledgeArtifact {
  return {
    repositoryId: "repo-xyz",
    audience: "DEVELOPER",
    depth: "MEDIUM",
    scope: { scopeType: "REPOSITORY", scopePath: "", modulePath: null, filePath: null, symbolName: null },
    status: "READY",
    progress: 0,
    progressPhase: null,
    progressMessage: null,
    stale: false,
    errorCode: null,
    errorMessage: null,
    generationMode: "UNDERSTANDING_FIRST",
    rendererVersion: null,
    sections: [],
    generatedAt: null,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    ...overrides,
  } as KnowledgeArtifact;
}

function makeJob(artifactId: string, status: LLMJobView["status"], jobType = "generate_workflow_story"): LLMJobView {
  return {
    id: `job-${artifactId}`,
    subsystem: "knowledge",
    job_type: jobType,
    status,
    artifact_id: artifactId,
    progress: 0.5,
    elapsed_ms: 1000,
    updated_at: new Date().toISOString(),
  };
}

function baseProps(overrides: Partial<KnowledgeTabProps> = {}): KnowledgeTabProps {
  return {
    repoId: "repo-xyz",
    active: true,
    repo: { id: "repo-xyz", name: "Test Repo", generationModeDefault: null },
    loadingOps: new Set<string>(),
    startLoading: vi.fn(),
    finishLoading: vi.fn(),
    isLoading: vi.fn(() => false),
    knowledgeLoading: false,
    currentUnderstanding: null,
    understandingBuilding: false,
    understandingJob: null,
    understandingDedupeNote: false,
    setUnderstandingDedupeNote: vi.fn(),
    handleBuildRepositoryUnderstanding: vi.fn(),
    repoJobs: null,
    repoJobsError: null,
    repoActiveJobs: [],
    repoRecentJobs: [],
    cancellingJobIds: {},
    setCancellingJobIds: vi.fn(),
    fetchRepoJobs: vi.fn(),
    reexecuteRepo: vi.fn(),
    ...overrides,
  };
}

function openSection(labelPattern: RegExp) {
  fireEvent.click(screen.getByRole("button", { name: labelPattern }));
}

// ─────────────────────────────────────────────────────────────────────────
// CA-364 — Workflow Story button consolidation
// ─────────────────────────────────────────────────────────────────────────

describe("CA-364 — Workflow Story button consolidation (CA-242 rule)", () => {
  it("empty state: shows single Generate story button (primary, not secondary)", () => {
    setupKnowledgeQuery([]);
    render(<KnowledgeTab {...baseProps()} />);
    openSection(/workflow story/i);

    const generateBtn = screen.getByRole("button", { name: /generate story/i });
    expect(generateBtn).toBeInTheDocument();
    // U-M4: no secondary variant class on the only available action
    expect(generateBtn.className).not.toMatch(/secondary/);
    expect(screen.queryByRole("button", { name: /^cancel$/i })).not.toBeInTheDocument();
  });

  it("in-flight: shows Cancel only — no Refresh or Regenerate", () => {
    const artifact = makeArtifact({ id: "ws-1", type: "WORKFLOW_STORY", status: "GENERATING" });
    const job = makeJob("ws-1", "generating");
    setupKnowledgeQuery([artifact]);

    render(<KnowledgeTab {...baseProps({ repoActiveJobs: [job] })} />);
    openSection(/workflow story/i);

    expect(screen.getByRole("button", { name: /^cancel$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /regenerate/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /refresh story/i })).not.toBeInTheDocument();
  });

  it("stale: shows Regenerate (primary) — no Cancel", () => {
    const artifact = makeArtifact({ id: "ws-2", type: "WORKFLOW_STORY", status: "READY", stale: true });
    setupKnowledgeQuery([artifact]);

    render(<KnowledgeTab {...baseProps()} />);
    openSection(/workflow story/i);

    expect(screen.getByRole("button", { name: /^regenerate$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^cancel$/i })).not.toBeInTheDocument();
  });

  it("failed: shows Regenerate (primary) — no Cancel", () => {
    const artifact = makeArtifact({ id: "ws-3", type: "WORKFLOW_STORY", status: "FAILED" });
    setupKnowledgeQuery([artifact]);

    render(<KnowledgeTab {...baseProps()} />);
    openSection(/workflow story/i);

    expect(screen.getByRole("button", { name: /^regenerate$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^cancel$/i })).not.toBeInTheDocument();
  });

  it("fresh/ready: shows Refresh secondary — no Cancel or Regenerate", () => {
    const artifact = makeArtifact({ id: "ws-4", type: "WORKFLOW_STORY", status: "READY", stale: false });
    setupKnowledgeQuery([artifact]);

    render(<KnowledgeTab {...baseProps()} />);
    openSection(/workflow story/i);

    expect(screen.getByRole("button", { name: /refresh story/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^cancel$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^regenerate$/i })).not.toBeInTheDocument();
  });
});

// ─────────────────────────────────────────────────────────────────────────
// CA-365 — "More Ways To Explore" per-artifact independence
// ─────────────────────────────────────────────────────────────────────────

describe("CA-365 — More Ways To Explore: per-artifact button independence (RUBY-H1)", () => {
  it("Learning Path in-flight shows Cancel; Code Tour fresh shows Refresh — simultaneously", () => {
    const lpArtifact = makeArtifact({ id: "lp-1", type: "LEARNING_PATH", status: "GENERATING" });
    const ctArtifact = makeArtifact({ id: "ct-1", type: "CODE_TOUR", status: "READY", stale: false });
    const lpJob = makeJob("lp-1", "generating", "generate_learning_path");
    setupKnowledgeQuery([lpArtifact, ctArtifact]);

    render(<KnowledgeTab {...baseProps({ repoActiveJobs: [lpJob] })} />);
    openSection(/more ways to explore/i);

    expect(screen.getByRole("button", { name: /^cancel$/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /refresh code tour/i })).toBeInTheDocument();
  });

  it("Code Tour stale shows Regenerate independently of Learning Path fresh state", () => {
    const lpArtifact = makeArtifact({ id: "lp-2", type: "LEARNING_PATH", status: "READY", stale: false });
    const ctArtifact = makeArtifact({ id: "ct-2", type: "CODE_TOUR", status: "READY", stale: true });
    setupKnowledgeQuery([lpArtifact, ctArtifact]);

    render(<KnowledgeTab {...baseProps()} />);
    openSection(/more ways to explore/i);

    expect(screen.getByRole("button", { name: /refresh learning path/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^regenerate$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^cancel$/i })).not.toBeInTheDocument();
  });

  it("both in-flight: both show Cancel independently (two Cancel buttons)", () => {
    const lpArtifact = makeArtifact({ id: "lp-3", type: "LEARNING_PATH", status: "GENERATING" });
    const ctArtifact = makeArtifact({ id: "ct-3", type: "CODE_TOUR", status: "GENERATING" });
    const lpJob = makeJob("lp-3", "generating", "generate_learning_path");
    const ctJob = makeJob("ct-3", "generating", "generate_code_tour");
    setupKnowledgeQuery([lpArtifact, ctArtifact]);

    render(<KnowledgeTab {...baseProps({ repoActiveJobs: [lpJob, ctJob] })} />);
    openSection(/more ways to explore/i);

    const cancelButtons = screen.getAllByRole("button", { name: /^cancel$/i });
    expect(cancelButtons).toHaveLength(2);
  });

  it("empty state for both: each shows Generate (primary, not secondary)", () => {
    setupKnowledgeQuery([]);
    render(<KnowledgeTab {...baseProps()} />);
    openSection(/more ways to explore/i);

    const generateButtons = screen.getAllByRole("button", { name: /^generate$/i });
    expect(generateButtons.length).toBeGreaterThanOrEqual(2);
    generateButtons.forEach((btn) => {
      expect(btn.className).not.toMatch(/secondary/);
    });
  });
});

// ─────────────────────────────────────────────────────────────────────────
// CA-370 — MEDIUM confidence badge uses dark text token
// ─────────────────────────────────────────────────────────────────────────

describe("CA-370 — MEDIUM confidence badge contrast", () => {
  it("MEDIUM section badge has text-[var(--text-inverse-on-amber)] class, not text-white", () => {
    const artifact = makeArtifact({
      id: "ws-badge-1",
      type: "WORKFLOW_STORY",
      status: "READY",
      stale: false,
      sections: [
        {
          id: "sec-1",
          artifactId: "ws-badge-1",
          title: "Overview",
          summary: "A test section",
          content: "Some content here",
          confidence: "MEDIUM",
          orderIndex: 0,
          inferred: false,
          evidence: [],
        },
      ],
    });
    setupKnowledgeQuery([artifact]);

    render(<KnowledgeTab {...baseProps()} />);
    openSection(/workflow story/i);

    const badge = screen.getByText("MEDIUM");
    expect(badge).toBeInTheDocument();
    expect(badge.className).toContain("text-[var(--text-inverse-on-amber)]");
    expect(badge.className).not.toContain("text-white");
  });

  it("HIGH section badge still uses text-white", () => {
    const artifact = makeArtifact({
      id: "ws-badge-2",
      type: "WORKFLOW_STORY",
      status: "READY",
      stale: false,
      sections: [
        {
          id: "sec-2",
          artifactId: "ws-badge-2",
          title: "Architecture",
          summary: "Summary",
          content: "Content",
          confidence: "HIGH",
          orderIndex: 0,
          inferred: false,
          evidence: [],
        },
      ],
    });
    setupKnowledgeQuery([artifact]);

    render(<KnowledgeTab {...baseProps()} />);
    openSection(/workflow story/i);

    const badge = screen.getByText("HIGH");
    expect(badge.className).toContain("text-white");
    expect(badge.className).not.toContain("text-inverse-on-amber");
  });
});

// ─────────────────────────────────────────────────────────────────────────
// CA-367 — accordion ARIA reciprocal link (3-attribute checklist per header)
// ─────────────────────────────────────────────────────────────────────────

describe("CA-367 — accordion ARIA: aria-expanded + aria-controls ↔ panel id + role=region + aria-labelledby", () => {
  function renderTab() {
    setupKnowledgeQuery([]);
    render(<KnowledgeTab {...baseProps()} />);
  }

  const accordions: Array<{
    headerId: string;
    panelId: string;
  }> = [
    { headerId: "accordion-header-guide", panelId: "accordion-panel-guide" },
    { headerId: "accordion-header-execution", panelId: "accordion-panel-execution" },
    { headerId: "accordion-header-workflow", panelId: "accordion-panel-workflow" },
    { headerId: "accordion-header-ask", panelId: "accordion-panel-ask" },
    { headerId: "accordion-header-explore", panelId: "accordion-panel-explore" },
  ];

  for (const { headerId, panelId } of accordions) {
    it(`${headerId}: collapsed state — aria-expanded=false, panel absent`, () => {
      renderTab();
      // guide opens by default; reset to closed state for execution/workflow
      const button = screen.getAllByRole("button").find((b) => b.id === headerId);
      if (!button) return; // accordion may not render if feature-flagged
      if (button.getAttribute("aria-expanded") === "true") {
        fireEvent.click(button);
      }
      const btn = screen.getAllByRole("button").find((b) => b.id === headerId)!;
      expect(btn.getAttribute("aria-expanded")).toBe("false");
      expect(btn.getAttribute("aria-controls")).toBe(panelId);
      expect(document.getElementById(panelId)).toBeNull();
    });

    it(`${headerId}: expanded state — aria-expanded=true, panel has id + role=region + aria-labelledby`, () => {
      renderTab();
      const button = screen.getAllByRole("button").find((b) => b.id === headerId);
      if (!button) return;
      if (button.getAttribute("aria-expanded") !== "true") {
        fireEvent.click(button);
      }
      const btn = screen.getAllByRole("button").find((b) => b.id === headerId)!;
      expect(btn.getAttribute("aria-expanded")).toBe("true");
      expect(btn.getAttribute("aria-controls")).toBe(panelId);

      const panel = document.getElementById(panelId);
      expect(panel).not.toBeNull();
      expect(panel!.getAttribute("role")).toBe("region");
      expect(panel!.getAttribute("aria-labelledby")).toBe(headerId);
    });
  }
});
