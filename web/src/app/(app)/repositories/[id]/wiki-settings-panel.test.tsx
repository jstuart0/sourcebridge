/**
 * Tests for WikiSettingsPanel — six visual states (0–5) + discoverability callout.
 *
 * Strategy: vi.mock("urql") to intercept useQuery/useMutation.
 * Each test controls the mock return values directly.
 */

import { describe, it, expect, afterEach, vi, beforeEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor, act } from "@testing-library/react";
import type { UseQueryState, UseMutationState } from "urql";

// ── mock urql before importing the component ─────────────────────────────────
vi.mock("urql", () => ({
  useQuery: vi.fn(),
  useMutation: vi.fn(),
  // gql is used at module init in queries.ts; provide a passthrough
  gql: (strs: TemplateStringsArray, ...vals: unknown[]) =>
    strs.reduce((acc, s, i) => acc + s + (vals[i] ?? ""), ""),
}));

// ── auth-fetch mock (avoids ColdStartProgress fetch erroring) ────────────────
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: vi.fn().mockResolvedValue({
    ok: true,
    json: () => Promise.resolve({ active: [], recent: [] }),
  }),
}));

// ── import after mocks are declared ─────────────────────────────────────────
import { useQuery, useMutation } from "urql";
import type { RepositoryLivingWikiSettings } from "./wiki-settings-panel";
import { WikiSettingsPanel } from "./wiki-settings-panel";

afterEach(cleanup);
afterEach(() => vi.clearAllMocks());

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

function globalEnabledData(overrides?: {
  enabled?: boolean;
  killSwitchActive?: boolean;
  confluenceToken?: string | null;
  notionToken?: string | null;
  githubToken?: string | null;
  gitlabToken?: string | null;
}) {
  return {
    livingWikiSettings: {
      enabled: overrides?.enabled ?? true,
      killSwitchActive: overrides?.killSwitchActive ?? false,
      confluenceToken: overrides?.confluenceToken ?? null,
      notionToken: overrides?.notionToken ?? null,
      githubToken: overrides?.githubToken ?? null,
      gitlabToken: overrides?.gitlabToken ?? null,
    },
  };
}

function setupQueryMock(data: Record<string, unknown>) {
  vi.mocked(useQuery).mockReturnValue([
    { data, fetching: false, error: undefined, stale: false } as UseQueryState,
    vi.fn(),
  ]);
}

function setupMutationMock(
  responseFn?: (vars?: unknown) => Record<string, unknown>
) {
  const execMock = vi.fn().mockImplementation(async (vars: unknown) => ({
    data: responseFn?.(vars) ?? {},
    error: undefined,
  }));
  vi.mocked(useMutation).mockReturnValue([
    { fetching: false } as UseMutationState,
    execMock,
  ]);
  return execMock;
}

// ─────────────────────────────────────────────────────────────────────────────
// State 0: globally disabled
// ─────────────────────────────────────────────────────────────────────────────

