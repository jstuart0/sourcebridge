// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Tests for SwitchProfileDialog — slice 2 + slice 4 polish.
 *
 * Coverage:
 *   - Dialog renders ONLY when open.
 *   - Modal text matches UX intake §4.1 verbatim. The exact phrasing is
 *     load-bearing — it answers the user's actual question (what
 *     happens to my running jobs?) and the test asserts each clause.
 *   - Confirm button POSTs to /admin/llm-profiles/{id}/activate.
 *   - Cancel button does not POST to activate.
 *   - Escape key closes when not submitting.
 *   - Backdrop click closes when not submitting.
 *   - Server-side error (e.g. 5xx) surfaces inline.
 *   - Slice 4: in-flight LLM-job count is fetched on open and
 *     surfaced when N > 0; N=0 hides the line; fetch failure does
 *     not block the modal.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (path: string, init?: RequestInit) => mockAuthFetch(path, init),
}));

import { SwitchProfileDialog } from "./switch-profile-dialog";

// Default mock: the active-job-count endpoint returns 0. Specific
// tests override with mockImplementation when they need different
// behavior (count > 0, fetch error, activate response).
beforeEach(() => {
  mockAuthFetch.mockReset();
  mockAuthFetch.mockImplementation(async (path: string) => {
    if (typeof path === "string" && path.includes("active-job-count")) {
      return new Response(JSON.stringify({ count: 0 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    // Default: 200 OK for any other path. Specific tests override.
    return new Response(null, { status: 200 });
  });
});

// Convenience helper for tests that need to override only the
// active-job-count and otherwise use the default-200 fallback.
function withJobCount(count: number) {
  mockAuthFetch.mockImplementation(async (path: string) => {
    if (typeof path === "string" && path.includes("active-job-count")) {
      return new Response(JSON.stringify({ count }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (typeof path === "string" && path.endsWith("/activate")) {
      return new Response(null, { status: 204 });
    }
    return new Response(null, { status: 200 });
  });
  return mockAuthFetch;
}

afterEach(() => {
  cleanup();
});

describe("SwitchProfileDialog — render gating", () => {
  it("renders nothing when open=false", () => {
    render(
      <SwitchProfileDialog
        open={false}
        fromProfileName="Anthropic prod"
        toProfileId="ca_llm_profile:ollama-exp"
        toProfileName="Ollama experimental"
        onClose={() => {}}
      />,
    );
    expect(screen.queryByTestId("switch-profile-dialog")).toBeNull();
  });

  it("renders when open=true", () => {
    render(
      <SwitchProfileDialog
        open
        fromProfileName="Anthropic prod"
        toProfileId="ca_llm_profile:ollama-exp"
        toProfileName="Ollama experimental"
        onClose={() => {}}
      />,
    );
    expect(screen.getByTestId("switch-profile-dialog")).toBeInTheDocument();
  });
});

describe("SwitchProfileDialog — UX §4.1 verbatim text", () => {
  // The text is the contract. If anything fails here, do NOT change
  // the test to match the code — open a UX intake change first. The
  // language was tested against the in-flight-job concern.
  it("says 'Switching from <FROM> to <TO>'", () => {
    render(
      <SwitchProfileDialog
        open
        fromProfileName="Anthropic prod"
        toProfileId="ca_llm_profile:ollama-exp"
        toProfileName="Ollama experimental"
        onClose={() => {}}
      />,
    );
    const body = screen.getByTestId("switch-profile-body");
    expect(body.textContent).toContain("Switching from");
    expect(body.textContent).toContain("Anthropic prod");
    expect(body.textContent).toContain("to");
    expect(body.textContent).toContain("Ollama experimental");
  });

  it("says 'Jobs already running keep using <FROM>'", () => {
    render(
      <SwitchProfileDialog
        open
        fromProfileName="Anthropic prod"
        toProfileId="ca_llm_profile:ollama-exp"
        toProfileName="Ollama experimental"
        onClose={() => {}}
      />,
    );
    const body = screen.getByTestId("switch-profile-body");
    expect(body.textContent).toMatch(/Jobs already running keep using/i);
    expect(body.textContent).toContain("Anthropic prod");
  });

  it("says 'Jobs started after this point use <TO>'", () => {
    render(
      <SwitchProfileDialog
        open
        fromProfileName="Anthropic prod"
        toProfileId="ca_llm_profile:ollama-exp"
        toProfileName="Ollama experimental"
        onClose={() => {}}
      />,
    );
    const body = screen.getByTestId("switch-profile-body");
    expect(body.textContent).toMatch(/Jobs started after this point use/i);
    expect(body.textContent).toContain("Ollama experimental");
  });

  it("ends with the 'Switch?' question", () => {
    render(
      <SwitchProfileDialog
        open
        fromProfileName="Anthropic prod"
        toProfileId="ca_llm_profile:ollama-exp"
        toProfileName="Ollama experimental"
        onClose={() => {}}
      />,
    );
    const body = screen.getByTestId("switch-profile-body");
    // It's "Switch?" within the same sentence.
    expect(body.textContent).toMatch(/Switch\?/);
  });
});

describe("SwitchProfileDialog — confirm flow", () => {
  it("POSTs to /admin/llm-profiles/{id}/activate on confirm", async () => {
    withJobCount(0);
    const onActivated = vi.fn();
    const onClose = vi.fn();
    render(
      <SwitchProfileDialog
        open
        fromProfileName="Anthropic prod"
        toProfileId="ca_llm_profile:ollama-exp"
        toProfileName="Ollama experimental"
        onClose={onClose}
        onActivated={onActivated}
      />,
    );

    fireEvent.click(screen.getByTestId("switch-profile-confirm"));

    // Find the activate call specifically (the on-open count fetch is
    // also in mock.calls).
    await waitFor(() => {
      const activate = mockAuthFetch.mock.calls.find(
        ([path]) =>
          typeof path === "string" &&
          path.endsWith("/activate") &&
          path.includes(encodeURIComponent("ca_llm_profile:ollama-exp")),
      );
      expect(activate).toBeDefined();
      expect(activate?.[1]?.method).toBe("POST");
    });
    await waitFor(() => {
      expect(onActivated).toHaveBeenCalled();
      expect(onClose).toHaveBeenCalled();
    });
  });

  it("surfaces server error inline on confirm failure", async () => {
    mockAuthFetch.mockImplementation(async (path: string) => {
      if (typeof path === "string" && path.includes("active-job-count")) {
        return new Response(JSON.stringify({ count: 0 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      // Activate fails.
      return new Response(JSON.stringify({ error: "internal explosion" }), {
        status: 500,
        headers: { "Content-Type": "application/json" },
      });
    });
    render(
      <SwitchProfileDialog
        open
        fromProfileName="A"
        toProfileId="ca_llm_profile:b"
        toProfileName="B"
        onClose={() => {}}
      />,
    );

    fireEvent.click(screen.getByTestId("switch-profile-confirm"));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });
    expect(screen.getByRole("alert").textContent).toMatch(/internal explosion/);
  });
});

describe("SwitchProfileDialog — slice 4 in-flight job warning", () => {
  it("renders the in-flight count line when N > 0", async () => {
    withJobCount(3);
    render(
      <SwitchProfileDialog
        open
        fromProfileName="Anthropic prod"
        toProfileId="ca_llm_profile:ollama-exp"
        toProfileName="Ollama experimental"
        onClose={() => {}}
      />,
    );
    const countEl = await screen.findByTestId("switch-profile-job-count");
    expect(countEl.textContent).toMatch(/3 LLM jobs are currently in flight/);
    expect(countEl.textContent).toMatch(/keep using/);
    expect(countEl.textContent).toMatch(/Anthropic prod/);
  });

  it("uses singular phrasing when N === 1", async () => {
    withJobCount(1);
    render(
      <SwitchProfileDialog
        open
        fromProfileName="A"
        toProfileId="ca_llm_profile:b"
        toProfileName="B"
        onClose={() => {}}
      />,
    );
    const countEl = await screen.findByTestId("switch-profile-job-count");
    expect(countEl.textContent).toMatch(/1 LLM job is currently in flight/);
    // "It" not "They" for singular case.
    expect(countEl.textContent).toMatch(/It will keep using/);
  });

  it("does NOT render the in-flight line when N === 0", async () => {
    withJobCount(0);
    render(
      <SwitchProfileDialog
        open
        fromProfileName="A"
        toProfileId="ca_llm_profile:b"
        toProfileName="B"
        onClose={() => {}}
      />,
    );
    // Wait one tick for the on-open fetch to resolve.
    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalled();
    });
    expect(screen.queryByTestId("switch-profile-job-count")).toBeNull();
    // Standard copy still renders.
    const body = screen.getByTestId("switch-profile-body");
    expect(body.textContent).toMatch(/Switching from/);
  });

  it("does NOT block the modal when the count fetch fails", async () => {
    mockAuthFetch.mockImplementation(async (path: string) => {
      if (typeof path === "string" && path.includes("active-job-count")) {
        // Simulate a fetch failure (network error or 503).
        throw new Error("network down");
      }
      return new Response(null, { status: 204 });
    });
    render(
      <SwitchProfileDialog
        open
        fromProfileName="A"
        toProfileId="ca_llm_profile:b"
        toProfileName="B"
        onClose={() => {}}
      />,
    );
    // Modal still renders; standard copy is visible.
    const body = screen.getByTestId("switch-profile-body");
    expect(body.textContent).toMatch(/Switching from/);
    // No count line.
    expect(screen.queryByTestId("switch-profile-job-count")).toBeNull();
  });

  it("does NOT render the in-flight line when the endpoint is 503 (embedded mode)", async () => {
    mockAuthFetch.mockImplementation(async (path: string) => {
      if (typeof path === "string" && path.includes("active-job-count")) {
        return new Response(JSON.stringify({ error: "no job store" }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(null, { status: 204 });
    });
    render(
      <SwitchProfileDialog
        open
        fromProfileName="A"
        toProfileId="ca_llm_profile:b"
        toProfileName="B"
        onClose={() => {}}
      />,
    );
    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalled();
    });
    expect(screen.queryByTestId("switch-profile-job-count")).toBeNull();
  });
});

describe("SwitchProfileDialog — cancel paths", () => {
  it("Cancel button calls onClose without POSTing the activate", () => {
    const onClose = vi.fn();
    render(
      <SwitchProfileDialog
        open
        fromProfileName="A"
        toProfileId="ca_llm_profile:b"
        toProfileName="B"
        onClose={onClose}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /Cancel/i }));
    expect(onClose).toHaveBeenCalled();
    // The on-open count fetch is allowed; assert no /activate POST.
    const activateCalls = mockAuthFetch.mock.calls.filter(
      ([path, init]) => typeof path === "string" && path.endsWith("/activate") && init?.method === "POST",
    );
    expect(activateCalls.length).toBe(0);
  });

  it("Escape key calls onClose without POSTing the activate", () => {
    const onClose = vi.fn();
    render(
      <SwitchProfileDialog
        open
        fromProfileName="A"
        toProfileId="ca_llm_profile:b"
        toProfileName="B"
        onClose={onClose}
      />,
    );
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
    const activateCalls = mockAuthFetch.mock.calls.filter(
      ([path, init]) => typeof path === "string" && path.endsWith("/activate") && init?.method === "POST",
    );
    expect(activateCalls.length).toBe(0);
  });

  it("backdrop click closes; clicking modal body does not", () => {
    const onClose = vi.fn();
    render(
      <SwitchProfileDialog
        open
        fromProfileName="A"
        toProfileId="ca_llm_profile:b"
        toProfileName="B"
        onClose={onClose}
      />,
    );
    const backdrop = screen.getByTestId("switch-profile-dialog");
    // Click on the backdrop (target === currentTarget).
    fireEvent.click(backdrop);
    expect(onClose).toHaveBeenCalled();

    onClose.mockReset();
    fireEvent.click(screen.getByText(/Switch active profile\?/i));
    expect(onClose).not.toHaveBeenCalled();
  });
});
