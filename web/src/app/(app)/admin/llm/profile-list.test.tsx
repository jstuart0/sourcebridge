// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Tests for ProfileList — slice 2 of LLM provider profiles.
 *
 * Coverage:
 *   - Renders one row per profile, in order.
 *   - Active row has the [Active] pill (slice-1-flag #1: comes from
 *     profile.is_active; we never recompute client-side).
 *   - Active row hides the [Delete] button (D5) and shows the
 *     "switch first to delete" hint.
 *   - Non-active row's radio click fires onActivateRequested with the
 *     row's id (the parent then opens the SwitchProfileDialog).
 *   - Active row's radio click is a no-op.
 *   - [Edit] click selects a profile for the editor.
 *   - [Duplicate] POSTs a new profile and calls onDuplicated.
 *   - [Delete] confirm flow DELETEs and calls onDeleted; row error
 *     bubbles up inline.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (path: string, init?: RequestInit) => mockAuthFetch(path, init),
}));

import { ProfileList } from "./profile-list";
import type { ProfileResponse } from "./profile-editor";

function makeProfile(overrides: Partial<ProfileResponse>): ProfileResponse {
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
    created_at: overrides.created_at,
    updated_at: overrides.updated_at,
  };
}

beforeEach(() => {
  mockAuthFetch.mockReset();
});

afterEach(() => {
  cleanup();
});

describe("ProfileList — rendering", () => {
  it("renders one row per profile in input order", () => {
    const profiles = [
      makeProfile({ id: "ca_llm_profile:a", name: "Alpha" }),
      makeProfile({ id: "ca_llm_profile:b", name: "Beta" }),
      makeProfile({ id: "ca_llm_profile:c", name: "Gamma" }),
    ];
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={() => {}}
        onAddProfile={() => {}}
      />,
    );
    expect(screen.getByText("Alpha")).toBeInTheDocument();
    expect(screen.getByText("Beta")).toBeInTheDocument();
    expect(screen.getByText("Gamma")).toBeInTheDocument();
  });

  it("active row shows the [Active] pill (from profile.is_active)", () => {
    // slice-1-flag #1: is_active comes from the LIST response, never
    // recomputed client-side. ProfileList just trusts the prop.
    const profiles = [
      makeProfile({ id: "ca_llm_profile:a", name: "Alpha", is_active: false }),
      makeProfile({ id: "ca_llm_profile:b", name: "Beta", is_active: true }),
    ];
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={() => {}}
        onAddProfile={() => {}}
      />,
    );
    expect(screen.getByTestId("profile-row-ca_llm_profile:b-active-pill")).toBeInTheDocument();
    expect(screen.queryByTestId("profile-row-ca_llm_profile:a-active-pill")).toBeNull();
  });

  it("active row hides the [Delete] button and shows the hint copy (D5)", () => {
    const profiles = [
      makeProfile({ id: "ca_llm_profile:a", name: "Alpha", is_active: false }),
      makeProfile({ id: "ca_llm_profile:b", name: "Beta", is_active: true }),
    ];
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={() => {}}
        onAddProfile={() => {}}
      />,
    );
    expect(screen.queryByTestId("profile-row-ca_llm_profile:b-delete")).toBeNull();
    expect(screen.getByTestId("profile-row-ca_llm_profile:a-delete")).toBeInTheDocument();
    expect(screen.getByText(/active — switch first to delete/i)).toBeInTheDocument();
  });
});

describe("ProfileList — interactions", () => {
  it("radio click on a non-active profile fires onActivateRequested with that id", () => {
    const profiles = [
      makeProfile({ id: "ca_llm_profile:a", name: "Alpha", is_active: true }),
      makeProfile({ id: "ca_llm_profile:b", name: "Beta", is_active: false }),
    ];
    const onActivateRequested = vi.fn();
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={onActivateRequested}
        onAddProfile={() => {}}
      />,
    );
    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:b-radio"));
    expect(onActivateRequested).toHaveBeenCalledWith("ca_llm_profile:b");
  });

  it("radio on the active row does NOT fire onActivateRequested (no-op)", () => {
    const profiles = [
      makeProfile({ id: "ca_llm_profile:a", name: "Alpha", is_active: true }),
    ];
    const onActivateRequested = vi.fn();
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={onActivateRequested}
        onAddProfile={() => {}}
      />,
    );
    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:a-radio"));
    expect(onActivateRequested).not.toHaveBeenCalled();
  });

  it("[Edit] click fires onSelectForEdit with the row's id", () => {
    const profiles = [makeProfile({ id: "ca_llm_profile:a", name: "Alpha" })];
    const onSelectForEdit = vi.fn();
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={onSelectForEdit}
        onActivateRequested={() => {}}
        onAddProfile={() => {}}
      />,
    );
    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:a-edit"));
    expect(onSelectForEdit).toHaveBeenCalledWith("ca_llm_profile:a");
  });

  it("[+ Add profile] header button fires onAddProfile", () => {
    const onAddProfile = vi.fn();
    render(
      <ProfileList
        profiles={[makeProfile({ id: "ca_llm_profile:a", name: "Alpha" })]}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={() => {}}
        onAddProfile={onAddProfile}
      />,
    );
    fireEvent.click(screen.getByTestId("profile-list-add"));
    expect(onAddProfile).toHaveBeenCalled();
  });
});