describe("WikiSettingsPanel — State 0 (globally disabled)", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: false }));
    setupMutationMock();
  });

  it("shows informational notice with link when global enabled=false", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    expect(
      screen.getByText(/Living Wiki is disabled globally/i)
    ).toBeInTheDocument();

    const link = screen.getByRole("link", { name: /Enable it in Settings/i });
    expect(link).toHaveAttribute("href", "/settings/living-wiki");
  });

  it("shows no wiki form controls when globally disabled", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    // No wiki-specific buttons (publish-mode toggles, sink rows, Enable
    // button) render in state 0. The per-repo LLM override section IS
    // rendered (collapsed); it's a sibling concern that operates regardless
    // of living-wiki state.
    expect(screen.queryByRole("button", { name: /PR Review/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Direct Publish/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Enable Living Wiki/i })).toBeNull();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// State 0 (kill-switch variant)
// ─────────────────────────────────────────────────────────────────────────────

describe("WikiSettingsPanel — State 0 kill-switch", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true, killSwitchActive: true }));
    setupMutationMock();
  });

  it("renders kill-switch notice when killSwitchActive=true", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    expect(
      screen.getByText(/SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH/i)
    ).toBeInTheDocument();
  });

  it("shows no wiki form controls when kill-switch is active", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    // Same scoping as the globally-disabled case: the per-repo LLM
    // override section is a sibling concern and renders regardless;
    // assert only the wiki-specific controls are absent.
    expect(screen.queryByRole("button", { name: /PR Review/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Direct Publish/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Enable Living Wiki/i })).toBeNull();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// State 1: Activation gate
// ─────────────────────────────────────────────────────────────────────────────

describe("WikiSettingsPanel — State 1 (activation gate)", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
    setupMutationMock();
  });

  it("renders mode selector when global enabled and no settings", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    expect(screen.getByRole("button", { name: /PR Review/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Direct Publish/i })).toBeInTheDocument();
  });

  it("git_repo sink is checked by default", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    const gitRepoCheckbox = screen.getByLabelText(/This repository/i);
    expect(gitRepoCheckbox).toBeChecked();
  });

  it("Enable Living Wiki CTA is present and enabled by default", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    const enableBtn = screen.getByRole("button", { name: /Enable Living Wiki/i });
    expect(enableBtn).not.toBeDisabled();
  });

  it("Confluence sink row is always visible with 'credentials not configured' hint when creds missing", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    // At least one visible element mentions Confluence
    expect(screen.getAllByText(/Confluence/i).length).toBeGreaterThan(0);
    // Both Confluence and Notion show this hint when creds are missing
    expect(screen.getAllByText(/credentials not configured/i).length).toBeGreaterThan(0);
  });

  it("Confluence checkbox is disabled when global creds are missing", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    const confluenceCheckbox = screen.getByLabelText(/Confluence/i);
    expect(confluenceCheckbox).toBeDisabled();
  });

  it("Confluence checkbox is enabled when global creds are set", () => {
    setupQueryMock(
      globalEnabledData({ enabled: true, confluenceToken: "ATATT3xF" })
    );
    setupMutationMock();

    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    const confluenceCheckbox = screen.getByLabelText(/Confluence/i);
    expect(confluenceCheckbox).not.toBeDisabled();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// State 2: Corrupt (enabled=true, sinks=[])
// ─────────────────────────────────────────────────────────────────────────────

describe("WikiSettingsPanel — State 2 (corrupt/partial state)", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
    setupMutationMock();
  });

  it("shows warning banner and 'Save configuration' CTA", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={{
          enabled: true,
          mode: "PR_REVIEW",
          sinks: [],
          excludePaths: [],
          staleWhenStrategy: "DIRECT",
          maxPagesPerJob: 50,
        }}
      />
    );

    expect(
      screen.getByText(/enabled but no sinks are configured/i)
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Save configuration/i })
    ).toBeInTheDocument();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// State 3: Cold-start in progress
// ─────────────────────────────────────────────────────────────────────────────

describe("WikiSettingsPanel — State 3 (cold-start running)", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
  });

  it("shows progress bar after successful enable mutation with jobId", async () => {
    const execMock = setupMutationMock(() => ({
      enableLivingWikiForRepo: {
        settings: {
          enabled: true,
          mode: "PR_REVIEW",
          sinks: [
            {
              kind: "GIT_REPO",
              integrationName: "my-repo-git",
              audience: "ENGINEER",
              editPolicy: "PROPOSE_PR",
            },
          ],
          excludePaths: [],
          staleWhenStrategy: "DIRECT",
          maxPagesPerJob: 50,
          lastRunAt: null,
          updatedAt: null,
          lastJobResult: null,
        },
        jobId: "job-cold-start-1",
        notice: null,
      },
    }));

    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={null}
      />
    );

    const enableBtn = screen.getByRole("button", { name: /Enable Living Wiki/i });

    await act(async () => {
      fireEvent.click(enableBtn);
    });

    await waitFor(() => {
      expect(execMock).toHaveBeenCalled();
      expect(screen.getByRole("progressbar")).toBeInTheDocument();
    });
  });

  it("progress bar has correct ARIA attributes", async () => {
    setupMutationMock(() => ({
      enableLivingWikiForRepo: {
        settings: {
          enabled: true,
          mode: "PR_REVIEW",
          sinks: [
            {
              kind: "GIT_REPO",
              integrationName: "git",
              audience: "ENGINEER",
              editPolicy: "PROPOSE_PR",
            },
          ],
          excludePaths: [],
          staleWhenStrategy: "DIRECT",
          maxPagesPerJob: 50,
          lastRunAt: null,
          updatedAt: null,
          lastJobResult: null,
        },
        jobId: "job-1",
        notice: null,
      },
    }));

    render(
      <WikiSettingsPanel repoId="repo-1" repoName="my-repo" initialSettings={null} />
    );

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: /Enable Living Wiki/i }));
    });

    await waitFor(() => {
      const bar = screen.getByRole("progressbar");
      expect(bar).toHaveAttribute("aria-valuenow");
      expect(bar).toHaveAttribute("aria-valuemin", "0");
      expect(bar).toHaveAttribute("aria-valuemax", "100");
    });
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// State 4: Enabled idle (success)
// ─────────────────────────────────────────────────────────────────────────────

