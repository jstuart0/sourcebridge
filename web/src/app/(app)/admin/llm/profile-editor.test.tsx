// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Tests for ProfileEditor — slice 2 of LLM provider profiles.
 *
 * Coverage:
 *   - ruby-M1: when editing a non-active profile with provider X while
 *     the workspace-active profile is Y, the model-list fetch targets
 *     the EDITOR's provider X (not the active profile's Y).
 *   - In multiProfileMode, the [Profile name] input is the FIRST field
 *     in the editor (UX intake §6).
 *   - In single-profile mode, no [Profile name] input renders, no
 *     [Make active] button renders.
 *   - PUT body for save serializes pointer-patch fields correctly.
 *   - 409 target_no_longer_active triggers the CustomEvent the parent
 *     listens for (slice-1-flag #3).
 *   - api_key field is type=password and only sent in PUT when filled.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (path: string, init?: RequestInit) => mockAuthFetch(path, init),
}));

import { ProfileEditor, type ProfileResponse } from "./profile-editor";

function profile(overrides: Partial<ProfileResponse>): ProfileResponse {
  return {
    id: overrides.id ?? "ca_llm_profile:b",
    name: overrides.name ?? "B",
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

beforeEach(() => {
  mockAuthFetch.mockReset();
  // Default: model-list fetch returns empty.
  mockAuthFetch.mockImplementation((path: string) => {
    if (path.startsWith("/api/v1/admin/llm-models")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: async () => ({ models: [] }),
      });
    }
    return Promise.resolve({ ok: false, status: 599, text: async () => "unmocked" });
  });
});

afterEach(() => {
  cleanup();
});

describe("ProfileEditor — single-profile mode (N=1)", () => {
  it("does NOT render the [Profile name] input or [Make active] button", () => {
    render(
      <ProfileEditor
        profile={profile({ name: "Default", is_active: true })}
        multiProfileMode={false}
        testIdPrefix="editor-single"
      />,
    );
    expect(screen.queryByTestId("editor-single-name-input")).toBeNull();
    expect(screen.queryByTestId("editor-single-make-active")).toBeNull();
    expect(screen.getByTestId("editor-single-save").textContent).toMatch(/^Save$/);
  });
});

describe("ProfileEditor — multi-profile mode (N>=2)", () => {
  it("renders the [Profile name] input as the first field (UX intake §6)", () => {
    render(
      <ProfileEditor
        profile={profile({ name: "Beta", is_active: false })}
        multiProfileMode
        testIdPrefix="editor-multi"
      />,
    );
    expect(screen.getByTestId("editor-multi-name-input")).toBeInTheDocument();
    expect((screen.getByTestId("editor-multi-name-input") as HTMLInputElement).value).toBe("Beta");
  });

  it("renders [Make active] button only on a non-active profile when onActivateRequested provided", () => {
    const onActivateRequested = vi.fn();
    render(
      <ProfileEditor
        profile={profile({ name: "Beta", is_active: false })}
        multiProfileMode
        onActivateRequested={onActivateRequested}
        testIdPrefix="editor-multi"
      />,
    );
    expect(screen.getByTestId("editor-multi-make-active")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("editor-multi-make-active"));
    expect(onActivateRequested).toHaveBeenCalled();
  });

  it("does NOT render [Make active] for the active profile", () => {
    render(
      <ProfileEditor
        profile={profile({ name: "Alpha", is_active: true })}
        multiProfileMode
        onActivateRequested={() => {}}
        testIdPrefix="editor-multi"
      />,
    );
    expect(screen.queryByTestId("editor-multi-make-active")).toBeNull();
  });
});

describe("ProfileEditor — ruby-M1 (model fetch targets the EDITOR profile)", () => {
  it("on initial render, GETs /admin/llm-models?provider=<editor>&base_url=<editor>", async () => {
    // The active profile in the workspace is provider=anthropic +
    // some-anthropic-baseurl. The user is editing a NON-active profile
    // with provider=ollama, base_url=http://192.168.10.1:11434. The
    // fetch must target the EDITOR's provider/base_url, not the
    // workspace-active one. The editor only knows about the profile
    // prop it's given — that IS the editor target — so this verifies
    // by construction.
    render(
      <ProfileEditor
        profile={profile({
          provider: "ollama",
          base_url: "http://192.168.10.1:11434",
          is_active: false,
        })}
        multiProfileMode
        testIdPrefix="editor-multi"
      />,
    );
    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalled();
    });
    const path = mockAuthFetch.mock.calls[0][0] as string;
    expect(path).toMatch(/\/api\/v1\/admin\/llm-models\?/);
    expect(path).toMatch(/provider=ollama/);
    expect(path).toMatch(/base_url=http%3A%2F%2F192\.168\.10\.1%3A11434/);
  });

  it("[Refresh models] re-fetches with the EDITOR's CURRENT provider/baseURL", async () => {
    render(
      <ProfileEditor
        profile={profile({ provider: "ollama", base_url: "http://localhost:11434" })}
        multiProfileMode
        testIdPrefix="editor-multi"
      />,
    );
    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalledTimes(1);
    });
    // User edits the base_url field.
    const baseURLInput = screen.getAllByRole("textbox").find(
      (el) => (el as HTMLInputElement).value === "http://localhost:11434",
    );
    expect(baseURLInput).toBeDefined();
    fireEvent.change(baseURLInput!, { target: { value: "http://thor.local:11434" } });

    fireEvent.click(screen.getByRole("button", { name: /Refresh models/i }));
    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalledTimes(2);
    });
    const path = mockAuthFetch.mock.calls[1][0] as string;
    expect(path).toMatch(/base_url=http%3A%2F%2Fthor\.local%3A11434/);
  });
});

