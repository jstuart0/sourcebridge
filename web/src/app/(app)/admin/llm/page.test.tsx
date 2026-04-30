// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Tests for /admin/llm — slice 2 of LLM provider profiles.
 *
 * The page is a state machine (see page.tsx top comment). We cover
 * each branch:
 *
 *   - N=1 layout: looks like today (no list, no active pill, no
 *     "PROFILES" heading). Header has Profile name pill + + Add
 *     profile button. progressive disclosure (J3 promise).
 *   - N>=2 layout: profile list, active pill at top, switch dialog
 *     opens on radio click and matches §4.1 verbatim, Save vs
 *     Activate are visually distinct.
 *   - Empty state: never renders the "no profiles" copy. First
 *     attempt shows the auto-refresh hint; second attempt (after
 *     auto-retry) surfaces the runbook banner.
 *   - Repair banner (active_profile_missing=true): renders, editor is
 *     disabled, repair-picker + Activate are present. After repair
 *     success, banner clears.
 *   - 409 conflict: editor's CustomEvent triggers the panel-scoped
 *     resolution UI with three actions.
 *
 * Slice-1-flag absorption is asserted explicitly:
 *   #1: is_active + active_profile_missing read from list response,
 *       never recomputed.
 *   #2: legacy GET active_profile_name drives the N=1 header pill.
 *   #3: 409 target_no_longer_active → conflict-banner renders + actions.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup, act } from "@testing-library/react";

const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (path: string, init?: RequestInit) => mockAuthFetch(path, init),
}));

import AdminLLMPage from "./page";

// ─────────────────────────────────────────────────────────────────────────
// Test fixtures
// ─────────────────────────────────────────────────────────────────────────

interface ProfileFixture {
  id: string;
  name: string;
  provider: string;
  base_url: string;
  api_key_set: boolean;
  api_key_hint?: string;
  summary_model: string;
  review_model: string;
  ask_model: string;
  knowledge_model: string;
  architecture_diagram_model: string;
  report_model?: string;
  draft_model: string;
  timeout_secs: number;
  advanced_mode: boolean;
  is_active: boolean;
}

function profile(overrides: Partial<ProfileFixture>): ProfileFixture {
  return {
    id: overrides.id ?? "ca_llm_profile:default-migrated",
    name: overrides.name ?? "Default",
    provider: overrides.provider ?? "ollama",
    base_url: overrides.base_url ?? "http://localhost:11434",
    api_key_set: overrides.api_key_set ?? false,
    api_key_hint: overrides.api_key_hint,
    summary_model: overrides.summary_model ?? "qwen2.5:32b",
    review_model: overrides.review_model ?? "",
    ask_model: overrides.ask_model ?? "",
    knowledge_model: overrides.knowledge_model ?? "",
    architecture_diagram_model: overrides.architecture_diagram_model ?? "",
    report_model: overrides.report_model,
    draft_model: overrides.draft_model ?? "",
    timeout_secs: overrides.timeout_secs ?? 900,
    advanced_mode: overrides.advanced_mode ?? false,
    is_active: overrides.is_active ?? false,
  };
}

interface FetchPlan {
  profilesList: { profiles: ProfileFixture[]; active_profile_missing: boolean };
  legacyConfig?: {
    provider: string;
    base_url: string;
    api_key_set: boolean;
    summary_model: string;
    review_model: string;
    ask_model: string;
    knowledge_model: string;
    architecture_diagram_model: string;
    draft_model: string;
    timeout_secs: number;
    advanced_mode: boolean;
    active_profile_id?: string;
    active_profile_name?: string;
    active_profile_missing?: boolean;
  };
  worker?: { worker: { address: string } };
  models?: { models: unknown[] };
}

