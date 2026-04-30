"use client";

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { authFetch } from "@/lib/auth-fetch";

// ─────────────────────────────────────────────────────────────────────
// Profile name pill — slice 4 inline-rename UX polish.
//
// Renders the N=1 header pill ("Profile: <name>") as a click target
// that swaps into an inline rename input on click. Save calls
// PUT /api/v1/admin/llm-profiles/<id> with { name } and reloads
// via the parent's onRenamed callback.
//
// The N=1 layout never exposes the name field on the editor itself
// (intentional — the page looks identical to the pre-profiles
// /admin/llm grid), so this pill is the only rename affordance for
// the single-profile case. Without this affordance, users in N=1
// would have to create a second profile first to discover the
// rename button on the row, which buries an obvious need behind
// a non-obvious workflow.
//
// Editing rules (matches the editor's name field in N>=2 mode):
//   - empty / whitespace-only → 422 ErrProfileNameRequired
//   - >64 chars → 422 ErrProfileNameTooLong
//   - duplicate (case-insensitive) → 409 ErrDuplicateProfileName
//
// Esc cancels (no-op). Enter saves (same as clicking Save).
// ─────────────────────────────────────────────────────────────────────

export interface ProfileNamePillProps {
  profileId: string;
  /** Current name as displayed on the pill. */
  currentName: string;
  /** Callback after a successful rename so the parent reloads. */
  onRenamed: () => void;
  /** Optional disabled state (e.g. while the repair banner is up). */
  disabled?: boolean;
  /** Test-id prefix; defaults to "profile-name-pill". */
  testIdPrefix?: string;
}

async function handleApiError(res: Response): Promise<string> {
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

export function ProfileNamePill({
  profileId,
  currentName,
  onRenamed,
  disabled,
  testIdPrefix = "profile-name-pill",
}: ProfileNamePillProps) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(currentName);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  // Keep the draft in sync if the prop changes while not editing
  // (e.g. another tab renamed the profile).
  useEffect(() => {
    if (!editing) setDraft(currentName);
  }, [currentName, editing]);

  // When entering edit mode, focus + select the input so the user
  // can immediately type-replace.
  useEffect(() => {
    if (editing && inputRef.current) {
      inputRef.current.focus();
      inputRef.current.select();
    }
  }, [editing]);

  const cancel = () => {
    setEditing(false);
    setDraft(currentName);
    setError(null);
  };

  const save = async () => {
    const trimmed = draft.trim();
    if (trimmed === "") {
      setError("Profile name cannot be empty.");
      return;
    }
    if (trimmed.length > 64) {
      setError("Profile name must be 64 characters or fewer.");
      return;
    }
    if (trimmed === currentName) {
      // No-op rename — just exit edit mode without an API hit.
      setEditing(false);
      setError(null);
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const idPath = encodeURIComponent(profileId);
      const res = await authFetch(`/api/v1/admin/llm-profiles/${idPath}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: trimmed }),
      });
      if (!res.ok) throw new Error(await handleApiError(res));
      setEditing(false);
      onRenamed();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  if (!editing) {
    return (
      <button
        type="button"
        onClick={() => {
          if (!disabled) setEditing(true);
        }}
        disabled={disabled}
        title={disabled ? undefined : "Click to rename this profile"}
        data-testid={`${testIdPrefix}-display`}
        className="inline-flex cursor-pointer items-center gap-1.5 rounded-full border border-[var(--border-default)] bg-[var(--bg-raised)] px-2.5 py-1 text-xs font-medium text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] disabled:cursor-not-allowed disabled:opacity-60"
      >
        <span aria-hidden="true">Profile:</span>
        <span className="text-[var(--text-primary)]">{currentName}</span>
        <span aria-hidden="true" className="text-[var(--text-tertiary)]">
          ✎
        </span>
        <span className="sr-only">Rename profile</span>
      </button>
    );
  }

  return (
    <div
      className="inline-flex items-center gap-2"
      data-testid={`${testIdPrefix}-editor`}
    >
      <label className="inline-flex items-center gap-1.5 text-xs text-[var(--text-secondary)]">
        <span>Profile:</span>
        <input
          ref={inputRef}
          type="text"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              save();
            } else if (e.key === "Escape") {
              e.preventDefault();
              cancel();
            }
          }}
          disabled={submitting}
          maxLength={64}
          aria-label="Profile name"
          data-testid={`${testIdPrefix}-input`}
          className="h-7 rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-2 text-xs text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--color-primary,#3b82f6)]"
        />
      </label>
      <Button
        variant="primary"
        size="sm"
        onClick={save}
        disabled={submitting}
        data-testid={`${testIdPrefix}-save`}
      >
        {submitting ? "Saving…" : "Save"}
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={cancel}
        disabled={submitting}
        data-testid={`${testIdPrefix}-cancel`}
      >
        Cancel
      </Button>
      {error ? (
        <span
          role="alert"
          data-testid={`${testIdPrefix}-error`}
          className="text-xs text-[var(--color-error,#ef4444)]"
        >
          {error}
        </span>
      ) : null}
    </div>
  );
}