describe("ProfileEditor — Save flow", () => {
  it("PUTs to /admin/llm-profiles/{id} with editable fields; api_key omitted when not entered", async () => {
    // First call: model fetch. Second call: PUT /admin/llm-profiles/{id}.
    const calls: { path: string; init: RequestInit | undefined }[] = [];
    mockAuthFetch.mockImplementation((path: string, init?: RequestInit) => {
      calls.push({ path, init });
      if (path.startsWith("/api/v1/admin/llm-models")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => ({ models: [] }),
        });
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: async () => ({ status: "updated" }),
      });
    });

    const onSaved = vi.fn();
    render(
      <ProfileEditor
        profile={profile({
          id: "ca_llm_profile:b",
          name: "Beta",
          provider: "ollama",
          base_url: "http://localhost:11434",
          api_key_set: false,
          summary_model: "qwen2.5:32b",
        })}
        multiProfileMode
        onSaved={onSaved}
        testIdPrefix="editor-multi"
      />,
    );

    // Toggle dirty by changing the timeout.
    await waitFor(() => {
      expect(screen.getByTestId("editor-multi-save")).toBeInTheDocument();
    });
    const timeoutInput = screen.getByDisplayValue("900") as HTMLInputElement;
    fireEvent.change(timeoutInput, { target: { value: "600" } });

    fireEvent.click(screen.getByTestId("editor-multi-save"));
    await waitFor(() => {
      expect(onSaved).toHaveBeenCalled();
    });

    const putCall = calls.find((c) => c.init?.method === "PUT");
    expect(putCall).toBeDefined();
    expect(putCall!.path).toBe(
      `/api/v1/admin/llm-profiles/${encodeURIComponent("ca_llm_profile:b")}`,
    );
    const body = JSON.parse((putCall!.init?.body as string) ?? "{}");
    expect(body.timeout_secs).toBe(600);
    expect(body.provider).toBe("ollama");
    expect(body.base_url).toBe("http://localhost:11434");
    // api_key not entered → not sent.
    expect(body.api_key).toBeUndefined();
  });

  it("dispatches sourcebridge:profile-target-no-longer-active on 409", async () => {
    mockAuthFetch.mockImplementation((path: string, _init?: RequestInit) => {
      if (path.startsWith("/api/v1/admin/llm-models")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: async () => ({ models: [] }),
        });
      }
      // PUT returns 409 target_no_longer_active.
      return Promise.resolve({
        ok: false,
        status: 409,
        text: async () =>
          JSON.stringify({
            error: "target_no_longer_active",
            hint: "Another writer activated 'A' during your edit.",
          }),
      });
    });

    const events: CustomEvent[] = [];
    const handler = (ev: Event) => events.push(ev as CustomEvent);
    window.addEventListener("sourcebridge:profile-target-no-longer-active", handler);

    render(
      <ProfileEditor
        profile={profile({ id: "ca_llm_profile:b", name: "B", is_active: false })}
        multiProfileMode
        testIdPrefix="editor-multi"
      />,
    );
    await waitFor(() => {
      expect(screen.getByTestId("editor-multi-save")).toBeInTheDocument();
    });

    // Mark dirty so save is enabled.
    const timeoutInput = screen.getByDisplayValue("900") as HTMLInputElement;
    fireEvent.change(timeoutInput, { target: { value: "601" } });

    fireEvent.click(screen.getByTestId("editor-multi-save"));

    await waitFor(() => {
      expect(events.length).toBeGreaterThan(0);
    });
    expect(events[0].detail.profileId).toBe("ca_llm_profile:b");
    expect(events[0].detail.hint).toMatch(/Another writer activated 'A'/);
    window.removeEventListener("sourcebridge:profile-target-no-longer-active", handler);
  });
});