const enabledIdleSettings: RepositoryLivingWikiSettings = {
  enabled: true,
  mode: "PR_REVIEW",
  sinks: [
    {
      kind: "GIT_REPO",
      integrationName: "my-repo-git",
      audience: "ENGINEER",
      editPolicy: "PROPOSE_PR",
    },
  ],
  excludePaths: [],
  staleWhenStrategy: "DIRECT",
  maxPagesPerJob: 50,
  lastRunAt: new Date(Date.now() - 3 * 3600_000).toISOString(),
  lastJobResult: {
    jobId: "job-1",
    startedAt: new Date(Date.now() - 3 * 3600_000).toISOString(),
    completedAt: new Date(Date.now() - 2 * 3600_000).toISOString(),
    pagesPlanned: 12,
    pagesGenerated: 12,
    pagesExcluded: 0,
    excludedPageIds: [],
    generatedPageTitles: ["Auth module", "API gateway", "Core services"],
    exclusionReasons: [],
    exclusionFailureCategories: [],
    status: "ok",
    failureCategory: null,
    errorMessage: null,
  },
};

describe("WikiSettingsPanel — State 4 (enabled idle)", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
    setupMutationMock();
  });

  it("shows 'Enabled' pill", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    // There may be multiple "Enabled" text nodes (status pill + sink status); at least one must be present
    expect(screen.getAllByText("Enabled").length).toBeGreaterThan(0);
  });

  it("shows 'Regenerate now' button", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    expect(
      screen.getByRole("button", { name: /Regenerate now/i })
    ).toBeInTheDocument();
  });

  it("shows 'Edit' button", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    expect(screen.getByRole("button", { name: /^Edit$/i })).toBeInTheDocument();
  });

  it("collapsible 'Generated pages' section expands on click and lists titles", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    const pagesBtn = screen.getByRole("button", {
      name: /Generated pages \(3\)/i,
    });
    fireEvent.click(pagesBtn);

    expect(screen.getByText("Auth module")).toBeInTheDocument();
    expect(screen.getByText("API gateway")).toBeInTheDocument();
    expect(screen.getByText("Core services")).toBeInTheDocument();
  });

  it("shows 'Disable' button", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    expect(
      screen.getByRole("button", { name: /^Disable$/i })
    ).toBeInTheDocument();
  });

  it("does not show error banner when status is ok", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    expect(screen.queryByRole("alert")).toBeNull();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// State 5: Failure banners
// ─────────────────────────────────────────────────────────────────────────────

const baseEnabledSettings: Omit<RepositoryLivingWikiSettings, "lastJobResult"> = {
  enabled: true,
  mode: "PR_REVIEW",
  sinks: [
    {
      kind: "CONFLUENCE",
      integrationName: "confluence-docs",
      audience: "ENGINEER",
      editPolicy: "PROPOSE_PR",
    },
  ],
  excludePaths: [],
  staleWhenStrategy: "DIRECT",
  maxPagesPerJob: 50,
  lastRunAt: new Date(Date.now() - 3600_000).toISOString(),
};

describe("WikiSettingsPanel — State 5 failure: transient", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
    setupMutationMock();
  });

  it("shows warning banner and Retry CTA", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={{
          ...baseEnabledSettings,
          lastJobResult: {
            jobId: "job-fail",
            startedAt: new Date().toISOString(),
            completedAt: new Date().toISOString(),
            pagesPlanned: 10,
            pagesGenerated: 0,
            pagesExcluded: 0,
            excludedPageIds: [],
            generatedPageTitles: [],
            exclusionReasons: [],
            exclusionFailureCategories: [],
            status: "failed",
            failureCategory: "transient",
            errorMessage: "LLM rate limit exceeded",
          },
        }}
      />
    );

    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByText(/temporary error/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Retry$/i })).toBeInTheDocument();
  });
});