describe("ProfileList — duplicate flow", () => {
  it("[Duplicate] POSTs a new profile (sans api_key) and calls onDuplicated", async () => {
    mockAuthFetch.mockResolvedValue({
      ok: true,
      status: 201,
      json: async () => ({ id: "ca_llm_profile:alpha-copy" }),
    });
    const profiles = [
      makeProfile({
        id: "ca_llm_profile:a",
        name: "Alpha",
        provider: "anthropic",
        api_key_set: true,
        summary_model: "claude-sonnet-4-20250514",
      }),
    ];
    const onDuplicated = vi.fn();
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={() => {}}
        onAddProfile={() => {}}
        onDuplicated={onDuplicated}
      />,
    );

    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:a-duplicate"));

    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalled();
    });
    const [path, init] = mockAuthFetch.mock.calls[0];
    expect(path).toBe("/api/v1/admin/llm-profiles");
    expect(init?.method).toBe("POST");
    const body = JSON.parse((init?.body as string) ?? "{}");
    expect(body.name).toBe("Alpha (copy)");
    expect(body.provider).toBe("anthropic");
    expect(body.summary_model).toBe("claude-sonnet-4-20250514");
    // api_key intentionally not copied
    expect(body.api_key).toBeUndefined();
    await waitFor(() => {
      expect(onDuplicated).toHaveBeenCalledWith("ca_llm_profile:alpha-copy");
    });
  });
});

describe("ProfileList — delete flow", () => {
  it("[Delete] shows confirm prompt; confirm DELETEs and calls onDeleted", async () => {
    mockAuthFetch.mockResolvedValue({
      ok: true,
      status: 204,
      text: async () => "",
    });
    const profiles = [
      makeProfile({ id: "ca_llm_profile:a", name: "Alpha", is_active: true }),
      makeProfile({ id: "ca_llm_profile:b", name: "Beta", is_active: false }),
    ];
    const onDeleted = vi.fn();
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={() => {}}
        onAddProfile={() => {}}
        onDeleted={onDeleted}
      />,
    );

    // Click [Delete] on the non-active row -> shows confirm prompt.
    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:b-delete"));
    expect(screen.getByText(/Delete "Beta"\?/i)).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:b-delete-confirm"));
    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalled();
    });
    const [path, init] = mockAuthFetch.mock.calls[0];
    expect(path).toBe(
      `/api/v1/admin/llm-profiles/${encodeURIComponent("ca_llm_profile:b")}`,
    );
    expect(init?.method).toBe("DELETE");
    await waitFor(() => {
      expect(onDeleted).toHaveBeenCalled();
    });
  });

  it("delete server error surfaces inline (panel-scoped, not toast)", async () => {
    mockAuthFetch.mockResolvedValue({
      ok: false,
      status: 500,
      text: async () => JSON.stringify({ error: "store unreachable" }),
    });
    const profiles = [
      makeProfile({ id: "ca_llm_profile:a", name: "Alpha", is_active: true }),
      makeProfile({ id: "ca_llm_profile:b", name: "Beta", is_active: false }),
    ];
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        onSelectForEdit={() => {}}
        onActivateRequested={() => {}}
        onAddProfile={() => {}}
      />,
    );

    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:b-delete"));
    fireEvent.click(screen.getByTestId("profile-row-ca_llm_profile:b-delete-confirm"));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });
    expect(screen.getByRole("alert").textContent).toMatch(/store unreachable/);
  });
});

describe("ProfileList — disabled state", () => {
  it("disables radios, edit, duplicate, delete buttons + add when disabled=true", () => {
    const profiles = [
      makeProfile({ id: "ca_llm_profile:a", name: "Alpha", is_active: true }),
      makeProfile({ id: "ca_llm_profile:b", name: "Beta", is_active: false }),
    ];
    render(
      <ProfileList
        profiles={profiles}
        selectedProfileId=""
        disabled
        onSelectForEdit={() => {}}
        onActivateRequested={() => {}}
        onAddProfile={() => {}}
      />,
    );
    expect((screen.getByTestId("profile-row-ca_llm_profile:b-radio") as HTMLInputElement).disabled).toBe(
      true,
    );
    expect((screen.getByTestId("profile-row-ca_llm_profile:b-edit") as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(
      (screen.getByTestId("profile-row-ca_llm_profile:b-duplicate") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (screen.getByTestId("profile-row-ca_llm_profile:b-delete") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect((screen.getByTestId("profile-list-add") as HTMLButtonElement).disabled).toBe(true);
  });
});
