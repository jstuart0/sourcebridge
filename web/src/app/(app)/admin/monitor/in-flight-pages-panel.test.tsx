// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, cleanup } from "@testing-library/react";

// Mock authFetch so tests run without a real server.
const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (path: string, init?: RequestInit) => mockAuthFetch(path, init),
}));

import { InFlightPagesPanel, formatElapsedInFlight } from "./in-flight-pages-panel";

// ── helpers ──────────────────────────────────────────────────────────────────

function makeInFlightPage(
  id: string,
  elapsedMs: number,
  attempt = 1,
  templateId = "architecture"
) {
  return {
    page_id: id,
    template_id: templateId,
    attempt,
    started_at: new Date(Date.now() - elapsedMs).toISOString(),
    elapsed_ms: elapsedMs,
  };
}

function makeResponse(
  pages: ReturnType<typeof makeInFlightPage>[],
  opts?: { medianMs?: number; medianKnown?: boolean }
) {
  return {
    job_id: "job-test",
    as_of: new Date().toISOString(),
    median_completed_ms: opts?.medianMs ?? 0,
    median_completed_ms_known: opts?.medianKnown ?? false,
    pages,
  };
}

function jsonReply(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    })
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  // Default: never-resolving fetch keeps initial loading state.
  mockAuthFetch.mockReturnValue(new Promise(() => {}));
});

afterEach(() => {
  cleanup();
});

// ── formatElapsedInFlight unit tests ─────────────────────────────────────────

describe("formatElapsedInFlight", () => {
  it("formats sub-minute durations as Xs", () => {
    expect(formatElapsedInFlight(0)).toBe("0s");
    expect(formatElapsedInFlight(5_000)).toBe("5s");
    expect(formatElapsedInFlight(59_500)).toBe("60s");
  });

  it("formats minute-plus durations as Xm Ys", () => {
    expect(formatElapsedInFlight(60_000)).toBe("1m 0s");
    expect(formatElapsedInFlight(90_000)).toBe("1m 30s");
    expect(formatElapsedInFlight(150_000)).toBe("2m 30s");
  });
});

// ── Component render tests ────────────────────────────────────────────────────

describe("InFlightPagesPanel", () => {
  it("renders loading state initially", () => {
    // mockAuthFetch default is a never-resolving promise.
    render(<InFlightPagesPanel jobId="job-test" />);
    expect(screen.getByText(/Loading in-flight pages/i)).toBeInTheDocument();
  });

  it("renders empty state when no pages are in-flight", async () => {
    mockAuthFetch.mockReturnValue(jsonReply(makeResponse([])));
    render(<InFlightPagesPanel jobId="job-test" />);
    await waitFor(() =>
      expect(
        screen.getByText(/No pages currently in-flight/i)
      ).toBeInTheDocument()
    );
  });

  it("renders 1 page row without warn-dot (elapsed < 3× median)", async () => {
    mockAuthFetch.mockReturnValue(
      jsonReply(makeResponse([makeInFlightPage("arch.auth", 5_000)], {
        medianMs: 10_000,
        medianKnown: true,
      }))
    );
    render(<InFlightPagesPanel jobId="job-test" />);
    await waitFor(() =>
      expect(screen.getByText("arch.auth")).toBeInTheDocument()
    );
    expect(screen.getByText("architecture")).toBeInTheDocument();
    // 5s < 3×10s=30s — no warn dot.
    expect(screen.queryByLabelText(/Slow page warning/i)).not.toBeInTheDocument();
  });

  it("renders warn-dot when elapsed > 3× median (median known)", async () => {
    mockAuthFetch.mockReturnValue(
      jsonReply(
        makeResponse([makeInFlightPage("arch.billing", 60_000)], {
          medianMs: 10_000, // 3×10s=30s < 60s → warn
          medianKnown: true,
        })
      )
    );
    render(<InFlightPagesPanel jobId="job-test" />);
    await waitFor(() =>
      expect(screen.getByLabelText(/Slow page warning/i)).toBeInTheDocument()
    );
  });

  it("renders warn-dot using flat 300s threshold when median unknown", async () => {
    mockAuthFetch.mockReturnValue(
      jsonReply(
        makeResponse([makeInFlightPage("arch.overview", 310_000)], {
          medianKnown: false,
        })
      )
    );
    render(<InFlightPagesPanel jobId="job-test" />);
    await waitFor(() =>
      expect(screen.getByLabelText(/Slow page warning/i)).toBeInTheDocument()
    );
  });

  it("does NOT render warn-dot when elapsed < 300s and median unknown", async () => {
    mockAuthFetch.mockReturnValue(
      jsonReply(
        makeResponse([makeInFlightPage("arch.glossary", 120_000)], {
          medianKnown: false,
        })
      )
    );
    render(<InFlightPagesPanel jobId="job-test" />);
    await waitFor(() =>
      expect(screen.getByText("arch.glossary")).toBeInTheDocument()
    );
    expect(screen.queryByLabelText(/Slow page warning/i)).not.toBeInTheDocument();
  });

  it("renders 3 page rows with correct template and attempt columns", async () => {
    const pages = [
      makeInFlightPage("arch.a", 1_000, 1, "architecture"),
      makeInFlightPage("arch.b", 2_000, 2, "architecture"),
      makeInFlightPage("arch.c", 3_000, 1, "api_reference"),
    ];
    mockAuthFetch.mockReturnValue(jsonReply(makeResponse(pages)));
    render(<InFlightPagesPanel jobId="job-test" />);
    await waitFor(() =>
      expect(screen.getByText("arch.a")).toBeInTheDocument()
    );
    expect(screen.getByText("arch.b")).toBeInTheDocument();
    expect(screen.getByText("arch.c")).toBeInTheDocument();
    expect(screen.getByText("api_reference")).toBeInTheDocument();
    // attempt=2 row
    expect(screen.getAllByText("2")).toHaveLength(1);
  });

  it("renders error state on 503", async () => {
    mockAuthFetch.mockReturnValue(
      Promise.resolve(new Response(null, { status: 503 }))
    );
    render(<InFlightPagesPanel jobId="job-test" />);
    await waitFor(() =>
      expect(
        screen.getByText(/in-flight data unavailable/i)
      ).toBeInTheDocument()
    );
  });
});
