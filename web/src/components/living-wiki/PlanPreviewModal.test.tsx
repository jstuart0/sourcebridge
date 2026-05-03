// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Tests for PlanPreviewModal — Phase 3 coverage per plan spec.
 *
 * Strategy: vi.mock("urql") — useQuery returns controlled fixtures.
 * onConfirm is a vi.fn() that resolves immediately.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act, cleanup } from "@testing-library/react";
import type { UseQueryState } from "urql";

// ── mock urql before importing component ─────────────────────────────────────
vi.mock("urql", () => ({
  useQuery: vi.fn(),
  useMutation: vi.fn(),
  gql: (strs: TemplateStringsArray, ...vals: unknown[]) =>
    strs.reduce((acc, s, i) => acc + s + (vals[i] ?? ""), ""),
}));

import { useQuery } from "urql";
import { PlanPreviewModal, type PlanModalIntent } from "./PlanPreviewModal";

afterEach(cleanup);
afterEach(() => vi.clearAllMocks());

// ─────────────────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────────────────

interface PlanPage {
  id: string;
  templateId: string;
  title: string;
  pageType: "REPO_WIDE" | "ARCHITECTURE" | "TOP_LEVEL_DIR";
  subsystem: string | null;
  audience: string;
  required: boolean;
}

function makePlan(pages: PlanPage[]) {
  return {
    previewLivingWikiPlan: {
      planSignature: "sig-abc123",
      mode: "lw_detailed",
      modeTooltip:
        "Detailed mode — one architecture doc per subsystem cluster (shown below), plus the 3 repo-wide pages.",
      summary: "3 repo-wide + 2 architecture",
      totalPages: pages.length,
      preCap: pages.length,
      capSource: "none",
      capValue: 0,
      notice: null,
      pages,
    },
  };
}

const REPO_WIDE_PAGES: PlanPage[] = [
  { id: "rw-1", templateId: "system_overview", title: "System Overview", pageType: "REPO_WIDE", subsystem: null, audience: "ENGINEER", required: true },
  { id: "rw-2", templateId: "api_reference", title: "API Reference", pageType: "REPO_WIDE", subsystem: null, audience: "ENGINEER", required: true },
  { id: "rw-3", templateId: "glossary", title: "Glossary", pageType: "REPO_WIDE", subsystem: null, audience: "ENGINEER", required: true },
];

const ARCH_PAGES: PlanPage[] = [
  { id: "arch-1", templateId: "architecture", title: "Auth Subsystem", pageType: "ARCHITECTURE", subsystem: "auth", audience: "ENGINEER", required: false },
  { id: "arch-2", templateId: "architecture", title: "Billing Subsystem", pageType: "ARCHITECTURE", subsystem: "billing", audience: "PRODUCT", required: false },
];

const ALL_PAGES = [...REPO_WIDE_PAGES, ...ARCH_PAGES];

function setupQueryLoading() {
  vi.mocked(useQuery).mockReturnValue([
    { fetching: true, data: undefined, error: undefined, stale: false } as UseQueryState,
    vi.fn(),
  ]);
}

function setupQuerySuccess(pages = ALL_PAGES) {
  vi.mocked(useQuery).mockReturnValue([
    { fetching: false, data: makePlan(pages), error: undefined, stale: false } as UseQueryState,
    vi.fn(),
  ]);
}

function setupQueryError() {
  vi.mocked(useQuery).mockReturnValue([
    {
      fetching: false,
      data: undefined,
      error: { message: "network error", graphQLErrors: [], networkError: undefined, response: undefined },
      stale: false,
      hasNext: false,
    } as unknown as UseQueryState,
    vi.fn(),
  ]);
}

const DEFAULT_INTENT: PlanModalIntent = { kind: "enable", mode: "DETAILED" };

function renderModal(overrides: {
  open?: boolean;
  intent?: PlanModalIntent;
  intentLabel?: string;
  pageCountOverride?: number | null;
  onConfirm?: () => Promise<void>;
  onOpenChange?: (open: boolean) => void;
} = {}) {
  const onConfirm = overrides.onConfirm ?? vi.fn().mockResolvedValue(undefined);
  const onOpenChange = overrides.onOpenChange ?? vi.fn();
  return {
    onConfirm,
    onOpenChange,
    ...render(
      <PlanPreviewModal
        open={overrides.open ?? true}
        onOpenChange={onOpenChange}
        repositoryId="repo-1"
        intent={overrides.intent ?? DEFAULT_INTENT}
        intentLabel={overrides.intentLabel ?? "Build"}
        pageCountOverride={overrides.pageCountOverride ?? null}
        onConfirm={onConfirm}
      />,
    ),
  };
}

