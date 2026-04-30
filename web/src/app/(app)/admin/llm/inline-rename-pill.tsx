"use client";

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { useEffect, useRef, useState } from "react";

import { authFetch } from "@/lib/auth-fetch";

// ─────────────────────────────────────────────────────────────────────────
// Inline-rename pill — slice 4 polish (UX intake §2.2).
//
// In N=1 mode the page header shows a static "Profile: Default" pill.
// Without inline rename, the only way to change the name is to scroll
// to the editor below and edit the (small, easy-to-miss) Profile-name
// field — which users coming straight from the legacy /admin/llm grid
// don't expect to be there. This component makes the pill itself the
// rename affordance:
//
//   click  → swaps to <input> seeded with the current name, focused +
//            text-selected; pencil icon hint
//   Enter  → commit (PUT /admin/llm-profiles/<id> { name }); on success
//            calls onRenamed and collapses back to the pill; on error
//            shows the message inline next to the pill, keeps focus
//   Esc    → cancel without writing
//   blur   → commit (matches platform expectation; saving on blur is
//            standard for this style of inline editor)
//
// Constraints that mirror the editor:
//   - empty trimmed name → 422 (rejected at server; we don't preempt
//     server validation but we *do* refuse to send empty so the user
//     gets immediate feedback rather than waiting for the network)
//   - name unchanged → no-op, just collapse back
//   - 64-char limit aligns with backend ErrProfileNameTooLong
//
// Server contract notes:
//   - PUT /admin/llm-profiles/<id> with { name } sends a partial-update
//     patch; other fields are preserved by the pointer-omitted semantic.
//   - 409 on duplicate name → surfaced inline; user can retype or Esc.
// ─────────────────────────────────────────────────────────────────────────

const MAX_NAME_LEN = 64;

export interface InlineRenamePillProps {
  profileId: string;
  currentName: string;
  /** Show "Profile: " prefix (N=1 header) vs no prefix (N>=2 contexts). */
  prefix?: string;
  /** Called after a successful rename so the parent can refetch. */
  onRenamed?: (newName: string) => void;
  /** Optional className passed onto the outermost element. */
  className?: string;
  /** Test-ID prefix; defaults to "inline-rename-pill". */
  testIdPrefix?: string;
}

async function readApiError(res: Response): Promise<string> {
  const text = await res.text();
  try {
    const json = JSON.parse(text);
    if (json.error) return json.error;
  } catch {
    /* not JSON */
  }
  if (text.trimStart().startsWith("<")) {
    return `Server error (HTTP ${res.status}). The API may be restarting — try again in a moment.`;
  }
  return text || `HTTP ${res.status}`;
}

export function InlineRenamePill({
  profileId,
  currentName,
  prefix = "Profile: ",
  onRenamed,
  className,
  testIdPrefix = "inline-rename-pill",
}: InlineRenamePillProps) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(currentName);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const inputRef = useRef<HTMLInputElement | null>(null);

  // Keep the displayed value in sync if the parent's currentName prop
  // changes (e.g. after a refetch). We only resync when NOT actively
  // editing so we don't clobber the user's in-progress text.
  useEffect(() => {
    if (!editing) {
      setValue(currentName);
    }
  }, [currentName, editing]);

  // Focus + select-all on entering edit mode.
  useEffect(() => {
    if (editing && inputRef.current) {
      inputRef.current.focus();
      inputRef.current.select();
    }
  }, [editing]);

  const beginEdit = () => {
    setError(null);
    setValue(currentName);
    setEditing(true);
  };

  const cancel = () => {
    setEditing(false);
    setError(null);
    setValue(currentName);
  };

  const commit = async () => {
    const trimmed = value.trim();
    if (trimmed === "") {
      setError("Name cannot be empty.");
      return;
    }
    if (trimmed === currentName) {
      // No-op; collapse back without an HTTP roundtrip.
      setEditing(false);
      setError(null);
      return;
    }
    if (trimmed.length > MAX_NAME_LEN) {
      setError(`Name cannot exceed ${MAX_NAME_LEN} characters.`);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const idPath = encodeURIComponent(profileId);
      const res = await authFetch(`/api/v1/admin/llm-profiles/${idPath}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: trimmed }),
      });
      if (!res.ok) {
        throw new Error(await readApiError(res));
      }
      setEditing(false);
      onRenamed?.(trimmed);
    } catch (e) {
      setError((e as Error).message);
      // Keep the editor open + focused so the user can fix and retry.
      if (inputRef.current) {
        inputRef.current.focus();
      }
    } finally {
      setBusy(false);
    }
  };

  if (editing) {
    return (
      <span
        className={
          "inline-flex flex-wrap items-center gap-1.5 " + (className ?? "")
        }
        data-testid={`${testIdPrefix}-editing`}
      >
        <span
          className="inline-flex items-center gap-1.5 rounded-full border border-[var(--border-default)] bg-[var(--bg-raised)] px-2.5 py-1 text-xs font-medium text-[var(--text-secondary)]"
        >
          <span aria-hidden>{prefix}</span>
          <input
            ref={inputRef}
            type="text"
            value={value}
            disabled={busy}
            onChange={(e) => setValue(e.target.value)}
            onBlur={() => {
              // Blur commits — mirroring platform conventions (Notion,
              // Linear, etc.). If the user pressed Esc, `cancel` already
              // toggled editing=false so the blur handler is a no-op.
              if (editing && !busy) {
                void commit();
              }
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void commit();
              } else if (e.key === "Escape") {
                e.preventDefault();
                cancel();
              }
            }}
            maxLength={MAX_NAME_LEN}
            aria-label="Profile name"
            data-testid={`${testIdPrefix}-input`}
            className="w-44 rounded-[4px] border border-[var(--border-default)] bg-[var(--bg-base)] px-1.5 py-0 text-xs text-[var(--text-primary)] outline-none focus:border-[var(--color-accent,#3b82f6)]"
          />
        </span>
        {error ? (
          <span
            role="alert"
            data-testid={`${testIdPrefix}-error`}
            className="text-xs text-[var(--color-error,#ef4444)]"
          >
            {error}
          </span>
        ) : null}
      </span>
    );
  }

  return (
    <button
      type="button"
      onClick={beginEdit}
      title="Click to rename"
      data-testid={`${testIdPrefix}-button`}
      className={
        "group inline-flex items-center gap-1.5 rounded-full border border-[var(--border-default)] bg-[var(--bg-raised)] px-2.5 py-1 text-xs font-medium text-[var(--text-secondary)] hover:border-[var(--color-accent,#3b82f6)] hover:text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent,#3b82f6)] " +
        (className ?? "")
      }
    >
      <span aria-hidden>{prefix}</span>
      <span data-testid={`${testIdPrefix}-name`}>{currentName}</span>
      {/* Pencil icon — small, only revealed on hover/focus to keep the
          default state visually identical to the legacy static pill. */}
      <svg
        className="h-3 w-3 opacity-0 transition-opacity group-hover:opacity-70 group-focus:opacity-70"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden
      >
        <path d="M11.013 1.427a1.75 1.75 0 0 1 2.474 0l1.086 1.086a1.75 1.75 0 0 1 0 2.474l-8.61 8.61a2 2 0 0 1-.97.524l-3.013.7a.5.5 0 0 1-.6-.6l.7-3.013a2 2 0 0 1 .524-.97l8.41-8.81Z" />
        <path d="m9.5 2.5 4 4" />
      </svg>
    </button>
  );
}
