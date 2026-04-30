// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Tests for InlineRenamePill — slice 4 polish (UX intake §2.2).
 *
 * Coverage:
 *   - Default state renders as a clickable pill showing "<prefix><name>".
 *   - Click swaps to inline input seeded with currentName, focused +
 *     selected.
 *   - Enter commits via PUT /admin/llm-profiles/<id> with { name }.
 *   - Esc cancels without writing.
 *   - Blur commits.
 *   - Empty trimmed name shows inline error WITHOUT firing PUT.
 *   - Unchanged name collapses without firing PUT.
 *   - Server 409 (duplicate) surfaces the error message inline.
 *   - >64 char name shows inline error WITHOUT firing PUT.
 *   - prop currentName change while NOT editing updates display.
 *   - prop currentName change while editing does NOT clobber in-progress.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

const mockAuthFetch = vi.fn();
vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (path: string, init?: RequestInit) => mockAuthFetch(path, init),
}));

import { InlineRenamePill } from "./inline-rename-pill";

beforeEach(() => {
  mockAuthFetch.mockReset();
});

afterEach(() => {
  cleanup();
});

function ok(payload: Record<string, unknown> = {}): Response {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function err(status: number, message: string): Response {
  return new Response(JSON.stringify({ error: message }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("InlineRenamePill — default state", () => {
  it("renders a button with prefix + name", () => {
    render(
      <InlineRenamePill
        profileId="ca_llm_profile:default-migrated"
        currentName="Default"
      />,
    );
    const btn = screen.getByTestId("inline-rename-pill-button");
    expect(btn).toBeInTheDocument();
    expect(btn).toHaveTextContent("Profile: Default");
  });

  it("uses a custom prefix when provided", () => {
    render(
      <InlineRenamePill
        profileId="x"
        currentName="Foo"
        prefix=""
      />,
    );
    const btn = screen.getByTestId("inline-rename-pill-button");
    expect(btn.textContent).toContain("Foo");
    expect(btn.textContent).not.toContain("Profile:");
  });

  it("uses the supplied testIdPrefix", () => {
    render(
      <InlineRenamePill
        profileId="x"
        currentName="Foo"
        testIdPrefix="header-profile-pill"
      />,
    );
    expect(screen.getByTestId("header-profile-pill-button")).toBeInTheDocument();
  });
});

describe("InlineRenamePill — entering edit mode", () => {
  it("clicking the pill swaps to an input seeded with currentName", () => {
    render(
      <InlineRenamePill
        profileId="x"
        currentName="Default"
      />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    expect(input).toBeInTheDocument();
    expect(input.value).toBe("Default");
  });

  it("focuses the input and selects all text on entering edit mode", () => {
    render(
      <InlineRenamePill profileId="x" currentName="Default" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    expect(document.activeElement).toBe(input);
    // selectionStart=0, selectionEnd=name length means full select.
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe("Default".length);
  });
});

describe("InlineRenamePill — committing", () => {
  it("Enter commits via PUT /admin/llm-profiles/<id> { name }", async () => {
    mockAuthFetch.mockResolvedValueOnce(ok({}));
    const onRenamed = vi.fn();
    render(
      <InlineRenamePill
        profileId="ca_llm_profile:abc"
        currentName="Default"
        onRenamed={onRenamed}
      />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Default Updated" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalledWith(
        "/api/v1/admin/llm-profiles/ca_llm_profile%3Aabc",
        expect.objectContaining({
          method: "PUT",
          headers: { "Content-Type": "application/json" },
        }),
      );
    });
    const init = mockAuthFetch.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(init.body as string)).toEqual({ name: "Default Updated" });
    expect(onRenamed).toHaveBeenCalledWith("Default Updated");
    // After successful commit, editor should collapse.
    await waitFor(() => {
      expect(screen.queryByTestId("inline-rename-pill-input")).toBeNull();
    });
  });

  it("blur commits", async () => {
    mockAuthFetch.mockResolvedValueOnce(ok({}));
    const onRenamed = vi.fn();
    render(
      <InlineRenamePill
        profileId="x"
        currentName="Default"
        onRenamed={onRenamed}
      />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "After blur" } });
    fireEvent.blur(input);

    await waitFor(() => {
      expect(onRenamed).toHaveBeenCalledWith("After blur");
    });
  });

  it("trims whitespace before committing", async () => {
    mockAuthFetch.mockResolvedValueOnce(ok({}));
    render(
      <InlineRenamePill
        profileId="x"
        currentName="Old"
      />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "  New  " } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(mockAuthFetch).toHaveBeenCalled();
    });
    const init = mockAuthFetch.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(init.body as string)).toEqual({ name: "New" });
  });
});

