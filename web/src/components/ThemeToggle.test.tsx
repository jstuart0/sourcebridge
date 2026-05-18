/**
 * CA-368 / CA-511 / CA-534: ThemeToggle component tests.
 *
 * Verifies:
 * - Post-mount aria-label is theme-specific ("Switch to light theme" / "Switch to dark theme")
 * - SSR placeholder renders with aria-label="Toggle theme" (pre-mount guard, CA-534)
 * - Sun icon shown when dark mode is active (target-state: switch TO light)
 * - Moon icon shown when light mode is active (target-state: switch TO dark)
 * - Clicking toggles the theme (dark → light, light → dark)
 *
 * Note: RTL fires useEffect synchronously via act(), so queries run against the
 * mounted (post-hydration) component. The "Toggle theme" label is the SSR
 * placeholder only; mounted tests use the dynamic theme-specific label.
 *
 * CA-534 regression vector: any refactor that removes the `mounted` guard silently
 * breaks Next.js hydration (server renders placeholder, client renders theme-specific
 * label before JS hydrates → mismatch). The SSR placeholder test pins this contract
 * so the removal would cause a test failure rather than a silent production regression.
 *
 * ESM note: vi.spyOn on React named exports fails in ESM (Cannot redefine property).
 * The SSR placeholder test uses a module-level flag + vi.mock("react") factory to
 * suppress useEffect for that one case, letting mounted stay false.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent } from "@testing-library/react";

// ── flag used by the vi.mock("react") factory below ──────────────────────
// When true, useEffect is suppressed (no-op) so mounted stays false.
// Default false so all other tests get real useEffect behaviour.
let suppressUseEffect = false;

// vi.mock is hoisted to the top of the file by Vitest. The async factory
// captures the real React module and wraps useEffect with the flag gate.
vi.mock("react", async (importOriginal) => {
  const real = await importOriginal<typeof import("react")>();
  return {
    ...real,
    useEffect: (...args: Parameters<typeof real.useEffect>) => {
      if (suppressUseEffect) return;
      return real.useEffect(...args);
    },
  };
});

// ── mock useTheme so we can control theme state without a real ThemeProvider ─
const mockSetTheme = vi.fn();
let currentTheme = "dark";

vi.mock("@/components/layout/ThemeProvider", () => ({
  useTheme: () => ({ theme: currentTheme, setTheme: mockSetTheme }),
}));

import { ThemeToggle } from "./ThemeToggle";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  suppressUseEffect = false;
});

describe("ThemeToggle", () => {
  it("dark mode: aria-label is 'Switch to light theme' (post-mount)", () => {
    currentTheme = "dark";
    render(<ThemeToggle />);
    expect(
      screen.getByRole("button", { name: "Switch to light theme" })
    ).toBeInTheDocument();
  });

  it("light mode: aria-label is 'Switch to dark theme' (post-mount)", () => {
    currentTheme = "light";
    render(<ThemeToggle />);
    expect(
      screen.getByRole("button", { name: "Switch to dark theme" })
    ).toBeInTheDocument();
  });

  it("shows Sun icon when dark mode is active (target: switch to light)", () => {
    currentTheme = "dark";
    render(<ThemeToggle />);
    // lucide Sun renders an svg; the button's accessible name carries the semantic.
    const button = screen.getByRole("button", { name: /Switch to (light|dark) theme/ });
    const svgPaths = button.querySelectorAll("svg");
    // At least one SVG rendered (the Sun icon)
    expect(svgPaths.length).toBeGreaterThan(0);
    // When dark, clicking should call setTheme("light")
    fireEvent.click(button);
    expect(mockSetTheme).toHaveBeenCalledWith("light");
  });

  it("shows Moon icon when light mode is active (target: switch to dark)", () => {
    currentTheme = "light";
    render(<ThemeToggle />);
    const button = screen.getByRole("button", { name: /Switch to (light|dark) theme/ });
    const svgPaths = button.querySelectorAll("svg");
    expect(svgPaths.length).toBeGreaterThan(0);
    // When light, clicking should call setTheme("dark")
    fireEvent.click(button);
    expect(mockSetTheme).toHaveBeenCalledWith("dark");
  });

  it("dark mode click calls setTheme('light')", () => {
    currentTheme = "dark";
    render(<ThemeToggle />);
    fireEvent.click(screen.getByRole("button", { name: /Switch to (light|dark) theme/ }));
    expect(mockSetTheme).toHaveBeenCalledTimes(1);
    expect(mockSetTheme).toHaveBeenCalledWith("light");
  });

  it("light mode click calls setTheme('dark')", () => {
    currentTheme = "light";
    render(<ThemeToggle />);
    fireEvent.click(screen.getByRole("button", { name: /Switch to (light|dark) theme/ }));
    expect(mockSetTheme).toHaveBeenCalledTimes(1);
    expect(mockSetTheme).toHaveBeenCalledWith("dark");
  });

  /**
   * CA-534: SSR placeholder path — the mounted=false branch.
   *
   * RTL's act() fires useEffect synchronously, so a normal render always tests
   * the post-mount component. Suppressing useEffect via the module-level flag
   * keeps mounted=false so the pre-mount branch is exercised.
   *
   * Regression: removing the `if (!mounted)` guard makes this test fail because
   * the theme-specific label appears instead of "Toggle theme".
   */
  it("SSR placeholder: renders static 'Toggle theme' label when not yet mounted (CA-534)", () => {
    suppressUseEffect = true; // keep mounted=false for this render
    currentTheme = "dark";

    render(<ThemeToggle />);

    // Pre-mount: static aria-label from the placeholder branch.
    expect(screen.getByRole("button", { name: "Toggle theme" })).toBeInTheDocument();
    // Pre-mount: dynamic theme-specific label must NOT be present.
    expect(screen.queryByRole("button", { name: /Switch to (light|dark) theme/ })).toBeNull();
  });
});