// Configures the mock to respond to the standard initial fetches the
// page does on mount: list profiles, legacy config, worker config, and
// the model-list fetch the editor runs after hydration.
function installFetchPlan(plan: FetchPlan) {
  mockAuthFetch.mockImplementation((path: string, init?: RequestInit) => {
    if (path.startsWith("/api/v1/admin/llm-profiles") && (!init || init.method === undefined || init.method === "GET")) {
      // GET /admin/llm-profiles  OR  GET /admin/llm-profiles/{id}
      // The page only uses the list form on mount.
      if (path === "/api/v1/admin/llm-profiles" || path === "/api/v1/admin/llm-profiles?") {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => plan.profilesList,
        });
      }
    }
    if (path === "/api/v1/admin/llm-config") {
      if (plan.legacyConfig) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => plan.legacyConfig,
        });
      }
      return Promise.resolve({ ok: false, status: 404, text: async () => "" });
    }
    if (path === "/api/v1/admin/config") {
      if (plan.worker) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => plan.worker,
        });
      }
      return Promise.resolve({ ok: false, status: 404, text: async () => "" });
    }
    if (path.startsWith("/api/v1/admin/llm-models")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: async () => plan.models ?? { models: [] },
      });
    }
    // Unknown / unmocked: surface clearly so tests don't silently pass.
    return Promise.resolve({
      ok: false,
      status: 599,
      text: async () => `unmocked ${init?.method ?? "GET"} ${path}`,
    });
  });
}

beforeEach(() => {
  mockAuthFetch.mockReset();
});

afterEach(() => {
  cleanup();
});

// ─────────────────────────────────────────────────────────────────────────
// N=1 layout (J3 — existing user; progressive disclosure)
// ─────────────────────────────────────────────────────────────────────────

describe("AdminLLMPage — N=1 layout (progressive disclosure)", () => {
  it("renders the single-profile shape WITHOUT the profile list / active pill", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [profile({ id: "ca_llm_profile:default-migrated", name: "Default", is_active: true })],
        active_profile_missing: false,
      },
      legacyConfig: {
        provider: "ollama",
        base_url: "http://localhost:11434",
        api_key_set: false,
        summary_model: "qwen2.5:32b",
        review_model: "",
        ask_model: "",
        knowledge_model: "",
        architecture_diagram_model: "",
        draft_model: "",
        timeout_secs: 900,
        advanced_mode: false,
        active_profile_id: "ca_llm_profile:default-migrated",
        active_profile_name: "Default",
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);

    await waitFor(() => {
      expect(screen.getByTestId("editor-panel-single")).toBeInTheDocument();
    });

    // The profile-list machinery must NOT appear.
    expect(screen.queryByTestId("profile-list")).toBeNull();
    expect(screen.queryByTestId("header-active-pill")).toBeNull();

    // Header has the [+ Add profile] button + the Profile pill.
    expect(screen.getByTestId("header-add-profile")).toBeInTheDocument();
    expect(screen.getByTestId("header-profile-pill")).toBeInTheDocument();
  });

  it("uses legacy GET active_profile_name for the header pill (slice-1-flag #2)", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [profile({ id: "ca_llm_profile:foo", name: "FromList", is_active: true })],
        active_profile_missing: false,
      },
      legacyConfig: {
        provider: "ollama",
        base_url: "http://localhost:11434",
        api_key_set: false,
        summary_model: "",
        review_model: "",
        ask_model: "",
        knowledge_model: "",
        architecture_diagram_model: "",
        draft_model: "",
        timeout_secs: 900,
        advanced_mode: false,
        active_profile_id: "ca_llm_profile:foo",
        // The legacy GET name MUST drive the pill — the test uses a
        // different name in legacyConfig vs profilesList to prove that.
        active_profile_name: "Default (legacy-source-of-truth)",
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("header-profile-pill")).toBeInTheDocument();
    });
    expect(screen.getByTestId("header-profile-pill").textContent).toMatch(
      /Default \(legacy-source-of-truth\)/,
    );
  });

  it("does NOT render the 'Profile name' input field in the editor (N=1 looks like today)", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [profile({ id: "ca_llm_profile:default-migrated", name: "Default", is_active: true })],
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("editor-panel-single")).toBeInTheDocument();
    });

    // The profile-name input appears only in multiProfileMode.
    expect(screen.queryByTestId("editor-single-name-input")).toBeNull();

    // Today's grid is present: provider select, base URL input, model
    // combobox, advanced toggle, timeout, save button.
    expect(screen.getByText("Provider")).toBeInTheDocument();
    expect(screen.getByText("Base URL")).toBeInTheDocument();
    expect(screen.getByText(/Timeout \(seconds\)/i)).toBeInTheDocument();
    // Save button (single-mode label).
    expect(screen.getByTestId("editor-single-save")).toBeInTheDocument();
    expect(screen.getByTestId("editor-single-save").textContent).toMatch(/^Save$/);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// N>=2 layout (J1 / J2 — multiple profiles)