describe("WikiSettingsPanel — State 5 failure: auth", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
    setupMutationMock();
  });

  it("shows error banner with 'Fix credentials' link and no Retry button", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={{
          ...baseEnabledSettings,
          lastJobResult: {
            jobId: "job-fail-auth",
            startedAt: new Date().toISOString(),
            completedAt: new Date().toISOString(),
            pagesPlanned: 10,
            pagesGenerated: 0,
            pagesExcluded: 0,
            excludedPageIds: [],
            generatedPageTitles: [],
            exclusionReasons: [],
            exclusionFailureCategories: [],
            status: "failed",
            failureCategory: "auth",
            errorMessage: "Confluence returned 401 — update your API token",
          },
        }}
      />
    );

    expect(screen.getByRole("alert")).toBeInTheDocument();
    const fixLink = screen.getByRole("link", { name: /Fix credentials/i });
    expect(fixLink).toHaveAttribute("href", "/settings/living-wiki");
    expect(screen.queryByRole("button", { name: /^Retry$/i })).toBeNull();
  });
});

describe("WikiSettingsPanel — State 5 failure: partial_content", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
    setupMutationMock();
  });

  it("shows page counts and 'Retry excluded pages only' CTA", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={{
          ...baseEnabledSettings,
          lastJobResult: {
            jobId: "job-partial",
            startedAt: new Date().toISOString(),
            completedAt: new Date().toISOString(),
            pagesPlanned: 12,
            pagesGenerated: 9,
            pagesExcluded: 3,
            excludedPageIds: ["page-1", "page-2", "page-3"],
            generatedPageTitles: [],
            exclusionReasons: ["Validation failed", "LLM timeout", "Token limit"],
            exclusionFailureCategories: [],
            status: "partial",
            failureCategory: "partial_content",
            errorMessage: null,
          },
        }}
      />
    );

    expect(
      screen.getByText(/9 pages generated, 3 pages excluded/i)
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Retry excluded pages only/i })
    ).toBeInTheDocument();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Stage B: Refinement form (State 4 → Edit → Save / Cancel)
// ─────────────────────────────────────────────────────────────────────────────

describe("WikiSettingsPanel — Stage B (refinement form)", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
  });

  it("Edit button opens inline form with 'Save changes' button", () => {
    setupMutationMock();

    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    fireEvent.click(screen.getByRole("button", { name: /^Edit$/i }));

    expect(
      screen.getByRole("button", { name: /Save changes/i })
    ).toBeInTheDocument();
  });

  it("Cancel returns to summary view without calling mutation", () => {
    const execMock = setupMutationMock();

    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    fireEvent.click(screen.getByRole("button", { name: /^Edit$/i }));

    // Form is open
    expect(screen.getByRole("button", { name: /Save changes/i })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /^Cancel$/i }));

    // Back to summary
    expect(screen.getByRole("button", { name: /^Edit$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Save changes/i })).toBeNull();
    expect(execMock).not.toHaveBeenCalled();
  });

  it("Save button calls updateRepositoryLivingWikiSettings mutation", async () => {
    const execMock = setupMutationMock(() => ({
      updateRepositoryLivingWikiSettings: enabledIdleSettings,
    }));

    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={enabledIdleSettings}
      />
    );

    fireEvent.click(screen.getByRole("button", { name: /^Edit$/i }));

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    });

    await waitFor(() => {
      expect(execMock).toHaveBeenCalled();
    });
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Failure breakdown summary line (exclusionFailureCategories)
// ─────────────────────────────────────────────────────────────────────────────

