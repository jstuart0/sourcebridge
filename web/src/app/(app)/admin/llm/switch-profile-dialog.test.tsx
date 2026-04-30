// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Tests for SwitchProfileDialog — slice 2 of LLM provider profiles.
 *
 * Coverage:
 *   - Dialog renders ONLY when open.
 *   - Modal text matches UX intake §4.1 verbatim. The exact phrasing is
 *     load-bearing — it answers the user's actual question (what
 *     happens to my running jobs?) and the test asserts each clause.
 *   - Confirm button POSTs to /admin/llm-profiles/{id}/activate.
 *   - Cancel button is a no-op (no fetch).
 *   - Escape key closes when not submitting.
 *   - Backdrop click closes when not submitting.
 *   - Server-side error (e.g. 5xx) surfaces inline.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (path: string, init?: RequestInit) => mockAuthFetch(path, init),
}));

import { SwitchProfileDialog } from "./switch-profile-dialog";

beforeEach(() => {
  mockAuthFetch.mockReset();
});

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
    mockAuthFetch.mockResolvedValue({
      ok: true,
      status: 204,
      text: async () => "",
    });
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

    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalled();
    });
    const [path, init] = mockAuthFetch.mock.calls[0];
    expect(path).toBe(
      `/api/v1/admin/llm-profiles/${encodeURIComponent("ca_llm_profile:ollama-exp")}/activate`,
    );
    expect(init?.method).toBe("POST");
    await waitFor(() => {
      expect(onActivated).toHaveBeenCalled();
      expect(onClose).toHaveBeenCalled();
    });
  });

  it("surfaces server error inline on confirm failure", async () => {
    mockAuthFetch.mockResolvedValue({
      ok: false,
      status: 500,
      text: async () => JSON.stringify({ error: "internal explosion" }),
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

describe("SwitchProfileDialog — cancel paths", () => {
  it("Cancel button calls onClose without fetching", () => {
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
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  it("Escape key calls onClose without fetching", () => {
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
    expect(mockAuthFetch).not.toHaveBeenCalled();
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