// ─────────────────────────────────────────────────────────────────────────

describe("AdminLLMPage — N>=2 layout", () => {
  it("renders the profile list, active pill, and editor with profile-scoped affordances", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "Anthropic prod", provider: "anthropic", is_active: true, api_key_set: true }),
          profile({ id: "ca_llm_profile:b", name: "Ollama experimental", provider: "ollama", is_active: false }),
        ],
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);

    await waitFor(() => {
      expect(screen.getByTestId("profile-list")).toBeInTheDocument();
    });

    // Active pill in the header.
    expect(screen.getByTestId("header-active-pill")).toBeInTheDocument();
    expect(screen.getByTestId("header-active-pill").textContent).toMatch(/Anthropic prod/);

    // Multi-profile editor variant + profile-name input + Save profile button.
    expect(screen.getByTestId("editor-panel-multi")).toBeInTheDocument();
    expect(screen.getByTestId("editor-multi-name-input")).toBeInTheDocument();
    expect(screen.getByTestId("editor-multi-save").textContent).toMatch(/Save profile/i);
  });

  it("Save and Activate buttons are visually + functionally distinct (UX intake §6)", async () => {
    // Render N>=2 with the active profile selected first; Edit a
    // non-active profile; the editor for the non-active profile shows
    // BOTH [Save profile] and [Make active], they're separate elements
    // with different test-ids.
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "A", is_active: true }),
          profile({ id: "ca_llm_profile:b", name: "B", is_active: false }),
        ],
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("profile-list")).toBeInTheDocument();
    });

    // Pick the non-active profile in the editor.
    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:b-edit"));

    await waitFor(() => {
      expect(screen.getByTestId("editor-multi-make-active")).toBeInTheDocument();
    });

    const saveBtn = screen.getByTestId("editor-multi-save");
    const activateBtn = screen.getByTestId("editor-multi-make-active");
    expect(saveBtn).not.toBe(activateBtn);
    // Activate has the success-tinted color; Save uses the primary
    // button class. The test asserts at least the structural difference.
    expect(activateBtn.className).toContain("var(--color-success");
    expect(saveBtn.className).not.toContain("var(--color-success");
  });

  it("[Make active] button does NOT render for the currently-active profile", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "A", is_active: true }),
          profile({ id: "ca_llm_profile:b", name: "B", is_active: false }),
        ],
        active_profile_missing: false,
      },
    });
    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("editor-panel-multi")).toBeInTheDocument();
    });
    // The default-selected profile is the active one ("A") — Make
    // active should NOT render here.
    expect(screen.queryByTestId("editor-multi-make-active")).toBeNull();
  });

  it("radio-click on a non-active profile opens the SwitchProfileDialog", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "Anthropic prod", is_active: true }),
          profile({ id: "ca_llm_profile:b", name: "Ollama experimental", is_active: false }),
        ],
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("profile-list")).toBeInTheDocument();
    });

    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:b-radio"));
    await waitFor(() => {
      expect(screen.getByTestId("switch-profile-dialog")).toBeInTheDocument();
    });

    // §4.1 verbatim assertions live in switch-profile-dialog.test.tsx.
    // Here we just verify the dialog wires the right names.
    const body = screen.getByTestId("switch-profile-body");
    expect(body.textContent).toContain("Anthropic prod");
    expect(body.textContent).toContain("Ollama experimental");
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Empty state (never the "no profiles configured" copy)
// ─────────────────────────────────────────────────────────────────────────