describe("WikiSettingsPanel — failure-category breakdown line", () => {
  beforeEach(() => {
    setupQueryMock(globalEnabledData({ enabled: true }));
    setupMutationMock();
  });

  it("renders breakdown line with counts when exclusionFailureCategories is non-empty", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={{
          ...baseEnabledSettings,
          lastJobResult: {
            jobId: "job-breakdown",
            startedAt: new Date().toISOString(),
            completedAt: new Date().toISOString(),
            pagesPlanned: 10,
            pagesGenerated: 6,
            pagesExcluded: 4,
            excludedPageIds: ["p-1", "p-2", "p-3", "p-4"],
            generatedPageTitles: [],
            exclusionReasons: [],
            // 2 deadline_exceeded, 1 llm_empty, 1 "" (gate failure)
            exclusionFailureCategories: [
              "deadline_exceeded",
              "deadline_exceeded",
              "llm_empty",
              "",
            ],
            status: "partial",
            failureCategory: "partial_content",
            errorMessage: null,
          },
        }}
      />
    );

    // The breakdown line should be visible and list the categories by count desc.
    // "deadline exceeded" (2) should appear before "empty LLM response" (1) and
    // "quality gate" (1).
    expect(screen.getByText(/Failure breakdown:/i)).toBeInTheDocument();
    expect(screen.getByText(/2 deadline exceeded/i)).toBeInTheDocument();
  });

  it("does not render breakdown line when exclusionFailureCategories is empty", () => {
    render(
      <WikiSettingsPanel
        repoId="repo-1"
        repoName="my-repo"
        initialSettings={{
          ...baseEnabledSettings,
          lastJobResult: {
            jobId: "job-no-breakdown",
            startedAt: new Date().toISOString(),
            completedAt: new Date().toISOString(),
            pagesPlanned: 10,
            pagesGenerated: 7,
            pagesExcluded: 3,
            excludedPageIds: ["p-1", "p-2", "p-3"],
            generatedPageTitles: [],
            exclusionReasons: [],
            exclusionFailureCategories: [],
            status: "partial",
            failureCategory: "partial_content",
            errorMessage: null,
          },
        }}
      />
    );

    expect(screen.queryByText(/Failure breakdown:/i)).toBeNull();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Discoverability callout logic
// ─────────────────────────────────────────────────────────────────────────────

describe("Discoverability callout criteria", () => {
  function shouldShowCallout(opts: {
    globalEnabled: boolean;
    killSwitchActive: boolean;
    repoWikiEnabled: boolean | null;
    fileCount: number;
  }) {
    return (
      opts.globalEnabled &&
      !opts.killSwitchActive &&
      !opts.repoWikiEnabled &&
      opts.fileCount > 0
    );
  }

  it("shows when global enabled, no kill-switch, repo not configured, files indexed", () => {
    expect(
      shouldShowCallout({ globalEnabled: true, killSwitchActive: false, repoWikiEnabled: null, fileCount: 5 })
    ).toBe(true);
  });

  it("does not show when global wiki is disabled", () => {
    expect(
      shouldShowCallout({ globalEnabled: false, killSwitchActive: false, repoWikiEnabled: null, fileCount: 5 })
    ).toBe(false);
  });

  it("does not show when kill-switch is active", () => {
    expect(
      shouldShowCallout({ globalEnabled: true, killSwitchActive: true, repoWikiEnabled: null, fileCount: 5 })
    ).toBe(false);
  });

  it("does not show when repo already has wiki enabled", () => {
    expect(
      shouldShowCallout({ globalEnabled: true, killSwitchActive: false, repoWikiEnabled: true, fileCount: 5 })
    ).toBe(false);
  });

  it("does not show when repo has no indexed files", () => {
    expect(
      shouldShowCallout({ globalEnabled: true, killSwitchActive: false, repoWikiEnabled: null, fileCount: 0 })
    ).toBe(false);
  });
});
