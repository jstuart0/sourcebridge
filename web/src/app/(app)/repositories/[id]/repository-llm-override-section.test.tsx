/**
 * Tests for RepositoryLLMOverrideSection — slice 3 of plan
 * 2026-04-29-workspace-llm-source-of-truth-r2.md.
 *
 * Coverage:
 *  - Collapsed by default (the <details> doesn't expand on render).
 *  - "Inheriting workspace LLM settings." hint when no override exists.
 *  - Saved-state hint summarizes provider / api_key / model when set.
 *  - Save mutation is called with the right input shape.
 *  - Clear mutation is called and onSaved fires with null.
 *  - ENCRYPTION_KEY_REQUIRED extension code surfaces a clear error.
 *
 * The component uses urql's `useMutation`; we mock it via the same
 * pattern the existing wiki-settings-panel tests use.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";

const mockSetMutation = vi.fn();
const mockClearMutation = vi.fn();

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

import { RepositoryLLMOverrideSection } from "./repository-llm-override-section";

beforeEach(() => {
  mockSetMutation.mockReset();
  mockClearMutation.mockReset();
});

afterEach(() => {
  cleanup();
});

describe("RepositoryLLMOverrideSection — collapsed default", () => {
  it("renders the summary in a collapsed <details> when no override is saved", () => {
    render(
      <RepositoryLLMOverrideSection repoId="repo-A" override={null} />,
    );
    const summary = screen.getByTestId("repo-llm-override-summary");
    expect(summary).toBeInTheDocument();
    // <details> open is false by default in JSDOM.
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

  it("renders a saved-state hint summarizing the override when set", () => {
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

describe("RepositoryLLMOverrideSection — save flow", () => {
  it("invokes setRepositoryLLMOverride with the patch input on Save", async () => {
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
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={null}
        onSaved={onSaved}
      />,
    );

    // Open the <details>.
    const details = screen.getByTestId("repo-llm-override-summary").closest("details");
    if (details) details.open = true;

    // Pick a provider that doesn't need an API key field (ollama).
    const providerSelect = screen.getByRole("combobox") as HTMLSelectElement;
    fireEvent.change(providerSelect, { target: { value: "ollama" } });

    const textboxes = screen.getAllByRole("textbox");
    // textboxes[0] = baseURL, textboxes[1] = summaryModel (api key not shown for ollama)
    fireEvent.change(textboxes[0], { target: { value: "http://192.168.10.222:11434" } });
    fireEvent.change(textboxes[1], { target: { value: "qwen2.5:32b" } });

    const saveBtn = screen.getByRole("button", { name: /Save override/i });
    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(mockSetMutation).toHaveBeenCalled();
    });
    const args = mockSetMutation.mock.calls[0][0];
    expect(args.repositoryId).toBe("repo-A");
    expect(args.input.provider).toBe("ollama");
    expect(args.input.baseURL).toBe("http://192.168.10.222:11434");
    expect(args.input.summaryModel).toBe("qwen2.5:32b");
    expect(args.input.advancedMode).toBe(false);
    // No apiKey provided; should not be in the input.
    expect(args.input.apiKey).toBeUndefined();
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

    render(
      <RepositoryLLMOverrideSection repoId="repo-A" override={null} />,
    );

    const details = screen.getByTestId("repo-llm-override-summary").closest("details");
    if (details) details.open = true;

    const providerSelect = screen.getByRole("combobox") as HTMLSelectElement;
    fireEvent.change(providerSelect, { target: { value: "anthropic" } });

    // Anthropic shows the API key field. Find it by input type=password.
    const apiKeyInput = document.querySelector('input[type="password"]') as HTMLInputElement;
    fireEvent.change(apiKeyInput, { target: { value: "sk-ant-test" } });

    const saveBtn = screen.getByRole("button", { name: /Save override/i });
    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(
        screen.getByText(/SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY/i),
      ).toBeInTheDocument();
    });
  });
});

describe("RepositoryLLMOverrideSection — clear flow", () => {
  it("invokes clearRepositoryLLMOverride and calls onSaved with null", async () => {
    mockClearMutation.mockResolvedValue({
      data: { clearRepositoryLLMOverride: {} },
    });

    const onSaved = vi.fn();
    render(
      <RepositoryLLMOverrideSection
        repoId="repo-A"
        override={{
          provider: "openai",
          apiKeySet: true,
          apiKeyHint: "sk-ABCD...WXYZ",
          advancedMode: false,
        }}
        onSaved={onSaved}
      />,
    );

    const details = screen.getByTestId("repo-llm-override-summary").closest("details");
    if (details) details.open = true;

    const clearBtn = screen.getByRole("button", { name: /Clear override/i });
    fireEvent.click(clearBtn);

    await waitFor(() => {
      expect(mockClearMutation).toHaveBeenCalledWith({ repositoryId: "repo-A" });
    });
    await waitFor(() => {
      expect(onSaved).toHaveBeenCalledWith(null);
    });
  });

  it("does not show the 'Clear' button when there is no saved override", () => {
    render(
      <RepositoryLLMOverrideSection repoId="repo-A" override={null} />,
    );
    const details = screen.getByTestId("repo-llm-override-summary").closest("details");
    if (details) details.open = true;

    expect(screen.queryByRole("button", { name: /Clear override/i })).toBeNull();
  });
});

describe("RepositoryLLMOverrideSection — advanced mode", () => {
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
    const details = screen.getByTestId("repo-llm-override-summary").closest("details");
    if (details) details.open = true;

    // Verify per-area form fields are present. The descriptive paragraph
    // also mentions "architecture diagrams" so we use getAllByText for
    // labels that appear in both the form section and the description.
    expect(screen.getByText(/Code Review/i)).toBeInTheDocument();
    expect(screen.getAllByText(/Discussion/i).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText(/Knowledge Generation/i).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText(/Architecture Diagrams/i).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText(/Draft Model/i)).toBeInTheDocument();
  });
});