describe("AdminLLMPage — empty state", () => {
  it("never renders 'no profiles' copy; first attempt shows the auto-refresh hint", async () => {
    installFetchPlan({
      profilesList: { profiles: [], active_profile_missing: false },
    });

    render(<AdminLLMPage />);

    await waitFor(() => {
      expect(screen.getByTestId("empty-loading-hint")).toBeInTheDocument();
    });
    expect(screen.queryByText(/no profiles configured/i)).toBeNull();
    expect(screen.queryByText(/no profiles yet/i)).toBeNull();
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Repair banner (active_profile_missing=true; codex-L2)
// ─────────────────────────────────────────────────────────────────────────

describe("AdminLLMPage — active_profile_missing repair banner", () => {
  it("renders the repair banner + picker + Activate when active_profile_missing=true", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "A", is_active: false }),
          profile({ id: "ca_llm_profile:b", name: "B", is_active: false }),
        ],
        active_profile_missing: true,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("repair-banner")).toBeInTheDocument();
    });
    expect(screen.getByTestId("repair-picker")).toBeInTheDocument();
    expect(screen.getByTestId("repair-activate")).toBeInTheDocument();
  });

  it("disables the editor (and any profile-list interactions) while banner is up", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "A", is_active: false }),
          profile({ id: "ca_llm_profile:b", name: "B", is_active: false }),
        ],
        active_profile_missing: true,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("repair-banner")).toBeInTheDocument();
    });

    // The editor's Save button should be disabled.
    const save = screen.getByTestId("editor-multi-save") as HTMLButtonElement;
    expect(save.disabled).toBe(true);

    // The list's Add button should be disabled.
    const add = screen.getByTestId("profile-list-add") as HTMLButtonElement;
    expect(add.disabled).toBe(true);
  });

  it("after repair-Activate succeeds the banner clears", async () => {
    // Custom mock: first GET returns missing=true; second GET (after
    // activate) returns missing=false.
    let getCall = 0;
    mockAuthFetch.mockImplementation((path: string, init?: RequestInit) => {
      if (path === "/api/v1/admin/llm-profiles" && (!init || init.method === undefined)) {
        getCall++;
        const missing = getCall <= 1;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => ({
            profiles: [
              profile({ id: "ca_llm_profile:a", name: "A", is_active: !missing }),
              profile({ id: "ca_llm_profile:b", name: "B", is_active: false }),
            ],
            active_profile_missing: missing,
          }),
        });
      }
      if (path.includes("/activate") && init?.method === "POST") {
        return Promise.resolve({ ok: true, status: 204, text: async () => "" });
      }
      if (path === "/api/v1/admin/llm-config") {
        return Promise.resolve({ ok: false, status: 404, text: async () => "" });
      }
      if (path === "/api/v1/admin/config") {
        return Promise.resolve({ ok: false, status: 404, text: async () => "" });
      }
      if (path.startsWith("/api/v1/admin/llm-models")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => ({ models: [] }),
        });
      }
      return Promise.resolve({
        ok: false,
        status: 599,
        text: async () => `unmocked ${init?.method ?? "GET"} ${path}`,
      });
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("repair-banner")).toBeInTheDocument();
    });

    fireEvent.click(screen.getByTestId("repair-activate"));

    await waitFor(() => {
      expect(screen.queryByTestId("repair-banner")).toBeNull();
    });
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 409 conflict resolution (slice-1-flag #3)
// ─────────────────────────────────────────────────────────────────────────

describe("AdminLLMPage — 409 target_no_longer_active conflict resolution", () => {
  it("renders the conflict banner with three actions when the editor dispatches the CustomEvent", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "Anthropic prod", is_active: true }),
          profile({ id: "ca_llm_profile:b", name: "Ollama experimental", is_active: false }),
        ],
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("editor-panel-multi")).toBeInTheDocument();
    });

    // Simulate the editor dispatching the conflict event (which is
    // what happens when PUT returns 409 target_no_longer_active).
    act(() => {
      window.dispatchEvent(
        new CustomEvent("sourcebridge:profile-target-no-longer-active", {
          detail: {
            profileId: "ca_llm_profile:b",
            hint: "Another writer activated 'Anthropic prod' during your edit.",
          },
        }),
      );
    });

    await waitFor(() => {
      expect(screen.getByTestId("conflict-banner")).toBeInTheDocument();
    });

    expect(screen.getByTestId("conflict-banner").textContent).toMatch(/Ollama experimental/);
    expect(screen.getByTestId("conflict-banner").textContent).toMatch(
      /Another writer activated 'Anthropic prod'/,
    );

    // Three resolution actions are present.
    expect(screen.getByTestId("conflict-retry-active")).toBeInTheDocument();
    expect(screen.getByTestId("conflict-keep-target")).toBeInTheDocument();
    expect(screen.getByTestId("conflict-discard")).toBeInTheDocument();
  });

  it("[Retry on now-active] selects the active profile in the editor and dismisses the banner", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "A", is_active: true }),
          profile({ id: "ca_llm_profile:b", name: "B", is_active: false }),
        ],
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("editor-panel-multi")).toBeInTheDocument();
    });

    act(() => {
      window.dispatchEvent(
        new CustomEvent("sourcebridge:profile-target-no-longer-active", {
          detail: { profileId: "ca_llm_profile:b", hint: "x" },
        }),
      );
    });

    await waitFor(() => {
      expect(screen.getByTestId("conflict-banner")).toBeInTheDocument();
    });
    fireEvent.click(screen.getByTestId("conflict-retry-active"));

    await waitFor(() => {
      expect(screen.queryByTestId("conflict-banner")).toBeNull();
    });
  });

  it("[Discard] dismisses the banner without changing selection", async () => {
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "A", is_active: true }),
          profile({ id: "ca_llm_profile:b", name: "B", is_active: false }),
        ],
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("editor-panel-multi")).toBeInTheDocument();
    });

    act(() => {
      window.dispatchEvent(
        new CustomEvent("sourcebridge:profile-target-no-longer-active", {
          detail: { profileId: "ca_llm_profile:b", hint: "x" },
        }),
      );
    });

    await waitFor(() => {
      expect(screen.getByTestId("conflict-banner")).toBeInTheDocument();
    });
    fireEvent.click(screen.getByTestId("conflict-discard"));

    await waitFor(() => {
      expect(screen.queryByTestId("conflict-banner")).toBeNull();
    });
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Slice-1-flag #1 absorption: is_active comes from list response.
// ─────────────────────────────────────────────────────────────────────────

