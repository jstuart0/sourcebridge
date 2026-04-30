/**
 * Tests for RepositoryLLMOverrideSection — slice 3 of the LLM provider
 * profiles plan reshapes the section to a three-mode radio (workspace
 * inherit / saved profile / inline override). These tests cover:
 *
 *  - Collapsed-by-default rendering (regression).
 *  - Saved-state hint per mode.
 *  - Three-mode radio: each mode selectable, mode-switching is
 *    non-destructive.
 *  - Saved-profile mode: dropdown populated from /admin/llm-profiles,
 *    Active pill marker, read-only preview shows non-secret fields
 *    only (NEVER api_key_hint).
 *  - Save dispatches the right mutation per mode (clearProfile on
 *    inline; { profileId } on profile; clearRepositoryLLMOverride on
 *    workspace).
 *  - PROFILE_NO_LONGER_EXISTS resolution panel renders when the
 *    profileMissing prop is true.
 *  - PROFILE_NO_LONGER_EXISTS error code surfaces a clear actionable
 *    message on save.
 *  - ENCRYPTION_KEY_REQUIRED on inline-mode save still works (slice 2
 *    regression).
 *  - Clear-back-to-workspace via the "Inherit workspace settings"
 *    radio + Save.
 *  - Active-profile-missing hint on the workspace list.
 *
 * The component uses urql's useMutation and authFetch for the
 * REST list call. Both are mocked.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

const mockSetMutation = vi.fn();
const mockClearMutation = vi.fn();
const mockAuthFetch = vi.fn();

vi.mock("urql", async (importOriginal) => {
  const actual = await importOriginal<typeof import("urql")>();
  return {
    ...actual,
    useMutation: vi.fn((doc: { definitions?: { name?: { value?: string } }[] }) => {
      const name = doc?.definitions?.[0]?.name?.value ?? "";
      if (name === "SetRepositoryLLMOverride") {
        return [{ fetching: false }, mockSetMutation];
      }
      if (name === "ClearRepositoryLLMOverride") {
        return [{ fetching: false }, mockClearMutation];
      }
      return [{ fetching: false }, vi.fn()];
    }),
  };
});

vi.mock("@/lib/auth-fetch", () => ({
  authFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import { RepositoryLLMOverrideSection } from "./repository-llm-override-section";

// Minimal ListProfilesResponse helper.
function listProfilesResponse(profiles: Array<Partial<{ id: string; name: string; provider: string; base_url: string; api_key_set: boolean; api_key_hint: string; summary_model: string; advanced_mode: boolean; is_active: boolean }>>, opts?: { active_profile_missing?: boolean }) {
  return {
    ok: true,
    json: async () => ({
      profiles: profiles.map((p) => ({
        id: p.id ?? "",
        name: p.name ?? "",
        provider: p.provider ?? "",
        base_url: p.base_url ?? "",
        api_key_set: p.api_key_set ?? false,
        api_key_hint: p.api_key_hint,
        summary_model: p.summary_model ?? "",
        review_model: "",
        ask_model: "",
        knowledge_model: "",
        architecture_diagram_model: "",
        draft_model: "",
        timeout_secs: 0,
        advanced_mode: p.advanced_mode ?? false,
        is_active: p.is_active ?? false,
      })),
      active_profile_missing: opts?.active_profile_missing ?? false,
    }),
  };
}

beforeEach(() => {
  mockSetMutation.mockReset();
  mockClearMutation.mockReset();
  mockAuthFetch.mockReset();
  // Default: no profiles in the workspace; the picker shows "Pick a profile".
  mockAuthFetch.mockResolvedValue(listProfilesResponse([]));
});

afterEach(() => {
  cleanup();
});

// Helper: open the <details>.
function openSection() {
  const summary = screen.getByTestId("repo-llm-override-summary");
  const details = summary.closest("details");
  if (details) details.open = true;
  return details;
}

// ───────────────────────────────────────────────────────────────────
// Collapsed default + saved-state hint
// ───────────────────────────────────────────────────────────────────

describe("RepositoryLLMOverrideSection — collapsed default", () => {
  it("renders the summary in a collapsed <details> when no override is saved", () => {
    render(
      <RepositoryLLMOverrideSection repoId="repo-A" override={null} />,
    );
    const summary = screen.getByTestId("repo-llm-override-summary");
    expect(summary).toBeInTheDocument();
    const details = summary.closest("details");
    expect(details).not.toBeNull();
    expect(details?.open).toBe(false);
  });

  it("shows the 'Inheriting workspace LLM settings.' hint when override is null", () => {
    render(
      <RepositoryLLMOverrideSection repoId="repo-A" override={null} />,
    );
    expect(
      screen.getByText(/Inheriting workspace LLM settings/i),
    ).toBeInTheDocument();
  });

  it("shows a profile-mode hint when the saved override carries profileId + profileName", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          profileId: "ca_llm_profile:default-migrated",
          profileName: "Default",
          apiKeySet: false,
          advancedMode: false,
        }}
      />,
    );
    expect(screen.getByText(/Using saved profile.*Default/i)).toBeInTheDocument();
  });

  it("shows the deleted-profile hint when profileMissing=true", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          profileId: "ca_llm_profile:gone",
          apiKeySet: false,
          advancedMode: false,
        }}
        profileMissing
      />,
    );
    expect(
      screen.getByText(/referenced profile no longer exists/i),
    ).toBeInTheDocument();
  });

  it("renders an inline-mode hint summarizing the override when set inline", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          provider: "ollama",
          baseURL: "http://localhost:11434",
          apiKeySet: false,
          advancedMode: false,
          summaryModel: "qwen2.5:32b",
        }}
      />,
    );
    expect(
      screen.getByText(/Override active.*provider=ollama/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/model=qwen2.5:32b/i)).toBeInTheDocument();
  });
});

// ───────────────────────────────────────────────────────────────────
// Three-mode radio
// ───────────────────────────────────────────────────────────────────

describe("RepositoryLLMOverrideSection — three-mode radio", () => {
  it("starts in workspace mode when override is null", async () => {
    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} />);
    openSection();
    const workspaceRadio = screen.getByTestId("repo-llm-override-mode-workspace") as HTMLInputElement;
    expect(workspaceRadio.checked).toBe(true);
    // Inline panel must NOT be present.
    expect(screen.queryByTestId("repo-llm-override-inline-panel")).toBeNull();
    // Profile panel must NOT be present.
    expect(screen.queryByTestId("repo-llm-override-profile-panel")).toBeNull();
  });

  it("starts in profile mode when override.profileId is set", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          profileId: "ca_llm_profile:default-migrated",
          profileName: "Default",
          apiKeySet: false,
          advancedMode: false,
        }}
      />,
    );
    openSection();
    const profileRadio = screen.getByTestId("repo-llm-override-mode-profile") as HTMLInputElement;
    expect(profileRadio.checked).toBe(true);
    expect(screen.getByTestId("repo-llm-override-profile-panel")).toBeInTheDocument();
  });

  it("starts in inline mode when override has inline fields and no profileId", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          provider: "ollama",
          apiKeySet: false,
          advancedMode: false,
        }}
      />,
    );
    openSection();
    const inlineRadio = screen.getByTestId("repo-llm-override-mode-inline") as HTMLInputElement;
    expect(inlineRadio.checked).toBe(true);
    expect(screen.getByTestId("repo-llm-override-inline-panel")).toBeInTheDocument();
  });

  it("toggles between modes when the radios are clicked", () => {
    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} />);
    openSection();

    fireEvent.click(screen.getByTestId("repo-llm-override-mode-profile"));
    expect((screen.getByTestId("repo-llm-override-mode-profile") as HTMLInputElement).checked).toBe(true);
    expect(screen.getByTestId("repo-llm-override-profile-panel")).toBeInTheDocument();
    expect(screen.queryByTestId("repo-llm-override-inline-panel")).toBeNull();

    fireEvent.click(screen.getByTestId("repo-llm-override-mode-inline"));
    expect((screen.getByTestId("repo-llm-override-mode-inline") as HTMLInputElement).checked).toBe(true);
    expect(screen.getByTestId("repo-llm-override-inline-panel")).toBeInTheDocument();
    expect(screen.queryByTestId("repo-llm-override-profile-panel")).toBeNull();

    fireEvent.click(screen.getByTestId("repo-llm-override-mode-workspace"));
    expect((screen.getByTestId("repo-llm-override-mode-workspace") as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByTestId("repo-llm-override-inline-panel")).toBeNull();
    expect(screen.queryByTestId("repo-llm-override-profile-panel")).toBeNull();
  });

  it("preserves inline values when toggling away to profile mode and back", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          provider: "ollama",
          apiKeySet: false,
          advancedMode: false,
          summaryModel: "qwen2.5:32b",
          baseURL: "http://localhost:11434",
        }}
      />,
    );
    openSection();
    // Start in inline mode; the inline values render.
    const inlinePanel = screen.getByTestId("repo-llm-override-inline-panel");
    expect(inlinePanel).toBeInTheDocument();
    const provider = inlinePanel.querySelector("select") as HTMLSelectElement;
    expect(provider.value).toBe("ollama");

    // Toggle to profile mode.
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-profile"));
    expect(screen.queryByTestId("repo-llm-override-inline-panel")).toBeNull();

    // Back to inline — values should be preserved (mode toggle is non-destructive).
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-inline"));
    const inlinePanel2 = screen.getByTestId("repo-llm-override-inline-panel");
    const provider2 = inlinePanel2.querySelector("select") as HTMLSelectElement;
    expect(provider2.value).toBe("ollama");
  });
});

// ───────────────────────────────────────────────────────────────────
// Saved-profile mode
// ───────────────────────────────────────────────────────────────────

describe("RepositoryLLMOverrideSection — saved-profile mode", () => {
  it("loads the profile list and renders the dropdown with an Active marker", async () => {
    mockAuthFetch.mockResolvedValue(
      listProfilesResponse([
        { id: "ca_llm_profile:default-migrated", name: "Default", provider: "anthropic", api_key_set: true, is_active: true },
        { id: "ca_llm_profile:local", name: "Local Dev", provider: "ollama", api_key_set: false, is_active: false },
      ]),
    );
    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} />);
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-profile"));

    await waitFor(() => {
      expect(screen.getByTestId("repo-llm-override-profile-picker")).toBeInTheDocument();
    });

    const picker = screen.getByTestId("repo-llm-override-profile-picker") as HTMLSelectElement;
    const options = Array.from(picker.querySelectorAll("option")).map((o) => o.textContent ?? "");
    // First option is the placeholder.
    expect(options[0]).toMatch(/Pick a profile/i);
    // Active profile is marked with the (Active) suffix.
    expect(options.some((o) => /Default.*Active/i.test(o))).toBe(true);
    expect(options.some((o) => /Local Dev/.test(o))).toBe(true);
    // The non-active profile must NOT carry the Active suffix.
    const localOpt = options.find((o) => o.includes("Local Dev"));
    expect(localOpt).not.toMatch(/Active/i);
  });

  it("shows the read-only preview after picking a profile, hiding api_key_hint", async () => {
    mockAuthFetch.mockResolvedValue(
      listProfilesResponse([
        {
          id: "ca_llm_profile:default-migrated",
          name: "Default",
          provider: "anthropic",
          base_url: "https://api.anthropic.com",
          api_key_set: true,
          api_key_hint: "sk-ant-...XYZ", // <-- MUST NEVER appear in the preview
          summary_model: "claude-sonnet-4",
          is_active: true,
        },
      ]),
    );
    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} />);
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-profile"));

    await waitFor(() => {
      expect(screen.getByTestId("repo-llm-override-profile-picker")).toBeInTheDocument();
    });
    fireEvent.change(screen.getByTestId("repo-llm-override-profile-picker"), {
      target: { value: "ca_llm_profile:default-migrated" },
    });

    const preview = screen.getByTestId("repo-llm-override-profile-preview");
    expect(preview).toBeInTheDocument();
    expect(preview.textContent).toMatch(/anthropic/);
    expect(preview.textContent).toMatch(/api.anthropic.com/);
    expect(preview.textContent).toMatch(/configured/i);
    expect(preview.textContent).toMatch(/claude-sonnet-4/);
    // Critical security: api_key_hint must NOT appear.
    expect(preview.textContent).not.toMatch(/sk-ant-/);
    expect(preview.textContent).not.toMatch(/XYZ/);
  });

  it("dispatches setRepositoryLLMOverride with { profileId } when saving in profile mode", async () => {
    mockAuthFetch.mockResolvedValue(
      listProfilesResponse([
        { id: "ca_llm_profile:default-migrated", name: "Default", is_active: true },
      ]),
    );
    mockSetMutation.mockResolvedValue({
      data: {
        setRepositoryLLMOverride: {
          profileId: "ca_llm_profile:default-migrated",
          profileName: "Default",
          apiKeySet: false,
          advancedMode: false,
        },
      },
    });

    const onSaved = vi.fn();
    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} onSaved={onSaved} />);
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-profile"));

    await waitFor(() => {
      expect(screen.getByTestId("repo-llm-override-profile-picker")).toBeInTheDocument();
    });
    fireEvent.change(screen.getByTestId("repo-llm-override-profile-picker"), {
      target: { value: "ca_llm_profile:default-migrated" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Save profile selection/i }));

    await waitFor(() => {
      expect(mockSetMutation).toHaveBeenCalled();
    });
    const args = mockSetMutation.mock.calls[0][0];
    expect(args.repositoryId).toBe("repo-A");
    expect(args.input).toEqual({ profileId: "ca_llm_profile:default-migrated" });
    // Critically: input must NOT carry inline fields. Mode discrimination
    // is preserved end-to-end; the server clears inline fields atomically
    // on this input.
    expect(args.input.provider).toBeUndefined();
    expect(args.input.apiKey).toBeUndefined();
    expect(args.input.summaryModel).toBeUndefined();
    expect(onSaved).toHaveBeenCalled();
  });

  it("blocks save when no profile is picked yet", async () => {
    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} />);
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-profile"));

    fireEvent.click(screen.getByRole("button", { name: /Save profile selection/i }));

    await waitFor(() => {
      expect(screen.getByText(/Pick a profile from the dropdown/i)).toBeInTheDocument();
    });
    expect(mockSetMutation).not.toHaveBeenCalled();
  });

  it("surfaces the active-profile-missing workspace hint when the API reports it", async () => {
    mockAuthFetch.mockResolvedValue(
      listProfilesResponse(
        [{ id: "ca_llm_profile:a", name: "A", is_active: false }],
        { active_profile_missing: true },
      ),
    );
    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} />);
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-profile"));

    await waitFor(() => {
      expect(screen.getByText(/Workspace has no active profile/i)).toBeInTheDocument();
    });
  });

  it("surfaces PROFILE_NO_LONGER_EXISTS as a clear inline error on save", async () => {
    mockAuthFetch.mockResolvedValue(
      listProfilesResponse([{ id: "ca_llm_profile:foo", name: "Foo" }]),
    );
    mockSetMutation.mockResolvedValue({
      error: {
        message: "The selected profile no longer exists.",
        graphQLErrors: [
          {
            message: "The selected profile no longer exists. Pick another profile, switch to inline override, or revert to workspace inheritance.",
            extensions: { code: "PROFILE_NO_LONGER_EXISTS" },
          },
        ],
      },
    });

    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} />);
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-profile"));

    await waitFor(() => {
      expect(screen.getByTestId("repo-llm-override-profile-picker")).toBeInTheDocument();
    });
    fireEvent.change(screen.getByTestId("repo-llm-override-profile-picker"), {
      target: { value: "ca_llm_profile:foo" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Save profile selection/i }));

    await waitFor(() => {
      expect(
        screen.getByText(/profile no longer exists/i),
      ).toBeInTheDocument();
    });
  });
});

// ───────────────────────────────────────────────────────────────────
// Profile-missing resolution panel (parent-driven)
// ───────────────────────────────────────────────────────────────────

describe("RepositoryLLMOverrideSection — profile-missing resolution panel", () => {
  it("renders the resolution panel when profileMissing is true", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          profileId: "ca_llm_profile:gone",
          apiKeySet: false,
          advancedMode: false,
        }}
        profileMissing
      />,
    );
    openSection();
    const panel = screen.getByTestId("repo-llm-override-profile-missing");
    expect(panel).toBeInTheDocument();
    expect(panel.textContent).toMatch(/no longer exists/i);
    expect(panel.textContent).toMatch(/ca_llm_profile:gone/);
    // The panel should suggest concrete actions.
    expect(panel.textContent).toMatch(/Pick another profile/i);
    expect(panel.textContent).toMatch(/inline override/i);
    expect(panel.textContent).toMatch(/workspace/i);
  });

  it("does NOT render the resolution panel when profileMissing is false", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          profileId: "ca_llm_profile:default-migrated",
          profileName: "Default",
          apiKeySet: false,
          advancedMode: false,
        }}
      />,
    );
    openSection();
    expect(screen.queryByTestId("repo-llm-override-profile-missing")).toBeNull();
  });
});

// ───────────────────────────────────────────────────────────────────
// Inline mode (slice 2 regression — still works)
// ───────────────────────────────────────────────────────────────────

describe("RepositoryLLMOverrideSection — inline-mode regression", () => {
  it("dispatches setRepositoryLLMOverride with clearProfile + inline fields", async () => {
    mockSetMutation.mockResolvedValue({
      data: {
        setRepositoryLLMOverride: {
          provider: "ollama",
          apiKeySet: false,
          advancedMode: false,
        },
      },
    });

    const onSaved = vi.fn();
    render(
      <RepositoryLLMOverrideSection repoId="repo-A" override={null} onSaved={onSaved} />,
    );
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-inline"));

    const inlinePanel = screen.getByTestId("repo-llm-override-inline-panel");
    const providerSelect = inlinePanel.querySelector("select") as HTMLSelectElement;
    fireEvent.change(providerSelect, { target: { value: "ollama" } });

    const textboxes = inlinePanel.querySelectorAll('input[type="text"]') as NodeListOf<HTMLInputElement>;
    // [0] = baseURL, [1] = summaryModel (no api_key field for ollama)
    fireEvent.change(textboxes[0], { target: { value: "http://192.168.10.222:11434" } });
    fireEvent.change(textboxes[1], { target: { value: "qwen2.5:32b" } });

    fireEvent.click(screen.getByRole("button", { name: /Save override/i }));

    await waitFor(() => {
      expect(mockSetMutation).toHaveBeenCalled();
    });
    const args = mockSetMutation.mock.calls[0][0];
    expect(args.repositoryId).toBe("repo-A");
    expect(args.input.clearProfile).toBe(true);
    expect(args.input.provider).toBe("ollama");
    expect(args.input.baseURL).toBe("http://192.168.10.222:11434");
    expect(args.input.summaryModel).toBe("qwen2.5:32b");
  });

  it("surfaces ENCRYPTION_KEY_REQUIRED with a clear, actionable message", async () => {
    mockSetMutation.mockResolvedValue({
      error: {
        message: "Cannot save API key: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY is not set",
        graphQLErrors: [
          {
            message: "Cannot save API key: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY is not set on the server.",
            extensions: { code: "ENCRYPTION_KEY_REQUIRED" },
          },
        ],
      },
    });

    render(<RepositoryLLMOverrideSection repoId="repo-A" override={null} />);
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-inline"));

    const inlinePanel = screen.getByTestId("repo-llm-override-inline-panel");
    const providerSelect = inlinePanel.querySelector("select") as HTMLSelectElement;
    fireEvent.change(providerSelect, { target: { value: "anthropic" } });

    const apiKeyInput = inlinePanel.querySelector('input[type="password"]') as HTMLInputElement;
    fireEvent.change(apiKeyInput, { target: { value: "sk-ant-test" } });

    fireEvent.click(screen.getByRole("button", { name: /Save override/i }));

    await waitFor(() => {
      expect(
        screen.getByText(/SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY/i),
      ).toBeInTheDocument();
    });
  });

  it("reveals per-area model fields when advanced-mode toggle is on", () => {
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          provider: "openai",
          apiKeySet: false,
          advancedMode: true,
          summaryModel: "gpt-4o",
          reviewModel: "gpt-4o-mini",
        }}
      />,
    );
    openSection();
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-inline"));

    expect(screen.getByText(/Code Review/i)).toBeInTheDocument();
    expect(screen.getAllByText(/Discussion/i).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText(/Knowledge Generation/i).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText(/Architecture Diagrams/i).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText(/Draft Model/i)).toBeInTheDocument();
  });
});

// ───────────────────────────────────────────────────────────────────
// Workspace mode (clear-back-to-workspace)
// ───────────────────────────────────────────────────────────────────

describe("RepositoryLLMOverrideSection — workspace-mode save", () => {
  it("dispatches clearRepositoryLLMOverride when saving in workspace mode", async () => {
    mockClearMutation.mockResolvedValue({ data: { clearRepositoryLLMOverride: {} } });

    const onSaved = vi.fn();
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          provider: "openai",
          apiKeySet: true,
          apiKeyHint: "sk-OAI...XYZW",
          advancedMode: false,
        }}
        onSaved={onSaved}
      />,
    );
    openSection();
    // Toggle to workspace mode.
    fireEvent.click(screen.getByTestId("repo-llm-override-mode-workspace"));
    fireEvent.click(screen.getByRole("button", { name: /Save \(clear override\)/i }));

    await waitFor(() => {
      expect(mockClearMutation).toHaveBeenCalledWith({ repositoryId: "repo-A" });
    });
    await waitFor(() => {
      expect(onSaved).toHaveBeenCalledWith(null);
    });
  });
});

// ───────────────────────────────────────────────────────────────────
// Prop-sync regression
// ───────────────────────────────────────────────────────────────────

describe("RepositoryLLMOverrideSection — prop sync after async load", () => {
  it("syncs form state when override prop transitions from null to populated", async () => {
    const onSaved = vi.fn();
    mockSetMutation.mockResolvedValue({
      data: {
        setRepositoryLLMOverride: {
          provider: "openai",
          apiKeySet: false,
          advancedMode: false,
        },
      },
    });

    const { rerender } = render(
      <RepositoryLLMOverrideSection repoId="repo-A" override={null} onSaved={onSaved} />,
    );

    rerender(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          provider: "openai",
          baseURL: "https://api.openai.com/v1",
          apiKeySet: true,
          apiKeyHint: "sk-A...XYZW",
          advancedMode: false,
          summaryModel: "gpt-4o",
        }}
        onSaved={onSaved}
      />,
    );

    openSection();
    // The prop transition derives mode = "inline".
    expect((screen.getByTestId("repo-llm-override-mode-inline") as HTMLInputElement).checked).toBe(true);
    const inlinePanel = screen.getByTestId("repo-llm-override-inline-panel");
    const providerSelect = inlinePanel.querySelector("select") as HTMLSelectElement;
    expect(providerSelect.value).toBe("openai");
    const textboxes = inlinePanel.querySelectorAll('input[type="text"]') as NodeListOf<HTMLInputElement>;
    expect(textboxes[0].value).toBe("https://api.openai.com/v1");
    expect(textboxes[1].value).toBe("gpt-4o");
  });
});