describe("InlineRenamePill — cancel + no-op paths", () => {
  it("Esc cancels and reverts the displayed value", () => {
    render(
      <InlineRenamePill profileId="x" currentName="Default" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Edited but cancelled" } });
    fireEvent.keyDown(input, { key: "Escape" });

    expect(screen.queryByTestId("inline-rename-pill-input")).toBeNull();
    const btn = screen.getByTestId("inline-rename-pill-button");
    expect(btn).toHaveTextContent("Profile: Default");
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  it("unchanged name collapses without firing PUT", async () => {
    render(
      <InlineRenamePill profileId="x" currentName="Default" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    // Don't change; just commit.
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(screen.queryByTestId("inline-rename-pill-input")).toBeNull();
    });
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  it("trim-equals-current also collapses without firing PUT", async () => {
    render(
      <InlineRenamePill profileId="x" currentName="Default" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "  Default  " } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(screen.queryByTestId("inline-rename-pill-input")).toBeNull();
    });
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });
});

describe("InlineRenamePill — validation errors", () => {
  it("empty trimmed name shows inline error WITHOUT firing PUT", () => {
    render(
      <InlineRenamePill profileId="x" currentName="Old" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "    " } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(screen.getByTestId("inline-rename-pill-error")).toHaveTextContent(
      /cannot be empty/i,
    );
    expect(mockAuthFetch).not.toHaveBeenCalled();
    // Editor stays open so the user can fix.
    expect(screen.getByTestId("inline-rename-pill-input")).toBeInTheDocument();
  });

  it(">64 char name shows inline error WITHOUT firing PUT", () => {
    render(
      <InlineRenamePill profileId="x" currentName="Old" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    // The component's maxLength={64} also caps native input length;
    // we bypass it via fireEvent.change which sets the value directly.
    const tooLong = "A".repeat(65);
    fireEvent.change(input, { target: { value: tooLong } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(screen.getByTestId("inline-rename-pill-error")).toHaveTextContent(
      /cannot exceed/i,
    );
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });
});

describe("InlineRenamePill — server errors", () => {
  it("409 duplicate name surfaces the error message inline", async () => {
    mockAuthFetch.mockResolvedValueOnce(
      err(409, "llm profile with this name already exists"),
    );
    render(
      <InlineRenamePill profileId="x" currentName="Default" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Existing" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(screen.getByTestId("inline-rename-pill-error")).toHaveTextContent(
        /already exists/i,
      );
    });
    // Editor stays open so user can fix and retry.
    expect(screen.getByTestId("inline-rename-pill-input")).toBeInTheDocument();
  });

  it("500 server error surfaces the message inline", async () => {
    mockAuthFetch.mockResolvedValueOnce(err(500, "boom"));
    render(
      <InlineRenamePill profileId="x" currentName="Default" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "X" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(screen.getByTestId("inline-rename-pill-error")).toHaveTextContent(
        /boom/i,
      );
    });
  });
});

describe("InlineRenamePill — prop sync", () => {
  it("currentName change updates display when NOT editing", () => {
    const { rerender } = render(
      <InlineRenamePill profileId="x" currentName="Old" />,
    );
    expect(screen.getByTestId("inline-rename-pill-button")).toHaveTextContent(
      "Profile: Old",
    );
    rerender(<InlineRenamePill profileId="x" currentName="New" />);
    expect(screen.getByTestId("inline-rename-pill-button")).toHaveTextContent(
      "Profile: New",
    );
  });

  it("currentName change does NOT clobber in-progress edit", () => {
    const { rerender } = render(
      <InlineRenamePill profileId="x" currentName="Old" />,
    );
    fireEvent.click(screen.getByTestId("inline-rename-pill-button"));
    const input = screen.getByTestId("inline-rename-pill-input") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "User typed this" } });
    // Parent rerender (e.g. after refetch) lands new currentName.
    rerender(<InlineRenamePill profileId="x" currentName="ServerSays" />);
    // Input still holds the user's in-progress value.
    expect(input.value).toBe("User typed this");
  });
});