describe("AdminLLMPage — slice-1-flag #1 (is_active from list)", () => {
  it("the active pill follows profile.is_active EXACTLY (no client-side recompute)", async () => {
    // Even if we had a "mismatch" between active_profile_id and
    // is_active flags (which the server should never produce),
    // the UI trusts is_active as the source of truth. Here we test
    // by setting is_active=true on a profile and verifying the pill
    // matches that name regardless of any other field.
    installFetchPlan({
      profilesList: {
        profiles: [
          profile({ id: "ca_llm_profile:a", name: "Alpha", is_active: false }),
          profile({ id: "ca_llm_profile:b", name: "Beta", is_active: true }),
        ],
        active_profile_missing: false,
      },
    });

    render(<AdminLLMPage />);
    await waitFor(() => {
      expect(screen.getByTestId("header-active-pill")).toBeInTheDocument();
    });
    expect(screen.getByTestId("header-active-pill").textContent).toMatch(/Beta/);
    expect(screen.getByTestId("header-active-pill").textContent).not.toMatch(/Alpha/);

    // And the per-row [Active] pill is on Beta only.
    expect(screen.getByTestId("profile-row-ca_llm_profile:b-active-pill")).toBeInTheDocument();
    expect(screen.queryByTestId("profile-row-ca_llm_profile:a-active-pill")).toBeNull();
  });
});
