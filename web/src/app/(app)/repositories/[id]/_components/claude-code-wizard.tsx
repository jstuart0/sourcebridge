"use client";

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * ClaudeCodeWizard — inline token-mint flow for the "Use with Claude Code" card.
 *
 * States: idle → naming → minting → revealed → connected (compact)
 *                       ↘ error
 *
 * The wizard replaces the 3-step cloud block when:
 *   serverCaps.authRequired && serverCaps.mcpEnabled
 *
 * "Use existing token instead" collapses back to the slice-3 manual 3-step block
 * (caller must handle this via the onUseExisting callback).
 */

import { useCallback, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { mintApiToken, type CreatedToken, type MintTokenError } from "@/lib/api-tokens";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type WizardState =
  | { phase: "idle" }
  | { phase: "naming" }
  | { phase: "minting" }
  | { phase: "revealed"; token: CreatedToken }
  | { phase: "error"; error: MintTokenError }
  | { phase: "connected"; tokenName: string };

// escapeSingleQuotes makes a value safe to interpolate inside a single-quoted
// shell string. The token format is constrained today (no quotes), but a
// crafted server response could relax that constraint — defense in depth (L2).
function escapeSingleQuotes(s: string): string {
  return s.replace(/'/g, `'\\''`);
}

// ---------------------------------------------------------------------------
// localStorage helpers — UX hint only, not a security guarantee
// ---------------------------------------------------------------------------

function connectedKey(repoId: string) {
  return `sb:claude-code-connected:${encodeURIComponent(repoId)}`;
}

function readConnectedHint(repoId: string): { tokenName: string } | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(connectedKey(repoId));
    if (!raw) return null;
    return JSON.parse(raw) as { tokenName: string };
  } catch {
    return null;
  }
}

function writeConnectedHint(repoId: string, tokenName: string): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(connectedKey(repoId), JSON.stringify({ tokenName }));
  } catch {
    /* localStorage blocked */
  }
}

function clearConnectedHint(repoId: string): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.removeItem(connectedKey(repoId));
  } catch {
    /* ignore */
  }
}

// ---------------------------------------------------------------------------
// Small shared primitives
// ---------------------------------------------------------------------------

function CopyButton({ text, label }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);

  function copy() {
    void navigator.clipboard.writeText(text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }

  return (
    <button
      type="button"
      onClick={copy}
      className="shrink-0 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 py-1.5 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)]"
      aria-label={label ?? "Copy to clipboard"}
    >
      {copied ? "Copied!" : "Copy"}
    </button>
  );
}

function CodeBlock({ code, label }: { code: string; label?: string }) {
  return (
    <div className="flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto rounded-[var(--control-radius)] bg-[var(--bg-subtle)] px-3 py-2 text-xs font-mono text-[var(--text-primary)] leading-relaxed whitespace-pre-wrap break-all">
        {code}
      </pre>
      <div className="pt-1.5">
        <CopyButton text={code} label={label} />
      </div>
    </div>
  );
}

function InputField({
  value,
  onChange,
  disabled,
  id,
}: {
  value: string;
  onChange: (v: string) => void;
  disabled: boolean;
  id: string;
}) {
  return (
    <input
      id={id}
      type="text"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      autoComplete="off"
      spellCheck={false}
      className="h-10 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] disabled:opacity-60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)]"
    />
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

interface ClaudeCodeWizardProps {
  repoId: string;
  /** Called when the user wants to fall back to the manual 3-step block. */
  onUseExisting: () => void;
}

export function ClaudeCodeWizard({ repoId, onUseExisting }: ClaudeCodeWizardProps) {
  // Determine initial state from localStorage hint
  function initialState(): WizardState {
    const hint = readConnectedHint(repoId);
    if (hint) return { phase: "connected", tokenName: hint.tokenName };
    return { phase: "idle" };
  }

  const [wizardState, setWizardState] = useState<WizardState>(initialState);
  const [tokenName, setTokenName] = useState("Claude Code");
  const abortRef = useRef<AbortController | null>(null);

  // Derive server origin once for the setup command
  const serverOrigin =
    typeof window !== "undefined"
      ? `${window.location.protocol}//${window.location.host}`
      : "<your-server-url>";

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  function startNaming() {
    setWizardState({ phase: "naming" });
  }

  function cancelNaming() {
    setWizardState({ phase: "idle" });
  }

  const mint = useCallback(async () => {
    const name = tokenName.trim();
    if (!name) return;

    abortRef.current = new AbortController();
    setWizardState({ phase: "minting" });

    const result = await mintApiToken(name, abortRef.current.signal);

    if (result.ok) {
      writeConnectedHint(repoId, result.token.name);
      setWizardState({ phase: "revealed", token: result.token });
    } else {
      setWizardState({ phase: "error", error: result.error });
    }
  }, [tokenName, repoId]);

  function dismiss() {
    const hint = readConnectedHint(repoId);
    setWizardState({ phase: "connected", tokenName: hint?.tokenName ?? tokenName.trim() });
  }

  function reconnect() {
    setTokenName("Claude Code");
    setWizardState({ phase: "naming" });
  }

  function resetToIdle() {
    clearConnectedHint(repoId);
    setWizardState({ phase: "idle" });
  }

  // ---------------------------------------------------------------------------
  // Render helpers
  // ---------------------------------------------------------------------------

  function renderIdle() {
    return (
      <div className="mt-3 space-y-2">
        <Button
          variant="primary"
          size="sm"
          onClick={startNaming}
        >
          Connect Claude Code
        </Button>
        <p className="text-xs text-[var(--text-tertiary)]">
          We&apos;ll create an API token and build the setup command. Takes about 30 seconds.
        </p>
        <p className="text-xs text-[var(--text-tertiary)]">
          <button
            type="button"
            onClick={onUseExisting}
            className="underline underline-offset-2 hover:text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)] rounded"
          >
            Use existing token instead
          </button>
        </p>
      </div>
    );
  }

  function renderNaming() {
    return (
      <div className="mt-3 space-y-3">
        <div className="space-y-1.5">
          <label
            htmlFor="wizard-token-name"
            className="text-xs font-medium text-[var(--text-secondary)]"
          >
            Token name
          </label>
          <InputField
            id="wizard-token-name"
            value={tokenName}
            onChange={setTokenName}
            disabled={false}
          />
          <p className="text-xs text-[var(--text-tertiary)]">
            This name appears in your token list at{" "}
            <a
              href="/settings/tokens"
              className="underline underline-offset-2 hover:text-[var(--text-primary)]"
            >
              /settings/tokens
            </a>
            . You can revoke it from there anytime.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="primary"
            size="sm"
            onClick={() => void mint()}
            disabled={!tokenName.trim()}
          >
            Generate token
          </Button>
          <Button variant="secondary" size="sm" onClick={cancelNaming}>
            Cancel
          </Button>
        </div>
      </div>
    );
  }

  function renderMinting() {
    return (
      <div className="mt-3 space-y-3">
        <div className="space-y-1.5">
          <label
            htmlFor="wizard-token-name-minting"
            className="text-xs font-medium text-[var(--text-secondary)]"
          >
            Token name
          </label>
          <InputField
            id="wizard-token-name-minting"
            value={tokenName}
            onChange={setTokenName}
            disabled={true}
          />
        </div>
        <div className="flex items-center gap-2">
          <Button variant="primary" size="sm" disabled>
            <span
              aria-hidden="true"
              className="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-[var(--accent-contrast)] border-t-transparent"
            />
            Generating...
          </Button>
        </div>
      </div>
    );
  }

  function renderRevealed(token: CreatedToken) {
    // Step 1: write the token to ~/.sourcebridge/token (0600). The token is in
    // shell history for ONE command, but never in dotfiles or sync repos.
    const writeTokenCmd =
      `mkdir -p ~/.sourcebridge && ` +
      `( umask 077 && printf '%s' '${escapeSingleQuotes(token.token)}' > ~/.sourcebridge/token ) && ` +
      `chmod 600 ~/.sourcebridge/token`;

    // Step 2: setup command, no token on the command line. The CLI reads it
    // from the file written in step 1 via readAPIToken().
    const setupCmd = `sourcebridge setup claude --server '${escapeSingleQuotes(serverOrigin)}' --repo-id '${escapeSingleQuotes(repoId)}'`;

    // Step 3: rc-line that reads the file at shell start. No literal token in dotfiles.
    const rcLine = `echo 'export SOURCEBRIDGE_API_TOKEN=$(cat ~/.sourcebridge/token)' >> ~/.zshrc`;

    return (
      <div className="mt-3 space-y-4">
        {/* Token reveal — shown once for password-manager use */}
        <div className="rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-subtle)] p-3 space-y-2">
          <p className="text-xs font-medium text-[var(--text-primary)]">
            Your API token — copy it now if you want it in a password manager. The setup steps below also save it to disk for you.
          </p>
          <CodeBlock code={token.token} label="Copy token value" />
        </div>

        {/* Step 1: save token to disk */}
        <div className="space-y-1.5">
          <p className="text-xs font-semibold text-[var(--text-primary)]">
            Step 1 — Save the token securely
          </p>
          <CodeBlock code={writeTokenCmd} label="Copy token-save command" />
          <p className="text-xs text-[var(--text-tertiary)]">
            This writes your token to{" "}
            <code className="rounded bg-[var(--bg-base)] px-1 py-0.5 text-xs">~/.sourcebridge/token</code>{" "}
            with read-only-by-you permissions. The CLI and the MCP integration read it from that file.
          </p>
        </div>

        {/* Step 2: run setup */}
        <div className="space-y-1.5">
          <p className="text-xs font-semibold text-[var(--text-primary)]">
            Step 2 — Run the setup command
          </p>
          <CodeBlock code={setupCmd} label="Copy setup command" />
          <p className="text-xs text-[var(--text-tertiary)]">
            The setup command picks up the token from the file above. No need to paste the token again.
          </p>
        </div>

        {/* Step 3: persist to shell profile */}
        <div className="space-y-1.5">
          <p className="text-xs font-semibold text-[var(--text-primary)]">
            Step 3 — Make Claude Code use it every shell
          </p>
          <CodeBlock code={rcLine} label="Copy shell rc line" />
          <p className="text-xs text-[var(--text-tertiary)]">
            Use <code className="rounded bg-[var(--bg-base)] px-1 py-0.5 text-xs">~/.bashrc</code> on Bash,{" "}
            <code className="rounded bg-[var(--bg-base)] px-1 py-0.5 text-xs">~/.config/fish/config.fish</code> on Fish.
            The shell reads the token from your file at startup — it is never written into the rc file itself.
            Restart Claude Code (or open a new shell) after adding it.
          </p>
        </div>

        <Button
          variant="secondary"
          size="sm"
          onClick={dismiss}
        >
          I&apos;ve copied everything — close
        </Button>
      </div>
    );
  }

  function renderError(error: MintTokenError) {
    let heading: string;
    let body: React.ReactNode;
    let actions: React.ReactNode;

    if (error.kind === "forbidden") {
      heading = "Permission denied";
      body = (
        <p className="text-sm text-[var(--text-secondary)]">
          You don&apos;t have permission to create API tokens on this server. Ask an admin, or{" "}
          <a
            href="/settings/tokens"
            className="underline underline-offset-2 hover:text-[var(--text-primary)]"
          >
            go to /settings/tokens
          </a>{" "}
          if you think your session may have expired.
        </p>
      );
      actions = (
        <Button variant="secondary" size="sm" onClick={cancelNaming}>
          Back
        </Button>
      );
    } else if (error.kind === "duplicate") {
      heading = "Name already exists";
      body = (
        <p className="text-sm text-[var(--text-secondary)]">
          A token named &ldquo;{error.name}&rdquo; already exists. Edit the name above and try again.
        </p>
      );
      actions = (
        <div className="flex gap-2">
          <Button variant="primary" size="sm" onClick={() => setWizardState({ phase: "naming" })}>
            Change name
          </Button>
          <Button variant="secondary" size="sm" onClick={cancelNaming}>
            Cancel
          </Button>
        </div>
      );
    } else if (error.kind === "network") {
      heading = "Couldn't reach the server";
      body = (
        <p className="text-sm text-[var(--text-secondary)]">
          A network error occurred. Check your connection and try again.
        </p>
      );
      actions = (
        <div className="flex gap-2">
          <Button variant="primary" size="sm" onClick={() => void mint()}>
            Retry
          </Button>
          <button
            type="button"
            onClick={onUseExisting}
            className="text-xs text-[var(--text-tertiary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
          >
            Use external token flow
          </button>
        </div>
      );
    } else {
      heading = "Something went wrong";
      const rawMsg = (error as { kind: "unknown"; message: string; status: number }).message ?? "";
      const truncated = rawMsg.length > 200 ? rawMsg.slice(0, 200) + "…" : rawMsg;
      const httpStatus = (error as { kind: "unknown"; status?: number }).status;
      // Log full message to the console for debugging without surfacing it.
      if (typeof window !== "undefined") {
        console.warn("[claude-code-wizard] unknown error:", rawMsg);
      }
      body = (
        <p className="text-sm text-[var(--text-secondary)]">
          An unexpected error occurred{httpStatus ? ` (HTTP ${httpStatus})` : ""}.
          {truncated && (
            <>
              {" "}
              <span className="text-[var(--text-tertiary)]">Details: {truncated}</span>
            </>
          )}
        </p>
      );
      actions = (
        <div className="flex gap-2">
          <Button variant="primary" size="sm" onClick={() => void mint()}>
            Retry
          </Button>
          <Button variant="secondary" size="sm" onClick={cancelNaming}>
            Cancel
          </Button>
        </div>
      );
    }

    return (
      <div className="mt-3 space-y-3">
        <div className="rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] bg-[rgba(239,68,68,0.08)] px-3 py-2.5 space-y-1.5">
          <p className="text-xs font-semibold text-[var(--color-error,#ef4444)]">{heading}</p>
          {body}
        </div>
        {actions}
      </div>
    );
  }

  function renderConnected(tokenName: string) {
    return (
      <div className="mt-3 space-y-2">
        <div className="flex items-center gap-2">
          <span className="inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-[var(--color-success,#22c55e)]">
            <svg
              viewBox="0 0 12 12"
              fill="none"
              aria-hidden="true"
              className="h-2.5 w-2.5"
            >
              <path
                d="M2 6l3 3 5-5"
                stroke="white"
                strokeWidth="1.5"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </span>
          <p className="text-sm text-[var(--text-secondary)]">
            Connected as <span className="font-medium text-[var(--text-primary)]">{tokenName}</span>
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--text-tertiary)]">
          <button
            type="button"
            onClick={reconnect}
            className="underline underline-offset-2 hover:text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)] rounded"
          >
            Reconnect
          </button>
          <button
            type="button"
            onClick={() => {
              resetToIdle();
              onUseExisting();
            }}
            className="underline underline-offset-2 hover:text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-focus)] rounded"
          >
            Use a different token
          </button>
          <a
            href="/settings/tokens"
            className="underline underline-offset-2 hover:text-[var(--text-primary)]"
          >
            Manage tokens
          </a>
        </div>
      </div>
    );
  }

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  const state = wizardState;

  return (
    <>
      {state.phase === "idle" && renderIdle()}
      {state.phase === "naming" && renderNaming()}
      {state.phase === "minting" && renderMinting()}
      {state.phase === "revealed" && renderRevealed(state.token)}
      {state.phase === "error" && renderError(state.error)}
      {state.phase === "connected" && renderConnected(state.tokenName)}
    </>
  );
}