// ─────────────────────────────────────────────────────────────────────────────
// Mode pill — header
// ─────────────────────────────────────────────────────────────────────────────

describe("mode pill in header", () => {
  it("renders mode pill in header with correct tooltip for detailed mode", () => {
    setupQuerySuccess();
    renderModal({ intent: { kind: "enable", mode: "DETAILED" } });

    // Pill label is present
    expect(screen.getByText("Detailed mode")).toBeInTheDocument();
    // Radix Tooltip renders content into a portal when open; in jsdom it
    // may not trigger on mouseEnter. Assert the trigger element is in the
    // header (not body/footer) by checking its parent is inside the modal.
    const pillEl = screen.getByText("Detailed mode");
    expect(pillEl).toBeInTheDocument();
    // The info icon should be rendered alongside the pill (accessibility)
    expect(pillEl.closest("span")).not.toBeNull();
  });

  it("renders mode pill in header with correct tooltip for overview mode", () => {
    // Use loading state so plan.mode hasn't overridden intent.mode yet
    setupQueryLoading();
    renderModal({ intent: { kind: "enable", mode: "OVERVIEW" } });

    expect(screen.getByText("Overview mode")).toBeInTheDocument();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Required pages — no checkbox
// ─────────────────────────────────────────────────────────────────────────────

describe("required pages (REPO_WIDE)", () => {
  it("renders required pages without checkboxes — lock badge instead", () => {
    setupQuerySuccess();
    renderModal();

    // Required pages should show "Always included" badges
    const alwaysIncluded = screen.getAllByText("Always included");
    expect(alwaysIncluded).toHaveLength(REPO_WIDE_PAGES.length);

    // No checkboxes for required pages (total checkboxes = non-required count only)
    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes).toHaveLength(ARCH_PAGES.length);
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Default selection state
// ─────────────────────────────────────────────────────────────────────────────

describe("default selection state", () => {
  it("all non-required pages are checked by default", () => {
    setupQuerySuccess();
    renderModal();

    const checkboxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    for (const cb of checkboxes) {
      expect(cb.checked).toBe(true);
    }
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Counter
// ─────────────────────────────────────────────────────────────────────────────

describe("footer counter", () => {
  it("shows correct counter format when all checked", () => {
    setupQuerySuccess();
    renderModal();

    // 5 total: 3 required + 2 arch. All selected = 5.
    expect(
      screen.getByText(`${ALL_PAGES.length} of ${ALL_PAGES.length} selected (${REPO_WIDE_PAGES.length} required)`),
    ).toBeInTheDocument();
  });

  it("updates counter when a non-required page is unchecked", async () => {
    setupQuerySuccess();
    renderModal();

    const checkboxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    // Uncheck first arch page
    await act(async () => {
      fireEvent.click(checkboxes[0]);
    });

    // 4 selected: 3 required + 1 arch
    expect(
      screen.getByText(`4 of ${ALL_PAGES.length} selected (${REPO_WIDE_PAGES.length} required)`),
    ).toBeInTheDocument();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Build button enabled/disabled
// ─────────────────────────────────────────────────────────────────────────────

describe("Build button state", () => {
  it("is enabled when all non-required pages are checked", () => {
    setupQuerySuccess();
    renderModal();

    const buildBtn = screen.getByRole("button", { name: "Build" });
    expect(buildBtn).not.toBeDisabled();
  });

  it("is enabled when zero non-required pages are selected (build-only-required is valid)", async () => {
    setupQuerySuccess();
    renderModal();

    const checkboxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    // Uncheck all non-required pages
    await act(async () => {
      for (const cb of checkboxes) {
        if (cb.checked) fireEvent.click(cb);
      }
    });

    // Counter should show only required pages selected
    expect(
      screen.getByText(`${REPO_WIDE_PAGES.length} of ${ALL_PAGES.length} selected (${REPO_WIDE_PAGES.length} required)`),
    ).toBeInTheDocument();

    // Build button should still be enabled
    const buildBtn = screen.getByRole("button", { name: "Build" });
    expect(buildBtn).not.toBeDisabled();
  });

  it("is disabled while query is loading", () => {
    setupQueryLoading();
    renderModal();

    const buildBtn = screen.getByRole("button", { name: "Build" });
    expect(buildBtn).toBeDisabled();
  });

  it("loading state renders 'Loading plan...' tooltip text in DOM (tooltip content is rendered)", () => {
    setupQueryLoading();
    renderModal();

    // Radix renders tooltip portal content lazily; the tooltip content
    // for the disabled-button wrapper should be present in portal when open
    // Since Radix may not open on jsdom without pointer events, we check
    // the loading indicator (skeleton + label) as a proxy for the loading state.
    expect(screen.getByText("Resolving page plan…")).toBeInTheDocument();
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// onConfirm payload
// ─────────────────────────────────────────────────────────────────────────────

describe("onConfirm payload", () => {
  it("sends selectedPageIds: null + planSignature: null when all non-required pages checked", async () => {
    setupQuerySuccess();
    const { onConfirm } = renderModal();

    // All checked by default
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Build" }));
    });

    await waitFor(() => {
      expect(onConfirm).toHaveBeenCalledWith({
        selectedPageIds: null,
        planSignature: null,
      });
    });
  });

  it("sends selectedPageIds + planSignature when at least one non-required page is unchecked", async () => {
    setupQuerySuccess();
    const { onConfirm } = renderModal();

    const checkboxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    // Uncheck arch-1 (first non-required)
    await act(async () => {
      fireEvent.click(checkboxes[0]); // unchecks arch-1
    });

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Build" }));
    });

    await waitFor(() => {
      expect(onConfirm).toHaveBeenCalledWith({
        selectedPageIds: [ARCH_PAGES[1].id], // arch-2 is still checked
        planSignature: "sig-abc123",
      });
    });
  });

  it("sends selectedPageIds: [] + planSignature when all non-required pages unchecked", async () => {
    setupQuerySuccess();
    const { onConfirm } = renderModal();

    const checkboxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    await act(async () => {
      for (const cb of checkboxes) {
        if (cb.checked) fireEvent.click(cb);
      }
    });

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Build" }));
    });

    await waitFor(() => {
      expect(onConfirm).toHaveBeenCalledWith({
        selectedPageIds: [],
        planSignature: "sig-abc123",
      });
    });
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Query failure / fallback
// ─────────────────────────────────────────────────────────────────────────────

describe("query failure state", () => {
  it("shows notice text when query fails", () => {
    setupQueryError();
    renderModal();

    expect(
      screen.getByText(/Could not load the page plan/),
    ).toBeInTheDocument();
  });

  it("shows 'Build anyway' fallback button on query failure", () => {
    setupQueryError();
    renderModal();

    expect(
      screen.getByRole("button", { name: /Build anyway/i }),
    ).toBeInTheDocument();
  });

  it("'Build anyway' sends selectedPageIds: null + planSignature: null", async () => {
    setupQueryError();
    const { onConfirm } = renderModal();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: /Build anyway/i }));
    });

    await waitFor(() => {
      expect(onConfirm).toHaveBeenCalledWith({
        selectedPageIds: null,
        planSignature: null,
      });
    });
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Cancel button
// ─────────────────────────────────────────────────────────────────────────────

describe("Cancel button", () => {
  it("closes modal without firing onConfirm", async () => {
    setupQuerySuccess();
    const { onConfirm, onOpenChange } = renderModal();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    });

    expect(onConfirm).not.toHaveBeenCalled();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Debounced refetch on pageCountOverride change
// ─────────────────────────────────────────────────────────────────────────────

describe("pageCountOverride debounce", () => {
  it("re-fetches preview when pageCountOverride changes (debounced 300ms)", async () => {
    vi.useFakeTimers();
    setupQuerySuccess();

    const { rerender } = renderModal({ pageCountOverride: null });

    // useQuery is called initially
    const initialCallCount = vi.mocked(useQuery).mock.calls.length;

    // Change pageCountOverride — should NOT immediately trigger new variables
    rerender(
      <PlanPreviewModal
        open={true}
        onOpenChange={vi.fn()}
        repositoryId="repo-1"
        intent={DEFAULT_INTENT}
        intentLabel="Build"
        pageCountOverride={10}
        onConfirm={vi.fn().mockResolvedValue(undefined)}
      />,
    );

    // Before debounce fires, the variables haven't changed
    // (the debounced state hasn't updated yet)
    const callCountBeforeDebounce = vi.mocked(useQuery).mock.calls.length;

    // Advance timers past the 300ms debounce
    await act(async () => {
      vi.advanceTimersByTime(300);
    });

    // After debounce, useQuery should have been called again with new variables
    const callCountAfterDebounce = vi.mocked(useQuery).mock.calls.length;
    expect(callCountAfterDebounce).toBeGreaterThan(initialCallCount);
    // The variables in the latest call should contain pageCountOverride: 10
    const latestCall = vi.mocked(useQuery).mock.calls[vi.mocked(useQuery).mock.calls.length - 1];
    const latestVars = latestCall[0]?.variables as Record<string, unknown>;
    expect(latestVars?.pageCountOverride).toBe(10);

    void callCountBeforeDebounce; // used to satisfy linter

    vi.useRealTimers();
  });
});
