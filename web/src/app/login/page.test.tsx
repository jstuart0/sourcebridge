/**
 * CA-535: Login page contract tests.
 *
 * Scope: narrow form-contract tests — label association, input types, submit
 * button presence. These pin the label<→>input wiring introduced in the
 * CA-499 <Input> migration so drift (e.g. a changed `htmlFor` / `id` pair)
 * causes a test failure rather than a silent screen-reader regression.
 *
 * The login page renders in two modes based on the /auth/info response:
 *   - needsSetup=false  →  "Password" label + "Sign In" button
 *   - needsSetup=true   →  "Create Password" + "Confirm Password" labels + "Create Account" button
 *
 * Strategy: mock `fetch` (native) to control the /auth/info response, mock
 * `next/navigation` for `useRouter`, and mock auth-token-store so no
 * localStorage state leaks between tests.
 */

import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { render, screen, cleanup, waitFor } from "@testing-library/react";

// ── mock next/navigation ──────────────────────────────────────────────────
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

// ── mock auth-token-store so localStorage state doesn't affect tests ──────
vi.mock("@/lib/auth-token-store", () => ({
  getStoredToken: vi.fn(() => null),
  setStoredToken: vi.fn(),
  clearStoredToken: vi.fn(),
}));

// ── mock auth-utils (isTokenExpired never called when token is null) ───────
vi.mock("@/lib/auth-utils", () => ({
  isTokenExpired: vi.fn(() => false),
}));

// ── import after mocks ────────────────────────────────────────────────────
import LoginPage from "./page";

// ── helpers ───────────────────────────────────────────────────────────────

/**
 * Stub globalThis.fetch to return an /auth/info payload.
 * `setup_done: true`  → needsSetup=false  (normal login form)
 * `setup_done: false` → needsSetup=true   (initial setup form)
 */
function mockAuthInfo(payload: { setup_done: boolean; password_min_length?: number }) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(payload),
    })
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

// ── tests ─────────────────────────────────────────────────────────────────

describe("LoginPage — normal login (setup_done: true)", () => {
  beforeEach(() => {
    mockAuthInfo({ setup_done: true });
  });

  it('Password label is associated with the password input via htmlFor/id', async () => {
    render(<LoginPage />);

    // waitFor: the form only renders after the async /auth/info fetch resolves.
    const passwordInput = await screen.findByLabelText(/^Password$/i);
    expect(passwordInput).toBeInTheDocument();
    expect(passwordInput).toHaveAttribute("type", "password");
  });

  it('Sign In submit button is present', async () => {
    render(<LoginPage />);

    const button = await screen.findByRole("button", { name: /sign in/i });
    expect(button).toBeInTheDocument();
  });

  it('Confirm Password field is NOT rendered in login mode', async () => {
    render(<LoginPage />);

    // Wait for the form to appear, then assert the confirm field is absent.
    await screen.findByLabelText(/^Password$/i);
    expect(screen.queryByLabelText(/confirm password/i)).toBeNull();
  });
});

describe("LoginPage — initial setup (setup_done: false)", () => {
  beforeEach(() => {
    mockAuthInfo({ setup_done: false, password_min_length: 12 });
  });

  it('Create Password label is associated with the password input', async () => {
    render(<LoginPage />);

    const passwordInput = await screen.findByLabelText(/create password/i);
    expect(passwordInput).toBeInTheDocument();
    expect(passwordInput).toHaveAttribute("type", "password");
  });

  it('Confirm Password label is associated with the confirm input', async () => {
    render(<LoginPage />);

    const confirmInput = await screen.findByLabelText(/confirm password/i);
    expect(confirmInput).toBeInTheDocument();
    expect(confirmInput).toHaveAttribute("type", "password");
  });

  it('Create Account submit button is present', async () => {
    render(<LoginPage />);

    const button = await screen.findByRole("button", { name: /create account/i });
    expect(button).toBeInTheDocument();
  });
});

describe("LoginPage — loading state", () => {
  it('renders a loading spinner before /auth/info resolves', async () => {
    // Use a promise that never resolves so the page stays in loading state.
    vi.stubGlobal(
      "fetch",
      vi.fn().mockReturnValue(new Promise(() => {}))
    );

    render(<LoginPage />);

    // The spinner is present immediately (before the fetch resolves).
    const spinner = screen.getByRole("status", { name: /checking server status/i });
    expect(spinner).toBeInTheDocument();

    // The form is not yet rendered.
    expect(screen.queryByRole("button", { name: /sign in/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /create account/i })).toBeNull();
  });
});
