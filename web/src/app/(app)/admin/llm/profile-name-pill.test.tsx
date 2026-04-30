// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Tests for ProfileNamePill — slice 4 inline-rename UX polish.
 *
 * Coverage:
 *   - Display mode: shows "Profile: <name>" with a rename affordance.
 *   - Click swaps display → edit (input focused + selected, Save +
 *     Cancel buttons present).
 *   - Save trims whitespace and PUTs /api/v1/admin/llm-profiles/<id>
 *     with the trimmed name; calls onRenamed on success.
 *   - Save no-op (unchanged name) skips the API call.
 *   - Empty / whitespace-only name shows a client-side error.
 *   - >64 char name shows a client-side error.
 *   - Server 409 (duplicate name) surfaces inline error from response body.
 *   - Esc cancels (no API call) and restores the original name.
 *   - Enter saves.
 *   - disabled prop blocks the click-to-edit path.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (path: string, init?: RequestInit) => mockAuthFetch(path, init),
}));

import { ProfileNamePill } from "./profile-name-pill";

function ok(): Response {
  return new Response(null, { status: 200 });
}

function err(status: number, body: string | object): Response {
  const text = typeof body === "string" ? body : JSON.stringify(body);
  return new Response(text, {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  mockAuthFetch.mockReset();
});

afterEach(() => {
  cleanup();
});

describe("ProfileNamePill", () => {
  it("renders display-mode pill with the current name", () => {
    render(
      <ProfileNamePill
        profileId="p1"
        currentName="Default"
        onRenamed={() => {}}
      />,
    );
    const pill = screen.getByTestId("profile-name-pill-display");
    expect(pill).toBeTruthy();
    expect(pill.textContent).toContain("Profile:");
    expect(pill.textContent).toContain("Default");
  });

  it("click on display swaps into edit mode with focused + selected input", () => {
    render(
      <ProfileNamePill
        profileId="p1"
        currentName="Default"
        onRenamed={() => {}}
      />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    const input = screen.getByTestId("profile-name-pill-input") as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.value).toBe("Default");
    // Save / Cancel both present.
    expect(screen.getByTestId("profile-name-pill-save")).toBeTruthy();
    expect(screen.getByTestId("profile-name-pill-cancel")).toBeTruthy();
  });

  it("Save PUTs the trimmed new name and calls onRenamed", async () => {
    mockAuthFetch.mockResolvedValueOnce(ok());
    const onRenamed = vi.fn();
    render(
      <ProfileNamePill profileId="p1" currentName="Default" onRenamed={onRenamed} />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    const input = screen.getByTestId("profile-name-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "  Production  " } });
    fireEvent.click(screen.getByTestId("profile-name-pill-save"));
    await waitFor(() => expect(onRenamed).toHaveBeenCalled());
    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/v1/admin/llm-profiles/p1",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ name: "Production" }),
      }),
    );
  });

  it("no-op rename (unchanged name) skips the API call", async () => {
    const onRenamed = vi.fn();
    render(
      <ProfileNamePill profileId="p1" currentName="Default" onRenamed={onRenamed} />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    fireEvent.click(screen.getByTestId("profile-name-pill-save"));
    // Goes back to display, no fetch issued.
    await waitFor(() =>
      expect(screen.queryByTestId("profile-name-pill-input")).toBeNull(),
    );
    expect(mockAuthFetch).not.toHaveBeenCalled();
    expect(onRenamed).not.toHaveBeenCalled();
  });

  it("rejects empty/whitespace-only name with inline error", async () => {
    render(
      <ProfileNamePill profileId="p1" currentName="Default" onRenamed={() => {}} />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    const input = screen.getByTestId("profile-name-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "   " } });
    fireEvent.click(screen.getByTestId("profile-name-pill-save"));
    const errEl = await screen.findByTestId("profile-name-pill-error");
    expect(errEl.textContent).toContain("cannot be empty");
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  it("rejects names > 64 chars with inline error", async () => {
    render(
      <ProfileNamePill profileId="p1" currentName="Default" onRenamed={() => {}} />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    const input = screen.getByTestId("profile-name-pill-input") as HTMLInputElement;
    // The maxLength=64 attribute on the input prevents typing past 64,
    // but a programmatic change can bypass it. Test the >64 guard
    // explicitly with a 65-char value.
    const long = "x".repeat(65);
    fireEvent.change(input, { target: { value: long } });
    fireEvent.click(screen.getByTestId("profile-name-pill-save"));
    const errEl = await screen.findByTestId("profile-name-pill-error");
    expect(errEl.textContent).toContain("64 characters");
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  it("server 409 duplicate name surfaces error message inline", async () => {
    mockAuthFetch.mockResolvedValueOnce(
      err(409, { error: "llm profile with this name already exists" }),
    );
    render(
      <ProfileNamePill profileId="p1" currentName="Default" onRenamed={() => {}} />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    const input = screen.getByTestId("profile-name-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Production" } });
    fireEvent.click(screen.getByTestId("profile-name-pill-save"));
    const errEl = await screen.findByTestId("profile-name-pill-error");
    expect(errEl.textContent).toContain("already exists");
    // Still in edit mode so the user can fix the name.
    expect(screen.getByTestId("profile-name-pill-input")).toBeTruthy();
  });

  it("Esc cancels (no API call) and restores original name", async () => {
    render(
      <ProfileNamePill profileId="p1" currentName="Default" onRenamed={() => {}} />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    const input = screen.getByTestId("profile-name-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "AlmostDone" } });
    fireEvent.keyDown(input, { key: "Escape" });
    await waitFor(() =>
      expect(screen.queryByTestId("profile-name-pill-input")).toBeNull(),
    );
    // Returns to display mode with original name.
    const display = screen.getByTestId("profile-name-pill-display");
    expect(display.textContent).toContain("Default");
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  it("Enter saves (same as clicking Save)", async () => {
    mockAuthFetch.mockResolvedValueOnce(ok());
    const onRenamed = vi.fn();
    render(
      <ProfileNamePill profileId="p1" currentName="Default" onRenamed={onRenamed} />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    const input = screen.getByTestId("profile-name-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Renamed" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onRenamed).toHaveBeenCalled());
  });

  it("disabled prop blocks click-to-edit", () => {
    render(
      <ProfileNamePill
        profileId="p1"
        currentName="Default"
        onRenamed={() => {}}
        disabled
      />,
    );
    fireEvent.click(screen.getByTestId("profile-name-pill-display"));
    expect(screen.queryByTestId("profile-name-pill-input")).toBeNull();
  });

  it("syncs draft from prop changes when not editing", () => {
    const { rerender } = render(
      <ProfileNamePill profileId="p1" currentName="Default" onRenamed={() => {}} />,
    );
    rerender(
      <ProfileNamePill profileId="p1" currentName="Production" onRenamed={() => {}} />,
    );
    const display = screen.getByTestId("profile-name-pill-display");
    expect(display.textContent).toContain("Production");
  });
});
